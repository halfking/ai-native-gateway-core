package telemetry

import (
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// shouldIsolateProbeEntry reports whether this entry belongs on the physical
// probe lane (env flag on + probekind markers).
func shouldIsolateProbeEntry(entry *RequestLogEntry) bool {
	if entry == nil {
		return false
	}
	stage, task, actor := "", "", ""
	if entry.OriginStage != nil {
		stage = *entry.OriginStage
	}
	if entry.TaskType != nil {
		task = *entry.TaskType
	}
	if entry.OriginActor != nil {
		actor = *entry.OriginActor
	}
	return probelane.ShouldIsolate(stage, task, entry.QualityFlags, actor)
}

// probeLaneTurn maps a RequestLogEntry onto the probe-lane persist DTO.
func probeLaneTurn(entry *RequestLogEntry) probelane.Turn {
	ts := time.Now().UTC()
	if entry.EventAt != nil && !entry.EventAt.IsZero() {
		ts = entry.EventAt.UTC()
	}
	statusCode := entry.ClientStatusCode
	if statusCode == nil {
		statusCode = entry.UpstreamStatusCode
	}
	totalTok := (*int)(nil)
	if entry.PromptTokens != nil || entry.CompletionTokens != nil {
		n := 0
		if entry.PromptTokens != nil {
			n += *entry.PromptTokens
		}
		if entry.CompletionTokens != nil {
			n += *entry.CompletionTokens
		}
		totalTok = &n
	}
	raw := ""
	if entry.OutboundModel != nil && *entry.OutboundModel != "" {
		raw = *entry.OutboundModel
	} else if entry.ClientModel != nil {
		raw = *entry.ClientModel
	}
	return probelane.Turn{
		RequestID:        entry.RequestID,
		TS:               ts,
		TenantID:         entry.TenantID,
		CredentialID:     intPtrToStringPtr(entry.CredentialID),
		RawModelName:     raw,
		ClientModel:      entry.ClientModel,
		OutboundModel:    entry.OutboundModel,
		Success:          entry.Success,
		StatusCode:       statusCode,
		ErrorKind:        entry.ErrorKind,
		LatencyMs:        entry.LatencyMs,
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		TotalTokens:      totalTok,
		OriginStage:      entry.OriginStage,
		OriginActor:      entry.OriginActor,
		TaskType:         entry.TaskType,
		APIKeyID:         intPtrToStringPtr(entry.APIKeyID),
		ProviderID:       entry.ProviderID,
		ClientIP:         entry.ClientIP,
		RequestPreview:   entry.RequestPreview,
		GwSessionID:      entry.GwSessionID,
		AgentName:        entry.AgentName,
		QualityFlags:     entry.QualityFlags,
	}
}

func intPtrToStringPtr(p *int) *string {
	if p == nil || *p == 0 {
		return nil
	}
	s := strconv.Itoa(*p)
	return &s
}
