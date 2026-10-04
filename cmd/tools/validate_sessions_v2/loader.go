package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// V1Turn represents a turn from request_logs (V1 schema)
type V1Turn struct {
	RequestID    string
	Ts           time.Time
	SessionID    string
	TenantID     string
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Usage is stored as JSONB in request_logs
	Usage   json.RawMessage
	CostUSD float64

	// Compression metadata
	CompressionMeta json.RawMessage

	// Request body (for bodies validation)
	RequestBody  json.RawMessage
	ResponseBody json.RawMessage

	Success bool
}

// V2Turn represents a turn from session_turns (V2 schema)
type V2Turn struct {
	RequestID string
	TurnNo    int
	Ts        time.Time
	SessionID string
	TenantID  string

	SubmitMode string

	Model        string
	Provider     string
	CredentialID string

	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	InjectionVerdict string
	OutputVerdict    string

	LatencyMs  int
	StatusCode int
	Success    bool
	ErrorKind  string

	SourceKind string
	Quality    string
}

// V2Body represents a turn's bodies from session_bodies
type V2Body struct {
	SessionID string
	TurnNo    int
	TenantID  string
	RequestID string
	Ts        time.Time

	RequestDelta  json.RawMessage
	ResponseDelta json.RawMessage
	OutboundBody  json.RawMessage

	RequestAttachments  json.RawMessage
	ResponseAttachments json.RawMessage
}

// V2Session represents the session snapshot from sessions table
type V2Session struct {
	SessionID string
	TenantID  string
	CreatedAt time.Time
	UpdatedAt time.Time
	Status    string

	TotalTurns   int
	TotalTokens  int
	TotalCostUSD float64

	LastTurnNo          int
	LastRequestSummary  string
	LastResponseSummary string
	LastModel           string
	LastProvider        string

	PrimaryRequestID string
}

// SessionLoader loads V1 and V2 data for validation
type SessionLoader struct {
	db sessionDB
}

type sessionDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// v1BodyQuery / v1BodyQueryParent 取代了原先那条「子查询内 UNION ALL」的查询。
//
// # 为什么必须改（审计 §9.196，因果已确证）
//
// migration 765 `bodies_columnar_storage` 把 `request_logs_bodies` 的
// RANGE 分区转成了 **Citus `columnar`** 访问方法。**只要**该分区出现在
// 一个**未命名子查询**（relid=0 的 RTE）里并参与 `UNION ALL`，
// 执行器初始化就抛：
//
//	invalid perminfoindex 0 in RTE with relid 0
//
// 复现（干净库上，**0 行的分区**即可，审计 §9.196.4）：
//
//	ALTER TABLE request_logs_bodies_2026_11 SET ACCESS METHOD columnar;
//	SELECT count(*) FROM (SELECT request_id FROM request_logs_bodies_hot
//	                      UNION ALL SELECT request_id FROM request_logs_bodies) x;
//	⇒ ERROR: invalid perminfoindex 0 in RTE with relid 0
//
// 它与行数、数据内容、统计信息、DDL 全部无关（已逐项实测排除）。
//
// # 为什么拆成两条，而不是给子查询补一个分区键谓词
//
// 两种改法都实测能通过。选前者是因为**补谓词会改语义**：谓词下推改变
// 分区裁剪路径，而原查询的 `ts = $2` 本已足够定位；用两条顺序查询则
// **逐字保留**「hot 优先、母表兜底」的择一规则，与原
// `ORDER BY source_priority LIMIT 1` 的结果**完全一致**。
//
// # 代价（写明，不藏）
//
// 原本 1 次往返变成最多 2 次。`LoadV1Turns` 对每个轮次调用一次，
// 命中率低时（多数行只在母表）**会变成 2 倍往返**。
// 这是真实代价；但在「一条都查不出来」与「慢一倍」之间，前者不可接受。

// v1BodyQuery 先查 hot 腿（原 source_priority=0）。
// 未命名子查询的形状已实测会触发 perminfoindex 错误，
// 因此这里**不**使用 `FROM ( ... UNION ALL ... )`。
const v1BodyQuery = `
	SELECT COALESCE(request_body, '{}'::jsonb), COALESCE(response_body, '{}'::jsonb)
	FROM request_logs_bodies_hot
	WHERE request_id = $1 AND ts = $2
	LIMIT 1
`

