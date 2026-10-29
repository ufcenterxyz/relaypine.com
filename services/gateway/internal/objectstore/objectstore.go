// Package objectstore is a small client for S3-compatible object storage such
// as Tencent COS, Amazon S3, Cloudflare R2 and MinIO. It covers what the
// gateway needs: whole-object PUT, GET and DELETE of objects up to a few
// megabytes. Requests use AWS Signature Version 4 with a signed payload hash,
// the form every S3-compatible service accepts.
package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("object not found")
	// ErrArchived means the object is in an archive storage class and must be
	// restored before it can be read.
	ErrArchived = errors.New("object is archived")
)

// Error is a request the service refused, with its S3 error code when it sent
// one. Messages never include credentials.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	text := fmt.Sprintf("object storage returned %d", e.Status)
	if e.Code != "" {
		text += " " + e.Code
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

type Config struct {
	// Endpoint is the service origin, such as https://cos.ap-singapore.myqcloud.com.
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle addresses the bucket in the URL path instead of the host name.
	// MinIO usually needs it; COS, S3 and R2 accept the default host style.
	PathStyle bool
}

type Client struct {
	scheme, host, bucket, region string
	pathStyle                    bool
	accessKeyID, secretKey       string
	http                         *http.Client
	now                          func() time.Time
	// backoff is the first retry delay; each later retry waits four times longer.
	backoff time.Duration
}

const maxAttempts = 3

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("object storage endpoint must be an http or https origin")
	}
	if strings.TrimSpace(cfg.Region) == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("object storage region and access keys are required")
	}
	if !bucketName.MatchString(cfg.Bucket) {
		return nil, errors.New("object storage bucket name is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		scheme: u.Scheme, host: strings.ToLower(u.Host), bucket: cfg.Bucket, region: strings.TrimSpace(cfg.Region), pathStyle: cfg.PathStyle,
		accessKeyID: cfg.AccessKeyID, secretKey: cfg.SecretAccessKey,
		// A redirect means a wrong endpoint or region; signed requests are never resent elsewhere.
		http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:  time.Now, backoff: 200 * time.Millisecond,
	}, nil
}

// Bucket names the bucket for logs.
func (c *Client) Bucket() string { return c.bucket }

// Put writes the whole object, replacing any object with the same key.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	resp, err := c.send(ctx, http.MethodPut, key, body)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	return nil
}

// Get reads an object of at most limit bytes.
func (c *Client) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	resp, err := c.send(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > limit {
			return nil, errors.New("object exceeds the read limit")
		}
		return body, nil
	case http.StatusNotFound:
		return nil, ErrNotFound
	}
	e := responseError(resp)
	var refused *Error
	if errors.As(e, &refused) && refused.Code == "InvalidObjectState" {
		return nil, ErrArchived
	}
	return nil, e
}

// Delete removes an object. A missing object is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.send(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return responseError(resp)
}

// send retries network errors, throttling and server errors. Every operation
// here is idempotent, and each attempt is signed afresh.
func (c *Client) send(ctx context.Context, method, key string, body []byte) (*http.Response, error) {
	if key == "" || len(key) > 1024 || strings.HasPrefix(key, "/") {
		return nil, errors.New("invalid object key")
	}
	var last error
	for attempt := range maxAttempts {
		if attempt > 0 {
			delay := c.backoff << (2 * (attempt - 1))
			delay += time.Duration(rand.Int64N(int64(delay)/2 + 1))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := c.request(ctx, method, key, body)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			last = responseError(resp)
			closeBody(resp)
			continue
		}
		return resp, nil
	}
	return nil, last
}

func (c *Client) request(ctx context.Context, method, key string, body []byte) (*http.Request, error) {
	host, path := c.bucket+"."+c.host, "/"+escapePath(key)
	if c.pathStyle {
		host, path = c.host, "/"+c.bucket+path
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.scheme+"://"+host+path, reader)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	now := c.now().UTC()
	date := now.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", date)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("Authorization", c.authorization(signedRequest{
		method: method, path: path, payloadHash: payloadHash, time: now,
		headers: map[string]string{"host": host, "x-amz-content-sha256": payloadHash, "x-amz-date": date},
	}))
	return req, nil
}

const amzDateFormat = "20060102T150405Z"

type signedRequest struct {
	method, path, query string
	// headers are the signed headers, keyed by lower-case name.
	headers     map[string]string
	payloadHash string
	time        time.Time
}

// authorization returns the Signature Version 4 Authorization header value.
func (c *Client) authorization(r signedRequest) string {
	names := make([]string, 0, len(r.headers))
	for name := range r.headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name + ":" + strings.Join(strings.Fields(r.headers[name]), " ") + "\n")
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{r.method, r.path, r.query, canonical.String(), signed, r.payloadHash}, "\n")
	date := r.time.UTC().Format(amzDateFormat)
	scope := date[:8] + "/" + c.region + "/s3/aws4_request"
	requestHash := sha256.Sum256([]byte(request))
	key := []byte("AWS4" + c.secretKey)
	for _, part := range []string{date[:8], c.region, "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	signature := hex.EncodeToString(hmacSHA256(key, "AWS4-HMAC-SHA256\n"+date+"\n"+scope+"\n"+hex.EncodeToString(requestHash[:])))
	return "AWS4-HMAC-SHA256 Credential=" + c.accessKeyID + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// escapePath encodes an object key as S3 signs it: every byte except
// unreserved characters and "/" is percent-encoded.
func escapePath(key string) string {
	var b strings.Builder
	for i := 0; i < len(key); i++ {
		ch := key[i]
		if 'A' <= ch && ch <= 'Z' || 'a' <= ch && ch <= 'z' || '0' <= ch && ch <= '9' || strings.IndexByte("-_.~/", ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func responseError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(raw, &body)
	message := body.Message
	if len(message) > 200 {
		message = message[:200]
	}
	return &Error{Status: resp.StatusCode, Code: body.Code, Message: message}
}

// closeBody drains a little of the body so the connection can be reused.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}
