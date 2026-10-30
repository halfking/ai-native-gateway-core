package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ★★★ 审核单的「原始上下文」补齐（2026-10-29 输出合规误报轮 · owner 反馈
// 「审批列表里看不到原始的上下文，搞不明白是什么原因被拦截，无法进行审批」）。
//
// ★ 缺口本体（211 生产实测）：★ 审核单表里**能说明「为什么」的字段全靠拦截点
//   自报**（rule / reason / field / digest）⇒ ★★ 而历史记录的这四个字段**全是
//   空的**（拦截点没说话）⇒ ★★ 于是审核面**既没有「为什么」也没有「拦了什么」**
//   ⇒ ★★ 审核员只能看到一串 ID ⇒ ★★ 「无法进行审批」★。
//
// ★ 做法：**读时补**（★ 不是把载荷写进审核表 —— 917 的契约是「审核记录表不含
//   载荷原文」；★ 这里是**联表读**，不落库）★
//
//	① 原始上下文：★ 从请求日志（会话族读面）按 request_id 取回这次请求的
//	   **原始上下文**：请求预览 / 响应预览 / 模型 / 会话 ID / token 数 /
//	   失败阶段 / 失败码 ⇒ ★★ 审核员至少能看到「这次请求长什么样、走的哪个
//	   模型、失败在哪个阶段」。
//	② 归因（attribution）：★★ 审核单没有规则时，用两个**事实**判「这次拦截
//	   是不是内容规则命中」：★ ① 请求日志的 error_kind（output_policy_blocked
//	   ⇒ 拦截发生在**出站侧**）；★ ② 邻近时段的**出站规则事件**里**有没有阻断
//	   动作** ⇒ ★★ 没有 ⇒ ★★ 判定为「守卫侧拦截（非内容规则命中）」★★ 这正是
//	   误报轮的典型形态（守卫装不下 ⇒ 被当成违规）⇒ ★★ 审核员看到这一句就能
//	   判「这是误报」，而不用猜。
//
// ★ 边界：★★ 候选规则是**候选**（时间窗匹配，★ 不是事实）⇒ ★★ 措辞与字段名
//   都必须带上「候选」，★ 不许把它写成「命中的规则」★★。

