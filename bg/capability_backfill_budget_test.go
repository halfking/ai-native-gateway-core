// capability_backfill_budget_test.go — 每日出网探测预算闸门（2026-10-03）。
//
// 背景：加预算之前只有两个隐式约束——batchLimit=50/轮 与 staleAfter=6h。
// 两者都**不是**日账单：batchLimit 乘轮次才是，而轮次由 interval 决定
// （改一个周期常量就能把日花费翻倍，没有任何东西会拦）；staleAfter 决定
// 多少行处于 due 态，绑定数一多 due 行数就逼近 batchLimit，于是每轮都打满。
// 本机实测 eligible 绑定 154 > 50 ⇒ 稳态就是 50 探 × 48 轮 = 2400 次/天
// 的真实上游调用，而这个数字此前无人显式批准。
//
// 本组判据盯三件容易被做错的事：
//  1. 预算封的是**出网次数**，不是「尝试次数」——admission 拒绝 / 端点未解析
//     / 解密失败这些零出网成本的行不得计费；
//  2. 边界：正好用完的那一次必须放行，第 N+1 次必须被拒（差一位就是错的）；
//  3. 记账点必须在**出网之前**，否则被拒的那一发已经花掉了钱。
package bg

import (
	"context"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// newBudgetKeyring 建一个与 capability_backfill_test.go 同款的真实 keyring，
// 让解密路径也是真的（不给 decrypt 开后门）。
func newBudgetKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	var key [32]byte
	copy(key[:], "capability-backfill-test-32byte")
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return kr
}

// envNamePattern 是合法的 shell 环境变量名形状。
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// budgetHarness 起一个只替换 probe/scan/persist 的回填任务，并数真实出网次数。
type budgetHarness struct {
	b        *CapabilityBackfill
	probes   *int
	probeMu  *sync.Mutex
	envelope string
}

