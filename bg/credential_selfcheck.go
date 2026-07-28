// Package bg — credential_selfcheck.go
//
// CredentialSelfcheckWorker is the new per-credential daily self-check
// worker mandated by the 2026-07-14 spec rewrite.  It supersedes the
// featured-model self-check tick in bg/self_check_worker.go (which is
// now gated behind LLM_GATEWAY_USE_NEW_PROBE_MODE).
//
// 2026-07-23 (Phase 2.3 收敛进度): 节点探测路径尚未迁移到 system-monitor。
// runOne() 内部仍调用 Executor.Run 直接打上游；Phase 3 计划改为
// systemmonitor.Submit(SystemMonitorTaskType_ChatTool, automaticity=automatic)。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6.3 + KEEP/FUTURE 表。
//
// KEEP: 本文件保留至 Phase 3 切流完成 + 监控指标验证自动任务覆盖率 ≥ 90%。
//
//	切流条件：1) NodeProbe/ActiveProbe 也迁移完成；2) 监控仪表盘看到 system_probe_runs
//	连续 7 天累计自动任务量 ≥ 旧 self_check_runs 的 80%。 [@monitoring] [review 2026-Q3]
//
// Cadence
// ───────
//   - Once per 24h per (active) credential.
//   - Cycle tick: 5 minutes.  Each tick picks ONE due credential and
//     runs the model-selection algorithm on it, so the worker is
//     not spammy (max ~12 checks/hour, much less than the 252
//     observation of 2+ probes/min under the old design).
//
// Model selection (2026-07-15 — featured-first)
// ───────────────────────────────────────────────
//  1. "featured"   — models in routing_policy.featured_models that this
//     credential can serve.  PRIMARY tier: a credential
//     that serves any featured model MUST be probed on a
//     featured model, never on an obscure binding.
//  2. "most_used"  — if no featured model is served, fall back to the
//     top-1 model by 24h successful traffic (still
//     "common", not "uncommon").
//  3. "fallback_N" — if the preferred model fails, retry the next
//     model in the same tier order (remaining featured
//     → most_used → random pool).  Up to 3 attempts.
//  4. "random"     — for credentials with zero traffic AND no featured
//     bindings, pick uniformly at random from available
//     models.  Last-resort safety net.
//
// All attempts are persisted on self_check_runs (selection_strategy +
// attempted_models JSONB) and final status reflects the last attempt.
// If 3 fallbacks all fail the run ends with status='failed'.
//
// Outbound X-LLM-Origin-* headers
// ────────────────────────────────
//
//	X-LLM-Origin-Stage : self_check
//	X-LLM-Origin-Actor : credential-selfcheck-worker
//	X-Forwarded-For    : $LLM_GATEWAY_EGRESS_FORWARDED_FOR + egress IP
//	X-Real-IP          : $LLM_GATEWAY_EGRESS_IP
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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// credentialSelfcheckDB is the subset of *pgxpool.Pool that
// CredentialSelfcheckWorker needs. Declared as an interface (mirroring
// pickDB in shared_pick.go) so pickModels can be unit-tested with
// pgxmock; *pgxpool.Pool satisfies it transparently.
type credentialSelfcheckDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// CredentialSelfcheckWorker handles self-checks for credentials with recent
// request errors. Featured-model checks are owned by ModelProbeRunner.
type CredentialSelfcheckWorker struct {
	db      credentialSelfcheckDB
	apiKey  string
	baseURL string
	client  *http.Client

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once

	// rng is concurrency-safe (rand.Rand with mutex) so model fallback
	// selection on a never-used credential can pick uniformly.
	rng   *rand.Rand
	rngMu sync.Mutex
}

// NewCredentialSelfcheckWorker constructs the worker.  baseURL="" picks
// LLM_GATEWAY_SELF_CHECK_BASE_URL or the local gateway loopback URL.
func NewCredentialSelfcheckWorker(db credentialSelfcheckDB, apiKey, baseURL string) *CredentialSelfcheckWorker {
	if baseURL == "" {
		if envURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SELF_CHECK_BASE_URL")); envURL != "" {
			baseURL = envURL
		} else {
			baseURL = "http://127.0.0.1:8781/v1"
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

// Start launches the worker goroutine. It is safe to call repeatedly.
func (w *CredentialSelfcheckWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		go w.loop(ctx)
		slog.Info("credential_selfcheck_worker started",
			"cycle_interval", credentialSelfcheckCycleInterval,
			"window", credentialSelfcheckWindow,
		)
	})
}

