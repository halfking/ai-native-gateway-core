package ursm_test

// §143.21 的文档门：`credential_model_index` **冷族的真实形态**、**A2 的重新裁决**、
// **archive 路径零产出**、**Go 侧 TTL 配置空转**、**211 侧同形态 + 台账缺口更广**，
// 以及**「08 月数据丢失」这个阴性结论的复核**。
//
// 背景（runbook §143.21）：
// §143.19 留下 A1（裁剪冷月分区，每组只留 `MAX(bucket)` 一行）与
// A2（修 `cleanup_old_credential_model_index()`，让 7 天 TTL 在冷族上生效），
// 分别标成语义风险「中」与「低」，并称两者「互补」；
// §143.20 补了 211 这个独立实例。本节把 A1/A2 从「量已量」推进到「形态已查清、风险已重定级」。
//
// # 为什么这些结论必须有门
//
// ① ★★★★ **A1/A2 的语义风险被低估了一档**。冷族不是「重复桶」，
//    而是 promote（`DefaultRetentionWindow = 8 * time.Hour`）逐点搬进来的
//    **5 分钟粒度凭据健康度历史快照**（252 09 月 1,018 组 × 平均 295.9 个 bucket，
//    单组最多 3,635；211 10 月 40,524 行 / 702 组）
//    ⇒ 「每组只留最新一行」= **永久销毁历史曲线**
//    ⇒ 必须钉住「A1 的风险是**高**」，否则下一个人会照着「中」就动手删数据。
//
// ② ★★★★ **A2 与 654 的 archive 抢同一批数据**。654 的语义是
//    「INSERT 进 archive 的 columnar 月分区 → DETACH → DROP 源分区」
//    ⇒ archive 表是**真正的历史存储**
//    ⇒ A2 若直接 DELETE，archive 就再也搬不到那些行 ⇒ **历史被静默丢弃**。
//    ⇒ 门钉住「先搬后删」这个正确形态，否则 A2 会被当成「低风险」直接落地。
//
// ③ ★★★ **archive 几乎零产出**（252 的 archive 表 0 行 / 只有 1 个 32 kB 空分区；
//    `archive_credential_model_index` 在 `pg_stat_statements` 里 0 次调用；
//    211 的 archive 0 行 / 0 分区）⇒ 「冷族天然保有 ~2 个月」其实是
//    **「archive 没跑」的巧合**，不是设计在起作用。
//
// ④ ★★★ **Go 侧 `lifecycle.credential_model_index_ttl_days` 是空转配置**
//    （Go 只把它写进日志，SQL 函数把 `cutoff_ts` 写死 `INTERVAL '7 days'`）
//    ⇒ 运维改设置对生产零效果 ⇒ 门钉住这个事实 + 修法里的关键陷阱
//    （**必须 DROP 旧重载**，否则无参调用会静默解析到旧函数）。
//
// ⑤ ★★ **「08 月数据丢失」是假命题**（阴性结论）—— 211 上 `2026_07`/`2026_08`
//    分区**存在且为 0 行** ⇒ 那些月本来就是空分区。
//    ⇒ 门钉住「复核过」这件事，防止有人把「归档表 0 行」当成「归档失败/数据丢失」再报一轮。

import (
	"os"
	"strings"
	"testing"
)

const runbook14321 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14321 取出 §143.21 这一节的正文。
//
// ★ 同款纪律（§143.17~§143.20 一致）：必须**按节切**再断言
// —— 本 runbook 已 36,000+ 行，同一个量会出现十几次 ⇒ 整篇断言等于断言不到东西。
func section14321(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14321)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.21 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.21 的小节标题：§143.21 的结论整节丢了")
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

// mustContain14321 断言 §143.21 正文里至少含 needle 之一。
func mustContain14321(t *testing.T, section string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(section, n) {
			t.Fatalf("§143.21 正文里缺关键读数 %q —— 判据要求的关键证据不见了", n)
		}
	}
}

