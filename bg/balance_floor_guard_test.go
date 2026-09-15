package bg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// ---------------------------------------------------------------- parsers --

const zhipuPlanSample = `{"code":200,"msg":"ok","success":true,
 "data":{"level":"lite","limits":[
   {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":41,
    "remaining":1958,"percentage":2,"nextResetTime":1787776664946},
   {"type":"TOKENS_LIMIT","unit":6,"number":7,"usage":100000,"currentValue":23000,
    "remaining":470000,"percentage":23,"nextResetTime":1788381464946}
 ]}}`

func TestParseZhipuPlan(t *testing.T) {
	st, err := parseZhipuPlan([]byte(zhipuPlanSample))
	if err != nil {
		t.Fatalf("parseZhipuPlan: %v", err)
	}
	if st.Kind != "zhipu_plan" {
		t.Fatalf("kind = %q", st.Kind)
	}
	if len(st.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(st.Windows))
	}
	// unit 3 → 5h, unit 6 → 7d（只认 unit 字段，不按重置时间排序）
	if st.Windows[0].Window != "5h" || st.Windows[1].Window != "7d" {
		t.Fatalf("windows = %q/%q, want 5h/7d", st.Windows[0].Window, st.Windows[1].Window)
	}
	// percentage 方向 = 已用，直接存
	if st.Windows[0].UsedPercent != 2 || st.Windows[1].UsedPercent != 23 {
		t.Fatalf("used = %v/%v, want 2/23", st.Windows[0].UsedPercent, st.Windows[1].UsedPercent)
	}
	// 仅 TOKENS_LIMIT 参与 token 下限
	if st.MinTokensRemaining == nil || *st.MinTokensRemaining != 470000 {
		t.Fatalf("min tokens = %v, want 470000", st.MinTokensRemaining)
	}
	if st.MaxUsedPercent != 23 {
		t.Fatalf("max used = %v, want 23", st.MaxUsedPercent)
	}
	if st.Windows[0].ResetAt == nil {
		t.Fatalf("reset_at missing")
	}
}

func TestParseZhipuPlanBusinessError(t *testing.T) {
	// HTTP 200 仍可能业务失败（success=false），必须报错而不是返回空窗口。
	_, err := parseZhipuPlan([]byte(`{"code":200,"msg":"无权限","success":false,"data":null}`))
	if err == nil || !strings.Contains(err.Error(), "无权限") {
		t.Fatalf("want business error, got %v", err)
	}
}

func TestParseZhipuPlanIgnoresUnknownLimitTypes(t *testing.T) {
	_, err := parseZhipuPlan([]byte(`{"success":true,"data":{"level":"lite","limits":[
		{"type":"REQUEST_LIMIT","unit":3,"percentage":50,"remaining":10}]}}`))
	if err == nil {
		t.Fatalf("want error when only unknown limit types present")
	}
}

func TestParseZhipuPlanStringNumbers(t *testing.T) {
	// 部分厂商/网关会返回字符串数字，flexNum 必须兼容。
	st, err := parseZhipuPlan([]byte(`{"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":"3","percentage":"2","remaining":"1958",
		 "nextResetTime":"1787776664946"}]}}`))
	if err != nil {
		t.Fatalf("string numbers: %v", err)
	}
	if st.Windows[0].Window != "5h" || st.MinTokensRemaining == nil || *st.MinTokensRemaining != 1958 {
		t.Fatalf("string-number parse wrong: %+v", st)
	}
}

const minimaxPlanSample = `{"base_resp":{"status_code":0,"status_msg":"ok"},
 "model_remains":[
   {"model_name":"video","current_interval_remaining_percent":100,
    "current_weekly_status":3,"current_weekly_remaining_percent":100},
   {"model_name":"general","current_interval_remaining_percent":99,
    "end_time":1787776664946,"current_weekly_status":1,
    "current_weekly_remaining_percent":77,"weekly_end_time":1788381464946}
 ]}`

func TestParseMiniMaxPlan(t *testing.T) {
	st, err := parseMiniMaxPlan([]byte(minimaxPlanSample))
	if err != nil {
		t.Fatalf("parseMiniMaxPlan: %v", err)
	}
	if st.Kind != "minimax_plan" {
		t.Fatalf("kind = %q", st.Kind)
	}
	// 只取 general 条目；video（恒 100）混入会冲淡数字
	if len(st.Windows) != 2 {
		t.Fatalf("windows = %d, want 2 (general interval+weekly)", len(st.Windows))
	}
	// 方向反转：剩余 99 → 已用 1；剩余 77 → 已用 23
	if st.Windows[0].UsedPercent != 1 || st.Windows[1].UsedPercent != 23 {
		t.Fatalf("used = %v/%v, want 1/23", st.Windows[0].UsedPercent, st.Windows[1].UsedPercent)
	}
	// MiniMax 无绝对量 → token 下限不可评估
	if st.MinTokensRemaining != nil {
		t.Fatalf("min tokens must be nil for minimax")
	}
	if st.MaxUsedPercent != 23 {
		t.Fatalf("max used = %v, want 23", st.MaxUsedPercent)
	}
}

