package session

import (
	"context"
	"errors"
	"testing"
)

// stubStore 满足 SessionStore 接口的最小化实现，用于测试 StickyRouter。
type stubStore struct {
	store map[string]*Session
	err   error
}

func newStubStore() *stubStore {
	return &stubStore{store: make(map[string]*Session)}
}

func (s *stubStore) Get(ctx context.Context, sessionID string) (*Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	sess, ok := s.store[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// 1. 新会话返回空字符串
func TestStickyRouter_EmptySessionID(t *testing.T) {
	r := NewStickyRouter(newStubStore())
	cred, err := r.GetPreferredCredential(context.Background(), "")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cred != "" {
		t.Fatalf("cred = %q, want empty", cred)
	}
}

// 2. 有上次凭据的会话返回该 ID
func TestStickyRouter_ExistingSessionWithCredential(t *testing.T) {
	store := newStubStore()
	store.store["s1"] = &Session{SessionID: "s1", LastCredentialID: "cred-99"}
	r := NewStickyRouter(store)

	cred, err := r.GetPreferredCredential(context.Background(), "s1")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cred != "cred-99" {
		t.Fatalf("cred = %q, want cred-99", cred)
	}
}

// 3. 没有凭偏好的会话返回空字符串
func TestStickyRouter_ExistingSessionNoCredential(t *testing.T) {
	store := newStubStore()
	store.store["s1"] = &Session{SessionID: "s1"}
	r := NewStickyRouter(store)

	cred, err := r.GetPreferredCredential(context.Background(), "s1")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cred != "" {
		t.Fatalf("cred = %q, want empty", cred)
	}
}

// 4. store 错误时不报错，返回空字符串
func TestStickyRouter_StoreError(t *testing.T) {
	store := newStubStore()
	store.err = errors.New("redis down")
	r := NewStickyRouter(store)

	cred, err := r.GetPreferredCredential(context.Background(), "s1")
	if err != nil {
		t.Fatalf("err = %v, want nil (吞掉错误返回空字符串)", err)
	}
	if cred != "" {
		t.Fatalf("cred = %q, want empty", cred)
	}
}

// 5. store 中没有该 session（不存在的 ID）→ 空字符串
func TestStickyRouter_UnknownSession(t *testing.T) {
	store := newStubStore()
	r := NewStickyRouter(store)

	cred, err := r.GetPreferredCredential(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if cred != "" {
		t.Fatalf("cred = %q, want empty", cred)
	}
}

// 6. SetPreferredCredential 写入内存 *Session
func TestStickyRouter_SetPreferredCredential(t *testing.T) {
	store := newStubStore()
	store.store["s1"] = &Session{SessionID: "s1"}
	r := NewStickyRouter(store)

	if err := r.SetPreferredCredential(context.Background(), "s1", "cred-X"); err != nil {
		t.Fatalf("SetPreferredCredential err = %v", err)
	}
	cred, _ := r.GetPreferredCredential(context.Background(), "s1")
	if cred != "cred-X" {
		t.Fatalf("cred = %q, want cred-X", cred)
	}
}

// 7. SetPreferredCredential 对空 sessionID 静默成功
func TestStickyRouter_SetPreferredCredential_EmptyID(t *testing.T) {
	store := newStubStore()
	r := NewStickyRouter(store)
	if err := r.SetPreferredCredential(context.Background(), "", "cred-X"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// 8. SetPreferredCredential 对不存在的 session 静默成功
func TestStickyRouter_SetPreferredCredential_UnknownSession(t *testing.T) {
	store := newStubStore()
	r := NewStickyRouter(store)
	if err := r.SetPreferredCredential(context.Background(), "unknown", "cred-X"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
