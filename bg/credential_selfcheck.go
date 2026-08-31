// Package bg — credential_selfcheck.go
//
// CredentialSelfcheckWorker is the new per-credential daily self-check
// worker mandated by the 2026-07-14 spec rewrite. It uses one tenant-scoped
// featured/recent-model primary and only due automatic failed bindings as
// recovery follow-ups.
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/loopback"
	"github.com/kaixuan/llm-gateway-go/recentmodels"
	"github.com/redis/go-redis/v9"
)

const credentialSelfcheckCycleInterval = 5 * time.Minute
const credentialSelfcheckWindow = 24 * time.Hour

type credentialSelfcheckDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type credentialSelfcheckConn interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Release(discard bool)
}

type credentialSelfcheckConnPool interface {
	Acquire(ctx context.Context) (credentialSelfcheckConn, error)
}

type pgxCredentialSelfcheckPool struct{ pool *pgxpool.Pool }

func (p pgxCredentialSelfcheckPool) Acquire(ctx context.Context) (credentialSelfcheckConn, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return pgxCredentialSelfcheckConn{Conn: conn}, nil
}

type pgxCredentialSelfcheckConn struct{ *pgxpool.Conn }

func (c pgxCredentialSelfcheckConn) Release(discard bool) {
	if discard {
		conn := c.Hijack()
		_ = conn.Close(context.Background())
		return
	}
	c.Conn.Release()
}

// CredentialSelfcheckWorker handles daily checks for credentials with recent
// failures. Redis is optional; a seven-day database fallback preserves the
// same business-request source when Redis is unavailable.
type CredentialSelfcheckWorker struct {
	db       credentialSelfcheckDB
	connPool credentialSelfcheckConnPool
	apiKey   string
	baseURL  string
	client   *http.Client

	stopCh      chan struct{}
	stopOnce    sync.Once
	startOnce   sync.Once
	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	probeSink   ProbeEventSink
	redis       *redis.Client
}

func (w *CredentialSelfcheckWorker) SetProbeSink(sink ProbeEventSink) {
	if w != nil {
		w.probeSink = sink
	}
}

// SetRedisClient wires the shared tenant-scoped seven-day usage ranking.
func (w *CredentialSelfcheckWorker) SetRedisClient(client *redis.Client) {
	if w != nil {
		w.redis = client
	}
}

func (w *CredentialSelfcheckWorker) publishSelfcheck(credentialID int, runID int64, status string) {
	if w == nil || w.probeSink == nil {
		return
	}
	w.probeSink.PublishProbeEvent(ProbeStreamEvent{
		ID:           fmt.Sprintf("selfcheck:%d:%d", credentialID, runID),
		TaskType:     "selfcheck",
		Source:       "selfcheck",
		Status:       status,
		CredentialID: int64(credentialID),
		Scheduled:    true,
		Reason:       "daily_selfcheck",
		TimestampMs:  time.Now().UnixMilli(),
	})
}

func NewCredentialSelfcheckWorker(db *pgxpool.Pool, apiKey, baseURL string) *CredentialSelfcheckWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SELF_CHECK_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = loopback.GatewayBase() + "/v1"
		}
	}
	worker := &CredentialSelfcheckWorker{
		db:      db,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
		stopCh:  make(chan struct{}),
	}
	if db != nil {
		worker.connPool = pgxCredentialSelfcheckPool{pool: db}
	}
	return worker
}

func (w *CredentialSelfcheckWorker) Start(parent context.Context) {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		w.lifecycleMu.Lock()
		select {
		case <-w.stopCh:
			w.lifecycleMu.Unlock()
			return
		default:
		}
		ctx, cancel := context.WithCancel(parent)
		w.cancel = cancel
		w.wg.Add(1)
		w.lifecycleMu.Unlock()
		go w.loop(ctx)
		slog.Info("credential_selfcheck_worker started", "cycle_interval", credentialSelfcheckCycleInterval, "window", credentialSelfcheckWindow)
	})
}

func (w *CredentialSelfcheckWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.lifecycleMu.Lock()
		cancel := w.cancel
		w.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	w.wg.Wait()
}