// v1BodyQueryParent 是 hot 未命中时的兜底腿（原 source_priority=1）。
const v1BodyQueryParent = `
	SELECT COALESCE(request_body, '{}'::jsonb), COALESCE(response_body, '{}'::jsonb)
	FROM request_logs_bodies
	WHERE request_id = $1 AND ts = $2
	LIMIT 1
`

// NewSessionLoader creates a new session loader
func NewSessionLoader(db sessionDB) *SessionLoader {
	return &SessionLoader{db: db}
}

// LoadV1Turns loads all turns for a session from request_logs
// Uses two-step query to avoid JOIN timeout with request_logs_bodies
func (l *SessionLoader) LoadV1Turns(ctx context.Context, tenantID, sessionID string) ([]V1Turn, error) {
	// Step 1: Query request_logs for metadata
	// Note: staging schema doesn't have 'usage' or 'compression_meta' columns
	// Token counts are stored as separate columns (prompt_tokens, completion_tokens, etc.)
	metaQuery := `
		SELECT 
			request_id,
			ts,
			gw_session_id,
			tenant_id,
			COALESCE(client_model, '') as client_model,
			COALESCE(provider_id::text, '') as provider_id,
			COALESCE(credential_id::text, '') as credential_id,
			COALESCE(prompt_tokens, 0) as prompt_tokens,
			COALESCE(completion_tokens, 0) as completion_tokens,
			COALESCE(cache_read_tokens, 0) as cache_read_tokens,
			COALESCE(cache_write_tokens, 0) as cache_write_tokens,
			COALESCE(cost_usd, 0) as cost_usd,
			COALESCE(success, false) as success
		FROM request_logs
		WHERE tenant_id = $1 AND gw_session_id = $2
		ORDER BY ts ASC
	`

	rows, err := l.db.Query(ctx, metaQuery, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query request_logs: %w", err)
	}
	defer rows.Close()

	var turns []V1Turn
	for rows.Next() {
		var turn V1Turn
		var promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int
		err := rows.Scan(
			&turn.RequestID,
			&turn.Ts,
			&turn.SessionID,
			&turn.TenantID,
			&turn.ClientModel,
			&turn.ProviderID,
			&turn.CredentialID,
			&promptTokens,
			&completionTokens,
			&cacheReadTokens,
			&cacheWriteTokens,
			&turn.CostUSD,
			&turn.Success,
		)
		if err != nil {
			return nil, fmt.Errorf("scan request_logs row: %w", err)
		}

		// Reconstruct usage JSON from separate columns
		usage := map[string]interface{}{
			"prompt_tokens":      promptTokens,
			"completion_tokens":  completionTokens,
			"cache_read_tokens":  cacheReadTokens,
			"cache_write_tokens": cacheWriteTokens,
			"total_tokens":       promptTokens + completionTokens,
		}
		usageJSON, _ := json.Marshal(usage)
		turn.Usage = usageJSON

		// Initialize empty compression_meta and bodies (bodies filled in step 2)
		turn.CompressionMeta = json.RawMessage("{}")
		turn.RequestBody = json.RawMessage("{}")
		turn.ResponseBody = json.RawMessage("{}")
		turns = append(turns, turn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request_logs: %w", err)
	}

	// Step 2: Query the independent body store using the request log's identity.
	// Bodies may still be in the hot table or already promoted to partitions. The
	// timestamp is part of the body table key and prevents a reused request ID from
	// receiving another turn's body.
	//
	// 两条腿**顺序**执行（审计 §9.196）：原来那条「子查询内 UNION ALL」的查询在
	// `request_logs_bodies` 的 RANGE 分区是 Citus `columnar` 时会让执行器初始化
	// 直接失败（invalid perminfoindex 0 in RTE with relid 0），而 migration 765
	// `bodies_columnar_storage` 正是干这件事的。拆成两条后语义不变：hot 优先、
	// 母表兜底，与原 `ORDER BY source_priority LIMIT 1` 一致。
	for i := range turns {
		var requestBody, responseBody json.RawMessage
		err := l.db.QueryRow(ctx, v1BodyQuery, turns[i].RequestID, turns[i].Ts).Scan(&requestBody, &responseBody)
		if err == pgx.ErrNoRows {
			// hot 未命中 → 走已 promote 的母表分区。
			err = l.db.QueryRow(ctx, v1BodyQueryParent, turns[i].RequestID, turns[i].Ts).Scan(&requestBody, &responseBody)
		}
		if err == pgx.ErrNoRows {
			// No bodies for this request - keep empty defaults
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("query request_logs_bodies for request_id=%s: %w", turns[i].RequestID, err)
		}
		turns[i].RequestBody = requestBody
		turns[i].ResponseBody = responseBody
	}

	return turns, nil
}

// LoadV2Turns loads all turns for a session from the canonical current-month view.
func (l *SessionLoader) LoadV2Turns(ctx context.Context, tenantID, sessionID string) ([]V2Turn, error) {
	query := `
			SELECT 
				request_id,
				turn_no,
				ts,
				session_id,
				tenant_id,
				submit_mode,
				COALESCE(model, '') as model,
				COALESCE(provider, '') as provider,
				COALESCE(credential_id, '') as credential_id,
				COALESCE(prompt_tokens, 0) as prompt_tokens,
				COALESCE(completion_tokens, 0) as completion_tokens,
				COALESCE(cache_read_tokens, 0) as cache_read_tokens,
				COALESCE(cache_write_tokens, 0) as cache_write_tokens,
				COALESCE(cost_usd, 0) as cost_usd,
				COALESCE(injection_verdict, 'skip') as injection_verdict,
				COALESCE(output_verdict, 'skip') as output_verdict,
				COALESCE(latency_ms, 0) as latency_ms,
				COALESCE(status_code, 0) as status_code,
				COALESCE(success, false) as success,
				COALESCE(error_kind, '') as error_kind,
				source_kind,
				quality
			FROM public.session_turns_with_current_month
			WHERE tenant_id = $1 AND session_id = $2
			ORDER BY turn_no ASC
		`

	rows, err := l.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session_turns: %w", err)
	}
	defer rows.Close()

	var turns []V2Turn
	for rows.Next() {
		var turn V2Turn
		err := rows.Scan(
			&turn.RequestID,
			&turn.TurnNo,
			&turn.Ts,
			&turn.SessionID,
			&turn.TenantID,
			&turn.SubmitMode,
			&turn.Model,
			&turn.Provider,
			&turn.CredentialID,
			&turn.PromptTokens,
			&turn.CompletionTokens,
			&turn.CacheReadTokens,
			&turn.CacheWriteTokens,
			&turn.CostUSD,
			&turn.InjectionVerdict,
			&turn.OutputVerdict,
			&turn.LatencyMs,
			&turn.StatusCode,
			&turn.Success,
			&turn.ErrorKind,
			&turn.SourceKind,
			&turn.Quality,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session_turns row: %w", err)
		}
		turns = append(turns, turn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session_turns: %w", err)
	}

	return turns, nil
}

// CanonicalV2BodiesView is the only body relation accepted by the parity gate.
//
// 2026-10-02（§9.31）修正：本值此前是 `public.session_bodies_with_current_month`
// ——**该关系在真库里根本不存在**。全仓搜索确认：这个名字只作为 UNIQUE **约束名**
// 出现在迁移 614/645（`ADD CONSTRAINT session_bodies_with_current_month
// UNIQUE (tenant_id, request_id, partition_date)`），**从没有任何 CREATE VIEW**。
// 真库 pg_class 核实：`session_bodies_with_current_month` relkind = **'i'（索引/约束）**，
// 真正的合并视图是 `session_bodies_unified` relkind = **'v'**。
// ⇒ 本工具此前每次运行都 `relation "session_bodies_with_current_month" does not exist`，
// **parity 门一直在产出零证据**。
//
// 改指 `session_bodies_unified` 的依据（真库逐条核实，非推断）：
//   - 列：工具需要 session_id / turn_no / tenant_id / request_id / ts /
//     request_delta / response_delta / outbound_body / request_attachments /
//     response_attachments —— 该视图**十个全有**（外加 id / partition_date / kind）。
//     所以原注释「列语义不足」的反对**站不住**。
//   - 覆盖面：定义为 `session_bodies_hot UNION ALL session_bodies`（两个存储面），
//     1,771,097 行、2026-09-03 → 实时。原注释担心的「当月语义不足」实际是
//     **覆盖全保留期而非仅当月**——对一个**完整性/parity**门来说这是优点不是缺点。
//
// ⚠️ 这确实**改变了门的判定口径**（当月 → 全保留期）。原状态是「跑不起来」，
// 任何能跑的口径都是改善，但**这是语义变更，请负责人复核**：
// 若确实需要「仅当月」，正确做法是**新建一个视图**，而不是引用一个不存在的名字。
const CanonicalV2BodiesView = "public.session_bodies_unified"

// LoadV2Bodies loads all bodies for a session from the canonical body view.
func (l *SessionLoader) LoadV2Bodies(ctx context.Context, tenantID, sessionID string) ([]V2Body, error) {
	query := `
			SELECT 
				session_id,
				turn_no,
				tenant_id,
				request_id,
				ts,
				COALESCE(request_delta, '[]'::jsonb) as request_delta,
				COALESCE(response_delta, '[]'::jsonb) as response_delta,
				COALESCE(outbound_body, '[]'::jsonb) as outbound_body,
				COALESCE(request_attachments, '[]'::jsonb) as request_attachments,
				COALESCE(response_attachments, '[]'::jsonb) as response_attachments
				FROM public.session_bodies_unified b
				WHERE b.tenant_id = $1 AND b.session_id = $2
				  AND EXISTS (
					SELECT 1
					FROM public.session_turns_with_current_month t
					WHERE t.tenant_id = b.tenant_id
					  AND t.session_id = b.session_id
					  AND t.turn_no = b.turn_no
					  AND t.request_id = b.request_id
					  AND t.tenant_id = $1
					  AND t.session_id = $2
				  )
				ORDER BY b.turn_no ASC
		`

	rows, err := l.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query session_bodies: %w", err)
	}
	defer rows.Close()

	var bodies []V2Body
	for rows.Next() {
		var body V2Body
		err := rows.Scan(
			&body.SessionID,
			&body.TurnNo,
			&body.TenantID,
			&body.RequestID,
			&body.Ts,
			&body.RequestDelta,
			&body.ResponseDelta,
			&body.OutboundBody,
			&body.RequestAttachments,
			&body.ResponseAttachments,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session_bodies row: %w", err)
		}
		bodies = append(bodies, body)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session_bodies: %w", err)
	}

	return bodies, nil
}

