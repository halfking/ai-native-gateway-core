package errorplaybook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/controller"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
)

const (
	remediationRoundTime   = 5 * time.Second
	remediationQueryTime   = 2 * time.Second
	remediationMaxLookback = 65 * time.Minute
	remediationCallTime    = 300 * time.Millisecond
	remediationBatch       = 16
)

// Remediation evaluates enabled read-only templates from the playbook worker.
type Remediation struct {
	source  *SQLSource
	store   *SQLStore
	runtime *controller.Controller
}

func NewRemediation(source *SQLSource, store *SQLStore, runtime *controller.Controller) *Remediation {
	return &Remediation{source: source, store: store, runtime: runtime}
}

func (p *Remediation) run(ctx context.Context, until time.Time, interval time.Duration) {
	p.runCooldown(ctx, until, interval)
	p.runSSEAnalysis(ctx, until, interval)
	p.runConfigDrift(ctx)
}

func recommendationActor(id string) string { return "plugin:" + id }

func IsRecommendationActor(actor string) bool {
	id, ok := strings.CutPrefix(actor, "plugin:")
	return ok && remediation.IsPluginID(id)
}

func recommendationActors() []string {
	ids := remediation.PluginIDs()
	for i, id := range ids {
		ids[i] = recommendationActor(id)
	}
	return ids
}

func (p *Remediation) endpoint(pluginID string) controller.CapabilityEndpoint {
	return p.runtime.ReadyCapability(pluginID, pluginruntime.CapabilityErrorRemediation)
}

func remediationWindow(until time.Time, interval time.Duration) (start, end time.Time) {
	end = until.UTC().Truncate(remediation.Window)
	lookback := min(remediationMaxLookback, max(15*time.Minute, interval+remediation.Window))
	start = end.Add(-lookback).Truncate(remediation.Window)
	return start, end
}

func requestRecommendation(ctx context.Context, target controller.CapabilityEndpoint, signal any) ([]byte, error) {
	payload, err := json.Marshal(signal)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, remediationCallTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+remediation.SuggestPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Open a fresh connection so a later generation never shares a socket.
	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 4096,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", target.SocketPath)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, remediation.MaxPayload+1))
}

func requestReview(ctx context.Context, target controller.CapabilityEndpoint, pluginID string, signal any) (remediation.Review, error) {
	data, err := requestRecommendation(ctx, target, signal)
	if err != nil {
		return remediation.Review{}, err
	}
	review, err := remediation.DecodeReview(bytes.NewReader(data))
	if err != nil || !review.Valid(target.Version, pluginID) {
		return remediation.Review{}, errors.New("invalid remediation recommendation")
	}
	return review, nil
}

func (s *SQLStore) recordRecommendation(ctx context.Context, pluginID, eventID, version string, start time.Time, audit json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := "errorplaybook/" + pluginID + "/" + version + "/" + eventID
	// Serialize each event/version across gateway replicas. The following
	// statement takes a fresh read-committed snapshot after acquiring the lock.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO error_playbook_executions (error_type, executed_by, trigger_source, status, context, duration_ms)
		SELECT $1, $1, $2, $3, $4::jsonb, 0
		WHERE NOT EXISTS (
			SELECT 1 FROM error_playbook_executions e
			WHERE e.error_type = $1 AND e.executed_by = $1 AND e.created_at >= $5
			  AND e.context->>'event_id' = $6 AND e.context->>'template_version' = $7
		)`, recommendationActor(pluginID), TriggerAutoAnalyze, StatusAwaitingApproval, audit,
		start, eventID, version); err != nil {
		return err
	}
	return tx.Commit()
}
