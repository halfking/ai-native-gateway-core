package relay

import (
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
