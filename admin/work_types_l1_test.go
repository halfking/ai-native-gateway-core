// work_types_l1_test.go — tests for GET /api/admin/work-types/l1-task-types
// and the pure mergeL1TaskTypes function it uses internally.
//
// Tests inject the l1CountsFetch hook (no pgxmock / DB pool needed),
// keeping the suite fast and runnable in any environment.
package admin

import (
	"context"
	"encoding/json"
	"errors"
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

func stubL1Handler(counts map[string]int, err error) *WorkTypeHandlers {
	h := NewWorkTypeHandlers(nil)
	h.l1CountsFetch = func(_ context.Context) (map[string]int, error) {
		return counts, err
	}
	return h
}

func decodeL1Response(t *testing.T, res *httptest.ResponseRecorder) []L1TaskTypeMeta {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Items []L1TaskTypeMeta `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Items
}

func l1ByKey(items []L1TaskTypeMeta) map[string]L1TaskTypeMeta {
	out := make(map[string]L1TaskTypeMeta, len(items))
	for _, it := range items {
		out[it.Key] = it
	}
	return out
}

func TestListL1TaskTypesDbCountsOverlayCanonical(t *testing.T) {
	h := stubL1Handler(map[string]int{
		"code":       5,
		"chat":       3,
		"multimodal": 7,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/work-types/l1-task-types", nil)
	res := httptest.NewRecorder()
	h.listL1TaskTypes(res, req)

	items := decodeL1Response(t, res)
	byKey := l1ByKey(items)

	if got := byKey["code"]; got.Count != 5 || got.Icon != "\U0001f4bb" || got.Label != "代码" {
		t.Fatalf("canonical[code] = %+v, want count=5 icon=💻 label=代码", got)
	}
	if got := byKey["chat"]; got.Count != 3 || got.Icon != "\U0001f4ac" || got.Label != "通用对话" {
		t.Fatalf("canonical[chat] = %+v, want count=3 icon=💬 label=通用对话", got)
	}
	if got := byKey["vision"]; got.Count != 0 {
		t.Fatalf("canonical[vision].count = %d, want 0", got.Count)
	}
	if got, ok := byKey["multimodal"]; !ok {
		t.Fatalf("multimodal missing from response, items=%+v", items)
	} else if got.Count != 7 || got.Icon != "\u25c6" || got.Label != "multimodal" {
		t.Fatalf("multimodal = %+v, want count=7 icon=◆ label=multimodal", got)
	}
	if len(items) != len(canonicalL1TaskTypes)+1 {
		t.Fatalf("items count = %d, want %d", len(items), len(canonicalL1TaskTypes)+1)
	}
}

func TestListL1TaskTypesDbQueryErrorFallsBackToCanonical(t *testing.T) {
	h := stubL1Handler(nil, errors.New("simulated connection refused"))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/work-types/l1-task-types", nil)
	res := httptest.NewRecorder()
	h.listL1TaskTypes(res, req)

	items := decodeL1Response(t, res)
	if len(items) != len(canonicalL1TaskTypes) {
		t.Fatalf("items = %d, want %d (must fall back to canonical even on DB error)",
			len(items), len(canonicalL1TaskTypes))
	}
	for _, it := range items {
		if it.Count != 0 {
			t.Fatalf("after DB error, item %+v should have count=0", it)
		}
	}
}

func TestListL1TaskTypesExtraKeysSorted(t *testing.T) {
	h := stubL1Handler(map[string]int{
		"zeta":  1,
		"alpha": 1,
		"mike":  1,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/work-types/l1-task-types", nil)
	res := httptest.NewRecorder()
	h.listL1TaskTypes(res, req)

	items := decodeL1Response(t, res)
	if len(items) != len(canonicalL1TaskTypes)+3 {
		t.Fatalf("items = %d, want %d", len(items), len(canonicalL1TaskTypes)+3)
	}
	extras := items[len(canonicalL1TaskTypes):]
	gotKeys := []string{extras[0].Key, extras[1].Key, extras[2].Key}
	want := []string{"alpha", "mike", "zeta"}
	for i := range gotKeys {
		if gotKeys[i] != want[i] {
			t.Fatalf("extras order = %v, want %v", gotKeys, want)
		}
	}
}

func TestMergeL1TaskTypes(t *testing.T) {
	t.Run("empty db counts → canonical only", func(t *testing.T) {
		got := mergeL1TaskTypes(nil)
		if len(got) != len(canonicalL1TaskTypes) {
			t.Fatalf("len = %d, want %d", len(got), len(canonicalL1TaskTypes))
		}
		for _, it := range got {
			if it.Count != 0 {
				t.Fatalf("count = %d for %s, want 0", it.Count, it.Key)
			}
		}
	})
	t.Run("canonical key with count overlay", func(t *testing.T) {
		got := mergeL1TaskTypes(map[string]int{"code": 42})
		byKey := l1ByKey(got)
		if byKey["code"].Count != 42 {
			t.Fatalf("code.count = %d, want 42", byKey["code"].Count)
		}
		if byKey["chat"].Count != 0 {
			t.Fatalf("chat.count = %d, want 0 (not in dbCounts)", byKey["chat"].Count)
		}
	})
	t.Run("extra key gets diamond icon + key-as-label", func(t *testing.T) {
		got := mergeL1TaskTypes(map[string]int{"vision_audio": 9})
		byKey := l1ByKey(got)
		v := byKey["vision_audio"]
		if v.Icon != "\u25c6" {
			t.Fatalf("icon = %q, want ◆", v.Icon)
		}
		if v.Label != "vision_audio" {
			t.Fatalf("label = %q, want vision_audio (key as label fallback)", v.Label)
		}
		if v.Count != 9 {
			t.Fatalf("count = %d, want 9", v.Count)
		}
	})
	t.Run("empty string db counts ignored (defensive)", func(t *testing.T) {
		got := mergeL1TaskTypes(map[string]int{"": 99, "code": 1})
		byKey := l1ByKey(got)
		if _, ok := byKey[""]; ok {
			t.Fatalf("empty key leaked into response: %+v", byKey[""])
		}
		if byKey["code"].Count != 1 {
			t.Fatalf("code.count = %d, want 1", byKey["code"].Count)
		}
	})
}
