package modelquality

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ModelInvoker 模型调用接口 - 适配网关的实际调用逻辑
type ModelInvoker interface {
	// InvokeModel 调用指定供应商的模型
	InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error)
}

// DefaultBenchmarkExecutor 默认基准测试执行器
type DefaultBenchmarkExecutor struct {
	invoker ModelInvoker
	timeout time.Duration
}

// NewBenchmarkExecutor 创建基准测试执行器
func NewBenchmarkExecutor(invoker ModelInvoker, timeout time.Duration) *DefaultBenchmarkExecutor {
	if timeout == 0 {
		timeout = 30 * time.Second // 默认30秒超时
	}
	return &DefaultBenchmarkExecutor{
		invoker: invoker,
		timeout: timeout,
	}
}

// Execute 执行完整基准测试
func (e *DefaultBenchmarkExecutor) Execute(ctx context.Context, modelName string, provider string, suite *BenchmarkSuite) (*BenchmarkReport, error) {
	report := &BenchmarkReport{
		ID:            uuid.New().String(),
		BenchmarkType: suite.Type,
		ModelName:     modelName,
		Provider:      provider,
		TotalQuestions: len(suite.Questions),
		StartTime:     time.Now(),
		Results:       make([]TestResult, 0, len(suite.Questions)),
		SubjectScores: make(map[string]float64),
		Status:        "running",
	}

	// 按学科统计
	subjectStats := make(map[string]*subjectStat)

	// 逐题测试
	for i, question := range suite.Questions {
		// 检查上下文取消
		select {
		case <-ctx.Done():
			report.Status = "cancelled"
			report.EndTime = time.Now()
			report.Duration = report.EndTime.Sub(report.StartTime)
			return report, ctx.Err()
		default:
		}

		// 执行单题测试
		result, err := e.ExecuteQuestion(ctx, modelName, provider, question)
		if err != nil {
			result = &TestResult{
				QuestionID: question.ID,
				ModelName:  modelName,
				Provider:   provider,
				Error:      err.Error(),
				Timestamp:  time.Now(),
			}
			report.ErrorCount++
		}

		report.Results = append(report.Results, *result)

		// 更新统计
		if result.Error == "" {
			report.TotalTokens += result.TokenUsage
			if result.Correct {
				report.CorrectCount++
			}

			// 学科统计
			if _, exists := subjectStats[question.Subject]; !exists {
				subjectStats[question.Subject] = &subjectStat{}
			}
			subjectStats[question.Subject].total++
			if result.Correct {
				subjectStats[question.Subject].correct++
			}
		}

		// 进度日志 (每10题打印一次)
		if (i+1)%10 == 0 || i == len(suite.Questions)-1 {
			fmt.Printf("[Benchmark Progress] %d/%d questions completed\n", i+1, len(suite.Questions))
		}
	}

	// 计算最终指标
	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)
	report.Accuracy = float64(report.CorrectCount) / float64(report.TotalQuestions) * 100

	// 计算平均延迟
	var totalLatency int64
	validCount := 0
	for _, r := range report.Results {
		if r.Error == "" {
			totalLatency += r.Latency
			validCount++
		}
	}
	if validCount > 0 {
		report.AvgLatency = float64(totalLatency) / float64(validCount)
	}

	// 计算分学科得分
	for subject, stat := range subjectStats {
		if stat.total > 0 {
			report.SubjectScores[subject] = float64(stat.correct) / float64(stat.total) * 100
		}
	}

	// 判断最终状态
	if report.ErrorCount == 0 {
		report.Status = "success"
	} else if report.ErrorCount < report.TotalQuestions {
		report.Status = "partial"
	} else {
		report.Status = "failed"
	}

	return report, nil
}

// ExecuteQuestion 执行单个问题测试
func (e *DefaultBenchmarkExecutor) ExecuteQuestion(ctx context.Context, modelName string, provider string, q Question) (*TestResult, error) {
	result := &TestResult{
		QuestionID: q.ID,
		ModelName:  modelName,
		Provider:   provider,
		Timestamp:  time.Now(),
	}

	// 构造prompt
	prompt := e.buildPrompt(q)

	// 设置超时
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// 调用模型
	start := time.Now()
	response, tokenUsage, latency, err := e.invoker.InvokeModel(ctx, provider, modelName, prompt)
	if err != nil {
		result.Error = err.Error()
		result.Latency = time.Since(start).Milliseconds()
		return result, err
	}

	result.Latency = latency.Milliseconds()
	result.TokenUsage = tokenUsage
	result.Answer = strings.TrimSpace(response)

	// 解析答案并判断正确性
	parsedAnswer := e.parseAnswer(result.Answer)
	result.Correct = (parsedAnswer == q.Answer)

	return result, nil
}

// buildPrompt 构造测试prompt
func (e *DefaultBenchmarkExecutor) buildPrompt(q Question) string {
	var sb strings.Builder
	
	sb.WriteString("Answer the following multiple choice question by selecting the correct option (A, B, C, or D).\n\n")
	sb.WriteString("Question: ")
	sb.WriteString(q.Question)
	sb.WriteString("\n\nOptions:\n")
	
	for i, opt := range q.Options {
		letter := string(rune('A' + i))
		sb.WriteString(fmt.Sprintf("%s. %s\n", letter, opt))
	}
	
	sb.WriteString("\nPlease respond with ONLY the letter of the correct answer (A, B, C, or D). Do not include any explanation.")
	
	return sb.String()
}

// parseAnswer 解析模型返回的答案
func (e *DefaultBenchmarkExecutor) parseAnswer(response string) string {
	// 清理响应
	response = strings.TrimSpace(response)
	
	// 策略0: JSON格式 {"answer": "A"} (在大写转换前检查)
	if strings.HasPrefix(response, "{") && strings.HasSuffix(response, "}") {
		var jsonResp map[string]interface{}
		if err := json.Unmarshal([]byte(response), &jsonResp); err == nil {
			if ans, ok := jsonResp["answer"].(string); ok {
				ans = strings.ToUpper(strings.TrimSpace(ans))
				if len(ans) > 0 && ans[0] >= 'A' && ans[0] <= 'D' {
					return string(ans[0])
				}
			}
		}
	}
	
	// 转换为大写用于后续匹配
	responseUpper := strings.ToUpper(response)
	
	// 策略1: 直接匹配单个字母
	if len(responseUpper) == 1 && responseUpper >= "A" && responseUpper <= "D" {
		return responseUpper
	}
	
	// 策略2: 匹配 "答案是A" 或 "The answer is A"
	if strings.Contains(responseUpper, "ANSWER IS ") {
		parts := strings.Split(responseUpper, "ANSWER IS ")
		if len(parts) > 1 {
			candidate := strings.TrimSpace(parts[1])
			if len(candidate) > 0 && candidate[0] >= 'A' && candidate[0] <= 'D' {
				return string(candidate[0])
			}
		}
	}
	
	// 策略3: 匹配开头的字母
	if len(responseUpper) > 0 && responseUpper[0] >= 'A' && responseUpper[0] <= 'D' {
		return string(responseUpper[0])
	}
	
	// 无法解析,返回空字符串
	return ""
}

// subjectStat 学科统计
type subjectStat struct {
	total   int
	correct int
}
