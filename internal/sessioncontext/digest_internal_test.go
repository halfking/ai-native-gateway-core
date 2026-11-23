package sessioncontext

// 分段抽取式摘要 + 分层合并的单元测试（2026-11-23 大会话归约提速轮·第三步）。
//
// 钉住的契约：
//  1. 分词与打分是确定的；平分时下标小者优先；
//  2. 选择尊重预算，并与已选行高度重合的行会被跳过；
//  3. 人类消息（尤其是开头句）排在工具输出前面；
//  4. 行首标签：带工具引用的消息一律 tool_result（Anthropic 形态下它们挂在 user 上）；
//  5. 分段：按原始字节累计、不切开消息、不 eligible 的不占字节；
//  6. 段的缓存键随「内容 / 是否入选 / 大消息的角色与长度」变化；
//  7. 分层合并：结果装进预算，且首/中/尾都有名额；
//  8. 缓存有界、先进先出。

import (
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func linesUnits(lines []string, label string, weight float64) []digestUnit {
	units := make([]digestUnit, 0, len(lines))
	for _, l := range lines {
		u := newDigestUnit(l, label, weight)
		if u != nil {
			u.msg = len(units) // 每行各算一条消息 ⇒ 不受「同一条消息至多两行」约束
			units = append(units, *u)
		}
	}
	return units
}

func TestDigestTermsASCIIHanAndStopwords(t *testing.T) {
	got := digestTerms("The ReadAt 回卷 hits config/db.yaml on port 6432, 回卷 again")
	want := []string{"readat", "回卷", "hits", "config", "db", "yaml", "port", "6432", "again"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("terms = %v, want %v", got, want)
	}
	for _, w := range got {
		if digestStopwords[w] {
			t.Fatalf("停用词不该进词项: %q", w)
		}
	}
}

func TestSelectUnitsDeterministicOrderedAndWithinBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vocab := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india",
		"juliet", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu", "amber", "cobalt", "ivory", "jade"}
	lines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		// 每行从词表里取 5 个互不相同的词 ⇒ 行与行之间几乎没有重合
		var w []string
		for k := 0; k < 5; k++ {
			w = append(w, vocab[rng.Intn(len(vocab))])
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s entry %d", w[0], w[1], w[2], w[3], w[4], i))
	}
	units := linesUnits(lines, "assistant", 1.2)
	first := selectUnits(units, 1000)
	for rep := 0; rep < 5; rep++ {
		if again := selectUnits(units, 1000); fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatalf("同样的输入必须给出同样的选择: %v vs %v", again, first)
		}
	}
	for k := 1; k < len(first); k++ {
		if first[k] <= first[k-1] {
			t.Fatalf("返回的下标必须升序: %v", first)
		}
	}
	total := 0
	for _, i := range first {
		total += len(unitLine(units[i])) + 1
	}
	if total > 1000 {
		t.Fatalf("超出预算: %d", total)
	}
	if total < 700 {
		t.Fatalf("预算没装满就停了？只用了 %d / 1000", total)
	}
}

func TestSelectUnitsSkipsRedundantLines(t *testing.T) {
	units := linesUnits([]string{
		"deploy the billing service to cluster east-1 with retries 4",
		"deploy the billing service to cluster east-1 with retries 4", // 完全重复
		"deploy the billing service to cluster east-1 with retries 4",
		"deploy the billing service to cluster east-1 with retries 9", // 只差一个数字：另一条事实
		"the invoice ledger migration adds column legacy_flag_7",
	}, "assistant", 1.2)
	sel := selectUnits(units, 4096)
	if len(sel) != 3 {
		t.Fatalf("完全重复的行只应保留一条，只差数字的要各留一条: selected %v (%q)", sel, renderSelection(units, sel))
	}
	if sel[0] != 0 {
		t.Fatalf("应保留最早的那条: %v", sel)
	}
}

func TestSelectUnitsPrefersHumanOpenersOverToolOutput(t *testing.T) {
	var units []digestUnit
	tool := strings.Repeat("ok done fine result output value total count", 3)
	for i := 0; i < 40; i++ {
		units = append(units, linesUnits([]string{fmt.Sprintf("row %d status done module part item", i)}, "tool_result", digestLabelWeight("tool_result"))...)
	}
	_ = tool
	ask := linesUnits([]string{"Please also verify the invoice ledger totals before the release cut."}, "user", digestLabelWeight("user"))
	ask[0].first = true
	units = append(units, ask...)
	sel := selectUnits(units, 200) // 只放得下两三行
	if len(sel) == 0 {
		t.Fatal("什么都没选")
	}
	if units[sel[len(sel)-1]].label != "user" {
		t.Fatalf("人类消息的开头句必须入选: %q", renderSelection(units, sel))
	}
}

