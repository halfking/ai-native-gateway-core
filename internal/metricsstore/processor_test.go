package metricsstore

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"log"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
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

	// 3) 批量增强维度（★ 整批一次；参数是 request_id 数组，不再是单值）
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs(pq.Array([]string{"req-1"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}).AddRow("req-1", "sess-1", "task-1", 2, "openai", "anthropic"))

	// 4) 批量模型映射
	// ★★ 这条 expectation 以前是**缺**的：生产代码把该查询的失败**吞掉**（只打日志）
	//   ⇒ sqlmock 抛的「意外调用」错误被业务代码吸收 ⇒ 用例照样绿
	//   ⇒ 也就是说：这条用例**从来没有验证过模型映射这条路**（门全绿 ≠ 门覆盖我）。
	//   补上之后，本用例才真正覆盖「raw_model_name → canonical_id / outbound_model」。
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WithArgs(pq.Array([]string{"gpt-x"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"raw_name", "canonical_id", "canonical_name",
		}).AddRow("gpt-x", int64(42), "glm-5.3-flash"))

	// 5) 写入
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
// 关键列（会话/任务/模态/协议/附件数/费用/来源/标准模型维度）逐一钉死。
//
// ★ 模型映射**成功**且**有会话行**时的期望值。
func writeArgMatchers() []driver.Value {
	m := writeArgMatchersBase()
	m[9] = "glm-5.3-flash" // outbound_model ← model_aliases.canonical_name
	m[10] = int64(42)      // canonical_id ← model_aliases.canonical_id
	m[11] = "gpt-x"        // raw_model_name ← usage_facts.raw_model_name
	m[15] = "sess-1"       // session_id ← gw_session_id
	m[16] = "task-1"       // task_id ← gw_task_id
	m[21] = "multimodal"   // modality（派生：带附件）
	m[22] = "openai"       // protocol ← client_protocol
	m[23] = int64(2)       // attachment_count
	return m
}

// writeArgMatchersNoMapping 构造写入参数的匹配器（模型映射**失败**时）：
// raw_model_name 仍来自 usage_facts，但 canonical_id / outbound_model 为空。
func writeArgMatchersNoMapping() []driver.Value {
	m := writeArgMatchersBase()
	m[11] = "gpt-x"      // raw_model_name 仍然来自 usage_facts
	m[15] = "sess-1"     // session_id
	m[16] = "task-1"     // task_id
	m[21] = "multimodal" // modality（派生：带附件）
	m[22] = "openai"     // protocol
	m[23] = int64(2)     // attachment_count
	return m
}

// writeArgMatchersNoSession 构造写入参数的匹配器（该请求在视图里**没有**会话行）：
// 会话/任务/协议维度为空（post-890 视图是会话族两臂体 ⇒ 非会话类请求查不到行
// 是语义正确，不是失败），模态按「无附件、无多模态 token」派生为 text。
func writeArgMatchersNoSession() []driver.Value {
	m := writeArgMatchersBase()
	m[11] = "gpt-x" // raw_model_name 仍然来自 usage_facts
	m[21] = "text"  // modality（无附件 ⇒ text）
	return m
}

func writeArgMatchersBase() []driver.Value {
	matchers := make([]driver.Value, 43)
	for i := range matchers {
		matchers[i] = sqlmock.AnyArg()
	}
	matchers[33] = int64(115)        // cost_cents ← round(1.15 * 100)
	matchers[42] = "async_processor" // source
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
	// ★ 模型映射查询仍要发生（增强失败不能短路掉它，否则映射这条路的失败会看不见）
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WillReturnRows(sqlmock.NewRows([]string{
			"raw_name", "canonical_id", "canonical_name",
		}).AddRow("gpt-x", int64(42), "glm-5.3-flash"))
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

// ★ 缺陷回归：post-890 的 canonical 视图是**会话族两臂体** ⇒ 非会话类请求
// （探测流量 / 健康检查 / 非会话 API）**本来就查不到行**。
// 「没有会话行」是语义正确，**不能**当成增强失败 ⇒
// 否则良性情况每批都刷「维度增强失败」汇总日志，把真失败（表被删/列被改）淹没。
func TestEnrichDimensions_NoSessionRowIsNotAFailure(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	// 视图里没有该请求的行 ⇒ 结果集里没有这个 request_id
	mock.ExpectQuery(enrichDimensionsSQL).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}))

	p := NewProcessor(db)
	metric := &RequestMetric{RequestID: "req-probe", TenantID: "t1"}

	if err := p.enrichDimensions(context.Background(), metric); err != nil {
		t.Fatalf("查不到会话行不算增强失败（会刷假警报淹没真警报）：%v", err)
	}
	if metric.SessionID != nil {
		t.Errorf("无会话行时 session_id 应保持为空，实际 %v", *metric.SessionID)
	}
	// 模态仍要按默认值派生（无附件 ⇒ text）
	if metric.Modality == nil || *metric.Modality != "text" {
		t.Errorf("无会话行时 modality 应派生为 text，实际 %v", metric.Modality)
	}
}

