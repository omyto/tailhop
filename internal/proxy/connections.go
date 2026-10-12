package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

var errNoHalfClose = errors.New("half-close unsupported")

// limitedListener accepts and immediately closes excess connections. It never
// waits for capacity or starts a rejection goroutine.
type limitedListener struct {
	net.Listener
	p *Proxy
}

func (p *Proxy) Listener(l net.Listener) net.Listener {
	return &limitedListener{Listener: l, p: p}
}
func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.p.mu.Lock()
		if l.p.stopping {
			l.p.mu.Unlock()
			c.Close()
			return nil, net.ErrClosed
		}
		if len(l.p.clients) >= l.p.maxClients {
			l.p.mu.Unlock()
			c.Close()
			l.p.log.Warn("connection rejected", "source_ip", sourceIP(c.RemoteAddr().String()), "reason", "max_clients")
			continue
		}
		wrapped := &clientConn{Conn: c, listener: l}
		l.p.clients[wrapped] = struct{}{}
		l.p.mu.Unlock()
		wrapped.startIdle()
		return wrapped, nil
	}
}

type clientConn struct {
	net.Conn
	listener *limitedListener
	once     sync.Once
	err      error
	idleMu   sync.Mutex
	timer    *time.Timer
	last     time.Time
	enabled  bool
	closed   bool
	cancel   context.CancelFunc
}

func (c *clientConn) Close() error {
	c.once.Do(func() {
		c.idleMu.Lock()
		c.closed = true
		c.enabled = false
		if c.timer != nil {
			c.timer.Stop()
		}
		cancel := c.cancel
		c.idleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		c.err = c.Conn.Close()
		c.listener.p.mu.Lock()
		delete(c.listener.p.clients, c)
		c.listener.p.mu.Unlock()
		c.listener.p.notify()
	})
	return c.err
}

// The timer never changes net/http's deadlines. Reads and writes share the
// last-activity timestamp; a stale callback rechecks it under the same mutex.
func (c *clientConn) startIdle() {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if c.closed || c.enabled {
		return
	}
	c.enabled = true
	c.last = time.Now()
	if c.timer == nil {
		c.timer = time.AfterFunc(c.listener.p.idleTimeout, c.expire)
	} else {
		c.timer.Reset(c.listener.p.idleTimeout)
	}
}
func (c *clientConn) stopIdle() {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	c.enabled = false
	if c.timer != nil {
		c.timer.Stop()
	}
}
func (c *clientConn) touch() {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	if c.enabled && !c.closed {
		c.last = time.Now()
		c.timer.Reset(c.listener.p.idleTimeout)
	}
}
func (c *clientConn) expire() {
	c.idleMu.Lock()
	if !c.enabled || c.closed {
		c.idleMu.Unlock()
		return
	}
	remaining := c.listener.p.idleTimeout - time.Since(c.last)
	if remaining > 0 {
		c.timer.Reset(remaining)
		c.idleMu.Unlock()
		return
	}
	// Claim terminal closure before unlocking, so a concurrent read/reset cannot
	// revive this connection or schedule another timer after expiration.
	c.closed = true
	c.enabled = false
	c.idleMu.Unlock()
	c.Close()
}
func (c *clientConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}
func (c *clientConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

// Close must cancel the handler even when net/http cannot detect a disconnect
// with its background reader (for example, an unfinished request body).
func (c *clientConn) context(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	c.idleMu.Lock()
	c.cancel = cancel
	closed := c.closed
	c.idleMu.Unlock()
	if closed {
		cancel()
	}
	return context.WithValue(ctx, clientConnKey{}, c)
}

type clientConnKey struct{}

// A half-close does not release admission capacity: the other direction may
// remain active. The full Close is the only owner of the slot.
func (c *clientConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errNoHalfClose
}
func (c *clientConn) CloseRead() error {
	if half, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return half.CloseRead()
	}
	return errNoHalfClose
}
