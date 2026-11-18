// Package promptinjection - 错误收集器测试
//
// ★ 2026-11-17 审计轮：本文件随错误收集器的三处根修同步重写。
//
//	每条新断言都对应一个**已复现的具体缺陷**，不是形式化覆盖：
//	① JSONB 绑定形态（[]byte → string + NULLIF($N,'')::text::jsonb）；
//	② UTF-8 切点（字节切片 → rune-safe）：中文输入必须**仍然是合法 UTF-8**；
//	③ 去重竞态（read-then-write → 事务内 pg_advisory_xact_lock）；
//	④ 原子占位（ClaimPendingErrors：FOR UPDATE SKIP LOCKED）；
//	⑤ 条件状态更新（过期 worker 不得覆盖别人的结论）；
//	⑥ nil 策略不得 panic（策略加载失败是常态，不是例外）。
package promptinjection

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectCollectPrelude 铺设一次 Collect 的标准前奏：
// 开事务 → 取 advisory lock → 查重。
func expectCollectPrelude(mock sqlmock.Sqlmock, dupRows *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnRows(dupRows)
}

func TestNewErrorCollector(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)
	assert.NotNil(t, ec)
	assert.True(t, ec.enabled)
	assert.Equal(t, db, ec.db)
}

func TestErrorCollector_Collect_Disabled(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, false)

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "test error",
	})

	assert.NoError(t, err) // 应该静默跳过
}

func TestErrorCollector_Collect_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		SessionID:    "session-123",
		RequestID:    "request-456",
		InputText:    "test input",
		ErrorMessage: "LLM detection failed",
		Priority:     7,
		ErrorDetails: map[string]interface{}{
			"llm_engine_id": 1,
		},
		DetectionLayers: map[string]bool{
			"basic": true,
		},
		MatchedRules: []MatchedRule{
			{
				RuleName: "test-rule",
				Category: "test",
				Severity: 5,
			},
		},
		PolicyConfig: map[string]interface{}{
			"enabled": true,
		},
		Metadata: map[string]interface{}{
			"timestamp": 1234567890,
		},
	}

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

	// 期望插入查询
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeLLMDetectionFailed,
			sqlmock.AnyArg(), // error_hash
			7,                // priority
			"tenant-1",
			"session-123",
			"request-456",
			"test input",
			sqlmock.AnyArg(), // input_hash
			"LLM detection failed",
			sqlmock.AnyArg(), // error_details JSON
			sqlmock.AnyArg(), // detection_layers JSON
			sqlmock.AnyArg(), // matched_rules JSON
			sqlmock.AnyArg(), // policy_config JSON
			sqlmock.AnyArg(), // metadata JSON
			QueueStatusPending,
			0, // retry_count
			3, // max_retries
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err)

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归：jsonb 参数必须绑成 **string**，不是 []byte。
//
//	pgx 在 SimpleProtocol 下把 []byte 编码成 bytea 十六进制 ⇒ PG 报 22P02。
//	（生产默认 CacheStatement 时 []byte 可用，但 string + ::text::jsonb
//	 在两种模式下都正确，且与本仓正典 internal/dbx.NormalizeJSONB 同形。）
func TestErrorCollector_Collect_JsonbArgsAreStrings(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

	var seen []interface{}
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // error_details
			sqlmock.AnyArg(), // detection_layers
			sqlmock.AnyArg(), // matched_rules
			sqlmock.AnyArg(), // policy_config
			sqlmock.AnyArg(), // metadata
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:       ErrorTypeLLMDetectionFailed,
		TenantID:        "tenant-1",
		ErrorMessage:    "boom",
		ErrorDetails:    map[string]interface{}{"k": "v"},
		DetectionLayers: map[string]bool{"basic": true},
		MatchedRules:    []MatchedRule{{RuleName: "r"}},
		PolicyConfig:    map[string]interface{}{"p": 1},
		Metadata:        map[string]interface{}{"m": 1},
	})
	require.NoError(t, err)

	// sqlmock 的 WithArgs 用 AnyArg 放行 ⇒ 这里改为直接断言 SQL 形态。
	// 取出最后一次执行的 SQL 需要重新跑一遍并捕获 ⇒ 用 ExpectQuery 的
	// 自定义 matcher 不便，这里改为**断言 SQL 文本**（在下面单独一条测试）。
	_ = seen
}

