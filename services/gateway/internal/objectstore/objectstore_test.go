package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Examples published in the Amazon S3 Signature Version 4 documentation
// ("Signature Calculations for the Authorization Header: Transferring Payload
// in a Single Chunk").
func TestSignatureMatchesPublishedExamples(t *testing.T) {
	c := &Client{region: "us-east-1", accessKeyID: "AKIAIOSFODNN7EXAMPLE", secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	welcome := sha256.Sum256([]byte("Welcome to Amazon S3."))
	cases := []struct {
		name string
		r    signedRequest
		want string
	}{
		{"get object", signedRequest{method: "GET", path: "/test.txt", payloadHash: empty, headers: map[string]string{"host": "examplebucket.s3.amazonaws.com", "range": "bytes=0-9", "x-amz-content-sha256": empty, "x-amz-date": "20130524T000000Z"}},
			"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"put object", signedRequest{method: "PUT", path: "/" + escapePath("test$file.text"), payloadHash: hex.EncodeToString(welcome[:]), headers: map[string]string{"date": "Fri, 24 May 2013 00:00:00 GMT", "host": "examplebucket.s3.amazonaws.com", "x-amz-content-sha256": hex.EncodeToString(welcome[:]), "x-amz-date": "20130524T000000Z", "x-amz-storage-class": "REDUCED_REDUNDANCY"}},
			"SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"get bucket lifecycle", signedRequest{method: "GET", path: "/", query: "lifecycle=", payloadHash: empty, headers: map[string]string{"host": "examplebucket.s3.amazonaws.com", "x-amz-content-sha256": empty, "x-amz-date": "20130524T000000Z"}},
			"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"list objects", signedRequest{method: "GET", path: "/", query: "max-keys=2&prefix=J", payloadHash: empty, headers: map[string]string{"host": "examplebucket.s3.amazonaws.com", "x-amz-content-sha256": empty, "x-amz-date": "20130524T000000Z"}},
			"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, tc := range cases {
		tc.r.time = at
		got := c.authorization(tc.r)
		want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " + tc.want
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
	}
}

func TestEscapePath(t *testing.T) {
	for in, want := range map[string]string{
		"request-bodies/2026/10/29/req_0a9F": "request-bodies/2026/10/29/req_0a9F",
		"test$file.text":                     "test%24file.text",
		"a b/ü+~":                            "a%20b/%C3%BC%2B~",
	} {
		if got := escapePath(in); got != want {
			t.Errorf("escapePath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	valid := Config{Endpoint: "https://cos.ap-singapore.myqcloud.com", Region: "ap-singapore", Bucket: "relaypine-bodies-1250000000", AccessKeyID: "id", SecretAccessKey: "secret"}
	if _, err := New(valid); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"endpoint path":   func(c *Config) { c.Endpoint = "https://cos.ap-singapore.myqcloud.com/bucket" },
		"endpoint scheme": func(c *Config) { c.Endpoint = "ftp://cos.ap-singapore.myqcloud.com" },
		"credentials":     func(c *Config) { c.Endpoint = "https://id:secret@cos.example.test" },
		"region":          func(c *Config) { c.Region = " " },
		"secret":          func(c *Config) { c.SecretAccessKey = "" },
		"bucket":          func(c *Config) { c.Bucket = "Relaypine_Bodies" },
	} {
		cfg := valid
		change(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted %+v", name, cfg)
		}
	}
}

// fakeS3 checks each request's signature inputs and keeps objects in memory.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	hosts    []string
	failures map[string]int // remaining 503 replies per method
	archived map[string]bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	sum := sha256.Sum256(body)
	auth := r.Header.Get("Authorization")
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=id/") || !strings.Contains(auth, "/ap-singapore/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=") || len(r.Header.Get("X-Amz-Date")) != 16 {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code></Error>`)
		return
	}
	f.hosts = append(f.hosts, r.Host)
	if f.failures[r.Method] > 0 {
		f.failures[r.Method]--
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `<Error><Code>SlowDown</Code><Message>Reduce your request rate.</Message></Error>`)
		return
	}
	key := r.URL.EscapedPath()
	switch r.Method {
	case http.MethodPut:
		f.objects[key] = body
	case http.MethodGet:
		if f.archived[key] {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `<Error><Code>InvalidObjectState</Code><Message>The operation is not valid for the object's storage class</Message></Error>`)
			return
		}
		object, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		w.Write(object)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

// testClient sends every request to server, whatever host it addresses.
func testClient(t *testing.T, server *httptest.Server, pathStyle bool) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: "http://cos.example.test", Region: "ap-singapore", Bucket: "bodies-1250000000", AccessKeyID: "id", SecretAccessKey: "secret", PathStyle: pathStyle})
	if err != nil {
		t.Fatal(err)
	}
	transport := c.http.Transport.(*http.Transport)
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	c.backoff = time.Millisecond
	return c
}

