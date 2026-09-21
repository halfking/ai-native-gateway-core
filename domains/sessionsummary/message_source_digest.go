package sessionsummary

// per-turn digest MessageSource（24h 审计第二轮 Track B#4）。
//
// 审计结论：V1/V2 MessageSource 都把每轮坍缩为最后一条 request 消息（恒为
// user prompt），assistant 回复完全不参与，摘要 LLM 看不到网关实际回复了
// 什么，摘要质量有天花板。本文件新增 per-turn digest 层：每轮产出
// 「user 提问 + assistant 回复要点」的人读摘要，让摘要 LLM 看到双向对话。
//
// 数据源（复用，不重建）：public.session_turns.digest —— 写路径
// （domains/session/v2/session_writer_v2.Write）落库时已经用
// sessiondigest.Build(requestDelta, ResponseBody, ...) 把每轮
// user_input + assistant_output 压缩为人读 envelope（sessiondigest.compact
// 的 260-rune 上限，whitespace 归一），存量 NULL 行由
// session_digest_backfill 后台 job 以相同输入形状回填。因此本层只做一次
// 单视图查询（session_turns_with_current_month，hot ∪ partition，
// admin/turn_digest 同款读面），无需 JOIN session_bodies_unified、无需
// 解析可能巨大的 response_delta 全文 —— 每轮只取 digest 一个必要字段。
// 会话内查询可走 session_turns_hot 的
// idx_session_turns_hot_tenant_session_turn (tenant_id, session_id,
// turn_no DESC) 索引，与既有 admin/backfill 读者同一访问路径，无全表扫描。
//
// 行序 / LIMIT 语义与 V1/V2 源对齐：SQL 侧 ORDER BY ts ASC LIMIT 20
// （同一 20 轮窗口）；每轮最多展开为 2 条 SessionMessage（user + assistant，
// ts 相同、顺序固定 user 在前），空侧跳过。MessageSource 接口文档承诺的
// 「升序、按时间序」不变；总条数上限因此从 20 放宽到 40，摘要 prompt 构造
// （buildSummaryPrompt / buildRollingPrompt 的 maxMessages=10）已有自己的
// 条数上限，这里不重复裁剪。
//
// 截断：持久化 envelope 已在写侧 compact，但本层仍对展开结果做一次
// 防御性 rune 截断（truncateRunes，2026-09-08 b1f9da6ca 修法的同款），
// 上限与 sessiondigest.compact 的 260 对齐——防止未来算法上限放宽或
// 存量行格式漂移把超长文本重新灌进摘要 prompt，且永不产生非法 UTF-8。
//
// 门控：改变摘要输入会改变线上摘要输出，因此本层由 platform 开关
// sessions_summary_per_turn_digest 控制（默认关闭）。开关关闭时
// gatedPerTurnDigestSource 把调用逐字节委托给 V2 源
// （v2SessionBodiesSource），与既有行为完全一致；开启时走 digest 语义。
// 每次调用都重新读开关（settings.GetPlatformBool 无缓存层），支持热更新。

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// perTurnDigestMaxRunes is the defensive per-side cap applied when expanding
// a persisted digest envelope into SessionMessages. Deliberately identical to
// sessiondigest.compact's ceiling so the layer is a no-op for well-formed
// envelopes and only bites on drifted/legacy data.
const perTurnDigestMaxRunes = 260

// digestSourceDB is the minimal pool surface the digest source needs.
// Declared as an interface so unit tests can inject pgxmock (same seam as
// session/v2's digestBackfillDB); production wires a *pgxpool.Pool.
type digestSourceDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// compile-time assertion that the production pool satisfies the seam.
var _ digestSourceDB = (*pgxpool.Pool)(nil)

// perTurnDigestSource is the digest-backed MessageSource: one row per turn
// from session_turns_with_current_month, expanded to user+assistant messages.
type perTurnDigestSource struct {
	pool digestSourceDB
}

// digestTurnRow is one turn's projection from the digest query.
type digestTurnRow struct {
	RequestID string
	Model     string
	Ts        time.Time
	Digest    []byte // raw JSONB envelope; may be NULL/empty
}

// sessionTurnDigestQuery reads the persisted per-turn digest envelope.
// Single view, no JOIN: digest already carries both sides' human-readable
// projection. The WHERE/ORDER/LIMIT shape matches v2SessionBodiesBaseQuery
// (session filter, optional tenant, optional since-ts, ascending ts,
// LIMIT 20) so the turn window is identical to the V1/V2 sources.
const sessionTurnDigestQuery = `
	SELECT
		t.request_id,
		COALESCE(t.model, '') AS model,
		t.ts,
		t.digest
	FROM public.session_turns_with_current_month t
	WHERE t.session_id = $1
`

