package ursm_test

// §143.25 的文档门：**把「11-03 的超时风险」从「未量化」变成「实测 3.4 秒」**、
// **推翻「columnar 批量写更慢」的直觉**、**把失败模式重新定级为「静默且不可恢复」**、
// 以及**订正 §143.23 的「本地无 columnar」**（订正横幅六）。
//
// 背景（runbook §143.25）：
// §143.22 结论 4 / §143.23.5 / §143.24.3 一致留下同一个口子——「301,255 行单事务搬进
// columnar」vs `runArchive` 的 10 分钟 timeout，耗时始终未被量化。本节用**本地真库**
// （654 函数体逐字、真 columnar 路径）把它实测掉：**中位 3,410 ms**。
//
// # 为什么这次必须钉门
//
// ① ★★★★ **「未量化」本身是决策阻塞**：属主无法判断要不要为 11-03 做任何准备。
//    实测把它从风险清单里划掉 ⇒ 门钉住「已量化 + 读数」。
// ② ★★★★ **「columnar 更慢」是个会被到处复用的错误直觉** ⇒ 若被后人从本节读到
//    「columnar 写 301,255 行只要 3.4 秒」却没读到对照臂的 4,213 ms，就会得出
//    「columnar 适合所有搬数据场景」的过度推广 ⇒ 门钉住**两个臂的数字都在**。
// ③ ★★★★ **失败模式的定级方向与上一轮相反**（上一轮是「超时」，这一轮是「静默且不可恢复」）
//    ⇒ 门钉住「唯一窗口 / 无指标 / 无补跑入口 / 不丢数据也不自愈」这四条事实。
// ④ ★★★★ **订正要保留原文**（§143.23.5 的「本地只测了 heap 路径」必须仍在原处）。
// ⑤ ★★★★ **守卫脚本本身**：若有人把实测脚本「修回」只测 heap 的形态
//    （即无条件把 `USING columnar` 替换成 `USING heap`），本节的实测结论就失去证据力
//    ⇒ 门必须钉住 **columnar 臂仍然存在**。

import (
	"os"
	"strings"
	"testing"
)

const runbook14325 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// columnarScript14325 是 §143.25 的本地实测脚本（一次性库，不硬编码密码）。
const columnarScript14325 = "../../scripts/audit/verify_columnar_bulk_write_local.py"

// section14325 取出 §143.25 这一节的正文。
func section14325(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14325)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.25 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.25 的小节标题：§143.25 的结论整节丢了")
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

// mustContain14325 断言 §143.25 正文里至少含 needle 之一。
func mustContain14325(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.25 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// TestRunbook14325TimeoutRiskIsQuantified 钉住「超时风险已量化，且量级上极不可能触发」。
//
// ★ 这是本节最关键的一条：它解除的是「要不要为 11-03 准备什么」这个决策阻塞。
func TestRunbook14325TimeoutRiskIsQuantified(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"3,410 ms",
		"301,255",
		"600,000 ms",
		"176×",
		"裕量",
	)
}

// TestRunbook14325ColumnarIsNotSlowerThanHeap 钉住 **两个臂的数字都在**。
//
// ★ 只钉 columnar 的 3,410 ms 是不够的：那会让人读成「columnar 适合所有搬数据场景」。
// ★ 必须同时钉住 heap 对照臂（4,213 ms）与倍数关系（0.81× / 0.77×）。
func TestRunbook14325ColumnarIsNotSlowerThanHeap(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"4,213 ms",
		"0.81×",
		"0.77×",
		"列式批量写入",
	)
}

// TestRunbook14325FailureModeReclassified 钉住失败模式重定级的四条事实。
//
// ★ 方向与 §143.22/§143.24 相反：这一轮的风险不是「超时」，而是「失败没人知道、且不可恢复」。
func TestRunbook14325FailureModeReclassified(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"now.Day() <= 3",
		"唯一归档窗口",
		"无指标、无告警",
		"人工补跑没有产品入口",
		"不会丢数据",
		"不会自愈",
	)
}

// TestRunbook14325ObservationHasFailureVerdict 钉住观测口径里**新增的失败判定**与人工补跑形态。
func TestRunbook14325ObservationHasFailureVerdict(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"失败并回滚",
		"人工补跑",
		"archive_credential_model_index(date '2026-09-03')",
	)
}

// TestRunbook14325HonestBoundaries 钉住诚实边界：本地≠生产；对生产写入 0；不改生产代码。
func TestRunbook14325HonestBoundaries(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"对生产的写入 = 0",
		"本地实测 ≠ 生产实测",
		"不是「生产上一定是 3.4 秒」",
		"生产行为未变",
	)
}

// TestRunbook14325NoTimeoutKnobChangeRecommended 钉住「**不要**为防超时去改 10 分钟超时」。
//
// ★ 这是本节的一条**反向结论**：实测把超时从风险清单里划掉 ⇒ 改它属于为不存在的问题付代价。
func TestRunbook14325NoTimeoutKnobChangeRecommended(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"不要**为了「防超时」去改 `runArchive` 的 10 分钟超时",
		"明确不推荐",
	)
}

// TestRunbook14325RecommendationsAreTiered 钉住建议清单按成本排序、且含「不赶上线」的裁决。
func TestRunbook14325RecommendationsAreTiered(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"**R1**",
		"**R2**",
		"**R3**",
		"**R4**",
		"**R5**",
		"不需要赶上线",
	)
}

