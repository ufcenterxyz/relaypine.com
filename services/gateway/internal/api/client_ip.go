package api

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseProxyCIDRs accepts only explicit networks. Empty configuration trusts no
// forwarded headers; a malformed configuration must stop startup.
func ParseProxyCIDRs(value string) ([]netip.Prefix, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, value := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, errors.New("proxy trust requires explicit IPv4 or IPv6 CIDRs, excluding /0")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func inNetworks(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func parseIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	return ip.Unmap(), err == nil && ip.Zone() == ""
}

// clientIP follows the configured trust chain, never a client-supplied claim
// alone. Ingress must overwrite X-Forwarded-For with its socket peer (the default
// ingress-nginx configuration), or append that peer to a validated chain.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, ok := parseIP(host)
	if !ok {
		return "unknown"
	}
	if !inNetworks(peer, s.TrustedProxyCIDRs) {
		return peer.String()
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) != 1 || len(forwarded[0]) > 2048 {
		return peer.String()
	}
	values := strings.Split(forwarded[0], ",")
	if len(values) > 16 {
		return peer.String()
	}
	chain := make([]netip.Addr, len(values))
	for i, value := range values {
		ip, valid := parseIP(value)
		if !valid {
			return peer.String()
		}
		chain[i] = ip
	}
	current := peer
	for i := len(chain) - 1; i >= 0 && inNetworks(current, s.TrustedProxyCIDRs); i-- {
		current = chain[i]
	}
	// Cloudflare's visitor header is authoritative only when ingress observed an
	// actual Cloudflare socket peer. A direct-origin caller cannot opt into trust
	// by sending this header or inserting an address to the left of its own hop.
	if inNetworks(current, s.CloudflareProxyCIDRs) {
		visitor := r.Header.Values("CF-Connecting-IP")
		if len(visitor) == 1 {
			if ip, valid := parseIP(visitor[0]); valid {
				return ip.String()
			}
		}
	}
	return current.String()
}
