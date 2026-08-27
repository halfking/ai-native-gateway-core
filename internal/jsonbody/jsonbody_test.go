package jsonbody

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadOptional_EmptyBodyAccepted(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
	w := httptest.NewRecorder()

	var dst struct {
		Limit int `json:"limit"`
	}
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("empty body must succeed: ok=%v err=%v", ok, err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("empty body must not write a status: got %d", w.Code)
	}
}

func TestReadOptional_NilBodyAccepted(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()

	var dst struct{ X int }
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("nil body must succeed: ok=%v err=%v", ok, err)
	}
}

func TestReadOptional_ValidBodyParsed(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"limit":42}`))
	w := httptest.NewRecorder()

	var dst struct {
		Limit int `json:"limit"`
	}
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("valid body must succeed: ok=%v err=%v", ok, err)
	}
	if dst.Limit != 42 {
		t.Fatalf("dst not populated: %+v", dst)
	}
}

func TestReadOptional_MalformedBodyRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	var dst struct{ X int }
	ok, err := ReadOptional(w, r, &dst)
	if ok || err == nil {
		t.Fatalf("malformed body must fail: ok=%v err=%v", ok, err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body must write 400, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "jsonbody.invalid_body") {
		t.Fatalf("response must carry stable error code, got: %s", body)
	}
}

func TestReadOptional_TooLargeBodyRejected(t *testing.T) {
	// Body bigger than MaxOptionalBody — must surface ErrBodyTooLarge.
	big := strings.Repeat("a", MaxOptionalBody+1024)
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"x":"`+big+`"}`))
	w := httptest.NewRecorder()

	var dst struct{ X string }
	ok, err := ReadOptional(w, r, &dst)
	if ok {
		t.Fatalf("oversize body must fail")
	}
	if !IsBodyTooLarge(err) {
		t.Fatalf("oversize body must surface ErrBodyTooLarge, got %v", err)
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body must write 413, got %d", w.Code)
	}
}

func TestReadOptional_TrailingDataRejected(t *testing.T) {
	// Two top-level JSON values must be rejected (P1-19 strict
	// single-value contract).
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"x":1}{"y":2}`))
	w := httptest.NewRecorder()

	var dst struct{ X int }
	ok, _ := ReadOptional(w, r, &dst)
	if ok {
		t.Fatalf("trailing data must fail")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing data must write 400, got %d", w.Code)
	}
}

// ─── ReadRequired (P1-1) ──────────────────────────────────────────

func TestReadRequired_EmptyBodyRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, err := ReadRequired(w, r, &dst)
	if ok {
		t.Fatalf("required path with empty body must fail")
	}
	if !IsEmptyBody(err) {
		t.Fatalf("err must wrap ErrEmptyBody, got %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("required empty body must write 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "jsonbody.empty_required_body") {
		t.Fatalf("response must carry stable error code, got: %s", w.Body.String())
	}
}

func TestReadRequired_NilBodyRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()

	var dst struct{}
	ok, err := ReadRequired(w, r, &dst)
	if ok {
		t.Fatalf("required path with nil body must fail")
	}
	if !IsEmptyBody(err) {
		t.Fatalf("err must wrap ErrEmptyBody, got %v", err)
	}
}

func TestReadRequired_ValidBodyParsed(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":7}`))
	w := httptest.NewRecorder()

	var dst struct {
		A int `json:"a"`
	}
	ok, err := ReadRequired(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("valid body must succeed: ok=%v err=%v", ok, err)
	}
	if dst.A != 7 {
		t.Fatalf("dst not populated: %+v", dst)
	}
}

func TestReadRequired_MalformedRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	var dst struct{}
	ok, _ := ReadRequired(w, r, &dst)
	if ok {
		t.Fatalf("malformed body must fail")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body must write 400, got %d", w.Code)
	}
}

func TestReadRequired_OversizeRejected(t *testing.T) {
	big := strings.Repeat("a", MaxRequiredBody+1024)
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"x":"`+big+`"}`))
	w := httptest.NewRecorder()

	var dst struct{ X string }
	ok, err := ReadRequired(w, r, &dst)
	if ok {
		t.Fatalf("oversize required body must fail")
	}
	if !IsBodyTooLarge(err) {
		t.Fatalf("err must be ErrBodyTooLarge, got %v", err)
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize required body must write 413, got %d", w.Code)
	}
}

func TestReadRequiredWithLimit_CustomCap(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, _ := ReadRequiredWithLimit(w, r, &dst, 100)
	// 100 bytes is below MaxRequiredBody but the body is small enough.
	// We expect the helper to accept the body (it's not over 100 bytes).
	if !ok {
		t.Fatalf("under-cap body must succeed: %s", w.Body.String())
	}
}
