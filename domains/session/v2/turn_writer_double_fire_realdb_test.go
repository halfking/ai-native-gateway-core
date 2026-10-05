//go:build !integration

package v2

// turn_writer_double_fire_realdb_test.go — 2026-10-05（审计 §9.250.5 盲区 ①）。
//
// # 这道门在守什么
//
// 镜像对**每个请求 fire 两次**（telemetry onPersisted：一次 INSERT-persist、
// 一次 UPDATE-persist，见 turn_writer.go 的 "2026-08-05 (v2 mirror bug)" 注释）。
// 第一次拿到的 `compression_strategy` 是空的（那时压缩器还没跑），
// 第二次才带上真实值。首 fire 的 `ON CONFLICT DO NOTHING` 赢下插入，
// 第二次 fire 的字段本来会被静默丢弃 —— 于是冲突路径上有一整段**单调富化**：
//
//	compression_strategy = COALESCE(NULLIF($6,  ''), compression_strategy)
//	title                = COALESCE(NULLIF($10, ''), title)
//	summary              = COALESCE(NULLIF($11, ''), summary)
//	digest               = CASE WHEN $12 <> '' AND $12 <> 'null' THEN $12::jsonb ELSE digest END
//
// 那句 `COALESCE(NULLIF(...))` 的契约是：**后一次 fire 的空值永远不能把前一次
// 已经填好的值抹掉**（monotonic enrichment）。
//
// # 为什么必须是真库门
//
// `domains/session/v2/turn_writer_dup_test.go` 里那 6 个重复写测试**全部用
// pgxmock**：`RowsAffected` 是 mock 说了算的，它只能证明「UPDATE 语句被发出了」，
// **证明不了值真的落进了行里**。而 `COALESCE(NULLIF(...))` 写错成 `= $6` 时：
//   - 语句照发、参数个数照对 → **那 6 个 mock 测试全绿**；
//   - 真库上后一次空 fire 会把 title 抹成空串 —— 一个**用户可见**的字段消失。
//
// 同族盲区（§9.250.5 已列）：AST 形状判据看不见 SQL 语义。
// 这道门是它的行为侧对照：真跑一次，看**值**。
//
// # 两个用例互为对照，缺一不可
//
//   - 用例 A（阳性）：第二次 fire 带**非空** strategy/title ⇒ 行里必须拿到它。
//     若富化根本没跑，拿到的是第一次 fire 的空串 ⇒ 转红。
//   - 用例 B（★ 阴性对照 / 本门的重点）：第三次 fire **全空** ⇒
//     行里必须**仍然是**第二次 fire 填进去的值。
//     若 `COALESCE(NULLIF(...))` 被写成裸 `= $6`，本用例立刻转红 ——
//     而用例 A 仍然是绿的。**这正是只测 A 会漏掉的那一半。**
//
// # 变异台账（全部基于**能编译且 SQL 合法**的变异）
//
//	M-R1 title 的 COALESCE(NULLIF($10,''), title) → title = $10
//	      ⇒ **只** fire#3 的 ★ 断言转红（实测报第 250 行），fire#2 正常、
//	        用例 A 的 title 断言**未报** ⇒ 证实「A 绿 / B 红」确实可分。
//	M-R2 compression_strategy 的 COALESCE(NULLIF($6,''), …) → 裸赋值
//	      ⇒ fire#3 的 ★ strategy 断言转红（第 245 行）。
//	M-R3 summary 的 COALESCE(NULLIF($11,''), summary) → 裸赋值
//	      ⇒ fire#3 的 ★ summary 断言转红（第 254 行）。
//	还原后复跑 → PASS；`git diff --stat domains/session/v2/turn_writer.go` 为空；
//	本地真库残留 `request_id LIKE 'probe-dblfire-%'` 两张脸各 0 行。
//
// ⚠ **第一版台账全部无效，记在这里以免下一轮重蹈**：
// 我第一次注入时把标记写成 Go 注释 `= $10, // M-R1`，
// 而这三行**位于反引号原始 SQL 字符串内部** ⇒ 注释进了 SQL ⇒
// 真库报 `syntax error at or near "//" (SQLSTATE 42601)`，
// 测试在 **fire#2** 就炸了。**三条变异都红了，但红的理由与判据无关。**
// ⇒ **「转红」还不够，必须核对它是在哪一条断言上红的。**
// 与本仓既有教训同族（§9.250.3：编译失败 ≠ 判据转红）——
// 这里的形态是「SQL 语法失败 ≠ 语义判据转红」。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// realDBPoolForDoubleFire 复用 client_protocol_realdb_test.go 的接入约定：
// 未设 TEST_DATABASE_URL 就 skip，绝不静默通过。
func realDBPoolForDoubleFire(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database double-fire enrichment proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	return pool
}

