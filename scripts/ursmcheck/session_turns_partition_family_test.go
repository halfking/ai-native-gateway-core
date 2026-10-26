package ursm_test

// §143.17 的文档门：session_turns 分区族的**全族读数**、**877 前提被生产证伪**、
// 以及**252 日志为空**这三条结论必须在场。
//
// 背景（runbook §143.17）：
// §143.16 把「按体积排 DROP」升级成「逐条实测 + 读方证据 + 分档 T0~T3」。
// §143.17 把同一套框架推到**分区族**，产出四条新证据：
//   ① 23 条父表声明里 8 条**全族 0 扫描**，合计 ≈322MB（但**这不是「索引没用」**）；
//   ② 迁移 877 的立项前提（`search_text ILIKE '%…%'` 是热路径）在生产**被证伪**；
//   ③ 877 在台账里有**两条互相矛盾**的登记（旧版已上、新版未上，却一个字都没写）；
//   ④ 252 的 PG 日志**是空的**（`logging_collector=off`）⇒ 靠日志判断慢查询这条线是断的。
//
// # 为什么「文档也要有门」
//
// ① **「0 扫描」这个说法最容易被反向传播**。§143.17 花了 8 条 EXPLAIN 才把它
//    钉成「**生产没有跑那种查询**」而不是「**索引没用**」——
//    一旦被简化成「0 扫描 = 白占空间，删掉」，下一步就会直接 DROP 掉 8 条声明
//    （≈322MB），而**其中 7 条的 EXPLAIN 明明会选它们**。
//    ⇒ 这道门钉住「T4 能力取舍」这个档位名与它的 8 条名单。
//
// ② **877 的结论会被「优化建议」覆盖**。它现在只剩一行「可以给 search_text 加索引」
//    在迁移文件头里；§143.17 证明那件事在生产是**净负**（83MB + 每行 trigram 维护
//    成本，换回 0 次查询）⇒ 这个结论必须留在 runbook 里等人拍板。
//
// ③ **台账缺口（877 两条矛盾登记）会被人当噪声**。§143.17 把它写成了
//    「旧版已上、新版未上」这个**可核对的事实**，不是一句抱怨。
//
// # 这道门测什么
//
// 门断言的是**文档结构**（小节在场、关键量在、关键结论在、待拍板清单在），
// 不连库、不跑 SQL ⇒ CI 与开发机都能跑。
// 判据的**语义**正确性由 §143.17 记录的生产实测背书（脚本见 §143.17.8）。

import (
	"os"
	"strings"
	"testing"
)

const runbook14317 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14317 取出 §143.17 这一节的正文。
//
// ★ 为什么必须**按节切**再断言：本 runbook 有 34,000+ 行，
// 同一个量（例如 "78 MB"、"83 MB"）在别的节里出现过十几次
// ⇒ 整篇断言等于**断言不到东西**（判据钉不住要钉的那一段）。
// ⇒ 判据的单位必须与被测对象的单位一致（§168）。
func section14317(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14317)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.17 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.17 的小节标题：§143.17 的结论整节丢了")
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

// TestRunbook14317HasPartitionFamilyReadings：8 个小节 + 关键实测读数必须在场。
//
// 关键量是**实测读数**，不是修辞：
//   - 322 MB（8 条全族 0 扫描的合计）
//   - 78 MB（分区侧 GIN）/ 83 MB（含 hot 侧 5.4MB）
//   - 177,621,479（最热声明 `idx_session_turns_request` 的全族扫描数）
//   - 51,217,944（老路径那条的扫描数 —— 同分区同列，一热一死）
//   - 34,528 / 8,442（`pg_stat_statements` 里两条 search_text 语句的次数）
//   - 111 MB（唯一约束声明，1 次扫描）
func TestRunbook14317HasPartitionFamilyReadings(t *testing.T) {
	sec := section14317(t)

	for i := 0; i <= 8; i++ {
		mark := "§143.17." + itoa14317(i)
		if !strings.Contains(sec, mark) {
			t.Errorf("§143.17 缺少小节 %s：本节的证据链是分层展开的，缺一节就会丢掉那一层的量", mark)
		}
	}
	for _, fact := range []string{
		"322 MB", "78 MB", "83 MB",
		"177,621,479", "51,217,944",
		"34,528", "8,442",
		"111 MB",
		"2026-10-18 22:03:37",
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.17 缺少实测读数 %q：清单的量必须是实测值，不能只剩结论", fact)
		}
	}
}

