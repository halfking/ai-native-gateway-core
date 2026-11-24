package errorplaybook

// 数据面异常探测源（DataQualitySource）的单元与契约测试。
//
// ★ 判据分两层：
//   ① 行为层（sqlmock）：门槛/去重/总闸/失败不阻断/错误上抛 —— 全部
//      进程内、不经真库 ⇒ CI 无环境也可跑；
//   ② 契约层（源码钉）：探测器检查项 ↔ 手册条目（迁移 999a）一一对应、
//      命名域隔离、无重复检查项 —— 防「检查项在探测器里但手册没有
//      对应条目」的静默失配（那会让事件走未知错误分析，每次烧一次
//      LLM 分析费）。
//
// SQL 与真库形状的对账（列名/表名）由真库契约测试承担
//（data_quality_source_realdb_test.go：无 DSN 即跳过，同 995 纪律）。

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// ─────────────────────────────────────────────────────────────────────────────
// ② 契约层：检查项 ↔ 手册条目一一对应（源码钉，无需 DB）
// ─────────────────────────────────────────────────────────────────────────────

// dataQualityCheckTypes 探测器全部检查项（与 data_quality_source.go 闭集同步）。
var dataQualityCheckTypes = []string{
	AnomalyModelAttributionMissing,
	AnomalyBillingCreditGap,
	AnomalySessionSummaryDimMissing,
}

// TestAnomalyTypesAreDataMaintenancePrefixed 钉闭集命名域：所有探测类型都
// 必须带 data_maintenance: 前缀（与 errorsx kind / PI 队列类型命名域隔离，
// PickEntry 的类型级匹配不会误伤既有条目）。
func TestAnomalyTypesAreDataMaintenancePrefixed(t *testing.T) {
	for _, at := range dataQualityCheckTypes {
		if !strings.HasPrefix(at, "data_maintenance:") {
			t.Errorf("anomaly type %q 不带 data_maintenance: 前缀 ⇒ 命名域泄漏", at)
		}
	}
}

// TestAnomalyTypesHaveMigrationEntries 钉「探测器检查项 ↔ 迁移 999a 手册
// 条目」一一对应：每个检查项的 error_type 必须有一条同名种子条目。
//
// ⚠ 本测试读 canonical 迁移文件（读不到即跳过 → CI 无树形态不红）。
func TestAnomalyTypesHaveMigrationEntries(t *testing.T) {
	const migrationRelPath = "../../sql/migrations/startup/999a_data_maintenance_playbook.sql"
	raw, err := os.ReadFile(migrationRelPath)
	if err != nil {
		t.Skipf("迁移文件不可读（非仓内运行？）：%v", err)
	}
	for _, at := range dataQualityCheckTypes {
		if !strings.Contains(string(raw), "'"+at+"'") {
			t.Errorf("探测器检查项 %q 在迁移 999a 里没有对应手册条目 ⇒ 事件将走未知错误分析（每次烧一次 LLM 分析费）", at)
		}
	}
}

// dataQualitySourceSrc 由 go:embed 注入（避免文件路径依赖）。
//
//go:embed data_quality_source.go
var dataQualitySourceSrc string

// TestAnomalyTypesUniqueInSource 钉每个检查项的 anomalyType 在源码里只出现
// 一次（防复制粘贴两段同名检查项 ⇒ 同一事件一轮发两次）。
//
// ⚠ 源码里检查项用的是**常量**（anomalyType: AnomalyModelAttributionMissing），
// 不是字符串字面量 ⇒ needle 用常量名。常量名与闭集的对应关系由
// TestAnomalyTypesDeclaredInDetector（构造后逐项对拍）钉住。
func TestAnomalyTypesUniqueInSource(t *testing.T) {
	constNames := map[string]string{
		AnomalyModelAttributionMissing:  "AnomalyModelAttributionMissing",
		AnomalyBillingCreditGap:         "AnomalyBillingCreditGap",
		AnomalySessionSummaryDimMissing: "AnomalySessionSummaryDimMissing",
	}
	for _, at := range dataQualityCheckTypes {
		needle := "anomalyType: " + constNames[at] + ","
		if n := strings.Count(dataQualitySourceSrc, needle); n != 1 {
			t.Errorf("%s 作为检查项出现 %d 次，want 1（重复 ⇒ 一轮发两次）", at, n)
		}
	}
}

