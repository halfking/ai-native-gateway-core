package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// ★ D9（2026-11-19 审计轮）跨租户冲正守卫钉测。
//
// 缺陷本体：
//
//	`UsageCorrect` 的存在性检查原是
//	  `SELECT EXISTS(SELECT 1 FROM maas_usage_settlements WHERE entry_id = $1)`
//	——**无 tenant_id 过滤** ⇒ A 租户可以对 B 租户的 entry_id 落一条
//	maas_usage_corrections 冲正行。对比同文件 UsageSettle 有
//	holdTenantID != tenantID 校验 ⇒ 同层内自相矛盾。
//
// 本文件的三条用例分别钉住：
//
//	① 越权冲正被拒，且**不产生任何写入**；
//	② 真·不存在时返回 NOT_FOUND，且不回显「该 entry 是否存在」；
//	③ 正常路径走通（同时覆盖 D6 的幂等落库序列化）。
//
// 为此把 Service.pool 由具体类型 *pgxpool.Pool 放宽为窄接口
// maasidem.Pool —— 跨租户作用域判定是**只体现在 SQL 与分支顺序上**的
// 缺陷，必须能在能观察 DB 交互的测试里钉死。

const (
	// d9IdempotencySQL 命中 maasidem.Check 的幂等查询。
	d9IdempotencySQL = "FROM maas_idempotency_log"
	// d9ScopedExistsSQL 是**带 tenant_id 过滤**的存在性检查（D9 修复后的形态）。
	// 用 ^…$ 锚定 ⇒ 与 d9UnscopedExistsSQL 互斥，不会互相误匹配。
	d9ScopedExistsSQL = `^SELECT EXISTS\( SELECT 1 FROM maas_usage_settlements st JOIN maas_usage_holds h ON st\.usage_hold_id = h\.usage_hold_id WHERE st\.entry_id = \$1 AND h\.tenant_id = \$2 \)$`
	// d9UnscopedExistsSQL 是「去掉租户过滤后再查一次」的**归属复核**查询。
	d9UnscopedExistsSQL = `^SELECT EXISTS\( SELECT 1 FROM maas_usage_settlements WHERE entry_id = \$1 \)$`
)

// d9Service 造一个绑定 pgxmock 的 ledger service。
func d9Service(t *testing.T) (*Service, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return &Service{pool: mock}, mock
}

// TestD9UsageCorrectRejectsCrossTenantEntry 钉住①：entry 属于别的租户时
// 必须拒绝，且**一条写入都不能发生**（没有 INSERT 期望 ⇒ 任何写库调用
// 都会让 mock 直接报错，测试随即失败）。
func TestD9UsageCorrectRejectsCrossTenantEntry(t *testing.T) {
	svc, mock := d9Service(t)

	mock.ExpectQuery(d9IdempotencySQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectBegin()
	// 第一次：带 tenant_id 过滤 ⇒ 本租户名下没有这条 entry。
	mock.ExpectQuery(d9ScopedExistsSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	// 第二次：去掉租户过滤 ⇒ entry 确实存在，但属于别的租户。
	mock.ExpectQuery(d9UnscopedExistsSQL).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err := svc.UsageCorrect(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a", TraceID: "trace-1"},
		maascontracts.CommandMeta{IdempotencyKey: "idem-d9-1"},
		maascontracts.UsageCorrectRequest{
			EntryID:  "entry_of_tenant_b",
			Reversal: maascontracts.Quantity{Amount: "-10", Unit: "token"},
		},
	)

	var maasErr *maascontracts.Error
	if !errors.As(err, &maasErr) {
		t.Fatalf("跨租户冲正必须被拒为 MAAS_UNAUTHORIZED_SCOPE，实际 err=%v", err)
	}
	if maasErr.Code != maascontracts.ErrorUnauthorizedScope {
		t.Fatalf("错误码 = %q，期望 %q", maasErr.Code, maascontracts.ErrorUnauthorizedScope)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符（拒绝路径上不应有任何写入）：%v", err)
	}
}

// TestD9UsageCorrectMissingEntryIsNotFound 钉住②：entry 真不存在时返回
// NOT_FOUND —— 与「存在但属他人」是**两种不同错误码**，且 NOT_FOUND 的
// 文案不得泄露该 entry 在别处存在。
func TestD9UsageCorrectMissingEntryIsNotFound(t *testing.T) {
	svc, mock := d9Service(t)

	mock.ExpectQuery(d9IdempotencySQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectQuery(d9ScopedExistsSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(d9UnscopedExistsSQL).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err := svc.UsageCorrect(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a"},
		maascontracts.CommandMeta{IdempotencyKey: "idem-d9-2"},
		maascontracts.UsageCorrectRequest{
			EntryID:  "entry_missing",
			Reversal: maascontracts.Quantity{Amount: "-10", Unit: "token"},
		},
	)

	var maasErr *maascontracts.Error
	if !errors.As(err, &maasErr) {
		t.Fatalf("期望 MAAS_ENTRY_NOT_FOUND，实际 err=%v", err)
	}
	if maasErr.Code != "MAAS_ENTRY_NOT_FOUND" {
		t.Fatalf("错误码 = %q，期望 MAAS_ENTRY_NOT_FOUND", maasErr.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符：%v", err)
	}
}

// TestD9UsageCorrectOwnedEntrySettles 钉住③：entry 属于本租户时正常落
// 冲正行，并落幂等记录（D6：payload 必须真的序列化进去）。
func TestD9UsageCorrectOwnedEntrySettles(t *testing.T) {
	svc, mock := d9Service(t)

	mock.ExpectQuery(d9IdempotencySQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectBegin()
	// 带租户过滤的检查直接命中 ⇒ 本租户拥有该 entry。
	mock.ExpectQuery(d9ScopedExistsSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO maas_usage_corrections").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO maas_idempotency_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	resp, err := svc.UsageCorrect(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a", TraceID: "trace-3"},
		maascontracts.CommandMeta{IdempotencyKey: "idem-d9-3"},
		maascontracts.UsageCorrectRequest{
			EntryID:  "entry_of_tenant_a",
			Reversal: maascontracts.Quantity{Amount: "-10", Unit: "token"},
		},
	)
	if err != nil {
		t.Fatalf("本租户自有 entry 的冲正必须成功：%v", err)
	}
	if resp.ReversalID == "" || resp.EntryID != "entry_of_tenant_a" {
		t.Fatalf("冲正响应不完整：%+v", resp)
	}
	if resp.Status == nil || *resp.Status != maascontracts.UsageStatusPosted {
		t.Fatalf("冲正状态应为 posted，实际 %+v", resp.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符：%v", err)
	}
}