func TestSelectUnitsCapsLinesPerMessageAndRanksToolLinesLast(t *testing.T) {
	// 同一条消息的 5 行都很出色：至多留 digestMaxLinesPerMessage 行。
	var units []digestUnit
	for i := 0; i < 5; i++ {
		u := newDigestUnit(fmt.Sprintf("user: request number %d about the %s module please verify it carefully", i, []string{"alpha", "bravo", "charlie", "delta", "echo"}[i]), "user", 2.5)
		u.msg = 7
		units = append(units, *u)
	}
	for i := 0; i < 5; i++ {
		u := newDigestUnit(fmt.Sprintf("assistant: conclusion number %d about the %s module looks fine now", i, []string{"foxtrot", "golf", "hotel", "india", "juliet"}[i]), "assistant", 1.5)
		u.msg = 8
		units = append(units, *u)
	}
	sel := selectUnits(units, 4096)
	perMsg := map[int]int{}
	for _, i := range sel {
		perMsg[units[i].msg]++
	}
	if perMsg[7] != digestMaxLinesPerMessage || perMsg[8] != digestMaxLinesPerMessage {
		t.Fatalf("同一条消息至多留 %d 行: %v", digestMaxLinesPerMessage, perMsg)
	}

	// 人类/助手的行先用预算，工具结果的行只拿剩余额度。
	var toolLines, rich []digestUnit
	for i := 0; i < 20; i++ {
		u := newDigestUnit(fmt.Sprintf("tool output row %d status ok value %d path/dir_%d/file.go", i, i*13, i%3), "tool_result", digestLabelWeight("tool_result"))
		u.msg = i
		toolLines = append(toolLines, *u)
	}
	rich = append(rich, toolLines...)
	for i := 0; i < 30; i++ {
		u := newDigestUnit(fmt.Sprintf("conclusion number %d about module %s looks fine now", i, []string{"foxtrot", "golf", "hotel", "india", "juliet"}[i%5]), "assistant", 1.5)
		u.msg = 100 + i
		rich = append(rich, *u)
	}
	const budget = 1000
	sel = selectUnits(rich, budget)
	tool := 0
	for _, i := range sel {
		if rich[i].label == "tool_result" {
			tool += len(unitLine(rich[i])) + 1
		}
	}
	if tool > budget/5 {
		t.Fatalf("助手行足够时工具结果占了 %d 字节——它们只该拿剩余额度", tool)
	}
	// 只有几行人类/助手时：预算用不完的部分留给工具行（否则摘要平白丢信息）。
	poor := append([]digestUnit{}, toolLines...)
	one := newDigestUnit("the only conclusion written by the assistant about module zulu", "assistant", 1.5)
	one.msg = 200
	poor = append(poor, *one)
	if sel := selectUnits(poor, budget); unitsCost(pick(poor, sel)) < budget/2 {
		t.Fatalf("非工具行没填满时应让工具行用剩余额度: 只用了 %d / %d", unitsCost(pick(poor, sel)), budget)
	}
	if sel := selectUnits(toolLines, budget); len(sel) == 0 {
		t.Fatal("全是工具行时也应选出内容")
	}

	// 类别排序要独立于分数存在：一条全是稀有标识符的工具输出行，分数会高过一条
	// 措辞平平的助手行——仅靠权重拦不住它抢到前面。这里让助手行的词项在组里
	// 人人都说（稀有词占比≈0），工具行的词项独一无二。
	var mixed []digestUnit
	for i := 0; i < 40; i++ {
		u := newDigestUnit("fine now ok done", "assistant", 1.5)
		u.msg = 300 + i
		mixed = append(mixed, *u)
	}
	noisy := newDigestUnit("tool output row 7 value 91 path/dir_2/file.go checksum 0x9f3a zeta id 5531", "tool_result", digestLabelWeight("tool_result"))
	noisy.msg = 400
	mixed = append(mixed, *noisy)
	order := unitOrder(mixed)
	// 前提：单看分数（含角色权重），工具行确实更高——否则这条测试钉不住类别排序。
	if scoreOf(mixed, len(mixed)-1) <= scoreOf(mixed, 0) {
		t.Fatalf("测试前提不成立：工具行的分数应当高于助手行（%.3f vs %.3f）", scoreOf(mixed, len(mixed)-1), scoreOf(mixed, 0))
	}
	if mixed[order[0]].label != "assistant" {
		t.Fatalf("人类/助手的行必须排在工具结果前面，哪怕后者分数更高: 先试的是 %q", unitLine(mixed[order[0]]))
	}
	// 预算只放得下两行：助手行（去重后只剩一条）必须在，工具行只能在剩余额度里。
	sel = selectUnits(mixed, len(unitLine(mixed[0]))+1+len(unitLine(*noisy))-1)
	if len(sel) != 1 || mixed[sel[0]].label != "assistant" {
		t.Fatalf("预算不够时先留的应是助手行: %q", renderSelection(mixed, sel))
	}
}