func TestParseMiniMaxPlanWeeklyInactive(t *testing.T) {
	// current_weekly_status==3（未激活）时周窗不得出现（假信息防护）。
	st, err := parseMiniMaxPlan([]byte(`{"base_resp":{"status_code":0},"model_remains":[
		{"model_name":"general","current_interval_remaining_percent":50,
		 "current_weekly_status":3,"current_weekly_remaining_percent":100}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(st.Windows) != 1 || st.Windows[0].Window != "5h" {
		t.Fatalf("windows = %+v, want only 5h", st.Windows)
	}
}

func TestParseMiniMaxPlanBusinessError(t *testing.T) {
	_, err := parseMiniMaxPlan([]byte(`{"base_resp":{"status_code":1004,"status_msg":"invalid key"},
		"model_remains":[]}`))
	if err == nil || !strings.Contains(err.Error(), "1004") {
		t.Fatalf("want business error, got %v", err)
	}
}

// ------------------------------------------------------------- evaluation --

func int64p(v int64) *int64       { return &v }
func float64p(v float64) *float64 { return &v }

func TestEvaluatePlanFloor(t *testing.T) {
	st := &planState{
		Kind:               "zhipu_plan",
		MinTokensRemaining: int64p(470000),
		MaxUsedPercent:     23,
	}
	floor500k := int64p(500000)
	floor95 := float64p(95)

	// 安全区：剩余 47万 < 50万下限 → 摘出（保住最后 50 万）
	if got := evaluatePlanFloor(floor500k, nil, st); got != floorPull {
		t.Fatalf("token breach: want floorPull, got %v", got)
	}
	// 剩余回到滞回带以上（>= 55万）→ 恢复
	st.MinTokensRemaining = int64p(560000)
	if got := evaluatePlanFloor(floor500k, nil, st); got != floorRestore {
		t.Fatalf("token recovered: want floorRestore, got %v", got)
	}
	// 滞回带内（50万 < 剩余 < 55万）→ 维持现状
	st.MinTokensRemaining = int64p(520000)
	if got := evaluatePlanFloor(floor500k, nil, st); got != floorNone {
		t.Fatalf("token hysteresis: want floorNone, got %v", got)
	}
	// 百分比击穿：已用 96 >= 95 → 摘出
	st.MinTokensRemaining = nil
	st.MaxUsedPercent = 96
	if got := evaluatePlanFloor(nil, floor95, st); got != floorPull {
		t.Fatalf("percent breach: want floorPull, got %v", got)
	}
	// 窗口重置：已用 0 → 恢复
	st.MaxUsedPercent = 0
	if got := evaluatePlanFloor(nil, floor95, st); got != floorRestore {
		t.Fatalf("percent recovered: want floorRestore, got %v", got)
	}
	// MiniMax 无 token 数据 + 只配 token 下限：摘出方向 fail-open（breach
	// 保持 false）；恢复方向返回 floorRestore 无害 —— 恢复 UPDATE 带
	// state_reason_code='balance_floor' 所有权守卫，未被摘出时恒为 0 行，
	// 而已被摘出的凭据在套餐不再回报 token 计价条目时回池是合理出路
	// （否则永远卡在 suspended）。
	if got := evaluatePlanFloor(floor500k, nil, &planState{MaxUsedPercent: 10}); got != floorRestore {
		t.Fatalf("unevaluable token floor: want floorRestore (harmless via ownership guard), got %v", got)
	}
	// 无下限配置 → 永不动作
	if got := evaluatePlanFloor(nil, nil, st); got != floorNone {
		t.Fatalf("no floors: want floorNone, got %v", got)
	}
	// 双下限：token 恢复但百分比仍击穿 → 摘出优先
	st.MaxUsedPercent = 97
	st.MinTokensRemaining = int64p(600000)
	if got := evaluatePlanFloor(floor500k, floor95, st); got != floorPull {
		t.Fatalf("mixed floors: want floorPull, got %v", got)
	}
	// 极小百分比下限（floor=1%）：used=0（窗口刚重置）必须可恢复，
	// 否则 floor-2pp 为负、凭据永远卡在 suspended。
	floor1 := float64p(1)
	st.MinTokensRemaining = nil
	st.MaxUsedPercent = 1
	if got := evaluatePlanFloor(nil, floor1, st); got != floorPull {
		t.Fatalf("tiny floor breach: want floorPull, got %v", got)
	}
	st.MaxUsedPercent = 0
	if got := evaluatePlanFloor(nil, floor1, st); got != floorRestore {
		t.Fatalf("tiny floor window reset: want floorRestore, got %v", got)
	}
}

// ------------------------------------------------------------------ urls ----

func TestOriginQuotaURL(t *testing.T) {
	cases := []struct {
		base, path, want string
		wantErr          bool
	}{
		{"https://open.bigmodel.cn/api/paas/v4", "/api/monitor/usage/quota/limit",
			"https://open.bigmodel.cn/api/monitor/usage/quota/limit", false},
		{"https://open.bigmodel.cn/api/coding/paas/v4/", "/api/monitor/usage/quota/limit",
			"https://open.bigmodel.cn/api/monitor/usage/quota/limit", false},
		{"https://api.minimaxi.com/v1", "/v1/api/openplatform/coding_plan/remains",
			"https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains", false},
		{"api.minimaxi.com/v1", "/x", "", true},
		{"", "/x", "", true},
	}
	for _, c := range cases {
		got, err := originQuotaURL(c.base, c.path)
		if c.wantErr {
			if err == nil {
				t.Fatalf("originQuotaURL(%q): want error", c.base)
			}
			continue
		}
		if err != nil {
			t.Fatalf("originQuotaURL(%q): %v", c.base, err)
		}
		if got != c.want {
			t.Fatalf("originQuotaURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestFlexNumStringAndNumber(t *testing.T) {
	var n flexNum
	if err := n.UnmarshalJSON([]byte(`"10.73"`)); err != nil || float64(n) != 10.73 {
		t.Fatalf("string: %v %v", err, n)
	}
	if err := n.UnmarshalJSON([]byte(`42`)); err != nil || float64(n) != 42 {
		t.Fatalf("number: %v %v", err, n)
	}
	if err := n.UnmarshalJSON([]byte(`"abc"`)); err == nil {
		t.Fatalf("want error for non-numeric string")
	}
}

// ------------------------------------------------------- env / contracts ----

func TestBalanceFloorGuardEnvContract(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance floor guard source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"LLM_GATEWAY_BALANCE_FLOOR_INTERVAL",
		"LLM_GATEWAY_BALANCE_FLOOR_GUARD",
		// 摘出/恢复所有权：只碰自己的行，绝不写 manual_disabled
		"state_reason_code = 'balance_floor'",
		`COALESCE(state_reason_code, '') = 'balance_floor'`,
		"COALESCE(manual_disabled, FALSE) = FALSE",
		// 恢复滞回
		"planFloorRestoreFactor",
		// 智谱无 Bearer / MiniMax 反转的两个实测细节落在代码里
		`"Authorization", apiKey`,
		"100 - *iv",
		// fail-open：探测失败不摘出
		"balance_floor_guard: plan probe failed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance floor guard is missing %q", want)
		}
	}
}

func TestBalanceFloorGuardPullNeverTouchesManualDisabled(t *testing.T) {
	// 契约：所有写 quota_state='balance_exhausted' 的 UPDATE（货币 sweep 与
	// 套餐 pull）都必须 (a) 尊重 manual_disabled，(b) 只从 quota_state='ok'
	// 摘出（不覆盖 auth_failed / 反应式 balance_exhausted）；所有翻回
	// 'ok' 的恢复 UPDATE 都必须带 state_reason_code='balance_floor'
	// 所有权守卫。
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	pulls := strings.Count(body, "SET quota_state = 'balance_exhausted'")
	if pulls != 2 {
		t.Fatalf("expected exactly 2 pull UPDATEs (currency sweep + plan), found %d", pulls)
	}
	for _, marker := range []string{
		"COALESCE(manual_disabled, FALSE) = FALSE",
		"COALESCE(quota_state, 'ok') = 'ok'",
	} {
		if got := strings.Count(body, marker); got < 2 {
			t.Fatalf("marker %q must appear in both pull UPDATEs, found %d", marker, got)
		}
	}

	restores := strings.Count(body, "SET quota_state = 'ok'")
	if restores != 4 {
		t.Fatalf("expected exactly 4 restore UPDATEs (currency + plan + cleared-floor release + stale-probe escape hatch), found %d", restores)
	}
	// 两处恢复（货币/detail 固定文案，套餐/detail 前缀文案）各自带
	// reason='balance_floor' 守卫。marker 文案位于 SET 子句内，因此从
	// marker 位置向前找所属的 UPDATE 语句头，再检查到 marker 为止的块。
	for i, marker := range []string{
		"recharged above hysteresis band",
		"recovered above hysteresis band",
	} {
		idx := strings.Index(body, marker)
		if idx < 0 {
			t.Fatalf("restore marker %d (%q) not found", i, marker)
		}
		start := strings.LastIndex(body[:idx], "UPDATE credentials")
		if start < 0 || start > idx {
			t.Fatalf("restore UPDATE %d header not found", i)
		}
		// marker 在 SET 子句内，所有权守卫在其后的 WHERE 里 → 窗口延伸到
		// marker 之后 400 字符（足够覆盖 WHERE 头三个条件）。
		end := idx + len(marker) + 400
		if end > len(body) {
			end = len(body)
		}
		if !strings.Contains(body[start:end], `COALESCE(state_reason_code, '') = 'balance_floor'`) {
			t.Fatalf("restore UPDATE %d missing balance_floor ownership guard", i)
		}
	}
}

// TestBalanceFloorGuardSweepFairnessAndGuards pins the 2026-09-13 sweep-SQL
// hardening: currency passes A/C rotate fairly (oldest-checked first) instead
// of starving under LIMIT, pass B mirrors pass C's provider-side guard (a
// pulled row must always stay visible to pass C's restore), and the plan pull
// UPDATE carries the same status/lifecycle column guards as its candidate
// SELECT.
func TestBalanceFloorGuardSweepFairnessAndGuards(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance floor guard source failed: %v", err)
	}
	body := string(src)
	// pass A refresh + pass C restore both rotate oldest-checked-first.
	if got := strings.Count(body, "ORDER BY c.balance_last_checked_at ASC NULLS FIRST"); got != 2 {
		t.Fatalf("expected fair-rotation ORDER BY in both currency SELECTs (A/C), found %d", got)
	}
	// pass B (unaliased UPDATE) mirrors pass C's provider guard via EXISTS.
	for _, marker := range []string{
		"SELECT 1 FROM providers p",
		"WHERE p.id = credentials.provider_id",
		"p.enabled = TRUE\n\t\t        AND COALESCE(p.manual_disabled, FALSE) = FALSE",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("currency pull UPDATE must mirror pass C's provider guard, missing %q", marker)
		}
	}
	// All unaliased routability-guarded UPDATEs (currency pass B + plan pull
	// + cleared-floor release + stale-probe escape hatch) guard
	// status/lifecycle exactly like their candidate SELECTs.
	for _, marker := range []string{
		"AND status = 'active'",
		"AND lifecycle_status = 'active'",
	} {
		if got := strings.Count(body, marker); got != 4 {
			t.Fatalf("marker %q must guard both pull UPDATEs (pass B + plan), the release UPDATE and the escape-hatch UPDATE, found %d", marker, got)
		}
	}
}

// TestBalanceFloorGuardClearedFloorRelease pins the cleared-floor release
// contract: a floor-pulled row must become routable again within one cycle
// once the floor that pulled it is gone — currency floor NULL AND (both plan
// floors NULL, or a non-plan vendor whose plan floors are inert config with
// no probe data source). The release must never touch a zhipu/minimax row
// that still has plan floors configured (owned by the plan hysteresis path)
// or rows pulled for other reasons.
func TestBalanceFloorGuardClearedFloorRelease(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func (g *BalanceFloorGuard) releaseClearedFloorCredentials")
	if idx < 0 {
		t.Fatalf("releaseClearedFloorCredentials not found")
	}
	end := strings.Index(body[idx+1:], "\nfunc ")
	if end < 0 {
		t.Fatalf("release function end not found")
	}
	fn := body[idx : idx+1+end]

	for _, want := range []string{
		"COALESCE(quota_state, 'ok') = 'balance_exhausted'",
		`COALESCE(state_reason_code, '') = 'balance_floor'`,
		"balance_floor_usd IS NULL",
		"quota_floor_tokens IS NULL",
		"quota_floor_percent IS NULL",
		// 惰性 plan 下限边界：非套餐厂商的 plan floor 无数据源、从不参与
		// 摘出/恢复，不得让"清了货币下限"的行永久卡死。
		"OR NOT EXISTS (",
		"IN ('zhipu', 'minimax')",
		// 与 pass C 恢复选点对齐的可路由守卫：他方禁用期间不释放。
		"AND status = 'active'",
		"AND lifecycle_status = 'active'",
		"COALESCE(manual_disabled, FALSE) = FALSE",
		"SELECT 1 FROM providers p",
		"p.enabled = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
	} {
		if !strings.Contains(fn, want) {
			t.Fatalf("release UPDATE missing %q", want)
		}
	}
	cycleIdx := strings.Index(body, "func (g *BalanceFloorGuard) cycle(")
	if cycleIdx < 0 {
		t.Fatalf("cycle not found")
	}
	cycleEnd := strings.Index(body[cycleIdx+1:], "\nfunc ")
	cycleFn := body[cycleIdx : cycleIdx+1+cycleEnd]
	if !strings.Contains(cycleFn, "releaseClearedFloorCredentials") {
		t.Fatalf("cycle must call releaseClearedFloorCredentials")
	}
}

func TestBalanceFloorGuardMainWiring(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read main.go failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"bg.NewBalanceFloorGuard(",
		"balanceFloorGuard.SetKeyring(",
		"balanceFloorGuard.Start(",
		"balanceFloorGuard.Stop()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main.go wiring missing %q", want)
		}
	}
}

// TestBalanceFloorGuardOwnershipExemptions pins the ownership invariant across
// ALL writers of quota_state (2026-09-13 audit round): every path that could
// flip a floor-pulled row back to routable must exempt
// state_reason_code='balance_floor', and the guard's own pulls must not
// clobber availability states owned by other workers.
func TestBalanceFloorGuardOwnershipExemptions(t *testing.T) {
	// 1) write-through: 每个成功请求都会跑的 healthyCredentialSQL 必须豁免
	//    floor 摘除行，否则高峰凭据会被 in-flight 成功瞬间 un-pull（P0）。
	np, err := os.ReadFile("node_probe_write_through.go")
	if err != nil {
		t.Fatalf("read node probe write-through failed: %v", err)
	}
	if !strings.Contains(string(np), `COALESCE(state_reason_code, '') <> 'balance_floor'`) {
		t.Fatalf("healthyCredentialSQL must exempt balance_floor-pulled rows")
	}
	// 2) webhook 充值回调：floor 摘除行不得派发 chat 探测。
	bqp, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance quota probe failed: %v", err)
	}
	for _, want := range []string{
		"credentialFloorPulled",
		"guard owns recovery",
		// 定期 sweep 的选点豁免（带表别名）与 webhook 的摘除判定。
		"state_reason_code, '') <> 'balance_floor'",
		"state_reason_code, '') = 'balance_floor'",
	} {
		if !strings.Contains(string(bqp), want) {
			t.Fatalf("balance quota probe missing %q", want)
		}
	}
	// 3) guard 自己的摘出不得覆盖 auth_failed / 他方 suspended。
	guard, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance floor guard failed: %v", err)
	}
	gb := string(guard)
	if got := strings.Count(gb, "COALESCE(availability_state, 'ready') NOT IN ('suspended', 'auth_failed')"); got < 2 {
		t.Fatalf("both pull UPDATEs must guard availability ownership, found %d", got)
	}
	// 4) writer.go 的配额分支不得重打 balance_floor reason（所有权丢失会
	//    让 probe 豁免失效）。
	wr, err := os.ReadFile("../domains/credential/writer.go")
	if err != nil {
		t.Fatalf("read writer failed: %v", err)
	}
	if got := strings.Count(string(wr), `COALESCE(state_reason_code, '') <> 'balance_floor'`); got < 2 {
		t.Fatalf("writer quota branches must not rebrand balance_floor rows, found %d", got)
	}
}

func TestPlanWindowResetAtParsing(t *testing.T) {
	st, err := parseZhipuPlan([]byte(`{"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"percentage":1,"remaining":100,
		 "nextResetTime":1787776664946}]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := time.UnixMilli(1787776664946)
	if st.Windows[0].ResetAt == nil || !st.Windows[0].ResetAt.Equal(want) {
		t.Fatalf("reset_at = %v, want %v", st.Windows[0].ResetAt, want)
	}
}

func TestBalanceFloorGuardDisabledNoCycle(t *testing.T) {
	t.Setenv("LLM_GATEWAY_BALANCE_FLOOR_GUARD", "off")
	g := NewBalanceFloorGuard(nil, nil)
	if !g.disabled {
		t.Fatalf("guard must be disabled with LLM_GATEWAY_BALANCE_FLOOR_GUARD=off")
	}
	// CycleNow 在 disabled/db nil 时必须是无操作成功，供测试/运维安全调用。
	if err := g.CycleNow(context.Background()); err != nil {
		t.Fatalf("CycleNow on disabled guard must be a no-op, got %v", err)
	}
}

// TestBalanceFloorGuardEscapeHatchThreshold pins the #4 escape-hatch env
// contract (R28, 2026-09-14; default tightened 24h→2h by audit B-E1
// 2026-09-16): LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS defaults
// to 2, 0 disables the hatch entirely, positive values are honoured, and a
// value whose hours→Duration conversion overflows falls back to the default
// (same d>0 recheck as the interval knob — A-P3-5) instead of producing a
// negative duration that would corrupt the staleness comparison.
func TestBalanceFloorGuardEscapeHatchThreshold(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		wantStale time.Duration
		wantOff   bool
	}{
		{"default when unset", "", 2 * time.Hour, false},
		{"zero disables hatch", "0", 2 * time.Hour, true},
		{"hours honoured", "72", 72 * time.Hour, false},
		{"off string does not disable hatch", "off", 2 * time.Hour, false},
		{"negative falls back to default", "-5", 2 * time.Hour, false},
		{"int64 hours overflow falls back to default", "9223372036854775807", 2 * time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.env == "" {
				t.Setenv("LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS", "")
				os.Unsetenv("LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS")
			} else {
				t.Setenv("LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS", c.env)
			}
			g := NewBalanceFloorGuard(nil, nil)
			if g.escapeOff != c.wantOff {
				t.Fatalf("escapeOff = %v, want %v (env %q)", g.escapeOff, c.wantOff, c.env)
			}
			if g.escapeStale != c.wantStale {
				t.Fatalf("escapeStale = %v, want %v (env %q)", g.escapeStale, c.wantStale, c.env)
			}
		})
	}
}

