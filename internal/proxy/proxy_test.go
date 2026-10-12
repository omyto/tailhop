package proxy

import (
	"bufio"
	"bytes"
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
	"testing/synctest"
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
	server := proxyServer(t, p)
	t.Cleanup(server.Close)
	t.Cleanup(p.transport.CloseIdleConnections)
	u, _ := url.Parse(server.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	t.Cleanup(tr.CloseIdleConnections)
	return p, server, &http.Client{Transport: tr, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// All integration tests use the production admission wrapper and server defaults.
func proxyServer(t *testing.T, p *Proxy) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(p)
	s.Config = p.Server()
	s.Listener = p.Listener(s.Listener)
	s.Start()
	return s
}
func slots(s *httptest.Server) int {
	l := s.Listener.(*limitedListener)
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	return len(l.p.clients)
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
		p := New(testLog(), 100, 10*time.Minute)
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
		p := New(testLog(), 100, 10*time.Minute)
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
			server := proxyServer(t, p)
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
	s := proxyServer(t, p)
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
	s := proxyServer(t, p)
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
	p := New(testLog(), 100, 10*time.Minute)
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
	s := proxyServer(t, p)
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

// A channel listener and net.Pipe make timer/lifecycle tests deterministic with
// synctest's fake clock. No sleeps on wall-clock time or Tailnet are required.
type pipeListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(100, 64, 0, 1), Port: 8080} }
func pipeClient(l *pipeListener) net.Conn {
	client, server := net.Pipe()
	l.incoming <- server
	return client
}
func pipeProxy(p *Proxy) (*http.Server, *limitedListener, *pipeListener, <-chan error) {
	l := newPipeListener()
	limited := p.Listener(l).(*limitedListener)
	server := p.Server()
	done := make(chan error, 1)
	go func() { done <- server.Serve(limited) }()
	return server, limited, l, done
}
func countSlots(l *limitedListener) int {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	return len(l.p.clients)
}
func pipeRequest(c net.Conn, method, target string) (*http.Response, error) {
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: example.com\r\n\r\n", method, target)
	return http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
}
func stopPipeProxy(t *testing.T, p *Proxy, s *http.Server, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Shutdown(ctx, s); err != nil {
		t.Error(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Error(err)
	}
}

func TestAdmissionKeepAliveAndIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := New(testLog(), 1, time.Minute)
		server, limited, l, done := pipeProxy(p)
		c := pipeClient(l)
		resp, err := pipeRequest(c, "GET", "/invalid")
		if err != nil || resp.StatusCode != 400 {
			t.Fatalf("%v %v", resp, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		synctest.Wait()
		if countSlots(limited) != 1 {
			t.Fatal("idle keep-alive lost capacity")
		}
		excess := pipeClient(l)
		if _, err := excess.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("excess connection not closed: %v", err)
		}
		excess.Close()
		if countSlots(limited) != 1 {
			t.Fatal("rejection changed active capacity")
		}
		// Expiration closes the socket and restores capacity.
		time.Sleep(time.Minute)
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("idle HTTP slot leaked")
		}
		c.Close()
		next := pipeClient(l)
		resp, err = pipeRequest(next, "GET", "/invalid")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		next.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("client close slot leaked")
		}
		stopPipeProxy(t, p, server, done)
	})
}

func TestPendingConnectAdmissionAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		canceled := make(chan struct{})
		p := newProxy(testLog(), func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		})
		p.maxClients = 1
		server, limited, l, done := pipeProxy(p)
		c := pipeClient(l)
		response := make(chan *http.Response, 1)
		go func() { resp, _ := pipeRequest(c, "CONNECT", "example.com:443"); response <- resp }()
		<-entered
		if countSlots(limited) != 1 {
			t.Fatal("pending CONNECT was not counted")
		}
		excess := pipeClient(l)
		if _, err := excess.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("excess: %v", err)
		}
		excess.Close()
		shutdown := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		go func() { shutdown <- p.Shutdown(ctx, server) }()
		<-canceled
		resp := <-response
		if resp == nil || resp.StatusCode != 503 {
			t.Fatalf("pending shutdown: %v", resp)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err := <-shutdown; err != nil {
			t.Fatal(err)
		}
		c.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("pending CONNECT slot leaked")
		}
		p.mu.Lock()
		n := len(p.tunnels)
		p.mu.Unlock()
		if n != 0 {
			t.Fatal("late tunnel registered")
		}
		<-done
	})
}

