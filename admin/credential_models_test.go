package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateProviderOffer_RequiresCredentialID(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/providers/1/models/", bytes.NewReader([]byte(`{}`)))
	h.createProviderOffer(rr, req, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
}

func TestCreateProviderOffer_RejectsMissingCredentialIDEvenWithRaw(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/providers/1/models/",
		bytes.NewReader([]byte(`{"raw_model_name":"gpt-4o"}`)))
	h.createProviderOffer(rr, req, 1)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
}
