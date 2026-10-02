package sessionv2mirror

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestIsTerminalFailureAcceptedSet（§9.59，2026-10-02）
//
// 这道门钉的是**机制**：`PersistHook` 的首门是
//
//	if !entry.Success && !isTerminalFailure(entry) { return }
//
// 也就是说，「一个失败的请求会不会被镜像进 session_turns」**完全**由
// isTerminalFailure 决定。这个接受集合此前没有任何直接断言：
// `cmd/gateway/dual_read_class_parity_test.go` 钉的是 SQL 分类与钩子的**一致性**，
// `outbox_backfill_contract_test.go` 钉的是两条 v1 腿都套了同一道测试——
// 都没有说清「到底哪几种状态算终态」。
//
// # 为什么它必须是门，而不是注释
//
// 252 生产库实测（§9.59）：非探针 v1 行里 **19 条卡在 request_status='in_progress'**
// 且会话族无孪生；而配对成功的 12,752 行里 **in_progress 一条都没有**。
// 这个「零 in_progress」正是这道首门在工作。
// 但它同时意味着：**任何扩大或收窄这个接受集合的改动，会直接改变镜像覆盖面，
// 而现有测试全绿。** 集合变了没人知道，就像 §9.54.3 那样，直到有人去数行数才发现。
//
// # 它**不**断言什么
//
// 不断言 PersistHook 的其余闸门（合成会话、IsProbeSyntheticEntry、shadow 开关、
// 超时预算），也不断言写入成功率。这里只钉「终态失败的定义」这一个谓词。
func TestIsTerminalFailureAcceptedSet(t *testing.T) {
	sp := func(s string) *string { return &s }

	cases := []struct {
		name  string
		entry *telemetry.RequestLogEntry
		want  bool
		why   string
	}{
		{
			name:  "nil entry 不算终态失败",
			entry: nil,
			want:  false,
			why:   "nil 必须走首门的 !Success 分支之外的短路，不能当成终态",
		},
		{
			name:  "成功的请求永远不算终态失败",
			entry: &telemetry.RequestLogEntry{Success: true, RequestStatus: sp(telemetry.RequestStatusFailure)},
			want:  false,
			why:   "Success=true 时首门的 !Success 已经为假，这里必须也让路，否则会双重跳过",
		},
		{
			name:  "request_status=failure 算终态",
			entry: &telemetry.RequestLogEntry{RequestStatus: sp(telemetry.RequestStatusFailure)},
			want:  true,
			why:   "网关侧明确判失败的终态，必须被镜像",
		},
		{
			name:  "request_status=rate_limited 算终态",
			entry: &telemetry.RequestLogEntry{RequestStatus: sp(telemetry.RequestStatusRateLimited)},
			want:  true,
			why:   "限流是网关拒了客户端，属终态（不是系统错误，但仍结束了这次调用）",
		},
		{
			name:  "request_status=in_progress 不算终态",
			entry: &telemetry.RequestLogEntry{RequestStatus: sp(telemetry.RequestStatusInProgress)},
			want:  false,
			why: "★这条是镜像覆盖面的边界：in_progress 行在会话族永远没有孪生。" +
				"v2 turns 按 request_id 幂等且首次插入后不可更新，镜像占位行会把 " +
				"success=false 永久写死并吞掉后续的成功富化（hook.go 注释所述）",
		},
		{
			name:  "request_status=success 但 Success=false 仍不算终态失败",
			entry: &telemetry.RequestLogEntry{RequestStatus: sp(telemetry.RequestStatusSuccess)},
			want:  false,
			why:   "状态与 Success 矛盾时，集合只认 status 枚举，不猜哪个是真的",
		},
		{
			name:  "error_kind 非空即可算终态（status 不认得时）",
			entry: &telemetry.RequestLogEntry{ErrorKind: sp("provider_error")},
			want:  true,
			why:   "这是「第二个入口」：status 缺失或异常时，error_kind 是唯一的终态证据",
		},
		{
			name:  "error_kind 全空白不算终态",
			entry: &telemetry.RequestLogEntry{ErrorKind: sp("   ")},
			want:  false,
			why:   "TrimSpace 语义：空白串等同于没写，不能因为「有值」就当终态",
		},
		{
			name:  "status 带首尾空白时仍应被识别",
			entry: &telemetry.RequestLogEntry{RequestStatus: sp("  failure  ")},
			want:  true,
			why:   "两侧都做 TrimSpace；只 trim 一边会让带空白的真实终态掉出镜像",
		},
		{
			name:  "两者都缺省不算终态",
			entry: &telemetry.RequestLogEntry{},
			want:  false,
			why:   "最常见形态：早期失败行什么都不填，只能算「开始了但没结束」",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTerminalFailure(c.entry); got != c.want {
				t.Errorf("isTerminalFailure = %v，期望 %v。%s", got, c.want, c.why)
			}
		})
	}
}

// # 为什么这道门不重复钉 PersistHook 的首门
//
// 我第一版在这里**加过一个自指护栏** `TestTerminalGateAndIsTerminalFailureStayConsistent`，
// 里面复刻了一份首门的判定形式（`!Success && !isTerminalFailure`）并断言它。
// 变异验证立刻证明它是**装饰**：
//
//	把 hook.go:71 的真门改成 `if !entry.Success { return }`（无条件拒绝所有失败）
//	→ 我那道护栏**全绿**（它测的是自己那份复刻）
//	→ 真正抓住它的是既有的 TestPersistHook_MirrorsTerminalFailure / ...RateLimited
//
// **一道在真门被改坏后仍然通过的判据，比没有它更坏** —— 它给读代码的人一个
// 「首门已被钉住」的错觉，而实际钉住它的是另一组测试、另一个文件。
// 与其留一个假保证，不如删掉，指向真正在管的那两道。
//
// 所以本文件**只**钉 isTerminalFailure 这一个谓词的接受集合。
// 首门由 hook_test.go 的 TestPersistHook_MirrorsTerminalFailure /
// TestPersistHook_MirrorsTerminalRateLimited 从**真门那一侧**钉住。
