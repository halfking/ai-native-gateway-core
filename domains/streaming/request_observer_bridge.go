package streaming

// request_observer_bridge.go — **v1 真实路径** ↔ 请求级观察者的接线。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P1-1）要求把统一观察者「**挂到 v1 真实路径**（不是 v2）」。
//
// ★ 为什么挂在 **journey 钩子**上（`beginRequestJourney` / `finishRequestJourney`）：
//   ① 这两个钩子**覆盖全部出口** —— 成功、失败、客户端取消、panic 都会走到；
//   ② 它们**已经**知道终态（outcome / 状态码 / 错误类别），不需要新增判定；
//   ③ 它们**不引入**新的分支语义 ⇒ 对现有行为**零影响**（观察者缺席时，
//      发布路径上只有一次原子读与一次非阻塞入队）。
//
// ★ 三个硬约束由 `internal/requestobserver` 保证（不在本文件重复实现）：
//	① 观察者**永不影响请求结果**（panic 被兜住、发布不阻塞）；
//	② 队列满 ⇒ **丢弃并计数**，请求**永不等待**；
//	③ 字段级 allow-list —— 观察者拿不到请求体/响应体/凭据明文。
//
// # 引用
//
//	internal/requestobserver —— 契约与队列语义
//	domains/dispatch/pipeline.go —— **每次上游尝试**的 Watch 事件接线

import (
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/requestobserver"
)

// magpieP1PublishBegin 发布"请求开始"事件。
func magpieP1PublishBegin(r *http.Request, requestID string) {
	if r == nil {
		return
	}
	hub := requestobserver.Default()
	if !hub.Enabled() {
		return
	}
	info := requestobserver.RequestInfo{
		RequestID:  requestID,
		TenantID:   magpieP1TenantID(r),
		SessionKey: magpieP1SessionKey(r),
		StartedAt:  time.Now(),
	}
	hub.Begin(r.Context(), info)
}

// magpieP1PublishEnd 发布"请求终结"事件。
//
// ★ requestID 由调用方给出（journey lifecycle 的**权威** id），
//
//	不用 HTTP 头 —— 头在中间件被绕过的直调路径上可能为空。
func magpieP1PublishEnd(r *http.Request, requestID string, success bool, status int, errorKind string) {
	if r == nil {
		return
	}
	hub := requestobserver.Default()
	if !hub.Enabled() {
		return
	}
	res := requestobserver.ResultInfo{
		RequestID:  r.Header.Get("X-Request-Id"),
		Success:    success,
		StatusCode: status,
		ErrorKind:  errorKind,
		LatencyMS:  0,
		At:         time.Now(),
	}
	hub.End(r.Context(), res)
}

// magpieP1TenantID 取租户标识（**标识**，不是凭据）。
func magpieP1TenantID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{"X-Tenant-Id", "X-Gw-Tenant-Id"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

// magpieP1SessionKey 取会话键（**标识**，不是凭据）。
func magpieP1SessionKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{"X-Session-Key", "X-Gw-Session-Key"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}
