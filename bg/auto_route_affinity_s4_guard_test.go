package bg

import (
	"os"
	"strings"
	"testing"
)

// S4 停写跨门边界的**第三个失效方向**（2026-10-02 审计）。
//
// affinity 聚合的驱动表是 auto_route_selections_all，由
// domains/hooks/observability/telemetry/selection_writer.go 写入，
// **该写方不咨询** settings.KeyRequestLogsWriteEnabled（实测 0 处调用）。
// 而聚合里的两条合成流量排除臂（NOT EXISTS ... FROM request_logs_hot / request_logs）
// 整体在门内。
//
// 停写一旦生效：驱动表继续收新行，证据表冻结 ⇒ 两条 NOT EXISTS **恒真**
// ⇒ goal-* / auto-title-generator / auto-summary-generator / session-summary
// 这些合成流量不再被排除，直接进入亲和度聚合 ⇒ 模型被污染，且无任何信号。
//
// 与另两个方向的区别：ledger 的 usage_credit 是「两侧同期比较」⇒ 假报机；
// credential_recovery 的 lookback 是「证据缺失」⇒ 静默洞；这个是
// 「**过滤臂在门内、驱动表在门外**」⇒ 过滤静默失效、方向是**过度纳入**。

// 真库实测（2026-10-02，库 llm_gateway）：

//	synthetic_requests = 131（session_turns 中 origin_actor 命中上述集合）
//	present_in_v1_hot   = 0    ← 热表臂覆盖 0 条
//	present_in_v1_mother= 131  ← 母表臂覆盖全部
//
// 即：**今天唯一兜住过滤的是 request_logs 母表，而那正是本项目要删掉的表。**
// 停写后新合成流量不再有 v1 记录 ⇒ 母表臂也失效；v1 退役后该臂彻底消失。
// 所以第三条（session 族）臂不是「防御性冗余」，它是终局下的承重臂。
//
// 而这恰恰使它**最容易被误删**：在今天的数据上它对聚合结果零影响（本地 564 条
// selections 三条臂都是 564 通过），任何人看到「新加的臂什么都没改变」都会当冗余
// 清掉。下面这道门就是为此存在。

const syntheticActorGuard = "'auto-title-generator','auto-summary-generator','session-summary'"

// TestAffinitySynthesisExclusionCoversSessionFamily pins the third arm.
func TestAffinitySynthesisExclusionCoversSessionFamily(t *testing.T) {
	src := mustReadSource(t, "auto_route_affinity_worker.go")

	if !strings.Contains(src, "FROM session_turns st") {
		t.Fatal("亲和度聚合的合成流量排除臂不再覆盖 session 族。\n" +
			"真库实测：131 条合成请求在 request_logs_hot 里覆盖 0 条、在 request_logs 母表覆盖 131 条。\n" +
			"所以今天唯一兜住过滤的是母表——而那正是本项目要删掉的表。停写后新合成流量不再有 v1 记录，\n" +
			"v1 退役后该臂彻底消失 ⇒ 合成流量（goal-* / 标题、摘要生成器）会直接进入亲和度聚合，\n" +
			"污染模型且无任何信号。\n" +
			"注意：这条臂在今天的数据上对聚合结果零影响，看起来像冗余——它不是，它是终局的承重臂。")
	}
	// 三条臂的 actor 集合必须一致，否则新臂的判别口径与旧臂漂移。
	for _, want := range []string{
		"COALESCE(st.origin_actor, '') LIKE 'goal-%'",
		"COALESCE(st.origin_actor, '') IN (" + syntheticActorGuard + ")",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("session 族排除臂的判别条件与 v1 臂不一致，缺：%s\n"+
				"三条臂必须用同一套 actor 集合，否则口径漂移（v1 兜住的 session 未必兜得住，反之亦然）", want)
		}
	}
	// 原有的两条 v1 臂必须保留：双写期它们是主覆盖，删掉会立刻改变今天的聚合结果。
	for _, want := range []string{
		"FROM request_logs_hot rl",
		"FROM request_logs rl",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("v1 排除臂 %q 被删除了：双写期它是主覆盖（真库 131/131），"+
				"删掉会立刻改变今天的亲和度聚合结果", want)
		}
	}
}

// TestAffinityAggregationDriverIsNotGated records the structural fact the fix
// depends on: the driving table keeps receiving rows during stop-write, so the
// filter arm is the only thing that can go stale. If this ever changes, the
// third arm's necessity changes with it and this test should be revisited.
func TestAffinityAggregationDriverIsNotGated(t *testing.T) {
	writer := mustReadSource(t, "../domains/hooks/observability/telemetry/selection_writer.go")
	if !strings.Contains(writer, "INTO auto_route_selections_hot") {
		t.Fatal("selection_writer.go 不再写 auto_route_selections_hot —— 亲和度聚合的驱动表来源变了，" +
			"第三条排除臂的必要性需要重新评估")
	}
	if strings.Contains(writer, "RequestLogsWriteEnabled()") {
		// 这不会让本改动失效（第三条臂仍然正确且必要），但意味着驱动表也会停写，
		// 届时亲和度聚合整体无新输入——属于另一条待记的失效路径。
		t.Log("提示：selection_writer.go 现在也咨询 S4 门了。若驱动表随之停写，" +
			"亲和度聚合将整体无新输入（与本条排除臂的失效是两件事，别混）")
	}
}

// mustReadSource reads a file relative to the bg package directory.
func mustReadSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