// scoreOf 镜像 unitOrder 的打分（含角色权重、首末行、长度因子），只用来确认测试前提。
func scoreOf(units []digestUnit, i int) float64 {
	df := map[string]int{}
	for _, u := range units {
		for _, t := range u.terms {
			df[t]++
		}
	}
	u := units[i]
	if len(u.terms) == 0 {
		return 0
	}
	sum := 0.0
	for _, t := range u.terms {
		sum += math.Log(1 + float64(len(units))/float64(df[t]))
	}
	s := sum / float64(len(u.terms))
	s = (s + 0.4*unitSignal(u.text) + 0.3*math.Min(1, float64(len(u.text))/80)) * u.weight
	if u.first && !digestIsTool(u.label) {
		s *= 1.5
	}
	if u.last {
		s *= 1.15
	}
	return s
}

func pick(units []digestUnit, sel []int) []digestUnit {
	out := make([]digestUnit, 0, len(sel))
	for _, i := range sel {
		out = append(out, units[i])
	}
	return out
}

func TestDigestLabelSeparatesToolResultsFromHumans(t *testing.T) {
	cases := []struct {
		info messageInfo
		want string
	}{
		{messageInfo{role: "user"}, "user"},
		{messageInfo{role: "user", refs: []string{"id:toolu_1"}}, "tool_result"}, // Anthropic 形态
		{messageInfo{role: "tool", refs: []string{"id:call_1"}}, "tool_result"},  // OpenAI 形态
		{messageInfo{role: "assistant"}, "assistant"},
		{messageInfo{role: "assistant", calls: []string{"id:toolu_1"}}, "assistant"},
		{messageInfo{}, "assistant"},
		{messageInfo{role: "function"}, "function"},
	}
	for _, c := range cases {
		if got := digestLabel(c.info); got != c.want {
			t.Fatalf("%+v: label = %q, want %q", c.info, got, c.want)
		}
	}
	if !(digestLabelWeight("user") > digestLabelWeight("assistant") && digestLabelWeight("assistant") > digestLabelWeight("tool_result")) {
		t.Fatal("权重次序必须是 user > assistant > tool_result")
	}
}

func TestSegmentHistoryByBytesWithoutSplittingMessages(t *testing.T) {
	mk := func(lengths []int64, eligible []bool) digestInput {
		in := digestInput{eligible: eligible}
		for _, n := range lengths {
			in.spans = append(in.spans, largebody.Span{Length: n})
		}
		return in
	}
	segs := segmentHistory(mk([]int64{100, 100, 100, 100, 100}, []bool{true, true, true, true, true}), 250)
	// 累计到 ≥250 才收段：[0,2] [3,4]
	if fmt.Sprint(segs) != fmt.Sprint([]digestSegment{{0, 2}, {3, 4}}) {
		t.Fatalf("segments = %v", segs)
	}
	// 不 eligible 的消息不占字节也不打断段。
	segs = segmentHistory(mk([]int64{100, 900, 100, 100}, []bool{true, false, true, true}), 250)
	if fmt.Sprint(segs) != fmt.Sprint([]digestSegment{{0, 3}}) {
		t.Fatalf(" ineligible 不该占字节: %v", segs)
	}
	// 一条超过目标大小的消息自成一段。
	segs = segmentHistory(mk([]int64{10, 1000, 10, 10}, []bool{true, true, true, true}), 250)
	if fmt.Sprint(segs) != fmt.Sprint([]digestSegment{{0, 1}, {2, 3}}) {
		t.Fatalf("大消息应与前面的累计成段、后面的另起一段: %v", segs)
	}
	if segmentHistory(mk([]int64{10}, []bool{false}), 250) != nil {
		t.Fatal("没有可摘的消息 ⇒ 没有段")
	}
}

