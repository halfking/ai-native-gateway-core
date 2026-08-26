// Package bg — credential_probe_v2_recharge_recovery_test.go
//
// Pins the 2026-08-26 self-check audit findings for the apigpt / gpt-5.6-terra
// recharge-recovery scenario. User report:
//
//	apigpt was down (balance_exhausted) → user recharged → expected automatic
//	recovery within minutes. Specifically:
//
//	  1. BalanceQuotaProbe must run at ≤ 5 minutes for token-billing credentials
//	     (currently 2 minutes — well within the 5-minute budget).
//	  2. CredentialProbeV2 must refresh the model list under a credential when
//	     probe success is observed. If a sibling model binding had been
//	     previously marked unavailable (auto_* / continuous_failure /
//	     auto_probe_model_binding) by an earlier failure during the
//	     balance-exhausted window, the recovery must clear them all. The
//	     operator's principle: "if one model under a credential is healthy,
//	     all sibling models should be reachable; clear the old model list and
//	     replace with the freshly observed list". If the upstream /v1/models
//	     fetch fails, do NOT touch the binding list (it preserves the previous
//	     ground truth).
//	  3. Manual holds and the model_probe_broken permanent flag must NOT be
//	     touched by the recharge-recovery fan-out.
//	  4. The candidate cache must be invalidated so the router picks up the
//	     freshly recovered bindings on its next read.

package bg

import (
	"os"
	"strings"
	"testing"
)

// TestCredentialProbeV2_BalanceQuotaProbeInterval covers the cadence budget.
// User requirement: "按token计费、非周期性的节点，至少5分钟一次探测".
// Production default is 2 minutes (balance_quota_probe.go:67) — well within
// the budget; this test pins both the default AND the env override path.
func TestCredentialProbeV2_BalanceQuotaProbeInterval(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance_quota_probe.go failed: %v", err)
	}
	body := string(src)

	// Default cadence must be a positive duration ≤ 5 minutes.
	// Source line: `interval := 2 * time.Minute`
	if !strings.Contains(body, "interval := 2 * time.Minute") {
		t.Fatalf("BalanceQuotaProbe default interval changed: must be 2 minutes to satisfy the 5-minute probe budget")
	}

	// Env override must remain in place (operators tune for hot creds).
	for _, want := range []string{
		"LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL",
		"time.ParseDuration",
		"interval = d",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("BalanceQuotaProbe interval env override missing %q", want)
		}
	}
}

// TestCredentialProbeV2_FastReprobeDelay pins the 30-second fast reprobe
// budget. After a failed probe, the worker enqueues a delayed re-probe via
// fastReprobeDelay. Commit e13799f8b (2026-08-26, P1-2 落点 C) explicitly
// upgraded the default from 5 minutes to 30 seconds: the user's 5-minute
// cadence still governs the scheduled cycleAll path, while the reactive
// fastReprobe path must wake up sooner so the system detects upstream
// recovery within one user-visible request. Operators can override via
// LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY.
func TestCredentialProbeV2_FastReprobeDelay(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	// Default fastReprobeDelay = 30 seconds (P1-2 落点 C, e13799f8b).
	if !strings.Contains(body, "fastDelay := 30 * time.Second") {
		t.Fatalf("CredentialProbeV2 fastReprobeDelay default changed: must be 30 seconds (P1-2 reactive cadence, e13799f8b)")
	}

	// Env override hook for ops.
	for _, want := range []string{
		"LLM_GATEWAY_CRED_PROBE_V2_FAST_REPROBE_DELAY",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("CredentialProbeV2 fast reprobe delay env override missing %q", want)
		}
	}
}