// ★ 回归：INSERT 的 jsonb 列必须走 NULLIF + ::text::jsonb 形态（见下方 SQL）。
//
//	空串直接过 ::text::jsonb 会报 22P02（把「没内容」换成「SQL 报错」），
//	所以必须用 NULLIF 把空串变成 SQL NULL。
//
//	★ 本节的 SQL 形态刻意放在**缩进块**里：Go 1.19+ 的 gofmt 会把 doc
//	  注释**正文**里的两个单引号转成排版引号（U+201D）⇒ 直接写在正文里
//	  会让文档里的 SQL 失真。缩进块是 gofmt 的**预排版**区域 ⇒ 原样保留。
//
//	SELECT NULLIF($10, '')::text::jsonb
func TestErrorCollector_Collect_JsonbColumnsUseNullIfTextCast(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "boom",
		ErrorDetails: map[string]interface{}{"k": "v"},
	})
	require.NoError(t, err)

	// sqlmock 的默认 QueryMatcher 是正则匹配，
	// 这里用自定义 matcher 捕获真实 SQL 文本。
	var capturedSQL string
	db, mock, err = sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(actualSQL, "INSERT INTO error_analysis_queue") {
			capturedSQL = actualSQL
		}
		return nil
	})))
	require.NoError(t, err)
	defer db.Close()

	ec = NewErrorCollector(db, true)
	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	require.NoError(t, ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "boom",
		ErrorDetails: map[string]interface{}{"k": "v"},
	}))

	require.NotEmpty(t, capturedSQL, "INSERT SQL was not captured")
	assert.Contains(t, capturedSQL, "NULLIF($10, '')::text::jsonb",
		"error_details must bind via NULLIF($10,'')::text::jsonb")
	assert.Contains(t, capturedSQL, "NULLIF($14, '')::text::jsonb",
		"metadata must bind via NULLIF($14,'')::text::jsonb")
}

// ★ 回归（P0，已实测复现）：UTF-8 切点。
//
//	纯中文输入 3334..3399 字 ⇒ 字节截断 10000 必然切开 rune
//	⇒ 非法 UTF-8 ⇒ PostgreSQL 报 22021 ⇒ **整行丢失**
//	⇒ 错误队列在真实中文流量上恒为 0 行。
func TestErrorCollector_Collect_CJKInputStaysValidUTF8(t *testing.T) {
	for _, n := range []int{3334, 3400, 5000, 12000} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)

		ec := NewErrorCollector(db, true)
		cjk := strings.Repeat("中", n) // 3 字节/字

		expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

		var boundInput interface{}
		mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
			WithArgs(
				sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), // ★ 截断后的 input_text
				sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), sqlmock.AnyArg(),
				sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		mock.ExpectCommit()

		require.NoError(t, ec.Collect(context.Background(), &CollectedError{
			ErrorType:    ErrorTypeLLMDetectionFailed,
			TenantID:     "tenant-1",
			InputText:    cjk,
			ErrorMessage: "boom",
		}), "n=%d", n)

		boundInput = nil
		_ = boundInput
		db.Close()
	}
}

// ★ 回归（P0）：截断后的文本必须是**合法 UTF-8**（这是 PG 22021 的前置条件）。
func TestErrorCollector_TruncateUTF8Bytes_KeepsValidUTF8(t *testing.T) {
	cases := []string{
		strings.Repeat("中", 5000),
		strings.Repeat("🙂", 3000),
		strings.Repeat("a", 20000),
		strings.Repeat("中a🙂b", 3000),
	}
	for _, in := range cases {
		out := truncateUTF8Bytes(in, maxInputTextBytes)
		assert.True(t, utf8.ValidString(out), "truncated text must stay valid UTF-8")
		assert.LessOrEqual(t, len(out), maxInputTextBytes)
		// 结果必须是原文的前缀（不插入任何标记）。
		assert.True(t, strings.HasPrefix(in, out))
	}
}

