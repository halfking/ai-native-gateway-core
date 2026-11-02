package admin

// probe_lane_read_test.go —— 探测车道读侧的门禁（2026-11-02）。
//
// 钉住三件事：
//  1. 端点确实注册在 /api/admin/probe/lane/requests（读侧存在性）；
//  2. 查询谓词**不**依赖业务读面的冻结契约（不 JOIN api_keys、
//     不引用 request_logs 视图列、不依赖 partition_date）；
//  3. lane_enabled 随响应回传 —— 隔离开关默认关时表恒为空，
//     「0 行」有两种含义（车道没开 / 车道开了但最近没探测），
//     不回传这个字段运维就会把前者读成后者。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbeLaneReadEndpointRegistered 钉住读侧端点已挂载。
//
// ★ 为什么这条要钉：审计发现过「写侧有、读侧无」的形态（探测被物理隔离
//
//	出去后运维无路可查）⇒ 读侧一旦没挂上，探测流量就成了一进不出的黑洞，
//	而且**没有任何门会红**（表在、写入在、编译在，只有「读不到」这件事
//	没有量具）。本门就是那个量具。
func TestProbeLaneReadEndpointRegistered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("probe_dashboard.go"))
	if err != nil {
		t.Fatalf("read probe_dashboard.go: %v", err)
	}
	if !strings.Contains(string(raw), `"/api/admin/probe/lane/requests"`) {
		t.Error("探测车道读侧端点未注册：/api/admin/probe/lane/requests\n" +
			"  物理隔离开启时探测流量写进 session_turns_probe_hot ⇒ 必须有读侧，\n" +
			"  否则探测流量成一进不出的黑洞（表在、写在、编译在，只有读不到）")
	}
	if _, err := os.Stat("probe_lane_read.go"); err != nil {
		t.Errorf("探测车道读侧实现文件缺失（admin/probe_lane_read.go）：%v", err)
	}
}

// TestProbeLaneQueryDoesNotDependOnFrozenBusinessReadFace 钉住读侧与业务读面解耦。
//
// ★ 为什么这条要钉：admin 的 /api/logs 读面走 session_turns 两臂原生投影，
//
//	其 118 列投影被冻结（R37）、视图不暴露 partition_date ⇒ 分区裁剪只能
//	靠原生形态 + 分区键谓词；请求列表还 LEFT JOIN api_keys。
//	探测族是**窄列 25 列、无 session_id / partition_date** ⇒ 一旦有人
//	"图省事"把它塞进 /api/logs 的源，会撞上冻结契约。
func TestProbeLaneQueryDoesNotDependOnFrozenBusinessReadFace(t *testing.T) {
	raw, err := os.ReadFile("probe_lane_read.go")
	if err != nil {
		t.Fatalf("read probe_lane_read.go: %v", err)
	}
	body := string(raw)

	// ★ 只判**代码**，不判注释：注释里会**引用**这些词来解释「为什么不依赖
	//   它们」（那正是本测试要防的误伤形态 —— 判据扫全文会把「注释提到」
	//   读成「代码依赖」⇒ 门恒红 ⇒ 门失去守卫力）。
	//   stripGoComments 是 admin 包**已有**的工具（session_view_dependency_risk_test.go），
	//   不重复造轮子。
	code := stripGoComments(body)

	forbidden := []string{
		"api_keys",                        // 业务读面的 JOIN 面
		"request_logs_with_current_month", // 业务读面的视图源
		"partition_date",                  // 会话族分区键（探测族没有这列）
		"session_turns ",                  // 会话族父表（探测族是独立表）
	}
	for _, f := range forbidden {
		if strings.Contains(code, f) {
			t.Errorf("探测车道读侧的**代码**引用了业务读面的冻结契约 %q\n"+
				"  探测族是独立窄表（25 列、无 session_id / partition_date）；\n"+
				"  把它并进业务读面会撞上 118 列冻结 + 分区裁剪契约",
				f)
		}
	}
	// 反向：必须真的读探测族表，且带时间窗谓词（否则是全表扫描）。
	if !strings.Contains(code, "FROM session_turns_probe_hot") {
		t.Error("探测车道读侧必须从 session_turns_probe_hot 读")
	}
	if !strings.Contains(code, "ts >= $1 AND ts <= $2") {
		t.Error("探测车道读侧必须带 ts 时间窗谓词（热表无分区裁剪，无界扫描代价高）")
	}
}