func TestTurnWriterDoubleFireEnrichmentIsMonotonic_RealDB(t *testing.T) {
	pool := realDBPoolForDoubleFire(t)
	ctx := context.Background()

	// 前提自证：视图与列必须真的在。缺任一项时下面每一步都会以「读到零值」通过，
	// 那是一条恒真的门。
	var hasView, hasCols bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_views
		                 WHERE schemaname='public' AND viewname='session_turns_with_current_month')`).
		Scan(&hasView); err != nil {
		t.Fatalf("probe view: %v", err)
	}
	if !hasView {
		t.Fatalf("public.session_turns_with_current_month 不存在 —— 本门前提不成立，" +
			"它会在缺视图时恒绿。**不要**把恒绿当通过。")
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) = 4 FROM information_schema.columns
		WHERE table_name='session_turns_hot'
		  AND column_name IN ('compression_strategy','title','summary','submit_mode')`).
		Scan(&hasCols); err != nil {
		t.Fatalf("probe columns: %v", err)
	}
	if !hasCols {
		t.Fatalf("session_turns_hot 缺 compression_strategy/title/summary/submit_mode 之一 —— " +
			"本门前提不成立，会恒绿。**不要**把恒绿当通过。")
	}

	stamp := time.Now().UTC().Format("20060102150405.000000000")
	stamp = strings.ReplaceAll(stamp, ".", "")
	reqID := "probe-dblfire-" + stamp
	sessID := "probe-dblfire-sess-" + stamp
	tenant := "probe-dblfire-tenant"

	// 清理必须覆盖两张脸（AppendTurn 走 hot，但分区提升后同一行会出现在母表），
	// 且收尾要验「没留残留」，否则这道门会污染共享真库。
	t.Cleanup(func() {
		for _, tbl := range []string{"public.session_turns", "public.session_turns_hot"} {
			if _, err := pool.Exec(ctx, `DELETE FROM `+tbl+` WHERE request_id = $1`, reqID); err != nil {
				t.Errorf("cleanup %s from %s: %v", reqID, tbl, err)
			}
		}
		var leftover int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM (
			  SELECT request_id FROM session_turns_hot WHERE request_id LIKE 'probe-dblfire-%'
			  UNION ALL SELECT request_id FROM session_turns  WHERE request_id LIKE 'probe-dblfire-%'
			) x`).Scan(&leftover); err != nil {
			t.Errorf("post-cleanup probe: %v", err)
			return
		}
		if leftover != 0 {
			t.Errorf("本门在库里留了 %d 行探针数据（request_id LIKE 'probe-dblfire-%%'）", leftover)
		}
	})

	w := newTurnWriter(pool)
	ts := time.Now().UTC().Truncate(time.Microsecond)

	base := TurnRecord{
		SessionID: sessID,
		TenantID:  tenant,
		RequestID: reqID,
		Ts:        ts,
		// ⚠ DigestJSON 必须自带合法 JSON：AppendTurn 按 `string(rec.DigestJSON)`
		// 传给 jsonb 列，零值 → `''::jsonb` → 22P02。
		DigestJSON: []byte(`{}`),
	}

	// fire #1：镜像的 INSERT-persist。压缩器还没跑，strategy/title 全空。
	fire1 := base
	firstTurnNo, err := w.AppendTurn(ctx, fire1)
	if err != nil {
		t.Fatalf("AppendTurn fire#1: %v", err)
	}

	// fire #2：镜像的 UPDATE-persist。这次带上真实的压缩结论与标题摘要。
	fire2 := base
	// submit_mode 有 CHECK 约束（full/delta/snapshot/inferred_compressed/
	// attachment_only），且富化条件是 `$9 <> '' AND $9 <> 'full'` ——
	// 'full' 属于「不覆盖」的默认值，所以这里必须用一个**非 full** 的合法值。
	fire2.CompressionStrategy = "zstd"
	fire2.CompressionApplied = true
	fire2.Title = "probe-title"
	fire2.Summary = "probe-summary"
	fire2.SubmitMode = "delta"
	secondTurnNo, err := w.AppendTurn(ctx, fire2)
	if err != nil {
		t.Fatalf("AppendTurn fire#2: %v", err)
	}

	if secondTurnNo != firstTurnNo {
		t.Fatalf("同一 request_id 的第二次 fire 换了 turn_no：#1=%d #2=%d —— "+
			"镜像会把同一个请求记成两轮，会话轮次与计费都会错",
			firstTurnNo, secondTurnNo)
	}

	var (
		gotRows                  int
		gotStrategy, gotTitle    string
		gotSummary, gotSubmitMod string
		gotApplied               bool
	)
	// 读 session_turns_with_current_month 而不是裸 hot 表：这道门要验的是
	// **对账侧能看到的那个面**（§9.248.3 已确认它 = hot ∪ archived）。
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER () AS n, COALESCE(compression_strategy,''), COALESCE(title,''),
		       COALESCE(summary,''), COALESCE(submit_mode,''), COALESCE(compression_applied,false)
		  FROM session_turns_with_current_month
		 WHERE tenant_id = $1 AND request_id = $2`, tenant, reqID).
		Scan(&gotRows, &gotStrategy, &gotTitle, &gotSummary, &gotSubmitMod, &gotApplied); err != nil {
		t.Fatalf("read back after fire#2: %v", err)
	}

	// 用例 A（阳性）：晚到的非空字段必须落进行里。
	if gotRows != 1 {
		t.Fatalf("同一 request_id 在 %s 上有 %d 行（应为 1）—— "+
			"重复 fire 不幂等，或张脸不齐", "session_turns_with_current_month", gotRows)
	}
	if gotStrategy != "zstd" {
		t.Errorf("fire#2 的 compression_strategy=%q 没有落库（读到 %q）—— "+
			"冲突路径的富化没有生效，生产上表现为「压缩策略恒为空、每轮都重算」",
			"zstd", gotStrategy)
	}
	if gotTitle != "probe-title" {
		t.Errorf("fire#2 的 title=%q 没有落库（读到 %q）", "probe-title", gotTitle)
	}
	if gotSummary != "probe-summary" {
		t.Errorf("fire#2 的 summary=%q 没有落库（读到 %q）", "probe-summary", gotSummary)
	}
	if gotSubmitMod != "delta" {
		t.Errorf("fire#2 的 submit_mode=%q 没有落库（读到 %q）—— "+
			"submit_mode 会被钉死在第一次 fire 的取值上", "delta", gotSubmitMod)
	}
	if !gotApplied {
		t.Errorf("fire#2 的 compression_applied=true 没有落库（读到 false）")
	}

	// fire #3：★ 阴性对照。全空的后续 fire。
	// 若 `COALESCE(NULLIF(...))` 被写成裸 `= $N`，这一发就会把值抹掉。
	fire3 := base
	if _, err := w.AppendTurn(ctx, fire3); err != nil {
		t.Fatalf("AppendTurn fire#3 (empty re-fire): %v", err)
	}

	var (
		rows3                int
		strategy3, title3    string
		summary3, submitMod3 string
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER () AS n, COALESCE(compression_strategy,''), COALESCE(title,''),
		       COALESCE(summary,''), COALESCE(submit_mode,'')
		  FROM session_turns_with_current_month
		 WHERE tenant_id = $1 AND request_id = $2`, tenant, reqID).
		Scan(&rows3, &strategy3, &title3, &summary3, &submitMod3); err != nil {
		t.Fatalf("read back after fire#3: %v", err)
	}

	if rows3 != 1 {
		t.Fatalf("第三次 fire 之后行数变成 %d（应为 1）", rows3)
	}
	// ↓↓↓ 本门的重点：空 fire 不得抹值。
	if strategy3 != "zstd" {
		t.Errorf("★ 一次全空的 fire#3 把 compression_strategy 从 %q 抹成了 %q —— "+
			"富化不是单调的：COALESCE(NULLIF($6,''), compression_strategy) 退化成了裸赋值。",
			"zstd", strategy3)
	}
	if title3 != "probe-title" {
		t.Errorf("★ 一次全空的 fire#3 把 title 从 %q 抹成了 %q —— "+
			"COALESCE(NULLIF($10,''), title) 退化成了裸赋值。", "probe-title", title3)
	}
	if summary3 != "probe-summary" {
		t.Errorf("★ 一次全空的 fire#3 把 summary 从 %q 抹成了 %q —— "+
			"COALESCE(NULLIF($11,''), summary) 退化成了裸赋值。", "probe-summary", summary3)
	}
	if submitMod3 != "delta" {
		t.Errorf("★ 一次全空的 fire#3 把 submit_mode 从 %q 抹成了 %q —— "+
			"submit_mode 会被钉死在最后一次 fire 的取值上。", "delta", submitMod3)
	}

	t.Logf("双 fire 富化：turn_no 恒为 %d，行数恒为 1；晚到字段已落库，且全空的 fire#3 没有抹掉它们",
		firstTurnNo)
}
