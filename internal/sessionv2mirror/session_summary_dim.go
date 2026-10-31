package sessionv2mirror

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// session_summary_dim.go — S4 停写交接（2026-10-13）。
//
// # 这块在守什么
//
// session_summaries 的聚合列（request_count / success_count / total_cost_usd /
// tokens / latency / models_used / primary_model …）此前**只**由
// _request_logs_hot 上的两个触发器喂养：
//
//	trg_update_session_summary（563 建链、572/661 修 DECIMAL(10,6) 比值溢出）
//	trg_sync_session_task_id（首值同步 gw_task_id）
//
// S4 停写（storage.request_logs_write_enabled=false）后 _request_logs_hot 不再
// 有 INSERT，两个触发器断粮 —— 而它下游是活的用户面：admin 会话列表/详情/
// 相关会话（session_management_api.go）、会话分析（session_analytics_handler.go）、
// usage_enhanced 的意图列。不接管，新会话在这些界面上**永远不出现**。
//
// # 交接语义（与触发器逐列对齐 + 三处有意差异）
//
// 对齐：upsert 形状、首值优先（first_request_at/primary_model/gw_task_id）、
// 增量均值（avg = (avg*n + v)/(n+1)）、token 比例拆分 input/output cost
// （比值 ≤1，无界 numeric 除法 —— 不重蹈 563 的 DECIMAL(10,6) 覆辙）、
// array_unique_append 维度数组。
//
// 有意差异（均朝「数据源更干净」方向，plan §3 分析产物层）：
//  1. 只在终态 entry 上跑（hook 已过滤 in_progress）——触发器对罕见的
//     in_progress INSERT 也 +1 error_count，本写入器不会；
//  2. 内部回环（会话标题/摘要自动请求）不再计入 request_count——触发器
//     无差别计数，标题流量曾把 request_count 抬高 ~10%（72h 对账
//     internal_loopback 815/12306）；
//  3. 探针合成会话不进（IsProbeSyntheticSession，与 turns 同口径）。
//
// # 为什么这里没有「双喂防护」门（2026-10-29）
//
// 原先这里有一道门：v1 写入开启时整体 no-op。理由是「v1 停写**之前**
// _request_logs_hot 上的 trg_update_session_summary 还在计数，本写入器
// 若同时跑会双计数」。那道门要成立，需要**两个事实同时为真**：
//
//	① 有一个 v1 写入方在往 _request_logs_hot 落行（触发器才有输入）；
//	② 该触发器（含 trg_sync_session_task_id 与 update_session_summary()）还存在。
//
// 两个前提都已随 v1 族退役消失（逐条实测见
// TestSessionSummaryDimWriter_NoV1TriggerPremise 与
// TestSessionSummaryDimWriter_IgnoresRetiredV1Switch）：
//
//	① PG 侧 v1 写入分支已从 domains/hooks/observability/telemetry/client.go
//	   整段删除 ⇒ 本二进制**不写** _request_logs_hot。即使库还停在 890
//	   之前（灰度窗口），触发器也**无输入可触发**。
//	② 890 物理删除了整个 v1 族：_request_logs_hot / _request_logs_bodies_hot
//	   连同两个触发器与 update_session_summary() 函数一并消失。
//
// 于是那道门不再是「双喂防护」，而是**纯粹的停摆开关**：把退役开关
// storage.request_logs_write_enabled 拨到 true，会让 session_summaries
// 的聚合**静默停写**——而本写入器是这些聚合列（request_count /
// success_count / error_count / total_cost_usd / tokens / latency /
// models_used …）的**唯一写方**（其余写方只碰 title / summary /
// key_topics / user_intent / handoff_* / archived_at 等旁列）。
// 后果是用户面（admin 会话列表/详情、会话分析、usage_enhanced 意图列）
// 在新会话上**永远不出现**，且**无日志、无错误**。
//
// 这就是「退役语义仍在支配活行为」：一个控制「v1 写不写」的开关，
// 决定了「v2 聚合写不写」。
//
// 现在：门删除，聚合**无条件**写入。双喂前提若将来复活（某个迁移重新
// 建出 v1 族 + 触发器），真库门会当场报红，由迁移作者重新决定要不要
// 加门——而不是让一道早该退休的开关一直悄悄支配聚合。
//
// （确需停写聚合是另一个显式开关的问题；用 v1 退役开关来停是语义串台。）
type SessionSummaryDimWriter struct {
	pool *pgxpool.Pool
}

// NewSessionSummaryDimWriter 创建 session_summaries 聚合维护器；pool 为 nil
// 时返回的写入器所有调用返回 error（与 SessionDimWriter 同款装配语义）。
func NewSessionSummaryDimWriter(pool *pgxpool.Pool) *SessionSummaryDimWriter {
	return &SessionSummaryDimWriter{pool: pool}
}

// v1WritesEnabled 曾是 S4 停写门的 seam（测试可替换；生产读热加载设置）。
//
// 2026-10-29：该 seam 随门一并删除 —— 门的前提（v1 写入方 + 触发器）已随
// v1 族退役消失，留着它就等于留着一个「看起来还能调」的退役开关。
// 取而代之的守卫在 TestSessionSummaryDimWriter_IgnoresRetiredV1Switch
// （静态：本文件不得再读 v1 退役开关）与真库门
// TestSessionSummaryDimWriter_NoV1TriggerPremise（动态：前提不存在）。

