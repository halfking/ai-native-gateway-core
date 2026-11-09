//go:build integration

package modelcredibility

// store_passive_realdb_test.go — passive_model_fingerprints（迁移 955）的
// 真库契约腿：Upsert → Load 往返 + 幂等覆盖 + jsonb 分布保真。
//
// 纯单测拼不出 SQL 的 jsonb 绑定行为（string 通道 vs bytea hex 陷阱
// bfbbb5618）与 ON CONFLICT 语义 —— 必须真库。
//
// 运行方式（store_rollup_integration_test.go 同款专用变量规矩）：
//
//	TEST_PG_CONTRACTS_ISOLATED=1 TEST_MODELCREDIBILITY_PASSIVE_DATABASE_URL=..._test 库 \
//	  go test -tags=integration ./domains/modelcredibility -run TestPassiveBaselineRealPG
//
// ★ 专用变量而非 TEST_DATABASE_URL 的理由（2026-11-01 12h 审计轮先例）：
// 事实源下发的 TEST_DATABASE_URL 指向产品库 llm_gateway ⇒ _test 后缀守卫
// 必然 Fatal ⇒ 与 rollup 腿（TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL）
// 同款处置，登记 scripts/audit/dsn_exclusive_registry.tsv。
//
// 前置：目标 *_test 库已应用迁移 955（passive_model_fingerprints）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openPassiveIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("TEST_PG_CONTRACTS_ISOLATED") != "1" {
		t.Skip("TEST_PG_CONTRACTS_ISOLATED=1 is required; this test mutates an isolated PostgreSQL database")
	}
	dsn := os.Getenv("TEST_MODELCREDIBILITY_PASSIVE_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MODELCREDIBILITY_PASSIVE_DATABASE_URL is not set; skipping PostgreSQL contract test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("test database unreachable: %v", err)
	}
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database identity: %v", err)
	}
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing to run against non-test database %q (must end with _test)", database)
	}
	return pool
}

