// Package workers — session summary writeback to kxmemory.
package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/analysis"
)

// MemoraWritebackConfig configures best-effort session summary writeback.
type MemoraWritebackConfig struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

// MemoraWritebackHook writes completed session summaries to kxmemory.
type MemoraWritebackHook struct {
	db     analysis.DB
	config MemoraWritebackConfig
	client *http.Client
	logger *slog.Logger
}

type memoraSummaryPayload struct {
	SessionID    string         `json:"session_id"`
	TenantID     string         `json:"tenant_id"`
	UserID       string         `json:"user_id"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	KeyTopics    []string       `json:"key_topics"`
	UserIntent   *string        `json:"user_intent,omitempty"`
	WorkTypes    []string       `json:"work_types"`
	QualityScore *int           `json:"quality_score,omitempty"`
	ModelUsed    *string        `json:"model_used,omitempty"`
	TurnCount    int            `json:"turn_count"`
	TotalTokens  int64          `json:"total_tokens"`
	SessionStart time.Time      `json:"session_start"`
	SessionEnd   time.Time      `json:"session_end"`
	Metadata     map[string]any `json:"metadata"`
}

// NewMemoraWritebackHook creates a close hook. Empty URL or API key disables it.
func NewMemoraWritebackHook(db analysis.DB, config MemoraWritebackConfig, logger *slog.Logger) *MemoraWritebackHook {
	if logger == nil {
		logger = slog.Default()
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	return &MemoraWritebackHook{
		db:     db,
		config: config,
		client: &http.Client{Timeout: config.Timeout},
		logger: logger,
	}
}

// OnSessionClosed loads the generated summary and writes it to kxmemory.
func (h *MemoraWritebackHook) OnSessionClosed(ctx context.Context, tenantID, gwSessionID string) error {
	if h == nil || h.db == nil || strings.TrimSpace(h.config.BaseURL) == "" || strings.TrimSpace(h.config.APIKey) == "" {
		return nil
	}

	payload, err := h.loadPayload(ctx, tenantID, gwSessionID)
	if err != nil {
		return err
	}
	if payload == nil {
		return nil
	}
	return h.post(ctx, payload)
}

func (h *MemoraWritebackHook) loadPayload(ctx context.Context, tenantID, gwSessionID string) (*memoraSummaryPayload, error) {
	const query = `
		SELECT ss.session_key, ss.tenant_id,
		       COALESCE(NULLIF(sd.end_user_id, ''), NULLIF(sd.owner_user, '')) AS user_id,
		       ss.title, ss.summary, COALESCE(ss.key_topics, '{}'), ss.user_intent,
		       COALESCE(ss.work_types, '{}'), ss.quality_score, ss.primary_model,
		       ss.request_count, ss.total_tokens, ss.first_request_at, ss.last_request_at
		FROM session_summaries ss
		JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		WHERE ss.session_key = $1 AND ss.tenant_id = $2`

	var payload memoraSummaryPayload
	if err := h.db.QueryRow(ctx, query, gwSessionID, tenantID).Scan(
		&payload.SessionID, &payload.TenantID, &payload.UserID,
		&payload.Title, &payload.Summary, &payload.KeyTopics, &payload.UserIntent,
		&payload.WorkTypes, &payload.QualityScore, &payload.ModelUsed,
		&payload.TurnCount, &payload.TotalTokens, &payload.SessionStart, &payload.SessionEnd,
	); err != nil {
		return nil, fmt.Errorf("load memora summary payload: %w", err)
	}

	if strings.TrimSpace(payload.UserID) == "" || strings.TrimSpace(payload.Summary) == "" {
		h.logger.Warn("memora writeback skipped: missing owner or summary",
			"tenant_id", tenantID, "session_id", gwSessionID)
		return nil, nil
	}
	if strings.TrimSpace(payload.Title) == "" {
		payload.Title = "Untitled session"
	}
	payload.Metadata = map[string]any{
		"source": "llm-gateway-go",
		"schema": "session-summary/v1",
	}
	return &payload, nil
}

func (h *MemoraWritebackHook) post(ctx context.Context, payload *memoraSummaryPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal memora payload: %w", err)
	}

	url := strings.TrimRight(h.config.BaseURL, "/") + "/api/session/ingest-summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create memora request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.config.APIKey)

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("send memora request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("read memora response: %w", readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("memora returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return nil
}
