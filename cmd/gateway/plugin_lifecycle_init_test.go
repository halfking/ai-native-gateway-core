package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLifecycle records the last action and returns the configured error.
type fakeLifecycle struct {
	gotAction   string
	gotPluginID string
	err         error
}

func (f *fakeLifecycle) Activate(ctx context.Context, pluginID string) error {
	f.gotAction = "activate"
	f.gotPluginID = pluginID
	return f.err
}
func (f *fakeLifecycle) Deactivate(ctx context.Context, pluginID string) error {
	f.gotAction = "deactivate"
	f.gotPluginID = pluginID
	return f.err
}
func (f *fakeLifecycle) Uninstall(ctx context.Context, pluginID string) error {
	f.gotAction = "uninstall"
	f.gotPluginID = pluginID
	return f.err
}

func runLifecycle(t *testing.T, h http.HandlerFunc, path string, idParam string) (*httptest.ResponseRecorder, *fakeLifecycle) {
	t.Helper()
	lc := &fakeLifecycle{}
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if idParam != "" {
		req = req.WithContext(contextWithPathValue(req.Context(), "id", idParam))
	}
	rec := httptest.NewRecorder()
	// The handler factories capture `lc` in a closure; rebind via the wrapper.
	// To keep the test fully isolated, we re-build the handler here so the
	// captured `lc` is the freshly-allocated fake below.
	_ = lc
	h.ServeHTTP(rec, req)
	return rec, lc
}

// pathCtxKey is unexported; we use the http.Request SetPathValue method via
// a small helper that mirrors stdlib's rctx mutation. We can't import
// net/http internals, so we use httptest.NewRequest which respects the
// http.Request PathValue flow. ServeMux sets path values automatically when
// matching registered patterns — for the unit test we instead build the
// handler with a closure over a fixed id and dispatch by stripping the
// url prefix.
//
// Instead, we exercise handlers directly without routing: each handler reads
// r.PathValue("id"). We construct the request with a pattern that supplies
// the value via httptest's SetPathValue (Go 1.22+).

func contextWithPathValue(ctx context.Context, key, value string) context.Context {
	return ctx
}

func newReqWithPathValue(method, path, id string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.SetPathValue("id", id)
	return r
}

func TestLifecycleHandlers_HappyPath(t *testing.T) {
	cases := []struct {
		action string
		factory func(pluginLifecycle) http.HandlerFunc
		wantStatus string
	}{
		{action: "activate", factory: makePluginActivateHandler, wantStatus: "activated"},
		{action: "deactivate", factory: makePluginDeactivateHandler, wantStatus: "deactivated"},
		{action: "uninstall", factory: makePluginUninstallHandler, wantStatus: "uninstalled"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			lc := &fakeLifecycle{}
			h := tc.factory(lc)
			req := newReqWithPathValue(http.MethodPost, "/api/v1/plugins/asm/"+tc.action, "asm")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if lc.gotAction != tc.action {
				t.Errorf("action = %q, want %q", lc.gotAction, tc.action)
			}
			if lc.gotPluginID != "asm" {
				t.Errorf("plugin id = %q, want asm", lc.gotPluginID)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("json: %v", err)
			}
			if body["status"] != tc.wantStatus {
				t.Errorf("status = %q, want %q", body["status"], tc.wantStatus)
			}
			if body["plugin_id"] != "asm" {
				t.Errorf("plugin_id = %q, want asm", body["plugin_id"])
			}
		})
	}
}

func TestLifecycleHandlers_InvalidID(t *testing.T) {
	// Use the request path with an URL-safe encoded form (httptest rejects
	// raw spaces in NewRequest). The handler reads id from r.PathValue, so
	// what we set there is what the validator sees — the URL string only
	// needs to round-trip.
	badIDs := []struct {
		raw  string
		path string
	}{
		{raw: "", path: ""},
		{raw: "../etc", path: "..%2Fetc"},
		{raw: "Plugin", path: "Plugin"},
		{raw: "-start", path: "-start"},
		{raw: "with space", path: "with%20space"},
	}
	for _, tc := range badIDs {
		t.Run("bad-"+tc.raw, func(t *testing.T) {
			lc := &fakeLifecycle{}
			h := makePluginActivateHandler(lc)
			req := newReqWithPathValue(http.MethodPost, "/api/v1/plugins/"+tc.path+"/activate", tc.raw)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (id=%q)", rec.Code, tc.raw)
			}
			if lc.gotAction != "" {
				t.Errorf("handler should not dispatch on invalid id; got action %q", lc.gotAction)
			}
		})
	}
}

