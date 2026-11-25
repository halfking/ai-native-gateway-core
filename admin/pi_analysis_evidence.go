package admin

// pi_analysis_evidence.go — PI 分析的取证采集器（2026-11-25 取证轮）。
//
// 实现 promptinjection.EvidenceProvider（接口定义在域包；这里只负责「去哪里取」）：
//
//	① 请求账本：出错的那一条请求（模型 / 协议 / 状态 / 失败阶段 / 耗时 / tokens）；
//	② 上游尝试：该请求在各上游上的失败记录（candidate_failure_logs_hot）；
//	③ 会话上下文：同一会话里前后相邻的请求——「前因后果」的时间线；
//	④ 同型错误分布：时间窗内同型错误按供应商/模型的分布（「偶发还是一直在发生」）；
//	⑤ 服务日志：本实例近期日志缓冲里与该错误相关的行（internal/logging 环形缓冲）；
//	⑥ 错误处理手册：同型错误已有的标准处置。
//
// ★ 纪律（三条）：
//
//	· **一路失败只降级不阻断**：每一路独立超时、独立 recover；失败在 Notes 里
//	  如实留一句「哪一路没取到、为什么」，其余各路照常。取证是分析的辅料，
//	  不能让辅料拖垮主菜。
//	· **租户隔离按构造成立**：每条 SQL 都带 tenant_id 谓词，取值来自队列行
//	  （不是调用方传参）⇒ 背景分析与按需取证走的是同一条受限路径。
//	· **说明文案是对外消息**：Notes / service_log_meta.note 会原样显示在管理面上，
//	  用英文（与本文件其余对外消息一致）；界面上的固定文案一律走 i18n。
//	· **「没搜到」≠「没发生」**：服务日志只覆盖本进程启动之后；覆盖窗口的
//	  两端（最老/最新一行的时间）随材料一起返回，由读的人判断结论成不成立。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// piEvidenceTotalTimeout 是一次取证的**总**预算（与分析器的 evidenceGatherTimeout 一致）。
//
//	★ 2026-11-26 订正：此前这个总预算只是「写在注释里」——每一步各自从 Background
//	  起 3s 超时、不看调用方的截止时间 ⇒ 最坏六步 × 3s，分析会被拖住 18s，与
//	  「8s 总预算、永不阻塞分析」的说法不符。现在 Gather 内部真正执行它：总预算 =
//	  min(调用方的截止时间, 本值)；每一步仍有自己的上限（且不超过总预算）；预算用尽后
//	  剩余各路直接标「预算用尽」并跳过。是变量只为测试能调短。
var piEvidenceTotalTimeout = 8 * time.Second

// 各路取证的条数与时间上限。
const (
	piEvidenceStepTimeout     = 3 * time.Second
	piEvidenceSessionSpan     = 30 * time.Minute
	piEvidenceWindowSpan      = time.Hour
	piEvidenceMaxSessionRows  = 20
	piEvidenceMaxAttemptRows  = 10
	piEvidenceMaxWindowRows   = 8
	piEvidenceMaxPlaybookRows = 5
	piEvidenceLogBefore       = 2 * time.Minute
	piEvidenceLogAfter        = 5 * time.Minute
	piEvidenceWarnBefore      = time.Minute
	piEvidenceWarnAfter       = 2 * time.Minute
	piEvidenceMaxServiceLines = 40
	piEvidenceLineRunes       = 2000
	piEvidenceMsgRunes        = 300
	piEvidenceNoteRunes       = 160
)

// PIEvidenceGatherer 是取证采集器。
type PIEvidenceGatherer struct {
	db *sql.DB
	// searchLogs 可替换（测试注入）；nil ⇒ 用进程内日志缓冲。
	searchLogs func(logging.RecentQuery) logging.RecentResult
	nowFn      func() time.Time
}

// NewPIEvidenceGatherer 创建取证采集器。db 与 PI 分析用同一个库。
func NewPIEvidenceGatherer(db *sql.DB) *PIEvidenceGatherer {
	return &PIEvidenceGatherer{db: db, nowFn: time.Now}
}

