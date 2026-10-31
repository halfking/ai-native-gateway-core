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
//
// ───────────────────────────────────────────────────────────────────────────
// ★★ 2026-10-30（R184 轮）**预算变了 ⇒ ⇒ ⇒ 本门与 runbook 的算术必须重算**
//
//   上游 44ba4dff7 把批次预算 60 s → **150 s**（`promoteBatchBudget`）、
//   周期预算 5 min → **10 min**（`promoteCycleTimeout`），并给
//   `session_bodies_hot` 加了专用旋钮
//   `lifecycle.session_bodies_hot_promote_batch_size`（代码 fallback **500**）。
//
//   ⇒ ⇒ ★★ **按 60 s 写的算术已经全部过期**（★「余量还剩多少」是会漂的读数，
//   ⇒ ⇒ ★★ 不是事实）⇒ ⇒ **本门被改成钉住新事实，并补两条新断言**：
//
// ① **两道闸必须同值**（★ Go ctx 常量 / SQL 字面量 / 门常量三者相等）
//      —— ★ 这是本轮新增的：**只钉「两边都改过」挡不住「只改了一边」**，
//      ★ 而「只改了一边」会让预算算术当场失效。
//   ② **TOAST 族的不变式**（★ R184教会的那条）
//      —— ★ 29.1 ms/行 是**通用族**的读数；正文表是 TOAST 重行（≈34 KB/行），
//      ★ R184 的 252 实测 500 行要 35~90 s ⇒ **70~180 ms/行**（贵 2.4~6.2×）
//      ★ ⇒ ⇒ 「批大小 vs 预算」必须**按族估行宽**，不能用一个全局常数。
//
//   ⇒ ⇒ ★★ **通用族的算术**（每行 29.1 ms 不变，只换预算）：
//      150,000 ms ÷ 29.1 ms/行 ≈ **5,154 行**（60 s 时是 2,064）
//      ⇒ 代码默认 5,000 需要 **145.5 s ≤ 150 s** ⇒ 【不再必超时】（余量 ≈3%）
//      ⇒ ⇒ ⇒ ★★ **但贴线** ⇒ ⇒ ⇒ **「不再必超时」是算术层面的，不是实测层面的**
//      ⇒ ⇒ ⇒ ⇒ **⇒ 本门要求：默认批贴线时，runbook 必须带可复算的数字登记**。
// ═══════════════════════════════════════════════════════════════════════

