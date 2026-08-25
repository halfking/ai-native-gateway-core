package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
)

func TestHandleUnifiedRequestDetailFromMemory(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := "in_progress"
	meta := requestdetail.Meta{RequestID: "req-unified-01", TenantID: "default", Status: &status}
	bodies := requestdetail.Bodies{RequestBody: json.RawMessage(`{"messages":[]}`)}
	if err := store.Put(meta, &bodies); err != nil {
		t.Fatal(err)
	}
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-unified-01", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got requestdetail.Detail
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != requestdetail.SourceFile && got.Source != requestdetail.SourceMemory {
		t.Fatalf("unexpected source %s", got.Source)
	}
	if got.Persistence != requestdetail.PersistenceInFlight {
		t.Fatalf("unexpected persistence %s", got.Persistence)
	}
}

func TestHandleUnifiedRequestDetailNotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/req-x", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d", rr.Code)
	}
}
