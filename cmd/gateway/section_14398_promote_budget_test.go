//go:build !integration

package main

// ═══════════════════════════════════════════════════════════════════════
// §143.98 会话正文族（session_bodies）promote【预算不变式】门
//
// ★ ★ 为什么要有这道门：
//
//   2026-10-23 实测（生产 252 只读 + 154 的 24h 日志）查清了
//   `session_bodies_hot` 的 promote「每小时必败一次」的算术：
//
//     每行写成本 ≈ 29 ms（★ 两个独立量具互证：
//         ① 154 日志的「首批批次：行数 vs 耗时」过原点回归 = 29.9 ms/行；
//         ② 生产 pg_stat_statements 的 `INSERT INTO session_bodies_hot`
//            = 28.2 ms/行（77,886 行））
//     批次截止 = 60 s（★ Go ctx + `SET LOCAL statement_timeout = '60s'`）
//     ⇒ ⇒ **60 s 内最大可行 batch ≈ 2,064 行**
//     ⇒ ⇒ 而 `lifecycle.promote_batch_size` 在生产 **没有 settings_kv 行**
//     ⇒ ⇒ 走代码默认 **5,000** ⇒ ⇒ **需要 ≈ 145 s = 2.4× 预算**
//     ⇒ ⇒ **每个满批必然超时 ⇒ 整批回滚 ⇒ 搬 0 行**
//
//   ⇒ ⇒ **「排水搬 0 行」不是一个神秘故障，而是一条可判定的算术**。
//   ⇒ ⇒ 这道门把那条算术**钉成可执行判据**：谁把批大小调到不可提交的量级，
//   ⇒ ⇒ 或者谁把「推荐值」改成同样不可提交的量级，门都会红。
// ═══════════════════════════════════════════════════════════════════════

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	section14398AuditSQL   = "scripts/audit/audit_session_bodies_promote_budget.sql"
	section14398PartitionM = "bg/partition_manager.go"
	section14398Runbook    = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14398Lessons    = "LESSONS.md"
	section14398Agents     = "AGENTS.md"

	// ★ ★ 现场实测的每行写成本（毫秒）—— 2026-10-23 于生产 252 只读实测：
	//   ① 154 日志「首批批次：行数 vs 耗时」过原点回归（9 个样本）= 29.9
	//   ② 生产 pg_stat_statements 的热表单行 INSERT（77,886 行）= 28.2
	//   ③ 生产审计脚本 Q9 的现场复算 = 29.1
	//   ⇒ 取 29.1 作判据常量（★ 三个读数都落在 28~30 区间内）。
	section14398MeasuredMsPerRow = 29.1

	// ★ 批次预算：Go ctx 60s + PG `SET LOCAL statement_timeout = '60s'`。
	section14398BudgetMs = 60_000

	// ★ 推荐批大小：60,000 / 29.1 ≈ 2,064 ⇒ 取 1,000（★ 约 2× 余量，
	// ★ 且每批 ≈ 29 s ⇒ 300 s 周期可容 10 批 ⇒ 单 tick 上限 10,000 行
	// ★ ≫ 到达率 ≈ 785 行/h ⇒ ⇒ 积压可清）。
	section14398RecommendedBatch = 1000
)

