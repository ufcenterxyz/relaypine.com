package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mailRoundTrip func(*http.Request) (*http.Response, error)

func (f mailRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMailgunDeliveryUsesAuthenticatedPrivateMessage(t *testing.T) {
	sender, err := NewMailgunSender("test-api-key", "mail.relaypine.test", "RelayPine <login@mail.relaypine.test>", "")
	if err != nil {
		t.Fatal(err)
	}
	sender.client.Transport = mailRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.mailgun.net/v3/mail.relaypine.test/messages" {
			t.Fatal("unexpected Mailgun endpoint")
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "api" || password != "test-api-key" {
			t.Fatal("missing provider authentication")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string]string{"from": "RelayPine <login@mail.relaypine.test>", "to": "verified@example.test", "o:tracking": "no", "o:require-tls": "yes"} {
			if r.FormValue(field) != want {
				t.Fatalf("incorrect field %s", field)
			}
		}
		if !strings.Contains(r.FormValue("text"), "012345") || !strings.Contains(r.FormValue("text"), "10 minutes") {
			t.Fatal("missing code or expiry")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"message":"Queued"}`))}, nil
	})
	if err = sender.SendCode(context.Background(), "verified@example.test", "012345"); err != nil {
		t.Fatal(err)
	}
	sender.client.Transport = mailRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("private provider error including secret"))}, nil
	})
	if err = sender.SendCode(context.Background(), "verified@example.test", "012345"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("provider error leaked or ignored")
	}
}
func TestMailgunConfigurationRejectsUntrustedOriginsAndHeaders(t *testing.T) {
	for _, cfg := range [][4]string{
		{"key", "domain.test", "login@domain.test", "http://api.mailgun.net"},
		{"key", "domain.test", "login@domain.test", "https://attacker.test"},
		{"key", "domain.test/escape", "login@domain.test", ""},
		{"key", "domain.test", "login@domain.test\r\nBcc:attacker@test.invalid", ""},
		{"", "domain.test", "login@domain.test", ""},
	} {
		if _, err := NewMailgunSender(cfg[0], cfg[1], cfg[2], cfg[3]); err == nil {
			t.Fatal("invalid sender configuration accepted")
		}
	}
	if _, err := NewMailgunSender("key", "domain.test", "login@domain.test", "https://api.eu.mailgun.net"); err != nil {
		t.Fatal("EU provider rejected")
	}
}
