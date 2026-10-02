package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
			in:         s4GateInput{v1Rows: 0, genuineLoss: 0, v1WritesOn: false},
			wantVoid:   true,
			wantReason: s4GateReasonV1WritesDisabled,
		},
		{
			name:       "v1 停写但窗口里还有 V1 行（冻结期的尾部窗口）",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: false},
			wantVoid:   true,
			wantReason: s4GateReasonV1WritesDisabled,
		},
		{
			name:       "v1 写入中但窗口零 V1 流量（空扫描 = 什么都没证明）",
			in:         s4GateInput{v1Rows: 0, genuineLoss: 0, v1WritesOn: true},
			wantVoid:   true,
			wantReason: s4GateReasonNoV1Traffic,
		},
		{
			name:       "v1 写入中、零真漏写、有流量 —— 唯一允许报绿的一行",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 0, v1WritesOn: true},
			wantVoid:   false,
			wantReason: "",
		},
		{
			name:       "真漏写 > 0：不是 void，是真的不安全",
			in:         s4GateInput{v1Rows: 245460, genuineLoss: 1, v1WritesOn: true},
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
	bothOn := s4GateVerdictOf(s4GateInput{v1Rows: 1, genuineLoss: 0, v1WritesOn: true})
	if !bothOn.Ready || bothOn.Void {
		t.Fatalf("两条证据都在时必须 ready 且非 void，实得 %+v", bothOn)
	}
	for _, in := range []s4GateInput{
		{v1Rows: 1, genuineLoss: 0, v1WritesOn: false}, // 缺「写入中」
		{v1Rows: 0, genuineLoss: 0, v1WritesOn: true},  // 缺「扫到东西」
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
				v := s4GateVerdictOf(s4GateInput{v1Rows: rows, genuineLoss: loss, v1WritesOn: on})
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
	if !strings.Contains(clean, "v1Rows:      sum.V1Rows") {
		t.Fatal("s4GateVerdictOf 未接收 sum.V1Rows：空扫描又会被判成通过")
	}
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