// compile-time：采集器满足域包的取证接口。
var _ promptinjection.EvidenceProvider = (*PIEvidenceGatherer)(nil)

// Gather implements promptinjection.EvidenceProvider.
//
//	永不因为「某一路取不到」而返回 error——那些情况体现在 Notes 里。
//	返回 error 的唯一情形是采集器根本没配库。
func (g *PIEvidenceGatherer) Gather(ctx context.Context, target promptinjection.EvidenceTarget) (*promptinjection.EvidenceBundle, error) {
	if g == nil || g.db == nil {
		return nil, errors.New("pi evidence: database not configured")
	}
	started := g.now()
	b := &promptinjection.EvidenceBundle{GatheredAt: started}
	// ★ 总预算（见 piEvidenceTotalTimeout）：调用方给了更早的截止时间 ⇒ 以它为准。
	budget := piEvidenceBudget{deadline: time.Now().Add(piEvidenceTotalTimeout), total: piEvidenceTotalTimeout}
	if dl, ok := ctx.Deadline(); ok && dl.Before(budget.deadline) {
		budget.deadline, budget.total = dl, time.Until(dl)
	}
	run := func(name string, fn func(context.Context) error) { g.step(b, name, budget, fn) }
	if target.TenantID == "" {
		target.TenantID = "default"
	}

	requestErrKind := ""
	if target.RequestID != "" {
		// ① 请求账本。
		run("request ledger", func(c context.Context) error {
			row, err := g.requestRow(c, target)
			if err != nil {
				return err
			}
			if row == nil {
				b.Notes = append(b.Notes, "request ledger: no row found for this request (not persisted yet, or already moved out of the hot table)")
				return nil
			}
			b.Request = row
			requestErrKind, _ = row["error_kind"].(string)
			return nil
		})
		// ② 上游尝试。
		run("upstream attempts", func(c context.Context) error {
			part, err := g.attemptRows(c, target)
			if err != nil {
				return err
			}
			b.Attempts = part.Rows
			switch {
			case part.Degraded != "":
				b.NoteDegraded("upstream attempts", "upstream attempts: "+part.Degraded)
			case len(part.Rows) == 0:
				b.Notes = append(b.Notes, "upstream attempts: none recorded (guard blocks do not go through the candidate-failure channel)")
			}
			return nil
		})
	} else {
		b.NoteDegraded("request ledger", "queue item has no request id: the request ledger and upstream attempts cannot be looked up")
	}

	// ③ 会话上下文。
	if target.SessionID != "" {
		run("session context", func(c context.Context) error {
			rows, err := g.sessionRows(c, target)
			if err != nil {
				return err
			}
			b.Session = rows
			return nil
		})
	}

	// ④ 同型错误分布：请求失败用上游错误类型；其余用请求账本读到的 error_kind。
	kind := piEvidenceString(target.ErrorDetails, "error_kind")
	if kind == "" {
		kind = requestErrKind
	}
	if kind != "" {
		finalKind := kind
		run("same-kind error distribution", func(c context.Context) error {
			part, err := g.errorWindowRows(c, target.TenantID, finalKind)
			if err != nil {
				return err
			}
			b.ErrorWindow = part.Rows
			if part.Degraded != "" {
				b.NoteDegraded("error_window", "same-kind error distribution: "+part.Degraded)
			}
			return nil
		})
	}

	// ⑤ 服务日志（进程内缓冲：不出网、不查库，不会失败）。
	b.ServiceLogs, b.ServiceLogMeta = g.serviceLogs(target)

	// ⑥ 错误处理手册（可选系统：表不在场 ⇒ 标成「这一路没算出来」，不冒充「没有条目」）。
	for _, et := range piEvidenceDedupe([]string{target.ErrorType, piEvidenceString(target.ErrorDetails, "error_kind")}) {
		c, cancel, ok := budget.stepCtx()
		if !ok {
			b.NoteDegraded("playbook", budget.note("playbook"))
			break
		}
		part, err := g.playbookEntries(c, et)
		cancel()
		if err != nil {
			b.NoteDegraded("playbook", budget.failureNote("playbook", err))
			break
		}
		if part.Degraded != "" {
			b.NoteDegraded("playbook", "playbook: "+part.Degraded)
			break
		}
		b.Playbook = append(b.Playbook, part.Rows...)
		if len(b.Playbook) >= piEvidenceMaxPlaybookRows {
			b.Playbook = b.Playbook[:piEvidenceMaxPlaybookRows]
			break
		}
	}

	b.DurationMs = int(g.now().Sub(started).Milliseconds())
	return b, nil
}

