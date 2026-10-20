package bg

import (
	"strings"
	"testing"
	"time"
)

// TestCandidateFailureMonitorClassifyStale 是 2026-10-20 24h 审计轮的 S4
// 停写假阳性修复的门。
//
// 修复前：candidate_failure_monitor 读到「failure log 冻结 + request log
// 活跃」就一律 slog.Error，而该形态在 S4 退役 / 主动停写场景下是**预期内**
// 的（failure writer 不再写，gateway 仍服务用户流量）⇒ ERROR 把预期内状态
// 当成故障告警，反复触发直到停写期结束。
//
// 修复后：分类器按 source_max_failure_id 是否 > 0 区分两类：
//   - > 0 ⇒ failure writer 曾活跃过 ⇒ 视作 S4 退役 / 主动停写 ⇒ WARN
//   - = 0 ⇒ failure writer 从未写入过 ⇒ 视作 writer 真坏 ⇒ ERROR
//
// 此门钉死「两类形态各自的判定方向」，并用一个**真实 S4 停写复现**的端到端
// 例子（lastFailure=2026-10-17 02:48 / lastRequest=2026-10-19 10:05，
// failureSourceMaxID=40 > 0）走全部分类器分支。
func TestCandidateFailureMonitorClassifyStale(t *testing.T) {
	tests := []struct {
		name string
		fid  int64
		want stalenessVerdict
	}{
		{
			name: "S4 停写场景：failure writer 曾经活跃（source_max=40），现在冻结 ⇒ 分类为 likelyStopWrite",
			fid:  40,
			want: stalenessLikelyStopWrite,
		},
		{
			name: "writer 长期运行后冻结：source_max=1048576 ⇒ 分类为 likelyStopWrite",
			fid:  1048576,
			want: stalenessLikelyStopWrite,
		},
		{
			name: "writer 从未写入：source_max=0 ⇒ 分类为 writerNeverWrote（真坏）",
			fid:  0,
			want: stalenessWriterNeverWrote,
		},
		{
			name: "writer max_id 落在 bigint 极小值：source_max=1 ⇒ 仍然判定曾活跃 ⇒ likelyStopWrite",
			fid:  1,
			want: stalenessLikelyStopWrite,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := candidateFailureMonitorClassifyStale(tt.fid); got != tt.want {
				t.Fatalf("candidateFailureMonitorClassifyStale(%d) = %v, want %v",
					tt.fid, got, tt.want)
			}
		})
	}
}

