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
	// 路径是 ../../../ 而不是 ../../：本包在 cmd/tools/propose-baseline-prices
	// （深度 3），../.. 只到 cmd/。错一层的直接后果不是「路径解析失败」，
	// 而是 t.Skip —— 于是「实抓页面端到端判据」**从来没跑过**，而 SKIP 在
	// 报告里长得和通过一模一样。⇒ 修正层级，并改成 Fatalf：
	// 夹具是仓里跟踪的文件，它不见了是**本仓的错**，不是「环境不具备条件」。
	raw, err := os.ReadFile("../../../internal/vendorprice/testdata/live-xai-pricing.md")
	if err != nil {
		t.Fatalf("live fixture unavailable: %v", err)
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
	// 同上：错一层会退化成 SKIP，而这条判据正是「手写名单一个都到不了下限」
	// 的唯一端到端证据。
	raw, err := os.ReadFile("../../../internal/vendorprice/testdata/live-anthropic-models-overview.md")
	if err != nil {
		t.Fatalf("live fixture unavailable: %v", err)
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

// ---------------------------------------------------------------------------
// 尾注解形态：展示名尾部成对的 (...) 是注解，不是名字的一部分
//
// ★ 这条是**实测出来的缺口**，不是设想出来的（2026-10-05，真实 960 名单
// × docs/02-resources/research/pricing/raw 的 10 份原厂快照）：
//
//	提案 ready_to_review 10 条 → 加了这一条之后 14 条（+40%），
//	unresolved 8 → 4，剩下的 4 条**确实不在目录里**。
//
// 原先 8 条 unresolved 里有 4 条是**假否定**：名字在目录里，分数也够
// （剥掉注解后 0.90），只是尾部注解把分数压到 0.84，于是被下限拒掉。
// 而它们的 canonical 永远拿不到基准价 —— claude-opus-4 / claude-opus-4.1 /
// claude-sonnet-4 / claude-haiku-3-5 四行都真实存在于 models_canonical。
//
// ★ 其中一条原本正指着**另一个模型**：Claude Haiku 3.5 的原样形态最佳候选
// 是 claude-3-haiku（0.84，Anthropic 另一个模型）。它没造成错挂，**只因为
// 0.90 下限挡住了**。也就是说「放松下限去救这 4 条」会直接错挂一个价 ——
// 这就是修法落在形态上、而不是落在阈值上的原因。
//
// 修法也不落在提取器上：`splitIdent` 只认**前导** markdown 链接，尾部链接
// （`Claude Haiku 3.5 ([retired…](url))`）整个掉进标识里。让提取器去猜哪个
// 括号是注解，等于把厂商措辞硬编码进仓库；而决定「括号算不算名字」的是
// **名单**，不是词表 —— 见 buildResolutionForms 的注释与实测对照。
// ---------------------------------------------------------------------------

// qualifierFixture 是量具自证用的**真实**名单子集：只留下这四条要用到的
// canonical，外加干扰项（claude-3-haiku 正是原先误指的那个模型）。
//
// ★ 每一项都**必须**在真库 960 名单里存在，而"非成员"那几个必须**不在**
//
//	—— 第一次写这份夹具时手滑把 claude-mythos-5 放了进去（真库里没有），
//	而下面那条阴性对照的量具自证立刻把它揪了出来。这正是那条自证存在的
//	理由：夹具写错时，判据会**变成恒真**而不是变红。
var qualifierFixtureNonMembers = []string{
	"claude-mythos-5", "grok-4.20-0309-reasoning", "grok-4.20-0309-non-reasoning",
	"grok-4.20-multi-agent-0309",
}
var qualifierFixture = []string{
	"claude-opus-4", "claude-opus-4.1", "claude-opus-4-5", "claude-opus-4-8",
	"claude-sonnet-4", "claude-sonnet-4-5", "claude-haiku-3-5", "claude-haiku-4-5",
	"claude-3-haiku", "claude-3-5-haiku", "claude-fable-5",
	"lyria-3-clip-preview", "lyria-3-pro-preview",
	"grok-4.3", "grok-4.4", "grok-4.20", "grok-4.20-multi-agent",
}

func TestResolveCanonicalDropsTrailingQualifierAnnotation(t *testing.T) {
	for _, tc := range []struct{ display, want string }{
		{"Claude Opus 4.1 (deprecated)", "claude-opus-4.1"},
		{"Claude Opus 4 (deprecated)", "claude-opus-4"},
		{"Claude Sonnet 4 (deprecated)", "claude-sonnet-4"},
		{"Claude Haiku 3.5 (retired, except on Bedrock and Vertex AI)", "claude-haiku-3-5"},
		// 括号看着像产品名的一部分（30 秒片长），但名单里只有剥掉之后那个 ——
		// 这条证明判据不是按「像不像注解」分的，而是按名单判的。
		{"Lyria 3 Clip Preview (30s)", "lyria-3-clip-preview"},
	} {
		// 量具自证①：目标 canonical 真的在名单里，否则下面可能因为别的理由通过。
		if !contains(qualifierFixture, tc.want) {
			t.Fatalf("fixture lacks %q — this case would pass for the wrong reason", tc.want)
		}
		// 量具自证②：原样形态确实过不了下限，而剥掉之后确实过 —— 否则这条
		// 判据证明的不是「剥掉注解救回了名字」。
		qualifier, ident, ok := trailingQualifier(tc.display)
		if !ok {
			t.Fatalf("%q: trailingQualifier did not find an annotation to drop", tc.display)
		}
		rawBest := modelname.MatchStandardModels(tc.display, qualifierFixture)
		if len(rawBest) == 0 || rawBest[0].Score >= resolutionScoreFloor {
			t.Skipf("matcher behaviour changed: %q now scores %v on the raw display name — "+
				"this test's premise (the annotation pushes it under the floor) no longer holds",
				tc.display, rawBest)
		}
		strippedBest := modelname.MatchStandardModels(ident, qualifierFixture)
		if len(strippedBest) == 0 || strippedBest[0].Score < resolutionScoreFloor {
			t.Fatalf("%q: dropping %q does not clear the floor (%v) — the fix would not help",
				tc.display, qualifier, strippedBest)
		}

		r, u := resolveCanonical(vendorprice.Candidate{Model: tc.display}, qualifierFixture)
		if r == nil {
			t.Fatalf("%q was not resolved: %s", tc.display, u.Reason)
		}
		if got := resolvedCanonical(*r); got != tc.want {
			t.Errorf("%q resolved to %q, want %q", tc.display, got, tc.want)
		}
		// 人必须在提案里看得见「注解被丢了」——否则原展示名与 canonical 对不上，
		// 而看提案的人只能逐条回页面才知道。
		joined := strings.Join(r.Warnings, " | ")
		if !strings.Contains(joined, "trailing") || !strings.Contains(joined, qualifier) {
			t.Errorf("%q: the warning must name the dropped annotation %q, got %q",
				tc.display, qualifier, joined)
		}
	}
}

// 阴性对照：剥形态**不许**造出匹配。四个确实不在目录里的名字，两个形态都
// 必须落在下限之下。
//
// 这条是上面那条的牙齿：如果实现改成「剥不掉就编一个」，上面照样绿，
// 只有这条会红。
func TestResolveCanonicalKeepsNonMembersUnresolved(t *testing.T) {
	for _, display := range []string{
		"Claude Mythos 5 (limited availability)",
		"grok-4.20-multi-agent-0309",
		"grok-4.20-0309-reasoning",
		"grok-4.20-0309-non-reasoning",
	} {
		// 量具自证：这些名字**整体**都不在夹具里（夹具刻意没放
		// claude-mythos-5 / *-0309），否则这条就是在测别的东西。
		// 写成查夹具而不是查真库，是为了让失败直接指向"夹具写错了"这一个
		// 可改的地方。
		if contains(qualifierFixture, display) {
			t.Fatalf("fixture contains the non-member %q — this case no longer tests a "+
				"non-member", display)
		}
		if _, bare, ok := trailingQualifier(display); ok {
			slug := strings.ToLower(strings.ReplaceAll(bare, " ", "-"))
			if contains(qualifierFixture, slug) {
				t.Fatalf("%q: its bare form slugs to %q, which IS in the fixture — the "+
					"catalog does contain this model, so it must resolve rather than stay "+
					"unresolved", display, slug)
			}
			if !contains(qualifierFixtureNonMembers, slug) {
				t.Errorf("%q slugs to %q, which is in neither list — add it to exactly one so "+
					"this case keeps testing what it claims to", display, slug)
			}
		}
		r, u := resolveCanonical(vendorprice.Candidate{Model: display}, qualifierFixture)
		if r != nil {
			t.Errorf("%q resolved to %q on a name that is not in the catalog — "+
				"dropping the annotation must never manufacture a match",
				display, resolvedCanonical(*r))
		}
		if u.Reason == "" {
			t.Errorf("%q was dropped without a reason", display)
		}
	}
}

// 两个形态都过线却指向**不同** canonical 时必须拒收。
//
// ★ 如实标注：这条在真实总体里**没有观测到触发**（2026-10-05 实测 2,002 条
// 带尾注解的展示名，冲突 0 次）。它是**保险**，不是对已发生问题的修复：
// 单看任一个形态都是「自信」的，只有并排比才发现它们说的是两个模型，
// 而那正是价挂到错模型上的那一种。失败方向是拒收。
func TestResolveCanonicalRefusesWhenTheTwoFormsDisagree(t *testing.T) {
	names := []string{"widget-classic", "widget-classic-30s"}
	const display = "Widget Classic (30s)"

	// 量具自证：两个形态必须**各自**都过下限，且指向不同名字。缺任何一条，
	// 下面就可能是被下限或 margin 挡掉的，而不是被冲突判据挡掉的。
	fullBest := modelname.MatchStandardModels(display, names)
	if len(fullBest) == 0 || fullBest[0].Score < resolutionScoreFloor {
		t.Skipf("raw form %q does not clear the floor (%v) — test premise changed", display, fullBest)
	}
	_, ident, ok := trailingQualifier(display)
	if !ok {
		t.Fatalf("%q has no droppable annotation", display)
	}
	strippedBest := modelname.MatchStandardModels(ident, names)
	if len(strippedBest) == 0 || strippedBest[0].Score < resolutionScoreFloor {
		t.Skipf("stripped form %q does not clear the floor (%v) — test premise changed", ident, strippedBest)
	}
	if fullBest[0].Name == strippedBest[0].Name {
		t.Skipf("both forms resolve to %q — not a disagreement", fullBest[0].Name)
	}

	r, u := resolveCanonical(vendorprice.Candidate{Model: display}, names)
	if r != nil {
		t.Fatalf("a two-form disagreement was accepted as %q", resolvedCanonical(*r))
	}
	if !strings.Contains(u.Reason, "ambiguous") {
		t.Errorf("reason must name the ambiguity, got %q", u.Reason)
	}
	for _, want := range []string{fullBest[0].Name, strippedBest[0].Name} {
		if !strings.Contains(u.Reason, want) {
			t.Errorf("reason must name %q so a human can adjudicate: %q", want, u.Reason)
		}
	}
}

// resolvedCanonical 是 buildDraft 取 SSOT 键的唯一通道，它靠 warning 里的
// `resolved to canonical "` 子串回读。这条钉住「加了尾注解说明之后，回读
// 仍然拿得到键」——warning 是跨文件契约，改它的措辞会让所有已解析条目
// 静默丢掉 SSOT 键（models 为空，而 why 读起来像「原厂页没有可用价」）。
func TestResolvedCanonicalRoundTripsTheQualifierForm(t *testing.T) {
	display := "Claude Opus 4 (deprecated)"
	r, u := resolveCanonical(vendorprice.Candidate{Model: display}, qualifierFixture)
	if r == nil {
		t.Fatalf("not resolved: %s", u.Reason)
	}
	if got := resolvedCanonical(*r); got != "claude-opus-4" {
		t.Fatalf("round trip lost the SSOT key: got %q from warnings %v", got, r.Warnings)
	}
	// buildDraft 是按这个键写进 models 的；键为空时那一条会静默消失。
	d := buildDraft(&proposal{
		Corroborated: []corroborated{{
			Canonical: resolvedCanonical(*r), DisplayName: display, Verdict: "corroborated",
			Currency: "USD", VendorPage: &baselineSide{Input: ptr(15), Output: ptr(75)},
		}},
	}, "2026-10-05T00:00:00Z")
	if _, ok := d.Models["claude-opus-4"]; !ok {
		t.Errorf("SSOT draft lost the entry: models=%v refused=%v", d.Models, d.Refused)
	}
}

// trailingQualifier 的配对守卫：注解里带括号、括号不成对、整串就是括号、
// 括号里是空的 —— 这四种都不能被劈坏。
func TestTrailingQualifierRefusesToChopNames(t *testing.T) {
	for _, tc := range []struct{ display, wantIdent, wantQualifier string }{
		{"Claude Haiku 3.5 (retired)", "Claude Haiku 3.5", "retired"},
		// 注解内部自带成对括号：两种配对在这里**恰好**同解，所以这一条
		// 分不开它们（第一版只有它，结果朴素配对的变异是绿的）。
		{"Model X (available (beta) today)", "Model X", "available (beta) today"},
		// ★ 这条才是分得开两种配对的样本：名字自己带括号，尾部还有第二个
		// 括号组。朴素「取第一个 (」会剥掉 "(preview)" 那一对，把标识留成
		// "Model "、注解留成 "preview) Opus 4 (deprecated" —— 名字被劈坏。
		// 深度扫描剥的是**最后一对**。真实页面 0 例，但这是本判据存在的理由。
		{"Model (preview) Opus 4 (deprecated)", "Model (preview) Opus 4", "deprecated"},
		{"NoParens Here", "NoParens Here", ""},
		{"(preview)", "(preview)", ""},         // 整串就是一个括号
		{"Trailing (  )", "Trailing (  )", ""}, // 括号里只有空白
		{"Unbalanced ( text", "Unbalanced ( text", ""},
		{"Ends With ) Only", "Ends With ) Only", ""},
	} {
		q, ident, ok := trailingQualifier(tc.display)
		if ident != tc.wantIdent {
			t.Errorf("%q: ident = %q, want %q", tc.display, ident, tc.wantIdent)
		}
		if q != tc.wantQualifier {
			t.Errorf("%q: qualifier = %q, want %q", tc.display, q, tc.wantQualifier)
		}
		if (q != "") != ok {
			t.Errorf("%q: ok = %v but qualifier = %q", tc.display, ok, q)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
