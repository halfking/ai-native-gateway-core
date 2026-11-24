package metricsstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// 这组用例钉住 request_metrics 的写入契约：
// ① 批量写入必须落在**同一个事务**里（批内失败 ⇒ 整批回滚，不留半批数据）；
// ② ON CONFLICT 必须更新**所有**业务列（否则旧值长期残留）；
// ③ attributes 必须是 jsonb（不是 bytea）；
// ④ 参数顺序与 SQL 占位符一一对应（错位会让值写进错误的列，且不报错）。

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	return db, mock, func() { _ = db.Close() }
}

func TestRequestMetric_Struct(t *testing.T) {
	// 测试结构体字段完整性
	metric := &RequestMetric{
		RequestID:    "req_123",
		TenantID:     "tenant_1",
		OccurredAt:   time.Now(),
		OccurredDate: time.Now(),
		BillingDate:  time.Now(),

		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,

		CostCents:      1234, // $12.34
		CreditsCharged: 5000,

		Status:       "success",
		TrafficClass: "business",

		Source: "test",
	}

	if metric.RequestID != "req_123" {
		t.Errorf("RequestID mismatch")
	}

	if metric.CostCents != 1234 {
		t.Errorf("CostCents mismatch: got %d, want 1234", metric.CostCents)
	}
}

// ★ 缺陷回归：批量写入原先在循环里调 w.UpsertRequestMetric（走 w.db.ExecContext）
// ⇒ 完全绕过事务 ⇒ 批内失败时前面成功的行已提交，库里留下半批数据。
// 这里用 sqlmock 断言：整批走 Begin → Exec → Commit，且**没有**额外的顶层 Exec。
//
// ★ 2026-11-24 吞吐优化轮：批内从「N 次单行 upsert」改为「**1 条多行 INSERT**」
// ⇒ 这条用例现在同时钉住**次数性质**：2 行 ⇒ **恰好 1 次** Exec
// （不是 2 次）⇒ 多出来的调用无处可去 ⇒ 实现退回逐行写法时门直接红。
func TestBatchUpsertRequestMetrics_RunsInsideOneTransaction(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	metrics := []*RequestMetric{
		sampleMetric("batch-1", "2026-11-20"),
		sampleMetric("batch-2", "2026-11-20"),
	}

	expectedSQL, expectedArgs, err := buildBatchUpsert(metrics)
	if err != nil {
		t.Fatalf("buildBatchUpsert: %v", err)
	}

	mock.ExpectBegin()
	// ★ 只 expect **一次** Exec：两行合成一条多行 INSERT。
	//   若实现退回「每行一次 upsert」，第二次 Exec 就是「意外调用」⇒ 用例转红。
	mock.ExpectExec(expectedSQL).
		WithArgs(toDriverValues(expectedArgs)...).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	w := NewWriter(db)
	if err := w.BatchUpsertRequestMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("BatchUpsertRequestMetrics: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("期望整批在一个事务里、且只发一次 INSERT: %v", err)
	}
}

// ★ 2026-11-24 吞吐优化轮：多行 INSERT 的**参数编号**必须逐行平移，
// 且**类型转换必须原样保留**（$42::jsonb / $4::date）。
//
// ★ 为什么单独钉：丢一个 ::jsonb ⇒ attributes 写入报「column is of type jsonb
//
//	but expression is of type text」⇒ 整批回滚 ⇒ 统计表直接停更。
func TestBuildBatchUpsert_ShiftsPlaceholdersAndKeepsCasts(t *testing.T) {
	metrics := []*RequestMetric{
		sampleMetric("row-a", "2026-11-20"),
		sampleMetric("row-b", "2026-11-20"),
		sampleMetric("row-c", "2026-11-20"),
	}

	query, args, err := buildBatchUpsert(metrics)
	if err != nil {
		t.Fatalf("buildBatchUpsert: %v", err)
	}

	// 3 行 × 43 参数 = 129 个参数
	if len(args) != 129 {
		t.Fatalf("参数个数 = %d，期望 129（3 行 × 43）", len(args))
	}

	// 第 1 行用 $1..$43；第 2 行用 $44..$86；第 3 行用 $87..$129。
	if !strings.Contains(query, "$1") {
		t.Error("第 1 行应使用 $1 起始的占位符")
	}
	if !strings.Contains(query, "$44") {
		t.Error("第 2 行应从 $44 起（43 参数/行 ⇒ 平移 43）")
	}
	if !strings.Contains(query, "$87") {
		t.Error("第 3 行应从 $87 起（平移 86）")
	}

	// ★ 类型转换必须随占位符一起平移。
	if !strings.Contains(query, "$42::jsonb") {
		t.Error("第 1 行的 attributes 必须仍是 $42::jsonb")
	}
	if !strings.Contains(query, "$85::jsonb") {
		t.Error("第 2 行的 attributes 应平移为 $85::jsonb（42+43）")
	}
	if !strings.Contains(query, "$128::jsonb") {
		t.Error("第 3 行的 attributes 应平移为 $128::jsonb（42+86）")
	}
	if !strings.Contains(query, "$4::date") {
		t.Error("occurred_date 的 ::date 转换必须保留")
	}
	if !strings.Contains(query, "$47::date") {
		t.Error("第 2 行的 occurred_date 应平移为 $47::date")
	}

	// ★ 占位符总数必须等于参数个数（多一个 ⇒ 编号错位 ⇒ 值进错列）。
	if got := countPlaceholders(query); got != len(args) {
		t.Errorf("SQL 里占位符个数 = %d，参数个数 = %d ⇒ 两者必须相等（错位会把值写进错误的列且不报错）", got, len(args))
	}
}

