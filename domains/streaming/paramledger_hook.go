package streaming

// paramledger_hook.go — 流式响应的参数回显还原接入（2026-09-22）。
//
// StreamNativeResponsesSSE 逐帧转发上游 Responses SSE；response.created /
// in_progress / completed 等生命周期帧的 data 是完整 Response 对象，会
// 回显上游实际收到的 reasoning.effort。若出站时 paramguard/reqprobe 曾
// 降级该参数（x-high→high），客户端会看到降级值。本钩子在写回前按
// request_id 查参数账本并还原为客户端原始值。
//
// 账本实例由 cmd/gateway/main.go 启动时注入（与 Executor.ParamLedger
// 同一实例：Full 模式带 Redis 镜像，lite 模式纯内存）。nil 时零开销。

import "github.com/kaixuan/llm-gateway-go/internal/paramledger"

var paramLedgerSingleton *paramledger.Ledger

// SetParamLedger 注入进程级参数账本（幂等，启动时调用一次）。
func SetParamLedger(l *paramledger.Ledger) { paramLedgerSingleton = l }

// ParamLedger 返回当前账本（可能 nil；测试用）。
func ParamLedger() *paramledger.Ledger { return paramLedgerSingleton }

// restoreEchoFrame 还原单帧 SSE 原始字节中的 reasoning.effort 回显。
// 无账本 / 无调整 / 帧不含该字段时原样返回。
func restoreEchoFrame(raw []byte, requestID string) []byte {
	if paramLedgerSingleton == nil || len(raw) == 0 || requestID == "" {
		return raw
	}
	return paramLedgerSingleton.RestoreResponsesEffort(raw, requestID)
}
