package ursm_test

// pipefail_race_threshold_wording_test.go —— 台账的【阈值措辞】门（2026-11-03 第三十一轮）
//
// # 这道门补的是哪块缺口
//
// 第三十轮实测把「≤ 64 KiB **确定**不翻转 / > 64 KiB **确定**翻转」这个**二分**推翻成
// 「**实测边界内**不翻转 / > 边界**不保证**翻转」——理由是**实测**，不是推敲：
//
//   · ★ 翻转是**竞态 ⇒ 概率型**（第三十一轮每点跑 5 次）
//   · Linux 上 87,943 / 107,831 / 131,072 B 实测 **0/5** 翻转
//     ⇒ ⇒ 「> 边界**必然**翻转」在实测上**不成立**
//   · 第三十轮那个「阈值非单调」的读数，**是单次采样的假象**
//     （5 次重复后 85,831 B 只翻 1/5）
//
// ⇒ ⇒ 台账里凡是引阈值的行，都必须满足三条：
//
//	① **禁二分**：不许出现「确定/必然/一定 + 翻转」的措辞
//	② **必带平台**：阈值必须标平台（linux/debian 与 macOS 的边界可差 4–16 倍
//	   ⇒ ★ 本机（macOS）量的阈值**不能用来判生产**（211 = Linux））
//	③ **判据量要对**：行里写的判据量必须与**量具算出来的形态**一致
//	   （★ 判据量 = 「被杀的是写端还是中间段」⇒ 阈值档位也随之不同）
//
// # 为什么判据量要跟形态绑定
//
// 第三十一轮实测出**统一判据**：
//
//	① **谁被杀** = 紧邻消费者的那个阶段
//	   （消费者早退 ⇒ 关闭读端 ⇒ 它的写端挨 EPIPE）
//	② **阈值** = 被杀者的**写入粒度**
//	   ⇒ 行式小写（`grep`）⇒ `PIPE_BUF`（Linux 4,096 B / macOS 16–32 KiB）
//	   ⇒ 块写（`cat`/`curl`/`echo`…）⇒ **管道容量**（≈64 KiB）
//
// ⇒ 所以「写端总量」与「中间段的输出体量」是**两个不同的量**，
// ★ 而台账的旧措辞把它们混成一个「管道缓冲 65,536 B」⇒ **阈值可差 16 倍**。
//
// # ★ 这道门与量具是**同一个实现**
//
// ⇒ 判据量取**量具候选集的第 13 列**（`criterion`，★ 由量具算，不在门里重算）
// ⇒ ⇒ 门与量具**零漂移**；量具一改，门立刻跟着变。
//
// # ★ 门自身的「有牙」证明
//
// `TestPipefailRaceThresholdGateHasTeeth` 对三条判据各造一个**坏样本**
// ⇒ 断言判据**必须**报出它 ⇒ 证明判据 ①②③ 真的在检查，而不是恒真。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// r31DichotomyRe = ★ 被禁的二分措辞。
//
// ★★ 为什么连「必然 / 一定 / 必定」一起禁：
// 第三十轮的错就出在「确定」这个词上（★ 实测证明它两个方向都不成立）
// ⇒ ⇒ 同义的换字写法也必须禁 ⇒ ★ 否则这道门只是堵住一个词。
var r31DichotomyRe = regexp.MustCompile(`(确定|必然|必定|一定)\s*(不)?翻转`)

// r31ThresholdRe = ★ 「这一行引了阈值」的判据（⇒ 触发平台标注与判据量核对）。
var r31ThresholdRe = regexp.MustCompile(`64\s*KiB|65,?536|65536|4,?096|16\s*KiB|32\s*KiB|PIPE_BUF|管道容量|管道缓冲|阈值`)

// r31PlatformRe = ★ 平台标注（★ linux / macOS 两个平台都算）。
var r31PlatformRe = regexp.MustCompile(`(?i)linux|macOS|darwin|debian`)

// r31CriterionRe = ★ 规范判据句的机器可读形态：「判据量 = **<量>**」。
var r31CriterionRe = regexp.MustCompile(`判据量\s*=\s*\*\*(.+?)\*\*`)

// r31ExpectedQty = criterion ⇒ 规范判据句里**必须**出现的量。
//
// ★★ 与量具的 CRIT_* 一一对应（★ 改一处必须改两处 ⇒ 门会校验）。
var r31ExpectedQty = map[string]string{
	"写端总量/管道容量":        "写端总量",
	"写端匹配输出/PIPE_BUF":  "写端匹配输出",
	"中间段匹配输出/PIPE_BUF": "中间段的匹配输出体量",
	"中间段输出体量/管道容量":     "中间段的输出体量",
}

