package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDualReadValidator_Constructor(t *testing.T) {
	v := NewDualReadValidator(nil) // nil pool must not panic
	if v == nil {
		t.Fatal("nil validator")
	}
	// Verify the Compare method exists with the right signature via reflection.
	// NumIn on a bound method value excludes the receiver, so we expect
	// ctx + tenant + session + lastN = 4 inputs.
	rv := reflect.ValueOf(v)
	compare := rv.MethodByName("Compare")
	if !compare.IsValid() {
		t.Fatal("Compare method not set")
	}
	mt := compare.Type()
	if mt.NumIn() != 4 || mt.NumOut() != 2 {
		t.Fatalf("Compare signature mismatch: in=%d out=%d", mt.NumIn(), mt.NumOut())
	}
}

func TestDualReadValidator_NilPoolCompare(t *testing.T) {
	v := NewDualReadValidator(nil)
	_, err := v.Compare(context.Background(), "t1", "s1", 10)
	if err == nil {
		t.Fatal("expected error when pool is nil, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message, got %v", err)
	}
}

// TestDualReadValidator_SummarizeSignature 钉住 population 级漂移度量的入口。
// Compare/CompareDetail 都是单会话的，必须先知道查哪个 session——这正是
// 2026-09-30 审计里镜像漏写长期没被发现的原因。Summarize 是全量视图，
// 少了它「S4 能不能开」就只能靠人临时写 SQL。
func TestDualReadValidator_SummarizeSignature(t *testing.T) {
	v := NewDualReadValidator(nil)
	rv := reflect.ValueOf(v)
	sum := rv.MethodByName("Summarize")
	if !sum.IsValid() {
		t.Fatal("Summarize method not set")
	}
	mt := sum.Type()
	// ctx + tenant + windowHours = 3 in, (*MirrorDriftSummary, error) = 2 out.
	if mt.NumIn() != 3 || mt.NumOut() != 2 {
		t.Fatalf("Summarize signature mismatch: in=%d out=%d", mt.NumIn(), mt.NumOut())
	}
}

func TestDualReadValidator_NilPoolSummarize(t *testing.T) {
	v := NewDualReadValidator(nil)
	if _, err := v.Summarize(context.Background(), "", 168); err == nil {
		t.Fatal("nil pool must error rather than report zero drift; a false zero would read as S4-ready")
	}
}

// TestMirrorDriftClassSQLCoversThreeBuckets 守住「按设计排除」与「真漏写」的区分。
//
// 这个区分是 S4 判据的核心：把 internal_loopback / non_terminal 也算成漏写，
// 会让 93% 的噪声淹没真正的问题行；反过来把 genuine_loss 混进「按设计排除」，
// 就会在数据仍然丢的情况下判定 S4 可开。
func TestMirrorDriftClassSQLCoversThreeBuckets(t *testing.T) {
	// 两个按设计排除臂必须逐条对上 Go 侧的判据来源。2026-09-30 实测踩中：
	// 初版只按 work_type 认内部回环，而 IsInternalAutoEntry 认的是
	// is_auto_request + request_type / origin_actor / task_type。work_type
	// 未打戳的生成器行因此被误判 genuine_loss，s4_ready 永远为假。
	for _, want := range []string{
		"is_auto_request", // IsInternalAutoEntry 首道门（NULL 不算内部回环）
		"origin_actor",    // auto-title/auto-summary/session-summary
		"request_type",    // title_gen / summary
		"task_type",       // taskless auto entry 兜底
		"internal_loopback",
		"non_terminal",
		"genuine_loss",
	} {
		if !strings.Contains(mirrorDriftClassSQL, want) {
			t.Fatalf("class SQL missing %q", want)
		}
	}
	// work_type 不是 Go 侧的排除判据。把它当键会在
	// 「is_auto_request=TRUE + task_type 非空 + work_type=session_title」
	// （业务轮次，hook 照常镜像）上吞掉真漏写。
	if strings.Contains(mirrorDriftClassSQL, "work_type") {
		t.Fatal("class SQL must not key internal_loopback on work_type — the Go gate never reads it, so it would mask real loss")
	}
	// 内部回环臂必须被 is_auto_request 显式门控：NULL 不能算内部回环。
	if !strings.Contains(mirrorDriftClassSQL, "COALESCE(rl.is_auto_request, false)") {
		t.Fatal("internal_loopback arm must be gated on COALESCE(is_auto_request,false); a NULL is_auto_request is not an internal loopback")
	}
	// The scope must anti-join on BOTH session_turns legs; a single leg would
	// miss rows sitting in the hot table and under-report drift.
	for _, want := range []string{"session_turns_hot", "NOT EXISTS"} {
		if !strings.Contains(mirrorDriftScopeSQL, want) {
			t.Fatalf("scope SQL missing %q", want)
		}
	}
	if n := strings.Count(mirrorDriftScopeSQL, "NOT EXISTS"); n != 2 {
		t.Fatalf("scope SQL must anti-join both turns legs, found %d NOT EXISTS", n)
	}
	// 分类臂读到的每一列都必须在 scope 里被投影出来，否则 PG 直接 42703。
	for _, col := range []string{"is_auto_request", "origin_actor", "request_type", "task_type", "success", "error_kind"} {
		if !strings.Contains(mirrorDriftScopeSQL, col) {
			t.Fatalf("scope SQL does not project %q but the class expression reads it", col)
		}
	}
}

