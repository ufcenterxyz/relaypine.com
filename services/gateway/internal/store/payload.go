package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"relaypine.com/gateway/internal/domain"
	"relaypine.com/gateway/internal/objectstore"
)

// Captured bodies can hold prompts, images and model output. Each one is gzip
// compressed, then sealed with the master key and bound to its request ID, so
// a body copied onto another request cannot be opened as that request.
//
// With object storage configured, a body is uploaded after its response
// completes and MySQL keeps only an index row. A body that cannot be uploaded
// right away is kept in MySQL; MaintainPayloads moves it later, along with
// bodies stored before object storage was configured.
const payloadScope = "request_payload:"

const (
	// The relay bounds the captured text; these limits only guard the column
	// (MEDIUMBLOB) and decompression of a damaged body.
	maxSealedPayloadBytes = 16<<20 - 1
	maxPayloadJSONBytes   = 32 << 20
	// Bodies waiting for upload are held in memory up to this total. Further
	// bodies are written to MySQL instead.
	maxUploadBacklogBytes = 32 << 20
	payloadObjectPrefix   = "request-bodies/"
	// A finished request reports its body as pending this long, while the
	// upload may still be running.
	payloadSaveWindow = 2 * time.Minute
)

// Where a body lives.
const (
	payloadInDatabase = "database"
	payloadInObject   = "object"
)

