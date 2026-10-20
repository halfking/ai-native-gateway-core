package ursm_test

// §143.33 的文档门：**P16 量化 ⇒ 它不是瓶颈 ⇒ 降级**；并**订正 §143.31 的推断**。
//
// 背景（runbook §143.33）：
// ① §143.31 把 `stats_ground_truth_gap`（路由健康检查）登记成 P16「CPU 侧竞争者」，
//    并推断「它与 rollup 抢 CPU ⇒ 252 的 load 14/4 核 是多个作业共同造成的」；
// ② §143.33 把它量化 ⇒ 总 DB 时间只有 0.17% 墙钟、物理读 230 块/次
//    ⇒ ⇒ **它既不吃 CPU 也不吃 I/O ⇒ P16 降级**，且那条因果链**作废**；
// ③ ⇒ ⇒ **252 上有两条独立的瓶颈线**（I/O 线 = 巡检；总 DB 时间线 = matview 刷新）；
// ④ ⇒ ⇒ **§143.29 把 P1「降为第三」要调回**（但两把尺子口径不同，顺序取决于要解决磁盘还是 CPU）；
// ⑤ ⇒ ⇒ **§143.31.4 的诚实边界「没有被量化」已闭合**，且**方向与当时的预设相反**。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **「它与 rollup 抢 CPU」是本项目至今最贵的一个未经量化的推断** ——
//    它已经进了 §143.31.1 的正文与 §143.31.3 的表格 ⇒ 不钉住订正，
//    后人会照着「多个作业共同造成 load」继续排优先级。
// ② ★★★★★ **「降级」与「升级」必须同样钉住** ——
//    只钉「P16 不是瓶颈」不够：还得钉住「它仍然值得盯（max 15.9 s）」与
//    「P1 要调回」⇒ 否则会有人把降级读成「彻底不用管」或「把 P16 提到最前」。
// ③ ★★★★ **「两条瓶颈线」是本节最容易被后人混谈的结论** ⇒ 门钉住它。
// ④ ★★★★ **订正必须【就地】**：横幅断言钉在 §143.31（横幅的「老家」），
//    §143.33 的正文断言钉在 §143.33 ⇒ 防止「横幅被人从 §143.31 挪走」而门还绿。
// ⑤ ★★★ **诚实边界**（写入 = 0 / 未改代码 / 门不钉行号）必须钉住。

import (
	"os"
	"strings"
	"testing"
)

const runbook14333 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14333 取出指定小节的正文（从 "## <prefix>" 到下一个 "## " 之前）。
func section14333(t *testing.T, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14333)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## "+prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到小节 %q —— 这一节整节丢了", prefix)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// mustContain14333 断言给定文本里至少含 needle 之一。
func mustContain14333(t *testing.T, what string, body string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Fatalf("%s 里缺关键读数 %q —— 判据要求的关键证据不见了", what, n)
		}
	}
}

// TestRunbook14333SectionHeadingsPresent 钉住 §143.33 的子节骨架齐全。
func TestRunbook14333SectionHeadingsPresent(t *testing.T) {
	s := section14333(t, "§143.33 ")

	for _, h := range []string{
		"### §143.33.0 结论先行",
		"### §143.33.1 复核方法",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.33 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14333P16Quantified 钉住 P16 的量化读数（生产只读实测）。
func TestRunbook14333P16Quantified(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"stats_ground_truth_gap",
		"2026-10-18 22:03:37",
		"331",
		"264,220.1 ms",
		"798.2 ms",
		"15,910.5 ms",
		"76,147",
		"230 块/次",
		"0.17%",
	)
}

// TestRunbook14333ReadingsAreStable 钉住「复读」这条证据 ——
//
// ★ 只钉单点读数，后人会问「这是不是噪声」；两次读数并排才是稳定性证据。
func TestRunbook14333ReadingsAreStable(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"稳定性复读",
		"329 → 331",
		"256,837 → 264,220.1 ms",
		"780.7 → 798.2 ms",
		"76,147（不变）",
		"均值漂移 2.2%",
	)
}

