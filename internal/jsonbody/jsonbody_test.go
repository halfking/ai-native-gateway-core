package jsonbody

import (
	"bytes"
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

// ─── 2026-08-27 audit fix: compatibility behaviours ───────────────

func TestReadOptional_BOMBodyAccepted(t *testing.T) {
	// Windows tooling and several SDKs prepend a UTF-8 BOM. Go's
	// encoding/json rejects it outright; the audit fix strips exactly
	// one leading BOM so those senders keep working.
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"limit":7}`)...)
	r := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	w := httptest.NewRecorder()

	var dst struct {
		Limit int `json:"limit"`
	}
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("BOM-prefixed body must succeed: ok=%v err=%v", ok, err)
	}
	if dst.Limit != 7 {
		t.Fatalf("dst not populated through BOM: %+v", dst)
	}
}

func TestReadRequired_BOMBodyAccepted(t *testing.T) {
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":3}`)...)
	r := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	w := httptest.NewRecorder()

	var dst struct {
		A int `json:"a"`
	}
	ok, err := ReadRequired(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("BOM-prefixed required body must succeed: ok=%v err=%v", ok, err)
	}
	if dst.A != 3 {
		t.Fatalf("dst not populated: %+v", dst)
	}
}

func TestReadRequired_NullBodyRejectedAsEmpty(t *testing.T) {
	// A literal `null` decodes "successfully" into a zero-valued
	// struct, which downstream code mistakes for a real empty payload.
	// Treat it as no-body for required endpoints.
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`null`))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, err := ReadRequired(w, r, &dst)
	if ok {
		t.Fatalf("null body must be rejected on required endpoints")
	}
	if !IsEmptyBody(err) {
		t.Fatalf("err must wrap ErrEmptyBody, got %v", err)
	}
}

func TestReadOptional_NullBodyTreatedAsEmpty(t *testing.T) {
	// Optional endpoints: `null` carries no data, so proceed with the
	// zero value — same as an empty body.
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`null`))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("null body must be accepted on optional endpoints: ok=%v err=%v", ok, err)
	}
	if dst.A != 0 {
		t.Fatalf("dst must stay zero-valued, got %+v", dst)
	}
}

func TestReadOptional_WhitespaceOnlyBodyAccepted(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("  \n\t  "))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, err := ReadOptional(w, r, &dst)
	if !ok || err != nil {
		t.Fatalf("whitespace-only body must be accepted: ok=%v err=%v", ok, err)
	}
}

func TestDecodeBody_TrailingStillRejected(t *testing.T) {
	// Compatibility must not weaken the strict single-value contract:
	// two concatenated documents stay rejected even after BOM
	// handling was added.
	err := DecodeBody([]byte(`{"a":1}{"b":2}`), &struct{ A int }{})
	if err == nil {
		t.Fatalf("trailing data must still be rejected")
	}
}

func TestDecodeBody_NullSentinel(t *testing.T) {
	err := DecodeBody([]byte(`  null  `), &struct{}{})
	if !IsEmptyBody(err) {
		t.Fatalf("DecodeBody(null) must return ErrEmptyBody, got %v", err)
	}
}

func TestDecodeBody_RejectsMalformedTrailingData(t *testing.T) {
	for _, body := range []string{
		`{"a":1}garbage`,
		`{"a":1}{`,
		`null garbage`,
	} {
		if err := DecodeBody([]byte(body), &struct{ A int }{}); err == nil {
			t.Errorf("trailing input must be rejected: %q", body)
		}
	}
}

func TestDecodeBody_AcceptsTrailingWhitespace(t *testing.T) {
	var dst struct {
		A int `json:"a"`
	}
	if err := DecodeBody([]byte("{\"a\":1} \n\t"), &dst); err != nil {
		t.Fatalf("trailing whitespace must be accepted: %v", err)
	}
	if dst.A != 1 {
		t.Fatalf("body was not decoded: %+v", dst)
	}
}
