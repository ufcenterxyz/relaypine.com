package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Targets are resolved on every new connection; the validated IP, rather than
// its hostname, is dialed. This also applies when a SOCKS proxy is used.
type safeDialer struct{ allowPrivate bool }

func (d safeDialer) addresses(ctx context.Context, address string) ([]string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no addresses")
	}
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		if !d.allowPrivate && !publicIP(ip) {
			return nil, errors.New("private or reserved upstream address blocked")
		}
		result = append(result, net.JoinHostPort(ip.String(), port))
	}
	return result, nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}

func (d safeDialer) Dial(network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return d.DialContext(ctx, network, address)
}
func (d safeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	addresses, err := d.addresses(ctx, address)
	if err != nil {
		return nil, err
	}
	var last error
	for _, addr := range addresses {
		conn, err := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func transport(proxyURL string, allowPrivate bool) (*http.Transport, error) {
	direct := safeDialer{allowPrivate: allowPrivate}
	dial := direct.DialContext
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil || (u.Scheme != "socks5" && u.Scheme != "socks5h") || u.Hostname() == "" || u.Port() == "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || u.Fragment != "" {
			return nil, errors.New("invalid SOCKS5 proxy")
		}
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
		socks, err := proxy.SOCKS5("tcp", u.Host, auth, direct)
		if err != nil {
			return nil, err
		}
		cd, ok := socks.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("proxy has no cancellation support")
		}
		dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			addresses, err := direct.addresses(ctx, address)
			if err != nil {
				return nil, err
			}
			var last error
			for _, addr := range addresses {
				conn, err := cd.DialContext(ctx, network, addr)
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		}
	}
	return &http.Transport{Proxy: nil, DialContext: dial, ForceAttemptHTTP2: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 90 * time.Second, DisableCompression: true}, nil
}

func upstreamURL(base, path string, allowPrivate bool) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(allowPrivate && u.Scheme == "http")) {
		return "", errors.New("upstream must have a valid HTTPS base URL")
	}
	prefix := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(prefix, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	u.Path = prefix + path
	u.RawPath = ""
	if strings.Contains(u.Path, "..") {
		return "", fmt.Errorf("invalid upstream path")
	}
	return u.String(), nil
}
