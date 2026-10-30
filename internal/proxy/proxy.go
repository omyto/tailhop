package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type tunnel struct {
	client, upstream net.Conn
	mu               sync.Mutex
	timer            *time.Timer
	last             time.Time
	idle             time.Duration
	closed           bool
	reason           string
}

func (t *tunnel) close(reason string) {
	t.mu.Lock()
	// Claim the cause before closing either peer. Secondary copy errors from
	// those closes must not overwrite an idle timeout, shutdown, or EOF.
	if !t.closed {
		t.closed = true
		t.reason = reason
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	t.client.Close()
	t.upstream.Close()
}
func (t *tunnel) startIdle(idle time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.idle = idle
	t.last = time.Now()
	t.timer = time.AfterFunc(idle, t.expire)
}
func (t *tunnel) touch() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed && t.timer != nil {
		t.last = time.Now()
		t.timer.Reset(t.idle)
	}
}
func (t *tunnel) expire() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	remaining := t.idle - time.Since(t.last)
	if remaining > 0 {
		t.timer.Reset(remaining)
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.reason = "idle_timeout"
	t.mu.Unlock()
	t.client.Close()
	t.upstream.Close()
}

type activityWriter struct {
	net.Conn
	t *tunnel
}

func (w activityWriter) Write(b []byte) (int, error) {
	n, err := w.Conn.Write(b)
	if n > 0 {
		w.t.touch()
	}
	return n, err
}
func sourceIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return "unknown"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	return ip.Unmap().String()
}

// Transport retains context values when detaching cancellation for pooling.
// Carry the request lifetime to the actual dial so canceled clients do not
// leave speculative DNS/dial operations running.
type requestContextKey struct{}

type Proxy struct {
	transport     *http.Transport
	dial          func(context.Context, string, string) (net.Conn, error)
	log           *slog.Logger
	mu            sync.Mutex
	stopping      bool
	active        int
	maxClients    int
	idleTimeout   time.Duration
	pendingCtx    context.Context
	pendingCancel context.CancelFunc
	forceCtx      context.Context
	forceCancel   context.CancelFunc
	clients       map[*clientConn]struct{}
	tunnels       map[*tunnel]struct{}
	changed       chan struct{}
}

// Configure before serving; production configuration is validated in main.
func New(log *slog.Logger, maxClients int, idleTimeout time.Duration) *Proxy {
	p := newProxy(log, newPolicyDialer().DialContext)
	p.maxClients = maxClients
	p.idleTimeout = idleTimeout
	return p
}
func newProxy(log *slog.Logger, dial func(context.Context, string, string) (net.Conn, error)) *Proxy {
	pending, pendingCancel := context.WithCancel(context.Background())
	force, forceCancel := context.WithCancel(context.Background())
	p := &Proxy{
		maxClients: 100, idleTimeout: 10 * time.Minute,
		pendingCtx: pending, pendingCancel: pendingCancel,
		forceCtx: force, forceCancel: forceCancel,
		log:     log,
		dial:    dial,
		clients: make(map[*clientConn]struct{}),
		tunnels: make(map[*tunnel]struct{}),
		changed: make(chan struct{}, 1),
		transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				stopping := force
				if request, ok := ctx.Value(requestContextKey{}).(context.Context); ok {
					stopping = request
				}
				ctx, cancel, stop := stopContext(ctx, stopping)
				defer cancel()
				defer stop()
				return dial(ctx, network, address)
			},
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
	return p
}
func stopContext(parent, stopping context.Context) (context.Context, context.CancelFunc, func() bool) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(stopping, cancel)
	if stopping.Err() != nil {
		cancel()
	}
	return ctx, cancel, stop
}
func (p *Proxy) Server() *http.Server {
	return &http.Server{
		Handler: p, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, IdleTimeout: p.idleTimeout,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if c, ok := conn.(*clientConn); ok {
				return c.context(ctx)
			}
			return ctx
		},
		ConnState: func(conn net.Conn, state http.ConnState) {
			if c, ok := conn.(*clientConn); ok {
				switch state {
				case http.StateHijacked:
					c.stopIdle()
				case http.StateIdle:
					c.startIdle() // Resume HTTP after an unsuccessful CONNECT.
				}
			}
		},
	}
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
func failure(w http.ResponseWriter, err error) int {
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
	return code
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	stopping := p.stopping
	if !stopping {
		p.active++
	}
	p.mu.Unlock()
	if !stopping {
		defer func() { p.mu.Lock(); p.active--; p.mu.Unlock(); p.notify() }()
		ctx, cancel, stop := stopContext(r.Context(), p.forceCtx)
		defer cancel()
		defer stop()
		r = r.WithContext(ctx)
	}
	if stopping {
		p.reject(w, r, 503, "", "shutdown")
		return
	}
	if r.ProtoMajor != 1 {
		p.reject(w, r, 405, "", "unsupported_mode")
		return
	}
	if r.Method == http.MethodConnect {
		// Pending CONNECT dials retain their existing bounded cancellation policy.
		// Once hijacked, only the established tunnel's shared timer owns inactivity.
		if c, ok := r.Context().Value(clientConnKey{}).(*clientConn); ok {
			c.stopIdle()
		}
		p.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil || r.URL.Opaque != "" || r.URL.Fragment != "" {
		p.fail(w, r, ErrTarget, "")
		return
	}
	host, port, err := authority(r.URL.Host, true)
	if err != nil {
		p.fail(w, r, err, host)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		p.reject(w, r, 405, "", "unsupported_mode")
		return
	}
	// Allow an origin to respond before the streaming request body reaches EOF.
	http.NewResponseController(w).EnableFullDuplex()
	out := r.Clone(context.WithValue(r.Context(), requestContextKey{}, r.Context()))
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
		p.fail(w, r, err, host)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 101 {
		p.reject(w, r, 502, host, "unsupported_upstream")
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
		p.log.Warn("request interrupted", "source_ip", sourceIP(r.RemoteAddr), "method", r.Method, "host", host, "status", resp.StatusCode, "reason", "stream_error")
		panic(http.ErrAbortHandler)
	}
	p.log.Info("request", "source_ip", sourceIP(r.RemoteAddr), "method", r.Method, "host", host, "status", resp.StatusCode, "duration", time.Since(start))
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
		p.fail(w, r, err, host)
		return
	}
	ctx, cancel, stop := stopContext(r.Context(), p.pendingCtx)
	upstream, err := p.dial(ctx, "tcp", net.JoinHostPort(host, port))
	if ctx.Err() != nil {
		if upstream != nil {
			upstream.Close()
		}
		err = ctx.Err()
	}
	cancel()
	stop()
	if err != nil {
		if p.pendingCtx.Err() != nil {
			p.reject(w, r, 503, host, "shutdown")
		} else {
			p.fail(w, r, err, host)
		}
		return
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		p.reject(w, r, 405, "", "unsupported_mode")
		return
	}
	// Registration and hijacking are serialized with shutdown.
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		upstream.Close()
		p.reject(w, r, 503, "", "shutdown")
		return
	}
	client, rw, err := h.Hijack()
	if err != nil {
		p.mu.Unlock()
		upstream.Close()
		return
	}
	t := &tunnel{client: client, upstream: upstream}
	started := time.Now()
	completionReason := "handshake_error"
	p.tunnels[t] = struct{}{}
	p.mu.Unlock()
	defer func() {
		// A return before copying finishes means the CONNECT response failed.
		// An already claimed terminal cause always takes precedence.
		t.close(completionReason)
		t.mu.Lock()
		reason := t.reason
		t.mu.Unlock()
		p.log.Info("tunnel closed", "reason", reason, "source_ip", sourceIP(r.RemoteAddr), "host", host, "port", port, "duration", time.Since(started))
		p.mu.Lock()
		delete(p.tunnels, t)
		p.mu.Unlock()
		p.notify()
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
	t.startIdle(p.idleTimeout)
	p.log.Info("tunnel established", "source_ip", sourceIP(r.RemoteAddr), "host", host, "port", port, "status", 200)
	results := make(chan error, 2)
	copyHalf := func(dst net.Conn, src io.Reader) {
		_, e := io.Copy(activityWriter{dst, t}, src)
		if e == nil {
			if c, ok := dst.(interface{ CloseWrite() error }); ok {
				e = c.CloseWrite()
			} else {
				t.close("eof")
			}
		}
		if e != nil {
			reason := "io_error"
			if errors.Is(e, errNoHalfClose) {
				reason = "eof"
			}
			t.close(reason)
		}
		results <- e
	}
	go copyHalf(upstream, rw.Reader)
	go copyHalf(client, upstream)
	<-results
	<-results
	completionReason = "eof"
}