// sessionSummaryUpsertSQL 对齐 trg_update_session_summary 的 upsert 语义，
// 并并入 trg_sync_session_task_id 的 gw_task_id 首值同步。
//
// 参数全部按**显式标量**传入（空值用空串/0 表示，SQL 侧 NULLIF 归一）：
// CASE WHEN $x IS NOT NULL THEN … 分支里 PG 无法从 nil 参数推断类型
// （SQLSTATE 42P08），显式标量 + NULLIF/cast 让类型推断处处有锚点。
// provider 查找用 $13::int8 = 0 表示「无 provider」（providers 无 0 号行，
// 子查询自然回落 '{}' / NULL）。
const sessionSummaryUpsertSQL = `
INSERT INTO session_summaries (
    session_key, tenant_id, first_request_at, last_request_at,
    request_count, success_count, error_count,
    total_cost_usd, input_cost_usd, output_cost_usd,
    total_prompt_tokens, total_completion_tokens,
    avg_latency_ms, min_latency_ms, max_latency_ms,
    models_used, work_types, providers, client_models,
    primary_model, gw_task_id, updated_at
) VALUES (
    $1, $2, $3, $3,
    1, CASE WHEN $4 THEN 1 ELSE 0 END, CASE WHEN $4 THEN 0 ELSE 1 END,
    $5::numeric, $6::numeric, $7::numeric,
    $8, $9,
    $10, $10, $10,
    CASE WHEN NULLIF($11, '') IS NOT NULL THEN ARRAY[$11]::text[] ELSE '{}'::text[] END,
    CASE WHEN NULLIF($12, '') IS NOT NULL THEN ARRAY[$12]::text[] ELSE '{}'::text[] END,
    COALESCE((SELECT ARRAY[p.code]::text[] FROM providers p WHERE p.id = $13::int8 LIMIT 1), '{}'::text[]),
    CASE WHEN NULLIF($14, '') IS NOT NULL THEN ARRAY[$14]::text[] ELSE '{}'::text[] END,
    NULLIF($11, ''), NULLIF($15, ''), NOW()
) ON CONFLICT (session_key) DO UPDATE SET
    last_request_at = GREATEST(session_summaries.last_request_at, EXCLUDED.last_request_at),
    request_count = session_summaries.request_count + 1,
    success_count = session_summaries.success_count + CASE WHEN $4 THEN 1 ELSE 0 END,
    error_count = session_summaries.error_count + CASE WHEN $4 THEN 0 ELSE 1 END,
    total_cost_usd = session_summaries.total_cost_usd + $5::numeric,
    input_cost_usd = session_summaries.input_cost_usd + $6::numeric,
    output_cost_usd = session_summaries.output_cost_usd + $7::numeric,
    total_prompt_tokens = session_summaries.total_prompt_tokens + $8,
    total_completion_tokens = session_summaries.total_completion_tokens + $9,
    avg_latency_ms = ((session_summaries.avg_latency_ms * session_summaries.request_count + $10)
        / (session_summaries.request_count + 1))::INT,
    min_latency_ms = LEAST(session_summaries.min_latency_ms, $10),
    max_latency_ms = GREATEST(session_summaries.max_latency_ms, $10),
    models_used = array_unique_append(session_summaries.models_used, NULLIF($11, '')),
    work_types = array_unique_append(session_summaries.work_types, NULLIF($12, '')),
    providers = array_unique_append(session_summaries.providers,
        (SELECT p.code FROM providers p WHERE p.id = $13::int8 LIMIT 1)),
    client_models = array_unique_append(session_summaries.client_models, NULLIF($14, '')),
    primary_model = COALESCE(session_summaries.primary_model, NULLIF($11, '')),
    gw_task_id = COALESCE(session_summaries.gw_task_id, NULLIF($15, '')),
    updated_at = NOW()`

// UpsertSessionDim 以终态 entry 维护 session_summaries 聚合（DimWriter 接口，
// 由 PersistHook 的 dims 循环调用）。
//
// 无条件写入：v1 退役开关**不再**支配本写入器（理由见文件头「为什么这里没有
// 双喂防护门」）。本写入器是聚合列的唯一写方，让一个退役开关能静默停掉它，
// 就是在用退役语义支配活行为。
func (w *SessionSummaryDimWriter) UpsertSessionDim(ctx context.Context, entry *telemetry.RequestLogEntry) error {
	if entry == nil || entry.GwSessionID == nil || *entry.GwSessionID == "" {
		return nil
	}
	if w == nil || w.pool == nil {
		return errors.New("sessionv2mirror: session summary writer has no pool")
	}

	ts := time.Now()
	if entry.EventAt != nil {
		ts = *entry.EventAt
	}
	prompt, completion := intPtrOrZero(entry.PromptTokens), intPtrOrZero(entry.CompletionTokens)
	ratio := 0.5
	if total := prompt + completion; total > 0 {
		ratio = float64(prompt) / float64(total)
	}
	cost := floatPtrOrZero(entry.CostUSD)
	inputCost, outputCost := cost*ratio, cost-cost*ratio

	_, err := w.pool.Exec(ctx, sessionSummaryUpsertSQL,
		*entry.GwSessionID,
		orDefaultStr(entry.TenantID, "default"),
		ts,
		entry.Success,
		cost,
		inputCost,
		outputCost,
		prompt,
		completion,
		intPtrOrZero(entry.LatencyMs),
		strOrEmpty(entry.OutboundModel),
		strOrEmpty(entry.WorkType),
		int64(intPtrOrZero(entry.ProviderID)),
		strOrEmpty(entry.ClientModel),
		strOrEmpty(entry.GwTaskID),
	)
	if err != nil {
		return err
	}
	return nil
}

func intPtrOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func floatPtrOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// strOrEmpty 归一 nil/空串 → ""（SQL 侧 NULLIF($x,”) 负责转 NULL）。
func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orDefaultStr(s string, def string) string {
	if s == "" {
		return def
	}
	return s
}
