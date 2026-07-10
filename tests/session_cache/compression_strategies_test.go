// Package session_cache_test - compression_strategies_test.go
//
// 测试多种压缩策略的效果
package session_cache_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────────────────────────────────────
// 测试：增量压缩
// ──────────────────────────────────────────────────────────────────────────────

func TestIncrementalCompression(t *testing.T) {
	ctx := context.Background()
	compressor := NewIncrementalCompressor()

	// 场景1：首次压缩（无已有摘要）
	t.Run("FirstTimeCompression", func(t *testing.T) {
		messages := generateMessages(15)
		compressed, summary, newIndex, err := compressor.Compress(ctx, messages, "", 0)

		require.NoError(t, err)
		assert.NotEmpty(t, summary, "首次压缩应生成摘要")
		assert.Equal(t, 11, len(compressed), "15条消息应该压缩成1条摘要+10条保留")
		assert.Equal(t, 5, newIndex, "摘要应覆盖前5条消息")
		t.Logf("✓ 首次压缩: %d→%d条消息", len(messages), len(compressed))
	})

	// 场景2：增量压缩（复用摘要）
	t.Run("IncrementalReuse", func(t *testing.T) {
		// 已有摘要，覆盖前5条消息
		existingSummary := "[会话摘要] 主要话题：API使用方法"
		lastIndex := 5

		// 新增3条消息（总共8条，新增3条<增量阈值5）
		newMessages := generateMessages(8)
		_, summary, _, err := compressor.Compress(ctx, newMessages, existingSummary, lastIndex)

		require.NoError(t, err)
		assert.Equal(t, existingSummary, summary, "应复用已有摘要")
		t.Logf("✓ 增量复用: %d条消息，新增%d条，复用摘要，未重新生成",
			len(newMessages), len(newMessages)-lastIndex)
	})

	// 场景3：增量超过阈值，强制重新压缩
	t.Run("IncrementalThresholdExceeded", func(t *testing.T) {
		existingSummary := "[旧摘要]"
		lastIndex := 5

		// 新增10条消息（超过增量阈值5）
		newMessages := generateMessages(15)
		_, summary, _, err := compressor.Compress(ctx, newMessages, existingSummary, lastIndex)

		require.NoError(t, err)
		assert.NotEqual(t, existingSummary, summary, "应生成新摘要")
		t.Logf("✓ 阈值触发: 新增10条，重新生成摘要")
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 测试：智能滑动窗口
// ──────────────────────────────────────────────────────────────────────────────

func TestSmartSlidingWindow(t *testing.T) {
	ctx := context.Background()
	window := NewSmartSlidingWindow()

	// 场景1：消息数在窗口内
	t.Run("WithinWindow", func(t *testing.T) {
		messages := generateMessages(10)
		compressed, dropped, err := window.Compress(ctx, messages)

		require.NoError(t, err)
		assert.LessOrEqual(t, len(compressed), len(messages))
		t.Logf("✓ 窗口内: %d→%d条，丢弃%d条", len(messages), len(compressed), len(dropped))
	})

	// 场景2：消息数超过窗口
	t.Run("ExceedsWindow", func(t *testing.T) {
		messages := generateMessagesWithImportance(50)
		compressed, dropped, err := window.Compress(ctx, messages)

		require.NoError(t, err)
		assert.Less(t, len(compressed), len(messages), "应该丢弃一些消息")
		assert.Greater(t, len(dropped), 0, "应该有被丢弃的消息")
		t.Logf("✓ 超出窗口: 50→%d条，丢弃%d条（%.1f%%）",
			len(compressed), len(dropped),
			float64(len(dropped))/float64(len(messages))*100)
	})

	// 场景3：保留重要消息
	t.Run("PreservesImportantMessages", func(t *testing.T) {
		messages := []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好！有什么可以帮您？"},
			{Role: "user", Content: "如何实现JWT认证？"},
			{Role: "assistant", Content: "JWT认证需要以下步骤..."},
			{Role: "user", Content: "谢谢"},
			{Role: "assistant", Content: "不客气！"},
		}

		importances := window.calculateImportances(messages)

		// 验证重要消息评分更高
		jwtScore := importances[2].Score
		greetingScore := importances[0].Score
		assert.Greater(t, jwtScore, greetingScore, "JWT认证消息应比寒暄更重要")
		t.Logf("✓ 重要性评分: JWT=%f > 寒暄=%f", jwtScore, greetingScore)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 测试：混合压缩策略
// ──────────────────────────────────────────────────────────────────────────────

func TestHybridCompression(t *testing.T) {
	ctx := context.Background()
	hybrid := NewHybridCompressor()

	// 场景1：选择策略
	t.Run("StrategySelection", func(t *testing.T) {
		testCases := []struct {
			name     string
			msgCount int
			tokens   int
			expected CompressionStrategy
		}{
			{"短对话", 5, 500, StrategyNone},
			{"中等对话", 20, 3000, StrategyIncremental},
			{"较长对话", 60, 8000, StrategySlidingWin},
			{"超长对话", 150, 20000, StrategySummary},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				strategy := hybrid.SelectStrategy(tc.msgCount, tc.tokens)
				assert.Equal(t, tc.expected, strategy)
				t.Logf("✓ %s: %d消息/%dtokens → %s",
					tc.name, tc.msgCount, tc.tokens, strategy)
			})
		}
	})

	// 场景2：完整压缩流程
	t.Run("FullHybridCompression", func(t *testing.T) {
		raw := &RawSession{
			SessionID:     "test_hybrid",
			TenantID:      "tenant_001",
			Messages:      generateMessages(60), // 60条消息，超过30条阈值
			TotalTokens:   8000,
			MessageHashes: make([]string, 60),
		}
		for i := range raw.MessageHashes {
			raw.MessageHashes[i] = hashMessage(raw.Messages[i])
		}

		compressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
		require.NoError(t, err)
		assert.NotNil(t, compressed)
		assert.Less(t, len(compressed.CompressedMessages), len(raw.Messages))
		t.Logf("✓ 混合压缩: 策略=%s, 压缩比=%.2f, 节省%.1f%%",
			compressed.Strategy,
			compressed.CompressionRatio,
			(1-compressed.CompressionRatio)*100)
	})

	// 场景3：不进行二次压缩
	t.Run("NoSecondaryCompression", func(t *testing.T) {
		messages := []Message{
			{Role: "system", Content: "[会话摘要] 主要话题：API认证"},
			{Role: "user", Content: "新问题"},
			{Role: "assistant", Content: "回答"},
		}

		strategy := hybrid.SelectStrategy(len(messages), 1000)
		assert.Equal(t, StrategyNone, strategy)
		t.Logf("✓ 不二次压缩: 短对话（含摘要）选择策略=%s", strategy)
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// 测试：压缩效果对比
// ──────────────────────────────────────────────────────────────────────────────

func TestCompressionEffectiveness(t *testing.T) {
	ctx := context.Background()
	hybrid := NewHybridCompressor()

	sizes := []int{5, 20, 50, 100, 200, 500}

	t.Log("=== 压缩效果对比 ===")
	t.Log("消息数 | 策略          | 压缩后 | Token节省")
	t.Log("-------|---------------|--------|----------")

	for _, size := range sizes {
		raw := &RawSession{
			SessionID:     fmt.Sprintf("test_%d", size),
			Messages:      generateMessages(size),
			TotalTokens:   size * 150,
			MessageHashes: make([]string, size),
		}
		for i := range raw.MessageHashes {
			raw.MessageHashes[i] = hashMessage(raw.Messages[i])
		}

		compressed, err := hybrid.CompressWithHybrid(ctx, raw, "", 0)
		require.NoError(t, err)

		saving := (1 - compressed.CompressionRatio) * 100
		t.Logf("%6d | %-13s | %6d | %5.1f%%",
			size,
			compressed.Strategy,
			len(compressed.CompressedMessages),
			saving)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────────────────────────

func generateMessages(count int) []Message {
	messages := make([]Message, count)
	for i := 0; i < count; i++ {
		if i%2 == 0 {
			messages[i] = Message{
				Role:    "user",
				Content: fmt.Sprintf("用户问题 #%d: 这是测试消息", i+1),
			}
		} else {
			messages[i] = Message{
				Role:    "assistant",
				Content: fmt.Sprintf("助手回答 #%d: 这是测试回复内容", i+1),
			}
		}
	}
	return messages
}

func generateMessagesWithImportance(count int) []Message {
	messages := make([]Message, count)
	templates := []struct {
		user      string
		assistant string
	}{
		{"你好", "你好！"},
		{"如何实现JWT认证？需要详细步骤", "JWT认证需要以下步骤...（详细说明）"},
		{"谢谢", "不客气！"},
		{"遇到Error: TS-999，如何解决？", "这个错误通常由以下原因导致...（详细分析）"},
		{"嗯", "请继续"},
		{"请总结一下我们的对话", "本次对话主要讨论了...（总结）"},
	}

	for i := 0; i < count; i++ {
		t := templates[i%len(templates)]
		if i%2 == 0 {
			messages[i] = Message{Role: "user", Content: t.user}
		} else {
			messages[i] = Message{Role: "assistant", Content: t.assistant}
		}
	}
	return messages
}

// 防止 unused 警告
var _ = time.Now
