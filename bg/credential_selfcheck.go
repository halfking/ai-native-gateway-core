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
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/recentmodels"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

const credentialSelfcheckCycleInterval = 5 * time.Minute
const credentialSelfcheckWindow = 15 * time.Minute

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

	// P0-3 (probe-cost-optimization §5) rate-limit circuit breaker state.
	// These fields are touched only by the worker loop goroutine. Provider
	// rate limits stop fallbacks for the current credential; only a response
	// marked by this gateway can seed the one-cycle shared-key cooldown.
	gatewayRateLimitedInRun     bool
	lastCycleGatewayRateLimited bool
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
			w.cycleOnceRecovered(ctx)
		}
	}
}

// cycleOnceRecovered 守护单轮 selfcheck cycle（R51 审计 P2：loop goroutine
// 原先无任何 recover，单次 panic 即整进程崩溃）——panic 记日志后 tick 循环继续。
func (w *CredentialSelfcheckWorker) cycleOnceRecovered(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("credential_selfcheck_worker: cycle panic recovered", "recover", rec)
		}
	}()
	w.cycleOnce(ctx)
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
		-- v1 臂改为 LEFT JOIN：原本是 INNER JOIN（e.last_error_at IS NOT NULL），
		-- 那会让 v1 证据成为硬前置，即使补了 session 臂也永远轮不上它 ——
		-- 即「补了臂但没接线」。改成 LEFT JOIN 后，硬前置由下面的 se 承担。
		LEFT JOIN LATERAL (
			SELECT MAX(rl.ts) AS last_error_at
			FROM request_logs_hot rl
			WHERE rl.credential_id = c.id
			  AND rl.ts >= now() - interval '24 hours'
			  -- R50 note: probe rows are deliberately INCLUDED here (no
			  -- origin_stage arm). A failed probe is genuine evidence the
			  -- credential is unhealthy — unlike usage scans (INV-3), the
			  -- self-reinforcing direction is damped by the
			  -- credentialSelfcheckWindow between picks. Same trade-off as
			  -- the deferred F18 candidate_failure_logs attribution.
			  AND (rl.success = FALSE OR COALESCE(rl.status_code, 0) >= 400)
		) e ON e.last_error_at IS NOT NULL
		-- 2026-10-02 审计：第二条错误证据臂指向 session 族（SSOT，不受 S4 停写门管）。
		--
		-- 上面那条臂是**硬过滤**（e.last_error_at IS NOT NULL），不是排序：停写生效 24h 后
		-- request_logs_hot 不再产生新的失败行 ⇒ 没有任何凭据能通过 ⇒ 自检**自己静默**。
		-- 这与 credential_recovery 的 lookbackCandidateSQL 同形（方向 ②：证据缺失）。
		--
		-- 真库实测（2026-10-02，24h 窗口，按凭据去重）：
		--   v1 侧有报错的凭据 41，session 侧 15，**两侧都有 15，只有 v1 有 26**。
		-- 再按 origin_stage 拆那 26：**全部是 node_probe**。
		-- ⇒ 业务失败在两族都有（15/15 重合），探针失败**只存在于 v1**——
		--    因为探针流量按设计不走 session 写路径，**它无法被端口**。
		--
		-- 所以本臂的定位要写清楚：
		--   - 保住的是**业务失败**的检测（停写后仍有效）；
		--   - 放弃的是「探针最近失败 ⇒ 现在去复检」这条**快捷信号**。
		-- 后者不是能力丢失：探针系统本身（node_probe / node_probe_state）不受该门管，
		-- 仍会直接发现不健康。selfcheck 用 v1 探针失败行只是取证捷径。
		-- ⇒ 因此**不门控整个自检 worker**（那会连同仍然有效的故障发现一起停掉），
		--   改为补这条臂把「止错」的部分保住。
		--
		-- 纯增量：两条臂同时生效，命中集合是原集合的超集，不改变今天的挑选结果
		-- （24h 内 v1 已覆盖全部业务失败——§9.28 修正：两侧都有的凭据是 **19** 个而非 15，
		-- 原先只读父表少计了 4 个；仅 v1 有的 22 个仍全部是 node_probe）。
		-- credential_id 类型不同（v1 是 bigint、session_turns 是 text），故显式 CAST。
		--
		-- ⚠️ 必须同时读 **hot 与父表**两个面（§9.28）。会话族的写方只写
		-- session_turns_hot，冷行由 promote_session_turns_hot_to_partition
		-- 搬到分区父表，**两者的边界随 promote 节奏移动**（本机实测边界在
		-- 2026-10-02 06:06:31 / 06:07:14，父表落后 hot 约 8.7 小时）。
		-- 只读父表 ⇒ **对最新轮次盲**，而最新轮次恰恰是刚失败、最该被抓的那些。
		-- 710 视图用的是 session_turns_hot UNION ALL session_turns（同款惯例），
		-- 直读方必须照做。
		LEFT JOIN LATERAL (
			SELECT MAX(st.ts) AS last_error_at
			FROM (
				SELECT ts, success, status_code, credential_id FROM session_turns
				UNION ALL
				SELECT ts, success, status_code, credential_id FROM session_turns_hot
			) st
			WHERE st.credential_id = c.id::text
			  AND st.ts >= now() - interval '24 hours'
			  AND (st.success = FALSE OR COALESCE(st.status_code, 0) >= 400)
		) se ON COALESCE(e.last_error_at, se.last_error_at) IS NOT NULL
		LEFT JOIN LATERAL (
			SELECT MAX(completed_at) AS last_at
			FROM self_check_runs scr
			WHERE scr.model_name = 'cred-' || c.id::text
		) l ON TRUE
		WHERE c.status = 'active'
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(l.last_at, '1970-01-01'::timestamptz) < now() - $1::interval
		-- 2026-09-08: least-recently-checked first. With a 15m window and one
		-- pick per 5m tick, newest-error-first let 3 noisy credentials starve
		-- everything else; rotating on l.last_at guarantees every erroring
		-- credential gets its turn.
		-- e.last_error_at → COALESCE(e.last_error_at, se.last_error_at)：与上面的硬前置
		-- 同一个接线问题。停写后唯一合格的群体 v1 值为 NULL，PostgreSQL 的
		-- DESC 默认 NULLS FIRST，会把他们全部挤到最前，丢掉「最近报错优先」的排序语义。
		ORDER BY COALESCE(l.last_at, '1970-01-01'::timestamptz) ASC,
		         COALESCE(e.last_error_at, se.last_error_at) DESC, c.id
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

	// P0-3 (probe-cost-optimization §5): a cycle that follows a rate-limit
	// abort probes only the primary model, once. Memory clears on the first
	// cycle that finishes without a 429.
	models := pick.models
	ratelimitAbort := settings.GetPlatformBool("probe.selfcheck.ratelimit_abort", true)
	if ratelimitAbort && w.lastCycleGatewayRateLimited && len(models) > 1 {
		slog.Warn("credential_selfcheck_worker: previous cycle hit gateway shared-key limit, primary-only this cycle",
			"credential_id", credentialID, "candidates_suppressed", len(models)-1)
		models = models[:1]
	}
	w.gatewayRateLimitedInRun = false

	var attempted []string
	lastErrType, lastErrDetail := "none", ""
	hadToolCall, success := false, false
	totalRounds, successRounds, totalTokens, totalLatency := 0, 0, 0, 0
	for i, model := range models {
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
		// P0-3 abort: a rate-limit rejection means this credential (or the
		// self-check key itself) is throttled for the whole cycle — walking
		// the remaining candidates only amplifies the load and feeds the
		// failure matrix. Terminate the fallback loop for this cycle.
		if r.RateLimited && ratelimitAbort {
			slog.Warn("credential_selfcheck_worker: rate-limited, aborting remaining candidates this cycle",
				"credential_id", credentialID, "model", model, "err_detail", r.ErrDetail, "candidates_suppressed", len(models)-i-1)
			break
		}
	}
	// Inter-cycle memory is reserved for this gateway's shared-key admission
	// response. Provider/unknown 429s are scoped to the current credential run.
	w.lastCycleGatewayRateLimited = w.gatewayRateLimitedInRun && ratelimitAbort
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
	// 2026-09-22 审计修正（探测量策略 INV-3）：shared Redis 榜单 TTL=7 天
	// （recentmodels.TTL，tenant 级），直接选主模型可能选中 4-7 天前用过的
	// 模型，越出"3 天内使用过"的探测范围。先按本凭据 3 天业务使用集合收窄
	// （查询失败 fail-open 保留原榜单，与必要性门禁同姿态）。
	recent := w.recentInUsageWindow(queryCtx, credentialID, tenant, recentmodels.Read(queryCtx, w.redis, tenant, 10))
	primary, strategy := selectSelfcheckPrimary(bindings, featured, recent)
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

