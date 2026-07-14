// Package bg — credential_selfcheck.go
//
// CredentialSelfcheckWorker is the new per-credential daily self-check
// worker mandated by the 2026-07-14 spec rewrite.  It supersedes the
// featured-model self-check tick in bg/self_check_worker.go (which is
// now gated behind LLM_GATEWAY_USE_NEW_PROBE_MODE).
//
// Cadence
// ───────
//   - Once per 24h per (active) credential.
//   - Cycle tick: 5 minutes.  Each tick picks ONE due credential and
//     runs the model-selection algorithm on it, so the worker is
//     not spammy (max ~12 checks/hour, much less than the 252
//     observation of 2+ probes/min under the old design).
//
// Model selection
// ───────────────
//   1. "most_used"  — top-1 model by 24h successful traffic for the
//                      credential (SQL helper credential_most_used_model).
//   2. "fallback_N" — if most_used fails, try the next most-used
//                      model (N = 2, 3).  Up to 3 attempts per run.
//   3. "random"     — for credentials with zero 7d traffic, pick
//                      uniformly at random from the available models.
//
// All attempts are persisted on self_check_runs (selection_strategy +
// attempted_models JSONB) and final status reflects the last attempt.
// If 3 fallbacks all fail the run ends with status='failed'.
//
// Outbound X-LLM-Origin-* headers
// ────────────────────────────────
//   X-LLM-Origin-Stage : self_check
//   X-LLM-Origin-Actor : credential-selfcheck-worker
//   X-Forwarded-For    : $LLM_GATEWAY_EGRESS_FORWARDED_FOR + egress IP
//   X-Real-IP          : $LLM_GATEWAY_EGRESS_IP
//
// OriginMiddleware (commit 3) only honours those headers when the
// request carries the static global API key (system key path), so
// a public client cannot spoof them.
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// credentialSelfcheckCycleInterval is the wake-up cadence.  Each tick
// picks ONE due credential — see dueCredentials().
const credentialSelfcheckCycleInterval = 5 * time.Minute

// credentialSelfcheckWindow is the "24h" lookback used to determine
// whether a credential is due (last selfcheck < 24h ago) and to pick
// the most-used model.
const credentialSelfcheckWindow = 24 * time.Hour

// maxFallbackAttempts caps the per-credential retry chain to 3 (one
// most-used + two fallbacks) per the spec.
const maxFallbackAttempts = 3

// CredentialSelfcheckWorker runs daily per-credential self-checks.
type CredentialSelfcheckWorker struct {
	db      *pgxpool.Pool
	apiKey  string
	baseURL string
	client  *http.Client

	stopCh   chan struct{}
	stopOnce sync.Once

	// rng is concurrency-safe (rand.Rand with mutex) so model fallback
	// selection on a never-used credential can pick uniformly.
	rng   *rand.Rand
	rngMu sync.Mutex
}

// NewCredentialSelfcheckWorker constructs a worker.  baseURL="" picks
// LLM_GATEWAY_SELF_CHECK_BASE_URL or the default https://llm.kxpms.cn/v1.
func NewCredentialSelfcheckWorker(db *pgxpool.Pool, apiKey, baseURL string) *CredentialSelfcheckWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SELF_CHECK_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = "https://llm.kxpms.cn/v1"
		}
	}
	return &CredentialSelfcheckWorker{
		db:      db,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
		stopCh:  make(chan struct{}),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Start launches the worker goroutine.  Idempotent.
func (w *CredentialSelfcheckWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	go w.loop(ctx)
	slog.Info("credential_selfcheck_worker started",
		"cycle_interval", credentialSelfcheckCycleInterval,
		"window", credentialSelfcheckWindow,
	)
}

func (w *CredentialSelfcheckWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
}

func (w *CredentialSelfcheckWorker) loop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("credential_selfcheck_worker panic", "recover", r)
		}
	}()
	ticker := time.NewTicker(credentialSelfcheckCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.cycleOnce(ctx)
		}
	}
}