// piEvidenceBudget 是一次取证的**总**预算：截止时间 + 实际生效的时长（写进说明里）。
type piEvidenceBudget struct {
	deadline time.Time
	total    time.Duration
}

// note 是「预算用尽」的说明（写出实际生效的预算，不写死默认值）。
func (bu piEvidenceBudget) note(name string) string {
	return fmt.Sprintf("%s: evidence budget exhausted (limit %s); this part of the evidence is missing",
		name, bu.total.Round(time.Millisecond))
}

// timeoutNote 是「这一步的截止时间到了」的说明：要分「这一路自己超时」与「总预算用尽」——
// 两者的处置不同（前者调大单路上限，后者是库里当时就是慢）。
func (bu piEvidenceBudget) timeoutNote(name string) string {
	if !time.Now().Before(bu.deadline) {
		return bu.note(name)
	}
	return fmt.Sprintf("%s: query timed out (limit %s); this part of the evidence is missing", name, piEvidenceStepTimeout)
}

// failureNote 把一步取证的**非超时**错误写成说明。
func (bu piEvidenceBudget) failureNote(name string, err error) string {
	return fmt.Sprintf("%s unavailable: %s", name, piEvidenceTruncate(err.Error(), piEvidenceNoteRunes))
}

// isStepTimeout 判定一步取证是否因截止时间而失败。
//
//	★ 看**上下文是否已到期**，不看错误内容：截止时间到了之后，驱动报什么错各不相同
//	  （pgx 报 context deadline exceeded、pq / sqlmock 报 canceling query due to user
//	  request）⇒ 按错误内容判会把超时归成普通的「不可用」，把「库里当时慢」写成「查询出错」。
func isStepTimeout(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() != nil
}

// stepCtx 给一步取证造上下文。
//
//	★ 不继承调用方的取消：按需取证的 HTTP 请求断开不该把已经发出去的只读查询半途
//	  掐掉——那只会让「为什么没取到」多一种难以复现的原因。但**总预算必须管用**：
//	  预算已用尽 ⇒ ok=false（调用方标「预算用尽」）；否则这一步的截止时间取
//	  「自己的上限」与「总预算」中更早的那个。
func (bu piEvidenceBudget) stepCtx() (context.Context, context.CancelFunc, bool) {
	now := time.Now()
	if !now.Before(bu.deadline) {
		return nil, nil, false
	}
	stepDeadline := now.Add(piEvidenceStepTimeout)
	if bu.deadline.Before(stepDeadline) {
		stepDeadline = bu.deadline
	}
	ctx, cancel := context.WithDeadline(context.Background(), stepDeadline)
	return ctx, cancel, true
}

// step 跑一路取证：独立超时 + panic 收口；失败只在 Notes 里留一句。
func (g *PIEvidenceGatherer) step(b *promptinjection.EvidenceBundle, name string, budget piEvidenceBudget, fn func(ctx context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			b.NoteDegraded(name, fmt.Sprintf("%s: internal error: %v", name, r))
			slog.Error("pi_analysis: evidence step panicked", "step", name, "panic", r)
		}
	}()
	ctx, cancel, ok := budget.stepCtx()
	if !ok {
		b.NoteDegraded(name, budget.note(name))
		return
	}
	defer cancel()
	started := g.now()
	err := fn(ctx)
	slog.Debug("pi_analysis: evidence step done", "step", name,
		"ms", g.now().Sub(started).Milliseconds(), "has_error", err != nil)
	if err == nil {
		return
	}
	if isStepTimeout(ctx, err) {
		b.NoteDegraded(name, budget.timeoutNote(name))
		return
	}
	b.NoteDegraded(name, budget.failureNote(name, err))
}

