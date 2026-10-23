package admin

// R77 轮（2026-10-24）：`/api/admin/compression/stats` 的 SQL 形状守卫 +
// 预算纪律守卫。
//
// ★ 为什么要守形状：生产 252 共享库实测，旧形状（三条 bodies LEFT JOIN +
// 一条昂贵且可选的载荷聚合**共用一个 10s ctx**）⇒ 端点**恒 200 但响应体残缺**
// （`hourly_series` 长度 0、`estimated_original_tokens` / `summary_mode_rows` /
// `token_band_*` 整段缺失）⇒ **调用方无从分辨「真的是 0」与「超时了」**。
//
// ★ 三条形状不变量（每条都有生产读数支撑，见
//   docs/audit/2026-10-24-R77-compression-stats-200假成功与载荷聚合归因.md）：
//
//	① bodies 腿必须是 EXISTS 半连接（不搬载荷），且 **EXISTS 只许求值一次**
//	   ⇒ 生产实测：agg 1,355ms → 416ms、bucket 517ms → 237ms，且逐行等价；
//	   ⇒ **求值两次 = 762ms，正好是求值一次的两倍** ⇒ 这是最容易回退的一处；
//	② 昂贵且可选的那一步**必须排在最后**，并带**独立且小于总预算的上限**
//	   ⇒ 否则它会吃掉其余三条必需查询的预算；
//	③ 任何一步拿不到 ⇒ **必须显式记账**（degraded + reason），不得静默消失。
//
// ★ 判据一律断言**真实产物**（compressionStatsSQL / compressionStatsSteps 的输出），
// 而非源码字面量 ⇒ 否则「换个地方写同样的 SQL」就会把门变成恒绿门
// （R76 轮同款做法）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// compressionStatsExistsMarker 是 bodies 半连接的判定片段。
// ★ 用固定片段而不是整段 bodies 源：整段太长且含换行，断言会变成脆的字符串比对。
const compressionStatsExistsMarker = "EXISTS (SELECT 1 FROM "

// compressionStatsProdSQL 返回生产形态的 4 段查询（无租户过滤、小时桶）。
func compressionStatsProdSQL() compressionStatsSQLs {
	return compressionStatsSQL("", "date_trunc('hour', rl.ts)")
}

// TestR77CompressionStatsBodiesJoinMustBeExistsSemantics（★ 判据①前半）
//
// ★ bodies 腿必须走 EXISTS 半连接：
//
//	① LEFT JOIN 会把 bodies 的**载荷列**带进结果集（生产实测那条载荷聚合
//	   要读 24h 窗口内**全部** request_body = 1,278 MB）；
//	② LEFT JOIN 还**不免疫行放大**（bodies 源 = hot ∪ 6 个分区，同一个
//	   request_id 落在两处就会放大；实测当前窗口放大 = 0，但**不是被强制的约束**）。
func TestR77CompressionStatsBodiesJoinMustBeExistsSemantics(t *testing.T) {
	q := compressionStatsProdSQL()

	for name, sql := range map[string]string{"agg": q.agg, "bucket": q.bucket} {
		if !strings.Contains(sql, compressionStatsExistsMarker) {
			t.Errorf("★ %s 查询里找不到 bodies 的 EXISTS 半连接", name)
		}
		if strings.Contains(sql, "LEFT JOIN "+dbpkg.SessionFamilyBodiesSourceSQL()) {
			t.Errorf("★ %s 查询仍有 bodies 的 LEFT JOIN ⇒ 载荷会被带进结果集，且不免疫行放大", name)
		}
		// 半连接里的判定谓词必须保留（去掉它口径就变了：会把没有 outbound body 的轮次算进来）
		if !strings.Contains(sql, "rb.outbound_body IS NOT NULL") {
			t.Errorf("★ %s 查询的半连接缺 `rb.outbound_body IS NOT NULL` ⇒ 口径被改了", name)
		}
	}

	// ★ 就地阳性对照：载荷聚合那一条**必须**保留 LEFT JOIN
	//   （半连接只能回答「有没有」，回答不了「正文多长」）
	//   ⇒ 这条对照证明上面的判据不是「一律禁 LEFT JOIN」这类错误规则。
	if !strings.Contains(q.estOrig, "LEFT JOIN "+dbpkg.SessionFamilyBodiesSourceSQL()) {
		t.Error("★ estimated-orig 查询必须保留 bodies 的 LEFT JOIN —— 它要读 rb.request_body 的正文长度，" +
			"半连接回答不了「正文多长」")
	}
	if !strings.Contains(q.estOrig, "rb.request_body") {
		t.Error("★ estimated-orig 查询必须读 rb.request_body（它算的就是原始字节数）")
	}
}