// countPlaceholders 统计 SQL 里 $N 形式的占位符个数。
func countPlaceholders(query string) int {
	count := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			j++
		}
		if j > i+1 {
			count++
			i = j - 1
		}
	}
	return count
}

// ★ 2026-11-24 吞吐优化轮：超大批次必须**分块**，
// 因为 PostgreSQL 单条语句的绑定参数上限是 65535（43 参数/行 ⇒ 1524 行是硬顶）。
//
// ★ 为什么钉：不分块 ⇒ 大批次直接报「extended protocol limited to 65535 parameters」
// ⇒ 整批回滚 ⇒ 统计表停更（而这恰恰发生在流量高峰、最需要它的时候）。
func TestBuildBatchUpsert_ChunksLargeBatches(t *testing.T) {
	// 造 maxBatchInsertRows+1 行 ⇒ 应分成 2 块。
	metrics := make([]*RequestMetric, maxBatchInsertRows+1)
	for i := range metrics {
		metrics[i] = sampleMetric("chunk-"+strconv.Itoa(i), "2026-11-20")
	}

	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	chunk1, args1, err := buildBatchUpsert(metrics[:maxBatchInsertRows])
	if err != nil {
		t.Fatalf("buildBatchUpsert(chunk1): %v", err)
	}
	chunk2, args2, err := buildBatchUpsert(metrics[maxBatchInsertRows:])
	if err != nil {
		t.Fatalf("buildBatchUpsert(chunk2): %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(chunk1).WithArgs(toDriverValues(args1)...).WillReturnResult(sqlmock.NewResult(0, maxBatchInsertRows))
	mock.ExpectExec(chunk2).WithArgs(toDriverValues(args2)...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	w := NewWriter(db)
	if err := w.BatchUpsertRequestMetrics(context.Background(), metrics); err != nil {
		t.Fatalf("BatchUpsertRequestMetrics: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("期望超大批次被分块写入: %v", err)
	}

	// 单块参数数不得超过 PG 的 65535 硬顶。
	if len(args1) > 65535 {
		t.Errorf("单块参数数 = %d，超过 PostgreSQL 的 65535 上限", len(args1))
	}
}

// ★ 缺陷回归：批内失败 ⇒ 整批回滚。
//
// ★ 2026-11-24 吞吐优化轮：改成多行 INSERT 后，失败点从「第 2 条」变成
//
//	「这条 INSERT 语句本身」⇒ 仍必须整批回滚（不留半批数据）。
func TestBatchUpsertRequestMetrics_RollsBackWholeBatch(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	metrics := []*RequestMetric{
		sampleMetric("batch-ok-1", "2026-11-20"),
		sampleMetric("batch-bad-1", "2026-11-20"),
	}

	expectedSQL, expectedArgs, err := buildBatchUpsert(metrics)
	if err != nil {
		t.Fatalf("buildBatchUpsert: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(expectedSQL).
		WithArgs(toDriverValues(expectedArgs)...).
		WillReturnError(errors.New("probe: rejected batch-bad-1"))
	mock.ExpectRollback()

	w := NewWriter(db)

	err = w.BatchUpsertRequestMetrics(context.Background(), metrics)
	if err == nil {
		t.Fatal("批内失败应返回错误")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("期望失败后整批回滚: %v", err)
	}
}

func TestBatchUpsertRequestMetrics_EmptySlice(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()

	w := NewWriter(db)
	if err := w.BatchUpsertRequestMetrics(context.Background(), []*RequestMetric{}); err != nil {
		t.Errorf("空批次不应报错: %v", err)
	}
	// 空批次连事务都不该开
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("空批次不应产生任何数据库调用: %v", err)
	}
}

// ★ ON CONFLICT 必须更新所有业务列。
// 这里不靠肉眼扫 SQL，而是把 DO UPDATE SET 里的列名抽出来，与应有集合比对 ——
// 少更新一列时，库里会长期留着旧值，而且**不会报错**。
func TestUpsertRequestMetricSQL_ConflictUpdatesEveryColumn(t *testing.T) {
	updated := parseUpdateColumns(upsertRequestMetricSQL)
	if len(updated) == 0 {
		t.Fatal("未解析到 ON CONFLICT DO UPDATE SET 列")
	}

	want := []string{
		"tenant_id", "occurred_date", "billing_date",
		"gateway_node_id", "gateway_instance", "endpoint_node",
		"provider_id", "outbound_model", "canonical_id",
		"payer_id", "payer_type", "user_id",
		"session_id", "task_id",
		"org_id", "team_id",
		"api_key_id", "application_id",
		"modality", "protocol", "attachment_count", "attachment_size",
		"prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "total_tokens",
		"image_tokens", "audio_tokens", "video_tokens",
		"cost_cents", "credits_charged",
		"status", "traffic_class", "request_type", "error_kind",
		"latency_ms", "ttft_ms",
		"attributes", "source", "updated_at",
	}

	got := map[string]bool{}
	for _, c := range updated {
		got[c] = true
	}
	for _, c := range want {
		if !got[c] {
			t.Errorf("ON CONFLICT 未更新列 %q（缺它会让旧值残留）", c)
		}
	}

	// 冲突键本身不应出现在 SET 里
	for _, key := range []string{"request_id", "occurred_at"} {
		if got[key] {
			t.Errorf("冲突键 %q 不应出现在 DO UPDATE SET 里", key)
		}
	}
}

// parseUpdateColumns 抽取 ON CONFLICT ... DO UPDATE SET 段里被赋值的列名。
func parseUpdateColumns(sqlText string) []string {
	idx := strings.Index(sqlText, "DO UPDATE SET")
	if idx < 0 {
		return nil
	}
	set := sqlText[idx+len("DO UPDATE SET"):]

	var cols []string
	for _, line := range strings.Split(set, ",") {
		line = strings.TrimSpace(line)
		name := strings.SplitN(line, "=", 2)[0]
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, " ") {
			continue
		}
		cols = append(cols, name)
	}
	return cols
}

// ★ attributes 必须是 jsonb：驱动把 Go 字符串按 text 送过来，
// 靠端上显式 ::jsonb 转换解析（bytea→jsonb 没有可用转换）。
func TestUpsertRequestMetricSQL_AttributesUsesJSONBCast(t *testing.T) {
	if !strings.Contains(upsertRequestMetricSQL, "::jsonb") {
		t.Error("attributes 应显式 ::jsonb 转换")
	}
	if strings.Contains(upsertRequestMetricSQL, "attributes, source, created_at, updated_at\n\t) VALUES") &&
		!strings.Contains(upsertRequestMetricSQL, "$41::jsonb") {
		t.Error("attributes 占位符应带 ::jsonb")
	}
}

// ★ 参数顺序必须与 SQL 占位符一一对应：错位会把值写进错误的列且不报错。
func TestRequestMetricArgs_OrderMatchesPlaceholders(t *testing.T) {
	gw := "node-1"
	sess := "sess-1"
	task := "task-1"
	modality := "text"
	protocol := "openai"

	metric := &RequestMetric{
		RequestID:       "req-1",
		TenantID:        "t1",
		OccurredAt:      time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC),
		OccurredDate:    time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		BillingDate:     time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		GatewayNodeID:   &gw,
		SessionID:       &sess,
		TaskID:          &task,
		Modality:        &modality,
		Protocol:        &protocol,
		AttachmentCount: 2,
		CostCents:       115,
		Status:          "success",
		TrafficClass:    "business",
		Source:          "async_processor",
	}

	args, err := requestMetricArgs(metric)
	if err != nil {
		t.Fatalf("requestMetricArgs: %v", err)
	}
	if len(args) != 43 {
		t.Fatalf("参数个数应为 43，实际 %d", len(args))
	}

	// 占位符个数必须与参数个数一致
	placeholders := len(regexp.MustCompile(`\$[0-9]+`).FindAllString(upsertRequestMetricSQL, -1))
	if placeholders != len(args) {
		t.Errorf("占位符数 %d 与参数数 %d 不一致", placeholders, len(args))
	}

	if args[0] != "req-1" {
		t.Errorf("args[0] 应为 request_id，实际 %v", args[0])
	}
	if args[3] != "2026-11-20" {
		t.Errorf("args[3] 应为 occurred_date 文本，实际 %v", args[3])
	}
	if got := strArg(args[15]); got != "sess-1" {
		t.Errorf("args[15] 应为 session_id，实际 %v", got)
	}
	if got := strArg(args[16]); got != "task-1" {
		t.Errorf("args[16] 应为 task_id，实际 %v", got)
	}
	if got := strArg(args[21]); got != "text" {
		t.Errorf("args[21] 应为 modality，实际 %v", got)
	}
	if got := strArg(args[22]); got != "openai" {
		t.Errorf("args[22] 应为 protocol，实际 %v", got)
	}
	if args[23] != int32(2) {
		t.Errorf("args[23] 应为 attachment_count，实际 %v", args[23])
	}
	if args[33] != int64(115) {
		t.Errorf("args[33] 应为 cost_cents，实际 %v", args[33])
	}
	// args[40] = TTFTMs（本例无值）
	// args[41] = attributesJSON（本例无值应为 nil）
	if args[41] != nil {
		t.Errorf("args[41] 应为 attributes（本例无值应为 nil），实际 %v", args[41])
	}
	if args[42] != "async_processor" {
		t.Errorf("args[42] 应为 source，实际 %v", args[42])
	}
}

// ★ attributes 有值时必须是 JSON 文本（端上 ::jsonb 解析）。
func TestRequestMetricArgs_AttributesMarshaledToJSON(t *testing.T) {
	metric := &RequestMetric{
		RequestID:    "req-1",
		TenantID:     "t1",
		OccurredAt:   time.Now().UTC(),
		Attributes:   map[string]interface{}{"k": "v"},
		Status:       "success",
		TrafficClass: "business",
	}

	args, err := requestMetricArgs(metric)
	if err != nil {
		t.Fatalf("requestMetricArgs: %v", err)
	}
	got, ok := args[41].(string)
	if !ok {
		t.Fatalf("attributes 应序列化为字符串，实际 %T", args[41])
	}
	if got != `{"k":"v"}` {
		t.Errorf("attributes JSON 不符：%s", got)
	}
}

// ★ DATE 列必须送 YYYY-MM-DD 文本：直接送 time.Time 会让 PG 按**会话时区**
// 截断成 date ⇒ 同一个时间在不同 TimeZone 落到不同日期（跨时区偏一天）。
func TestFormatDate_UsesUTCCalendarDay(t *testing.T) {
	loc := time.FixedZone("plus8", 8*3600)
	ts := time.Date(2026, 11, 20, 1, 0, 0, 0, loc) // = 2026-11-19 17:00 UTC

	if got := formatDate(ts); got != "2026-11-19" {
		t.Errorf("formatDate 应按 UTC 日历日输出 2026-11-19，实际 %s", got)
	}
	if got := formatDate(time.Time{}); got != "" {
		t.Errorf("零值日期应输出空串，实际 %q", got)
	}
}

// strArg 取出参数里的字符串值（参数以 *string 传入时需解引用）。
func strArg(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	default:
		return ""
	}
}

func sampleMetric(requestID string, day string) *RequestMetric {
	d, _ := time.Parse("2006-01-02", day)
	at := time.Date(d.Year(), d.Month(), d.Day(), 2, 0, 0, 0, time.UTC)
	gw := "node-1"
	return &RequestMetric{
		RequestID:     requestID,
		TenantID:      "t1",
		OccurredAt:    at,
		OccurredDate:  d,
		BillingDate:   d,
		GatewayNodeID: &gw,
		Status:        "success",
		TrafficClass:  "business",
		Source:        "test",
	}
}

// toDriverValues 把 []interface{} 转成 sqlmock 需要的 []driver.Value。
func toDriverValues(args []interface{}) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}
