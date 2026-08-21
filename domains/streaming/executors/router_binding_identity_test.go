package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestCandidateSeedUsesOfferRawModelForURSMIdentity(t *testing.T) {
	candidate := provider.Candidate{
		ProviderID: 1, CredentialID: 2,
		RawModel: "shared-outbound-alias", OfferRawModel: "binding-model-a", StandardizedName: "client-model",
	}
	seed := candidateSeed(candidate, "tenant-a", "client-model")
	if seed.RawModel != "binding-model-a" {
		t.Fatalf("seed RawModel=%q, want offer raw binding model", seed.RawModel)
	}
}