// ★ 缺陷回归（2026-11-24 审计修正轮）：模型映射失败**不可静默**。
// 失败 ⇒ 本批带 raw_model_name 的行 canonical_id / outbound_model 维度为空，
// 而行照样落库、水位线照样推进 ⇒ 这是**永久**的维度丢失（不会被重试补齐）
// ⇒ 唯一可见出口就是这条汇总日志（「静默失败」正是那次事故的根因）。
func TestProcessBatch_ModelMappingFailureIsNotSilentlySwallowed(t *testing.T) {
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
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs(pq.Array([]string{"req-1"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}).AddRow("req-1", "sess-1", "task-1", 2, "openai", "anthropic"))
	// ★ 模型映射查询失败
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WillReturnError(errors.New(`relation "model_aliases" does not exist`))
	// 计量行仍要落库（映射失败不阻断计量）
	mock.ExpectBegin()
	mock.ExpectExec(upsertRequestMetricSQL).
		WithArgs(writeArgMatchersNoMapping()...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	var buf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOut)

	p := NewProcessor(db)
	processed, err := p.ProcessBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("映射失败不应让整批失败（计量不能丢）：%v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d，期望 1", processed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("数据库交互与预期不符: %v", err)
	}
	if !strings.Contains(buf.String(), "模型映射失败") {
		t.Fatalf("模型映射失败必须可见（汇总日志），实际日志：%s", buf.String())
	}
}

// ★ 缺陷回归（2026-11-24 审计修正轮）：模型别名映射必须**确定**。
// model_aliases 的唯一约束是 UNIQUE(canonical_id, raw_name)
// ⇒ 同一个 raw_name 合法地对应**多个** canonical_id
// ⇒ 批量查询若不给唯一胜者，同一模型在不同批次可能落到不同 canonical_id
//
//	⇒ 按模型聚合时同一模型被拆成两个口径（且不可复现）。
//
// 钉死：DISTINCT ON (raw_name) + ORDER BY (raw_name, canonical_id)。
func TestBatchEnrichModelInfoSQL_IsDeterministicPerRawName(t *testing.T) {
	if !strings.Contains(batchEnrichModelInfoSQL, "DISTINCT ON (ma.raw_name)") {
		t.Errorf("批量映射必须按 raw_name 取唯一一行：%s", batchEnrichModelInfoSQL)
	}
	if !strings.Contains(batchEnrichModelInfoSQL, "ORDER BY ma.raw_name, ma.canonical_id") {
		t.Errorf("批量映射必须给出确定的胜者（按 canonical_id 升序）：%s", batchEnrichModelInfoSQL)
	}
	if strings.Contains(batchEnrichModelInfoSQL, "LIMIT 1") {
		t.Errorf("LIMIT 1 与 DISTINCT ON 混用没有意义，且会掩盖非确定性：%s", batchEnrichModelInfoSQL)
	}
}

// ★ 缺陷回归（2026-11-24 审计修正轮）：维度增强必须**整批一次**。
// 上一轮「Phase 2 批量查询优化」只批量化了轻查询（model_aliases），
// 把重查询（双腿视图）留在逐行形态 ⇒ 每批仍是 N+1 次 ⇒ 提交说明与实际不符。
func TestEnrichDimensionsSQL_BatchesAllRequestIDsInOneQuery(t *testing.T) {
	if !strings.Contains(enrichDimensionsSQL, "ANY($1)") {
		t.Errorf("维度增强必须整批一次（request_id = ANY($1)）：%s", enrichDimensionsSQL)
	}
	if !strings.Contains(enrichDimensionsSQL, "DISTINCT ON (rl.request_id)") {
		t.Errorf("必须按 request_id 取唯一一行（等价于逐行 ORDER BY ts DESC LIMIT 1）：%s", enrichDimensionsSQL)
	}
	if !strings.Contains(enrichDimensionsSQL, "ORDER BY rl.request_id, rl.ts DESC") {
		t.Errorf("必须按 (request_id, ts DESC) 排序取最新一条：%s", enrichDimensionsSQL)
	}
}