func (w *CredentialSelfcheckWorker) loop(ctx context.Context) {
	defer w.wg.Done()
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

func (w *CredentialSelfcheckWorker) cycleOnce(ctx context.Context) {
	credID, ok, err := w.pickDueCredential(ctx)
	if err != nil {
		slog.Warn("credential_selfcheck_worker: pick due credential failed", "error", err)
		return
	}
	if !ok || w.connPool == nil {
		return
	}
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	conn, err := w.connPool.Acquire(lockCtx)
	cancel()
	if err != nil {
		return
	}
	discardConn := false
	defer func() { conn.Release(discardConn) }()
	lockCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	var locked bool
	err = conn.QueryRow(lockCtx, `SELECT pg_try_advisory_lock($1)`, credID).Scan(&locked)
	cancel()
	if err != nil || !locked {
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer unlockCancel()
		var unlocked bool
		if unlockErr := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, credID).Scan(&unlocked); unlockErr != nil || !unlocked {
			discardConn = true
		}
	}()
	if err := w.runOne(ctx, credID); err != nil {
		slog.Warn("credential_selfcheck_worker: runOne failed", "credential_id", credID, "error", err)
	}
}

func (w *CredentialSelfcheckWorker) pickDueCredential(ctx context.Context) (int, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id int
	err := w.db.QueryRow(queryCtx, `
		SELECT c.id
		FROM credentials c
		JOIN LATERAL (
			SELECT MAX(rl.ts) AS last_error_at
			FROM request_logs_hot rl
			WHERE rl.credential_id = c.id
			  AND rl.ts >= now() - interval '24 hours'
			  AND (rl.success = FALSE OR COALESCE(rl.status_code, 0) >= 400)
		) e ON e.last_error_at IS NOT NULL
		LEFT JOIN LATERAL (
			SELECT MAX(completed_at) AS last_at
			FROM self_check_runs scr
			WHERE scr.model_name = 'cred-' || c.id::text
		) l ON TRUE
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(l.last_at, '1970-01-01'::timestamptz) < now() - $1::interval
		ORDER BY e.last_error_at DESC, l.last_at NULLS FIRST, c.id
		LIMIT 1`, fmt.Sprintf("%d seconds", int(credentialSelfcheckWindow.Seconds()))).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}

func (w *CredentialSelfcheckWorker) runOne(ctx context.Context, credentialID int) error {
	pick, err := w.pickModels(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("pick models: %w", err)
	}
	if len(pick.models) == 0 {
		startedAt := time.Now()
		runID, ierr := w.insertRun(ctx, credentialID, startedAt, 0)
		if ierr != nil {
			return fmt.Errorf("insert no-routable placeholder run: %w", ierr)
		}
		if ferr := w.finalizeRun(ctx, runID, startedAt, "failed", "no_eligible_model", 0, 0, false, 0, 0, "none", fmt.Sprintf("no_eligible_models: credential %d has no ranked or due failed binding", credentialID), []string{}); ferr != nil {
			return fmt.Errorf("finalize no-routable placeholder run: %w", ferr)
		}
		w.publishSelfcheck(credentialID, runID, "fail")
		return fmt.Errorf("credential %d has no routable models", credentialID)
	}
	startedAt := time.Now()
	runID, err := w.insertRun(ctx, credentialID, startedAt, len(pick.models))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	w.publishSelfcheck(credentialID, runID, "in-flight")
	var attempted []string
	lastErrType, lastErrDetail := "none", ""
	hadToolCall, success := false, false
	totalRounds, successRounds, totalTokens, totalLatency := 0, 0, 0, 0
	for i, model := range pick.models {
		strategy := pick.strategy
		if i > 0 {
			strategy = fmt.Sprintf("fallback_%d", i)
		}
		attempted = append(attempted, model)
		r := w.doRequest(ctx, credentialID, model)
		totalRounds++
		totalTokens += r.Tokens
		totalLatency += r.LatencyMs
		hadToolCall = hadToolCall || r.HadToolCall
		if r.Success {
			success, successRounds, lastErrType, lastErrDetail = true, successRounds+1, "none", ""
			slog.Info("credential_selfcheck_worker: model ok", "credential_id", credentialID, "model", model, "strategy", strategy, "latency_ms", r.LatencyMs)
			break
		}
		lastErrType, lastErrDetail = r.ErrType, r.ErrDetail
		slog.Warn("credential_selfcheck_worker: model failed", "credential_id", credentialID, "model", model, "strategy", strategy, "err_type", r.ErrType)
	}
	status := "success"
	if !success {
		status = "failed"
	}
	avgLatency := 0
	if totalRounds > 0 {
		avgLatency = totalLatency / totalRounds
	}
	if err := w.finalizeRun(ctx, runID, startedAt, status, pick.strategy, totalRounds, successRounds, hadToolCall, totalTokens, avgLatency, lastErrType, lastErrDetail, attempted); err != nil {
		return fmt.Errorf("finalize run: %w", err)
	}
	if err := w.auditToSystemProbeRuns(ctx, runID, credentialID, pick.models, startedAt, status); err != nil {
		slog.Warn("credential_selfcheck: audit write failed", "credential_id", credentialID, "error", err)
	}
	if status == "failed" {
		w.publishSelfcheck(credentialID, runID, "fail")
	} else {
		w.publishSelfcheck(credentialID, runID, "ok")
	}
	return nil
}