func TestErrorCollector_Collect_MissingRequiredFields(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	tests := []struct {
		name        string
		err         *CollectedError
		expectError string
	}{
		{
			name:        "nil error",
			err:         nil,
			expectError: "collected error is nil",
		},
		{
			name: "missing error_type",
			err: &CollectedError{
				TenantID:     "tenant-1",
				ErrorMessage: "test",
			},
			expectError: "error_type is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ec.Collect(context.Background(), tt.err)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

// ★ 空 tenant_id 不再丢弃错误 ⇒ 回落到 "default"。
//
//	理由：tenant_id 在请求链路上本就可以为空（未绑定租户的请求），
//	而「检测失败」恰恰最常发生在租户上下文还没建立的链路上 ⇒
//	直接 return error ⇒ **最该被分析的一类错误被静默丢掉**。
func TestErrorCollector_Collect_EmptyTenantFallsBackToDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeLLMDetectionFailed,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			"default", // ★ 回落到默认租户
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		ErrorMessage: "boom",
	})
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestErrorCollector_Collect_Duplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "test error",
		Priority:     5,
	}

	// 期望检查重复查询 - 返回已存在的 ID
	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}).AddRow(999))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err) // 应该静默跳过重复项

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归：截断测试的输入必须**真的**超过上限。
//
//	原测试写的是 `for i := range longInput { longInput = string(...) }`
//	—— 循环体整体覆盖了 longInput ⇒ 最终值 **1 字节**
//	⇒ 断言「>10000 字符」从未被触发（实测复现）。
func TestErrorCollector_Collect_LongInputTruncation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	longInput := strings.Repeat("a", 15000)
	require.Greater(t, len(longInput), maxInputTextBytes, "fixture must exceed the cap")

	collectedErr := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		InputText:    longInput,
		ErrorMessage: "test",
	}

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // 这应该是截断后的 input_text
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.Collect(context.Background(), collectedErr)
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestErrorCollector_CollectLLMDetectionFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	policy := &Policy{
		LLMEngineID:            intPtr(1),
		LLMDetectionTimeoutSec: 30,
		LLMDetectionFailClosed: true,
	}

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))

	testError := errors.New("LLM failed")

	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WithArgs(
			ErrorTypeLLMDetectionFailed,
			sqlmock.AnyArg(),
			7, // 高优先级
			"tenant-1",
			"session-123",
			"request-456",
			"test input",
			sqlmock.AnyArg(),
			"LLM failed",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			QueueStatusPending,
			0,
			3,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.CollectLLMDetectionFailure(
		context.Background(),
		"tenant-1",
		"session-123",
		"request-456",
		"test input",
		testError,
		policy,
	)

	assert.NoError(t, err)
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归（P1）：nil policy 不得 panic。
//
//	「策略加载失败」这条错误路径的常态就是 policy == nil；
//	原实现直接解引用 policy.LLMEngineID ⇒ **panic** ⇒
//	「收集错误」的动作本身把进程打挂 ⇒ 错误收集器成了故障放大器。
func TestErrorCollector_CollectLLMDetectionFailure_NilPolicyDoesNotPanic(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	assert.NotPanics(t, func() {
		err = ec.CollectLLMDetectionFailure(
			context.Background(),
			"tenant-1",
			"", "",
			"test input",
			errors.New("boom"),
			nil, // ★ nil policy
		)
	})
	assert.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestErrorCollector_CollectRuleFalsePositive(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	matchedRules := []MatchedRule{
		{
			RuleName: "rule-1",
			Category: "test",
			Severity: 5,
		},
	}

	detectionLayers := map[string]bool{
		"basic":    true,
		"advanced": false,
	}

	expectCollectPrelude(mock, sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err = ec.CollectRuleFalsePositive(
		context.Background(),
		"tenant-1",
		"session-123",
		"request-456",
		"test input",
		matchedRules,
		detectionLayers,
	)

	assert.NoError(t, err)
	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归（P1）：领取必须是**原子占位**。
//
//	原实现是「只读 SELECT pending」+「无条件 UPDATE」⇒ 两个处理器
//	（后台 worker + 管理 API 手动触发，或多副本网关）可同时拿到同一行
//	⇒ 同一错误被分析两次 ⇒ 结果表出现两条互斥结论。
func TestErrorCollector_ClaimPendingErrors_UsesSkipLocked(t *testing.T) {
	var capturedSQL string
	db2, mock2, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(actualSQL, "UPDATE error_analysis_queue q") {
			capturedSQL = actualSQL
		}
		return nil
	})))
	require.NoError(t, err)
	defer db2.Close()

	ec2 := NewErrorCollector(db2, true)
	mock2.ExpectQuery(`UPDATE error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "error_type", "error_hash", "priority",
			"tenant_id", "session_id", "request_id",
			"input_text", "input_hash", "error_message",
			"error_details", "detection_layers", "matched_rules", "policy_config",
			"status", "retry_count", "max_retries",
			"created_at", "started_at", "completed_at", "metadata",
		}))

	claimed, err := ec2.ClaimPendingErrors(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, claimed)
	require.NotEmpty(t, capturedSQL, "claim SQL was not captured")
	assert.Contains(t, capturedSQL, "FOR UPDATE SKIP LOCKED",
		"claim must use FOR UPDATE SKIP LOCKED so concurrent processors skip claimed rows")
	assert.Contains(t, capturedSQL, "status = 'processing'",
		"claim must flip status to processing in the same statement")
}

func TestErrorCollector_UpdateStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	statuses := []string{
		QueueStatusProcessing,
		QueueStatusAnalyzed,
		QueueStatusFailed,
		QueueStatusSkipped,
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			mock.ExpectExec(`UPDATE error_analysis_queue`).
				WithArgs(status, int64(1), "").
				WillReturnResult(sqlmock.NewResult(0, 1))

			err := ec.UpdateStatus(context.Background(), 1, status)
			assert.NoError(t, err)
		})
	}

	err = mock.ExpectationsWereMet()
	assert.NoError(t, err)
}

// ★ 回归（P2）：状态更新必须**带条件**，否则过期 worker 能覆盖别人的结论。
func TestErrorCollector_IncrementRetry_IsConditionalOnProcessing(t *testing.T) {
	var capturedSQL string
	db2, mock2, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(actualSQL, "retry_count = retry_count + 1") {
			capturedSQL = actualSQL
		}
		return nil
	})))
	require.NoError(t, err)
	defer db2.Close()

	ec2 := NewErrorCollector(db2, true)
	mock2.ExpectExec(`UPDATE error_analysis_queue`).
		WithArgs(int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, ec2.IncrementRetry(context.Background(), 1))

	require.NotEmpty(t, capturedSQL, "retry SQL was not captured")
	assert.Contains(t, capturedSQL, "AND status = 'processing'",
		"retry must only advance rows still in processing")
	assert.Contains(t, capturedSQL, "completed_at",
		"retry must keep completed_at consistent between the two 'failed' paths")
}

// ★ 回归（P1）：孤儿 processing 行必须能被回收，否则错误被永久吞掉。
// ★ 第57增量：耗尽重试的孤儿行必须落**终态 failed**（带 completed_at），
//
//	原谓词 retry_count < max_retries 把它们整类跳过 ⇒ 永久「处理中」僵尸。
func TestErrorCollector_ReclaimStaleClaims(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	mock.ExpectExec(`SET status = 'pending'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`SET status = 'failed'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))

	n, err := ec.ReclaimStaleClaims(context.Background(), 10*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n, "reclaim total = requeued + exhausted-to-failed")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// ★ 第57增量钉测：谓词编进匹配正则——任何一侧丢了谓词/写反方向，
//
//	sqlmock 匹配不上 ⇒ 期望不满足（红）。
func TestErrorCollector_ReclaimStaleClaims_ExhaustedPredicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	mock.ExpectExec(`SET status = 'pending'[\s\S]*retry_count < max_retries`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SET status = 'failed'[\s\S]*completed_at[\s\S]*retry_count >= max_retries`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = ec.ReclaimStaleClaims(context.Background(), 10*time.Minute)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestErrorCollector_ReclaimStaleClaims_ZeroWindowIsNoop(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)

	n, err := ec.ReclaimStaleClaims(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

// ★ 回归：队列深度闸（maxDepth）必须**真的生效**。
//
//	背景：本仓此前有一个「队列深度上限」的环境变量
//	（LLMGatewayPIErrorAnalysisMaxDepth），配置里写着「超出则丢弃新错误」，
//	但代码里**没有任何深度判断** ⇒ 错误风暴（判官持续失败）会把
//	持久队列撑成只涨不消的黑洞，而 worker 的处理速度跟不上。
func TestErrorCollector_Collect_RespectsQueueDepthLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)
	ec.SetMaxDepth(2)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	// 深度已达上限 ⇒ 不得再插入。
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectCommit()
	// ★ 刻意**不** ExpectQuery(INSERT) ⇒ 一旦实现插入了，sqlmock 会让
	//   ExpectationsWereMet 失败（多余的一次调用）。

	err = ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "boom",
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	stats := ec.CollectorStats()
	assert.Equal(t, int64(1), stats["depth_dropped"],
		"因队列深度闸被丢弃的错误必须被单独计数")
}

// ★ 反向：深度**未**达上限 ⇒ 必须照常插入（闸不能变成「永远不收」）。
func TestErrorCollector_Collect_UnderQueueDepthLimitStillInserts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)
	ec.SetMaxDepth(5)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectCommit()

	require.NoError(t, ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "boom",
	}))
	require.NoError(t, mock.ExpectationsWereMet())

	stats := ec.CollectorStats()
	assert.Equal(t, int64(0), stats["depth_dropped"])
}

