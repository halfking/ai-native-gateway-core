package ursm_test

// 252 巡检脚本 `pg17-index-bloat.sh` 的**重建目标口径**门（2026-10-19，§143.12）
//
// 本门盯三件事，全部是 §143.12 实测发现后订正的**口径错误**：
//
//	① headroom 公式把「重建目标」当成 100%
//	   ⇒ 密度越接近 90%，高估越离谱：密度 90.07% 的索引旧口径报「可回收 3.8MB」，
//	     而排序建（REINDEX / CREATE INDEX）只能到 btree 的 fillfactor 默认 90%
//	     ⇒ 真值 ≈ 0。--fix 于是反复挑中重建不动的索引。
//	   ⇒ 新口径：reclaim = dead + leaf×(1 − min(density,90)/90)。
//	     校准依据是**受控实验**（两个沙箱对照，只改插入顺序）：
//	       随机 68.13% → 排序 89.88%，实测少 24.22%，公式给 24.3%；
//	       同首列 70.20% → 排序 89.82%，实测少 21.98%，公式给 22.0%。
//
//	② reason 文案把「已用占比」标成「空隙」
//	   ⇒ `×${density}%空隙` 读起来是「空隙 89.14%」，实际空隙是 100−89.14=10.86%
//	     ⇒ 照字面读会把回收量算反一个量级。
//
//	③ 自校准基线的键名永远匹配不上（**生产上是死的**）
//	   ⇒ $STATE 记的是重建时动过的索引名；分区叶索引叫
//	     `ursm_node_snapshot_min_20261019_pkey`，而基线文件里唯一那条真实数据点
//	     叫 `ursm_node_snapshot_min_pkey`（分区父表上的索引，relkind='I'，
//	     压根不在候选集里）⇒ 90.20% 这个**生产上唯一的实测可达密度**从来没被用上。
//	   ⇒ 新口径：基线按**族键**（叶分区索引 → 同形状的父表索引）继承。
//
// ★ 手法沿用 `index_bloat_candidate_coverage_test.go` 的范式：假 podman 记录
//   脚本**实际下发**的 SQL 并按查询类型返回固定结果 ⇒ 断言的是行为，不是文本。
//   不连真库 ⇒ CI 与开发机都能跑。
//
// ⚠ 判据的公式由脚本自带的 `pg17-index-bloat-selftest.sql` 夹具负责，本门不重复；
//   本门钉的是「**目标口径**」与「**族键继承**」两件此前没有门的事。

import (
	"os"
	"strings"
	"testing"
)

// bloatHeadroomMB 独立复算期望值（不复用脚本里的 awk 逻辑，否则测的是同一份实现）。
// 口径：reclaim_pages = dead + leaf×(1 − min(density,90)/90)，再换算成 MB。
func bloatHeadroomMB(dead, leaf int, density float64) int {
	const fillfactor = 90.0
	y := density
	if y > fillfactor {
		y = fillfactor
	}
	pages := float64(dead) + float64(leaf)*(1-y/fillfactor)
	return int(pages * 8192 / 1048576)
}

// TestBloatHeadroomCeilingIsFillfactorNotFullDensity 钉住①：重建目标是 fillfactor(90)。
//
// 这条是**行为级**的：同一组输入，旧口径判 CANDIDATE、新口径判 ok。
// 选参数：leaf=60000、密度 90.05%
//   旧口径（到 100%）：60000×(1−0.9005)×8KB ≈ 46.7MB ≥ 32MB 门槛 ⇒ CANDIDATE
//   新口径（到 90%）  ：min(90.05,90)=90 ⇒ 0MB ⇒ ok
func TestBloatHeadroomCeilingIsFillfactorNotFullDensity(t *testing.T) {
	e := newBloatEnv(t)
	log, _ := e.run(t, 0, 60000, "90.05")

	if strings.Contains(log, "CANDIDATE") {
		t.Errorf("密度 90.05%% 的索引被判成候选 ⇒ 重建目标被当成了 100%%（应封顶在 fillfactor 90%%）\n脚本日志：\n%s", log)
	}
	if !strings.Contains(log, "headroom≈0MB") {
		t.Errorf("封顶后该索引的可回收量应恒为 0MB（日志里应出现 headroom≈0MB）\n脚本日志：\n%s", log)
	}
}