// TestR77CompressionStatsExistsMustBeEvaluatedOnce（★ 判据①后半）
//
// ★★★ 这是本轮最容易被回退、且**代价最直接**的一条：
// EXISTS 每多求值一次，成本就多一整轮 bodies 扫描
// （生产实测：求值一次 416ms、求值两次 762ms —— **正好翻倍**）。
// ⇒ 所以 EXISTS 必须在**子查询的投影里**算一次，外层聚合直接引用该列。
func TestR77CompressionStatsExistsMustBeEvaluatedOnce(t *testing.T) {
	q := compressionStatsProdSQL()

	for name, sql := range map[string]string{"agg": q.agg, "bucket": q.bucket} {
		if got := strings.Count(sql, compressionStatsExistsMarker); got != 1 {
			t.Errorf("★ %s 查询里 EXISTS 半连接必须**只求值一次**（生产实测：1 次 416ms / 2 次 762ms，正好翻倍），实测 %d 次\nSQL:\n%s",
				name, got, sql)
		}
		// EXISTS 必须落在子查询的投影里（AS has_outbound），外层聚合引用该列
		if !strings.Contains(sql, "AS has_outbound") {
			t.Errorf("★ %s 查询的 EXISTS 必须算成子查询投影列（AS has_outbound），外层聚合引用它", name)
		}
		if !strings.Contains(sql, "rl.has_outbound") {
			t.Errorf("★ %s 查询的外层聚合必须引用 rl.has_outbound（而不是再写一遍 EXISTS）", name)
		}
	}

	// ★ 就地阳性对照：同一把尺子必须能认出「求值两次」的形态（否则上面那条恒绿）。
	twice := "SELECT count(*) FILTER (WHERE " + compressionStatsExistsMarker +
		"bodies rb WHERE rb.request_id = t.request_id) FROM t"
	if got := strings.Count(twice, compressionStatsExistsMarker); got != 1 {
		t.Fatalf("★ 对照件构造错了：EXISTS 片段数 = %d（应为 1）", got)
	}
	twice += compressionStatsExistsMarker + "bodies rb WHERE rb.request_id = t.request_id)"
	if got := strings.Count(twice, compressionStatsExistsMarker); got != 2 {
		t.Fatalf("★ 对照件应当能被尺子数成 2 次 EXISTS，实测 %d ⇒ 尺子坏了", got)
	}
}

// TestR77CompressionStatsExpensiveOptionalStepMustBeLastAndBounded（★ 判据②）
//
// ★ 昂贵且可选的那一步（读全部载荷做 SUM）**必须排在最后**：
//
//	否则它超预算时，后面的必需查询就**一个都跑不到**（旧行为：hourly_series 恒空）。
//
// ★ 而且它必须有**独立且小于总预算**的上限 ⇒ 它不能把端点总预算吃光。
func TestR77CompressionStatsExpensiveOptionalStepMustBeLastAndBounded(t *testing.T) {
	steps := (&Handler{}).compressionStatsSteps(compressionStatsProdSQL(), nil, &compressionStatsResult{})
	if len(steps) == 0 {
		t.Fatal("★ 步骤序列是空的")
	}

	last := steps[len(steps)-1]
	if last.name != "estimated-orig" {
		t.Errorf("★ 最贵且可选的那一步（estimated-orig）必须排在最后，实际最后一步是 %q，序列=%v",
			last.name, compressionStatsStepNames(steps))
	}
	if last.budget <= 0 {
		t.Error("★ estimated-orig 那一步必须有独立预算上限（0 = 不限 ⇒ 它会吃光端点总预算）")
	}
	if last.budget >= compressionStatsTotalBudget {
		t.Errorf("★ estimated-orig 的预算上限必须小于端点总预算（%v），实际 %v",
			compressionStatsTotalBudget, last.budget)
	}

	// 其余步骤（必需的那些）**不得**被单独限预算 ⇒ 否则它们可能被静默跳过
	for _, s := range steps[:len(steps)-1] {
		if s.budget != 0 {
			t.Errorf("★ 必需步骤 %q 不得有独立预算上限（实际 %v）—— 它被跳过就等于核心数字缺失", s.name, s.budget)
		}
	}
}