// TestMirrorDriftClassSQLHandlesNullWorkType 守住一个会把「问题已修复」伪装出来的
// SQL 三值逻辑陷阱（2026-09-30 审计实测踩中两次）。
//
// genuine_loss 的行 work_type 全是 NULL。若分类写成
// `NOT (work_type IN ('session_title','session_summary'))`：
// NULL IN (...) → NULL，NOT NULL → NULL，整行被 WHERE 丢弃，
// 查询稳定返回 genuine_loss = 0，看上去像漏写已修完。
//
// 正确形态是 CASE WHEN … ELSE 'genuine_loss'：ELSE 分支兜住 NULL。
// 判据用「ELSE 兜底存在」而不是「字符串里没有 NOT (」——后者会因为
// 排版差异而给出假绿。
func TestMirrorDriftClassSQLHandlesNullWorkType(t *testing.T) {
	if !strings.Contains(mirrorDriftClassSQL, "ELSE 'genuine_loss'") {
		t.Fatal("class SQL must fall through to genuine_loss via ELSE so NULL work_type is still counted as loss")
	}
	// 两个按设计排除的分支都必须在 ELSE 之前，且用 IN/= 形式（它们对 NULL
	// 求值为 NULL 从而落入 ELSE，正是我们要的行为）。
	loopbackAt := strings.Index(mirrorDriftClassSQL, "THEN 'internal_loopback'")
	nonTerminalAt := strings.Index(mirrorDriftClassSQL, "THEN 'non_terminal'")
	elseAt := strings.Index(mirrorDriftClassSQL, "ELSE 'genuine_loss'")
	if loopbackAt < 0 || nonTerminalAt < 0 || elseAt < 0 {
		t.Fatalf("class SQL missing a branch: loopback=%d non_terminal=%d else=%d", loopbackAt, nonTerminalAt, elseAt)
	}
	if !(loopbackAt < nonTerminalAt && nonTerminalAt < elseAt) {
		t.Fatalf("branch order wrong: by-design exclusions must precede the genuine_loss ELSE (%d, %d, %d)", loopbackAt, nonTerminalAt, elseAt)
	}
	// 显式禁止把排除条件写成可被 NULL 吞掉的形式。
	if strings.Contains(mirrorDriftClassSQL, "NOT (") {
		t.Fatal("class SQL must not use NOT (... IN ...) — NULL work_type would be silently dropped")
	}
}