// TestBloatHeadroomMatchesSortedRebuildModel 钉住①的**数值**：
// 「页被摊薄」类的可回收量必须等于「排序建」的模型值。
//
// 取生产实测的一对真值（2026-10-19 pgstatindex 全字段）：
//   stats_event_dedup_pkey: leaf=13644、dead=0、密度 70.11%
//   旧口径给 31MB（= 填到 100%），新口径给 23MB（= 填到 90%）
//   受控实验交叉验证：70.20% → 排序建 89.82% ⇒ 体积少 21.98%
//   （新口径对同一密度给 22.0% ⇒ 吻合）
func TestBloatHeadroomMatchesSortedRebuildModel(t *testing.T) {
	e := newBloatEnv(t)
	log, _ := e.run(t, 0, 13644, "70.11")

	want := bloatHeadroomMB(0, 13644, 70.11)
	if want != 23 {
		t.Fatalf("测试前提不成立：独立复算期望 %dMB，与本门写死的 23MB 不符（口径变了？）", want)
	}
	if !strings.Contains(log, "headroom≈23MB") {
		t.Errorf("可回收量应按「排序建到 90%%」算成 23MB（独立复算同值）\n脚本日志：\n%s", log)
	}
	if strings.Contains(log, "headroom≈31MB") {
		t.Errorf("日志里出现旧口径的 31MB（填到 100%%）⇒ 公式没改成封顶口径\n脚本日志：\n%s", log)
	}
}

// TestBloatDeadPagesTermStillAdditive 钉住「死页项没有被新公式吃掉」。
//
// 新公式改的是**密度项**的目标，死页项必须照旧可加：
//   2026-10-19 生产真值：pg_toast_81522141_index  leaf=1、dead=29299、密度 0.54%
//   ⇒ 可回收 = 29299×8KB ≈ 228.9MB ⇒ 必须仍是候选（229MB 死 TOAST 索引是
//     §104 已定的主回收目标，不许被口径修正顺带降级成「ok」）。
func TestBloatDeadPagesTermStillAdditive(t *testing.T) {
	e := newBloatEnv(t)
	log, _ := e.run(t, 29299, 1, "0.54")

	want := bloatHeadroomMB(29299, 1, 0.54)
	if want != 228 {
		t.Fatalf("测试前提不成立：独立复算期望 %dMB", want)
	}
	if !strings.Contains(log, "CANDIDATE") {
		t.Errorf("229MB 死 TOAST 索引必须仍是候选（死页项没被新公式吃掉）\n脚本日志：\n%s", log)
	}
	if !strings.Contains(log, "headroom≈228MB") {
		t.Errorf("可回收量应仍为 228MB（死页项可加）\n脚本日志：\n%s", log)
	}
}

