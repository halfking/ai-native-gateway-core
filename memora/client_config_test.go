// Package memora - client_config_test.go (2026-06-20)
//
// Unit tests for the M1 dual-host configuration: SmartSearchBaseURL +
// SmartSearchAPIKey override BaseURL/APIKey only for /api/smart_search.
// The legacy Search()/Add()/Ping() path must keep using BaseURL.
package memora

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestClient_SmartSearchUsesSmartBaseURL verifies that when SmartSearchBaseURL
// is set, SmartSearch POSTs to it (not to BaseURL).
func TestClient_SmartSearchUsesSmartBaseURL(t *testing.T) {
	var smartHits, baseHits int32
	smartSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&smartHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"reranked":[{"id":"s1","memory":"smart","score":0.9}]}`))
	}))
	defer smartSrv.Close()
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"text_mem":[{"cube_id":"kb","memories":[{"id":"b1","memory":"base","relativity":0.5}]}]}}`))
	}))
	defer baseSrv.Close()

	c := NewClient(ClientConfig{
		BaseURL: baseSrv.URL, APIKey: "base-key",
		SmartSearchBaseURL: smartSrv.URL, SmartSearchAPIKey: "smart-key",
	})
	mems, err := c.SmartSearch(context.Background(), "tenant-A", "q", 8)
	if err != nil {
		t.Fatalf("SmartSearch err: %v", err)
	}
	if len(mems) != 1 || mems[0].Text != "smart" {
		t.Fatalf("expected 1 smart hit, got %+v", mems)
	}
	if atomic.LoadInt32(&smartHits) != 1 {
		t.Errorf("expected 1 smart hit, got %d", smartHits)
	}
	if atomic.LoadInt32(&baseHits) != 0 {
		t.Errorf("SmartSearch should not hit BaseURL (got %d base hits)", baseHits)
	}
}

// TestClient_SearchUsesBaseURLOnly verifies that legacy Search() never
// touches SmartSearchBaseURL (no cross-contamination).
func TestClient_SearchUsesBaseURLOnly(t *testing.T) {
	var smartHits, baseHits int32
	smartSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&smartHits, 1)
		w.Write([]byte(`{"reranked":[]}`))
	}))
	defer smartSrv.Close()
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"text_mem":[{"cube_id":"kb","memories":[{"id":"b1","memory":"base","relativity":0.5}]}]}}`))
	}))
	defer baseSrv.Close()

	c := NewClient(ClientConfig{BaseURL: baseSrv.URL, SmartSearchBaseURL: smartSrv.URL})
	mems, err := c.Search(context.Background(), "tenant-B", "q", 8)
	if err != nil || len(mems) != 1 || mems[0].Text != "base" {
		t.Fatalf("Search bad: %v %+v", err, mems)
	}
	if atomic.LoadInt32(&smartHits) != 0 {
		t.Errorf("Search must not hit SmartSearchBaseURL (got %d)", smartHits)
	}
	if atomic.LoadInt32(&baseHits) != 1 {
		t.Errorf("expected 1 base hit, got %d", baseHits)
	}
}

// TestClient_SmartSearchFallbackOnSmartError verifies that when the smart
// endpoint errors, we fall back to user-scoped single-vector search on
// the legacy BaseURL — preserving the multi-tenant safety guarantee.
func TestClient_SmartSearchFallbackOnSmartError(t *testing.T) {
	var smartHits, baseHits int32
	smartSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&smartHits, 1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer smartSrv.Close()
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"text_mem":[{"cube_id":"kb","memories":[{"id":"b1","memory":"fallback","relativity":0.5}]}]}}`))
	}))
	defer baseSrv.Close()

	c := NewClient(ClientConfig{BaseURL: baseSrv.URL, SmartSearchBaseURL: smartSrv.URL})
	mems, err := c.SmartSearch(context.Background(), "tenant-C", "q", 8)
	if err != nil || len(mems) != 1 || mems[0].Text != "fallback" {
		t.Fatalf("fallback bad: %v %+v", err, mems)
	}
	if atomic.LoadInt32(&smartHits) < 1 {
		t.Errorf("expected >=1 smart attempt, got %d", smartHits)
	}
	if atomic.LoadInt32(&baseHits) < 1 {
		t.Errorf("expected fallback to base, got %d base hits", baseHits)
	}
}

// TestClient_SmartSearchEmptyConfigUsesBaseURL verifies that when
// SmartSearchBaseURL is not configured, SmartSearch falls back to BaseURL
// (single-host legacy mode).
func TestClient_SmartSearchEmptyConfigUsesBaseURL(t *testing.T) {
	var baseHits int32
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&baseHits, 1)
		// Serve both /api/smart_search (empty reranked) AND /product/search.
		switch {
		case len(r.URL.Path) >= len("/api/smart_search") && r.URL.Path[len(r.URL.Path)-len("/api/smart_search"):] == "/api/smart_search":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"reranked":[]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":0,"data":{"text_mem":[{"cube_id":"kb","memories":[{"id":"b1","memory":"single-host","relativity":0.5}]}]}}`))
		}
	}))
	defer baseSrv.Close()

	c := NewClient(ClientConfig{BaseURL: baseSrv.URL}) // no SmartSearchBaseURL
	mems, err := c.SmartSearch(context.Background(), "tenant-D", "q", 8)
	if err != nil || len(mems) != 1 || mems[0].Text != "single-host" {
		t.Fatalf("single-host bad: %v %+v", err, mems)
	}
	if atomic.LoadInt32(&baseHits) < 1 {
		t.Errorf("expected >=1 base hit, got %d", baseHits)
	}
}