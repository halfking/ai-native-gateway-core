package bg

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// SelfCheckWorker runs periodic ping + tool-call smoke tests against the
// gateway to verify model availability and tool-call parsing correctness.
type SelfCheckWorker struct {
	db      *pgxpool.Pool
	apiKey  string
	baseURL string
	keyring *secret.Keyring
	client  *http.Client

	stopCh   chan struct{}
	stopOnce sync.Once

	triggerCh chan string

	cleanupCounter int

	faultModels map[string]time.Time
	faultMu     sync.RWMutex
}

// TriggerManualRun triggers a manual self-check run for the given model.
// Returns error if the trigger channel is full.
func (w *SelfCheckWorker) TriggerManualRun(model string) error {
	select {
	case <-w.stopCh:
		return fmt.Errorf("self-check worker is stopped")
	default:
	}
	select {
	case w.triggerCh <- model:
		return nil
	default:
		return fmt.Errorf("manual trigger channel is full, try again later")
	}
}

// selfCheckToolDef is the tool used by every self-check conversation.
var selfCheckToolDef = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "get_current_time",
		"description": "获取当前时间",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timezone": map[string]string{
					"type":        "string",
					"description": "时区，如 Asia/Shanghai",
				},
			},
			"required": []string{"timezone"},
		},
	},
}

// NewSelfCheckWorker creates a new SelfCheckWorker.
func NewSelfCheckWorker(db *pgxpool.Pool, apiKey, baseURL string, keyring *secret.Keyring) *SelfCheckWorker {
	if baseURL == "" {
		// Try env var first for flexible configuration
		if envURL := os.Getenv("LLM_GATEWAY_SELF_CHECK_BASE_URL"); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = "http://127.0.0.1:8781/v1"
		}
	}
	return &SelfCheckWorker{
		db:          db,
		apiKey:      apiKey,
		baseURL:     baseURL,
		keyring:     keyring,
		client:      &http.Client{Timeout: 30 * time.Second},
		stopCh:      make(chan struct{}),
		triggerCh:   make(chan string, 10),
		faultModels: make(map[string]time.Time),
	}
}

func (w *SelfCheckWorker) Start(ctx context.Context) {
	slog.Info("self_check_worker started")
	go func() {
		w.runOnce(ctx)

		// Load initial settings to compute the first ticker interval.
		s, err := w.loadSettings(ctx)
		if err != nil {
			slog.Error("self_check_worker: failed to load initial settings, using 1min fallback", "error", err)
			s = &scSettings{NormalInterval: 600, FaultInterval: 600}
		}

		tickerInterval := selfCheckTickerInterval(s)
		slog.Info("self_check_worker: using dynamic ticker interval",
			"interval_seconds", int(tickerInterval.Seconds()),
			"normal_interval_seconds", s.NormalInterval,
			"fault_interval_seconds", s.FaultInterval)

		ticker := time.NewTicker(tickerInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("self_check_worker stopping (ctx done)")
				return
			case <-w.stopCh:
				slog.Info("self_check_worker stopped")
				return
			case model := <-w.triggerCh:
				s, err := w.loadSettings(ctx)
				if err != nil {
					slog.Error("self_check_worker: manual trigger: load settings failed", "error", err)
					continue
				}
				if model != "" {
					w.runModel(ctx, model, s.MaxTokens)
					continue
				}
				if !s.Enabled {
					slog.Info("self_check_worker: manual trigger ignored because self-check is disabled")
					continue
				}
				models, err := w.selectModels(ctx, s)
				if err != nil {
					slog.Error("self_check_worker: manual trigger: select models failed", "error", err)
					continue
				}
				w.runModels(ctx, models, s.MaxTokens)
			case <-ticker.C:
				newSettings, err := w.loadSettings(ctx)
				if err != nil {
					slog.Error("self_check_worker: failed to reload settings", "error", err)
				} else if newInterval := selfCheckTickerInterval(newSettings); newInterval != tickerInterval {
					slog.Info("self_check_worker: adjusting ticker interval",
						"old_seconds", int(tickerInterval.Seconds()),
						"new_seconds", int(newInterval.Seconds()),
						"normal_interval_seconds", newSettings.NormalInterval,
						"fault_interval_seconds", newSettings.FaultInterval)
					ticker.Stop()
					ticker = time.NewTicker(newInterval)
					tickerInterval = newInterval
				}

				w.runOnce(ctx)
			}
		}
	}()
}

