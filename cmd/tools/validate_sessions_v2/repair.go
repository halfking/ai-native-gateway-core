package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RepairPlan describes what will be deleted and rebuilt
type RepairPlan struct {
	SessionID  string
	TenantID   string
	SourceRows int // V1 rows that will be used as source

	// What will be deleted
	DeleteCounts map[string]int // table -> row count

	// What will be rebuilt
	RebuildCounts map[string]int // table -> expected row count
}

// RepairResult contains the outcome of a repair operation
type RepairResult struct {
	SessionID          string
	TenantID           string
	Success            bool
	Error              error
	DeletedRows        map[string]int
	InsertedRows       map[string]int
	VerificationReport *SessionReport
}

// SessionRepairer repairs V2 data by rebuilding from V1
type SessionRepairer struct {
	db            *pgxpool.Pool
	loader        *SessionLoader
	validator     *SessionValidator
	reconstructor *MessageReconstructor
	reportGen     *ReportGenerator
}

// NewSessionRepairer creates a new session repairer
func NewSessionRepairer(
	db *pgxpool.Pool,
	loader *SessionLoader,
	validator *SessionValidator,
	reconstructor *MessageReconstructor,
	reportGen *ReportGenerator,
) *SessionRepairer {
	return &SessionRepairer{
		db:            db,
		loader:        loader,
		validator:     validator,
		reconstructor: reconstructor,
		reportGen:     reportGen,
	}
}

// PlanRepair generates a repair plan without executing it
func (r *SessionRepairer) PlanRepair(ctx context.Context, tenantID, sessionID string) (*RepairPlan, error) {
	plan := &RepairPlan{
		SessionID:     sessionID,
		TenantID:      tenantID,
		DeleteCounts:  make(map[string]int),
		RebuildCounts: make(map[string]int),
	}

	// Count V1 source rows
	v1Turns, err := r.loader.LoadV1Turns(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V1 turns: %w", err)
	}
	plan.SourceRows = len(v1Turns)

	// Count existing V2 rows that will be deleted
	v2Turns, err := r.loader.LoadV2Turns(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 turns: %w", err)
	}
	plan.DeleteCounts["session_turns"] = len(v2Turns)

	v2Bodies, err := r.loader.LoadV2Bodies(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 bodies: %w", err)
	}
	plan.DeleteCounts["session_bodies"] = len(v2Bodies)

	v2Session, err := r.loader.LoadV2Session(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 session: %w", err)
	}
	if v2Session != nil {
		plan.DeleteCounts["sessions"] = 1
	} else {
		plan.DeleteCounts["sessions"] = 0
	}

	// Count turn logs (may not exist for old sessions)
	var turnLogsCount int
	err = r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM public.session_turn_logs
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID).Scan(&turnLogsCount)
	if err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("count turn logs: %w", err)
	}
	plan.DeleteCounts["session_turn_logs"] = turnLogsCount

	// Expected rebuild counts
	plan.RebuildCounts["session_turns"] = plan.SourceRows
	plan.RebuildCounts["session_bodies"] = plan.SourceRows
	plan.RebuildCounts["sessions"] = 1
	// Turn logs are not rebuilt (they are transient)

	return plan, nil
}