// TestBalanceFloorGuardStaleProbeEscapeHatch pins the #4 plan-path escape
// hatch SQL (R28, 2026-09-14): floor-pulled rows whose plan probe evidence
// went cold (checked_at NULL or older than the threshold) are released with
// the same ownership invariants as the cleared-floor release, and cycle() must
// run it AFTER releaseClearedFloorCredentials and BEFORE sweepPlanQuotas so a
// same-tick successful probe re-pulls via floorPull with fresh checked_at.
func TestBalanceFloorGuardStaleProbeEscapeHatch(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	idx := strings.Index(body, "func (g *BalanceFloorGuard) releaseStaleProbeFloorCredentials")
	if idx < 0 {
		t.Fatalf("releaseStaleProbeFloorCredentials not found")
	}
	end := strings.Index(body[idx+1:], "\nfunc ")
	if end < 0 {
		t.Fatalf("escape hatch function end not found")
	}
	fn := body[idx : idx+1+end]

	// 释放只碰自己的行 + 陈旧判定（NULL 或早于阈值）。
	for _, want := range []string{
		// 陈旧判据：plan 证据缺失或早于 $1 阈值。
		"plan_quota_checked_at IS NULL",
		"plan_quota_checked_at < now() - $1::interval",
		// 只针对 plan-floor 摘出的行（货币行由 pass C / 清下限释放负责）。
		"quota_floor_tokens IS NOT NULL OR quota_floor_percent IS NOT NULL",
		// 所有权不变量：与 releaseClearedFloorCredentials 一致。
		"COALESCE(quota_state, 'ok') = 'balance_exhausted'",
		`COALESCE(state_reason_code, '') = 'balance_floor'`,
		"AND status = 'active'",
		"AND lifecycle_status = 'active'",
		"COALESCE(manual_disabled, FALSE) = FALSE",
		"SELECT 1 FROM providers p",
		"p.enabled = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		// 释放文案供日志/审计检索。
		"escape hatch, plan probe stale",
		// env=0 关闭逃生门（不执行 UPDATE）。
		"if g.escapeOff",
	} {
		if !strings.Contains(fn, want) {
			t.Fatalf("escape hatch UPDATE missing %q", want)
		}
	}
	// 绝不写 manual_disabled / quota_floor_*（逃生门只翻状态，不动配置）。
	for _, banned := range []string{"manual_disabled = TRUE", "quota_floor_tokens ="} {
		if strings.Contains(fn, banned) {
			t.Fatalf("escape hatch UPDATE must not assign %q", banned)
		}
	}

	// cycle 顺序：cleared-floor release → escape hatch → plan sweep。
	cycleIdx := strings.Index(body, "func (g *BalanceFloorGuard) cycle(")
	if cycleIdx < 0 {
		t.Fatalf("cycle not found")
	}
	cycleEnd := strings.Index(body[cycleIdx+1:], "\nfunc ")
	cycleFn := body[cycleIdx : cycleIdx+1+cycleEnd]
	cleared := strings.Index(cycleFn, "g.releaseClearedFloorCredentials(")
	escape := strings.Index(cycleFn, "g.releaseStaleProbeFloorCredentials(")
	sweep := strings.Index(cycleFn, "g.sweepPlanQuotas(")
	if cleared < 0 || escape < 0 || sweep < 0 {
		t.Fatalf("cycle must call releaseClearedFloorCredentials, releaseStaleProbeFloorCredentials and sweepPlanQuotas")
	}
	if !(cleared < escape && escape < sweep) {
		t.Fatalf("cycle order must be cleared-floor release -> escape hatch -> plan sweep, got %d/%d/%d", cleared, escape, sweep)
	}
}

