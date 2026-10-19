//go:build !integration

package quality

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 本文件钉住 §143.15 的结论：`provider_metrics_minute` 的写侧排序**没有收益**。
//
// 背景（runbook §143.15，docs/06-deployment/04-runbooks/runbooks/
// 2026-10-04-ursm-write-amplification-measurement.md）：
// §143.12 曾判定「加 ORDER BY provider_id, model_name, bucket 即可让 idx_pmm_model_bucket 变密」，
// §143.15 用本地沙箱（PostgreSQL 17.11 + 生产真实数据 + 生产原样索引定义）把它证伪了：
//   - V5（分钟内按键序插入）与 V1（哈希序插入）**逐字节相同** ⇒ 收益 0；
//   - V3（REINDEX 重建）⇒ 填充率 50.68% → 90.17%，而 V7（同样增量长出来的 ASC 索引）仍是 52%
//     ⇒ 真正的成因是「增量增长的 btree 留下冻结的半满页」，**与索引方向无关**。
//
// ⇒ 所以这里钉的是「不要加 ORDER BY」，以及支撑该结论的两个前提。
var (
	// WITHIN GROUP (ORDER BY ...) 是聚合函数内部的排序，与顶层 INSERT..SELECT 的排序无关。
	// §143.14 的教训：判据在原始文本里搜模式会把「别的东西」也算成违规 ⇒ 必须先剥掉。
	withinGroupOrderByRe = regexp.MustCompile(`(?is)WITHIN\s+GROUP\s*\(\s*ORDER\s+BY[^)]*\)`)
	// 分钟窗口的左界：date_trunc('minute', NOW() - INTERVAL '<n> <unit>')
	minuteWindowRe = regexp.MustCompile(`(?is)ts\s*>=\s*date_trunc\(\s*'minute'\s*,\s*NOW\(\)\s*-\s*INTERVAL\s*'(\d+)\s*([a-z]+)'\s*\)`)
	// 分钟窗口的右界：ts < date_trunc('minute', NOW())
	minuteUpperBoundRe = regexp.MustCompile(`(?is)ts\s*<\s*date_trunc\(\s*'minute'\s*,\s*NOW\(\)\s*\)`)
)

// TestMinuteAggregatorHasNoOrderBy：分钟聚合 SQL 不得带顶层 ORDER BY。
//
// 为什么这不是「把性能优化堵死」：§143.15 实测该改动的收益为 0（沙箱读数逐字节相同），
// 因为①批内每个 (provider_id, model_name) 至多出现一次 ⇒ 批内键集合与顺序无关；
// ②同一组内 bucket 随时间升序，这个方向由**时钟**决定，写侧排序改不动；
// ③即使能改成全局键序（V6），DESC 那条索引也仍然只有 52%（只有 REINDEX 能到 90%）。
func TestMinuteAggregatorHasNoOrderBy(t *testing.T) {
	stripped := withinGroupOrderByRe.ReplaceAllString(minuteMetricsSQL, "WITHIN_GROUP_STRIPPED")
	if idx := strings.Index(strings.ToUpper(stripped), "ORDER BY"); idx >= 0 {
		t.Errorf("分钟聚合 SQL 出现了顶层 ORDER BY（偏移 %d）：§143.15 实测该改动收益为 0 —— "+
			"批内键集合与顺序无关，组内时间方向由时钟决定，连全局键序都救不了 DESC 索引（沙箱 V5/V6）。\n"+
			"若要推翻这个结论，先重跑 runbook §143.15 的沙箱对照（PG17 + 生产数据 + 生产索引定义），"+
			"再回来改这条门；不要顺手加 ORDER BY。\n片段：%s",
			idx, snippet(stripped, idx))
	}
}

