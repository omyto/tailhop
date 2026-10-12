package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

type resolveFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolveFunc) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func TestPublicIP(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !publicIP(netip.MustParseAddr(s)) {
			t.Errorf("blocked public %s", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "fc00::1", "fe80::1", "100.64.0.1", "169.254.169.254", "198.18.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1", "::ffff:127.0.0.1", "::ffff:100.64.0.1", "0.0.0.0", "255.255.255.255", "224.0.0.1", "ff02::1", "64:ff9b::a00:1", "2002:0808:0808::1", "3fff::1", "2001::1"} {
		if publicIP(netip.MustParseAddr(s)) {
			t.Errorf("allowed %s", s)
		}
	}
}
func TestAuthority(t *testing.T) {
	for _, a := range []string{"example.com", "[2606:4700::1111]", "[::ffff:8.8.8.8]"} {
		_, port, e := authority(a, true)
		if e != nil || port != "80" {
			t.Fatalf("default port %s: %s %v", a, port, e)
		}
	}

	for _, a := range []string{"example.com:80", "[2606:4700::1111]:443", "8.8.8.8:443"} {
		if _, _, e := authority(a, false); e != nil {
			t.Errorf("%s: %v", a, e)
		}
	}
	for _, a := range []string{"example.com", "example.com:", "a:abc", "a:0", "a:65536", "a:80:443", "a@b:80", "[fe80::1%eth0]:80", "a..b:80", "-a:80", "::1:80", "[example.com]:80"} {
		if _, _, e := authority(a, false); e == nil {
			t.Errorf("accepted %s", a)
		}
	}
	if _, _, e := authority("example.com:22", false); !errors.Is(e, ErrBlocked) {
		t.Fatal(e)
	}
}
func TestVettedNumericDial(t *testing.T) {
	calls := 0
	d := &policyDialer{resolver: resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		if calls > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("8.8.8.8")}, nil
	}), dial: func(ctx context.Context, n, a string) (net.Conn, error) {
		if a != "8.8.8.8:80" || n != "tcp" {
			t.Fatalf("unvetted dial %s %s", n, a)
		}
		c, b := net.Pipe()
		b.Close()
		return c, nil
	}}
	c, e := d.DialContext(context.Background(), "tcp", "example.com:80")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if calls != 1 {
		t.Fatalf("resolved %d times", calls)
	}
	// A new connection must resolve and validate again, even for the same host.
	if _, e = d.DialContext(context.Background(), "tcp", "example.com:80"); !errors.Is(e, ErrBlocked) {
		t.Fatalf("rebound destination: %v", e)
	}
	if calls != 2 {
		t.Fatalf("resolution not repeated: %d", calls)
	}
}
func TestPolicyFailures(t *testing.T) {
	for _, kind := range []string{"blocked", "dns", "canceled", "dial"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := ErrBlocked
			d := &policyDialer{resolver: resolveFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				switch kind {
				case "dns":
					return nil, errors.New("dns failed")
				case "canceled":
					<-ctx.Done()
					return nil, ctx.Err()
				case "dial":
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				}
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}), dial: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }}
			if kind == "canceled" {
				cancel()
				want = context.Canceled
			}
			if kind == "dial" {
				want = context.DeadlineExceeded
			}
			_, e := d.DialContext(ctx, "tcp", "example.com:80")
			if e == nil || kind != "dns" && !errors.Is(e, want) {
				t.Fatalf("error %v", e)
			}
		})
	}
	d := newPolicyDialer()
	for _, a := range []string{"127.0.0.1:80", "[::ffff:127.0.0.1]:443", "8.8.8.8:22"} {
		if _, e := d.DialContext(context.Background(), "tcp", a); !errors.Is(e, ErrBlocked) {
			t.Fatalf("%s: %v", a, e)
		}
	}
}

func TestDialCandidatesAndDeadline(t *testing.T) {
	attempts := 0
	d := &policyDialer{
		resolver: resolveFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > dialTimeout {
				t.Error("DNS lookup lacks bounded deadline")
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
		}),
		dial: func(ctx context.Context, _, address string) (net.Conn, error) {
			attempts++
			if attempts == 1 {
				if address != "8.8.8.8:443" {
					t.Fatal(address)
				}
				return nil, errors.New("first candidate unavailable")
			}
			if address != "1.1.1.1:443" {
				t.Fatal(address)
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}
	c, e := d.DialContext(context.Background(), "tcp", "example.com:443")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if attempts != 2 {
		t.Fatal(attempts)
	}
	// Literal IPv6 bypasses DNS but still dials only the classified numeric IP.
	d.resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("resolved literal")
		return nil, nil
	})
	d.dial = func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }
	if _, e = d.DialContext(context.Background(), "tcp", "[2606:4700::1111]:443"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
