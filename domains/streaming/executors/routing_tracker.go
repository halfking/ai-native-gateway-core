package executors

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// RoutingAttempt 记录单次 upstream 尝试的详情
type RoutingAttempt struct {
	Seq          int    `json:"seq"`
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	CredentialID int64  `json:"credential_id"`
	RawModel     string `json:"raw_model"`
	UpstreamURL  string `json:"upstream_url"`
	Result       string `json:"result"` // success/canceled/timeout/model_not_found/rate_limit/error/pending
	LatencyMs    int64  `json:"latency_ms"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// ResultPending is the placeholder Result used by the handler when it
// pre-populates the tracker with the full candidate pool BEFORE execution
// (streaming/handler.go, "candidate #N from routing"). Those entries are
// book-keeping, not attempts: the executor appends a real record for every
// candidate it actually calls, and the planned pool is independently
// persisted as routing_decision_log.decision_trace (Trace.PlannedCandidates).
const ResultPending = "pending"

// compactForStorage drops candidate-pool placeholder entries (Result ==
// ResultPending) once a real successful attempt exists.
//
// Why this matters beyond tidiness: request_logs.routing_attempts is consumed
// arithmetically. bg/auto_route_settle_worker.go derives
//
//	retry_count = jsonb_array_length(routing_attempts) - 1
//
// so every surviving placeholder inflates the retry count of a request that
// had ZERO failovers — a first-try success with a 10-candidate pool would
// count as 10 retries and poison the auto-route tuning signal. Placeholders
// also defeat the long-standing "single success → store nothing" storage
// optimization below (len(attempts) is never 1 once the pool is loaded).
//
// Placeholders are kept when no attempt succeeded: on total failure the
// untried-candidate tail is diagnostic signal operators do want in the log
// detail view.
func compactForStorage(attempts []RoutingAttempt) []RoutingAttempt {
	hasSuccess := false
	for _, a := range attempts {
		if a.Result == "success" {
			hasSuccess = true
			break
		}
	}
	if !hasSuccess {
		return attempts
	}
	real := make([]RoutingAttempt, 0, len(attempts))
	for _, a := range attempts {
		if a.Result != ResultPending {
			real = append(real, a)
		}
	}
	return real
}

// RoutingAttemptsTracker 累积所有路由尝试，线程安全
type RoutingAttemptsTracker struct {
	mu       sync.Mutex
	attempts []RoutingAttempt
}

// NewRoutingAttemptsTracker 创建新的追踪器
func NewRoutingAttemptsTracker() *RoutingAttemptsTracker {
	return &RoutingAttemptsTracker{
		attempts: make([]RoutingAttempt, 0, 8), // 预分配 8 个候选的空间
	}
}

// Add 线程安全地添加尝试记录
func (t *RoutingAttemptsTracker) Add(attempt RoutingAttempt) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	attempt.Seq = len(t.attempts) + 1
	t.attempts = append(t.attempts, attempt)
}

// Count 返回尝试次数
func (t *RoutingAttemptsTracker) Count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.attempts)
}

// ToJSONBytes 序列化为 JSONB 字节
// 优化：仅在多次尝试或失败时才返回数据，单次成功返回 nil 节省存储。
// 序列化前会经过 compactForStorage 清理（见其注释）——候选池占位条目
// 在成功后剔除，否则 request_logs.routing_attempts 的长度会污染
// auto_route_settle_worker 的 retry_count 推导（长度-1 = 重试数）。
func (t *RoutingAttemptsTracker) ToJSONBytes() ([]byte, error) {
	if t == nil {
		return nil, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.attempts) == 0 {
		return nil, nil
	}

	attempts := compactForStorage(t.attempts)

	// 优化：单次成功不记录（清理占位条目后才是真实的尝试集合）
	if len(attempts) == 1 && attempts[0].Result == "success" {
		return nil, nil
	}
	if len(attempts) == 0 {
		return nil, nil
	}

	data := map[string]interface{}{
		"attempts": attempts,
	}
	return json.Marshal(data)
}

// ToJSONString 序列化为 JSONB 字符串（供 pgx 使用）
func (t *RoutingAttemptsTracker) ToJSONString() string {
	bytes, err := t.ToJSONBytes()
	if err != nil || bytes == nil {
		return ""
	}
	return string(bytes)
}

// Summary 生成人类可读摘要
// 格式: "候选1: 火山方舟(35) 模型未找到 1.5s → 候选2: NVIDIA(18) 取消 120s"
//
// 与 ToJSONBytes 一致：序列化前剔除成功后的候选池占位条目，单次成功
// 不生成摘要（否则首试成功的请求会显示一长串从未真正执行的候选）。
func (t *RoutingAttemptsTracker) Summary() string {
	if t == nil {
		return ""
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.attempts) == 0 {
		return ""
	}

	attempts := compactForStorage(t.attempts)
	if len(attempts) == 0 {
		return ""
	}
	// 优化：单次成功不生成摘要
	if len(attempts) == 1 && attempts[0].Result == "success" {
		return ""
	}

	parts := make([]string, len(attempts))
	for i, a := range attempts {
		latency := formatLatency(a.LatencyMs)
		providerDesc := fmt.Sprintf("%d", a.ProviderID)
		if a.ProviderName != "" {
			providerDesc = a.ProviderName
		}

		resultText := translateResult(a.Result)
		parts[i] = fmt.Sprintf("候选%d: %s(%d) %s %s",
			a.Seq, providerDesc, a.ProviderID, resultText, latency)
	}

	return strings.Join(parts, " → ")
}

// formatLatency 格式化延迟为人类可读形式
func formatLatency(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
}

// translateResult 翻译结果为中文
func translateResult(result string) string {
	switch result {
	case "success":
		return "成功"
	case "canceled":
		return "取消"
	case "timeout":
		return "超时"
	case "model_not_found":
		return "模型未找到"
	case "rate_limit":
		return "速率限制"
	case "unauthorized":
		return "未授权"
	case "error":
		return "错误"
	default:
		return result
	}
}

// ClassifyResult 根据错误信息分类结果
//
// 2026-07-20: previously wrapped err.Error() in a defer-recover() to
// mask a nil-receiver panic on *upstream.Error. The root cause has
// been fixed in upstream/client.go's (*Error).Error() method, which
// now nil-checks the receiver. The recover is no longer needed and
// has been removed; any future panic in err.Error() should be
// investigated rather than silently swallowed.
func ClassifyResult(err error, statusCode int) string {
	if err == nil {
		return "success"
	}

	errMsg := err.Error()

	// 按优先级判断
	if strings.Contains(errMsg, "context canceled") || strings.Contains(errMsg, "canceled") {
		return "canceled"
	}
	if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline exceeded") {
		return "timeout"
	}

	// 根据 HTTP 状态码判断
	switch statusCode {
	case 404:
		return "model_not_found"
	case 429:
		return "rate_limit"
	case 401, 403:
		return "unauthorized"
	case 0:
		// 没有 HTTP 响应，可能是网络错误或取消
		if strings.Contains(errMsg, "EOF") || strings.Contains(errMsg, "connection") {
			return "error"
		}
		return "canceled"
	default:
		return "error"
	}
}