// TestRunbook14333P16IsDowngraded 钉住「降级」这个结论本身。
//
// ★ 必须同时钉住「不是瓶颈」与「仍值得盯」⇒ 只钉前者会被读成「彻底不用管」。
func TestRunbook14333P16IsDowngraded(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"P16 降级",
		"次要",
		"值得盯，但不值得为它单独做动作",
		"15.9 s",
		"离 30 s 的角色级超时只差一半",
	)
}

// TestRunbook14333TwoIndependentBottleneckLines 钉住「两条独立的瓶颈线」。
func TestRunbook14333TwoIndependentBottleneckLines(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"两条独立的瓶颈线",
		"2.9 GB",
		"74 GB",
		"6,965 s",
		"IO|DataFileRead",
		"不要混谈",
	)
}

// TestRunbook14333P1IsRestored 钉住「§143.29 把 P1 降为第三，§143.33 要调回」。
//
// ★ 必须同时钉住「调回」与「两把尺子口径不同」⇒ 否则会把 P1 拉成无条件第一。
func TestRunbook14333P1IsRestored(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"把 P1「降为第三」",
		"调回",
		"物理读/次",
		"总 DB 时间",
		"顺序取决于【你要解决的是磁盘还是 CPU】",
		"先修巡检（P11）",
	)
}

// TestRunbook14333Corrects14331Inference 钉住「对 §143.31 推断的订正」这件事本身。
//
// ★ 关键：原文必须**逐字保留**（订正 = 加横幅，不是改写原文）。
func TestRunbook14333Corrects14331Inference(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"订正 §143.31 的一句推断",
		"原文保留",
		"前半",
		"后半",
		"没有证据支撑 ⇒ 作废",
		"它与 rollup 抢 CPU",
	)
}

// TestRunbook14333BannersLiveIn14331 钉住**订正横幅就地钉在 §143.31**。
//
// ★ 这条是本门最重要的一条：横幅是「就地订正」的载体，
//   它一旦被人从 §143.31 挪走（或删掉），§143.31.1 的错误推断就又变成「无注解的结论」。
func TestRunbook14333BannersLiveIn14331(t *testing.T) {
	s31 := section14333(t, "§143.31 ")
	s33 := section14333(t, "§143.33 ")

	// ★ 横幅必须在 §143.31 里（§143.33 只引用它们，不承载它们）。
	mustContain14333(t, "§143.31 正文", s31,
		"订正横幅十二",
		"订正横幅十三",
		"订正横幅十四",
	)
	for _, tag := range []string{"订正横幅十二", "订正横幅十三", "订正横幅十四"} {
		if strings.Contains(s33, "> **"+tag+"（") {
			t.Fatalf("订正横幅 %s 的【本体】出现在 §143.33 里 ⇒ 它被搬走了 ⇒ "+
				"「就地订正」不成立（§143.33 只能引用，不能承载）", tag)
		}
	}

	// ★ 被订正的原文必须逐字还在（原文保留）。
	mustContain14333(t, "§143.31 正文", s31,
		"它与 rollup 抢 CPU，而 rollup 与巡检抢 I/O",
		"`stats_ground_truth_gap` 只被识别出来，**没有被量化**",
	)

	// ★ 三条横幅各自主张的对象。
	mustContain14333(t, "§143.31 正文", s31,
		"订正横幅十二（2026-10-20，§143.33 就地订正本节 §143.31.1）",
		"订正横幅十三（2026-10-20，§143.33 就地订正本节 §143.31.3 的 P16 行）",
		"订正横幅十四（2026-10-20，§143.33 就地订正本节 §143.31.4 的这条诚实边界）",
	)
}

// TestRunbook14333HonestBoundaryIsClosed 钉住「§143.31.4 的诚实边界已闭合」，
//
// ★ 且**方向与当时的预设相反**（「登记时以为它是竞争者 ⇒ 量化后发现不是」）。
func TestRunbook14333HonestBoundaryIsClosed(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"没有被量化",
		"已被 §143.33 闭合",
		"方向与 §143.31 的预设【相反】",
		"必须补量化",
	)
}

