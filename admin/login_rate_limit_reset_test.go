package admin

import (
	"testing"
	"time"
)

// TestLoginLimiterResetsOnSuccessContract —— 钉死「成功登录必须 Reset」。
//
// 回归：JWT 成功路径曾只 clearLoginFailures、不 loginLimiter.Reset，
// Allow() 在入口已计数 → 同一 IP 连登 5 次后整分钟 429（llm.itestu.cn
// 2026-10-17 实锤）。legacy key 路径早有 Reset；本测用同款限流器形状
// 证明 Reset 后可继续 Allow（与 handleLogin 成功分支契约一致）。
func TestLoginLimiterResetsOnSuccessContract(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	key := "192.168.31.50"
	for i := 0; i < 5; i++ {
		if !rl.Allow(key) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if rl.Allow(key) {
		t.Fatal("6th attempt must be denied before Reset")
	}
	rl.Reset(key)
	if !rl.Allow(key) {
		t.Fatal("after Reset (success-login contract) next attempt must be allowed")
	}
}