func TestConnectSharedIdleAndOneWayActivity(t *testing.T) {
	for _, direction := range []string{"upload", "download", "idle"} {
		t.Run(direction, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				upstream, remote := net.Pipe()
				defer remote.Close()
				p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
				p.idleTimeout = time.Minute
				server, limited, l, done := pipeProxy(p)
				client := pipeClient(l)
				response := make(chan *http.Response, 1)
				go func() { resp, _ := pipeRequest(client, "CONNECT", "example.com:443"); response <- resp }()
				resp := <-response
				if resp == nil || resp.StatusCode != 200 {
					t.Fatalf("CONNECT: %v", resp)
				}
				synctest.Wait()
				if direction != "idle" {
					src, dst := client, remote
					if direction == "download" {
						src, dst = remote, client
					}
					for i := 0; i < 8; i++ {
						time.Sleep(30 * time.Second)
						written := make(chan error, 1)
						go func() { _, err := src.Write([]byte("x")); written <- err }()
						b := make([]byte, 1)
						if _, err := io.ReadFull(dst, b); err != nil {
							t.Fatal(err)
						}
						if err := <-written; err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						if countSlots(limited) != 1 {
							t.Fatal("active one-way tunnel expired")
						}
					}
				}
				// No traffic now: both directions must be closed after one shared timeout.
				time.Sleep(time.Minute)
				synctest.Wait()
				if _, err := client.Read(make([]byte, 1)); err != io.EOF {
					t.Fatalf("client idle close: %v", err)
				}
				if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
					t.Fatalf("upstream idle close: %v", err)
				}
				if countSlots(limited) != 0 {
					t.Fatal("idle tunnel slot leaked")
				}
				client.Close()
				stopPipeProxy(t, p, server, done)
			})
		})
	}
}

func TestAdmissionReleasePaths(t *testing.T) {
	for _, mode := range []string{"header_timeout", "malformed", "connect_failed", "force_tunnel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				upstream, remote := net.Pipe()
				defer remote.Close()
				p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) {
					if mode == "connect_failed" {
						return nil, errors.New("private detail")
					}
					return upstream, nil
				})
				server, limited, l, done := pipeProxy(p)
				c := pipeClient(l)
				switch mode {
				case "header_timeout":
					time.Sleep(10 * time.Second)
					synctest.Wait()
				case "malformed":
					go fmt.Fprint(c, "INVALID REQUEST\r\n\r\n")
					io.ReadAll(c)
				case "connect_failed":
					resp, err := pipeRequest(c, "CONNECT", "example.com:443")
					if err != nil || resp.StatusCode != 502 {
						t.Fatalf("%v %v", resp, err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					c.Close()
				case "force_tunnel":
					resp, err := pipeRequest(c, "CONNECT", "example.com:443")
					if err != nil || resp.StatusCode != 200 {
						t.Fatalf("%v %v", resp, err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := p.Shutdown(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				if countSlots(limited) != 0 {
					t.Fatal("slot leaked", mode)
				}
				c.Close()
				upstream.Close()
				stopPipeProxy(t, p, server, done)
			})
		})
	}
}

func TestWrappedHalfCloseAndExactlyOnceClose(t *testing.T) {
	a, b := tcpPair(t)
	p := New(testLog(), 1, time.Minute)
	l := p.Listener(newPipeListener()).(*limitedListener)
	c := &clientConn{Conn: a, listener: l}
	l.p.clients[c] = struct{}{}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal(err)
	}
	if countSlots(l) != 1 {
		t.Fatal("half-close released full connection")
	}
	go b.Write([]byte("reply"))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "reply" {
		t.Fatalf("%q %v", got, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Close() }()
	}
	wg.Wait()
	if countSlots(l) != 0 {
		t.Fatal("close did not release")
	}
}

func TestStreamingShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream, origin := net.Pipe()
		defer origin.Close()
		p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
		server, limited, l, done := pipeProxy(p)
		client := pipeClient(l)
		go func() {
			req, err := http.ReadRequest(bufio.NewReader(origin))
			if err != nil {
				return
			}
			req.Body.Close()
			fmt.Fprint(origin, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
			io.Copy(io.Discard, origin)
		}()
		resp, err := pipeRequest(client, "GET", "http://example.com/stream")
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 1)
		if _, err = io.ReadFull(resp.Body, b); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err = p.Shutdown(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(resp.Body); err == nil {
			t.Fatal("unfinished HTTP stream not terminated")
		}
		resp.Body.Close()
		client.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("HTTP stream slot leaked")
		}
		p.mu.Lock()
		active := p.active
		p.mu.Unlock()
		if active != 0 {
			t.Fatal("HTTP handler leaked")
		}
		<-done
	})
}

func TestSourceLogging(t *testing.T) {
	var logs bytes.Buffer
	p := newProxy(slog.New(slog.NewJSONHandler(&logs, nil)), func(context.Context, string, string) (net.Conn, error) { return nil, ErrBlocked })
	r := httptest.NewRequest("GET", "http://example.com/?query-secret", nil)
	r.RemoteAddr = "100.64.0.42:1234"
	r.Header.Set("X-Forwarded-For", "spoof-secret")
	r.Header.Set("Proxy-Authorization", "credential-secret")
	p.ServeHTTP(httptest.NewRecorder(), r)
	got := logs.String()
	if !strings.Contains(got, `"source_ip":"100.64.0.42"`) || !strings.Contains(got, `"reason":"blocked"`) {
		t.Fatal(got)
	}
	for _, secret := range []string{"query-secret", "spoof-secret", "credential-secret"} {
		if strings.Contains(got, secret) {
			t.Fatal("log secret", secret)
		}
	}
}

func TestGracefulStreamingShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream, origin := net.Pipe()
		defer origin.Close()
		continueStream := make(chan struct{})
		p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
		server, limited, l, done := pipeProxy(p)
		client := pipeClient(l)
		go func() {
			req, err := http.ReadRequest(bufio.NewReader(origin))
			if err != nil {
				return
			}
			req.Body.Close()
			fmt.Fprint(origin, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
			<-continueStream
			fmt.Fprint(origin, "1\r\ny\r\n0\r\n\r\n")
		}()
		resp, err := pipeRequest(client, "GET", "http://example.com/stream")
		if err != nil {
			t.Fatal(err)
		}
		first := make([]byte, 1)
		if _, err = io.ReadFull(resp.Body, first); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdown := make(chan error, 1)
		go func() { shutdown <- p.Shutdown(ctx, server) }()
		synctest.Wait()
		if p.forceCtx.Err() != nil {
			t.Fatal("active HTTP canceled before drain deadline")
		}
		close(continueStream)
		rest, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(rest) != "y" {
			t.Fatalf("%q %v", rest, err)
		}
		if err = <-shutdown; err != nil {
			t.Fatal(err)
		}
		client.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("drained HTTP slot leaked")
		}
		<-done
	})
}

type sourceConn struct{ net.Conn }

func (c sourceConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(100, 64, 0, 42), Port: 1234}
}
func TestConnectAndRejectionSourceLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		upstream, remote := net.Pipe()
		defer remote.Close()
		p := newProxy(slog.New(slog.NewJSONHandler(&logs, nil)), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
		p.maxClients = 1
		server, _, l, done := pipeProxy(p)
		client, accepted := net.Pipe()
		l.incoming <- sourceConn{accepted}
		resp, err := pipeRequest(client, "CONNECT", "example.com:443")
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", resp, err)
		}
		extra, rejected := net.Pipe()
		l.incoming <- sourceConn{rejected}
		if _, err = extra.Read(make([]byte, 1)); err != io.EOF {
			t.Fatal(err)
		}
		extra.Close()
		client.Close()
		remote.Close()
		synctest.Wait()
		stopPipeProxy(t, p, server, done)
		got := logs.String()
		if strings.Count(got, `"source_ip":"100.64.0.42"`) < 3 || !strings.Contains(got, "tunnel established") || !strings.Contains(got, "tunnel closed") || !strings.Contains(got, `"reason":"max_clients"`) {
			t.Fatal(got)
		}
	})
}

func TestLateConnectCannotRegisterAfterShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		late, remote := net.Pipe()
		defer remote.Close()
		p := newProxy(testLog(), func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			<-ctx.Done()
			return late, nil
		})
		server, limited, l, done := pipeProxy(p)
		client := pipeClient(l)
		received := make(chan *http.Response, 1)
		go func() { resp, _ := pipeRequest(client, "CONNECT", "example.com:443"); received <- resp }()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stopped := make(chan error, 1)
		go func() { stopped <- p.Shutdown(ctx, server) }()
		resp := <-received
		if resp == nil || resp.StatusCode != 503 {
			t.Fatalf("late dial response %v", resp)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Read(make([]byte, 1)); err != io.EOF {
			t.Fatal("late upstream leaked", err)
		}
		client.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("late CONNECT slot leaked")
		}
		p.mu.Lock()
		n := len(p.tunnels)
		p.mu.Unlock()
		if n != 0 {
			t.Fatal("late tunnel registered")
		}
		<-done
	})
}

func TestConcurrentAdmission(t *testing.T) {
	p := New(testLog(), 4, time.Minute)
	s := proxyServer(t, p)
	defer s.Close()
	address := strings.TrimPrefix(s.URL, "http://")
	var held []net.Conn
	for i := 0; i < 4; i++ {
		c, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		resp, err := pipeRequest(c, "GET", "/invalid")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		held = append(held, c)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", address)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			if resp, err := pipeRequest(c, "GET", "/invalid"); err == nil {
				resp.Body.Close()
				t.Error("excess connection admitted")
			}
		}()
	}
	wg.Wait()
	if slots(s) != 4 {
		t.Fatal("concurrent rejection changed slots", slots(s))
	}
	for _, c := range held {
		c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Shutdown(ctx, s.Config); err != nil {
		t.Fatal(err)
	}
	if slots(s) != 0 {
		t.Fatal("shutdown leaked slots", slots(s))
	}
}
func TestHTTPDialCanceledAtShutdownDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		canceled := make(chan struct{})
		p := newProxy(testLog(), func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		})
		server, limited, l, done := pipeProxy(p)
		client := pipeClient(l)
		responseDone := make(chan struct{})
		go func() {
			resp, _ := pipeRequest(client, "GET", "http://example.com/")
			if resp != nil {
				resp.Body.Close()
			}
			close(responseDone)
		}()
		<-entered
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.Shutdown(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		<-canceled
		<-responseDone
		client.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("HTTP dial slot leaked")
		}
		p.mu.Lock()
		active := p.active
		p.mu.Unlock()
		if active != 0 {
			t.Fatal("HTTP dial handler leaked")
		}
		<-done
	})
}
func TestUploadShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstream, origin := net.Pipe()
		defer origin.Close()
		received := make(chan struct{})
		p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
		server, limited, l, done := pipeProxy(p)
		client := pipeClient(l)
		go func() {
			req, err := http.ReadRequest(bufio.NewReader(origin))
			if err != nil {
				return
			}
			b := make([]byte, 1)
			if _, err = io.ReadFull(req.Body, b); err != nil {
				return
			}
			close(received)
			io.Copy(io.Discard, req.Body)
			req.Body.Close()
		}()
		writeDone := make(chan struct{})
		go func() {
			fmt.Fprint(client, "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
			close(writeDone)
		}()
		<-writeDone
		<-received
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.Shutdown(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		client.Close()
		synctest.Wait()
		if countSlots(limited) != 0 {
			t.Fatal("upload slot leaked")
		}
		p.mu.Lock()
		active := p.active
		p.mu.Unlock()
		if active != 0 {
			t.Fatal("upload handler leaked")
		}
		// net/http may finish its bounded TCP RST-avoidance delay after Close.
		// Advance fake time only; the proxy already returned within its budget.
		time.Sleep(time.Second)
		synctest.Wait()
		<-done
	})
}

func TestHTTPDialCanceledOnClientDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		canceled := make(chan struct{})
		p := newProxy(testLog(), func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		})
		server, limited, l, done := pipeProxy(p)
		defer stopPipeProxy(t, p, server, done)
		client := pipeClient(l)
		go fmt.Fprint(client, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
		<-entered
		client.Close()
		synctest.Wait()
		select {
		case <-canceled:
		default:
			t.Fatal("dial outlived client cancellation")
		}
		if countSlots(limited) != 0 {
			t.Fatal("canceled HTTP dial slot leaked")
		}
	})
}