type pickModelsResult struct {
	models   []string
	strategy string // "featured" | "recent" | "fallback"
}

type selfcheckBinding struct {
	raw          string
	standardized string
}

// pickModels selects one primary whose raw or standardized identity is present
// in featured ∪ recent. Due automatic failed bindings are the only appendages.
func (w *CredentialSelfcheckWorker) pickModels(ctx context.Context, credentialID int) (pickModelsResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var tenant string
	if err := w.db.QueryRow(queryCtx, `
		SELECT c.tenant_id
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE`, credentialID).Scan(&tenant); err != nil {
		if err == pgx.ErrNoRows {
			return pickModelsResult{}, nil
		}
		return pickModelsResult{}, err
	}
	bindings, err := w.selfcheckBindings(queryCtx, credentialID, true)
	if err != nil {
		return pickModelsResult{}, err
	}
	failed, err := w.selfcheckBindings(queryCtx, credentialID, false)
	if err != nil {
		return pickModelsResult{}, err
	}
	featured, err := w.selfcheckFeatured(queryCtx, tenant)
	if err != nil {
		return pickModelsResult{}, err
	}
	primary, strategy := selectSelfcheckPrimary(bindings, featured, recentmodels.Read(queryCtx, w.redis, tenant, 10))
	if primary == "" {
		recent, err := w.selfcheckRecentFallback(queryCtx, credentialID, tenant)
		if err != nil {
			return pickModelsResult{}, err
		}
		primary, strategy = selectSelfcheckPrimary(bindings, featured, recent)
	}
	out := make([]string, 0, 1+len(failed))
	seen := make(map[string]struct{}, 1+len(failed))
	if primary == "" {
		strategy = "failed_model"
	} else {
		out = append(out, primary)
		seen[recentmodels.Normalize(primary)] = struct{}{}
	}
	for _, binding := range failed {
		key := recentmodels.Normalize(binding.raw)
		if key == "" {
			key = recentmodels.Normalize(binding.standardized)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, binding.raw)
	}
	return pickModelsResult{models: out, strategy: strategy}, nil
}

func (w *CredentialSelfcheckWorker) selfcheckBindings(ctx context.Context, credentialID int, available bool) ([]selfcheckBinding, error) {
	rows, err := w.db.Query(ctx, `
		SELECT DISTINCT pm.raw_model_name, COALESCE(pm.standardized_name, pm.raw_model_name)
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.available, FALSE) = $2
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND ($2 = TRUE OR cmb.unavailable_recover_at IS NOT NULL AND cmb.unavailable_recover_at <= now())
		  AND ($2 = FALSE OR EXISTS (
			SELECT 1 FROM v_routable_credential_models v
			WHERE v.binding_id = cmb.id AND v.is_routable = TRUE
		  ))
		ORDER BY COALESCE(pm.standardized_name, pm.raw_model_name), pm.raw_model_name`, credentialID, available)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]selfcheckBinding, 0)
	for rows.Next() {
		var binding selfcheckBinding
		if err := rows.Scan(&binding.raw, &binding.standardized); err != nil {
			return nil, err
		}
		if binding.raw != "" {
			out = append(out, binding)
		}
	}
	return out, rows.Err()
}