// r31Row = 台账一行的最小视图（★ 只取门要用到的列）。
type r31Row struct {
	key      string
	file     string
	line     string
	reason   string
	evidence string
	text     string // reason + evidence
}

// r31Rows = 台账 → key → 行视图（★ 复用 R97 轮的门解析器 ⇒ 同一套列语义）。
func r31Rows(t *testing.T) map[string]r31Row {
	t.Helper()
	out := map[string]r31Row{}
	for k, cols := range r97Ledger(t) {
		if len(cols) < 13 {
			t.Fatalf("台账行列数 = %d，不足 13：%q", len(cols), cols)
		}
		reason, evid := cols[9], cols[10]
		out[k] = r31Row{
			key:      k,
			file:     cols[0],
			line:     cols[1],
			reason:   reason,
			evidence: evid,
			text:     reason + evid,
		}
	}
	return out
}

// r31CheckDichotomy = 判据 ①：禁二分措辞。
func r31CheckDichotomy(rows map[string]r31Row) []string {
	var out []string
	for k, r := range rows {
		if m := r31DichotomyRe.FindString(r.text); m != "" {
			out = append(out, fmt.Sprintf("%s:%s 出现二分措辞 %q（★ 实测：翻转是概率型 ⇒ 只能写「实测边界内不翻转 / > 边界不保证翻转」）",
				r.file, r.line, m))
		}
		_ = k
	}
	sort.Strings(out)
	return out
}

// r31CheckPlatform = 判据 ②：凡引阈值 ⇒ 必须带平台标注。
func r31CheckPlatform(rows map[string]r31Row) []string {
	var out []string
	for _, r := range rows {
		if !r31ThresholdRe.MatchString(r.text) {
			continue
		}
		if !r31PlatformRe.MatchString(r.text) {
			out = append(out, fmt.Sprintf("%s:%s 引了阈值却**没有平台标注**（★ linux 与 macOS 的边界可差 4–16 倍 ⇒ 本机读数不能判生产）",
				r.file, r.line))
		}
	}
	sort.Strings(out)
	return out
}

// r31CheckCriterion = 判据 ③：行内写的判据量必须与**量具算出的形态**一致。
func r31CheckCriterion(rows map[string]r31Row, crit map[string]string) []string {
	var out []string
	for k, r := range rows {
		if !r31ThresholdRe.MatchString(r.text) {
			continue
		}
		c, ok := crit[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s:%s 候选集里没有这一行 ⇒ 判据量无从核对", r.file, r.line))
			continue
		}
		if c == "跨行管道/未知" {
			// ★ 跨行管道（`\` 续行）⇒ 量具**看不到写端侧** ⇒ 老实标「未知」
			//   ⇒ ⇒ 这一行必须**人工判读**并写明（★ 不许假装量具看得出来）。
			if !strings.Contains(r.reason, "跨行管道") ||
				!(strings.Contains(r.reason, "人工读码") || strings.Contains(r.reason, "未知")) {
				out = append(out, fmt.Sprintf("%s:%s 是**跨行管道**（量具看不到写端侧）却没写明「人工判读/未知」", r.file, r.line))
			}
			continue
		}
		want, ok := r31ExpectedQty[c]
		if !ok {
			out = append(out, fmt.Sprintf("%s:%s 未知 criterion %q（★ 量具与门不同版本 ⇒ 门与量具漂移）", r.file, r.line, c))
			continue
		}
		m := r31CriterionRe.FindStringSubmatch(r.reason)
		if m == nil {
			out = append(out, fmt.Sprintf("%s:%s 没有规范判据句「判据量 = **…**」（★ 该行引了阈值 ⇒ 必须写清判据量）", r.file, r.line))
			continue
		}
		if m[1] != want {
			out = append(out, fmt.Sprintf("%s:%s 判据量写的是 %q，形态要求 %q（★ criterion=%s ⇒ 判据用错量 ⇒ 阈值可差 16 倍）",
				r.file, r.line, m[1], want, c))
		}
	}
	sort.Strings(out)
	return out
}