func newBudgetHarness(t *testing.T, n int, budget int) *budgetHarness {
	t.Helper()
	kr := newBudgetKeyring(t)
	envelope, err := secret.EncryptAESGCM([]byte("sk-budget-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	probes := 0
	var mu sync.Mutex
	h := &budgetHarness{probes: &probes, probeMu: &mu, envelope: envelope}
	h.b = &CapabilityBackfill{
		keyring:     kr,
		batchLimit:  n, // 故意等于行数：让 batchLimit **不是**本例的瓶颈
		staleAfter:  capabilityBackfillStaleAfter,
		dailyBudget: budget,
		probe: func(context.Context, probeTarget, providercap.Descriptor) httpProbeResult {
			mu.Lock()
			probes++
			mu.Unlock()
			// 无证据：有出网成本，但不写任何行（把「花钱」与「写」解耦）。
			return httpProbeResult{status: "ok", category: probeCategoryProviderError}
		},
		scan: func(context.Context) ([]dueBinding, error) {
			rows := make([]dueBinding, 0, n)
			for i := 0; i < n; i++ {
				rows = append(rows, admitAll(func(d *dueBinding) {
					d.BindingID = int64(2000 + i)
					d.CredentialID = 2000 + i
					d.Ciphertext = []byte(envelope)
					d.Protocol = "openai-responses"
					d.BaseURL = "http://budget-test.invalid"
				}))
			}
			return rows, nil
		},
		persist: func(context.Context, dueBinding, bool, []byte) error { return nil },
	}
	return h
}

func (h *budgetHarness) count() int {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	return *h.probes
}

// TestBudget_StopsAtTheDailyCeiling is the primary criterion.
//
// 20 行待探、batchLimit=20（不是瓶颈）、每日预算 5 ⇒ 必须恰好出网 5 次。
// 「恰好」是承重的：<5 说明闸门太紧（白白浪费额度、刷新变慢），
// >5 说明它没封住（账单超发，正是这个闸门要防的事）。
func TestBudget_StopsAtTheDailyCeiling(t *testing.T) {
	h := newBudgetHarness(t, 20, 5)
	if _, err := h.b.BackfillOnce(context.Background()); err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if got := h.count(); got != 5 {
		t.Fatalf("出网 %d 次，want 5：每日预算是 5，20 行待探。>5 说明账单超发，"+
			"<5 说明额度被白白浪费。", got)
	}
}

// TestBudget_AccumulatesAcrossCycles is what makes it a *daily* budget rather
// than a per-cycle one.
//
// 三个周期、每周期 2 行、每日预算 5 ⇒ 2 + 2 + 1 = 5，第三个周期必须只探 1 次。
// 只在单轮里测的话，一个「每轮都重置计数」的实现会全绿，而那等于没有日闸门。
func TestBudget_AccumulatesAcrossCycles(t *testing.T) {
	// ⚠️ 每轮必须换一批**新绑定**：attempt 退避（1h）会让同一批绑定在第二轮
	// 全部 backedOff，于是第二轮出网 0 次——那测的是退避，不是预算。
	// 第一次写这个用例时踩了这个坑：三轮都是同一批 BindingID，计数卡在 2。
	kr := newBudgetKeyring(t)
	envelope, err := secret.EncryptAESGCM([]byte("sk-budget-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	probes := 0
	var mu sync.Mutex
	b := &CapabilityBackfill{
		keyring:     kr,
		batchLimit:  2,
		staleAfter:  capabilityBackfillStaleAfter,
		dailyBudget: 5,
		probe: func(context.Context, probeTarget, providercap.Descriptor) httpProbeResult {
			mu.Lock()
			probes++
			mu.Unlock()
			return httpProbeResult{status: "ok", category: probeCategoryProviderError}
		},
		scan:    nil, // 每轮由下方闭包替换，见 batchOffset 的自增
		persist: func(context.Context, dueBinding, bool, []byte) error { return nil },
	}
	batch := 0
	b.scan = func(context.Context) ([]dueBinding, error) {
		batch++
		base := 10000 * batch
		rows := make([]dueBinding, 0, 2)
		for i := 0; i < 2; i++ {
			rows = append(rows, admitAll(func(d *dueBinding) {
				d.BindingID = int64(base + i)
				d.CredentialID = base + i
				d.Ciphertext = []byte(envelope)
				d.Protocol = "openai-responses"
				d.BaseURL = "http://budget-test.invalid"
			}))
		}
		return rows, nil
	}
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := b.BackfillOnce(ctx); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		mu.Lock()
		got := probes
		mu.Unlock()
		t.Logf("第 %d 轮后累计出网 %d 次", i, got)
	}
	mu.Lock()
	got := probes
	mu.Unlock()
	if got != 5 {
		t.Fatalf("三轮累计出网 %d 次，want 5（2+2+1）：预算是滚动 24h 的总量，每轮不重置。", got)
	}
}

// TestBudget_DoesNotChargeZeroCostRows is the criterion that separates
// "probes" from "attempts".
//
// 全部行都被 admission 拒绝（quota_expired）⇒ 一次都不该出网，预算分文不动。
// 若实现把记账放在循环里按行扣，日志会显示「预算已用尽」而实际上一个包都没发。
func TestBudget_DoesNotChargeZeroCostRows(t *testing.T) {
	kr := newBudgetKeyring(t)
	envelope, err := secret.EncryptAESGCM([]byte("sk-budget-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	probes := 0
	b := &CapabilityBackfill{
		keyring:     kr,
		batchLimit:  10,
		staleAfter:  capabilityBackfillStaleAfter,
		dailyBudget: 3,
		probe: func(context.Context, probeTarget, providercap.Descriptor) httpProbeResult {
			probes++
			return httpProbeResult{status: "ok", category: probeCategoryProviderError}
		},
		scan: func(context.Context) ([]dueBinding, error) {
			rows := make([]dueBinding, 0, 10)
			for i := 0; i < 10; i++ {
				rows = append(rows, admitAll(func(d *dueBinding) {
					d.BindingID = int64(3000 + i)
					d.CredentialID = 3000 + i
					d.Ciphertext = []byte(envelope)
					d.Protocol = "openai-responses"
					d.BaseURL = "http://budget-test.invalid"
					// 凭据额度已耗尽 ⇒ admission 拒绝 ⇒ 零出网。
					d.CredentialStatus = "quota_expired"
				}))
			}
			return rows, nil
		},
		persist: func(context.Context, dueBinding, bool, []byte) error { return nil },
	}
	if _, err := b.BackfillOnce(context.Background()); err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if probes != 0 {
		t.Fatalf("出网 %d 次，want 0：这些行被 admission 拒绝，根本没发包。", probes)
	}
	if rem := b.budgetRemaining(time.Now()); rem != 3 {
		t.Fatalf("预算剩余 %d，want 3：零出网成本的行不得计费。", rem)
	}
}

// TestBudget_ZeroDisablesTheGate pins the escape hatch: 0 (or negative) means
// "no budget", i.e. the pre-change behaviour. Without this, an operator who
// sets the env to 0 to "unblock" would silently get a gate that blocks
// everything — the opposite of what they asked for.
func TestBudget_ZeroDisablesTheGate(t *testing.T) {
	t.Setenv(capabilityBackfillDailyBudgetEnv, "0")
	if got := capabilityBackfillDailyBudget(); got != 0 {
		t.Fatalf("budget=%d, want 0 (disabled)", got)
	}
	h := newBudgetHarness(t, 20, capabilityBackfillDailyBudget())
	if _, err := h.b.BackfillOnce(context.Background()); err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	if got := h.count(); got != 20 {
		t.Fatalf("出网 %d 次，want 20：预算设为 0 应当等于「不设预算」。", got)
	}
}

// TestBudget_EnvParsing pins the operator-facing contract, including the
// invalid-value path: a typo must NOT silently become "no budget" (that would
// remove the ceiling exactly when someone thinks they set one).
func TestBudget_EnvParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", capabilityBackfillDefaultDailyBudget},
		{"100", 100},
		{"  250  ", 250},
		{"0", 0},
		{"-5", -5},
		{"abc", capabilityBackfillDefaultDailyBudget}, // 非法 ⇒ 回默认，不是「无限制」
		{"12.5", capabilityBackfillDefaultDailyBudget},
	}
	for _, tc := range cases {
		t.Run("raw="+tc.raw, func(t *testing.T) {
			if tc.raw == "" {
				os.Unsetenv(capabilityBackfillDailyBudgetEnv)
			} else {
				t.Setenv(capabilityBackfillDailyBudgetEnv, tc.raw)
			}
			if got := capabilityBackfillDailyBudget(); got != tc.want {
				t.Fatalf("budget=%d, want %d (raw=%q)", got, tc.want, tc.raw)
			}
		})
	}
}

// TestBudget_WindowIsRolling pins that the window slides: entries older than
// 24h stop counting. Without this, "daily" would quietly mean "since process
// start" and a long-lived process would permanently stop probing.
func TestBudget_WindowIsRolling(t *testing.T) {
	h := newBudgetHarness(t, 5, 5)
	base := time.Now()
	// 花光 5 次额度。
	for i := 0; i < 5; i++ {
		if !h.b.chargeProbe(base) {
			t.Fatalf("第 %d 次被拒，但预算还有额度", i+1)
		}
	}
	if h.b.chargeProbe(base) {
		t.Fatal("第 6 次应被预算拒绝")
	}
	// 窗口滑过 24h 之后额度归还。
	later := base.Add(capabilityBackfillDailyBudgetWindow + time.Minute)
	if !h.b.chargeProbe(later) {
		t.Fatal("窗口滑过 24h 后额度应归还：daily 预算必须是滚动的，" +
			"否则长跑进程会永久停止探测")
	}
}

// TestBudget_DefaultMatchesWhatTheOldConfigWouldHaveSpent documents *why* the
// default is 2400 and guards against someone "fixing" it to a round number
// that silently halves the probe rate.
//
// 2400 = batchLimit(50) × 48 轮/天（30min 周期）. The point of the default is
// that turning the gate on must NOT change today's behaviour.
func TestBudget_DefaultMatchesWhatTheOldConfigWouldHaveSpent(t *testing.T) {
	cyclesPerDay := int((24 * time.Hour) / capabilityBackfillInterval)
	implied := capabilityBackfillBatchLimit * cyclesPerDay
	if capabilityBackfillDefaultDailyBudget != implied {
		t.Fatalf("默认预算 %d，但旧配置自己会花 %d 次/天（batchLimit %d × %d 轮）。"+
			"两者不等意味着开启预算会**改变今天的行为**——那不是止血，是静默降频。",
			capabilityBackfillDefaultDailyBudget, implied,
			capabilityBackfillBatchLimit, cyclesPerDay)
	}
	t.Logf("默认预算 = %d 次/天（= 旧配置隐含账单，零行为变化）",
		capabilityBackfillDefaultDailyBudget)
}

// TestBudget_IsSeparateFromAttemptLedger pins that the two ledgers are not the
// same data. They answer different questions:
//
//	attempts  : 这条绑定最近探过没有（每绑定一条，用于退避）
//	probes    : 窗口内一共出了几次网（用于预算）
//
// Merging them would make a binding probed 4 times in a window count as 1
// (undercount ⇒ the ceiling does not hold) or force de-duplication that
// silently drops charges.
func TestBudget_IsSeparateFromAttemptLedger(t *testing.T) {
	h := newBudgetHarness(t, 1, 100)
	row := dueBinding{BindingID: 4242}
	// 同一条绑定连探 5 次。
	for i := 0; i < 5; i++ {
		h.b.recordAttempt(row.BindingID)
		if !h.b.chargeProbe(time.Now()) {
			t.Fatalf("第 %d 次出网被预算拒绝，但预算是 100", i+1)
		}
	}
	if rem := h.b.budgetRemaining(time.Now()); rem != 95 {
		t.Fatalf("预算剩余 %d，want 95：同一条绑定探 5 次就是 5 次钱。", rem)
	}
}

// TestBudget_ProbeTimestampsMaxIsAboveTheCeiling keeps the ledger bounded
// without letting the cap itself become the effective budget.
func TestBudget_ProbeTimestampsMaxIsAboveTheCeiling(t *testing.T) {
	if capabilityBackfillProbeTimestampsMax <= capabilityBackfillDefaultDailyBudget {
		t.Fatalf("台账上限 %d 必须大于默认预算 %d，否则台账会比预算先触顶，"+
			"截断后预算实际失效。",
			capabilityBackfillProbeTimestampsMax, capabilityBackfillDefaultDailyBudget)
	}
	// 环境变量名必须是合法 shell 标识符，否则运维按文档 export 出来会静默无效。
	if !envNamePattern.MatchString(capabilityBackfillDailyBudgetEnv) {
		t.Fatalf("预算环境变量名 %q 不合法：运维照文档 export 会无效且无任何提示。",
			capabilityBackfillDailyBudgetEnv)
	}
}

// TestBudget_LedgerCapScalesWithConfiguredBudget pins the R37-P2 regression:
// the ledger cap must scale with the configured budget. With the cap pinned at
// default×2 (4800) while the env asks for 5000, the ledger hits the cap BEFORE
// the budget does, gets reset, and the gate silently fails open — 5001st,
// 5002nd, … probes all pass, only a Warn scrolls by.
func TestBudget_LedgerCapScalesWithConfiguredBudget(t *testing.T) {
	t.Setenv(capabilityBackfillDailyBudgetEnv, "5000")
	b := NewCapabilityBackfill(nil, nil, nil, nil)
	if b.dailyBudget != 5000 {
		t.Fatalf("budget=%d, want 5000", b.dailyBudget)
	}
	if b.probeLedgerCap < b.dailyBudget*2 {
		t.Fatalf("台账上限 %d 必须 ≥ 预算×2=%d：cap < budget 时台账先于预算触顶，"+
			"截断后剩余额度回满，闸门静默失效（fail-open）。",
			b.probeLedgerCap, b.dailyBudget*2)
	}
	now := time.Now()
	for i := 0; i < b.dailyBudget; i++ {
		if !b.chargeProbe(now) {
			t.Fatalf("第 %d 次出网被拒，但预算是 %d（台账不应先于预算触顶）",
				i+1, b.dailyBudget)
		}
	}
	if b.chargeProbe(now) {
		t.Fatalf("第 %d 次应被预算拒绝：预算 %d 必须真实封顶。"+
			"（修复前形态：cap=4800 在第 4801 次截断台账 → 剩余回满 → 永远到不了 5000）",
			b.dailyBudget+1, b.dailyBudget)
	}
}

// TestBudget_EgressPointIsTheGate calls probeAndPersist **directly**, bypassing
// BackfillOnce's loop.
//
// Why this exists (2026-10-03, 变异 B1 实测): BackfillOnce 的循环在每轮之后
// 查一次 budgetRemaining 并收尾，于是「记账发生在出网之前还是之后」在
// BackfillOnce 这一层**观察不到**——变异 B1（把记账挪到出网之后）判据全绿。
// 那不是 B1 无害，是**这一层没有能区分它的判据**。
//
// 真正的契约是「**花钱的地方必须先查账**」，而花钱的地方是 probeAndPersist。
// 所以判据必须落在那一层：预算是 1，**不经过循环**连按两次
// probeAndPersist，第二次必须一包都不发。
func TestBudget_EgressPointIsTheGate(t *testing.T) {
	kr := newBudgetKeyring(t)
	envelope, err := secret.EncryptAESGCM([]byte("sk-budget-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	probes := 0
	b := &CapabilityBackfill{
		keyring:     kr,
		batchLimit:  100,
		staleAfter:  capabilityBackfillStaleAfter,
		dailyBudget: 1,
		probe: func(context.Context, probeTarget, providercap.Descriptor) httpProbeResult {
			probes++
			return httpProbeResult{status: "ok", category: probeCategoryProviderError}
		},
		persist: func(context.Context, dueBinding, bool, []byte) error { return nil },
	}
	row := admitAll(func(d *dueBinding) {
		d.BindingID = 777
		d.CredentialID = 777
		d.Ciphertext = []byte(envelope)
		d.Protocol = "openai-responses"
		d.BaseURL = "http://budget-test.invalid"
	})
	ctx := context.Background()

	// 第一次：预算 1 次，必须放行。
	if _, didProbe, err := b.probeAndPersist(ctx, row); err != nil || !didProbe {
		t.Fatalf("第一次应放行：didProbe=%v err=%v", didProbe, err)
	}
	// 第二次：预算已满，必须**一包都不发**。
	_, didProbe, err := b.probeAndPersist(ctx, row)
	if err != nil {
		t.Fatalf("第二次: %v", err)
	}
	if didProbe {
		t.Fatal("预算已满时第二次仍出网了：闸门必须在出网点，" +
			"不能只在 BackfillOnce 的循环里预检（变异 B1 实测全绿）")
	}
	if probes != 1 {
		t.Fatalf("实际出网 %d 次，want 1", probes)
	}
}
