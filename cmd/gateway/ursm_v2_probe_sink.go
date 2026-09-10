package main

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/bg"
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

// ursmV2EvidenceSource adapts the URSM v2 manager's Redis-backed
// ProbeHealthEvidence snapshot to bg's NodeHealthEvidenceSource contract for
// the self-check necessity gate (bg/probe_necessity.go). Evidence comes from
// the same node hashes routing reads, so "redis 缓存中的当前请求状态" is
// authoritative — including the k2/legacy key fallback the store applies per
// schema mode.
type ursmV2EvidenceSource struct {
	manager *v2.Manager
}

func (s ursmV2EvidenceSource) NodeHealthEvidence(ctx context.Context, tenant string, credentialID int, models []string) (map[string]bg.NodeHealthEvidence, error) {
	if s.manager == nil {
		return nil, fmt.Errorf("ursm v2 manager not wired")
	}
	evidence, err := s.manager.ProbeHealthEvidence(ctx, tenant, credentialID, models)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bg.NodeHealthEvidence, len(evidence))
	for _, e := range evidence {
		out[e.RawModel] = bg.NodeHealthEvidence{
			Known:              e.Known,
			Healthy:            e.Healthy,
			LastRequestAt:      e.LastRequestAt,
			LastRequestFailed:  e.LastRequestFailed,
			LastRequestErrorAt: e.LastRequestErrorAt,
		}
	}
	return out, nil
}
