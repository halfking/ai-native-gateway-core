package executors

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// mockStateProvider 是 StateManager 的测试桩
type mockStateProvider struct {
	enabled   bool
	available map[string]bool // "credID:model" -> available
}

func (m *mockStateProvider) Enabled() bool { return m.enabled }

func (m *mockStateProvider) IsAvailable(ctx context.Context, credentialID int, model string) (bool, string) {
	if !m.enabled {
		return true, ""
	}
	key := makeKey(credentialID, model)
	if avail, ok := m.available[key]; ok {
		if !avail {
			return false, "mock_cooling"
		}
	}
	return true, ""
}

func (m *mockStateProvider) GetState(ctx context.Context, credID int, model string) (*credentialstate.State, error) {
	return nil, nil
}

func makeKey(credID int, model string) string {
	return string(rune(credID)) + ":" + model
}

// mockURSMv2Manager 是 URSM v2 的测试桩
type mockURSMv2Manager struct {
	mode  ursmv2api.RolloutMode
	ready bool
}

func (m *mockURSMv2Manager) Mode() ursmv2api.RolloutMode { return m.mode }

func (m *mockURSMv2Manager) Ready(ctx context.Context) bool { return m.ready }

func (m *mockURSMv2Manager) FilterAndScore(ctx context.Context, seeds []ursmv2.CandidateSeed) ([]ursmv2api.NodeView, error) {
	return nil, nil
}

func (m *mockURSMv2Manager) Plan(ctx context.Context, seeds []ursmv2.CandidateSeed, tenant, canonical string) []ursmv2.CandidateSeed {
	return nil
}

func (m *mockURSMv2Manager) RecordRequest(ctx context.Context, ev ursmv2api.RequestOutcome) error {
	return nil
}

func (m *mockURSMv2Manager) ShouldUseV2(tenant, model, requestID string) bool {
	return false
}

func (m *mockURSMv2Manager) SetReady(ctx context.Context, ready bool) error {
	m.ready = ready
	return nil
}

// TestSelectStateBackend_AuthoritativeMode 测试 URSM v2 authoritative 模式
func TestSelectStateBackend_AuthoritativeMode(t *testing.T) {
	ursmMgr := &mockURSMv2Manager{
		mode:  ursmv2api.ModeAuthoritative,
		ready: true,
	}
	stateMgr := &mockStateProvider{enabled: true}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(ursmMgr, stateMgr, ctx)

	if backend.Name() != "ursm_v2_authoritative" {
		t.Errorf("expected ursm_v2_authoritative backend, got %s", backend.Name())
	}

	if !backend.IsAuthoritative() {
		t.Error("ursm_v2_authoritative backend should be authoritative")
	}
}

// TestSelectStateBackend_NotReady confirms authoritative mode is fail-closed
// even for callers that use the backend selector directly.
func TestSelectStateBackend_NotReady(t *testing.T) {
	ursmMgr := &mockURSMv2Manager{
		mode:  ursmv2api.ModeAuthoritative,
		ready: false,
	}
	stateMgr := &mockStateProvider{enabled: true}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(ursmMgr, stateMgr, ctx)

	if backend.Name() != "ursm_v2_not_ready" {
		t.Errorf("expected ursm_v2_not_ready backend when not ready, got %s", backend.Name())
	}

	if !backend.IsAuthoritative() {
		t.Error("rejecting backend must remain authoritative")
	}
	if got := backend.FilterAvailable(ctx, []provider.Candidate{{CredentialID: 1}}); len(got) != 0 {
		t.Fatalf("not-ready authoritative backend returned %d candidates", len(got))
	}
}

// TestSelectStateBackend_CanaryMode 测试 URSM v2 canary 模式
func TestSelectStateBackend_CanaryMode(t *testing.T) {
	ursmMgr := &mockURSMv2Manager{
		mode:  ursmv2api.ModeCanary,
		ready: true,
	}
	stateMgr := &mockStateProvider{enabled: true}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(ursmMgr, stateMgr, ctx)

	// Canary 模式不触发 authoritative 分支，应降级到 StateManager
	if backend.Name() != "legacy_state_manager" {
		t.Errorf("expected legacy_state_manager in canary mode, got %s", backend.Name())
	}
}

// TestSelectStateBackend_OffMode 测试 URSM v2 off 模式
func TestSelectStateBackend_OffMode(t *testing.T) {
	ursmMgr := &mockURSMv2Manager{
		mode:  ursmv2api.ModeOff,
		ready: false,
	}
	stateMgr := &mockStateProvider{enabled: true}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(ursmMgr, stateMgr, ctx)

	if backend.Name() != "legacy_state_manager" {
		t.Errorf("expected legacy_state_manager in off mode, got %s", backend.Name())
	}
}

