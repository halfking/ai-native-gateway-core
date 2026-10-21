package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/memory"                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/session"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// compressorSessionDisabled returns true when the v3 session compressor is
// explicitly turned off via env. Default = enabled; missing Redis/DB degrades
// the cache to the tiers that are available (at minimum the in-process L1).
func compressorSessionDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE")))
	return v == "1" || v == "true" || v == "yes"
}

// ──────────────────────────────────────────────────────────────────────────────
// Redis backend adapter (session.RedisClient → compression.SessionCacheBackend)
// ──────────────────────────────────────────────────────────────────────────────

type redisBackendAdapter struct {
	c *session.RedisClient
}

func redisBackendFromClient(c *session.RedisClient) compression.SessionCacheBackend {
	if c == nil {
		return nil
	}
	return &redisBackendAdapter{c: c}
}

func (a *redisBackendAdapter) HSet(ctx context.Context, key string, values ...any) error {
	fields := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		k, ok := values[i].(string)
		if !ok {
			continue
		}
		fields[k] = values[i+1]
	}
	return a.c.HSet(ctx, key, fields)
}

func (a *redisBackendAdapter) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	data, err := redissafe.SafeHGetAll(ctx, a.c.Client(), key)
	if err != nil {
		// Preserve the go-redis HGETALL contract this adapter replaces:
		// a missing key is an empty map + nil, i.e. a cache miss. The typed
		// WRONGTYPE error still propagates.
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return data, nil
}

func (a *redisBackendAdapter) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl)
}

func (a *redisBackendAdapter) Del(ctx context.Context, key string) error {
	return a.c.Del(ctx, key)
}

// ──────────────────────────────────────────────────────────────────────────────
// Postgres backend adapter (*db.DB → compression.SessionCacheDB)
// ──────────────────────────────────────────────────────────────────────────────

type pgBackendAdapter struct {
	dbConn *db.DB
}

func dbBackendFromPool(dbConn *db.DB) compression.SessionCacheDB {
	if dbConn == nil || !dbConn.Enabled() {
		return nil
	}
	return &pgBackendAdapter{dbConn: dbConn}
}

