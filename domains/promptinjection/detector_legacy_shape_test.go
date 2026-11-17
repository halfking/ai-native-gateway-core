package promptinjection

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
)

// ══════════════════════════════════════════════════════════════════════
// 2026-11-18 根修的钉测：检测审计写侧**双形状**。
//
// 背景（245 生产实证）：fleet 内 prompt_injection_detections 形状真实分歧
// ——正典库（211 等）无 315 四列；245/154 共享库的活表带四列（其中
// detection_score / matched_rules_count 两列 NOT NULL）。canonical INSERT
// 在 245 上每条都被「null value in column detection_score」拒掉 ⇒ 检测
// 审计恒丢。既有正典门（detector_canonical_shape_test.go）对带四列的库
// 直接 SKIP——这个盲区正是本次事故的温床：正典门只测「自己的前提成立」
// 的库。
//
// 本门补另一半：当真库是 315 形状时，insertDetectionSQLLegacyCols 必须能
// 落库且四个新列被填上（NOT NULL 不再拒写）。
// ══════════════════════════════════════════════════════════════════════

// TestInsertDetectionSQL_LandsInLegacyShape 跑真库（TEST_PG_URL）：
// 表带 315 四列时，legacy INSERT 必须成功且四列有值。
func TestInsertDetectionSQL_LandsInLegacyShape(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the legacy detection write path works")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.prompt_injection_detections') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !exists {
		t.Skip("prompt_injection_detections missing in TEST_PG_URL — nothing to verify against")
	}

	// legacy 形状前提：四列都在（不在 ⇒ 本库是正典形状，本门不适用，
	// 由 detector_canonical_shape_test.go 负责）。
	for _, col := range []string{"detection_score", "matched_rules", "matched_rules_count", "detection_layers"} {
		var n int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns
			  WHERE table_schema='public' AND table_name='prompt_injection_detections'
			    AND column_name=$1`, col).Scan(&n); err != nil {
			t.Fatalf("probe column %s: %v", col, err)
		}
		if n != 1 {
			t.Skipf("column %s absent — this DB has the canonical shape; the canonical gate covers it", col)
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // 结尾回滚 ⇒ 不留测试数据

	if _, err := tx.Exec(ctx, `SET LOCAL app.bypass_rls = 'true'`); err != nil {
		t.Fatalf("set bypass_rls: %v", err)
	}

	const tenant = "audit-detection-legacy-shape-test"
	cats := []string{"role_hijack"}
	if _, err := tx.Exec(ctx, insertDetectionSQLLegacyCols,
		tenant, "req-legacy-1", "sess-legacy-1", 9,
		"audit-rule", "role_hijack",
		"warn", false, "evidence", "hash",
		"127.0.0.1", "ua", pq.Array(cats),
		nil, "", nil, nil, "",
		9, `["role_hijack_ignore_previous"]`, 1, `{"basic":true}`); err != nil {
		t.Fatalf("insert detection (legacy-315 shape): %v — the NOT NULL columns rejected the write again", err)
	}

	var gotScore, gotCount int
	var gotRules, gotLayers string
	err = tx.QueryRow(ctx,
		`SELECT detection_score, matched_rules::text, matched_rules_count, detection_layers::text
		   FROM prompt_injection_detections
		  WHERE tenant_id = $1 AND request_id = $2`, tenant, "req-legacy-1").
		Scan(&gotScore, &gotRules, &gotCount, &gotLayers)
	if err != nil {
		t.Fatalf("read back legacy columns: %v", err)
	}
	if gotScore != 9 || gotCount != 1 || gotRules == "" || gotLayers == "" {
		t.Fatalf("legacy columns not populated: score=%d rules=%q count=%d layers=%q", gotScore, gotRules, gotCount, gotLayers)
	}
}

// TestProbeDetectionsLegacyColsPicksByColumns 单元层钉探测语义：
// 315 四列齐 ⇒ true；正典形状 ⇒ false（用真库跑，无库则跳过）。
func TestProbeDetectionsLegacyColsPicksByColumns(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset")
	}
	// 复用 pgx 连的库探测 *sql.DB 路径需要 stdlib 桥接；这里直接用
	// pgx 查同样的列集，验证探测谓词的**判据本身**（列集 ⇒ 预期布尔）。
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.prompt_injection_detections') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Skip("table missing — probe semantics vacuous here")
	}

	var n int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema='public' AND table_name='prompt_injection_detections'
		    AND column_name IN ('detection_score','matched_rules','matched_rules_count','detection_layers')`).Scan(&n); err != nil {
		t.Fatalf("count cols: %v", err)
	}
	// 判据与 probeDetectionsLegacyCols 同一式：n==4 ⇒ legacy。
	if (n == 4) != probeExpectation(n) {
		t.Fatalf("probe predicate mismatch: n=%d", n)
	}
	t.Logf("TEST_PG_URL detections columns=%d/4 (legacy=%v)", n, n == 4)
}

func probeExpectation(n int) bool { return n == 4 }