// TestAnomalyTypesDeclaredInDetector 钉探测器构造的检查项数量与闭集一致：
// 新加检查项必须同步更新 dataQualityCheckTypes（否则上面对应关系门失明）。
func TestAnomalyTypesDeclaredInDetector(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close() //nolint:errcheck // 测试桩
	s := NewDataQualitySource(&stubSource{}, db, dqCfgForTest)
	if s == nil {
		t.Fatal("source is nil")
	}
	if len(s.checks) != len(dataQualityCheckTypes) {
		t.Fatalf("探测器检查项 %d 个，闭集 %d 个 ⇒ 不同步（新增检查项要同步更新 dataQualityCheckTypes）",
			len(s.checks), len(dataQualityCheckTypes))
	}
	for i, at := range dataQualityCheckTypes {
		if s.checks[i].anomalyType != at {
			t.Errorf("checks[%d].anomalyType = %q, want %q（顺序也要一致）", i, s.checks[i].anomalyType, at)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ① 行为层（sqlmock）
// ─────────────────────────────────────────────────────────────────────────────

type stubSource struct {
	events []Event
	err    error
}

func (s *stubSource) FetchWindow(context.Context, time.Time, time.Time) ([]Event, time.Time, error) {
	if s.err != nil {
		return nil, time.Time{}, s.err
	}
	return s.events, time.Now(), nil
}

func dqCfgForTest() DataQualityConfig {
	cfg := DefaultDataQualityConfig()
	cfg.Thresholds.ModelAttribution = 5
	cfg.Thresholds.BillingCreditGap = 5
	cfg.Thresholds.SessionSummaryDim = 5
	return cfg
}

// newDetectMockDB 建一个 sqlmock 连接：探测器按固定顺序发 3 条探测查询，
// 每条返回 counts 对应的计数（缺省 0）。failFirst ⇒ 每次查询都失败。
func newDetectMockDB(t *testing.T, counts map[string]int64, failFirst bool) *sql.DB {
	t.Helper()
	db, mock := newDetectMockDBWithMock(t, counts)
	for _, at := range dataQualityCheckTypes {
		var cnt int64
		if v, ok := counts[at]; ok {
			cnt = v
		}
		exp := mock.ExpectQuery("\\s*SELECT COUNT\\(\\*\\)")
		if failFirst {
			exp.WillReturnError(errors.New("detect query boom"))
		} else {
			exp.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(cnt))
		}
	}
	return db
}

func newDetectMockDBWithMock(t *testing.T, counts map[string]int64) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, at := range dataQualityCheckTypes {
		var cnt int64
		if v, ok := counts[at]; ok {
			cnt = v
		}
		mock.ExpectQuery("\\s*SELECT COUNT\\(\\*\\)").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(cnt))
	}
	return db, mock
}

// TestDataQualitySourceDetectsOverThreshold：计数达到门槛 ⇒ 事件进窗口，
// inner 事件照常透传，未达门槛的不产事件。
func TestDataQualitySourceDetectsOverThreshold(t *testing.T) {
	db := newDetectMockDB(t, map[string]int64{
		AnomalyModelAttributionMissing:  10, // ≥ 5
		AnomalyBillingCreditGap:         2,  // < 5
		AnomalySessionSummaryDimMissing: 7,  // ≥ 5
	}, false)
	s := NewDataQualitySource(&stubSource{events: []Event{{ErrorType: "rate_limit"}}}, db, dqCfgForTest)
	events, _, err := s.FetchWindow(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("FetchWindow: %v", err)
	}
	got := map[string]int{}
	inner := 0
	for _, e := range events {
		got[e.ErrorType]++
		if e.ErrorType == "rate_limit" {
			inner++
		}
	}
	if inner != 1 {
		t.Fatalf("inner 事件未透传：rate_limit=%d", inner)
	}
	if got[AnomalyModelAttributionMissing] != 1 || got[AnomalySessionSummaryDimMissing] != 1 {
		t.Fatalf("达标探测项未各产一条事件：%v", got)
	}
	if got[AnomalyBillingCreditGap] != 0 {
		t.Fatalf("未达门槛的检查项不应产事件：%v", got)
	}
}