// lastOutboundSessionSQL 是压缩会话缓存 L3 冷启动回退的会话族腿（S3 波 2
// 停写切换，见 LastOutboundForSession 注释）。占位符：$1 = 会话 ID（由
// SessionFamilyTurnsForSessionSQL 内推消费），$2 = 租户。提升为包级变量
// 是给形状钉桩用的。
//
// # ★★ 2026-10-21（§143.55，E1 读侧落地）：本式原来在 final_full 门下**必然落空**
//
// 原式是 `JOIN <bodies 源> sb ON sb.request_id = rl.request_id` + `WHERE sb.outbound_body IS NOT NULL`。
// 而 E1（迁移 708 / 开关 `storage.session_final_full_enabled`）的写入形态是：
//
//	逐轮行：outbound_body = **SQL NULL**（`bodiesRec.OutboundBody = nil` ⇒ `jsonTextOrNull(nil)`）
//	快照行：request_id = 'final_full:<session>'、turn_no = 0、kind = 'final_full'
//
// ⇒ 两个条件同时把门关上：
//
//	① JOIN 键：快照行的 request_id 是 `'final_full:<session>'`，**与任何一轮的
//	   request_id 都不相等** ⇒ 快照行永远匹配不上 turns 侧的任何一行；
//	② 过滤：逐轮行的 outbound_body 是 SQL NULL ⇒ `IS NOT NULL` 把它们全部滤掉。
//
// ⇒ 门一开，本式恒返回 **0 行** ⇒ L3 冷启动拿不到任何 outbound ⇒ 压缩会话缓存
// 冷启动退化成「无历史」。
//
// # 修法：三条腿（turns 元数据 + 逐轮正文 + final_full 快照），COALESCE 取优先级
//
//	final_full 优先、逐轮兜底 —— 与 `domains/session/v2/turn_reader.go`
//
// 的 `LoadLatestOutbound`（方案 D2「差集提取改读 final_full；未命中回退旧
// outbound_body」）**同一口径**。
//
// ★ 为什么 final_full 腿用 `session_bodies_unified` 视图、而不是改
// `db.SessionFamilyBodiesSourceSQL()` 的投影：
//
//	那个 helper 的列集合是**合同**（§9.231），它**刻意不投影 ts**，而且它投影的是
//	`request_delta AS request_body` 这类 v1 同形别名 —— 要在它上面按
//	`(tenant_id, session_id, kind)` 定位快照行，就得把 `session_id`/`kind`/`ts`
//	加进它的投影 ⇒ **改的是合同本身**（会波及 admin 的 14 个消费点）。
//	⇒ 而 final_full 腿要的就是这三个列 ⇒ 直接读既有视图
//	（`public.session_bodies_unified` = hot ∪ parent，708 起带 kind 列），
//	与 `turn_reader.go` / `bodies_writer.go` 的既有读法一致。
//
// ★ 为什么用 LEFT JOIN LATERAL 而不是标量子查询：
//
//   - 标量子查询在 SELECT 与 WHERE 里各写一遍 ⇒ **同一子查询被算两次**；
//   - LATERAL 只算一次，且 `LIMIT 1` 保证**不放大行**（一个外层行最多配 1 行）；
//   - LATERAL 的结果列 `ff.outbound_body` 可以同时进 SELECT（COALESCE）与
//     WHERE（"有正文"过滤）⇒ 不用重复求值。
//
// ★ 为什么 WHERE 保留 `sb.outbound_body IS NOT NULL` 这一条（而不是删掉）：
//
//	删掉 ⇒ LEFT JOIN 会把「本轮没有正文」的 turns 行也放进来 ⇒ **门关时结果集
//	被放大**（LEFT JOIN 的右侧无匹配 ⇒ 该 turns 行照样成行）。
//	保留 ⇒ `LEFT JOIN ... WHERE sb.outbound_body IS NOT NULL` 与原来的
//	`INNER JOIN ... WHERE sb.outbound_body IS NOT NULL` **逐行等价**
//	（无匹配的右行被 WHERE 滤掉 ⇒ 等价于内连接）⇒ 门关时语义不变。
//
// ★ 「门关时语义不变」的边界（诚实声明）：
//
//  1. **门关**（库里没有 final_full 行）⇒ 快照腿恒 NULL ⇒ COALESCE 恒取
//     `sb.outbound_body` ⇒ 返回行集与原式**逐行相同**；
//  2. **门开**（有 final_full 行）⇒ 返回 final_full 快照（更新的完整 outbound），
//     这正是 E1 的目的；
//  3. **门曾经开过又关掉**（库里还留着 final_full 行）⇒ 本式仍优先读快照
//     （与 `LoadLatestOutbound` 同口径）⇒ 与原式**不同**（原式只读逐轮）。
//     这是**有意的**：快照行比逐轮行新，且它才是「最后完整 outbound」。
var lastOutboundSessionSQL = `
		SELECT COALESCE(ff.outbound_body, sb.outbound_body),
		       rl.outbound_msg_count, rl.outbound_token_est,
		       rl.outbound_msg_hashes, rl.compression_meta
		FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl
		LEFT JOIN ` + db.SessionFamilyBodiesSourceSQL() + ` sb
		  ON sb.request_id = rl.request_id
		LEFT JOIN LATERAL (
		  SELECT f.outbound_body
		  FROM public.session_bodies_unified f
		  WHERE f.tenant_id = $2
		    AND f.session_id = $1
		    AND f.kind = 'final_full'
		    AND f.outbound_body IS NOT NULL
		    AND f.outbound_body <> 'null'::jsonb
		  ORDER BY f.ts DESC
		  LIMIT 1
		) ff ON TRUE
		WHERE rl.tenant_id = $2
		  AND (sb.outbound_body IS NOT NULL OR ff.outbound_body IS NOT NULL)
		ORDER BY rl.ts DESC
		LIMIT 1
	`

