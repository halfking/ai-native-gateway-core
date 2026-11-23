package metricsstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// 这组用例钉住异步处理器的三个口径：
// ① 水位线是**复合游标**（occurred_at, request_id）—— 单时间戳 + LIMIT 会永久跳过同刻记录；
// ② 同一请求取**最大 revision** —— 只取 revision=1 会让修订后的用量/费用永远进不了统计表；
// ③ 会话/任务维度取活库真实列名（gw_session_id / gw_task_id）。

// ★ 缺陷回归：美元→分原先用 int64(costUSD * 100) 截断，1.15 美元会变成 114 分。
func TestUsdToCents_RoundsInsteadOfTruncating(t *testing.T) {
	tests := []struct {
		name     string
		costUSD  float64
		expected int64
	}{
		{name: "整数美元", costUSD: 10.00, expected: 1000},
		{name: "小数美元", costUSD: 12.34, expected: 1234},
		{name: "零美元", costUSD: 0.00, expected: 0},
		{name: "小额美元", costUSD: 0.01, expected: 1},
		{name: "截断会丢分的值", costUSD: 1.15, expected: 115},
		{name: "修订版金额", costUSD: 99.99, expected: 9999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usdToCents(tt.costUSD)
			if got != tt.expected {
				t.Errorf("美元转分错误: got %d, want %d", got, tt.expected)
			}
		})
	}
}

// ★ 缺陷回归：请求头水位线用单时间戳 + LIMIT，同一时刻的另一条会被永久跳过。
func TestSelectUsageFactsSQL_UsesCompositeCursor(t *testing.T) {
	if !strings.Contains(selectUsageFactsSQL, "(src.occurred_at, src.request_id) >") {
		t.Error("水位线必须是 (occurred_at, request_id) 复合游标")
	}
	if !strings.Contains(selectUsageFactsSQL, "ORDER BY uf.occurred_at, uf.request_id") {
		t.Error("读取顺序必须与游标口径一致（occurred_at, request_id）")
	}
}

// ★ 缺陷回归：同请求只取 revision=1 ⇒ 修订后的用量/费用永远进不了统计表。
func TestSelectUsageFactsSQL_TakesMaxRevision(t *testing.T) {
	if !strings.Contains(selectUsageFactsSQL, "DISTINCT ON (src.request_id)") {
		t.Error("应按 request_id 取最大 revision")
	}
	if !strings.Contains(selectUsageFactsSQL, "revision DESC") {
		t.Error("应按 revision 倒序取最新修订")
	}
	if strings.Contains(selectUsageFactsSQL, "revision = 1") {
		t.Error("不应再固定取 revision = 1")
	}
}

// ★ 缺陷回归：request_logs 里没有 session_id、没有 gateway_instance；
// 真实列名是 gw_session_id / gw_task_id（原 SQL 直接报「column does not exist」
// ⇒ 整条增强静默失败 ⇒ session_id 永远写不进去）。
func TestEnrichDimensionsSQL_UsesLiveColumnNames(t *testing.T) {
	if !strings.Contains(enrichDimensionsSQL, "gw_session_id") {
		t.Error("应取活库真实列 gw_session_id")
	}
	if !strings.Contains(enrichDimensionsSQL, "gw_task_id") {
		t.Error("应取活库真实列 gw_task_id")
	}
	if strings.Contains(enrichDimensionsSQL, "gateway_instance") {
		t.Error("request_logs 没有 gateway_instance 列")
	}
	if strings.Contains(enrichDimensionsSQL, "SELECT session_id") {
		t.Error("request_logs 没有 session_id 列")
	}
}