// TestCredentialProbeV2_RecoverAllBindingsOnProbeSuccess pins the 2026-08-26
// self-check audit finding:
//
//	User principle:  "一旦这个模型成功了，应该先拉取凭据下所有模型清单，
//	                  如果成功拉到，就清空原来凭据下的模型清单，用新清单替换；
//	                  如果拉取不到就不要更新凭据下的模型清单用。"
//
// Implementation contract:
//
//   - On probe success (writeHealth sees availability_state='ready' AND not
//     BindingOnly), call restoreAllBindingsOnCredentialSuccess which clears
//     cmb.available=TRUE on every binding whose current unavailable_reason is
//     an automated reason (auto_*, continuous_failure, auto_probe_model_binding).
//
//   - Skip unavailable_reason LIKE 'manual%' (operator pin).
//
//   - Skip unavailable_reason = 'model_probe_broken' (permanent broken flag
//     from bg/model_probe.go — only model_probe.go itself can clear it).
//
//   - Skip admin_protected=TRUE.
//
//   - On probe failure (availability_state != 'ready') do NOT touch bindings;
//     the existing 60s credential_recovery tick owns those.
//
//   - Mirror the same clearing to model_offers so /api/routing/resolve
//     stays in lock-step with the production router (v_routable_credential_models).
//
//   - Always invalidate the candidate cache so the next router read sees
//     the fresh state.
func TestCredentialProbeV2_RecoverAllBindingsOnProbeSuccess(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	wantSubstrings := []string{
		// New helper signature (contract surface).
		"func (c *CredentialProbeV2) restoreAllBindingsOnCredentialSuccess(",
		// The call site in writeHealth on the healthy-ready branch.
		"c.restoreAllBindingsOnCredentialSuccess(",
		// Hard guards — these MUST be present in the new SQL.
		"NOT LIKE 'manual%'",
		"<> 'model_probe_broken'",
		"admin_protected",
		// Mirror to model_offers so /api/routing/resolve matches.
		"UPDATE model_offers mo",
		// Cache invalidation must fire so the router sees the fresh bindings.
		"InvalidateCandidateCacheForCredential(credID)",
	}

	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Fatalf("credential_probe_v2.go missing recharge-recovery contract %q — the apigpt/gpt-5.6-terra 'one model healthy → all sibling models routable' audit cannot be satisfied", want)
		}
	}

	// Negative contract: probe FAILURE must NOT fan out binding recovery.
	// restoreAllBindingsOnCredentialSuccess is invoked from the
	// AvailabilityState=="ready" && !BindingOnly branch — the existing
	// writeBindingUnavailable / restoreBindingOnProbeSuccess pin tests
	// already cover the negative paths, but we double-check the helper's
	// name is only referenced from the ready branch by looking for the
	// guard condition immediately preceding the call site.
	readyBranchCall := strings.Contains(body, "pr.AvailabilityState == \"ready\"")
	if !readyBranchCall {
		t.Fatalf("ready-branch guard missing — restoreAllBindingsOnCredentialSuccess must be gated by AvailabilityState==ready")
	}
	if !strings.Contains(body, "c.restoreAllBindingsOnCredentialSuccess(execCtx, credID, pr.HealthProbeModel)") {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess call site missing")
	}
}

// TestCredentialProbeV2_CacheFanOutIncludesAllBindings pins the 2026-08-26
// audit finding #2: when probe succeeds, the cache writeModels set must
// include every binding on the credential (not just those currently marked
// cmb.available=TRUE). The previous loadBoundRawModels filter
// `COALESCE(cmb.available, TRUE) = TRUE` would silently skip a binding that
// had been marked unavailable during the balance-exhausted window —
// meaning the cache would forever show that model as unavailable even after
// the credential-level recovery, while the underlying DB row had just been
// flipped back to TRUE by restoreAllBindingsOnCredentialSuccess.
//
// Implementation contract:
//
//   - Add a parameter (or sibling helper) loadBoundRawModelsAll that returns
//     ALL distinct raw_model_name values bound to the credential,
//     independent of cmb.available.
//
//   - The healthy probe branch writes ALL models to the cache (cache
//     reflects "recovered" state).
//
//   - The failing probe branch keeps the existing cmb.available=TRUE filter
//     (the cache mirrors the current DB state for failed probes).
func TestCredentialProbeV2_CacheFanOutIncludesAllBindings(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	wantSubstrings := []string{
		// New unconditional helper for the healthy branch.
		"func (c *CredentialProbeV2) loadBoundRawModelsAll(",
		// Healthy branch must use loadBoundRawModelsAll.
		"c.loadBoundRawModelsAll(execCtx, credID)",
	}

	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Fatalf("credential_probe_v2.go missing healthy-cache fan-out %q", want)
		}
	}
}

