package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleOriginsRequireDistinctSecureHosts(t *testing.T) {
	for _, tc := range []struct {
		public, admin string
		production    bool
		valid         bool
	}{
		{"https://relaypine.com/", "https://ADMIN.relaypine.com", true, true},
		{"http://localhost:4810", "http://admin.localhost:4810", false, true},
		{"https://relaypine.com", "", true, false},
		{"https://relaypine.com", "http://admin.relaypine.com", true, false},
		{"http://localhost:4810", "http://localhost:4814", false, false},
		{"https://relaypine.com", "https://relaypine.com:443", true, false},
		{"https://relaypine.com", "https://user@admin.relaypine.com", true, false},
		{"https://relaypine.com", "https://admin.relaypine.com/path", true, false},
		{"https://relaypine.com", "https://admin.relaypine.com?", true, false},
		{"https://relaypine.com", "https://admin.relaypine.com/#foo", true, false},
		{"https://relaypine.com", "https://admin.relaypine.com.", true, false},
		{"ftp://relaypine.com", "https://admin.relaypine.com", false, false},
	} {
		_, _, err := ValidateAppURLs(tc.public, tc.admin, tc.production)
		if (err == nil) != tc.valid {
			t.Errorf("origins %q %q accepted=%v want=%v", tc.public, tc.admin, err == nil, tc.valid)
		}
	}
}
func TestConsoleSurfaceUsesHostAndRejectsCrossSurfaceOrigin(t *testing.T) {
	s := &Server{AppURL: "https://relaypine.test", AdminAppURL: "https://admin.relaypine.test", Relay: http.NotFoundHandler()}
	h := s.Handler()
	for _, tc := range []struct {
		host, forwarded, path, origin, method string
		want                                  int
		surface                               string
	}{
		{"relaypine.test", "admin.relaypine.test", "/api/auth/config", "", "GET", 200, "customer"},
		{"admin.relaypine.test", "relaypine.test", "/api/auth/config", "", "GET", 200, "admin"},
		{"attacker.test", "admin.relaypine.test", "/api/auth/config", "", "GET", 404, ""},
		{"relaypine.test", "admin.relaypine.test", "/api/admin/credits", "https://relaypine.test", "POST", 404, ""},
		{"relaypine.test", "admin.relaypine.test", "/api/auth/code", "https://admin.relaypine.test", "POST", 403, ""},
		{"admin.relaypine.test", "relaypine.test", "/api/auth/code", "https://relaypine.test", "POST", 403, ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Host = tc.host
		r.Header.Set("X-Forwarded-Host", tc.forwarded)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("host=%s path=%s got=%d want=%d", tc.host, tc.path, w.Code, tc.want)
		}
		if tc.surface != "" && !strings.Contains(w.Body.String(), `"surface":"`+tc.surface+`"`) {
			t.Fatal("surface mismatch")
		}
	}
}