func TestLifecycleHandlers_NilLifecycleReturns503(t *testing.T) {
	h := makePluginActivateHandler(nil)
	req := newReqWithPathValue(http.MethodPost, "/api/v1/plugins/asm/activate", "asm")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "lifecycle_unavailable") {
		t.Errorf("body missing lifecycle_unavailable code: %s", rec.Body.String())
	}
}

func TestLifecycleHandlers_ErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not-found", err: &pluginLifecycleError{code: "plugin.not_found", msg: "x"}, wantStatus: 404, wantCode: "plugin.not_found"},
		{name: "invalid-id", err: &pluginLifecycleError{code: "plugin.invalid_id", msg: "x"}, wantStatus: 400, wantCode: "plugin.invalid_id"},
		{name: "unavailable", err: &pluginLifecycleError{code: "plugin.lifecycle_unavailable", msg: "x"}, wantStatus: 503, wantCode: "plugin.lifecycle_unavailable"},
		{name: "internal", err: errors.New("kaboom"), wantStatus: 500, wantCode: "plugin.internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lc := &fakeLifecycle{err: tc.err}
			h := makePluginDeactivateHandler(lc)
			req := newReqWithPathValue(http.MethodPost, "/api/v1/plugins/asm/deactivate", "asm")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("json: %v", err)
			}
			if body["code"] != tc.wantCode {
				t.Errorf("code = %q, want %q", body["code"], tc.wantCode)
			}
		})
	}
}

func TestIsValidPluginID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"asm", true},
		{"ai-session-manager", true},
		{"com.acme.x", true},
		{"plugin2", true},
		{"", false},
		{"../etc", false},
		{"Plugin", false},
		{"-start", false},
		{"with space", false},
		{strings.Repeat("a", 65), false}, // > 64 chars
	}
	for _, tc := range cases {
		got := isValidPluginID(tc.in)
		if got != tc.want {
			t.Errorf("isValidPluginID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNoopLifecycle_AllUnavailable(t *testing.T) {
	var lc pluginLifecycle = noopPluginLifecycle{}
	if err := lc.Activate(context.Background(), "x"); err == nil {
		t.Error("Activate on noop should error")
	}
	if err := lc.Deactivate(context.Background(), "x"); err == nil {
		t.Error("Deactivate on noop should error")
	}
	if err := lc.Uninstall(context.Background(), "x"); err == nil {
		t.Error("Uninstall on noop should error")
	}
}

func TestResolveLifecycle_NilSupervisor(t *testing.T) {
	lc := resolveLifecycle(nil, "/tmp/plugins")
	if lc == nil {
		t.Fatal("resolveLifecycle(nil) returned nil")
	}
	if _, ok := lc.(noopPluginLifecycle); !ok {
		t.Errorf("resolveLifecycle(nil) = %T, want noopPluginLifecycle", lc)
	}
}

func TestResolvePluginBundle_StaysInsidePluginsDir(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "ai-session-manager")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	got, err := resolvePluginBundle(root, "ai-session-manager")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != inner {
		t.Errorf("got %q, want %q", got, inner)
	}
}

func TestResolvePluginBundle_MissingDirIsOk(t *testing.T) {
	root := t.TempDir()
	got, err := resolvePluginBundle(root, "ghost")
	if err != nil {
		t.Fatalf("missing dir should be idempotent: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for idempotent uninstall", got)
	}
}

func TestResolvePluginBundle_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	// pluginID regex would reject these, but defense-in-depth: assume someone
	// upstream bypassed the regex. resolvePluginBundle must still hold.
	for _, badID := range []string{"../etc", "..", "a/../b"} {
		if _, err := resolvePluginBundle(root, badID); err == nil {
			t.Errorf("resolvePluginBundle(%q) = nil err, want escape rejection", badID)
		}
	}
}

// Compile-time check that fakeLifecycle satisfies the interface.
var _ pluginLifecycle = (*fakeLifecycle)(nil)