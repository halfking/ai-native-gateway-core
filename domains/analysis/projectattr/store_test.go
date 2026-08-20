package projectattr

import (
	"reflect"
	"strings"
	"testing"
)

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

// TestStoreInterface_HasNoSessionDimMutationMethod 是 plan 红线的回归测试。
//
// 计划红线：推断结果只写 session_project_attribution，**绝不写**
// session_dim.project_id。session_dim.project_id 由 ACC 认领路径（migration
// 407）写入，计费口径依赖它；掺入 ~85% 准确率的猜测会让所有下游数字失去
// 可审计性。
//
// 防护策略：把"无写入通道"编码到接口本身。如果 Store 接口没有
// UpdateSessionDim / SetSessionProjectId / 之类的方法，调用方根本拿不到
// 写入句柄。本测试用反射枚举 Store 的全部方法，断言：
//
//  1. 没有名字看起来能写 session_dim 的方法（黑名单词表）。
//  2. 已有的 5 个方法都是"读"或"写 session_project_attribution"。
//
// 这样即便将来有人新增 Store 方法，CI 会在 review 前就拦截掉越界写入。
// 见 attributor.go:11-14 的注释：本包完全不介入 session_dim.project_id。
func TestStoreInterface_HasNoSessionDimMutationMethod(t *testing.T) {
	// 黑名单：任何带这些词的方法都不该出现在 Store 接口里。
	forbidden := []string{
		"UpdateSessionDim",
		"SetSessionProjectId",
		"SetSessionProject",
		"WriteSessionDim",
		"UpsertSessionDim",
		"MutateSessionDim",
	}

	storeType := reflect.TypeOf((*Store)(nil)).Elem()
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		for _, bad := range forbidden {
			if strings.Contains(m.Name, bad) {
				t.Errorf("Store interface exposes %q — this is a red-line violation. "+
					"Use session_project_attribution for inferred values, not session_dim.", m.Name)
			}
		}
	}

	// 顺带枚举当前 5 个方法的语义方向，确认每个不是 mutation-to-session_dim。
	// 写操作白名单：仅允许以 session_project_attribution 为目标（按命名约定
	// "SaveAttribution"）。如未来新增 Store 方法且包含 UPDATE/INSERT/MERGE
	// 等词，必须 review 这条断言。
	writeVerbs := []string{"Update", "Set", "Upsert", "Insert", "Merge", "Delete", "Mutate"}
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		for _, verb := range writeVerbs {
			if strings.HasPrefix(m.Name, verb) {
				t.Errorf("Store method %q starts with write verb %q — review whether this method targets session_dim. "+
					"If it targets session_project_attribution, add it to the explicit allowlist below.",
					m.Name, verb)
			}
		}
	}
}

// TestPGStore_OnlyWritesToSessionProjectAttribution 是结构性断言的姊妹：
// 锁住 PGStore 的方法集合必须严格等于白名单。任何越界新增方法都会被这条
// 测试拦截（结合上一条的接口级锁），共同保证"任何代码路径都不会触碰
// session_dim"。store.go 的方法列表是手工维护的 source-of-truth；若新增
// 方法，必须同时更新这里的白名单 + 在 PR 描述里说明目标表。
func TestPGStore_OnlyWritesToSessionProjectAttribution(t *testing.T) {
	// 用指针类型，否则 pointer-receiver 方法不出现。
	pgType := reflect.TypeOf((*PGStore)(nil))

	allowed := map[string]bool{
		"HasAuthoritativeProject": true,
		"HasAttribution":          true,
		"SaveAttribution":         true,
		"LoadProjects":            true,
		"LoadSignals":             true,
	}

	// 1. PGStore 的每个方法都必须在白名单里。
	for i := 0; i < pgType.NumMethod(); i++ {
		m := pgType.Method(i)
		if !allowed[m.Name] {
			t.Errorf("PGStore exposes %q which is not in the red-line allowlist. "+
				"If this method targets session_dim, it violates the inference-vs-claim red line. "+
				"If it targets session_project_attribution, add it to the allowlist in store_test.go "+
				"and document in PR.", m.Name)
		}
	}

	// 2. 白名单里的每个方法都必须在 PGStore 上实现（防止白名单陈旧）。
	for name := range allowed {
		if _, ok := pgType.MethodByName(name); !ok {
			t.Errorf("allowlist contains %q but PGStore does not implement it; "+
				"remove from allowlist or restore the method on PGStore", name)
		}
	}

	// 3. 黑名单：方法名不能包含 session_dim 写入语义。
	forbidden := []string{
		"UpdateSessionDim", "SetSessionProjectId", "SetSessionProject",
		"WriteSessionDim", "UpsertSessionDim", "MutateSessionDim",
		"InsertSessionDim",
	}
	for i := 0; i < pgType.NumMethod(); i++ {
		m := pgType.Method(i)
		for _, bad := range forbidden {
			if strings.Contains(m.Name, bad) {
				t.Errorf("PGStore method %q matches forbidden pattern %q — red-line violation", m.Name, bad)
			}
		}
	}
}