// TestBalanceFloorGuardPlanProbeFailureBackoff pins the #12a failure-backoff
// wiring (R28, 2026-09-14): a failed plan probe stamps
// plan_quota_probe_failed_at WITHOUT touching plan_quota_checked_at (the
// escape hatch's staleness evidence —刷新它会永久哑火 #4), the sweep scan
// parks failed rows for 15 minutes and sinks them to the front by the
// COALESCEd stamp, and persistPlanState clears the stamp on success.
func TestBalanceFloorGuardPlanProbeFailureBackoff(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)

	// 1) 失败分支记账：handlePlanCredential 的失败 UPDATE 只写 failed_at。
	hidx := strings.Index(body, "func (g *BalanceFloorGuard) handlePlanCredential")
	if hidx < 0 {
		t.Fatalf("handlePlanCredential not found")
	}
	hend := strings.Index(body[hidx+1:], "\nfunc ")
	hfn := body[hidx : hidx+1+hend]
	failIdx := strings.Index(hfn, "balance_floor_guard: plan probe failed")
	if failIdx < 0 {
		t.Fatalf("failure log marker not found")
	}
	// 失败分支：从失败日志到函数内 persistPlanState 调用之间是记账 UPDATE。
	persistIdx := strings.Index(hfn, "g.persistPlanState(")
	if persistIdx < 0 || persistIdx < failIdx {
		t.Fatalf("handlePlanCredential structure changed: persistPlanState call not after failure branch")
	}
	failBranch := hfn[failIdx:persistIdx]
	stmtStart := strings.Index(failBranch, "UPDATE credentials")
	if stmtStart < 0 {
		t.Fatalf("failure branch stamp UPDATE not found")
	}
	stmtEnd := strings.Index(failBranch[stmtStart:], "`")
	if stmtEnd < 0 {
		t.Fatalf("failure branch stamp UPDATE terminator not found")
	}
	failSQL := failBranch[stmtStart : stmtStart+stmtEnd]
	if !strings.Contains(failSQL, "SET plan_quota_probe_failed_at = now()") {
		t.Fatalf("failure branch must stamp plan_quota_probe_failed_at")
	}
	// 关键耦合：失败记账绝不写 plan_quota_checked_at（#4 逃生门判据）。
	if strings.Contains(failSQL, "plan_quota_checked_at") {
		t.Fatalf("failure branch must NOT write plan_quota_checked_at (would neutralize the #4 escape hatch staleness check)")
	}

	// 2) 成功清戳：persistPlanState 置 failed_at = NULL。
	pidx := strings.Index(body, "func (g *BalanceFloorGuard) persistPlanState")
	if pidx < 0 {
		t.Fatalf("persistPlanState not found")
	}
	pend := strings.Index(body[pidx+1:], "\nfunc ")
	pfn := body[pidx : pidx+1+pend]
	if !strings.Contains(pfn, "plan_quota_probe_failed_at = NULL") {
		t.Fatalf("persistPlanState must clear plan_quota_probe_failed_at on success")
	}

	// 3) 扫描退避：失败行冷却 15 分钟，且按 COALESCE(failed_at, checked_at)
	//    沉底排序；非失败行仍按 checked_at 公平轮转。
	sidx := strings.Index(body, "func (g *BalanceFloorGuard) sweepPlanQuotas")
	if sidx < 0 {
		t.Fatalf("sweepPlanQuotas not found")
	}
	send := strings.Index(body[sidx+1:], "\nfunc ")
	sfn := body[sidx : sidx+1+send]
	if !strings.Contains(sfn, "COALESCE(c.plan_quota_probe_failed_at, to_timestamp(0)) < now() - interval '15 minutes'") {
		t.Fatalf("plan sweep must park failed rows for 15 minutes")
	}
	if !strings.Contains(sfn, "ORDER BY COALESCE(c.plan_quota_probe_failed_at, c.plan_quota_checked_at) ASC NULLS FIRST") {
		t.Fatalf("plan sweep must order by COALESCE(failed_at, checked_at) ASC NULLS FIRST")
	}
}