// TestSelectStateBackend_ShadowMode (P0-3) pins the critical
// shadow-mode contract for the 7-day cutover comparison window:
// ModeShadow must pick the LEGACY state backend (routing behavior
// unchanged) even when URSM v2 manager reports Ready=true. The
// shadow double-write opt-in only affects the executor sidecar
// (record path), NOT the read path used by routing. If this test
// fails, a refactor accidentally let shadow mode promote URSM v2
// to authoritative-equivalent, which would route production
// traffic through untrusted comparison data.
func TestSelectStateBackend_ShadowMode(t *testing.T) {
	cases := []struct {
		name       string
		ready      bool
		stateMgrOK bool
	}{
		{"shadow + ready + state manager", true, true},
		{"shadow + not ready + state manager", false, true},
		{"shadow + ready + no state manager", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ursmMgr := &mockURSMv2Manager{
				mode:  ursmv2api.ModeShadow,
				ready: tc.ready,
			}
			var stateMgr credentialstate.StateProvider
			if tc.stateMgrOK {
				stateMgr = &mockStateProvider{enabled: true}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			backend := selectStateBackend(ursmMgr, stateMgr, ctx)
			if backend.Name() != "legacy_state_manager" && backend.Name() != "db_only" {
				t.Fatalf("shadow mode must NOT promote to ursm_v2_authoritative, got %s", backend.Name())
			}
			if backend.IsAuthoritative() {
				t.Fatalf("shadow mode backend must NOT be authoritative, got %s", backend.Name())
			}
		})
	}
}

// TestSelectStateBackend_CanaryMode_BackendUnchanged (P0-3) pins that
// canary mode ALSO stays on the legacy state backend for routing
// (canary only affects the executor sidecar via rollout.ShouldUseV2,
// NOT the read path). Same reasoning as the shadow-mode test —
// the 7-day cutover window must never accidentally take over
// production routing during the comparison run. The existing
// TestSelectStateBackend_CanaryMode already pins the backend
// selection; this test additionally asserts IsAuthoritative()==false
// to lock down the contract.
func TestSelectStateBackend_CanaryMode_BackendUnchanged(t *testing.T) {
	ursmMgr := &mockURSMv2Manager{
		mode:  ursmv2api.ModeCanary,
		ready: true,
	}
	stateMgr := &mockStateProvider{enabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(ursmMgr, stateMgr, ctx)
	if backend.IsAuthoritative() {
		t.Fatalf("canary mode backend must NOT be authoritative, got %s", backend.Name())
	}
}

// TestSelectStateBackend_NoStateManager 测试无 StateManager 时回退到 DB-only
func TestSelectStateBackend_NoStateManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(nil, nil, ctx)

	if backend.Name() != "db_only" {
		t.Errorf("expected db_only backend when no managers, got %s", backend.Name())
	}

	if backend.IsAuthoritative() {
		t.Error("db_only backend should not be authoritative")
	}
}

// TestSelectStateBackend_StateManagerDisabled 测试 StateManager 禁用时回退到 DB-only
func TestSelectStateBackend_StateManagerDisabled(t *testing.T) {
	stateMgr := &mockStateProvider{enabled: false}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	backend := selectStateBackend(nil, stateMgr, ctx)

	if backend.Name() != "db_only" {
		t.Errorf("expected db_only backend when StateManager disabled, got %s", backend.Name())
	}
}

// TestLegacyStateBackend_FilterAvailable 测试 LegacyStateBackend 的过滤逻辑
func TestLegacyStateBackend_FilterAvailable(t *testing.T) {
	stateMgr := &mockStateProvider{
		enabled: true,
		available: map[string]bool{
			makeKey(1, "gpt-4"): true,
			makeKey(2, "gpt-4"): false, // cooling
		},
	}

	backend := &LegacyStateBackend{sm: stateMgr}

	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "gpt-4", LifecycleStatus: "active", Routable: true},
		{CredentialID: 2, RawModel: "gpt-4", LifecycleStatus: "active", Routable: true},   // 会被 StateManager 过滤
		{CredentialID: 3, RawModel: "gpt-4", LifecycleStatus: "disabled", Routable: true}, // DB 字段不可用
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	filtered := backend.FilterAvailable(ctx, candidates)

	if len(filtered) != 1 {
		t.Errorf("expected 1 available candidate, got %d", len(filtered))
	}

	if len(filtered) > 0 && filtered[0].CredentialID != 1 {
		t.Errorf("expected credential 1, got %d", filtered[0].CredentialID)
	}
}

// TestDBOnlyBackend_FilterAvailable 测试 DBOnlyBackend 仅依赖 DB 字段
func TestDBOnlyBackend_FilterAvailable(t *testing.T) {
	backend := &DBOnlyBackend{}

	candidates := []provider.Candidate{
		{CredentialID: 1, RawModel: "gpt-4", LifecycleStatus: "active", Routable: true},
		{CredentialID: 2, RawModel: "gpt-4", LifecycleStatus: "disabled", Routable: true},
		{CredentialID: 3, RawModel: "gpt-4", LifecycleStatus: "active", Routable: true},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	filtered := backend.FilterAvailable(ctx, candidates)

	if len(filtered) != 2 {
		t.Errorf("expected 2 available candidates, got %d", len(filtered))
	}

	for _, c := range filtered {
		if !c.IsAvailable() {
			t.Errorf("filtered candidate %d should be available", c.CredentialID)
		}
	}
}
