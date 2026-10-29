package store

import (
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"testing"

	"relaypine.com/gateway/internal/domain"
)

func TestPayloadIsCompressedAndBoundToItsRequest(t *testing.T) {
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	s := &Store{aead: aead}
	body := strings.Repeat(`{"role":"user","content":"a confidential prompt"},`, 2000)
	p := domain.RequestPayload{
		Request:  domain.CapturedRequest{Method: "POST", Path: "/anthropic/v1/messages", Headers: map[string]string{"user-agent": "sdk"}, Body: body, Bytes: len(body)},
		Response: domain.CapturedResponse{Status: 200, ContentType: "text/event-stream", Body: "data: {}\n\n", Bytes: 10},
		Upstream: &domain.CapturedResponse{Status: 429, Body: "slow down", Bytes: 9},
	}
	sealed, err := s.packPayload(p, "req_one")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "confidential") || len(sealed) >= len(body)/10 {
		t.Fatalf("payload not compressed and sealed: %d bytes", len(sealed))
	}
	opened, err := s.openPayload(sealed, "req_one")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Request.Body != body || opened.Request.Headers["user-agent"] != "sdk" || opened.Response != p.Response || *opened.Upstream != *p.Upstream {
		t.Fatalf("round trip changed payload: %+v", opened.Response)
	}
	if _, err = s.openPayload(sealed, "req_two"); err == nil {
		t.Fatal("payload opened as another request")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = s.openPayload(sealed, "req_one"); err == nil {
		t.Fatal("modified payload accepted")
	}
}
