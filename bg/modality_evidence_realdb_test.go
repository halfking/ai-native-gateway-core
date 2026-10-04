// modality_evidence_realdb_test.go — R24 审计（2026-10-05）真库回归。
//
// modality 证据列（carry_evidence/read_evidence）的参数编码缺陷只能对真 PG
// 复现：pgxmock / 参数捕获单测都测不出 SimpleProtocol 内联语义（与 R22
// capability evidence_json 同根，252 真库 4.4h 窗口 6 次写入全败 invalid
// input syntax for type json，modality 判级与 canonical 回写一并丢失）。
// 正反对照：经 capabilityEvidenceParam 包装的 string 插 jsonb 必须成功；
// 裸 []byte 必须炸 invalid input syntax（负例同时证明池确实是
// SimpleProtocol——若该模式失效，守卫会以「负例未引爆」红灯而不是静默放过）。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（bg 真库回归惯例，同
// capability_evidence_realdb_test.go）。
package bg

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestModalityEvidenceParamRealDB(t *testing.T) {
	pool := simpleProtocolPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := pool.Exec(ctx, `CREATE TEMP TABLE r24_modality_probe (carry_evidence jsonb, read_evidence jsonb)`); err != nil {
		t.Fatalf("create temp probe table: %v", err)
	}

	// 正例：persistRow 同形 —— 两个证据列都经 capabilityEvidenceParam。
	if _, err := pool.Exec(ctx,
		`INSERT INTO r24_modality_probe VALUES ($1, $2)`,
		capabilityEvidenceParam([]byte(`{"level":"accepted","http_status":200}`)),
		capabilityEvidenceParam([]byte(`{"level":"confirmed","position_score":0.9}`)),
	); err != nil {
		t.Fatalf("wrapped evidence insert failed: %v", err)
	}
	var carry, read string
	if err := pool.QueryRow(ctx, `SELECT carry_evidence::text, read_evidence::text FROM r24_modality_probe`).Scan(&carry, &read); err != nil {
		t.Fatalf("read back evidence: %v", err)
	}
	if !strings.Contains(carry, `"accepted"`) || !strings.Contains(read, `"confirmed"`) {
		t.Fatalf("evidence roundtrip mismatch: carry=%q read=%q", carry, read)
	}

	// 负例（对照证明）：裸 []byte → bytea hex 字面量，jsonb 解析必炸。
	// 变异验证：若有人把 persistRow 的 $10/$11 改回裸证据，此缺陷形态即
	// 本负例展示的失败。
	_, err := pool.Exec(ctx, `INSERT INTO r24_modality_probe VALUES ($1, $2)`,
		[]byte(`{"probe":"hex"}`), []byte(`{"probe":"hex2"}`))
	if err == nil {
		t.Fatal("raw []byte evidence insert unexpectedly succeeded — " +
			"pool is not SimpleProtocol; this realdb regression is not exercising the production mode")
	}
	if !strings.Contains(err.Error(), "invalid input syntax") {
		t.Fatalf("raw []byte evidence failed with unexpected error (want invalid input syntax): %v", err)
	}
}
