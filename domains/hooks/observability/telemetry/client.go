package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
	filestore "github.com/kaixuan/llm-gateway-go/storage/file"
)

var errNoTelemetryDB = errors.New("telemetry database not configured")

// requestLogsWriteEnabled 报告宽表家族的写入开关 —— S4 停写 gate（存储优化
// 方案 v2，settings 键 storage.request_logs_write_enabled，★ **代码默认 =
// false** ⇒ 未显式配置即**不双写**；HotReload 即时生效）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 宽表的 PG 写入分支已整段删除，本包
// 剩下的唯一消费点是**文件系统**正文镜像（persistRequestLog →
// mirrorRequestBodies）——它不碰 PG。usage_ledger 计费、api_keys 计数、
// outbox 会话事件照常；session 六表族（sessionv2mirror）是唯一事实源。
// 与 Lite sink（cmd/gateway/lite_telemetry_sink.go）同一键、同一默认。
func requestLogsWriteEnabled() bool {
	return settings.RequestLogsWriteEnabled()
}

// pgErrorDiagnostics extracts the server-side error fields that err.Error()
// omits (Detail/Hint/Table/Column/Constraint). 2026-09-05 PG log audit:
// request-log persists failing with bare "numeric field overflow
// (SQLSTATE 22003)" carry the offending precision/scale only in Detail —
// without it the overflowing column cannot be located from gateway logs.
func pgErrorDiagnostics(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	fields := make([]string, 0, 5)
	if pgErr.Detail != "" {
		fields = append(fields, "detail="+pgErr.Detail)
	}
	if pgErr.Hint != "" {
		fields = append(fields, "hint="+pgErr.Hint)
	}
	if pgErr.TableName != "" {
		fields = append(fields, "table="+pgErr.TableName)
	}
	if pgErr.ColumnName != "" {
		fields = append(fields, "column="+pgErr.ColumnName)
	}
	if pgErr.ConstraintName != "" {
		fields = append(fields, "constraint="+pgErr.ConstraintName)
	}
	return strings.Join(fields, "; ")
}

// ★ 2026-10-20（迁移 890，v1 族退役）：heapPartitionNameRE（宽表月度分区名
// 白名单）随 claimSessionFinalSuccessExec 整条删除——它只用于拼 v1 分区名。

// quoteIdent 是 pgx.Identifier 等价的轻量版, 用于动态拼分区名 (避免注入).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// OutboxWriter writes events to outbox_events table for Gateway → ASM delivery.
// Implemented by internal/outbox.Writer. Nil-safe (checked before use).
type OutboxWriter interface {
	Write(ctx context.Context, env outbox.EventEnvelope) error
}

type requestLogDB interface {
	execQuerier
	Begin(context.Context) (pgx.Tx, error)
}

type execQuerier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RequestLogSink 是 lite 存储模式（无 PostgreSQL）下的请求日志持久化 sink。
//
// 背景（2026-09-05 审计 B2）：lite 模式跳过 PG 初始化后 dbPool 为 nil，
// Enabled() 恒 false，整条 EmitRequestLog → worker → persist 管道惰性化，
// 请求日志完全不落盘。SetRequestLogSink 注入本接口后：
//   - Enabled() 视为已启用，请求日志照常入队（决策日志 / context_attrs
//     侧表仍依赖 PG，lite 下继续跳过，不受影响）；
//   - persistRequestLog 在 requestLogDatabase() 为 nil 时改走 sink，
//     成功后与 PG 路径同样触发 onPersisted hooks 与 releaseBodies。
//
// full 模式从不注入 sink，所有路径行为零变化。
type RequestLogSink interface {
	// PersistRequestLog 持久化一条请求日志（INSERT 与 UPDATE 两类 op 都
	// 经此入口，实现方需对同一 request_id 幂等/UPSERT）。返回错误时由
	// telemetry flush 侧记 failPermanent 并告警，不阻塞请求热路径。
	PersistRequestLog(ctx context.Context, entry *RequestLogEntry) error
}

// requestLogSinkHolder 是 atomic.Value 的装载类型（要求 Load 端类型断言稳定）。
type requestLogSinkHolder struct {
	sink RequestLogSink
}

// BodyMirrorFunc 是热区请求侧 body 镜像的投递回调（2026-09-24 方案 H3，
// docs/storage/2026-09-24-hotzone-dual-mode-plan.md §3-H3）。由存储装配层
// 注入，底层为 storage/file.RequestMirror 的 fire-and-forget 异步写（失败仅
// 计数，绝不阻断主链路）。
//
// 契约：
//   - direction 取 "req"（上行请求 body）/ "resp"（下行响应 body）/
//     "out"（流式终态 outbound body），与 storage/file 的
//     DirRequest/DirResponse/DirOutput 字面值一一对应；
//   - payload 与会话族正文落库同源换算（strPtrToJSON /
//     jsonOrNull），空载荷（"null"/"{}"）已在调用侧跳过；
//   - at 是镜像文件日期分区取样时刻（对位 PG 侧 ts=now() 的语义）。
//
// telemetry 侧不等待、不重试、不因镜像失败改变持久化结果。
type BodyMirrorFunc func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time)

// bodyMirrorHolder 是 atomic.Value 的装载类型（要求 Load 端类型断言稳定）。
type bodyMirrorHolder struct {
	fn BodyMirrorFunc
}

type Client struct {
	dbPool       *pgxpool.Pool
	requestLogDB requestLogDB

	// requestLogSinkStore 持有 lite 存储模式注入的非 PG 请求日志 sink
	// （atomic.Value 一致性要求包装为同一具体类型）。
	requestLogSinkStore atomic.Value // requestLogSinkHolder
	// bodyMirrorStore 持有热区请求侧镜像回调（H3；与 requestLogSinkStore
	// 同款 atomic.Value 装载，与运行中的 worker 并发安全）。
	bodyMirrorStore atomic.Value // bodyMirrorHolder
	queue           chan any
	done            chan struct{}
	wg              sync.WaitGroup

	lifecycleMu sync.RWMutex
	stopped     atomic.Bool

	// onPersisted hooks run after every successful INSERT/UPDATE of a
	// request-log row. Consumers use AddOnRequestLogPersisted to register.
	onPersisted []func(entry *RequestLogEntry)

	// onEmitted is invoked immediately when EmitRequestLog is called,
	// BEFORE the entry is queued or persisted to the database. This
	// provides faster real-time updates from the in-memory pipeline
	// rather than waiting for DB write completion. The callback runs
	// on the caller's goroutine and MUST be non-blocking (use select
	// with default for channel sends). May be nil.
	onEmitted func(entry *RequestLogEntry)

	// onDecisionEmitted mirrors onEmitted for decision rows: invoked
	// synchronously inside EmitDecisionLog after the liveness guards pass
	// (not stopped, PG enabled), before the queue/sync-insert decision.
	// Registered via SetOnDecisionLogEmitted. May be nil.
	onDecisionEmitted func(entry *DecisionLogEntry)
	fallback          dbdegradation.BackupWriter
	degraded          atomic.Bool

	// outboxWriter (optional): writes request.completed events to outbox_events
	// in the same transaction as the request-log write for Gateway → ASM delivery.
	// Phase 3 WP4: Business write point integration.
	outboxWriter OutboxWriter

	// 2026-07-16: failure counters so ops can detect "telemetry rows
	// silently dropping" via FailCounts() instead of grepping stderr.
	// Bumped in worker/EmitRequestLog paths; safe for concurrent reads.
	failTransient uint64
	failPermanent uint64
	failRetried   uint64
	failFallback  uint64

	// ★ 2026-10-20（迁移 890，v1 族退役）：原 v1 月度分区名缓存
	// （heapPartitionsMu / heapPartitions / heapPartitionsCachedAt）随
	// claimSessionFinalSuccess 整条删除——它只服务于 v1 的认领半连接。
}

type DecisionLogEntry struct {
	RequestID           string          `json:"request_id"`
	IdempotencyKey      *string         `json:"idempotency_key,omitempty"`
	TenantID            string          `json:"tenant_id"`
	APIKeyID            *int            `json:"api_key_id,omitempty"`
	Model               string          `json:"model"`
	ChosenCredentialID  *int            `json:"chosen_credential_id,omitempty"`
	ChosenProviderID    *int            `json:"chosen_provider_id,omitempty"`
	Tier                *int            `json:"tier,omitempty"`
	CandidatesTried     int             `json:"candidates_tried"`
	LatencyMs           int             `json:"latency_ms"`
	Success             bool            `json:"success"`
	ErrorClass          *string         `json:"error_class,omitempty"`
	PromptTokens        *int            `json:"prompt_tokens,omitempty"`
	CompletionTokens    *int            `json:"completion_tokens,omitempty"`
	CostUSD             *float64        `json:"cost_usd,omitempty"`
	RequestBytes        *int            `json:"request_bytes,omitempty"`
	ResponseBytes       *int            `json:"response_bytes,omitempty"`
	ClientModel         *string         `json:"client_model,omitempty"`
	ResolvedRawModel    *string         `json:"resolved_raw_model,omitempty"`
	OutboundModel       *string         `json:"outbound_model,omitempty"`
	StickyHit           *bool           `json:"sticky_hit,omitempty"`
	ClientProfile       *string         `json:"client_profile,omitempty"`
	RequestMode         *string         `json:"request_mode,omitempty"`
	IdentityHash        *string         `json:"identity_hash,omitempty"`
	TransformRuleID     *string         `json:"transform_rule_id,omitempty"`
	EgressProtocol      *string         `json:"egress_protocol,omitempty"`
	FailureStage        *string         `json:"failure_stage,omitempty"`
	FailureDetailCode   *string         `json:"failure_detail_code,omitempty"`
	ResolutionPath      *string         `json:"resolution_path,omitempty"`
	CanonicalModel      *string         `json:"canonical_model,omitempty"`
	ResolutionRawModels []string        `json:"resolution_raw_models,omitempty"`
	DecisionTrace       json.RawMessage `json:"decision_trace,omitempty"`
}

// RequestLogOp distinguishes insert-at-start from update-on-complete.
type RequestLogOp string

const (
	RequestLogInsert RequestLogOp = "insert"
	RequestLogUpdate RequestLogOp = "update"
)

// Request log lifecycle status stored in the request-log row's request_status.
const (
	RequestStatusInProgress  = "in_progress"
	RequestStatusSuccess     = "success"
	RequestStatusFailure     = "failure"
	RequestStatusRateLimited = "rate_limited" // gateway rejected the client (RPM/concurrent/throttle): NOT a system error
)

