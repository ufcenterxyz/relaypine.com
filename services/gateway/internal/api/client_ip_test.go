package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIPRequiresVerifiedProxyChain(t *testing.T) {
	trusted, err := ParseProxyCIDRs("10.0.101.210/32,10.0.101.211/32")
	if err != nil {
		t.Fatal(err)
	}
	cloudflare, err := ParseProxyCIDRs("173.245.48.0/20,2400:cb00::/32")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{TrustedProxyCIDRs: trusted, CloudflareProxyCIDRs: cloudflare}
	for _, tc := range []struct {
		name, peer, forwarded, visitor, want string
		duplicateForwarded, duplicateVisitor bool
	}{
		{name: "untrusted peer cannot spoof headers", peer: "192.0.2.10:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1", want: "192.0.2.10"},
		{name: "direct origin ignores forged Cloudflare header", peer: "10.0.101.210:1234", forwarded: "192.0.2.10", visitor: "198.51.100.1", want: "192.0.2.10"},
		{name: "inserted Cloudflare hop cannot bypass direct origin", peer: "10.0.101.210:1234", forwarded: "173.245.48.1, 192.0.2.10", visitor: "198.51.100.1", want: "192.0.2.10"},
		{name: "verified Cloudflare visitor", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1", want: "198.51.100.1"},
		{name: "verified IPv6 visitor", peer: "10.0.101.210:1234", forwarded: "2400:cb00::1", visitor: "2001:db8::10", want: "2001:db8::10"},
		{name: "multiple trusted internal proxies", peer: "10.0.101.210:1234", forwarded: "173.245.48.1, 10.0.101.211", visitor: "198.51.100.1", want: "198.51.100.1"},
		{name: "first untrusted intermediary owns visitor claim", peer: "10.0.101.210:1234", forwarded: "173.245.48.1, 192.0.2.11, 10.0.101.211", visitor: "198.51.100.1", want: "192.0.2.11"},
		{name: "mapped IPv4 addresses normalized", peer: "[::ffff:10.0.101.210]:1234", forwarded: "::ffff:173.245.48.1", visitor: "::ffff:198.51.100.1", want: "198.51.100.1"},
		{name: "missing XFF cannot trust Cloudflare header", peer: "10.0.101.210:1234", visitor: "198.51.100.1", want: "10.0.101.210"},
		{name: "invalid chain falls back to socket", peer: "10.0.101.210:1234", forwarded: "invalid, 173.245.48.1", visitor: "198.51.100.1", want: "10.0.101.210"},
		{name: "empty chain element rejected", peer: "10.0.101.210:1234", forwarded: ", 173.245.48.1", visitor: "198.51.100.1", want: "10.0.101.210"},
		{name: "duplicate XFF rejected", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1", duplicateForwarded: true, want: "10.0.101.210"},
		{name: "duplicate visitor rejected", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1", duplicateVisitor: true, want: "173.245.48.1"},
		{name: "visitor list rejected", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1, 198.51.100.2", want: "173.245.48.1"},
		{name: "visitor with port rejected", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "198.51.100.1:80", want: "173.245.48.1"},
		{name: "zoned visitor rejected", peer: "10.0.101.210:1234", forwarded: "173.245.48.1", visitor: "fe80::1%eth0", want: "173.245.48.1"},
		{name: "too many hops rejected", peer: "10.0.101.210:1234", forwarded: strings.Repeat("192.0.2.1,", 16) + "173.245.48.1", visitor: "198.51.100.1", want: "10.0.101.210"},
		{name: "oversized header rejected", peer: "10.0.101.210:1234", forwarded: strings.Repeat(" ", 2048) + "173.245.48.1", visitor: "198.51.100.1", want: "10.0.101.210"},
		{name: "unrecognized socket fails closed", peer: "invalid", forwarded: "173.245.48.1", visitor: "198.51.100.1", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/auth/code", nil)
			r.RemoteAddr = tc.peer
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if tc.visitor != "" {
				r.Header.Set("CF-Connecting-IP", tc.visitor)
			}
			if tc.duplicateForwarded {
				r.Header.Add("X-Forwarded-For", "198.51.100.2")
			}
			if tc.duplicateVisitor {
				r.Header.Add("CF-Connecting-IP", "198.51.100.2")
			}
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("client IP = %q; want %q", got, tc.want)
			}
		})
	}
	r := httptest.NewRequest("POST", "/api/auth/code", nil)
	r.RemoteAddr = "10.0.101.210:1234"
	r.Header.Set("X-Forwarded-For", "173.245.48.1")
	r.Header.Set("CF-Connecting-IP", "198.51.100.1")
	if got := (&Server{}).clientIP(r); got != "10.0.101.210" {
		t.Fatal("default server trusted a forwarded header")
	}
}

func TestProxyCIDRsRejectUnsafeConfiguration(t *testing.T) {
	for _, input := range []string{"invalid", "10.0.0.1", "10.0.0.1/32,", "0.0.0.0/0", "::/0", "::ffff:10.0.0.1/128"} {
		if _, err := ParseProxyCIDRs(input); err == nil {
			t.Fatalf("accepted invalid proxy trust %q", input)
		}
	}
	if got, err := ParseProxyCIDRs(" "); err != nil || len(got) != 0 {
		t.Fatal("empty trust configuration should disable proxy headers")
	}
}

func TestPublicLimitSeparatesVerifiedVisitorsBehindIngress(t *testing.T) {
	f := newAPIFixture(t)
	f.enablePublicRequests(t)
	f.s.TrustedProxyCIDRs, _ = ParseProxyCIDRs("10.0.101.210/32")
	f.s.CloudflareProxyCIDRs, _ = ParseProxyCIDRs("173.245.48.0/20")
	r := httptest.NewRequest("POST", "/api/auth/code", nil)
	r.RemoteAddr = "10.0.101.210:1234"
	r.Header.Set("X-Forwarded-For", "173.245.48.1")
	r.Header.Set("CF-Connecting-IP", "198.51.100.1")
	scope := "test-visitors-" + f.prefix
	for i := 0; i < 100; i++ {
		if !f.s.publicLimit(f.ctx, r, fmt.Sprintf("visitor-%d@example.test", i), scope) {
			t.Fatalf("visitor was limited before IP allowance: %d", i)
		}
	}
	if f.s.publicLimit(f.ctx, r, "overflow@example.test", scope) {
		t.Fatal("IP allowance was not enforced")
	}
	r.Header.Set("CF-Connecting-IP", "198.51.100.2")
	if !f.s.publicLimit(f.ctx, r, "another@example.test", scope) {
		t.Fatal("one visitor exhausted another visitor's IP allowance")
	}
}
