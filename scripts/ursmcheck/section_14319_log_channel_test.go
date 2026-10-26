package ursm_test

// §143.19 的文档门：252 PG 日志通道的真实拓扑、**按库拆库**的生产慢查询清单、
// §143.17 两条判词的**就地订正横幅**、以及 `refreshIndexSQL` 的成本根因。
//
// 背景（runbook §143.19）：
// §143.16~§143.18 把「索引能不能删」这件事做成了逐条实测；本节把同一套纪律
// 搬到**观测手段本身**上：
//   ① 252 的 PG 日志通道**从未断过**（§143.17.0 第 4 条 / §143.17.5 的判词被证伪）
//      —— 错的是**读法**（`docker logs` 走 journald/k8s-file 驱动，读不到 conmon 的 `ctr.log`）；
//   ② 生产慢查询清单**必须先按 `dbid` 拆库**（本实例混库；`pg_stat_statements` 1.11
//      只有 `dbid`、没有 `database` 列）⇒ 不拆会把 smm 的 `daily_kline` 混进本库清单；
//   ③ **两个独立量具交叉互证**（`pg_stat_statements` + `ctr.log` 逐块解析）家族排序吻合；
//   ④ `refreshIndexSQL` 的成本根因是**冷分区存了 5 分钟粒度的全历史**
//      （必须全扫 434,658 行才能算每组的 `MAX(bucket)`）⇒ 裁剪到「每组最新一行」
//      语义对账全绿（1,079 行，双向差集 0/0）。
//
// # 为什么「文档也要有门」
//
// ① **「日志读不到」是本仓反复踩的坑**。§143.17.5 写「252 上没有可用的 PG 查询日志」，
//    另一会话写「pg-252-pg17 SQL 日志全量解析」确实拿到了日志 —— 两边其实都对，
//    差别只在**读法**。⇒ 这道门钉住「真实通道路径」与「外抄 cron」在场，
//    防止下一个人再写出「日志是空的」。
//
// ② **混库不拆库 ⇒ 读数直接错**。本轮第一版查询没拆库，
//    `SELECT source, MAX(date) FROM daily_kline GROUP BY source`（**smm 股票库**的表）
//    进了本库 top-6。⇒ 这道门钉住「按 `dbid` 拆分」这件事本身，
//    而不只是钉住某个数字。
//
// ③ **跨节订正必须就地留横幅**（本仓纪律：错误结论就地加横幅 + 保留原文）。
//    §143.17 有三处要改（§143.17.0 第 4 条 / §143.17.5 / §143.17.6-C1），
//    ⇒ 这道门钉住**三处横幅都在场**，且**原文不得删**。
//
// ④ **有一条「不订正」也要钉住**：§143.17.4 的「命名形态 ⇒ 新版从未执行」**结论是对的**
//    （本会话前半段曾误判它被证伪 ⇒ 那是无效推理：父声明名在两版里**同名**）。
//    ⇒ 门钉住「§143.19 明确写了不订正 + 给了 DDL 原文证据」，
//    防止后人把「订正」做成「反向的错订正」。
//
// # 这道门测什么
//
// 门断言的是**文档结构**（小节在场、关键量在、关键结论在、订正横幅在、原文还在），
// 不连库、不跑 SQL ⇒ CI 与开发机都能跑。
// 判据的**语义**正确性由 §143.19 记录的生产实测背书（脚本见 §143.19.8）。

import (
	"os"
	"strings"
	"testing"
)

const runbook14319 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14319 取出 §143.19 这一节的正文。
//
// ★ 与 §143.18 同款纪律：必须**按节切**再断言。本 runbook 有 35,000+ 行，
// 同一个量（例如 "351 条"、"12.36 小时"、"434,658"）在别的节里也可能出现
// ⇒ 整篇断言等于**断言不到东西**（判据钉不住要钉的那一段）。
// ⇒ 判据的单位必须与被测对象的单位一致。
func section14319(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14319)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.19 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.19 的小节标题：§143.19 的结论整节丢了")
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

// section14317 取 §143.17 这一节的正文。
//
// ★ 复用 `session_turns_partition_family_test.go` 里已有的 `section14317`
// （同款实现、同款纪律：按节切再断言）—— 本文件**不重复定义**它
// （Go 的同包重复声明会编译失败；且两份实现漂移本身就是风险）。
func section14317Section(t *testing.T) string {
	t.Helper()
	return section14317(t)
}

