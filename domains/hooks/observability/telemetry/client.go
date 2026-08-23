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
)

var errNoTelemetryDB = errors.New("telemetry database not configured")

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

type Client struct {
	dbPool       *pgxpool.Pool
	requestLogDB requestLogDB

	queue chan any
	done  chan struct{}
	wg    sync.WaitGroup

	lifecycleMu sync.RWMutex
	stopped     atomic.Bool

	// onPersisted hooks run after every successful INSERT/UPDATE of a
	// request_logs row. Consumers use AddOnRequestLogPersisted to register.
	onPersisted []func(entry *RequestLogEntry)

	// onEmitted is invoked immediately when EmitRequestLog is called,
	// BEFORE the entry is queued or persisted to the database. This
	// provides faster real-time updates from the in-memory pipeline
	// rather than waiting for DB write completion. The callback runs
	// on the caller's goroutine and MUST be non-blocking (use select
	// with default for channel sends). May be nil.
	onEmitted func(entry *RequestLogEntry)
	fallback  dbdegradation.BackupWriter
	degraded  atomic.Bool

	// outboxWriter (optional): writes request.completed events to outbox_events
	// in the same transaction as request_logs INSERT for Gateway → ASM delivery.
	// Phase 3 WP4: Business write point integration.
	outboxWriter OutboxWriter

	// 2026-07-16: failure counters so ops can detect "telemetry rows
	// silently dropping" via FailCounts() instead of grepping stderr.
	// Bumped in worker/EmitRequestLog paths; safe for concurrent reads.
	failTransient uint64
	failPermanent uint64
	failRetried   uint64
	failFallback  uint64
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

// Request log lifecycle status stored in request_logs.request_status.
const (
	RequestStatusInProgress  = "in_progress"
	RequestStatusSuccess     = "success"
	RequestStatusFailure     = "failure"
	RequestStatusRateLimited = "rate_limited" // gateway rejected the client (RPM/concurrent/throttle): NOT a system error
)

type RequestLogEntry struct {
	Op            RequestLogOp `json:"op,omitempty"`
	RequestID     string       `json:"request_id"`
	EventAt       *time.Time   `json:"event_at,omitempty"`
	TenantID      string       `json:"tenant_id"`
	ApplicationID *int         `json:"application_id,omitempty"`
	APIKeyID      *int         `json:"api_key_id,omitempty"`
	EndUserID     *string      `json:"end_user_id,omitempty"`
	ClientModel   *string      `json:"client_model,omitempty"`
	OutboundModel *string      `json:"outbound_model,omitempty"`
	CredentialID  *int         `json:"credential_id,omitempty"`
	ProviderID    *int         `json:"provider_id,omitempty"`
	CanonicalID   *int         `json:"canonical_id,omitempty"`
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
	// Mirror-only transport dimensions. The request_logs persistence SQL ignores
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

	// P7.2: promoted from auto_decision JSONB to dedicated columns
	// for indexable queries (see ensureRequestLogAutoDecisionColumns).
	TaskTypeChosen *string  `json:"task_type_chosen,omitempty"`
	ConfidenceNum  *float64 `json:"confidence_num,omitempty"`
	ModelChosen    *string  `json:"model_chosen,omitempty"`
	StrategyUsed   *string  `json:"strategy_used,omitempty"`
	CreditsCharged *int64   `json:"credits_charged,omitempty"`

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
	// solely on LCS inference. NOT a request_logs column — mirror-only transport
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
	// request_logs.upstream_finish_reason — the SOLE home for the
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
	// request_logs.client_request_id for debug / cross-system tracing.
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

	// 2026-07-01: 附件元数据（migration 325）
	// 从请求体中提取的 base64/data-URI 附件元数据，存储到 request_logs.attachments JSONB。
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
	T0ArrivedAt       *time.Time `json:"t0_arrived_at,omitempty"`
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
	// Persisted to request_logs.request_type (migration 510).
	RequestType *string `json:"request_type,omitempty"`

	// DiscardEvents (2026-08-19) is the JSONB array of attempt-discard
	// events the survival / stream-recovery / empty-gate paths pushed into
	// the audit StreamCapture. The audit StreamCapture also exposes it via
	// SummaryAsMap under "discard_events", and the per-request log path
	// (request_log_pipeline.go) copies it onto this field. The current
	// upsert SQL does not yet include the column — operators read the
	// The streaming survival/recovery path appends structured discard events;
	// the telemetry upsert persists them in request_logs_hot.discard_events.
	DiscardEvents json.RawMessage `json:"discard_events,omitempty"`
}

func NewClient() *Client {
	return newClientWithBufSize(4096)
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
	return c != nil && (c.dbPool != nil || c.requestLogDB != nil)
}

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
	// 2026-07-05 migration 341: 会话查找窗口（默认 5 分钟）落在 request_logs_hot
	// 的 0-7 天热数据范围内，查询 _hot 表（独立 heap 表，性能最优）即可。
	// 符合 docs/partition/partition-standards.md 查询规范。
	err := c.dbPool.QueryRow(queryCtx, `
		SELECT gw_session_id
		FROM request_logs_hot
		WHERE tenant_id = $1
		  AND api_key_id = $2
		  AND identity_hash = $3
		  AND gw_session_id LIKE 'gw\_%'
		  AND ts >= NOW() - ($4 * INTERVAL '1 second')
		ORDER BY ts DESC
		LIMIT 1
	`, tenantID, apiKeyID, identityHash, int(since.Seconds())).Scan(&sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", nil
		}
		return "", err
	}
	return sessionID, nil
}

func (c *Client) SetDB(pool *pgxpool.Pool) {
	c.dbPool = pool
	c.requestLogDB = pool
}