func (w *CredentialSelfcheckWorker) selfcheckFeatured(ctx context.Context, tenant string) ([]string, error) {
	var featured []string
	if err := w.db.QueryRow(ctx, `
		SELECT COALESCE(featured_models, ARRAY[]::TEXT[])
		FROM routing_policy WHERE tenant_id = $1 ORDER BY id LIMIT 1`, tenant).Scan(&featured); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return featured, nil
}

func (w *CredentialSelfcheckWorker) selfcheckRecentFallback(ctx context.Context, credentialID int, tenant string) ([]recentmodels.Entry, error) {
	rows, err := w.db.Query(ctx, `
		SELECT model, COUNT(*)::int AS count
		FROM (
			SELECT COALESCE(mc.canonical_name, mc2.canonical_name, rl.client_model) AS model
			FROM request_logs_hot rl
			LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
			LEFT JOIN LATERAL (
				SELECT canonical_id FROM model_aliases
				WHERE raw_name = lower(rl.client_model) AND status = 'active' LIMIT 1
			) ma ON TRUE
			LEFT JOIN models_canonical mc2 ON mc2.id = ma.canonical_id
			WHERE rl.credential_id = $1
			  AND rl.tenant_id = $2
			  AND rl.ts >= now() - interval '7 days'
			  AND rl.success = TRUE
			  AND NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)
			  AND COALESCE(rl.client_model, '') <> ''
		) ranked
		GROUP BY model ORDER BY count DESC, model LIMIT 10`, credentialID, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]recentmodels.Entry, 0)
	for rows.Next() {
		var entry recentmodels.Entry
		if err := rows.Scan(&entry.Model, &entry.Count); err != nil {
			return nil, err
		}
		if entry.Model != "" {
			out = append(out, entry)
		}
	}
	return out, rows.Err()
}

func selectSelfcheckPrimary(bindings []selfcheckBinding, featured []string, recent []recentmodels.Entry) (string, string) {
	featuredSet := make(map[string]struct{}, len(featured))
	for _, model := range featured {
		if normalized := recentmodels.Normalize(model); normalized != "" {
			featuredSet[normalized] = struct{}{}
		}
	}
	scores := make(map[string]int, len(recent))
	for _, entry := range recent {
		if normalized := recentmodels.Normalize(entry.Model); normalized != "" && entry.Count > scores[normalized] {
			scores[normalized] = entry.Count
		}
	}
	type candidate struct {
		raw, name string
		score     int
		featured  bool
	}
	var candidates []candidate
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		keys := []string{recentmodels.Normalize(binding.raw), recentmodels.Normalize(binding.standardized)}
		key := keys[0]
		if key == "" {
			key = keys[1]
		}
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		c := candidate{raw: binding.raw, name: binding.standardized}
		if c.name == "" {
			c.name = binding.raw
		}
		for _, alias := range keys {
			if score := scores[alias]; score > c.score {
				c.score = score
			}
			if _, ok := featuredSet[alias]; ok {
				c.featured = true
			}
		}
		if c.featured || c.score > 0 {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return "", ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].featured != candidates[j].featured {
			return candidates[i].featured
		}
		if candidates[i].name != candidates[j].name {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].raw < candidates[j].raw
	})
	strategy := "recent"
	if candidates[0].featured {
		strategy = "featured"
	}
	return candidates[0].raw, strategy
}

type credentialSelfcheckRound struct {
	Success     bool
	HadToolCall bool
	Tokens      int
	LatencyMs   int
	HTTPCode    int
	ErrType     string
	ErrDetail   string
}

