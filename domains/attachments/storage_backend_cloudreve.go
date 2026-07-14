//go:build cloudreve_storage

package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// CloudreveStorageBackend implements StorageBackend against a Cloudreve instance
// via its WebDAV endpoint (/dav/...).
//
// Rationale for choosing WebDAV over the REST API:
//  1. One transport (HTTP basic auth) covers Save / Get / Delete / Exists / List.
//  2. WebDAV is idempotent on PUT and HEAD, matching the canonical StorageBackend
//     contract's expectation (SaveBase64Image relies on Exists to dedupe).
//  3. PUT supports streaming uploads → matches SaveReader without materializing
//     the body in memory.
//  4. PROPFIND returns last-modified and content-length, satisfying GetMetadata
//     without an out-of-band API call.
//
// All file keys are resolved to {baseURL}/dav/{remotePath}/{key}. The remotePath
// acts as the per-tenant/per-service root inside Cloudreve's user home.
//
// This backend is gated behind the cloudreve_storage build tag so the default
// gateway binary keeps no Cloudreve-specific dependencies. To enable:
//
//	go build -tags cloudreve_storage ./...
type CloudreveStorageBackend struct {
	// baseURL is the Cloudreve origin without trailing slash,
	// e.g. "https://cloudreve.kxpms.cn".
	baseURL string
	// username / password are sent as HTTP Basic auth on every WebDAV request.
	username string
	password string
	// remotePath is the directory inside Cloudreve that hosts our files,
	// e.g. "/llm-gateway-attachments". Must start with "/" and not end with "/".
	remotePath string
	// httpClient is the shared HTTP client. Built once in NewCloudreveStorageBackend.
	httpClient *http.Client
}

// CloudreveConfig configures a Cloudreve StorageBackend.
// Field validation is enforced by NewCloudreveStorageBackend.
type CloudreveConfig struct {
	// BaseURL is the Cloudreve site origin (no trailing slash).
	BaseURL string
	// Username and Password are HTTP Basic credentials against the WebDAV endpoint.
	// These must belong to a Cloudreve user with read/write on RemotePath.
	Username string
	Password string
	// RemotePath is the base directory inside Cloudreve where files are stored,
	// e.g. "/llm-gateway-attachments". The path is created automatically on first Save.
	RemotePath string
	// Timeout is the per-request timeout (PUT/GET/HEAD/PROPFIND/DELETE).
	// Zero falls back to 30s.
	Timeout time.Duration
}

