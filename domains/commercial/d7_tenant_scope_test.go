package commercial

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// ★ D7（2026-11-19 审计轮）跨租户写 + 双写原子性守卫钉测。
//
// 缺陷本体（三重）：
//
//	① **越权**：`req.TenantID` 原先直接覆盖 `auth.TenantID` 且**零校验**
//	   ⇒ 可为**其它租户**落 maas_quotes 行。对比同层的 inventory / ledger /
//	   trading 三处都做了校验，唯独 commercial 没有。
//	② **契约违背**：contracts/maascontracts/ports.go 声明 QuotePort 是
//	   "pure function operation with no side effects or state changes"，
//	   实现却每次调用 INSERT 两张表（漂移登记在审计主档 D7）。
//	③ **非原子**：quote 行与 quote_lines 行原是两次独立 Exec，第二次失败
//	   留下**孤儿 quote 行**（报价有记录、明细没有）。
//
// 本文件三条用例分别钉住 ①（拒绝且零写入）、③（明细写失败 ⇒ 整体回滚、
// 不留孤儿行）与正常路径。

// d7CalculatePriceSQL 命中计价查询。
const d7CalculatePriceSQL = "FROM maas_calculate_price"

// d7Service 造一个绑定 pgxmock 的 commercial service。
func d7Service(t *testing.T) (*Service, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	return &Service{pool: mock}, mock
}

// d7QuoteRequest 造一个带指定租户的报价请求。
func d7QuoteRequest(tenantID string) maascontracts.EstimateQuoteRequest {
	other := tenantID
	return maascontracts.EstimateQuoteRequest{
		TenantID:           &other,
		ModelSKU:           "model-x",
		Quantity:           maascontracts.Quantity{Amount: "10", Unit: "token"},
		ResourceSnapshotID: "rs_1",
		PriceVersionID:     strPtr("pv_1"),
	}
}

func strPtr(s string) *string { return &s }

// TestD7EstimateQuoteRejectsCrossTenant 钉住①：请求里的 tenant 与认证
// tenant 不一致时必须拒绝，且**一条写入都不能发生**（没有 ExpectBegin /
// ExpectExec ⇒ 任何写库调用都会让 mock 直接报错，测试随即失败）。
func TestD7EstimateQuoteRejectsCrossTenant(t *testing.T) {
	svc, mock := d7Service(t)

	mock.ExpectQuery(d7CalculatePriceSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"unit_price_minor", "total_minor", "currency", "rule_id"}).
			AddRow("100", "1000", "CNY", "rule_1"))

	_, err := svc.EstimateQuote(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a", TraceID: "trace-1"},
		d7QuoteRequest("tenant_b"),
	)

	var maasErr *maascontracts.Error
	if !errors.As(err, &maasErr) {
		t.Fatalf("跨租户报价必须被拒为 MAAS_UNAUTHORIZED_SCOPE，实际 err=%v", err)
	}
	if maasErr.Code != maascontracts.ErrorUnauthorizedScope {
		t.Fatalf("错误码 = %q，期望 %q", maasErr.Code, maascontracts.ErrorUnauthorizedScope)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符（拒绝路径上不应有任何写入）：%v", err)
	}
}

// TestD7EstimateQuoteLinesFailureRollsBackQuote 钉住③：明细行写失败时，
// quote 行必须**一并回滚**——旧实现是两次独立 Exec，明细失败会留下孤儿
// quote 行。这里通过「期望 Rollback、且不期望 Commit」来判别。
func TestD7EstimateQuoteLinesFailureRollsBackQuote(t *testing.T) {
	svc, mock := d7Service(t)

	mock.ExpectQuery(d7CalculatePriceSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"unit_price_minor", "total_minor", "currency", "rule_id"}).
			AddRow("100", "1000", "CNY", "rule_1"))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO maas_quotes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO maas_quote_lines").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	_, err := svc.EstimateQuote(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a"},
		d7QuoteRequest("tenant_a"),
	)
	if err == nil {
		t.Fatal("明细行写失败时必须报错（且不得提交 quote 行）")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符（明细失败 ⇒ 必须回滚、不得提交）：%v", err)
	}
}

// TestD7EstimateQuoteWritesBothRowsInOneTransaction 钉住正常路径：quote 与
// quote_lines 两行都在**同一个事务**里写入并只提交一次。
func TestD7EstimateQuoteWritesBothRowsInOneTransaction(t *testing.T) {
	svc, mock := d7Service(t)

	mock.ExpectQuery(d7CalculatePriceSQL).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"unit_price_minor", "total_minor", "currency", "rule_id"}).
			AddRow("100", "1000", "CNY", "rule_1"))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO maas_quotes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO maas_quote_lines").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	resp, err := svc.EstimateQuote(
		context.Background(),
		maascontracts.AuthContext{TenantID: "tenant_a"},
		d7QuoteRequest("tenant_a"),
	)
	if err != nil {
		t.Fatalf("本租户报价必须成功：%v", err)
	}
	if resp.QuoteID == "" || resp.QuoteSnapshotID == "" {
		t.Fatalf("报价响应不完整：%+v", resp)
	}
	if resp.HoldCreated {
		t.Fatal("契约 v0.1 要求 EstimateQuote MUST NOT create a hold")
	}
	if resp.Status != maascontracts.QuoteStatusReference {
		t.Fatalf("报价状态 = %q，期望 %q", resp.Status, maascontracts.QuoteStatusReference)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符：%v", err)
	}
}