// TestRunbook14333DoesNotPinLineNumber 钉住「门不钉行号」。
//
// ★ 本节发现文档引用的行号会漂移（947 → 948）⇒ 门若钉行号，
//   别人的并行提交就会把门打红 ⇒ 那是【量具坏了】而不是【守卫失效】。
//
// ⚠ ★★ **本门的第一版曾禁掉「§143.33 里出现那个带行号的源码引用」** ——
//   那条禁令【本身是错的】，已撤回。理由：
//   ★ **§143.33 的第 7 条正是「展示这个漂移」⇒ 它必须引用 947 与 948 两个数字**，
//   ⇒ **禁掉引用 = 把证据本身删掉**。
//   ⇒ ⇒ ★★★ **⇒ 「禁止【提及】」与「禁止【依赖】」是两件事** ——
//   要禁的是**判据依赖行号**（门自己的 needle），**不是文档提及行号**。
//   ⇒ ⇒ ★★ **⇒ 本门因此改成：门自身只钉稳定锚点（CheckID），同时要求文档把漂移写出来。**
func TestRunbook14333DoesNotPinLineNumber(t *testing.T) {
	s := section14333(t, "§143.33 ")

	// 稳定锚点是 CheckID，不是行号。
	mustContain14333(t, "§143.33 正文", s,
		"stats_ground_truth_gap\", Severity",
		"文档引用漂移",
		"前提",
	)

	// ★ 漂移必须被【展示】出来：两个行号都要在，且要说明方向。
	mustContain14333(t, "§143.33 正文", s,
		"947",
		"948",
		"并行会话提交之后",
		"行号",
	)

	// ★★★ **本门自己的【断言 needle 集合】里不得含「源码文件名 + 行号」** ——
	//   这是「判据不依赖行号」的真检查（★ 不是扫源码全文 —— 那会自指）。
	//   ⚠ ★★ **扫描源码全文是错的**：检查清单自身、以及解释「为什么禁」的注释
	//   都会含有那个字面量 ⇒ ⇒ **自指 ⇒ 恒红 ⇒ 门失去意义**。
	//   ⇒ ⇒ ★★★ **⇒ 正确形态：把「本门用到的 needle」显式列出来，逐个判定。**
	for _, needle := range section14333Needles() {
		if looksLikeFileLineRef(needle) {
			t.Fatalf("本门的断言 needle %q 是「源码文件名 + 行号」⇒ "+
				"行号会随并行提交漂移（实测 947 → 948）⇒ 应当改钉 CheckID", needle)
		}
	}
}

// section14333Needles 返回本门【实际用于断言】的全部 needle。
//
// ★ 显式列出（而不是反射/扫描源码）—— 这既是自检的输入，
//   也是「门到底钉了什么」的单一事实源。
func section14333Needles() []string {
	return []string{
		// §143.33 正文
		"### §143.33.0 结论先行",
		"### §143.33.1 复核方法",
		"stats_ground_truth_gap",
		"2026-10-18 22:03:37",
		"331",
		"264,220.1 ms",
		"798.2 ms",
		"15,910.5 ms",
		"76,147",
		"230 块/次",
		"0.17%",
		"稳定性复读",
		"329 → 331",
		"256,837 → 264,220.1 ms",
		"780.7 → 798.2 ms",
		"76,147（不变）",
		"均值漂移 2.2%",
		"P16 降级",
		"次要",
		"值得盯，但不值得为它单独做动作",
		"15.9 s",
		"离 30 s 的角色级超时只差一半",
		"两条独立的瓶颈线",
		"2.9 GB",
		"74 GB",
		"6,965 s",
		"IO|DataFileRead",
		"不要混谈",
		"把 P1「降为第三」",
		"调回",
		"物理读/次",
		"总 DB 时间",
		"顺序取决于【你要解决的是磁盘还是 CPU】",
		"先修巡检（P11）",
		"订正 §143.31 的一句推断",
		"原文保留",
		"前半",
		"后半",
		"没有证据支撑 ⇒ 作废",
		"它与 rollup 抢 CPU",
		"没有被量化",
		"已被 §143.33 闭合",
		"方向与 §143.31 的预设【相反】",
		"必须补量化",
		"stats_ground_truth_gap\", Severity",
		"文档引用漂移",
		"前提",
		"947",
		"948",
		"并行会话提交之后",
		"行号",
		"对生产的写入 = 0",
		"没有**改任何代码 / 配置 / 调度",
		"可原样重跑",
		"TestRunbook14333",
		// 量具入仓
		"scripts/audit/sql/p16_ground_truth_gap_cost.sql",
		"0.166%",
		"交叉验证成立",
		"不是抄一个数字",
		"第一次跑是红的",
		"s2.stats_reset",
		"必须先在真库上跑通一次再 commit",
		// §143.31 横幅
		"订正横幅十二",
		"订正横幅十三",
		"订正横幅十四",
		"订正横幅十二（2026-10-20，§143.33 就地订正本节 §143.31.1）",
		"订正横幅十三（2026-10-20，§143.33 就地订正本节 §143.31.3 的 P16 行）",
		"订正横幅十四（2026-10-20，§143.33 就地订正本节 §143.31.4 的这条诚实边界）",
		"它与 rollup 抢 CPU，而 rollup 与巡检抢 I/O",
		"`stats_ground_truth_gap` 只被识别出来，**没有被量化**",
	}
}

