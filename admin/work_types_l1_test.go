package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListL1TaskTypesWithoutDatabaseReturnsCanonicalTypes(t *testing.T) {
	h := NewWorkTypeHandlers(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/work-types/l1-task-types", nil)
	res := httptest.NewRecorder()

	h.listL1TaskTypes(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	var body struct {
		Items []L1TaskTypeMeta `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != len(canonicalL1TaskTypes) {
		t.Fatalf("items = %d, want %d", len(body.Items), len(canonicalL1TaskTypes))
	}
	for i, item := range body.Items {
		if item.Key != canonicalL1TaskTypes[i].Key || item.Count != 0 {
			t.Fatalf("item[%d] = %+v, want key %q and count 0", i, item, canonicalL1TaskTypes[i].Key)
		}
	}
}

func TestListL1TaskTypesRejectsNonGet(t *testing.T) {
	h := NewWorkTypeHandlers(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/work-types/l1-task-types", nil)
	res := httptest.NewRecorder()

	h.listL1TaskTypes(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}