func selfCheckTickerInterval(s *scSettings) time.Duration {
	interval := s.NormalInterval
	if s.FaultInterval > 0 && (interval <= 0 || s.FaultInterval < interval) {
		interval = s.FaultInterval
	}
	if interval < 600 {
		interval = 600
	}
	return time.Duration(interval/10) * time.Second
}

func (w *SelfCheckWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// --------------------------------------------------------------------------
// Settings
// --------------------------------------------------------------------------

type scSettings struct {
	Enabled        bool
	NormalInterval int
	FaultInterval  int
	ModelSource    string
	MaxModels      int
	MaxTokens      int
	FeaturedModels []string
}

func (w *SelfCheckWorker) loadSettings(ctx context.Context) (*scSettings, error) {
	s := &scSettings{}
	var rawModels []byte
	err := w.db.QueryRow(ctx, `
		SELECT enabled, normal_interval_seconds, fault_interval_seconds,
		       model_source, max_models, max_tokens_per_run, featured_model_ids
		FROM self_check_settings WHERE id=1`,
	).Scan(&s.Enabled, &s.NormalInterval, &s.FaultInterval,
		&s.ModelSource, &s.MaxModels, &s.MaxTokens, &rawModels)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Seed the single settings row with defaults so the worker can run
			// even before the admin panel has been opened (migration seed may
			// not have run). Retry the query once after seeding.
			if seedErr := w.ensureDefaultSettings(ctx); seedErr != nil {
				return nil, seedErr
			}
			err = w.db.QueryRow(ctx, `
				SELECT enabled, normal_interval_seconds, fault_interval_seconds,
				       model_source, max_models, max_tokens_per_run, featured_model_ids
				FROM self_check_settings WHERE id=1`,
			).Scan(&s.Enabled, &s.NormalInterval, &s.FaultInterval,
				&s.ModelSource, &s.MaxModels, &s.MaxTokens, &rawModels)
		}
		if err != nil {
			return nil, err
		}
	}
	json.Unmarshal(rawModels, &s.FeaturedModels)
	return s, nil
}

// ensureDefaultSettings inserts the default single-row settings if missing.
func (w *SelfCheckWorker) ensureDefaultSettings(ctx context.Context) error {
	_, err := w.db.Exec(ctx, `
		INSERT INTO self_check_settings (id, featured_model_ids)
		VALUES (1, $1::jsonb)
		ON CONFLICT (id) DO NOTHING`,
		`["minimax-m2.7","glm-5.2","mimo-v2.5","claude-sonnet-5","gpt-5.4","gpt-5.6-luna","deepseek-v4-pro"]`)
	return err
}

// --------------------------------------------------------------------------
// Core loop
// --------------------------------------------------------------------------

func (w *SelfCheckWorker) runOnce(ctx context.Context) {
	w.cleanupCounter++
	if w.cleanupCounter >= 60 {
		w.cleanupCounter = 0
		w.cleanupOldRecords(ctx)
	}

	s, err := w.loadSettings(ctx)
	if err != nil {
		slog.Error("self_check_worker: load settings failed", "error", err)
		return
	}
	if !s.Enabled {
		return
	}
	models, err := w.selectModels(ctx, s)
	if err != nil {
		slog.Error("self_check_worker: select models failed", "error", err)
		return
	}
	if len(models) == 0 {
		return
	}

	now := time.Now()
	var toTest []string
	for _, m := range models {
		interval := time.Duration(s.NormalInterval) * time.Second
		w.faultMu.RLock()
		if _, faulted := w.faultModels[m]; faulted {
			interval = time.Duration(s.FaultInterval) * time.Second
		}
		w.faultMu.RUnlock()
		var lastRun *time.Time
		err := w.db.QueryRow(ctx,
			`SELECT started_at FROM self_check_runs WHERE model_name=$1 ORDER BY started_at DESC LIMIT 1`, m,
		).Scan(&lastRun)
		if err == nil && lastRun != nil && now.Sub(*lastRun) < interval {
			continue
		}
		toTest = append(toTest, m)
	}
	if len(toTest) == 0 {
		return
	}

	w.runModels(ctx, toTest, s.MaxTokens)
}

