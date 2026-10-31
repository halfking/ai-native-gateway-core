package sanitizerules

// postgres_block_review_test.go —— ReviewPersistence 的 Postgres 实现 SQL 形状
// 与错误清洗判据（sqlmock；真实建表由迁移 917 + startup 契约门负责）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func newMockStore(t *testing.T) (*PostgresStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPostgresStore(db), mock
}

// TestPgInsertBlockReviewRoundTrip：插入 → RETURNING 扫描成行（含空 decided_at）。
func TestPgInsertBlockReviewRoundTrip(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`INSERT INTO sanitize_block_reviews`).WillReturnRows(
		// ★ 列序与 blockReviewColumns 逐字对应（迁移 921 末尾追加了
		//   session_id / label / label_comment / labeled_by / labeled_at）。
		sqlmock.NewRows([]string{"id", "tenant_id", "client_type", "scope", "rule_id", "rule_name",
			"reason", "field_path", "payload_digest", "request_id", "match_detail", "status",
			"created_at", "decided_by", "decided_at", "session_id", "label", "label_comment",
			"labeled_by", "labeled_at", "degraded"}).
			AddRow(7, "acme", "zcode", "egress", "builtin.egress_sink", "外传方向",
				"sink destination", "$.command", "digest", "req-1", "说明",
				"pending", now, "", nil, "sess-1", "", "", "", nil, false))
	got, err := s.InsertBlockReview(context.Background(), BlockReview{
		TenantID: "acme", ClientType: "zcode", Scope: "egress", Rule: "builtin.egress_sink",
		RuleName: "外传方向", Reason: "sink destination", Field: "$.command",
		Digest: "digest", RequestID: "req-1", MatchDetail: "说明", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got.ID != 7 || got.ClientType != "zcode" || got.RuleName != "外传方向" || got.MatchDetail != "说明" {
		t.Fatalf("扫描结果不对: %+v", got)
	}
	if !got.DecidedAt.IsZero() {
		t.Fatalf("pending 行的 decided_at 应为零值: %v", got.DecidedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestPgDecideBlockReviewZeroRowsMeansConflict：UPDATE 命中 0 行 = 不存在或已决
// ——实现必须区分两者：不存在报 NotFound、已决报 Conflict。
func TestPgDecideBlockReviewZeroRowsMeansConflict(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(`UPDATE sanitize_block_reviews`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id, tenant_id, client_type`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "tenant_id", "client_type", "scope", "rule_id", "rule_name",
			"reason", "field_path", "payload_digest", "request_id", "match_detail", "status",
			"created_at", "decided_by", "decided_at", "session_id", "label", "label_comment",
			"labeled_by", "labeled_at", "degraded"}).
			AddRow(3, "acme", "", "egress", "builtin.egress_sink", "", "", "", "", "", "",
				"approved", time.Now(), "", nil, "", "", "", "", nil, false))
	_, err := s.DecideBlockReviewDB(context.Background(), 3, "rejected", "boss", time.Now())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("已决记录应报 Conflict，got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestPgUpsertEgressReleaseBindsExpiry：审核批准的 7 天过期必须进 SQL 参数
// （键：tenant+scope+rule+digest 冲突时更新过期与来源）。
func TestPgUpsertEgressReleaseBindsExpiry(t *testing.T) {
	s, mock := newMockStore(t)
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	mock.ExpectExec(`INSERT INTO sanitize_egress_releases`).WithArgs(
		"acme", "egress", "builtin.egress_sink", "", "zcode", "boss", "review",
		sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := s.UpsertEgressReleaseDB(context.Background(), EgressRelease{
		TenantID: "acme", Scope: "egress", Rule: "builtin.egress_sink",
		ClientType: "zcode", ApprovedBy: "boss", Source: "review", ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestPgLoadHardBlockStateParsesStoredList：装载解析存储的 `scope:rule` 列表；
// 坏 JSON 报错而非静默用默认清单；★ 种子行（updated_by=”）不算已管理。
func TestPgLoadHardBlockStateParsesStoredList(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(`SELECT rules, updated_by FROM sanitize_hardblock_state`).WillReturnRows(
		sqlmock.NewRows([]string{"rules", "updated_by"}).AddRow(`["input:*","egress:builtin.egress_sink"]`, "boss"))
	allows, found, err := s.LoadHardBlockState(context.Background())
	if err != nil || !found || len(allows) != 2 {
		t.Fatalf("load: found=%v err=%v allows=%+v", found, err, allows)
	}

	// ★ 211 实测踩到的坑（拦截持久化轮当轮止血）：迁移 917 种下的默认行
	// （rules='[]'、updated_by=''）被当成「管理员保存的空清单」⇒ 首次启动
	// 把硬阻断清单覆盖成空。种子行必须不算 found（回落 env/出厂默认）。
	s2, mock2 := newMockStore(t)
	mock2.ExpectQuery(`SELECT rules, updated_by FROM sanitize_hardblock_state`).WillReturnRows(
		sqlmock.NewRows([]string{"rules", "updated_by"}).AddRow(`[]`, ""))
	_, found2, err := s2.LoadHardBlockState(context.Background())
	if err != nil || found2 {
		t.Fatalf("种子行不得视为已管理: found=%v err=%v", found2, err)
	}

	// 空清单 + 非空 actor = 管理员显式保存的空清单 ⇒ found 且生效为空。
	s3, mock3 := newMockStore(t)
	mock3.ExpectQuery(`SELECT rules, updated_by FROM sanitize_hardblock_state`).WillReturnRows(
		sqlmock.NewRows([]string{"rules", "updated_by"}).AddRow(`[]`, "boss"))
	allows3, found3, err := s3.LoadHardBlockState(context.Background())
	if err != nil || !found3 || len(allows3) != 0 {
		t.Fatalf("显式空清单: found=%v err=%v allows=%+v", found3, err, allows3)
	}

	s4, mock4 := newMockStore(t)
	mock4.ExpectQuery(`SELECT rules, updated_by FROM sanitize_hardblock_state`).WillReturnRows(
		sqlmock.NewRows([]string{"rules", "updated_by"}).AddRow(`"not-an-array"`, "boss"))
	if _, _, err := s4.LoadHardBlockState(context.Background()); err == nil {
		t.Fatal("坏存储必须报错（不能静默回落默认清单）")
	}
}

// TestPgErrorSanitized：驱动错误可能携带参数原文（含被拦载荷）——错误必须是
// 固定文案（storeError），原文一个字节都不许出现。
//
// 审计修（2026-10-29）：旧判据只断言长度 ≤120，而驱动消息本身就没有
// " - " 分隔符时 sanitizeDBErr 原文照传且长度合法 ⇒ 断言恒绿。现在的判据
// 是「秘密词不出现」。
func TestPgErrorSanitized(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(`INSERT INTO sanitize_block_reviews`).WillReturnError(
		errors.New(`insert failed: password=supersecret-value at character 1`))
	_, err := s.InsertBlockReview(context.Background(), BlockReview{Scope: "egress"})
	if err == nil {
		t.Fatal("必须返回错误")
	}
	if strings.Contains(err.Error(), "supersecret-value") {
		t.Fatalf("错误信息携带参数原文: %q", err.Error())
	}
}

// TestPgFindPendingNoRowsIsNotError：首插（无同键 pending）的 no-rows 是
// 「没找到」，不是错误——否则生产上每次首插都误报 dedupe WARN。
//
// 审计修（2026-10-29）：旧实现判 pgx.ErrNoRows，而 database/sql 通道实际
// 返回 sql.ErrNoRows ⇒ no-rows 恒走错误分支。
func TestPgFindPendingNoRowsIsNotError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(`FROM sanitize_block_reviews`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "tenant_id", "client_type", "scope", "rule_id", "rule_name",
			"reason", "field_path", "payload_digest", "request_id", "match_detail", "status",
			"created_at", "decided_by", "decided_at"}))
	_, found, err := s.FindPendingBlockReview(context.Background(), "acme", "egress", "r", "d", "q")
	if err != nil {
		t.Fatalf("no-rows 必须是 (found=false, err=nil)，got err=%v", err)
	}
	if found {
		t.Fatal("空结果不得报 found")
	}
}

// TestPgGetMissingReviewMapsNotFound：按 ID 取不存在的单必须映射成 ErrNotFound
// （admin 层据此给 404；旧实现透传 sql.ErrNoRows ⇒ 503）。
func TestPgGetMissingReviewMapsNotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(`FROM sanitize_block_reviews WHERE id=\$1`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "tenant_id", "client_type", "scope", "rule_id", "rule_name",
			"reason", "field_path", "payload_digest", "request_id", "match_detail", "status",
			"created_at", "decided_by", "decided_at"}))
	if _, err := s.GetBlockReviewDB(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的单应映射 ErrNotFound（404），got %v", err)
	}
}

// TestStoreErrorKeepsRootCause：错误清洗**不能把根因一起洗掉**，但**也不能
// 把绑定参数回显出来**。
//
// ★ 2026-10-29（211 生产实测）：「审核单列表读库失败」只留下一句
// 「sanitize rule block review list read failed」⇒ 根因（哪一列不存在 /
// 哪个连接池被关 / 权限不足）一个都看不到 ⇒ 定位只能靠猜。
// ⇒ 对外消息**逐字不变**，另带一个**可安全外露**的结构化根因切片。
func TestStoreErrorKeepsRootCause(t *testing.T) {
	root := errors.New(`ERROR: relation "sanitize_block_reviews" does not exist (SQLSTATE 42P01)`)
	err := storeError("block review list read", root)
	if err == nil {
		t.Fatal("storeError 不该返回 nil")
	}
	if !strings.Contains(err.Error(), "sanitize rule block review list read failed") {
		t.Fatalf("对外消息必须逐字不变，got %q", err.Error())
	}
	// 自由文本（含参数回显）**不能**出现在对外消息里。
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("原始错误文本不得外露（可能回显绑定参数），got %q", err.Error())
	}
	// 根因切片：非 PG 错误 ⇒ detail_unavailable（不许编）。
	if got := storeErrorDetail(err); got != "detail_unavailable" {
		t.Fatalf("非 PG 错误的根因切片应是 detail_unavailable，got %q", got)
	}
}

// TestStoreErrorDetailCarriesPGFields：PG 错误 ⇒ 根因切片带 SQLSTATE 与
// 约束/列名（这些**不含绑定参数**），但**不含**驱动的自由文本。
func TestStoreErrorDetailCarriesPGFields(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:           "42P01",
		Message:        "relation \"sanitize_block_reviews\" does not exist",
		ConstraintName: "",
		ColumnName:     "",
	}
	// 驱动会把绑定参数回显在自由文本里，但**结构化的 PgError 仍在链上**。
	wrapped := fmt.Errorf("insert failed: password=[REDACTED:secret]] at character 1: %w", pgErr)
	err := storeError("block review insert", wrapped)
	if !strings.Contains(err.Error(), "sanitize rule block review insert failed") {
		t.Fatalf("对外消息必须逐字不变，got %q", err.Error())
	}
	if strings.Contains(err.Error(), "password=[REDACTED:secret]]") {
		t.Fatalf("参数回显不得外露，got %q", err.Error())
	}
	detail := storeErrorDetail(err)
	if !strings.Contains(detail, "sqlstate=42P01") {
		t.Fatalf("根因切片必须带 SQLSTATE，got %q", detail)
	}
	if !strings.Contains(detail, "does not exist") {
		t.Fatalf("根因切片必须带 PG message，got %q", detail)
	}
}

// TestPgInsightsTenantFilterIsLegalSQL：带租户过滤的读数必须是**合法** SQL。
//
// ★ 回归门（2026-10-30 审计轮）：旧拼接在租户过滤时生成
// `FROM sanitize_block_reviews AND tenant_id=$1`（缺 WHERE 前缀）⇒
// 租户管理员的读数恒 503、super_admin 的 ?tenant_id= 过滤恒 503。
// sqlmock 断言两件事：① 收到的 SQL 里 WHERE 出现在 AND 之前（形状）；
// ② 降级放行单不进读数（WHERE NOT degraded）。
func TestPgInsightsTenantFilterIsLegalSQL(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{"tenant_id", "rule_id", "rule_name", "total",
		"pending", "approved", "rejected", "false_positive", "true_positive",
		"rule_too_broad", "needs_rule", "labeled"}).
		AddRow("acme", "builtin.egress_sink", "外传方向", 3, 1, 2, 0, 1, 0, 0, 0, 1)
	mock.ExpectQuery(`FROM sanitize_block_reviews WHERE NOT degraded AND tenant_id=\$1`).
		WillReturnRows(rows)
	insights, err := s.BlockReviewInsightsDB(context.Background(), "acme", 50)
	if err != nil {
		t.Fatalf("带租户过滤的读数不得报错（回归：曾生成非法 SQL）: %v", err)
	}
	if len(insights) != 1 || insights[0].Rule != "builtin.egress_sink" {
		t.Fatalf("读数结果不对: %+v", insights)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
	_ = now
}

// TestPgInsightsExcludeDegraded：无租户过滤的读数同样必须带 NOT degraded
// （211 实测 24h 内 226/226 条全是降级单 ⇒ 不排除的话规则优化输入面只剩噪音）。
func TestPgInsightsExcludeDegraded(t *testing.T) {
	s, mock := newMockStore(t)
	rows := sqlmock.NewRows([]string{"tenant_id", "rule_id", "rule_name", "total",
		"pending", "approved", "rejected", "false_positive", "true_positive",
		"rule_too_broad", "needs_rule", "labeled"})
	mock.ExpectQuery(`WHERE NOT degraded`).WillReturnRows(rows)
	if _, err := s.BlockReviewInsightsDB(context.Background(), "", 50); err != nil {
		t.Fatalf("insights read: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestPgCountPendingBlockDecisionsExcludesDegraded：待决断数的 SQL 必须是
// `pending 且非降级`（管理面徽标口径，迁移 926）。
func TestPgCountPendingBlockDecisionsExcludesDegraded(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(`status='pending' AND NOT degraded`).WillReturnRows(
		sqlmock.NewRows([]string{"count"}).AddRow(2))
	n, err := s.CountPendingBlockDecisionsDB(context.Background(), "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestPgInsertBlockReviewPersistsDegraded：降级标记要真正落列
// （列序真值源含 degraded ⇒ RETURNING 扫回来仍是 true）。
func TestPgInsertBlockReviewPersistsDegraded(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`INSERT INTO sanitize_block_reviews`).WillReturnRows(
		sqlmock.NewRows([]string{"id", "tenant_id", "client_type", "scope", "rule_id", "rule_name",
			"reason", "field_path", "payload_digest", "request_id", "match_detail", "status",
			"created_at", "decided_by", "decided_at", "session_id", "label", "label_comment",
			"labeled_by", "labeled_at", "degraded"}).
			AddRow(9, "acme", "zcode", "egress", "builtin.guard.capacity_exceeded", "",
				"capacity", "", "", "req-9", "", "pending", now, "", nil, "", "", "", "", nil, true))
	got, err := s.InsertBlockReview(context.Background(), BlockReview{
		TenantID: "acme", Scope: "egress", Rule: "builtin.guard.capacity_exceeded",
		Reason: "capacity", CreatedAt: now, Degraded: true,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !got.Degraded {
		t.Fatal("degraded 标记必须在 RETURNING 扫描后仍然为 true")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}