import (
	"fmt"
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
	//   ★★ 适用面：★ 这是【通用族】的口径；★ 正文表（TOAST 重行）不适用，
	//      ★ 正文表见 section14398ToastMsPerRow。
	section14398MeasuredMsPerRow = 29.1

	// ★ 批次预算：Go ctx + PG `SET LOCAL statement_timeout`（两者同值）。
	// ★ 2026-10-30 R184：60_000 → 150_000（= `promoteBatchBudget`）。
	section14398BudgetMs = 150_000

	// ★ 推荐批大小：150,000 / 29.1 ≈ 5,154 ⇒ 取 1,000（★ ≈29 s ⇒ 余量 5×，
	// ★ 且每批 ≈ 29 s ⇒ 周期预算可容多批 ⇒ 单 tick 上限 ≫ 到达率 ⇒ ⇒积压可清）。
	section14398RecommendedBatch = 1000

	// ★★ TOAST 重行族（`session_bodies_hot`）的每行成本上界（毫秒）——
	// ★★ 2026-10-30 R184 的 252 实测：单批 500 行要 35~90 s
	// ★★ ⇒ 折算 70~180 ms/行（均值 ≈34 KB/行 ⇒ 比通用族贵 2.4~6.2×）。
	// ★★ 取上界 180 作判据（★ 取上界 ⇒ 判据对「负载偏高」那侧是保守的）。
	section14398ToastMsPerRow = 180.0

	// ★★ TOAST 族必须专用的批大小（★ 与
	// ★★ `bg/partition_manager.go` 的 `sessionBodiesPromoteBatchSize` 对齐）。
	section14398ToastBatch = 500

	// ★★ 「贴线」阈值：默认批的耗时占预算的比例 ≥ 该值时，
	// ★★ 必须已在 runbook 里带**可复算的数字**登记（★ 登记式判据：修好了自动转绿）。
	// ★★ 为什么是 80%：★ 默认 5,000 行在新预算下是 145.5 s / 150 s = **97%**
	// ★★ ⇒ ⇒ 「装得下」与「装得下但贴线」是两件事 ⇒ ⇒ 只有后者才需要登记。
	section14398TightRatioNum = 80
	section14398TightRatioDen = 100
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

// section14398MaxFeasibleBatch 返回预算内可提交的最大批（★ 由常量算出 ⇒ 不抄数）。
func section14398MaxFeasibleBatch() int {
	// ★ 用变量承接除法：★ 常量表达式会被编译期折算成带小数的常量，
	// ★ 而 Go 不允许把带小数的【常量】直接转成 int（编译错误）。
	msPerRow := section14398MeasuredMsPerRow
	return int(float64(section14398BudgetMs) / msPerRow)
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

	// ★★★ 2026-10-30 R184：★ 脚本的 Q9 必须用【当前预算】而不是旧预算。
	// ★★★ 否则它会在生产上用 60 s 现场算出一个【已经过期】的结论
	// ★★★ ⇒ ⇒ **量具说谎比没有量具更糟**（★ 它会给出一个看着很精确的错答案）。
	budgetRE := regexp.MustCompile(`(?m)^\s*(\d+)\s+AS budget_seconds`)
	m := budgetRE.FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("★ 审计脚本的 Q9 里找不到 `AS budget_seconds` —— ★ 判据失效（结构变了？）")
	}
	if m[1] != strconv.Itoa(section14398BudgetMs/1000) {
		t.Errorf("★ 审计脚本的 Q9 预算常量是 %s 秒，而门钉的是 %d 秒 —— ★ ★ 脚本会用过期预算现场算结论 ⇒ 必须同批更新",
			m[1], section14398BudgetMs/1000)
	}

	// ★★★ 2026-10-30 R184：★ Q9 的 `cur` 段必须读【本族专用键】。
	// ★★★ R184 起 `resolvePromoteConfig` 分族取键 ⇒ 读全局键 = 量错旋钮。
	curStart := strings.Index(raw, "cur AS (")
	if curStart < 0 {
		t.Fatalf("★ 审计脚本里找不到 `cur AS (` —— ★ 判据失效（结构变了？）")
	}
	curEnd := strings.Index(raw[curStart:], "\n)")
	if curEnd < 0 {
		t.Fatalf("★ 审计脚本的 `cur AS (` 段没有闭合 —— ★ 判据失效")
	}
	curBlock := raw[curStart : curStart+curEnd]
	if !strings.Contains(curBlock, "lifecycle.session_bodies_hot_promote_batch_size") {
		t.Errorf("★ 审计脚本 Q9 的 `cur` 段没读 `lifecycle.session_bodies_hot_promote_batch_size` —— ★ R184 起本族走专用键 ⇒ 读错旋钮 = 量错对象")
	}
	if strings.Contains(curBlock, "WHERE key = 'lifecycle.promote_batch_size'") {
		t.Errorf("★ 审计脚本 Q9 的 `cur` 段仍在读全局键 `lifecycle.promote_batch_size` —— ★ R184 起本族不读它 ⇒ 这个旋钮量错了对象")
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
	maxFeasible := section14398MaxFeasibleBatch()
	needMs := float64(section14398RecommendedBatch) * msPerRow
	if needMs >= float64(section14398BudgetMs) {
		t.Errorf("★ 推荐批大小 %d 在实测 %.1f ms/行下需要 %.0f ms ≥ 预算 %d ms "+
			"⇒ ⇒ 推荐值本身就会超时 ⇒ ⇒ 它必须是 ≤ %d 行",
			section14398RecommendedBatch, msPerRow, needMs,
			section14398BudgetMs, maxFeasible)
	}

	// ★ 代码默认值的现状：★ 若它超出预算，★ 则**必须已在 runbook 里登记**
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
	// ★★★ 2026-10-30 R184：★ 判据从「超预算才登记」扩到「**超预算 或 贴线**都要登记」。
	// ★★★ 理由：★ 新预算下默认 5,000 行是 145.5 s / 150 s = 97% ⇒ 「装得下」
	// ★★★ 但**贴线** ⇒ ⇒ 一次负载抬升就又回到「满批必超时」⇒ ⇒ 而那正是本轮
	// ★★★ 真正修掉的活锁 ⇒ ⇒ **贴线这件事必须有人知道**（★ 登记即知情）。
	overBudget := defaultNeedMs >= float64(section14398BudgetMs)
	tight := defaultNeedMs*float64(section14398TightRatioDen) >= float64(section14398BudgetMs)*float64(section14398TightRatioNum)
	if overBudget || tight {
		if !strings.Contains(runbook, "§143.98") {
			t.Errorf("★ 代码默认 promoteBatchSize=%d 在实测 %.1f ms/行下需要 %.0f ms（预算 %d ms，%.0f%%）"+
				"⇒ ⇒ %s ⇒ ★ 但 runbook 里没有 `§143.98` 登记这件事",
				defaultBatch, section14398MeasuredMsPerRow, defaultNeedMs, section14398BudgetMs,
				defaultNeedMs*100/float64(section14398BudgetMs),
				map[bool]string{true: "满批必超时", false: "贴线（余量不足 20%）"}[overBudget])
		}
		// ★ 登记必须带**可复算的数字**⇒ ⇒ 数字由常量算出（★ 不抄数 ⇒ 不会漂）。
		needSeconds := strconv.Itoa(int(defaultNeedMs / 1000))
		for _, needle := range []string{"batch_size", strconv.Itoa(maxFeasible), needSeconds} {
			if !strings.Contains(runbook, needle) {
				t.Errorf("★ runbook 的 §143.98 缺 `%s` —— ★ 缺陷登记必须带可复算的数字"+
					"（★ 当前口径：max_feasible_batch=%d / need_seconds=%s）",
					needle, maxFeasible, needSeconds)
			}
		}
	}

	// ★★★ 2026-10-30 R184 新增：★ TOAST 重行族的不变式。
	// ★★★ 通用族的 29.1 ms/行**不适用**于正文表（TOAST 重行 ≈34 KB/行）⇒
	// ★★★ R184 的 252 实测 500 行要 35~90 s ⇒ 70~180 ms/行 ⇒ ⇒ 若按29.1 算，
	// ★★★ 会以为 5,000 行只要 145 s（装得下）⇒ ★★ **而它实际要 350~900 s**
	// ★★★ ⇒ ⇒ ⇒ 这条判据存在的理由：★ **别让「通用族装得下」掩盖「本族装不下」**。
	toastNeedMs := float64(section14398ToastBatch) * section14398ToastMsPerRow
	if toastNeedMs >= float64(section14398BudgetMs) {
		t.Errorf("★ TOAST 重行族批 %d 行 × %.0f ms/行 = %.0f ms ≥ 预算 %d ms ⇒ ⇒ 它会整批回滚"+
			"⇒ ⇒ 必须再调小批（★ 该族是 R184 专用键的靶子）",
			section14398ToastBatch, section14398ToastMsPerRow, toastNeedMs, section14398BudgetMs)
	}
	// ★ 专用键的取值必须与代码里的常量一致（★ 两处不许各写一个数）。
	if !strings.Contains(raw, fmt.Sprintf("const sessionBodiesPromoteBatchSize = %d", section14398ToastBatch)) {
		t.Errorf("★ %s 里的 sessionBodiesPromoteBatchSize 不是 %d —— ★ 门钉的 TOAST 族批大小与代码不一致 ⇒ ⇒ 两处各写一个数 = 迟早对不上",
			section14398PartitionM, section14398ToastBatch)
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
		"context.WithTimeout(cycleCtx, promoteBatchBudget)",
		"SET LOCAL statement_timeout = '150s'",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ %s 缺 `%s` —— ★ 批次预算的两道闸变了 ⇒ §143.98 的算术要重算",
				section14398PartitionM, needle)
		}
	}

	// ★ ② 周期预算是 10 分钟且**全族共享**（⇒ 一个族吃光预算 ⇒ 后面的族被跳过）。
	if !strings.Contains(raw, "const promoteCycleTimeout = 10 * time.Minute") {
		t.Errorf("★ %s 的 promoteCycleTimeout 不再是 10 分钟 —— ★ §143.98 的「共享周期预算」结论要重算",
			section14398PartitionM)
	}

	// ★★★ 2026-10-30 R184 新增：★★ **两道闸必须同值**。
	// ★★★ 只钉「两边都改过」挡不住「只改了一边」⇒ ⇒ 而「只改了一边」=
	// ★★★ Go ctx 说 150 s、PG侧说 60 s ⇒ ⇒预算算术当场失效（且**看起来没改**）。
	constRE := regexp.MustCompile(`(?m)^\s*const promoteBatchBudget = (\d+) \* time\.Second`)
	cm := constRE.FindStringSubmatch(raw)
	if cm == nil {
		t.Fatalf("★ %s 里找不到 `const promoteBatchBudget = <n> * time.Second` —— ★ 判据失效（结构变了？）", section14398PartitionM)
	}
	goSeconds, err := strconv.Atoi(cm[1])
	if err != nil {
		t.Fatalf("★ 解析 promoteBatchBudget 失败：%v", err)
	}
	if goSeconds != section14398BudgetMs/1000 {
		t.Errorf("★ promoteBatchBudget = %d 秒，而门钉的是 %d 秒 —— ★ 预算变了 ⇒ §143.98 的算术要重算",
			goSeconds, section14398BudgetMs/1000)
	}
	wantSQL := fmt.Sprintf("SET LOCAL statement_timeout = '%ds'", goSeconds)
	if !strings.Contains(raw, wantSQL) {
		t.Errorf("★ %s 缺 `%s` —— ★ Go ctx 与 PG statement_timeout **必须同值**（★ 只改一边 = 预算算术当场失效）",
			section14398PartitionM, wantSQL)
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