// ------------------------------------------- lifecycle & concurrency (2026-09-16) --
// 审计修复：A-C1 keyring 锁 / A-C2 Start 重入 / B-E1 逃生门 2h / D-L1 Stop join /
// E-B1 并发探测 / F-L1 周期汇总 / F-L2 Warn 限流 / G-O1 HTTP 重试。

// TestBalanceFloorGuardStartReentryAndStopJoin pins A-C2 + D-L1: a second
// Start must not spawn another worker (proven by workerDone staying the same
// channel), Stop must return only after the worker actually exited (proven by
// workerDone being closed), and Start-after-Stop must not revive the worker.
func TestBalanceFloorGuardStartReentryAndStopJoin(t *testing.T) {
	t.Setenv("LLM_GATEWAY_BALANCE_FLOOR_INTERVAL", "1h") // ticker 在测试内不点火
	g := NewBalanceFloorGuard(nil, nil)
	g.Start(context.Background())
	g.lifecycleMu.Lock()
	started, firstDone := g.started, g.workerDone
	g.lifecycleMu.Unlock()
	if !started || firstDone == nil {
		t.Fatalf("Start must arm the worker (started=%v done=%v)", started, firstDone)
	}
	// A-C2: 重入 Start 不得再起 worker。
	g.Start(context.Background())
	g.lifecycleMu.Lock()
	secondDone := g.workerDone
	g.lifecycleMu.Unlock()
	if secondDone != firstDone {
		t.Fatalf("reentrant Start must be a no-op (A-C2)")
	}
	// D-L1: Stop 必须等 worker 退出 —— 返回后 workerDone 一定已关闭。
	g.Stop()
	select {
	case <-firstDone:
	default:
		t.Fatalf("Stop must wait for worker exit (D-L1)")
	}
	// 一次性生命周期：Stop 后 Start 不得复活 worker；重复 Stop 幂等。
	g.Start(context.Background())
	g.Stop()
	g.lifecycleMu.Lock()
	after := g.workerDone
	g.lifecycleMu.Unlock()
	if after != firstDone {
		t.Fatalf("Start after Stop must not re-arm the worker")
	}
}

