package credentialhealth

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// 本文件钉的是 2026-11-17「节点状态漂移根修轮」的两处守卫改动（F3），
// 以及配套的凭据级守卫（F4）。
//
// 背景（211 真库取证，2026-11-17 01:09 ~ 01:25 CST）：
//
//  ① `unavailable_reason <> 'model_probe_broken'` 是 SQL 三值逻辑陷阱 ——
//     reason 为 NULL 时求值为 NULL ⇒ **整行被排除**。而 model_offers 镜像恢复
//     （moExpiredRecoverySQL，靠 INSTEAD OF UPDATE 触发器回写）恰恰把 reason
//     清成 NULL ⇒ **被镜像恢复过的行从此再也进不了 cmb 侧恢复语句**
//     ⇒ 它的 unavailable_recover_at 永远清不掉 ⇒ 行上留着一个已过期的冷却
//     地平线（实测 289 行 available=TRUE 却带着已过期的 unavailable_recover_at，
//     分布在 8 个凭据上）⇒ 这是一条随镜像恢复次数单调累积的漂移来源。
//
//  ② cmb 侧恢复语句原本带 `FROM provider_models pm` 内连接（对 cmb 自身更新
//     毫无语义用途）⇒ 把**父行已删的孤儿绑定**整类挡在恢复之外；model_offers
//     侧（视图同样是内连接）也看不到 ⇒ 双盲 ⇒ 永久卡在 available=FALSE
//     （实测 45 行孤儿绑定，其中 32 行 unavailable）。
//
//  ③ F4：`auto_credential_%` 这个 reason 表达的是「**整个凭据**坏了」
//     ⇒ 它的恢复条件应当是**凭据级**的。没有这条守卫时，凭据还欠着配额
//     （suspended / periodic_exhausted）、它的绑定却已被冷却到期恢复成
//     available=true ⇒ 绑定面说可用、凭据面说不可用 ⇒ 又一处面间漂移。
//
// 四层钉：
//   - 形状钉（无外部依赖）：守卫字面量 ＋ 孤儿盲区字面量 ＋ 人工意图守卫。
//   - 真库语义腿（TEST_PG_DSN）：逐条断言语义 ＋ 幂等。
//   - 负控：**独立重放**的旧 SQL ⇒ 证明新语句做到的两件事旧语句做不到。
//     （负控必须与生产路径隔离，否则生产路径先跑完会把夹具状态改掉，
//       负控就变成在断言「已经被修好的状态」。）

// legacyCmbExpiredRecoverySQL 是 F3 修复前的 cmb 侧语句（负控用）。
//
// ★ 它是本测试的「非空转证明」：如果旧语句也能做到下面两件事，那么断言本身
//
//	就是废的（AGENTS.md 负控纪律）。
func legacyCmbExpiredRecoverySQL() string {
	return `
		UPDATE credential_model_bindings cmb
		SET available              = TRUE,
		    unavailable_reason     = NULL,
		    unavailable_at         = NULL,
		    unavailable_recover_at = NULL,
		    updated_at             = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND cmb.unavailable_reason <> 'model_probe_broken'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_recover_at,
		               cmb.unavailable_at + INTERVAL '30 seconds') IS NOT NULL
		  AND COALESCE(cmb.unavailable_recover_at,
		               cmb.unavailable_at + INTERVAL '30 seconds') < now()
		  AND EXISTS (
		    SELECT 1 FROM credentials c
		    WHERE c.id = cmb.credential_id
		      AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  )
	`
}