// ★ 缺陷回归：request_metrics 没有 modality 源列，只能派生。
func TestDeriveModality(t *testing.T) {
	tests := []struct {
		name          string
		image, audio  int32
		video         int32
		attachmentCnt int32
		expected      string
	}{
		{name: "纯文本", expected: "text"},
		{name: "图像", image: 10, expected: "image"},
		{name: "音频", audio: 10, expected: "audio"},
		{name: "视频", video: 10, expected: "video"},
		{name: "图+音混合", image: 10, audio: 10, expected: "multimodal"},
		{name: "有附件无多模态 token", attachmentCnt: 2, expected: "multimodal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveModality(tt.image, tt.audio, tt.video, tt.attachmentCnt)
			if got != tt.expected {
				t.Errorf("模态派生错误: got %q, want %q", got, tt.expected)
			}
		})
	}
}

// ★ 端到端行为门：水位线读到的复合游标必须**原样**传给来源查询，
// 且写库时 session_id / cost_cents / modality / protocol 都落在正确的列上。
func TestProcessBatch_PassesCompositeCursorAndWritesEnrichedValues(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	wmAt := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)

	// 1) 读水位线
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).AddRow(wmAt, "req-prev"))

	// 2) 读来源记录 —— 游标必须是 (true, wmAt, "req-prev", batchSize)
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, wmAt, "req-prev", int64(100)).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "tenant_id", "occurred_at", "provider_id", "canonical_id",
			"raw_model_name", "api_key_id", "application_id", "end_user_id",
			"prompt_tokens", "completion_tokens", "cache_read_tokens", "reasoning_tokens",
			"image_tokens", "audio_tokens", "video_tokens", "total_tokens",
			"cost_amount", "credits_charged", "status", "traffic_class",
			"error_kind", "latency_ms", "ttft_ms",
		}).AddRow(
			"req-1", "t1", time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC),
			nil, nil, "gpt-x", nil, nil, nil,
			10, 20, 0, 0,
			0, 0, 0, 30,
			1.15, 0, "success", "business",
			nil, 5, 1,
		))

	// 3) 增强维度（活库列名）
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs("req-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "upstream_protocol",
		}).AddRow("sess-1", "task-1", 2, "openai", "anthropic"))

	// 4) 写入
	mock.ExpectBegin()
	mock.ExpectExec(upsertRequestMetricSQL).
		WithArgs(writeArgMatchers()...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := NewProcessor(db)
	processed, err := p.ProcessBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d，期望 1", processed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("数据库交互与预期不符: %v", err)
	}
}

// writeArgMatchers 构造写入参数的匹配器：除关键列外一律 AnyArg，
// 关键列（会话/任务/模态/协议/附件数/费用/来源）逐一钉死。
func writeArgMatchers() []driver.Value {
	matchers := make([]driver.Value, 42)
	for i := range matchers {
		matchers[i] = sqlmock.AnyArg()
	}
	matchers[14] = "sess-1"          // session_id ← gw_session_id
	matchers[15] = "task-1"          // task_id ← gw_task_id
	matchers[20] = "multimodal"      // modality（派生：带附件）
	matchers[21] = "openai"          // protocol ← client_protocol
	matchers[22] = int64(2)          // attachment_count
	matchers[32] = int64(115)        // cost_cents ← round(1.15 * 100)
	matchers[41] = "async_processor" // source
	return matchers
}

