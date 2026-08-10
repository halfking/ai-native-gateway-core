package modelquality

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ModelInvoker 模型调用接口 - 适配网关的实际调用逻辑
//
// 2026-08-10 重构：签名从 (prompt string) 改为 (q Question)。
// 原因：MockModelInvoker 需要题目的真实答案才能按配置的 BaseAccuracy
// 生成正确/错误回答；只传 prompt 时它无法判断"正确答案是什么"，导致
// 模拟准确率退化为 ~25% 随机（4 选 1 瞎蒙）。真实调用器(Gateway/Direct)
// 内部仍用 buildPrompt(q) 把题目转成发给模型的文本。
type ModelInvoker interface {
	// InvokeModel 调用指定供应商的模型回答一道选择题。
	// 返回模型原始回答文本、token 使用量、延迟。答案判定由 executor.parseAnswer 完成。
	InvokeModel(ctx context.Context, provider string, modelName string, q Question) (response string, tokenUsage int, latency time.Duration, err error)
}

// DefaultBenchmarkExecutor 默认基准测试执行器
type DefaultBenchmarkExecutor struct {
	invoker ModelInvoker
	timeout time.Duration

	// probeKind 2026-08-10: 标记本次测试的调用路径（gateway/direct/mock），
	// 写入 BenchmarkReport/QualityScore 以便自检记录区分"通过网关"与"直连"。
	// 未设置时按 invoker 类型自动推断（见 inferProbeKind）。
	probeKind ProbeKind
}

// SetProbeKind 设置本次测试的调用路径标记。供 worker/CLI 显式指定。
func (e *DefaultBenchmarkExecutor) SetProbeKind(k ProbeKind) { e.probeKind = k }

// inferProbeKind 未显式设置时按 invoker 类型推断 ProbeKind。
func inferProbeKind(invoker ModelInvoker, explicit ProbeKind) ProbeKind {
	if explicit != "" {
		return explicit
	}
	switch invoker.(type) {
	case *GatewayModelInvoker:
		return ProbeKindGateway
	case *MockModelInvoker:
		return ProbeKindMock
	default:
		return ""
	}
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
		ID:             uuid.New().String(),
		BenchmarkType:  suite.Type,
		ModelName:      modelName,
		Provider:       provider,
		ProbeKind:      inferProbeKind(e.invoker, e.probeKind), // 2026-08-10: 记录调用路径
		TotalQuestions: len(suite.Questions),
		StartTime:      time.Now(),
		Results:        make([]TestResult, 0, len(suite.Questions)),
		SubjectScores:  make(map[string]float64),
		Status:         "running",
	}
	runQuestion := func(ctx context.Context, q Question) (*TestResult, error) {
		return e.ExecuteQuestion(ctx, modelName, provider, q)
	}
	return e.runSuite(ctx, report, suite, runQuestion)
}

// runSuite 是 Execute / ExecuteForNode 共用的测试循环 + 指标汇总。
// runQuestion 负责把一道题发给目标（网关或直连节点）并返回 TestResult。
// 这样 node 维度只需改 report 头和 runQuestion，统计逻辑完全复用。
func (e *DefaultBenchmarkExecutor) runSuite(
	ctx context.Context,
	report *BenchmarkReport,
	suite *BenchmarkSuite,
	runQuestion func(ctx context.Context, q Question) (*TestResult, error),
) (*BenchmarkReport, error) {
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
		result, err := runQuestion(ctx, question)
		if err != nil {
			result = &TestResult{
				QuestionID: question.ID,
				ModelName:  report.ModelName,
				Provider:   report.Provider,
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
			slog.Debug("benchmark progress",
				"completed", i+1,
				"total", len(suite.Questions),
				"model", report.ModelName)
		}
	}

	// 计算最终指标
	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)
	if report.TotalQuestions > 0 {
		report.Accuracy = float64(report.CorrectCount) / float64(report.TotalQuestions) * 100
	}

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

	// 设置超时
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// 调用模型（调用器内部按需 buildPrompt）
	start := time.Now()
	response, tokenUsage, latency, err := e.invoker.InvokeModel(ctx, provider, modelName, q)
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

// NodeExecutor 针对单个凭据节点执行基准测试（直连，绕过网关）。
// 产出带 CredentialID 的 BenchmarkReport，供"单凭据节点智商"聚合使用。
type NodeExecutor struct {
	invoker *DirectNodeInvoker
	node    CredentialNode
	timeout time.Duration
}

// NewNodeExecutor 创建针对指定凭据节点的执行器。
func NewNodeInvoker(invoker *DirectNodeInvoker, node CredentialNode, timeout time.Duration) *NodeExecutor {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &NodeExecutor{invoker: invoker, node: node, timeout: timeout}
}

// Execute 对该节点执行完整基准测试。
func (n *NodeExecutor) Execute(ctx context.Context, suite *BenchmarkSuite) (*BenchmarkReport, error) {
	report := &BenchmarkReport{
		ID:             uuid.New().String(),
		BenchmarkType:  suite.Type,
		ModelName:      n.node.RawModel,
		Provider:       n.node.Provider,
		CredentialID:   n.node.CredentialID,
		CanonicalModel: n.node.RawModel,
		ProbeKind:      ProbeKindDirect, // 2026-08-10: 直连节点，绕过网关
		TotalQuestions: len(suite.Questions),
		StartTime:      time.Now(),
		Results:        make([]TestResult, 0, len(suite.Questions)),
		SubjectScores:  make(map[string]float64),
		Status:         "running",
	}
	dummy := &DefaultBenchmarkExecutor{timeout: n.timeout}
	runQuestion := func(ctx context.Context, q Question) (*TestResult, error) {
		result := &TestResult{
			QuestionID: q.ID,
			ModelName:  report.ModelName,
			Provider:   report.Provider,
			Timestamp:  time.Now(),
		}
		qctx, cancel := context.WithTimeout(ctx, n.timeout)
		defer cancel()
		start := time.Now()
		response, tokenUsage, _, err := n.invoker.InvokeModel(qctx, n.node, q)
		if err != nil {
			result.Error = err.Error()
			result.Latency = time.Since(start).Milliseconds()
			return result, err
		}
		result.Latency = time.Since(start).Milliseconds()
		result.TokenUsage = tokenUsage
		result.Answer = strings.TrimSpace(response)
		parsed := dummy.parseAnswer(result.Answer)
		result.Correct = (parsed == q.Answer)
		return result, nil
	}
	return dummy.runSuite(ctx, report, suite, runQuestion)
}