func compressionStatsStepNames(steps []compressionStatsStep) []string {
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.name)
	}
	return names
}

// TestR77CompressionStatsSlowStepMustNotStarveLaterSteps（★ 判据③的行为腿）
//
// ★ 一条慢步骤**不得**让后面的步骤「看起来成功但没有数据」。
// ⇒ 后面的步骤必须被**显式记为 skipped**（而不是静默返回空结果）。
func TestR77CompressionStatsSlowStepMustNotStarveLaterSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	ran := false
	steps := []compressionStatsStep{
		{
			name: "slow",
			run: func(stepCtx context.Context) error {
				select {
				case <-stepCtx.Done():
					return stepCtx.Err()
				case <-time.After(2 * time.Second):
					return nil
				}
			},
		},
		{
			name: "after",
			run: func(stepCtx context.Context) error {
				ran = true
				return nil
			},
		},
	}

	results := compressionStatsRunSteps(ctx, steps)
	if len(results) != 2 {
		t.Fatalf("★ 步骤结果数应等于步骤数，实测 %d", len(results))
	}
	if results[0].err == nil {
		t.Error("★ 慢步骤应当因预算耗尽而失败")
	}
	if !results[1].skipped {
		t.Error("★ 预算耗尽后，后面的步骤必须被显式记为 skipped（不得静默跳过）")
	}
	if ran {
		t.Error("★ 预算耗尽后，后面的步骤不应被执行")
	}
	// ★ 降级原因必须**点名**是哪一步不可得
	reason := compressionStatsDegradedReason(results)
	if reason == "" {
		t.Fatal("★ 有步骤不可得 ⇒ 降级原因不得为空")
	}
	if !strings.Contains(reason, "after") {
		t.Errorf("★ 降级原因必须点名不可得的步骤，实际 %q", reason)
	}
}

// TestR77CompressionStatsDegradedMustBeRecorded（★ 判据③的记账腿）
//
// ★ 「拿不到」必须**显式记账**：degraded = true + 原因非空。
// ⇒ ★ 旧行为是「ctx 一过期就把后面的查询结果静默丢掉、仍然返回 200」。
func TestR77CompressionStatsDegradedMustBeRecorded(t *testing.T) {
	boom := errors.New("boom")
	results := compressionStatsRunSteps(context.Background(), []compressionStatsStep{
		{name: "ok", run: func(context.Context) error { return nil }},
		{name: "broken", run: func(context.Context) error { return boom }},
	})

	if results[0].err != nil {
		t.Errorf("★ 成功的步骤不应有错误，实测 %v", results[0].err)
	}
	if results[1].err == nil {
		t.Error("★ 失败的步骤必须记下错误")
	}
	reason := compressionStatsDegradedReason(results)
	if reason == "" {
		t.Fatal("★ 有步骤失败 ⇒ 降级原因不得为空")
	}
	if !strings.Contains(reason, "broken") {
		t.Errorf("★ 降级原因必须点名失败的步骤，实际 %q", reason)
	}

	// ★ 反向自检：全部成功 ⇒ 不得报降级（否则降级标记是恒真的）
	allOK := compressionStatsRunSteps(context.Background(), []compressionStatsStep{
		{name: "ok", run: func(context.Context) error { return nil }},
	})
	if reason := compressionStatsDegradedReason(allOK); reason != "" {
		t.Errorf("★ 全部成功时不得报降级，实际 %q", reason)
	}
}