// TestRunbook14325CorrectionBannerSixKeepsOriginal 钉住订正横幅六 + **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
// ★ 横幅必须**点名**要订正的是哪条，否则后人读不到改了什么。
func TestRunbook14325CorrectionBannerSixKeepsOriginal(t *testing.T) {
	raw, err := os.ReadFile(runbook14325)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	full := string(raw)

	if n := strings.Count(full, "订正横幅六（2026-10-20，§143.25.4 就地订正"); n != 2 {
		t.Fatalf("runbook 里找到 %d 处「订正横幅六」标题（§143.23.5 就地横幅 + §143.25.4 引用），期望 2 处", n)
	}

	// ★ 原文必须保留：§143.23.5 那条被订正的边界仍在原处。
	if !strings.Contains(full, "- ⚠ **本地只测了 heap 路径**（无 columnar AM）") {
		t.Fatalf("§143.23.5 的原文「本地只测了 heap 路径」不见了 —— 本仓纪律要求订正时**保留原文**（只加横幅，不静默改写）")
	}

	// ★ 横幅必须点名要订正的那条结论。
	for _, n := range []string{
		"本地 citus 13.3-1 不含 `columnar` access method",
		"扩展名不是 `columnar`",
		"这条的前提是错的",
	} {
		if !strings.Contains(full, n) {
			t.Fatalf("订正横幅六里没有点名要订正的 %q —— 被订正的结论没被点名，后人读不到改了什么", n)
		}
	}
}

// TestRunbook14325ScriptKeepsColumnarPath 钉住**实测脚本仍然走真 columnar 路径**。
//
// ★ 这是本节证据力的根：脚本若被改成「无条件把 `USING columnar` 替换成 `USING heap`」，
//   §143.25 的实测结论就退回成 §143.23 的 heap 路径 ⇒ 结论失去证据力。
func TestRunbook14325ScriptKeepsColumnarPath(t *testing.T) {
	raw, err := os.ReadFile(columnarScript14325)
	if err != nil {
		t.Fatalf("读实测脚本失败：%v", err)
	}
	script := string(raw)

	// ★ columnar 臂必须还在 A/B 交错循环里。
	if !strings.Contains(script, `("columnar", col_vals)`) {
		t.Fatalf("实测脚本里找不到 columnar 臂（`(\"columnar\", col_vals)`）—— 实测退回成只测 heap，§143.25 的结论失去证据力")
	}
	// ★ AM 替换必须**只**发生在 heap 臂上（条件替换，而不是无条件替换）。
	if !strings.Contains(script, `if am == "heap":`) {
		t.Fatalf("实测脚本里找不到 `if am == \"heap\":` —— AM 替换不再受限于 heap 臂，columnar 臂可能被悄悄改掉")
	}
	// ★ 脚本必须自己装 columnar 扩展（否则跑不起来，也说明本地其实具备该能力）。
	if !strings.Contains(script, "citus_columnar") {
		t.Fatalf("实测脚本里没有 `citus_columnar` —— 本地 columnar 能力的用法被改掉了")
	}
	// ★ 必须保留「从仓内 654 迁移提取函数体」的做法（不手抄）。
	for _, n := range []string{
		"654_archive_credential_model_index_detach_drop.sql",
		"USING columnar",
	} {
		if !strings.Contains(script, n) {
			t.Fatalf("实测脚本里没有 %q —— 「函数体从仓内迁移提取、columnar 臂逐字不改」这个证据力做法丢了", n)
		}
	}
}

// TestRunbook14325SectionHeadingsPresent 钉住 §143.25 的子节骨架齐全。
//
// ★ 门要在**整节丢失**时也能红（不只是数字丢一个）。
func TestRunbook14325SectionHeadingsPresent(t *testing.T) {
	s := section14325(t)

	for _, h := range []string{
		"### §143.25.0 结论先行",
		"### §143.25.1 ",
		"### §143.25.2 ",
		"### §143.25.3 ",
		"### §143.25.4 ",
		"### §143.25.5 ",
		"### §143.25.6 ",
		"### §143.25.7 诚实边界",
		"### §143.25.9 复核方法（可原样重跑）",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.25 缺少子节 %q —— 整节骨架不全", h)
		}
	}
}

// TestRunbook14325MigrationNumberDrift 钉住「迁移号 885 已被并行会话占用 ⇒ 下一个空号 890」。
//
// ⚠★ **这里的 885 指的是「被并行会话占用的那个号」，不是本会话的 v1 族退役迁移号**
//   （本会话的 v1 族退役迁移在改号后是 **890**、配套索引迁移是 **891**）。
//   ⇒ §143.25.8 记的是**当时**的取号现场（885 被占 ⇒ 下一个空号 890），
//   原文保留、不做改号；改号会让这条 needle 与 runbook 正文脱钩 ⇒ 门变红。
//   ⇒ 890 随后**被本会话占用**，这就是「取号会漂移」的活样本（订正见 §143.25.8）。
//
// ★ 这是共享仓库的硬约束：任何「预留某个迁移号」的结论**都会漂移**
//   ⇒ 必须钉住「先现场取号」这条纪律，否则后人会照着旧号写迁移、撞上别人的号。
func TestRunbook14325MigrationNumberDrift(t *testing.T) {
	s := section14325(t)

	mustContain14325(t, s,
		"885 已被并行会话占用",
		"890",
		"下一个空号",
		"动迁移号之前必须现场取号",
	)
}