func (w *SelfCheckWorker) runModels(ctx context.Context, models []string, maxTokens int) {
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, model := range models {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			w.runModel(ctx, m, maxTokens)
		}(model)
	}
	wg.Wait()
}

func (w *SelfCheckWorker) cleanupOldRecords(ctx context.Context) {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	tag, err := w.db.Exec(ctx, `DELETE FROM self_check_runs WHERE started_at < $1`, cutoff)
	if err != nil {
		slog.Error("self_check_worker: cleanup failed", "error", err)
		return
	}
	n := tag.RowsAffected()
	if n > 0 {
		slog.Info("self_check_worker: cleaned up old records", "deleted_runs", n)
	}
}

// --------------------------------------------------------------------------
// Model selection
// --------------------------------------------------------------------------

func (w *SelfCheckWorker) selectModels(ctx context.Context, s *scSettings) ([]string, error) {
	modelSet := make(map[string]struct{})
	if s.ModelSource == "featured" || s.ModelSource == "both" {
		for _, m := range s.FeaturedModels {
			modelSet[m] = struct{}{}
		}
	}
	if s.ModelSource == "top10" || s.ModelSource == "both" {
		top, err := w.topNModels(ctx, s.MaxModels)
		if err != nil {
			return nil, err
		}
		for _, m := range top {
			modelSet[m] = struct{}{}
		}
	}
	if len(modelSet) > s.MaxModels {
		trimmed := make(map[string]struct{})
		for _, m := range s.FeaturedModels {
			if len(trimmed) >= s.MaxModels {
				break
			}
			if _, ok := modelSet[m]; ok {
				trimmed[m] = struct{}{}
			}
		}
		for m := range modelSet {
			if len(trimmed) >= s.MaxModels {
				break
			}
			if _, ok := trimmed[m]; !ok {
				trimmed[m] = struct{}{}
			}
		}
		modelSet = trimmed
	}
	var out []string
	for m := range modelSet {
		out = append(out, m)
	}
	return out, nil
}

