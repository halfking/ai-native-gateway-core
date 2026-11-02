package probelane

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer matches *pgxpool.Pool / pgx.Tx Exec.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Turn is the narrow probe-lane row written to session_turns_probe_hot.
// Defined here (not telemetry.RequestLogEntry) so this package stays a leaf
// and cannot form an import cycle with domains/hooks/observability/telemetry.
type Turn struct {
	RequestID        string
	TS               time.Time
	TenantID         string
	CredentialID     *string
	RawModelName     string
	ClientModel      *string
	OutboundModel    *string
	Success          bool
	StatusCode       *int
	ErrorKind        *string
	LatencyMs        *int
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
	OriginStage      *string
	OriginActor      *string
	TaskType         *string
	APIKeyID         *string
	ProviderID       *int
	ClientIP         *string
	RequestPreview   *string
	GwSessionID      *string
	AgentName        *string
	QualityFlags     []string
}

// PersistTurn upserts one probe-lane row into session_turns_probe_hot.
func PersistTurn(ctx context.Context, exec Execer, turn Turn) error {
	if exec == nil {
		return fmt.Errorf("probelane: nil exec")
	}
	if turn.RequestID == "" {
		return fmt.Errorf("probelane: empty request_id")
	}
	ts := turn.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	tenant := turn.TenantID
	if tenant == "" {
		tenant = "default"
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO session_turns_probe_hot (
			request_id, ts, tenant_id, credential_id, raw_model_name,
			client_model, outbound_model, success, status_code, error_kind,
			latency_ms, prompt_tokens, completion_tokens, total_tokens,
			origin_stage, origin_actor, task_type, api_key_id, provider_id,
			client_ip, request_preview, gw_session_id, agent_name, quality_flags
		) VALUES (
			$1,$2,$3,$4,$5,
			$6,$7,$8,$9,$10,
			$11,$12,$13,$14,
			$15,$16,$17,$18,$19,
			$20,$21,$22,$23,$24
		)
		ON CONFLICT (request_id) DO UPDATE SET
			ts = EXCLUDED.ts,
			success = COALESCE(EXCLUDED.success, session_turns_probe_hot.success),
			status_code = COALESCE(EXCLUDED.status_code, session_turns_probe_hot.status_code),
			error_kind = COALESCE(EXCLUDED.error_kind, session_turns_probe_hot.error_kind),
			latency_ms = COALESCE(EXCLUDED.latency_ms, session_turns_probe_hot.latency_ms),
			prompt_tokens = COALESCE(EXCLUDED.prompt_tokens, session_turns_probe_hot.prompt_tokens),
			completion_tokens = COALESCE(EXCLUDED.completion_tokens, session_turns_probe_hot.completion_tokens),
			total_tokens = COALESCE(EXCLUDED.total_tokens, session_turns_probe_hot.total_tokens),
			origin_stage = COALESCE(EXCLUDED.origin_stage, session_turns_probe_hot.origin_stage),
			origin_actor = COALESCE(EXCLUDED.origin_actor, session_turns_probe_hot.origin_actor),
			task_type = COALESCE(EXCLUDED.task_type, session_turns_probe_hot.task_type),
			quality_flags = COALESCE(EXCLUDED.quality_flags, session_turns_probe_hot.quality_flags)
	`,
		turn.RequestID,
		ts,
		tenant,
		turn.CredentialID,
		nullIfEmpty(turn.RawModelName),
		turn.ClientModel,
		turn.OutboundModel,
		turn.Success,
		turn.StatusCode,
		turn.ErrorKind,
		turn.LatencyMs,
		turn.PromptTokens,
		turn.CompletionTokens,
		turn.TotalTokens,
		turn.OriginStage,
		turn.OriginActor,
		turn.TaskType,
		turn.APIKeyID,
		turn.ProviderID,
		turn.ClientIP,
		turn.RequestPreview,
		turn.GwSessionID,
		turn.AgentName,
		turn.QualityFlags,
	)
	if err != nil {
		return fmt.Errorf("probelane persist: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