// LoadV2Session loads the session snapshot from sessions table
func (l *SessionLoader) LoadV2Session(ctx context.Context, tenantID, sessionID string) (*V2Session, error) {
	query := `
			SELECT 
				session_id,
				tenant_id,
				created_at,
				updated_at,
				status,
				total_turns,
				total_tokens,
				total_cost_usd,
				COALESCE(last_turn_no, 0) as last_turn_no,
				COALESCE(last_request_summary, '') as last_request_summary,
				COALESCE(last_response_summary, '') as last_response_summary,
				COALESCE(last_model, '') as last_model,
				COALESCE(last_provider, '') as last_provider,
				COALESCE(primary_request_id, '') as primary_request_id
			FROM sessions
			WHERE tenant_id = $1 AND session_id = $2
			LIMIT 1
		`

	var session V2Session
	err := l.db.QueryRow(ctx, query, tenantID, sessionID).Scan(
		&session.SessionID,
		&session.TenantID,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.Status,
		&session.TotalTurns,
		&session.TotalTokens,
		&session.TotalCostUSD,
		&session.LastTurnNo,
		&session.LastRequestSummary,
		&session.LastResponseSummary,
		&session.LastModel,
		&session.LastProvider,
		&session.PrimaryRequestID,
	)

	if err == pgx.ErrNoRows {
		return nil, nil // Session not found in V2
	}
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}

	return &session, nil
}

