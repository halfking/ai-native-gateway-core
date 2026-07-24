package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttachmentSignURL_RoundTrip(t *testing.T) {
	h := NewAttachmentHandler(nil, []byte("att-key"))
	signed, err := h.signForTest("default", "s1", 3, "att_xyz", "obj/path", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if signed == "" {
		t.Fatal("empty signed url")
	}
}
func TestAttachmentSignURL_ExpiredReturns410(t *testing.T) {
	h := NewAttachmentHandler(nil, []byte("att-key"))
	r := h.Routes()
	signed, _ := h.signForTest("default", "s1", 3, "att_xyz", "obj/path", time.Microsecond)
	time.Sleep(2 * time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/signed?p="+signed, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusGone {
		t.Fatalf("expected 410 (Gone) for expired, got %d", rr.Code)
	}
}
func TestAttachmentSignURL_BadSignature(t *testing.T) {
	h := NewAttachmentHandler(nil, []byte("att-key"))
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/signed?p=invalid.badsig", nil))
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusForbidden {
		t.Fatalf("expected 400/403 for bad sig, got %d", rr.Code)
	}
}
