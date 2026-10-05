package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

func ptr(f float64) *float64 { return &f }

// TestApplyUnitGate 是**消费侧**那道独立复核的判据。
//
// 它和提取器里的那道重复，是故意的。两道判据的失效方式不同：提取器改版、
// 页面改版、或有人放宽提取器口径时，提取器那道会先失效，而这一道正好是
// 把候选带进 SSOT 形状的最后一关。
//
// 变异实测：把 applyUnitGate 直接返回入参，上面这条立刻转红——
// 也就是说这道门确实在咬，而不是一个恒真的摆设。
func TestApplyUnitGate(t *testing.T) {
	t.Run("rejects a non-token unit", func(t *testing.T) {
		in := ptr(0.002)
		c := applyUnitGate(vendorprice.Candidate{
			Model: "some-image-model", Input: in,
			Unit: vendorprice.UnitPerImage,
			// 故意把置信度设成可用：提取器一旦放宽口径，这就是它会给出的东西。
			Confidence: vendorprice.ConfidenceTableRow,
		})
		if c.Confidence == vendorprice.ConfidenceTableRow {
			t.Fatal("a per-image price was allowed through to the review queue — " +
				"0.002 per image in a per-1M column is four orders of magnitude off")
		}
		if !strings.Contains(strings.Join(c.Warnings, " "), "proposal tool") {
			t.Errorf("rejection must name itself so a reviewer knows which gate fired: %v", c.Warnings)
		}
	})

	for _, unit := range []string{
		vendorprice.UnitPerSecond,
		vendorprice.UnitPerHour,
		vendorprice.UnitPer1KCalls,
		vendorprice.UnitPerChar,
		vendorprice.UnitPerMinute,
		vendorprice.UnitPerMessage,
	} {
		t.Run("rejects "+unit, func(t *testing.T) {
			c := applyUnitGate(vendorprice.Candidate{
				Unit: unit, Confidence: vendorprice.ConfidenceTableRow,
			})
			if c.Confidence == vendorprice.ConfidenceTableRow {
				t.Errorf("unit %q passed the gate", unit)
			}
		})
	}

	t.Run("passes per_1m through untouched", func(t *testing.T) {
		in := ptr(5.0)
		c := applyUnitGate(vendorprice.Candidate{
			Model: "some-text-model", Input: in,
			Unit: vendorprice.UnitPer1M, Confidence: vendorprice.ConfidenceTableRow,
		})
		if c.Confidence != vendorprice.ConfidenceTableRow {
			t.Errorf("a genuine per-1M price was rejected: %v", c.Warnings)
		}
		if c.Input != in {
			t.Error("the gate must not touch the price value")
		}
	})

	t.Run("passes an unstated unit through", func(t *testing.T) {
		// 单位未知时提取器自己就判了 unusable；这一道不该在这里替它猜。
		c := applyUnitGate(vendorprice.Candidate{
			Unit: vendorprice.UnitUnknown, Confidence: vendorprice.ConfidenceUnusable,
		})
		if len(c.Warnings) != 0 {
			t.Errorf("gate added a warning for an unstated unit: %v", c.Warnings)
		}
	})
}

// 端到端：实抓的 xAI 页面里那些按图/按秒计费的价格，一条都不许进
// ready_to_review。
func TestApplyUnitGate_EndToEndOnLiveXaiPage(t *testing.T) {
	raw, err := os.ReadFile("../../internal/vendorprice/testdata/live-xai-pricing.md")
	if err != nil {
		t.Skipf("live fixture unavailable: %v", err)
	}
	var leaked []string
	for _, c := range vendorprice.Extract("xai", "https://docs.x.ai/developers/models", raw) {
		gated := applyUnitGate(c)
		if gated.Confidence != vendorprice.ConfidenceTableRow {
			continue
		}
		if c.Unit != "" && c.Unit != vendorprice.UnitPer1M {
			leaked = append(leaked, c.Model+" unit="+c.Unit)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("non-token units reached the review queue: %v", leaked)
	}
}

// ---------------------------------------------------------------------------
// canonical 名单的**出处**门
//
// margin / 下限那两道是「解析对不对」，这一道是「判词可不可审计」。
// 三者的失效方式不同，所以要各自单独钉。
// ---------------------------------------------------------------------------

func writeList(t *testing.T, content string) string {
	t.Helper()
	p := t.TempDir() + "/canonical.txt"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write list: %v", err)
	}
	return p
}