// mustContain14319 断言 §143.19 正文里至少含 needle 之一。
func mustContain14319(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.19 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// mustNotContain14319 断言 §143.19 正文里**不含** needle。
func mustNotContain14319(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(section, n) {
			t.Fatalf("§143.19 正文里出现了不该出现的串 %q —— 可能是把错误结论抄进来了", n)
		}
	}
}

// mustNotAssert14319 是**行级负控**：断言 §143.19 正文里没有「断言某个错误结论」的行。
//
// ★ 为什么不能直接用 `mustNotContain`（本轮连踩两次）：
//
//	当正文**合法地否定**一个错误推论时，措辞必然包含那个被否掉的短语
//	（例：「**不能**当成『872 变慢了』」）⇒ 纯子串匹配会**误伤否定语境**。
//
// ⇒ 这里改成：逐行扫，命中 needle 的行若**同时**含否定/引用标记，则**放行**；
//
//	否则判定为「把错误结论当结论写了」⇒ 失败。
//
// 否定/引用标记表：
//
//	否定 = 不能 / 不成立 / 不可 / 没有 / 并非 / 错 / 被否 / 不该 / 不是 / 而非 / 误
//	引用 = 『 』 " 「 」
func mustNotAssert14319(t *testing.T, section string, needles []string) {
	t.Helper()
	negMarkers := []string{
		"不能", "不成立", "不可", "没有", "并非", "被否", "不该", "不是",
		"而非", "误判", "无效推理", "错",
	}
	quoteMarkers := []string{"『", "』", "「", "」", `"`}

	for _, ln := range strings.Split(section, "\n") {
		for _, n := range needles {
			if !strings.Contains(ln, n) {
				continue
			}
			hasNeg := false
			for _, m := range negMarkers {
				if strings.Contains(ln, m) {
					hasNeg = true
					break
				}
			}
			hasQuote := false
			for _, m := range quoteMarkers {
				if strings.Contains(ln, m) {
					hasQuote = true
					break
				}
			}
			if hasNeg || hasQuote {
				// 否定/引用语境 ⇒ 合法的引用被否掉的推论 ⇒ 放行。
				continue
			}
			t.Fatalf("§143.19 有一行把 %q 当成了结论（无否定/引用标记）：%s", n, strings.TrimSpace(ln))
		}
	}
}

// TestRunbook14319LogChannelTopologyIsReal 钉住「252 的 PG 日志通道从未断过」这条订正。
//
// ★ 为什么要钉「路径」而不只是钉「有日志」：
// 订正的**根因**是**读法错**（`docker logs` 走 journald/k8s-file 驱动，
// 读不到 conmon 落盘的 `ctr.log`）⇒ 只写「有日志」的话，下一个人
// 还会用 `docker logs` 去读，然后再次得出「日志是空的」。
// ⇒ 必须钉住：conmon 落盘路径 + `overlay-containers` + `userdata/ctr.log`。
func TestRunbook14319LogChannelTopologyIsReal(t *testing.T) {
	s := section14319(t)

	// 真实通道的三个要素：conmon 落盘 / overlay-containers 路径 / ctr.log 文件名。
	mustContain14319(t, s,
		"conmon",
		"overlay-containers",
		"userdata/ctr.log",
	)

	// 轮转 + 小时级外抄 + 保留期（这是「通道在产」的三个独立证据）。
	mustContain14319(t, s,
		"ctr.log-20261020",
		"pg17-ctrlog-snapshot",
		"pg17-ctrlog-archive",
		"30 天保留",
	)

	// ★ 订正的**本质**必须写出来：「读不到」≠「没有」。
	//   这条是本节最贵的教训（§143.17.5 就是把它读反了）。
	mustContain14319(t, s,
		"「读不到」",
		"docker logs",
	)

	// 磁盘水位（避免下一个人以为日志快把盘写满了）。
	mustContain14319(t, s,
		"可用 70 G",
	)
}

