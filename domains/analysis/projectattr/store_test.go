package projectattr

import "testing"

// PgxQuerier 必须真正满足 Querier。这条断言是对上一版缺陷的回归防护：
// 当时只定义了 Querier 接口却没有适配层，pgxpool.Pool 的 Exec/Query 签名
// 与之不符，NewPGStore(pool) 根本无法编译——因为没人构造过，CI 也没发现。
var _ Querier = (*PgxQuerier)(nil)

// nil 池不应 panic，让上层可以按配置禁用整个功能。
func TestNewPgxQuerier_NilPool(t *testing.T) {
	if q := NewPgxQuerier(nil); q != nil {
		t.Fatalf("NewPgxQuerier(nil) = %v, want nil", q)
	}
	if s := NewPGStoreFromPool(nil); s != nil {
		t.Fatalf("NewPGStoreFromPool(nil) = %v, want nil", s)
	}
}

// 未配置存储层时返回 ErrNoStore 而不是 panic。
func TestPGStore_NilQuerierReturnsErrNoStore(t *testing.T) {
	var s *PGStore
	if _, err := s.HasAttribution(t.Context(), "t", "gw"); err != ErrNoStore {
		t.Fatalf("HasAttribution err = %v, want ErrNoStore", err)
	}
	if _, err := s.HasAuthoritativeProject(t.Context(), "t", "gw"); err != ErrNoStore {
		t.Fatalf("HasAuthoritativeProject err = %v, want ErrNoStore", err)
	}
	if _, err := s.LoadSignals(t.Context(), "t", "gw"); err != ErrNoStore {
		t.Fatalf("LoadSignals err = %v, want ErrNoStore", err)
	}
	if _, err := s.LoadProjects(t.Context(), "t"); err != ErrNoStore {
		t.Fatalf("LoadProjects err = %v, want ErrNoStore", err)
	}
	if err := s.SaveAttribution(t.Context(), "t", "gw", Result{}); err != ErrNoStore {
		t.Fatalf("SaveAttribution err = %v, want ErrNoStore", err)
	}
}

// InheritFromHistory 在没有 Querier 或没有指纹时安全返回空。
func TestInheritFromHistory_SafeWithoutInputs(t *testing.T) {
	f := InheritFromHistory(nil, 0)
	ref, err := f(t.Context(), Signals{IdentityHash: "fp"})
	if ref != "" || err != nil {
		t.Fatalf("got (%q, %v), want empty", ref, err)
	}
}
