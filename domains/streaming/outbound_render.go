package streaming

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/provider"
)

// renderOutboundFromTransform mirrors Python prepare_candidate → render_outbound_model().
// When no transform template matches, returns "" so streaming.resolveOutboundModel uses
// the offer's COALESCE(outbound_model_name, raw_model_name) from cand.RawModel.
func renderOutboundFromTransform(
	txResult *transformation.TransformResult,
	cand provider.Candidate,
	canonicalName string,
) string {
	if txResult == nil || txResult.OutboundModel == "" {
		return ""
	}
	offerRaw := cand.OfferRawModel
	if offerRaw == "" {
		offerRaw = cand.RawModel
	}
	return transformation.RenderOutboundModel(
		txResult.OutboundModel,
		cand.RawModel,
		offerRaw,
		canonicalName,
	)
}

// outboundModelForLog picks the supplier-facing model name stored on request_logs.
// When the transform leaves outbound equal to the client model, prefer the
// credential offer raw model so /request-logs can show "req → provider".
func outboundModelForLog(clientModel, explicitOutbound, candidateRaw string) string {
	clientModel = strings.TrimSpace(clientModel)
	explicitOutbound = strings.TrimSpace(explicitOutbound)
	candidateRaw = strings.TrimSpace(candidateRaw)

	if candidateRaw != "" && candidateRaw != clientModel {
		return candidateRaw
	}
	if explicitOutbound != "" && explicitOutbound != clientModel {
		return explicitOutbound
	}
	if explicitOutbound != "" {
		return explicitOutbound
	}
	if candidateRaw != "" {
		return candidateRaw
	}
	return clientModel
}