func TestHTTPStalledStreamingIdleTimeout(t *testing.T) {
	for _, mode := range []string{"request_body", "response_body", "response_with_unfinished_upload"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				upstream, origin := net.Pipe()
				defer origin.Close()
				ready := make(chan struct{})
				originDone := make(chan struct{})
				p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
				p.idleTimeout = time.Minute
				server, limited, l, done := pipeProxy(p)
				client := pipeClient(l)
				go func() {
					defer close(originDone)
					req, err := http.ReadRequest(bufio.NewReader(origin))
					if err != nil {
						return
					}
					if mode == "request_body" {
						b := make([]byte, 1)
						io.ReadFull(req.Body, b)
						close(ready)
						io.Copy(io.Discard, req.Body)
					} else {
						if mode == "response_with_unfinished_upload" {
							b := make([]byte, 1)
							io.ReadFull(req.Body, b)
						}
						fmt.Fprint(origin, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
						close(ready)
						io.Copy(io.Discard, origin)
					}
				}()
				if mode == "request_body" {
					fmt.Fprint(client, "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
				} else {
					if mode == "response_with_unfinished_upload" {
						go fmt.Fprint(client, "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n")
					} else {
						go fmt.Fprint(client, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
					}
					resp, err := http.ReadResponse(bufio.NewReader(client), nil)
					if err != nil {
						t.Fatal(err)
					}
					b := make([]byte, 1)
					if _, err = io.ReadFull(resp.Body, b); err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
				}
				<-ready
				synctest.Wait()
				time.Sleep(time.Minute)
				synctest.Wait()
				if countSlots(limited) != 0 {
					t.Fatal("idle stream retained slot")
				}
				p.mu.Lock()
				active := p.active
				p.mu.Unlock()
				if active != 0 {
					t.Fatal("idle stream handler not canceled")
				}
				<-originDone
				client.Close()
				// Let net/http finish its bounded RST-avoidance delay using fake time.
				time.Sleep(time.Second)
				synctest.Wait()
				stopPipeProxy(t, p, server, done)
			})
		})
	}
}

func TestHTTPActiveOneWayStreaming(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				upstream, origin := net.Pipe()
				defer origin.Close()
				request := make(chan *http.Request, 1)
				p := newProxy(testLog(), func(context.Context, string, string) (net.Conn, error) { return upstream, nil })
				p.idleTimeout = time.Minute
				server, limited, l, done := pipeProxy(p)
				client := pipeClient(l)
				go func() {
					req, err := http.ReadRequest(bufio.NewReader(origin))
					if err == nil {
						request <- req
					}
				}()
				if direction == "upload" {
					fmt.Fprint(client, "POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n")
					req := <-request
					for i := 0; i < 8; i++ {
						time.Sleep(30 * time.Second)
						written := make(chan error, 1)
						go func() { _, err := fmt.Fprint(client, "1\r\nx\r\n"); written <- err }()
						b := make([]byte, 1)
						if _, err := io.ReadFull(req.Body, b); err != nil {
							t.Fatal(err)
						}
						if err := <-written; err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						if countSlots(limited) != 1 {
							t.Fatal("active upload expired")
						}
					}
					go fmt.Fprint(client, "0\r\n\r\n")
					io.Copy(io.Discard, req.Body)
					req.Body.Close()
					go fmt.Fprint(origin, "HTTP/1.1 204 No Content\r\n\r\n")
					resp, err := http.ReadResponse(bufio.NewReader(client), nil)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
				} else {
					go fmt.Fprint(client, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
					req := <-request
					req.Body.Close()
					go fmt.Fprint(origin, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
					resp, err := http.ReadResponse(bufio.NewReader(client), nil)
					if err != nil {
						t.Fatal(err)
					}
					for i := 0; i < 8; i++ {
						time.Sleep(30 * time.Second)
						written := make(chan error, 1)
						go func() { _, err := fmt.Fprint(origin, "1\r\nx\r\n"); written <- err }()
						b := make([]byte, 1)
						if _, err := io.ReadFull(resp.Body, b); err != nil {
							t.Fatal(err)
						}
						if err := <-written; err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						if countSlots(limited) != 1 {
							t.Fatal("active download expired")
						}
					}
					go fmt.Fprint(origin, "0\r\n\r\n")
					if _, err := io.ReadAll(resp.Body); err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
				}
				client.Close()
				synctest.Wait()
				stopPipeProxy(t, p, server, done)
			})
		})
	}
}