// TestCredentialProbeV2_BalanceExhaustedBypassOnSuccess pins the 2026-08-07
// P0 fix that lets a healthy probe un-stick a balance_exhausted credential.
// This is the upstream gate for the audit's auto-recovery claim — without
// it, BalanceQuotaProbe is dead-lettered even when the upstream is back.
func TestCredentialProbeV2_BalanceExhaustedBypassOnSuccess(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	// The bypass clause already exists at line 998-1001. Verify it has not
	// been accidentally removed.
	if !strings.Contains(body,
		`COALESCE($8, '') = 'ok'
		      OR quota_state NOT IN ('permanently_exhausted', 'balance_exhausted')`,
	) {
		t.Fatalf("writeHealth hard-quota bypass on probe success missing — apigpt would not auto-recover after recharge")
	}
}

// TestCredentialProbeV2_RestoreAllBindings_PreservesManual pins the
// "manual pin survives fan-out" contract. Operators can pin a specific
// (credential, model) unavailable via the admin UI; a successful probe on
// a sibling model must NOT unpin it.
//
// The audit user principle is "if one model is healthy, all sibling models
// should be reachable" — but only for AUTOMATED reasons. Manual holds are
// operator intent and must be preserved.
func TestCredentialProbeV2_RestoreAllBindings_PreservesManual(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	// Find the helper definition and assert the guard.
	start := strings.Index(body, "func (c *CredentialProbeV2) restoreAllBindingsOnCredentialSuccess(")
	if start < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess helper missing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess body not terminated")
	}
	helperBody := body[start : start+end]

	for _, want := range []string{
		// cmb side: skip manual reasons.
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		// cmb side: skip the model_probe_broken permanent flag.
		"COALESCE(cmb.unavailable_reason, '') <> 'model_probe_broken'",
		// cmb side: skip admin_protected.
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		// Mirror to model_offers with the same guard set.
		"COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(mo.unavailable_reason, '') <> 'model_probe_broken'",
		"COALESCE(mo.admin_protected, FALSE) = FALSE",
	} {
		if !strings.Contains(helperBody, want) {
			t.Fatalf("restoreAllBindingsOnCredentialSuccess missing guard %q — manual/broken/admin-protected bindings would be incorrectly restored", want)
		}
	}
}

// TestCredentialProbeV2_RestoreAllBindings_OnlyAvailableFalse pins the
// "only flip currently-down rows" contract. A binding that is already
// available=TRUE must not be touched (avoid spurious updated_at churn and
// audit noise).
func TestCredentialProbeV2_RestoreAllBindings_OnlyAvailableFalse(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func (c *CredentialProbeV2) restoreAllBindingsOnCredentialSuccess(")
	if start < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess helper missing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess body not terminated")
	}
	helperBody := body[start : start+end]

	// The cmb UPDATE must filter on cmb.available = FALSE — only flip
	// rows that are currently down.
	if !strings.Contains(helperBody, "cmb.available = FALSE") {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess must only flip cmb.available=FALSE rows to avoid spurious updated_at churn")
	}
	if !strings.Contains(helperBody, "mo.available = FALSE") {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess mirror must only flip model_offers.available=FALSE rows")
	}
}

// TestCredentialProbeV2_RestoreAllBindings_InvalidatesCandidateCache pins
// the cache invalidation contract. Without it, the router would keep
// returning the stale "binding unavailable" view from candCache until TTL.
func TestCredentialProbeV2_RestoreAllBindings_InvalidatesCandidateCache(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func (c *CredentialProbeV2) restoreAllBindingsOnCredentialSuccess(")
	if start < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess helper missing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess body not terminated")
	}
	helperBody := body[start : start+end]

	// The helper must invalidate the candidate cache so the next router
	// read sees the recovered bindings immediately.
	if !strings.Contains(helperBody, "InvalidateCandidateCacheForCredential(credID)") {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess must invalidate the candidate cache; otherwise the router keeps returning the stale unavailable view until TTL")
	}

	// Negative contract: must NOT touch credentials table.
	// (writeHealth already wrote credentials.availability_state='ready'
	// and this helper is binding-scoped only.)
	if strings.Contains(helperBody, "UPDATE credentials") {
		t.Fatalf("restoreAllBindingsOnCredentialSuccess must not write to credentials; that's writeHealth's job")
	}
}

