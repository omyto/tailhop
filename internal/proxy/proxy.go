package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type tunnel struct{ client, upstream net.Conn }

func (t *tunnel) close() { t.client.Close(); t.upstream.Close() }

type Proxy struct {
	transport *http.Transport
	dial      func(context.Context, string, string) (net.Conn, error)
	log       *slog.Logger
	mu        sync.Mutex
	stopping  bool
	tunnels   map[*tunnel]struct{}
	changed   chan struct{}
}

func New(log *slog.Logger) *Proxy { return newProxy(log, newPolicyDialer().DialContext) }
func newProxy(log *slog.Logger, dial func(context.Context, string, string) (net.Conn, error)) *Proxy {
	return &Proxy{
		log:     log,
		dial:    dial,
		tunnels: make(map[*tunnel]struct{}),
		changed: make(chan struct{}, 1),
		transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dial,
			DisableCompression:     true,
			ForceAttemptHTTP2:      false,
			MaxIdleConns:           64,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        60 * time.Second,
			MaxResponseHeaderBytes: 32 << 10,
			ResponseHeaderTimeout:  30 * time.Second,
			TLSHandshakeTimeout:    10 * time.Second,
			ExpectContinueTimeout:  time.Second,
		},
	}
}
func (p *Proxy) Server() *http.Server {
	return &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10}
}
func strip(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, s := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(s))
		}
	}
	for _, s := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(s)
	}
}
func failure(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, ErrTarget) {
		code = 400
	} else if errors.Is(err, ErrBlocked) {
		code = 403
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = 504
	} else {
		var n net.Error
		if errors.As(err, &n) && n.Timeout() {
			code = 504
		}
	}
	http.Error(w, http.StatusText(code), code)
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	stopping := p.stopping
	p.mu.Unlock()
	if stopping {
		http.Error(w, "Service Unavailable", 503)
		return
	}
	if r.ProtoMajor != 1 {
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil || r.URL.Opaque != "" || r.URL.Fragment != "" {
		failure(w, ErrTarget)
		return
	}
	host, port, err := authority(r.URL.Host, true)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	// Allow an origin to respond before the streaming request body reaches EOF.
	http.NewResponseController(w).EnableFullDuplex()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Host = net.JoinHostPort(host, port)
	out.URL.Host = out.Host
	out.Header = r.Header.Clone()
	strip(out.Header)
	out.Trailer = nil
	out.TransferEncoding = nil
	out.Close = false
	start := time.Now()
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		failure(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 101 {
		http.Error(w, "Bad Gateway", 502)
		return
	}
	strip(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if err := http.NewResponseController(w).Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
	_, err = io.Copy(flushWriter{w}, resp.Body)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	p.log.Info("request", "method", r.Method, "host", host, "status", resp.StatusCode, "duration", time.Since(start))
}

type flushWriter struct{ http.ResponseWriter }

func (w flushWriter) Write(b []byte) (int, error) {
	n, e := w.ResponseWriter.Write(b)
	if e == nil {
		e = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, e
}
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := authority(r.RequestURI, false)
	if err != nil {
		failure(w, err)
		return
	}
	upstream, err := p.dial(r.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		failure(w, err)
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	// Registration and hijacking are serialized with shutdown.
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		upstream.Close()
		http.Error(w, "Service Unavailable", 503)
		return
	}
	client, rw, err := h.Hijack()
	if err != nil {
		p.mu.Unlock()
		upstream.Close()
		return
	}
	t := &tunnel{client, upstream}
	p.tunnels[t] = struct{}{}
	p.mu.Unlock()
	defer func() {
		t.close()
		p.mu.Lock()
		delete(p.tunnels, t)
		p.mu.Unlock()
		select {
		case p.changed <- struct{}{}:
		default:
		}
	}()
	client.SetDeadline(time.Time{})
	client.SetWriteDeadline(time.Now().Add(dialTimeout))
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if rw.Flush() != nil {
		return
	}
	client.SetWriteDeadline(time.Time{})
	results := make(chan error, 2)
	copyHalf := func(dst net.Conn, src io.Reader) {
		_, e := io.Copy(dst, src)
		if e == nil {
			if c, ok := dst.(interface{ CloseWrite() error }); ok {
				e = c.CloseWrite()
			} else {
				t.close()
			}
		}
		if e != nil {
			t.close()
		}
		results <- e
	}
	go copyHalf(upstream, rw.Reader)
	go copyHalf(client, upstream)
	<-results
	<-results
	p.log.Info("tunnel closed", "host", host, "port", port)
}

// Shutdown drains requests and tunnels concurrently under a shared deadline.
func (p *Proxy) Shutdown(ctx context.Context, s *http.Server) error {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(ctx) }()
	for {
		p.mu.Lock()
		n := len(p.tunnels)
		p.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-p.changed:
		case <-ctx.Done():
			p.mu.Lock()
			for t := range p.tunnels {
				t.close()
			}
			p.mu.Unlock()
			s.Close()
			p.transport.CloseIdleConnections()
			<-done
			return ctx.Err()
		}
	}
	err := <-done
	if err != nil {
		s.Close()
	}
	p.transport.CloseIdleConnections()
	return err
}
