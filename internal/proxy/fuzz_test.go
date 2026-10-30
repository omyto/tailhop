package proxy

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func FuzzAuthority(f *testing.F) {
	for _, target := range []string{
		"example.com", "example.com:80", "example.com.:00443", "8.8.8.8:443",
		"[2606:4700::1111]", "[::ffff:127.0.0.1]:80", "[fe80::1%eth0]:443",
		"", "a:22", "a:0", "a:65536", "a:", "a:+80", "a:80:443", "::1:80",
		"[example.com]:80", "user:pass@example.com:80", "a/b:80", "a?x:80",
		"a#x:80", "a..b:80", "a\r\nHost: b:80", strings.Repeat("a", 64) + ":80",
	} {
		f.Add(target, false)
		f.Add(target, true)
	}
	f.Fuzz(func(t *testing.T, target string, defaultPort bool) {
		host, port, err := authority(target, defaultPort)
		if errors.Is(err, ErrTarget) {
			return
		}
		if err != nil && !errors.Is(err, ErrBlocked) {
			t.Fatalf("unexpected authority error: %v", err)
		}
		n, parseErr := strconv.Atoi(port)
		if parseErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			t.Fatalf("non-canonical port %q", port)
		}
		if (err == nil) != (port == "80" || port == "443") {
			t.Fatalf("port policy bypass: %q: %v", port, err)
		}
		canonical := net.JoinHostPort(host, port)
		u, parseErr := url.Parse("http://" + canonical + "/")
		if parseErr != nil || host == "" || u.Hostname() != host || u.User != nil || u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			t.Fatalf("authority changes URL structure: %q", canonical)
		}
		if !defaultPort {
			originalHost, _, splitErr := net.SplitHostPort(target)
			if splitErr != nil || originalHost != host {
				t.Fatalf("accepted authority without an explicit port: %q", target)
			}
		}
		gotHost, gotPort, gotErr := authority(canonical, false)
		if gotHost != host || gotPort != port || !errors.Is(gotErr, err) {
			t.Fatalf("authority round trip changed: %q", canonical)
		}
	})
}

func FuzzPublicIP(f *testing.F) {
	for _, text := range []string{
		"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8",
		"0.0.0.0", "127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1",
		"100.64.0.0", "100.127.255.255", "169.254.169.254", "192.0.2.1",
		"198.18.0.0", "198.19.255.255", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1",
		"ff02::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1",
		"2002::1", "3fff::1",
	} {
		f.Add(netip.MustParseAddr(text).AsSlice())
	}
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3})
	// These independently check special-use classes not covered by IsPrivate.
	var nonPublic []netip.Prefix
	for _, cidr := range []string{"100.64.0.0/10", "169.254.0.0/16", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "64:ff9b::/96", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		nonPublic = append(nonPublic, netip.MustParsePrefix(cidr))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		ip, _ := netip.AddrFromSlice(raw)
		allowed := publicIP(ip)
		if allowed {
			plain := ip.Unmap()
			if !plain.IsValid() || plain.IsPrivate() || plain.IsLoopback() || plain.IsUnspecified() || plain.IsMulticast() || plain.IsLinkLocalUnicast() || plain.IsLinkLocalMulticast() {
				t.Fatalf("allowed non-public address: %v", ip)
			}
			for _, prefix := range nonPublic {
				if prefix.Contains(plain) {
					t.Fatalf("allowed special-use address: %v", ip)
				}
			}
		}
		if !ip.IsValid() {
			return
		}
		if allowed != publicIP(ip.Unmap()) {
			t.Fatalf("IPv4-mapped alias changes policy: %v", ip)
		}
		if ip.Is4() && allowed != publicIP(netip.AddrFrom16(ip.As16())) {
			t.Fatalf("IPv4-to-mapped conversion changes policy: %v", ip)
		}
		if ip.Is6() && publicIP(ip.WithZone("eth0")) {
			t.Fatalf("allowed scoped address: %v", ip)
		}
	})
}

func FuzzStripHeaders(f *testing.F) {
	f.Add("Custom", "value", false)
	f.Add("Proxy-Authorization", "credentials", true)
	f.Add("x", "\x00\r\n", false)
	f.Fuzz(func(t *testing.T, token, value string, nominateEnd bool) {
		if token == "" || len(token) > 128 {
			t.Skip()
		}
		for _, c := range token {
			if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", c) {
				t.Skip()
			}
		}
		name := "X-Hop-" + token
		h := http.Header{}
		h.Set(name, value)
		h["X-End"] = []string{"first", "second"}
		h.Set("Connection", " keep-alive, "+strings.ToLower(name)+", ")
		h.Add("Connection", "\t"+strings.ToUpper(name)+"\t, Upgrade")
		if nominateEnd {
			h.Add("Connection", "x-end")
		}
		hop := []string{"Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"}
		for _, key := range hop {
			h.Set(key, value)
		}
		strip(h)
		for _, key := range append(hop, "Connection", name) {
			if _, exists := h[http.CanonicalHeaderKey(key)]; exists {
				t.Fatalf("forwarded hop-by-hop header %q", key)
			}
		}
		if nominateEnd {
			if _, exists := h["X-End"]; exists {
				t.Fatal("forwarded Connection-nominated end header")
			}
		} else if !reflect.DeepEqual(h.Values("X-End"), []string{"first", "second"}) {
			t.Fatal("changed legitimate multi-value end-to-end header")
		}
		once := h.Clone()
		strip(h)
		if !reflect.DeepEqual(h, once) {
			t.Fatal("header stripping is not idempotent")
		}
	})
}