// V1TimeRange is the span of v1 rows that actually exist for a tenant.
type V1TimeRange struct {
	MinTS, MaxTS time.Time
	Rows         int64
}

// LoadV1TimeRange reports the real span of v1 data for a tenant.
//
// §9.222: batch validation is advertised as "compare v1 against v2 over a
// window", but request_logs is **not** a permanent store. On 252 the monthly
// job `pg17-drop-old-columnar-partitions.sh` (RETAIN_MONTHS=2) DETACHes and
// DROPs every request_logs partition older than two months, while
// session_turns is on no rotation list at all. So a window wider than two
// months does not come back empty and does not error — it comes back
// **silently truncated**, and a parity report over a truncated window reads
// exactly like a parity report over the window that was asked for.
//
// The caller cannot detect that from the candidate count alone, because a
// truncated window can still yield ≥100 sessions. This is the only place the
// tool can tell the truth about it, so the range is measured here and
// surfaced rather than inferred.
func (l *SessionLoader) LoadV1TimeRange(ctx context.Context, tenantID string) (V1TimeRange, error) {
	var r V1TimeRange
	err := l.db.QueryRow(ctx, `
		SELECT min(ts), max(ts), count(*)
		FROM request_logs
		WHERE tenant_id = $1
		  AND gw_session_id IS NOT NULL
		  AND gw_session_id <> ''
	`, tenantID).Scan(&r.MinTS, &r.MaxTS, &r.Rows)
	if err != nil {
		return r, fmt.Errorf("query v1 time range: %w", err)
	}
	return r, nil
}