func (g *PIEvidenceGatherer) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn()
	}
	return time.Now()
}

// ── ① 请求账本 ─────────────────────────────────────────────────────────────

// piEvidenceRequestCols 是请求账本行的投影。两处口径与 admin/logs.go 一致：
// 读面取 logsSourceFromSQL()（设置项可切到会话族原生读面）、request_status
// 用 requestLogStatusExpr（历史行 success 为 NULL 时不出假状态）。
//
// ★ 这份清单与 requestRow 的 Scan 目标**逐项、按序**对应（TestEvidenceRequestSelectListMatchesScan
// 从本常量文本数出列数，与测试夹具逐列核对）——2026-11-26 在 211 上实测过一次脱节：
// 清单里有 rl.is_auto_request 而 Scan 少一个目标 ⇒ database/sql 报
// 「expected 23 destination arguments in Scan, not 22」⇒ 请求账本整路降级。
// sqlmock 看不出来：模拟行是手写的，列数跟着 Scan 走、而不是跟着 SQL 走。
const piEvidenceRequestCols = `
	rl.ts, ` + requestLogStatusExpr + ` AS request_status, rl.success, rl.error_kind,
	rl.failure_stage, rl.failure_detail_code, rl.upstream_status_code,
	rl.client_model, rl.outbound_model, rl.canonical_model, rl.provider_id, rl.credential_id,
	rl.client_protocol, rl.client_endpoint, rl.agent_name, rl.latency_ms,
	rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
	rl.stream_interrupted, rl.routing_attempts, rl.is_auto_request,
	LEFT(COALESCE(rl.request_preview, ''), 300) AS request_preview`

// requestRow 返回请求账本行；没有这一行 ⇒ (nil, nil)。
func (g *PIEvidenceGatherer) requestRow(ctx context.Context, target promptinjection.EvidenceTarget) (map[string]any, error) {
	var (
		ts                                          time.Time
		status                                      sql.NullString
		success                                     sql.NullBool
		errKind, failStage, failDetail              sql.NullString
		upstreamStatus                              sql.NullInt64
		clientModel, outboundModel, canonicalModel  sql.NullString
		providerID, credID                          sql.NullInt64
		protocol, endpoint, agent                   sql.NullString
		latency, promptTok, completionTok, totalTok sql.NullInt64
		streamInterrupted                           sql.NullBool
		// routing_attempts 在读面里是 **jsonb**（211 实测；2026-11-25 对着真实列类型核过）——
		// 按整数扫会让这一路稳定报错。可空 JSONB 一律用 NullableJSON（能吃 NULL）。
		routingAttempts NullableJSON
		isAuto          sql.NullBool
		preview         sql.NullString
	)
	err := g.db.QueryRowContext(ctx, `SELECT `+piEvidenceRequestCols+`
		FROM `+logsSourceFromSQL()+`
		WHERE rl.request_id = $1 AND rl.tenant_id = $2
		ORDER BY rl.ts DESC LIMIT 1`, target.RequestID, target.TenantID).
		Scan(&ts, &status, &success, &errKind, &failStage, &failDetail, &upstreamStatus,
			&clientModel, &outboundModel, &canonicalModel, &providerID, &credID,
			&protocol, &endpoint, &agent, &latency,
			&promptTok, &completionTok, &totalTok,
			&streamInterrupted, &routingAttempts, &isAuto, &preview)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row := map[string]any{"ts": ts.Format(time.RFC3339)}
	// 空值不出现这个键（不是 "" 占位、也不是 null 占位）——与下面的数值列同一条口径；
	// 「这一列是空的」与「这一列有值但为空串」在读面上必须分得开。
	piEvidencePutStr(row, "request_status", status)
	piEvidencePutStr(row, "error_kind", errKind)
	piEvidencePutStr(row, "failure_stage", failStage)
	piEvidencePutStr(row, "failure_detail_code", failDetail)
	piEvidencePutStr(row, "client_model", clientModel)
	piEvidencePutStr(row, "outbound_model", outboundModel)
	piEvidencePutStr(row, "canonical_model", canonicalModel)
	piEvidencePutStr(row, "client_protocol", protocol)
	piEvidencePutStr(row, "client_endpoint", endpoint)
	piEvidencePutStr(row, "agent_name", agent)
	piEvidencePutStr(row, "request_preview", sql.NullString{
		Valid: preview.Valid, String: piEvidenceTruncate(preview.String, piEvidenceMsgRunes)})
	if success.Valid {
		row["success"] = success.Bool
	}
	piEvidencePutInt(row, "upstream_status_code", upstreamStatus)
	piEvidencePutInt(row, "provider_id", providerID)
	piEvidencePutInt(row, "credential_id", credID)
	piEvidencePutInt(row, "latency_ms", latency)
	piEvidencePutInt(row, "prompt_tokens", promptTok)
	piEvidencePutInt(row, "completion_tokens", completionTok)
	piEvidencePutInt(row, "total_tokens", totalTok)
	if len(routingAttempts) > 0 && string(routingAttempts) != "null" {
		// routing_attempts 的真实形状（211 实测）：
		//   {"attempts": [{"seq":1,"result":"pending","raw_model":"…","provider_id":14,
		//                  "credential_id":21,"provider_name":"…","error_message":"…"}, …]}
		// 只提炼「几次尝试、各自落到哪个供应商、结果如何」——不带整份载荷进提示词。
		row["routing_attempts"], row["routing_outcomes"] = piEvidenceRoutingSummary(routingAttempts)
	}
	if streamInterrupted.Valid {
		row["stream_interrupted"] = streamInterrupted.Bool
	}
	if isAuto.Valid {
		row["is_auto_request"] = isAuto.Bool
	}
	return row, nil
}

