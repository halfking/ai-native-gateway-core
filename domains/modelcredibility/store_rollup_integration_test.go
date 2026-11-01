//go:build integration

package modelcredibility

// store_rollup_integration_test.go — RollupProviderModel 的真库契约测试。
//
// ★ 钉的判据（2026-10-31 审计轮修复）：分数与结论必须取自**同一行**
// （最差凭据的完整判定），样本数是该 (provider, model) 组的全组计数。
// 旧实现 MIN(score) + MIN(verdict) 各自独立聚合，会拼出数据里不存在的
// 组合（如 40 分配 diluted）——纯单测拼不出 SQL 聚合行为，必须真库。
//
// 运行方式（照搬 maas/bills_realdb_test.go 的专用变量规矩）：
//
//	TEST_PG_CONTRACTS_ISOLATED=1 TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL=..._test 库 \
//	  go test -tags=integration ./domains/modelcredibility -run TestRollupProviderModelRealPG
//
// ★ 为什么用**专用变量**而不是事实源统一下发的 TEST_DATABASE_URL（2026-11-01 12h 审计轮）：
//
//	事实源 scripts/audit/datasource_env.sh 下发的 TEST_DATABASE_URL 指向**产品库**
//	（库名 llm_gateway）⇒ 这条腿的 `current_database()` + `_test` 后缀守卫**必然 Fatal**
//	⇒ 门 `TestDatasourceEnvTargetGuardNoUnregisteredMismatch`（判据：事实源承载的名字被
//	一条「按库名做决定」的腿真读 ⇒ 事实源给的值这条腿一定会拒绝）**整包报红**。
//	⇒ 与 maas 月账单 e2e（TEST_MAAS_BILLS_DATABASE_URL）同款处置：
//	专用名 + 登记 scripts/audit/dsn_exclusive_registry.tsv + sql/schema 车道登记。
//
// 前置：目标 *_test 库已应用迁移 922（provider_model_credibility）。

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openRollupIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("TEST_PG_CONTRACTS_ISOLATED") != "1" {
		t.Skip("TEST_PG_CONTRACTS_ISOLATED=1 is required; this test mutates an isolated PostgreSQL database")
	}
	dsn := os.Getenv("TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL is not set; skipping PostgreSQL contract test")
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
		t.Fatalf("refusing mutating PostgreSQL contract test against database %q; use a dedicated *_test database", database)
	}
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.provider_model_credibility') IS NOT NULL`).Scan(&ok); err != nil || !ok {
		t.Fatalf("provider_model_credibility missing; apply migration 922 to the isolated database")
	}
	return pool
}

func insertRollupVerdict(t *testing.T, pool *pgxpool.Pool, credID, provID int64, model, verdict string, score float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_model_credibility
		    (tenant_id, credential_id, provider_id, raw_model_name,
		     credibility_score, verdict, status, measured_at)
		VALUES ('default', $1, $2, $3, $4, $5, 'success', now())
		ON CONFLICT (tenant_id, credential_id, raw_model_name) DO UPDATE SET
		    credibility_score = EXCLUDED.credibility_score,
		    verdict = EXCLUDED.verdict,
		    status = EXCLUDED.status,
		    measured_at = EXCLUDED.measured_at`,
		credID, provID, model, score, verdict); err != nil {
		t.Fatalf("insert verdict row: %v", err)
	}
}

func TestRollupProviderModelRealPG(t *testing.T) {
	pool := openRollupIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tag := time.Now().UnixNano()
	model := "rollup-same-row-" + strconv.FormatInt(tag, 10)
	provA, provB := int64(910000+tag%100000), int64(920000+tag%100000)

	cleanup := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM provider_model_credibility WHERE raw_model_name = $1`, model)
	}
	cleanup()
	t.Cleanup(cleanup)

	// 组 1（provA / model）：三条线。
	//   cred 1: verified 88
	//   cred 2: diluted  40   ← 最差凭据：结论最重 + 分数最低（同一行）
	//   cred 3: unknown  20   ← 分数最低但结论比 diluted 轻
	// 旧行为（MIN 分数 + MIN 字典序结论）会读出 20 分 + diluted —— 一个
	// 数据里不存在的组合；正确读数是 40 分 + diluted（cred 2 整行）。
	insertRollupVerdict(t, pool, tag+1, provA, model, "verified", 88)
	insertRollupVerdict(t, pool, tag+2, provA, model, "diluted", 40)
	insertRollupVerdict(t, pool, tag+3, provA, model, "unknown", 20)

	// 组 2（provB / model）：两条线，字典序陷阱反向 ——
	//   cred 4: suspected 91（字典序 's' < 'v' ⇒ MIN(verdict) 会选中它）
	//   cred 5: diluted   55（真正最差的凭据）
	insertRollupVerdict(t, pool, tag+4, provB, model, "suspected", 91)
	insertRollupVerdict(t, pool, tag+5, provB, model, "diluted", 55)

	store := NewStore(pool)
	rows, err := store.RollupProviderModel(ctx)
	if err != nil {
		t.Fatalf("RollupProviderModel: %v", err)
	}
	type wantRow struct {
		credibility float64
		sampleCount int64
		verdict     string
	}
	want := map[int64]wantRow{
		provA: {credibility: 40, sampleCount: 3, verdict: "diluted"},
		provB: {credibility: 55, sampleCount: 2, verdict: "diluted"},
	}
	got := map[int64]wantRow{}
	for _, r := range rows {
		if r.RawModelName != model {
			continue
		}
		got[r.ProviderID] = wantRow{credibility: r.Credibility, sampleCount: int64(r.SampleCount), verdict: r.Verdict}
	}
	if len(got) != 2 {
		t.Fatalf("rolled up %d groups for %s, want 2 (groups: %v)", len(got), model, got)
	}
	for provID, w := range want {
		g, ok := got[provID]
		if !ok {
			t.Fatalf("provider %d missing from rollup", provID)
		}
		if g.credibility != w.credibility || g.verdict != w.verdict || g.sampleCount != w.sampleCount {
			t.Errorf("provider %d: got (score=%v verdict=%s samples=%d), want (score=%v verdict=%s samples=%d) — score and verdict must come from the same row",
				provID, g.credibility, g.verdict, g.sampleCount, w.credibility, w.verdict, w.sampleCount)
		}
	}
}