// recentInUsageWindow narrows the shared seven-day Redis ranking (tenant-scoped,
// recentmodels.TTL=7d) to models with real (non-probe) business traffic on THIS
// credential within the 3-day probe scope window (探测量策略 INV-3). An empty
// in-window result means the ranking carries nothing policy-probeable; the
// caller then falls through to the 3-day DB fallback. Query errors fail OPEN
// (ranking kept as-is) — same posture as the necessity gate: this is an
// optimization over a verification probe already bounded to one per run.
func (w *CredentialSelfcheckWorker) recentInUsageWindow(ctx context.Context, credentialID int, tenant string, recent []recentmodels.Entry) []recentmodels.Entry {
	if len(recent) == 0 {
		return recent
	}
	used, err := w.recentUsageModels(ctx, credentialID, tenant)
	if err != nil {
		slog.Debug("credential_selfcheck_worker: 3-day usage filter unavailable, keeping shared ranking",
			"credential_id", credentialID, "error", err)
		return recent
	}
	return filterRecentEntriesByUsage(recent, used)
}

// recentUsageModels returns the normalized set of models with successful
// business (non-probe) traffic on the credential within probeUsageWindowInterval
// (3 days). Normalization matches recentmodels.Normalize so ZSET members and
// client_model variants compare equal.
func (w *CredentialSelfcheckWorker) recentUsageModels(ctx context.Context, credentialID int, tenant string) (map[string]struct{}, error) {
	rows, err := w.db.Query(ctx, `
		SELECT DISTINCT rl.client_model
		FROM request_logs_hot rl
		WHERE rl.credential_id = $1
		  AND rl.tenant_id = $2
		  AND rl.ts >= now() - `+probeUsageWindowInterval+`
		  AND rl.success = TRUE
		  AND `+fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl")+`
		  AND COALESCE(rl.client_model, '') <> ''`, credentialID, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			continue
		}
		if n := recentmodels.Normalize(m); n != "" {
			out[n] = struct{}{}
		}
	}
	return out, rows.Err()
}

// filterRecentEntriesByUsage keeps only ranking entries whose normalized model
// is in the in-window usage set (pure — unit-testable without a database).
func filterRecentEntriesByUsage(recent []recentmodels.Entry, used map[string]struct{}) []recentmodels.Entry {
	if len(recent) == 0 || len(used) == 0 {
		return nil
	}
	out := make([]recentmodels.Entry, 0, len(recent))
	for _, entry := range recent {
		if _, ok := used[recentmodels.Normalize(entry.Model)]; ok {
			out = append(out, entry)
		}
	}
	return out
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
			  -- 2026-09-20 probe-volume policy: 3-day usage scope (was 7 days)
			  AND rl.ts >= now() - interval '3 days'
			  AND rl.success = TRUE
			  -- R50: dual-arm exclusion (quality_flags + origin_stage) — the
			  -- probe gateway round carries no 'probe' flag, only the flag arm
			  -- let it count as usage here (same gap R49 F4 closed elsewhere).
			  AND `+fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl")+`
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
	// RateLimited is the P0-3 abort signal: the round failed with a
	// rate-limit rejection (gateway 429 / gw_rpm_exceeded / key_throttled).
	// runOne terminates the credential's remaining candidates on sight.
	RateLimited bool
	// GatewayRateLimited distinguishes the gateway's own shared-key admission
	// from a provider 429. Only the former may seed inter-cycle worker state.
	GatewayRateLimited bool
}

// isSelfcheckRateLimit reports whether the round's failure is a rate-limit
// rejection — the P0-3 abort trigger. HTTP 429 covers both the gateway's own
// limiter (gw_rpm_exceeded / gw_key_throttled early-exits surface as 429 with
// a rate_limit_error body) and an upstream 429 passed through the pinned
// credential; the classified kind and detail markers catch deployments that
// surface the same throttles on a non-429 status.
func isSelfcheckRateLimit(status int, errType, detail string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if errType == string(errorsx.KindRateLimit) {
		return true
	}
	for _, marker := range []string{"gw_rpm_exceeded", "gw_key_throttled", "key_throttled", "rate_limit_exceeded"} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
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
	r.GatewayRateLimited = resp.Header.Get(ratelimit.GatewayRateLimitScopeHeader) == ratelimit.GatewayRateLimitScopeSharedKey
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	if resp.StatusCode != http.StatusOK {
		r.ErrType = string(errorsx.ClassifyErrorWithBody(resp.StatusCode, buf[:n]))
		r.ErrDetail = truncateStrSC(string(buf[:n]), 200)
		if isSelfcheckRateLimit(resp.StatusCode, r.ErrType, r.ErrDetail) {
			r.RateLimited = true
			w.gatewayRateLimitedInRun = w.gatewayRateLimitedInRun || r.GatewayRateLimited
		}
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
			selection_strategy = $12, attempted_models = $13::text::jsonb WHERE id = $1`,
		runID, completedAt, int(completedAt.Sub(startedAt).Milliseconds()), status, roundsTotal, roundsSuccess, hadToolCall, totalTokens, avgLatency, errType, errDetail, strategy, string(attemptedJSON))
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
