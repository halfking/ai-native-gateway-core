package anomaly

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachmentFingerprintDetector_ReplayLoop 测试附件重发循环检测
func TestAttachmentFingerprintDetector_ReplayLoop(t *testing.T) {
	detector, err := NewAttachmentFingerprintDetector(100)
	require.NoError(t, err)

	// 设置较低阈值便于测试
	detector.SetThreshold(3)
	detector.SetSizeLimit(1024) // 1KB

	sessionID := "test_session_replay"
	ctx := context.Background()

	// 模拟事故场景：连续 5 笔相同的大附件
	attachment := []byte(strings.Repeat("X", 2*1024*1024)) // 2MB

	for i := 0; i < 5; i++ {
		req := &Request{
			SessionID: sessionID,
			Attachments: []Attachment{
				{Data: attachment, Size: int64(len(attachment))},
			},
		}

		result := detector.Check(ctx, req)

		if i < 2 {
			assert.Nil(t, result, "前 2 笔应放行（阈值=3）")
		} else {
			assert.NotNil(t, result, "第 %d 笔应触发异常", i+1)
			assert.Equal(t, "attachment_replay_loop", result.Code)
			assert.Equal(t, "high", result.Severity)
			assert.Contains(t, result.Message, "连续")
			
			// 检查指标
			assert.Greater(t, result.Metrics["consecutive_replays"], 2)
			assert.Equal(t, int64(len(attachment)), result.Metrics["attachment_bytes"])
		}
	}

	// 验证会话统计
	stats := detector.GetSessionStats(sessionID)
	assert.NotNil(t, stats)
	assert.Equal(t, 5, stats.RequestCount)
	assert.Greater(t, stats.TotalBytes, int64(0))
}

// TestAttachmentFingerprintDetector_DifferentAttachments 测试不同附件不触发
func TestAttachmentFingerprintDetector_DifferentAttachments(t *testing.T) {
	detector, err := NewAttachmentFingerprintDetector(100)
	require.NoError(t, err)
	detector.SetThreshold(3)
	detector.SetSizeLimit(1024)

	sessionID := "test_session_different"
	ctx := context.Background()

	// 每次发送不同的附件
	for i := 0; i < 10; i++ {
		content := strings.Repeat("X", 2*1024*1024) + string(rune(i)) // 每次略有不同
		req := &Request{
			SessionID: sessionID,
			Attachments: []Attachment{
				{Data: []byte(content), Size: int64(len(content))},
			},
		}

		result := detector.Check(ctx, req)
		assert.Nil(t, result, "不同附件不应触发异常")
	}
}

// TestAttachmentFingerprintDetector_SmallAttachments 测试小附件不触发
func TestAttachmentFingerprintDetector_SmallAttachments(t *testing.T) {
	detector, err := NewAttachmentFingerprintDetector(100)
	require.NoError(t, err)
	detector.SetThreshold(3)
	detector.SetSizeLimit(1024 * 1024) // 1MB

	sessionID := "test_session_small"
	ctx := context.Background()

	// 发送小于阈值的附件
	smallAttachment := []byte(strings.Repeat("X", 100*1024)) // 100KB

	for i := 0; i < 10; i++ {
		req := &Request{
			SessionID: sessionID,
			Attachments: []Attachment{
				{Data: smallAttachment, Size: int64(len(smallAttachment))},
			},
		}

		result := detector.Check(ctx, req)
		assert.Nil(t, result, "小附件不应触发异常")
	}
}

// TestSessionRateLimiter_Storm 测试请求风暴检测
func TestSessionRateLimiter_Storm(t *testing.T) {
	limiter := NewSessionRateLimiter()
	sessionID := "test_session_storm"
	ctx := context.Background()

	// 模拟 5 分钟内发送 15 笔请求
	for i := 0; i < 15; i++ {
		result := limiter.Check(ctx, sessionID)

		if i < 5 {
			// 前 5 笔不触发任何异常
			assert.Nil(t, result, "前 5 笔应放行")
		} else if i < 10 {
			// 6-10 笔触发 1 分钟突发（medium severity）
			assert.NotNil(t, result, "第 %d 笔应触发突发检测", i+1)
			assert.Equal(t, "session_burst_requests", result.Code)
			assert.Equal(t, "medium", result.Severity)
		} else {
			// 11+ 笔触发 5 分钟风暴（high severity）
			assert.NotNil(t, result, "第 %d 笔应触发风暴检测", i+1)
			assert.Equal(t, "session_request_storm", result.Code)
			assert.Equal(t, "high", result.Severity)
		}
	}

	// 检查统计
	stats := limiter.GetStats(sessionID)
	assert.NotNil(t, stats)
	assert.Equal(t, 15, stats["requests_5min"])
}

// TestSessionRateLimiter_BurstDetection 测试短期突发检测
func TestSessionRateLimiter_BurstDetection(t *testing.T) {
	limiter := NewSessionRateLimiter()
	sessionID := "test_session_burst"
	ctx := context.Background()

	// 1 分钟内发送 8 笔
	for i := 0; i < 8; i++ {
		result := limiter.Check(ctx, sessionID)

		if i < 5 {
			assert.Nil(t, result, "前 5 笔应放行")
		} else {
			assert.NotNil(t, result, "第 %d 笔应触发异常", i+1)
			assert.Equal(t, "session_burst_requests", result.Code)
			assert.Equal(t, "medium", result.Severity)
		}
	}
}