// HasV1RowsInRange reports whether the v1 family — this tenant's request_logs
// rows that carry a session header, the same family LoadV1TimeRange and
// LoadSessionsInRange read — has at least one row in [from, until).
//
// §R44/移交.1: the end-boundary guard needs this as a measured probe, not a
// derivation from LoadV1TimeRange's MaxTS. MaxTS decides "does data exist at
// or after the boundary" but not "does data exist inside the end day": a row
// can sit inside [end, endDayCutoff(end)) while MaxTS has already moved past
// the cutoff, and the two worlds must not be conflated.
//
// Cost is bounded the same way the body lookups are: LIMIT 1, and the ts
// bounds let the request_logs partition machinery prune to a single day. The
// family filters are byte-identical to LoadV1TimeRange's on purpose — the
// probe must answer for exactly the rows the window claims to compare.
func (l *SessionLoader) HasV1RowsInRange(ctx context.Context, tenantID string, from, until time.Time) (bool, error) {
	var one int32
	err := l.db.QueryRow(ctx, `
		SELECT 1
		FROM request_logs
		WHERE tenant_id = $1
		  AND gw_session_id IS NOT NULL
		  AND gw_session_id <> ''
		  AND ts >= $2
		  AND ts < $3
		LIMIT 1
	`, tenantID, from, until).Scan(&one)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("probe v1 rows in [%s, %s): %w",
			from.Format(time.RFC3339), until.Format(time.RFC3339), err)
	}
	return true, nil
}

// windowEndSlack is how far the newest v1 row may sit below the requested end
// before the "window wider than the data" arm fires.
//
// §R44/移交.1: -end-date is date-granular (time.Parse("2006-01-02") → that
// day's 00:00:00Z), so the half-open load `ts < end` covers whole days up to
// end-1. Data stopping anywhere *inside* day end-1 is the normal shape of a
// live source queried later the same day; demanding MaxTS >= end exactly (the
// pre-§R44 comparison) refused every such run. Data stopping *before* day
// end-1 means the window promises a full day that has no data — that is the
// fe5003034 end-side arm, kept, narrowed to the day the flags can actually
// express.
const windowEndSlack = 24 * time.Hour

// endDayCutoff is the exclusive upper bound of the day the operator named with
// -end-date. -end-date parses to that day's 00:00:00Z and the load is
// half-open (`ts < end`), so the named day's own rows — the remainder of the
// day the operator's token covers — are exactly [end, endDayCutoff(end)) and
// are invisible to the report. For a midnight end that is [D 00:00, D+1 00:00);
// for a non-midnight end it is the stretch up to the next midnight, which
// still contains any row sitting exactly at `end`.
//
// Flags parse in UTC, so UTC-day truncation is the calendar the operator's
// token lives in.
func endDayCutoff(end time.Time) time.Time {
	return end.Truncate(24 * time.Hour).Add(24 * time.Hour)
}