func TestSegmentKeyChangesWithContentEligibilityAndBigMessage(t *testing.T) {
	cfg := DigestConfig{Enabled: true}
	mk := func() digestInput {
		return digestInput{
			spans:    []largebody.Span{largebody.Span{Length: 10}, largebody.Span{Length: 20}, largebody.Span{Length: 30}},
			infos:    []messageInfo{{role: "user"}, {role: "assistant"}, {role: "assistant"}},
			eligible: []bool{true, true, true},
			msgHash:  [][32]byte{{1}, {2}, {3}},
		}
	}
	seg := digestSegment{0, 2}
	base := mk().segmentKey(seg, cfg)
	if mk().segmentKey(seg, cfg) != base {
		t.Fatal("键必须可复现")
	}
	changed := mk()
	changed.msgHash[1] = [32]byte{9}
	if changed.segmentKey(seg, cfg) == base {
		t.Fatal("消息内容变了，键必须变")
	}
	changed = mk()
	changed.eligible[1] = false // 这条消息被判为不安全 ⇒ 不再参与
	if changed.segmentKey(seg, cfg) == base {
		t.Fatal("消息是否入选变了，键必须变")
	}
	changed = mk()
	changed.spans[1] = largebody.Span{Length: materializeItem + 1} // 变成大消息：身份 = 角色 + 长度
	if changed.segmentKey(seg, cfg) == base {
		t.Fatal("大消息的键必须与它还是小消息时不同")
	}
	bigA, bigB := mk(), mk()
	bigA.spans[1], bigB.spans[1] = largebody.Span{Length: materializeItem + 1}, largebody.Span{Length: materializeItem + 1}
	bigA.msgHash[1] = [32]byte{7} // 大消息不读内容 ⇒ 内容哈希不参与
	if bigA.segmentKey(seg, cfg) != bigB.segmentKey(seg, cfg) {
		t.Fatal("大消息的身份不该依赖内容哈希")
	}
	bigB.infos[1].role = "user"
	if bigA.segmentKey(seg, cfg) == bigB.segmentKey(seg, cfg) {
		t.Fatal("大消息的角色变了，键必须变")
	}
	other := DigestConfig{Enabled: true, SegmentBytes: cfg.SegmentBytes + 1024}
	if mk().segmentKey(seg, other) == base {
		t.Fatal("分段参数变了，键必须变")
	}
}