// ── ② 上游尝试 ─────────────────────────────────────────────────────────────

// piEvidencePart 是一路查询的结果：Rows 是算出来的行；Degraded 非空 ⇒ 这一路
// **没算出来**（原因在里面），调用方不得把它读成「确实是空」。
type piEvidencePart struct {
	Rows     []map[string]any
	Degraded string
}

// piEvidenceNotProvisioned 是「这张表在本部署上不存在」的统一说明（可选系统缺席）。
const piEvidenceNotProvisioned = "table is not provisioned on this deployment"

func (g *PIEvidenceGatherer) attemptRows(ctx context.Context, target promptinjection.EvidenceTarget) (piEvidencePart, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT ts, provider_id::text, COALESCE(credential_id::text, ''), COALESCE(raw_model_name, ''),
		       COALESCE(attempt_index, 0), error_kind, COALESCE(error_message, ''),
		       COALESCE(extracted_upstream_status_code, upstream_status_code, 0),
		       COALESCE(retryable, false), COALESCE(latency_ms, 0)
		FROM candidate_failure_logs_hot
		WHERE request_id = $1 AND tenant_id = $2
		ORDER BY ts DESC, attempt_index DESC
		LIMIT $3`, target.RequestID, target.TenantID, piEvidenceMaxAttemptRows)
	if err != nil {
		if isPIRelationMissing(err) {
			// 表不在场的部署：如实标成「这一路没算出来」，不冒充「没有失败记录」。
			return piEvidencePart{Degraded: piEvidenceNotProvisioned}, nil
		}
		return piEvidencePart{}, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			// ts 在真实表里是可空列（information_schema 实测 is_nullable = YES）⇒ 用 NullString 同款的
			// NullTime 接；按 time.Time 接会在遇到 NULL 行时让整路报错。
			ts        sql.NullTime
			provider  string
			cred      string
			model     string
			attempt   int
			kind      string
			message   string
			code      int
			retryable bool
			latency   int
		)
		if err := rows.Scan(&ts, &provider, &cred, &model, &attempt, &kind, &message, &code, &retryable, &latency); err != nil {
			return piEvidencePart{}, err
		}
		if !ts.Valid {
			continue // 没有时间的失败记录不进材料（也不编造时间）
		}
		out = append(out, map[string]any{
			"ts": ts.Time.Format(time.RFC3339), "provider_id": provider, "credential_id": cred,
			"model": model, "attempt_index": attempt, "error_kind": kind,
			"error_message": piEvidenceTruncate(message, piEvidenceMsgRunes), "upstream_status_code": code,
			"retryable": retryable, "latency_ms": latency,
		})
	}
	if err := rows.Err(); err != nil {
		return piEvidencePart{}, err
	}
	return piEvidencePart{Rows: out}, nil
}

// ── ③ 会话上下文 ───────────────────────────────────────────────────────────

func (g *PIEvidenceGatherer) sessionRows(ctx context.Context, target promptinjection.EvidenceTarget) ([]map[string]any, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT rl.ts, `+requestLogStatusExpr+` AS request_status,
		       rl.client_model, rl.error_kind, rl.upstream_status_code, rl.latency_ms, rl.total_tokens,
		       (rl.request_id = $2) AS is_target
		FROM `+logsSourceFromSQL()+`
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $3
		  AND rl.ts BETWEEN $4::timestamptz - $5::interval AND $4::timestamptz + $5::interval
		ORDER BY rl.ts ASC
		LIMIT $6`,
		target.SessionID, target.RequestID, target.TenantID,
		target.ErrorAt.UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("%d seconds", int(piEvidenceSessionSpan.Seconds())),
		piEvidenceMaxSessionRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			ts                   time.Time
			status, model, kind  sql.NullString
			code, latency, total sql.NullInt64
			isTarget             sql.NullBool
		)
		if err := rows.Scan(&ts, &status, &model, &kind, &code, &latency, &total, &isTarget); err != nil {
			return nil, err
		}
		row := map[string]any{
			"ts": ts.Format(time.RFC3339), "request_status": status.String,
			"client_model": model.String, "error_kind": kind.String, "is_target": isTarget.Bool,
		}
		piEvidencePutInt(row, "upstream_status_code", code)
		piEvidencePutInt(row, "latency_ms", latency)
		piEvidencePutInt(row, "total_tokens", total)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ── ④ 同型错误分布 ─────────────────────────────────────────────────────────

