package requestfact

import (
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// GatewayMetadata carries the request-context / session-metadata values the
// fact metadata block projects (audit 2026-09-08 #5). None of them live in
// the request body, so the handler layer collects them and stamps them onto
// the parsed request IR via InjectGatewayMetadata:
//
//   - Project: X-Gw-Project-Id request header (requestAttemptMeta.ProjectID,
//     domains/streaming/request_meta.go);
//   - Tags: session user_tags (X-Gw-Tags, SessionMetadata.UserTags in
//     domains/session/v2);
//   - TurnTotal: session turn counter (SessionSnapshot.TotalTurns,
//     public.sessions total_turns).
//
// UserID has a protocol-level source (OpenAI `user` / Anthropic
// `metadata.user_id`) and is therefore read from the IR directly, not
// injected.
type GatewayMetadata struct {
	Project   string
	Tags      []string
	TurnTotal int
}

// InjectGatewayMetadata stamps the gateway-side values onto the request IR's
// metadata carrier so ProjectRequestMetadata can project them onto the fact.
// Call once after the client IR is parsed. The upstream serializers never
// emit Project/Tags/TurnTotal (only user_id / request_id / Other reach the
// wire), so the stamp cannot leak into outbound bodies.
func InjectGatewayMetadata(requestIR *ir.InternalRequest, gateway GatewayMetadata) {
	if requestIR == nil {
		return
	}
	if requestIR.Metadata == nil {
		if gateway.Project == "" && gateway.TurnTotal == 0 && len(gateway.Tags) == 0 {
			return
		}
		requestIR.Metadata = &ir.Metadata{}
	}
	requestIR.Metadata.Project = gateway.Project
	requestIR.Metadata.TurnTotal = gateway.TurnTotal
	if len(gateway.Tags) > 0 {
		requestIR.Metadata.Tags = append([]string(nil), gateway.Tags...)
	} else {
		requestIR.Metadata.Tags = nil
	}
}

// ProjectRequestMetadata derives the fact's metadata block from the parsed
// request IR (audit 2026-09-08 #5). user_id prefers the Anthropic
// metadata.user_id carrier and falls back to the normalized OpenAI `user`
// field; project/tags/turn_total are the gateway-injected carriers. The
// returned block is a copy: mutating it never aliases the IR.
func ProjectRequestMetadata(requestIR *ir.InternalRequest) Metadata {
	var meta Metadata
	if requestIR == nil {
		return meta
	}
	if requestIR.Metadata != nil {
		meta.Project = requestIR.Metadata.Project
		meta.TurnTotal = requestIR.Metadata.TurnTotal
		if len(requestIR.Metadata.Tags) > 0 {
			meta.Tags = append([]string(nil), requestIR.Metadata.Tags...)
		}
		meta.UserID = requestIR.Metadata.UserID
	}
	if meta.UserID == "" {
		meta.UserID = requestIR.User
	}
	return meta
}