// TestMinuteAggregatorBatchIsOneMinute：钉住「一次跑只处理 1 个分钟桶」。
//
// 它是上面那条推理的基石：批 = 单个分钟桶 ⇒ 批内每个 (provider_id, model_name) 至多一行
// ⇒ 批内键集合与批内顺序无关 ⇒ 写侧排序对索引落位没有影响。
// 若窗口被改宽（例如 10 分钟），这条推理的前提就不成立，必须重新评估。
func TestMinuteAggregatorBatchIsOneMinute(t *testing.T) {
	m := minuteWindowRe.FindStringSubmatch(minuteMetricsSQL)
	if m == nil {
		t.Fatalf("分钟聚合 SQL 的窗口左界形状变了（找不到 ts >= date_trunc('minute', NOW() - INTERVAL '...')）：%s",
			snippet(minuteMetricsSQL, strings.Index(minuteMetricsSQL, "WHERE")))
	}
	if m[1] != "1" || !strings.HasPrefix(strings.ToLower(m[2]), "minute") {
		t.Errorf("分钟聚合窗口不再是 1 分钟（实测 %s %s）：§143.15 的结论（写侧排序无收益）建立在"+
			"「一次跑只处理 1 个分钟桶」这个前提上；窗口改宽后批内可能出现同一 (provider_id, model_name) 多行，"+
			"届时期内顺序才会影响索引落位，必须重跑 runbook §143.15 的沙箱对照后重判。",
			m[1], m[2])
	}
	if !minuteUpperBoundRe.MatchString(minuteMetricsSQL) {
		t.Error("分钟聚合 SQL 缺少窗口右界 ts < date_trunc('minute', NOW())：窗口不再是一分钟闭区间")
	}
}

// TestRunbook14312OrderByClaimCarriesCorrection：§143.12 那三处「加 ORDER BY 即可」
// 旁边必须带订正横幅。§143.15 已把该结论证伪，但原文按纪律保留 ⇒ 必须同时保留横幅，
// 否则下一个读 runbook 的人会把被推翻的结论当成结论。
func TestRunbook14312OrderByClaimCarriesCorrection(t *testing.T) {
	const runbook = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	raw, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")

	anchors := []struct {
		what string
		mark string
	}{
		{"§143.12 分族表里的 provider_metrics_minute 行", "internal/quality/minute_aggregator.go` 的 `INSERT..SELECT..GROUP BY`"},
		{"§143.12 「待评估的 1 个」那句", "需确认 ORDER BY 的代价与收益"},
		{"§143.12 分族清单里的「4 | 142MB | ~48MB」那行", "批量 `INSERT..SELECT`"},
		{"§143.12 下一轮候选 ①", "唯一现成的第二个可治项"},
	}
	for _, a := range anchors {
		idx := -1
		for i, ln := range lines {
			if strings.Contains(ln, a.mark) {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Errorf("runbook 里找不到 %s（锚点 %q）：§143.12 的结构变了，请同步更新这条门", a.what, a.mark)
			continue
		}
		if !hasCorrectionBanner(lines, idx) {
			t.Errorf("runbook §143.12 的 %s 旁边没有 §143.15 的订正横幅：该结论已被 §143.15 证伪"+
				"（写侧 ORDER BY 收益 = 0），原文按纪律保留 ⇒ 必须同时保留横幅，否则会被当成结论继续读。",
				a.what)
		}
	}
}

// hasCorrectionBanner：从第 idx 行往下最多 12 行内找同时含「订正」与「§143.15」的横幅行。
func hasCorrectionBanner(lines []string, idx int) bool {
	end := idx + 12
	if end > len(lines) {
		end = len(lines)
	}
	for i := idx; i < end; i++ {
		if strings.Contains(lines[i], "订正") && strings.Contains(lines[i], "§143.15") {
			return true
		}
	}
	return false
}

// snippet：取 offset 附近的一小段文本，供失败信息展示。
func snippet(s string, offset int) string {
	const radius = 120
	if offset < 0 {
		return ""
	}
	start := offset - radius
	if start < 0 {
		start = 0
	}
	end := offset + radius
	if end > len(s) {
		end = len(s)
	}
	return strings.TrimSpace(s[start:end])
}
