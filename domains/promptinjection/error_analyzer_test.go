// Package promptinjection - 错误分析器测试
package promptinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLLMClient 模拟 LLM 客户端
type MockLLMClient struct {
	response   string
	tokensUsed int
	err        error
}

func (m *MockLLMClient) Analyze(ctx context.Context, prompt string) (string, int, error) {
	return m.response, m.tokensUsed, m.err
}

func TestNewErrorAnalyzer(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}

	analyzer := NewErrorAnalyzer(db, collector, llmClient, "claude-3-5-sonnet", true)

	assert.NotNil(t, analyzer)
	assert.True(t, analyzer.enabled)
	assert.Equal(t, "claude-3-5-sonnet", analyzer.analyzerModel)
	// 2026-11-25：解析器换代（容错解析）⇒ 版本号升到 2.0.0。
	assert.Equal(t, "2.0.0", analyzer.analyzerVersion)
	assert.Equal(t, 10, analyzer.batchSize)
}

func TestErrorAnalyzer_ProcessBatch_Disabled(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}

	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", false)

	processed, err := analyzer.ProcessBatch(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0, processed)
}

func TestErrorAnalyzer_ProcessBatch_NoPendingErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}

	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	// ★ 2026-11-17 审计轮：ProcessBatch 现在先回收孤儿 processing 行，
	//   再用**原子占位**（UPDATE ... FOR UPDATE SKIP LOCKED）领取。
	// ★ 第57增量：回收分两条 UPDATE（未耗尽→pending；已耗尽→failed+completed_at）。
	mock.ExpectExec(`SET status = 'pending'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET status = 'failed'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 期望领取返回空结果
	mock.ExpectQuery(`UPDATE error_analysis_queue`).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "error_type", "error_hash", "priority",
			"tenant_id", "session_id", "request_id",
			"input_text", "input_hash", "error_message",
			"error_details", "detection_layers", "matched_rules", "policy_config",
			"status", "retry_count", "max_retries",
			"created_at", "started_at", "completed_at", "metadata",
		}))

	processed, err := analyzer.ProcessBatch(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0, processed)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorAnalyzer_AnalyzeError_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)

	// 模拟 LLM 返回有效的 JSON 响应
	llmResponse := `{
		"root_cause": "LLM service timeout",
		"error_category": "timeout",
		"is_false_positive": false,
		"confidence_score": 0.85,
		"suggested_fix": "Increase timeout from 30s to 60s",
		"rule_adjustment": {"timeout_sec": 60},
		"code_changes": {"file": "config.yaml", "change": "timeout: 60"},
		"severity_level": "medium",
		"impact_scope": "tenant",
		"affected_users_estimate": 10,
		"analysis_details": {"recommendation": "adjust timeout"}
	}`

	llmClient := &MockLLMClient{
		response:   llmResponse,
		tokensUsed: 150,
		err:        nil,
	}

	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	queuedError := &QueuedError{
		ID:              1,
		ErrorType:       ErrorTypeLLMDetectionFailed,
		ErrorHash:       "hash123",
		Priority:        7,
		TenantID:        "tenant-1",
		SessionID:       sql.NullString{String: "session-1", Valid: true},
		ErrorMessage:    "LLM detection timeout",
		ErrorDetails:    json.RawMessage(`{"llm_engine_id": 1}`),
		DetectionLayers: json.RawMessage(`{"basic": true}`),
		MatchedRules:    json.RawMessage(`[]`),
		CreatedAt:       time.Now(),
	}

	// 期望查找相似案例的查询
	mock.ExpectQuery(`SELECT .+ FROM error_analysis_queue q INNER JOIN error_analysis_results`).
		WithArgs(ErrorTypeLLMDetectionFailed, "tenant-1", int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "error_type", "error_message", "created_at",
			"root_cause", "error_category", "is_false_positive",
		}))

	result, err := analyzer.AnalyzeError(context.Background(), queuedError)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, int64(1), result.QueueID)
	assert.NotEmpty(t, result.AnalysisID)
	assert.Equal(t, "LLM service timeout", result.RootCause)
	assert.Equal(t, "timeout", result.ErrorCategory)
	assert.False(t, result.IsFalsePositive)
	assert.Equal(t, 0.85, result.ConfidenceScore)
	assert.Equal(t, "Increase timeout from 30s to 60s", result.SuggestedFix)
	assert.Equal(t, "medium", result.SeverityLevel)
	assert.Equal(t, "tenant", result.ImpactScope)
	assert.Equal(t, 10, result.AffectedUsersEstimate)
	assert.Equal(t, 150, result.LLMTokensUsed)
	assert.GreaterOrEqual(t, result.AnalysisDurationMs, 0) // May be 0 if execution is very fast

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorAnalyzer_AnalyzeError_LLMFailure(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)

	llmClient := &MockLLMClient{
		err: errors.New("LLM service unavailable"),
	}

	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	queuedError := &QueuedError{
		ID:              1,
		ErrorType:       ErrorTypeLLMDetectionFailed,
		TenantID:        "tenant-1",
		ErrorMessage:    "test error",
		ErrorDetails:    json.RawMessage(`{}`),
		DetectionLayers: json.RawMessage(`{}`),
		MatchedRules:    json.RawMessage(`[]`),
	}

	result, err := analyzer.AnalyzeError(context.Background(), queuedError)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "LLM analysis failed")
}

func TestErrorAnalyzer_BuildPrompt(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	queuedError := &QueuedError{
		ID:              1,
		ErrorType:       ErrorTypeLLMDetectionFailed,
		TenantID:        "tenant-1",
		SessionID:       sql.NullString{String: "session-1", Valid: true},
		RequestID:       sql.NullString{String: "request-1", Valid: true},
		InputText:       sql.NullString{String: "test input", Valid: true},
		ErrorMessage:    "LLM timeout",
		ErrorDetails:    json.RawMessage(`{"llm_engine_id": 1, "timeout": 30}`),
		DetectionLayers: json.RawMessage(`{"basic": true, "advanced": false}`),
		MatchedRules:    json.RawMessage(`[{"rule_name": "test-rule", "severity": 5}]`),
		RetryCount:      2,
	}

	prompt, err := analyzer.buildPrompt(queuedError)

	assert.NoError(t, err)
	assert.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "错误类型: llm_detection_failed")
	assert.Contains(t, prompt, "租户ID: tenant-1")
	assert.Contains(t, prompt, "会话ID: session-1")
	assert.Contains(t, prompt, "请求ID: request-1")
	assert.Contains(t, prompt, "LLM timeout")
	assert.Contains(t, prompt, "test input")
	assert.Contains(t, prompt, "重试次数: 2")

	// ★ 2026-11-17 审计轮 P1（提示词注入面）：
	//   攻击者可控的原文必须落在 <untrusted_input> 框架里，
	//   且框架必须声明「是数据不是指令」。
	assert.Contains(t, prompt, "<untrusted_input>")
	assert.Contains(t, prompt, "</untrusted_input>")
	assert.Contains(t, prompt, "待分析的数据")
}

func TestErrorAnalyzer_GetPromptTemplate(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	tests := []struct {
		name      string
		errorType string
	}{
		{"LLM Detection Failed", ErrorTypeLLMDetectionFailed},
		{"Rule False Positive", ErrorTypeRuleFalsePositive},
		{"Canary Detection Failed", ErrorTypeCanaryDetectionFailed},
		{"Vector Match Failed", ErrorTypeVectorMatchFailed},
		{"Output Guard Blocked", ErrorTypeOutputGuardBlocked},
		{"Generic", "unknown_type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := analyzer.getPromptTemplate(tt.errorType)
			assert.NotEmpty(t, template.SystemPrompt)
			assert.NotEmpty(t, template.UserPrompt)
		})
	}
}

func TestErrorAnalyzer_ParseResponse_ValidJSON(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	response := `{
		"root_cause": "Configuration error",
		"error_category": "configuration",
		"is_false_positive": true,
		"confidence_score": 0.95,
		"suggested_fix": "Update config.yaml",
		"severity_level": "low",
		"impact_scope": "session",
		"affected_users_estimate": 1
	}`

	result, err := analyzer.parseResponse(response)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Configuration error", result.RootCause)
	assert.Equal(t, "configuration", result.ErrorCategory)
	assert.True(t, result.IsFalsePositive)
	assert.Equal(t, 0.95, result.ConfidenceScore)
	assert.Equal(t, "low", result.SeverityLevel)
}

func TestErrorAnalyzer_ParseResponse_JSONInMarkdown(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	response := "Here is my analysis:\n\n" +
		"```json\n" +
		"{\n" +
		"	\"root_cause\": \"Test error\",\n" +
		"	\"error_category\": \"test\",\n" +
		"	\"is_false_positive\": false,\n" +
		"	\"confidence_score\": 0.8,\n" +
		"	\"suggested_fix\": \"Fix it\",\n" +
		"	\"severity_level\": \"medium\",\n" +
		"	\"impact_scope\": \"tenant\",\n" +
		"	\"affected_users_estimate\": 5\n" +
		"}\n" +
		"```\n\n" +
		"That's my recommendation."

	result, err := analyzer.parseResponse(response)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Test error", result.RootCause)
	assert.Equal(t, "test", result.ErrorCategory)
}