// mustNotAssert14321 是**行级负控**：断言 §143.21 正文里没有「断言某个错误结论」的行。
//
// ★ 为什么不能纯子串匹配（§143.19 已踩过两次）：正文**合法地否定**一个错误推论时，
// 措辞必然包含那个被否掉的短语 ⇒ 纯子串会误伤否定语境。
// ⇒ 命中 needle 的行若**同时**含否定/引用标记则放行；否则判为「把错误结论当结论写了」。
func mustNotAssert14321(t *testing.T, section string, needles []string) {
	t.Helper()
	negMarkers := []string{
		"不能", "不成立", "不可", "没有", "并非", "被否", "不该", "不是",
		"而非", "误判", "无效推理", "错", "证伪", "无收益", "死路", "假命题",
		"上调", "订正", "冲突", "改写", "零产出", "空转",
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
				continue
			}
			t.Fatalf("§143.21 有一行把 %q 当成了结论（无否定/引用标记）：%s", n, strings.TrimSpace(ln))
		}
	}
}

// TestRunbook14321ColdFamilyIsHealthTimeSeries 钉住「冷族是健康度历史快照，不是冗余桶」。
//
// ★ 这是本节**风险重定级**的依据：若把它当成重复桶，A1 的风险会被低估一档。
func TestRunbook14321ColdFamilyIsHealthTimeSeries(t *testing.T) {
	s := section14321(t)

	// 形态：组数 × 每组 bucket 数（两个实例各一组读数）。
	mustContain14321(t, s,
		"301,255",     // 252 09 月总行数
		"1,018",       // 252 09 月 distinct 组数
		"295.9",       // 每组平均 bucket 数
		"3,635",       // 单组最多 bucket 数
		"40,524",      // 211 10 月总行数
		"702",         // 211 10 月 distinct 组数
		"DefaultRetentionWindow = 8 * time.Hour", // promote 窗口（机制）
		"8.4 h",       // 实测 hot 跨度，与 8h 窗口吻合
	)

	// 结论：这批行是历史曲线 ⇒ A1 的风险被上调。
	mustContain14321(t, s,
		"健康度历史快照",
		"永久销毁",
		"风险",
	)

	// 负控：不得出现「冷族是冗余/可以随手丢」这类断言句。
	mustNotAssert14321(t, s, []string{
		"零价值",
		"冗余桶",
		"可以丢",
	})
}

// TestRunbook14321A2IsReclassifiedAgainstArchive 钉住 A2 的重新裁决。
//
// ★ 核心：**A2 与 654 的 archive 抢同一批数据** ⇒ 「直接 DELETE」会静默丢历史。
func TestRunbook14321A2IsReclassifiedAgainstArchive(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"654",              // 冲突的来源
		"DETACH",           // archive 的三步：搬 → DETACH → DROP
		"先搬进 archive 再删", // A2 的正确形态
		"静默丢弃",          // 直接 DELETE 的后果
	)

	// A1/A2 的关系被改写为「同一决策的两个参数」（原 §143.19.7 说「互补」）。
	mustContain14321(t, s,
		"不是「互补」",
		"保留期",
	)
}

// TestRunbook14321ArchivePathHasZeroOutput 钉住「archive 几乎零产出」。
//
// ★ 为什么必须钉：若 archive 其实在正常跑，本节的「冷族靠巧合留存」判词就不成立。
func TestRunbook14321ArchivePathHasZeroOutput(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"credential_model_index_archive",
		"0 行",
		"193 次",  // promote 的调用次数（对照：archive 是 0 次）
		"18.2 s",
	)

	// 负控：不得把「archive 0 行」当成「归档失败导致数据丢失」的断言。
	mustNotAssert14321(t, s, []string{
		"归档失败导致数据丢失",
		"数据丢失了",
	})
}