func TestMergeDigestLayersFitsBudgetAndKeepsEveryRegion(t *testing.T) {
	// 64 个段，每段 20 行、行里带独一无二的段号。内容刻意同构（只差编号）——
	// 这正是「按分数填预算」会让强的一段包揽名额的形态。
	newSummaries := func() []string {
		summaries := make([]string, 0, 64)
		for s := 0; s < 64; s++ {
			var b strings.Builder
			for l := 0; l < 20; l++ {
				fmt.Fprintf(&b, "assistant: segment-%02d line-%02d fact number %d about module mod_%d\n", s, l, s*100+l, s%7)
			}
			summaries = append(summaries, b.String())
		}
		return summaries
	}
	seen := func(got string) map[int]int {
		counts := map[int]int{}
		for _, m := range regexp.MustCompile(`segment-(\d+)`).FindAllStringSubmatch(got, -1) {
			n, _ := strconv.Atoi(m[1])
			counts[n]++
		}
		return counts
	}

	const budget = 8192 // 每段摊到 128 字节 ⇒ 每段至少放得下一两行
	got := mergeDigestLayers(newSummaries(), budget)
	if got == "" {
		t.Fatal("合并结果不该为空")
	}
	if len(got) > budget {
		t.Fatalf("合并结果 %d 字节，超过预算 %d", len(got), budget)
	}
	if counts := seen(got); len(counts) != 64 {
		t.Fatalf("64 段里只有 %d 段有名额——每一段的份额必须由它在树里的位置决定:\n%s", len(counts), got)
	}
	// 行序必须是原文顺序（段号不减）。
	last := -1
	for _, m := range regexp.MustCompile(`segment-(\d+)`).FindAllStringSubmatch(got, -1) {
		n, _ := strconv.Atoi(m[1])
		if n < last {
			t.Fatalf("行序错乱: segment %d 出现在 %d 之后", n, last)
		}
		last = n
	}
	// 预算收紧（摊到每段放不下一行）：这时不可能覆盖所有段——物理上预算只够 31 行。
	// 不变量是：结果非空、不超预算、额度不被浪费（至少用掉九成）、至少一半的段有名额、结果确定。
	tight := mergeDigestLayers(newSummaries(), 2048)
	if tight == "" || len(tight) > 2048 {
		t.Fatalf("收紧预算后 %d 字节", len(tight))
	}
	if len(tight) < 2048*9/10 {
		t.Fatalf("预算没用完就停了：只用了 %d / 2048", len(tight))
	}
	// 有名额的段数不应少于「预算 / 最长一行」太多（每行至少占这么多字节）。
	maxCost := 0
	for _, sum := range newSummaries() {
		for _, line := range strings.Split(sum, "\n") {
			maxCost = max(maxCost, len(line)+1)
		}
	}
	if floor := 2048/maxCost - 2; len(seen(tight)) < floor {
		t.Fatalf("收紧预算下只有 %d 段有名额，少于物理上限 %d", len(seen(tight)), floor)
	}
	if again := mergeDigestLayers(newSummaries(), 2048); again != tight {
		t.Fatal("收紧预算下的结果必须确定")
	}
	// 同样的输入必须给出同样的输出。
	if again := mergeDigestLayers(newSummaries(), budget); again != got {
		t.Fatal("合并结果必须确定")
	}
	if mergeDigestLayers(nil, budget) != "" {
		t.Fatal("没有段 ⇒ 空串")
	}
	if got := mergeDigestLayers([]string{"user: hello there and welcome back", ""}, budget); got != "user: hello there and welcome back" {
		t.Fatalf("空节点应被忽略: %q", got)
	}
	// 人类消息（更长的行）在各层都保得住：预算负担得起每段的保底时，每一段
	// 至少留下它最好的一行——不能让「短」而不是「重要」决定谁留下。
	withHuman := newSummaries()
	withHuman[40] = "user: Before the release cut please verify the invoice ledger totals with the finance team in Berlin.\n" + withHuman[40]
	gotHuman := mergeDigestLayers(withHuman, budget)
	if !strings.Contains(gotHuman, "user: Before the release cut") {
		t.Fatalf("预算负担得起保底时，人类消息必须保留:\n%s", gotHuman)
	}
	for s := 0; s < 64; s++ {
		if s == 40 {
			continue // 它的名额用在了那行人类消息上（上面已断言）
		}
		if !strings.Contains(gotHuman, fmt.Sprintf("segment-%02d ", s)) {
			t.Fatalf("加了人类消息后第 %d 段反而没了名额", s)
		}
	}
}

func TestSplitBudgetProportionalWithFloor(t *testing.T) {
	leaf := func(costs ...int) *digestNode { // costs：各行成本
		units := make([]digestUnit, 0, len(costs))
		for _, c := range costs {
			units = append(units, digestUnit{text: strings.Repeat("x", c-1), terms: []string{fmt.Sprint(c)}, weight: 1}) // 没有标签 ⇒ 行成本恰为 c
		}
		return newDigestLeaf(units)
	}
	sameA, sameB := leaf(100, 100), leaf(100, 100)
	if got := splitBudget(400, sameA, sameB); got != 200 {
		t.Fatalf("需求相同应对半: %d", got)
	}
	// 需求小的拿够即止。
	small, big := leaf(100), leaf(100, 100, 100)
	if got := splitBudget(300, small, big); got != 100 {
		t.Fatalf("需求 100 的子树至多拿 100: %d", got)
	}
	// 保底：左边最优一行 300 字节，按比例只分到 200 ⇒ 提到 300（右边仍 ≥ 它的保底）。
	longBest, other := leaf(300, 100), leaf(100, 100)
	if got := splitBudget(400, longBest, other); got != 300 {
		t.Fatalf("负担得起时左边应拿到它的保底 300: %d", got)
	}
	// 保底之和超出预算 ⇒ 回到按比例（保证不了就不假装保证）。
	if got := splitBudget(300, longBest, other); got >= 300 {
		t.Fatalf("保底之和 %d 超过预算 300 时不应硬给左边 300: %d", longBest.floor+other.floor, got)
	}
	if got := splitBudget(400, &digestNode{demand: 0}, other); got != 0 {
		t.Fatalf("没有需求的子树应分到 0: %d", got)
	}
}