// TestDataQualitySourceDedupWindow：同一检查项在去重窗口内**不重复发、
// 也不重复查库**（防噪第二级：跳过发生在查询之前 ⇒ 留痕不刷屏的同时
// 探测轮的 DB 开销也是零）。
func TestDataQualitySourceDedupWindow(t *testing.T) {
	db, mock := newDetectMockDBWithMock(t, map[string]int64{
		AnomalyModelAttributionMissing:  10,
		AnomalyBillingCreditGap:         10,
		AnomalySessionSummaryDimMissing: 10,
	})
	// round2 若违约查库，这些兜底期望会让探测成功并返回超大计数
	// ⇒ lastSeen 必然前移 ⇒ 末尾断言红（变异路径不再被错误吞掉）。
	for range dataQualityCheckTypes {
		mock.ExpectQuery(`\s*SELECT COUNT\(\*\)`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(99999))
	}
	s := NewDataQualitySource(&stubSource{}, db, dqCfgForTest)
	ctx := context.Background()
	now := time.Now()
	ev1, _, err := s.FetchWindow(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("round1: %v", err)
	}
	if len(ev1) != 3 {
		t.Fatalf("round1 应报 3 条，got %d: %v", len(ev1), anomalyTypesOf(ev1))
	}
	// 第二轮（窗口内）：三个检查项全部命中去重 ⇒ **0 次查询、0 条事件**。
	// 用 sqlmock 的期望计数做量具：round2 结束后期望必须「被消费完」
	// （round1 消费 3 条）且没有新增 ⇒ 若探测器违约查库，sqlmock 返回
	// 意外查询错误 ⇒ detect 视为失败 ⇒ 不产事件 ⇒ 两条违约路径
	// （查了但发 / 查了不发）都逃不过「期望计数」这一把尺。
	ev2, _, err := s.FetchWindow(ctx, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("round2: %v", err)
	}
	if len(ev2) != 0 {
		t.Fatalf("去重窗口内的第二轮不应产事件，got %d: %v", len(ev2), anomalyTypesOf(ev2))
	}
	// ★ 量探测器内部状态：round2 是去重跳过 ⇒ 每个检查项的 lastSeen
	// 必须**保持 round1 的值**（round1 的 until == now；未被 round2 前移）。
	// 若去重失效（变异：跳过分支被短路），round2 会重新留痕 ⇒ lastSeen
	// 前移到 round2 的 until ⇒ 本断言红。
	for i, c := range s.checks {
		if !c.lastSeen.Equal(now) {
			t.Errorf("checks[%d]（%s）round2 后 lastSeen = %v，want 保持 round1 的 %v ⇒ 去重窗口没有拦住重复留痕",
				i, c.anomalyType, c.lastSeen, now)
		}
	}
}

