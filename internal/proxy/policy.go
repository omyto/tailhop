package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

var ErrBlocked = errors.New("destination blocked")
var ErrTarget = errors.New("invalid target")

const dialTimeout = 10 * time.Second

// Conservative IANA special-purpose exclusions; IPv6 must also be in global
// unicast allocation space. Transition mechanisms are deliberately excluded.
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var excluded = func() []netip.Prefix {
	var p []netip.Prefix
	for _, s := range strings.Fields("0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.0.2.0/24 192.88.99.0/24 192.168.0.0/16 198.18.0.0/15 198.51.100.0/24 203.0.113.0/24 224.0.0.0/4 240.0.0.0/4 2001::/23 2001:db8::/32 2002::/16 3fff::/20") {
		p = append(p, netip.MustParsePrefix(s))
	}
	return p
}()

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range excluded {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func authority(a string, defaultPort bool) (string, string, error) {
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		if !defaultPort {
			return "", "", ErrTarget
		}
		if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") {
			host = a[1 : len(a)-1]
			ip, e := netip.ParseAddr(host)
			if e != nil || !ip.Is6() {
				return "", "", ErrTarget
			}
		} else {
			if strings.ContainsAny(a, ":[]") {
				return "", "", ErrTarget
			}
			host = a
		}
		port = "80"
	}
	if strings.HasPrefix(a, "[") {
		if ip, e := netip.ParseAddr(host); e != nil || !ip.Is6() {
			return "", "", ErrTarget
		}
	}
	if host == "" || strings.ContainsAny(host, "%@/\\?# \t\r\n") {
		return "", "", ErrTarget
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		if ip.Zone() != "" {
			return "", "", ErrTarget
		}
	} else {
		name := strings.TrimSuffix(host, ".")
		if len(name) > 253 {
			return "", "", ErrTarget
		}
		for _, label := range strings.Split(name, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", "", ErrTarget
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return "", "", ErrTarget
				}
			}
		}
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", "", ErrTarget
		}
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 {
		return "", "", ErrTarget
	}
	if n != 80 && n != 443 {
		return "", "", ErrBlocked
	}
	return host, strconv.Itoa(n), nil
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type policyDialer struct {
	resolver resolver
	dial     func(context.Context, string, string) (net.Conn, error)
}

func newPolicyDialer() *policyDialer {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &policyDialer{net.DefaultResolver, d.DialContext}
}
func (d *policyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := authority(address, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var ips []netip.Addr
	if ip, e := netip.ParseAddr(host); e == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = d.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	err = ErrBlocked
	for _, ip := range ips {
		if !publicIP(ip) {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var c net.Conn
		c, err = d.dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		if c != nil {
			c.Close()
		}
	}
	return nil, err
}