type RequestLogEntry struct {
	// SessionV2WriteRequired records this request's already-selected fact source.
	// Once a transaction skips V1, later configuration changes must not cancel
	// its V2 persistence. JSON preserves the decision through fallback/outbox.
	SessionV2WriteRequired bool         `json:"session_v2_write_required,omitempty"`
	Op                     RequestLogOp `json:"op,omitempty"`
	RequestID              string       `json:"request_id"`
	EventAt                *time.Time   `json:"event_at,omitempty"`
	TenantID               string       `json:"tenant_id"`
	ApplicationID          *int         `json:"application_id,omitempty"`
	APIKeyID               *int         `json:"api_key_id,omitempty"`
	EndUserID              *string      `json:"end_user_id,omitempty"`
	CustomerID             *int64       `json:"customer_id,omitempty"`
	// RequestClass 是请求类型（V6-W1.6 R8，migration 610）：immediate|scheduled。
	// nil 落库为 'immediate'（列 NOT NULL DEFAULT，INSERT 侧 COALESCE）；
	// 由 handler 从 ExecParams.DispatchDueAt（X-Gw-Due-At 头）推导并盖章。
	RequestClass *string `json:"request_class,omitempty"`
	// DueAt 是定时请求的到期时刻（scheduled 时非 nil；immediate 为 nil）。
	DueAt *time.Time `json:"due_at,omitempty"`
	// SystemFingerprint 是上游 X-System-Fingerprint 响应头（2026-09-12 挂账
	// 闭环）：此前仅被 integrity detector 写入 context JSONB，603 建的专用列
	// 全表零行、7 天漂移检测器空转。现由 handler emitTelemetry 盖章。
	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 宽表的专用列写入点已删除，值由
	// 会话族的镜像写入路径承接（s1a_fields.go）。
	// nil = 上游未返回该头。
	SystemFingerprint *string `json:"system_fingerprint,omitempty"`
	ClientModel       *string `json:"client_model,omitempty"`
	OutboundModel     *string `json:"outbound_model,omitempty"`
	CredentialID      *int    `json:"credential_id,omitempty"`
	ProviderID        *int    `json:"provider_id,omitempty"`
	CanonicalID       *int    `json:"canonical_id,omitempty"`
	// 2026-07-27: 标准/canonical 模型名(全小写),从 models_canonical.canonical_name 提取。
	// 之前需要每次 JOIN models_canonical 才能拿到标准名,实时请求流的模型筛
	// 选因此无法直接做低成本的 GROUP BY。现在直接写,过滤 SQL 简单到极致。
	// NULL 表示 modelResolution 未匹配到 canonical row(走 passthrough)。
	CanonicalModel   *string `json:"canonical_model,omitempty"`
	ClientProfile    *string `json:"client_profile,omitempty"`
	RequestMode      *string `json:"request_mode,omitempty"`
	AffinityHit      *bool   `json:"affinity_hit,omitempty"`
	PromptTokens     *int    `json:"prompt_tokens,omitempty"`
	CompletionTokens *int    `json:"completion_tokens,omitempty"`
	CacheReadTokens  *int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int    `json:"cache_write_tokens,omitempty"`
	// audit-ir-multimodal (2026-07-13): Multimodal and reasoning token fields
	ReasoningTokens *int     `json:"reasoning_tokens,omitempty"`
	ImageTokens     *int     `json:"image_tokens,omitempty"`
	AudioTokens     *int     `json:"audio_tokens,omitempty"`
	VideoTokens     *int     `json:"video_tokens,omitempty"`
	ProviderTokens  *int     `json:"provider_tokens,omitempty"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	CostDisplay     *float64 `json:"cost_display,omitempty"`
	CostCurrency    *string  `json:"cost_currency,omitempty"`
	LatencyMs       *int     `json:"latency_ms,omitempty"`
	Success         bool     `json:"success"`
	RequestStatus   *string  `json:"request_status,omitempty"`
	ErrorKind       *string  `json:"error_kind,omitempty"`
	// UsageSource indicates where the token counts came from:
	//   "llm"       — extracted from upstream response.usage block
	//   "estimated" — computed locally from request/response text (fallback)
	//   ""          — not available (request failed before parsing)
	UsageSource  *string `json:"usage_source,omitempty"`
	IdentityHash *string `json:"identity_hash,omitempty"`
	// 2026-07-27: 客户端感知扩展 — 由 streaming 层 fillAttemptMeta 填充,经
	// buildEntry / handler.go 复制到此处,主表 INSERT 可持久化。
	// 之前只在 request_context_attrs 侧表写入,主表永远 NULL,导致
	// GROUP BY agent_name 统计为 0。本次修复把 4 个字段从 meta 透传到主表。
	AgentName          *string `json:"agent_name,omitempty"`
	AgentType          *string `json:"agent_type,omitempty"`
	ClientProtocol     *string `json:"client_protocol,omitempty"`
	UpstreamProtocol   *string `json:"upstream_protocol,omitempty"`
	ProtocolConversion *bool   `json:"protocol_conversion,omitempty"`
	VirtualClientID    *string `json:"virtual_client_id,omitempty"`
	StreamFirstChunkMs *int    `json:"stream_first_chunk_ms,omitempty"`
	StreamChunkCount   *int    `json:"stream_chunk_count,omitempty"`
	StreamChunksSent   *int    `json:"stream_chunks_sent,omitempty"`
	StreamDoneReceived *bool   `json:"stream_done_received,omitempty"`
	StreamInterrupted  *bool   `json:"stream_interrupted,omitempty"`
	ResponseChecksum   *string `json:"response_checksum,omitempty"`
	FailureDetailCode  *string `json:"failure_detail_code,omitempty"`
	FailureStage       *string `json:"failure_stage,omitempty"`
	TransformRuleID    *string `json:"transform_rule_id,omitempty"`
	EgressProtocol     *string `json:"egress_protocol,omitempty"`
	RequestPreview     *string `json:"request_preview,omitempty"`
	TransformSummary   *string `json:"transform_summary,omitempty"`
	ResponsePreview    *string `json:"response_preview,omitempty"`
	RequestBody        *string `json:"request_body,omitempty"`
	ResponseBody       *string `json:"response_body,omitempty"`
	GwSessionID        *string `json:"gw_session_id,omitempty"`
	GwTaskID           *string `json:"gw_task_id,omitempty"`
	// AgentRole/ParentSessionID（R50 F15，2026-09-21）：会话角色归因三列的
	// 前两列（第三列 parent_task_id 复用 GwTaskID）。与下方 Mirror-only
	// 维度同款契约：宽表持久化 SQL 不落这些字段，仅作进程内 +
	// session_mirror_outbox JSON 载荷传输，供 sessionv2mirror 桥进
	// public.sessions 的 agent_role / parent_session_id 列（730）。
	AgentRole       *string `json:"agent_role,omitempty"`
	ParentSessionID *string `json:"parent_session_id,omitempty"`
	// Mirror-only transport dimensions. The wide-table persistence SQL ignores
	// these fields; onPersisted consumers use them to build the V2 request DTO.
	ProjectID       *string `json:"project_id,omitempty"`
	Namespace       *string `json:"namespace,omitempty"`
	APIKeyPrefix    *string `json:"api_key_prefix,omitempty"`
	APIKeyOwnerUser *string `json:"api_key_owner_user,omitempty"`
	ApplicationCode *string `json:"application_code,omitempty"`
	// v2.0 auto-route observability (requires 2026-06-15-auto-route-mode.sql)
	IsAutoRequest  *bool    `json:"is_auto_request,omitempty"`
	TaskType       *string  `json:"task_type,omitempty"`
	AutoProfile    *string  `json:"auto_profile,omitempty"`
	AutoDecision   *string  `json:"auto_decision,omitempty"`
	AutoConfidence *float64 `json:"auto_confidence,omitempty"`
	WorkType       *string  `json:"work_type,omitempty"`

	// 2026-09-29 X-Gw-Test-Mode (see streaming/auto_route.go) is held in the
	// RequestLogContext for now — persistence into the request-log row is tracked
	// separately as a follow-up migration so this commit stays a runtime-only
	// change. Test traffic is observable today via:
	//   1. the X-Gw-Mock-Marker response header on every mock-mode reply,
	//   2. the mock_marker field in the synthetic response body,
	//   3. the X-Gw-Source-Actor header that authorises the caller
	//      (auto-testbench / autoroute-e2e-audit / manual-probe).
	// The struct field is intentionally absent so the existing INSERT/
	// UPDATE column lists and DB schema stay byte-for-byte unchanged.

	// P7.2: promoted from auto_decision JSONB to dedicated columns
	// for indexable queries (see ensureRequestLogAutoDecisionColumns).
	TaskTypeChosen *string  `json:"task_type_chosen,omitempty"`
	ConfidenceNum  *float64 `json:"confidence_num,omitempty"`
	ModelChosen    *string  `json:"model_chosen,omitempty"`
	StrategyUsed   *string  `json:"strategy_used,omitempty"`
	CreditsCharged *int64   `json:"credits_charged,omitempty"`

	// Wave 3 B1 (2026-09-22): peak/off-peak rate multiplier resolved at the
	// request start time and applied to the charge. Stamped into
	// usage_ledger(_hot).rate_multiplier and the request-log row's
	// credits_rate_multiplier (migration 736) so billing
	// replay can reconcile the exact factor used. nil/1.0 = no multiplier.
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`

	// Round 47 (2026-06-18) compression v7 T2: parent-child chain tracking.
	// Mirrors the 4 columns added by db/migrations/013_compression_columns.sql
	// (parent_request_id, compression_reason, compression_strategy,
	// compression_meta). Populated by compressor/ when mode=1 (auto_threshold)
	// or mode=2 (on_4xx) fires. Single-level chain: a child row has at most
	// 1 parent (its own request_id pre-compression); no grandparent.
	// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.1.
	ParentRequestID     *string         `json:"parent_request_id,omitempty"`
	CompressionReason   *string         `json:"compression_reason,omitempty"`
	CompressionStrategy *string         `json:"compression_strategy,omitempty"`
	CompressionMeta     json.RawMessage `json:"compression_meta,omitempty"`

	// 2026-08-19: token-band observability for multi-layer session compression.
	// Mirrors the token_band column added by
	// sql/migrations/startup/542_request_logs_token_band.sql.
	// Populated by SessionCompressor when sliding-window fires.
	// Values: "below" | "preliminary" | "forced" | NULL.
	TokenBand *string `json:"token_band,omitempty"`

	// v3 (2026-06-19) session-level outbound body T23.
	// Mirrors 4 columns added by db/migrations/016_outbound_body.sql.
	// Populated by compression.SessionCompressor when the session cache
	// rewrites the body (delta-append + optional sliding-window summary).
	// NULL means no session compressor was active for this request.
	OutboundBody      json.RawMessage `json:"outbound_body,omitempty"`
	OutboundMsgCount  *int            `json:"outbound_msg_count,omitempty"`
	OutboundTokenEst  *int            `json:"outbound_token_est,omitempty"`
	OutboundMsgHashes json.RawMessage `json:"outbound_msg_hashes,omitempty"`

	// 2026-08-05: X-Gw-Submit-Mode client header (delta | snapshot | full |
	// attachment_only). Carried through so the sessionv2mirror hook can feed
	// the V2 SubmitModeDetector's P0 (explicit-header) path instead of relying
	// solely on LCS inference. NOT a request-log column — mirror-only transport
	// field; the telemetry persist path ignores it.
	SubmitModeHeader *string `json:"submit_mode_header,omitempty"`

	// 2026-06-19: tool_call quality signals (017_quality_fix_mode.sql).
	// QualityFlags is the array of detected issues (empty_tool_name,
	// duplicate_tool_call_id, …). QualityFixActions is the JSON
	// {flag: {detected, renamed, dropped}} tally of what was actually
	// done in the response body. QualityScore is 0..1, nil when the
	// quality processor was off for this provider.
	QualityFlags      []string        `json:"quality_flags,omitempty"`
	QualityFixActions json.RawMessage `json:"quality_fix_actions,omitempty"`
	QualityScore      *float64        `json:"quality_score,omitempty"`

	// 2026-06-19 T-NEW-7: the upstream finish_reason (stop, tool_calls,
	// length, end_turn, function_call, max_tokens, …). Stored in
	// the request-log row's upstream_finish_reason — the SOLE home for the
	// finish_reason.  Distinct from FailureDetailCode which is now
	// reserved for actual failure / interruption codes.  Populated for
	// BOTH success and failure rows.
	UpstreamFinishReason *string `json:"upstream_finish_reason,omitempty"`

	// 2026-06-23: structured tool_calls array (042_tool_calls_column.sql).
	// Populated from both streaming (audit.StreamCapture.ToolCalls) and
	// non-streaming (extracted from response_body.choices[0].message.tool_calls).
	// OpenAI format: [{id, type, function: {name, arguments}}].
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`

	// 2026-06-26: client-supplied X-Request-Id. Persisted in
	// the request-log row's client_request_id for debug / cross-system tracing.
	// Distinct from RequestID (which is now ALWAYS a server-generated
	// UUID, see migration 054 and the requestid_mw / handler fixes) so
	// that client retries reusing the same id do not collapse into a
	// single audit row.
	ClientRequestID *string `json:"client_request_id,omitempty"`

	// 2026-06-30: 上游错误诊断字段（migration 320）
	// 用于记录上游返回的 HTTP 状态码、客户端超时、流式传输统计等信息
	UpstreamStatusCode *int    `json:"upstream_status_code,omitempty"`
	ClientTimeout      *bool   `json:"client_timeout,omitempty"`
	ClientEndpoint     *string `json:"client_endpoint,omitempty"`
	StreamChunkErrors  *int    `json:"stream_chunk_errors,omitempty"`

	// 2026-10-16: the HTTP status the CLIENT actually received, set by the
	// gateway on the exits where it authors the error response itself
	// (RequestLogContext.SetClientStatus). Distinct from
	// UpstreamStatusCode, which is what the provider returned — for a
	// gateway-authored rejection (401 / 402 / 403 / 5xx) there is no upstream
	// status at all, so session_turns.status_code used to fall through to a
	// hard-coded 500 and every auth rejection was persisted as if the upstream
	// had 500'd (260/260 invalid_key rows on the local PG).
	//
	// NOT a request-log column: like SubmitModeHeader this is a
	// mirror-only transport field consumed by the session v2 mirror and the
	// lite sink. nil means "no client status was recorded for this exit" and
	// consumers must fall back to their own derivation — it never means 500.
	ClientStatusCode *int `json:"client_status_code,omitempty"`

	// 2026-07-01: 附件元数据（migration 325）
	// 从请求体中提取的 base64/data-URI 附件元数据，存储到请求行的 attachments JSONB。
	// 附件实体文件已保存到文件系统（按 SHA256 hash 去重），此处仅记录路径、大小、类型等元信息。
	Attachments json.RawMessage `json:"attachments,omitempty"`

	// 2026-07-14 (migration 341): client-side origin metadata.
	// ClientIP / ClientForwardedFor are populated by middleware/origin_mw.go
	// for every business row (the columns were added by the 2026-07-11
	// observability migration but every row on 252 was NULL — no writer
	// existed until commit 3 of this change).
	ClientIP           *string `json:"client_ip,omitempty"`
	ClientForwardedFor *string `json:"client_forwarded_for,omitempty"`
	// OriginStage labels the row by which component produced it:
	//   self_check       — bg/credential_selfcheck.go (24h/cred daily)
	//   node_probe       — bg/node_probe.go (5s..24h backoff)
	//   system_health    — bg/system_health.go (30s windowed read)
	//   business         — normal user request
	//   probe_direct / probe_v2 / model_probe / passive_probe / manual
	//                    — legacy values from older workers; kept valid by
	//                      the additive CHECK constraint.
	OriginStage *string `json:"origin_stage,omitempty"`
	// OriginActor names the worker / actor that emitted the row, e.g.
	//   credential-selfcheck-worker, node-probe-worker,
	//   system-health-worker, manual:<session_user_id>
	OriginActor *string `json:"origin_actor,omitempty"`

	// 2026-07-19: 路由尝试追踪字段，记录每次 upstream 尝试详情。
	// RoutingAttempts 是 JSONB 数组，RoutingSummary 是人类可读摘要。
	// 单次成功时两者均为空，节省存储空间。
	RoutingAttempts json.RawMessage `json:"routing_attempts,omitempty"`
	RoutingSummary  *string         `json:"routing_summary,omitempty"`

	// 2026-07-25: 请求/响应体大小（用于 Redis 实时统计和看板展示）
	RequestBytes  *int `json:"request_bytes,omitempty"`
	ResponseBytes *int `json:"response_bytes,omitempty"`

	// V3.1 (2026-08-13, migration 491): 9-stage dispatch queue timestamps.
	// Populated from domains/dispatch.QueuedRequest when the V2 pipeline is on.
	// All nullable — early complete / legacy path leave them NULL.
	T0ArrivedAt *time.Time `json:"t0_arrived_at,omitempty"`

	// T0Missing is set by updateRequestLog's upsert-race branch when the t1
	// UPDATE matched 0 rows AND request_id does not exist on the v1 side —
	// i.e. this request produced a terminal record but never had a t0.
	//
	// ⚠ It is a **signal to the mirror side**, not a landing pad on its own.
	// The terminal turn is written asynchronously (hook.go dispatches the DB
	// write to a bounded goroutine pool; shadowWriteDispatchAsync defaults to
	// true), so a marker written from the synchronous telemetry path races
	// the turn insert and loses — it would match 0 rows on essentially every
	// attempt. Carrying the fact on the entry lets sessionv2mirror apply the
	// flag to the turn *after* w.Write has returned, where the row is
	// guaranteed to exist.
	//
	// json:"-": it is process-local control flow, never part of a payload.
	// The durable form of the fact is the is_abandoned column (migration 821).
	T0Missing bool `json:"-"`

	// FinalSuccessClaimed marks that this request **qualified** for the
	// session's single final success, so the session family can carry the same
	// mark (audit §9.203).
	//
	// ★ 2026-10-20（迁移 890，v1 族退役）：它不再由 v1 认领（事务内的 UPDATE
	// ... SET is_final_success=TRUE）授予——那条路径随 v1 族一起落库。现在由
	// 本包的资格判定（shouldClaimFinalSuccess）直接置位，真正的**仲裁**交给
	// 会话族：session_turns 的 partial 唯一索引
	// （uq_session_turns_hot_final_success）保证一个会话只有一个最终成功，
	// internal/sessionv2mirror 的 markTurnFinalSuccess 在 w.Write 返回之后
	// 给那一行打标（23505 = 本会话别的轮次已认领 ⇒ 第一个认领者站住）。
	//
	// Why the fact must ride on the entry: the session turn is written by the
	// mirror hook afterwards and asynchronously (hook.go dispatches to a
	// bounded goroutine pool; shadowWriteDispatchAsync defaults to true). A
	// marker issued from the telemetry side therefore races the turn insert and
	// loses — it would match 0 rows on essentially every attempt. Same shape
	// as T0Missing/is_abandoned, same placement: flag here, apply after
	// w.Write has returned.
	//
	// ⚠ Unlike T0Missing this one IS serialised, so the replay path can see
	// that a replayed turn is supposed to be the final success. The durable
	// form of the fact is the session_turns column.
	FinalSuccessClaimed bool `json:"final_success_claimed,omitempty"`

	T1TotalEnqueuedAt *time.Time `json:"t1_total_enqueued_at,omitempty"`
	T2TotalDequeuedAt *time.Time `json:"t2_total_dequeued_at,omitempty"`
	T3ModelEnqueuedAt *time.Time `json:"t3_model_enqueued_at,omitempty"`
	T4ModelDequeuedAt *time.Time `json:"t4_model_dequeued_at,omitempty"`
	T5CredEnqueuedAt  *time.Time `json:"t5_cred_enqueued_at,omitempty"`
	T6CredDequeuedAt  *time.Time `json:"t6_cred_dequeued_at,omitempty"`
	T7ForwardStartAt  *time.Time `json:"t7_forward_start_at,omitempty"`
	T8ResponseStartAt *time.Time `json:"t8_response_start_at,omitempty"`
	T9ResponseEndAt   *time.Time `json:"t9_response_end_at,omitempty"`

	// RequestType (2026-08-13, V3.2 BE-A3) classifies the row for the
	// homepage live-stream parent/child tree:
	//   main / title_gen / summary / sensitive_check / compression / other
	// Persisted to the request-log row's request_type (migration 510).
	RequestType *string `json:"request_type,omitempty"`

	// DiscardEvents (2026-08-19) is the JSONB array of attempt-discard
	// events the survival / stream-recovery / empty-gate paths pushed into
	// the audit StreamCapture. The audit StreamCapture also exposes it via
	// SummaryAsMap under "discard_events", and the per-request log path
	// (request_log_pipeline.go) copies it onto this field. The current
	// upsert SQL does not yet include the column — operators read the
	// The streaming survival/recovery path appends structured discard events.
	// ★ 2026-10-20（迁移 890，v1 族退役）：discard_events 列只存在于 v1 族，
	// 会话族没有对应列 ⇒ 这批事件在本包**不再落库**（字段与 JSON 形状保留）。
	DiscardEvents json.RawMessage `json:"discard_events,omitempty"`
}

func NewClient() *Client {
	return newClientWithBufSize(4096)
}

// NewClientWithRequestLogDB constructs a client around an injected
// request-log DB surface (Exec/QueryRow/Begin). Production wiring uses
// NewClient + SetDB(*pgxpool.Pool); this constructor exists so OUT-of-package
// tests (domains/streaming) can drive decision-log emission with pgxmock —
// it is the exported spelling of the in-package `&Client{requestLogDB: mockDB}`
// literal. The worker lifecycle is identical to NewClient.
func NewClientWithRequestLogDB(db requestLogDB) *Client {
	c := newClientWithBufSize(4096)
	c.requestLogDB = db
	return c
}

// inferRequestTypeV32 derives the V3.2 request_type from the entry's existing
// fields. Returns "main" for a plain client request (the column DEFAULT).
// Precedence: explicit RequestType > compression > origin_actor > parent link.
func inferRequestTypeV32(entry *RequestLogEntry) string {
	if entry == nil {
		return "main"
	}
	if entry.RequestType != nil && *entry.RequestType != "" {
		return *entry.RequestType
	}
	// 压缩重写的请求：有 compression_reason。
	if entry.CompressionReason != nil && *entry.CompressionReason != "" {
		return "compression"
	}
	if entry.OriginActor != nil {
		switch *entry.OriginActor {
		case "auto-title-generator":
			return "title_gen"
		case "auto-summary-generator", "session-summary":
			return "summary"
		}
	}
	// 有父请求但无压缩/actor：归为 other（扩展请求）。
	if entry.ParentRequestID != nil && *entry.ParentRequestID != "" {
		return "other"
	}
	return "main"
}

func newClientWithBufSize(bufSize int) *Client {
	c := &Client{
		queue: make(chan any, bufSize),
		done:  make(chan struct{}),
	}
	c.wg.Add(1)
	go c.worker()
	return c
}

func (c *Client) Enabled() bool {
	return c != nil && (c.pgEnabled() || c.requestSink() != nil)
}

// pgEnabled 报告是否配置了 PostgreSQL 持久化后端（历史 Enabled 语义）。
// 决策日志与 context_attrs 侧表只有 PG 实现，其发射门槛继续用本方法，
// 注入 lite sink 不会让这两类条目进入队列后在 flush 侧反复失败告警。
func (c *Client) pgEnabled() bool {
	return c != nil && (c.dbPool != nil || c.requestLogDB != nil)
}

// findRecentGatewaySessionSQL 是会话粘滞查找（请求热路径）。2026-10-13
// S3 波 2（停写切换）：v1 宽表已停写，新流量的 gw_session_id 只落
// session_turns_hot（api_key_id/identity_hash 由 707 S1a 加列，
// idx_session_turns_hot_ts 供窗口过滤）。
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 那条 UNION 腿整段删除——v1 族随 890
// 落库后读它必然 42P01。窗口 ≤ 热表保留期，不需要 parent 腿。
// 提升为包级变量是给形状钉桩用的。
var findRecentGatewaySessionSQL = `
		SELECT session_id FROM (
			SELECT t.session_id AS session_id, t.ts AS ts
			FROM public.session_turns_hot t
			WHERE t.tenant_id = $1
			  AND t.api_key_id = $2::text
			  AND t.identity_hash = $3
			  AND t.session_id LIKE 'gw\_%'
			  AND t.ts >= NOW() - ($4 * INTERVAL '1 second')
		) recent_session
		ORDER BY ts DESC
		LIMIT 1
	`

func (c *Client) FindRecentGatewaySession(ctx context.Context, tenantID, identityHash string, apiKeyID int, since time.Duration) (string, error) {
	if c == nil || c.dbPool == nil || tenantID == "" || identityHash == "" || apiKeyID <= 0 {
		return "", nil
	}
	if since <= 0 {
		since = 5 * time.Minute
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var sessionID string
	// 会话查找窗口（默认 5 分钟）落在会话族热表的保留期内，查热表
	// （独立 heap 表，性能最优）即可。
	// 符合 docs/partition/partition-standards.md 查询规范。
	err := c.dbPool.QueryRow(queryCtx, findRecentGatewaySessionSQL, tenantID, apiKeyID, identityHash, int(since.Seconds())).Scan(&sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", nil
		}
		return "", err
	}
	return sessionID, nil
}

func (c *Client) SetDB(pool *pgxpool.Pool) {
	// Typed-nil guard: (*pgxpool.Pool)(nil) stored into the requestLogDB
	// interface would make requestLogDatabase() report non-nil and the
	// insert path SIGSEGV on Begin (request_log_database_test contract).
	if pool == nil {
		c.dbPool = nil
		c.requestLogDB = nil
		return
	}
	c.dbPool = pool
	c.requestLogDB = pool
}

// SetRequestLogSink 注入 lite 存储模式的请求日志 sink（见 RequestLogSink 文档）。
// 传入 nil 等价于摘除 sink，Client 回到纯 PG 行为。可在任意时刻调用
// （内部 atomic.Value 存储，与运行中的 worker 并发安全）。
func (c *Client) SetRequestLogSink(sink RequestLogSink) {
	if c == nil {
		return
	}
	c.requestLogSinkStore.Store(requestLogSinkHolder{sink: sink})
}

// requestSink 返回当前注入的 sink（未注入时 nil）。atomic 读，热路径安全。
func (c *Client) requestSink() RequestLogSink {
	if c == nil {
		return nil
	}
	if h, ok := c.requestLogSinkStore.Load().(requestLogSinkHolder); ok {
		return h.sink
	}
	return nil
}

// SetBodyMirror 注入热区请求侧 body 镜像回调（见 BodyMirrorFunc 文档）。
// 传入 nil 等价于摘除镜像，Client 回到纯落库行为（no-op）。可在任意时刻调用
// （内部 atomic.Value 存储，与运行中的 worker 并发安全）。装配层仅在 full
// 模式热区启用时注入（lite 沿用既有 session_bodies 写入路径，不重复镜像）。
func (c *Client) SetBodyMirror(fn BodyMirrorFunc) {
	if c == nil {
		return
	}
	c.bodyMirrorStore.Store(bodyMirrorHolder{fn: fn})
}

// bodyMirrorFn 返回当前注入的镜像回调（未注入时 nil）。atomic 读，热路径安全。
func (c *Client) bodyMirrorFn() BodyMirrorFunc {
	if c == nil {
		return nil
	}
	if h, ok := c.bodyMirrorStore.Load().(bodyMirrorHolder); ok {
		return h.fn
	}
	return nil
}

func (c *Client) requestLogDatabase() requestLogDB {
	if c == nil {
		return nil
	}
	if c.requestLogDB != nil {
		return c.requestLogDB
	}
	// Typed-nil guard: returning the *pgxpool.Pool field directly would wrap
	// a nil pointer into a non-nil interface and defeat every `db == nil`
	// check downstream (SIGSEGV on Begin — request_log_database_test).
	if c.dbPool == nil {
		return nil
	}
	return c.dbPool
}

// DBPool 返回底层 *pgxpool.Pool,供需要直接操作 PG 的模块使用。
// 仅返回已注入的 pool, 未注入时返回 nil。
// 用途示例: trace.Recorder.FlushToPG 直接复用同一连接池。
//
// 调用方不得关闭该 pool(其生命周期由 Client 管理方负责)。
func (c *Client) DBPool() *pgxpool.Pool {
	if c == nil {
		return nil
	}
	return c.dbPool
}

func (c *Client) SetDegraded(enabled bool) { c.degraded.Store(enabled) }

func (c *Client) SetFallbackWriter(writer dbdegradation.BackupWriter) {
	c.fallback = writer
}

// SetOutboxWriter registers an outbox writer for Gateway → ASM event delivery.
// Phase 3 WP4: Business write point integration.
func (c *Client) SetOutboxWriter(writer OutboxWriter) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.outboxWriter = writer
}

func (c *Client) ReplayFallback(ctx context.Context, record dbdegradation.BackupRecord) error {
	var entry RequestLogEntry
	if err := json.Unmarshal(record.Payload, &entry); err != nil {
		return err
	}
	normalizeRequestStatus(&entry)
	var err error
	if entry.Op == RequestLogUpdate {
		err = c.updateRequestLog(&entry)
	} else {
		err = c.insertRequestLog(&entry)
	}
	if err == nil {
		// R44 (storage ledger E6, 09-15/09-18 各 1 行 G2 缺失实锤): 回放路径
		// 此前绕过 onPersisted hooks —— 同事务的 final-success claim 置位
		// is_final_success 后 sessionv2mirror hook 永远收不到终态信号，
		// 镜像行结构性缺失且重放器无法归零（无登记可重放）。进 fallback
		// 文件的 entry 此前必然从未成功落库（degraded/写失败路径进入），
		// hooks 从未对它触发过，此处补发即 exactly-once。
		c.firePersistedHooks(&entry)
	}
	return err
}

// SetOnRequestLogPersisted registers the sole persisted hook (replaces any prior hooks).
// Prefer AddOnRequestLogPersisted when multiple consumers are needed.
func (c *Client) SetOnRequestLogPersisted(fn func(entry *RequestLogEntry)) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if fn == nil {
		c.onPersisted = nil
		return
	}
	c.onPersisted = []func(entry *RequestLogEntry){fn}
}

// AddOnRequestLogPersisted appends a hook invoked after each successful
// INSERT/UPDATE of a request-log row. Hooks run on the telemetry worker
// goroutine and must be cheap and non-blocking.
//
// 2026-07-27 concurrency fix: append under lifecycleMu.Lock(). The worker
// goroutine reads onPersisted under RLock (see persistEntry), so an unlocked
// append here is a concurrent map/slice write that -race flags and that can
// lose a just-registered hook.
func (c *Client) AddOnRequestLogPersisted(fn func(entry *RequestLogEntry)) {
	if fn == nil {
		return
	}
	c.lifecycleMu.Lock()
	c.onPersisted = append(c.onPersisted, fn)
	c.lifecycleMu.Unlock()
}

// SetOnRequestLogEmitted registers a hook invoked immediately when
// EmitRequestLog is called, BEFORE DB persistence. This provides
// faster real-time updates from the in-memory pipeline. The hook
// runs on the caller's goroutine and MUST be non-blocking (use
// select with default for channel sends). Pass nil to clear.
//
// 2026-07-27 concurrency fix: guard with lifecycleMu — EmitRequestLog reads
// onEmitted under RLock, so the assignment must take the same lock.
func (c *Client) SetOnRequestLogEmitted(fn func(entry *RequestLogEntry)) {
	c.lifecycleMu.Lock()
	c.onEmitted = fn
	c.lifecycleMu.Unlock()
}

// SetOnDecisionLogEmitted registers a hook invoked synchronously inside
// EmitDecisionLog (before the queue/sync-insert decision), whenever the
// client is live enough to accept a decision row (not stopped, PG enabled).
// It mirrors SetOnRequestLogEmitted: runs on the caller's goroutine and MUST
// be non-blocking. Primary consumer is test capture (routing_decision_log
// has no lite sink, so a real DB pool would otherwise be required to observe
// emissions); production code may use it for live mirrors.
//
// 2026-09-09 audit round 3 (#4): introduced for the rate-limited
// not-run-class decision row wiring test.
func (c *Client) SetOnDecisionLogEmitted(fn func(entry *DecisionLogEntry)) {
	c.lifecycleMu.Lock()
	c.onDecisionEmitted = fn
	c.lifecycleMu.Unlock()
}

func (c *Client) EmitDecisionLog(entry *DecisionLogEntry) {
	if c == nil {
		return
	}
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	// 决策日志只有 PG 表（routing_decision_log_hot），无 lite sink；
	// 注入 lite sink 时（Enabled()==true）也必须保持跳过，避免入队后
	// 在 flush 侧以 errNoTelemetryDB 反复告警。
	if c.stopped.Load() || !c.pgEnabled() {
		return
	}
	if c.onDecisionEmitted != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("telemetry onDecisionEmitted panic", "request_id", entry.RequestID)
				}
			}()
			c.onDecisionEmitted(entry)
		}()
	}
	select {
	case c.queue <- entry:
	default:
		// Decision logs power /routing-decisions — never silently drop on backpressure.
		if err := c.insertDecisionLog(entry); err != nil {
			slog.Warn("telemetry decision sync insert failed", "request_id", entry.RequestID, "error", err)
		}
	}
}

func (c *Client) EmitRequestLog(entry *RequestLogEntry) {
	if c == nil {
		return
	}
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	if c.stopped.Load() || !c.Enabled() {
		return
	}
	if entry.Op == "" {
		entry.Op = RequestLogInsert
	}

	// Fire the onEmitted hook immediately from the caller's goroutine
	// BEFORE queuing. This provides faster real-time updates from the
	// in-memory pipeline rather than waiting for DB write completion.
	if c.onEmitted != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("telemetry onEmitted panic", "request_id", entry.RequestID)
				}
			}()
			c.onEmitted(entry)
		}()
	}

	select {
	case c.queue <- entry:
	default:
		// Request logs power /request-logs — never silently drop on backpressure.
		if err := c.persistRequestLog(entry); err != nil {
			atomic.AddUint64(&c.failPermanent, 1)
			if diag := pgErrorDiagnostics(err); diag != "" {
				slog.Warn("telemetry pg error diagnostics", "request_id", entry.RequestID, "op", entry.Op, "diagnostics", diag)
			}
			if c.fallback != nil {
				if fallbackErr := c.fallback.WriteRequestLog(context.Background(), entry.RequestID+":"+string(entry.Op), entry); fallbackErr != nil {
					slog.Warn("telemetry request sync fallback failed", "request_id", entry.RequestID, "db_error", err, "fallback_error", fallbackErr)
				} else {
					atomic.AddUint64(&c.failFallback, 1)
					slog.Warn("telemetry request db persist failed; fallback written", "request_id", entry.RequestID, "op", entry.Op, "error", err)
				}
			} else {
				slog.Warn("telemetry request sync persist failed", "request_id", entry.RequestID, "op", entry.Op, "error", err)
			}
		}
	}
}

func (c *Client) EmitRequestLogInsert(entry *RequestLogEntry) {
	if entry == nil || entry.RequestID == "" {
		// 2026-08-23 (audit P1-1): INSERT path UPSERTs on request_id with
		// ON CONFLICT (client.go:917). An empty RequestID would create a
		// brand-new orphan row keyed by "" — strictly worse than the
		// UPDATE path which at least collides with an existing orphan.
		// Mirror the UPDATE guard so the two paths share one source of
		// truth. nil entry is defensive — current callers never pass nil
		// but the cost of one branch is negligible.
		incSanitizeEvent("discarded", "request_id", "string_field", "required_field_guard")
		return
	}
	entry.Op = RequestLogInsert
	entry.TenantID = nonEmpty(entry.TenantID, "default")
	c.EmitRequestLog(entry)
}

func (c *Client) EmitRequestLogUpdate(entry *RequestLogEntry) {
	if entry == nil {
		// 2026-08-23 (audit P2-1): defensive nil guard so a future caller
		// doesn't panic on entry.RequestID. Counter bump uses the same
		// tuple as the RequestID guard so dashboards see one signal.
		incSanitizeEvent("discarded", "request_id", "string_field", "required_field_guard")
		return
	}
	entry.Op = RequestLogUpdate

	// 2026-08-23 (245 incident, minimax-m3): guard against silently writing
	// a useless UPDATE. The persistRequestLog → updateRequestLog path UPSERTs
	// on request_id; if it is empty the row becomes orphan and the WAL has
	// no key to reconcile against. TenantID defaults to the same fallback
	// insertRequestLog uses via nonEmpty() (currently "default") so the
	// required-field guard matches INSERT semantics — do NOT hardcode
	// "default" here or the two paths will drift.
	// Success/RequestStatus are not strictly nullable on UPDATE (a late
	// enrichment may legitimately omit them), so this guard intentionally
	// only blocks the truly useless writes.
	if entry.RequestID == "" {
		slog.Error("telemetry EmitRequestLogUpdate dropped: missing request_id",
			"client_model", stringValue(entry.ClientModel),
			"outbound_model", stringValue(entry.OutboundModel))
		incSanitizeEvent("discarded", "request_id", "string_field", "required_field_guard")
		return
	}
	entry.TenantID = nonEmpty(entry.TenantID, "default")

	c.EmitRequestLog(entry)
}

func (c *Client) Stop() {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	if c.stopped.Swap(true) {
		c.lifecycleMu.Unlock()
		c.wg.Wait()
		return
	}
	close(c.done)
	c.lifecycleMu.Unlock()
	c.wg.Wait()
}

// FailCounts returns a snapshot of telemetry write failure counters.
// Safe to call concurrently from any goroutine. Use these values
// to alert when permanent/fallback failures grow faster than the
// gateway can recover (see incident 2026-07-16 where 12 rows dropped
// silently to "audit_log insert failed" with no surfaced metric).
func (c *Client) FailCounts() (transient, permanent, retried, fallback uint64) {
	if c == nil {
		return 0, 0, 0, 0
	}
	return atomic.LoadUint64(&c.failTransient),
		atomic.LoadUint64(&c.failPermanent),
		atomic.LoadUint64(&c.failRetried),
		atomic.LoadUint64(&c.failFallback)
}

func (c *Client) worker() {
	defer c.wg.Done()

	batch := make([]any, 0, 50)
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case <-c.done:
			c.flush(batch)
			return
		case item := <-c.queue:
			batch = append(batch, item)
			if len(batch) >= 50 {
				c.flush(batch)
				batch = batch[:0]
				timer.Reset(200 * time.Millisecond)
			} else if len(batch) == 1 {
				timer.Reset(200 * time.Millisecond)
			}
		case <-timer.C:
			if len(batch) > 0 {
				c.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (c *Client) flush(batch []any) {
	batch = mergeRequestLogBatch(batch)
	for _, item := range batch {
		switch v := item.(type) {
		case *DecisionLogEntry:
			if err := c.insertDecisionLog(v); err != nil {
				slog.Warn("telemetry decision db insert failed", "request_id", v.RequestID, "error", err)
			}
		case *RequestLogEntry:
			if err := c.persistRequestLog(v); err != nil {
				atomic.AddUint64(&c.failPermanent, 1)
				if diag := pgErrorDiagnostics(err); diag != "" {
					slog.Warn("telemetry pg error diagnostics", "request_id", v.RequestID, "op", v.Op, "diagnostics", diag)
				}
				if c.fallback != nil {
					if fallbackErr := c.fallback.WriteRequestLog(context.Background(), v.RequestID+":"+string(v.Op), v); fallbackErr != nil {
						slog.Warn("telemetry request fallback failed", "request_id", v.RequestID, "db_error", err, "fallback_error", fallbackErr)
					} else {
						atomic.AddUint64(&c.failFallback, 1)
						slog.Warn("telemetry request db persist failed; fallback written", "request_id", v.RequestID, "op", v.Op, "error", err)
					}
				} else {
					slog.Warn("telemetry request db persist failed", "request_id", v.RequestID, "op", v.Op, "error", err)
				}
			}
		case *ContextAttrsEntry:
			// 2026-07-15: 侧表 request_context_attrs。失败仅日志，不阻塞主请求日志。
			if err := c.persistContextAttrs(v); err != nil {
				slog.Warn("telemetry context_attrs persist failed",
					"request_id", v.RequestID, "error", err)
			}
		}
	}
}

func (c *Client) insertDecisionLog(entry *DecisionLogEntry) error {
	if c.dbPool == nil {
		return errNoTelemetryDB
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rawModelsJSON, _ := json.Marshal(coalesceRawModels(entry.ResolutionRawModels))
	traceJSON := coalesceTrace(entry.DecisionTrace)
	// INSERT directly targets routing_decision_log_hot (the canonical
	// write target per the 2026-07 data-lifecycle architecture). Monthly
	// partitions are pre-created for current+next month by
	// bg/partition_manager.go ensure_routing_decision_log_partition(), but
	// INSERTs always land in *_default and a separate background migrator
	// moves historical rows into the matching month partition. Bypassing
	// the parent's auto-routing ensures writes never accidentally land in
	// a non-default partition (which would block subsequent UPDATEs once
	// the partition is converted to columnar).
	// 2026-07-16: JSONB columns use $N::text::jsonb (not CAST($N AS jsonb))
	// to fix pgx binary protocol 22P02 error — same fix as apihub/pg_store.go:71.
	_, err := c.dbPool.Exec(ctx, `
		INSERT INTO routing_decision_log_hot (
			ts, request_id, idempotency_key, tenant_id, api_key_id,
			model, chosen_credential_id, chosen_provider_id, tier,
			candidates_tried, latency_ms, success, error_class,
			prompt_tokens, completion_tokens, cost_usd,
			request_bytes, response_bytes,
			client_model, resolved_raw_model, sticky_hit, client_profile,
			outbound_model, request_mode, identity_hash, transform_rule_id,
			egress_protocol, failure_stage, failure_detail_code,
			resolution_path, canonical_model, resolution_raw_models, decision_trace
		) VALUES (
			now(), $1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15,
			$16, $17,
			$18, $19, $20, $21,
			$22, $23, $24, $25,
			$26, $27, $28,
			$29, $30, $31::text::jsonb, $32::text::jsonb
		)
	`,
		entry.RequestID,
		entry.IdempotencyKey,
		nonEmpty(entry.TenantID, "default"),
		entry.APIKeyID,
		entry.Model,
		entry.ChosenCredentialID,
		entry.ChosenProviderID,
		entry.Tier,
		entry.CandidatesTried,
		entry.LatencyMs,
		entry.Success,
		entry.ErrorClass,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CostUSD,
		entry.RequestBytes,
		entry.ResponseBytes,
		entry.ClientModel,
		entry.ResolvedRawModel,
		entry.StickyHit,
		entry.ClientProfile,
		entry.OutboundModel,
		entry.RequestMode,
		entry.IdentityHash,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.ResolutionPath,
		entry.CanonicalModel,
		string(rawModelsJSON),
		string(traceJSON),
	)
	return err
}

// firePersistedHooks invokes the onPersisted hooks (panic-isolated) after a
// successful INSERT/UPDATE of a request-log row. Shared by persistRequestLog
// (worker/sync paths) and ReplayFallback (degraded-recovery replay path).
func (c *Client) firePersistedHooks(entry *RequestLogEntry) {
	c.lifecycleMu.RLock()
	hooks := append([]func(*RequestLogEntry){}, c.onPersisted...)
	c.lifecycleMu.RUnlock()
	for _, hook := range hooks {
		func(h func(*RequestLogEntry)) {
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("telemetry onPersisted panic", "request_id", entry.RequestID)
				}
			}()
			h(entry)
		}(hook)
	}
}

func (c *Client) persistRequestLog(entry *RequestLogEntry) error {
	normalizeRequestStatus(entry)
	// H3 请求侧镜像（2026-09-24 方案 §3-H3）：在任何 PG 往返（含 tx.Begin）
	// 与 degraded 早退之前 fire-and-forget 投递三件套镜像——PG 不可用、
	// degraded 回落、lite sink 路径下镜像仍持续写入（H3 验收项「PG 不可用时
	// 镜像仍写入」）。显式复用 requestLogsWriteEnabled()（S4 停写 gate），
	// 停写时镜像同步停，避免「停写却仍落盘」语义分裂。
	// 空载荷在 helper 内逐项跳过；三件套换算与会话族正文落库同源。
	if requestLogsWriteEnabled() {
		c.mirrorRequestBodies(entry)
	}
	if c.degraded.Load() {
		if c.fallback == nil {
			return errNoTelemetryDB
		}
		return c.fallback.WriteRequestLog(context.Background(), entry.RequestID+":"+string(entry.Op), entry)
	}
	var err error
	// lite 存储模式（2026-09-05 审计 B2）：无 PG 后端但注入了 sink 时，
	// INSERT/UPDATE 两类 op 统一交给 sink（实现方负责按 request_id 幂等），
	// 成功后走与 PG 路径完全相同的 onPersisted hooks / releaseBodies 收尾。
	if sink := c.requestSink(); sink != nil && c.requestLogDatabase() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = sink.PersistRequestLog(ctx, entry)
		cancel()
	} else if entry.Op == RequestLogUpdate {
		err = c.updateRequestLog(entry)
	} else {
		err = c.insertRequestLog(entry)
	}
	if err == nil {
		c.firePersistedHooks(entry)
		if entry.Success && len(strings.TrimSpace(stringValue(entry.ResponseBody))) == 0 {
			metrics.RecordSuccessfulResponseBodyMissing()
			slog.Warn("successful request log missing response body",
				"request_id", entry.RequestID,
				"tenant_id", entry.TenantID,
				"credential_id", entry.CredentialID,
				"provider_id", entry.ProviderID,
				"operation", entry.Op,
			)
		}
		entry.releaseBodies()
	}
	return err
}

// releaseBodies drops the three largest telemetry payloads only after their
// transaction committed and every synchronous persisted hook consumed entry.
// Failed and degraded writes retain bodies so the fallback writer can retry.
func (entry *RequestLogEntry) releaseBodies() {
	if entry == nil {
		return
	}
	entry.RequestBody = nil
	entry.ResponseBody = nil
	entry.OutboundBody = nil
}

// mirrorRequestBodies 把三件套（request/response/outbound）按与会话族正文
// 落库完全相同的换算函数投递给热区镜像：
//
//	requestBody  = strPtrToJSON(entry.RequestBody)  → "req"
//	responseBody = strPtrToJSON(entry.ResponseBody) → "resp"
//	outbound     = jsonOrNull(entry.OutboundBody)   → "out"
//
// 换算的空值语义：strPtrToJSON 对 nil → "null"、空串/非法 JSON → "{}"；
// jsonOrNull 对空 RawMessage → "null"。PG 侧 NULLIF 仅剔除 'null'，"{}" 会
// 照落库；镜像侧两类都跳过——无正文内容可对账，字面 "null"/"{}" 文件只是
// 噪声（对账脚本需豁免 PG 侧对应行）。tenant 取 application_code（与
// bodies_hot 的 tenant 口径一致），空串回退 entry.TenantID，双空则整体跳过
// （镜像路径以 tenant 命名目录，空值必被 validMirrorID 拒绝并误计失败指标，
// 静默跳过更干净）。未注入镜像（nil）时整体 no-op。独立成方法是为了测试
// 不依赖 DB。
func (c *Client) mirrorRequestBodies(entry *RequestLogEntry) {
	mirror := c.bodyMirrorFn()
	if mirror == nil || entry == nil || entry.RequestID == "" {
		return
	}
	tenant := stringValue(entry.ApplicationCode)
	if tenant == "" {
		tenant = entry.TenantID
	}
	if tenant == "" {
		return
	}
	// 镜像文件的日期分区取样时刻对位 PG 侧的 ts=now() 语义；
	// entry.EventAt 目前无 emitter 盖章（仅 merge / ReplayFallback
	// 反序列化可能携带），非 nil 时优先采用以对齐重放语义。
	at := time.Now()
	if entry.EventAt != nil {
		at = *entry.EventAt
	}
	if req := strPtrToJSON(entry.RequestBody); mirrorableBody(req) {
		mirror(tenant, entry.RequestID, "req", json.RawMessage(req), at)
	}
	if resp := strPtrToJSON(entry.ResponseBody); mirrorableBody(resp) {
		mirror(tenant, entry.RequestID, "resp", json.RawMessage(resp), at)
	}
	if out := jsonOrNull(entry.OutboundBody); mirrorableBody(out) {
		mirror(tenant, entry.RequestID, "out", json.RawMessage(out), at)
	}
}

// mirrorableBody 报告换算后的载荷是否值得镜像："null"（无数据）与 "{}"
// （空串/非法 JSON 的收敛值）跳过。
//
// 2026-10-01 F5：判定规则下沉到 storage/file.MirrorablePayload 单一事实源，
// 与 admin ingest 共用（见 ConvertBodyPayload 的同批说明）。
func mirrorableBody(payload string) bool {
	return filestore.MirrorablePayload(payload)
}

func (c *Client) insertRequestLog(entry *RequestLogEntry) error {
	db := c.requestLogDatabase()
	if db == nil {
		return errNoTelemetryDB
	}
	// 纵深守卫（第三十轮）：request_id 是 DISTINCT ON 去重键（预算执行面
	// checkBudgetDB 与观测面同款），空串行会被折叠成 1 行 → SUM 少算 →
	// 提前放行（fail-open 方向）。上游 EmitRequestLogInsert/Update 双守卫
	// 已拦截空值，RequestID 恒为服务端 UUID（migration 054 + requestid_mw），
	// 本断言零误伤——能到这里还带空 request_id 的行本就不该存在。
	if entry.RequestID == "" {
		incSanitizeEvent("discarded", "request_id", "string_field", "required_field_guard")
		return fmt.Errorf("telemetry: request_log row without request_id")
	}
	// Keep request class/due time inside the database domain before the
	// asynchronous writer begins its transaction. This prevents a malformed
	// caller from turning one bad audit field into a retried write failure.
	normalizeRequestClassAndDueAt(entry, false)
	// Defence-in-depth: scrub any invalid UTF-8 from all string-valued fields
	// before INSERT.  PostgreSQL rejects invalid bytes with SQLSTATE 22021,
	// which (because we wrap usage_ledger + api_keys updates in a single
	// transaction) causes the entire row to be dropped — exactly the symptom
	// reported in the 2026-06-11 incident where glm-5.1 calls succeeded for
	// the client but no request-log row was written.  Even after fixing the
	// upstream truncation sites, this layer guarantees that no future
	// regression can silently lose rows.
	sanitizeRequestLogEntry(entry)
	totalTokens := total(entry.PromptTokens, entry.CompletionTokens)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 宽表写入分支整段删除 ⇒ 停写键
	// （storage.request_logs_write_enabled）在本函数**不再门控任何 PG 写入**；
	// 会话族（sessionv2mirror）成为唯一 SSOT ⇒ 会话写入**恒为必需**。
	entry.SessionV2WriteRequired = true

	// §9.51：进程内指纹 arm 必须与持久化写入解耦——它记录的是「本实例见过带
	// 指纹的请求」，不是「值落进了哪张表」。此前 arm 落在 v1 的落列函数里，
	// 会被停写门一起关掉（详见 observeSystemFingerprint）。
	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 写入分支整段删除后，这里已无门控。
	observeSystemFingerprint(entry)

	// INSERT directly targets usage_ledger_hot (the canonical write
	// target per the 2026-07 data-lifecycle architecture). UPDATE-heavy
	// operations (cost/tokens/latency enrichment after streaming) require
	// a heap-storage row that supports UPDATE in place. *_default stays
	// heap; the matching month partition (after migration) is also heap
	// (columnar cannot hold UPDATE-heavy data).
	_, err = tx.Exec(ctx, `
		INSERT INTO usage_ledger_hot (
			request_id, ts, tenant_id, application_id, api_key_id,
			end_user_id, credential_id, provider_id, canonical_id,
			raw_model_name, prompt_tokens, completion_tokens,
			cache_read_tokens, cache_write_tokens,
			total_tokens, cost_usd, latency_ms, success, error_kind,
			rate_multiplier
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13,
			$14, $15, $16, $17, $18,
			COALESCE($19, 1.0)
		)
	`,
		entry.RequestID,
		nonEmpty(entry.TenantID, "default"),
		entry.ApplicationID,
		entry.APIKeyID,
		entry.EndUserID,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		firstString(entry.OutboundModel, entry.ClientModel),
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		totalTokens,
		entry.CostUSD,
		entry.LatencyMs,
		entry.Success,
		entry.ErrorKind,
		entry.RateMultiplier,
	)
	if err != nil {
		return err
	}
	// ★ 2026-10-20（迁移 890，v1 族退役）：本段 v1 宽表写入分支（主行 UPSERT
	// + protocol/fingerprint 增补 + v1 正文族）整段删除——v1 族随 890 落库后写
	// 它必然 42P01。usage_ledger 计费、api_keys 计数与 outbox 会话开启事件照常
	// 提交；会话族镜像承接本请求的事实落点。

	if entry.APIKeyID != nil && *entry.APIKeyID > 0 && entry.Success {
		var promptAdd, completionAdd int64
		if entry.PromptTokens != nil {
			promptAdd = int64(*entry.PromptTokens)
		}
		if entry.CompletionTokens != nil {
			completionAdd = int64(*entry.CompletionTokens)
		}
		var costAdd float64
		if entry.CostUSD != nil {
			costAdd = *entry.CostUSD
		}
		_, err = tx.Exec(ctx, `
			UPDATE api_keys SET
				total_requests = total_requests + 1,
				total_prompt_tokens = total_prompt_tokens + $2,
				total_completion_tokens = total_completion_tokens + $3,
				total_cost_usd = total_cost_usd + $4,
				last_request_at = now()
			WHERE id = $1
		`, *entry.APIKeyID, promptAdd, completionAdd, costAdd)
		if err != nil {
			return err
		}
	}

	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 的终态成功认领整段删除（见
	// updateRequestLog 同处说明）⇒ 认领资格直接挂到 entry 上，由会话族自己
	// 仲裁（session_turns 的 partial 唯一索引 + markTurnFinalSuccess）。
	if shouldClaimFinalSuccess(entry) {
		entry.FinalSuccessClaimed = true
	}

	// Publish only the session opener here. The request is provisional until
	// its terminal update supplies final usage, latency, and status.
	if c.outboxWriter != nil && entry.GwSessionID != nil && *entry.GwSessionID != "" {

		tenantID := nonEmpty(entry.TenantID, "default")
		userID := "unknown"
		if entry.EndUserID != nil && strings.TrimSpace(*entry.EndUserID) != "" {
			userID = *entry.EndUserID
		} else if entry.APIKeyID != nil {
			userID = strconv.Itoa(*entry.APIKeyID)
		}
		opened, err := outbox.BuildSessionOpenedEventV1(tenantID, stringValue(entry.GwSessionID), userID)
		if err != nil {
			return err
		}
		if err := insertSessionOpenedEvent(ctx, tx, opened, entry.RequestID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// insertSessionOpenedEvent writes the idempotent session opener as part of the
// request-log transaction.
func insertSessionOpenedEvent(ctx context.Context, tx pgx.Tx, envelope outbox.EventEnvelope, requestID string) error {
	payloadJSON, err := json.Marshal(envelope.Payload)
	if err != nil {
		return fmt.Errorf("marshal session opened payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		ON CONFLICT (event_id) DO NOTHING
	`,
		envelope.EventID,
		envelope.EventType,
		envelope.SchemaVersion,
		envelope.TenantID,
		envelope.AggregateID,
		envelope.AggregateVersion,
		envelope.OccurredAt,
		// 13 轮审计（R11 FIX-C 同根）：pool 强制 SimpleProtocol，[]byte 内联
		// 成 bytea hex 字面量，jsonb 列解析必炸——WP4 ASM 双 env 配置后该
		// 错误随请求日志事务整体上抛。string 化（client.go:2722 同款）。
		string(payloadJSON),
	)
	if err != nil {
		return fmt.Errorf("insert session opened event for request %s: %w", requestID, err)
	}
	return nil
}