func (g *PIEvidenceGatherer) errorWindowRows(ctx context.Context, tenantID, kind string) (piEvidencePart, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT provider_id::text, COALESCE(raw_model_name, ''), error_kind, COUNT(*), MAX(ts)
		FROM candidate_failure_logs_hot
		WHERE ts > NOW() - $2::interval AND tenant_id = $1 AND error_kind = $3
		GROUP BY provider_id, raw_model_name, error_kind
		ORDER BY COUNT(*) DESC
		LIMIT $4`,
		tenantID, fmt.Sprintf("%d seconds", int(piEvidenceWindowSpan.Seconds())), kind, piEvidenceMaxWindowRows)
	if err != nil {
		if isPIRelationMissing(err) {
			return piEvidencePart{Degraded: piEvidenceNotProvisioned}, nil
		}
		return piEvidencePart{}, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			provider, model, ek string
			count               int64
			last                time.Time
		)
		if err := rows.Scan(&provider, &model, &ek, &count, &last); err != nil {
			return piEvidencePart{}, err
		}
		out = append(out, map[string]any{
			"provider_id": provider, "model": model, "error_kind": ek,
			"count": count, "last_ts": last.Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return piEvidencePart{}, err
	}
	return piEvidencePart{Rows: out}, nil
}

// ── ⑤ 服务日志 ─────────────────────────────────────────────────────────────

// serviceLogs 从进程内日志缓冲里取与该错误相关的行（新 → 旧）。
//
//	两遍检索：先按请求号精确找（INFO 及以上），再取错误发生时刻附近的
//	WARN/ERROR——多数检测失败的日志行**不带请求号**，只能按时间窗找。
//	缓冲的覆盖窗口两端随元数据返回：缓冲起点晚于要查的窗口时明说
//	「那段时间的日志已经不在了」，不让「没搜到」被读成「没发生」。
func (g *PIEvidenceGatherer) serviceLogs(target promptinjection.EvidenceTarget) ([]promptinjection.ServiceLogLine, map[string]any) {
	search := g.searchLogs
	if search == nil {
		search = logging.SearchRecent
	}
	meta := map[string]any{"available": true, "scanned": 0}
	from := target.ErrorAt.Add(-piEvidenceLogBefore)
	to := target.ErrorAt.Add(piEvidenceLogAfter)

	lines := []promptinjection.ServiceLogLine{}
	scanned := 0
	if target.RequestID != "" {
		res := search(logging.RecentQuery{From: from, To: to, MustContain: []string{target.RequestID}, Limit: piEvidenceMaxServiceLines})
		scanned += res.Scanned
		meta["by_request_id"] = len(res.Lines)
		lines = append(lines, piEvidenceToServiceLines(res.Lines)...)
	}
	if len(lines) < piEvidenceMaxServiceLines {
		res := search(logging.RecentQuery{
			From: target.ErrorAt.Add(-piEvidenceWarnBefore), To: target.ErrorAt.Add(piEvidenceWarnAfter),
			MinLevel: slog.LevelWarn, Limit: piEvidenceMaxServiceLines,
		})
		scanned += res.Scanned
		meta["warn_error_near"] = len(res.Lines)
		for _, l := range piEvidenceToServiceLines(res.Lines) {
			if len(lines) >= piEvidenceMaxServiceLines {
				break
			}
			if piEvidenceContainsLine(lines, l) {
				continue
			}
			lines = append(lines, l)
		}
	}
	piEvidenceSortLines(lines)
	meta["scanned"] = scanned

	// 缓冲的覆盖窗口（一次空查询即返回两端）。
	bounds := search(logging.RecentQuery{Limit: 1})
	if bounds.OldestAt == nil {
		meta["available"] = false
		meta["note"] = "log buffer on this instance is empty (process just started)"
		return lines, meta
	}
	meta["oldest_at"] = bounds.OldestAt.Format(time.RFC3339)
	if bounds.NewestAt != nil {
		meta["newest_at"] = bounds.NewestAt.Format(time.RFC3339)
	}
	switch {
	case bounds.OldestAt.After(to):
		meta["covers_window"] = false
		meta["note"] = "logs on this instance are only kept after start-up; lines from when the error happened are no longer in the buffer"
	case len(lines) == 0:
		meta["covers_window"] = true
		meta["note"] = "no log line in the time window mentions this request id or is at WARN/ERROR level"
	default:
		meta["covers_window"] = true
	}
	return lines, meta
}

func piEvidenceToServiceLines(in []logging.RecentLine) []promptinjection.ServiceLogLine {
	out := make([]promptinjection.ServiceLogLine, 0, len(in))
	for _, l := range in {
		out = append(out, promptinjection.ServiceLogLine{
			Time: l.Time, Level: l.Level, Line: piEvidenceTruncate(l.Line, piEvidenceLineRunes),
		})
	}
	return out
}

func piEvidenceContainsLine(lines []promptinjection.ServiceLogLine, l promptinjection.ServiceLogLine) bool {
	for _, x := range lines {
		if x.Time.Equal(l.Time) && x.Line == l.Line {
			return true
		}
	}
	return false
}

// piEvidenceSortLines 按时间「新 → 旧」排（行数 ≤ 40，插入排序足够）。
func piEvidenceSortLines(lines []promptinjection.ServiceLogLine) {
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j].Time.After(lines[j-1].Time); j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
}

// ── ⑥ 错误处理手册 ─────────────────────────────────────────────────────────

func (g *PIEvidenceGatherer) playbookEntries(ctx context.Context, errorType string) (piEvidencePart, error) {
	rows, err := g.db.QueryContext(ctx, `
		SELECT id, title, root_cause, plan_kind, enabled, auto_execute,
		       hit_count, success_count, failure_count, last_status
		FROM error_playbook_entries
		WHERE error_type = $1
		ORDER BY confidence DESC, id ASC
		LIMIT $2`, errorType, piEvidenceMaxPlaybookRows)
	if err != nil {
		if isPIRelationMissing(err) {
			// 手册是可选系统：表不在场 ⇒ 如实标成「这一路没算出来」（不是「没有同型条目」）。
			return piEvidencePart{Degraded: piEvidenceNotProvisioned}, nil
		}
		return piEvidencePart{}, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id             int64
			title, root    string
			kind           string
			enabled, auto  bool
			hits, ok, fail int64
			// last_status 是可空列（995：`last_status TEXT`，从未执行过的条目为 NULL）
			// ⇒ 必须用 NullString 接；按 string 接会在真实数据上整路报错。
			last sql.NullString
		)
		if err := rows.Scan(&id, &title, &root, &kind, &enabled, &auto, &hits, &ok, &fail, &last); err != nil {
			return piEvidencePart{}, err
		}
		out = append(out, map[string]any{
			"id": id, "title": title, "root_cause": piEvidenceTruncate(root, piEvidenceMsgRunes), "plan_kind": kind,
			"enabled": enabled, "auto_execute": auto, "hit_count": hits, "success_count": ok,
			"failure_count": fail, "last_status": last.String,
		})
	}
	if err := rows.Err(); err != nil {
		return piEvidencePart{}, err
	}
	return piEvidencePart{Rows: out}, nil
}

// ── 小工具 ─────────────────────────────────────────────────────────────────

// piEvidencePutStr 在文本列非空（非 NULL、非空串）时才写入。
func piEvidencePutStr(m map[string]any, key string, v sql.NullString) {
	if v.Valid && v.String != "" {
		m[key] = v.String
	}
}

func piEvidencePutInt(m map[string]any, key string, v sql.NullInt64) {
	if v.Valid {
		m[key] = v.Int64
	}
}

func piEvidenceString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func piEvidenceDedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// piEvidenceTruncate 按 rune 截断（不切坏多字节字符）。
func piEvidenceTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// isPIRelationMissing 报告 err 是否是「表不在场」（可选系统的表缺席不是故障）。
//
//	★ 判据必须窄：只认「关系不存在」（SQLSTATE 42P01 的消息形态
//	  `relation "…" does not exist`）。2026-11-26 真库契约测试实测：此前写成
//	  「消息里含 does not exist 就算」⇒ `column "…" of relation "…" does not exist`
//	  （42P03，列缺失——那是**真实的结构不匹配**）也被当成「表没部署」而静默降级，
//	  把缺陷藏成「本部署没有这个可选系统」。
func isPIRelationMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// 列缺失（42703）的消息是 `column "c" of relation "t" does not exist` / `column "c" does not exist`
	// ⇒ 里面同样带着 `relation "…" does not exist` ⇒ 必须先排除，再认表缺失。
	if strings.Contains(msg, `column "`) {
		return false
	}
	return strings.Contains(msg, "42P01") || piRelationMissingRE.MatchString(msg)
}