// looksLikeFileLineRef 判断一个 needle 是否形如「源码文件名 + 行号」
// （例如某个 Go 文件路径后面跟着冒号 + 数字）。
//
// ★ 这正是本轮实测会漂移的那种引用（并行会话插一行 ⇒ 行号 +1）。
func looksLikeFileLineRef(needle string) bool {
	colon := strings.LastIndex(needle, ":")
	if colon < 0 {
		return false
	}
	tail := needle[colon+1:]
	if tail == "" {
		return false
	}
	for _, r := range tail {
		if r < '0' || r > '9' {
			return false
		}
	}
	// 前缀必须像「某个文件路径」，且带扩展名。
	prefix := needle[:colon]
	return strings.Contains(prefix, "/") && strings.Contains(prefix, ".")
}

// TestRunbook14333InstrumentIsInRepo 钉住「量具入仓」这条证据链。
//
// ★ 关键不是「脚本存在」（那太弱），而是三件事并排：
//   ① 量具真的在仓里、路径可重跑；
//   ② 它【独立算出了同一个量】（0.166% vs 0.17%）⇒ 不是抄一个数字；
//   ③ 它【第一次跑是红的】⇒ 「写了没验」这件事被记录下来。
func TestRunbook14333InstrumentIsInRepo(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"scripts/audit/sql/p16_ground_truth_gap_cost.sql",
		"0.166%",
		"交叉验证成立",
		"不是抄一个数字",
		"第一次跑是红的",
		"s2.stats_reset",
		"必须先在真库上跑通一次再 commit",
	)

	// ★ 仓里那份脚本必须真的存在（且含被修好的那条列引用）。
	raw, err := os.ReadFile("../../scripts/audit/sql/p16_ground_truth_gap_cost.sql")
	if err != nil {
		t.Fatalf("仓内量具脚本不存在：%v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"p16_ground_truth_gap_cost",
		"s2.stats_reset",
		"v5_pct_of_window",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("仓内量具脚本里缺 %q —— 脚本与文档不一致", needle)
		}
	}
	// ★ 那个修过的错误列引用不能回来（`stats_reset` 在 info 视图上，不在 pgss 上）。
	if strings.Contains(body, "s.stats_reset") {
		t.Fatalf("仓内量具脚本里出现 `s.stats_reset` ⇒ 那是第一次跑时红的那个错列引用")
	}
}

// TestRunbook14333HonestBoundaries 钉住本节的诚实边界。
func TestRunbook14333HonestBoundaries(t *testing.T) {
	s := section14333(t, "§143.33 ")

	mustContain14333(t, "§143.33 正文", s,
		"对生产的写入 = 0",
		"没有**改任何代码 / 配置 / 调度",
		"可原样重跑",
		"TestRunbook14333",
	)
}