// Stop requests termination. It is safe to call repeatedly, including before Start.
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

// pickDueCredential returns one active credential with a recent failed request
// that has not been self-checked in the last 24h. Healthy credentials are not
// scanned here; featured models are checked by ModelProbeRunner instead.
//
// Returns (id, true, nil) if a candidate exists, (0, false, nil) otherwise.
//
// 2026-07-22 fix: the LATERAL subquery now groups last_at by credential_id
// (via model_name='cred-<id>') instead of tenant_id.  The previous query
// shared one last_at across all credentials of the same tenant — and since
// every production credential lives in tenant_id='default', the worker
// permanently re-picked c.id=2 (the lowest id) and never advanced.  The
// credential_selfcheck worker writes its model_name as "cred-<id>" so we
// can identify per-credential runs without adding a column.  Pre-7/18
// legacy rows used raw model names ("gpt-5.6-luna", …) which never match
// the "cred-<int>" pattern; that's intentional — the legacy worker is
// retired and its last_at values should not gate the new worker.
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
	pick, err := w.pickModels(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("pick models: %w", err)
	}
	if len(pick.models) == 0 {
		// 2026-07-22 fix: previously this path returned an error WITHOUT
		// inserting a self_check_runs row. Because pickDueCredential derives
		// last_at from self_check_runs.completed_at, this caused the worker
		// to re-pick the same broken credential every 5-min tick forever
		// (verified on prod 154: credential_id=2 is auth-failed → 0 routable
		// → runOne returns → completed_at never advances → deadlock).
		//
		// Fix: still record a 'failed' run so last_at advances and the worker
		// moves on. The row carries status='failed' and
		// selection_strategy='random' (the latter is the only allowed value
		// in self_check_runs_selection_strategy_check when no featured /
		// most_used pick was made). error_type='none' is used because the
		// live DB on 154 still has the 338-migration CHECK
		// (http_000|http_502|http_503|http_504|timeout|upstream_fail|none)
		// — migration 339's DROP CONSTRAINT was never applied there. The
		// human-readable reason lives in error_detail.
		startedAt := time.Now()
		runID, ierr := w.insertRun(ctx, credentialID, startedAt, 0)
		if ierr != nil {
			return fmt.Errorf("insert no-routable placeholder run: %w", ierr)
		}
		if ferr := w.finalizeRun(ctx, runID, startedAt, "failed", "random",
			0, 0, false, 0, 0,
			"none",
			fmt.Sprintf("no_routable_models: credential %d has 0 routable bindings", credentialID),
			[]string{}); ferr != nil {
			return fmt.Errorf("finalize no-routable placeholder run: %w", ferr)
		}
		return fmt.Errorf("credential %d has no routable models", credentialID)
	}

	startedAt := time.Now()
	runID, err := w.insertRun(ctx, credentialID, startedAt, len(pick.models))
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	var (
		success       bool
		attemptedJSON = make([]string, 0, len(pick.models))
		lastErrType   = "none"
		lastErrDetail = ""
		hadToolCall   = false
		successRounds = 0
		totalRounds   = 0
		totalTokens   = 0
		totalLatency  = 0
	)

	for i, model := range pick.models {
		strategy := pick.strategy
		if i > 0 {
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
	if err := w.finalizeRun(ctx, runID, startedAt, status, pick.strategy, totalRounds, successRounds, hadToolCall, totalTokens, avgLatency, lastErrType, lastErrDetail, attemptedJSON); err != nil {
		return fmt.Errorf("finalize run: %w", err)
	}

	// Phase 3 Stage 1 Task 1.2: 打标为 legacy_selfcheck，写入 system_probe_runs
	// 用于 Phase 3 切流进度监控（对齐 docs/会话优化v2/35-*.md §2.1）
	if err := w.auditToSystemProbeRuns(ctx, runID, credentialID, pick.models, startedAt, status); err != nil {
		// 写入失败不阻塞主流程，仅记录日志
		slog.Warn("credential_selfcheck: failed to audit to system_probe_runs (non-blocking)",
			"credential_id", credentialID, "error", err)
	}

	return nil
}

// pickModelsResult carries the ordered model list plus the strategy
// that picked position 0, so runOne can label the self_check_runs row
// accurately and finalizeRun can persist selection_strategy.
type pickModelsResult struct {
	models   []string
	strategy string // "featured" | "most_used" | "random"
}

// pickModels returns the ordered list of up to 3 models to try for
// credentialID.  Position 0 is the preferred model; positions 1..2 are
// fallbacks.
//
// Selection priority (per 2026-07-15 directive — "不能找不常用的模型，
// 要找特性模型中的模型来进行探测，只有没有时才会随机选择"):
//
//  1. featured   — models in routing_policy.featured_models that this
//     credential can serve.  Ordered by 24h successful
//     traffic DESC (hottest featured first), then by name
//     for stability.  This is the PRIMARY tier: a
//     credential that serves any featured model MUST be
//     probed on a featured model, never on an obscure
//     binding that happens to be routable.
//  2. most_used  — if the credential serves NO featured model, fall
//     back to the 24h most-used model (still "common",
//     not "uncommon").
//  3. random     — if no featured AND no 24h traffic, pick uniformly
//     at random from the routable pool.  This is the
//     last-resort safety net for never-used / sandbox
//     credentials.
//
// Fallback slots (positions 1..2) are filled in the same tier order:
// remaining featured models, then most_used, then random pool — so a
// failed featured attempt retries on the next featured model before
// degrading to non-featured.
func (w *CredentialSelfcheckWorker) pickModels(ctx context.Context, credentialID int) (pickModelsResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// ── Tier 1: featured models this credential serves ──────────────
	// Reuses the same predicate as bg/shared_pick.go and
	// bg/model_probe.go:featuredCycle so all three probe layers agree
	// on what counts as "featured".  Ordered by standardized_name for
	// stable, deterministic picks across cycles (same as shared_pick.go).
	//
	// PG note: when paired with `SELECT DISTINCT`, every ORDER BY
	// expression must appear in the SELECT list (SQLSTATE 42P10).
	// We therefore SELECT both raw_model_name and the sort key, then
	// de-duplicate to keep the wire shape identical to the previous
	// query — only the sort expression was added to the SELECT list.
	featuredRows, err := w.db.Query(queryCtx, `
		SELECT DISTINCT pm.raw_model_name, COALESCE(pm.standardized_name, pm.raw_model_name) AS sort_key
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		CROSS JOIN routing_policy pol
		WHERE pol.tenant_id = 'default'
		  AND cmb.credential_id = $1
		  AND COALESCE(cmb.available, FALSE) = TRUE
		  AND EXISTS (
		    SELECT 1
		    FROM v_routable_credential_models v
		    WHERE v.binding_id = cmb.id
		      AND v.is_routable = TRUE
		  )
		  AND (
		    COALESCE(pm.standardized_name, pm.raw_model_name) = ANY(pol.featured_models)
		    OR pm.raw_model_name = ANY(pol.featured_models)
		  )
		ORDER BY sort_key
	`, credentialID)
	if err != nil {
		return pickModelsResult{}, err
	}
	var featured []string
	for featuredRows.Next() {
		var m, sortKey string
		if err := featuredRows.Scan(&m, &sortKey); err == nil && m != "" {
			featured = append(featured, m)
		}
	}
	featuredRows.Close()
	if err := featuredRows.Err(); err != nil {
		return pickModelsResult{}, err
	}

	// ── Tier 2: most-used model in last 24h ─────────────────────────
	var top1 string
	if err := w.db.QueryRow(queryCtx,
		`SELECT raw_model_name FROM credential_most_used_model($1, 24)`,
		credentialID,
	).Scan(&top1); err != nil && err.Error() != "no rows in result set" {
		return pickModelsResult{}, err
	}

	// ── Tier 3: full routable pool (for random fallback) ────────────
	poolRows, err := w.db.Query(queryCtx, `
		SELECT DISTINCT pm.raw_model_name
		FROM credentials c
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1
		  AND c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(cmb.available, FALSE) = TRUE
		  AND EXISTS (
		    SELECT 1
		    FROM v_routable_credential_models v
		    WHERE v.binding_id = cmb.id
		      AND v.is_routable = TRUE
		  )
	`, credentialID)
	if err != nil {
		return pickModelsResult{}, err
	}
	var pool []string
	for poolRows.Next() {
		var m string
		if err := poolRows.Scan(&m); err == nil && m != "" {
			pool = append(pool, m)
		}
	}
	poolRows.Close()
	if err := poolRows.Err(); err != nil {
		return pickModelsResult{}, err
	}
	if len(pool) == 0 && len(featured) == 0 && top1 == "" {
		return pickModelsResult{}, nil // no routable models at all
	}

	// ── Assemble ordered list + determine strategy ──────────────────
	out := make([]string, 0, maxFallbackAttempts)
	seen := make(map[string]struct{}, maxFallbackAttempts)
	add := func(m string) {
		if m == "" {
			return
		}
		if _, dup := seen[m]; dup {
			return
		}
		if len(out) >= maxFallbackAttempts {
			return
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}

	var strategy string
	switch {
	case len(featured) > 0:
		strategy = "featured"
		for _, m := range featured {
			add(m)
		}
		// Fill remaining slots with most_used, then random pool.
		add(top1)
	case top1 != "":
		strategy = "most_used"
		add(top1)
	default:
		strategy = "random"
	}

	// Fill any remaining fallback slots from the routable pool.
	// Shuffle the pool so consecutive runs of random-strategy
	// credentials cover different models over time.
	w.rngMu.Lock()
	w.rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	w.rngMu.Unlock()
	for _, m := range pool {
		if len(out) >= maxFallbackAttempts {
			break
		}
		add(m)
	}

	return pickModelsResult{models: out, strategy: strategy}, nil
}

// credentialSelfcheckRound is the structured result of a single model
// attempt.  Mirrors the per-round fields persisted today.
type credentialSelfcheckRound struct {
	Success     bool
	HadToolCall bool
	Tokens      int
	LatencyMs   int
	HTTPCode    int
	ErrType     string
	ErrDetail   string
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
	status, strategy string, roundsTotal, roundsSuccess int, hadToolCall bool,
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
		strategy, attemptedJSON,
	)
	return err
}

// auditToSystemProbeRuns writes a legacy_selfcheck entry to system_probe_runs
// for Phase 3 migration tracking (Stage 1 Task 1.2).
//
// 设计依据: docs/会话优化v2/35-SystemMonitor-Phase3-切流计划.md §2.1
func (w *CredentialSelfcheckWorker) auditToSystemProbeRuns(
	ctx context.Context,
	taskID int64,
	credentialID int,
	models []string,
	startedAt time.Time,
	status string,
) error {
	if len(models) == 0 {
		return nil // 无模型可探测，跳过
	}

	// 使用第一个尝试的模型作为 raw_model
	rawModel := models[0]

	// 构造 system_probe_runs 插入语句（复用 SystemMonitor Audit 的 schema）
	// 注意：source="legacy_selfcheck" 是关键标记
	query := `
		INSERT INTO system_probe_runs (
		task_id, task_type, automaticity, credential_id, raw_model, source,
			worker_id, status, started_at, finished_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
	`

	taskType := "chat_tool"      // schema 允许的工具调用探测类型
	automaticity := "automatic"  // 旧 worker 是自动探测
	source := "legacy_selfcheck" // Phase 3 关键：标记为旧 worker
	workerID := "credential-selfcheck-worker"
	finishedAt := time.Now()

	// 映射状态：success/partial/failed
	probeStatus := "success"
	if status == "failed" {
		probeStatus = "failed"
	} else if status == "partial" {
		probeStatus = "success" // partial 也算成功（至少有1轮成功）
	}

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := w.db.Exec(queryCtx, query,
		taskID, taskType, automaticity, credentialID, rawModel, source,
		workerID, probeStatus, startedAt, finishedAt,
	)
	return err
}
