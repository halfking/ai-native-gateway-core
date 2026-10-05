package bg

// `VerifyOnce` 这个调度层的两条「零值语义」判据。
//
// # 缺口
//
// `VerifyOnce` 的循环体里两处阈值直接参与控制流，而这两处的**零值含义**从未被验过：
//
//  1. `if rem := m.budgetRemaining(now); rem == 0 { budgetExhausted = true; break }`
//
//     而 `budgetRemaining` 在 `dailyBudget <= 0` 时**返回 0**（注释：「与 chargeProbe
//     的语义对齐」—— `chargeProbe` 在 `dailyBudget<=0` 时返回 true 即放行）。
//     ⇒ 把日预算配成 **0** 时，「不限」在记账侧成立、在循环侧变成「**每轮只探一条**」。
//     2026-10-05 实测这个配置是可达的：`modalityVerifyDailyBudget` 走
//     `strconv.Atoi`，`"0"` 是合法整数、不触发那条「不是整数」的告警。
//
//  2. `if probed >= m.batchLimit { break }` —— `batchLimit` 为 0 时首轮即 break，
//     整个核实任务**什么都不做**且不报错。构造器用的是包常量所以非零，但任何
//     「直接字面量构造」的路径（含测试）都会踩到。
//
// # 判据的形状：先写正确行为的断言，看它在修复前红
//
// 观测的是「这一轮真的探了几条」，可测量是探针被调用的次数。
// ⚠ 不验探针本身（见 modality_zero_egress_realdb_test.go 的说明）：
// `scan` 与 `probe` 是结构体注释里点名的接缝，而 `probeAndPersist` / `persistRow`
// / `rollupVerdict` 走**真库真实代码**。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

