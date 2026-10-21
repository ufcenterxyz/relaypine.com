package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

type MailgunSender struct {
	apiKey, domain, from, baseURL string
	client                        *http.Client
}

func NewMailgunSender(apiKey, domain, from, baseURL string) (*MailgunSender, error) {
	if baseURL == "" {
		baseURL = "https://api.mailgun.net"
	}
	if baseURL != "https://api.mailgun.net" && baseURL != "https://api.eu.mailgun.net" {
		return nil, errors.New("unsupported Mailgun API origin")
	}
	parsed, err := mail.ParseAddress(from)
	if apiKey == "" || domain == "" || strings.ContainsAny(domain, " /?#:@\\\r\n") || err != nil || parsed.Address == "" || strings.ContainsAny(from, "\r\n") {
		return nil, errors.New("complete Mailgun delivery configuration required")
	}
	return &MailgunSender{apiKey: apiKey, domain: domain, from: from, baseURL: baseURL, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (m *MailgunSender) SendCode(ctx context.Context, email, code string) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	fields := map[string]string{
		"from": m.from, "to": email, "subject": "Your RelayPine verification code",
		"text":       fmt.Sprintf("Your RelayPine verification code is: %s\n\nEnter this code on RelayPine to verify your email. It expires in 10 minutes and can only be used once.\n\nIf you did not request this code, you can ignore this email. Never share this code with anyone.", code),
		"o:tracking": "no", "o:require-tls": "yes",
	}
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			return errors.New("email request unavailable")
		}
	}
	if form.Close() != nil {
		return errors.New("email request unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/v3/"+url.PathEscape(m.domain)+"/messages", &body)
	if err != nil {
		return errors.New("email request unavailable")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.SetBasicAuth("api", m.apiKey)
	res, err := m.client.Do(req)
	if err != nil {
		return errors.New("email delivery unavailable")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("email delivery rejected")
	}
	return nil
}
