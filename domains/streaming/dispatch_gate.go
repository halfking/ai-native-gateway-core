package streaming

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// dispatchAllowModelChangeEnabled reports whether model-level failover is
// permitted for auto-routed requests.
//
// Source of truth is the hot-reloadable platform setting
// dispatch_v2.allow_model_change (default false). The legacy env var
// AUTO_ROUTE_FALLBACK_ENABLED=true remains a backward-compatible force-on
// override.
func dispatchAllowModelChangeEnabled() bool {
	return dispatch.IsModelChangeEnabled()
}

func autoTaskFromLogContext(c *RequestLogContext) string {
	if c == nil {
		return ""
	}
	return c.TaskType
}

func autoProfileFromLogContext(c *RequestLogContext) string {
	if c == nil {
		return ""
	}
	return c.AutoProfile
}

func autoWorkTypeFromLogContext(c *RequestLogContext) string {
	if c == nil {
		return ""
	}
	return c.WorkType
}

func autoSignalsFromLogContext(c *RequestLogContext) autoroute.ClassificationSignals {
	if c == nil {
		return autoroute.ClassificationSignals{}
	}
	return c.AutoSignals
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