// TestBalanceFloorGuardSetKeyringConcurrent exercises the A-C1 lock: concurrent
// SetKeyring writers racing decryptKey readers. Only meaningful under -race —
// the ciphertext is shaped as a v1 envelope so DecryptAny actually reads the
// keyring before failing on the GCM tag.
func TestBalanceFloorGuardSetKeyringConcurrent(t *testing.T) {
	g := NewBalanceFloorGuard(nil, nil)
	var key [32]byte
	key[0] = 7
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g.SetKeyring(kr)
		}()
		go func() {
			defer wg.Done()
			// 解密必然失败，但读取路径必须触碰 keyring —— -race 捕获无锁读。
			_, _ = g.decryptKey([]byte("v1:k1:not-a-real-envelope"))
		}()
	}
	wg.Wait()
}

// TestBalanceFloorGuardPlanConcurrencyEnv pins the E-B1 env knob contract:
// default 10, values clamped into 1..100, unparsable falls back to default.
func TestBalanceFloorGuardPlanConcurrencyEnv(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 10},
		{"4", 4},
		{"0", 1},
		{"-3", 1},
		{"1000", 100},
		{"abc", 10},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			if c.env == "" {
				t.Setenv("LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY", "")
				os.Unsetenv("LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY")
			} else {
				t.Setenv("LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY", c.env)
			}
			g := NewBalanceFloorGuard(nil, nil)
			if g.planConcurrency != c.want {
				t.Fatalf("planConcurrency = %d, want %d (env %q)", g.planConcurrency, c.want, c.env)
			}
		})
	}
}