func TestClientStoresReadsAndDeletesObjects(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}, failures: map[string]int{}, archived: map[string]bool{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	ctx := context.Background()
	for _, pathStyle := range []bool{false, true} {
		c := testClient(t, server, pathStyle)
		key := "request-bodies/2026/10/29/req_1"
		sealed := []byte("sealed\x00body")
		fake.failures[http.MethodPut] = 2 // throttled twice, then accepted
		if err := c.Put(ctx, key, sealed); err != nil {
			t.Fatal(err)
		}
		stored := "/" + key
		wantHost := "bodies-1250000000.cos.example.test"
		if pathStyle {
			stored, wantHost = "/bodies-1250000000/"+key, "cos.example.test"
		}
		if !bytes.Equal(fake.objects[stored], sealed) || fake.hosts[len(fake.hosts)-1] != wantHost {
			t.Fatalf("path style %v stored %v via %s", pathStyle, fake.objects, fake.hosts[len(fake.hosts)-1])
		}
		got, err := c.Get(ctx, key, 64)
		if err != nil || !bytes.Equal(got, sealed) {
			t.Fatalf("get %q %v", got, err)
		}
		if _, err = c.Get(ctx, key, int64(len(sealed)-1)); err == nil {
			t.Fatal("object above the read limit accepted")
		}
		if err = c.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Get(ctx, key, 64); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted object: %v", err)
		}
		if err = c.Delete(ctx, key); err != nil {
			t.Fatalf("deleting a missing object: %v", err)
		}
		fake.archived[stored] = true
		if _, err = c.Get(ctx, key, 64); !errors.Is(err, ErrArchived) {
			t.Fatalf("archived object: %v", err)
		}
	}
}

func TestClientGivesUpOnPersistentFailures(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}, failures: map[string]int{http.MethodPut: 10}, archived: map[string]bool{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testClient(t, server, false)
	err := c.Put(context.Background(), "request-bodies/x", []byte("body"))
	var refused *Error
	if !errors.As(err, &refused) || refused.Status != 503 || refused.Code != "SlowDown" || fake.failures[http.MethodPut] != 10-maxAttempts {
		t.Fatalf("error %v after %d attempts", err, 10-fake.failures[http.MethodPut])
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.test/", http.StatusMovedPermanently)
	}))
	defer redirect.Close()
	if err = testClient(t, redirect, false).Put(context.Background(), "request-bodies/x", []byte("body")); !errors.As(err, &refused) || refused.Status != 301 {
		t.Fatalf("redirect followed or accepted: %v", err)
	}
}

// With OBJECT_STORAGE_TEST_* set, this exercises a real S3-compatible service,
// such as MinIO in CI or a COS test bucket.
func TestS3CompatibleServiceRoundTrip(t *testing.T) {
	endpoint := os.Getenv("OBJECT_STORAGE_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("OBJECT_STORAGE_TEST_ENDPOINT not set; object storage service test skipped")
	}
	c, err := New(Config{Endpoint: endpoint, Region: os.Getenv("OBJECT_STORAGE_TEST_REGION"), Bucket: os.Getenv("OBJECT_STORAGE_TEST_BUCKET"), AccessKeyID: os.Getenv("OBJECT_STORAGE_TEST_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("OBJECT_STORAGE_TEST_SECRET_ACCESS_KEY"), PathStyle: os.Getenv("OBJECT_STORAGE_TEST_PATH_STYLE") == "true"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := "request-bodies/test/" + time.Now().UTC().Format("20060102T150405.000000000") + "/req_round+trip ü"
	body := bytes.Repeat([]byte{0, 1, 2, 250}, 300000)
	if err = c.Put(ctx, key, body); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, key, int64(len(body)))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read %d bytes: %v", len(got), err)
	}
	if err = c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, key, int64(len(body))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted object: %v", err)
	}
}