// TestRunbook14319SlowQueryListIsSplitByDB 钉住「按 dbid 拆库」这条纪律。
//
// ★ 为什么要钉「拆库」而不只是钉某个数字：
// 本实例混库（llm_gateway / smm / postgres / pocket / redclaw / kxmemory / memora…），
// `pg_stat_statements` 1.11 只有 `dbid`（没有 `database` 列）⇒
// 不拆库就会把 smm 的 `daily_kline` 混进本库清单。
// ⇒ 门钉住：dbid 口径 + 混库事实 + 「本库清单」的读数 + smm 被排除的证据。
func TestRunbook14319SlowQueryListIsSplitByDB(t *testing.T) {
	s := section14319(t)

	// 拆库这件事本身（口径，不是结果）。
	mustContain14319(t, s,
		"dbid",
		"混库",
	)

	// 本库清单的关键读数（2026-10-20 实测）。
	mustContain14319(t, s,
		"351",   // 慢语句条数
		"7,414", // 总调用次数
		"12.36", // 总 DB 时间（小时）
	)

	// ★ smm 的表必须**显式排除**（这是本轮踩过的坑）：
	//   `SELECT source, MAX(date) FROM daily_kline GROUP BY source` 是 smm 股票库的表。
	mustContain14319(t, s,
		"daily_kline",
		"smm",
	)

	// 集中度必须给「累计占比」而不是只给条数（否则「治理面很窄」这个结论没有依据）。
	mustContain14319(t, s,
		"72.0%",
	)
}

// TestRunbook14319RefreshIndexSQLRootCause 钉住「成本根因 = 读多少行」这条结论。
//
// ★ 为什么要钉「根因」而不只是钉「某个数」：
// 本节最容易被后人误读成「872 没生效 ⇒ 再转一次列存」或
// 「该给这个查询换个索引」—— 而这两个方向都已被生产实测封死：
//
//	① 872 已完全生效（compliant=t / stripe 放大已除）；
//	② `DISTINCT ON` 走索引实测 7,412 ms（比现形态慢 8.4 倍）⇒ 换形态是死路。
//
// ⇒ 门钉住：全扫行数 / 同口径 A/B / DISTINCT ON 的实测数 / 裁剪后的语义对账。
func TestRunbook14319RefreshIndexSQLRootCause(t *testing.T) {
	s := section14319(t)

	// 根因：必须全扫才能算每组的 MAX(bucket)。
	mustContain14319(t, s,
		"434,658",
		"MAX(bucket)",
	)

	// 同口径 A/B（只改扫描范围）——这是「行数是成本」的唯一硬证据。
	mustContain14319(t, s,
		"880.963",
		"155.358",
	)

	// ★ 裁剪方案的安全边界：语义对账必须全绿（双向差集 0/0）。
	mustContain14319(t, s,
		"only_now = 0",
		"only_trim = 0",
	)

	// ★ 缺陷本体：TTL 清理对冷族是空文（前提漂移：317 是 columnar，872 转成 heap 了）。
	mustContain14319(t, s,
		"cleanup_old_credential_model_index",
		"前提漂移",
	)

	// ★ 「DISTINCT ON 这条路被封死」的实测数（防止有人再提换形态的方案）。
	mustContain14319(t, s,
		"161,495.853",
		"7,412.081",
	)

	// ★ 归档不是替代品：twoMonthsAgo ⇒ 09 月分区 11-03 才归档 ⇒ 不是缺陷。
	mustContain14319(t, s,
		"twoMonthsAgo",
	)
}

// TestRunbook14319CorrectionBannersIn14317 钉住 §143.17 的三处就地订正横幅。
//
// ★ 纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）⇒
// 这道门同时钉住两件事：横幅**在场**，且**原文还在**。
func TestRunbook14319CorrectionBannersIn14317(t *testing.T) {
	s := section14317Section(t)

	// 横幅必须在场（三处：§143.17.0 第 4 条 / §143.17.5 / §143.17.6-C1）。
	if n := strings.Count(s, "订正横幅（2026-10-20，§143.19.1.1"); n < 3 {
		t.Fatalf("§143.17 里只找到 %d 处 §143.19.1.1 的订正横幅，期望 ≥3 处"+
			"（§143.17.0 第 4 条 / §143.17.5 / §143.17.6-C1）", n)
	}

	// ★ 原文必须还在（就地订正 ≠ 静默改写）。
	//   这两条正是被证伪的判词，删掉它们就等于掩盖了历史判断。
	mustContain14319(t, s,
		"252 上没有可用的 PG 查询日志",
		"252 的 PG 查询日志目前是**空的**",
		"打开 252 的 PG 日志",
	)

	// ★ 订正必须给出**正确读法**，不只是说「之前错了」。
	mustContain14319(t, s,
		"overlay-containers",
		"userdata/ctr.log",
		"pg17-ctrlog-snapshot",
	)

	// ★ C1 的动作项订正必须在场（`logging_collector=on` 会重复落盘 ⇒ 撤销）。
	mustContain14319(t, s,
		"logging_collector=on",
		"重复落盘",
	)
}

