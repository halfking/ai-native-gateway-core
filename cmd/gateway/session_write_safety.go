package main

import (
	"fmt"

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
