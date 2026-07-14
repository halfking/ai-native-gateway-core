//go:build storage_s3

package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3Fixture records every method+path the SDK issues so tests can assert
// the protocol contract. request_count lets us write cheap invariants.
type s3Fixture struct {
	server       *httptest.Server
	store        map[string][]byte
	reqs         []string
	requestCount int32
}

func newS3Fixture(t *testing.T) (*s3Fixture, *S3StorageBackend) {
	t.Helper()
	f := &s3Fixture{
		store: map[string][]byte{},
	}

	mux := http.NewServeMux()
	// S3 PathStyle URL: http://host/BUCKET/KEY for path-style,
	// http://BUCKET.host/KEY for virtual-hosted. We use path-style.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.requestCount, 1)
		f.reqs = append(f.reqs, r.Method+"\t"+r.URL.Path)

		// URL.Path for path-style: "/bucket/key"
		pathParts := strings.SplitN(strings.TrimLeft(r.URL.Path, "/"), "/", 2)
		if len(pathParts) < 2 || pathParts[0] != "test-bucket" {
			// HEAD bucket (no key) returns 200 here for HealthCheck.
			if r.Method == http.MethodHead && r.URL.Path == "/test-bucket" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
			return
		}
		key := pathParts[1]

		switch r.Method {
		case http.MethodPut:
			// Read entire body; io.ReadAll handles short reads + EOF.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			f.store[key] = body
			w.Header().Set("ETag", `"fixture-etag"`)
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			data, ok := f.store[key]
			if !ok {
				http.Error(w, "NotFound", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		case http.MethodHead:
			data, ok := f.store[key]
			if !ok {
				http.Error(w, "NotFound", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("ETag", `"fixture-etag"`)
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			delete(f.store, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	backend, err := NewS3StorageBackend(&S3Config{
		Region:          "us-east-1",
		BucketName:      "test-bucket",
		AccessKeyID:     "fake",
		SecretAccessKey: "fake",
		Endpoint:        f.server.URL,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3StorageBackend: %v", err)
	}
	return f, backend
}

func TestS3StorageBackend_Constructor_ValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *S3Config
		want string
	}{
		{"nil", nil, "config is nil"},
		{"missing bucket", &S3Config{Region: "us-east-1"}, "BucketName is required"},
		{"defaults region", &S3Config{BucketName: "b"}, ""}, // bucket exist check happens at runtime via HealthCheck
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewS3StorageBackend(tc.cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestS3StorageBackend_RoundTrip(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	key := "2026/07/a1/abc.png"
	payload := []byte("HELLO S3")

	if err := b.Save(ctx, key, payload); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("roundtrip = %q want %q", got, payload)
	}
}

func TestS3StorageBackend_GetReader(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	_ = b.Save(ctx, "k", []byte("streaming"))

	r, err := b.GetReader(ctx, "k")
	if err != nil {
		t.Fatalf("GetReader: %v", err)
	}
	defer r.Close()
	buf := make([]byte, 16)
	n, _ := r.Read(buf)
	if n < 8 {
		t.Errorf("read %d bytes", n)
	}
}

func TestS3StorageBackend_SaveReader(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	// bytes.NewReader is seekable, which the AWS SDK v2 requires to compute
	// the input checksum without TLS. strings.NewReader also works, but
	// bytes.Reader is the safest cross-version choice.
	payload := []byte("streamed")
	if err := b.SaveReader(ctx, "stream.bin", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("SaveReader: %v", err)
	}
	got, _ := b.Get(ctx, "stream.bin")
	if string(got) != "streamed" {
		t.Errorf("got %q", got)
	}
}

func TestS3StorageBackend_SaveReader_RejectsBadSize(t *testing.T) {
	_, b := newS3Fixture(t)
	err := b.SaveReader(context.Background(), "k", strings.NewReader("x"), -1)
	if err == nil {
		t.Fatal("expected error for negative size")
	}
}

func TestS3StorageBackend_Exists(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()

	if ok, err := b.Exists(ctx, "missing"); ok || err != nil {
		t.Errorf("Exists(missing) = %v,%v want false,nil", ok, err)
	}
	_ = b.Save(ctx, "real", []byte("data"))
	if ok, err := b.Exists(ctx, "real"); !ok || err != nil {
		t.Errorf("Exists(real) = %v,%v want true,nil", ok, err)
	}
}

func TestS3StorageBackend_Delete(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	_ = b.Save(ctx, "k", []byte("d"))
	if err := b.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, _ := b.Exists(ctx, "k")
	if ok {
		t.Error("still present after Delete")
	}
}

func TestS3StorageBackend_GetMetadata(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	_ = b.Save(ctx, "m", []byte("0123456789"))

	md, err := b.GetMetadata(ctx, "m")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if md.Key != "m" {
		t.Errorf("Key = %q want m", md.Key)
	}
	if md.Size == 0 {
		t.Error("Size = 0")
	}
	if md.ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q", md.ContentType)
	}
	if md.ETag != "fixture-etag" {
		t.Errorf("ETag = %q", md.ETag)
	}
}

func TestS3StorageBackend_GetBackendType(t *testing.T) {
	_, b := newS3Fixture(t)
	if got := b.GetBackendType(); got != "s3" {
		t.Errorf("GetBackendType = %q want s3", got)
	}
}

func TestS3StorageBackend_HealthCheck(t *testing.T) {
	_, b := newS3Fixture(t)
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestS3StorageBackend_SanitizeKey(t *testing.T) {
	_, b := newS3Fixture(t)
	ctx := context.Background()
	for _, k := range []string{"../escape", "a/../../escape", ".", "/"} {
		if err := b.Save(ctx, k, []byte("x")); err == nil {
			t.Errorf("expected error for key %q", k)
		}
	}
}

func TestS3StorageBackend_KeyFor(t *testing.T) {
	tests := []struct {
		prefix string
		key    string
		want   string
		errOK  bool
	}{
		{"", "abc", "abc", false},
		{"attachments", "abc.png", "attachments/abc.png", false},
		{"attachments/", "abc.png", "attachments/abc.png", false},
		{"x", "../escape.txt", "", true},
	}
	for _, tc := range tests {
		got, err := (&S3StorageBackend{prefix: strings.Trim(tc.prefix, "/")}).keyFor(tc.key)
		if tc.errOK {
			if err == nil {
				t.Errorf("keyFor(%q,%q) expected error", tc.prefix, tc.key)
			}
			continue
		}
		if err != nil {
			t.Errorf("keyFor(%q,%q): %v", tc.prefix, tc.key, err)
			continue
		}
		if got != tc.want {
			t.Errorf("keyFor(%q,%q) = %q want %q", tc.prefix, tc.key, got, tc.want)
		}
	}
}

func TestIsS3NotFound(t *testing.T) {
	if isS3NotFound(nil) {
		t.Error("nil should not classify")
	}
	if isS3NotFound(errors.New("random")) {
		t.Error("random should not classify")
	}
	notFound := &types.NotFound{}
	if !isS3NotFound(notFound) {
		t.Error("types.NotFound should classify as not-found")
	}
	if !isS3NotFound(wrappedError{err: notFound}) {
		t.Error("wrapped types.NotFound should classify as not-found")
	}
}

// wrappedError is a tiny helper that wraps an error in a fmt.Errorf chain so
// errors.As can walk it. Inline so the test stays self-contained.
type wrappedError struct{ err error }

func (w wrappedError) Error() string { return "wrapped: " + w.err.Error() }
func (w wrappedError) Unwrap() error { return w.err }