// piRelationMissingRE 匹配 PG 的 undefined_table 消息（`relation "x" does not exist`）。
var piRelationMissingRE = regexp.MustCompile(`relation "[^"]+" does not exist`)

// piEvidenceRoutingSummary 把 routing_attempts（jsonb）提炼成 (尝试次数, 逐次摘要)。
//
//	形状不认识时不猜：尝试次数给 -1、摘要给截短后的原文。
func piEvidenceRoutingSummary(raw NullableJSON) (any, []string) {
	var doc struct {
		Attempts []map[string]any `json:"attempts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Attempts == nil {
		// 顶层数组的旧形状也认。
		var legacy []map[string]any
		if json.Unmarshal(raw, &legacy) == nil && legacy != nil {
			doc.Attempts = legacy
		} else {
			return -1, []string{piEvidenceTruncate(string(raw), piEvidenceMsgRunes)}
		}
	}
	outcomes := make([]string, 0, 8)
	for i, a := range doc.Attempts {
		if i >= 8 {
			break
		}
		provider := a["provider_name"]
		if provider == nil || provider == "" {
			provider = a["provider_id"]
		}
		outcomes = append(outcomes, piEvidenceTruncate(fmt.Sprintf("#%v %v → %v %v",
			a["seq"], provider, a["result"], a["error_message"]), 120))
	}
	return len(doc.Attempts), outcomes
}
