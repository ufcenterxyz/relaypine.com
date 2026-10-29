package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"time"

	"relaypine.com/gateway/internal/domain"
)

// Captured bodies can hold prompts, images and model output. They are gzip
// compressed, then sealed with the master key and bound to their request ID, so
// a row copied onto another request cannot be opened as that request.
const payloadScope = "request_payload:"

// The relay bounds the captured text; these limits only guard the column
// (MEDIUMBLOB) and decompression of a damaged row.
const (
	maxSealedPayloadBytes = 16<<20 - 1
	maxPayloadJSONBytes   = 32 << 20
)

func (s *Store) PayloadRetentionDays() int { return int(s.payloadRetention / (24 * time.Hour)) }

// SavePayload stores one request's captured exchange. It is a no-op when body
// capture is disabled.
func (s *Store) SavePayload(ctx context.Context, requestID string, p domain.RequestPayload) error {
	if s.payloadRetention <= 0 {
		return nil
	}
	if !validID(requestID) {
		return errors.New("invalid request ID")
	}
	sealed, err := s.packPayload(p, requestID)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "INSERT INTO request_payloads(request_id,created_at,payload) VALUES(?,?,?)", requestID, time.Now().UTC(), sealed)
	return dbError(err)
}

// RequestDetail returns one request and its captured exchange. Customers can
// read only their own workspace and never receive the raw upstream reply.
func (s *Store) RequestDetail(ctx context.Context, user domain.User, id string) (domain.RequestDetail, error) {
	detail := domain.RequestDetail{RetentionDays: s.PayloadRetentionDays()}
	if user.ID == "" || user.TenantID == "" || (user.Operator && !s.IsSuperAdmin(user)) {
		return detail, ErrUnauthorized
	}
	if !validID(id) {
		return detail, ErrNotFound
	}
	// The join condition enforces retention exactly, even before pruning runs.
	query := "SELECT " + requestColumns + ",p.payload FROM requests r LEFT JOIN request_payloads p ON p.request_id=r.id AND p.created_at>=? WHERE r.id=?"
	args := []any{time.Now().UTC().Add(-s.payloadRetention), id}
	if !user.Operator {
		query += " AND r.tenant_id=?"
		args = append(args, user.TenantID)
	}
	var sealed []byte
	request, err := scanRequest(s.DB.QueryRowContext(ctx, query, args...), user, &sealed)
	if err != nil {
		return detail, dbError(err)
	}
	detail.Request = request
	if user.Operator {
		trail := []domain.Request{request}
		if err = s.attachAttempts(ctx, trail); err != nil {
			return detail, err
		}
		detail.Request = trail[0]
	}
	if len(sealed) == 0 || s.payloadRetention <= 0 {
		return detail, nil
	}
	// An unreadable row, such as one sealed under an earlier master key, is
	// reported as unavailable instead of hiding the request metadata.
	payload, err := s.openPayload(sealed, request.ID)
	if err != nil {
		return detail, nil
	}
	if !user.Operator {
		payload.Upstream = nil
	}
	detail.Payload = &payload
	return detail, nil
}

// PrunePayloads deletes expired bodies in bounded batches. With zero
// retention, every stored body has expired.
func (s *Store) PrunePayloads(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-s.payloadRetention)
	_, err := s.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE created_at<? ORDER BY created_at LIMIT 1000", cutoff)
	return err
}

func (s *Store) packPayload(p domain.RequestPayload, requestID string) ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var packed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&packed, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err = zw.Write(raw); err != nil {
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	size := s.aead.NonceSize()
	nonce := make([]byte, size, size+packed.Len()+s.aead.Overhead())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := s.aead.Seal(nonce, nonce, packed.Bytes(), []byte(payloadScope+requestID))
	if len(sealed) > maxSealedPayloadBytes {
		return nil, errors.New("request payload exceeds storage limit")
	}
	return sealed, nil
}

func (s *Store) openPayload(sealed []byte, requestID string) (domain.RequestPayload, error) {
	var p domain.RequestPayload
	size := s.aead.NonceSize()
	if len(sealed) < size {
		return p, errors.New("invalid stored payload")
	}
	packed, err := s.aead.Open(nil, sealed[:size], sealed[size:], []byte(payloadScope+requestID))
	if err != nil {
		return p, errors.New("stored payload decryption failed")
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return p, errors.New("invalid stored payload")
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxPayloadJSONBytes+1))
	if err != nil || len(raw) > maxPayloadJSONBytes {
		return p, errors.New("invalid stored payload")
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("invalid stored payload")
	}
	return p, nil
}
