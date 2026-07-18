package safety

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContentFilter_APIKeyDetection 测试 API Key 检测
func TestContentFilter_APIKeyDetection(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	tests := []struct {
		name       string
		content    string
		wantSafe   bool
		wantAction Action
	}{
		{
			name:       "OpenAI API Key",
			content:    "My key is sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGH",
			wantSafe:   false,
			wantAction: ActionBlock,
		},
		{
			name:       "Anthropic API Key",
			content:    "使用这个key: sk-ant-api03-aBcDeFgHiJkLmNoPqRsTuVwXyZ1234567890aBcDeFgHiJkLmNoPqRsTuVwXyZ1234567890aBcDeFgHiJkLmN",
			wantSafe:   false,
			wantAction: ActionBlock,
		},
		{
			name:       "Safe content",
			content:    "Hello world",
			wantSafe:   true,
			wantAction: ActionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := filter.CheckRequest(context.Background(), &CheckRequest{
				Content: tt.content,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantSafe, result.Safe)
			assert.Equal(t, tt.wantAction, result.Action)
		})
	}
}

// TestContentFilter_PIIDetection 测试 PII 检测
func TestContentFilter_PIIDetection(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	tests := []struct {
		name       string
		content    string
		wantAction Action
	}{
		{
			name:       "身份证号",
			content:    "我的身份证是 110101199001011234",
			wantAction: ActionSanitize,
		},
		{
			name:       "手机号",
			content:    "联系电话 13800138000",
			wantAction: ActionSanitize,
		},
		{
			name:       "邮箱",
			content:    "我的邮箱是 test@example.com",
			wantAction: ActionWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := filter.CheckRequest(context.Background(), &CheckRequest{
				Content: tt.content,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantAction, result.Action)
		})
	}
}

// TestContentFilter_Sanitize 测试脱敏功能
func TestContentFilter_Sanitize(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	content := "我的手机号是 13800138000"
	result, err := filter.CheckRequest(context.Background(), &CheckRequest{
		Content: content,
	})

	require.NoError(t, err)
	assert.Equal(t, ActionSanitize, result.Action)
	assert.NotEmpty(t, result.SanitizedContent)
	assert.Contains(t, result.SanitizedContent, "***")            // 应包含脱敏标记
	assert.NotContains(t, result.SanitizedContent, "13800138000") // 不应包含原始手机号
}

// TestContentFilter_SQLInjection 测试 SQL 注入检测
func TestContentFilter_SQLInjection(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "UNION 注入",
			content:   "1' UNION SELECT * FROM users--",
			wantBlock: true,
		},
		{
			name:      "DROP TABLE",
			content:   "'; DROP TABLE users; --",
			wantBlock: true,
		},
		{
			name:      "正常 SQL 讨论",
			content:   "我想学习 SQL",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := filter.CheckRequest(context.Background(), &CheckRequest{
				Content: tt.content,
			})
			require.NoError(t, err)

			if tt.wantBlock {
				assert.Equal(t, ActionBlock, result.Action)
			} else {
				assert.NotEqual(t, ActionBlock, result.Action)
			}
		})
	}
}

// TestContentFilter_SensitiveWords 测试敏感词检测
func TestContentFilter_SensitiveWords(t *testing.T) {
	rules := append(BuiltinRules(), SensitiveWordsRules()...)
	filter := NewContentFilter(rules)

	tests := []struct {
		name      string
		content   string
		wantBlock bool
	}{
		{
			name:      "内部机密",
			content:   "这是公司的内部机密文件",
			wantBlock: true,
		},
		{
			name:      "商业秘密",
			content:   "商业秘密不得泄露",
			wantBlock: true,
		},
		{
			name:      "正常内容",
			content:   "今天天气不错",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := filter.CheckRequest(context.Background(), &CheckRequest{
				Content: tt.content,
			})
			require.NoError(t, err)

			if tt.wantBlock {
				assert.Equal(t, ActionBlock, result.Action)
			} else {
				assert.NotEqual(t, ActionBlock, result.Action)
			}
		})
	}
}

// TestContentFilter_UpdateRules 测试规则更新
func TestContentFilter_UpdateRules(t *testing.T) {
	filter := NewContentFilter([]Rule{})

	// 初始应该放行
	result, err := filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "test keyword",
	})
	require.NoError(t, err)
	assert.True(t, result.Safe)

	// 添加规则
	err = filter.UpdateRules([]Rule{
		{
			ID:       "test_rule",
			Name:     "Test Rule",
			Type:     RuleTypeKeyword,
			Pattern:  "keyword",
			Action:   ActionBlock,
			Severity: SeverityHigh,
			Enabled:  true,
		},
	})
	require.NoError(t, err)

	// 现在应该拦截
	result, err = filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "test keyword",
	})
	require.NoError(t, err)
	assert.False(t, result.Safe)
	assert.Equal(t, ActionBlock, result.Action)
}

// TestContentFilter_Metrics 测试统计指标
func TestContentFilter_Metrics(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	// 执行几次检测
	filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "Hello world",
	})
	filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGH",
	})
	filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "13800138000",
	})

	metrics := filter.Metrics()
	assert.Equal(t, int64(3), metrics.TotalChecks)
	assert.Greater(t, metrics.TotalBlocked, int64(0))
	assert.Greater(t, metrics.AverageLatency.Nanoseconds(), int64(0))
}

// TestContentFilter_Concurrent 测试并发安全
func TestContentFilter_Concurrent(t *testing.T) {
	filter := NewContentFilter(BuiltinRules())

	// 100 个 goroutine 并发检测
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			content := "test content"
			if idx%2 == 0 {
				content = "sk-1234567890abcdefghijklmnopqrstuvwxyzABCDEFGH"
			}

			_, err := filter.CheckRequest(context.Background(), &CheckRequest{
				Content: content,
			})
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// 等待所有完成
	for i := 0; i < 100; i++ {
		<-done
	}

	metrics := filter.Metrics()
	assert.Equal(t, int64(100), metrics.TotalChecks)
}

// TestContentFilter_WhiteList 测试白名单
func TestContentFilter_WhiteList(t *testing.T) {
	rules := []Rule{
		{
			ID:        "test_with_whitelist",
			Name:      "Test with whitelist",
			Type:      RuleTypeKeyword,
			Pattern:   "敏感词",
			Action:    ActionBlock,
			Severity:  SeverityHigh,
			Enabled:   true,
			WhiteList: []string{"测试环境"},
		},
	}

	filter := NewContentFilter(rules)

	// 不在白名单，应拦截
	result, err := filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "这是敏感词",
	})
	require.NoError(t, err)
	assert.Equal(t, ActionBlock, result.Action)

	// 在白名单，应放行
	result, err = filter.CheckRequest(context.Background(), &CheckRequest{
		Content: "测试环境中的敏感词",
	})
	require.NoError(t, err)
	assert.Equal(t, ActionAllow, result.Action)
}

// BenchmarkContentFilter_Check 性能测试
func BenchmarkContentFilter_Check(b *testing.B) {
	filter := NewContentFilter(BuiltinRules())
	content := "Hello world, this is a test message with some content"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		filter.CheckRequest(context.Background(), &CheckRequest{
			Content: content,
		})
	}
}

// BenchmarkContentFilter_CheckWithHits 带匹配的性能测试
func BenchmarkContentFilter_CheckWithHits(b *testing.B) {
	filter := NewContentFilter(BuiltinRules())
	content := "我的手机号是 13800138000，邮箱是 test@example.com"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		filter.CheckRequest(context.Background(), &CheckRequest{
			Content: content,
		})
	}
}
