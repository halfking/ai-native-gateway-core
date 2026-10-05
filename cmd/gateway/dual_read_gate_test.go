package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// readGoSource 读同包源码。go test 的工作目录是包目录本身。
func readGoSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

var (
	gateBlockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	gateLineCommentRE  = regexp.MustCompile(`(?m)//[^\n]*`)
)

// stripGoComments 必须先于任何源码断言：判据一旦能在注释里被满足，就不是判据。
// （本文件头部的注释里就写着 `sum.S4Ready = sum.GenuineLossRows == 0` 这段
// 被禁止的写法——不剥离注释，下面那道源码门会永远红。）
func stripGoComments(src string) string {
	src = gateBlockCommentRE.ReplaceAllString(src, " ")
	return gateLineCommentRE.ReplaceAllString(src, " ")
}

// 2026-10-02：S4 前置门的真空为绿修复。
//
// 这些门的作用是让「S4 停写前置判据」这个**唯一会给出许可的信号**不能在没有
// 证据的情况下变绿。原先 `S4Ready = GenuineLossRows == 0`，而停写让该条件恒真
// （真库实测见 dual_read_gate.go 头部），于是这道门在关停的那一刻起永久报绿、
// 且响应与健康态逐字节相同。

// s4ReadyJSON 把判定结果按生产形状序列化后再断言。
//
// 为什么不直接断言结构体字段：这道门要防的失效模式是「JSON 里少了一个字段」
// 或「字段名拼错」，而结构体断言对这两者完全无感——字段存在，值也对，只是
// 调用方按名字读不到。断言打在产物上。
func s4ReadyJSON(t *testing.T, in s4GateInput) map[string]any {
	t.Helper()
	v := s4GateVerdictOf(in)
	sum := MirrorDriftSummary{
		V1Rows:           in.v1Rows,
		GenuineLossRows:  in.genuineLoss,
		V1WritesEnabled:  in.v1WritesOn,
		S4Ready:          v.Ready,
		S4GateVoid:       v.Void,
		S4GateVoidReason: v.Reason,
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestS4Gate_NeverReadyOnVacuousEvidence 覆盖缺陷本身。
//
// 每一行都是「没有证据」的一种形态。判据统一为：**产物里 s4_ready 不得为 true**。
func TestS4Gate_NeverReadyOnVacuousEvidence(t *testing.T) {
	cases := []struct {
		name       string
		in         s4GateInput
		wantVoid   bool
		wantReason string
	}{
		{
			name:       "v1 停写 + 零真漏写（缺陷本体：停写后恒真）",
			in:         s4GateInput{v1Rows: 0, genuineLoss: 0, v1WritesOn: false, v1CoveragePP: 0, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonV1WritesDisabled,
		},
		{
			name:       "v1 停写但窗口里还有 V1 行（冻结期的尾部窗口）",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: false, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonV1WritesDisabled,
		},
		{
			name:       "v1 写入中但窗口零 V1 流量（空扫描 = 什么都没证明）",
			in:         s4GateInput{v1Rows: 0, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 0, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonNoV1Traffic,
		},
		{
			name:       "v1 写入中、零真漏写、有流量且覆盖率达标 —— 唯一允许报绿的一行",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   false,
			wantReason: "",
		},
		{
			name:       "真漏写 > 0：不是 void，是真的不安全",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 1, v1WritesOn: true, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   false,
			wantReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prod := s4ReadyJSON(t, tc.in)

			ready, ok := prod["s4_ready"].(bool)
			if !ok {
				t.Fatalf("产物缺 s4_ready 布尔字段：%v", prod)
			}
			if ready != tc.in.v1WritesOn && tc.in.genuineLoss == 0 && tc.in.v1Rows > 0 {
				t.Fatalf("期望 s4_ready=%v，实际 %v", tc.in.v1WritesOn, ready)
			}
			// 本表的核心断言：三条 void 行都必须报 false。
			if tc.wantVoid && ready {
				t.Fatalf("真空为绿：s4_ready=%v 而判定为 void（reason=%q）—— JSON=%v",
					ready, prod["s4_gate_void_reason"], prod)
			}
			if got := prod["s4_gate_void"].(bool); got != tc.wantVoid {
				t.Fatalf("s4_gate_void=%v，期望 %v（reason=%v）", got, tc.wantVoid, prod["s4_gate_void_reason"])
			}
			if got, _ := prod["s4_gate_void_reason"].(string); got != tc.wantReason {
				t.Fatalf("s4_gate_void_reason=%q，期望 %q", got, tc.wantReason)
			}
			if got, _ := prod["v1_writes_enabled"].(bool); got != tc.in.v1WritesOn {
				t.Fatalf("v1_writes_enabled=%v，期望 %v", got, tc.in.v1WritesOn)
			}
		})
	}
}

// TestS4Gate_ReadyRequiresBothEvidenceKinds 钉住「两条证据缺一不可」。
//
// 分开写是因为上面那张表在「两条都满足」时只有一行 ready=true；这里把
// 「只满足一条」单独拎出来，防止将来有人把任一条挪进「不必需」分支。
func TestS4Gate_ReadyRequiresBothEvidenceKinds(t *testing.T) {
	bothOn := s4GateVerdictOf(s4GateInput{v1Rows: 1, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 100, windowHours: 24})
	if !bothOn.Ready || bothOn.Void {
		t.Fatalf("两条证据都在时必须 ready 且非 void，实得 %+v", bothOn)
	}
	for _, in := range []s4GateInput{
		{v1Rows: 1, genuineLoss: 0, v1WritesOn: false, v1CoveragePP: 100, windowHours: 24}, // 缺「写入中」
		{v1Rows: 0, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 0, windowHours: 24},    // 缺「扫到东西」
	} {
		v := s4GateVerdictOf(in)
		if v.Ready {
			t.Fatalf("缺一条证据却报 ready：%+v（输入 %+v）", v, in)
		}
		if !v.Void {
			t.Fatalf("缺一条证据必须是 void（可区分的失败），实得 %+v（输入 %+v）", v, in)
		}
	}
}

// TestS4Gate_VoidIsNeverReady pins the invariant as a relation rather than as
// five table rows: no void verdict may be ready, for any input at all. This is
// the property the defect violated, and unlike the table it cannot be defeated
// by adding a new reason string.
func TestS4Gate_VoidIsNeverReady(t *testing.T) {
	for _, rows := range []int64{0, 1, 2, 1000} {
		for _, loss := range []int64{0, 1, 50} {
			for _, on := range []bool{true, false} {
				v := s4GateVerdictOf(s4GateInput{v1Rows: rows, genuineLoss: loss, v1WritesOn: on, v1CoveragePP: 100, windowHours: 24})
				if v.Void && v.Ready {
					t.Fatalf("void⇒ready 违反：rows=%d loss=%d on=%v → %+v", rows, loss, on, v)
				}
				if v.Void && v.Reason == "" {
					t.Fatalf("void 必须带 reason，否则调用方无法行动：%+v", v)
				}
				if !v.Void && v.Reason != "" {
					t.Fatalf("非 void 不得带 void reason：%+v", v)
				}
			}
		}
	}
}

// TestSummarizeNoLongerAssignsS4ReadyDirectly 是这道修复的**回归门**：
// 断言 Summarize 里对 S4Ready 的赋值**有且仅有一处**，且那一处来自 verdict。
//
// 为什么不用「禁止出现 `sum.S4Ready =`」这种写法：修好后的正确代码正是
// `sum.S4Ready = verdict.Ready`，它同样以 `sum.S4Ready =` 开头。第一版守卫就是这么
// 写的，结果对自己的正确实现报红——守卫写宽会误伤正确代码，而误报的守卫会被
// 关掉。正确形态是**数赋值点**并核对右值，而不是禁前缀。
//
// 上一版守不住「顺手简化」的场景也在这里：若有人把右值换回直接判定，赋值点
// 仍是唯一的一处，但右值不再是 verdict.Ready，本门照红。
func TestSummarizeNoLongerAssignsS4ReadyDirectly(t *testing.T) {
	clean := stripGoComments(readGoSource(t, "dual_read_validator.go"))

	assigns := s4ReadyAssignRE.FindAllStringSubmatch(clean, -1)
	if len(assigns) != 1 {
		t.Fatalf("S4Ready 的赋值点必须恰好 1 处，实得 %d：%q", len(assigns), assigns)
	}
	if got := strings.TrimSpace(assigns[0][1]); got != "verdict.Ready" {
		t.Fatalf("S4Ready 的右值必须是 verdict.Ready（经 s4GateVerdictOf 判定），实得 %q——"+
			"直接判定会让该字段在停写后恒真", got)
	}
	if !strings.Contains(clean, "s4GateVerdictOf(") {
		t.Fatal("Summarize 未调用 s4GateVerdictOf：判定被绕开了")
	}
	// 判定的输入必须真的用上了「窗口扫到多少 V1 行」——只看真漏写会把空扫描
	// 判成通过，而空扫描什么都没证明。
	//
	// ⚠ 这里用正则而不是 strings.Contains + 硬编码空格。§9.233.2 记过一次
	// 同族的错（Evidence 锚在 `` `+fn()+` `` 与 `` ` + fn() + ` `` 上，
	// gofmt 在两种拼写间切换，3 个文件失配）；本节又踩了一次 ——
	// 往字面量里加一个更长的字段名，gofmt 会把整个字面量重新对齐，
	// `v1Rows:      sum.V1Rows` 的 6 个空格变成 7 个，而 Contains 不认。
	// ⇒ **锚在标识符 token 上，且对 gofmt 会改写的排版免疫。**
	for _, f := range []struct{ structField, summaryField string }{
		{"v1Rows", "V1Rows"},
		{"genuineLoss", "GenuineLossRows"},
		{"v1CoveragePP", "V1CoveragePP"},
		{"windowHours", "WindowHours"},
	} {
		if !s4GateArgRE(f.structField, f.summaryField).MatchString(clean) {
			t.Fatalf("s4GateVerdictOf 未接收 sum.%s：这条输入没进判定，门就少一条证据（§9.235）",
				f.summaryField)
		}
	}
}

// s4GateArgRE 匹配调用点里 `<结构体字段>: sum.<汇总字段>`，不关心 gofmt 怎么排版。
//
// 两个名字分开传，因为它们**确实不同**：`s4GateInput` 的字段是未导出的小写名
// （v1Rows），`MirrorDriftSummary` 的字段是导出的（大写 V1Rows）。
// 第一版图省事只传一个名字，于是正则要求字面量里出现 `V1Rows:` ——
// 而源码写的是 `v1Rows:` ⇒ 永远匹配不上。
// ★ 那是一条**恒假的断言**：它不会误报，只会一直红，诱使人把判据删掉而不是修。
// 教训：**锚点写死时，先在真实源码上跑一次**；一条从来没绿过的门不是门。
func s4GateArgRE(structField, summaryField string) *regexp.Regexp {
	return regexp.MustCompile(structField + `\s*:\s*sum\.` + summaryField + `\b`)
}

var s4ReadyAssignRE = regexp.MustCompile(`sum\.S4Ready\s*=\s*([^\n]+)`)

// TestZeroDriftIsNotAlwaysFalse guards the opposite direction of the same
// root cause: with v1 writes off, ZeroDrift must be reported as *not
// evaluable* rather than as *drifted*, because a frozen v1 makes OnlyInV2 grow
// with the clock. A permanently-false gate is as useless as a permanently-true
// one, and it would make the spec's "7 天零漂移" exit condition unsatisfiable.
func TestZeroDriftIsNotAlwaysFalse(t *testing.T) {
	src := readGoSource(t, "dual_read_validator.go")
	clean := stripGoComments(src)
	if !strings.Contains(clean, "ZeroDriftEvaluable") {
		t.Fatal("DualReadDetail 必须带 ZeroDriftEvaluable 字段")
	}
	if !strings.Contains(clean, "d.ZeroDriftEvaluable = d.V1WritesEnabled") {
		t.Fatal("ZeroDriftEvaluable 未与 V1WritesEnabled 绑定：停写后仍会被当作真漂移读")
	}
	// 赋 ZeroDrift 之前必须先有可评估性判定，否则顺序型回归会静默通过。
	iEval := strings.Index(clean, "d.ZeroDriftEvaluable = d.V1WritesEnabled")
	iAssign := strings.Index(clean, "d.ZeroDrift = d.OnlyInV1Count == 0")
	if iEval < 0 || iAssign < 0 || iEval > iAssign {
		t.Fatalf("可评估性判定必须在 ZeroDrift 赋值之前（eval@%d, assign@%d）", iEval, iAssign)
	}
}

// TestS4Gate_CoverageRule is the control pair for rule 3 (§9.235).
//
// The first two rules are binary and had been in place since 2026-10-02. This
// one is new, and a new rule on a gate that grants an irreversible permission
// needs both directions proven — but the direction that matters most is the
// one that had **no real-world example**: measured v1 coverage is 95%+ for any
// recent window, so "v1 wrote for a fifth of the window" has never been
// observed. These rows are the only place it is ever exercised.
//
// The two headline rows are measured, not invented:
//
//	last 6h  → 0.00%  (v1's newest row is hours old)
//	last 24h → 68.00%
//
// Both have v1Rows > 0, so both pass the older "no_v1_traffic_in_window" rule.
func TestS4Gate_CoverageRule(t *testing.T) {
	cases := []struct {
		name       string
		in         s4GateInput
		wantVoid   bool
		wantReason string
	}{
		{
			// The one measured case that triggers rule 3, verified with BOTH
			// storage faces on 2026-10-05: v1 wrote in 26 of the 128 hours that
			// saw traffic on either side, across the 09-06→09-12 window that
			// contains the local v1 write outage.
			name:       "实测 09-06~09-12 窗口：跨 5 天写入中断，覆盖率 20.31%",
			in:         s4GateInput{v1Rows: 76100, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 20.31, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonInsufficientV1Coverage,
		},
		{
			// ⚠ SYNTHETIC, not measured. An earlier draft of this table listed
			// "last 6h → 0%" and "last 24h → 68%" as measured local shapes. They
			// were artefacts of a manual query that read only `request_logs` and
			// not `request_logs_hot` — §9.160.7's trap, hit for the third time in
			// this section, and this time in the evidence table for the gate
			// written to catch exactly that class of mistake. Re-measured with
			// both faces, v1 is writing normally and the last 6h/24h/72h are all
			// at 100%.
			//
			// The row stays, as a synthetic input, because the "v1 rows present
			// but coverage near zero" direction still has no real example — and
			// saying so is the honest reason for it to be here.
			name:       "构造：v1 有行但覆盖率 0%（无真实样本）",
			in:         s4GateInput{v1Rows: 3120, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 0, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonInsufficientV1Coverage,
		},
		{
			// Measured 2026-10-05 with both faces: the last 6h, 24h and 72h are
			// each 100% (25/25, 25/25, 73/73 traffic-bearing hours). This is
			// the shape rule 3 must let through, so its absence would make the
			// rule un-actionable.
			name:       "实测 24h 窗口：覆盖率 100%",
			in:         s4GateInput{v1Rows: 4789, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   false,
			wantReason: "",
		},
		{
			// Rule 2 must keep its own reason: "nothing to compare at all" and
			// "compared a fifth of it" send the operator to different knobs.
			name:       "零覆盖但 v1 完全没有行 —— 归 rule 2，不归 rule 3",
			in:         s4GateInput{v1Rows: 0, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 0, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonNoV1Traffic,
		},
		{
			name:       "刚过门槛",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 90.01, windowHours: 24},
			wantVoid:   false,
			wantReason: "",
		},
		{
			name:       "刚不过门槛",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 89.99, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonInsufficientV1Coverage,
		},
		{
			// Coverage is an independent third input: a real loss must still
			// win over adequate coverage, and must still be reported as drift
			// rather than void.
			name:       "覆盖率达标但有真漏写 —— 仍是不安全，不是 void",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 3, v1WritesOn: true, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   false,
			wantReason: "",
		},
		{
			// Write-disable still outranks coverage: if v1 is off there is
			// nothing to compare regardless of what coverage says.
			name:       "v1 停写优先于覆盖率",
			in:         s4GateInput{v1Rows: 100, genuineLoss: 0, v1WritesOn: false, v1CoveragePP: 100, windowHours: 24},
			wantVoid:   true,
			wantReason: s4GateReasonV1WritesDisabled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s4GateVerdictOf(tc.in)
			if got.Void != tc.wantVoid {
				t.Fatalf("Void=%v want %v (reason=%q, input=%+v)", got.Void, tc.wantVoid, got.Reason, tc.in)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason=%q want %q (input=%+v)", got.Reason, tc.wantReason, tc.in)
			}
			// The invariant that matters most: a void must never also be ready,
			// whatever the input. Spelled as a relation so a future rule cannot
			// defeat it by adding another branch.
			if got.Void && got.Ready {
				t.Fatalf("void 且 ready 同时成立：%+v（input=%+v）", got, tc.in)
			}
		})
	}
}

// TestS4Gate_CoverageIsReported pins the response field.
//
// The gate's whole point is to stop reporting a conclusion with no sample size
// attached. A void reason that does not say how much was actually compared
// leaves the operator to guess, and the guess they will make is the old one.
func TestS4Gate_CoverageIsReported(t *testing.T) {
	src := stripGoComments(readGoSource(t, "dual_read_validator.go"))
	if !strings.Contains(src, `V1CoveragePP float64 `+"`"+`json:"v1_coverage_pp"`) {
		t.Fatal("MirrorDriftSummary 少了 v1_coverage_pp：结论不带样本量，正是本节要堵的失败模式")
	}
	// Initialised to -1, not 0: a measured 0% and "never measured" must not
	// look alike, because one is a real state worth acting on and the other is
	// an instrumentation gap.
	if !strings.Contains(src, "V1CoveragePP: -1,") {
		t.Fatal("V1CoveragePP 必须以 -1 起步：0 与「从未测量」在产物里必须可区分")
	}
}

// TestS4Gate_MinimumWindow is the control pair for rule 4 (§9.236).
//
// Rule 3 asks what **fraction** of the window was compared against v1 and says
// nothing about how big the window is. That leaves the degenerate case: a
// 1-hour window is 100% covered by construction, so if that hour happens to be
// clean the gate answers `s4_ready = true`.
//
// This is not a corner case, and it is not hypothetical — it was measured on
// production 252 before the rule was written. Over the last 7 days genuine_loss
// is 10 rows across 7 distinct hours: only 4.17% of hours contain any loss.
// Treating those hours as independent, a window of N hours misses every one of
// them with probability (1 − 0.04167)^N:
//
//	1h → 95.8%   6h → 77.5%   24h → 36.1%   72h → 4.8%   168h → 0.08%
//
// So a one-hour window returns a clean bill of health while being wrong
// nineteen times out of twenty.
//
// The rows below are grouped by which rule is supposed to catch the input,
// because that is the thing worth pinning: a short window that is ALSO missing
// its v1 data must be reported as "nothing to compare", not as "your window is
// short" — otherwise the operator is sent to fix the wrong thing.
func TestS4Gate_MinimumWindow(t *testing.T) {
	clean := func(hrs int) s4GateInput {
		return s4GateInput{
			v1Rows: 4789, genuineLoss: 0, v1WritesOn: true,
			v1CoveragePP: 100, windowHours: hrs,
		}
	}
	cases := []struct {
		name       string
		in         s4GateInput
		wantVoid   bool
		wantReason string
	}{
		// The measured production shapes, all with loss=0 and coverage 100%.
		{name: "生产 1h 窗口（实测 0 损失）", in: clean(1),
			wantVoid: true, wantReason: s4GateReasonWindowTooShort},
		{name: "生产 6h 窗口（实测 0 损失）", in: clean(6),
			wantVoid: true, wantReason: s4GateReasonWindowTooShort},
		{name: "24h 窗口 —— 刚好够", in: clean(24),
			wantVoid: false, wantReason: ""},
		{name: "72h 窗口（实测 0 损失）", in: clean(72),
			wantVoid: false, wantReason: ""},
		{name: "168h 窗口", in: clean(168),
			wantVoid: false, wantReason: ""},

		// Rule ordering: each earlier rule keeps its own reason even when the
		// window is also too short. The operator is told the first thing that
		// is actually wrong.
		{name: "1h 且 v1 完全没有行 → 归 rule 2", in: s4GateInput{
			v1Rows: 0, genuineLoss: 0, v1WritesOn: true,
			v1CoveragePP: 0, windowHours: 1},
			wantVoid: true, wantReason: s4GateReasonNoV1Traffic},
		{name: "1h 且覆盖率 20% → 归 rule 3", in: s4GateInput{
			v1Rows: 76100, genuineLoss: 0, v1WritesOn: true,
			v1CoveragePP: 20.31, windowHours: 1},
			wantVoid: true, wantReason: s4GateReasonInsufficientV1Coverage},
		{name: "1h 且 v1 停写 → 归 rule 1", in: s4GateInput{
			v1Rows: 4789, genuineLoss: 0, v1WritesOn: false,
			v1CoveragePP: 100, windowHours: 1},
			wantVoid: true, wantReason: s4GateReasonV1WritesDisabled},

		// Loss still outranks the window length: a short window that DID see
		// loss is a drift verdict, not a void one. Otherwise "the window is
		// short" would start being reported on windows that have a real answer.
		{name: "1h 但有真漏写 → 仍是不安全（drift），不是 void", in: s4GateInput{
			v1Rows: 4789, genuineLoss: 2, v1WritesOn: true,
			v1CoveragePP: 100, windowHours: 1},
			wantVoid: false, wantReason: ""},

		// Zero means "the caller did not say". Silence must not read as
		// consent, or a future caller that forgets the field gets Ready.
		{name: "未传 windowHours（0）→ 太短", in: s4GateInput{
			v1Rows: 4789, genuineLoss: 0, v1WritesOn: true, v1CoveragePP: 100},
			wantVoid: true, wantReason: s4GateReasonWindowTooShort},

		// Edge: either side of the floor.
		{name: "刚好不够（23h）", in: clean(23),
			wantVoid: true, wantReason: s4GateReasonWindowTooShort},
		{name: "刚好够（25h）", in: clean(25),
			wantVoid: false, wantReason: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s4GateVerdictOf(tc.in)
			if got.Void != tc.wantVoid {
				t.Fatalf("Void=%v want %v (reason=%q, input=%+v)", got.Void, tc.wantVoid, got.Reason, tc.in)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason=%q want %q (input=%+v)", got.Reason, tc.wantReason, tc.in)
			}
			if got.Void && got.Ready {
				t.Fatalf("void 且 ready 同时成立：%+v", got)
			}
		})
	}
}

// TestS4Gate_MinimumWindowIsPinnedToTheMeasuredArgument keeps the floor tied to
// the number that justified it.
//
// A threshold with no recorded derivation is a number somebody will eventually
// tune for convenience. The derivation is the miss-rate table in
// s4GateMinWindowHours' comment; this test does not recompute it (that would be
// a measurement, not a gate) but it does make the coupling visible: changing the
// floor without re-deriving it is a deliberate act that shows up in a diff at
// this line.
func TestS4Gate_MinimumWindowIsPinnedToTheMeasuredArgument(t *testing.T) {
	if s4MinWindowHours != 24 {
		t.Fatalf("s4MinWindowHours = %d，期望 24。改这个数之前先重算 s4GateMinWindowHours "+
			"注释里的漏检概率表（生产实测：7 天内 genuine_loss 落在 4.17%% 的小时上）—— "+
			"门槛的数字要有出处，不能只为了让门变绿或变红而调", s4MinWindowHours)
	}
	// The 7d window is the spec's exit condition and must stay reachable.
	if s4MinWindowHours > 168 {
		t.Fatalf("门槛 24h 的上限不得高于 spec 的 7 天退出条件（168h）")
	}
}

// TestS4WindowClamp pins the [1, 720] clamp on windowHours.
//
// Found by mutation P7: deleting the lower clamp left every gate green, because
// rule 4 catches the *verdict* (0 and negative are both < 24) and masks the
// consequence. The consequence is in the measurement, not the verdict — with
// windowHours = 0 the window start equals the end, the coverage series has one
// bucket, and coverage comes out 100% for any database that wrote anything at
// all. A rule downstream happens to stop the wrong number from being acted on,
// which is exactly the arrangement in which a broken measurement survives for
// years looking fine.
//
// The clamp is therefore pinned on its own terms: an offline call with a nil
// pool would fail before the clamp only if the guard runs first, so this test
// checks the clamp by reading the measurement fields a zero/negative window
// produces through the real database, where it is cheap to see.
func TestS4WindowClamp(t *testing.T) {
	// Offline: the guard order matters. `SummarizeFrom` must reject a
	// misconfigured validator before touching the window, which is why a nil
	// pool is the way to reach the early return.
	if _, err := NewDualReadValidator(nil).SummarizeFrom(context.Background(), "", time.Now(), 0); err == nil {
		t.Error("nil validator 必须返回错误而不是崩溃")
	}

	// ⚠ Calling clampWindowHours directly is NOT enough, and the first version
	// of this test learned that the hard way: mutation P7 removed the *call*
	// from SummarizeFrom and left the function, so every assertion here still
	// passed. A test that exercises a function does not pin its wiring.
	// The call site is pinned by TestS4WindowClampIsApplied in the real-database
	// file; what this test owns is the function's own behaviour.
	if got := clampWindowHours(0); got != 1 {
		t.Errorf("clampWindowHours(0) = %d, want 1", got)
	}
	if got := clampWindowHours(-5); got != 1 {
		t.Errorf("clampWindowHours(-5) = %d, want 1", got)
	}
	if got := clampWindowHours(24); got != 24 {
		t.Errorf("clampWindowHours(24) = %d, want 24（区间内不得被改）", got)
	}
	if got := clampWindowHours(9999); got != 720 {
		t.Errorf("clampWindowHours(9999) = %d, want 720", got)
	}
}