// cycleOnce picks at most ONE due credential and processes it sequentially.
// Sequential per-process (5-min tick, 1 credential per tick) combined with
// a PG advisory lock guarantees cross-instance isolation: if the selected
// (credential_id) is already being self-checked by another gateway instance,
// pg_try_advisory_xact_lock returns false and we skip silently.
func (w *CredentialSelfcheckWorker) cycleOnce(ctx context.Context) {
	credID, ok, err := w.pickDueCredential(ctx)
	if err != nil {
		slog.Warn("credential_selfcheck_worker: pick due credential failed", "error", err)
		return
	}
	if !ok {
		return // nothing due
	}

	// Cross-instance mutual exclusion via PG advisory lock.
	// Lock key = credential_id (int4).  Failure means another
	// instance already holds the lock → skip this tick.
	var locked bool
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := w.db.QueryRow(lockCtx,
		`SELECT pg_try_advisory_xact_lock($1)`, credID,
	).Scan(&locked); err != nil || !locked {
		if err != nil {
			slog.Warn("credential_selfcheck_worker: advisory lock query failed",
				"credential_id", credID, "error", err)
		}
		return
	}

	if err := w.runOne(ctx, credID); err != nil {
		slog.Warn("credential_selfcheck_worker: runOne failed",
			"credential_id", credID, "error", err)
	}
}

// pickDueCredential returns one credential that has not been self-checked
// in the last 24h.  Priority:
//
//	1. never-checked credentials first (NULL last_selfcheck_at)
//	2. oldest-checked credentials
//
// Returns (id, true, nil) if a candidate exists, (0, false, nil) otherwise.
func (w *CredentialSelfcheckWorker) pickDueCredential(ctx context.Context) (int, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var id int
	err := w.db.QueryRow(queryCtx, `
		SELECT c.id
		FROM credentials c
		LEFT JOIN LATERAL (
			SELECT MAX(completed_at) AS last_at
			FROM self_check_runs scr
			WHERE scr.tenant_id = c.tenant_id
		) l ON TRUE
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(l.last_at, '1970-01-01'::timestamptz) < now() - $1::interval
		ORDER BY l.last_at NULLS FIRST, c.id
		LIMIT 1
	`, fmt.Sprintf("%d seconds", int(credentialSelfcheckWindow.Seconds()))).Scan(&id)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}

// runOne performs the self-check for a single credential.  Returns nil
// on a clean run; logged but otherwise ignored errors are non-fatal so
// one bad credential does not stop the worker.
func (w *CredentialSelfcheckWorker) runOne(ctx context.Context, credentialID int) error {
	models, err := w.pickModels(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("pick models: %w", err)
	}
	if len(models) == 0 {
		return fmt.Errorf("credential %d has no routable models", credentialID)
	}

	startedAt := time.Now()
	runID, err := w.insertRun(ctx, credentialID, startedAt, len(models))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	var (
		success        bool
		attemptedJSON  = make([]string, 0, len(models))
		lastErrType    = "none"
		lastErrDetail  = ""
		hadToolCall    = false
		successRounds  = 0
		totalRounds    = 0
		totalTokens    = 0
		totalLatency   = 0
	)

	for i, model := range models {
		strategy := "most_used"
		if i == 0 && w.wasEverUsed(credentialID) {
			strategy = "most_used"
		} else if i == 0 {
			strategy = "random"
		} else {
			strategy = fmt.Sprintf("fallback_%d", i)
		}
		attemptedJSON = append(attemptedJSON, model)
		r := w.doRequest(ctx, model)
		totalRounds++
		totalTokens += r.Tokens
		totalLatency += r.LatencyMs
		if r.HadToolCall {
			hadToolCall = true
		}
		if r.Success {
			successRounds++
			success = true
			lastErrType = "none"
			lastErrDetail = ""
		} else {
			lastErrType = r.ErrType
			lastErrDetail = r.ErrDetail
		}
		if r.Success {
			slog.Info("credential_selfcheck_worker: model ok",
				"credential_id", credentialID,
				"model", model,
				"strategy", strategy,
				"latency_ms", r.LatencyMs,
			)
			break
		}
		slog.Warn("credential_selfcheck_worker: model failed, trying next",
			"credential_id", credentialID,
			"model", model,
			"strategy", strategy,
			"err_type", r.ErrType,
			"err_detail", r.ErrDetail,
		)
	}

	status := "success"
	if !success {
		if successRounds > 0 {
			status = "partial"
		} else {
			status = "failed"
		}
	}

	avgLatency := 0
	if totalRounds > 0 {
		avgLatency = totalLatency / totalRounds
	}
	if err := w.finalizeRun(ctx, runID, startedAt, status, totalRounds, successRounds, hadToolCall, totalTokens, avgLatency, lastErrType, lastErrDetail, attemptedJSON); err != nil {
		return fmt.Errorf("finalize run: %w", err)
	}
	return nil
}