// WindowExceedsV1Data reports whether the window the operator asked for does
// not match the v1 data that actually exists, and why.
//
// The check is deliberately split out from validateBatch so it can be tested in
// both directions without a database: a guard that has only ever been observed
// not firing is indistinguishable from a guard that cannot fire.
//
// Three arms, in the order the operator should read them:
//
//   - start arm (fe5003034/§9.222): the window claims data older than the
//     oldest surviving v1 row — the retention trap.
//   - end-day arm (§R44/移交.1): the source still has v1 rows inside the day
//     the operator named with -end-date. That day is applied as 00:00:00Z and
//     loaded half-open (`ts < end`), so its own rows are invisible to the
//     report; on 252 this silently short-counted 15h53m32s while the guard
//     compared in the opposite direction (MaxTS.Before(end)) and stayed quiet.
//     hasRowsInEndDay is measured by HasV1RowsInRange over
//     [end, endDayCutoff(end)) — a LIMIT-1 probe; MaxTS alone cannot decide
//     this arm (a row inside the day while MaxTS has already moved past it).
//   - end arm (fe5003034, narrowed by windowEndSlack): the window promises a
//     full day beyond the newest v1 row.
//
// A zero requestedStart means "unbounded below", which is what `-end-date`
// alone produces; that case is never start truncation.
func WindowExceedsV1Data(requestedStart, requestedEnd time.Time, actual V1TimeRange, hasRowsInEndDay bool) (bool, string) {
	if actual.Rows == 0 {
		// No v1 rows at all: reported as truncation would be wrong wording, and
		// the zero-candidate path already fails the gate closed.
		return false, ""
	}
	if !requestedStart.IsZero() && actual.MinTS.After(requestedStart) {
		return true, fmt.Sprintf("requested start %s precedes the oldest v1 row %s (%d rows)",
			requestedStart.Format(time.RFC3339), actual.MinTS.Format(time.RFC3339), actual.Rows)
	}
	if !requestedEnd.IsZero() && hasRowsInEndDay {
		cutoff := endDayCutoff(requestedEnd)
		return true, fmt.Sprintf(
			"requested end %s is a half-open bound (ts < %s) and cuts off newer v1 rows: the source still has "+
				"family rows in the remainder of that day [%s, %s) that the window does not load "+
				"(newest v1 row %s, %d rows) — re-run with -end-date %s to cover the day the window currently drops",
			requestedEnd.Format(time.RFC3339), requestedEnd.Format(time.RFC3339),
			requestedEnd.Format(time.RFC3339), cutoff.Format(time.RFC3339),
			actual.MaxTS.Format(time.RFC3339), actual.Rows,
			cutoff.Format("2006-01-02"))
	}
	if !requestedEnd.IsZero() && actual.MaxTS.Before(requestedEnd.Add(-windowEndSlack)) {
		return true, fmt.Sprintf("requested end %s is after the newest v1 row %s (%d rows)",
			requestedEnd.Format(time.RFC3339), actual.MaxTS.Format(time.RFC3339), actual.Rows)
	}
	return false, ""
}

// LoadSessionsInRange loads session IDs within a date range for batch validation
func (l *SessionLoader) LoadSessionsInRange(ctx context.Context, tenantID string, startDate, endDate time.Time, settleWindow time.Duration, maxSessions int) ([]string, error) {
	if endDate.IsZero() {
		// ts < $3 with a zero time is `ts < year 1`: the query matches nothing
		// and the tool reports "no settled sessions found", which points the
		// operator at their data instead of at their flags. This used to be
		// reachable by passing -start-date without -end-date.
		return nil, errors.New("end of the validation window is unset: pass -end-date (YYYY-MM-DD); " +
			"a zero end bound silently matches zero rows rather than reporting an error")
	}
	settleThreshold := time.Now().Add(-settleWindow)

	query := `
			SELECT gw_session_id
			FROM request_logs
			WHERE tenant_id = $1
			  AND ts >= $2
			  AND ts < $3
			  AND gw_session_id IS NOT NULL
			  AND gw_session_id != ''
			GROUP BY gw_session_id
			HAVING MAX(ts) < $4
			ORDER BY gw_session_id
			LIMIT $5
		`

	// For batch mode, we select from request_logs and filter by settle window
	// We'll additionally filter by updated_at from sessions table if it exists
	rows, err := l.db.Query(ctx, query, tenantID, startDate, endDate, settleThreshold, maxSessions)
	if err != nil {
		return nil, fmt.Errorf("query session IDs: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("scan session_id: %w", err)
		}

		sessionIDs = append(sessionIDs, sessionID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session IDs: %w", err)
	}

	return sessionIDs, nil
}
