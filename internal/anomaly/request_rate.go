package anomaly

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// SessionRateLimiter 请求频率检测器（观察模式，2026-11-15）。
// 检测场景：客户端陷入重试循环，短时间内发送大量请求。窗口按 api key ID
// 分桶（无 key 身份的请求由调用方跳过、不计数——空桶共享缺陷的消除面）；
// 检出只产出观察信号，客户端限流由按 key RPM 链执法。
type SessionRateLimiter struct {
	windows sync.Map // key=api key ID 十进制字串, val=*RateWindow
}

// RateWindow 请求频率窗口
type RateWindow struct {
	Timestamps []time.Time
	mu         sync.Mutex
}

// NewSessionRateLimiter 创建频率检测器
func NewSessionRateLimiter() *SessionRateLimiter {
	return &SessionRateLimiter{}
}

// Check 检测请求频率。
//
// 2026-11-15 观察模式改判：窗口 key 从 session_id 改为 **api key ID**（空
// session 曾让所有无会话头请求共享一个全局桶——211 事故定案）；结果恒带
// ObserveOnly=true（调用方只记录不拦截）。被观察请求仍计入窗口（检测信号
// 的本意），但观察模式没有拦截 ⇒ 不存在「被拦请求喂窗口」的自锁面。
func (l *SessionRateLimiter) Check(ctx context.Context, keyID int64) *AnomalyResult {
	now := time.Now()

	windowKey := strconv.FormatInt(keyID, 10)
	val, _ := l.windows.LoadOrStore(windowKey, &RateWindow{})
	window := val.(*RateWindow)

	window.mu.Lock()
	defer window.mu.Unlock()

	// 清理 10 分钟外的记录
	cutoff := now.Add(-10 * time.Minute)
	validIdx := 0
	for i, ts := range window.Timestamps {
		if ts.After(cutoff) {
			validIdx = i
			break
		}
	}
	if validIdx > 0 {
		window.Timestamps = window.Timestamps[validIdx:]
	} else if len(window.Timestamps) > 0 && window.Timestamps[0].Before(cutoff) {
		// 所有记录都过期了
		window.Timestamps = []time.Time{}
	}

	// 添加当前时刻
	window.Timestamps = append(window.Timestamps, now)

	// 检查 1：5 分钟内 >10 笔（平均 2/min）
	fiveMinAgo := now.Add(-5 * time.Minute)
	recentCount := 0
	for _, ts := range window.Timestamps {
		if ts.After(fiveMinAgo) {
			recentCount++
		}
	}

	if recentCount > 10 {
		return &AnomalyResult{
			Code:        "session_request_storm",
			Severity:    "high",
			ObserveOnly: true,
			Message: fmt.Sprintf("会话请求频率过高：5 分钟内 %d 笔（%.1f/min）",
				recentCount, float64(recentCount)/5.0),
			Metrics: map[string]interface{}{
				"requests_5min":  recentCount,
				"rate_per_min":   float64(recentCount) / 5.0,
				"requests_10min": len(window.Timestamps),
				"api_key_id":     keyID,
			},
		}
	}

	// 检查 2：1 分钟内 >5 笔（更激进的短期检测）
	oneMinAgo := now.Add(-1 * time.Minute)
	oneMinCount := 0
	for _, ts := range window.Timestamps {
		if ts.After(oneMinAgo) {
			oneMinCount++
		}
	}

	if oneMinCount > 5 {
		return &AnomalyResult{
			Code:        "session_burst_requests",
			Severity:    "medium",
			ObserveOnly: true,
			Message: fmt.Sprintf("会话请求突发：1 分钟内 %d 笔",
				oneMinCount),
			Metrics: map[string]interface{}{
				"requests_1min": oneMinCount,
				"rate_per_min":  float64(oneMinCount),
				"api_key_id":    keyID,
			},
		}
	}

	return nil
}

// GetStats 获取会话频率统计（key = api key ID 的十进制字串）。
func (l *SessionRateLimiter) GetStats(keyID int64) map[string]interface{} {
	val, ok := l.windows.Load(strconv.FormatInt(keyID, 10))
	if !ok {
		return nil
	}

	window := val.(*RateWindow)
	window.mu.Lock()
	defer window.mu.Unlock()

	if len(window.Timestamps) == 0 {
		return nil
	}

	now := time.Now()
	count1m, count5m, count10m := 0, 0, 0
	oneMinAgo := now.Add(-1 * time.Minute)
	fiveMinAgo := now.Add(-5 * time.Minute)
	tenMinAgo := now.Add(-10 * time.Minute)

	for _, ts := range window.Timestamps {
		if ts.After(oneMinAgo) {
			count1m++
		}
		if ts.After(fiveMinAgo) {
			count5m++
		}
		if ts.After(tenMinAgo) {
			count10m++
		}
	}

	return map[string]interface{}{
		"requests_1min":  count1m,
		"requests_5min":  count5m,
		"requests_10min": count10m,
		"rate_1min":      float64(count1m),
		"rate_5min":      float64(count5m) / 5.0,
		"rate_10min":     float64(count10m) / 10.0,
	}
}

// Clear 清除会话窗口（key = api key ID）。
func (l *SessionRateLimiter) Clear(keyID int64) {
	l.windows.Delete(strconv.FormatInt(keyID, 10))
}