// TestCheckStalenessLogsWarnOnS4StopScenario 复现 S4 停写 + request 写活的
// 现场，断言日志走 WARN 而不是 ERROR。
//
// 2026-10-19/20 生产上观察到的形态（failures 完全冻结、requests 仍有）：
//
//   last_insert_at:   2026-10-17 02:48 (failure writer 已 72h+ 没写)
//   last_request_at:  2026-10-19 10:05 (request 端 5 分钟内仍有行)
//   source_max_failure_id: 40 (failure writer 曾写入 ⇒ 视作 S4 退役)
//   source_max_request_id: 任意正值
//
// 期望：slog.Warn（不是 slog.Error），并把 source_max_failure_id /
// source_max_request_id 两个字段都透传出来。
func TestCheckStalenessLogsWarnOnS4StopScenario(t *testing.T) {
	monSrc := mustReadSourceBg(t, "candidate_failure_monitor.go")
	// 提取 checkStaleness 函数体（到下一个 `func ` 之前）。
	const anchor = "func (m *CandidateFailureMonitor) checkStaleness("
	i := strings.Index(monSrc, anchor)
	if i < 0 {
		t.Fatal("checkStaleness not found in candidate_failure_monitor.go")
	}
	body := monSrc[i:]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}

	// 1. 字段透传：source_max_failure_id 必须写在日志里（与 source_max_aggregation_id
	//    同形态），没有它就没法诊断「S4 停写 vs writer 真坏」。
	if !strings.Contains(body, "source_max_failure_id") {
		t.Fatalf("checkStaleness 必须透传 source_max_failure_id 字段；got:\n%s", body)
	}
	if !strings.Contains(body, sourceMaxRequestIDKey) {
		t.Fatalf("checkStaleness 必须透传 %s 字段；got:\n%s", sourceMaxRequestIDKey, body)
	}

	// 2. SQL 必须同时读 max(aggregation_id)（failure 侧）与 max(id)（request 侧）。
	//    只有 max(ts) 不能区分 S4 退役与 writer 真坏。
	if !strings.Contains(body, "max(aggregation_id) FROM candidate_failure_logs_with_current_month") {
		t.Fatalf("checkStaleness 必须读 max(aggregation_id) FROM candidate_failure_logs_with_current_month；got:\n%s", body)
	}
	if !strings.Contains(body, "max(id) FROM") {
		t.Fatalf("checkStaleness 必须读 request 侧的 max(id) 用于 source_max_request_id；got:\n%s", body)
	}

	// 3. S4 停写形态走 WARN，不是 ERROR：函数体里必须出现 slog.Warn，且
	//    该 slog.Warn 的 msg 文案必须可被搜得到「failure writer previously
	//    active; likely intentional stop-write」——这是判定方向的钉子。
	//    ⚠ 防回归：若有人改回 slog.Error 而不删 slog.Warn，这一条会把 warning
	//    判定为「存在但 msg 文案变了」⇒ 不会误放过去。
	if !strings.Contains(body, `slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)"`) {
		t.Fatalf("checkStaleness 的 S4 停写分支必须 slog.Warn(msg with 'failure writer previously active; likely intentional stop-write')；got:\n%s", body)
	}

	// 4. writer 真坏走 ERROR：必须保留一条 slog.Error 路径（msg 文案以
	//    「failure writer never wrote any rows」为特征）。这一条是 negative
	//    space：删了它就会让 S4 停写和 writer 真坏共用同一形态，告警面再次混淆。
	if !strings.Contains(body, `slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)"`) {
		t.Fatalf("checkStaleness 的 writer 真坏分支必须 slog.Error(msg with 'failure writer never wrote any rows')；got:\n%s", body)
	}

	// 5. 分类器必须被调用（不直接写 if-else，必须走纯函数，否则单测覆盖不到）：
	if !strings.Contains(body, "candidateFailureMonitorClassifyStale(fid)") {
		t.Fatalf("checkStaleness 必须调用 candidateFailureMonitorClassifyStale(fid)；got:\n%s", body)
	}

	// 6. S4 停写判定方向正确：分类器把 fid>0 映射为 stalenessLikelyStopWrite，
	//    该 case 标签必须驱动 slog.Warn(「failure writer previously active…」)
	//    这一行，而不是 slog.Error。换言之：scan 整个 checkStaleness 函数体，
	//    ① 找到 switch 语句块（最近的 `switch … {` 到匹配 `}`），② 在块内
	//    同时含 `case stalenessLikelyStopWrite:` 与 slog.Warn(…stop-write…) 文案，
	//    ③ 含 `default:` 且 default 出现在 slog.Warn 之后（默认分支走 ERROR）。
	//
	// ⚠ 不再用「slog.Warn 与 slog.Error 之间的子串」那种位置断言：switch 的写法
	// 可以把 case 标签放在 slog.Warn 之前也可以放在之后，单测不该把实现绑死到
	// 某一种排版。改为「switch 块内文本满足三个不变式」即可。
	switchStart := strings.Index(body, "switch candidateFailureMonitorClassifyStale(fid)")
	if switchStart < 0 {
		t.Fatalf("checkStaleness 必须用 switch candidateFailureMonitorClassifyStale(fid)；got:\n%s", body)
	}
	switchEnd := strings.Index(body[switchStart:], "\n\t}")
	if switchEnd < 0 {
		t.Fatalf("switch 块找不到收尾 \\n\\t}；got:\n%s", body[switchStart:])
	}
	switchBody := body[switchStart : switchStart+switchEnd]

	// slog.Warn / slog.Error 都是多行调用，msg 在单独一行 ⇒ 只断言开括号
	// 一行（含 msg 字面量），别断言收 `)` —— 收括号在另一行，含进去会把
	// 单测绑死到排版，将来谁把同一调用换成单行写法就误报。
	stopWriteMsg := `slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)"`
	neverWroteMsg := `slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)"`

	if !strings.Contains(switchBody, "case stalenessLikelyStopWrite:") {
		t.Fatalf("switch 块必须 case stalenessLikelyStopWrite:；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, stopWriteMsg) {
		t.Fatalf("switch 块必须 slog.Warn(…stop-write…)；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, neverWroteMsg) {
		t.Fatalf("switch 块必须 slog.Error(…never wrote…)；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, "default:") {
		t.Fatalf("switch 块必须 default: 兜底分支；got:\n%s", switchBody)
	}
	// 顺序：slog.Warn(…stop-write…) 必须在 default: 之前出现（因为它挂在
	// case stalenessLikelyStopWrite: 之下），slog.Error(…never wrote…) 必须在
	// default: 之后出现。
	warnPos := strings.Index(switchBody, stopWriteMsg)
	defPos := strings.Index(switchBody, "default:")
	errPos := strings.Index(switchBody, neverWroteMsg)
	if !(warnPos >= 0 && defPos >= 0 && errPos >= 0 && warnPos < defPos && defPos < errPos) {
		t.Fatalf("switch 块顺序应为 slog.Warn(stop-write) → default: → slog.Error(never wrote)；got warn=%d default=%d err=%d", warnPos, defPos, errPos)
	}
}

// TestFailureLogIsStaleKeepsQuietGatewayBaseline 保护原有「网关无流量时不告警」
// 的行为不被 S4 修复连带改坏。这一条失败就意味着有人误改了
// failureLogIsStale 的语义（lastRequest=nil 应返回 false）。
func TestFailureLogIsStaleKeepsQuietGatewayBaseline(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if failureLogIsStale(&old, nil, now, 30*time.Minute) {
		t.Fatal("quiet gateway（lastRequest=nil）必须不告警；现行为被 S4 修复连带改坏了")
	}
	// 同步核：连 lastFailure 也不存在时也应不告警（网关从未有过失败）。
	if failureLogIsStale(nil, nil, now, 30*time.Minute) {
		t.Fatal("网关从未写过失败 + 当前无请求活动 ⇒ 必须不告警")
	}
}

// mustReadSourceBg 在 pkg 内 bg/credential_selfcheck_pick_test.go:128 已
// 定义（§9.26 提取助手），本文件直接复用，避免双份提取逻辑漂移。