package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func originProxy(t *testing.T, h http.Handler) (*Proxy, *httptest.Server, *http.Client) {
	t.Helper()
	origin := httptest.NewServer(h)
	t.Cleanup(origin.Close)
	p := newProxy(testLog(), func(ctx context.Context, _, a string) (net.Conn, error) {
		if a != "example.com:80" {
			return nil, fmt.Errorf("unexpected authority %s", a)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(origin.URL, "http://"))
	})
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	t.Cleanup(p.transport.CloseIdleConnections)
	u, _ := url.Parse(server.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	t.Cleanup(tr.CloseIdleConnections)
	return p, server, &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func TestForward(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	p, _, client := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com:80" || r.RequestURI != "/path?q=secret" {
			t.Errorf("target %s %s", r.Host, r.RequestURI)
		}
		for _, k := range []string{"Proxy-Authorization", "Proxy-Connection", "X-Hop", "Keep-Alive", "Upgrade"} {
			if r.Header.Get(k) != "" {
				t.Errorf("leaked %s", k)
			}
		}
		if r.Header.Get("X-End") != "yes" {
			t.Error("end-to-end header lost")
		}
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Connection", "X-Response-Hop")
		w.Header().Set("X-Response-Hop", "remove")
		w.Header().Set("Proxy-Authenticate", "remove")
		w.Header().Set("X-End", "response")
		w.WriteHeader(201)
		fmt.Fprintf(w, "%s:%s", r.Method, b)
	}))
	if p.transport.Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"} {
		r, _ := http.NewRequest(method, "http://example.com/path?q=secret", strings.NewReader("payload"))
		r.Header.Set("Connection", "X-Hop, Keep-Alive")
		r.Header.Set("X-Hop", "secret")
		r.Header.Set("Proxy-Authorization", "secret")
		r.Header.Set("Proxy-Connection", "keep-alive")
		r.Header.Set("X-End", "yes")
		resp, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 201 || resp.Header.Get("X-End") != "response" || resp.Header.Get("X-Response-Hop") != "" || resp.Header.Get("Proxy-Authenticate") != "" {
			t.Fatalf("response %v", resp)
		}
		if method != "HEAD" && string(b) != method+":payload" {
			t.Fatalf("body %q", b)
		}
	}
}
func TestStreaming(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	_, _, c := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NewResponseController(w).EnableFullDuplex()
		b := make([]byte, 5)
		if _, e := io.ReadFull(r.Body, b); e != nil || string(b) != "first" {
			t.Errorf("request streaming: %q %v", b, e)
		}
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		close(first)
		<-release
		rest, _ := io.ReadAll(r.Body)
		w.Write(rest)
	}))
	reader, writer := io.Pipe()
	result := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://example.com/stream", reader)
		resp, e := c.Do(r)
		if e != nil {
			errs <- e
		} else {
			result <- resp
		}
	}()
	go func() { writer.Write([]byte("first")); <-release; writer.Write([]byte("last")); writer.Close() }()
	select {
	case <-first:
	case e := <-errs:
		t.Fatal(e)
	case <-time.After(3 * time.Second):
		t.Fatal("request buffered")
	}
	var resp *http.Response
	select {
	case resp = <-result:
	case e := <-errs:
		t.Fatal(e)
	case <-time.After(3 * time.Second):
		t.Fatal("response buffered")
	}
	b := make([]byte, 5)
	if _, e := io.ReadFull(resp.Body, b); e != nil || string(b) != "first" {
		t.Fatalf("%q %v", b, e)
	}
	unblock()
	rest, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || string(rest) != "last" {
		t.Fatalf("%q %v", rest, e)
	}
}
func TestRedirect(t *testing.T) {
	_, _, c := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://other.example/")
		w.WriteHeader(302)
	}))
	resp, e := c.Get("http://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "http://other.example/" {
		t.Fatal(resp)
	}
}
func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		target string
		code   int
	}{
		{"/origin", 400}, {"https://example.com/", 400}, {"http://user:pass@example.com/", 400}, {"http://example.com:22/", 403}, {"http://example.com:/", 400},
	} {
		p := New(testLog())
		r := httptest.NewRequest("GET", tc.target, nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s: %d", tc.target, w.Code)
		}
	}
	for _, tc := range []struct {
		err  error
		code int
	}{{ErrBlocked, 403}, {errors.New("secret"), 502}, {context.DeadlineExceeded, 504}} {
		for _, method := range []string{"GET", "CONNECT"} {
			p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return nil, tc.err })
			target := "http://example.com/"
			if method == "CONNECT" {
				target = "example.com:443"
			}
			r := httptest.NewRequest(method, target, nil)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
			}
		}
	}
	for _, a := range []string{"example.com", "example.com:abc", "::1:443", "example.com:22"} {
		p := New(testLog())
		w := httptest.NewRecorder()
		r := httptest.NewRequest("CONNECT", "example.com:443", nil)
		r.RequestURI = a
		p.ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 403 {
			t.Fatalf("%s: %d", a, w.Code)
		}
	}
}
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	c, e := net.Dial("tcp", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	s, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); s.Close() })
	return c.(*net.TCPConn), s.(*net.TCPConn)
}
func connectClient(t *testing.T, address, early string) (*net.TCPConn, *bufio.Reader) {
	t.Helper()
	c, e := net.Dial("tcp", address)
	if e != nil {
		t.Fatal(e)
	}
	tcp := c.(*net.TCPConn)
	t.Cleanup(func() { tcp.Close() })
	tcp.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(tcp, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n%s", early)
	return tcp, bufio.NewReader(tcp)
}
func handshake(t *testing.T, r *bufio.Reader) {
	t.Helper()
	resp, e := http.ReadResponse(r, &http.Request{Method: "CONNECT"})
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
}
func TestConnectBufferedHalfClose(t *testing.T) {
	for _, remoteFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(remoteFirst), func(t *testing.T) {
			upstream, remote := tcpPair(t)
			p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
			server := httptest.NewServer(p)
			defer server.Close()
			c, r := connectClient(t, strings.TrimPrefix(server.URL, "http://"), "early")
			handshake(t, r)
			b := make([]byte, 5)
			remote.SetDeadline(time.Now().Add(5 * time.Second))
			if _, e := io.ReadFull(remote, b); e != nil || string(b) != "early" {
				t.Fatalf("buffered %q %v", b, e)
			}
			if remoteFirst {
				remote.Write([]byte("reply"))
				remote.CloseWrite()
				got, e := io.ReadAll(r)
				if e != nil || string(got) != "reply" {
					t.Fatalf("remote EOF %q %v", got, e)
				}
				c.Write([]byte("later"))
				c.CloseWrite()
				got, e = io.ReadAll(remote)
				if e != nil || string(got) != "later" {
					t.Fatalf("reverse after EOF %q %v", got, e)
				}
			} else {
				c.CloseWrite()
				got, e := io.ReadAll(remote)
				if e != nil || len(got) != 0 {
					t.Fatalf("client EOF %q %v", got, e)
				}
				remote.Write([]byte("reply"))
				remote.CloseWrite()
				got, e = io.ReadAll(r)
				if e != nil || string(got) != "reply" {
					t.Fatalf("reply %q %v", got, e)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if e := p.Shutdown(ctx, server.Config); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestConnectDialBeforeHandshake(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	upstream, remote := net.Pipe()
	defer remote.Close()
	p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) {
		close(entered)
		<-release
		return upstream, nil
	})
	s := httptest.NewServer(p)
	defer s.Close()
	c, r := connectClient(t, strings.TrimPrefix(s.URL, "http://"), "")
	<-entered
	// No tunnel is registered while the dial is still pending.
	p.mu.Lock()
	n := len(p.tunnels)
	p.mu.Unlock()
	if n != 0 {
		t.Fatal("hijacked before dial")
	}
	close(release)
	handshake(t, r)
	c.Close()
	remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.Shutdown(ctx, s.Config)
}
func TestConcurrentShutdown(t *testing.T) {
	p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); io.Copy(b, b) }()
		return a, nil
	})
	s := httptest.NewServer(p)
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, r := connectClient(t, strings.TrimPrefix(s.URL, "http://"), "")
			handshake(t, r)
			_ = c
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	e := p.Shutdown(ctx, s.Config)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("shutdown %v", e)
	}
	if _, e := net.DialTimeout("tcp", strings.TrimPrefix(s.URL, "http://"), time.Second); e == nil {
		t.Fatal("listener still accepting")
	}
	// Wait for handler cleanup with notifications rather than sleeps.
	timeout := time.After(time.Second)
	for {
		p.mu.Lock()
		n := len(p.tunnels)
		p.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-p.changed:
		case <-timeout:
			t.Fatal("tunnels leaked")
		}
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("GET", "http://example.com/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

// Exercise a raw absolute-form target with a conflicting Host header.
func TestHostNormalization(t *testing.T) {
	_, s, _ := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com:80" {
			t.Errorf("Host = %q", r.Host)
		}
		w.Write([]byte("ok"))
	}))
	c, e := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(c, "GET http://example.com/ HTTP/1.1\r\nHost: ambiguous.invalid\r\nConnection: close\r\n\r\n")
	resp, e := http.ReadResponse(bufio.NewReader(c), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
}
func TestConcurrentForward(t *testing.T) {
	_, _, c := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(w, r.Body) }))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprint(i)
			resp, e := c.Post("http://example.com/", "text/plain", strings.NewReader(payload))
			if e != nil {
				t.Error(e)
				return
			}
			defer resp.Body.Close()
			b, e := io.ReadAll(resp.Body)
			if e != nil || string(b) != payload {
				t.Errorf("response %q %v", b, e)
			}
		}(i)
	}
	wg.Wait()
}
func TestBlockedAtDialBoundary(t *testing.T) {
	p := New(testLog())
	for _, method := range []string{"GET", "CONNECT"} {
		for _, host := range []string{"127.0.0.1", "100.64.0.1", "169.254.169.254", "[::ffff:127.0.0.1]", "[fc00::1]"} {
			target := "http://" + host + "/"
			if method == "CONNECT" {
				target = host + ":443"
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest(method, target, nil))
			if w.Code != 403 {
				t.Errorf("%s %s = %d", method, target, w.Code)
			}
		}
	}
}
func TestConnectFallbackEOF(t *testing.T) {
	a, b := net.Pipe()
	p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return a, nil })
	s := httptest.NewServer(p)
	defer s.Close()
	c, r := connectClient(t, strings.TrimPrefix(s.URL, "http://"), "")
	handshake(t, r)
	// A net.Pipe has no half-close; EOF must close the other copy direction.
	b.Close()
	if _, e := io.ReadAll(r); e != nil {
		t.Fatal(e)
	}
	c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := p.Shutdown(ctx, s.Config); e != nil {
		t.Fatal(e)
	}
}