// ★ 阻断级缺陷回归：迁移 890 已把 v1 族 request_logs（含母表）整体删除，
// 裸读 FROM request_logs 在生产上直接 42P01 ⇒ 增强整条静默失败
// （请求照进库、计数照涨，只是所有会话/任务/协议维度全空 ⇒ 从数据上看不出坏了）。
// 这里用**仓内守卫自己的那条正则**自检，保证以后不会有人再写回裸母表读。
func TestEnrichDimensionsSQL_UsesCanonicalTwoLeggedView(t *testing.T) {
	// 与 internal/sqlreadguard/guard_test.go 同一形态的正则
	bareRequestLogsRe := regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`)

	if bareRequestLogsRe.MatchString(enrichDimensionsSQL) {
		t.Errorf("enrich 查询不得裸读 v1 母表（已被迁移 890 删除）：%s", enrichDimensionsSQL)
	}
	if !strings.Contains(enrichDimensionsSQL, "FROM request_logs_with_current_month") {
		t.Errorf("enrich 数据源必须是双腿视图 request_logs_with_current_month：%s", enrichDimensionsSQL)
	}
	// 协议列必须是视图真实存在的列（视图里没有 upstream_protocol）
	if strings.Contains(enrichDimensionsSQL, "upstream_protocol") {
		t.Error("视图里没有 upstream_protocol（协议回落列应是 egress_protocol）")
	}
	if !strings.Contains(enrichDimensionsSQL, "egress_protocol") {
		t.Error("应取视图的 egress_protocol 作为协议回落列")
	}
}

// ★ 缺陷回归：enhance 失败必须**外报**。
// 静默吞掉的形态最坏：表/列被删 ⇒ 请求照进库、计数照涨，只是维度全空 ⇒ 无从发现。
func TestEnrichDimensions_SurfacesErrorInsteadOfSwallowing(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	mock.ExpectQuery(enrichDimensionsSQL).
		WillReturnError(errors.New(`relation "request_logs" does not exist`))

	p := NewProcessor(db)
	metric := &RequestMetric{RequestID: "req-x", TenantID: "t1"}

	err := p.enrichDimensions(context.Background(), metric)
	if err == nil {
		t.Fatal("增强失败必须返回错误（吞掉＝维度全空却看不出来）")
	}
	if !strings.Contains(err.Error(), "req-x") {
		t.Errorf("错误信息应带上 request_id，实际：%v", err)
	}
}

// ★ 增强失败**不阻断**计量落库：维度空着，token/费用必须照写。
func TestProcessBatch_EnrichFailureStillWritesMetrics(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	wmAt := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).AddRow(wmAt, "req-prev"))
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, wmAt, "req-prev", int64(100)).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "tenant_id", "occurred_at", "provider_id", "canonical_id",
			"raw_model_name", "api_key_id", "application_id", "end_user_id",
			"prompt_tokens", "completion_tokens", "cache_read_tokens", "reasoning_tokens",
			"image_tokens", "audio_tokens", "video_tokens", "total_tokens",
			"cost_amount", "credits_charged", "status", "traffic_class",
			"error_kind", "latency_ms", "ttft_ms",
		}).AddRow(
			"req-1", "t1", time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC),
			nil, nil, "gpt-x", nil, nil, nil,
			10, 20, 0, 0,
			0, 0, 0, 30,
			1.15, 0, "success", "business",
			nil, 5, 1,
		))
	// 增强查询失败
	mock.ExpectQuery(enrichDimensionsSQL).
		WillReturnError(errors.New(`relation "request_logs_with_current_month" does not exist`))
	// 但计量行仍要落库
	mock.ExpectBegin()
	mock.ExpectExec(upsertRequestMetricSQL).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := NewProcessor(db)
	processed, err := p.ProcessBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("增强失败不应让整批失败（计量不能丢）：%v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d，期望 1", processed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("数据库交互与预期不符: %v", err)
	}
}

// ★ 水位线读取顺序必须与来源查询的游标口径**完全一致**（occurred_at DESC, request_id DESC），
// 否则会出现「读出来的最大值不是游标意义上的最大值」⇒ 漏数或重复。
func TestWatermarkSQL_OrdersBySameCompositeKeyAsCursor(t *testing.T) {
	if !strings.Contains(watermarkSQL, "ORDER BY occurred_at DESC, request_id DESC") {
		t.Errorf("水位线必须按 (occurred_at DESC, request_id DESC) 取最大复合游标：%s", watermarkSQL)
	}
	if !strings.Contains(selectUsageFactsSQL, "(src.occurred_at, src.request_id) >") {
		t.Errorf("来源查询必须用同一个复合游标比较：%s", selectUsageFactsSQL)
	}
}
