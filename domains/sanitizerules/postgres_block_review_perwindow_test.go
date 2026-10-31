package sanitizerules

// 逐单窗口候选读面的**真库**判据（2026-10-31 拦截归因轮）。
//
// ★ 为什么需要真库腿：★ 这个读面换了 SQL 形态（★ LATERAL + unnest + 数组
// 绑定 ★）⇒ ★★ 「SQL 形状对不对」与「绑定能不能过去」只有真库能证 ★★
// ⇒ ★ 纯单测（sqlmock）只会断言**我们写的字符串**，★ 证不了 PG认不认 ★。
//
// ★ 钉的四件事：
//	① 逐窗隔离：★ 每个窗口只拿到**自己窗口内**的事件（★ 不串单 ★）
//	② 租户门：★ 别的租户的事件**永远不出现**（★ fail-closed ★）
//	③ 数组绑定：★ 多个窗口一次往返取回（★ 逐窗预算 ★）
//	④ 逐窗预算：★ 撞到 LIMIT 时**只有那一窗**被截断（★ 不牵连别的窗 ★）

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// insertRuleEvent 往事件表塞一行（★ 真库腿专用 ★）。
func insertRuleEvent(t *testing.T, s *PostgresStore, tenant, rule string, at time.Time) {
	t.Helper()
	fingerprint := fmt.Sprintf("fp-%s-%s-%d", tenant, rule, at.UnixNano())
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO sanitize_rule_events
(fingerprint, tenant_id, rule_id, version, phase, action, sensitive_type, field_kind, sample_masked, count, first_seen, last_seen)
VALUES ($1,$2,$3,1,'egress','log','custom','command','[REDACTED:custom]',1,$4,$4)`,
		fingerprint, tenant, rule, at); err != nil {
		t.Fatalf("插入规则事件失败: %v", err)
	}
}

// TestPostgresPerWindowCandidateReadIsolatesWindowsAndTenants 真库腿。
//
// ★ 未设置 SANITIZE_RULES_TEST_DSN 时跳过（★ 沿用仓内既有约定 ★）。
func TestPostgresPerWindowCandidateReadIsolatesWindowsAndTenants(t *testing.T) {
	// ★ 两种查询协议各跑一遍（★ 生产用 simple protocol，★ extended也要能过
	// ★ ⇒ 数组绑定不能只在一种协议下成立 ★）。
	for _, tc := range []struct {
		name string
		mode pgx.QueryExecMode
	}{{"production_simple", pgx.QueryExecModeSimpleProtocol}, {"extended", pgx.QueryExecModeCacheStatement}} {
		t.Run(tc.name, func(t *testing.T) {
			runPerWindowIsolation(t, integrationStoreMode(t, tc.mode))
		})
	}
}

func runPerWindowIsolation(t *testing.T, s *PostgresStore) {
	ctx := context.Background()
	base := time.Date(2026, 10, 29, 8, 45, 0, 0, time.UTC)

	// 三个事件：一远一近一外租户。
	insertRuleEvent(t, s, "default", "pii.server_ip", base)
	insertRuleEvent(t, s, "default", "pii.address.egress", base.Add(2*time.Hour))
	insertRuleEvent(t, s, "other", "pii.email.egress", base)
	insertRuleEvent(t, s, "default", "secret.bearer.egress", base.Add(40*time.Hour))

	windows := []EgressRuleEventWindow{
		{Index: 0, From: base.Add(-15 * time.Minute), To: base.Add(15 * time.Minute)},
		{Index: 1, From: base.Add(40*time.Hour - 15*time.Minute), To: base.Add(40*time.Hour + 15*time.Minute)},
	}
	got, err := s.ListEgressRuleEventsForWindowsDB(ctx, []string{"default"}, windows, 50)
	if err != nil {
		t.Fatalf("ListEgressRuleEventsForWindowsDB: %v", err)
	}

	// ① 逐窗隔离：窗 0 只有 base 那一条（★ 2 小时后的不在窗内 ★）。
	if len(got[0]) != 1 || got[0][0].RuleID != "pii.server_ip" {
		t.Fatalf("窗 0 的候选 = %+v，期望只有 pii.server_ip", got[0])
	}
	// ② 窗 1 只拿到 40 小时后的那一条（★ 旧的不能串进来 ★）。
	if len(got[1]) != 1 || got[1][0].RuleID != "secret.bearer.egress" {
		t.Fatalf("窗 1 的候选 = %+v，期望只有 secret.bearer.egress", got[1])
	}
	// ③ 租户门：别的租户的事件一律不出现。
	for idx, hits := range got {
		for _, h := range hits {
			if h.RuleID == "pii.email.egress" {
				t.Fatalf("窗 %d 出现了别的租户的事件（租户门失效）", idx)
			}
		}
	}
	// ④ 空租户集合 ⇒ fail-closed（返回空，不报错也不放行）。
	empty, err := s.ListEgressRuleEventsForWindowsDB(ctx, nil, windows, 50)
	if err != nil {
		t.Fatalf("空租户集合不应报错: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空租户集合必须 fail-closed（返回空），实际 %+v", empty)
	}
}

// TestPostgresPerWindowCandidateReadTruncatesOnlyItsOwnWindow 真库腿：
// ★ 逐窗预算 ⇒ 一窗撞 LIMIT 不牵连别的窗 ★。
func TestPostgresPerWindowCandidateReadTruncatesOnlyItsOwnWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode pgx.QueryExecMode
	}{{"production_simple", pgx.QueryExecModeSimpleProtocol}, {"extended", pgx.QueryExecModeCacheStatement}} {
		t.Run(tc.name, func(t *testing.T) {
			runPerWindowBudget(t, integrationStoreMode(t, tc.mode))
		})
	}
}

func runPerWindowBudget(t *testing.T, s *PostgresStore) {
	ctx := context.Background()
	base := time.Date(2026, 10, 29, 8, 45, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertRuleEvent(t, s, "default", fmt.Sprintf("rule.busy.%d", i), base.Add(time.Duration(i)*time.Minute))
	}
	insertRuleEvent(t, s, "default", "rule.quiet", base.Add(40*time.Hour))

	windows := []EgressRuleEventWindow{
		{Index: 0, From: base.Add(-15 * time.Minute), To: base.Add(15 * time.Minute)},
		{Index: 1, From: base.Add(40*time.Hour - 15*time.Minute), To: base.Add(40*time.Hour + 15*time.Minute)},
	}
	got, err := s.ListEgressRuleEventsForWindowsDB(ctx, []string{"default"}, windows, 2)
	if err != nil {
		t.Fatalf("ListEgressRuleEventsForWindowsDB: %v", err)
	}
	if len(got[0]) != 2 {
		t.Fatalf("窗 0 应被逐窗预算截断到 2 条，实际 %d", len(got[0]))
	}
	if len(got[1]) != 1 || got[1][0].RuleID != "rule.quiet" {
		t.Fatalf("窗 1 不受窗 0 的预算影响（期望 1 条 rule.quiet），实际 %+v", got[1])
	}
}
