package session

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestNewCacheInjector(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	if ci == nil || ci.sessionMgr != mgr {
		t.Fatal("NewCacheInjector did not set sessionMgr")
	}
}

func TestCacheInjector_InjectCacheParams_EmptySessionID(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	got, err := ci.InjectCacheParams(context.Background(), "", body, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_NilCandidate(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	got, err := ci.InjectCacheParams(context.Background(), "s", body, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_NotSupportCache(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: false}
	got, err := ci.InjectCacheParams(context.Background(), "s", body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_SessionNotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true}
	got, err := ci.InjectCacheParams(context.Background(), "nonexistent", body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged")
	}
}

func TestCacheInjector_InjectCacheParams_BadJSON(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`not json`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "checkpoint"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(body) {
		t.Fatal("body should be unchanged on bad JSON")
	}
}

func TestCacheInjector_InjectCacheParams_CheckpointMode(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "checkpoint"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for checkpoint mode")
	}
}

func TestCacheInjector_InjectCacheParams_TokensModeNewMeta(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "tokens"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for tokens mode (new meta)")
	}
}

func TestCacheInjector_InjectCacheParams_TokensModeExistingMeta(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"metadata":{"cache_control":{"type":"old"}}}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "tokens"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) == string(body) {
		t.Fatal("body should be modified for tokens mode (existing meta)")
	}
}

func TestCacheInjector_InjectCacheParams_HeaderMode(t *testing.T) {
	mgr, _ := newTestManager(t)
	ci := NewCacheInjector(mgr)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	body := []byte(`{"x":1}`)
	cand := &provider.Candidate{SupportsPromptCache: true, CacheMode: "header"}
	got, err := ci.InjectCacheParams(ctx, sess.SessionID, body, cand)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// header mode is a no-op
	if string(got) != string(body) {
		t.Fatal("body should be unchanged for header mode")
	}
}