// ★ 缺陷回归（2026-11-24 审计修正轮）：维度增强**每批只发一次**查询。
// 两条来源记录 ⇒ 维度查询仍然只有 1 次（逐行形态会是 2 次）。
// ★ 这条是「量具真正覆盖被测对象」的对拍：实现若退回逐行，这里会因
//
//	「意外调用」而红（sqlmock 有序匹配）。
func TestProcessBatch_EnrichesDimensionsInOneQueryPerBatch(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	wmAt := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).AddRow(wmAt, "req-prev"))

	cols := []string{
		"request_id", "tenant_id", "occurred_at", "provider_id", "canonical_id",
		"raw_model_name", "api_key_id", "application_id", "end_user_id",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "reasoning_tokens",
		"image_tokens", "audio_tokens", "video_tokens", "total_tokens",
		"cost_amount", "credits_charged", "status", "traffic_class",
		"error_kind", "latency_ms", "ttft_ms",
	}
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, wmAt, "req-prev", int64(100)).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("req-1", "t1", wmAt, nil, nil, "gpt-x", nil, nil, nil,
				10, 20, 0, 0, 0, 0, 0, 30, 1.15, 0, "success", "business", nil, 5, 1).
			AddRow("req-2", "t1", wmAt, nil, nil, "gpt-x", nil, nil, nil,
				10, 20, 0, 0, 0, 0, 0, 30, 1.15, 0, "success", "business", nil, 5, 1))

	// ★ 只 expect 一次维度查询：参数是整批的 request_id 数组
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs(pq.Array([]string{"req-1", "req-2"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}).AddRow("req-1", "sess-1", "task-1", 2, "openai", "anthropic"))

	// ★ 模型映射也只有一次（两条记录的 raw_model_name 相同 ⇒ 去重成一个）
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WithArgs(pq.Array([]string{"gpt-x"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"raw_name", "canonical_id", "canonical_name",
		}).AddRow("gpt-x", int64(42), "glm-5.3-flash"))

	mock.ExpectBegin()
	// ★ 2026-11-24 吞吐优化轮：写入从「逐行 Exec」改为「**1 条多行 INSERT**」
	// ⇒ 两条记录合成一条语句、86 个参数（43/行）。
	//
	// ★ 关键性质：多行 INSERT 的**语句文本只取决于行数**（占位符按 43/行平移），
	//   与各行的具体值无关 ⇒ 值用既有的宽松匹配器（关键列钉死、其余 AnyArg）。
	// req-1 有会话行；req-2 在视图里没有行 ⇒ 维度留空（语义正确，不是失败）
	expectedSQL, _, err := buildBatchUpsert([]*RequestMetric{
		sampleMetric("req-1", "2026-11-20"),
		sampleMetric("req-2", "2026-11-20"),
	})
	if err != nil {
		t.Fatalf("buildBatchUpsert: %v", err)
	}
	row1 := writeArgMatchers()
	row2 := writeArgMatchersNoSession()
	merged := append(append([]driver.Value{}, row1...), row2...)
	mock.ExpectExec(expectedSQL).
		WithArgs(merged...).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	p := NewProcessor(db)
	processed, err := p.ProcessBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d，期望 2", processed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("数据库交互与预期不符（若这里报「意外调用」，说明维度/映射退回逐行了）: %v", err)
	}
}

// strPtr 返回指向 s 的指针（测试构造用）。
func strPtr(s string) *string { return &s }

// int64Ptr 返回指向 i 的指针（测试构造用）。
func int64Ptr(i int64) *int64 { return &i }

// int32Ptr 返回指向 i 的指针（测试构造用）。
func int32Ptr(i int32) *int32 { return &i }

