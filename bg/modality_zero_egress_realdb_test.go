package bg

// `probeAndPersist` 的**零出网路径**判据：准入拒绝 / 解密失败 / 预算耗尽时，
// 这一轮核实**不许碰网络、不许进账单、不许留退避**。
//
// # 这条验的是什么、不验什么
//
// ⚠ **不验探针本身**。`ModalityVerification` 的注释写得很明确：「probe 本身
// **不是**接缝 —— 那条路径必须是真的，否则测的就是 mock」。所以这里不注入一个
// 假装会识别的探针、也不据此声称「自动核实有效」。
//
// 验的是**探针周围那些决策**，而它们全部是真实代码 + 真实数据库：
//
//   · 哪几条绑定**允许**出网（`modalityVerifyAdmit` 已有纯函数判据，但**没有**
//     任何判据证明 `probeAndPersist` 真的停在那里 —— 闸门写对了、调用点却漏了
//     return，测试照样全绿）；
//   · 记账（`chargeProbe`）与退避（`recordAttempt`）**在出网之前**，所以被拒的
//     那几条不该进账单 —— 这条承诺写在函数注释里，此前无人验；
//   · 真的出网了 ⇒ 证据行与模型级判词真的落库。
//
// 观测用的是**探针被调用的次数**，不是探针返回的东西。这与「mock 行为」相反：
// 被测对象是「有没有出网」这个事实，而「出网」在测试里是唯一必须被替换的东西。
//
// # 为什么要「正向对照」
//
// 全是「应该没发生」的断言，一个函数如果**永远**提前 return 也能全绿。所以最后
// 有一个必须发生的情形：闸门放行 + 预算充足 ⇒ 探针被调用一次，且真库多出一行
// 证据。少了它，上面那些断言都是空的。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

