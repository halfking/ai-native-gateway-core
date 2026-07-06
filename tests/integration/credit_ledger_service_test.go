//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/maas"
)

func TestCreditLedgerService_GrantAndAdjust(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("connect to db: %v", err)
	}
	defer pool.Close()

	svc := maas.NewService(pool)
	tenantID := fmt.Sprintf("it-credit-ledger-%d", time.Now().UnixNano())
	grantNote := "integration-grant-" + time.Now().Format("20060102150405.000000")
	adjustNote := "integration-adjust-" + time.Now().Format("20060102150405.000000")
	adjustRevertNote := adjustNote + "-cleanup"

	_, err = pool.Exec(ctx, `
		INSERT INTO tenants (code, name, status, description, contact_email)
		VALUES ($1, $2, 'active', '', '')
	`, tenantID, tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM credit_ledger_with_current_month WHERE tenant_id = $1`, tenantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenant_credit_wallets WHERE tenant_id = $1`, tenantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE code = $1`, tenantID)
	})

	wBefore, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetWallet before: %v", err)
	}

	const grantAmount int64 = 11
	if err := svc.GrantCredits(ctx, tenantID, grantAmount, grantNote); err != nil {
		t.Fatalf("GrantCredits failed: %v", err)
	}

	wAfterGrant, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetWallet after grant: %v", err)
	}
	if got, want := wAfterGrant.GrantedBalance, wBefore.GrantedBalance+grantAmount; got != want {
		t.Fatalf("granted balance after grant = %d, want %d", got, want)
	}

	var entryType string
	var amount int64
	var balanceAfter int64
	var poolName *string
	err = pool.QueryRow(ctx, `
		SELECT entry_type, amount, balance_after, pool
		FROM credit_ledger_with_current_month
		WHERE tenant_id = $1 AND note = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, grantNote).Scan(&entryType, &amount, &balanceAfter, &poolName)
	if err != nil {
		t.Fatalf("query grant ledger: %v", err)
	}
	if entryType != "adjust" {
		t.Fatalf("grant ledger entry_type = %s, want adjust", entryType)
	}
	if amount != grantAmount {
		t.Fatalf("grant ledger amount = %d, want %d", amount, grantAmount)
	}
	if balanceAfter != wAfterGrant.TotalAvailable {
		t.Fatalf("grant ledger balance_after = %d, wallet total = %d", balanceAfter, wAfterGrant.TotalAvailable)
	}
	if poolName == nil || *poolName != "granted" {
		t.Fatalf("grant ledger pool = %v, want granted", poolName)
	}

	const adjustAmount int64 = 7
	if err := svc.AdjustCredits(ctx, tenantID, adjustAmount, adjustNote); err != nil {
		t.Fatalf("AdjustCredits failed: %v", err)
	}

	wAfterAdjust, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetWallet after adjust: %v", err)
	}
	if got, want := wAfterAdjust.PurchasedBalance, wAfterGrant.PurchasedBalance+adjustAmount; got != want {
		t.Fatalf("purchased balance after adjust = %d, want %d", got, want)
	}
	if got, want := wAfterAdjust.TotalAvailable, wAfterGrant.TotalAvailable+adjustAmount; got != want {
		t.Fatalf("wallet total after adjust = %d, want %d", got, want)
	}

	err = pool.QueryRow(ctx, `
		SELECT entry_type, amount, balance_after, pool
		FROM credit_ledger_with_current_month
		WHERE tenant_id = $1 AND note = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, adjustNote).Scan(&entryType, &amount, &balanceAfter, &poolName)
	if err != nil {
		t.Fatalf("query adjust ledger: %v", err)
	}
	if entryType != "topup" {
		t.Fatalf("adjust ledger entry_type = %s, want topup", entryType)
	}
	if amount != adjustAmount {
		t.Fatalf("adjust ledger amount = %d, want %d", amount, adjustAmount)
	}
	if balanceAfter != wAfterAdjust.TotalAvailable {
		t.Fatalf("adjust ledger balance_after = %d, wallet total = %d", balanceAfter, wAfterAdjust.TotalAvailable)
	}
	if poolName == nil || *poolName != "purchased" {
		t.Fatalf("adjust ledger pool = %v, want purchased", poolName)
	}

	// Cleanup: revert the purchased-balance mutation via the same service path.
	if err := svc.AdjustCredits(ctx, tenantID, -adjustAmount, adjustRevertNote); err != nil {
		t.Fatalf("cleanup adjust revert failed: %v", err)
	}

	wFinal, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetWallet final: %v", err)
	}
	if got, want := wFinal.GrantedBalance, wAfterGrant.GrantedBalance; got != want {
		t.Fatalf("granted balance after cleanup = %d, want %d", got, want)
	}
	if got := wFinal.PurchasedBalance; got != 0 {
		t.Fatalf("purchased balance after cleanup = %d, want 0", got)
	}
	if got, want := wFinal.TotalAvailable, wAfterGrant.TotalAvailable; got != want {
		t.Fatalf("wallet total after cleanup = %d, want %d", got, want)
	}
}