// TestRunbook14317StatesZeroScanIsNotUnused：「0 扫描」必须**连同它的限定**一起在。
//
// 这条专门防一个具体的退化：把 §143.17 简化成「8 条索引 0 扫描，删掉回收 322MB」。
// 实测（8 条 EXPLAIN）证明**计划器全都选它们** ⇒ 真相是「生产没有跑那种查询」，
// 删掉 = **放弃那项查询能力**，不是回收无用空间 ⇒ 这是独立档位 **T4「能力取舍」**。
//
// ⇒ 判据钉三样东西同时在场：
//
//	① 量具口径（`pg_inherits` 的**索引级**父子关系 —— 不能按名字配对，
//	   生产上同分区有两代命名）；
//	② 判词的改写（「计划器会选」这一侧）；
//	③ 8 条声明的**名单**（少一条就意味着有人已经动过它了）。
func TestRunbook14317StatesZeroScanIsNotUnused(t *testing.T) {
	sec := section14317(t)

	if !strings.Contains(sec, "pg_inherits") {
		t.Error("§143.17 里没有 pg_inherits：分区族「父声明 → 叶实例」的权威映射口径丢了（不能按名字配对）")
	}
	if !strings.Contains(sec, "T4「能力取舍」") {
		t.Error("§143.17 里没有 T4「能力取舍」这个档位：\n" +
			"⇒ 「0 扫描」被简化成「白占空间，删掉」的风险没有挡住。\n" +
			"实测 8 条 EXPLAIN 证明计划器**全都会选它们** ⇒ 那是「生产没跑那种查询」，\n" +
			"删掉 = 放弃一项查询能力（T4），不是回收无用空间（T3）")
	}
	if !strings.Contains(sec, "计划器会选") {
		t.Error("§143.17 里没有「计划器会选」这一侧：\n" +
			"⇒ 只留「8 条 0 扫描」而没有「计划器会选」这半句，读者会把 0 扫描读成「索引没用」")
	}
	for _, idx := range []string{
		"idx_session_turns_tenant_task_type",
		"idx_session_turns_tenant_namespace",
		"idx_session_turns_tenant_project",
		"idx_session_turns_search_trgm",
		"idx_session_turns_t0_arrived",
		"idx_session_turns_abandoned_parent",
		"idx_session_turns_attachment_count_gw",
		"uq_session_turns_final_success",
	} {
		if !strings.Contains(sec, idx) {
			t.Errorf("§143.17 的 8 条零扫描名单里没有 %s：名单必须逐条在，"+
				"少一条就分不清是「没写」还是「已经被人动过了」", idx)
		}
	}
}

// TestRunbook14317Records877FalsificationAndLogGap：877 证伪 + 台账缺口 + 日志空洞，
// 三件事必须同时在。
//
// 三者缺一不可：
//
//	· 只有「证伪」没有「台账缺口」⇒ 下一个人会以为台账是可信的（它不是）；
//	· 只有「台账缺口」没有「证伪」⇒ 不知道该不该撤回 877；
//	· 没有「252 日志是空的」⇒ 会继续用「查日志」这条**根本读不出东西**的线。
func TestRunbook14317Records877FalsificationAndLogGap(t *testing.T) {
	sec := section14317(t)

	for _, fact := range []string{
		"旧版已上、新版未上", // 台账缺口的**结论**
		"2771026d",  // 旧版（已应用）的 SHA
		"2df3437e",  // 当前文件（未应用）的 SHA
		"改写前 SHA",   // 655 先例的写法，877 缺的就是这句
		"logging_collector=off",
		"96h",
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.17 缺少 %q：877 的证伪结论 / 台账缺口 / 252 日志空洞必须成套在", fact)
		}
	}
	if !strings.Contains(sec, "本节对生产的写入 = 0") {
		t.Error("§143.17 丢了「本节对生产的写入 = 0」这条边界声明：\n" +
			"⇒ 读者无法知道这一节只是只读观测，误以为可以照着它的建议直接上生产")
	}
}

// TestRunbook14317HasOwnerDecisionTable：待拍板清单必须在场。
//
// §143.17 的产出**不是**可以直接执行的动作，而是一组要属主拍板的取舍
// （A1 撤回 877 / A2 修台账 / A3 加门 / B1~B5 索引取舍 / C1 打开日志）⇒
// 这张表是本节的**交付物**。表丢了，读数就只是观测。
func TestRunbook14317HasOwnerDecisionTable(t *testing.T) {
	sec := section14317(t)

	for _, item := range []string{
		"| **A1** |", "| **A2** |", "| **A3** |",
		"| **B1** |", "| **B2** |", "| **B3** |", "| **B4** |", "| **B5** |",
		"| **C1** |",
	} {
		if !strings.Contains(sec, item) {
			t.Errorf("§143.17 的待拍板清单里缺 %s：本节的交付物是这张表，读数本身只是观测", item)
		}
	}
}

