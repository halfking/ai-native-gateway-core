//go:build integration

package integration

import (
	"context"
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
	tenantID := "default"
	grantNote := "integration-grant-" + time.Now().Format("20060102150405.000000")
	adjustNote := "integration-adjust-" + time.Now().Format("20060102150405.000000")

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

	const adjustAmount int64 = -3
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
	if entryType != "adjust" {
		t.Fatalf("adjust ledger entry_type = %s, want adjust", entryType)
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

	// Cleanup: revert balances via the same service path so the environment is left unchanged.
	if err := svc.AdjustCredits(ctx, tenantID, -grantAmount, grantNote+"-cleanup"); err != nil {
		t.Fatalf("cleanup grant revert failed: %v", err)
	}
	if err := svc.AdjustCredits(ctx, tenantID, -adjustAmount, adjustNote+"-cleanup"); err != nil {
		t.Fatalf("cleanup adjust revert failed: %v", err)
	}

	wFinal, err := svc.GetWallet(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetWallet final: %v", err)
	}
	if wFinal.TotalAvailable != wBefore.TotalAvailable {
		t.Fatalf("wallet total after cleanup = %d, want original %d", wFinal.TotalAvailable, wBefore.TotalAvailable)
	}
}
