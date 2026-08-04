package main

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// ursmV2ProbeSink keeps bg.NodeProbeWorker decoupled from the concrete URSM
// manager while letting authoritative deployments feed active probe evidence
// into the sole routing-state authority.
type ursmV2ProbeSink struct {
	manager *v2.Manager
}

func (s ursmV2ProbeSink) ApplyProbeForTenant(ctx context.Context, tenant string, credentialID int, rawModel string, success bool, latencyMs int) error {
	return s.manager.ApplyProbeForTenant(ctx, tenant, api.ProbeOutcome{
		CredentialID: credentialID,
		RawModel:     rawModel,
		Success:      success,
		LatencyMs:    latencyMs,
	})
}