// TestCmbExpiredRecoverySQL_Shape pins the guard fixes at the SQL level.
//
// 这一层不依赖数据库 ⇒ 它是「无论有没有真库都能跑」的最小保证。
func TestCmbExpiredRecoverySQL_Shape(t *testing.T) {
	sql := cmbExpiredRecoverySQL()

	// ① NULL 三值逻辑陷阱必须消失：守卫必须 COALESCE 化。
	if !strings.Contains(sql, "COALESCE(cmb.unavailable_reason, '') <> 'model_probe_broken'") {
		t.Errorf("cmb 恢复语句的守卫必须 COALESCE 化：\n%s", sql)
	}
	// 反向钉：旧的 NULL 陷阱写法不得复活。
	if strings.Contains(sql, "cmb.unavailable_reason <> 'model_probe_broken'") {
		t.Errorf("cmb 恢复语句复活了 NULL 三值逻辑陷阱（reason 为 NULL 时整行被排除）：\n%s", sql)
	}

	// ② 孤儿盲区必须消失：cmb 自身 UPDATE 不得再内连接 provider_models。
	if strings.Contains(sql, "FROM provider_models pm") {
		t.Errorf("cmb 恢复语句不得内连接 provider_models（孤儿绑定会被整类挡在恢复之外）：\n%s", sql)
	}

	// ③ F4：凭据级归因必须配「凭据已 ready」守卫（cmb 侧与镜像侧都要）。
	for _, want := range []string{
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'auto_credential_%'",
		"c2.availability_state = 'ready'",
		"c2.lifecycle_status = 'active'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("cmb 恢复语句缺少 F4 守卫 %q：\n%s", want, sql)
		}
	}

	// ④ 人工意图守卫必须保留（恢复不得覆盖人工下线/人工保护）。
	for _, want := range []string{
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("cmb 恢复语句丢失守卫 %q：\n%s", want, sql)
		}
	}
}

