// Package probemode centralizes the LLM_GATEWAY_USE_NEW_PROBE_MODE switch.
//
// The same env parse previously lived as private copies in cmd/gateway
// (useNewProbeMode) and bg (newProbeModeEnabled). The R36 audit (A-3 sweep /
// R36 遗留#3 + #5) found data-path guards that kept reading the frozen
// legacy table while the new probe mode was active — revivers, recovery
// guards and routing filters kept enforcing verdicts from a table nothing
// updates anymore — so the switch, and the "which table holds live
// per-model probe verdicts" question it answers, now have one canonical
// home. bg delegates to this package; cmd/gateway's worker-gating copy
// (main_helpers.go useNewProbeMode) is intentionally left in place because
// main must decide worker startup before the DB pool exists, but its parse
// must stay character-for-character in sync with Enabled().
package probemode

import (
	"os"
	"strings"
)

// Enabled reports whether the new probe stack (credential_selfcheck +
// node_probe + system_health) owns the probe/self-check surface. Unset
// defaults to true (2026-07-14 spec rewrite); only an explicit falsey value
// restores the legacy worker set.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")))
	if v == "" {
		return true
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// GuardStateTable returns the (credential_id, raw_model_name, state) source
// that data-path guards must consult for per-model probe verdicts:
//
//   - legacy probe stack → model_probe_state, which legacy workers keep
//     updating;
//   - new probe stack (default) → the v_node_probe_state_compat projection,
//     whose identical column vocabulary tracks node_probe_state in real
//     time. model_probe_state is frozen under the new stack, so reading it
//     makes whatever verdict was on the table when the freeze happened
//     stick forever (stuck-suspended credentials, permanently unroutable
//     pairs, or — when a reviver dissolved the freeze — re-admission of
//     models the new system has proven dead).
//
// Compose into SQL as `FROM <returned> <alias>`; the alias is the caller's.
func GuardStateTable() string {
	if Enabled() {
		return "v_node_probe_state_compat"
	}
	return "model_probe_state"
}