func TestVerifyOnceTreatsZeroBudgetAsUnlimitedAndZeroBatchLimitAsUnset(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-loop','text','inferred')`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	var canonID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM public.models_canonical WHERE canonical_name='m-loop'`).Scan(&canonID); err != nil {
		t.Fatalf("read canonical id: %v", err)
	}

	// n 条**全部通过闸门**的目标：验的是循环的阈值语义，不是闸门（闸门在
	// modality_zero_egress_realdb_test.go 里逐条验过）。
	targets := make([]modalityVerifyTarget, 0, 4)
	for i := 0; i < 4; i++ {
		targets = append(targets, modalityVerifyTarget{
			CredentialID: 100 + i, CanonicalID: canonID, CanonicalName: "m-loop",
			RawModel: "raw-loop", Modality: "vision",
			CredentialStatus: "active", LifecycleStatus: "active",
			BindingAvailable: true, ProviderEnabled: true,
		})
	}

	// newVerifier 造一个「探针只计数、结论走真实落库」的任务。
	newVerifier := func(dailyBudget, batchLimit int) (*ModalityVerification, *int) {
		calls := 0
		m := &ModalityVerification{
			db: pool, attempts: map[string]time.Time{},
			dailyBudget: dailyBudget, batchLimit: batchLimit,
			decryptFn: func(modalityVerifyTarget) (string, error) { return "sk-test", nil },
		}
		m.persist = func(ctx context.Context, t modalityVerifyTarget, res SemanticProbeResult) error {
			return m.persistRow(ctx, t, res)
		}
		m.rollup = func(ctx context.Context, t modalityVerifyTarget) error {
			return m.rollupVerdict(ctx, t)
		}
		m.probe = func(ctx context.Context, t modalityVerifyTarget, apiKey string) SemanticProbeResult {
			calls++
			return SemanticProbeResult{
				Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed, HTTPStatus: 200,
			}
		}
		m.scan = func(ctx context.Context) ([]modalityVerifyTarget, error) { return targets, nil }
		return m, &calls
	}

	evidenceRows := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM public.model_modality_verification`).Scan(&n); err != nil {
			t.Fatalf("count evidence rows: %v", err)
		}
		return n
	}

	// ---- 场景 1：日预算 = 0 ⇒ 含义是「不限」，不是「每轮一条」 ----
	//
	// 四条目标全部应被探。修复前：第一条探完 budgetRemaining()==0 就 break ⇒ 只探 1 条。
	if _, err := pool.Exec(ctx, `DELETE FROM public.model_modality_verification`); err != nil {
		t.Fatalf("clear evidence: %v", err)
	}
	m, calls := newVerifier(0, 10)
	written, err := m.VerifyOnce(ctx)
	if err != nil {
		t.Fatalf("VerifyOnce with dailyBudget=0: %v", err)
	}
	if *calls != len(targets) {
		t.Errorf("dailyBudget=0 probed %d of %d target(s). 0 must mean \"no daily cap\" — that is "+
			"what chargeProbe already does (it returns true when dailyBudget<=0), and the loop "+
			"must agree with it. Right now the loop reads budgetRemaining()==0 as \"exhausted\" "+
			"and stops after the first probe, so an operator disabling the cap silently caps "+
			"verification at one probe per cycle", *calls, len(targets))
	}
	if written != len(targets) {
		t.Errorf("VerifyOnce reported written=%d, want %d", written, len(targets))
	}
	if got := evidenceRows(); got != len(targets) {
		t.Errorf("evidence rows = %d, want %d — each probed target must leave a row", got, len(targets))
	}

	// ---- 场景 2：batchLimit = 0 ⇒ 含义是「未设置」，不是「什么都不做」 ----
	//
	// 零值 batchLimit 只能来自字面量构造（构造器用的是包常量，非零）。但「什么都不做
	// 且不报错」是那种最难发现的失效：日志会打 cycle done，scanned=4 probed=0。
	if _, err := pool.Exec(ctx, `DELETE FROM public.model_modality_verification`); err != nil {
		t.Fatalf("clear evidence: %v", err)
	}
	m, calls = newVerifier(100, 0)
	if _, err := m.VerifyOnce(ctx); err != nil {
		t.Fatalf("VerifyOnce with batchLimit=0: %v", err)
	}
	if *calls == 0 {
		t.Error("batchLimit=0 probed nothing at all, with no error. A zero-value struct " +
			"(any construction path that does not go through NewModalityVerification) silently " +
			"disables the whole verification worker — the failure is invisible because the cycle " +
			"log still says \"cycle done\"")
	}
	t.Logf("batchLimit=0 probed %d target(s)", *calls)

	// ---- 场景 3：batchLimit 真的生效，且只数「真的出过网的」 ----
	//
	// 正数上限必须被遵守 —— 否则 batchLimit 就是一句空话。
	if _, err := pool.Exec(ctx, `DELETE FROM public.model_modality_verification`); err != nil {
		t.Fatalf("clear evidence: %v", err)
	}
	m, calls = newVerifier(100, 2)
	if _, err := m.VerifyOnce(ctx); err != nil {
		t.Fatalf("VerifyOnce with batchLimit=2: %v", err)
	}
	if *calls != 2 {
		t.Errorf("batchLimit=2 probed %d target(s), want exactly 2 — a positive batch limit that "+
			"is not enforced is worse than none, because the operator believes they capped the "+
			"cycle", *calls)
	}
	if got := evidenceRows(); got != 2 {
		t.Errorf("evidence rows = %d, want 2", got)
	}

	// ---- 场景 4：一条目标出错不得带崩整轮 ----
	//
	// `probeAndPersist` 的错误路径（解密失败）会 return err。VerifyOnce 对它是 continue。
	// 若是 return，整轮核实会因为一条坏凭据停摆 —— 而核实是周期任务，停一轮就是
	// 「这个模型又没被核实」。
	if _, err := pool.Exec(ctx, `DELETE FROM public.model_modality_verification`); err != nil {
		t.Fatalf("clear evidence: %v", err)
	}
	m, calls = newVerifier(100, 10)
	m.decryptFn = func(modalityVerifyTarget) (string, error) {
		return "", errDecryptForTest
	}
	if _, err := m.VerifyOnce(ctx); err != nil {
		t.Errorf("VerifyOnce returned %v because one target failed to decrypt. A cycle must "+
			"isolate per-target failures: a periodic job that aborts on the first bad credential "+
			"stops verifying every model until that one is fixed", err)
	}
	if *calls != 0 {
		t.Errorf("the probe ran %d time(s) even though every decrypt failed", *calls)
	}
}

// errDecryptForTest 只是给场景 4 用的一个哨兵错误，避免把 errors 的 import
// 混进其它场景的读法里。
var errDecryptForTest = errTest("ciphertext is not a payload this key can open")

type errTest string

func (e errTest) Error() string { return string(e) }
