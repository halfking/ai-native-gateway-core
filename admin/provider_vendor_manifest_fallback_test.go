package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestManifestFallbackIsAccepted drives the production resolver rather than
// copying its source predicate. A real 401 response plus a non-empty manifest
// must return the manifest models with source="manifest" while preserving the
// auth sentinel for discoverAndUpsertForCredential.
func TestManifestFallbackIsAccepted(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	tpl := "/models"
	manifest := `{"models":[{"id":"MiniMax-Text-01"},{"id":"abab6.5s-chat"}]}`
	cred := credentialRowLite{
		baseURL:            srv.URL,
		protocol:           "openai-completions",
		discoveryStrategy:  "manifest",
		modelsEndpointTpl:  &tpl,
		modelsManifestJSON: &manifest,
	}

	models, source, err := h.resolveModelsForCredential(context.Background(), cred, "bad-key", true)
	if len(models) != 2 || models[0] != "MiniMax-Text-01" || models[1] != "abab6.5s-chat" {
		t.Fatalf("models=%v, want manifest models", models)
	}
	if source != "manifest" {
		t.Fatalf("source=%q, want manifest", source)
	}
	if !errors.Is(err, errVendorAuthRejected) {
		t.Fatalf("errors.Is(err, errVendorAuthRejected)=false; err=%v", err)
	}
}

// TestManifestFallbackWithoutManifestFails verifies that auth failure with no
// catalog manifest remains an error; it must not create an empty success.
func TestManifestFallbackWithoutManifestFails(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	tpl := "/models"
	cred := credentialRowLite{baseURL: srv.URL, protocol: "openai-completions", modelsEndpointTpl: &tpl}
	models, source, err := h.resolveModelsForCredential(context.Background(), cred, "bad-key", true)
	if len(models) != 0 || source != "api" {
		t.Fatalf("models=%v source=%q, want empty/api", models, source)
	}
	if !errors.Is(err, errVendorAuthRejected) {
		t.Fatalf("errors.Is(err, errVendorAuthRejected)=false; err=%v", err)
	}
}

// TestClassifyVendorAuthReason pins the short status-code extractor used by
// the credentials.health_error column when the vendor /models endpoint
// rejects our key. The manifest-fallback path added in 2026-08-22 uses
// this so the operator can tell whether to rotate the API key vs. fix a
// different misconfiguration.
func TestClassifyVendorAuthReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: "auth error"},
		{name: "401", err: fmt.Errorf("%w: %d %s", errVendorAuthRejected, 401, "body"), want: "401"},
		{name: "403", err: fmt.Errorf("%w: %d %s", errVendorAuthRejected, 403, "body"), want: "403"},
		{name: "wrapped-but-no-status", err: fmt.Errorf("%w: no status code", errVendorAuthRejected), want: "auth error"},
		{name: "unrelated-error", err: errors.New("connection refused"), want: "auth error"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyVendorAuthReason(tc.err); got != tc.want {
				t.Fatalf("classifyVendorAuthReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestErrVendorAuthRejected_ErrorsIs confirms that fmt.Errorf("%w: ...", errVendorAuthRejected, ...)
// unwraps to the original sentinel via errors.Is, which is what
// discoverAndUpsertForCredential uses to detect 401/403 and switch into
// the manifest-fallback branch.
func TestErrVendorAuthRejected_ErrorsIs(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("%w: %d %s", errVendorAuthRejected, 401, "login fail")
	if !errors.Is(wrapped, errVendorAuthRejected) {
		t.Fatalf("errors.Is did not unwrap to errVendorAuthRejected")
	}

	unrelated := errors.New("connection refused")
	if errors.Is(unrelated, errVendorAuthRejected) {
		t.Fatalf("errors.Is falsely matched unrelated error")
	}
}

// TestEnrollCredentialModels_AllModelsFail_SurfacesFirstError pins the
// 2026-08-22 contract change: when every model in the discovered list
// fails enrollment (canonical upsert OR binding upsert), enrollCredentialModels
// must return a non-nil firstErr so discoverAndUpsertForCredential can
// include the actual underlying cause in its surfaced error — instead of
// the generic "model enrollment failed for every discovered model"
// message that masked the real problem for provider 14 (MiniMax).
func TestEnrollCredentialModels_AllModelsFail_SurfacesFirstError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(mock.Close)

	// Every EnsureCanonicalAndAliases call hits models_canonical. The
	// mock is configured to reject each one with a realistic
	// constraint-violation error so the test mirrors the production
	// symptom (provider 14: every model returned the same upsert error).
	// The SQL has 5 placeholders ($1..$5) and we use AnyArgs so the
	// mock matches every call without us having to reproduce the
	// exact computed family / modality values.
	constraintErr := fmt.Errorf("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")
	for i := 0; i < 3; i++ {
		mock.ExpectQuery(`(?s)INSERT INTO models_canonical.*RETURNING id`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(constraintErr)
	}

	prev := SetRefreshDBOverride(mock)
	t.Cleanup(prev)

	h := &Handler{}
	upserted, failed, firstErr := h.enrollCredentialModels(context.Background(), 99, []string{"minimax-m3", "minimax-m2.7", "minimax-m2.5"})

	if upserted != 0 {
		t.Fatalf("upserted = %d, want 0", upserted)
	}
	if failed != 3 {
		t.Fatalf("failed = %d, want 3 (one per model)", failed)
	}
	if firstErr == nil {
		t.Fatal("firstErr must be non-nil when every model fails so callers can surface the cause")
	}
	if !strings.Contains(firstErr.Error(), "minimax-m3") {
		t.Fatalf("firstErr must reference the first failing model; got %v", firstErr)
	}
	if !strings.Contains(firstErr.Error(), "ensure canonical") {
		t.Fatalf("firstErr must surface the underlying cause; got %v", firstErr)
	}
	if !strings.Contains(firstErr.Error(), "SQLSTATE 23505") {
		t.Fatalf("firstErr must surface the underlying DB error; got %v", firstErr)
	}
}
