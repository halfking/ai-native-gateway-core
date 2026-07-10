package security_engine

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestReport 完整测试报告
type TestReport struct {
	GeneratedAt      time.Time          `json:"generated_at"`
	TestCases        []TestResult       `json:"test_cases"`
	Performance      PerformanceMetrics `json:"performance"`
	Accuracy         AccuracyMetrics    `json:"accuracy"`
	Recommendations  []string           `json:"recommendations"`
	DetailedFailures []DetailedFailure  `json:"detailed_failures"`
}

// DetailedFailure 详细失败信息
type DetailedFailure struct {
	TestCaseID      int      `json:"test_case_id"`
	Category        string   `json:"category"`
	Content         string   `json:"content"`
	ExpectedIntent  string   `json:"expected_intent"`
	ActualIntent    string   `json:"actual_intent"`
	ExpectedThreats []string `json:"expected_threats"`
	ActualThreats   []string `json:"actual_threats"`
	ExpectedAllow   bool     `json:"expected_allow"`
	ActualAllow     bool     `json:"actual_allow"`
	ErrorMsg        string   `json:"error_msg"`
}

// saveResultsToJSON 保存测试结果到JSON文件
func saveResultsToJSON(t *testing.T, results []TestResult, perf PerformanceMetrics, acc AccuracyMetrics, recs []string) {
	// 提取失败用例详情
	failures := []DetailedFailure{}
	for _, r := range results {
		if !r.Passed {
			failures = append(failures, DetailedFailure{
				TestCaseID:      r.TestCaseID,
				Category:        r.Category,
				Content:         r.Content,
				ExpectedIntent:  r.ExpectedIntent,
				ActualIntent:    r.ActualIntent,
				ExpectedThreats: r.ExpectedThreats,
				ActualThreats:   r.ActualThreats,
				ExpectedAllow:   r.ExpectedAllow,
				ActualAllow:     r.ActualAllow,
				ErrorMsg:        r.ErrorMsg,
			})
		}
	}

	report := TestReport{
		GeneratedAt:      time.Now(),
		TestCases:        results,
		Performance:      perf,
		Accuracy:         acc,
		Recommendations:  recs,
		DetailedFailures: failures,
	}

	// 保存到文件
	filename := fmt.Sprintf("security_engine_test_report_%s.json", time.Now().Format("20060102_150405"))
	filepath := filename // 直接保存在当前目录

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("序列化报告失败: %v", err)
		return
	}

	err = os.WriteFile(filepath, data, 0644)
	if err != nil {
		t.Logf("保存报告失败: %v", err)
		return
	}

	t.Logf("\n✓ 测试报告已保存到: %s", filepath)
	t.Logf("✓ 包含 %d 个测试用例，%d 个失败用例详情", len(results), len(failures))
}