func (m *perTurnDigestSource) fetchDigestTurns(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]digestTurnRow, error) {
	if m.pool == nil {
		return nil, fmt.Errorf("sessionsummary: per-turn digest source pool is nil")
	}
	query := sessionTurnDigestQuery
	args := []any{sessionKey}
	argN := 2
	if tenantID != "" {
		query += " AND t.tenant_id = $" + strconv.Itoa(argN)
		args = append(args, tenantID)
		argN++
	}
	if !since.IsZero() {
		query += " AND t.ts > $" + strconv.Itoa(argN)
		args = append(args, since)
		argN++
	}
	query += " ORDER BY t.ts ASC LIMIT 20"

	rows, err := m.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []digestTurnRow
	for rows.Next() {
		var r digestTurnRow
		if err := rows.Scan(&r.RequestID, &r.Model, &r.Ts, &r.Digest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// expandDigestTurn turns one persisted digest envelope into the per-turn
// SessionMessage pair (user then assistant). Turns without a usable envelope
// — NULL column (backfill not reached yet), malformed JSON, unsupported
// version — are skipped (ok=false), mirroring how V1/V2 sources skip turns
// without a usable request_delta instead of emitting empty rows.
func expandDigestTurn(r digestTurnRow) []SessionMessage {
	envelope, err := sessiondigest.Unmarshal(r.Digest)
	if err != nil || envelope == nil {
		// err = malformed / unsupported version; nil envelope = SQL NULL or
		// JSON null. Either way the turn contributes no summary input — the
		// digest backfill job fills NULLs with the same Build shape the
		// writer uses, so gaps are transient.
		return nil
	}
	base := SessionMessage{
		RequestID: r.RequestID,
		Model:     r.Model,
		Timestamp: r.Ts,
	}
	var out []SessionMessage
	if user := truncateRunes(envelope.Payload.UserInput, perTurnDigestMaxRunes); user != "" {
		m := base
		m.Role = "user"
		m.Content = user
		out = append(out, m)
	}
	if assistant := truncateRunes(envelope.Payload.AssistantOutput, perTurnDigestMaxRunes); assistant != "" {
		m := base
		m.Role = "assistant"
		m.Content = assistant
		out = append(out, m)
	}
	return out
}

// expandDigestTurns maps every turn row to its user+assistant messages,
// preserving the ascending-ts order the query returned.
func expandDigestTurns(rows []digestTurnRow) []SessionMessage {
	messages := make([]SessionMessage, 0, 2*len(rows))
	for _, r := range rows {
		messages = append(messages, expandDigestTurn(r)...)
	}
	return messages
}

func (m *perTurnDigestSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	turns, err := m.fetchDigestTurns(ctx, tenantID, sessionKey, time.Time{})
	if err != nil {
		return nil, err
	}
	return expandDigestTurns(turns), nil
}

func (m *perTurnDigestSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	turns, err := m.fetchDigestTurns(ctx, tenantID, sessionKey, since)
	if err != nil {
		return nil, err
	}
	return expandDigestTurns(turns), nil
}

// defaultPerTurnDigestEnabled reads the gate flag. settings.GetPlatformBool
// returns the fallback (false) when Global is unset, so tests and tooling
// without an initialized settings registry see the safe default.
func defaultPerTurnDigestEnabled() bool {
	return settings.GetPlatformBool(settings.PerTurnDigestFlagKey, false)
}

// gatedPerTurnDigestSource is what production wires via SetMessageSource:
// flag off → every call is delegated verbatim to the V2 bodies source
// (byte-identical to the pre-B#4 behavior); flag on → the per-turn digest
// semantics. The check runs per call so the platform flag hot-reloads
// without a restart.
type gatedPerTurnDigestSource struct {
	digest   MessageSource
	fallback MessageSource
	enabled  func() bool
}

func (g *gatedPerTurnDigestSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	if g.enabled() {
		return g.digest.GetSessionMessages(ctx, tenantID, sessionKey)
	}
	return g.fallback.GetSessionMessages(ctx, tenantID, sessionKey)
}

func (g *gatedPerTurnDigestSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	if g.enabled() {
		return g.digest.GetMessagesSince(ctx, tenantID, sessionKey, since)
	}
	return g.fallback.GetMessagesSince(ctx, tenantID, sessionKey, since)
}

// NewPerTurnDigestSource constructs the gated per-turn digest MessageSource
// for Summarizer.SetMessageSource. It reads the persisted session_turns.digest
// envelopes (user + assistant per turn) when the
// sessions_summary_per_turn_digest platform flag is on (default off), and
// delegates to the V2 session_bodies source when off. A nil pool yields
// per-call errors rather than a panic, matching the other sources'
// nil-safety contract.
func NewPerTurnDigestSource(pool *pgxpool.Pool) MessageSource {
	return &gatedPerTurnDigestSource{
		digest:   &perTurnDigestSource{pool: pool},
		fallback: &v2SessionBodiesSource{pool: pool},
		enabled:  defaultPerTurnDigestEnabled,
	}
}