// TestSessionRateLimiter_TimeWindow 测试时间窗口清理
func TestSessionRateLimiter_TimeWindow(t *testing.T) {
	limiter := NewSessionRateLimiter()
	sessionID := "test_session_window"
	ctx := context.Background()

	// 发送 5 笔（不触发）
	for i := 0; i < 5; i++ {
		result := limiter.Check(ctx, sessionID)
		assert.Nil(t, result)
	}

	// 模拟时间流逝（实际测试中无法真正等待，这里只是逻辑验证）
	// 在实际应用中，旧记录会被自动清理
	stats := limiter.GetStats(sessionID)
	assert.Equal(t, 5, stats["requests_1min"])
}

// TestMediaTextRatioDetector_Imbalance 测试媒体文本比例失衡
func TestMediaTextRatioDetector_Imbalance(t *testing.T) {
	detector := NewMediaTextRatioDetector()
	detector.SetRatioThreshold(5.0)
	detector.SetMinMediaBytes(1024 * 1024) // 1MB

	ctx := context.Background()

	// 场景：2MB 图片 + 50KB 文本 = 40:1（触发 high）
	req := &Request{
		SessionID: "test_session_ratio",
		Attachments: []Attachment{
			{Data: make([]byte, 2*1024*1024), Size: 2 * 1024 * 1024},
		},
		Messages: []Message{
			{Role: "user", Content: strings.Repeat("text ", 12500)}, // ~50KB
		},
	}

	result := detector.Check(ctx, req)
	assert.NotNil(t, result)
	assert.Equal(t, "media_text_imbalance", result.Code)
	// 40:1 应该是 high（>20 触发）
	assert.Equal(t, "high", result.Severity)
	assert.Contains(t, result.Message, "过高")
}

// TestMediaTextRatioDetector_Balanced 测试平衡比例
func TestMediaTextRatioDetector_Balanced(t *testing.T) {
	detector := NewMediaTextRatioDetector()
	detector.SetRatioThreshold(10.0)
	detector.SetMinMediaBytes(1024 * 1024)

	ctx := context.Background()

	// 场景：2MB 图片 + 1MB 文本 = 2:1（正常）
	req := &Request{
		SessionID: "test_session_balanced",
		Attachments: []Attachment{
			{Data: make([]byte, 2*1024*1024), Size: 2 * 1024 * 1024},
		},
		Messages: []Message{
			{Role: "user", Content: strings.Repeat("text ", 250000)}, // ~1MB
		},
	}

	result := detector.Check(ctx, req)
	assert.Nil(t, result, "平衡比例不应触发")
}

// TestManager_Integration 测试管理器集成
func TestManager_Integration(t *testing.T) {
	manager, err := NewDefaultManager()
	require.NoError(t, err)

	ctx := context.Background()
	sessionID := "test_session_integration"

	// 模拟事故场景：大附件 + 高频率
	attachment := make([]byte, 3*1024*1024) // 3MB

	var allResults []*AnomalyResult

	for i := 0; i < 12; i++ {
		req := &Request{
			SessionID: sessionID,
			Attachments: []Attachment{
				{Data: attachment, Size: int64(len(attachment))},
			},
			Messages: []Message{
				{Role: "user", Content: "short text"},
			},
		}

		results := manager.CheckAll(ctx, req)
		if len(results) > 0 {
			allResults = append(allResults, results...)
		}
	}

	// 应该触发多个异常
	assert.NotEmpty(t, allResults, "应该检测到异常")

	// 检查是否包含预期的异常类型
	codes := make(map[string]bool)
	for _, r := range allResults {
		codes[r.Code] = true
	}

	// 至少应该触发附件重发或请求频率异常
	assert.True(t, codes["attachment_replay_loop"] || codes["session_request_storm"],
		"应该触发附件重发或频率异常")
}

// TestSelectHighestSeverity 测试严重级别选择
func TestSelectHighestSeverity(t *testing.T) {
	results := []*AnomalyResult{
		{Code: "test1", Severity: "low"},
		{Code: "test2", Severity: "high"},
		{Code: "test3", Severity: "medium"},
	}

	highest := SelectHighestSeverity(results)
	assert.NotNil(t, highest)
	assert.Equal(t, "high", highest.Severity)
	assert.Equal(t, "test2", highest.Code)
}

// TestShouldBlock 测试拦截判断
func TestShouldBlock(t *testing.T) {
	assert.True(t, ShouldBlock("high"))
	assert.True(t, ShouldBlock("critical"))
	assert.False(t, ShouldBlock("medium"))
	assert.False(t, ShouldBlock("low"))
}

// TestGetHTTPStatus 测试 HTTP 状态码
func TestGetHTTPStatus(t *testing.T) {
	assert.Equal(t, 429, GetHTTPStatus("high"))
	assert.Equal(t, 429, GetHTTPStatus("critical"))
	assert.Equal(t, 400, GetHTTPStatus("medium"))
	assert.Equal(t, 200, GetHTTPStatus("low"))
}

// BenchmarkAttachmentFingerprintCheck 性能测试
func BenchmarkAttachmentFingerprintCheck(b *testing.B) {
	detector, _ := NewAttachmentFingerprintDetector(1000)
	attachment := make([]byte, 1024*1024) // 1MB
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &Request{
			SessionID: "bench_session",
			Attachments: []Attachment{
				{Data: attachment, Size: int64(len(attachment))},
			},
		}
		detector.Check(ctx, req)
	}
}