func (w *SelfCheckWorker) topNModels(ctx context.Context, n int) ([]string, error) {
	rows, err := w.db.Query(ctx, `
		SELECT pm.raw_model_name
		FROM provider_models pm
		JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
		WHERE cmb.available = TRUE
		  AND EXISTS (
		    SELECT 1
		    FROM v_routable_credential_models v
		    WHERE v.binding_id = cmb.id
		      AND v.is_routable = TRUE
		  )
		GROUP BY pm.raw_model_name
		ORDER BY COUNT(*) DESC
		LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// --------------------------------------------------------------------------
// Single model test
// --------------------------------------------------------------------------

type roundResult struct {
	Success     bool
	HadToolCall bool
	Tokens      int
	LatencyMs   int
	HTTPCode    int
	ErrType     string
	ErrDetail   string
	ReqBody     string
	RespPreview string
}

func (w *SelfCheckWorker) runModel(ctx context.Context, model string, maxTokens int) {
	startedAt := time.Now()
	runID, err := w.insertRun(ctx, model, startedAt)
	if err != nil {
		slog.Error("self_check_worker: insert run failed", "model", model, "error", err)
		return
	}

	rounds := 0
	successRounds := 0
	hadToolCall := false
	var lastErrType, lastErrDetail string
	var totalTokens, totalLatency int

	// Round 0: ping
	r0 := w.doPing(ctx, model)
	if err := w.insertRound(ctx, runID, 0, r0); err == nil {
		rounds++
		if r0.Success {
			successRounds++
		} else {
			lastErrType = r0.ErrType
			lastErrDetail = r0.ErrDetail
		}
		totalTokens += r0.Tokens
		totalLatency += r0.LatencyMs
	} else {
		slog.Error("self_check_worker: insert round 0 failed", "model", model, "error", err)
	}

	// Rounds 1-3: conversation with tool
	if r0.Success {
		for i := 1; i <= 3; i++ {
			r := w.doConversationRound(ctx, model, i, maxTokens)
			if err := w.insertRound(ctx, runID, i, r); err == nil {
				rounds++
				if r.Success {
					successRounds++
				} else {
					lastErrType = r.ErrType
					lastErrDetail = r.ErrDetail
				}
				if r.HadToolCall {
					hadToolCall = true
				}
				totalTokens += r.Tokens
				totalLatency += r.LatencyMs
			} else {
				slog.Error("self_check_worker: insert round failed", "model", model, "round", i, "error", err)
			}
			if !r.Success {
				break
			}
		}
	}

	completedAt := time.Now()
	durationMs := int(completedAt.Sub(startedAt).Milliseconds())
	status := "success"
	if successRounds == 0 {
		status = "failed"
	} else if successRounds < rounds {
		status = "partial"
	}
	avgLatency := 0
	if rounds > 0 {
		avgLatency = totalLatency / rounds
	}

	// Fault isolation on failure.
	var upstreamResult, upstreamError *string
	var upstreamLatency *int
	upstreamTested := false
	if status == "failed" || status == "partial" {
		upstreamTested = true
		uRes, uLat, uErr := w.isolateUpstream(ctx, model)
		upstreamResult = &uRes
		upstreamLatency = &uLat
		if uErr != "" {
			upstreamError = &uErr
		}
		w.faultMu.Lock()
		if _, ok := w.faultModels[model]; !ok {
			w.faultModels[model] = time.Now()
		}
		w.faultMu.Unlock()
	} else {
		w.faultMu.Lock()
		delete(w.faultModels, model)
		w.faultMu.Unlock()
	}

	if lastErrType == "" {
		lastErrType = "none"
	}
	w.updateRun(ctx, runID, completedAt, durationMs, status, rounds, successRounds,
		hadToolCall, totalTokens, avgLatency, lastErrType, lastErrDetail,
		upstreamTested, upstreamResult, upstreamLatency, upstreamError)

	slog.Info("self_check_worker: run completed",
		"model", model, "status", status,
		"rounds", fmt.Sprintf("%d/%d", successRounds, rounds),
		"latency_ms", avgLatency, "tokens", totalTokens)
}

// --------------------------------------------------------------------------
// Round execution
// --------------------------------------------------------------------------

func (w *SelfCheckWorker) doPing(ctx context.Context, model string) roundResult {
	body := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 10,
	}
	reqBody, _ := json.Marshal(body)
	return w.doRequest(ctx, model, string(reqBody), false)
}

func (w *SelfCheckWorker) doConversationRound(ctx context.Context, model string, roundIdx int, maxTokens int) roundResult {
	var messages []map[string]any

	switch roundIdx {
	case 1:
		messages = []map[string]any{
			{"role": "user", "content": "请用 get_current_time 工具查询当前北京时间。只调用工具，不要输出其他内容。"},
		}
	case 2:
		messages = []map[string]any{
			{"role": "user", "content": "请用 get_current_time 工具查询当前北京时间。只调用工具，不要输出其他内容。"},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{
						"id":   "call_selfcheck_001",
						"type": "function",
						"function": map[string]any{
							"name":      "get_current_time",
							"arguments": `{"timezone":"Asia/Shanghai"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_selfcheck_001",
				"content":      time.Now().Format("2006-01-02T15:04:05+08:00"),
			},
		}
	case 3:
		messages = []map[string]any{
			{"role": "user", "content": "用一个词形容你刚才的操作。"},
		}
	}

	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"tools":      []map[string]any{selfCheckToolDef},
	}
	reqBody, _ := json.Marshal(body)
	return w.doRequest(ctx, model, string(reqBody), roundIdx == 1)
}