// blockReviewContext 是审核单的原始上下文（读时补齐，不落库）。
type blockReviewContext struct {
	// RequestPreview 是这次请求的**原始上下文**（请求体预览，有界）。
	RequestPreview string `json:"request_preview,omitempty"`
	// ResponsePreview 是响应的预览（★ 多数被拦请求这里为空：★ 响应在落库前
	// 就被拦住了 ⇒ ★★ 这正是「拦了什么」拿不到的原因）。
	ResponsePreview string `json:"response_preview,omitempty"`
	// Model 是这次请求走的模型。
	Model string `json:"model,omitempty"`
	// SessionID 是原始会话的 ID（★ 审核员可以据此去会话里看全文）。
	SessionID string `json:"session_id,omitempty"`
	// ErrorKind / FailureStage / FailureDetailCode 是这次请求的失败口径。
	ErrorKind        string `json:"error_kind,omitempty"`
	FailureStage     string `json:"failure_stage,omitempty"`
	FailureDetail    string `json:"failure_detail_code,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	LatencyMS        int64  `json:"latency_ms,omitempty"`
	ClientProfile    string `json:"client_profile,omitempty"`
	// AttributionKind 是归因结论的**类别**：
	//
	//	rule    —— 审核单自带命中规则（最可信）。
	//	guard   —— 出站侧拦截、且邻近时段无内容规则阻断 ⇒ 守卫侧拦截。
	//	unknown —— 出站侧拦截、但邻近时段**有**内容规则命中 ⇒ 只给候选。
	AttributionKind string `json:"attribution_kind,omitempty"`
	// Attribution 是归因结论（人话，一句话说清「为什么被拦」）。
	Attribution string `json:"attribution,omitempty"`
	// CandidateRules 是邻近时段的出站规则命中（★ **候选**，不是事实）。
	CandidateRules []blockReviewCandidateRule `json:"candidate_rules,omitempty"`
	// CandidateRulesTruncated 表示候选规则**可能被读取上限截断** ⇒ ★★
	// 「候选为空」不能被当成「邻近时段确实没有内容规则阻断」★★。
	CandidateRulesTruncated bool `json:"candidate_rules_truncated,omitempty"`
}

// blockReviewCandidateRule 是一条候选规则（时间窗匹配 ⇒ 候选，不是事实）。
type blockReviewCandidateRule struct {
	RuleID string `json:"rule_id"`
	Action string `json:"action,omitempty"`
	Status string `json:"status,omitempty"`
	Count  int64  `json:"count,omitempty"`
	// FirstSeen / LastSeen 是这次命中的时间跨度（★ 窗口重叠判定用）。
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// ★ 候选规则的判定窗口：★ 审核单时刻**前后**各一段。
//
// ★ 为什么是这个量级：★ 出站规则的命中事件是**按指纹聚合**的（★ 同一规则
// 反复命中会聚成一行，first_seen/last_seen 覆盖一段时间）⇒ ★★ 窗口太窄会
// 「明明有候选却看不到」，★ 太宽会把不相干的规则塞进来 ⇒ ★★ 取 **±15 分钟**
// —— 覆盖「同一请求的出站扫描发生在拦截前后」的尺度，★ 又不至于把半天的
// 规则全拉进来。
const blockReviewCandidateWindow = 15 * time.Minute

// blockReviewCandidateMax 是**每条审核单展示的候选规则上限**。
//
// ★ 为什么要封顶：★ 候选是**时间窗内**的出站规则命中 ⇒ ★★ 窗口内命中很多条
//
//	规则时（★ 211 实测：★ 一次请求里 server_ip / server_ipv6 / address /
//	network_destination / bearer / password.assignment 都会命中）★★ ⇒ ★★ 全列
//	出来 = 审核员看到一堵墙 ⇒ ★★ 反而读不出重点 ★★ ⇒ ★★ 封顶 + 其余只给
//	计数（★ 但**不隐藏**：★ 「还有 N 条」这句本身就是信息）★★。
const blockReviewCandidateMax = 8

// blockReviewCandidateQueryLimit 是**候选规则的读取上限**（★ 一次查询的条数上限）★。
//
// ★★ 这个上限是「候选可能不完整」的来源（★ 2026-10-30 12h 审计轮 A9）★★
// ⇒ ★★ 因此撞到上限时**必须**把 truncated 标出来 ⇒ ★★ 归因不许把
// 「候选为空」当成「邻近时段确实没有内容规则阻断」★★。
const blockReviewCandidateQueryLimit = 50

// attachBlockReviewContexts 给审核单补齐原始上下文与归因。
//
// ★ 失败语义：★★ **补上下文失败不阻断列表**（★ 审核单列表是主功能，★ 上下文
//
//	是增强）⇒ ★★ 但要留痕（WARN）★★ —— 否则又是一次「静默降级」。
func (h *Handler) attachBlockReviewContexts(ctx context.Context, reviews []sanitizerules.BlockReview) map[int64]*blockReviewContext {
	out := make(map[int64]*blockReviewContext, len(reviews))
	if len(reviews) == 0 || h.db == nil {
		return out
	}
	ids := make([]string, 0, len(reviews))
	seen := map[string]bool{}
	for _, r := range reviews {
		rid := strings.TrimSpace(r.RequestID)
		if rid == "" || seen[rid] {
			continue
		}
		seen[rid] = true
		ids = append(ids, rid)
	}
	if len(ids) == 0 {
		return out
	}
	rows, err := h.loadBlockReviewContextRows(ctx, h.db, ids, reviews)
	if err != nil {
		// ★ 不阻断列表，但必须留痕（★ 静默降级 = 假绿）。
		slog.Warn("sanitize block review context read failed; serving without context",
			"error", err.Error(), "request_ids", len(ids))
		return out
	}
	candidates, candidatesTruncated := h.loadBlockReviewCandidateRules(ctx, reviews)
	for i := range reviews {
		r := reviews[i]
		row, ok := rows[strings.TrimSpace(r.RequestID)]
		if !ok {
			continue
		}
		ctxObj := row.context(r)
		ctxObj.CandidateRules = candidates[r.ID]
		ctxObj.CandidateRulesTruncated = candidatesTruncated
		ctxObj.AttributionKind, ctxObj.Attribution = deriveBlockReviewAttribution(r, ctxObj, candidates[r.ID], candidatesTruncated)
		out[r.ID] = &ctxObj
	}
	return out
}

// blockReviewContextDB 是审核单上下文读面需要的 DB 能力。
//
// ★ 抽成接口（★ 而不是直接用 *pgxpool.Pool）★ ⇒ ★★ 单测可以注入 pgxmock
//
//	（★ 与 admin 包既有的写法一致：★ setCredentialLifecycleStatus 等）★★。
type blockReviewContextDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// blockReviewContextRow 是请求日志里的一行（按 request_id 索引）。
type blockReviewContextRow struct {
	requestID        string
	requestPreview   string
	responsePreview  string
	clientModel      string
	outboundModel    string
	sessionID        string
	errorKind        string
	failureStage     string
	failureDetail    string
	promptTokens     int
	completionTokens int
	latencyMS        int64
	clientProfile    string
}

// context 组装给审核单的上下文对象。
func (row blockReviewContextRow) context(_ sanitizerules.BlockReview) blockReviewContext {
	model := row.outboundModel
	if model == "" {
		model = row.clientModel
	}
	return blockReviewContext{
		RequestPreview:   row.requestPreview,
		ResponsePreview:  row.responsePreview,
		Model:            model,
		SessionID:        row.sessionID,
		ErrorKind:        row.errorKind,
		FailureStage:     row.failureStage,
		FailureDetail:    row.failureDetail,
		PromptTokens:     row.promptTokens,
		CompletionTokens: row.completionTokens,
		LatencyMS:        row.latencyMS,
		ClientProfile:    row.clientProfile,
	}
}

// loadBlockReviewContextRows 按 request_id 批量取这次请求的原始上下文。
//
// ★ 读面用**会话族**（★ session_turns —— v1 族 `_request_logs*` 已整体退役，
// ★ 见 db/request_logs_view_schema.go 的 canonical视图注释）★
// ★ 预览字段**有界**（★ 请求预览本身已经是库里裁过的头几百字节，★ 这里再
// 截一道 ⇒ 不把整段正文带进管理面）。
func (h *Handler) loadBlockReviewContextRows(ctx context.Context, db blockReviewContextDB, requestIDs []string, reviews []sanitizerules.BlockReview) (map[string]blockReviewContextRow, error) {
	out := map[string]blockReviewContextRow{}
	if len(requestIDs) == 0 {
		return out, nil
	}
	// ★ 分区下界：★ 会话族按 partition_date 分区 ⇒ ★★ 不给下界就会扫全部分区
	//   （db/request_logs_view_schema.go 记着这个坑）★★ ⇒ 用「审核单里的
	//   最早/最晚时刻」推月份边界（★ 多留一个月，★ 覆盖跨月与分区归属偏移）。
	pdFrom, pdTo := blockReviewPartitionBounds(reviews)
	// ★★ 租户门（2026-10-30 12h 审计轮）★★：★★ request_id 不是全局唯一 ★★
	//   —— UNIQUE (tenant_id, request_id, partition_date) —— 且**由客户端决定**
	//   （X-Request-Id）★★ ⇒ ★★ 只按 request_id 查 ⇒ ★★ 别的租户用同一个
	//   request_id 就会被一起查出来 ⇒ ★★ 审核员看到别家的请求预览 / 会话 ID ★★
	//   ⇒ ★★ 且结果 map 以 request_id 为键 ⇒ ★★ 后写覆盖先写 ⇒ ★★ 归因被带偏 ★★
	// ⇒ ★★ 必须同时按租户收窄；★★ 租户集合为空 ⇒ ★★ fail-closed（返回空）★★
	tenantIDs := blockReviewTenantIDs(reviews)
	if len(tenantIDs) == 0 {
		return out, nil
	}
	q := `SELECT request_id,
	COALESCE(request_preview, ''), COALESCE(response_preview, ''),
	COALESCE(client_model, ''), COALESCE(outbound_model, ''),
	COALESCE(gw_session_id, ''), COALESCE(error_kind, ''),
	COALESCE(failure_stage, ''), COALESCE(failure_detail_code, ''),
	COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
	COALESCE(latency_ms, 0), COALESCE(client_profile, '')
FROM ` + dbpkg.SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo) + `
WHERE request_id = ANY($1) AND tenant_id = ANY($2)`
	rows, err := db.Query(ctx, q, requestIDs, tenantIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row blockReviewContextRow
		if err := rows.Scan(&row.requestID, &row.requestPreview, &row.responsePreview,
			&row.clientModel, &row.outboundModel, &row.sessionID, &row.errorKind,
			&row.failureStage, &row.failureDetail, &row.promptTokens,
			&row.completionTokens, &row.latencyMS, &row.clientProfile); err != nil {
			return nil, err
		}
		out[row.requestID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// blockReviewPartitionBounds 推出会话族读面的分区下界（月份粒度）。
//
// ★ 边界取**审核单自身的时刻**（★ 而不是「现在」★★）⇒ ★★ 否则三个月前的审核单
//
//	一条上下文都取不到（★ 而历史审核单恰恰是最需要看上下文的那批）★★
//
// ★ 各留一个月余量（★ 覆盖跨月与分区归属偏移）★★
func blockReviewPartitionBounds(reviews []sanitizerules.BlockReview) (string, string) {
	from, to := time.Time{}, time.Time{}
	for _, r := range reviews {
		if r.CreatedAt.IsZero() {
			continue
		}
		if from.IsZero() || r.CreatedAt.Before(from) {
			from = r.CreatedAt
		}
		if to.IsZero() || r.CreatedAt.After(to) {
			to = r.CreatedAt
		}
	}
	if from.IsZero() {
		now := time.Now()
		from, to = now.AddDate(0, -1, 0), now.AddDate(0, 1, 0)
	}
	from = from.AddDate(0, -1, 0)
	to = to.AddDate(0, 1, 0)
	// ★★ 日期布局必须是 "2006-01-02"（★ 年-月-**日**）★★。
	//
	// ★★ Go 的 layout 里 "01" 是**月**、"02" 才是日 ⇒ ★★ 写成 "2006-01-01"
	//   会把「日」渲染成「月」⇒ ★★ 2026-11-05 渲染成 '2026-11-11' ⇒ ★★
	//   分区下界被抬高、上界被推后 ⇒ ★★ 落在被跳过那几天的请求行**查不到**
	//   ⇒ ★★ 审核单变成「上下文暂不可用」—— ★★ 正是本功能要消灭的现象 ★★。
	//
	// ★ 本仓其它日期一律用 "2006-01-02"（★ 例：storage/file/request_mirror.go）★★
	return from.Format("'2006-01-02'"), to.Format("'2006-01-02'")
}

// blockReviewTenantIDs 取这批审核单涉及的租户集合（★ 去重、保序）★。
//
// ★★ 为什么必须有它（2026-10-30 12h 审计轮）★★：
//
//	① request_id **不是全局唯一** —— session_turns 的唯一键是
//	   UNIQUE (tenant_id, request_id, partition_date) ⇒ ★★ 只按 request_id 查
//	   ⇒ ★★ 别的租户用同一个 request_id 就会被一起查出来 ⇒ ★★ 审核员会看到
//	   别家的请求预览 / 会话 ID / 模型 / token 数 ★★
//	② request_id **由客户端决定**（X-Request-Id 头，客户端自选值）★
//	③ 候选规则事件表 sanitize_rule_events 的读面**原先没有租户条件** ⇒ ★★
//	   候选来自全租户 ⇒ ★★ 本租户的归因结论被别家的命中记录左右 ★★
//
// ★⇒ ⇒ 两个读面都按这个集合收窄；★ 集合为空 ⇒ ★ fail-closed（返回空）★
func blockReviewTenantIDs(reviews []sanitizerules.BlockReview) []string {
	out := make([]string, 0, len(reviews))
	seen := make(map[string]bool, len(reviews))
	for _, r := range reviews {
		tid := strings.TrimSpace(r.TenantID)
		if tid == "" || seen[tid] {
			continue
		}
		seen[tid] = true
		out = append(out, tid)
	}
	return out
}

// loadBlockReviewCandidateRules 取邻近时段命中的出站规则（★ 候选，不是事实）。
//
// ★★ 返回第二个值 truncated：★★ 候选查询是**有上限**的一次性读取
// ⇒ ★★ 「查回来的条数撞到上限」⇒ ★★ 候选集**可能不完整** ⇒ ★★ 调用方不许把
// 「没查到候选」当成「邻近时段确实没有内容规则阻断」（★ 见 A9）★★。
func (h *Handler) loadBlockReviewCandidateRules(ctx context.Context, reviews []sanitizerules.BlockReview) (map[int64][]blockReviewCandidateRule, bool) {
	out := map[int64][]blockReviewCandidateRule{}
	if len(reviews) == 0 {
		return out, false
	}
	// ★ 时间窗取所有审核单的并集（★ 一次查询，★ 免得每条审核单查一次）。
	from, to := time.Time{}, time.Time{}
	for _, r := range reviews {
		if r.CreatedAt.IsZero() {
			continue
		}
		if from.IsZero() || r.CreatedAt.Before(from) {
			from = r.CreatedAt
		}
		if to.IsZero() || r.CreatedAt.After(to) {
			to = r.CreatedAt
		}
	}
	if from.IsZero() {
		return out, false
	}
	from = from.Add(-blockReviewCandidateWindow)
	to = to.Add(blockReviewCandidateWindow)
	// ★★ 租户门（2026-10-30 12h 审计轮）★★：★★ 候选读面**原先没有租户条件**
	//   ⇒ ★★ 候选来自**全租户** ⇒ ★★ 租户 A 的审核员看到 B 的规则 ID / action /
	//   count ⇒ ★★ 且 A 的归因结论被 B 的命中记录左右 ★★ ⇒ ★★ 且 B 的流量挤占
	//   同一条 50 条预算 ⇒ ★★ A 的审核单更容易被误判成「守卫侧拦截」★★
	// ⇒ ★★ 必须按这批审核单自己的租户集合收窄 ★★
	tenantIDs := blockReviewTenantIDs(reviews)
	if len(tenantIDs) == 0 {
		return out, false
	}
	events, err := sanitizerules.ListEgressRuleEventsInWindow(ctx, tenantIDs, from, to, blockReviewCandidateQueryLimit)
	if err != nil {
		slog.Warn("sanitize block review candidate rules read failed",
			"error", err.Error())
		return out, false
	}
	// ★★ 撞到上限 ⇒ 候选集**可能不完整** ⇒ ★★ 必须如实标出 ★★
	truncated := len(events) >= blockReviewCandidateQueryLimit
	for _, r := range reviews {
		if r.CreatedAt.IsZero() {
			continue
		}
		var hits []blockReviewCandidateRule
		for _, ev := range events {
			// ★ 判据：「**最近一次命中**（last_seen）落在拦截前后 ±窗口内」★★
			//   ⇒ ★★ 不用「区间重叠」（★ 指纹聚合事件的区间能横跨数周 ⇒ ★★ 那个
			//   判据几乎恒真 ⇒ ★★ 候选会被全量历史淹没）★★
			if ev.LastSeen.Before(r.CreatedAt.Add(-blockReviewCandidateWindow)) ||
				ev.LastSeen.After(r.CreatedAt.Add(blockReviewCandidateWindow)) {
				continue
			}
			hits = append(hits, blockReviewCandidateRule{
				RuleID:    ev.RuleID,
				Action:    ev.Action,
				Status:    ev.Status,
				Count:     ev.Count,
				FirstSeen: ev.FirstSeen,
				LastSeen:  ev.LastSeen,
			})
		}
		if len(hits) > 0 {
			out[r.ID] = hits
		}
	}
	return out, truncated
}

// deriveBlockReviewAttribution 判定「这次拦截为什么发生」。
//
// ★ 三档（★ 措辞必须与证据强度匹配 —— ★★ 不许把推断说成事实★★）：
//
//	rule    —— 审核单自带命中规则 ⇒ 直接说规则名与说明。
//	guard   —— 出站侧拦截（error_kind=output_policy_blocked）★ 且邻近时段
//	 **没有**内容规则阻断 ⇒ ★★ 判定为守卫侧拦截（★守卫装不下 ⇒
//	           被当成违规 ⇒ 这就是误报轮的形态）★★
//	unknown —— 出站侧拦截，★ 但邻近时段**有**内容规则命中 ⇒ ★★ 只给候选，
//	结论留给人工。
//
// ★★ truncated（2026-10-30 12h 审计轮 A9）★★：★★ 候选查询有上限 ⇒ ★★
// 「候选为空」有两种成因：**真的没有** / ★★ **被上限截断了**★★ ⇒ ★★
// 后者**不能**被升格成「邻近时段没有任何内容规则阻断 ⇒ 判定为守卫侧拦截」
// ⇒ ★★ 那句话会直接引导审核员标「误报」⇒ ★★ 标到阈值后会自动放行该租户
// 该规则 7 天 ⇒ ★★ 真实的内容规则被悄悄放过去 ★★。
func deriveBlockReviewAttribution(review sanitizerules.BlockReview, c blockReviewContext, candidates []blockReviewCandidateRule, truncated bool) (string, string) {
	rule := strings.TrimSpace(review.Rule)
	if rule != "" {
		name := strings.TrimSpace(review.RuleName)
		if name == "" {
			name = rule
		}
		return "rule", "命中规则：" + name + "（" + rule + "）"
	}
	if c.ErrorKind != "output_policy_blocked" {
		if c.ErrorKind == "" {
			return "unknown", "拦截点未记录命中规则，且请求日志里没有失败口径（无法判断拦截发生在哪一侧）"
		}
		return "unknown", "拦截点未记录命中规则；请求失败口径：" + c.ErrorKind
	}
	if len(candidates) == 0 {
		// ★★ 候选集**被上限截断** ⇒ ★★ 「没查到」≠「不存在」⇒ ★★ 不许在这里
		//   断言「不是内容规则命中」⇒ ★★ 降级为 unknown（★ 结论留给人工）★★
		if truncated {
			return "unknown", "出站侧策略拦截；**拦截点没有记录命中的规则**（历史记录）。" +
				"邻近时段**未查到**出站规则候选，但候选查询已撞到读取上限" +
				"（可能被截断）⇒ **无法据此断言「不是内容规则命中」** ⇒ 请人工判断。" +
				"若响应体很大（本次请求 " + formatBlockReviewTokens(c) + "），" +
				"可能是守卫待检缓冲放不下（误报形态），但请先确认不是候选被截断。"
		}
		return "guard", "出站侧策略拦截；**拦截点没有记录命中的规则**（历史记录），且邻近时段没有任何内容规则阻断 ⇒ 判定为**守卫侧拦截**（不是内容规则命中）。" +
			"若响应体很大（本次请求 " + formatBlockReviewTokens(c) + "），多半是守卫待检缓冲放不下 ⇒属于误报形态，可直接标「误报」。"
	}
	names := make([]string, 0, len(candidates))
	shown := candidates
	hidden := 0
	if len(shown) > blockReviewCandidateMax {
		hidden = len(shown) - blockReviewCandidateMax
		shown = shown[:blockReviewCandidateMax]
	}
	for _, cand := range shown {
		names = append(names, cand.RuleID+"("+cand.Action+")")
	}
	text := "出站侧策略拦截；**拦截点没有记录命中的规则**（历史记录）。邻近时段命中的出站规则（**候选，非事实**）：" +
		strings.Join(names, "、")
	if hidden > 0 {
		text += fmt.Sprintf("（另有 %d 条未列）", hidden)
	}
	return "unknown", text + " —— 请人工判断是否与本次拦截有关。"
}

// formatBlockReviewTokens 把上下文里的 token 数与时延拼成一句人话。
func formatBlockReviewTokens(c blockReviewContext) string {
	parts := make([]string, 0, 3)
	if c.PromptTokens > 0 {
		parts = append(parts, "请求 "+strconv.Itoa(c.PromptTokens)+" tokens")
	}
	if c.CompletionTokens > 0 {
		parts = append(parts, "响应 "+strconv.Itoa(c.CompletionTokens)+" tokens")
	}
	if c.LatencyMS > 0 {
		parts = append(parts, "时长 "+strconv.FormatInt(c.LatencyMS/1000, 10)+"s")
	}
	if len(parts) == 0 {
		return "（无 token/时延读数）"
	}
	return strings.Join(parts, "、")
}

// blockReviewRow 是审核单在管理面的行（★ 审核单本体 + 读时补齐的上下文）。
type blockReviewRow struct {
	sanitizerules.BlockReview
	// Context 是原始上下文与归因（★ 补不齐时为 nil ⇒ 前端要能显示
	// 「上下文暂不可用」而不是空白）。
	Context *blockReviewContext `json:"context,omitempty"`
}