func TestReadCanonicalNamesReadsProvenance(t *testing.T) {
	cl, err := readCanonicalNames(writeList(t, `# source: models_canonical @ llm-gateway-pg
# exported_at: 2026-10-04T14:00:00Z
# count: 3
claude-opus-4-8
claude-sonnet-4-6
gpt-4o
`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(cl.Names) != 3 {
		t.Fatalf("names = %d want 3 (%v)", len(cl.Names), cl.Names)
	}
	if !cl.Verified() {
		t.Error("a list with a source header must count as verified")
	}
	if cl.Source != "models_canonical @ llm-gateway-pg" {
		t.Errorf("source = %q", cl.Source)
	}
	if cl.ExportedAt != "2026-10-04T14:00:00Z" {
		t.Errorf("exported_at = %q", cl.ExportedAt)
	}
	if cl.DeclaredCount != 3 {
		t.Errorf("declared count = %d want 3", cl.DeclaredCount)
	}
	if cl.Provenance() == "" || cl.Provenance() == "NONE" {
		t.Errorf("provenance string must describe the list, got %q", cl.Provenance())
	}
}

// 没有出处 ⇒ 未核实 ⇒ **不许出互证判词**。
//
// 变异实测：把 Verified() 改成恒 true，这条立刻转红。
func TestListWithoutProvenanceIsNotVerified(t *testing.T) {
	cl, err := readCanonicalNames(writeList(t, "claude-opus-4-8\nclaude-sonnet-4-6\n"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(cl.Names) != 2 {
		t.Fatalf("names = %d want 2", len(cl.Names))
	}
	if cl.Verified() {
		t.Fatal("a bare list with no provenance header must not count as verified — " +
			"its completeness is unknown, and a wrong canonical yields a FALSE \"two " +
			"sources agree\" verdict against the observation source")
	}
	if cl.Provenance() != "NONE — this file carries no provenance header; see the notice at the top of this proposal" {
		t.Errorf("provenance must say NONE explicitly, got %q", cl.Provenance())
	}
}

// 头里声明的条数与实际读出的条数不一致 ⇒ 名单被截断。
//
// 截断的名单会让 margin 判据失效（对手不在场 ≠ 有证据的领先），所以这个数字
// 必须能被单独看见，而不是只体现在「解析结果碰巧不好看」。
func TestDeclaredCountMismatchIsVisible(t *testing.T) {
	cl, err := readCanonicalNames(writeList(t, `# source: models_canonical @ prod
# count: 812
claude-opus-4-8
claude-sonnet-4-6
`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cl.DeclaredCount != 812 {
		t.Fatalf("declared = %d want 812", cl.DeclaredCount)
	}
	if cl.DeclaredCount == len(cl.Names) {
		t.Error("declared 812 but only 2 names present — the truncation must be detectable by comparing the two")
	}
}

// 端到端：拿一份**真的**陈旧名单（仓里的测试注册表只有 9 个旧代 canonical 名）
// 跑一遍，断言它产不出任何互证判词。
func TestStaleListProducesNoCorroborationVerdict(t *testing.T) {
	const list = "minimax-m2\nminimax-m3\nglm-4.7\nglm-5.1\nclaude-sonnet-4\nclaude-opus-4\ngpt-5\ndeepseek-v4\nmimo-v2.5\n"
	cl, err := readCanonicalNames(writeList(t, list))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if cl.Verified() {
		t.Fatal("a hand-written list must not be treated as verified")
	}
	// 这份名单跑当前的实抓页面，展示名一个都到不了下限。
	raw, err := os.ReadFile("../../internal/vendorprice/testdata/live-anthropic-models-overview.md")
	if err != nil {
		t.Skipf("live fixture unavailable: %v", err)
	}
	resolved := 0
	for _, c := range vendorprice.Extract("anthropic", "https://example.invalid/p", raw) {
		if c.Confidence != vendorprice.ConfidenceTableRow {
			continue
		}
		if r, _ := resolveCanonical(c, cl.Names); r != nil {
			resolved++
		}
	}
	if resolved != 0 {
		t.Errorf("%d display names resolved against a 9-entry stale list — a confident wrong "+
			"canonical here is what produces a false corroboration verdict", resolved)
	}
}

// ---------------------------------------------------------------------------
// 名称解析的两道判据，各自单独钉
//
// 实测（[claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-haiku-4.5]
// 完整名单）之后必须承认一件事：**承重的是 0.90 绝对下限，不是 margin**。
//
//	"Claude Fable 5"  → 0.90 claude-fable-5  | 2nd 0.45  margin 0.45
//	"Claude Opus 4.8" → 0.90 claude-opus-4-8  | 2nd 0.45  margin 0.45
//
// margin 全在 0.30–0.45，离 0.05 的阈值很远；名单截断时错的那个名字是
// 「no match」（0.60）而不是「错挂到另一个」（0.90）。所以两条判据的分工会
// 被写错——有人以为 margin 重要而放松 0.90，那才是错价入口。
// 这两条判据因此必须各有各的判据。
// ---------------------------------------------------------------------------

// 下限：分数不够就是不够，不许因为「只有一个候选」就放行。
func TestResolveCanonicalScoreFloorRejectsWeakMatches(t *testing.T) {
	// "Claude Opus 5.5" 对上 claude-opus-4 只有 0.60：名字里版本号对不上。
	// 名单里**只有**它一个候选，margin 是无穷大——这正是「对手不在场」
	// 最容易冒充「有证据的领先」的场景。
	names := []string{"claude-opus-4"}
	if r, _ := resolveCanonical(vendorprice.Candidate{Model: "Claude Opus 5.5"}, names); r != nil {
		t.Fatalf("resolved to %v at a score the floor should have rejected", r.Warnings)
	}
	// 量具自证：确认它确实是 0.60（否则上面那条可能是别的理由通过的）。
	ranked := modelname.MatchStandardModels("Claude Opus 5.5", names)
	if len(ranked) == 0 || ranked[0].Score >= resolutionScoreFloor {
		t.Skipf("matcher behaviour changed (best=%v) — this test's premise no longer holds", ranked)
	}
}

// margin：**名单里同一个模型有两种写法**时，胜负由字母序决定，必须报歧义。
//
// 这是 margin 判据唯一的真实触发场景，实测（仓里 modelname/normalize.go 明确
// 声明**不做** "claude-opus-4-8" ↔ "claude-opus-4.8" 的跨形态归一）：
//
//	名单 [claude-opus-4-8, claude-opus-4.8]  "Claude Opus 4.8"
//	  → 0.90 "claude-opus-4-8"  /  0.90 "claude-opus-4.8"   margin 0.00
//
// 分数在 0.90 下限**之上**，所以下限救不了；没有 margin 这条，价就挂到字母序
// 在前的那一行，另一行空着，而提案里显示「已解析，0.90 分」。
func TestResolveCanonicalMarginRejectsCrossFormTies(t *testing.T) {
	// 量具自证：确认这确实是一个「下限之上、margin 塌到 0」的真实场景，
	// 而不是构造出来的假象。
	names := []string{"claude-opus-4-8", "claude-opus-4.8"}
	ranked := modelname.MatchStandardModels("Claude Opus 4.8", names)
	if len(ranked) < 2 {
		t.Skip("matcher no longer returns two candidates for this input — test premise changed")
	}
	if ranked[0].Score < resolutionScoreFloor {
		t.Skipf("tie scores %.2f, below the floor — the floor would catch it and this test proves nothing",
			ranked[0].Score)
	}
	if ranked[0].Score-ranked[1].Score >= resolutionMargin {
		t.Skipf("margin is %.2f, not a tie — this test proves nothing",
			ranked[0].Score-ranked[1].Score)
	}

	r, u := resolveCanonical(vendorprice.Candidate{Model: "Claude Opus 4.8"}, names)
	if r != nil {
		t.Fatalf("a sub-margin cross-form tie was accepted: %v", r.Warnings)
	}
	if !strings.Contains(u.Reason, "ambiguous") {
		t.Errorf("reason must name ambiguity (and the runner-up), got %q", u.Reason)
	}
	for _, want := range []string{"claude-opus-4-8", "claude-opus-4.8"} {
		if !strings.Contains(u.Reason, want) {
			t.Errorf("reason should name the runner-up %q so a human can see the collision: %q", want, u.Reason)
		}
	}
}

// 干净名单上 margin 不该误伤：这道判据很容易被「加严过头」写成新的假阳性。
func TestResolveCanonicalMarginDoesNotFireOnACleanList(t *testing.T) {
	clean := []string{"claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "gpt-4o", "gpt-4o-mini"}
	for _, d := range []string{"Claude Opus 4.8", "Claude Sonnet 4.6", "GPT-4o mini"} {
		r, u := resolveCanonical(vendorprice.Candidate{Model: d}, clean)
		if r == nil {
			t.Errorf("%q was rejected on a clean list: %q — the margin guard is over-tight", d, u.Reason)
		}
	}
}

// 近重复检测：把「margin 挡住了」的根因（名单里同一个模型两种写法）报出来。
func TestNearDuplicateWarningsFindsCrossFormCollisions(t *testing.T) {
	got := nearDuplicateWarnings([]string{
		"claude-opus-4-8", "claude-opus-4.8", "claude-sonnet-4-6", "gpt-4o", "GPT-4O", "gpt-4o-mini",
	})
	if len(got) != 2 {
		t.Fatalf("groups = %d want 2: %v", len(got), got)
	}
	joined := strings.Join(got, " | ")
	for _, want := range []string{"claude-opus-4-8", "claude-opus-4.8", "GPT-4O", "gpt-4o"} {
		if !strings.Contains(joined, want) {
			t.Errorf("group report must name %q: %q", want, joined)
		}
	}
	// 干净名单不得报任何东西。
	if got := nearDuplicateWarnings([]string{"claude-opus-4-8", "claude-sonnet-4-6", "gpt-4o"}); len(got) != 0 {
		t.Errorf("a clean list produced warnings: %v", got)
	}
	// claude-opus-4-7 与 claude-opus-4-8 **不是**重复：折叠后仍是两个不同的键。
	if got := nearDuplicateWarnings([]string{"claude-opus-4-7", "claude-opus-4-8"}); len(got) != 0 {
		t.Errorf("different versions were reported as duplicates: %v", got)
	}
}