// TestPipefailRaceThresholdWordingHasNoDichotomy：判据 ①。
func TestPipefailRaceThresholdWordingHasNoDichotomy(t *testing.T) {
	rows := r31Rows(t)
	if len(rows) == 0 {
		t.Fatal("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	if bad := r31CheckDichotomy(rows); len(bad) > 0 {
		for _, b := range bad {
			t.Errorf("★ %s", b)
		}
	}
}

// TestPipefailRaceThresholdMentionsCarryPlatform：判据 ②。
func TestPipefailRaceThresholdMentionsCarryPlatform(t *testing.T) {
	rows := r31Rows(t)
	if len(rows) == 0 {
		t.Fatal("★ 台账为空 ⇒ 分母为 0 ⇒ 拒绝出结论")
	}
	if bad := r31CheckPlatform(rows); len(bad) > 0 {
		for _, b := range bad {
			t.Errorf("★ %s", b)
		}
	}
}

// TestPipefailRaceCriterionMatchesWording：判据 ③。
func TestPipefailRaceCriterionMatchesWording(t *testing.T) {
	rows := r31Rows(t)
	crit := r97ScanRepo(t).crit
	if len(crit) == 0 {
		t.Fatal("★ 候选集里没有 criterion ⇒ 量具与门不同版本 ⇒ 拒绝出结论")
	}
	if bad := r31CheckCriterion(rows, crit); len(bad) > 0 {
		for _, b := range bad {
			t.Errorf("★ %s", b)
		}
	}
}

// TestPipefailRaceThresholdGateHasTeeth：三条判据各自的**变异臂**。
//
// ★ 只动**内存里的合成样本**（★ 仓内台账零改动）。
func TestPipefailRaceThresholdGateHasTeeth(t *testing.T) {
	good := map[string]r31Row{
		"k1": {
			key: "k1", file: "a.sh", line: "1",
			reason:   "写端是变量 ⇒ ★ 判据量 = **写端总量**；阈值 = **管道容量 ≈ 64 KiB**（★ 平台相关）",
			evidence: "★ 平台：linux/debian:bookworm-slim + macOS/darwin25.6.0 ⇒ 65,536 B → 0/5 翻转",
			text:     "写端是变量 ⇒ ★ 判据量 = **写端总量**；阈值 = **管道容量 ≈ 64 KiB**（★ 平台相关）★ 平台：linux/debian:bookworm-slim + macOS/darwin25.6.0 ⇒ 65,536 B → 0/5 翻转",
		},
	}
	critGood := map[string]string{"k1": "写端总量/管道容量"}

	// ① 变异：塞回二分措辞 ⇒ 判据 ① 必须报
	m1 := good["k1"]
	m1.text = m1.text + " ⇒ > 64 KiB 确定翻转"
	m1.reason = m1.reason + " ⇒ > 64 KiB 确定翻转"
	mut1 := map[string]r31Row{"k1": m1}
	if len(r31CheckDichotomy(mut1)) == 0 {
		t.Error("★ 判据 ①（禁二分措辞）**没有牙**：塞回「确定翻转」却没报出来")
	}
	if len(r31CheckDichotomy(good)) != 0 {
		t.Error("★ 判据 ① 在**好样本**上就报错 ⇒ 判据恒真")
	}

	// ② 变异：去掉平台标注 ⇒ 判据 ② 必须报
	m2 := good["k1"]
	m2.text = "写端是变量 ⇒ ★ 判据量 = **写端总量**；阈值 = **管道容量 ≈ 64 KiB**"
	m2.reason = m2.text
	m2.evidence = ""
	mut2 := map[string]r31Row{"k1": m2}
	if len(r31CheckPlatform(mut2)) == 0 {
		t.Error("★ 判据 ②（阈值必带平台）**没有牙**：去掉平台标注却没报出来")
	}
	if len(r31CheckPlatform(good)) != 0 {
		t.Error("★ 判据 ② 在**好样本**上就报错 ⇒ 判据恒真")
	}

	// ③ 变异：把判据量写成中间段（形态却是写端）⇒ 判据 ③ 必须报
	m3 := good["k1"]
	m3.reason = strings.Replace(m3.reason, "**写端总量**", "**中间段的匹配输出体量**", 1)
	m3.text = m3.reason + m3.evidence
	mut3 := map[string]r31Row{"k1": m3}
	if len(r31CheckCriterion(mut3, critGood)) == 0 {
		t.Error("★ 判据 ③（判据量与形态一致）**没有牙**：把写端总量改成中间段却没报出来")
	}
	if len(r31CheckCriterion(good, critGood)) != 0 {
		t.Error("★ 判据 ③ 在**好样本**上就报错 ⇒ 判据恒真")
	}

	// ③′ 变异：形态是「跨行管道/未知」却没写明人工判读 ⇒ 必须报
	m4 := good["k1"]
	m4.reason = "写端是变量 ⇒ ★ 判据量 = **写端总量**"
	m4.text = m4.reason + m4.evidence
	mut4 := map[string]r31Row{"k1": m4}
	if len(r31CheckCriterion(mut4, map[string]string{"k1": "跨行管道/未知"})) == 0 {
		t.Error("★ 判据 ③ 的跨行分支**没有牙**：没写明「人工判读/未知」却没报出来")
	}
}