func TestProbeAndPersistNeverLeavesTheMachineOnARejectedTarget(t *testing.T) {
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
	// ★ 清理注册在建表之前。
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
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		 VALUES ('m-zero-egress','text','inferred')`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	// CanonicalID 必须真的取出来：persistRow 写的是 `NULLIF($1, 0)`，不填就落成
	// NULL，而下面那条读回是按 canonical_id 关联的 —— 夹具漏填会让断言变成
	// 「读不到行」而不是「关联没建立」。
	var canonID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM public.models_canonical WHERE canonical_name = 'm-zero-egress'`).
		Scan(&canonID); err != nil {
		t.Fatalf("read the seeded canonical id: %v", err)
	}

	// 通过全部闸门的基准目标。上面每一条拒绝场景都从它改一个字段。
	base := modalityVerifyTarget{
		CredentialID: 7, CanonicalID: canonID,
		CanonicalName: "m-zero-egress", RawModel: "raw-x", Modality: "vision",
		CredentialStatus: "active", LifecycleStatus: "active",
		BindingAvailable: true, ProviderEnabled: true,
	}

	evidenceRows := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.model_modality_verification`).
			Scan(&n); err != nil {
			t.Fatalf("count evidence rows: %v", err)
		}
		return n
	}

	// newVerifier 造一个带计数探针的任务。probeCalls 记「出网了几次」——
	// 这是本判据唯一关心的可观测量。
	newVerifier := func(probeCalls *int) *ModalityVerification {
		m := &ModalityVerification{
			db:          pool,
			attempts:    map[string]time.Time{},
			dailyBudget: 5,
			decryptFn:   func(modalityVerifyTarget) (string, error) { return "sk-test", nil },
			persist:     m0persist,
			rollup:      nil,
		}
		m.persist = func(ctx context.Context, t modalityVerifyTarget, res SemanticProbeResult) error {
			return m.persistRow(ctx, t, res)
		}
		m.rollup = func(ctx context.Context, t modalityVerifyTarget) error {
			return m.rollupVerdict(ctx, t)
		}
		m.probe = func(ctx context.Context, t modalityVerifyTarget, apiKey string) SemanticProbeResult {
			*probeCalls++
			return SemanticProbeResult{
				Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed,
				Score: 3, HTTPStatus: 200,
			}
		}
		return m
	}

	// ---- 逐条拒绝场景：探针一次都不许被调用 ----
	//
	// `admit` 闸门在解密**之前**（见 probeAndPersist 的顺序），所以这些场景连
	// 解密都不该发生 —— 一条被运维手工停用的凭据不该被解密，更不该被发请求。
	for _, tc := range []struct {
		why    string
		mutate func(*modalityVerifyTarget)
	}{
		{"binding_unavailable", func(t *modalityVerifyTarget) { t.BindingAvailable = false }},
		{"credential_manual_disabled", func(t *modalityVerifyTarget) { t.CredentialDisabled = true }},
		{"provider_manual_disabled", func(t *modalityVerifyTarget) { t.ProviderDisabled = true }},
		{"provider_disabled", func(t *modalityVerifyTarget) { t.ProviderEnabled = false }},
		{"lifecycle", func(t *modalityVerifyTarget) { t.LifecycleStatus = "cooling" }},
		{"credential_status", func(t *modalityVerifyTarget) { t.CredentialStatus = "retired" }},
		{"modality_unresolved", func(t *modalityVerifyTarget) { t.Modality = "" }},
		{"modality_unknown", func(t *modalityVerifyTarget) { t.Modality = "text" }},
	} {
		target := base
		tc.mutate(&target)
		if admit, why := modalityVerifyAdmit(target); admit {
			t.Fatalf("fixture bug: the %s case is not actually rejected by the admission gate "+
				"(reason=%q), so the assertions below would prove nothing", tc.why, why)
		}
		calls := 0
		decrypts := 0
		m := newVerifier(&calls)
		m.decryptFn = func(modalityVerifyTarget) (string, error) {
			decrypts++
			return "sk-test", nil
		}
		verified, changed, err := m.probeAndPersist(ctx, target)
		if err != nil {
			t.Errorf("%s: probeAndPersist returned %v, want nil — a rejected target is not an error",
				tc.why, err)
		}
		if verified || changed {
			t.Errorf("%s: reported verified=%v changed=%v, want false/false", tc.why, verified, changed)
		}
		if calls != 0 {
			t.Errorf("%s: the probe was invoked %d time(s). This target is rejected by the admission "+
				"gate, so it must not reach the network", tc.why, calls)
		}
		if decrypts != 0 {
			t.Errorf("%s: the credential was decrypted %d time(s) even though the admission gate "+
				"rejects this target. A soft-deleted or disabled credential must not be decrypted",
				tc.why, decrypts)
		}
		if got := len(m.probes); got != 0 {
			t.Errorf("%s: the probe budget ledger grew to %d — a target that never went out was "+
				"charged against the daily probe budget", tc.why, got)
		}
		if got := len(m.attempts); got != 0 {
			t.Errorf("%s: an attempt was recorded (%d) for a target that never went out, so it will "+
				"sit in the backoff ledger for nothing", tc.why, got)
		}
	}

	// ---- 解密失败：不该出网、不该进账单，且**要**报错 ----
	//
	// 与准入拒绝不同，解密失败是真错误（调用方要看），但它同样是零出网路径。
	calls := 0
	m := newVerifier(&calls)
	m.decryptFn = func(modalityVerifyTarget) (string, error) {
		return "", errors.New("ciphertext is not a payload this key can open")
	}
	if _, _, err := m.probeAndPersist(ctx, base); err == nil {
		t.Error("a decrypt failure returned no error — the caller cannot tell a skipped target " +
			"from a working one")
	}
	if calls != 0 {
		t.Errorf("the probe was invoked %d time(s) even though decryption failed", calls)
	}
	if len(m.probes) != 0 {
		t.Errorf("a failed decryption was charged %d probe(s) against the budget", len(m.probes))
	}
	if len(m.attempts) != 0 {
		t.Errorf("a failed decryption recorded %d attempt(s) in the backoff ledger", len(m.attempts))
	}

	// ---- 预算耗尽：闸门全过，但今天已经探够了 ----
	//
	// 预置一条额度把 dailyBudget=1 填满。`chargeProbe` 在 dailyBudget<=0 时直接
	// 放行，所以「耗尽」只能靠真的填满窗口来实现 —— 这里用 1/1。
	calls = 0
	m = newVerifier(&calls)
	m.dailyBudget = 1
	m.probes = []time.Time{time.Now()}
	before := len(m.probes)
	if _, _, err := m.probeAndPersist(ctx, base); err != nil {
		t.Errorf("an exhausted budget returned %v, want nil — running out of budget is a normal "+
			"end-of-day outcome, not an error", err)
	}
	if calls != 0 {
		t.Errorf("the probe was invoked %d time(s) with the daily budget already exhausted", calls)
	}
	if len(m.probes) != before {
		t.Errorf("the ledger grew from %d to %d entries although the budget was already spent",
			before, len(m.probes))
	}
	if evidenceRows() != 0 {
		t.Fatalf("evidence rows exist before the positive control — the fixture leaked")
	}

	// ---- 正向对照：闸门放行 + 预算充足 ⇒ 真的出网并落库 ----
	//
	// 少了这一段，上面所有「应该没发生」都能被「这个函数永远提前 return」骗过。
	beforeEvidence := evidenceRows()
	calls = 0
	m = newVerifier(&calls)
	m.dailyBudget = 5
	verified, changed, err := m.probeAndPersist(ctx, base)
	if err != nil {
		t.Fatalf("the admitted target failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the admitted target reached the probe %d time(s), want exactly 1 — every "+
			"\"should not have gone out\" assertion above is vacuous if the admitted path never "+
			"goes out either", calls)
	}
	if len(m.probes) != 1 {
		t.Errorf("an actual probe left the budget ledger at %d entries, want 1 — the budget would "+
			"never be spent on anything that really went out", len(m.probes))
	}
	if len(m.attempts) != 1 {
		t.Errorf("an actual probe recorded %d attempt(s) in the backoff ledger, want 1", len(m.attempts))
	}
	// 落库：证据行 + 模型级判词。
	if after := evidenceRows(); after != beforeEvidence+1 {
		t.Errorf("evidence rows went from %d to %d, want +1 — the probe ran but its verdict did "+
			"not reach the evidence table", beforeEvidence, after)
	}
	//
	// ★ 一次通过**不该**升级：这是 streak 规则（applyStreak 要 modalityVerifyStreakGoal
	// 次才把 read_level 抬成 confirmed），而两胜升级已由
	// TestRollupLabelsCanonicalAfterTwoSemanticPasses 钉住，不在这里重复。
	// 我第一版把期望写成 confirmed，被真库判掉 —— 一次通过之后 read_level 仍是 unknown。
	// 真正该看的是 pos_streak 变成了 1：**证据被记下了，只是还没到升级线。**
	var readLevel string
	var posStreak int
	if err := pool.QueryRow(ctx, `SELECT read_level, read_pos_streak
		FROM public.model_modality_verification WHERE credential_id = $1`, base.CredentialID).
		Scan(&readLevel, &posStreak); err != nil {
		t.Fatalf("read back the evidence row: %v", err)
	}
	if readLevel != ModalityLevelUnknown {
		t.Errorf("read_level = %q after ONE confirmed pass, want %q — upgrading on a single pass "+
			"is the exact false-positive this streak rule exists to prevent", readLevel, ModalityLevelUnknown)
	}
	if posStreak != 1 {
		t.Errorf("read_pos_streak = %d after one pass, want 1 — the pass was not recorded, so the "+
			"second pass could never reach the goal", posStreak)
	}
	// 证据行必须真的挂在这个 canonical 上（夹具漏填 CanonicalID 时这里是 NULL）。
	var linked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.model_modality_verification
		WHERE credential_id = $1 AND canonical_id = $2 AND modality = 'vision'`,
		base.CredentialID, base.CanonicalID).Scan(&linked); err != nil {
		t.Fatalf("check the evidence row's link: %v", err)
	}
	if linked != 1 {
		t.Errorf("%d evidence rows link (credential, canonical, vision), want 1 — an evidence row "+
			"that is not linked to its canonical model is invisible to 827's progress view", linked)
	}
	t.Logf("positive control: verified=%v changed=%v, read_level=%s pos_streak=%d",
		verified, changed, readLevel, posStreak)
}

// m0persist 只是为了让 newVerifier 的结构体字面量能过编译；真正的赋值在
// newVerifier 内部（要拿到 m 自己的方法）。留一个具名零值比在字面量里写
// `persist: nil` 再覆盖更难读错。
var m0persist = func(ctx context.Context, t modalityVerifyTarget, res SemanticProbeResult) error {
	return nil
}
