package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSON_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteJSON(rec, http.StatusCreated, "application/json", map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.Code; got != http.StatusCreated {
		t.Errorf("status = %d, want %d", got, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	// Trailing newline parity with json.Encoder / the old hand-written helpers.
	if got, want := rec.Body.String(), "{\"k\":\"v\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteJSON_CharsetVariant(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusOK, "application/json; charset=utf-8", struct{}{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
	}
	if got, want := rec.Body.String(), "{}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestWriteJSON_EmptyContentTypeLeavesHeaderUnset(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusOK, "", map[string]int{"n": 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); got != "" {
		t.Errorf("Content-Type = %q, want empty (sniffed by net/http)", got)
	}
	if !strings.HasSuffix(rec.Body.String(), "\n") {
		t.Errorf("body %q missing trailing newline", rec.Body.String())
	}
}

func TestWriteJSON_MarshalErrorCommitsNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	err := WriteJSON(rec, http.StatusOK, "application/json", make(chan int))
	if err == nil {
		t.Fatal("expected marshalling error for unsupported type")
	}
	// Nothing written: caller is free to emit its own error response.
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if c := rec.Header().Get("Content-Type"); c != "" {
		t.Errorf("Content-Type = %q, want unset", c)
	}
}

// TestWriteJSON_HTMLEscapingParity guards the byte-level equivalence with
// json.NewEncoder(w).Encode used by the previous streaming helpers: both
// escape <, >, & by default.
func TestWriteJSON_HTMLEscapingParity(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, http.StatusOK, "application/json", map[string]string{"q": "<a&b>"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := rec.Body.String(), "{\"q\":\"\\u003ca\\u0026b\\u003e\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