// TestRunbook14321GoTTLSettingIsInert 钉住「Go 侧 TTL 配置空转」+ 修法里的陷阱。
func TestRunbook14321GoTTLSettingIsInert(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"lifecycle.credential_model_index_ttl_days",
		"INTERVAL '7 days'",
		"零效果",
		"DROP", // 修法陷阱：必须 DROP 旧重载
	)
}

// TestRunbook14321TwoElevenSameDefectAndWiderLedgerGap 钉住 211 侧同形态 + 台账缺口更广。
func TestRunbook14321TwoElevenSameDefectAndWiderLedgerGap(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"逐字相同", // 211 的 cleanup 函数定义与 252 相同
		"317",
		"653",
		"654",
		"688",
		"726",
		"872",
		"大面积失真",
	)
}

// TestRunbook14321NegativeConclusionRechecked 钉住「08 月数据丢失 = 假命题」。
//
// ★ 阴性结论也要有门：不钉的话，下一个人会把「归档表 0 行」当成「数据丢失」再报一轮。
func TestRunbook14321NegativeConclusionRechecked(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"假命题",
		"2026_07", // 211 上存在的空分区（反证）
		"2026_08",
		"0 行",
	)

	// §143.21.6 必须有这一节（复核的载体）。
	if !strings.Contains(s, "§143.21.6") {
		t.Fatalf("§143.21 少了 §143.21.6（阴性结论复核）—— 复核过程不可查")
	}
}

// TestRunbook14321BannerThreeCorrects14319 钉住 §143.19.7 的「订正横幅三」+ **原文保留**。
//
// ★ 本仓纪律：错误结论**就地**加横幅 + **保留原文**（不静默改写）。
// ⇒ 这道门同时钉住两件事：横幅**在场**，且**原文还在**。
func TestRunbook14321BannerThreeCorrects14319(t *testing.T) {
	s14319 := section14319(t)

	if n := strings.Count(s14319, "订正横幅三（2026-10-20，§143.21.2 / §143.21.3 就地订正"); n != 1 {
		t.Fatalf("§143.19 里找到 %d 处「§143.21 的订正横幅三」，期望恰好 1 处", n)
	}

	// 横幅必须点名被订正的那四点。
	for _, n := range []string{
		"「中」→「高」",
		"「低」→「中-高」",
		"不是「互补」",
		"设计而非缺陷",
	} {
		if !strings.Contains(s14319, n) {
			t.Fatalf("订正横幅三里没有点名要订正的 %q —— 被订正的结论没被点名，后人读不到改了什么", n)
		}
	}

	// ★ 原文必须保留（§143.19.7-A1 / A2 的原始行仍在）。
	for _, n := range []string{
		"裁剪 `credential_model_index` 冷月分区：每组只留 `MAX(bucket)` 一行",
		"修 `cleanup_old_credential_model_index()`：872 之后冷月分区已是 heap，TTL 可用",
		"**这条与 A1 互补**",
	} {
		if !strings.Contains(s14319, n) {
			t.Fatalf("§143.19.7 的原文 %q 不在了 —— 本仓纪律要求订正时**保留原文**（只加横幅，不静默改写）", n)
		}
	}
}

// TestRunbook14321HonestBoundaries 钉住诚实边界：本节仍为 0 生产写，且不擅自落地裁剪。
func TestRunbook14321HonestBoundaries(t *testing.T) {
	s := section14321(t)

	mustContain14321(t, s,
		"对生产的写入 = 0",
		"DELETE",
		"TRUNCATE",
		"DROP",
		"REINDEX",
		"ANALYZE",
	)

	// A1′/A2′ 仍需属主授权 ⇒ 必须写明「生产写授权」。
	// ★ needle 取「生产写授权」而不是「需生产写授权」：正文写的是「**都需要生产写授权**」，
	// 逐字匹配「需生产写授权」会因一个字的差异而误判正文缺失（§143.21 写门时踩过）。
	mustContain14321(t, s, "生产写授权")
}
