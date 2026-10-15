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

// validSessionWriteConfigExamples returns four canonical configurations that
// pass validateSessionWriteSafety. Returned to main() on hard-fail so ops see
// the exact keys to set, not just the error sentinel. The list is intentionally
// short and matches settings.ValidateSessionWriteState's accepted shapes.
func validSessionWriteConfigExamples() string {
	return strings.Join([]string{
		"  - V1-only:    REQUEST_LOGS_WRITE_ENABLED=true  (no V2, no PG required)",
		"  - V2-only:    PG enabled + SESSIONS_V2_ENABLED=true",
		"  - V1+V2 dual: PG enabled + REQUEST_LOGS_WRITE_ENABLED=true (V2 runs alongside)",
		"  - Shadow:     PG enabled + SESSIONS_V2_ENABLED=true + SESSIONS_V2_SHADOW_WRITE=true",
	}, "\n")
}
