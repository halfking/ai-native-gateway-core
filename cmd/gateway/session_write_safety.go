package main

import (
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Flags cannot provide a V2 fact source in lite/no-PG mode: no writer is wired.
func validateSessionWriteSafety(pgEnabled bool) error {
	v1 := settings.RequestLogsWriteEnabled()
	if err := settings.ValidateSessionWriteState(v1,
		settings.GetPlatformBool("sessions_v2.enabled", false),
		settings.GetPlatformBool("sessions_v2.shadow_write", false)); err != nil {
		return err
	}
	if !v1 && !pgEnabled {
		return fmt.Errorf("%w: V2 persistence requires PostgreSQL", settings.ErrNoRequestWriteSource)
	}
	return nil
}

// validSessionWriteConfigExamples returns the canonical configurations that
// pass validateSessionWriteSafety. Returned to main() on hard-fail so ops see
// the exact keys to set, not just the error sentinel.
//
// 2026-10-20 (audit D0): the list used to name `REQUEST_LOGS_WRITE_ENABLED`
// — an env var **that does not exist**. The real name is derived from the
// key by settings.EnvNameAuto, and the old "V2-only" line omitted
// sessions_v2.shadow_write, i.e. it advertised a configuration the validator
// rejects. Both were operator-facing lies in the one message that an operator
// reads when the gateway refuses to start, so both are now generated from the
// real names. The list is intentionally short and matches
// settings.ValidateSessionWriteState's accepted shapes.
func validSessionWriteConfigExamples() string {
	v1Env := settings.EnvNameAuto(settings.KeyRequestLogsWriteEnabled)
	v2Env := "LLM_GATEWAY_SESSIONS_V2_ENABLED"
	shadowEnv := "LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE"
	return strings.Join([]string{
		"  - V1-only:    " + v1Env + "=true  (no V2, no PG required)",
		"  - V1+V2 dual: PG enabled + " + v1Env + "=true + " + v2Env + "=true + " + shadowEnv + "=true",
		"  - V2-only:    PG enabled + " + v2Env + "=true + " + shadowEnv + "=true",
	}, "\n")
}