func TestContentEncodingPreserved(t *testing.T) {
	_, _, c := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "" {
			t.Error("proxy added compression negotiation")
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write([]byte("opaque encoded bytes"))
	}))
	c.Transport.(*http.Transport).DisableCompression = true
	resp, e := c.Get("http://example.com/")
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil || string(b) != "opaque encoded bytes" || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("encoding %q %v %v", b, e, resp.Header)
	}
}

// Response headers alone must reach the client before an upload finishes.
func TestEarlyResponseHeaders(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	_, _, c := originProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NewResponseController(w).EnableFullDuplex()
		b := make([]byte, 1)
		if _, e := io.ReadFull(r.Body, b); e != nil {
			t.Error(e)
		}
		w.WriteHeader(202)
		w.(http.Flusher).Flush()
		io.Copy(io.Discard, r.Body)
		w.Write([]byte("done"))
	}))
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() { writer.Write([]byte("a")); <-release; writer.Write([]byte("b")); writer.Close() }()
	r, _ := http.NewRequest("POST", "http://example.com/", reader)
	// A bounded context releases a stuck upload if this behavior regresses.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, e := c.Do(r.WithContext(ctx))
		if e != nil {
			errs <- e
		} else {
			results <- resp
		}
	}()
	var resp *http.Response
	select {
	case resp = <-results:
	case e := <-errs:
		t.Fatal(e)
	case <-ctx.Done():
		t.Fatal("headers buffered")
	}
	if resp.StatusCode != 202 {
		t.Fatal(resp.Status)
	}
	unblock()
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || string(b) != "done" {
		t.Fatalf("%q %v", b, e)
	}
}
