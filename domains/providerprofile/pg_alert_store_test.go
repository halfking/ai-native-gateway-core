package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAlertStore(t *testing.T) providerprofile.AlertStore {
	skipIfNoDB(t)
	return providerprofile.NewPGAlertStore(dbPoolFromTestURL(t))
}

func TestPGAlertStore_SaveAndDedupe(t *testing.T) {
	store := newAlertStore(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	a := providerprofile.Alert{
		CredentialID: 99990010, ProviderID: 99990011,
		Type: providerprofile.AlertTypeAutoDisabled, Level: providerprofile.AlertLevelCritical,
		TriggerDate: date, CurrentScore: 30, Message: "总分连续3天<40",
		Dimension: "total_score", ActionTaken: "disabled",
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM provider_profile_alerts WHERE credential_id=$1`, a.CredentialID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &a))
	// 第二次同 (credential,date,type) 必须被去重，不报错也不新增
	require.NoError(t, store.SaveIfNew(ctx, &a))

	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM provider_profile_alerts WHERE credential_id=$1 AND alert_type='auto_disabled' AND trigger_date=$2`,
		a.CredentialID, date).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "dedupe: only one auto_disabled per credential/day/type")
}

func TestPGAlertStore_DifferentTypesNotDeduped(t *testing.T) {
	store := newAlertStore(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	credID := int64(99990015)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &providerprofile.Alert{
		CredentialID: credID, ProviderID: 99990011,
		Type: providerprofile.AlertTypeScoreDrop, Level: providerprofile.AlertLevelWarning,
		TriggerDate: date, Message: "drop24",
	}))
	require.NoError(t, store.SaveIfNew(ctx, &providerprofile.Alert{
		CredentialID: credID, ProviderID: 99990011,
		Type: providerprofile.AlertTypeDimensionLow, Level: providerprofile.AlertLevelCritical,
		TriggerDate: date, Message: "avail low",
	}))

	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_profile_alerts WHERE credential_id=$1 AND trigger_date=$2`, credID, date).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "different alert types same day are both kept")
}

func TestPGAlertStore_HasUnresolved(t *testing.T) {
	store := newAlertStore(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	credID := int64(99990020)
	t.Cleanup(func() {
		pool := dbPoolFromTestURL(t)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &providerprofile.Alert{
		CredentialID: credID, ProviderID: 99990021,
		Type: providerprofile.AlertTypeScoreDrop, Level: providerprofile.AlertLevelWarning,
		TriggerDate: date, Message: "drop",
	}))

	got, err := store.HasUnresolved(ctx, credID, providerprofile.AlertTypeScoreDrop, date)
	require.NoError(t, err)
	assert.True(t, got)

	got, err = store.HasUnresolved(ctx, credID, providerprofile.AlertTypeAutoDisabled, date)
	require.NoError(t, err)
	assert.False(t, got)
}

// TestPGAlertStore_SaveWithDetails 是 R11 252 SQL 日志审计的回归钉：
// Details 曾以 []byte 直传 pgx，SimpleProtocol 内联为 bytea hex 字面量导致
// jsonb 列解析必炸（生产 252 实证 ×33/161min，details 全量丢失）。
// 旧测试从未设置 Details，因此两个月内零报警。
func TestPGAlertStore_SaveWithDetails(t *testing.T) {
	store := newAlertStore(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()
	date := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	credID := int64(99990025)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &providerprofile.Alert{
		CredentialID: credID, ProviderID: 99990021,
		Type: providerprofile.AlertTypeAutoDisabled, Level: providerprofile.AlertLevelCritical,
		TriggerDate: date, CurrentScore: 39.54, Message: "可用性 7.2 低于 50，立即禁用",
		Dimension: "availability",
		Details:   map[string]interface{}{"action": "advisory_only", "threshold": 50.0},
	}))

	var detailsRaw []byte
	var action string
	err := pool.QueryRow(ctx,
		`SELECT details, details->>'action' FROM provider_profile_alerts
		 WHERE credential_id=$1 AND alert_type='auto_disabled' AND trigger_date=$2`,
		credID, date).Scan(&detailsRaw, &action)
	require.NoError(t, err)
	assert.Equal(t, "advisory_only", action)
	assert.JSONEq(t, `{"action":"advisory_only","threshold":50}`, string(detailsRaw))
}