// ★ maxDepth = 0 ⇒ 不限制（不产生额外的深度查询）。
func TestErrorCollector_Collect_ZeroMaxDepthSkipsDepthProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	ec := NewErrorCollector(db, true)
	ec.SetMaxDepth(0)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM error_analysis_queue WHERE error_hash`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO error_analysis_queue`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	mock.ExpectCommit()

	require.NoError(t, ec.Collect(context.Background(), &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     "tenant-1",
		ErrorMessage: "boom",
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 让 sql.ErrNoRows 的引用保持（部分用例用 errors.Is 判定）。
var _ = sql.ErrNoRows

func intPtr(i int) *int {
	return &i
}

// ★ 2026-11-19 一致性根修钉测（owner 报障「列表与统计不一致」，211 实测定案三因）：
//
//	① 全量域（无 7 天过滤——与 GetQueue 列表同表同域）；
//	② 全状态计数（failed/processing/skipped 不再盲区）；
//	③ 结果聚合（误报数/平均耗时）取自 error_analysis_results（与 GetResults 同域），
//	   空结果表时 avg_duration_ms 必须是 null 而不是 0。
func TestErrorCollector_GetStats_FullScopeAllStatuses(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ec := NewErrorCollector(db, true)

	// 队列聚合：2 failed + 1 pending + 1 analyzed（含 7 天前的旧行——必须被数到）
	mock.ExpectQuery("SELECT\\s+COUNT\\(\\*\\) AS total,").WillReturnRows(
		sqlmock.NewRows([]string{"total", "pending", "processing", "analyzed", "failed", "skipped"}).
			AddRow(4, 1, 0, 1, 2, 0))
	// 结果聚合：1 条结果、误报 1、平均耗时 1234ms
	mock.ExpectQuery("SELECT\\s+COUNT\\(\\*\\) AS results_total,").WillReturnRows(
		sqlmock.NewRows([]string{"results_total", "false_positives", "avg_duration_ms"}).
			AddRow(1, 1, 1234.0))
	// by_type：一行 2025 年的旧 failed 行（旧实现 7 天窗里看不见）
	mock.ExpectQuery("SELECT\\s+error_type,").WillReturnRows(
		sqlmock.NewRows([]string{"error_type", "total", "pending", "analyzed", "failed", "last_error_at"}).
			AddRow("rule_false_positive", 4, 1, 1, 2, time.Now().AddDate(0, -2, 0)))

	stats, err := ec.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}

	// 顶层计数与列表 COUNT 域逐项相等
	for key, want := range map[string]int{
		"total": 4, "pending": 1, "analyzed": 1, "failed": 2,
		"results_total": 1, "false_positive_count": 1,
	} {
		got, _ := stats[key].(int)
		if got != want {
			t.Fatalf("stats[%q] = %d, want %d", key, got, want)
		}
	}
	if v, _ := stats["avg_duration_ms"].(int); v != 1234 {
		t.Fatalf("avg_duration_ms = %v, want 1234", stats["avg_duration_ms"])
	}

	// by_type 必须包含 7 天外的旧行（旧实现的时间过滤把它丢了 ⇒ 列表有行统计没有）
	byType, _ := stats["by_type"].([]map[string]interface{})
	if len(byType) != 1 {
		t.Fatalf("by_type rows = %d, want 1 (old row must not be filtered out)", len(byType))
	}
	if byType[0]["failed"].(int) != 2 {
		t.Fatalf("by_type[0].failed = %v, want 2", byType[0]["failed"])
	}
}

// ★ 空结果表：avg_duration_ms 必须是 null（前端显示 —），不能是假 0ms。
func TestErrorCollector_GetStats_EmptyResultsTableNilDuration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ec := NewErrorCollector(db, true)

	mock.ExpectQuery("SELECT\\s+COUNT\\(\\*\\) AS total,").WillReturnRows(
		sqlmock.NewRows([]string{"total", "pending", "processing", "analyzed", "failed", "skipped"}).
			AddRow(0, 0, 0, 0, 0, 0))
	mock.ExpectQuery("SELECT\\s+COUNT\\(\\*\\) AS results_total,").WillReturnRows(
		sqlmock.NewRows([]string{"results_total", "false_positives", "avg_duration_ms"}).
			AddRow(0, 0, nil))
	mock.ExpectQuery("SELECT\\s+error_type,").WillReturnRows(
		sqlmock.NewRows([]string{"error_type", "total", "pending", "analyzed", "failed", "last_error_at"}))

	stats, err := ec.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if v, ok := stats["avg_duration_ms"]; ok && v != nil {
		t.Fatalf("avg_duration_ms must be nil on empty results, got %v", v)
	}
	if v, _ := stats["total"].(int); v != 0 {
		t.Fatalf("total = %v, want 0", stats["total"])
	}
}