// ★ 2026-11-24 吞吐优化轮：**自适应追平**的钉测。
//
// ★ 为什么这条最重要：旧形态是「每 tick 只处理一批」（上限 = batchSize/interval，
//
//	实测「5 分钟 100 条」= 1,200 条/小时，而实测流量已达 1,100 条/小时 ⇒ 余量 7%）。
//	新形态是「每 tick 连续追平，直到本批不满」⇒ 吞吐随积压自适应。
//
// ★ 断言的是**次数性质**，不是「调用成功」：
//
//	① 本批**不满** ⇒ 必须停止（再查一次就是多余的库往返）。
//	② 本批**满** ⇒ 必须继续追下一批（否则积压永远追不上）。
//
// ★ 若实现退回「每 tick 只跑一批」，②会红（第二轮查询是「意外调用」）。
func TestProcessorDrain_StopsWhenBatchNotFull(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	// 一个批次只返回 1 行（batchSize=10 ⇒ 不满）⇒ drain 必须只跑 1 轮。
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).
			AddRow(time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), "req-prev"))
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC), "req-prev", int64(10)).
		WillReturnRows(sqlmock.NewRows(usageFactsCols()).
			AddRow("req-1", "t1", time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC),
				nil, nil, "gpt-x", nil, nil, nil,
				10, 20, 0, 0, 0, 0, 0, 30, 1.15, 0, "success", "business", nil, 5, 1))
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs(pq.Array([]string{"req-1"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}))
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WithArgs(pq.Array([]string{"gpt-x"})).
		WillReturnRows(sqlmock.NewRows([]string{"raw_name", "canonical_id", "canonical_name"}))
	mock.ExpectBegin()
	mock.ExpectExec(upsertRequestMetricSQL).
		WithArgs(writeArgMatchersNoSession()...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := NewProcessor(db)
	p.drain(context.Background(), Config{Interval: time.Minute, BatchSize: 10, MaxRoundsPerTick: 20})

	// ★ 若 drain 多跑了一轮，这里的 ExpectationsWereMet 会因「多余的查询」而红。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("本批不满时应停止追平（不应有多余的库往返）: %v", err)
	}
}

// ★ 2026-11-24 吞吐优化轮：本批**满**时必须继续追下一批。
//
// ★ 这是吞吐优化的核心性质：旧形态（每 tick 一批）在此会红 ——
//
//	第二轮的查询会变成「意外调用」⇒ 门直接转红。
func TestProcessorDrain_ContinuesWhenBatchFull(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	wmAt := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)

	// 第 1 轮：满批（batchSize=1 ⇒ 返回 1 行即满）。
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).AddRow(wmAt, "req-prev"))
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, wmAt, "req-prev", int64(1)).
		WillReturnRows(sqlmock.NewRows(usageFactsCols()).
			AddRow("req-1", "t1", wmAt, nil, nil, "gpt-x", nil, nil, nil,
				10, 20, 0, 0, 0, 0, 0, 30, 1.15, 0, "success", "business", nil, 5, 1))
	mock.ExpectQuery(enrichDimensionsSQL).
		WithArgs(pq.Array([]string{"req-1"})).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "gw_session_id", "gw_task_id", "attachment_count",
			"client_protocol", "egress_protocol",
		}))
	mock.ExpectQuery(batchEnrichModelInfoSQL).
		WithArgs(pq.Array([]string{"gpt-x"})).
		WillReturnRows(sqlmock.NewRows([]string{"raw_name", "canonical_id", "canonical_name"}))
	mock.ExpectBegin()
	mock.ExpectExec(upsertRequestMetricSQL).
		WithArgs(writeArgMatchersNoSession()...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// 第 2 轮：不满 ⇒ 停止。
	//
	// ★ 注意：第 2 轮读到 0 行 ⇒ **不发**维度/映射查询
	//   （生产代码用 len(metrics) > 0 守卫 ⇒ 空批不空查库）。
	mock.ExpectQuery(watermarkSQL).WillReturnRows(
		sqlmock.NewRows([]string{"occurred_at", "request_id"}).AddRow(wmAt, "req-1"))
	mock.ExpectQuery(selectUsageFactsSQL).
		WithArgs(true, wmAt, "req-1", int64(1)).
		WillReturnRows(sqlmock.NewRows(usageFactsCols()))

	p := NewProcessor(db)
	p.drain(context.Background(), Config{Interval: time.Minute, BatchSize: 1, MaxRoundsPerTick: 20})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("本批满时必须继续追下一批: %v", err)
	}
}