func (w *CredentialSelfcheckWorker) doRequest(ctx context.Context, credentialID int, model string) credentialSelfcheckRound {
	pingBody, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "ping"}}, "max_tokens": 10})
	r := w.doHTTP(ctx, credentialID, model, string(pingBody), false)
	if !r.Success {
		return r
	}
	toolBody, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]any{{"role": "user", "content": "用 get_current_time 工具查询当前北京时间，只调用工具，不要输出其他。"}}, "max_tokens": 64, "tools": []map[string]any{selfCheckToolDef}})
	return w.doHTTP(ctx, credentialID, model, string(toolBody), true)
}

func (w *CredentialSelfcheckWorker) doHTTP(ctx context.Context, credentialID int, model, body string, expectTool bool) credentialSelfcheckRound {
	r := credentialSelfcheckRound{ErrType: "none"}
	if credentialID <= 0 {
		r.ErrType, r.ErrDetail = "unattributed", "credential_selfcheck: missing credential_id for pin; refusing to attribute"
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		r.ErrType, r.ErrDetail = "internal", err.Error()
		return r
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "self_check")
	req.Header.Set("X-LLM-Origin-Actor", "credential-selfcheck-worker")
	req.Header.Set("X-LLM-Pin-Credential", strconv.Itoa(credentialID))
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
			r.ErrType, r.ErrDetail = "timeout", "context cancelled"
		} else {
			r.ErrType, r.ErrDetail = "http_000", err.Error()
		}
		return r
	}
	defer resp.Body.Close()
	r.HTTPCode = resp.StatusCode
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	if resp.StatusCode != http.StatusOK {
		r.ErrType = string(errorsx.ClassifyErrorWithBody(resp.StatusCode, buf[:n]))
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
		r.ErrType, r.ErrDetail = "parse_error", "json unmarshal failed: "+err.Error()
		return r
	}
	if len(parsed.Choices) == 0 {
		r.ErrType, r.ErrDetail = "empty_response", "no choices in response"
		return r
	}
	r.Success, r.Tokens = true, parsed.Usage.TotalTokens
	r.HadToolCall = len(parsed.Choices[0].Message.ToolCalls) > 0
	if expectTool && !r.HadToolCall {
		slog.Info("credential_selfcheck_worker: expected tool call but model did not call", "model", model)
	}
	return r
}

func (w *CredentialSelfcheckWorker) insertRun(ctx context.Context, credentialID int, startedAt time.Time, attemptCount int) (int64, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id int64
	err := w.db.QueryRow(queryCtx, `INSERT INTO self_check_runs (model_name, started_at, status, tenant_id) VALUES ($1, $2, 'running', 'default') RETURNING id`, fmt.Sprintf("cred-%d", credentialID), startedAt).Scan(&id)
	return id, err
}

func (w *CredentialSelfcheckWorker) finalizeRun(ctx context.Context, runID int64, startedAt time.Time, status, strategy string, roundsTotal, roundsSuccess int, hadToolCall bool, totalTokens, avgLatency int, errType, errDetail string, attempted []string) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	attemptedJSON, _ := json.Marshal(attempted)
	completedAt := time.Now()
	_, err := w.db.Exec(queryCtx, `
		UPDATE self_check_runs SET completed_at = $2, duration_ms = $3, status = $4,
			rounds_total = $5, rounds_success = $6, had_tool_call = $7, total_tokens = $8,
			avg_latency_ms = $9, error_type = $10, error_detail = $11,
			selection_strategy = $12, attempted_models = $13::jsonb WHERE id = $1`,
		runID, completedAt, int(completedAt.Sub(startedAt).Milliseconds()), status, roundsTotal, roundsSuccess, hadToolCall, totalTokens, avgLatency, errType, errDetail, strategy, attemptedJSON)
	return err
}

func (w *CredentialSelfcheckWorker) auditToSystemProbeRuns(ctx context.Context, taskID int64, credentialID int, models []string, startedAt time.Time, status string) error {
	if len(models) == 0 {
		return nil
	}
	probeStatus := "success"
	if status == "failed" {
		probeStatus = "failed"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := w.db.Exec(queryCtx, `
		INSERT INTO system_probe_runs (task_id, task_type, automaticity, credential_id, raw_model, source, worker_id, status, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		taskID, "chat_tool", "automatic", credentialID, models[0], "legacy_selfcheck", "credential-selfcheck-worker", probeStatus, startedAt, time.Now())
	return err
}
