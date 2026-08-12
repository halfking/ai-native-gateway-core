package streaming

import (
	"os"

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
