package v2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// TestFilterAndScoreOutageFallback_ServesSoftExpiredMirror pins the
// 2026-09-04 availability gear: with Redis unreachable and a warm mirror,
// the manager serves read-only degraded routing for soft-expired entries
// inside OutageGrace — the state the router lands in when the authoritative
// pipeline read / mirror-serve PING fails during a Redis outage.
func TestFilterAndScoreOutageFallback_ServesSoftExpiredMirror(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 7, "gpt-x", 1, true, 120, 0.95)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 7, RawModel: "gpt-x", TenantID: "t", BaseURLMs: 200}}
	_, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err, "warm-up read must succeed while Redis is alive")

	// Soft-expire the mirror entry (soft TTL is 200ms in newMirrorManager)
	// and kill Redis: this is beyond both the grace gear and the stale
	// window — only the outage gear may serve.
	time.Sleep(250 * time.Millisecond)
	mr.Close()

	views, err := mgr.FilterAndScoreOutageFallback(context.Background(), seeds)
	require.NoError(t, err, "outage gear must serve soft-expired mirror entries while Redis is down")
	require.Len(t, views, 1)
	assert.True(t, views[0].Available)
	assert.Equal(t, 7, views[0].CredentialID)
}

// TestFilterAndScoreOutageFallback_RefusesWhenRedisAlive ensures the gear
// can never paper over a DELIBERATE recovery-gate closure: with Redis
// reachable the fallback must refuse so the router keeps failing closed.
func TestFilterAndScoreOutageFallback_RefusesWhenRedisAlive(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 7, "gpt-x", 1, true, 120, 0.95)
	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 7, RawModel: "gpt-x", TenantID: "t"}}
	_, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	_ = mr // still running — Redis alive

	_, err = mgr.FilterAndScoreOutageFallback(context.Background(), seeds)
	require.Error(t, err, "reachable Redis must refuse the outage fallback (gate closures are not bypassable)")
}

// TestFilterAndScoreOutageFallback_DisabledByZeroGrace pins the escape
// hatch: URSM_V2_OUTAGE_GRACE_SECONDS=0 (OutageGrace 0) restores strict
// fail-closed even when Redis is down and the mirror is warm.
func TestFilterAndScoreOutageFallback_DisabledByZeroGrace(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	mgr.cfg.OutageGrace = 0
	seedNode(t, mr, 7, "gpt-x", 1, true, 120, 0.95)
	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 7, RawModel: "gpt-x", TenantID: "t"}}
	_, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	mr.Close()

	_, err = mgr.FilterAndScoreOutageFallback(context.Background(), seeds)
	require.Error(t, err, "OutageGrace=0 must disable the gear")
	assert.True(t, errors.Is(err, store.ErrRedisUnavailable),
		"disabled gear must still surface ErrRedisUnavailable")
}

// TestFilterAndScoreOutageFallback_ColdMirrorRefuses ensures a process with
// no mirror entries (fresh boot, or outage longer than the LRU holds state)
// does not hallucinate routing decisions.
func TestFilterAndScoreOutageFallback_ColdMirrorRefuses(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	mr.Close()
	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 42, RawModel: "gpt-x", TenantID: "t"}}

	_, err := mgr.FilterAndScoreOutageFallback(context.Background(), seeds)
	require.Error(t, err, "cold mirror must refuse")
	assert.True(t, errors.Is(err, store.ErrRedisUnavailable))
}

// TestFilterAndScoreOutageFallback_PartialServe drops seeds without a
// usable mirror entry instead of failing the whole decision — a candidate
// never observed by this process is excluded, the rest keep serving.
func TestFilterAndScoreOutageFallback_PartialServe(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 7, "gpt-x", 1, true, 120, 0.95)
	warm := []CandidateSeed{{ProviderID: 1, CredentialID: 7, RawModel: "gpt-x", TenantID: "t"}}
	_, err := mgr.FilterAndScore(context.Background(), warm)
	require.NoError(t, err)
	mr.Close()

	seeds := append(warm, CandidateSeed{ProviderID: 2, CredentialID: 99, RawModel: "gpt-x", TenantID: "t"})
	views, err := mgr.FilterAndScoreOutageFallback(context.Background(), seeds)
	require.NoError(t, err)
	require.Len(t, views, 1, "only the mirror-known candidate may be served")
	assert.Equal(t, 7, views[0].CredentialID)
}

// TestConfigOutageGraceEnv pins the boot env parsing for the outage window.
func TestConfigOutageGraceEnv(t *testing.T) {
	t.Setenv("URSM_V2_OUTAGE_GRACE_SECONDS", "0")
	cfg := LoadFromEnv()
	assert.Zero(t, cfg.OutageGrace, "explicit 0 must disable the outage gear")

	t.Setenv("URSM_V2_OUTAGE_GRACE_SECONDS", "999999999")
	cfg = LoadFromEnv()
	assert.Equal(t, 24*time.Hour, cfg.OutageGrace, "the window is capped at 24h")

	t.Setenv("URSM_V2_OUTAGE_GRACE_SECONDS", "120")
	cfg = LoadFromEnv()
	assert.Equal(t, 2*time.Minute, cfg.OutageGrace)

	t.Setenv("URSM_V2_OUTAGE_GRACE_SECONDS", "not-a-number")
	cfg = LoadFromEnv()
	assert.Equal(t, DefaultConfig().OutageGrace, cfg.OutageGrace, "malformed value keeps the default")
	require.Error(t, cfg.Validate(), "malformed outage grace must fail validation")
	_ = api.ModeAuthoritative // keep the api import meaningful for mode context
}