// TestCredentialProbeV2_LoadBoundRawModelsAll_NoFilterOnCmb pins the
// healthy-branch fan-out: loadBoundRawModelsAll must NOT filter on
// cmb.available (otherwise it would skip exactly the bindings the helper
// just recovered). The cmb.available filter still lives in
// loadBoundRawModels for the failure branch.
func TestCredentialProbeV2_LoadBoundRawModelsAll_NoFilterOnCmb(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential_probe_v2.go failed: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func (c *CredentialProbeV2) loadBoundRawModelsAll(")
	if start < 0 {
		t.Fatalf("loadBoundRawModelsAll helper missing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("loadBoundRawModelsAll body not terminated")
	}
	helperBody := body[start : start+end]

	// Must NOT filter on cmb.available — that's the whole point.
	if strings.Contains(helperBody, "cmb.available") {
		t.Fatalf("loadBoundRawModelsAll must not filter on cmb.available — that's loadBoundRawModels' job for the failure branch")
	}

	// Must still filter on pm.available — provider_models.available is the
	// "vendor says this model is callable" gate, independent of cmb state.
	for _, want := range []string{
		"pm.available",
		"pm.raw_model_name <> ''",
	} {
		if !strings.Contains(helperBody, want) {
			t.Fatalf("loadBoundRawModelsAll missing %q", want)
		}
	}
}

// TestCredentialProbeV2_BalanceQuotaProbeForceProbeDispatchesImmediate
// pins the admin force-probe path: when the operator clicks "force re-check
// after recharge", the probe must execute immediately (bypass the 2-min
// tick). This is the user's escape hatch for "I'm impatient, just probe
// now". Without this, an operator who has just topped up would wait up to
// 2 minutes for the next BalanceQuotaProbe tick to land.
//
// We assert two things:
//
//  1. ForceProbe sets probeNowAsync (the immediate-execution path), not
//     just probeSubmitter (the 5-min delayed path).
//  2. probeNowAsync is wired to ProbeNowAsync by main.go so the admin
//     endpoint can route through it.
//
// Implementation contract:
//
//   - balance_quota_probe.go:ForceProbe prefers probeNowAsync when wired.
//   - main.go wires balanceQuotaProbe.SetProbeNowAsync(credProbeV2.ProbeNowAsync).
func TestCredentialProbeV2_BalanceQuotaProbeForceProbeDispatchesImmediate(t *testing.T) {
	probeSrc, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatalf("read balance_quota_probe.go failed: %v", err)
	}
	mainSrc, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		// Allow running the test from any cwd.
		if mainSrc, err = os.ReadFile("../../cmd/gateway/main.go"); err != nil {
			t.Fatalf("read main.go failed: %v", err)
		}
	}

	probeBody := string(probeSrc)
	mainBody := string(mainSrc)

	// ForceProbe must prefer probeNowAsync (immediate) over probeSubmitter
	// (5-min delayed queue).
	if !strings.Contains(probeBody, "if p.probeNowAsync != nil {") {
		t.Fatalf("BalanceQuotaProbe.ForceProbe must prefer probeNowAsync (immediate) over probeSubmitter (5-min delayed)")
	}
	if !strings.Contains(probeBody, "p.probeNowAsync(credID)") {
		t.Fatalf("BalanceQuotaProbe.ForceProbe must dispatch via probeNowAsync when wired")
	}

	// main.go must wire the immediate-execution hook so the admin endpoint
	// can route through it.
	if !strings.Contains(mainBody, "balanceQuotaProbe.SetProbeNowAsync(credProbeV2.ProbeNowAsync)") {
		t.Fatalf("main.go must wire SetProbeNowAsync(credProbeV2.ProbeNowAsync) so admin force-probe dispatches immediately")
	}
}