func TestErrorAnalyzer_ParseResponse_InvalidJSON(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	tests := []struct {
		name     string
		response string
	}{
		{"no JSON", "This is just text without JSON"},
		{"malformed JSON", "{invalid json}"},
		{"missing root_cause", `{"error_category": "test"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := analyzer.parseResponse(tt.response)
			assert.Error(t, err)
			assert.Nil(t, result)
		})
	}
}

// TestErrorAnalyzer_JSONCandidates：候选块枚举（2026-11-25 容错改造后
// extractJSON 退役——「取第一个合法块」的职责由 parseAnalysisResponse 承担，
// 这里钉住候选枚举本身的顺序与内容）。
func TestErrorAnalyzer_JSONCandidates(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "bare JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON in markdown",
			input:    "```json\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with surrounding text",
			input:    "Here is the result: {\"key\": \"value\"} end",
			expected: `{"key": "value"}`,
		},
		{
			name:     "no JSON",
			input:    "just text",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cands := jsonCandidates(tt.input)
			got := ""
			if len(cands) > 0 {
				got = cands[0]
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestErrorAnalyzer_SaveAnalysisResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	result := &AnalysisResult{
		QueueID:               1,
		AnalysisID:            "analysis-123",
		RootCause:             "Test root cause",
		ErrorCategory:         "test_category",
		IsFalsePositive:       false,
		ConfidenceScore:       0.85,
		SuggestedFix:          "Fix suggestion",
		RuleAdjustment:        map[string]interface{}{"key": "value"},
		CodeChanges:           map[string]interface{}{"file": "test.go"},
		SeverityLevel:         "medium",
		ImpactScope:           "tenant",
		AffectedUsersEstimate: 10,
		SimilarCases:          []map[string]interface{}{{"id": 1}},
		PatternFrequency:      1,
		AnalysisDetails:       map[string]interface{}{"detail": "value"},
		LLMResponseRaw:        "raw response",
		AnalysisDurationMs:    1500,
		LLMTokensUsed:         200,
	}

	mock.ExpectQuery(`INSERT INTO error_analysis_results`).
		WithArgs(
			int64(1),
			"analysis-123",
			"test-model",
			"2.0.0",
			"Test root cause",
			"test_category",
			false,
			0.85,
			"Fix suggestion",
			sqlmock.AnyArg(), // rule_adjustment JSON
			sqlmock.AnyArg(), // code_changes JSON
			"medium",
			"tenant",
			10,
			sqlmock.AnyArg(), // similar_cases JSON
			1,
			sqlmock.AnyArg(), // analysis_details JSON
			"raw response",
			1500,
			200,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	err = analyzer.SaveAnalysisResult(context.Background(), result)

	assert.NoError(t, err)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

func TestErrorAnalyzer_FindSimilarCases(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	llmClient := &MockLLMClient{}
	analyzer := NewErrorAnalyzer(db, collector, llmClient, "test-model", true)

	queuedError := &QueuedError{
		ID:        1,
		ErrorType: ErrorTypeLLMDetectionFailed,
		TenantID:  "tenant-1",
	}

	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "error_type", "error_message", "created_at",
		"root_cause", "error_category", "is_false_positive",
	}).
		AddRow(2, ErrorTypeLLMDetectionFailed, "Similar error 1", now,
			"Timeout", "timeout", false).
		AddRow(3, ErrorTypeLLMDetectionFailed, "Similar error 2", now,
			"Config error", "configuration", true)

	mock.ExpectQuery(`SELECT .+ FROM error_analysis_queue q INNER JOIN error_analysis_results`).
		WithArgs(ErrorTypeLLMDetectionFailed, "tenant-1", int64(1)).
		WillReturnRows(rows)

	cases, err := analyzer.findSimilarCases(context.Background(), queuedError)

	assert.NoError(t, err)
	assert.Len(t, cases, 2)

	assert.Equal(t, int64(2), cases[0]["id"])
	assert.Equal(t, ErrorTypeLLMDetectionFailed, cases[0]["error_type"])
	assert.Equal(t, "Timeout", cases[0]["root_cause"])

	assert.Equal(t, int64(3), cases[1]["id"])
	assert.True(t, cases[1]["is_false_positive"].(bool))

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归：SetBatchSize 必须真的改到分析器上。
//
//	背景：接线层读 LLMGatewayPIErrorAnalysisBatchSize，
//	但分析器此前把 batchSize **写死成 10** ⇒ 配置项形同虚设
//	⇒ 又一处「自述与实现不符」。
func TestErrorAnalyzer_SetBatchSize_ChangesClaimSize(t *testing.T) {
	ea := &ErrorAnalyzer{batchSize: 10}

	ea.SetBatchSize(37)
	if ea.batchSize != 37 {
		t.Fatalf("batchSize = %d, want 37", ea.batchSize)
	}

	// 非正值必须被忽略（不能把批次设成 0）。
	ea.SetBatchSize(0)
	if ea.batchSize != 37 {
		t.Fatalf("SetBatchSize(0) must be ignored, got %d", ea.batchSize)
	}
	ea.SetBatchSize(-5)
	if ea.batchSize != 37 {
		t.Fatalf("SetBatchSize(-5) must be ignored, got %d", ea.batchSize)
	}
}

// ★ 第57增量回归：请求级批量走 ProcessBatchN（局部 limit），
//
//	不得触碰共享 batchSize——原实现（handler 调 SetBatchSize）与后台
//	worker 的读并发（data race）且永久改后台配置。
//	钉两件事：① 领取 LIMIT 用的是**请求值**（3）；② 共享 batchSize 仍是 10。
func TestErrorAnalyzer_ProcessBatchN_UsesLocalLimitNotSharedState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	collector := NewErrorCollector(db, true)
	analyzer := NewErrorAnalyzer(db, collector, &MockLLMClient{}, "test-model", true)
	require.Equal(t, 10, analyzer.batchSize, "default batch size must stay 10")

	// 回收两腿（未耗尽→pending；耗尽→failed）。
	mock.ExpectExec(`SET status = 'pending'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET status = 'failed'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// 领取：LIMIT 绑定值必须是**请求的 3**，而不是共享的 10。
	mock.ExpectQuery(`UPDATE error_analysis_queue`).
		WithArgs(3).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "error_type", "error_hash", "priority",
			"tenant_id", "session_id", "request_id",
			"input_text", "input_hash", "error_message",
			"error_details", "detection_layers", "matched_rules", "policy_config",
			"status", "retry_count", "max_retries",
			"created_at", "started_at", "completed_at", "metadata",
		}))

	processed, err := analyzer.ProcessBatchN(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, 0, processed)
	assert.NoError(t, mock.ExpectationsWereMet())

	// ② 共享 batchSize 未被请求值污染。
	assert.Equal(t, 10, analyzer.batchSize,
		"ProcessBatchN must NOT mutate the shared batch size")
}

// TestErrorAnalyzer_OutputGuardBlockedTemplate 契约钉测（2026-11-20 PI 接入轮）：
// 输出守卫格式类拦截的分析模板必须①面向归因分类 ②要求分受众的继续建议与
// 操作 ③禁止 rule_adjustment（动作白名单指向 prompt_injection_rules，与
// 输出守卫规则是两张表；apply 面对这类事件恒 view-only）。
func TestErrorAnalyzer_OutputGuardBlockedTemplate(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	analyzer := NewErrorAnalyzer(db, NewErrorCollector(db, true), &MockLLMClient{}, "test-model", true)
	tpl := analyzer.getPromptTemplate(ErrorTypeOutputGuardBlocked)
	for _, want := range []string{"error_category", "upstream_interrupted", "gateway_guard_misjudge", "suggested_fix", "rule_adjustment"} {
		assert.Contains(t, tpl.SystemPrompt, want)
	}
	assert.Contains(t, tpl.SystemPrompt, "必须为 null")
	assert.NotEmpty(t, tpl.UserPrompt)
}