func repoRoot14398(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14398(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14398(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ①：只读审计脚本必须在场，且必须带「预算不变式自检」那一段
// ═══════════════════════════════════════════════════════════════════════

func TestSection14398_AuditScriptHasBudgetSelfCheck(t *testing.T) {
	raw := readRepoFile14398(t, section14398AuditSQL)

	// ★ Q1~Q9 九个探针一个都不能少（★ 少了就有一项读数没人负责）。
	for _, q := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8", "Q9"} {
		if !strings.Contains(raw, "=== "+q+" ") {
			t.Errorf("★ 只读审计脚本缺 %s 探针 —— ★ 少一项读数就有一项没人负责", q)
		}
	}

	// ★ 必须是【只读】的：★ 不许出现写/DDL 关键字（行首形态）。
	// ★ 这条判据的价值：★ 审计脚本会被人在生产上直接跑 ⇒ ⇒ 它自己绝不能写。
	writeRE := regexp.MustCompile(`(?m)^[[:space:]]*(INSERT|UPDATE|DELETE|DROP|CREATE|ALTER|TRUNCATE|REINDEX|VACUUM|GRANT|REVOKE|COPY)\b`)
	if m := writeRE.FindString(raw); m != "" {
		t.Errorf("★ 只读审计脚本里出现写/DDL 关键字 %q —— ★ 它会在生产上被执行 ⇒ 绝不能写", strings.TrimSpace(m))
	}

	// ★ 自检那段必须真的在算「会不会超时」⇒ 而不是只打印两个数。
	for _, needle := range []string{"will_time_out", "max_feasible_batch", "measured_ms_per_row"} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 审计脚本的 Q9 自检缺 `%s` 列 —— ★ 预算不变式必须在脚本里被现场算出来", needle)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ②：★ 预算不变式本身（★ 本门的心脏）
// ═══════════════════════════════════════════════════════════════════════

func TestSection14398_BudgetInvariant(t *testing.T) {
	// ★ 推荐值必须**能在预算内提交**（★ 否则推荐本身就是错的）。
	// ★ 用变量承接除法：★ 常量表达式会被编译期折算成带小数的常量，
	// ★ 而 Go 不允许把带小数的【常量】直接转成 int（编译错误）。
	msPerRow := section14398MeasuredMsPerRow
	maxFeasible := int(float64(section14398BudgetMs) / msPerRow)
	needMs := float64(section14398RecommendedBatch) * msPerRow
	if needMs >= float64(section14398BudgetMs) {
		t.Errorf("★ 推荐批大小 %d 在实测 %.1f ms/行下需要 %.0f ms ≥ 预算 %d ms "+
			"⇒ ⇒ 推荐值本身就会超时 ⇒ ⇒ 它必须是 ≤ %d 行",
			section14398RecommendedBatch, msPerRow, needMs,
			section14398BudgetMs, maxFeasible)
	}

	// ★ 代码默认值的现状：★ 若它仍超出预算，★ 则**必须已在 runbook 里登记**
	// ★ （★ 「缺陷已登记」是可执行的登记；★ 「缺陷存在但没人知道」不是）。
	raw := readRepoFile14398(t, section14398PartitionM)
	m := regexp.MustCompile(`(?m)^\s*const promoteBatchSize = (\d+)`).FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("★ %s 里找不到 `const promoteBatchSize = <n>` —— ★ 判据失效", section14398PartitionM)
	}
	defaultBatch, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("★ 解析 promoteBatchSize 失败：%v", err)
	}
	defaultNeedMs := float64(defaultBatch) * section14398MeasuredMsPerRow

	runbook := readRepoFile14398(t, section14398Runbook)
	if defaultNeedMs >= float64(section14398BudgetMs) {
		// ★ 现状：默认批大小超出预算 ⇒ 必须是「已登记」的缺陷。
		if !strings.Contains(runbook, "§143.98") {
			t.Errorf("★ 代码默认 promoteBatchSize=%d 在实测 %.1f ms/行下需要 %.0f ms ≥ 预算 %d ms "+
				"⇒ ⇒ 满批必超时 ⇒ ★ 但 runbook 里没有 `§143.98` 登记这个缺陷",
				defaultBatch, section14398MeasuredMsPerRow, defaultNeedMs, section14398BudgetMs)
		}
		for _, needle := range []string{"batch_size", "2,064", "145"} {
			if !strings.Contains(runbook, needle) {
				t.Errorf("★ runbook 的 §143.98 缺 `%s` —— ★ 缺陷登记必须带可复算的数字", needle)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ③：★ 代码侧的三条事实必须被钉住（★ 修了它们 ⇒ 门要能看出来）
// ═══════════════════════════════════════════════════════════════════════

func TestSection14398_CodeFactsPinned(t *testing.T) {
	raw := readRepoFile14398(t, section14398PartitionM)

	// ★ ① 批次截止的两道闸：Go ctx 与 PG statement_timeout。
	// ★    ★ 少任何一道 ⇒ 预算算术就不成立 ⇒ ⇒ 必须钉住。
	for _, needle := range []string{
		"context.WithTimeout(cycleCtx, 60*time.Second)",
		"SET LOCAL statement_timeout = '60s'",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ %s 缺 `%s` —— ★ 批次预算的两道闸变了 ⇒ §143.98 的算术要重算",
				section14398PartitionM, needle)
		}
	}

	// ★ ② 周期预算是 5 分钟且**全族共享**（⇒ 一个族吃光预算 ⇒ 后面的族被跳过）。
	if !strings.Contains(raw, "const promoteCycleTimeout = 5 * time.Minute") {
		t.Errorf("★ %s 的 promoteCycleTimeout 不再是 5 分钟 —— ★ §143.98 的「共享周期预算」结论要重算",
			section14398PartitionM)
	}

	// ★ ③ R51「每表保底至少 1 批」的不变式：★ §143.98 实测它**是死代码**
	// ★   （★ 循环条件里 `cycleCtx.Err() != nil` 排在最前 ⇒ 周期一过期，
	// ★   ★ 所有还没跑的族直接 break ⇒ ★ 实测有 17 个族被整轮跳过）。
	// ★   ⇒ 这里**钉住当前形态**（= 死代码那一版）⇒ ⇒ 将来有人修它时，
	// ★   ★ 门会红 ⇒ ★ 逼着同时更新 §143.98 的结论与登记。
	if !strings.Contains(raw, "if cycleCtx.Err() != nil || (batches >= promoteCycleMaxBatches && tableRanBatch) {") {
		t.Errorf("★ %s 的批次循环条件变形了 —— ★ §143.98 记录的「保底 1 批不变式是死代码」"+
			"这一结论必须重新核对（★ 改了就说明有人在修它 ⇒ ★ 要同步更新 runbook 与本门）",
			section14398PartitionM)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// 判据 ④：文档登记
// ═══════════════════════════════════════════════════════════════════════

func TestSection14398_DocsRegistered(t *testing.T) {
	runbook := readRepoFile14398(t, section14398Runbook)
	if !strings.Contains(runbook, "§143.98") {
		t.Errorf("runbook 里没有 `§143.98` —— ★ 过程不可查")
	}
	// ★ 必须带上「读侧才是最大 DB 时间消费者」这个结论（★ 它改变了优先级排序）。
	for _, needle := range []string{"outbound_body", "6,716"} {
		if !strings.Contains(runbook, needle) {
			t.Errorf("★ runbook 的 §143.98 缺 `%s` —— ★ 读侧放大是本轮最大的效率杠杆，必须在册", needle)
		}
	}
	lessons := readRepoFile14398(t, section14398Lessons)
	if !strings.Contains(lessons, "§293") {
		t.Errorf("LESSONS.md 里没有 `§293` —— ★ 通用式没沉淀")
	}
	agents := readRepoFile14398(t, section14398Agents)
	if !strings.Contains(agents, "R143") {
		t.Errorf("AGENTS.md 里没有 `R143` —— ★ 交接没更新")
	}
}