func (p *Proxy) notify() {
	select {
	case p.changed <- struct{}{}:
	default:
	}
}
func (p *Proxy) reject(w http.ResponseWriter, r *http.Request, code int, host, reason string) {
	http.Error(w, http.StatusText(code), code)
	p.log.Warn("request failed", "source_ip", sourceIP(r.RemoteAddr), "method", r.Method, "host", host, "status", code, "reason", reason)
}
func (p *Proxy) fail(w http.ResponseWriter, r *http.Request, err error, host string) {
	code := failure(w, err)
	reason := "upstream"
	switch {
	case errors.Is(err, ErrBlocked):
		reason = "blocked"
	case errors.Is(err, ErrTarget):
		reason = "invalid_target"
	case code == 504:
		reason = "timeout"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	}
	p.log.Warn("request failed", "source_ip", sourceIP(r.RemoteAddr), "method", r.Method, "host", host, "status", code, "reason", reason)
}

// Shutdown gives existing streams one shared drain budget. Pending CONNECT
// operations are canceled immediately; ordinary HTTP streams are canceled only
// when forced closed. Network closes never run under the proxy mutex.
func (p *Proxy) Shutdown(ctx context.Context, s *http.Server) error {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	p.pendingCancel()
	defer p.transport.CloseIdleConnections()
	// Also cancel detached transport dials after a successful drain.
	defer p.forceCancel()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(ctx) }()
	httpDone := false
	var httpErr error
	for {
		p.mu.Lock()
		active := p.active
		clients := len(p.clients)
		p.mu.Unlock()
		if httpDone && active == 0 && clients == 0 {
			return httpErr
		}
		select {
		case err := <-done:
			httpDone = true
			httpErr = err
			done = nil
		case <-p.changed:
		case <-ctx.Done():
			p.forceCancel()
			s.Close()
			p.mu.Lock()
			tunnels := make([]*tunnel, 0, len(p.tunnels))
			for t := range p.tunnels {
				tunnels = append(tunnels, t)
			}
			clients := make([]*clientConn, 0, len(p.clients))
			for c := range p.clients {
				clients = append(clients, c)
			}
			p.mu.Unlock()
			for _, t := range tunnels {
				t.close("shutdown")
			}
			for _, c := range clients {
				c.Close()
			}
			// Observe already completed cleanup without exceeding the caller's budget.
			if !httpDone {
				select {
				case <-done:
				default:
				}
			}
			return ctx.Err()
		}
	}
}