// TestRunbook14319DoesNotOverCorrect877 钉住「§143.17.4 的 877 判词**不订正**」。
//
// ★ 这是本节最容易被后人「好心办坏事」的地方：
// 本会话前半段曾误判「§143.17.4 的『命名形态 ⇒ 新版从未执行』被证伪」，
// 而那个判断**本身是无效推理**（父声明名在两版里**同名** ⇒ 无判别力）。
// ⇒ 若 §143.19 缺少这条「不订正 + 为什么」，下一个人会照着错误结论去改台账。
func TestRunbook14319DoesNotOverCorrect877(t *testing.T) {
	s := section14319(t)

	// 「不订正」这件事必须明写。
	mustContain14319(t, s,
		"不订正",
	)

	// ★ 判别力排序必须是「DDL 原文 > 产物对象名 > 父声明名」。
	mustContain14319(t, s,
		"DDL 原文",
		"叶实例名",
		"父声明名",
	)

	// ★ 无效推理的形态必须被点名（否则「自订正」会变成一句情绪话）。
	mustContain14319(t, s,
		"两版同名",
	)

	// 生产证据：DDL 原文里没有 `ON ONLY`（= 旧版形态），新版 stage 1/3 都不在统计里。
	mustContain14319(t, s,
		"ON ONLY",
		"127,274.8",
	)

	// ★ 不得出现「877 的前提被证伪 / 新版执行过」这类**断言性**表述。
	//
	// ⚠ 判据设计的坑（本轮连踩两次）：负控若写成纯子串匹配，
	// 它会把**合法的否定引用**也判红 —— 而 §143.19.5 的自订正表**必须**
	// 引用「我自己的『877 前提被证伪』判断」这句话（否则自订正就成了没有对象的空话）。
	// ⇒ 负控必须走**行级**判定（见 mustNotAssert14319 的说明）：
	//    命中短语的行若带否定/引用标记 ⇒ 合法引用；否则 ⇒ 把错误结论当结论写了。
	mustNotAssert14319(t, s, []string{
		"新版执行过",
		"新版已执行",
		"877 的立项前提被生产证伪",
	})
}

// TestRunbook14319HonestBoundaries 钉住本节的诚实边界。
//
// ★ 为什么这类门也要有：§143.19 里有几个**容易被过度解读**的数字
// （例如「均值没降 ⇒ 872 失败」其实是因为窗口不可比 + 噪声大于均值）。
// ⇒ 必须钉住「本节写入 = 0」「窗口不可比」「执行需授权」这三句。
func TestRunbook14319HonestBoundaries(t *testing.T) {
	s := section14319(t)

	mustContain14319(t, s,
		"本节对生产的写入 = 0",
		"窗口不可比",
		"stddev",
		"需属主授权",
	)

	// ★ 不得出现把 872 判定为「变慢/失败」的**断言性**表述。
	//
	// ⚠ 同款坑（第二踩）：正文里合法地写着「**不能**当成『872 变慢了』」，
	// 那是本节明确否掉的推论 ⇒ 负控必须匹配**断言句式**，不能匹配被否掉短语的任意出现。
	// ⇒ 做法：按**行**扫，只在「该行不含否定/引用标记」时才算违规。
	//    否定标记 = 不能 / 不成立 / 不可 / 没有 / 并非 / 错 / 被否 / 不该
	//    引用标记 = 『 』 " 「 」 ⇒
	mustNotAssert14319(t, s, []string{"872 变慢", "872 失败"})
}