// TestRunPlanProbesBoundedConcurrency pins E-B1 worker-pool semantics: at
// most `limit` probes in flight, every candidate processed exactly once.
func TestRunPlanProbesBoundedConcurrency(t *testing.T) {
	const limit = 4
	var inFlight, maxInFlight, processed atomic.Int64
	cands := make([]floorCandidate, 24)
	for i := range cands {
		cands[i] = floorCandidate{ID: int64(i + 1)}
	}
	runPlanProbes(context.Background(), cands, limit, func(ctx context.Context, c floorCandidate) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		defer processed.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
	})
	if got := maxInFlight.Load(); got > limit {
		t.Fatalf("max in-flight probes = %d, want <= %d", got, limit)
	}
	if got := processed.Load(); got != int64(len(cands)) {
		t.Fatalf("processed = %d, want %d", got, len(cands))
	}
}

// TestRunPlanProbesRecoversPanic pins per-candidate panic isolation on the
// worker pool — one panicking probe must not skip the remaining candidates.
func TestRunPlanProbesRecoversPanic(t *testing.T) {
	cands := []floorCandidate{{ID: 1}, {ID: 2}}
	var ran atomic.Int64
	runPlanProbes(context.Background(), cands, 2, func(ctx context.Context, c floorCandidate) {
		ran.Add(1)
		if c.ID == 1 {
			panic("boom")
		}
	})
	if ran.Load() != 2 {
		t.Fatalf("panic in one candidate must not skip others, ran=%d", ran.Load())
	}
}

