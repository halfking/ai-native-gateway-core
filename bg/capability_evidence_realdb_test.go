// capability_evidence_realdb_test.go — R22 审计（2026-10-03）真库回归。
//
// persistRow 的 $4（evidence_json）参数编码缺陷只能对真 PG 复现：pgxmock /
// 参数捕获单测都测不出 SimpleProtocol 内联语义（R11 FIX-C 同根，252 生产
// 18h 窗口 761 次写入全败 invalid input syntax for type json）。本文件在
// SimpleProtocol 池上做正反对照：string 参数插入 jsonb 必须成功；裸 []byte
// 必须炸 invalid input syntax（负例同时证明池确实是 SimpleProtocol——若该
// 模式失效，守卫会以「负例未引爆」红灯而不是静默放过）。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（bg 真库回归惯例，同
// sql_audit_realdb_test.go）。
package bg

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCapabilityEvidenceParamRealDB(t *testing.T) {
	pool := simpleProtocolPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE r22_evidence_probe (evidence jsonb)`); err != nil {
		t.Fatalf("create temp probe table: %v", err)
	}

	// 正例：string 参数 → jsonb 解析成功，值可回读。
	if _, err := pool.Exec(ctx,
		`INSERT INTO r22_evidence_probe VALUES ($1)`,
		capabilityEvidenceParam([]byte(`{"probe_mode":"responses_nonstream","body_sample":"ok"}`)),
	); err != nil {
		t.Fatalf("string evidence insert failed: %v", err)
	}
	var back string
	if err := pool.QueryRow(ctx, `SELECT evidence::text FROM r22_evidence_probe`).Scan(&back); err != nil {
		t.Fatalf("read back evidence: %v", err)
	}
	if !strings.Contains(back, `"body_sample"`) {
		t.Fatalf("evidence roundtrip mismatch: %q", back)
	}

	// 负例（对照证明）：裸 []byte → bytea hex 字面量，jsonb 解析必炸。
	// 变异验证：若有人把 persistRow 的 $4 改回裸 evidence，此缺陷形态即
	// 本负例展示的失败。
	_, err := pool.Exec(ctx, `INSERT INTO r22_evidence_probe VALUES ($1)`, []byte(`{"probe":"hex"}`))
	if err == nil {
		t.Fatal("raw []byte evidence insert unexpectedly succeeded — " +
			"pool is not SimpleProtocol; this realdb regression is not exercising the production mode")
	}
	if !strings.Contains(err.Error(), "invalid input syntax") {
		t.Fatalf("raw []byte evidence failed with unexpected error (want invalid input syntax): %v", err)
	}
}