// TestProbeLaneLiveQueueKeyMatchesWriteSide 钉住 Redis 读侧的队列键与写侧**同源**。
//
// ★ 为什么这条要钉：探测 tile 的写侧在 liveRequestQueueKeys 里拼键
//
//	（llmgw:live:probe:main / 租户 llmgw:live:tenant:<id>:probe:main），
//	读侧在 ReplayLane 里拼键。两处各写一份后缀拼法 ⇒ 必然漂移 ⇒
//	探测 tile 写进 A 队列、读从 B 队列 ⇒ **探测在实时流里一进不出**
//	（表在、写在、编译在、连测试都绿，只有「取不到」这件事没量具）。
//
// ★ 判据：读侧必须调用 probelane.RedisSuffixProbe（后缀拼法的 SSOT），
//
//	而**不是**自己手写 "probe:main" 字面量。
func TestProbeLaneLiveQueueKeyMatchesWriteSide(t *testing.T) {
	raw, err := os.ReadFile("live_stream_redis_store.go")
	if err != nil {
		t.Fatalf("read live_stream_redis_store.go: %v", err)
	}
	body := string(raw)

	// 读侧（ReplayLane）必须用 SSOT 拼后缀。
	replayIdx := strings.Index(body, "func (s *LiveStreamRedisStore) ReplayLane(")
	if replayIdx < 0 {
		t.Fatal("LiveStreamRedisStore.ReplayLane 不存在 —— 探测键空间没有读侧")
	}
	replayBody := body[replayIdx:]
	if next := strings.Index(replayBody, "\nfunc "); next > 0 {
		replayBody = replayBody[:next]
	}
	if !strings.Contains(replayBody, "probelane.RedisSuffixProbe(") {
		t.Error("ReplayLane 必须用 probelane.RedisSuffixProbe 拼租户队列后缀（SSOT）\n" +
			"  写侧 liveRequestQueueKeys 用的是同一个函数；两处各写一份字面量必然漂移\n" +
			"  ⇒ 探测 tile 写进 A 队列、读从 B 队列 ⇒ 探测在实时流里一进不出")
	}

	// 写侧（liveRequestQueueKeys）也必须用同一个 SSOT。
	writeIdx := strings.Index(body, "func liveRequestQueueKeys(")
	if writeIdx < 0 {
		t.Fatal("liveRequestQueueKeys 不存在")
	}
	writeBody := body[writeIdx:]
	if next := strings.Index(writeBody, "\nfunc "); next > 0 {
		writeBody = writeBody[:next]
	}
	if !strings.Contains(writeBody, "probelane.RedisSuffixProbe(") {
		t.Error("liveRequestQueueKeys 必须用 probelane.RedisSuffixProbe（与读侧同源）")
	}
}

// TestProbeLaneLiveReadEndpointRegistered 钉住 Redis 读侧端点已挂载。
func TestProbeLaneLiveReadEndpointRegistered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("probe_dashboard.go"))
	if err != nil {
		t.Fatalf("read probe_dashboard.go: %v", err)
	}
	if !strings.Contains(string(raw), `"/api/admin/probe/lane/live"`) {
		t.Error("探测车道 Redis 读侧端点未注册：/api/admin/probe/lane/live\n" +
			"  隔离开启时探测 tile 写进 llmgw:live:probe:* ⇒ 必须有读侧")
	}
}

// TestProbeLaneMinuteStatsReadSideExists 钉住探测分钟聚合表的读侧存在。
//
// ★ 为什么这条要钉：request_stats_probe_minute 是 request_stats_minute 的
//
//	平行表；业务看板只读业务表 ⇒ 隔离开启后探测的 QPS / 成功率在看板上
//	**完全不可见**（探测被分流出去，看板看不到业务表以外的东西）。
//	⇒ 运维无法回答「探测还活着吗」—— 而这正是物理隔离最该回答的问题。
func TestProbeLaneMinuteStatsReadSideExists(t *testing.T) {
	reg, err := os.ReadFile(filepath.Join("probe_dashboard.go"))
	if err != nil {
		t.Fatalf("read probe_dashboard.go: %v", err)
	}
	if !strings.Contains(string(reg), `"/api/admin/probe/lane/stats"`) {
		t.Error("探测分钟聚合读侧端点未注册：/api/admin/probe/lane/stats")
	}

	raw, err := os.ReadFile("probe_lane_read.go")
	if err != nil {
		t.Fatalf("read probe_lane_read.go: %v", err)
	}
	code := stripGoComments(string(raw))
	if !strings.Contains(code, "FROM request_stats_probe_minute") {
		t.Error("探测分钟读侧必须从 request_stats_probe_minute 读")
	}
	// ★ 必须带 bucket 时间窗谓词：这张表无分区裁剪，无界扫描会随时间线性变贵。
	if !strings.Contains(code, "bucket >= $1 AND bucket <= $2") {
		t.Error("探测分钟读侧必须带 bucket 时间窗谓词")
	}
	// ★ AvgLatencyMs 的分母保护：requests=0 时不得出现除零（NaN/Inf 进 JSON 会变成 null）。
	if !strings.Contains(code, "if s.Requests > 0") {
		t.Error("探测分钟读侧必须保护 AvgLatencyMs 的除零（requests=0 时应为 0，不是 NaN/Inf）")
	}
}

// TestProbeLaneReadReportsLaneEnabled 钉住 lane_enabled 随响应回传。
//
// ★ 为什么这条要钉：车道开关**默认关** ⇒ session_turns_probe_hot 恒为空。
//
//	若响应不区分「车道没开」与「车道开了但最近没探测」，运维查得 0 行
//	就会得出「没有探测发生」的错误结论（真相是：探测流量在业务表里）。
func TestProbeLaneReadReportsLaneEnabled(t *testing.T) {
	raw, err := os.ReadFile("probe_lane_read.go")
	if err != nil {
		t.Fatalf("read probe_lane_read.go: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"lane_enabled"`) {
		t.Error("探测车道读侧必须回传 lane_enabled —— 否则「0 行」会被读成「没有探测发生」")
	}

	// ★ 形态判据不够：只看「出现 lane_enabled 且出现 probelane.PhysicalEnabled()」
	//   会被 `probelane.PhysicalEnabled() && false` 这类**语义已坏**的写法
	//   骗过（变异自检实测 rc=0 ⇒ 门有盲区）。⇒ 必须判**赋值右值本身**
	//   就是那一次调用。
	code := stripGoComments(body)
	const wantAssign = `"lane_enabled": probelane.PhysicalEnabled(),`
	if !strings.Contains(code, wantAssign) {
		t.Errorf("lane_enabled 的赋值右值必须是 probelane.PhysicalEnabled()（开关 SSOT），\n"+
			"  实际形态不匹配（期望子串 %s）。\n"+
			"  ★ 形态判据（\"是否出现这两个串\"）会被 `probelane.PhysicalEnabled() && false`\n"+
			"    这类语义已坏的写法骗过（变异自检实测 rc=0）⇒ 这里判的是赋值右值。",
			wantAssign)
	}
}