// LastOutboundForSession runs the L3 cold-start fallback query.
//
// 2026-10-13 S3 波 2（停写切换）：gw_session_id 是会话内谓词（A 形态）。
// 原实现只读 v1 母表（outbound 自 Phase 1 起就大多在 bodies 表，
// 该查询本就接近恒空），S4 停写后新会话完全无行。会话族优先：turns 元数据
// 四列（113 列契约内）经 SessionFamilyTurnsForSessionSQL 按 session 内推，
// outbound_body 经 SessionFamilyBodiesSourceSQL 按 request_id 单键 JOIN
// （该源不投影 ts，ts 等值 JOIN 形态禁止）。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**v1 母表兜底腿已删除**。
// 那条腿只在会话族查不到行时触发，而它读的关系已被 DROP ⇒ 42P01 ⇒
// 「会话族没命中」会从「回退到 v1 取历史」变成「整条查询报错」。
// 镜像前的历史会话在会话族里本就没有对应轮次，回退腿取到的内容
// 属于「另一个存储面里的同一段历史」，退役后不可得 ⇒ 直接返回
// pgx.ErrNoRows（调用方按「无 outbound」处理），不制造 42P01。
func (a *pgBackendAdapter) LastOutboundForSession(ctx context.Context, tenantID, gwSessionID string) (*compression.LastOutboundRow, error) {
	if a.dbConn == nil {
		return nil, fmt.Errorf("db pool not configured")
	}
	pool := a.dbConn.Pool()
	if pool == nil {
		return nil, fmt.Errorf("db pool is nil")
	}
	sessionRow := pool.QueryRow(ctx, lastOutboundSessionSQL, gwSessionID, tenantID)
	return scanLastOutboundRow(sessionRow)
}

func scanLastOutboundRow(row interface{ Scan(dest ...any) error }) (*compression.LastOutboundRow, error) {
	var (
		ob     []byte
		mc     *int
		te     *int
		hashes []byte
		meta   []byte
	)
	if err := row.Scan(&ob, &mc, &te, &hashes, &meta); err != nil {
		return nil, err
	}
	out := &compression.LastOutboundRow{}
	if ob != nil {
		out.OutboundBody = json.RawMessage(ob)
	}
	if mc != nil {
		out.OutboundMsgCount = *mc
	}
	if te != nil {
		out.OutboundTokenEst = *te
	}
	if hashes != nil {
		out.OutboundMsgHashes = json.RawMessage(hashes)
	}
	if meta != nil {
		out.CompressionMeta = json.RawMessage(meta)
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Executor → compression.Dependencies
// ──────────────────────────────────────────────────────────────────────────────

// NewDependenciesFromExecutor wires the existing executor's Memora and
// Provider clients into the compression.Dependencies struct. Returns nil
// when either client is missing (LLM summary then degrades to mechanical
// trim — see compression.SessionCompressor).
func NewDependenciesFromExecutor(exec *executors.Executor) *compression.Dependencies {
	if exec == nil {
		return nil
	}
	deps := &compression.Dependencies{}
	if exec.Memora != nil {
		deps.Memora = memoraClientAdapter{c: exec.Memora}
	}
	if exec.Provider != nil {
		deps.Provider = providerClientAdapter{r: exec.Provider}
	}
	return deps
}

// memoraClientAdapter bridges *memclient.Client to the compression.MemoraClient
// interface (Disabled + Search).
type memoraClientAdapter struct {
	c memory.Reader
}

func (m memoraClientAdapter) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m memoraClientAdapter) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func (m memoraClientAdapter) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	return convertMemoraItems(items), nil
}

func convertMemoraItems(items []memory.Memory) []memory.Memory { return items }

type legacyMemoraReader struct {
	c *memclient.Client
}

type legacyMemoraWriter struct {
	s *memclient.Sink
}

func (m legacyMemoraReader) Disabled() bool {
	if m.c == nil {
		return true
	}
	return m.c.Disabled()
}

func (m legacyMemoraReader) Ping(ctx context.Context) error {
	if m.c == nil {
		return nil
	}
	return m.c.Ping(ctx)
}

func (m legacyMemoraReader) BaseURL() string {
	if m.c == nil {
		return ""
	}
	return m.c.BaseURL()
}

func (m legacyMemoraReader) AddMessage(ctx context.Context, userID string, messages []memory.Message, info map[string]any) error {
	if m.c == nil {
		return nil
	}
	legacyMessages := make([]memory.Message, 0, len(messages))
	for i := range messages {
		legacyMessages = append(legacyMessages, memory.Message{
			Role:    messages[i].Role,
			Content: messages[i].Content,
		})
	}
	return m.c.AddMessage(ctx, userID, legacyMessages, info)
}

