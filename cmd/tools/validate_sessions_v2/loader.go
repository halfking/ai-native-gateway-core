package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// errV1FamilyRetired 是「v1 族已退役」的唯一错误出口。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：v1 族（`_request_logs*`）被 DROP，
// 任何提到它的语句都会 42P01。而本工具的**全部存在意义**就是「v1 vs 会话族」
// 的对拍——v1 那一侧没了，这个比较就永远得不出结果。
//
// ⇒ 所有读 v1 的入口都返回这个错误，而不是发一条会 42P01 的查询。
//
//	这比「返回 0 行」诚实得多：0 行会被读成「两侧一致」，
//	而事实是「只有一侧，而且它没被读过」——那正是本仓反复吃过亏的
//	「真空为绿」（§9.222 / §9.235 记的都是同一个坑）。
//
// 错误文案里带上族名与迁移号，是为了让看到它的人**立刻知道这不是环境问题**。
var errV1FamilyRetired = errors.New(
	"v1 family retired: the v1 request-log family was dropped (migration 890); " +
		"this validator's v1 leg no longer exists, so there is nothing to compare")

// V1Turn represents a turn from the (retired) v1 request-log family.
//
// ★ 2026-10-20：v1 族已整体退役 ⇒ **不再有任何代码路径会填出这个结构**。
//
//	它保留下来是因为本工具的校验器/报告器仍以它为入参（validator.go /
//	report.go / repair.go 都不在本轮范围内），而那些调用点会因为
//	LoadV1Turns 返回 errV1FamilyRetired 而**不再被走到**。
type V1Turn struct {
	RequestID    string
	Ts           time.Time
	SessionID    string
	TenantID     string
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Usage is stored as JSONB in the v1 family
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

// ★ 2026-10-20（迁移 890，v1 族整体退役）：原先那两条 v1 bodies 腿
// （`v1BodyQuery` / `v1BodyQueryParent`）与它们背后的一大段注记已删除。
//
// 为什么连注记一起删：那段注记记的是「Citus columnar 分区 + 未命名子查询 ⇒
// invalid perminfoindex 0 in RTE with relid 0」这个**执行器**缺陷，以及
// 「拆成两条顺序查询」这个修法。它是 v1 bodies 表特有的问题——
// 而那张表连同它的分区已被 DROP ⇒ 这个缺陷**不可能再复现**。
// 留着一段讲一个已经不会发生的问题的注记，是给下一个人制造噪音：
// 他会以为「这里还有个形状要注意」，而真正的形状是「这一族已经不在了」。
//
// 保留的是它背后的那条通用教训（写在这里，免得它随注记一起消失）：
// **碰到「换个写法就绕过去了」的执行器缺陷，先问它绕过去之后语义有没有变**。
// 那一轮的正确修法是拆成两条顺序查询，**逐字保留**了「hot 优先、母表兜底」
// 的择一规则；而一个更省事的修法（给子查询补一个分区键谓词）会改语义
// （谓词下推改变分区裁剪路径）——两者在结果上不可区分，所以必须选可证明的那个。

// NewSessionLoader creates a new session loader
func NewSessionLoader(db sessionDB) *SessionLoader {
	return &SessionLoader{db: db}
}

// LoadV1Turns loads the v1 turns of a session.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**这条腿退役**，本方法返回
// errV1FamilyRetired，不发任何查询。
//
// 为什么不把 v1 那一腿改指会话族再继续比：那会让工具**拿自己比自己**
// （会话族 vs 会话族），结果恒为「零差异」——而这个「零差异」与
// 「两侧一致」在报告里长得一模一样。本仓为这件事付过两次学费
// （§9.222 的截断窗口、§9.235 的真空为绿）：**「没量」与「量了，没有」必须
// 长得不一样**。⇒ 宁可响亮地报「v1 已退役」，也不要产出一份看起来很干净的
// 零差异报告。
//
// 签名保持不变（返回 ([]V1Turn, error)）：调用点在 main.go / repair.go，
// 不在本轮范围内；它们会因为这个错误而**停止**（单会话模式 fatal，
// 批处理模式记一次 loader error 并跳过该会话），而不是产出一份假报告。
func (l *SessionLoader) LoadV1Turns(ctx context.Context, tenantID, sessionID string) ([]V1Turn, error) {
	return nil, errV1FamilyRetired
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

// V1TimeRange is the span of rows that actually exist for a tenant.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：名字里的 "V1" 保留了——
// 调用点（main.go 的批处理流程、window_guard_test.go）都不在本轮范围内，
// 改名会让它们编译不过。但**它描述的对象已经退役**：v1 族被 DROP ⇒
// LoadV1TimeRange 现在返回 errV1FamilyRetired ⇒ 这个结构不再被填出。
// （待办：调用点更新后把它改名为 TimeRange / 去掉 V1 前缀。）
type V1TimeRange struct {
	MinTS, MaxTS time.Time
	Rows         int64
}

// LoadV1TimeRange reports the real span of v1 data for a tenant.
//
// §9.222: batch validation is advertised as "compare v1 against v2 over a
// window", but the v1 family is **not** a permanent store. On 252 the monthly
// job `pg17-drop-old-columnar-partitions.sh` (RETAIN_MONTHS=2) DETACHes and
// DROPs every v1 partition older than two months, while session_turns is on no
// rotation list at all. So a window wider than two months does not come back
// empty and does not error — it comes back **silently truncated**, and a parity
// report over a truncated window reads exactly like a parity report over the
// window that was asked for.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**这条腿退役**，本方法返回
// errV1FamilyRetired。
//
// 为什么连「把窗口守卫改问会话族」都不做：那个守卫（WindowExceedsV1Data）
// 存在的理由是「v1 会被轮转丢弃，而会话族不会」——它要防的是**拿一个被截断的
// 窗口去出一份 parity 报告**。现在 parity 本身已经没有第二侧可出，这个守卫
// 也就没有对象了；把它改问会话族会造出一道**永远不触发**的假门
// （会话族不在任何轮转清单上）——比没有门更糟，因为它看起来在防着什么。
func (l *SessionLoader) LoadV1TimeRange(ctx context.Context, tenantID string) (V1TimeRange, error) {
	return V1TimeRange{}, errV1FamilyRetired
}

// WindowExceedsV1Data reports whether the window the operator asked for
// reaches past the v1 rows that actually exist, and why.
//
// The check is deliberately split out from validateBatch so it can be tested in
// both directions without a database: a guard that has only ever been observed
// not firing is indistinguishable from a guard that cannot fire.
//
// §R45/M2 superseded: this guard briefly grew a third "end-day" arm backed by a
// HasV1RowsInRange probe, because the half-open bound excluded the named day
// and MaxTS alone could not see that. The bound is now corrected in
// buildSessionRangeQuery, so the named day is actually loaded and MaxTS is
// again sufficient to answer "is that day covered". The probe and the arm are
// gone; keeping them would fire on every run whose named day holds data.
//
// Two arms, in the order the operator should read them:
//
//   - start arm (fe5003034/§9.222): the window claims data older than the
//     oldest surviving v1 row — the retention trap.
//   - end arm (fe5003034): the window promises a day beyond the newest v1 row.
//     requestedEnd is the midnight of the last **named** day (the date flags
//     parse with time.Parse("2006-01-02")), so "the newest v1 row is before it"
//     is exactly "the named day holds no v1 row at all" — a whole day missing.
//
// A zero requestedStart means "unbounded below", which is what `-end-date`
// alone produces; that case is never truncation.
func WindowExceedsV1Data(requestedStart, requestedEnd time.Time, actual V1TimeRange) (bool, string) {
	if actual.Rows == 0 {
		// No v1 rows at all: reported as truncation would be wrong wording, and
		// the zero-candidate path already fails the gate closed.
		return false, ""
	}
	if !requestedStart.IsZero() && actual.MinTS.After(requestedStart) {
		return true, fmt.Sprintf("requested start %s precedes the oldest v1 row %s (%d rows)",
			requestedStart.Format(time.RFC3339), actual.MinTS.Format(time.RFC3339), actual.Rows)
	}
	// A newest-day that is merely *partial* is not truncation: "up to today"
	// is the common operator input, and refusing it would refuse nearly every
	// legitimate run. The hours of the named day are not this guard's problem —
	// they are loaded, and they are fixed at the source, in
	// endDayExclusiveBound.
	if !requestedEnd.IsZero() && actual.MaxTS.Before(requestedEnd) {
		// Hand the operator the flag value that would actually match the data.
		// This is the one genuinely useful idea carried over from the superseded
		// end-day arm: a diagnosis alone leaves the operator to guess which day
		// to type. The suggested date comes from MaxTS — the newest day that
		// holds rows — so it needs no probe.
		return true, fmt.Sprintf(
			"requested end %s is after the newest v1 row %s (%d rows): the named day holds no v1 rows at all, "+
				"so the window is wider than the data — re-run with -end-date %s to name the last day that has data",
			requestedEnd.Format(time.RFC3339), actual.MaxTS.Format(time.RFC3339), actual.Rows,
			actual.MaxTS.UTC().Format("2006-01-02"))
	}
	return false, ""
}

// endDayExclusiveBound converts an inclusive end day into the half-open
// upper bound the query needs.
//
// §R45/M2 superseded the previous detector here. The flags name calendar days:
// an operator who passes -end-date 2026-10-04 means "through the end of
// 2026-10-04". The query is half-open (`ts < $3`), so feeding it the named
// day's own midnight excluded the entire named day — the query returned rows
// through 2026-10-03T23:59:59Z while the report still printed 2026-10-04. On
// 252 that silently dropped 15h53m32s of v1 rows that did exist (newest row
// 2026-10-04 15:53:32Z).
//
// That is the worst shape this tool has: a parity report over a window
// narrower than the one requested is indistinguishable from a parity report
// over the requested window, and §9.222's whole reason for existing is that
// same report being used to justify retiring request_logs.
//
// The named day is deliberately left untouched for display and for
// WindowExceedsV1Data; only the bound handed to SQL is shifted.
func endDayExclusiveBound(namedEndDay time.Time) time.Time {
	return namedEndDay.AddDate(0, 0, 1)
}

// LoadSessionsInRange loads session IDs within a date range for batch validation.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**这条腿退役**，本方法返回
// errV1FamilyRetired（候选集原本来自 v1 族的那张表，而它已被 DROP）。
//
// 为什么不把候选集改问会话族：候选集是**为 v1↔v2 对拍挑靶子**用的，
// 而对拍已经没有第二侧 ⇒ 没有靶子可挑。留着一条只为了「还能列出点东西」
// 而存在的查询，是给运维制造「工具还在正常跑」的错觉——而它下一步就会
// 因为 v1 腿退役而失败。
func (l *SessionLoader) LoadSessionsInRange(ctx context.Context, tenantID string, startDate, endDate time.Time, settleWindow time.Duration, maxSessions int) ([]string, error) {
	return nil, errV1FamilyRetired
}

// ★ 2026-10-20（迁移 890，v1 族整体退役）：原先那条构造候选集 SQL 的
// 纯函数（buildSessionRangeQuery）已删除——它唯一的调用点
// （LoadSessionsInRange）随 v1 候选集一起退役，而留着一条没人调用的
// SQL 构造函数只会让人以为这条路径还活着。
//
// endDayExclusiveBound 保留：它是**纯函数**（不碰数据库），仍被窗口守卫
// 与既有门使用；只是它原先唯一的调用点没了。
