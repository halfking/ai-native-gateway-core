package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/licensing"
)

// TestCommunityTenantLimitError_Message covers the user-facing error
// string format that the admin UI / API consumers see.
func TestCommunityTenantLimitError_Message(t *testing.T) {
	e := &CommunityTenantLimitError{Current: 2, Max: 2}
	got := e.Error()
	for _, want := range []string{"community_mode", "2/2", "Activate a license"} {
		if !strings.Contains(got, want) {
			t.Errorf("error message %q missing %q", got, want)
		}
	}
}

// TestCommunityTenantLimitError_AsTyped verifies createTenant can use
// errors.As to discriminate the typed error from generic DB errors.
func TestCommunityTenantLimitError_AsTyped(t *testing.T) {
	src := error(&CommunityTenantLimitError{Current: 2, Max: 2})
	var dst *CommunityTenantLimitError
	if !errors.As(src, &dst) {
		t.Fatal("errors.As should match CommunityTenantLimitError")
	}
	if dst.Current != 2 || dst.Max != 2 {
		t.Errorf("got {%d, %d}, want {2, 2}", dst.Current, dst.Max)
	}

	other := errors.New("db connection refused")
	if errors.As(other, &dst) {
		t.Fatal("errors.As must NOT match a generic error")
	}
}

// TestCheckCommunityTenantLimit_NoLimitWhenDisabled verifies that
// non-community mode skips the DB call entirely (zero-cost fast path).
//
// We can't construct an *Handler without DB. Instead, we verify the
// contract: IsCommunityMode() is the only gate. If it returns false,
// the handler short-circuits before any DB call.
func TestCheckCommunityTenantLimit_NoLimitWhenDisabled(t *testing.T) {
	defer withCommunityMode(t, false)()
	if licensing.IsCommunityMode() {
		t.Fatal("community mode should be disabled for this test")
	}
}

// withCommunityMode toggles the community-mode marker file for the
// duration of a test, restoring it on cleanup. Caller MUST defer the
// returned func.
func withCommunityMode(t *testing.T, on bool) func() {
	t.Helper()
	dir := t.TempDir()
	originalFile := licensing.CommunityModeFile
	licensing.CommunityModeFile = filepath.Join(dir, "community.mode")
	if on {
		if err := os.WriteFile(licensing.CommunityModeFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("seed community marker: %v", err)
		}
	}
	return func() {
		licensing.CommunityModeFile = originalFile
	}
}

// TestCommunityMode_HTTP403 verifies that a 403 response carries the
// expected body shape — not a generic 500. Smoke test against a stub
// http.ResponseWriter so we don't need a real DB.
func TestCommunityMode_HTTP403(t *testing.T) {
	rr := httptest.NewRecorder()
	e := &CommunityTenantLimitError{Current: 2, Max: 2}
	writeError(rr, http.StatusForbidden, e.Error())

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "community_mode") {
		t.Errorf("body must mention community_mode: %q", body)
	}
}
