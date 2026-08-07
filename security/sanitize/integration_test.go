package sanitize_test

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_InputOutputRoundTrip 测试输入脱敏和输出还原的完整流程
func TestIntegration_InputOutputRoundTrip(t *testing.T) {
	ctx := context.Background()

	// 1. 初始化脱敏器
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	// 2. 模拟用户输入（包含敏感信息）
	userInput := "我的手机号是13800138000，邮箱是test@example.com，请帮我查询订单"

	// 3. 输入侧脱敏
	inputResult, err := sanitizer.SanitizeInput(ctx, userInput)
	require.NoError(t, err)
	require.NotEmpty(t, inputResult.SanitizeMap)

	t.Logf("原始输入: %s", userInput)
	t.Logf("脱敏后输入: %s", inputResult.SanitizedText)
	t.Logf("映射表: %+v", inputResult.SanitizeMap)

	// 验证敏感信息被替换
	assert.Contains(t, inputResult.SanitizedText, "{SENSITIVE:phone:")
	assert.Contains(t, inputResult.SanitizedText, "{SENSITIVE:email:")
	assert.NotContains(t, inputResult.SanitizedText, "13800138000")
	assert.NotContains(t, inputResult.SanitizedText, "test@example.com")

	// 4. 模拟LLM响应（可能包含占位符）
	llmResponse := "您的手机号{SENSITIVE:phone:1}和邮箱{SENSITIVE:email:1}已验证，订单状态正常"

	// 5. 输出侧还原
	outputResult, err := sanitizer.RestoreOutput(ctx, llmResponse, inputResult.SanitizeMap)
	require.NoError(t, err)

	t.Logf("LLM响应: %s", llmResponse)
	t.Logf("还原后输出: %s", outputResult)

	// 验证占位符被还原为原始值
	assert.Contains(t, outputResult, "13800138000")
	assert.Contains(t, outputResult, "test@example.com")
	assert.NotContains(t, outputResult, "{SENSITIVE:phone:")
	assert.NotContains(t, outputResult, "{SENSITIVE:email:")
}

// TestIntegration_MultiRoundConversation 测试多轮对话场景
func TestIntegration_MultiRoundConversation(t *testing.T) {
	ctx := context.Background()
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	// 模拟会话级映射表（跨轮次累积）
	sessionMap := make(sanitize.SanitizeMap)

	// 第1轮：用户提供手机号
	input1 := "我的手机号是13800138000"
	result1, err := sanitizer.SanitizeInput(ctx, input1)
	require.NoError(t, err)

	// 合并到会话映射表
	for k, v := range result1.SanitizeMap {
		sessionMap[k] = v
	}

	t.Logf("第1轮 - 映射表大小: %d", len(sessionMap))

	// 第2轮：用户提供邮箱
	input2 := "我的邮箱是test@example.com"
	result2, err := sanitizer.SanitizeInput(ctx, input2)
	require.NoError(t, err)

	// 合并到会话映射表
	for k, v := range result2.SanitizeMap {
		sessionMap[k] = v
	}

	t.Logf("第2轮 - 映射表大小: %d", len(sessionMap))

	// 第3轮：LLM响应引用之前的敏感信息
	llmResponse := "您的手机号{SENSITIVE:phone:1}和邮箱{SENSITIVE:email:1}均已验证"

	// 使用累积的会话映射表还原
	output, err := sanitizer.RestoreOutput(ctx, llmResponse, sessionMap)
	require.NoError(t, err)

	t.Logf("第3轮 - 还原输出: %s", output)

	// 验证跨轮次还原成功
	assert.Contains(t, output, "13800138000")
	assert.Contains(t, output, "test@example.com")
}

// TestIntegration_HookPipeline 测试Hook在Pipeline中的执行
func TestIntegration_HookPipeline(t *testing.T) {
	ctx := context.Background()

	// 1. 初始化组件
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	inputHook, err := sanitize.NewSanitizerInputHook(sanitizer)
	require.NoError(t, err)

	outputHook, err := sanitize.NewSanitizerOutputHook(sanitizer)
	require.NoError(t, err)

	// 2. 模拟Pipeline请求
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("我的身份证是110101199001011234，请帮我办理业务"),
		Metadata:           make(map[string]any),
	}

	// 3. 执行输入Hook
	err = inputHook.Execute(ctx, env)
	require.NoError(t, err)

	t.Logf("脱敏后请求: %s", string(env.TransformedRequest))
	t.Logf("Metadata: %+v", env.Metadata)

	// 验证脱敏
	assert.Contains(t, string(env.TransformedRequest), "{SENSITIVE:id_card:")
	assert.NotContains(t, string(env.TransformedRequest), "110101199001011234")
	assert.Contains(t, env.Metadata, "sanitize_map")

	// 4. 模拟上游响应
	env.UpstreamResponse = []byte("您的身份证{SENSITIVE:id_card:1}已验证成功")

	// 5. 执行输出Hook
	err = outputHook.Execute(ctx, env)
	require.NoError(t, err)

	t.Logf("还原后响应: %s", string(env.UpstreamResponse))

	// 验证还原
	assert.Contains(t, string(env.UpstreamResponse), "110101199001011234")
	assert.NotContains(t, string(env.UpstreamResponse), "{SENSITIVE:id_card:")
}

// TestIntegration_PriorityOrder 测试Hook执行顺序
func TestIntegration_PriorityOrder(t *testing.T) {
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	inputHook, _ := sanitize.NewSanitizerInputHook(sanitizer)
	outputHook, _ := sanitize.NewSanitizerOutputHook(sanitizer)

	// 验证优先级
	assert.Equal(t, 10, inputHook.Priority(), "InputHook应该在PreRouting阶段优先级为10")
	assert.Equal(t, 50, outputHook.Priority(), "OutputHook应该在PostUpstream阶段优先级为50（在OutputCompliance之前）")
}

// TestIntegration_NoSensitiveData 测试无敏感信息的场景
func TestIntegration_NoSensitiveData(t *testing.T) {
	ctx := context.Background()
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	// 不包含敏感信息的输入
	input := "今天天气怎么样？"

	result, err := sanitizer.SanitizeInput(ctx, input)
	require.NoError(t, err)

	// 验证无敏感信息时不做修改
	assert.Equal(t, input, result.SanitizedText)
	assert.Empty(t, result.SanitizeMap)
	assert.Empty(t, result.Fragments)
}

// TestIntegration_PartialPlaceholderRestore 测试部分占位符还原
func TestIntegration_PartialPlaceholderRestore(t *testing.T) {
	ctx := context.Background()
	detector := sanitize.NewPatternDetector()
	sanitizer, err := sanitize.NewSanitizer(detector)
	require.NoError(t, err)

	// 创建映射表（只包含phone）
	sm := sanitize.SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
	}

	// LLM响应中包含phone（可还原）和email（不可还原）
	llmResponse := "您的手机号{SENSITIVE:phone:1}已验证，邮箱{SENSITIVE:email:1}未验证"

	// 还原
	output, err := sanitizer.RestoreOutput(ctx, llmResponse, sm)
	require.NoError(t, err)

	t.Logf("部分还原输出: %s", output)

	// 验证部分还原
	assert.Contains(t, output, "13800138000") // phone已还原
	assert.Contains(t, output, "{SENSITIVE:email:1}") // email保持占位符（因为映射表中没有）
}