// TestBloatBaselineIsInheritedByFamilyKey 钉住③：基线按族键继承。
//
// 正例：$STATE 里是**父表索引**的族键（`ursm_node_snapshot_min_pkey 90.2`，
//       生产上唯一那条真实数据点），候选是**叶分区索引**且族键指向它
//       ⇒ 密度 89.94% 应被判为「已达自然填充率」（基线 90.2 − 容差 2 = 88.2）。
// 反例：族键指回自己（匹配不上基线）⇒ 基线不生效 ⇒ 不得出现「已达自然填充率」。
//
// ★ 反例不是「装饰」：把族键回退成「只用索引名找」的变异，会让正例红、反例绿，
//   只有两个都钉住才挡得住「把族键逻辑删掉」这种改法。
func TestBloatBaselineIsInheritedByFamilyKey(t *testing.T) {
	// 正例：族键 = 父表索引名
	e := newBloatEnv(t)
	if err := os.WriteFile(e.state, []byte("ursm_node_snapshot_min_pkey\t90.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.candidates = "ursm_node_snapshot_min_20261019_pkey|ursm_node_snapshot_min_pkey"
	log, _ := e.run(t, 0, 152043, "89.94")
	if !strings.Contains(log, "已达自然填充率") {
		t.Errorf("叶分区索引应继承父表索引的基线（90.2%% − 容差 2）⇒ 应判「已达自然填充率」\n脚本日志：\n%s", log)
	}
	if !strings.Contains(log, "ursm_node_snapshot_min_pkey") {
		t.Errorf("日志应说明用的是哪条基线（族键），否则运维看不懂为什么被跳过\n脚本日志：\n%s", log)
	}

	// 反例：族键指回自己 ⇒ 匹配不上基线 ⇒ 基线不生效
	e2 := newBloatEnv(t)
	if err := os.WriteFile(e2.state, []byte("ursm_node_snapshot_min_pkey\t90.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2.candidates = "ursm_node_snapshot_min_20261019_pkey|ursm_node_snapshot_min_20261019_pkey"
	log2, _ := e2.run(t, 0, 152043, "89.94")
	if strings.Contains(log2, "已达自然填充率") {
		t.Errorf("基线键不匹配时不得套用基线（否则自校准会对不相关的索引生效）\n脚本日志：\n%s", log2)
	}
}

// TestBloatReasonDoesNotLabelUsedPercentAsVacant 钉住②：文案不许把「已用」写成「空隙」。
func TestBloatReasonDoesNotLabelUsedPercentAsVacant(t *testing.T) {
	e := newBloatEnv(t)
	// 密度 70.11% 会被算成候选（headroom 23MB < 32MB 门槛 ⇒ 其实是 ok）；
	// 这里用大一点的 leaf 让它进候选，reason 才会带 aggregate 文案。
	log, _ := e.run(t, 0, 136440, "70.11")
	if !strings.Contains(log, "aggregate(") {
		t.Fatalf("本门前提不成立：没有出现 aggregate 文案\n脚本日志：\n%s", log)
	}
	if strings.Contains(log, "%空隙") {
		t.Errorf("reason 仍把「已用占比」标成「空隙」⇒ 照字面读会把回收量算反一个量级\n脚本日志：\n%s", log)
	}
	if !strings.Contains(log, "已用") {
		t.Errorf("reason 应显式说明该百分比是「已用」\n脚本日志：\n%s", log)
	}
}

// TestBloatCandidateQueryResolvesParentIndexForFamilyKey 钉住「族键真的来自父表索引」。
//
// 这是③的结构面：候选集查询必须走 pg_inherits + 索引形状比对，
// 拿到同形状的父表索引名；否则族键退化成索引自身，基线继承就是空话。
func TestBloatCandidateQueryResolvesParentIndexForFamilyKey(t *testing.T) {
	e := newBloatEnv(t)
	_, sql := e.run(t, 0, 100, "90")

	const candMarker = "ORDER BY pg_relation_size(c.oid) DESC"
	idx := strings.Index(sql, candMarker)
	if idx < 0 {
		t.Fatalf("没有找到候选集查询（下发的 SQL：\n%s）", sql)
	}
	start := strings.LastIndex(sql[:idx], "SELECT c.oid::regclass")
	if start < 0 {
		start = 0
	}
	candSQL := sql[start:idx]

	if !strings.Contains(candSQL, "pg_inherits") {
		t.Errorf("候选集查询未走 pg_inherits ⇒ 拿不到分区父表 ⇒ 族键退化成索引自身\n候选集 SQL：\n%s", candSQL)
	}
	if !strings.Contains(candSQL, "regexp_replace") {
		t.Errorf("候选集查询未按索引**形状**比对父表索引 ⇒ 同名不同形的索引会误配\n候选集 SQL：\n%s", candSQL)
	}
	if !strings.Contains(candSQL, "COALESCE") {
		t.Errorf("候选集查询必须对「找不到父表索引」有兜底（非分区表的族键 = 自身）\n候选集 SQL：\n%s", candSQL)
	}
}

// ★ 这里**曾经**有过一条「族键缺失时回退到索引名」的门（TestBloatFamilyKeySurvivesEmptyField），
//   变异验证时发现它是**恒真门**：把脚本里的
//   `[ -z "$fam_key" ] && fam_key="$idx"` 删掉，那条门**照样全绿** ——
//   因为基线查找本来就还有第二道「按索引名找」的兜底，两条路径殊途同归。
//   ⇒ 恒真门不提供任何保证，删掉（2026-10-19，§143.12）。
//   族键路径本身的保证由 TestBloatBaselineIsInheritedByFamilyKey 承担，
//   且 M2 变异（把族键查找整段去掉）确认它会红。

