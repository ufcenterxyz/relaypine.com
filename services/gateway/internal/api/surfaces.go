package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ValidateAppURLs requires distinct hosts so host-only browser cookies isolate
// the public console and the administrative surface, including in development.
func ValidateAppURLs(appURL, adminURL string, production bool) (string, string, error) {
	normalize := func(raw string) (*url.URL, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") || strings.HasSuffix(u.Hostname(), ".") {
			return nil, errors.New("APP_URL and ADMIN_APP_URL must be valid HTTP origins")
		}
		if production && u.Scheme != "https" {
			return nil, errors.New("production console origins require HTTPS")
		}
		u.Host = strings.ToLower(u.Host)
		u.Path = ""
		return u, nil
	}
	public, err := normalize(appURL)
	if err != nil {
		return "", "", err
	}
	admin, err := normalize(adminURL)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(public.Hostname(), admin.Hostname()) {
		return "", "", errors.New("APP_URL and ADMIN_APP_URL must use different hostnames")
	}
	return public.String(), admin.String(), nil
}

// Trust the HTTP Host accepted by the ingress, never client-supplied forwarded
// host headers. The local Next.js proxy preserves this validated host as well.
func (s *Server) surface(r *http.Request) (string, string) {
	for _, item := range []struct{ name, origin string }{{"customer", s.AppURL}, {"admin", s.AdminAppURL}} {
		u, err := url.Parse(item.origin)
		if err == nil && u.Host != "" && strings.EqualFold(r.Host, u.Host) {
			return item.name, item.origin
		}
	}
	return "", ""
}
func (s *Server) surfaceInfo(surface string) map[string]any {
	return map[string]any{"surface": surface, "app_url": s.AppURL, "admin_app_url": s.AdminAppURL}
}