// ObjectStore keeps sealed bodies outside MySQL. objectstore.Client
// implements it for S3-compatible storage.
type ObjectStore interface {
	Put(ctx context.Context, key string, body []byte) error
	Get(ctx context.Context, key string, limit int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// PayloadRetentionDays is zero when bodies are kept indefinitely.
func (s *Store) PayloadRetentionDays() int { return int(s.payloadRetention / (24 * time.Hour)) }

// SavePayload stores one request's captured exchange. It is a no-op when
// capture is off. With object storage it returns before the upload finishes.
func (s *Store) SavePayload(ctx context.Context, requestID string, p domain.RequestPayload) error {
	if !s.payloadCapture {
		return nil
	}
	if !validID(requestID) {
		return errors.New("invalid request ID")
	}
	sealed, err := s.packPayload(p, requestID)
	if err != nil {
		return err
	}
	created := time.Now().UTC()
	if s.objects != nil {
		if upload, ok := s.uploads.start(len(sealed)); ok {
			go s.uploadPayload(upload, requestID, created, sealed)
			return nil
		}
	}
	return s.keepPayload(ctx, requestID, created, sealed)
}

// uploadPayload stores a body in object storage, then indexes it. A body that
// cannot be uploaded is kept in MySQL to be moved later.
func (s *Store) uploadPayload(ctx context.Context, requestID string, created time.Time, sealed []byte) {
	defer s.uploads.done(len(sealed))
	key := payloadObjectKey(created, requestID)
	put, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := s.objects.Put(put, key, sealed)
	cancel()
	if err == nil {
		index, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = s.DB.ExecContext(index, "INSERT INTO request_payloads(request_id,created_at,location,object_key) VALUES(?,?,?,?)", requestID, created, payloadInObject, key)
		cancel()
		if err == nil {
			return
		}
	} else {
		slog.Warn("Request body upload failed; keeping it in MySQL", "request_id", requestID, "error", err)
	}
	keep, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = s.keepPayload(keep, requestID, created, sealed); err != nil {
		slog.Warn("Request payload was not stored", "request_id", requestID)
	}
}

func (s *Store) keepPayload(ctx context.Context, requestID string, created time.Time, sealed []byte) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO request_payloads(request_id,created_at,location,payload) VALUES(?,?,?,?)", requestID, created, payloadInDatabase, sealed)
	return dbError(err)
}

// Keys are grouped by capture date. They are never shown to clients.
func payloadObjectKey(created time.Time, requestID string) string {
	return payloadObjectPrefix + created.UTC().Format("2006/01/02/") + requestID
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
	query := "SELECT " + requestColumns + ",r.updated_at,p.created_at,p.location,p.object_key,p.payload FROM requests r LEFT JOIN request_payloads p ON p.request_id=r.id WHERE r.id=?"
	args := []any{id}
	if !user.Operator {
		query += " AND r.tenant_id=?"
		args = append(args, user.TenantID)
	}
	var updated time.Time
	var stored sql.NullTime
	var location, key sql.NullString
	var sealed []byte
	request, err := scanRequest(s.DB.QueryRowContext(ctx, query, args...), user, &updated, &stored, &location, &key, &sealed)
	if err != nil {
		return detail, dbError(err)
	}
	if user.Operator {
		trail := []domain.Request{request}
		if err = s.attachAttempts(ctx, trail); err != nil {
			return detail, err
		}
		request = trail[0]
	}
	detail.Request = request
	now := time.Now().UTC()
	expired := func(at time.Time) bool { return s.payloadRetention > 0 && at.Before(now.Add(-s.payloadRetention)) }
	switch {
	case !stored.Valid && expired(request.CreatedAt):
		detail.PayloadStatus = domain.PayloadExpired
	case !stored.Valid && s.payloadCapture && (request.Status == "in_progress" || now.Sub(updated) < payloadSaveWindow):
		detail.PayloadStatus = domain.PayloadPending
	case !stored.Valid && !s.payloadCapture:
		detail.PayloadStatus = domain.PayloadDisabled
	case !stored.Valid:
		detail.PayloadStatus = domain.PayloadNotRecorded
	// The retention period applies exactly, even before cleanup removes the body.
	case expired(stored.Time):
		detail.PayloadStatus = domain.PayloadExpired
	default:
		detail.Payload, detail.PayloadStatus = s.loadPayload(ctx, request.ID, location.String, key.String, sealed)
		if detail.Payload != nil && !user.Operator {
			detail.Payload.Upstream = nil
		}
	}
	return detail, nil
}

// loadPayload reads and opens a stored body. A body that cannot be read now,
// such as one sealed under an earlier master key, is reported unavailable
// instead of hiding the request metadata.
func (s *Store) loadPayload(ctx context.Context, requestID, location, key string, sealed []byte) (*domain.RequestPayload, string) {
	if location == payloadInObject {
		if s.objects == nil {
			return nil, domain.PayloadUnavailable
		}
		// Someone is waiting on the request details; report unavailable rather
		// than hold the dialog on a slow storage service.
		read, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		if sealed, err = s.objects.Get(read, key, maxSealedPayloadBytes); err != nil {
			if errors.Is(err, objectstore.ErrArchived) {
				return nil, domain.PayloadArchived
			}
			if ctx.Err() == nil {
				slog.Warn("Request body could not be read from object storage", "request_id", requestID, "error", err)
			}
			return nil, domain.PayloadUnavailable
		}
	}
	payload, err := s.openPayload(sealed, requestID)
	if err != nil {
		return nil, domain.PayloadUnavailable
	}
	return &payload, domain.PayloadStored
}

// MaintainPayloads deletes bodies past the retention period, then moves bodies
// held in MySQL to object storage, oldest first. One replica works at a time;
// running out of time is not an error, and the next run continues.
func (s *Store) MaintainPayloads(ctx context.Context) error {
	if s.payloadRetention <= 0 && s.objects == nil {
		return nil
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('relaypine_request_bodies',0)").Scan(&locked); err != nil {
		return err
	}
	if locked.Int64 != 1 {
		return nil
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('relaypine_request_bodies')")
	if err = s.prunePayloads(ctx); err == nil {
		err = s.offloadPayloads(ctx)
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

// prunePayloads deletes expired bodies in bounded batches. An object is
// deleted before its index row, so an interrupted run leaves a row to retry
// rather than an orphaned object. Without object storage configured, rows for
// stored objects wait until it is configured again.
func (s *Store) prunePayloads(ctx context.Context) error {
	if s.payloadRetention <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-s.payloadRetention)
	if s.objects != nil {
		expired, err := s.payloadRefs(ctx, func(rows *sql.Rows, ref *payloadRef) error { return rows.Scan(&ref.id, &ref.key) },
			"SELECT request_id,object_key FROM request_payloads WHERE location=? AND created_at<? ORDER BY created_at LIMIT 200", payloadInObject, cutoff)
		if err != nil {
			return err
		}
		for _, row := range expired {
			if err = s.objects.Delete(ctx, row.key); err != nil {
				return err
			}
			if _, err = s.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE request_id=? AND location=?", row.id, payloadInObject); err != nil {
				return err
			}
		}
	}
	_, err := s.DB.ExecContext(ctx, "DELETE FROM request_payloads WHERE location=? AND created_at<? ORDER BY created_at LIMIT 1000", payloadInDatabase, cutoff)
	return err
}

// offloadPayloads moves bodies held in MySQL to object storage: bodies saved
// before object storage was configured and bodies whose upload failed. Bodies
// are read one at a time to bound memory.
func (s *Store) offloadPayloads(ctx context.Context) error {
	if s.objects == nil {
		return nil
	}
	const batch = 100
	for {
		held, err := s.payloadRefs(ctx, func(rows *sql.Rows, ref *payloadRef) error { return rows.Scan(&ref.id, &ref.created) },
			"SELECT request_id,created_at FROM request_payloads WHERE location=? ORDER BY created_at LIMIT 100", payloadInDatabase)
		if err != nil {
			return err
		}
		for _, row := range held {
			if err = s.offloadPayload(ctx, row.id, row.created); err != nil {
				return err
			}
		}
		if len(held) < batch {
			return nil
		}
	}
}

func (s *Store) offloadPayload(ctx context.Context, requestID string, created time.Time) error {
	var sealed []byte
	err := s.DB.QueryRowContext(ctx, "SELECT payload FROM request_payloads WHERE request_id=? AND location=?", requestID, payloadInDatabase).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	key := payloadObjectKey(created, requestID)
	if err = s.objects.Put(ctx, key, sealed); err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "UPDATE request_payloads SET location=?,object_key=?,payload=NULL WHERE request_id=? AND location=?", payloadInObject, key, requestID, payloadInDatabase)
	return err
}

type payloadRef struct {
	id, key string
	created time.Time
}

func (s *Store) payloadRefs(ctx context.Context, scan func(*sql.Rows, *payloadRef) error, query string, args ...any) ([]payloadRef, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []payloadRef
	for rows.Next() {
		var ref payloadRef
		if err = scan(rows, &ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// DrainPayloads waits for background uploads at shutdown; bodies saved after
// it starts go to MySQL directly. When ctx ends first, the remaining uploads
// stop and their bodies are kept in MySQL.
func (s *Store) DrainPayloads(ctx context.Context) {
	s.uploads.close()
	if s.uploads.wait(ctx) {
		return
	}
	s.uploads.abort()
	fallback, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	s.uploads.wait(fallback)
}

// payloadUploads tracks bodies uploading in the background.
type payloadUploads struct {
	mu      sync.Mutex
	bytes   int
	closed  bool
	running sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// start reserves memory for one upload. It reports false when the backlog is
// full or the store is shutting down; the caller then writes to MySQL.
func (u *payloadUploads) start(size int) (context.Context, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.bytes+size > maxUploadBacklogBytes {
		return nil, false
	}
	if u.ctx == nil {
		u.ctx, u.cancel = context.WithCancel(context.Background())
	}
	u.bytes += size
	u.running.Add(1)
	return u.ctx, true
}

func (u *payloadUploads) done(size int) {
	u.mu.Lock()
	u.bytes -= size
	u.mu.Unlock()
	u.running.Done()
}

func (u *payloadUploads) close() {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
}

// abort stops uploads still waiting on object storage.
func (u *payloadUploads) abort() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cancel != nil {
		u.cancel()
	}
}

func (u *payloadUploads) wait(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		u.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
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