func (c *Client) requestLogDatabase() requestLogDB {
	if c == nil {
		return nil
	}
	if c.requestLogDB != nil {
		return c.requestLogDB
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
	if entry.Op == RequestLogUpdate {
		return c.updateRequestLog(&entry)
	}
	return c.insertRequestLog(&entry)
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
// INSERT/UPDATE of a request_logs row. Hooks run on the telemetry worker
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

func (c *Client) EmitDecisionLog(entry *DecisionLogEntry) {
	if c == nil {
		return
	}
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	if c.stopped.Load() || !c.Enabled() {
		return
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
	entry.Op = RequestLogInsert
	c.EmitRequestLog(entry)
}

func (c *Client) EmitRequestLogUpdate(entry *RequestLogEntry) {
	entry.Op = RequestLogUpdate

	// 2026-08-23 (245 incident, minimax-m3): guard against silently writing
	// a useless UPDATE. The persistRequestLog → updateRequestLog path UPSERTs
	// on request_id; if it is empty the row becomes orphan and the WAL has
	// no key to reconcile against. TenantID defaulting to "default" is the
	// same fallback insertRequestLog applies at line ~1191 — keep the
	// semantics aligned so the required-field guard matches INSERT too.
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
	if entry.TenantID == "" {
		entry.TenantID = "default"
	}

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

func (c *Client) persistRequestLog(entry *RequestLogEntry) error {
	normalizeRequestStatus(entry)
	if c.degraded.Load() {
		if c.fallback == nil {
			return errNoTelemetryDB
		}
		return c.fallback.WriteRequestLog(context.Background(), entry.RequestID+":"+string(entry.Op), entry)
	}
	var err error
	if entry.Op == RequestLogUpdate {
		err = c.updateRequestLog(entry)
	} else {
		err = c.insertRequestLog(entry)
	}
	if err == nil {
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
	return err
}

func (c *Client) insertRequestLog(entry *RequestLogEntry) error {
	db := c.requestLogDatabase()
	if db == nil {
		return errNoTelemetryDB
	}
	// Defence-in-depth: scrub any invalid UTF-8 from all string-valued fields
	// before INSERT.  PostgreSQL rejects invalid bytes with SQLSTATE 22021,
	// which (because we wrap usage_ledger + request_logs + api_keys updates
	// in a single transaction) causes the entire row to be dropped — exactly
	// the symptom reported in the 2026-06-11 incident where glm-5.1 calls
	// succeeded for the client but no request_logs row was written.  Even
	// after fixing the upstream truncation sites, this layer guarantees that
	// no future regression can silently lose rows.
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
			total_tokens, cost_usd, latency_ms, success, error_kind
		) VALUES (
			$1, now(), $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13,
			$14, $15, $16, $17, $18
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
	)
	if err != nil {
		return err
	}
	// 2026-07-05 migration 341: INSERT directly targets request_logs_hot
	// (独立热表，0-7 天数据窗口)。所有 INSERT/UPDATE/DELETE 统一写入 _hot 表，
	// 后台 partition_manager 会定期调用 promote_request_logs_hot_to_partition()
	// 将冷数据（>7 天）迁移到月度分区。热表采用 heap 存储，支持高频写入；
	// 月度分区采用 columnar 存储，优化归档查询性能。
	//
	// The ON CONFLICT (request_id) clause catches same-row upserts
	// from race conditions (e.g. async retry landing on the same
	// request_id). 热表 PRIMARY KEY (request_id) (migration 455) 覆盖冲突目标。
	// 2026-07-16: JSONB columns use $N::text::jsonb to fix pgx binary protocol 22P02.
	_, err = tx.Exec(ctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, application_id, api_key_id,
			end_user_id, client_model, outbound_model,
			-- 2026-07-27: 标准模型名(全小写),见 458 迁移。NULL = 没匹配到 canonical row。
			-- Column order intentionally matches the Go arg list below so $N
			-- placeholders stay strictly sequential (a duplicated/shifted $N
			-- here previously produced a 90-arg-vs-89-placeholder mismatch
			-- that rejected every business-row INSERT).
			canonical_model,
			credential_id, provider_id, canonical_id,
			client_profile, request_mode, affinity_hit,
			prompt_tokens, completion_tokens,
			cache_read_tokens, cache_write_tokens,
			-- audit-ir-multimodal (2026-07-13): multimodal and reasoning token fields
			reasoning_tokens, image_tokens, audio_tokens, video_tokens, provider_tokens,
			total_tokens,
			cost_usd, cost_display, cost_currency,
			latency_ms, success, request_status, error_kind, search_text,
			identity_hash, response_checksum,
			transform_rule_id, egress_protocol, failure_stage, failure_detail_code,
			request_preview, transform_summary, response_preview,
			request_body, response_body,
			stream_first_chunk_ms, stream_chunk_count, stream_done_received,
			stream_interrupted,
			usage_source,
			gw_session_id, gw_task_id,
			api_key_prefix, api_key_owner_user, application_code,
			is_auto_request, task_type, auto_profile, auto_decision, auto_confidence,
			work_type, credits_charged,
			-- Round 47 compression v7 T2: parent-child chain (4 columns).
			parent_request_id, compression_reason, compression_strategy, compression_meta,
			-- 2026-08-19: token-band observability (1 column).
			token_band,
			-- v3 (2026-06-19) T23: session-level outbound body (4 columns).
			outbound_body, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
			-- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
			quality_flags, quality_fix_actions, quality_score,
			-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
			-- (db/migrations/018_upstream_finish_reason.sql). The new column is
			-- the SOLE home for the upstream finish_reason.
			upstream_finish_reason,
			-- 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
			tool_calls,
			-- 2026-06-26: client-supplied X-Request-Id (debug only;
			-- request_logs.request_id is server-generated, see migration 454).
			client_request_id,
			-- 2026-06-30: upstream diagnostics (migration 320).
			upstream_status_code, client_timeout, client_endpoint,
			stream_chunk_errors, stream_chunks_sent,
			-- 2026-07-01: 附件元数据 (migration 325). JSONB 数组,
			-- 存储从请求体提取的 base64/data-URI 附件元数据(路径/类型/大小/hash),
			-- 附件实体文件已落盘,此处仅记录元信息。
			attachments,
			-- 2026-07-14 (migration 341): client-side origin. client_ip / client_forwarded_for
			-- were added by 2026-07-11-observability-fields.sql; origin_stage / origin_actor
			-- by migration 341. All four are populated by middleware/origin_mw.go for every
			-- business row and by the probe workers (self_check / node_probe / system_health).
			client_ip, client_forwarded_for, origin_stage, origin_actor,
			-- 2026-07-19 (migration 350): routing attempts tracking.
			routing_attempts, routing_summary,
			-- 2026-07-27: 客户端感知扩展 (主表 GROUP BY 统计需要).
			-- agent_name/agent_type 来自 telemetry.ExtractAgentName + 语义 fallback.
			-- client_protocol 来自 URL path routing.
			-- virtual_client_id 来自 identity.BuildIdentityFromRequest.
			-- 之前这些字段只在侧表 request_context_attrs 写入,主表永远 NULL.
			agent_name, agent_type, client_protocol, virtual_client_id,
	-- V3.1 (migration 491): 9-stage dispatch queue timestamps.
			t0_arrived_at, t1_total_enqueued_at, t2_total_dequeued_at,
			t3_model_enqueued_at, t4_model_dequeued_at,
			t5_cred_enqueued_at, t6_cred_dequeued_at,
			t7_forward_start_at, t8_response_start_at, t9_response_end_at,
			-- 2026-08-19: streaming discard audit events.
			discard_events
		) VALUES (
		$1, now(), $2, $3, $4,
		$5, $6, $7,
		$8, $9, $10,
		$11,
		$12, $13, $14,
		$15, $16,
		$17, $18,
		-- audit-ir-multimodal (2026-07-13): $19-$23 multimodal tokens
		$19, $20, $21, $22, $23,
		$24,
		$25, $26, $27,
		$28, $29, $30, $31, $32,
		$33, $34,
		$35, $36, $37, $38,
		$39, $40, $41,
		$42::text::jsonb, $43::text::jsonb,
		$44, $45, $46,
		$47,
		$48,
		$49, $50,
		$51, $52, $53,
		$54, $55, $56, $57::text::jsonb, $58,
		$59, $60,
		$61, $62, $63, $64::text::jsonb,
		-- 2026-08-19: token-band observability.
		$65,
		-- v3 (2026-06-19) T23: session-level outbound body.
		$66::text::jsonb, $67, $68, $69::text::jsonb,
		-- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		$70::text[], $71::text::jsonb, $72,
		-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
		$73,
		-- 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
		$74::text::jsonb,
		-- 2026-06-26: client-supplied X-Request-Id.
		$75,
		-- 2026-06-30: upstream diagnostics (migration 320).
		$76, $77, $78,
		$79, $80,
		-- 2026-07-01: 附件元数据 (migration 325).
		$81::text::jsonb,
		-- 2026-07-14 (migration 341): client-side origin.
		$82, $83, $84, $85,
		-- 2026-07-19 (migration 350): routing attempts tracking.
		$86::text::jsonb, $87,
		-- 2026-07-27: 客户端感知字段(主表 INSERT 必填).
		$88, $89, $90, $91,
			-- V3.1 queue timestamps (migration 491): 9-stage dispatch queue timestamps.
			$92, $93, $94, $95, $96, $97, $98, $99, $100, $101,
			-- 2026-08-19: streaming discard audit events.
			$102::text::jsonb
		)

				-- 2026-08-06 fix: INSERT targets request_logs_hot (NOT the partitioned parent).
				-- Migration 455 (2026-07-23) gave request_logs_hot PRIMARY KEY (request_id),
				-- so ON CONFLICT must be (request_id). Using (request_id, ts) here triggers
				-- SQLSTATE 42P10 "no unique or exclusion constraint matching the ON CONFLICT specification".
				ON CONFLICT (request_id) DO UPDATE SET
				ts = EXCLUDED.ts,
			tenant_id = EXCLUDED.tenant_id,
			application_id = EXCLUDED.application_id,
			api_key_id = EXCLUDED.api_key_id,
			end_user_id = EXCLUDED.end_user_id,
			client_model = EXCLUDED.client_model,
			outbound_model = EXCLUDED.outbound_model,
			-- 2026-07-27: 标准名同步刷新(允许 EXCLUDED 覆盖,让 canonical rename 追溯完整)
			canonical_model = EXCLUDED.canonical_model,
			credential_id = EXCLUDED.credential_id,
			provider_id = EXCLUDED.provider_id,
			canonical_id = EXCLUDED.canonical_id,
			client_profile = EXCLUDED.client_profile,
			request_mode = EXCLUDED.request_mode,
			affinity_hit = COALESCE(EXCLUDED.affinity_hit, request_logs_hot.affinity_hit),
			prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens,
			total_tokens = EXCLUDED.total_tokens,
			cost_usd = EXCLUDED.cost_usd,
			cost_display = EXCLUDED.cost_display,
			cost_currency = EXCLUDED.cost_currency,
			latency_ms = EXCLUDED.latency_ms,
			success = EXCLUDED.success,
			request_status = EXCLUDED.request_status,
			error_kind = CASE
				WHEN EXCLUDED.success = TRUE THEN NULL
				ELSE EXCLUDED.error_kind
			END,
			search_text = EXCLUDED.search_text,
			identity_hash = EXCLUDED.identity_hash,
			response_checksum = EXCLUDED.response_checksum,
			transform_rule_id = EXCLUDED.transform_rule_id,
			egress_protocol = EXCLUDED.egress_protocol,
			failure_stage = EXCLUDED.failure_stage,
			failure_detail_code = EXCLUDED.failure_detail_code,
			request_preview = EXCLUDED.request_preview,
			transform_summary = EXCLUDED.transform_summary,
			response_preview = EXCLUDED.response_preview,
			request_body = EXCLUDED.request_body,
			response_body = EXCLUDED.response_body,
			stream_first_chunk_ms = EXCLUDED.stream_first_chunk_ms,
			stream_chunk_count = EXCLUDED.stream_chunk_count,
			stream_done_received = EXCLUDED.stream_done_received,
			stream_interrupted = EXCLUDED.stream_interrupted,
			usage_source = EXCLUDED.usage_source,
			gw_session_id = EXCLUDED.gw_session_id,
			gw_task_id = EXCLUDED.gw_task_id,
			api_key_prefix = EXCLUDED.api_key_prefix,
			api_key_owner_user = EXCLUDED.api_key_owner_user,
			application_code = EXCLUDED.application_code,
			is_auto_request = EXCLUDED.is_auto_request,
			task_type = EXCLUDED.task_type,
			auto_profile = EXCLUDED.auto_profile,
			auto_decision = EXCLUDED.auto_decision,
			auto_confidence = EXCLUDED.auto_confidence,
			work_type = EXCLUDED.work_type,
			credits_charged = EXCLUDED.credits_charged,
			parent_request_id = EXCLUDED.parent_request_id,
			compression_reason = EXCLUDED.compression_reason,
			compression_strategy = EXCLUDED.compression_strategy,
			compression_meta = EXCLUDED.compression_meta,
			outbound_body = EXCLUDED.outbound_body,
			outbound_msg_count = EXCLUDED.outbound_msg_count,
			outbound_token_est = EXCLUDED.outbound_token_est,
			outbound_msg_hashes = EXCLUDED.outbound_msg_hashes,
		quality_flags = EXCLUDED.quality_flags,
		quality_fix_actions = EXCLUDED.quality_fix_actions,
		quality_score = EXCLUDED.quality_score,
		upstream_finish_reason = EXCLUDED.upstream_finish_reason,
		tool_calls = EXCLUDED.tool_calls,
		client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs_hot.client_request_id),
		-- 2026-06-30: upstream diagnostics (migration 320)
		upstream_status_code = EXCLUDED.upstream_status_code,
		client_timeout = EXCLUDED.client_timeout,
		client_endpoint = EXCLUDED.client_endpoint,
		stream_chunk_errors = EXCLUDED.stream_chunk_errors,
		-- 2026-07-05 P0 fix: stream_chunks_sent is NOT NULL (migration 320).
		-- streamChunksSentArg() coerces nil pointer to 0 so the explicit
		-- INSERT never trips SQLSTATE 23502 even when the caller does
		-- not set the field (e.g. /api/telemetry/request-log HTTP path).
		stream_chunks_sent = COALESCE(EXCLUDED.stream_chunks_sent, 0),
		-- 2026-07-01 / 2026-08-22: attachments — prefer a real JSON value from
		-- EXCLUDED. First-write-wins against SQL NULL is correct, but an early
		-- in_progress INSERT that bound JSON null ('null'::jsonb) must not
		-- permanently block the completion path's real attachment metadata.
		attachments = CASE
			WHEN EXCLUDED.attachments IS NOT NULL
				AND jsonb_typeof(EXCLUDED.attachments) <> 'null'
			THEN EXCLUDED.attachments
			ELSE request_logs_hot.attachments
		END,
		-- 2026-08-22: routing_attempts / routing_summary were INSERT-only and
		-- missing from DO UPDATE SET, so EmitRequestLogUpdate success rows
		-- never persisted tracker output (same class as t0..t9 gap).
		routing_attempts = CASE
			WHEN EXCLUDED.routing_attempts IS NOT NULL
				AND jsonb_typeof(EXCLUDED.routing_attempts) <> 'null'
			THEN EXCLUDED.routing_attempts
			ELSE request_logs_hot.routing_attempts
		END,
		routing_summary = COALESCE(EXCLUDED.routing_summary, request_logs_hot.routing_summary),
		-- 2026-07-14 (migration 341): origin metadata. First-write-wins:
		-- the first writer (usually the origin middleware) keeps its value;
		-- later replays must not overwrite the real client IP / origin label.
		client_ip           = COALESCE(request_logs_hot.client_ip, EXCLUDED.client_ip),
		client_forwarded_for = COALESCE(request_logs_hot.client_forwarded_for, EXCLUDED.client_forwarded_for),
		origin_stage        = COALESCE(request_logs_hot.origin_stage, EXCLUDED.origin_stage),
		origin_actor        = COALESCE(request_logs_hot.origin_actor, EXCLUDED.origin_actor),
		-- 2026-07-27: 客户端感知字段 — first-write-wins (避免后续 retry / 补写覆盖)
		-- origin_mw / fillAttemptMeta 阶段提取的真实值。
		agent_name          = COALESCE(request_logs_hot.agent_name, EXCLUDED.agent_name),
		agent_type          = COALESCE(request_logs_hot.agent_type, EXCLUDED.agent_type),
		client_protocol     = COALESCE(request_logs_hot.client_protocol, EXCLUDED.client_protocol),
		virtual_client_id   = COALESCE(request_logs_hot.virtual_client_id, EXCLUDED.virtual_client_id),
		-- V3.1 queue timestamps: prefer newer non-null values from EXCLUDED.
		t0_arrived_at        = COALESCE(EXCLUDED.t0_arrived_at, request_logs_hot.t0_arrived_at),
		t1_total_enqueued_at = COALESCE(EXCLUDED.t1_total_enqueued_at, request_logs_hot.t1_total_enqueued_at),
		t2_total_dequeued_at = COALESCE(EXCLUDED.t2_total_dequeued_at, request_logs_hot.t2_total_dequeued_at),
		t3_model_enqueued_at = COALESCE(EXCLUDED.t3_model_enqueued_at, request_logs_hot.t3_model_enqueued_at),
		t4_model_dequeued_at = COALESCE(EXCLUDED.t4_model_dequeued_at, request_logs_hot.t4_model_dequeued_at),
		t5_cred_enqueued_at  = COALESCE(EXCLUDED.t5_cred_enqueued_at, request_logs_hot.t5_cred_enqueued_at),
		t6_cred_dequeued_at  = COALESCE(EXCLUDED.t6_cred_dequeued_at, request_logs_hot.t6_cred_dequeued_at),
		t7_forward_start_at  = COALESCE(EXCLUDED.t7_forward_start_at, request_logs_hot.t7_forward_start_at),
		t8_response_start_at = COALESCE(EXCLUDED.t8_response_start_at, request_logs_hot.t8_response_start_at),
			t9_response_end_at   = COALESCE(EXCLUDED.t9_response_end_at, request_logs_hot.t9_response_end_at),
			discard_events       = COALESCE(EXCLUDED.discard_events, request_logs_hot.discard_events)
		-- 2026-07-27 (L-2): terminal-state guard, mirroring the WAL guard in

		-- request_logger.go Update(). Without this, the deferred client-
		-- disconnect safety net could regress a row that already reached a
		-- terminal state: the handler writes success=TRUE / request_status=
		-- 'success' via the completion path, then the disconnect probe fires
		-- EmitRequestLogUpdate with success=FALSE / 'client_disconnect' right
		-- after the client reads the good response — clobbering the success
		-- row (split-brain vs the WAL, which already had this guard).
		--
		-- Skip the UPDATE entirely when the existing row is already terminal
		-- (success=TRUE OR request_status IN ('success','failure')) AND the
		-- incoming update is NOT itself terminal-success (a legitimate later
		-- enrichment of an already-success row, e.g. token accounting from a
		-- slower path, is still allowed). The failure→success promotion case
		-- is intentionally NOT allowed here: a disconnect probe must never
		-- upgrade a failure, and a success is written by the authoritative
		-- completion path before any probe fires.
		WHERE NOT (
			request_logs_hot.request_status = 'failure'
			OR (
				(request_logs_hot.success = TRUE
				 OR request_logs_hot.request_status = 'success')
				AND NOT (
					EXCLUDED.success = TRUE
					AND EXCLUDED.request_status = 'success'
				)
			)
		)
	`,
		entry.RequestID,
		nonEmpty(entry.TenantID, "default"),
		entry.ApplicationID,
		entry.APIKeyID,
		entry.EndUserID,
		entry.ClientModel,
		entry.OutboundModel,
		entry.CanonicalModel,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		entry.ClientProfile,
		entry.RequestMode,
		entry.AffinityHit,
		entry.PromptTokens,
		entry.CompletionTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		// audit-ir-multimodal (2026-07-13): multimodal token fields
		entry.ReasoningTokens,
		entry.ImageTokens,
		entry.AudioTokens,
		entry.VideoTokens,
		entry.ProviderTokens,
		totalTokens,
		entry.CostUSD,
		entry.CostDisplay,
		entry.CostCurrency,
		entry.LatencyMs,
		entry.Success,
		entry.RequestStatus,
		entry.ErrorKind,
		searchText(entry),
		entry.IdentityHash,
		entry.ResponseChecksum,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.RequestPreview,
		entry.TransformSummary,
		entry.ResponsePreview,
		// 2026-07-22: request_body and response_body are now stored in
		// request_logs_bodies_hot side table. Main table keeps NULL to avoid bloat.
		nil, // request_body
		nil, // response_body
		entry.StreamFirstChunkMs,
		entry.StreamChunkCount,
		entry.StreamDoneReceived,
		entry.StreamInterrupted,
		nonEmptyPtr(entry.UsageSource, "llm"),
		entry.GwSessionID,
		entry.GwTaskID,
		entry.APIKeyPrefix,
		entry.APIKeyOwnerUser,
		entry.ApplicationCode,
		entry.IsAutoRequest,
		entry.TaskType,
		entry.AutoProfile,
		strPtrToJSON(entry.AutoDecision),
		entry.AutoConfidence,
		entry.WorkType,
		entry.CreditsCharged,
		// Round 47 compression v7 T2: parent-child chain payload.
		entry.ParentRequestID,
		entry.CompressionReason,
		entry.CompressionStrategy,
		jsonOrNull(entry.CompressionMeta),
		// 2026-08-19: token-band observability.
		entry.TokenBand,
		// v3 (2026-06-19) T23: session-level outbound body payload.
		jsonOrNull(entry.OutboundBody),
		entry.OutboundMsgCount,
		entry.OutboundTokenEst,
		jsonOrNull(entry.OutboundMsgHashes),
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// quality_flags is always encoded as a PostgreSQL text array;
		// quality_fix_actions is JSONB.
		//
		// 2026-08-20 nil-语义约定（与 UPSERT 路径共用）：
		//
		//   entry.QualityFlags == nil     → qualityFlagsArg 返回 "{}" 字面量
		//   entry.QualityFlags == []string{} → qualityFlagsArg 返回 "{}" 字面量
		//   两者在 SQL 列上结果相同；区别仅在 SQL 日志 / audit 表里看到的
		//   文本是否带 array 维度。这是"未提供"与"显式清空"在 INSERT
		//   路径上唯一的语义差异，由 qualityFlagsArg 集中处理。
		//
		//   entry.QualityFixActions == nil     → "{}" （与 DEFAULT 一致）
		//   entry.QualityFixActions == []byte("{}") → "{}"
		//   两者在 SQL 列上结果完全相同；qualityActionsArgStr 不区分"未提供"
		//   与"显式清空"——这是 NOT NULL DEFAULT 的设计选择：调用方若需要
		//   区分，要么修改 schema 为 NULLABLE，要么改为单独字段承载。
		qualityFlagsArg(entry.QualityFlags),
		qualityActionsArgStr(entry.QualityFixActions),
		entry.QualityScore,
		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		// (db/migrations/018_upstream_finish_reason.sql). The new column is
		// the SOLE home for the upstream finish_reason.
		entry.UpstreamFinishReason,
		// 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
		jsonOrNull(entry.ToolCalls),
		// 2026-06-26: client-supplied X-Request-Id (debug only).
		entry.ClientRequestID,
		// 2026-06-30: upstream diagnostics (migration 320).
		entry.UpstreamStatusCode,
		entry.ClientTimeout,
		entry.ClientEndpoint,
		entry.StreamChunkErrors,
		// 2026-07-05 P0 fix: stream_chunks_sent is NOT NULL (migration 320).
		// streamChunksSentArg() coerces nil pointer to 0 so the explicit
		// INSERT never trips SQLSTATE 23502 even when the caller does
		// not set the field (e.g. /api/telemetry/request-log HTTP path).
		streamChunksSentArg(entry.StreamChunksSent),
		// 2026-07-01: 附件元数据 (migration 325)。为空时写入 NULL。
		attachmentsArgStr(entry.Attachments),
		// 2026-07-14 (migration 341): client-side origin.
		// client_ip / client_forwarded_for are written for every business
		// row by middleware/origin_mw.go; origin_stage / origin_actor are
		// written by the new probe workers (credential-selfcheck,
		// node-probe, system-health). All four accept NULL (the columns
		// are nullable) so legacy emitters don't break.
		entry.ClientIP,
		entry.ClientForwardedFor,
		entry.OriginStage,
		entry.OriginActor,
		// 2026-07-19 (migration 350): routing attempts tracking
		// 2026-08-22: use nullableJSONArg so an empty in_progress INSERT stores
		// SQL NULL (not JSON null); completion UPDATE can then COALESCE/CASE-fill.
		nullableJSONArg(entry.RoutingAttempts),
		entry.RoutingSummary,
		// 2026-07-27: 客户端感知字段 ($86-$89,与上面 INSERT 列表对齐)
		entry.AgentName,
		entry.AgentType,
		entry.ClientProtocol,
		entry.VirtualClientID,
		// V3.1 (migration 491): $91–$100 queue timestamps
		entry.T0ArrivedAt,
		entry.T1TotalEnqueuedAt,
		entry.T2TotalDequeuedAt,
		entry.T3ModelEnqueuedAt,
		entry.T4ModelDequeuedAt,
		entry.T5CredEnqueuedAt,
		entry.T6CredDequeuedAt,
		entry.T7ForwardStartAt,
		entry.T8ResponseStartAt,
		entry.T9ResponseEndAt,
		// 2026-08-19: discard audit events are JSONB and nullable.
		nullableJSONArg(entry.DiscardEvents),
	)

	if err != nil {
		return err
	}

	// 2026-07-22 Ticket #10: Persist full bodies in request_logs_bodies_hot.
	// The write path must tolerate both historical UNIQUE (request_id, ts)
	// and migration-455 UNIQUE (request_id) deployments, because some hosts
	// already recorded schema_migrations=455 but still serve the old index.
	err = c.upsertRequestLogBodies(ctx, tx, entry.RequestID, strPtrToJSON(entry.RequestBody), strPtrToJSON(entry.ResponseBody))
	if err != nil {
		slog.Error("persist request_logs_bodies_hot failed",
			"request_id", entry.RequestID,
			"has_request_body", entry.RequestBody != nil,
			"has_response_body", entry.ResponseBody != nil,
			"error", err,
		)
		return err
	}

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

	// v4 T7 (2026-08-18, migration 532): terminal-success rows with a
	// gw_session_id claim the session's single final-success marker in the
	// same transaction. Best-effort: any failure (incl. a lost concurrent
	// claim, SQLSTATE 23505 from uq_request_logs_hot_final_success_session)
	// degrades to a normal success row — never fails the business write.
	if shouldClaimFinalSuccess(entry) {
		claimSessionFinalSuccess(ctx, tx, entry.RequestID)
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
		payloadJSON,
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
		payloadJSON,
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

// request_logs row that still carries usage_source='estimated' with the real
// LLM-reported usage contained in entry, marking the row usage_source=
// 'corrected'. It is the write-side counterpart of the estimation fallback in
// relay/handler.go: emitTelemetry estimates tokens when the upstream never
// sent a usage block; when the real numbers later arrive for the same
// request_id (retry success / writeback paths), the regular UPDATE cannot
// replace them — its COALESCE semantics preserve the already-stored estimates.
//
// Only estimated rows are touched: llm rows keep their authoritative values
// and corrected rows keep the first correction (idempotent). Returns the
// number of rows corrected so callers can gate follow-ups (e.g. the
// format-anomalies actual_tokens backfill) on an actual transition.
// The caller is expected to invoke this off the request hot path (async,
// best-effort); a DB error is returned, never retried here.
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
	totalTokens := total(entry.PromptTokens, entry.CompletionTokens)
	tag, err := c.requestLogDatabase().Exec(ctx, `
		UPDATE request_logs_hot
		   SET prompt_tokens     = COALESCE($2, prompt_tokens),
		       completion_tokens = COALESCE($3, completion_tokens),
		       total_tokens      = COALESCE($4, total_tokens),
		       cache_read_tokens = COALESCE($5, cache_read_tokens),
		       cache_write_tokens = COALESCE($6, cache_write_tokens),
		       usage_source      = 'corrected'
		 WHERE request_id = $1
		   AND usage_source = 'estimated'
		   AND ($2 IS NOT NULL OR $3 IS NOT NULL)
	`,
		entry.RequestID,
		entry.PromptTokens,
		entry.CompletionTokens,
		totalTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (c *Client) updateRequestLog(entry *RequestLogEntry) error {
	db := c.requestLogDatabase()
	if db == nil {
		return errNoTelemetryDB
	}
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

	updated, err := tx.Exec(ctx, `
		-- 2026-07-23 migration 455: request_logs_hot PK changed from (request_id, ts)
		-- to (request_id). With request_id as the unique key, the CTE (which found
		-- the latest row among duplicates) is no longer needed. Direct UPDATE by
		-- request_id is now deterministic.
		UPDATE request_logs_hot
		   SET client_model = COALESCE($2, client_model),
		       outbound_model = COALESCE($3, outbound_model),
		       credential_id = COALESCE($4, credential_id),
		       provider_id = COALESCE($5, provider_id),
		       canonical_id = COALESCE($6, canonical_id),
		       client_profile = COALESCE($7, client_profile),
		       request_mode = COALESCE($8, request_mode),
		       affinity_hit = COALESCE($9, request_logs_hot.affinity_hit),
		       end_user_id = COALESCE($10, end_user_id),
		       prompt_tokens = COALESCE($11, prompt_tokens),
		       completion_tokens = COALESCE($12, completion_tokens),
		       total_tokens = COALESCE($13, total_tokens),
		       cache_read_tokens = COALESCE($14, cache_read_tokens),
		       cache_write_tokens = COALESCE($15, cache_write_tokens),
		       -- audit-ir-multimodal (2026-07-13): multimodal token fields
		       reasoning_tokens = COALESCE($16, reasoning_tokens),
		       image_tokens = COALESCE($17, image_tokens),
		       audio_tokens = COALESCE($18, audio_tokens),
		       video_tokens = COALESCE($19, video_tokens),
		       provider_tokens = COALESCE($20, provider_tokens),
		       cost_usd = COALESCE($21, cost_usd),
		       cost_display = COALESCE($22, cost_display),
		       cost_currency = COALESCE($23, cost_currency),
		       stream_first_chunk_ms = COALESCE($24, stream_first_chunk_ms),
		       stream_chunk_count = COALESCE($25, stream_chunk_count),
		       stream_done_received = COALESCE($26, stream_done_received),
		       stream_interrupted = COALESCE($27, stream_interrupted),
		       response_checksum = COALESCE($28, response_checksum),
		       response_preview = COALESCE($29, response_preview),
		       -- 2026-07-22: request_body and response_body removed from main table UPDATE.
		       -- These fields are now stored in request_logs_bodies_hot side table.
		       failure_stage = COALESCE($30, failure_stage),
		       failure_detail_code = COALESCE($31, failure_detail_code),
		       transform_rule_id = COALESCE($32, transform_rule_id),
		       egress_protocol = COALESCE($33, egress_protocol),
		       request_preview = COALESCE($34, request_preview),
		       transform_summary = COALESCE($35, transform_summary),
		       -- CO-2 (2026-08-15): 'corrected' is terminal in the
		       -- estimated → corrected state machine. A generic UPDATE
		       -- carrying usage_source='llm' racing the correction
		       -- backfill must not downgrade the row back to 'llm'.
		       usage_source = CASE
		           WHEN usage_source = 'corrected' THEN usage_source
		           ELSE COALESCE(NULLIF($36, ''), usage_source)
		       END,
		       success = COALESCE($37, success),
		       request_status = COALESCE($38, request_status),
		       -- 2026-06-20: clear error_kind on success to prevent
		       -- cross-request pollution (e.g. a previous failure's
		       -- error_kind leaking into a later successful UPDATE).
		       error_kind = CASE
		           WHEN COALESCE($37, success) = TRUE THEN NULL
		           ELSE COALESCE($39, error_kind)
		       END,
		       latency_ms = COALESCE($40, latency_ms),
		       identity_hash = COALESCE($41, identity_hash),
		       search_text = COALESCE($42, search_text),
		       gw_session_id = COALESCE($43, gw_session_id),
		       gw_task_id = COALESCE($44, gw_task_id),
		       api_key_prefix = COALESCE($45, api_key_prefix),
		       api_key_owner_user = COALESCE($46, api_key_owner_user),
		       application_code = COALESCE($47, application_code),
		       is_auto_request = COALESCE($48, is_auto_request),
		       task_type = COALESCE($49, task_type),
		       auto_profile = COALESCE($50, auto_profile),
		       auto_decision = COALESCE($51::text::jsonb, auto_decision),
		       auto_confidence = COALESCE($52, auto_confidence),
		       work_type = COALESCE($53, work_type),
		       credits_charged = COALESCE($54, credits_charged),
-- Round 47 compression v7 T2: parent-child chain payload.
			       parent_request_id = COALESCE($55, parent_request_id),
			       compression_reason = COALESCE($56, compression_reason),
			       compression_strategy = COALESCE($57, compression_strategy),
			       compression_meta = COALESCE($58::text::jsonb, compression_meta),
			       -- 2026-08-19: token-band observability.
			       token_band = COALESCE($59, token_band),
			       -- v3 (2026-06-19) T23: session-level outbound body payload.
			       outbound_body      = COALESCE($60::text::jsonb, outbound_body),
			       outbound_msg_count = COALESCE($61, outbound_msg_count),
			       outbound_token_est = COALESCE($62, outbound_token_est),
			       outbound_msg_hashes = COALESCE($63::text::jsonb, outbound_msg_hashes),
			       -- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
			       quality_flags        = COALESCE($64::text[], quality_flags),
			       quality_fix_actions  = COALESCE($65::text::jsonb, quality_fix_actions),
			       quality_score        = COALESCE($66, quality_score),
		   -- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		   -- (db/migrations/018_upstream_finish_reason.sql). The new column is
		   -- the SOLE home for the upstream finish_reason.
		   upstream_finish_reason = COALESCE($67, upstream_finish_reason),
		   -- 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
		   tool_calls = COALESCE($68::text::jsonb, tool_calls),
		   -- 2026-06-26: client-supplied X-Request-Id (debug only). COALESCE so
		   -- a late success UPDATE does not blank a value set on INSERT.
		   client_request_id = COALESCE($69, client_request_id),
		   -- 2026-06-30: upstream diagnostics (migration 320).
		   upstream_status_code = COALESCE($70, upstream_status_code),
		   client_timeout = COALESCE($71, client_timeout),
		   client_endpoint = COALESCE($72, client_endpoint),
		   stream_chunk_errors = COALESCE($73, stream_chunk_errors),
		   stream_chunks_sent = COALESCE($74, stream_chunks_sent),
		   -- 2026-07-14 (migration 341): origin metadata. First-write-wins
		   -- (see INSERT path rationale) — middleware/origin_mw.go sets
		   -- client_ip / client_forwarded_for on the inbound row and the
		   -- probe workers set origin_stage / origin_actor on probe rows.
		   client_ip            = COALESCE($75, client_ip),
		   client_forwarded_for = COALESCE($76, client_forwarded_for),
			   origin_stage         = COALESCE($77, origin_stage),
			   origin_actor         = COALESCE($78, origin_actor),
			   -- 2026-07-27: client perception fields. First-write-wins keeps
			   -- values extracted on the inbound row when a later completion,
			   -- failure, or disconnect update carries only partial metadata.
			   agent_name           = COALESCE(agent_name, $79),
			   agent_type           = COALESCE(agent_type, $80),
			   client_protocol      = COALESCE(client_protocol, $81),
			   virtual_client_id    = COALESCE(virtual_client_id, $82),
			   -- V3.1 queue timestamps (migration 491): success path uses
			   -- EmitRequestLogUpdate; without these columns t0..t9 stayed NULL
			   -- forever after the in_progress INSERT (2026-08-22 diagnose).
			   t0_arrived_at        = COALESCE($83, t0_arrived_at),
			   t1_total_enqueued_at = COALESCE($84, t1_total_enqueued_at),
			   t2_total_dequeued_at = COALESCE($85, t2_total_dequeued_at),
			   t3_model_enqueued_at = COALESCE($86, t3_model_enqueued_at),
			   t4_model_dequeued_at = COALESCE($87, t4_model_dequeued_at),
			   t5_cred_enqueued_at  = COALESCE($88, t5_cred_enqueued_at),
			   t6_cred_dequeued_at  = COALESCE($89, t6_cred_dequeued_at),
			   t7_forward_start_at  = COALESCE($90, t7_forward_start_at),
			   t8_response_start_at = COALESCE($91, t8_response_start_at),
			   t9_response_end_at   = COALESCE($92, t9_response_end_at),
			   discard_events       = COALESCE($93::text::jsonb, discard_events),
			   -- 2026-08-22: completion UPDATE must carry routing + attachments
			   -- (+ canonical refresh). Same class as t0..t9 gap — emitTelemetry
			   -- sets these on reqLog then EmitRequestLogUpdate.
			   canonical_model      = COALESCE($94, canonical_model),
			   routing_attempts     = CASE
			       WHEN $95::text IS NOT NULL AND $95::text <> '' AND $95::text <> 'null'
			       THEN $95::text::jsonb
			       ELSE routing_attempts
			   END,
			   routing_summary      = COALESCE($96, routing_summary),
			   attachments          = CASE
			       WHEN $97::text IS NOT NULL AND $97::text <> '' AND $97::text <> 'null'
			       THEN $97::text::jsonb
			       ELSE attachments
			   END
		   WHERE request_id = $1

		     AND NOT (
				request_logs_hot.request_status = 'failure'
				OR (
					(request_logs_hot.success = TRUE
					 OR request_logs_hot.request_status = 'success')
					AND NOT (
						COALESCE($37, FALSE) = TRUE
						AND $38 = 'success'
					)
				)
			)
		 RETURNING request_logs_hot.ts
`,
		entry.RequestID,
		entry.ClientModel,
		entry.OutboundModel,
		entry.CredentialID,
		entry.ProviderID,
		entry.CanonicalID,
		entry.ClientProfile,
		entry.RequestMode,
		entry.AffinityHit,
		entry.EndUserID,
		entry.PromptTokens,
		entry.CompletionTokens,
		totalTokens,
		entry.CacheReadTokens,
		entry.CacheWriteTokens,
		// audit-ir-multimodal (2026-07-13): multimodal token fields
		entry.ReasoningTokens,
		entry.ImageTokens,
		entry.AudioTokens,
		entry.VideoTokens,
		entry.ProviderTokens,
		entry.CostUSD,
		entry.CostDisplay,
		entry.CostCurrency,
		entry.StreamFirstChunkMs,
		entry.StreamChunkCount,
		entry.StreamDoneReceived,
		entry.StreamInterrupted,
		entry.ResponseChecksum,
		entry.ResponsePreview,
		// 2026-07-22: request_body and response_body removed from main table UPDATE.
		// These fields are now stored in request_logs_bodies_hot side table.
		entry.FailureStage,
		entry.FailureDetailCode,
		entry.TransformRuleID,
		entry.EgressProtocol,
		entry.RequestPreview,
		entry.TransformSummary,
		nonEmptyPtr(entry.UsageSource, ""),
		boolptr(entry.Success),
		entry.RequestStatus,
		entry.ErrorKind,
		entry.LatencyMs,
		entry.IdentityHash,
		searchText(entry),
		entry.GwSessionID,
		entry.GwTaskID,
		entry.APIKeyPrefix,
		entry.APIKeyOwnerUser,
		entry.ApplicationCode,
		entry.IsAutoRequest,
		entry.TaskType,
		entry.AutoProfile,
		strPtrToJSON(entry.AutoDecision),
		entry.AutoConfidence,
		entry.WorkType,
		entry.CreditsCharged,
		// Round 47 compression v7 T2: parent-child chain payload.
		entry.ParentRequestID,
		entry.CompressionReason,
		entry.CompressionStrategy,
		string(jsonOrNull(entry.CompressionMeta)),
		// 2026-08-19: token-band observability.
		entry.TokenBand,
		// v3 (2026-06-19) T23: session-level outbound body payload.
		string(jsonOrNull(entry.OutboundBody)),
		entry.OutboundMsgCount,
		entry.OutboundTokenEst,
		string(jsonOrNull(entry.OutboundMsgHashes)),
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
		// quality_flags is always encoded as a PostgreSQL text array;
		// quality_fix_actions is JSONB.
		qualityFlagsArg(entry.QualityFlags),
		string(qualityActionsArgStr(entry.QualityFixActions)),
		entry.QualityScore,
		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code
		// (db/migrations/018_upstream_finish_reason.sql). The new column is
		// the SOLE home for the upstream finish_reason.
		entry.UpstreamFinishReason,
		// 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
		string(jsonOrNull(entry.ToolCalls)),
		// 2026-06-26: client-supplied X-Request-Id (debug only).
		entry.ClientRequestID,
		// 2026-06-30: upstream diagnostics (migration 320).
		entry.UpstreamStatusCode,
		entry.ClientTimeout,
		entry.ClientEndpoint,
		entry.StreamChunkErrors,
		entry.StreamChunksSent,
		// 2026-07-14 (migration 341): client-side origin.
		entry.ClientIP,
		entry.ClientForwardedFor,
		entry.OriginStage,
		entry.OriginActor,
		entry.AgentName,
		entry.AgentType,
		entry.ClientProtocol,
		entry.VirtualClientID,
		entry.T0ArrivedAt,
		entry.T1TotalEnqueuedAt,
		entry.T2TotalDequeuedAt,
		entry.T3ModelEnqueuedAt,
		entry.T4ModelDequeuedAt,
		entry.T5CredEnqueuedAt,
		entry.T6CredDequeuedAt,
		entry.T7ForwardStartAt,
		entry.T8ResponseStartAt,
		entry.T9ResponseEndAt,
		nullableJSONArg(entry.DiscardEvents),
		entry.CanonicalModel,
		nullableJSONArg(entry.RoutingAttempts),
		entry.RoutingSummary,
		attachmentsArgStr(entry.Attachments),
	)

	if err != nil {
		return err
	}
	if updated.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM request_logs_hot WHERE request_id = $1
			)
		`, entry.RequestID).Scan(&exists); err != nil {
			return err
		}
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			slog.Warn("telemetry update rollback failed", "request_id", entry.RequestID, "error", rbErr)
		}
		if exists {
			return nil
		}
		fallback := *entry
		fallback.Op = RequestLogInsert
		return c.insertRequestLog(&fallback)
	}

	if err = c.upsertRequestLogBodies(ctx, tx, entry.RequestID, strPtrToJSON(entry.RequestBody), strPtrToJSON(entry.ResponseBody)); err != nil {
		return err
	}

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

	// v4 T7 (2026-08-18, migration 532): same final-success claim as the
	// INSERT path — see insertRequestLog for the degrade semantics. The
	// RowsAffected==0 fallback above re-enters insertRequestLog, which
	// claims on its own.
	if shouldClaimFinalSuccess(entry) {
		claimSessionFinalSuccess(ctx, tx, entry.RequestID)
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

// shouldClaimFinalSuccess reports whether entry represents a terminal success
// that carries a session key and therefore must attempt the v4 T7
// session-level final-success claim. Failures, cancellations, client
// disconnects and rows without a gw_session_id never claim (R6.2: 空
// gw_session_id 不受唯一约束；失败/取消路径不改).
func shouldClaimFinalSuccess(entry *RequestLogEntry) bool {
	return entry != nil &&
		entry.Success &&
		entry.GwSessionID != nil &&
		*entry.GwSessionID != ""
}

// claimSessionFinalSuccess marks the request_logs_hot row for requestID as
// THE single final success of its gw_session_id (v4 T7 / R6.2 / P1-7,
// migration 532). At most one row per session keeps is_final_success=TRUE;
// superseded success rows are left untouched (history is never rewritten —
// the admin timeline labels them "superseded" at read time).
//
// The claim is a single self-guarding UPDATE inside the caller's transaction:
//   - only terminal-success rows (success=TRUE AND request_status='success')
//     with a non-empty gw_session_id can claim;
//   - NOT EXISTS checks both the hot table and the promoted partition parent
//     (request_logs), so a claim already held by an earlier success — whether
//     still hot or already promoted past the 7-day window — blocks the claim;
//   - concurrent double claims lose the race against the partial unique
//     index uq_request_logs_hot_final_success_session (SQLSTATE 23505).
//
// Storage note: The UPDATE targets request_logs_hot (heap storage) NOT the
// parent partitioned table request_logs. Citus Columnar partitions do NOT
// support UPDATE/CTID scans (SQLSTATE 0A000: "UPDATE and CTID scans not
// supported for ColumnarScan"). The hot table is heap and supports UPDATE.
// The NOT EXISTS subquery checks the parent table (which may have Columnar
// partitions) but that is a SELECT, which Columnar supports.
//
// Failure semantics (all non-fatal, business row still commits):
//   - 23505: lost race → row degrades to a normal success (superseded);
//   - any other error (e.g. 42703 on a pre-532 schema during rolling deploy):
//     claim skipped with a warning, row degrades to an unmarked success that
//     sql/scripts/report_duplicate_session_success.sql can report for manual
//     backfill.
func claimSessionFinalSuccess(ctx context.Context, tx pgx.Tx, requestID string) {
	if tx == nil || requestID == "" {
		return
	}
	// The savepoint isolates the claim: without it a 23505 would abort the
	// whole request_logs transaction and drop the business row.
	if _, err := tx.Exec(ctx, `SAVEPOINT gw_final_success_claim`); err != nil {
		slog.Debug("final-success claim: savepoint failed, skipping",
			"request_id", requestID, "error", err)
		return
	}
	tag, err := tx.Exec(ctx, `
		UPDATE request_logs_hot
		   SET is_final_success = TRUE
		 WHERE request_id = $1
		   AND success = TRUE
		   AND request_status = 'success'
		   AND COALESCE(gw_session_id, '') <> ''
		   AND NOT EXISTS (
		        SELECT 1
		          FROM request_logs_hot other
		         WHERE other.gw_session_id = request_logs_hot.gw_session_id
		           AND other.is_final_success
		           AND other.request_id <> request_logs_hot.request_id
		   )
		   AND NOT EXISTS (
		        SELECT 1
		          FROM request_logs promoted
		         WHERE promoted.gw_session_id = request_logs_hot.gw_session_id
		           AND promoted.is_final_success
		   )
	`, requestID)
	if err != nil {
		_, rbErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT gw_final_success_claim`)
		var pgErr *pgconn.PgError
		// 23505 = unique_violation (lost the race against
		// uq_request_logs_hot_final_success_session).
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost the concurrent claim race: this row stays a normal
			// success (superseded by the winner). R6.2: 不回改历史行.
			slog.Info("final-success claim superseded by concurrent winner",
				"request_id", requestID)
		} else {
			slog.Warn("final-success claim degraded (non-fatal)",
				"request_id", requestID, "error", err)
		}
		if rbErr != nil {
			slog.Warn("final-success claim: rollback to savepoint failed",
				"request_id", requestID, "error", rbErr)
		}
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Debug("final-success claim granted", "request_id", requestID)
	}
	// Release is cosmetic (released at COMMIT anyway) and must not fail the tx.
	//nolint:errcheck // best-effort
	_, _ = tx.Exec(ctx, `RELEASE SAVEPOINT gw_final_success_claim`)
}

// upsertRequestLogBodies writes request/response body to request_logs_bodies_hot.
// Uses a single INSERT ... ON CONFLICT DO UPDATE (migration 455 gave the hot table
// UNIQUE(request_id)). The previous triple-UPDATE/INSERT/UPDATE pattern had two
// problems:
//  1. First UPDATE was dead code — the row doesn't exist yet in request_logs_bodies_hot.
//  2. Third UPDATE was redundant — step 2 (INSERT) already created the row.
func (c *Client) upsertRequestLogBodies(ctx context.Context, tx pgx.Tx, requestID, requestBodyJSON, responseBodyJSON string) error {
	// Keep missing bodies as NULL so metadata-only updates cannot erase a body
	// captured by the initial or successful request-log write.
	reqJSON := requestBodyJSON
	respJSON := responseBodyJSON
	// CO-5 compatibility: existing digest envelopes remain readable, but new
	// request and response bodies retain their complete JSON payloads. This
	// avoids discarding stream evidence before downstream audit consumers read it.
	if requestBodiesSummaryEnabled() {
		reqJSON = summarizeBodyJSON(reqJSON)
		respJSON = summarizeBodyJSON(respJSON)
	}
	// Use now() as ts for the hot table. Migration 455 (2026-07-23) changed the
	// unique key to UNIQUE(request_id); ON CONFLICT must be (request_id), NOT
	// (request_id, ts) — the composite form triggers 42P10 at runtime.
	// The UPDATE clause only replaces a body when this write supplied one.
	_, err := tx.Exec(ctx, `
		INSERT INTO request_logs_bodies_hot (request_id, ts, request_body, response_body)
		VALUES ($1, now(), NULLIF($2, 'null')::jsonb, NULLIF($3, 'null')::jsonb)
		ON CONFLICT (request_id) DO UPDATE
			SET request_body = COALESCE(EXCLUDED.request_body, request_logs_bodies_hot.request_body),
				    response_body = COALESCE(EXCLUDED.response_body, request_logs_bodies_hot.response_body),
				    ts = EXCLUDED.ts
	`, requestID, reqJSON, respJSON)
	return err
}

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
// request_logs.attachments JSONB column. Unlike quality_flags /
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
func strPtrToJSON(s *string) string {
	if s == nil {
		return "null"
	}
	if *s == "" || !json.Valid([]byte(*s)) {
		return "{}"
	}
	return *s
}

// streamChunksSentArg returns a value safe to bind to the NOT NULL
// request_logs.stream_chunks_sent integer column. The column has a
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
	if len(v) < len(original) {
		slog.Warn("telemetry JSON field repaired by truncation",
			"field", field,
			"original_bytes", len(original),
			"kept_bytes", len(v))
		incSanitizeEvent("repaired", field, "json_field", "sanitize")
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
	if len(cleaned) < len(*raw) {
		slog.Warn("telemetry JSONB field repaired by truncation",
			"field", field,
			"original_bytes", len(*raw),
			"kept_bytes", len(cleaned))
		incSanitizeEvent("repaired", field, "raw_json_field", "sanitize")
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

// lookupTurnNumber queries request_logs to count the turn number for this session.
// Returns the next turn number (1-based). If sessionID is empty or query fails, returns 1.
// Uses the same transaction tx to maintain consistency.
func lookupTurnNumber(ctx context.Context, tx pgx.Tx, sessionID string) int {
	if sessionID == "" {
		return 1
	}

	// Count existing requests in this session (including the current insert from the same tx)
	// Since we're in the middle of the INSERT transaction, we need to count INCLUDING
	// the row we just inserted. The turn_no should be: COUNT(*) for this session.
	var count int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(*) 
		FROM request_logs 
		WHERE gw_session_id = $1
	`, sessionID).Scan(&count)

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