// TestDataQualitySourceDisabledQuietly：总闸关 ⇒ inner 事件照常透传、
// 探测器不产事件、也不查库。
func TestDataQualitySourceDisabledQuietly(t *testing.T) {
	db, mock := newDetectMockDBWithMock(t, map[string]int64{
		AnomalyModelAttributionMissing:  0,
		AnomalyBillingCreditGap:         0,
		AnomalySessionSummaryDimMissing: 0,
	})
	// 不给 round2 登记任何兜底期望 ⇒ 违约查库会被 sqlmock 判为
	// 「unexpected query」；同时末尾用 ExpectationsWereMet 断言
	// round1 的 3 条期望未被消费（= 探测器在关闭态根本没查库）。
	cfg := dqCfgForTest()
	cfg.Enabled = false
	s := NewDataQualitySource(&stubSource{events: []Event{{ErrorType: "auth"}}}, db, func() DataQualityConfig { return cfg })
	events, _, err := s.FetchWindow(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("FetchWindow: %v", err)
	}
	if len(events) != 1 || events[0].ErrorType != "auth" {
		t.Fatalf("关闭态应只透传 inner 事件，got %v", anomalyTypesOf(events))
	}
	// ★ 关闭态必须 0 次查询：round1 的 3 条期望必须原封不动
	//（被消费 ⇒ 探测器查了库 ⇒ 总闸失效）。变异 M-DQ3（短路总闸）
	// 在这里红。
	if err := mock.ExpectationsWereMet(); err == nil {
		t.Fatal("关闭态下探测期望未被消费本应报错——期望被消费 ⇒ 探测器查了库 ⇒ 总闸失效")
	}
	// 更精确的量具：直接看探测条目状态（关闭态连 lastSeen 都不该动）。
	for i, c := range s.checks {
		if !c.lastSeen.IsZero() {
			t.Errorf("checks[%d]（%s）关闭态被探测过（lastSeen=%v）⇒ 总闸失效", i, c.anomalyType, c.lastSeen)
		}
	}
}

// TestDataQualitySourceFailureDoesNotBlockInner：探测查询失败 ⇒ inner
// 事件照常透传、FetchWindow 不返回错误（探测器坏了不能让上游失败摄取
// 也停摆）。
func TestDataQualitySourceFailureDoesNotBlockInner(t *testing.T) {
	db := newDetectMockDB(t, nil, true)
	s := NewDataQualitySource(&stubSource{events: []Event{{ErrorType: "rate_limit"}, {ErrorType: "timeout"}}}, db, dqCfgForTest)
	events, _, err := s.FetchWindow(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("探测失败不应让 FetchWindow 失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("探测失败时 inner 事件必须照常透传，got %d", len(events))
	}
}

// TestDataQualitySourceInnerErrorPropagates：inner 失败 ⇒ 原样上抛
// （包装器不得吞掉主链路的错误——那是 worker 水位与重试语义的承重面）。
func TestDataQualitySourceInnerErrorPropagates(t *testing.T) {
	db := newDetectMockDB(t, nil, false)
	innerErr := errors.New("inner boom")
	s := NewDataQualitySource(&stubSource{err: innerErr}, db, dqCfgForTest)
	_, _, err := s.FetchWindow(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if !errors.Is(err, innerErr) {
		t.Fatalf("inner 错误应原样上抛，got %v", err)
	}
}

// TestNewDataQualitySourceNilDB：nil db ⇒ nil 源（装配侧按 nil 跳过）。
func TestNewDataQualitySourceNilDB(t *testing.T) {
	if got := NewDataQualitySource(&stubSource{}, nil, dqCfgForTest); got != nil {
		t.Fatal("nil db 应返回 nil 源")
	}
}

// TestDefaultDataQualityConfigSane 钉默认配置的保守性：全开、门槛非零、
// 去重窗口一天。
func TestDefaultDataQualityConfigSane(t *testing.T) {
	cfg := DefaultDataQualityConfig()
	if !cfg.Enabled {
		t.Fatal("默认应启用（探测只读零写入，关闭要显式）")
	}
	if cfg.Thresholds.ModelAttribution <= 0 || cfg.Thresholds.BillingCreditGap <= 0 || cfg.Thresholds.SessionSummaryDim <= 0 {
		t.Fatalf("门槛必须非零（0 会让检查项每轮必报）：%+v", cfg.Thresholds)
	}
	if cfg.DedupWindow < time.Hour {
		t.Fatalf("去重窗口过短（%v）⇒ 留痕刷屏", cfg.DedupWindow)
	}
}
