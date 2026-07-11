package transformation

import (
	"os"
	"strings"
)

// ShouldEnableTransportIR determines whether IR transport should be enabled.
// Default: enabled (when LLM_GATEWAY_TRANSPORT_IR is unset or any value except "false"/"0").
// Explicit disable: LLM_GATEWAY_TRANSPORT_IR=false or LLM_GATEWAY_TRANSPORT_IR=0.
//
// This implements P0-1 from the 2026-07-11 audit: IR transport should be on by
// default to preserve vendor extensions (MiniMax bot_setting, GLM retrieval, etc.).
func ShouldEnableTransportIR() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_TRANSPORT_IR")))
	// Explicit false/0 disables; everything else (including unset) enables.
	return val != "false" && val != "0"
}
