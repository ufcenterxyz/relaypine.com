package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/objectstore"
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

// memoryObjects is object storage held in memory. Put waits on block when set.
type memoryObjects struct {
	mu              sync.Mutex
	objects         map[string][]byte
	putErr, getErr  error
	block           chan struct{}
	puts, deletions int
}

func (m *memoryObjects) Put(ctx context.Context, key string, body []byte) error {
	m.mu.Lock()
	block, err := m.block, m.putErr
	m.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), body...)
	m.puts++
	return nil
}

func (m *memoryObjects) Get(_ context.Context, key string, limit int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	body, ok := m.objects[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	if int64(len(body)) > limit {
		return nil, errors.New("object exceeds the read limit")
	}
	return append([]byte(nil), body...), nil
}

func (m *memoryObjects) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	m.deletions++
	return nil
}

func (m *memoryObjects) set(change func(*memoryObjects)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m)
}

func TestMySQLPayloadsMoveToObjectStorage(t *testing.T) {
	f := integrationFixture(t)
	s := f.s
	s.payloadCapture = true
	if err := s.Credit(f.ctx, f.user.TenantID, 1000, "test:payload-storage", "Payload storage test"); err != nil {
		t.Fatal(err)
	}
	payload := func(text string) domain.RequestPayload {
		body := `{"messages":[{"role":"user","content":"` + text + `"}]}`
		return domain.RequestPayload{Request: domain.CapturedRequest{Method: "POST", Path: "/openai/v1/chat/completions", Body: body, Bytes: len(body)}, Response: domain.CapturedResponse{Status: 200, Body: "{}", Bytes: 2}}
	}
	request := func() string {
		t.Helper()
		r := f.reservation(10, 5)
		if err := s.Reserve(f.ctx, r); err != nil {
			t.Fatal(err)
		}
		return r.Request.ID
	}
	type row struct {
		location string
		key      sql.NullString
		blob     []byte
		created  time.Time
	}
	stored := func(id string) row {
		t.Helper()
		var r row
		if err := s.DB.QueryRowContext(f.ctx, "SELECT location,object_key,payload,created_at FROM request_payloads WHERE request_id=?", id).Scan(&r.location, &r.key, &r.blob, &r.created); err != nil {
			t.Fatal(err)
		}
		return r
	}
	status := func(id string) (string, string) {
		t.Helper()
		detail, err := s.RequestDetail(f.ctx, f.user, id)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Payload == nil {
			return detail.PayloadStatus, ""
		}
		return detail.PayloadStatus, detail.Payload.Request.Body
	}

	// Without object storage, bodies are kept in MySQL.
	early := request()
	if err := s.SavePayload(f.ctx, early, payload("before object storage")); err != nil {
		t.Fatal(err)
	}
	if r := stored(early); r.location != payloadInDatabase || r.key.Valid || len(r.blob) == 0 {
		t.Fatalf("body kept as %+v", r)
	}

	// Configuring object storage moves bodies kept in MySQL, leaving index rows.
	objects := &memoryObjects{objects: map[string][]byte{}}
	s.objects = objects
	if err := s.MaintainPayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	moved := stored(early)
	if moved.location != payloadInObject || moved.blob != nil || moved.key.String != payloadObjectKey(moved.created, early) || len(objects.objects[moved.key.String]) == 0 {
		t.Fatalf("body not moved: %+v", moved)
	}
	if got, body := status(early); got != domain.PayloadStored || !strings.Contains(body, "before object storage") {
		t.Fatalf("moved body reads %s %q", got, body)
	}
	if bytes.Contains(objects.objects[moved.key.String], []byte("before object storage")) {
		t.Fatal("object stored in plaintext")
	}

	// New bodies are uploaded directly; MySQL keeps only the index row.
	direct := request()
	if err := s.SavePayload(f.ctx, direct, payload("uploaded directly")); err != nil {
		t.Fatal(err)
	}
	s.uploads.wait(f.ctx)
	if r := stored(direct); r.location != payloadInObject || r.blob != nil || !strings.HasPrefix(r.key.String, "request-bodies/"+r.created.UTC().Format("2006/01/02/")) {
		t.Fatalf("direct upload indexed as %+v", r)
	}
	if got, body := status(direct); got != domain.PayloadStored || !strings.Contains(body, "uploaded directly") {
		t.Fatalf("direct body reads %s %q", got, body)
	}

	// A failed upload keeps the body in MySQL until the next move.
	failed := request()
	objects.set(func(m *memoryObjects) { m.putErr = errors.New("service unavailable") })
	if err := s.SavePayload(f.ctx, failed, payload("upload failed")); err != nil {
		t.Fatal(err)
	}
	s.uploads.wait(f.ctx)
	if r := stored(failed); r.location != payloadInDatabase || len(r.blob) == 0 {
		t.Fatalf("failed upload kept as %+v", r)
	}
	if got, _ := status(failed); got != domain.PayloadStored {
		t.Fatalf("failed upload reads %s", got)
	}
	if err := s.MaintainPayloads(f.ctx); err == nil {
		t.Fatal("move reported success while storage fails")
	}
	objects.set(func(m *memoryObjects) { m.putErr = nil })
	if err := s.MaintainPayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	if r := stored(failed); r.location != payloadInObject {
		t.Fatalf("failed upload not moved: %+v", r)
	}

	// A full upload backlog writes to MySQL instead of holding more memory.
	s.uploads.mu.Lock()
	s.uploads.bytes = maxUploadBacklogBytes
	s.uploads.mu.Unlock()
	busy := request()
	if err := s.SavePayload(f.ctx, busy, payload("backlog full")); err != nil {
		t.Fatal(err)
	}
	if r := stored(busy); r.location != payloadInDatabase {
		t.Fatalf("backlogged body kept as %+v", r)
	}
	s.uploads.mu.Lock()
	s.uploads.bytes = 0
	s.uploads.mu.Unlock()

	// Bodies in an archive class, or that storage cannot return, are reported.
	objects.set(func(m *memoryObjects) { m.getErr = objectstore.ErrArchived })
	if got, _ := status(direct); got != domain.PayloadArchived {
		t.Fatalf("archived body reads %s", got)
	}
	objects.set(func(m *memoryObjects) { m.getErr = errors.New("timeout") })
	if got, _ := status(direct); got != domain.PayloadUnavailable {
		t.Fatalf("unreadable body reads %s", got)
	}
	objects.set(func(m *memoryObjects) { m.getErr = nil })

	// Shutdown stops slow uploads and keeps their bodies in MySQL.
	slow := request()
	objects.set(func(m *memoryObjects) { m.block = make(chan struct{}) })
	if err := s.SavePayload(f.ctx, slow, payload("slow upload")); err != nil {
		t.Fatal(err)
	}
	drain, cancel := context.WithTimeout(f.ctx, 50*time.Millisecond)
	s.DrainPayloads(drain)
	cancel()
	if r := stored(slow); r.location != payloadInDatabase || len(r.blob) == 0 {
		t.Fatalf("interrupted upload kept as %+v", r)
	}
	late := request()
	if err := s.SavePayload(f.ctx, late, payload("after shutdown began")); err != nil {
		t.Fatal(err)
	}
	if r := stored(late); r.location != payloadInDatabase {
		t.Fatalf("body saved during shutdown kept as %+v", r)
	}
	objects.set(func(m *memoryObjects) { m.block = nil })

	// With a retention period, expired objects are deleted with their rows.
	s.payloadRetention = 7 * 24 * time.Hour
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	for _, id := range []string{early, busy} {
		if _, err := s.DB.ExecContext(f.ctx, "UPDATE request_payloads SET created_at=? WHERE request_id=?", old, id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := status(early); got != domain.PayloadExpired {
		t.Fatalf("expired body reads %s", got)
	}
	deletions := objects.deletions
	if err := s.MaintainPayloads(f.ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM request_payloads WHERE request_id IN (?,?)", early, busy).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if _, kept := objects.objects[moved.key.String]; left != 0 || kept || objects.deletions != deletions+1 {
		t.Fatalf("expired bodies left %d rows, object kept %v", left, kept)
	}
	if got, body := status(direct); got != domain.PayloadStored || body == "" {
		t.Fatalf("current body reads %s", got)
	}
}
