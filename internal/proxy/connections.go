package proxy

import (
	"errors"
	"net"
	"sync"
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
		return wrapped, nil
	}
}

type clientConn struct {
	net.Conn
	listener *limitedListener
	once     sync.Once
	err      error
}

func (c *clientConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.listener.p.mu.Lock()
		delete(c.listener.p.clients, c)
		c.listener.p.mu.Unlock()
		c.listener.p.notify()
	})
	return c.err
}

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