func (m legacyMemoraReader) Search(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.Search(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (m legacyMemoraReader) SmartSearch(ctx context.Context, userID, query string, topK int) ([]memory.Memory, error) {
	if m.c == nil {
		return nil, nil
	}
	items, err := m.c.SmartSearch(ctx, userID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]memory.Memory, 0, len(items))
	for i := range items {
		out = append(out, memory.Memory{
			ID:     items[i].ID,
			Text:   items[i].Text,
			Tags:   items[i].Tags,
			Score:  items[i].Score,
			CubeID: items[i].CubeID,
		})
	}
	return out, nil
}

func (w legacyMemoraWriter) Enqueue(op memory.WriteOp) {
	if w.s == nil {
		return
	}
	msgs := make([]memory.Message, 0, len(op.Messages))
	for i := range op.Messages {
		msgs = append(msgs, memory.Message{
			Role:    op.Messages[i].Role,
			Content: op.Messages[i].Content,
		})
	}
	w.s.Enqueue(memory.WriteOp{
		UserID:   op.UserID,
		Info:     op.Info,
		Source:   op.Source,
		Messages: msgs,
	})
}

func (w legacyMemoraWriter) Stats() memory.Stats {
	if w.s == nil {
		return memory.Stats{}
	}
	stats := w.s.Stats()
	return memory.Stats{
		Enqueued:          stats.Enqueued,
		Dropped:           stats.Dropped,
		Processed:         stats.Processed,
		Errored:           stats.Errored,
		QueueLen:          stats.QueueLen,
		QueueCap:          stats.QueueCap,
		ConsecutiveErrors: stats.ConsecutiveErrors,
		LastError:         stats.LastError,
		LastErrorAt:       stats.LastErrorAt,
		Paused:            stats.Paused,
	}
}

func (w legacyMemoraWriter) Pause() {
	if w.s != nil {
		w.s.Pause()
	}
}

func (w legacyMemoraWriter) Resume() {
	if w.s != nil {
		w.s.Resume()
	}
}

// providerClientAdapter bridges executors providerResolver to compression.ProviderClient.
// The adapter discards the *provider.Policy return value because the
// compression only needs the candidate list.
type providerClientAdapter struct {
	r routingProviderResolver
}

// routingProviderResolver is the minimal subset of executors providerResolver
// we depend on. Re-declared here to avoid importing executors into the
// interface signature (which would create a circular type reference).
type routingProviderResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
	ModelKnown(ctx context.Context, model string) bool
}

func (p providerClientAdapter) Enabled() bool {
	if p.r == nil {
		return false
	}
	return p.r.Enabled()
}

func (p providerClientAdapter) ModelKnown(ctx context.Context, model string) bool {
	if p.r == nil {
		return false
	}
	return p.r.ModelKnown(ctx, model)
}

func (p providerClientAdapter) GetCandidates(ctx context.Context, model, profile string) ([]compression.ProviderCandidate, error) {
	return p.GetCandidatesForTenant(ctx, model, profile, "")
}

// GetCandidatesForTenant preserves tenant-specific routing policy for callers
// that can propagate the request tenant through the compression dependency.
func (p providerClientAdapter) GetCandidatesForTenant(ctx context.Context, model, profile, tenantID string) ([]compression.ProviderCandidate, error) {
	if p.r == nil {
		return nil, fmt.Errorf("provider resolver nil")
	}
	raw, _, err := p.r.GetCandidates(ctx, model, profile, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]compression.ProviderCandidate, 0, len(raw))
	for i := range raw {
		out = append(out, compression.ProviderCandidate{
			CredentialID:  raw[i].CredentialID,
			ProviderID:    raw[i].ProviderID,
			RawModel:      raw[i].RawModel,
			BaseURL:       raw[i].BaseURL,
			APIKey:        raw[i].APIKey,
			Protocol:      raw[i].Protocol,
			ContextWindow: nil, // provider.Candidate does not carry context window; compression falls back to default heuristic
			Available:     raw[i].Routable,
		})
	}
	return out, nil
}
