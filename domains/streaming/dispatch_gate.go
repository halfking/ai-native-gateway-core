package streaming

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// dispatchAllowModelChangeEnabled reports whether model-level failover is
// permitted for auto-routed requests.
//
// Source of truth is the platform setting dispatch_v2.allow_model_change
// (default false; admin-tunable, mirrors the Pipeline-wide kill-switch read at
// boot in cmd/gateway/main_dispatch.go). The legacy env var
// AUTO_ROUTE_FALLBACK_ENABLED=true is kept as a backward-compatible force-on
// override so existing deployments keep working until they migrate to the
// setting. Either source being "on" enables the feature.
//
// Note: this is the per-request gate; the dispatch Pipeline additionally
// requires its own boot-time allowModelChange to be true (see
// domains/dispatch/dispatcher.go tryModelChange), so both must agree.
func dispatchAllowModelChangeEnabled() bool {
	if os.Getenv("AUTO_ROUTE_FALLBACK_ENABLED") == "true" {
		return true
	}
	return settings.GetPlatformBool("dispatch_v2.allow_model_change", false)
}

// parsePinCredentialHeader reads the X-LLM-Pin-Credential header (trusted-only:
// OriginMiddleware strips it for non-system callers, so a non-nil return here is
// authoritative). Returns nil when absent or unparsable. Used by self-check /
// node-probe to force routing to a specific credential (需求 6, bullet 5).
func parsePinCredentialHeader(r *http.Request) *int {
	if r == nil {
		return nil
	}
	v := strings.TrimSpace(r.Header.Get("X-LLM-Pin-Credential"))
	if v == "" {
		return nil
	}
	id, err := strconv.Atoi(v)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}