// usageFactsCols 返回 selectUsageFactsSQL 的列清单（测试复用）。
func usageFactsCols() []string {
	return []string{
		"request_id", "tenant_id", "occurred_at", "provider_id", "canonical_id",
		"raw_model_name", "api_key_id", "application_id", "end_user_id",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "reasoning_tokens",
		"image_tokens", "audio_tokens", "video_tokens", "total_tokens",
		"cost_amount", "credits_charged", "status", "traffic_class",
		"error_kind", "latency_ms", "ttft_ms",
	}
}

// ★ 2026-11-24 吞吐优化轮：环境变量覆盖。
//
// ★ 三条判据：
//
//	① 合法值要生效（容量必须能在不改代码的前提下调）。
//	② 非法值要**回落默认值**并**不失败**（后台 worker 的配置写错
//	  不该让整个网关起不来 —— 那会把「统计表延迟」升级成「网关不可用」）。
//	③ 未设置时用默认值。
func TestConfigFromEnv_OverridesAndFallsBack(t *testing.T) {
	base := DefaultConfig()

	// ③ 未设置 ⇒ 默认值。
	t.Setenv("LLM_GATEWAY_METRICS_INTERVAL", "")
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "")
	if got := ConfigFromEnv(base); got != base {
		t.Errorf("未设置环境变量时应返回默认配置: got %+v want %+v", got, base)
	}

	// ① 合法值生效。
	t.Setenv("LLM_GATEWAY_METRICS_INTERVAL", "30s")
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "2000")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "5")
	got := ConfigFromEnv(base)
	if got.Interval != 30*time.Second {
		t.Errorf("Interval = %v，期望 30s", got.Interval)
	}
	if got.BatchSize != 2000 {
		t.Errorf("BatchSize = %d，期望 2000", got.BatchSize)
	}
	if got.MaxRoundsPerTick != 5 {
		t.Errorf("MaxRoundsPerTick = %d，期望 5", got.MaxRoundsPerTick)
	}

	// ② 非法值回落默认值（且不 panic、不报错）。
	t.Setenv("LLM_GATEWAY_METRICS_INTERVAL", "not-a-duration")
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "abc")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "-1")
	got = ConfigFromEnv(base)
	if got.Interval != base.Interval {
		t.Errorf("非法 Interval 应回落默认 %v，实际 %v", base.Interval, got.Interval)
	}
	if got.BatchSize != base.BatchSize {
		t.Errorf("非法 BatchSize 应回落默认 %d，实际 %d", base.BatchSize, got.BatchSize)
	}
	if got.MaxRoundsPerTick != base.MaxRoundsPerTick {
		t.Errorf("非法 MaxRoundsPerTick 应回落默认 %d，实际 %d", base.MaxRoundsPerTick, got.MaxRoundsPerTick)
	}
}

// 2026-11-24 12h 审计轮：BATCH_SIZE / MAX_ROUNDS 上限钳制钉测。
// 极端值必须被钳到 cap 而不是透传（透传=BATCH_SIZE 拉爆内存 / MAX_ROUNDS 拉长单 tick 成准死循环）。
func TestConfigFromEnv_ClampsExcessiveValues(t *testing.T) {
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "999999999")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "100000")
	cfg := ConfigFromEnv(DefaultConfig())
	if cfg.BatchSize != maxEnvBatchSizeCap {
		t.Fatalf("BatchSize 未钳制: got=%d want=%d", cfg.BatchSize, maxEnvBatchSizeCap)
	}
	if cfg.MaxRoundsPerTick != maxEnvMaxRoundsCap {
		t.Fatalf("MaxRoundsPerTick 未钳制: got=%d want=%d", cfg.MaxRoundsPerTick, maxEnvMaxRoundsCap)
	}
	// 合法值仍透传
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "800")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "50")
	cfg = ConfigFromEnv(DefaultConfig())
	if cfg.BatchSize != 800 || cfg.MaxRoundsPerTick != 50 {
		t.Fatalf("合法值被误钳: batch=%d rounds=%d", cfg.BatchSize, cfg.MaxRoundsPerTick)
	}
}
