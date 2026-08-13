// Package admin — node_operations_ratelimit.go
//
// V3.2-LP5 (2026-08-14) 节点操作限流：test-now 1req/s per-cred + 10req/min per-operator。
// 使用 golang.org/x/time/rate 实现 token bucket。
package admin

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// nodeOperationsRateLimiter 管理两层限流：per-credential + per-operator。
type nodeOperationsRateLimiter struct {
	mu sync.Mutex

	// perCredential: key=credential_id, value=limiter (1 req/s burst=1)
	perCredential map[int]*rate.Limiter

	// perOperator: key=operator_id (从 X-Operator-ID 或 auth 主体提取), value=limiter (10 req/min burst=3)
	perOperator map[string]*rate.Limiter
}

// newNodeOperationsRateLimiter 创建双层限流器。
func newNodeOperationsRateLimiter() *nodeOperationsRateLimiter {
	return &nodeOperationsRateLimiter{
		perCredential: make(map[int]*rate.Limiter),
		perOperator:   make(map[string]*rate.Limiter),
	}
}

// checkTestNow 检查 test-now 限流 (1 req/s per-cred + 10 req/min per-operator)。
// 返回 true=通过，false=超限。
func (rl *nodeOperationsRateLimiter) checkTestNow(ctx context.Context, credentialID int, operatorID string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// 1. per-credential: 1 req/s, burst=1
	credLimiter, exists := rl.perCredential[credentialID]
	if !exists {
		credLimiter = rate.NewLimiter(rate.Every(1*time.Second), 1)
		rl.perCredential[credentialID] = credLimiter
	}
	if !credLimiter.Allow() {
		return fmt.Errorf("rate limit exceeded: credential %d allows 1 req/s (credential_id=%d)", credentialID, credentialID)
	}

	// 2. per-operator: 10 req/min = 1 req/6s, burst=3
	operatorLimiter, exists := rl.perOperator[operatorID]
	if !exists {
		operatorLimiter = rate.NewLimiter(rate.Every(6*time.Second), 3)
		rl.perOperator[operatorID] = operatorLimiter
	}
	if !operatorLimiter.Allow() {
		return fmt.Errorf("rate limit exceeded: operator %s allows 10 req/min (operator_id=%s)", operatorID, operatorID)
	}

	return nil
}

// extractOperatorID 从请求中提取 operator 标识（优先 X-Operator-ID header，兜底用 RemoteAddr）。
func extractOperatorID(r *http.Request) string {
	if opID := r.Header.Get("X-Operator-ID"); opID != "" {
		return opID
	}
	// 兜底用 RemoteAddr（IP:port 格式）
	return r.RemoteAddr
}
