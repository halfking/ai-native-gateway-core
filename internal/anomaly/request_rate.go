package anomaly

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// SessionRateLimiter 会话请求频率检测器
// 检测场景：客户端陷入重试循环，短时间内发送大量请求
type SessionRateLimiter struct {
	windows sync.Map // key=session_id, val=*RateWindow
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

// Check 检测请求频率
func (l *SessionRateLimiter) Check(ctx context.Context, sessionID string) *AnomalyResult {
	now := time.Now()

	val, _ := l.windows.LoadOrStore(sessionID, &RateWindow{})
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
			Code:     "session_request_storm",
			Severity: "high",
			Message: fmt.Sprintf("会话请求频率过高：5 分钟内 %d 笔（%.1f/min）",
				recentCount, float64(recentCount)/5.0),
			Metrics: map[string]interface{}{
				"requests_5min":   recentCount,
				"rate_per_min":    float64(recentCount) / 5.0,
				"requests_10min":  len(window.Timestamps),
				"session_id":      sessionID,
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
			Code:     "session_burst_requests",
			Severity: "medium",
			Message: fmt.Sprintf("会话请求突发：1 分钟内 %d 笔",
				oneMinCount),
			Metrics: map[string]interface{}{
				"requests_1min": oneMinCount,
				"rate_per_min":  float64(oneMinCount),
				"session_id":    sessionID,
			},
		}
	}

	return nil
}

// GetStats 获取会话频率统计
func (l *SessionRateLimiter) GetStats(sessionID string) map[string]interface{} {
	val, ok := l.windows.Load(sessionID)
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

// Clear 清除会话窗口
func (l *SessionRateLimiter) Clear(sessionID string) {
	l.windows.Delete(sessionID)
}