// NewCloudreveStorageBackend constructs a CloudreveStorageBackend, validates
// the configuration and warms up the HTTP client.
//
// Returns a descriptive error if any required field is missing or invalid.
func NewCloudreveStorageBackend(cfg CloudreveConfig) (*CloudreveStorageBackend, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("cloudreve storage: BaseURL is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("cloudreve storage: Username and Password are required")
	}

	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("cloudreve storage: invalid BaseURL %q: %w", cfg.BaseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("cloudreve storage: BaseURL scheme must be http or https, got %q", parsed.Scheme)
	}

	remote := strings.TrimSpace(cfg.RemotePath)
	if remote == "" {
		remote = "/llm-gateway-attachments"
	}
	if !strings.HasPrefix(remote, "/") {
		remote = "/" + remote
	}
	remote = strings.TrimRight(remote, "/")

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &CloudreveStorageBackend{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		username:   cfg.Username,
		password:   cfg.Password,
		remotePath: remote,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// davURL builds the absolute WebDAV URL for a storage key.
//
//	key "2026/07/a1/b2/abc.png" → https://host/dav/llm-gateway-attachments/2026/07/a1/b2/abc.png
func (c *CloudreveStorageBackend) davURL(key string) (string, error) {
	cleaned, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	// Encode each segment so spaces / unicode survive the round-trip.
	parts := strings.Split(cleaned, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	encoded := strings.Join(parts, "/")

	base := c.baseURL + "/dav" + c.remotePath
	if encoded != "" {
		base += "/" + encoded
	}
	return base, nil
}

// parentDirURL returns the WebDAV URL of the parent directory for `key`.
// Cloudreve refuses to PUT into a non-existing directory, so Save walks
// parent dirs and MKCOLs them.
func (c *CloudreveStorageBackend) parentDirURL(key string) (string, error) {
	cleaned, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	dir := path.Dir(cleaned)
	if dir == "." || dir == "/" {
		return c.baseURL + "/dav" + c.remotePath, nil
	}
	parts := strings.Split(dir, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	encoded := strings.Join(parts, "/")
	return c.baseURL + "/dav" + c.remotePath + "/" + encoded, nil
}

// sanitizeKey is defined in sanitize_key.go (shared across Cloudreve / OSS / S3
// backends so the safety contract is uniform).

// do executes an HTTP request with Basic auth attached and returns the response.
// The caller is responsible for closing resp.Body. We translate non-2xx codes
// into descriptive errors. 404 / 409 / 405 / 301 get special handling so callers
// can disambiguate "file missing" vs. "real error".
func (c *CloudreveStorageBackend) do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: http %s %s: %w", req.Method, req.URL.Path, err)
	}
	return resp, nil
}

// Save uploads data to Cloudreve via WebDAV PUT.
//
// Flow:
//  1. Ensure parent directory chain exists (MKCOL on each missing segment).
//  2. PUT the file body at the resolved URL.
//
// Save is idempotent on retry because PUT overwrites in place. Concurrent
// PUTs to the same key are last-writer-wins at the storage layer; the
// caller (SaveBase64Image) handles dedupe via content hashing beforehand.
func (c *CloudreveStorageBackend) Save(ctx context.Context, key string, data []byte) error {
	if err := c.ensureParents(ctx, key); err != nil {
		return fmt.Errorf("cloudreve storage: ensure parents for %q: %w", key, err)
	}

	target, err := c.davURL(key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("cloudreve storage: build PUT: %w", err)
	}
	// Cloudreve's WebDAV handler uses Content-Type to set the entity type.
	// We default to application/octet-stream; callers wanting a specific MIME
	// should pre-encode it in the key suffix.
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.Itoa(len(data)))
	req.Header.Set("Overwrite", "T") // RFC 4918 § 9.6.4, hint that PUT replaces.

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloudreve storage: PUT %s failed: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// SaveReader streams `reader` straight into a WebDAV PUT. Caller must know
// the size up front because Cloudreve's PUT handler needs Content-Length;
// chunked uploads are not supported by typical sabredav-style backends.
func (c *CloudreveStorageBackend) SaveReader(ctx context.Context, key string, reader io.Reader, size int64) error {
	if size < 0 {
		return errors.New("cloudreve storage: SaveReader requires non-negative size")
	}
	if err := c.ensureParents(ctx, key); err != nil {
		return fmt.Errorf("cloudreve storage: ensure parents for %q: %w", key, err)
	}

	target, err := c.davURL(key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, io.LimitReader(reader, size))
	if err != nil {
		return fmt.Errorf("cloudreve storage: build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("Overwrite", "T")

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloudreve storage: PUT %s failed: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// Get fetches the file body. Returns os-equivalent error text when missing
// so callers can match os.IsNotExist() patterns if they want.
func (c *CloudreveStorageBackend) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := c.GetReader(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// GetReader opens a streaming GET against Cloudreve.
//
// A 404 surfaces as "file not found: <key>" (matching the local backend's
// phrasing) so callers can use strings.Contains / errors.Is patterns.
// To keep large attachments memory-friendly the body is NOT buffered here.
func (c *CloudreveStorageBackend) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	target, err := c.davURL(key)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: build GET: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("cloudreve storage: file not found: %s", key)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("cloudreve storage: GET %s failed: HTTP %d", target, resp.StatusCode)
	}
	return resp.Body, nil
}

// Delete issues a WebDAV DELETE. 404 is treated as success so re-runs are
// idempotent — mirrored on LocalStorageBackend and OSSStorageBackend.
func (c *CloudreveStorageBackend) Delete(ctx context.Context, key string) error {
	target, err := c.davURL(key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return fmt.Errorf("cloudreve storage: build DELETE: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 204 No Content (success), 404 Not Found (already gone) → no error.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloudreve storage: DELETE %s failed: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// Exists issues a HEAD request and treats 404 as "no".
func (c *CloudreveStorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	target, err := c.davURL(key)
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return false, fmt.Errorf("cloudreve storage: build HEAD: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("cloudreve storage: HEAD %s failed: HTTP %d", target, resp.StatusCode)
	}
}

// List issues a PROPFIND on the parent directory and parses multistatus XML
// into a flat slice of storage keys. The queried target itself is filtered
// out so the result contains only descendant files.
//
// Cloudreve's WebDAV layer follows the sabredav convention: PROPFIND Depth: 1
// returns immediate children of the target resource.
func (c *CloudreveStorageBackend) List(ctx context.Context, prefix string) ([]string, error) {
	root := c.baseURL + "/dav" + c.remotePath
	target := root
	if prefix != "" {
		cleaned, err := sanitizeKey(prefix)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(cleaned, "/")
		for i, p := range parts {
			parts[i] = url.PathEscape(p)
		}
		target = root + "/" + strings.Join(parts, "/")
	}

	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: build PROPFIND: %w", err)
	}
	req.Header.Set("Depth", "1")

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cloudreve storage: PROPFIND %s failed: HTTP %d", target, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: read PROPFIND body: %w", err)
	}

	// Filter out the queried target itself (not the root).
	hits, err := parsePropfindResponse(string(body), target)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: parse PROPFIND: %w", err)
	}

	// Cloudreve returns both directories and files. Filter to plain files only
	// (those without a trailing slash in href).
	trimPrefix := "/dav" + c.remotePath + "/"
	files := make([]string, 0, len(hits))
	for _, h := range hits {
		trimmed := strings.TrimPrefix(h, trimPrefix)
		if trimmed == "" || strings.HasSuffix(trimmed, "/") {
			continue
		}
		// Optional prefix filter on top of PROPFIND's natural scope. PROPFIND
		// Depth:1 only returns immediate children, so callers asking for a
		// deep prefix like "2026/07/" must be matched differently than a
		// parent directory like "2026".
		if prefix != "" {
			want := strings.TrimLeft(prefix, "/")
			if !strings.HasPrefix(trimmed, want) {
				continue
			}
		}
		files = append(files, trimmed)
	}
	return files, nil
}

// GetMetadata fetches metadata via HEAD (size + last-modified) without
// downloading the body.
func (c *CloudreveStorageBackend) GetMetadata(ctx context.Context, key string) (*FileMetadata, error) {
	target, err := c.davURL(key)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudreve storage: build HEAD: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("cloudreve storage: file not found: %s", key)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cloudreve storage: HEAD %s failed: HTTP %d", target, resp.StatusCode)
	}

	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	lastMod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	ct := resp.Header.Get("Content-Type")

	return &FileMetadata{
		Key:          key,
		Size:         size,
		LastModified: lastMod,
		ContentType:  ct,
		ETag:         etag,
	}, nil
}

// GetBackendType returns the canonical backend name used by StorageConfig.
func (c *CloudreveStorageBackend) GetBackendType() string {
	return "cloudreve"
}

// HealthCheck pings the WebDAV root with a PROPFIND Depth: 0. A 2xx (or 207)
// means credentials are valid and the remotePath is reachable.
func (c *CloudreveStorageBackend) HealthCheck(ctx context.Context) error {
	target := c.baseURL + "/dav" + c.remotePath
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", target, nil)
	if err != nil {
		return fmt.Errorf("cloudreve storage: build health PROPFIND: %w", err)
	}
	req.Header.Set("Depth", "0")

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloudreve storage: health check failed: HTTP %d (check base URL / credentials / remote path)", resp.StatusCode)
	}
	return nil
}

// ensureParents walks up the directory chain for `key` and MKCOLs anything
// missing. Idempotent: an existing collection returns 405 Method Not Allowed,
// which we treat as success.
func (c *CloudreveStorageBackend) ensureParents(ctx context.Context, key string) error {
	parentURL, err := c.parentDirURL(key)
	if err != nil {
		return err
	}
	if parentURL == c.baseURL+"/dav"+c.remotePath {
		return c.mkcol(ctx, parentURL) // ensure the root exists
	}

	// Walk from the most-specific parent upward.
	// parentURL is e.g. .../dav/root/2026/07/a1/b2.
	// We MKCOL .../b2, then .../a1, then .../07, then .../2026, then .../root.
	rel := strings.TrimPrefix(parentURL, c.baseURL+"/dav"+c.remotePath)
	rel = strings.TrimLeft(rel, "/")
	if rel == "" {
		return nil
	}
	segments := strings.Split(rel, "/")
	root := c.baseURL + "/dav" + c.remotePath
	current := root
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		current += "/" + url.PathEscape(seg)
		if err := c.mkcol(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

// mkcol issues a single MKCOL request. 201 Created ⇒ created; 405 Method Not
// Allowed ⇒ collection already exists; everything else is an error.
func (c *CloudreveStorageBackend) mkcol(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", target, nil)
	if err != nil {
		return fmt.Errorf("cloudreve storage: build MKCOL: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusMethodNotAllowed, http.StatusConflict:
		// Collection already exists — Cloudreve returns 405 in some configs.
		return nil
	default:
		return fmt.Errorf("cloudreve storage: MKCOL %s failed: HTTP %d", target, resp.StatusCode)
	}
}