// TestRunbook14317ReconcilesWithParallel877Audit：§143.17 与**另一会话**的 877 专项审计
// 必须**对齐**，且订正横幅必须在场。
//
// # 为什么要这道门
//
// 同一仓库的**另一会话**在 2026-10-19/20 推了 3 份 877 专项审计（已在 origin/main）：
//
//	docs/audit/2026-10-19-877-写放大定量决策.md
//	docs/audit/2026-10-20-877-读侧语义订正.md
//	docs/audit/2026-10-20-q0-demand-vs-broken-promise.md
//
// 其中**两处推翻了 §143.17 的原话**：
//
//	① 「生产的 `search_text` 是 **TOAST 宽列**」—— 实测**不是**（均值 92 / 最大 148 字符）；
//	② 「**撤回 877** = 推荐」—— 现行口径是「**不回滚** + `?q=` 降级为显式启用 + **30 天触发条件**」。
//
// ⇒ ★ 跨会话的两份文档**如果各说各话，下一个人会把被推翻的那条当结论用**
// ⇒ 这道门钉住「订正横幅 + 交叉引用 + 现行口径」三样东西同时在场。
//
// ⇒ ★ 判词**不钉**「TOAST 宽列」这几个字的**消失**（横幅里必须**引用**原话才能订正它）
// ⇒ 改钉**订正后的实测值**（均值 92 / 最大 148、distinct trigram 84.59）与**现行口径的关键词**。
func TestRunbook14317ReconcilesWithParallel877Audit(t *testing.T) {
	sec := section14317(t)

	// ① 订正横幅本体
	if !strings.Contains(sec, "§143.17.4 订正横幅") {
		t.Error("§143.17 缺「§143.17.4 订正横幅」：\n" +
			"⇒ 另一会话的生产实测已推翻本节两条原话（「TOAST 宽列」前提、\n" +
			"   「撤回 = 推荐」的处置），没有横幅就等于把被推翻的结论留在正文里继续被读")
	}
	// ② 三份对方文档的交叉引用（复现时要能直接找到对方材料）
	for _, ref := range []string{
		"docs/audit/2026-10-19-877-写放大定量决策.md",
		"docs/audit/2026-10-20-877-读侧语义订正.md",
		"docs/audit/2026-10-20-q0-demand-vs-broken-promise.md",
		"scripts/audit/q-usage.sh",
	} {
		if !strings.Contains(sec, ref) {
			t.Errorf("§143.17 没有引用对方材料 %q：跨会话的两份结论必须互相指名，\n"+
				"否则下一个人只能看到其中一份 ⇒ 就会把被推翻的那条当结论", ref)
		}
	}
	// ③ 订正后的实测值（而不是被推翻的原话）
	for _, fact := range []string{
		"均值 92 / 最大 148",                // search_text 真实宽度 ⇒ 不是 TOAST 宽列
		"84.59",                         // 生产 distinct trigram 数
		"2026-10-19 16:59:08.656651+08", // 877 已在生产应用
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.17 缺订正后的实测 %q：订正必须有**可复现的数**，不能只写「对方说不是」", fact)
		}
	}
	// ④ 现行口径（处置建议）
	for _, phrase := range []string{"不回滚 877", "30 天"} {
		if !strings.Contains(sec, phrase) {
			t.Errorf("§143.17 缺现行口径关键词 %q：\n"+
				"⇒ 处置口径必须与另一会话 2026-10-20 的决策一致\n"+
				"   （不回滚 + ?q= 降级为显式启用 + 30 天触发条件），\n"+
				"   否则两份文档会给出互相矛盾的动作", phrase)
		}
	}
	// ⑤ 被推翻的处置必须在正文里**明确标记**为不再是推荐项（原文保留但要打上标记）
	if !strings.Contains(sec, "不再是推荐项") {
		t.Error("§143.17 没有把原来的「选项 A（推荐）：撤回 877」标记为不再是推荐项：\n" +
			"⇒ 原文按纪律保留是可以的，但**必须**打上标记，否则读者仍会照「推荐」执行")
	}
}

// itoa14317：把 0~8 转成字符串（避免为两处断言引入 strconv）。
func itoa14317(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
