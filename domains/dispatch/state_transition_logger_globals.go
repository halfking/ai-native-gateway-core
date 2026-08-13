// Package dispatch — state_transition_logger_globals.go
//
// V3.2 (2026-08-14) 配套 helpers：
// 全局 StateTransitionLogger 单例 + nil-safe Log* helpers。
// 避免在 streaming / streamretry / 各 executor 里到处注入 logger 字段。
// 主流程在 main.go 调 SetGlobalStateTransitionLogger 一次性注入。
// 未注入时（DB 禁用 / 测试模式）所有 Log* 变成 no-op，不阻塞 relay。
package dispatch

import "sync"

// globalStateTransitionLogger 是 process-wide 单例，由 main.go 注入。
// 调用方通过 LogRouteDecisionGlobal / LogRetryGlobal 等 helpers 间接使用，
// 保证 nil 时安全。
var (
	globalStateTransitionLoggerMu sync.RWMutex
	globalStateTransitionLogger   *StateTransitionLogger
)

// SetGlobalStateTransitionLogger 注入全局 logger。传 nil 表示清空（disabled）。
// main.go 启动时调用一次；测试可反复设。
func SetGlobalStateTransitionLogger(l *StateTransitionLogger) {
	globalStateTransitionLoggerMu.Lock()
	globalStateTransitionLogger = l
	globalStateTransitionLoggerMu.Unlock()
}

// LogRouteDecisionGlobal 在 route_resolve / 节点选择 时记录。
// nil-safe（logger 未注入时静默 return）。
func LogRouteDecisionGlobal(requestID, tenantID, fromState, toState string, metadata map[string]any) {
	l := getGlobalStateTransitionLogger()
	if l == nil {
		return
	}
	l.LogRouteDecision(requestID, tenantID, fromState, toState, metadata)
}

// LogNodeSwitchGlobal 在节点切换（failover / sibling 切换）时记录。
// nil-safe。
func LogNodeSwitchGlobal(requestID, tenantID, fromNode, toNode string, metadata map[string]any) {
	l := getGlobalStateTransitionLogger()
	if l == nil {
		return
	}
	l.LogNodeSwitch(requestID, tenantID, fromNode, toNode, metadata)
}

// LogRetryGlobal 在 streamretry 重试时记录。
// nil-safe。
func LogRetryGlobal(requestID, tenantID string, retrySeq int, reasonClass string, metadata map[string]any) {
	l := getGlobalStateTransitionLogger()
	if l == nil {
		return
	}
	l.LogRetry(requestID, tenantID, retrySeq, reasonClass, metadata)
}

// LogErrorGlobal 在终态错误时记录。
// nil-safe。
func LogErrorGlobal(requestID, tenantID, fromState string, metadata map[string]any) {
	l := getGlobalStateTransitionLogger()
	if l == nil {
		return
	}
	l.LogError(requestID, tenantID, fromState, metadata)
}

func getGlobalStateTransitionLogger() *StateTransitionLogger {
	globalStateTransitionLoggerMu.RLock()
	defer globalStateTransitionLoggerMu.RUnlock()
	return globalStateTransitionLogger
}