// ExecuteRepair rebuilds V2 data from V1 in a transaction
func (r *SessionRepairer) ExecuteRepair(ctx context.Context, tenantID, sessionID string) (*RepairResult, error) {
	result := &RepairResult{
		SessionID:    sessionID,
		TenantID:     tenantID,
		DeletedRows:  make(map[string]int),
		InsertedRows: make(map[string]int),
	}

	// Load V1 source data before transaction
	v1Turns, err := r.loader.LoadV1Turns(ctx, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("load V1 turns: %w", err)
		return result, result.Error
	}

	if len(v1Turns) == 0 {
		result.Error = fmt.Errorf("no V1 turns found for session %s", sessionID)
		return result, result.Error
	}

	// Begin transaction
	tx, err := r.db.Begin(ctx)
	if err != nil {
		result.Error = fmt.Errorf("begin transaction: %w", err)
		return result, result.Error
	}
	defer tx.Rollback(ctx)

	// 1. Acquire the same canonical lock used by writer, aggregator, and promote.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		public.session_turns_advisory_lock_key($1, $2)
	)`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("acquire advisory lock: %w", err)
		return result, result.Error
	}

	// 2. Delete existing V2 data (reverse FK order)

	// Delete turn logs
	tag, err := tx.Exec(ctx, `
		DELETE FROM public.session_turn_logs
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("delete turn logs: %w", err)
		return result, result.Error
	}
	result.DeletedRows["session_turn_logs"] = int(tag.RowsAffected())

	// Delete bodies
	tag, err = tx.Exec(ctx, `
		DELETE FROM public.session_bodies
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("delete bodies: %w", err)
		return result, result.Error
	}
	result.DeletedRows["session_bodies"] = int(tag.RowsAffected())

	// Delete turns from both stores. Historical repair writes the partitioned
	// table directly, while live traffic uses session_turns_hot.
	hotTag, err := tx.Exec(ctx, `
		DELETE FROM public.session_turns_hot
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("delete hot turns: %w", err)
		return result, result.Error
	}
	tag, err = tx.Exec(ctx, `
		DELETE FROM public.session_turns
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("delete partitioned turns: %w", err)
		return result, result.Error
	}
	result.DeletedRows["session_turns"] = int(hotTag.RowsAffected() + tag.RowsAffected())

	// Delete session snapshot
	tag, err = tx.Exec(ctx, `
		DELETE FROM public.sessions
		WHERE tenant_id = $1 AND session_id = $2
	`, tenantID, sessionID)
	if err != nil {
		result.Error = fmt.Errorf("delete session: %w", err)
		return result, result.Error
	}
	result.DeletedRows["sessions"] = int(tag.RowsAffected())

	// 3. Rebuild from V1

	// Insert session_turns
	turnsInserted := 0
	for i, turn := range v1Turns {
		turnNo := i + 1

		// Extract submit mode from compression_meta
		submitMode := "full" // Default
		var compressionMeta map[string]interface{}
		if len(turn.CompressionMeta) > 0 {
			if err := json.Unmarshal(turn.CompressionMeta, &compressionMeta); err == nil {
				if mode, ok := compressionMeta["submit_mode"].(string); ok {
					submitMode = mode
				}
			}
		}

		// Extract tokens from usage
		var promptTokens, completionTokens int
		var usage map[string]interface{}
		if len(turn.Usage) > 0 {
			if err := json.Unmarshal(turn.Usage, &usage); err == nil {
				if pt, ok := usage["prompt_tokens"].(float64); ok {
					promptTokens = int(pt)
				}
				if ct, ok := usage["completion_tokens"].(float64); ok {
					completionTokens = int(ct)
				}
			}
		}

		// Extract verdicts
		injectionVerdict := "skip"
		outputVerdict := "skip"
		if len(turn.CompressionMeta) > 0 {
			if iv, ok := compressionMeta["injection_verdict"].(string); ok {
				injectionVerdict = iv
			}
			if ov, ok := compressionMeta["output_verdict"].(string); ok {
				outputVerdict = ov
			}
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO public.session_turns (
				tenant_id, session_id, turn_no, request_id, ts,
				submit_mode, model, provider, credential_id,
				prompt_tokens, completion_tokens, cost_usd,
				injection_verdict, output_verdict,
				success, source_kind, quality, partition_date
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10, $11, $12,
				$13, $14,
				$15, 'backfill', 'verified', $16
			)
		`, tenantID, sessionID, turnNo, turn.RequestID, turn.Ts,
			submitMode, turn.ClientModel, turn.ProviderID, turn.CredentialID,
			promptTokens, completionTokens, turn.CostUSD,
			injectionVerdict, outputVerdict,
			turn.Success, calendarDateUTC(turn.Ts))

		if err != nil {
			result.Error = fmt.Errorf("insert turn %d: %w", turnNo, err)
			return result, result.Error
		}
		turnsInserted++
	}
	result.InsertedRows["session_turns"] = turnsInserted

	// Insert session_bodies
	bodiesInserted := 0
	for i, turn := range v1Turns {
		turnNo := i + 1

		// Extract messages from request body
		var requestBody struct {
			Messages []Message `json:"messages"`
		}
		requestDelta := []Message{}
		if len(turn.RequestBody) > 0 {
			if err := json.Unmarshal(turn.RequestBody, &requestBody); err == nil {
				requestDelta = requestBody.Messages
			}
		}

		requestDeltaJSON, _ := json.Marshal(requestDelta)

		// Response delta is empty for backfill (we don't have structured response)
		responseDelta := []Message{}
		responseDeltaJSON, _ := json.Marshal(responseDelta)

		_, err = tx.Exec(ctx, `
			INSERT INTO public.session_bodies (
				tenant_id, session_id, turn_no, request_id, ts,
				request_delta, response_delta,
				request_attachments, response_attachments, partition_date
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7,
				'[]'::jsonb, '[]'::jsonb, $8
			)
		`, tenantID, sessionID, turnNo, turn.RequestID, turn.Ts,
			requestDeltaJSON, responseDeltaJSON, calendarDateUTC(turn.Ts))

		if err != nil {
			result.Error = fmt.Errorf("insert body %d: %w", turnNo, err)
			return result, result.Error
		}
		bodiesInserted++
	}
	result.InsertedRows["session_bodies"] = bodiesInserted

	// Insert sessions snapshot (aggregated from turns)
	totalTokens := 0
	totalCost := 0.0
	for _, turn := range v1Turns {
		var usage map[string]interface{}
		if len(turn.Usage) > 0 {
			if err := json.Unmarshal(turn.Usage, &usage); err == nil {
				if pt, ok := usage["prompt_tokens"].(float64); ok {
					totalTokens += int(pt)
				}
				if ct, ok := usage["completion_tokens"].(float64); ok {
					totalTokens += int(ct)
				}
			}
		}
		totalCost += turn.CostUSD
	}

	lastTurn := v1Turns[len(v1Turns)-1]

	_, err = tx.Exec(ctx, `
		INSERT INTO public.sessions (
			tenant_id, session_id, status,
			total_turns, total_tokens, total_cost_usd,
			last_turn_no, last_model, last_provider,
				primary_request_id, created_at, updated_at, partition_date
			) VALUES (
				$1, $2, 'closed',
				$3, $4, $5,
				$6, $7, $8,
				$9, $10, $11, $12
			)
		`, tenantID, sessionID,
		len(v1Turns), totalTokens, totalCost,
		len(v1Turns), lastTurn.ClientModel, lastTurn.ProviderID,
		v1Turns[0].RequestID, v1Turns[0].Ts, lastTurn.Ts, calendarDateUTC(lastTurn.Ts))

	if err != nil {
		result.Error = fmt.Errorf("insert session: %w", err)
		return result, result.Error
	}
	result.InsertedRows["sessions"] = 1

	// 4. Commit transaction
	if err := tx.Commit(ctx); err != nil {
		result.Error = fmt.Errorf("commit transaction: %w", err)
		return result, result.Error
	}

	result.Success = true
	return result, nil
}

func calendarDateUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// VerifyRepair runs validation after repair to confirm success
func (r *SessionRepairer) VerifyRepair(ctx context.Context, tenantID, sessionID string) (*SessionReport, error) {
	// Load data
	v1Turns, err := r.loader.LoadV1Turns(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V1 turns: %w", err)
	}

	v2Turns, err := r.loader.LoadV2Turns(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 turns: %w", err)
	}

	v2Bodies, err := r.loader.LoadV2Bodies(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 bodies: %w", err)
	}

	v2Session, err := r.loader.LoadV2Session(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load V2 session: %w", err)
	}

	// Run validation
	validator := NewSessionValidator(tenantID, sessionID)
	checks := validator.ValidateSession(v1Turns, v2Turns, v2Bodies, v2Session)
	reconResults := r.reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)

	// Generate report
	report := r.reportGen.GenerateSessionReport(tenantID, sessionID, v1Turns, v2Turns, checks, reconResults)

	return report, nil
}