func TestParseDigestLinesKeepsLabels(t *testing.T) {
	units := parseDigestLines(strings.Join([]string{
		"user: Please verify the invoice ledger totals before the release cut.",
		"tool_result: ok done fine result output value total count",
		"###",
		"plain line without any label at all here",
	}, "\n"))
	if len(units) != 3 {
		t.Fatalf("无效行应被过滤: %+v", units)
	}
	if units[0].label != "user" || units[0].weight != digestLabelWeight("user") {
		t.Fatalf("人类消息的标签与权重应保留: %+v", units[0])
	}
	if units[1].label != "tool_result" || units[1].weight >= units[0].weight {
		t.Fatalf("工具结果的权重应低于人类消息: %+v", units[1])
	}
	if units[2].label != "" || unitLine(units[2]) != "plain line without any label at all here" {
		t.Fatalf("没有标签的行原样保留: %+v", units[2])
	}
}

func TestDigestCacheIsBoundedFIFO(t *testing.T) {
	c := newDigestCache(16)
	var keys [][32]byte
	for i := 0; i < 17; i++ {
		k := [32]byte{byte(i)}
		keys = append(keys, k)
		c.put(k, fmt.Sprint(i))
	}
	if _, ok := c.get(keys[0]); ok {
		t.Fatal("超过容量后最早的条目应被淘汰")
	}
	for i := 1; i < 17; i++ {
		if v, ok := c.get(keys[i]); !ok || v != fmt.Sprint(i) {
			t.Fatalf("条目 %d 应仍在: %q %v", i, v, ok)
		}
	}
	c.put(keys[1], "changed")
	if v, _ := c.get(keys[1]); v != "1" {
		t.Fatalf("已存在的键不应被覆盖: %q", v)
	}
}

func TestMessageUnitsCapsLongMessagesAndFlagsEnds(t *testing.T) {
	lines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("line number %d with enough text to count as a unit", i))
	}
	raw := []byte(fmt.Sprintf(`{"role":"assistant","content":%q}`, strings.Join(lines, "\n")))
	units := messageUnits(raw, "assistant", 1.2)
	if len(units) != digestUnitsHead+digestUnitsTail {
		t.Fatalf("超长消息应截成 %d 行，实际 %d", digestUnitsHead+digestUnitsTail, len(units))
	}
	if !units[0].first || !units[len(units)-1].last || units[1].first {
		t.Fatalf("首行/末行标记不对: first=%v last=%v", units[0].first, units[len(units)-1].last)
	}
	if units[len(units)-1].text != lines[299] || units[0].text != lines[0] {
		t.Fatal("截断应保留最前与最后的行")
	}
	// 过短 / 纯符号 / 没有词项的行不参与。
	trim := messageUnits([]byte(`{"role":"user","content":"###\n\n12345\n()\n  \nok fine line here\n{\"a\":1}"}`), "user", 2.0)
	if len(trim) != 1 || trim[0].text != "ok fine line here" {
		t.Fatalf("无效行应被过滤: %+v", trim)
	}
	// 超长单行按 UTF-8 边界截断。
	long := messageUnits([]byte(fmt.Sprintf(`{"role":"user","content":%q}`, strings.Repeat("汉字", 300))), "user", 2.0)
	if len(long) != 1 || len(long[0].text) > digestUnitMaxBytes || !utf8.ValidString(long[0].text) {
		t.Fatalf("超长行应按 UTF-8 边界截到 %d 字节内: %d", digestUnitMaxBytes, len(long[0].text))
	}
}

func TestDigestConfigNormalized(t *testing.T) {
	c := DigestConfig{Enabled: true}.Normalized()
	if c.SegmentBytes != defaultDigestSegmentBytes || c.BudgetBytes != defaultDigestBudgetBytes || !c.Enabled {
		t.Fatalf("零值应取默认: %+v", c)
	}
	c = DigestConfig{SegmentBytes: 1, BudgetBytes: 1 << 20}.Normalized()
	if c.SegmentBytes != minDigestSegmentBytes || c.BudgetBytes != maxDigestBudgetBytes {
		t.Fatalf("越界值应夹回区间: %+v", c)
	}
	if perSegmentBudget(8<<10) != 512 || perSegmentBudget(64<<10) != 4096 || perSegmentBudget(256<<10) != 4096 {
		t.Fatal("每段预算 = 段大小/16，夹在 [256, 4096]")
	}
}