func (w *SelfCheckWorker) doRequest(ctx context.Context, model, reqBody string, expectToolCall bool) roundResult {
	r := roundResult{ReqBody: truncateStrSC(reqBody, 4096)}

	req, err := http.NewRequestWithContext(ctx, "POST", w.baseURL+"/chat/completions",
		strings.NewReader(reqBody))
	if err != nil {
		r.ErrType = "internal"
		r.ErrDetail = err.Error()
		return r
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := w.client.Do(req)
	r.LatencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		r.HTTPCode = 0
		if ctx.Err() != nil {
			r.ErrType = "timeout"
			r.ErrDetail = "context cancelled"
		} else {
			r.ErrType = "http_000"
			r.ErrDetail = err.Error()
		}
		return r
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	r.RespPreview = truncateStrSC(string(bodyBytes), 500)
	r.HTTPCode = resp.StatusCode

	if resp.StatusCode != 200 {
		r.ErrType = fmt.Sprintf("http_%d", resp.StatusCode)
		r.ErrDetail = truncateStrSC(string(bodyBytes), 200)
		return r
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		r.ErrType = "parse_error"
		r.ErrDetail = "json unmarshal failed: " + err.Error()
		return r
	}
	if len(chatResp.Choices) == 0 {
		r.ErrType = "empty_response"
		r.ErrDetail = "no choices in response"
		return r
	}

	r.Success = true
	r.Tokens = chatResp.Usage.TotalTokens
	if len(chatResp.Choices[0].Message.ToolCalls) > 0 {
		r.HadToolCall = true
	}
	if expectToolCall && !r.HadToolCall {
		slog.Info("self_check_worker: expected tool call but model did not call",
			"model", model, "content_preview", truncateStrSC(chatResp.Choices[0].Message.Content, 100))
	}

	return r
}

// --------------------------------------------------------------------------
// Upstream isolation
// --------------------------------------------------------------------------

func (w *SelfCheckWorker) isolateUpstream(ctx context.Context, model string) (result string, latencyMs int, errMsg string) {
	var secretCiphertext []byte
	var baseURL string
	err := w.db.QueryRow(ctx, `
		SELECT c.secret_ciphertext, p.base_url
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE pm.raw_model_name = $1
		  AND c.status = 'active' AND c.lifecycle_status = 'active'
		  AND cmb.available = TRUE
		LIMIT 1`, model).Scan(&secretCiphertext, &baseURL)
	if err != nil {
		return "no_credential", 0, "no routable credential found"
	}

	plainKey, err := w.decryptCredential(secretCiphertext)
	if err != nil {
		return "no_credential", 0, "decrypt failed: " + err.Error()
	}

	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}

	pingBody := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"ping"}],"max_tokens":10}`, model)
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions",
		strings.NewReader(pingBody))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := w.client.Do(req)
	latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		if ctx.Err() != nil {
			return "timeout", latencyMs, "context cancelled"
		}
		return "timeout", latencyMs, err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == 200 {
		return "success", latencyMs, ""
	}
	return "failed", latencyMs, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStrSC(string(respBody), 200))
}

func (w *SelfCheckWorker) decryptCredential(ciphertext []byte) (string, error) {
	s := string(ciphertext)
	if secret.IsV1Envelope(s) {
		if w.keyring == nil {
			return "", fmt.Errorf("AES-GCM keyring not configured")
		}
		pt, err := secret.DecryptAESGCM(ciphertext, w.keyring)
		return string(pt), err
	}
	return "", fmt.Errorf("unsupported encryption format (not v1 AES-GCM envelope)")
}

// --------------------------------------------------------------------------
// DB helpers
// --------------------------------------------------------------------------

func (w *SelfCheckWorker) insertRun(ctx context.Context, model string, startedAt time.Time) (int64, error) {
	var id int64
	err := w.db.QueryRow(ctx, `
		INSERT INTO self_check_runs (model_name, started_at, status, tenant_id)
		VALUES ($1, $2, 'running', 'default')
		RETURNING id`, model, startedAt).Scan(&id)
	return id, err
}

func (w *SelfCheckWorker) updateRun(ctx context.Context, runID int64, completedAt time.Time,
	durationMs int, status string, roundsTotal, roundsSuccess int, hadToolCall bool,
	totalTokens, avgLatency int, errType, errDetail string,
	upstreamTested bool, upstreamResult *string, upstreamLatency *int, upstreamError *string) {
	if _, err := w.db.Exec(ctx, `
		UPDATE self_check_runs SET
			completed_at=$2, duration_ms=$3, status=$4,
			rounds_total=$5, rounds_success=$6, had_tool_call=$7,
			total_tokens=$8, avg_latency_ms=$9,
			error_type=$10, error_detail=$11,
			upstream_tested=$12, upstream_result=$13,
			upstream_latency_ms=$14, upstream_error=$15
		WHERE id=$1`,
		runID, completedAt, durationMs, status,
		roundsTotal, roundsSuccess, hadToolCall,
		totalTokens, avgLatency,
		errType, errDetail,
		upstreamTested, upstreamResult, upstreamLatency, upstreamError); err != nil {
		slog.Warn("self_check: updateRun failed", "run_id", runID, "error", err)
	}
}

func (w *SelfCheckWorker) insertRound(ctx context.Context, runID int64, roundIdx int, r roundResult) error {
	_, err := w.db.Exec(ctx, `
		INSERT INTO self_check_round_results
			(run_id, round_index, is_ping, is_tool_call, latency_ms,
			 prompt_tokens, completion_tokens, total_tokens,
			 success, http_code, error_message, request_body, response_preview)
		VALUES ($1,$2,$3,$4,$5,0,0,$6,$7,$8,$9,$10,$11)`,
		runID, roundIdx, roundIdx == 0, r.HadToolCall, r.LatencyMs,
		r.Tokens, r.Success, r.HTTPCode,
		r.ErrDetail, r.ReqBody, r.RespPreview)
	return err
}

// truncateStrSC is a package-local helper (avoids conflict with model_probe.go's truncate).
func truncateStrSC(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// --------------------------------------------------------------------------
// System API key management
// --------------------------------------------------------------------------

// EnsureSystemAPIKey gets or creates a system-level API key for the self-check worker.
// secretKey is the gateway HMAC secret (cfg.SecretKey) and MUST be the same one used by
// the data-plane verifier (domains/authentication), otherwise the generated key will fail
// verification with "invalid_key". See self_check_worker.go's audit for the root cause.
func EnsureSystemAPIKey(ctx context.Context, db *pgxpool.Pool, encKey []byte, keyring *secret.Keyring, secretKey string) (string, error) {
	// Try to find an existing system key that belongs to this worker.
	var ciphertext []byte
	err := db.QueryRow(ctx, `
		SELECT key_ciphertext FROM api_keys
		WHERE COALESCE(is_system, FALSE) = TRUE AND status = 'active'
		  AND owner_user = 'self-check-worker'
		ORDER BY created_at DESC LIMIT 1`,
	).Scan(&ciphertext)
	if err == nil && len(ciphertext) > 0 {
		if keyring != nil {
			pt, err := secret.DecryptAESGCM(ciphertext, keyring)
			if err == nil {
				return string(pt), nil
			}
		}
		if len(encKey) == 32 {
			pt, err := secret.DecryptFernet(ciphertext, encKey)
			if err == nil {
				return pt, nil
			}
		}
		slog.Warn("self_check_worker: existing system key exists but cannot decrypt, creating new one")
	}

	// Generate a new system key.
	newKey := fmt.Sprintf("sk-selfcheck-%s", randomHexSC(24))
	// CRITICAL: key_hash must be HMAC-SHA256(secretKey, raw) — the same transform the
	// data-plane verifier uses at lookup time. Storing the plaintext (as the old code did)
	// means the verifier's WHERE key_hash = HMAC(...) never matches → 401 invalid_key.
	keyHash := authentication.HashAPIKey(secretKey, newKey)
	keyPrefix := newKey[:10] + "****"

	var encCiphertext string
	if keyring != nil {
		var err error
		encCiphertext, err = secret.EncryptAESGCM([]byte(newKey), keyring)
		if err != nil {
			return "", fmt.Errorf("encrypt new system key: %w", err)
		}
	} else if len(encKey) == 32 {
		enc, err := secret.EncryptFernet([]byte(newKey), encKey)
		if err != nil {
			return "", fmt.Errorf("encrypt new system key (fernet): %w", err)
		}
		encCiphertext = string(enc)
	} else {
		return "", fmt.Errorf("no encryption key available for system api key")
	}

	_, err = db.Exec(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix,
			owner_user, status, is_system, key_ciphertext, remark)
		VALUES (0, 'default', $1, $2, 'self-check-worker', 'active', TRUE, $3, 'Auto-generated system key for self-check')
		ON CONFLICT (key_hash) DO NOTHING`,
		keyHash, keyPrefix, encCiphertext)
	if err != nil {
		return "", fmt.Errorf("insert system api key: %w", err)
	}
	return newKey, nil
}

func randomHexSC(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // Use crypto/rand for security
	for i := range b {
		b[i] = "0123456789abcdef"[b[i]%16]
	}
	return string(b)
}

// EnsureSystemAPIKeyFromEnv reads the system API key from env var (no DB interaction).
func EnsureSystemAPIKeyFromEnv() string {
	return os.Getenv("LLM_GATEWAY_SELF_CHECK_API_KEY")
}
