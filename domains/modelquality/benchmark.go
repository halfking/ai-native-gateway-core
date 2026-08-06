package modelquality

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// BenchmarkType 基准测试类型
type BenchmarkType string

const (
	BenchmarkTypeMMLU     BenchmarkType = "mmlu"      // 完整MMLU测试
	BenchmarkTypeMMLULite BenchmarkType = "mmlu_lite" // 精简版MMLU(快速检测)
	BenchmarkTypeCustom   BenchmarkType = "custom"    // 自定义测试
)

// Question 测试题目
type Question struct {
	ID       string   `json:"id"`
	Subject  string   `json:"subject"`  // 学科分类
	Question string   `json:"question"` // 问题文本
	Options  []string `json:"options"`  // 选项 A/B/C/D
	Answer   string   `json:"answer"`   // 正确答案 (A/B/C/D)
}

// BenchmarkSuite 基准测试套件
type BenchmarkSuite struct {
	Type      BenchmarkType `json:"type"`
	Name      string        `json:"name"`
	Version   string        `json:"version"`
	Questions []Question    `json:"questions"`
	CreatedAt time.Time     `json:"created_at"`
}

// TestResult 单次测试结果
type TestResult struct {
	QuestionID string    `json:"question_id"`
	ModelName  string    `json:"model_name"`
	Provider   string    `json:"provider"`
	Answer     string    `json:"answer"`      // 模型回答
	Correct    bool      `json:"correct"`     // 是否正确
	Latency    int64     `json:"latency_ms"`  // 响应延迟(毫秒)
	TokenUsage int       `json:"token_usage"` // Token消耗
	Error      string    `json:"error,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// BenchmarkReport 完整测试报告
type BenchmarkReport struct {
	ID            string             `json:"id"`
	BenchmarkType BenchmarkType      `json:"benchmark_type"`
	ModelName     string             `json:"model_name"`
	Provider      string             `json:"provider"`
	TotalQuestions int               `json:"total_questions"`
	CorrectCount   int               `json:"correct_count"`
	Accuracy       float64           `json:"accuracy"`        // 准确率
	AvgLatency     float64           `json:"avg_latency_ms"`  // 平均延迟
	TotalTokens    int               `json:"total_tokens"`    // 总Token消耗
	StartTime      time.Time         `json:"start_time"`
	EndTime        time.Time         `json:"end_time"`
	Duration       time.Duration     `json:"duration"`
	Results        []TestResult      `json:"results"`
	SubjectScores  map[string]float64 `json:"subject_scores"` // 分学科得分
	Status         string            `json:"status"`         // success/partial/failed
	ErrorCount     int               `json:"error_count"`
}

// QualityScore 质量评分
type QualityScore struct {
	ModelName    string    `json:"model_name"`
	Provider     string    `json:"provider"`
	Accuracy     float64   `json:"accuracy"`      // 准确率 0-100
	Latency      float64   `json:"latency_p95"`   // P95延迟
	Stability    float64   `json:"stability"`     // 稳定性(成功率)
	OverallScore float64   `json:"overall_score"` // 综合评分 0-100
	Grade        string    `json:"grade"`         // A+/A/B+/B/C/D/F
	Timestamp    time.Time `json:"timestamp"`
	BenchmarkID  string    `json:"benchmark_id"`
}

// BenchmarkExecutor 基准测试执行器接口
type BenchmarkExecutor interface {
	// Execute 执行基准测试
	Execute(ctx context.Context, modelName string, provider string, suite *BenchmarkSuite) (*BenchmarkReport, error)
	
	// ExecuteQuestion 执行单个问题测试
	ExecuteQuestion(ctx context.Context, modelName string, provider string, q Question) (*TestResult, error)
}

// ScoreCalculator 评分计算器
type ScoreCalculator struct{}

// CalculateScore 计算质量评分
func (sc *ScoreCalculator) CalculateScore(report *BenchmarkReport) *QualityScore {
	score := &QualityScore{
		ModelName:   report.ModelName,
		Provider:    report.Provider,
		Accuracy:    report.Accuracy,
		Timestamp:   time.Now(),
		BenchmarkID: report.ID,
	}

	// 计算稳定性 (成功率)
	if report.TotalQuestions > 0 {
		successCount := report.TotalQuestions - report.ErrorCount
		score.Stability = float64(successCount) / float64(report.TotalQuestions) * 100
	} else {
		score.Stability = 100
	}

	// 计算P95延迟
	score.Latency = sc.calculateP95Latency(report.Results)

	// 综合评分: 准确率60% + 稳定性30% + 延迟10%
	// 延迟评分: <1000ms=100分, 1000-3000ms线性递减, >5000ms=0分
	latencyScore := sc.latencyToScore(score.Latency)
	score.OverallScore = score.Accuracy*0.6 + score.Stability*0.3 + latencyScore*0.1

	// 评级
	score.Grade = sc.scoreToGrade(score.OverallScore)

	return score
}

// calculateP95Latency 计算P95延迟
func (sc *ScoreCalculator) calculateP95Latency(results []TestResult) float64 {
	if len(results) == 0 {
		return 0
	}

	latencies := make([]int64, 0, len(results))
	for _, r := range results {
		if r.Error == "" {
			latencies = append(latencies, r.Latency)
		}
	}

	if len(latencies) == 0 {
		return 0
	}

	// 排序取95分位
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	p95Index := int(float64(len(latencies)) * 0.95)
	if p95Index >= len(latencies) {
		p95Index = len(latencies) - 1
	}

	return float64(latencies[p95Index])
}

// latencyToScore 延迟转评分
func (sc *ScoreCalculator) latencyToScore(latency float64) float64 {
	if latency < 1000 {
		return 100
	}
	if latency > 5000 {
		return 0
	}
	// 线性递减: 1000-5000ms -> 100-0分
	return 100 - (latency-1000)/4000*100
}

// scoreToGrade 评分转等级
func (sc *ScoreCalculator) scoreToGrade(score float64) string {
	switch {
	case score >= 95:
		return "A+"
	case score >= 90:
		return "A"
	case score >= 85:
		return "B+"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}

// ToJSON 转换为JSON
func (br *BenchmarkReport) ToJSON() (string, error) {
	data, err := json.MarshalIndent(br, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	return string(data), nil
}
