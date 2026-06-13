package relay

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/transform"
)

func TestRenderOutboundFromTransform_MatchesPythonDefault(t *testing.T) {
	cand := provider.Candidate{
		RawModel:      "z-ai/glm-5.1",
		OfferRawModel: "z-ai/glm-5.1",
	}
	got := renderOutboundFromTransform(nil, cand, "glm-5.1")
	if got != "" {
		t.Fatalf("no transform → empty explicit outbound, got %q", got)
	}

	candZhipu := provider.Candidate{
		RawModel:      "glm-5.1",
		OfferRawModel: "glm-5.1",
	}
	tx := &transform.TransformResult{OutboundModel: "{offer.raw_model_name}"}
	got = renderOutboundFromTransform(tx, candZhipu, "glm-5.1")
	if got != "glm-5.1" {
		t.Fatalf("template raw_model_name → %q, want glm-5.1", got)
	}

	txNvidia := &transform.TransformResult{OutboundModel: "{offer.outbound_model_name}"}
	got = renderOutboundFromTransform(txNvidia, cand, "glm-5.1")
	if got != "z-ai/glm-5.1" {
		t.Fatalf("template outbound_model_name → %q, want z-ai/glm-5.1", got)
	}
}