// TestConsensusStreakSurvivesPersistence 钉住「共识 streak 必须穿透落盘」的
// 全链路：SaveResult（落盘 pending 形态）→ LoadPrevious（读回）→ Evaluate
// （续判）。单元测试直接构造 Previous，盖不住 LoadPrevious 的读出 —— 本仓
// 2026-10-31 审计轮的变异臂实测：把 consensus_pending 从 LoadPrevious 摘掉，
// 全部单元测试仍绿（死锁回归在链路上不可见）⇒ 必须真库腿。
func TestConsensusStreakSurvivesPersistence(t *testing.T) {
	pool := openRollupIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tag := time.Now().UnixNano()
	credID, provID := tag+7, int64(930000+tag%100000)
	model := "consensus-persist-" + strconv.FormatInt(tag, 10)

	cleanup := func() {
		c1, _ := pool.Exec(context.Background(),
			`DELETE FROM provider_model_credibility WHERE credential_id=$1 AND raw_model_name=$2`, credID, model)
		_ = c1
		c2, _ := pool.Exec(context.Background(),
			`DELETE FROM provider_model_credibility_runs WHERE credential_id=$1 AND raw_model_name=$2`, credID, model)
		_ = c2
	}
	cleanup()
	t.Cleanup(cleanup)

	store := NewStore(pool)

	// 第 1 轮：原始判定达标但共识未满 —— 以生产真实落盘形态写入
	// （verdict=unknown + evidence.consensus_pending=true + consensus_streak=1）。
	first := Result{
		TenantID: "default", ProviderID: provID, CredentialID: credID,
		RawModelName: model,
		Scores:       SubScores{Identity: 100, Measured: MeasuredMask(uint16(DimIdentity))},
		Overall:      100, Verdict: VerdictUnknown, Status: StatusSuccess,
		Evidence: map[string]any{
			"consensus_streak": 1, "consensus_required": 2, "consensus_pending": true,
		},
		MeasuredAt: nowFunc(),
	}
	if err := store.SaveResult(ctx, first); err != nil {
		t.Fatalf("save first round: %v", err)
	}

	// 读回：consensus_pending 与 streak 必须被带出来。
	prev, err := store.LoadPrevious(ctx, credID, model)
	if err != nil {
		t.Fatalf("load previous: %v", err)
	}
	if prev == nil {
		t.Fatal("previous is nil after save")
	}
	if !prev.ConsensusPending {
		t.Fatal("LoadPrevious lost evidence.consensus_pending — the consensus streak would reset every round (deadlock回归)")
	}
	if prev.VerifiedStreak != 1 {
		t.Fatalf("VerifiedStreak = %d, want 1", prev.VerifiedStreak)
	}

	// 第 2 轮：同样的干净判定 + 读回的 Previous ⇒ K=2 共识满 ⇒ verified。
	// ★ 输入要凑够 VerifiedMinDimensions=2（身份+能力）——单维度的原始判定
	// 是 unknown（纪律三），根本走不到共识门。
	cfg := DefaultConfig()
	second := Evaluate(Input{
		Target:   NodeTarget{CredentialID: credID, ProviderID: provID, RawModelName: model, OutboundModel: model},
		Observed: Observed{HTTPStatus: 200, ReportedModel: model, Content: "ready"},
		Accuracy: 80, HasAccuracy: true,
		Peer: PeerReference{Samples: []PeerSample{
			{CredentialID: credID + 1, ProviderID: provID + 1, Accuracy: 79},
			{CredentialID: credID + 2, ProviderID: provID + 2, Accuracy: 78},
		}},
		Previous: prev,
		Now:      nowFunc(),
	}, cfg)
	if second.Verdict != VerdictVerified {
		t.Fatalf("round2 verdict = %s, want verified — streak must survive persistence (evidence=%v)",
			second.Verdict, second.Evidence)
	}
	if second.Evidence["consensus_streak"] != 2 {
		t.Fatalf("round2 streak = %v, want 2", second.Evidence["consensus_streak"])
	}
}
