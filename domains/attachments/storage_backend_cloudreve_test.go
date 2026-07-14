//go:build cloudreve_storage

package attachments

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cloudreveFixture wires a mock Cloudreve WebDAV server and returns a
// backend pointed at it. All requests are recorded so tests can assert the
// request method, path and headers.
type cloudreveFixture struct {
	server *httptest.Server
	store  map[string][]byte
	mtimes map[string]time.Time
	// reqLog preserves the order of every method+path the server received.
	reqLog []string
}

func newCloudreveFixture(t *testing.T) (*cloudreveFixture, *CloudreveStorageBackend) {
	t.Helper()
	f := &cloudreveFixture{
		store:  map[string][]byte{},
		mtimes: map[string]time.Time{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Catch-all for "/" and unrecognized paths. Cloudreve returns 207
		// with multistatus XML; for our tests we just acknowledge existence.
		f.record(r)
		if r.Method == "OPTIONS" {
			w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		key := strings.TrimPrefix(r.URL.Path, "/dav")

		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			f.store[key] = body
			f.mtimes[key] = time.Now()
			w.WriteHeader(http.StatusCreated)

		case http.MethodGet:
			data, ok := f.store[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		case http.MethodHead:
			data, ok := f.store[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			tm, has := f.mtimes[key]
			if has {
				w.Header().Set("Last-Modified", tm.UTC().Format(http.TimeFormat))
				w.Header().Set("ETag", `"deadbeef"`)
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			delete(f.store, key)
			delete(f.mtimes, key)
			w.WriteHeader(http.StatusNoContent)

		case "MKCOL":
			// Idempotent: collections are merely "exists", so just OK.
			w.WriteHeader(http.StatusCreated)

		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			// `key` here has "/dav" already trimmed; re-prepend it so propfindBody
			// sees the URL a real Cloudreve server would emit.
			_, _ = io.WriteString(w, f.propfindBody("/dav"+key))

		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	})

	f.server = httptest.NewServer(mux)

	backend, err := NewCloudreveStorageBackend(CloudreveConfig{
		BaseURL:    f.server.URL,
		Username:   "tester",
		Password:   "tester-pass",
		RemotePath: "/llm-gateway-attachments",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		f.server.Close()
		t.Fatalf("NewCloudreveStorageBackend: %v", err)
	}
	t.Cleanup(func() {
		f.server.Close()
	})
	return f, backend
}

func (f *cloudreveFixture) record(r *http.Request) {
	f.reqLog = append(f.reqLog, r.Method+"\t"+r.URL.Path)
}

// propfindBody returns a minimal multistatus document for every stored key
// under the requested collection. It treats "/llm-gateway-attachments" as
// the root because that matches the backend's default RemotePath.
func (f *cloudreveFixture) propfindBody(target string) string {
	prefix := "/dav/llm-gateway-attachments"
	if !strings.HasPrefix(target, prefix) {
		// Sub-directory query — for tests we return an empty multistatus so
		// the parser sees well-formed XML with no children.
		return `<D:multistatus xmlns:D="DAV:"></D:multistatus>`
	}
	rel := strings.TrimPrefix(target, prefix)
	rel = strings.TrimLeft(rel, "/")
	if rel != "" && !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	prefixSlash := prefix + "/" + rel

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	sb.WriteString(`<D:multistatus xmlns:D="DAV:">`)
	// The target itself, per RFC 4918.
	sb.WriteString(fmt.Sprintf(
		`<D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
		target,
	))
	for k := range f.store {
		// store keys have "/dav" already trimmed (see the mux.HandleFunc("/dav/", ...) above),
		// so the href Cloudreve-style emits should re-prepend "/dav" before
		// matching against the requested prefix.
		href := "/dav" + k
		if !strings.HasPrefix(href, prefixSlash) {
			continue
		}
		rest := strings.TrimPrefix(href, prefixSlash)
		if rest == "" {
			continue
		}
		_ = rest
		sb.WriteString(fmt.Sprintf(
			`<D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
			href,
		))
	}
	sb.WriteString(`</D:multistatus>`)
	return sb.String()
}

func TestNewCloudreveStorageBackend_ValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  CloudreveConfig
		want string
	}{
		{"missing base url", CloudreveConfig{Username: "u", Password: "p"}, "BaseURL"},
		{"missing creds", CloudreveConfig{BaseURL: "https://x"}, "Username"},
		{"bad url", CloudreveConfig{BaseURL: "ftp://x", Username: "u", Password: "p"}, "scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCloudreveStorageBackend(tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestNewCloudreveStorageBackend_DefaultsRemotePath(t *testing.T) {
	b, err := NewCloudreveStorageBackend(CloudreveConfig{
		BaseURL:  "https://cloudreve.example.com",
		Username: "u",
		Password: "p",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.remotePath != "/llm-gateway-attachments" {
		t.Errorf("remotePath = %q, want /llm-gateway-attachments", b.remotePath)
	}
	if b.httpClient.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", b.httpClient.Timeout)
	}
}

func TestCloudreveStorageBackend_RoundTrip(t *testing.T) {
	f, b := newCloudreveFixture(t)
	ctx := context.Background()

	key := "2026/07/a1/a1b2c3d4.png"
	payload := []byte("REDPIXEL")

	// Save may issue MKCOLs ahead of PUT — that's fine.
	if err := b.Save(ctx, key, payload); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
	}

	// GetReader returns streaming bytes for big files.
	r, err := b.GetReader(ctx, key)
	if err != nil {
		t.Fatalf("GetReader: %v", err)
	}
	streamed, _ := io.ReadAll(r)
	r.Close()
	if string(streamed) != string(payload) {
		t.Errorf("streamed mismatch")
	}

	if !contains(f.reqLog, "PUT\t/dav/llm-gateway-attachments/"+key) {
		t.Errorf("PUT request not seen, log=%v", f.reqLog)
	}
	if !contains(f.reqLog, "GET\t/dav/llm-gateway-attachments/"+key) {
		t.Errorf("GET request not seen, log=%v", f.reqLog)
	}
}

func TestCloudreveStorageBackend_SaveReader(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()
	key := "2026/07/stream.bin"
	payload := []byte("hello world payload")

	if err := b.SaveReader(ctx, key, strings.NewReader(string(payload)), int64(len(payload))); err != nil {
		t.Fatalf("SaveReader: %v", err)
	}
	got, _ := b.Get(ctx, key)
	if string(got) != string(payload) {
		t.Errorf("SaveReader roundtrip mismatch: %q", got)
	}
}

func TestCloudreveStorageBackend_SaveReader_RejectsBadSize(t *testing.T) {
	_, b := newCloudreveFixture(t)
	err := b.SaveReader(context.Background(), "k", strings.NewReader("x"), -1)
	if err == nil {
		t.Fatal("expected error for negative size, got nil")
	}
}

func TestCloudreveStorageBackend_Exists(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()

	ok, err := b.Exists(ctx, "nope.txt")
	if err != nil || ok {
		t.Fatalf("Exists(nope) ok=%v err=%v want false,nil", ok, err)
	}

	_ = b.Save(ctx, "real.txt", []byte("data"))
	ok, err = b.Exists(ctx, "real.txt")
	if err != nil || !ok {
		t.Fatalf("Exists(real) ok=%v err=%v want true,nil", ok, err)
	}
}

func TestCloudreveStorageBackend_Delete(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()
	key := "todelete.txt"

	_ = b.Save(ctx, key, []byte("data"))
	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, _ := b.Exists(ctx, key)
	if ok {
		t.Error("file still present after Delete")
	}
	// Idempotent re-delete must not error.
	if err := b.Delete(ctx, key); err != nil {
		t.Errorf("re-delete should be idempotent, got %v", err)
	}
}

func TestCloudreveStorageBackend_Get_NotFound(t *testing.T) {
	_, b := newCloudreveFixture(t)
	_, err := b.Get(context.Background(), "missing.png")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should mention 'not found'", err.Error())
	}
}

func TestCloudreveStorageBackend_GetMetadata(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()
	key := "meta.bin"
	payload := []byte("0123456789")
	_ = b.Save(ctx, key, payload)

	md, err := b.GetMetadata(ctx, key)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if md.Key != key {
		t.Errorf("Key = %q want %q", md.Key, key)
	}
	if md.Size != int64(len(payload)) {
		t.Errorf("Size = %d want %d", md.Size, len(payload))
	}
	if md.ContentType == "" {
		t.Error("ContentType empty")
	}
	if md.LastModified.IsZero() {
		t.Error("LastModified zero")
	}
	if md.ETag != "deadbeef" {
		t.Errorf("ETag = %q want deadbeef (sans quotes)", md.ETag)
	}
}

func TestCloudreveStorageBackend_GetMetadata_NotFound(t *testing.T) {
	_, b := newCloudreveFixture(t)
	_, err := b.GetMetadata(context.Background(), "nope.png")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestCloudreveStorageBackend_GetBackendType(t *testing.T) {
	_, b := newCloudreveFixture(t)
	if got := b.GetBackendType(); got != "cloudreve" {
		t.Errorf("GetBackendType = %q want cloudreve", got)
	}
}

func TestCloudreveStorageBackend_HealthCheck(t *testing.T) {
	_, b := newCloudreveFixture(t)
	if err := b.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestCloudreveStorageBackend_List(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()

	// Seed several files; the fixture's propfindBody filters by prefix.
	for _, k := range []string{
		"2026/07/aa/aaa.png",
		"2026/07/bb/bbb.png",
		"2026/08/cc/ccc.png",
	} {
		if err := b.Save(ctx, k, []byte("x")); err != nil {
			t.Fatalf("Save %s: %v", k, err)
		}
	}

	keys, err := b.List(ctx, "2026/07")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) < 2 {
		t.Errorf("List returned %d keys, expected >=2: %v", len(keys), keys)
	}
	for _, k := range keys {
		if strings.HasSuffix(k, "/") {
			t.Errorf("List should skip directory entries, got %q", k)
		}
	}
}

func TestCloudreveStorageBackend_SanitizePath(t *testing.T) {
	_, b := newCloudreveFixture(t)
	ctx := context.Background()

	bad := []string{
		"../escape.txt",
		"good/../../escape.txt",
		".",
		"/",
	}
	for _, k := range bad {
		if err := b.Save(ctx, k, []byte("x")); err == nil {
			t.Errorf("expected sanitize error for key %q, got nil", k)
		}
	}
}

func TestParsePropfindResponse(t *testing.T) {
	body := `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/root</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>
  <D:response><D:href>/dav/root/a.png</D:href><D:propstat><D:prop><D:resourcetype/></D:prop></D:propstat></D:response>
  <D:response><D:href>/dav/root/sub/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>
</D:multistatus>`
	hits, err := parsePropfindResponse(body, "https://x/dav/root")
	if err != nil {
		t.Fatalf("parsePropfindResponse: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %v", len(hits), hits)
	}
	if hits[0] != "/dav/root/a.png" {
		t.Errorf("unexpected hit: %q", hits[0])
	}
}

func TestParsePropfindResponse_BadXML(t *testing.T) {
	_, err := parsePropfindResponse("not xml", "https://x/dav/root")
	if err == nil {
		t.Fatal("expected xml parse error")
	}
}

func TestParsePropfindResponse_Empty(t *testing.T) {
	hits, err := parsePropfindResponse("", "https://x/dav/root")
	if err != nil {
		t.Fatalf("unexpected error on empty body: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits, got %v", hits)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Sanity: ensure default build still passes (no tag) by re-importing fs-safe helper.
func TestCloudreveStorageBackend_DavURLShape(t *testing.T) {
	b := &CloudreveStorageBackend{
		baseURL:    "https://example.com",
		username:   "u",
		password:   "p",
		remotePath: "/llm-gateway-attachments",
		httpClient: &http.Client{Timeout: time.Second},
	}
	u, err := b.davURL("2026/07/with space.png")
	if err != nil {
		t.Fatalf("davURL: %v", err)
	}
	expected := "https://example.com/dav/llm-gateway-attachments/2026/07/with%20space.png"
	if u != expected {
		t.Errorf("davURL = %q want %q", u, expected)
	}
}