// TestPlanProbeWarnGateRateLimit pins F-L2 rate limiting: first failure per
// credential warns, repeats inside the window are suppressed, other
// credentials warn independently, and the window expiry re-arms the Warn.
func TestPlanProbeWarnGateRateLimit(t *testing.T) {
	w := newWarnGate(15 * time.Minute)
	now := time.Now()
	if !w.allow(1, now) {
		t.Fatalf("first failure must warn")
	}
	if w.allow(1, now.Add(time.Minute)) {
		t.Fatalf("second failure within the window must be rate-limited")
	}
	if !w.allow(2, now.Add(time.Minute)) {
		t.Fatalf("another credential must warn independently")
	}
	if !w.allow(1, now.Add(15*time.Minute)) {
		t.Fatalf("after the window a new Warn must be due")
	}
}

// TestRetryableHTTPErr pins the G-O1 retry classification: transport errors
// and 5xx retry, 4xx and caller-context cancellation do not.
func TestRetryableHTTPErr(t *testing.T) {
	if retryableHTTPErr(nil) {
		t.Fatalf("nil must not be retryable")
	}
	if retryableHTTPErr(context.Canceled) || retryableHTTPErr(context.DeadlineExceeded) {
		t.Fatalf("ctx errors must not be retryable")
	}
	if retryableHTTPErr(&httpStatusError{code: 401, url: "u"}) {
		t.Fatalf("4xx must not be retryable")
	}
	if !retryableHTTPErr(&httpStatusError{code: 502, url: "u"}) {
		t.Fatalf("5xx must be retryable")
	}
	if !retryableHTTPErr(errors.New("dial tcp: i/o timeout")) {
		t.Fatalf("transport error must be retryable")
	}
}

// TestGetJSONRetryOn5xxThenSuccess pins G-O1 behaviour end to end: two 502s
// followed by a 200 must succeed on the third attempt.
func TestGetJSONRetryOn5xxThenSuccess(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	g := NewBalanceFloorGuard(nil, nil)
	body, err := g.getJSON(context.Background(), srv.URL, "Authorization", "test")
	if err != nil {
		t.Fatalf("retry must recover from 5xx: %v", err)
	}
	if calls.Load() != 3 || !strings.Contains(string(body), "ok") {
		t.Fatalf("calls=%d body=%q, want 3 attempts then success", calls.Load(), body)
	}
}

// TestGetJSONNoRetryOn4xx pins that a definitive 4xx (bad key / bad URL) is
// answered to the caller after exactly one attempt.
func TestGetJSONNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	g := NewBalanceFloorGuard(nil, nil)
	_, err := g.getJSON(context.Background(), srv.URL, "Authorization", "test")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("want HTTP 401 error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("4xx must not be retried, calls=%d", calls.Load())
	}
}

// TestGetJSONRetryExhausted pins the attempt ceiling: persistent 5xx fails
// after exactly 1 initial + 2 retries.
func TestGetJSONRetryExhausted(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	g := NewBalanceFloorGuard(nil, nil)
	_, err := g.getJSON(context.Background(), srv.URL, "Authorization", "test")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("want HTTP 500 error after exhaustion, got %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want exactly 3 attempts (1+2 retries)", calls.Load())
	}
}

// TestBalanceFloorGuardConcurrencyContracts pins the 2026-09-16 audit-fix
// markers in the guard source: keyring RWMutex (A-C1), Start reentry guard
// (A-C2), escape default 2h (B-E1), Stop join (D-L1), bounded probe pool +
// env knob (E-B1), sweep summary log (F-L1), Warn rate limit (F-L2), and the
// typed retry policy (G-O1).
func TestBalanceFloorGuardConcurrencyContracts(t *testing.T) {
	src, err := os.ReadFile("balance_floor_guard.go")
	if err != nil {
		t.Fatalf("read balance floor guard source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		// A-C1: keyring 写（SetKeyring）与读（decryptKey）都在锁内。
		"g.keyringMu.Lock()",
		"g.keyringMu.RLock()",
		// A-C2: Start 重入守卫。
		"if g.started {",
		// B-E1: 逃生门默认收紧为 2h。
		"escapeStale := 2 * time.Hour",
		// D-L1: Stop join workerDone。
		"close(g.workerDone)",
		"case <-done:",
		// E-B1: 有界 worker pool + 并发 env 旋钮。
		"semaphore.NewWeighted(",
		"LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY",
		// F-L1: 周期级汇总日志。
		"balance_floor_guard: plan sweep completed",
		// F-L2: 失败日志升 Warn + 限流闸。
		`slog.Warn("balance_floor_guard: plan probe failed"`,
		"g.warnGate.allow(",
		// G-O1: 类型化状态错误 + 重试判定。
		"httpStatusError",
		"retryableHTTPErr",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("balance floor guard is missing %q", want)
		}
	}
}
