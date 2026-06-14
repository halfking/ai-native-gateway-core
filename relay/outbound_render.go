package relay

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
)

// renderOutboundFromTransform mirrors Python prepare_candidate → render_outbound_model().
// When no transform template matches, returns "" so routing.resolveOutboundModel uses
// the offer's COALESCE(outbound_model_name, raw_model_name) from cand.RawModel.
func renderOutboundFromTransform(
	txResult *transform.TransformResult,
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
	return transform.RenderOutboundModel(
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