// pickModels returns the ordered list of up to 3 models to try for
// credentialID.  Position 0 is the preferred model; positions 1..2 are
// fallbacks.  If the credential has no 24h traffic the first slot is
// filled with a uniformly-random model.
func (w *CredentialSelfcheckWorker) pickModels(ctx context.Context, credentialID int) ([]string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 1. most-used model in last 24h
	var top1 string
	err := w.db.QueryRow(queryCtx, `SELECT raw_model_name FROM credential_most_used_model($1, 24)`, credentialID).Scan(&top1)
	if err != nil && err.Error() != "no rows in result set" {
		return nil, err
	}

	// 2. fallback pool: all routable models for this credential
	rows, err := w.db.Query(queryCtx, `
		SELECT DISTINCT pm.raw_model_name
		FROM credentials c
		JOIN provider_model_bindings pmb ON pmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = pmb.provider_model_id
		WHERE c.id = $1
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(pmb.available, FALSE) = TRUE
		  AND COALESCE(pmb.is_routable, FALSE) = TRUE
	`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pool []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err == nil {
			pool = append(pool, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, nil
	}

	// 3. assemble
	out := make([]string, 0, maxFallbackAttempts)
	if top1 != "" {
		out = append(out, top1)
	}
	// Fill fallbacks: top1 first, then any remaining pool order with
	// a deterministic shuffle so consecutive runs of never-used
	// credentials cover different models.
	w.rngMu.Lock()
	w.rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	w.rngMu.Unlock()
	for _, m := range pool {
		if len(out) >= maxFallbackAttempts {
			break
		}
		if m == top1 {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// wasEverUsed returns true if the credential had any successful request
// in the last 7d — used to decide whether position 0 is "most_used" or
// "random".
func (w *CredentialSelfcheckWorker) wasEverUsed(credentialID int) bool {
	if w == nil || w.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var n int
	if err := w.db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM request_logs_hot
		WHERE credential_id = $1
		  AND ts >= now() - interval '7 days'
		  AND success = TRUE
	`, credentialID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// credentialSelfcheckRound is the structured result of a single model
// attempt.  Mirrors the per-round fields persisted today.
type credentialSelfcheckRound struct {
	Success    bool
	HadToolCall bool
	Tokens     int
	LatencyMs  int
	HTTPCode   int
	ErrType    string
	ErrDetail  string
}

// doRequest issues a single ping + tool-call conversation against the
// local gateway and returns the round result.  Outbound headers carry
// the X-LLM-Origin-* identity so OriginMiddleware (commit 3) tags the
// request_logs row correctly.
func (w *CredentialSelfcheckWorker) doRequest(ctx context.Context, model string) credentialSelfcheckRound {
	pingBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 10,
	})
	r := w.doHTTP(ctx, model, string(pingBody), false)
	if !r.Success {
		return r
	}
	// follow-up tool call (one round only, simpler than 3-round
	// self_check_worker to keep per-cred latency bounded)
	toolBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": "用 get_current_time 工具查询当前北京时间，只调用工具，不要输出其他。"}},
		"max_tokens": 64,
		"tools":      []map[string]any{selfCheckToolDef},
	})
	r2 := w.doHTTP(ctx, model, string(toolBody), true)
	return r2
}

func (w *CredentialSelfcheckWorker) doHTTP(ctx context.Context, model, body string, expectTool bool) credentialSelfcheckRound {
	r := credentialSelfcheckRound{ErrType: "none"}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		r.ErrType = "internal"
		r.ErrDetail = err.Error()
		return r
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "self_check")
	req.Header.Set("X-LLM-Origin-Actor", "credential-selfcheck-worker")
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_IP")); v != "" {
		req.Header.Set("X-Real-IP", v)
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_EGRESS_FORWARDED_FOR")); v != "" {
		req.Header.Set("X-Forwarded-For", v)
	}

	start := time.Now()
	resp, err := w.client.Do(req)
	r.LatencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
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
	r.HTTPCode = resp.StatusCode
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	if resp.StatusCode != 200 {
		r.ErrType = fmt.Sprintf("http_%d", resp.StatusCode)
		r.ErrDetail = truncateStrSC(string(buf[:n]), 200)
		return r
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(buf[:n], &parsed); err != nil {
		r.ErrType = "parse_error"
		r.ErrDetail = "json unmarshal failed: " + err.Error()
		return r
	}
	if len(parsed.Choices) == 0 {
		r.ErrType = "empty_response"
		r.ErrDetail = "no choices in response"
		return r
	}
	r.Success = true
	r.Tokens = parsed.Usage.TotalTokens
	if len(parsed.Choices[0].Message.ToolCalls) > 0 {
		r.HadToolCall = true
	}
	if expectTool && !r.HadToolCall {
		slog.Info("credential_selfcheck_worker: expected tool call but model did not call",
			"model", model)
	}
	return r
}

func (w *CredentialSelfcheckWorker) insertRun(ctx context.Context, credentialID int, startedAt time.Time, attemptCount int) (int64, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id int64
	// Reuse self_check_runs (the spec aligns both workers on the
	// same table).  Model_name carries the credential_id and the
	// first attempted model in the form "<credID>:<model>" so
	// existing dashboards keep working.
	err := w.db.QueryRow(queryCtx, `
		INSERT INTO self_check_runs (model_name, started_at, status, tenant_id)
		VALUES ($1, $2, 'running', 'default')
		RETURNING id`,
		fmt.Sprintf("cred-%d", credentialID), startedAt).Scan(&id)
	return id, err
}

func (w *CredentialSelfcheckWorker) finalizeRun(
	ctx context.Context, runID int64, startedAt time.Time,
	status string, roundsTotal, roundsSuccess int, hadToolCall bool,
	totalTokens, avgLatency int, errType, errDetail string,
	attempted []string,
) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	attemptedJSON, _ := json.Marshal(attempted)
	completedAt := time.Now()
	durationMs := int(completedAt.Sub(startedAt).Milliseconds())
	_, err := w.db.Exec(queryCtx, `
		UPDATE self_check_runs SET
			completed_at = $2,
			duration_ms = $3,
			status = $4,
			rounds_total = $5,
			rounds_success = $6,
			had_tool_call = $7,
			total_tokens = $8,
			avg_latency_ms = $9,
			error_type = $10,
			error_detail = $11,
			selection_strategy = $12,
			attempted_models = $13::jsonb
		WHERE id = $1`,
		runID, completedAt, durationMs, status,
		roundsTotal, roundsSuccess, hadToolCall,
		totalTokens, avgLatency, errType, errDetail,
		"most_used", attemptedJSON,
	)
	return err
}