func insertRequestCompletedEvent(ctx context.Context, tx pgx.Tx, envelope outbox.EventEnvelope) error {
	payloadJSON, err := json.Marshal(envelope.Payload)
	if err != nil {
		return fmt.Errorf("marshal request completed payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (
			event_id, event_type, schema_version, tenant_id,
			aggregate_id, aggregate_version, occurred_at, payload, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		ON CONFLICT (event_id) DO NOTHING
	`,
		envelope.EventID,
		envelope.EventType,
		envelope.SchemaVersion,
		envelope.TenantID,
		envelope.AggregateID,
		envelope.AggregateVersion,
		envelope.OccurredAt,
		// 13 轮审计（R11 FIX-C 同根）：同 insertSessionOpenedEvent，jsonb
		// 参数必须 string 化，[]byte 在 SimpleProtocol 下必炸。
		string(payloadJSON),
	)
	if err != nil {
		return fmt.Errorf("insert request completed event: %w", err)
	}
	return nil
}

func requestLogEntryTerminal(entry *RequestLogEntry) bool {
	if entry == nil || entry.RequestStatus == nil {
		return false
	}
	switch *entry.RequestStatus {
	case RequestStatusSuccess, RequestStatusFailure, RequestStatusRateLimited:
		return true
	default:
		return false
	}
}

func buildRequestCompletedEvent(ctx context.Context, tx pgx.Tx, entry *RequestLogEntry) (outbox.EventEnvelope, error) {
	if entry == nil || entry.GwSessionID == nil || *entry.GwSessionID == "" {
		return outbox.EventEnvelope{}, fmt.Errorf("request completed event requires session_id")
	}
	sessionID := stringValue(entry.GwSessionID)
	tenantID := nonEmpty(entry.TenantID, "default")
	requestID := entry.RequestID
	provider := lookupProviderName(ctx, tx, entry.ProviderID)
	model := stringValue(entry.OutboundModel)
	if model == "" {
		model = stringValue(entry.ClientModel)
	}
	return outbox.BuildRequestCompletedEventV3(
		tenantID, sessionID, lookupTurnNumber(ctx, tx, sessionID),
		requestID, stringValue(entry.ClientRequestID), requestID,
		provider, model, stringValue(entry.RequestStatus),
		intValue(entry.PromptTokens), intValue(entry.CompletionTokens), intValue(entry.LatencyMs),
		entry.CostUSD, entry.Success,
	)
}

// CorrectEstimatedUsage 把 entry 里的真实用量校正到「估算值」上。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本函数**不再改写任何行**——它校正的是
// v1 宽表的 usage_source 列，v1 族随 890 落库后写它必然 42P01。签名与返回值
// 保持不变（调用方 domains/streaming/usage_backfill.go 用返回的修正行数门控
// 后续动作），恒返回 (0, nil) = 「本次没有发生校正」。
//
// ⚠ 校正这件事本身并没有消失：usage_ledger_hot 的用量由 telemetry 的写入路径
// 直接写权威值，本函数只是历史兼容入口。
func (c *Client) CorrectEstimatedUsage(ctx context.Context, entry *RequestLogEntry) (int64, error) {
	if c == nil || c.requestLogDatabase() == nil {
		return 0, errNoTelemetryDB
	}
	if entry == nil || entry.RequestID == "" {
		return 0, nil
	}
	if entry.PromptTokens == nil && entry.CompletionTokens == nil {
		return 0, nil
	}
	return 0, nil
}

func (c *Client) updateRequestLog(entry *RequestLogEntry) error {
	db := c.requestLogDatabase()
	if db == nil {
		return errNoTelemetryDB
	}
	normalizeRequestClassAndDueAt(entry, true)
	sanitizeRequestLogEntry(entry)
	totalTokens := total(entry.PromptTokens, entry.CompletionTokens)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 宽表写入分支整段删除 ⇒ 停写键
	// （storage.request_logs_write_enabled）在本函数**不再门控任何 PG 写入**；
	// 会话族（sessionv2mirror）成为唯一 SSOT ⇒ 会话写入**恒为必需**。
	entry.SessionV2WriteRequired = true

	// §9.51：进程内指纹 arm 必须与持久化写入解耦——它记录的是「本实例见过带
	// 指纹的请求」，不是「值落进了哪张表」。此前 arm 落在 v1 的落列函数里，
	// 会被停写门一起关掉（详见 observeSystemFingerprint）。
	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 写入分支整段删除后，这里已无门控。
	observeSystemFingerprint(entry)

	// 审计 §9.93：此处原有的 `if requestLogEntryTerminal(entry) { settleAbandonedTurn(...) }`
	// 整段已删除。`settleAbandonedTurn` 的函数体是空实现——它被调用、被注释
	// 解释为「让『终态才动落点』这条不变式有位置可钉」，但钉住的什么也没发生。
	// 这正是审计里点名过的形态：承重判据没有单点，只有一具被命名过的空壳。
	// 真实落点只有一个动作（在终态 turn 上打标），它已经在
	// internal/sessionv2mirror/abandoned_turn.go 里，且打标本身只可能发生在
	// 终态路径上——不需要第二个函数来复述这件事。

	if entry.PromptTokens != nil || entry.CompletionTokens != nil {
		// UPDATE directly targets usage_ledger_hot — UPDATE-heavy
		// operations require heap storage with row-level UPDATE support.
		// *_default stays heap regardless of how monthly partitions are
		// (re)configured.
		_, err = tx.Exec(ctx, `
			UPDATE usage_ledger_hot
			   SET prompt_tokens = COALESCE($2, prompt_tokens),
			       completion_tokens = COALESCE($3, completion_tokens),
			       total_tokens = COALESCE($4, total_tokens),
			       cache_read_tokens = COALESCE($5, cache_read_tokens),
			       cache_write_tokens = COALESCE($6, cache_write_tokens),
			       cost_usd = COALESCE($7, cost_usd),
			       latency_ms = COALESCE($8, latency_ms),
			       success = COALESCE($9, success),
			       error_kind = COALESCE($10, error_kind)
			 WHERE request_id = $1
		`,
			entry.RequestID,
			entry.PromptTokens,
			entry.CompletionTokens,
			totalTokens,
			entry.CacheReadTokens,
			entry.CacheWriteTokens,
			entry.CostUSD,
			entry.LatencyMs,
			boolptr(entry.Success),
			entry.ErrorKind,
		)
		if err != nil {
			return err
		}
	} else {
		// UPDATE directly targets usage_ledger_hot.
		_, err = tx.Exec(ctx, `
			UPDATE usage_ledger_hot
			   SET latency_ms = COALESCE($2, latency_ms),
			       success = COALESCE($3, success),
			       error_kind = COALESCE($4, error_kind),
			       cost_usd = COALESCE($5, cost_usd)
			 WHERE request_id = $1
		`,
			entry.RequestID,
			entry.LatencyMs,
			boolptr(entry.Success),
			entry.ErrorKind,
			entry.CostUSD,
		)
		if err != nil {
			return err
		}
	}

	// ★ 2026-10-20（迁移 890，v1 族退役）：本段 v1 宽表写入分支（主行 UPDATE
	// 与回落 INSERT + v1 正文族 + protocol/fingerprint 增补）整段删除——v1 族随
	// 890 落库后写它必然 42P01。usage_ledger 刷新、api_keys 计数与 outbox
	// request-completed 会话事件在门控外照常提交；会话族镜像承接本请求的
	// 事实落点。

	if entry.APIKeyID != nil && *entry.APIKeyID > 0 && entry.Success {
		var promptAdd, completionAdd int64
		if entry.PromptTokens != nil {
			promptAdd = int64(*entry.PromptTokens)
		}
		if entry.CompletionTokens != nil {
			completionAdd = int64(*entry.CompletionTokens)
		}
		var costAdd float64
		if entry.CostUSD != nil {
			costAdd = *entry.CostUSD
		}
		_, err = tx.Exec(ctx, `
			UPDATE api_keys SET
				total_requests = total_requests + 1,
				total_prompt_tokens = total_prompt_tokens + $2,
				total_completion_tokens = total_completion_tokens + $3,
				total_cost_usd = total_cost_usd + $4,
				last_request_at = now()
			WHERE id = $1
		`, *entry.APIKeyID, promptAdd, completionAdd, costAdd)
		if err != nil {
			return err
		}
	}

	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 的终态成功认领整段删除（见
	// insertRequestLog 同处说明）⇒ 认领资格直接挂到 entry 上，由会话族自己
	// 仲裁（session_turns 的 partial 唯一索引 + markTurnFinalSuccess）。
	if shouldClaimFinalSuccess(entry) {
		entry.FinalSuccessClaimed = true
	}
	if c.outboxWriter != nil && entry.GwSessionID != nil && *entry.GwSessionID != "" && requestLogEntryTerminal(entry) {
		completed, err := buildRequestCompletedEvent(ctx, tx, entry)
		if err != nil {
			return err
		}
		if err := insertRequestCompletedEvent(ctx, tx, completed); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ★ 2026-10-20（迁移 890，v1 族退役）：upsertProtocolMetadata 整条删除——
// 它补写的是 v1 宽表的 client_protocol / upstream_protocol /
// protocol_conversion 三列，v1 族随 890 落库后写它必然 42P01。
// ⇒ 协议三元组由会话族的镜像写入路径承接。

// ★ 2026-10-20（迁移 890，v1 族退役）：persistSystemFingerprint 整条删除——
// 它把 entry.SystemFingerprint 落到 v1 宽表的专用列，v1 族随 890 落库后写它
// 必然 42P01。指纹值由会话族承接（internal/sessionv2mirror/s1a_fields.go
// 把 entry.SystemFingerprint 搬进会话族的写入载荷）。
//
// ⚠ 进程内的指纹观测 arm 不受影响：它在 observeSystemFingerprint 里，与写入
// 解耦（详见该函数的注释）。

// observeSystemFingerprint 记录「本实例见过带指纹的请求」。
//
// 这是指纹漂移检测器（bg/integrity_fingerprint_drift.go）**唯一**的进程内
// arm 来源，经 SystemFingerprintObservedSince() 读出。
//
// §9.51：它**必须**与持久化写入解耦。此前 arm 发生在 persistSystemFingerprint
// 内部，而那个函数被写门的条件包着 ⇒ 停写之后，
// 即便上游开始返回 `X-System-Fingerprint`，检测器也永远不会恢复扫描。
// arm 记录的是**观测**（本进程处理过一个带指纹的请求），与该请求是否落库
// 无关。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 宽表写入分支整段删除后，本函数的
// 调用点（insertRequestLog / updateRequestLog）已**无门控**——arm 永远生效，
// 这正是 §9.51 想要的形态。
func observeSystemFingerprint(entry *RequestLogEntry) {
	if entry == nil || entry.SystemFingerprint == nil || *entry.SystemFingerprint == "" {
		return
	}
	markSystemFingerprintObserved()
}

// systemFingerprintLastObserved tracks (in-process) when an entry carrying a
// non-empty system_fingerprint was last persisted. The integrity fingerprint
// drift worker (bg/integrity_fingerprint_drift.go) reads it to skip its full
// 7-day canonical-view scan while no fingerprint traffic exists at all —
// 2026-09-25 audit round 8 (D11): on 252 every fingerprint column is empty
// (upstream never returns X-System-Fingerprint), so the drift query was a
// pure full-scan no-op that the 30s rolconfig killed under load.
var systemFingerprintLastObserved atomic.Value // time.Time

// markSystemFingerprintObserved records that fingerprint traffic exists.
func markSystemFingerprintObserved() {
	systemFingerprintLastObserved.Store(time.Now())
}

// SystemFingerprintObservedSince reports how long ago the last
// fingerprint-carrying entry was persisted in this process, and whether any
// was ever observed after startup. Zero ok means "never seen since boot" —
// callers must not treat that as "no fingerprints exist in the DB".
func SystemFingerprintObservedSince() (d time.Duration, ok bool) {
	v, loaded := systemFingerprintLastObserved.Load().(time.Time)
	if !loaded {
		return 0, false
	}
	return time.Since(v), true
}

// shouldClaimFinalSuccess reports whether entry represents a terminal success
// that carries a session key and therefore must attempt the v4 T7
// session-level final-success claim. Failures, cancellations, client
// disconnects and rows without a gw_session_id never claim (R6.2: 空
// gw_session_id 不受唯一约束；失败/取消路径不改).
//
// 2026-09-17 R34: gateway-internal auto loopbacks (auto-title gt_/auto-summary
// gs_ branch sessions) must not claim either. sessionv2mirror excludes them
// from session_turns via IsInternalAutoEntry, so a claimed internal row would
// leave is_final_success=TRUE with no mirrored turn — permanently inflating
// the GLOBAL_G2 reconciliation counter. Business auto turns carry TaskType,
// fail the IsInternalAutoEntry test, and still claim normally. The 2026-09-16
// F1 fix started propagating IsAutoRequest onto success entries, which is what
// made internal loopbacks reachable for this predicate at all.
func shouldClaimFinalSuccess(entry *RequestLogEntry) bool {
	return entry != nil &&
		entry.Success &&
		entry.GwSessionID != nil &&
		*entry.GwSessionID != "" &&
		!IsInternalAutoEntry(entry)
}

// ★ 2026-10-20（迁移 890，v1 族退役）：claimSessionFinalSuccess 与
// heapRequestLogsPartitions 整条删除——前者是 v1 终态成功认领的入口，后者是
// 它依赖的 v1 月度分区名缓存（pg_class.relam='h' 的宽表月度分区）。
// ⇒ 会话族的终态成功打标见 internal/sessionv2mirror/final_success_turn.go。

// ★ 2026-10-20（迁移 890，v1 族退役）：claimSessionFinalSuccessExec 整条
// 删除——它是 v1 族的终态成功认领（事务内的 UPDATE ... SET is_final_success=TRUE
// + NOT EXISTS 半连接），v1 族随 890 落库后必然 42P01。
//
// ⇒ 认领的**仲裁**改由会话族自己做：session_turns 的 partial 唯一索引
// （uq_session_turns_hot_final_success）在会话族内部保证「一个会话一个最终
// 成功」；internal/sessionv2mirror 的 markTurnFinalSuccess 在 w.Write 返回
// 之后给那一行打标（23505 = 本会话别的轮次已认领 ⇒ 第一个认领者站住）。

// ★ 2026-10-20（迁移 890，v1 族退役）：registerFinalSuccessClaimOutbox 整条
// 删除——它是 v1 终态成功认领的**补偿**登记（往 session_mirror_outbox 写一行
// source='claim'），v1 认领没了，补偿也就没有对象。
// ⇒ 会话族的终态成功打标见 internal/sessionv2mirror/final_success_turn.go。

// ★ 2026-10-20（迁移 890，v1 族退役）：upsertRequestLogBodies 整条删除——
// 它是 v1 正文族（宽表正文侧表）的**唯一**写入点，v1 族随 890 落库后写它必然
// 42P01。PG 侧正文改由会话族的正文镜像承担（internal/sessionv2mirror 写
// session_turn_bodies）。
//
// ⚠ 本包的文件系统正文镜像（mirrorRequestBodies）**不受影响**：它不碰 PG，
// 仍由 requestLogsWriteEnabled() 门控。

func intptr(v int) *int           { return &v } //nolint:unused
func floatptr(v float64) *float64 { return &v } //nolint:unused
func strptr(v string) *string     { return &v } //nolint:unused
func boolptr(v bool) *bool        { return &v }

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func nonEmptyPtr(p *string, fallback string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return fallback
	}
	return *p
}

// qualityFlagsArg converts a nil/empty []string into a value suitable
// for binding to a text[] column.  The column is NOT NULL with a
// DEFAULT '{}'::text[] — but specifying a column explicitly in the
// INSERT (which the gateway must, since it has 60+ other columns)
// OVERRIDES the default and applies whatever the bind value is.
//
// qualityFlagsArg returns a pgx-friendly text array value for the text[] column.
// An explicit INSERT value bypasses the column default, so empty flags must be
// encoded as PostgreSQL's empty-array literal rather than a nil array value.
func qualityFlagsArg(flags []string) any {
	if len(flags) == 0 {
		return "{}"
	}
	return pgtype.FlatArray[string](flags)
}

// qualityActionsArgStr returns string for $N::text::jsonb binding (pgx binary protocol fix).
//
// 2026-08-20: 之前还有一个返回 `any` 的 qualityActionsArg helper，2026-07-05
// 切到 $N::text::jsonb 路径时已无 caller，故删除。bind 时一律走
// $N::text::jsonb（字符串形式）而不是 $N::jsonb（pgx 二进制形式），避
// 免 pgx 把 Go []byte 当成 bytea 而不是 jsonb。
func qualityActionsArgStr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// attachmentsArg returns a value safe to bind to the NULLABLE
// the request-log row's attachments JSONB column. Unlike quality_flags /
// quality_fix_actions this column has no NOT NULL constraint, so an
// empty payload binds SQL NULL (NULL attachments == "无附件"), which
// is the intended semantic. The CAST($73 AS jsonb) in the INSERT
// accepts both NULL and a real JSON array.
func attachmentsArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// attachmentsArgStr returns string for $N::text::jsonb binding (pgx binary protocol fix).
// Returns "null" for empty input so PostgreSQL interprets it as JSON NULL.
func attachmentsArgStr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

// jsonOrNull returns string representation of json.RawMessage for $N::text::jsonb binding.
// Returns "null" for empty input so PostgreSQL interprets it as JSON NULL.
func jsonOrNull(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

// nullableJSONArg preserves SQL NULL for an absent JSONB value. JSONB `null`
// is a real value and would otherwise overwrite a prior discard-events array
// during an upsert or terminal stream update.
func nullableJSONArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// strPtrToJSON converts *string to a JSON literal for $N::text::jsonb binding.
// sanitizeRequestLogEntry normalizes JSONB string fields before persistence;
// this final guard prevents a malformed late mutation from aborting the whole
// request-log transaction.
//
// Empty string ("") is stored as "{}" rather than "null" so the body remains
// queryable as JSON and consumers don't receive a SQL NULL that requires a
// separate NULL-check. A nil pointer (never set) returns "null" since that
// carries the semantic "no data was provided".
//
// 2026-10-01 F5：本函数是**落库绑定**换算，也是镜像换算的同一语义；换算规则
// 已下沉到 storage/file.ConvertBodyPayload 作单一事实源（admin ingest 作为
// 第三落库点接镜像后需要共用同一份规则，否则某一侧改动会让对账静默失配）。
// 本函数保留为包内薄封装——它的调用点还包含 AutoDecision 等非 body 字段的
// $N::jsonb 绑定，那些字段不参与镜像。
func strPtrToJSON(s *string) string {
	return filestore.ConvertBodyPayload(s)
}

// streamChunksSentArg returns a value safe to bind to the NOT NULL
// the request-log row's stream_chunks_sent integer column. The column has a
// DEFAULT 0 but, like every other NOT NULL column in the explicit
// INSERT, an explicit bind OVERRIDES the default — and binding a
// nil *int from Go produces SQL NULL, tripping SQLSTATE 23502. We
// therefore coerce nil → 0 so that callers that don't populate this
// field (e.g. the /api/telemetry/request-log HTTP path, or any code
// path that predates migration 320) still get a successful INSERT.
// 2026-07-05 P0 fix: required by the hot-table migration.
func streamChunksSentArg(v *int) any {
	if v == nil {
		return 0
	}
	return *v
}

func coalesceRawModels(models []string) []string {
	if models == nil {
		return []string{}
	}
	return models
}

func coalesceTrace(trace json.RawMessage) []byte {
	if len(trace) == 0 {
		return []byte("{}")
	}
	return trace
}

func total(prompt, completion *int) *int {
	if prompt == nil && completion == nil {
		return nil
	}
	value := 0
	if prompt != nil {
		value += *prompt
	}
	if completion != nil {
		value += *completion
	}
	return &value
}

func firstString(values ...*string) *string {
	for _, value := range values {
		if value != nil && *value != "" {
			return value
		}
	}
	return nil
}

// stringValue extracts string from *string with empty fallback
func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// intValue extracts int from *int with 0 fallback
func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// SearchText 是 searchText 的导出包装（§9.98）。
//
// 为什么需要它：v1 把这列写在宽表的 search_text，而
// session_turns.search_text **列已建、字段已在 ProcessedRequest、INSERT 也已绑定**，
// 唯独**没有源**——internal/sessionv2mirror 拿不到未导出的 searchText，
// 于是 s1a_fields.go 那行注释「缺源，保持零值」就一直成立，
// 252 实测该列 **0% 填充**（v1 侧 100%）。
//
// 它是**纯函数**（只读 entry 的 9 个指针字段，无副作用、无 I/O），
// 所以镜像侧调用它得到的结果与 v1 逐字节相同——这正是「数据一致」需要的性质。
func SearchText(entry *RequestLogEntry) *string { return searchText(entry) }

func searchText(entry *RequestLogEntry) *string {
	parts := make([]string, 0, 6)
	for _, value := range []*string{
		entry.ClientModel, entry.OutboundModel, entry.ClientProfile, entry.RequestMode,
		entry.GwSessionID, entry.GwTaskID, entry.APIKeyPrefix, entry.APIKeyOwnerUser, entry.ApplicationCode,
	} {
		if value != nil && *value != "" {
			parts = append(parts, *value)
		}
	}
	if len(parts) == 0 {
		empty := ""
		return &empty
	}
	joined := strings.Join(parts, " ")
	return &joined
}

func normalizeRequestStatus(entry *RequestLogEntry) {
	if entry == nil {
		return
	}
	if entry.RequestStatus != nil && *entry.RequestStatus != "" {
		return
	}
	status := ResolveRequestStatus(entry.Success, entry.ErrorKind, entry.Op == RequestLogInsert)
	entry.RequestStatus = &status
}

// ResolveRequestStatus derives the three-state lifecycle label used by request_logs.
func ResolveRequestStatus(success bool, errorKind *string, isInitialInsert bool) string {
	if success {
		return RequestStatusSuccess
	}
	if errorKind != nil && strings.TrimSpace(*errorKind) != "" {
		return RequestStatusFailure
	}
	if isInitialInsert {
		return RequestStatusInProgress
	}
	return RequestStatusInProgress
}

// sanitizeUTF8 returns a copy of s with every invalid UTF-8 byte sequence
// replaced by the Unicode replacement character (U+FFFD).  The result is
// always valid UTF-8 and safe for PostgreSQL columns with encoding=UTF8.
//
// Additionally, backslashes are escaped to prevent "unsupported Unicode
// escape sequence" (SQLSTATE 22P05) errors when a string contains sequences
// that PostgreSQL interprets as Unicode escapes (e.g. \uXXXX, \UXXXXXXXX,
// or lone \ followed by non-hex chars).  See incident 2026-06-11.
//
// Use sanitizeUTF8JSON instead for strings stored in JSONB columns.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return strings.ReplaceAll(s, "\\", "\\\\")
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/10)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\uFFFD")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return strings.ReplaceAll(b.String(), "\\", "\\\\")
}

// scrubUTF8ForJSON replaces invalid UTF-8 byte sequences with U+FFFD.
// Unlike sanitizeUTF8, backslashes are preserved so JSON escape sequences stay intact.
func scrubUTF8ForJSON(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/10)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\uFFFD")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// truncateToValidJSON walks backward from the end of s, trying each closing
// brace/bracket as a candidate boundary until json.Valid succeeds.
func truncateToValidJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if json.Valid([]byte(s)) {
		return s, true
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != '}' && s[i] != ']' {
			continue
		}
		candidate := strings.TrimSpace(s[:i+1])
		if candidate != "" && json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// escapeInvalidEscape catches sequences that look like JSON \uXXXX escapes
// but the following 4 chars are not valid hex. PostgreSQL's jsonb cast
// rejects these with SQLSTATE 22P05 ("unsupported Unicode escape sequence").
// We double the backslash so the cast sees "\\\\uXXXX" which is harmless text.
// This scanner is JSON-escape aware: a backslash always consumes the character
// that follows it. Without that, the second backslash of a legitimately escaped
// backslash pair (`\\` — how JSON encodes one literal backslash) is re-read as
// the start of an escape. Content such as a Windows path ("C:\users" → JSON
// "C:\\users") or a LaTeX macro ("\usepackage") would then be rewritten into an
// odd number of backslashes, turning VALID JSON into invalid JSON and causing
// the entire body to be discarded downstream.
func escapeInvalidEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != 0x5C {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			// Trailing lone backslash cannot form a valid escape.
			b.WriteString(`\\`)
			i++
			continue
		}
		if s[i+1] != 'u' {
			// Copy any other escape (including `\\`) as a unit so the escaped
			// character is never re-scanned as an escape introducer.
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		hexOK := i+5 < len(s)
		for j := 0; hexOK && j < 4; j++ {
			k := s[i+2+j]
			if !((k >= '0' && k <= '9') || (k >= 'a' && k <= 'f') || (k >= 'A' && k <= 'F')) {
				hexOK = false
			}
		}
		if !hexOK {
			b.WriteString(`\\u`) // double the backslash
			i += 2               // skip the \u
			continue
		}
		b.WriteString(s[i : i+6])
		i += 6
	}
	return b.String()
}

// neutralizeNullUnicodeEscape rewrites \u0000 escapes. encoding/json accepts
// them as valid JSON but PostgreSQL's jsonb cast rejects them with SQLSTATE
// 22P05 ("\u0000 cannot be converted to text"). It is the only escape that is
// simultaneously valid JSON and invalid jsonb, so a json.Valid check cannot
// catch it — it must be rewritten explicitly.
func neutralizeNullUnicodeEscape(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != 0x5C {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		if s[i+1] == 'u' && i+6 <= len(s) && strings.EqualFold(s[i+2:i+6], "0000") {
			b.WriteString(`\ufffd`)
			i += 6
			continue
		}
		// Preserve escape pairs as units so `\\u0000` (literal backslash then
		// the text u0000) is not mistaken for a real escape.
		b.WriteByte(s[i])
		b.WriteByte(s[i+1])
		i += 2
	}
	return b.String()
}

// sanitizeUTF8JSON scrubs invalid UTF-8 and ensures the result is valid JSON
// before CAST(... AS jsonb). On unrecoverable corruption it returns "" so callers
// can store NULL and let UPDATE COALESCE keep the previous body.
//
// Repair heuristics are applied ONLY to input that is already invalid JSON.
// Running them over valid JSON is what silently destroyed request bodies:
// escapeInvalidEscape rewrote legitimately escaped backslashes, and
// truncateToValidJSON then had no valid prefix to fall back to, so a complete
// body became "" → SQL NULL. Valid JSON must always survive untouched apart
// from the \u0000 rewrite that PostgreSQL requires.
func sanitizeUTF8JSON(s string) string {
	cleaned := scrubUTF8ForJSON(s)

	// \u0000 is valid JSON but invalid jsonb, so it is rewritten in all cases.
	cleaned = neutralizeNullUnicodeEscape(cleaned)

	// Fast path: already valid JSON needs no repair.
	if json.Valid([]byte(cleaned)) {
		return cleaned
	}

	// Invalid JSON — now the repair heuristics are appropriate.
	repairedEscapes := escapeInvalidEscape(cleaned)
	if json.Valid([]byte(repairedEscapes)) {
		return repairedEscapes
	}
	if repaired, ok := truncateToValidJSON(repairedEscapes); ok {
		return repaired
	}
	if repaired, ok := truncateToValidJSON(cleaned); ok {
		return repaired
	}
	return ""
}

func sanitizeStringPtr(p **string) {
	if *p == nil {
		return
	}
	clean := sanitizeUTF8(**p)
	*p = &clean
}

func sanitizeRequestLogEntry(e *RequestLogEntry) {
	sanitizeStringPtr(&e.ClientModel)
	sanitizeStringPtr(&e.OutboundModel)
	sanitizeStringPtr(&e.ClientProfile)
	sanitizeStringPtr(&e.RequestMode)
	sanitizeStringPtr(&e.ErrorKind)
	sanitizeStringPtr(&e.UsageSource)
	sanitizeStringPtr(&e.IdentityHash)
	sanitizeStringPtr(&e.ResponseChecksum)
	sanitizeStringPtr(&e.TransformRuleID)
	sanitizeStringPtr(&e.EgressProtocol)
	sanitizeStringPtr(&e.ClientProtocol)
	sanitizeStringPtr(&e.UpstreamProtocol)
	sanitizeStringPtr(&e.FailureDetailCode)
	sanitizeStringPtr(&e.FailureStage)
	sanitizeStringPtr(&e.RequestPreview)
	sanitizeStringPtr(&e.TransformSummary)
	sanitizeStringPtr(&e.ResponsePreview)
	sanitizeStringPtr(&e.GwSessionID)
	sanitizeStringPtr(&e.GwTaskID)
	sanitizeStringPtr(&e.ProjectID)
	sanitizeStringPtr(&e.Namespace)
	sanitizeStringPtr(&e.APIKeyPrefix)
	sanitizeStringPtr(&e.APIKeyOwnerUser)
	sanitizeStringPtr(&e.ApplicationCode)
	sanitizeStringPtr(&e.TaskType)
	sanitizeStringPtr(&e.AutoProfile)
	sanitizeJSONField("auto_decision", &e.AutoDecision)
	sanitizeStringPtr(&e.WorkType)
	sanitizeStringPtr(&e.TaskTypeChosen)
	sanitizeStringPtr(&e.ModelChosen)
	sanitizeStringPtr(&e.StrategyUsed)
	sanitizeStringPtr(&e.ParentRequestID)
	sanitizeStringPtr(&e.CompressionReason)
	sanitizeStringPtr(&e.CompressionStrategy)
	e.RequestID = sanitizeUTF8(e.RequestID)
	e.TenantID = sanitizeUTF8(e.TenantID)
	if e.EndUserID != nil {
		clean := sanitizeUTF8(*e.EndUserID)
		e.EndUserID = &clean
	}
	if e.CostCurrency != nil {
		clean := sanitizeUTF8(*e.CostCurrency)
		e.CostCurrency = &clean
	}
	sanitizeJSONField("request_body", &e.RequestBody)
	sanitizeJSONField("response_body", &e.ResponseBody)
	sanitizeRawJSONField("compression_meta", &e.CompressionMeta)
	sanitizeRawJSONField("outbound_body", &e.OutboundBody)
	sanitizeRawJSONField("discard_events", &e.DiscardEvents)
	sanitizeRawJSONField("outbound_msg_hashes", &e.OutboundMsgHashes)
	sanitizeRawJSONField("quality_fix_actions", &e.QualityFixActions)
	sanitizeRawJSONField("tool_calls", &e.ToolCalls)
	sanitizeRawJSONField("attachments", &e.Attachments)
	sanitizeRawJSONField("routing_attempts", &e.RoutingAttempts)
}

// sanitizeJSONField normalizes a JSONB-bound string field in place, setting it
// to nil when the content cannot be made castable to jsonb.
//
// Dropping is logged: a silently discarded body is indistinguishable from a
// request that never had one, which is exactly how the escapeInvalidEscape
// regression stayed invisible in production. Only sizes are logged, never
// content — these fields hold user prompts.
func sanitizeJSONField(field string, p **string) {
	if *p == nil {
		return
	}
	original := **p
	v := sanitizeUTF8JSON(original)
	if v == "" {
		slog.Warn("telemetry JSON field discarded, storing NULL",
			"field", field,
			"bytes", len(original))
		incSanitizeEvent("discarded", field, "json_field", "sanitize")
		*p = nil
		return
	}
	// 2026-08-23 (Step 6 / 245 incident): keep the truncated prefix when
	// sanitizeUTF8JSON managed to rescue a valid JSON prefix. The previous
	// behaviour still set *p to the truncated value here, but the side
	// effect was a "repaired" label that conflated two semantics. The
	// outcome label is now `rescued` — auditors see partial JSON instead of
	// NULL, and dashboards can rate() rescued vs discarded independently.
	// PII note (audit P1-4): the retained prefix may still contain user
	// PII (prompt fragments, tool-call arguments, attachment metadata).
	// These columns are by-design PII-bearing so this is not a new
	// exposure, but downstream UI consumers must apply their own redaction
	// before rendering. Compliance should review the rescued path before
	// it is exposed to end-users outside the audit context.
	if len(v) < len(original) {
		slog.Warn("telemetry JSON field rescued by truncation",
			"field", field,
			"original_bytes", len(original),
			"kept_bytes", len(v))
		incSanitizeEvent("rescued", field, "json_field", "sanitize")
	}
	*p = &v
}

// sanitizeRawJSONField is the json.RawMessage counterpart of sanitizeJSONField.
// Both must report the same events: a silently shortened body is worse than a
// discarded one, because it still looks complete to whoever reads it back.
// outbound_body in particular holds the prompt actually sent upstream.
func sanitizeRawJSONField(field string, raw *json.RawMessage) {
	if len(*raw) == 0 {
		return
	}

	cleaned := sanitizeUTF8JSON(string(*raw))
	if cleaned == "" {
		slog.Warn("telemetry JSONB field discarded",
			"field", field,
			"bytes", len(*raw))
		incSanitizeEvent("discarded", field, "raw_json_field", "sanitize")
		*raw = nil
		return
	}
	// 2026-08-23 (Step 6): see sanitizeJSONField — keep truncated prefix,
	// log + count as `rescued` instead of the older `repaired` label.
	if len(cleaned) < len(*raw) {
		slog.Warn("telemetry JSONB field rescued by truncation",
			"field", field,
			"original_bytes", len(*raw),
			"kept_bytes", len(cleaned))
		incSanitizeEvent("rescued", field, "raw_json_field", "sanitize")
	}
	*raw = json.RawMessage(cleaned)
}

// mergeRequestLogBatch coalesces multiple updates for the same request_id so a
// burst of stream telemetry writes one DB round-trip instead of many.
func mergeRequestLogBatch(batch []any) []any {
	if len(batch) < 2 {
		return batch
	}
	merged := make([]any, 0, len(batch))
	pending := make(map[string]*RequestLogEntry)
	for _, item := range batch {
		entry, ok := item.(*RequestLogEntry)
		if !ok || entry.Op != RequestLogUpdate {
			merged = append(merged, item)
			continue
		}
		if existing, found := pending[entry.RequestID]; found {
			mergeRequestLogEntry(existing, entry)
			continue
		}
		cp := *entry
		pending[entry.RequestID] = &cp
		merged = append(merged, &cp)
	}
	return merged
}

func mergeRequestLogEntry(dst, src *RequestLogEntry) {
	if src == nil || dst == nil {
		return
	}
	dst.Op = RequestLogUpdate
	mergeStringPtr(&dst.ClientModel, src.ClientModel)
	mergeStringPtr(&dst.OutboundModel, src.OutboundModel)
	mergeStringPtr(&dst.CanonicalModel, src.CanonicalModel)
	mergeIntPtr(&dst.CredentialID, src.CredentialID)
	mergeIntPtr(&dst.ProviderID, src.ProviderID)
	mergeIntPtr(&dst.CanonicalID, src.CanonicalID)
	mergeStringPtr(&dst.ClientProfile, src.ClientProfile)
	mergeStringPtr(&dst.RequestMode, src.RequestMode)
	mergeBoolPtr(&dst.AffinityHit, src.AffinityHit)
	mergeStringPtr(&dst.EndUserID, src.EndUserID)
	mergeIntPtr(&dst.PromptTokens, src.PromptTokens)
	mergeIntPtr(&dst.CompletionTokens, src.CompletionTokens)
	mergeIntPtr(&dst.CacheReadTokens, src.CacheReadTokens)
	mergeIntPtr(&dst.CacheWriteTokens, src.CacheWriteTokens)
	mergeIntPtr(&dst.ReasoningTokens, src.ReasoningTokens)
	mergeIntPtr(&dst.ImageTokens, src.ImageTokens)
	mergeIntPtr(&dst.AudioTokens, src.AudioTokens)
	mergeIntPtr(&dst.VideoTokens, src.VideoTokens)
	mergeIntPtr(&dst.ProviderTokens, src.ProviderTokens)
	mergeFloatPtr(&dst.CostUSD, src.CostUSD)
	mergeFloatPtr(&dst.CostDisplay, src.CostDisplay)
	mergeStringPtr(&dst.CostCurrency, src.CostCurrency)
	mergeIntPtr(&dst.LatencyMs, src.LatencyMs)
	mergeBoolPtr(&dst.IsAutoRequest, src.IsAutoRequest)
	mergeStringPtr(&dst.TaskType, src.TaskType)
	mergeStringPtr(&dst.AutoProfile, src.AutoProfile)
	mergeStringPtr(&dst.AutoDecision, src.AutoDecision)
	mergeFloatPtr(&dst.AutoConfidence, src.AutoConfidence)
	mergeStringPtr(&dst.WorkType, src.WorkType)
	mergeStringPtr(&dst.TaskTypeChosen, src.TaskTypeChosen)
	mergeFloatPtr(&dst.ConfidenceNum, src.ConfidenceNum)
	mergeStringPtr(&dst.ModelChosen, src.ModelChosen)
	mergeStringPtr(&dst.StrategyUsed, src.StrategyUsed)
	mergeInt64Ptr(&dst.CreditsCharged, src.CreditsCharged)
	mergeStringPtr(&dst.ParentRequestID, src.ParentRequestID)
	mergeStringPtr(&dst.CompressionReason, src.CompressionReason)
	mergeStringPtr(&dst.CompressionStrategy, src.CompressionStrategy)
	mergeRawJSON(&dst.CompressionMeta, src.CompressionMeta)
	mergeRawJSON(&dst.OutboundBody, src.OutboundBody)
	mergeIntPtr(&dst.OutboundMsgCount, src.OutboundMsgCount)
	mergeIntPtr(&dst.OutboundTokenEst, src.OutboundTokenEst)
	mergeRawJSON(&dst.OutboundMsgHashes, src.OutboundMsgHashes)
	mergeStringPtr(&dst.SubmitModeHeader, src.SubmitModeHeader)
	if len(src.QualityFlags) > 0 {
		dst.QualityFlags = append([]string(nil), src.QualityFlags...)
	}
	mergeRawJSON(&dst.QualityFixActions, src.QualityFixActions)
	mergeFloatPtr(&dst.QualityScore, src.QualityScore)
	mergeStringPtr(&dst.UpstreamFinishReason, src.UpstreamFinishReason)
	mergeRawJSON(&dst.ToolCalls, src.ToolCalls)
	mergeIntPtr(&dst.UpstreamStatusCode, src.UpstreamStatusCode)
	mergeBoolPtr(&dst.ClientTimeout, src.ClientTimeout)
	mergeStringPtr(&dst.ClientEndpoint, src.ClientEndpoint)
	mergeIntPtr(&dst.StreamChunkErrors, src.StreamChunkErrors)
	mergeRawJSON(&dst.Attachments, src.Attachments)
	mergeStringPtr(&dst.ClientIP, src.ClientIP)
	mergeStringPtr(&dst.ClientForwardedFor, src.ClientForwardedFor)
	mergeStringPtr(&dst.OriginStage, src.OriginStage)
	mergeStringPtr(&dst.OriginActor, src.OriginActor)
	mergeRawJSON(&dst.RoutingAttempts, src.RoutingAttempts)
	mergeStringPtr(&dst.RoutingSummary, src.RoutingSummary)
	mergeRawJSON(&dst.DiscardEvents, src.DiscardEvents)
	mergeStringPtr(&dst.AgentName, src.AgentName)
	mergeStringPtr(&dst.AgentType, src.AgentType)
	mergeStringPtr(&dst.ClientProtocol, src.ClientProtocol)
	mergeStringPtr(&dst.UpstreamProtocol, src.UpstreamProtocol)
	mergeBoolPtr(&dst.ProtocolConversion, src.ProtocolConversion)
	mergeStringPtr(&dst.VirtualClientID, src.VirtualClientID)
	mergeIntPtr(&dst.RequestBytes, src.RequestBytes)
	mergeIntPtr(&dst.ResponseBytes, src.ResponseBytes)
	mergeIntPtr(&dst.StreamFirstChunkMs, src.StreamFirstChunkMs)
	mergeIntPtr(&dst.StreamChunkCount, src.StreamChunkCount)
	mergeIntPtr(&dst.StreamChunksSent, src.StreamChunksSent)
	mergeBoolPtr(&dst.StreamDoneReceived, src.StreamDoneReceived)
	mergeBoolPtr(&dst.StreamInterrupted, src.StreamInterrupted)
	mergeStringPtr(&dst.ResponseChecksum, src.ResponseChecksum)
	mergeStringPtr(&dst.ResponsePreview, src.ResponsePreview)
	mergeStringPtr(&dst.ResponseBody, src.ResponseBody)
	mergeStringPtr(&dst.FailureDetailCode, src.FailureDetailCode)
	mergeStringPtr(&dst.FailureStage, src.FailureStage)
	mergeStringPtr(&dst.TransformRuleID, src.TransformRuleID)
	mergeStringPtr(&dst.EgressProtocol, src.EgressProtocol)
	mergeStringPtr(&dst.RequestPreview, src.RequestPreview)
	mergeStringPtr(&dst.TransformSummary, src.TransformSummary)
	mergeStringPtr(&dst.RequestBody, src.RequestBody)
	mergeStringPtr(&dst.UsageSource, src.UsageSource)
	mergeStringPtr(&dst.ErrorKind, src.ErrorKind)
	mergeStringPtr(&dst.RequestStatus, src.RequestStatus)
	mergeStringPtr(&dst.IdentityHash, src.IdentityHash)
	mergeStringPtr(&dst.GwSessionID, src.GwSessionID)
	mergeStringPtr(&dst.GwTaskID, src.GwTaskID)
	mergeStringPtr(&dst.ProjectID, src.ProjectID)
	mergeStringPtr(&dst.Namespace, src.Namespace)
	mergeStringPtr(&dst.APIKeyPrefix, src.APIKeyPrefix)
	mergeStringPtr(&dst.APIKeyOwnerUser, src.APIKeyOwnerUser)
	mergeStringPtr(&dst.ApplicationCode, src.ApplicationCode)
	mergeTimePtr(&dst.EventAt, src.EventAt)
	mergeStringPtr(&dst.ClientRequestID, src.ClientRequestID)
	// 2026-07-27: preserve client perception metadata across batched updates.
	mergeStringPtr(&dst.AgentName, src.AgentName)
	mergeStringPtr(&dst.AgentType, src.AgentType)
	mergeStringPtr(&dst.ClientProtocol, src.ClientProtocol)
	mergeStringPtr(&dst.VirtualClientID, src.VirtualClientID)
	// V3.1 queue timestamps (migration 491)
	mergeTimePtr(&dst.T0ArrivedAt, src.T0ArrivedAt)
	mergeTimePtr(&dst.T1TotalEnqueuedAt, src.T1TotalEnqueuedAt)
	mergeTimePtr(&dst.T2TotalDequeuedAt, src.T2TotalDequeuedAt)
	mergeTimePtr(&dst.T3ModelEnqueuedAt, src.T3ModelEnqueuedAt)
	mergeTimePtr(&dst.T4ModelDequeuedAt, src.T4ModelDequeuedAt)
	mergeTimePtr(&dst.T5CredEnqueuedAt, src.T5CredEnqueuedAt)
	mergeTimePtr(&dst.T6CredDequeuedAt, src.T6CredDequeuedAt)
	mergeTimePtr(&dst.T7ForwardStartAt, src.T7ForwardStartAt)
	mergeTimePtr(&dst.T8ResponseStartAt, src.T8ResponseStartAt)
	mergeTimePtr(&dst.T9ResponseEndAt, src.T9ResponseEndAt)
	if src.Success {
		dst.Success = true
	}
	if src.RequestStatus != nil && *src.RequestStatus != "" {
		v := *src.RequestStatus
		dst.RequestStatus = &v
	} else if src.Success {
		v := RequestStatusSuccess
		dst.RequestStatus = &v
	} else if src.ErrorKind != nil && *src.ErrorKind != "" {
		v := RequestStatusFailure
		dst.RequestStatus = &v
	}
	if dst.Success {
		dst.ErrorKind = nil
	}
}

func mergeRawJSON(dst *json.RawMessage, src json.RawMessage) {
	if len(src) == 0 {
		return
	}
	trimmed := bytes.TrimSpace(src)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("[]")) {
		return
	}
	*dst = append((*dst)[:0], src...)
}

func mergeStringPtr(dst **string, src *string) {
	if src != nil && *src != "" {
		v := *src
		*dst = &v
	}
}

func mergeIntPtr(dst **int, src *int) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeInt64Ptr(dst **int64, src *int64) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeFloatPtr(dst **float64, src *float64) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeBoolPtr(dst **bool, src *bool) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func mergeTimePtr(dst **time.Time, src *time.Time) {
	if src != nil && !src.IsZero() {
		v := *src
		*dst = &v
	}
}

// ApplyOriginFromContext reads the origin metadata (origin_stage,
// origin_actor, client_ip, client_forwarded_for) that OriginMiddleware
// stored on the request context and copies it onto the entry.  This is
// the bridge between middleware/origin_mw.go and the telemetry insert
// path; it is intentionally defensive — nil-safe and idempotent so the
// caller can run it before every EmitRequestLogInsert/Update.
//
// Precedence rule (mirrors the first-write-wins COALESCE on the DB
// side): the entry's existing non-nil values are preserved, ctx only
// fills in missing ones.  That way a caller that set
// origin_stage="node_probe" programmatically (e.g. ActiveProbeEmitter)
// is not overwritten by a stale "business" value in ctx.
//
// Wire format (string keys — Go stdlib convention for cross-package
// context values):
//
//	"origin.stage"        string — self_check | node_probe | system_health | business
//	"origin.actor"        string — worker / actor name (e.g. node-probe-worker)
//	"origin.client_ip"    string — real client IP (single value, X-Real-IP > XFF[0] > RemoteAddr)
//	"origin.xff"          string — full X-Forwarded-For chain (≤ 1024B)
func (e *RequestLogEntry) ApplyOriginFromContext(ctx context.Context) {
	if e == nil || ctx == nil {
		return
	}
	if v, ok := ctx.Value("origin.stage").(string); ok && v != "" {
		if e.OriginStage == nil {
			s := v
			e.OriginStage = &s
		}
	}
	if v, ok := ctx.Value("origin.actor").(string); ok && v != "" {
		if e.OriginActor == nil {
			s := v
			e.OriginActor = &s
		}
	}
	if v, ok := ctx.Value("origin.client_ip").(string); ok && v != "" {
		if e.ClientIP == nil {
			s := v
			e.ClientIP = &s
		}
	}
	if v, ok := ctx.Value("origin.xff").(string); ok && v != "" {
		if e.ClientForwardedFor == nil {
			s := v
			e.ClientForwardedFor = &s
		}
	}
}

// originCtxKey remains defined for test compatibility; ApplyOriginFromContext
// now reads with plain string keys so values stored by middleware/origin_mw.go
// (which uses string-typed context keys) are reachable.
type originCtxKey string

// lookupProviderName queries the providers table to resolve provider_id to code.
// Returns "unknown" if providerID is nil or query fails (non-fatal fallback).
// Uses the same transaction tx to avoid additional connections.
func lookupProviderName(ctx context.Context, tx pgx.Tx, providerID *int) string {
	if providerID == nil {
		return "unknown"
	}

	var code string
	err := tx.QueryRow(ctx, `SELECT code FROM providers WHERE id = $1`, *providerID).Scan(&code)
	if err != nil {
		// Non-fatal: log and fallback to "unknown"
		slog.Warn("outbox: provider lookup failed", "provider_id", *providerID, "error", err)
		return "unknown"
	}

	return code
}

// lookupTurnNoSessionSQL 是 outbox turn_no 的**会话族权威读**（hot ∪ parent
// 两腿 MAX(turn_no)）。提升为包级变量是给形状钉桩用的。
var lookupTurnNoSessionSQL = `
		SELECT COALESCE(MAX(turn_no), 0)
		FROM (
			SELECT turn_no FROM public.session_turns_hot WHERE session_id = $1
			UNION ALL
			SELECT turn_no FROM public.session_turns WHERE session_id = $1
		) session_turn_nos
	`

// lookupTurnNumber queries the session family to count the turn number for
// this session. Returns the next turn number (1-based). If sessionID is empty
// or query fails, returns 1. Uses the same transaction tx to maintain
// consistency.
func lookupTurnNumber(ctx context.Context, tx pgx.Tx, sessionID string) int {
	if sessionID == "" {
		return 1
	}

	// Count existing requests in this session (including the current insert from the same tx)
	// Since we're in the middle of the INSERT transaction, we need to count INCLUDING
	// the row we just inserted. The turn_no should be: COUNT(*) for this session.
	//
	// 2026-10-20（迁移 890，v1 族退役）：原实现先读会话族两腿 MAX(turn_no)
	// （session_id 等值，idx_session_turns_session），0 时回退 v1 母表计数
	// （镜像前历史会话）——该回退腿随 890 落库后必然 42P01，已整段删除。
	// ⇒ 现在两腿恒 0 时 turn_no 取 1（会话首轮）。
	// 会话轮写与 outbox 发射并发时本读可能少计在途一轮——该值本就是提示性载荷，
	// 权威 turn_no 归 session/v2 aggregator（见上）。
	var count int
	err := tx.QueryRow(ctx, lookupTurnNoSessionSQL, sessionID).Scan(&count)

	// ★ 2026-10-20（迁移 890，v1 族退役）：v1 回退腿整段删除——v1 族随
	// 890 落库后再读它必然 42P01。两腿 MAX(turn_no) 恒 0 时 turn_no 取 1（首轮）。

	if err != nil {
		// Non-fatal: log and fallback to 1
		slog.Warn("outbox: turn number lookup failed", "session_id", sessionID, "error", err)
		return 1
	}

	// If count is 0, this is the first turn (shouldn't happen since we just inserted)
	// If count is 1, this is turn 1
	// If count is 2, this is turn 2, etc.
	if count == 0 {
		return 1
	}

	return count
}

// inferRequestType infers the request type from the request body.
// Returns "" if the request type cannot be inferred.

// inferRequestType derives the V3.2 request_type from the entry's existing
// fields. Returns "main" for a plain client request (the column DEFAULT).
// Precedence: explicit RequestType > compression > origin_actor > parent link.

// requestClassArg resolves the request_class bind value. The database invariant
// requires immediate rows to have no due_at and scheduled rows to have one;
// normalizeRequestClassAndDueAt enforces that invariant before every write.
func requestClassArg(c *string) string {
	if c != nil && *c == "scheduled" {
		return "scheduled"
	}
	return "immediate"
}

// normalizeRequestClassAndDueAt enforces the persisted domain invariant. Insert
// callers always write a class; update callers preserve existing values unless
// they explicitly provide RequestClass.
func normalizeRequestClassAndDueAt(entry *RequestLogEntry, preserveWhenUnset bool) {
	if entry == nil || (preserveWhenUnset && entry.RequestClass == nil) {
		return
	}
	if entry.RequestClass != nil && *entry.RequestClass == "scheduled" && entry.DueAt != nil && !entry.DueAt.IsZero() {
		return
	}
	immediate := "immediate"
	entry.RequestClass = &immediate
	entry.DueAt = nil
}
