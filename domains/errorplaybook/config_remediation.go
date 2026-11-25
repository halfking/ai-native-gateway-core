package errorplaybook

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
)

func (p *Remediation) runConfigDrift(ctx context.Context) {
	target := p.endpoint(remediation.DriftPluginID)
	if target.SocketPath == "" {
		return
	}
	rejection, ok := p.runtime.ConfigRejection()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remediationQueryTime)
	defer cancel()
	kind, expected := remediation.ConfigChanged, remediation.ReviewConfig
	if rejection.Invalid {
		kind, expected = remediation.ConfigInvalid, remediation.RestoreConfig
	}
	signal := remediation.DriftSignal{SchemaVersion: remediation.SchemaVersion, Kind: kind}
	eventID := hex.EncodeToString(rejection.ID[:])
	// The same divergence remains one event while its configuration and
	// template version are unchanged, even across worker ticks and restarts.
	recorded, err := p.store.hasConfigRecommendation(ctx, eventID, target.Version)
	if err != nil {
		slog.Warn("error_playbook: look up config recommendation failed", "error", err)
		return
	}
	if recorded {
		return
	}
	rec, err := requestReview(ctx, target, remediation.DriftPluginID, signal)
	if err != nil || rec.Recommendation != expected {
		slog.Warn("error_playbook: config recommendation rejected", "error", err)
		return
	}
	current, active := p.runtime.ConfigRejection()
	if !active || current != rejection || p.endpoint(remediation.DriftPluginID) != target {
		return
	}
	audit, err := json.Marshal(struct {
		remediation.DriftSignal
		EventID         string `json:"event_id"`
		TemplateVersion string `json:"template_version"`
		Recommendation  string `json:"recommendation"`
	}{signal, eventID, rec.TemplateVersion, rec.Recommendation})
	if err == nil {
		err = p.store.recordRecommendation(ctx, remediation.DriftPluginID, eventID, rec.TemplateVersion, time.Unix(0, 0), audit)
	}
	if err != nil {
		slog.Warn("error_playbook: record config recommendation failed", "error", err)
	}
}

func (s *SQLStore) hasConfigRecommendation(ctx context.Context, eventID, version string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM error_playbook_executions
			WHERE error_type = $1 AND executed_by = $1
			  AND context->>'event_id' = $2 AND context->>'template_version' = $3
		)`, recommendationActor(remediation.DriftPluginID), eventID, version).Scan(&found)
	return found, err
}