func TestPassiveBaselineRealPG(t *testing.T) {
	pool := openPassiveIntegrationPool(t)
	ctx := context.Background()
	store := NewStore(pool)

	// 唯一键隔离：用纳秒后缀避免并行跑互相踩。
	model := "realdb-passive-" + time.Now().Format("150405.000000000")
	defer func() {
		_, _ = pool.Exec(ctx,
			`DELETE FROM public.passive_model_fingerprints WHERE model_name = $1`, model)
	}()

	t.Run("roundtrip", func(t *testing.T) {
		in := &PassiveFingerprint{
			TenantID:     "default",
			ModelID:      model,
			CredentialID: 424242,
			TTFTMean:     1000 * time.Millisecond,
			TTFTP95:      1200 * time.Millisecond,
			ITTMean:      45 * time.Millisecond,
			ITTStd:       12 * time.Millisecond,
			ITTP50:       44 * time.Millisecond,
			ITTP95:       68 * time.Millisecond,
			ITTDistribution: map[string]float64{
				"40": 0.30, "50": 0.25, "60": 0.20, "30": 0.15, "70": 0.10,
			},
			ThroughputMean:    22.5,
			ThroughputStd:     3.2,
			OutputDensityMean: 0.8,
			OutputDensityStd:  0.1,
			PromptTokenSignature: map[string]int{
				"abc123": 9,
			},
			FormatFlags:   0x0F,
			SampleCount:   137,
			CollectedFrom: time.Now().Add(-time.Hour),
			CollectedTo:   time.Now(),
		}

		if err := store.UpsertPassiveBaseline(ctx, in); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		out, err := store.LoadPassiveBaseline(ctx, 424242, model)
		if err != nil || out == nil {
			t.Fatalf("load: %v, %+v", err, out)
		}

		// 时序往返（毫秒 real 精度 ⇒ 亚毫秒误差容忍）。
		checkMS := func(name string, got time.Duration, want time.Duration) {
			t.Helper()
			if diff := durationToMS(got) - durationToMS(want); diff < -0.01 || diff > 0.01 {
				t.Errorf("%s roundtrip = %v ms, want %v ms", name, durationToMS(got), durationToMS(want))
			}
		}
		checkMS("ttft_mean", out.TTFTMean, in.TTFTMean)
		checkMS("ttft_p95", out.TTFTP95, in.TTFTP95)
		checkMS("itt_mean", out.ITTMean, in.ITTMean)
		checkMS("itt_std", out.ITTStd, in.ITTStd)
		checkMS("itt_p50", out.ITTP50, in.ITTP50)
		checkMS("itt_p95", out.ITTP95, in.ITTP95)

		// 分布往返（键集与概率保真）。
		if len(out.ITTDistribution) != len(in.ITTDistribution) {
			t.Fatalf("itt distribution keys = %v, want %v", out.ITTDistribution, in.ITTDistribution)
		}
		for k, want := range in.ITTDistribution {
			got, ok := out.ITTDistribution[k]
			if !ok || diffAbs(got, want) > 1e-9 {
				t.Errorf("itt distribution[%q] = %v (ok=%v), want %v", k, got, ok, want)
			}
		}

		if diffAbs(out.ThroughputMean, in.ThroughputMean) > 1e-9 {
			t.Errorf("throughput_mean = %v, want %v", out.ThroughputMean, in.ThroughputMean)
		}
		if out.SampleCount != in.SampleCount {
			t.Errorf("sample_count = %d, want %d", out.SampleCount, in.SampleCount)
		}
		if out.FormatFlags != in.FormatFlags {
			t.Errorf("format_flags = %#x, want %#x", out.FormatFlags, in.FormatFlags)
		}
		if len(out.PromptTokenSignature) != 1 || out.PromptTokenSignature["abc123"] != 9 {
			t.Errorf("prompt_token_signature roundtrip = %v", out.PromptTokenSignature)
		}
		if out.CollectedFrom.IsZero() || out.CollectedTo.IsZero() {
			t.Errorf("collected window lost: from=%v to=%v", out.CollectedFrom, out.CollectedTo)
		}
	})

	t.Run("upsert_overwrites", func(t *testing.T) {
		second := &PassiveFingerprint{
			TenantID:      "default",
			ModelID:       model,
			CredentialID:  424242,
			TTFTMean:      500 * time.Millisecond,
			SampleCount:   50,
			CollectedFrom: time.Now().Add(-time.Minute),
			CollectedTo:   time.Now(),
		}
		if err := store.UpsertPassiveBaseline(ctx, second); err != nil {
			t.Fatalf("second upsert: %v", err)
		}
		out, err := store.LoadPassiveBaseline(ctx, 424242, model)
		if err != nil || out == nil {
			t.Fatalf("reload: %v, %+v", err, out)
		}
		if diffAbs(durationToMS(out.TTFTMean), 500) > 0.01 {
			t.Errorf("after overwrite ttft_mean = %v ms, want 500", durationToMS(out.TTFTMean))
		}
		if out.SampleCount != 50 {
			t.Errorf("after overwrite sample_count = %d, want 50", out.SampleCount)
		}
		// 覆盖后 ITT 分布应为空（新行没带）。
		if len(out.ITTDistribution) != 0 {
			t.Errorf("after overwrite itt_distribution = %v, want empty", out.ITTDistribution)
		}
	})

	t.Run("load_missing_returns_nil_nil", func(t *testing.T) {
		out, err := store.LoadPassiveBaseline(ctx, 999999999, "no-such-model")
		if err != nil {
			t.Fatalf("load missing: %v", err)
		}
		if out != nil {
			t.Errorf("load missing = %+v, want nil,nil", out)
		}
	})

	t.Run("collector_flush_end_to_end", func(t *testing.T) {
		// 全链路：Collector.Add × N → Flush → 库里读回合并基线。
		c := NewPassiveCollector()
		for _, pf := range makeFeatures(model, 424243, 45, 1000, 30) {
			c.Add(pf)
		}
		flushed, err := c.Flush(ctx, store)
		if err != nil {
			t.Fatalf("flush: %v", err)
		}
		if flushed != 1 {
			t.Fatalf("flushed = %d, want 1", flushed)
		}
		out, err := store.LoadPassiveBaseline(ctx, 424243, model)
		if err != nil || out == nil {
			t.Fatalf("load after flush: %v, %+v", err, out)
		}
		if diffAbs(durationToMS(out.TTFTMean), 1000) > 0.01 {
			t.Errorf("end-to-end ttft_mean = %v ms, want 1000", durationToMS(out.TTFTMean))
		}
		if out.SampleCount != 30 {
			t.Errorf("end-to-end sample_count = %d, want 30", out.SampleCount)
		}
		if len(out.ITTDistribution) == 0 {
			t.Error("end-to-end itt_distribution empty")
		}
	})
}

func diffAbs(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