// TestMoExpiredRecoverySQL_Shape pins the mirror-side guard alignment.
//
// 镜像语句写的是 model_offers 视图，**靠 INSTEAD OF UPDATE 触发器回写 cmb**，
// 而触发器的 SET 列表没有 unavailable_recover_at ⇒ 该语句永远清不掉冷却地平线
// ⇒ 闭环必须靠「cmb 侧先执行且守卫已 COALESCE 化」。
func TestMoExpiredRecoverySQL_Shape(t *testing.T) {
	sql := moExpiredRecoverySQL()

	if !strings.Contains(sql, "COALESCE(mo.unavailable_reason, '') <> 'model_probe_broken'") {
		t.Errorf("model_offers 镜像恢复语句的守卫必须 COALESCE 化（与 cmb 侧对齐）：\n%s", sql)
	}
	if strings.Contains(sql, "mo.unavailable_reason <> 'model_probe_broken'") {
		t.Errorf("model_offers 镜像恢复语句复活了 NULL 三值逻辑陷阱：\n%s", sql)
	}
	// F4：镜像侧必须与 cmb 侧同款守卫，否则镜像会把 cmb 侧刻意留住的行放回来。
	for _, want := range []string{
		"COALESCE(mo.unavailable_reason, '') NOT LIKE 'auto_credential_%'",
		"c2.availability_state = 'ready'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("model_offers 镜像恢复语句缺少 F4 守卫 %q：\n%s", want, sql)
		}
	}
	// 镜像语句只恢复三列（触发器只回写这三列 + updated_at）。
	if !strings.Contains(sql, "unavailable_at     = NULL") {
		t.Errorf("model_offers 镜像恢复语句形状漂移：\n%s", sql)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 真库语义腿
// ─────────────────────────────────────────────────────────────────────────────

type guardFixture struct {
	id            int
	name          string
	available     bool
	reason        *string
	unavailAt     *time.Time
	recoverAt     *time.Time
	orphan        bool // provider_model_id 指向不存在的父行
	credID        int64
	expectAvail   bool // 终态 available
	expectCleared bool // 终态 unavailable_recover_at 是否应被清空
}

const (
	credReady     = int64(10) // ready + active
	credManualOff = int64(11) // ready + manual_disabled
	credSuspended = int64(12) // suspended（欠配额）+ active
)

func TestCmbExpiredRecoverySQL_RealPG(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("TEST_PG_DSN 未设置 —— 本次没有跑真库语义腿")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	const schema = "cmb_recovery_guard_test"
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	// 最小夹具：只保留被生产 SQL 触碰的列。
	if _, err := conn.Exec(ctx, `
		CREATE TABLE provider_models (id BIGINT PRIMARY KEY);
		CREATE TABLE credentials (
			id BIGINT PRIMARY KEY,
			manual_disabled BOOLEAN NOT NULL DEFAULT FALSE,
			availability_state TEXT NOT NULL DEFAULT 'ready',
			lifecycle_status TEXT NOT NULL DEFAULT 'active'
		);
		CREATE TABLE credential_model_bindings (
			id BIGSERIAL PRIMARY KEY,
			credential_id BIGINT NOT NULL,
			provider_model_id BIGINT NOT NULL,
			available BOOLEAN NOT NULL DEFAULT TRUE,
			unavailable_reason TEXT,
			unavailable_at TIMESTAMPTZ,
			unavailable_recover_at TIMESTAMPTZ,
			admin_protected BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		INSERT INTO provider_models (id) VALUES (1);
		INSERT INTO credentials (id, manual_disabled, availability_state, lifecycle_status) VALUES
			(10, FALSE, 'ready',     'active'),
			(11, TRUE,  'ready',     'active'),
			(12, FALSE, 'suspended', 'active');
	`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	strPtr := func(s string) *string { return &s }
	timePtr := func(t time.Time) *time.Time { return &t }

	fixtures := []guardFixture{
		{
			name:          "drift_available_null_reason",
			available:     true,
			reason:        nil,
			recoverAt:     timePtr(past),
			credID:        credReady,
			expectAvail:   true,
			expectCleared: true, // ★ 已过期地平线必须被清（F3 核心收益）
		},
		{
			name:          "orphan_binding_down",
			available:     false,
			reason:        strPtr("auto_credential_quota_permanent"),
			unavailAt:     timePtr(past),
			recoverAt:     timePtr(past),
			orphan:        true,
			credID:        credReady,
			expectAvail:   true, // ★ 孤儿行必须能被恢复（F3 第二项）
			expectCleared: true,
		},
		{
			name:        "manual_pinned",
			available:   false,
			reason:      strPtr("manual_offline"),
			unavailAt:   timePtr(past),
			recoverAt:   timePtr(past),
			credID:      credReady,
			expectAvail: false,
		},
		{
			name:        "probe_broken_pinned",
			available:   false,
			reason:      strPtr("model_probe_broken"),
			unavailAt:   timePtr(past),
			recoverAt:   timePtr(past),
			credID:      credReady,
			expectAvail: false,
		},
		{
			name:        "future_cooldown",
			available:   false,
			reason:      strPtr("auto_timeout"),
			unavailAt:   timePtr(past),
			recoverAt:   timePtr(future),
			credID:      credReady,
			expectAvail: false,
		},
		{
			name:        "credential_manual_disabled",
			available:   false,
			reason:      strPtr("auto_timeout"),
			unavailAt:   timePtr(past),
			recoverAt:   timePtr(past),
			credID:      credManualOff,
			expectAvail: false,
		},
		{
			name:          "credential_level_reason_cred_ready",
			available:     false,
			reason:        strPtr("auto_credential_quota_permanent"),
			unavailAt:     timePtr(past),
			recoverAt:     timePtr(past),
			credID:        credReady,
			expectAvail:   true, // ★ 凭据已 ready ⇒ 凭据级归因的绑定随之恢复
			expectCleared: true,
		},
		{
			name:        "credential_level_reason_cred_suspended",
			available:   false,
			reason:      strPtr("auto_credential_quota_permanent"),
			unavailAt:   timePtr(past),
			recoverAt:   timePtr(past),
			credID:      credSuspended,
			expectAvail: false, // ★ 凭据还欠配额 ⇒ 绑定不得先被放回（F4）
		},
	}

	insert := func(f *guardFixture) {
		pmID := int64(1)
		if f.orphan {
			pmID = 999999 // 父行不存在
		}
		var reasonArg, unavailArg, recoverArg any
		if f.reason != nil {
			reasonArg = *f.reason
		}
		if f.unavailAt != nil {
			unavailArg = *f.unavailAt
		}
		if f.recoverAt != nil {
			recoverArg = *f.recoverAt
		}
		if err := conn.QueryRow(ctx, `
			INSERT INTO credential_model_bindings
				(credential_id, provider_model_id, available, unavailable_reason,
				 unavailable_at, unavailable_recover_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id
		`, f.credID, pmID, f.available, reasonArg, unavailArg, recoverArg).Scan(&f.id); err != nil {
			t.Fatalf("insert %s: %v", f.name, err)
		}
	}
	for i := range fixtures {
		insert(&fixtures[i])
	}

	// ── 生产 SQL：逐条断言 ───────────────────────────────────────────────
	tag, err := conn.Exec(ctx, cmbExpiredRecoverySQL())
	if err != nil {
		t.Fatalf("production sql: %v", err)
	}
	if tag.RowsAffected() < 3 {
		t.Errorf("生产 SQL 只命中 %d 行，期望 ≥3（漂移清理 + 孤儿恢复 + 凭据级恢复）", tag.RowsAffected())
	}
	for _, f := range fixtures {
		assertFixture(t, conn, f, "production/"+f.name, f.expectAvail, f.expectCleared)
	}

	// 幂等：再跑一次不应再命中任何行（清完地平线后谓词不再成立）。
	tag2, err := conn.Exec(ctx, cmbExpiredRecoverySQL())
	if err != nil {
		t.Fatalf("production sql (2nd): %v", err)
	}
	if tag2.RowsAffected() != 0 {
		t.Errorf("生产 SQL 不幂等：第二次仍命中 %d 行（一次性清理的自激风险）", tag2.RowsAffected())
	}

	// ── 负控：**独立重放**旧 SQL，证明它做不到那两件事 ──────────────────
	//   （重放而不是复用上面的夹具：生产路径已经把它们改掉了。）
	neg := []guardFixture{
		{
			name:        "neg_drift_available_null_reason",
			available:   true,
			reason:      nil,
			recoverAt:   timePtr(past),
			credID:      credReady,
			expectAvail: true,
		},
		{
			name:      "neg_orphan_binding_down",
			available: false,
			reason:    strPtr("auto_credential_quota_permanent"),
			unavailAt: timePtr(past),
			recoverAt: timePtr(past),
			orphan:    true,
			credID:    credReady,
		},
	}
	for i := range neg {
		insert(&neg[i])
	}
	if _, err := conn.Exec(ctx, legacyCmbExpiredRecoverySQL()); err != nil {
		t.Fatalf("legacy sql: %v", err)
	}
	// 旧 SQL 清不掉漂移行的过期地平线（NULL 三值逻辑陷阱）。
	assertFixture(t, conn, neg[0], "legacy/drift_available_null_reason", true, false)
	// 旧 SQL 恢复不了孤儿绑定（provider_models 内连接）。
	assertFixture(t, conn, neg[1], "legacy/orphan_binding_down", false, false)
}

// assertFixture 断言一行绑定的 available 与 unavailable_recover_at 终态。
func assertFixture(t *testing.T, conn *pgx.Conn, f guardFixture, label string, wantAvail, wantRecoverCleared bool) {
	t.Helper()
	var avail bool
	var recoverAt *string
	err := conn.QueryRow(context.Background(), `
		SELECT available, unavailable_recover_at::text
		FROM credential_model_bindings WHERE id = $1
	`, f.id).Scan(&avail, &recoverAt)
	if err != nil {
		t.Fatalf("%s: scan: %v", label, err)
	}
	if avail != wantAvail {
		t.Errorf("%s: available = %v，期望 %v", label, avail, wantAvail)
	}
	cleared := recoverAt == nil
	if wantRecoverCleared && !cleared {
		t.Errorf("%s: unavailable_recover_at 仍是 %s（期望被清空 —— 这正是漂移残留的那一列）",
			label, *recoverAt)
	}
	if !wantRecoverCleared && cleared {
		t.Errorf("%s: unavailable_recover_at 被误清（期望保留）", label)
	}
}
