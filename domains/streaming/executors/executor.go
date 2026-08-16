package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/attachments"     //nolint:depguard // MM-1 outbound attachment URL rewrite
	"github.com/kaixuan/llm-gateway-go/domains/credential"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/memory"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/routingstate"
	"github.com/kaixuan/llm-gateway-go/domains/session"        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/irconv"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/kaixuan/llm-gateway-go/internal/runctx"
	gwtrace "github.com/kaixuan/llm-gateway-go/internal/trace"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit" // AUDIT-3: 限流总开关
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/settings"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// Default minimax-m3 catalog context window (512K). DB models_canonical is SSoT for runtime;
// keep in sync via deploy/sql/20260613-minimax-m3-context-window-512k.sql.
const MinimaxM3ContextWindow = 512_000

const maxBodySize = 128 << 20 // 128MB - increased for large context models like claude-opus-4-8 (1M context)

// providerResolver is the subset of *provider.Client the routing executor
// depends on. Defined as an interface so compaction tests can swap in a
// stub without booting the DB pool.
type providerResolver interface {
	Enabled() bool
	// 2026-07-03: Bug #7 fix - added tenantID parameter
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
	ModelKnown(ctx context.Context, model string) bool
}

type modalityProviderResolver interface {
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
}

type probeCandidateResolver interface {
	GetProbeCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, error)
}

type NormalizerFunc func(chunk []byte, isStream bool) []byte

// 2026-07-16: 自适应超时与状态管理接口（避免循环依赖）

// TimeoutCalculator 计算自适应超时
type TimeoutCalculator interface {
	Calculate(input AdaptiveTimeoutInput) time.Duration
}

// AdaptiveTimeoutInput 自适应超时输入参数
type AdaptiveTimeoutInput struct {
	RequestSize int
	IsSession   bool
	IsRetry     bool
	AttemptNum  int
	ProviderURL string
	RecentTTFB  *time.Duration
}

// TTFBRecorder 记录和查询 TTFB 历史
type TTFBRecorder interface {
	Record(credentialID int, ttfb time.Duration)
	Get(credentialID int) *TTFBStats
}

// TTFBStats TTFB 统计信息
type TTFBStats struct {
	RecentTTFB  time.Duration
	AvgTTFB     time.Duration
	UpdatedAt   time.Time
	SampleCount int
}

// PredictiveTTFBDecision describes a candidate that exceeded the configured
// predictive TTFT threshold. AvgTTFB is kept as a duration internally and is
// converted to milliseconds at the trace boundary.
type PredictiveTTFBDecision struct {
	Reason      string
	AvgTTFB     time.Duration
	SampleCount int
}

// PredictiveTTFBSkipper is the narrow request-routing contract for O-1.
// Implementations must be read-only; candidate health and limiter state are
// intentionally managed by the existing executor flow.
type PredictiveTTFBSkipper interface {
	ShouldSkip(credentialID int) (PredictiveTTFBDecision, bool)
}

// RequestValidator 请求格式校验器
type RequestValidator interface {
	Validate(ctx context.Context, requestBody []byte) (*ValidationResult, error)
}

// ValidationResult 校验结果
type ValidationResult struct {
	Valid    bool
	Errors   []ValidationError
	Warnings []string
}

// ValidationError 校验错误
type ValidationError struct {
	Field   string
	Code    string
	Message string
}

// ExecutionRecorder 记录执行结果
type ExecutionRecorder interface {
	RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error
}

// RawDataLogger 记录原始请求/响应载荷（2026-07-26）
type RawDataLogger interface {
	LogRequest(requestID string, protocol string, body []byte) error
	LogResponse(requestID string, protocol string, body []byte, isStream bool) error
}

// IntegrityDetector (2026-07-28) is the per-request observer that
// emits model-integrity events (model_mismatch, finish_refusal,
// finish_truncation, token_arith_fail, empty_response,
// repeated_content, fingerprint_drift). The Executor calls it from
// both the OpenAI non-stream success path and the streaming completion
// path; nil disables the feature (legacy behavior).
//
// Defined in the integrity package; declared here as an interface so
// the executors package doesn't import integrity (keeps the dependency
// graph one-directional — integrity is wired in main.go and pushed in
// via Executor.IntegrityDetector).
type IntegrityDetector interface {
	Observe(ctx context.Context, c IntegrityCandidate)
}

// IntegrityCandidate is the minimal view the executor hands the
// detector. Mirrors integrity.Candidate; duplicated as a struct so
// this package does not need to import integrity. The detector itself
// will accept the public type via an adapter at construction time in
// main.go.
type IntegrityCandidate struct {
	RequestID          string
	TenantID           string
	ApplicationID      *int
	APIKeyID           *int
	ProviderID         *int
	ProviderCode       string
	CredentialID       *int
	ClientModel        string
	OutboundModel      string
	RawModel           string
	RespModel          string
	FinishReason       string
	PromptTokens       *int
	CompletionTokens   *int
	TotalTokens        *int
	InputTokens        *int
	OutputTokens       *int
	ChunkCount         int
	ChunksSent         int
	ContentPreview     string
	TextContent        string
	ProviderResponseID string
	SystemFingerprint  string
	UsageSource        string
	IsStream           bool
	// ResponseBody is the upstream-returned body, kept verbatim for
	// the detector to inspect (extract finish_reason / tokens / model
	// without re-doing the JSON parse). Empty disables body-based
	// checks.
	ResponseBody []byte

	// RepeatedContentDetected and the fields below carry the incremental
	// (mid-stream) repeated-content finding from the stream capture's
	// integrity observer. When set, the detector records this finding
	// rather than rescanning TextContent, so exactly one
	// repeated_content event is written per request. See
	// domains/hooks/audit/stream_integrity.go.
	RepeatedContentDetected    bool
	RepeatedContentHash        string
	RepeatedContentHits        int
	RepeatedContentBlockSize   int
	RepeatedContentBlocksTotal int
	// StreamAborted reports that the incremental rule cut the stream.
	StreamAborted bool
}

// UpstreamRequestLogger records the exact body sent to an upstream provider.
// Implementations are optional so existing diagnostic fakes stay compatible.
type UpstreamRequestLogger interface {
	LogUpstreamRequest(requestID string, protocol string, body []byte) error
}

// envelopeAwareUpstreamRequestLogger is the optional interface that
// loggers implement to receive the full correlation envelope on
// upstream request log writes. Falls back to the basic
// UpstreamRequestLogger path when the assertion fails.
type envelopeAwareUpstreamRequestLogger interface {
	LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
}

// envelopeAwareUpstreamResponseLogger is the optional interface for
// upstream response frames that want to carry the correlation
// envelope. Loggers that don't implement it fall back to the basic
// RawDataLogger.LogResponse path.
type envelopeAwareUpstreamResponseLogger interface {
	LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
}

// envelopeAwareClientResponseLogger is the optional interface for the
// final body returned to the client. Loggers that don't implement it
// fall back to the basic ClientResponseLogger path.
type envelopeAwareClientResponseLogger interface {
	LogClientResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope)
}

// envelopeFromParams builds a correlation envelope from ExecParams so
// every raw entry can be cross-referenced with request_logs.
//
// 2026-07-28 §5.7: when ExecParams.Audit is non-nil, the full
// AuditContext is the source of truth — every field the operator
// dashboard needs is populated. When Audit is nil (legacy tests /
// async-retry paths that build ExecParams directly), the flat
// fields on ExecParams are used. The previous implementation
// incorrectly bound GWTaskID to the model name; AuditContext
// resolves GWTaskID from X-Gw-Task-Id or session.TaskID.
func envelopeFromParams(params *ExecParams) RawCorrelationEnvelope {
	if params == nil {
		return RawCorrelationEnvelope{}
	}
	if params.Audit != nil {
		return params.Audit.RawCorrelationEnvelope()
	}
	env := RawCorrelationEnvelope{
		ClientRequestID:  params.ClientRequestID,
		GWSessionID:      params.SessionID,
		GWTaskID:         params.GWTaskID,
		ParentRequestID:  params.ParentRequestID,
		TenantID:         params.TenantID,
		APIKeyID:         params.KeyID,
		ProviderID:       params.ProviderID,
		CredentialID:     params.CredentialID,
		AttemptNo:        params.AttemptNo,
		UpstreamEndpoint: params.UpstreamEndpoint,
		TraceID:          params.TraceID,
		SpanID:           params.SpanID,
	}
	if params.AppID != nil {
		env.ApplicationID = fmt.Sprintf("%d", *params.AppID)
	}
	return env
}

// ClientResponseLogger records the final body returned to the client.
// Streaming bridges use RawDataLogger.LogResponse for pre-conversion upstream
// frames; this optional interface is used for non-streaming client responses.
type ClientResponseLogger interface {
	LogClientResponse(requestID string, protocol string, body []byte) error
}

// AnomalyReporter 报告协议转换异常（2026-07-26）
type AnomalyReporter interface {
	ReportAnomaly(requestID string, anomalyType string, details map[string]interface{}) error
}

// SemanticAnalyzer 语义分析器，检测工具调用和内容丢失（2026-07-26）
type SemanticAnalyzer interface {
	AnalyzeRequest(requestID string, irReq interface{}) error
	AnalyzeResponse(requestID string, irResp interface{}) (*SemanticAnalysisResult, error)
}

// SemanticAnalysisResult is the protocol-neutral result returned by a
// SemanticAnalyzer. It keeps the Executor independent from a concrete IR
// implementation while giving stream bridges enough information to report an
// actionable anomaly.
type SemanticAnalysisResult struct {
	IsIncomplete          bool
	Reason                string
	Confidence            float64
	SuspectedMissingTools bool
	Indicators            []string
}

// ExecutionOutcome 执行结果
type ExecutionOutcome struct {
	CredentialID   int
	ProviderID     int
	RawModel       string
	CanonicalModel string
	RequestID      string
	TenantID       string
	Success        bool
	ErrorKind      errorsx.ErrorKind
	ErrorDetail    string
	LatencyMs      int64
	TTFBMs         int64
	IsStream       bool
	ChunkCount     int
	IsRetry        bool
	AttemptNum     int
	StartedAt      time.Time
	CompletedAt    time.Time
}

type StreamOutcome = struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption

	// Kind (2026-07-28 §5.6) is the structured errorsx.ErrorKind the
	// executor assigns to the interruption. When non-empty,
	// streamErrorKindForDetailCode prefers it over the legacy
	// detail-code switch. Empty when the executor did not classify
	// the outcome (e.g. async-retry paths, legacy bridges).
	Kind errorsx.ErrorKind
}

type StreamHandler func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, catalogCode string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome

// ProbeSyncFunc is the contract bg.NodeProbeWorker.ProbeSync satisfies.
// Defined here (rather than imported from bg) so the executors package
// does not transitively depend on the bg package. Wiring happens in
// cmd/gateway/main.go.
//
// Returns true iff at least one (cred, model) pair recovered on both
// direct + gateway rounds; the executor then re-plans candidates and
// retries the request transparently. Returns false on every other
// outcome (timeout, client cancel, all-failed).
type ProbeSyncFunc func(
	ctx context.Context,
	candidates []credentialstate.NoCandidatesCandidate,
	tenantID string,
	parentReqID string,
) bool

// NodeProbeHealthyFunc is the contract bg.MarkNodeProbeHealthy satisfies.
// Defined here (rather than imported from bg) so the executors package
// does not transitively depend on the bg package. Wiring happens in
// cmd/gateway/main.go.
// Clears the node_probe_state failure gate for a (credential, model) pair
// after a successful real business request.
type NodeProbeHealthyFunc func(ctx context.Context, credentialID int, rawModel string) error

// StreamWrapperFunc is injected by the main.go wiring to handle streaming
// responses. Receives the upstream resp and returns a StreamOutcome to let
// Execute() decide failover. The fourth argument (capture) is the audit
// StreamCapture that accumulates IR chunks for request_logs.response_summary.
type StreamWrapperFunc func(w http.ResponseWriter, resp *http.Response, norm NormalizerFunc, capture *audit.StreamCapture) StreamOutcome

// AnthropicPassthroughFunc is the signature for the Q4 Anthropic SSE
// AnthropicPassthroughFunc forwards Anthropic-format SSE upstream to the
// client unchanged (Q4 path: anthropic client → anthropic upstream).
// pc is an optional pending-store capturer (Track C C5, 2026-06-21)
// that records the SSE body so it can be replayed on client reconnect.
// Wired from main.go so the routing package does not import relay.
type AnthropicPassthroughFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// ChatToAnthropicFunc converts an OpenAI chat completions body to
// Anthropic Messages format. Wired from main.go so the routing
// package does not import relay.
type ChatToAnthropicFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAIFunc converts an Anthropic Messages body to OpenAI
// chat completions format. Wired from main.go so the routing
// package does not import relay.
type AnthropicToOpenAIFunc func(body []byte) ([]byte, error)

// AnthropicToOpenAISSEFunc is the streaming counterpart of
// AnthropicToOpenAIFunc: reads Anthropic-format SSE upstream and
// writes OpenAI-format SSE chunks to w (Q3 path: openai client →
// anthropic upstream). pc is the optional pending-store capturer.
type AnthropicToOpenAISSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// AnthropicToResponsesSSEFunc is the streaming counterpart that reads
// Anthropic-format SSE upstream and writes OpenAI Responses API SSE to w.
// Used by executeAnthropic when ClientProtocol == "openai-responses"
// (Phase E, 2026-07-01). Same signature as AnthropicToOpenAISSEFunc
// so the executor wiring is symmetric.
type AnthropicToResponsesSSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// OpenAIToResponsesSSEFunc is the streaming counterpart that reads
// OpenAI chat.completion.chunk SSE upstream and writes OpenAI Responses
// API SSE to w. Used by executeOpenAI when ClientProtocol ==
// "openai-responses" (Phase E, 2026-07-01).
type OpenAIToResponsesSSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// AnthropicToChatResponseFunc is the non-stream counterpart that
// converts an Anthropic Messages JSON body into an OpenAI
// chat.completion JSON body. Wired from main.go.
type AnthropicToChatResponseFunc func(body []byte, clientModel string) ([]byte, error)

// ChatResponseToAnthropicFunc is the Q2 (anthropic client ← openai
// upstream) non-stream **response** counterpart of AnthropicToChatResponseFunc:
// converts an OpenAI Chat Completions JSON response body into an
// Anthropic Messages JSON response body.
//
// Wired from main.go (streaming.ConvertChatResponseToAnthropic). When
// the IR feature flag is on, the executor calls
// `IR.ParseOpenAIResponse + IR.SerializeAnthropicResponse` directly
// instead of this hook.
type ChatResponseToAnthropicFunc func(body []byte, clientModel, requestID string) ([]byte, error)

// OpenAIToAnthropicSSEFunc is the Q2 streaming **response** counterpart
// of AnthropicToOpenAIStream: reads OpenAI-format SSE upstream and
// writes Anthropic-format SSE to the client. Used by executeOpenAI
// when ClientProtocol == "anthropic-messages" AND the upstream is
// OpenAI-shaped (cand.Protocol != "anthropic-messages").
//
// Wired from main.go (streaming.StreamOpenAIToAnthropicSSE).
type OpenAIToAnthropicSSEFunc func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// SanitizeAnthropicToolsFunc strips OpenAI/custom tool type wrappers from
// an Anthropic Messages request body before forwarding to upstream.
type SanitizeAnthropicToolsFunc func(body []byte) []byte

// NormalizeOpenAIToolsFunc normalizes tools[] in a chat body to nested
// OpenAI-Chat shape.
type NormalizeOpenAIToolsFunc func(body []byte) []byte

// StripMinimaxFieldsFunc strips minimax-private top-level fields from
// a chat response body before it is returned to the client.
// Wired from main.go (relay.StripMinimaxFieldsBody).
type StripMinimaxFieldsFunc func(body []byte) []byte

// XMLCoerceNonStreamFunc transforms a non-streaming chat response body,
// rewriting any `<tool_call><function=...>` XML embedded in assistant
// `content` into structured OpenAI `tool_calls` entries.  The second
// argument is true when the original request body supplied a `tools` array.
// Implementations are expected to be no-ops when toolsRequested is false.
type XMLCoerceNonStreamFunc func(body []byte, toolsRequested bool) []byte

// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
// QualityProcessNonStreamFunc is the per-provider tool_call quality
// post-processor. It returns the (possibly rewritten) body plus the
// four quality signals that the routing executor must propagate
// back into the ExecuteResult for emitTelemetry to persist on the
// request_log row.
//
// Implementations are expected to be no-ops when mode is "off" or
// empty (returning the original body and zero signals).
type QualityProcessNonStreamFunc func(body []byte, mode string) (outBody []byte, flags []string, fixActions []byte, score *float64)

// QualitySetModeFunc stamps the per-provider quality_fix_mode onto
// the upstream request context. relay/stream.go pulls it back out
// with qualityFixModeFromContext on every SSE line so the streaming
// path can run the same checks as the non-stream path.
type QualitySetModeFunc func(ctx context.Context, mode string) context.Context

// IRConverter is the unified protocol-conversion interface (Phase B, 2026-06-22).
// The dependency-free contract lives in internal/irconv so transformation and
// executors share the exact method types without an import cycle.
type IRConverter = irconv.Converter

// ProviderScoped is implemented by converters that isolate circuit-breaker
// state by provider. It is separate from IRConverter so stateless converters
// remain valid implementations.
type ProviderScoped = irconv.ScopedConverter

// RequestLogEmitter (2026-06-20) is the minimum interface needed by
// runAsyncRetry to update request_logs when a backgrounded retry
// eventually succeeds. Implemented by *telemetry.Client in
// production. Using an interface (rather than a direct
// *telemetry.Client field) so tests can inject a mock without
// needing a real database.
type RequestLogEmitter interface {
	Enabled() bool
	EmitRequestLogUpdate(entry *telemetry.RequestLogEntry)
}

type Executor struct {
	Router     *Router
	Circuit    *credential.Manager
	Limiter    *credential.Limiter
	Pools      *pool.PoolManager
	Upstream   *upstreampkg.Client
	Normalize  NormalizerFunc
	StreamChat StreamHandler
	// AttachmentURLRewriter (MM-1): when non-nil, the candidate loop swaps
	// inline base64 image blocks for gateway URLs on outbound bodies when
	// the target provider's attachment capability prefers URL references.
	// nil (default) keeps the legacy byte-for-byte outbound body.
	AttachmentURLRewriter *attachments.OutboundURLRewriter
	// AttachmentURLFetchFallback (MM-2, doc 19): when non-nil, the candidate
	// loop re-inlines gateway attachment URLs as base64 data URIs for
	// providers the URL-support matrix marks as unable to fetch URL sources.
	// nil (default, flag LLM_GATEWAY_ATTACHMENT_URL_FETCH_FALLBACK off)
	// keeps the legacy byte-for-byte outbound body.
	AttachmentURLFetchFallback *attachments.URLFetchFallback
	// dispatchPipeline (V2, 479): when non-nil AND dispatch_v2 gate is on,
	// Execute routes through the multi-tier dispatch pipeline instead of the
	// synchronous candidate loop. See executor_dispatch.go.
	dispatchPipeline *dispatch.Pipeline
	// traceRecorder (2026-07-17) 注入请求链路追踪器,记录 upstream_request /
	// stream_start 事件。nil 时降级为 NoopRecorder 等价。
	traceRecorder gwtrace.Recorder

	// liveActions (2026-08-15, V3.3-OBS OBS-B1) 请求生命周期动作事件发射器
	// （credential_selected / upstream_request / reply / node_switch /
	// model_switch / no_route，docs/会话优化v3/24 §2）。nil 安全（Emit 对
	// nil receiver 是 no-op）；旁路异步，热路径零阻塞。安全红线：Detail 只
	// 放 id/模型名/错误 kind，严禁正文 / API key / 系统 prompt。
	liveActions *liveactions.Emitter
	// XMLCoerceNonStream post-processes a non-stream chat response body to
	// turn XML-style tool calls into structured tool_calls. Wired from
	// main.go (relay.coerceXMLToolCallsInChatResponse) so the routing
	// package does not need to import relay.
	XMLCoerceNonStream XMLCoerceNonStreamFunc
	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
	// QualityProcessNonStream is the per-provider tool_call quality
	// post-processor for non-stream responses. Wired from main.go
	// (relay.WrapQualityProcessNonStream) so the routing package
	// does not need to import relay.
	QualityProcessNonStream QualityProcessNonStreamFunc
	// QualitySetMode stamps the per-provider mode onto the upstream
	// request context. relay/stream.go reads it via
	// qualityFixModeFromContext. Wired from main.go
	// (relay.SetQualityFixModeOnContext wrapped as a func).
	QualitySetMode QualitySetModeFunc
	// IR (Phase B, 2026-06-22) is the unified protocol converter.
	// When non-nil, the executor uses Parse→IR→Serialize instead of
	// the 6 scattered callbacks (ChatToAnthropic, AnthropicToOpenAI, etc).
	// When nil (default), falls back to existing callback wiring.
	IR IRConverter
	// AnthropicPassthroughStream is the Q4 Anthropic SSE forwarder with
	// side-channel audit capture. Wired from main.go (relay.StreamAnthropicPassthrough).
	AnthropicPassthroughStream AnthropicPassthroughFunc
	// ChatToAnthropic converts OpenAI chat body to Anthropic Messages
	// format. Used by executeAnthropic when ClientProtocol != "anthropic-messages".
	ChatToAnthropic ChatToAnthropicFunc
	// AnthropicToOpenAI converts Anthropic Messages body to OpenAI chat
	// format. Used by executeOpenAI when ClientProtocol == "anthropic-messages".
	AnthropicToOpenAI AnthropicToOpenAIFunc
	// AnthropicToOpenAIStream is the Q3 streaming counterpart of
	// AnthropicToOpenAI: reads Anthropic-format SSE upstream and writes
	// OpenAI-format SSE chunks to the client. Used by executeAnthropic
	// when ClientProtocol != "anthropic-messages" (openai client ->
	// anthropic upstream). Defaults to nil; when nil the Q3 stream
	// path falls back to PassthroughStream which the OpenAI client
	// can't parse.
	AnthropicToOpenAIStream AnthropicToOpenAISSEFunc
	// AnthropicToResponsesStream (Phase E, 2026-07-01) is the streaming
	// bridge from Anthropic SSE upstream to OpenAI Responses API SSE.
	// Used by executeAnthropic when ClientProtocol == "openai-responses".
	// Wired from main.go via streaming.StreamAnthropicSSEToResponses.
	AnthropicToResponsesStream AnthropicToResponsesSSEFunc
	// OpenAIToResponsesStream (Phase E, 2026-07-01) is the streaming
	// bridge from OpenAI chat.completion.chunk SSE upstream to OpenAI
	// Responses API SSE. Used by executeOpenAI when ClientProtocol ==
	// "openai-responses". Wired from main.go via
	// streaming.StreamOpenAIToResponsesSSE.
	OpenAIToResponsesStream OpenAIToResponsesSSEFunc
	// AnthropicToChatResponse is the Q3 non-stream counterpart:
	// converts an Anthropic Messages JSON body into an OpenAI
	// chat.completion JSON body. Used by executeAnthropic when
	// ClientProtocol != "anthropic-messages".
	AnthropicToChatResponse AnthropicToChatResponseFunc
	// ChatResponseToAnthropic is the Q2 non-stream response counterpart:
	// converts an OpenAI Chat Completions JSON body into an Anthropic
	// Messages JSON body. Used by executeOpenAI when
	// ClientProtocol == "anthropic-messages" AND the upstream is
	// OpenAI-shaped (cand.Protocol != "anthropic-messages"). Nil means
	// the legacy pre-fix behaviour applies (raw OpenAI body forwarded).
	// 2026-06-29: added to close Q2 row of the protocol conversion
	// matrix audit (docs/2026-06-29-protocol-conversion-matrix.md).
	ChatResponseToAnthropic ChatResponseToAnthropicFunc
	// OpenAIToAnthropicStream is the Q2 streaming response counterpart:
	// converts an OpenAI-format SSE upstream into an Anthropic-format
	// SSE stream for the client. Used by executeOpenAI when
	// ClientProtocol == "anthropic-messages" AND params.IsStream.
	// 2026-06-29: see ChatResponseToAnthropic.
	OpenAIToAnthropicStream OpenAIToAnthropicSSEFunc
	// SanitizeAnthropicTools strips invalid tool type fields from Anthropic
	// Messages bodies (Q3/Q4) before forwarding to minimax/anthropic upstream.
	SanitizeAnthropicTools SanitizeAnthropicToolsFunc
	// NormalizeOpenAITools coerces flat/anthropic tool defs to OpenAI-Chat shape.
	NormalizeOpenAITools NormalizeOpenAIToolsFunc
	// StripMinimaxFields strips minimax-private top-level fields
	// (nvext, base_resp, input_sensitive*, output_sensitive*) from
	// the chat response body before it is returned to the client.
	// Wired from main.go (relay.StripMinimaxFieldsBody).
	StripMinimaxFields StripMinimaxFieldsFunc
	// StripZhipuFields strips zhipu/GLM-private fields (zhipu_request_id,
	// web_search_results, retrieval_documents, etc.). Wired from main.go.
	// Audit-09 fix: 2026-07-12, preserves system_fingerprint and usage detail.
	StripZhipuFields func([]byte) []byte
	// StripDeepSeekFields strips deepseek-private fields (deepseek_request_id,
	// model_type, cache_hit_tokens, etc.). Wired from main.go.
	// Audit-09 fix: 2026-07-12, preserves reasoning_tokens and cache tokens.
	StripDeepSeekFields func([]byte) []byte
	// StripDoubaoFields strips doubao/volcengine-private fields (doubao_request_id,
	// seeddance_request_id, content_safety_score, etc.). Wired from main.go.
	// Audit-09 fix: 2026-07-12, preserves system_fingerprint.
	StripDoubaoFields func([]byte) []byte
	// RedactBodyFn (2026-07-09, 增强 1) write-time 客户端可见脱敏。
	// 在 w.Write 前调用，让客户端真正收到脱敏后字节。
	// 签名：func(body []byte, sessionID, tenantID string) []byte
	// Wired from main.go via streaming.BuildRedactBodyFn.
	RedactBodyFn func([]byte, string, string) []byte
	Auditor      audit.Sink
	State        *credential.Writer
	// Provider is the credential/candidate resolver. Typed as an interface
	// (defined in routing) so the compaction fallback tests can inject a
	// stub without standing up a real pgx pool. The concrete
	// *provider.Client satisfies the interface, so production wiring
	// is unchanged.
	Provider       providerResolver
	DB             *db.DB
	HeaderProfiles *HeaderProfileCache
	FpSlots        *credentialfpslot.Manager
	// IdentityPool is the Layer 0 global cap on distinct end-user fingerprints.
	// Wired from cmd/gateway/main.go. Nil disables the feature (unlimited identities).
	IdentityPool interface {
		Acquire(ctx context.Context, ident interface{}) (interface{}, bool, error)
		Enabled() bool
	}
	// PeakCollector records per-credential-model concurrency for the
	// auto-tune background worker. Nil disables the feature.
	PeakCollector interface {
		Acquire(credID int64, model string)
		Release(credID int64, model string)
	}
	// DisguisePool injects User-Agent / Accept-Language headers.
	// HeadersForSlot returns deterministic headers keyed by slot index
	// (same slot → same UA, every call). Headers is the random fallback
	// for stateless (no-slot) requests. Nil disables the feature.
	DisguisePool interface {
		Headers() map[string]string
		HeadersForSlot(slot int) map[string]string
		MaybeRotate()
	}

	StreamTimeout        time.Duration
	UpstreamTimeout      time.Duration
	StreamRetryThreshold int // Max chunks sent before stream becomes non-resumable (default 50)

	// KeepaliveInterval (Phase 3, 2026-07-23): Keepalive heartbeat interval in seconds
	// for long-running streaming requests. Loaded from system_settings or TimeoutConfig.
	// 0 disables keepalive. Default: 15 seconds.
	KeepaliveInterval int

	// MnfStreak tracks consecutive model_not_found occurrences per
	// (stickyKey, credentialID). When the count reaches
	// MnfStickyBreakThreshold, the sticky binding is deleted so the
	// next request re-picks. This is the client hot-path complement
	// to the background 3-strike consensus in bg/model_probe.go: the
	// background probe is authoritative for credential health, but
	// the streak breaker protects the user from being pinned to a
	// broken credential for the full 30-minute sticky TTL when the
	// upstream has clearly gone away. Nil disables the feature
	// (production default: enabled; tests may omit).
	MnfStreak               *MnfStreak
	MnfStickyBreakThreshold int  // default 3
	MnfStreakEnabled        bool // feature flag (env-gated by main.go)

	// BUG-4 fix (2026-06-18): mnf_cooling temporarily disables a
	// credential_model_binding when it accumulates too many
	// model_not_found errors in a short window. This prevents a
	// 0%-success credential from being repeatedly selected when
	// it's the only routable candidate.
	MnfCoolThreshold int // default 5, env LLM_GATEWAY_MNF_COOL_THRESHOLD
	MnfCoolMinutes   int // default 2, env LLM_GATEWAY_MNF_COOL_MINUTES

	// Round 47 compression v7 T16: the unified compression dispatcher
	// (mode=0/1/2). Built at startup by main.go from
	// LLM_GATEWAY_COMPRESSION_MODE + LLM_GATEWAY_COMPRESSION_WINDOW_FRACTION
	// env. Nil is allowed (treated as ModeOff) for tests and unconfigured
	// single-tenant installs.
	Compressor *compression.Compressor

	// HealthTracker records call history for intelligent availability
	// tracking, continuous failure detection, and concurrency auto-tuning.
	// Nil disables the feature.
	HealthTracker *HealthTracker

	// 2026-06-23 Phase 2 (P1): per-candidate failure logger. Writes one
	// row to candidate_failure_logs per failed (request_id, credential,
	// model, attempt_index) tuple so operators can see WHICH credentials
	// are failing in a sequence (the request_logs row only carries the
	// LAST candidate's error, leaving the others invisible). Nil disables
	// the feature — the executor's failure log calls become no-ops.
	FailureLogger *CandidateFailureWriter

	// Memora is the optional context-compression oracle. When non-nil
	// and enabled, the executor (a) enqueues per-request writes to
	// Memora for later retrieval, and (b) on context-length overflow,
	// queries Memora for L1 session facts and rebuilds the body before
	// retrying. Nil means the entire Memora path is a no-op.
	Memora memory.Reader
	// MemoraSink is the async write buffer for Memora persistence.
	// When nil (or when Memora is disabled), enqueue calls are no-ops.
	// Wired from main.go alongside Memora; the sink owns its own worker
	// goroutines and graceful-shutdown lifecycle.
	MemoraSink memory.Writer

	// PendingStore (Track C, 2026-06-18) is the durable cache for
	// client reconnect and vendor async retry. When set, the
	// executor can transparently demote a slow request to async
	// mode: if the synchronous candidate walk exceeds
	// AsyncShortTimeout (default 15s) without success, the
	// executor spawns a goroutine that continues trying the
	// remaining candidates with an independent context
	// (AsyncLongTimeout, default 300s), writes the eventual
	// outcome to PendingStore, and the handler returns 202 +
	// X-Gw-Pending so the client can poll
	// GET /v1/sessions/{id}/pending-response.
	//
	// Nil disables both async retry and the async branch — the
	// executor falls back to the existing synchronous exhaustion
	// path. Wired from main.go when Redis is available.
	PendingStore          *pending.Store
	AsyncShortTimeout     time.Duration
	AsyncLongTimeout      time.Duration
	AsyncMaxFallbackCreds int // cap on credential fallbacks in async goroutine

	// AsyncRetrySem (2026-07-19, Phase 1 success-rate priority) limits
	// concurrent async retry goroutines. When the semaphore is full,
	// startAsyncRetry returns nil (no async) and the request falls back
	// to synchronous exhaustion. Default 200 when non-nil. Nil = unlimited
	// (preserves the pre-Phase-1 behaviour).
	AsyncRetrySem chan struct{}

	// ModelFallbackChain (Phase 2, 2026-07-19) maps a client-requested
	// model name to fallback model names from other providers. When all
	// candidates for the primary model fail, the executor fetches
	// candidates for each fallback model and retries transparently.
	// Example:
	//
	//	"claude-sonnet-4-20250514" → ["gpt-4o-2024-11-20", "gemini-2.0-flash-001"]
	//
	// Empty map (or nil) disables cross-provider fallback. The keys and
	// values must be client model names as they appear in the request
	// "model" field (after CanonicalizeClientModel). Wired from main.go
	// via env LLM_GATEWAY_MODEL_FALLBACK (format: "primary=fb1,fb2;...").
	ModelFallbackChain map[string][]string

	// RequestLogEmitter (2026-06-20): optional hook that runAsyncRetry
	// calls when a backgrounded retry succeeds, so the original
	// request_logs row (stuck at "in_progress" because the sync phase
	// did not call emitTelemetry) gets corrected to "success".
	//
	// Without this hook, async-retry success leaves request_logs in
	// its post-sync state, and operators see "model_not_found" /
	// "in_progress" for requests that actually completed via the
	// async path. Wired from cmd/gateway/main.go after telemetryClient
	// is created. Nil is safe (preserves the pre-fix behavior).
	RequestLogEmitter RequestLogEmitter

	// SyncRetryTimeout (2026-06-21): when ALL candidates fail for a
	// non-streaming request, the executor keeps retrying candidates
	// synchronously for this duration before returning an error to
	// the client. During this period the HTTP connection is held
	// open, the client's context is respected (disconnect aborts the
	// loop via ctx.Done()), and the async fallback path is NOT
	// started. This prevents wasting tokens on retries that the
	// client can no longer consume.
	//
	// Default 120s (set in cmd/gateway/main.go). <= 0 disables sync
	// retry and preserves the old behavior (immediate async fallback
	// or synchronous exhaustion).
	SyncRetryTimeout time.Duration

	// SyncNoCandidateProbe (2026-07-17): when the router returns zero
	// candidates, the executor holds the request goroutine up to
	// SyncNoCandidateTimeout and asks ProbeSync to fan out parallel
	// (cred,model) direct probes. If at least one pair recovers the
	// executor re-plans and retries the user's request transparently.
	// Disable (false) to preserve the legacy "fire-and-forget + 503"
	// behaviour for emergency rollback. Wired from main.go via env
	// LLM_GATEWAY_SYNC_NO_CANDIDATE_PROBE (default true).
	SyncNoCandidateProbe   bool
	SyncNoCandidateTimeout time.Duration

	// ProbeSync is the synchronous probe entry-point invoked from the
	// no-candidate branch. Wired from main.go to bg.NodeProbeWorker.
	// Nil disables the feature even if SyncNoCandidateProbe is true.
	ProbeSync ProbeSyncFunc

	// NodeProbeHealthy is called on the success path of a real business
	// request to clear node_probe_state.last_direct_ok=FALSE, so the
	// v_routable_credential_models view does not keep excluding the
	// credential after transient probe timeouts.
	// Wired from main.go to bg.MarkNodeProbeHealthy.
	// Nil is safe — the call is skipped.
	NodeProbeHealthy NodeProbeHealthyFunc

	// asyncDepth is the recursion guard (Track C C4). The async
	// goroutine (runAsyncRetry) calls Execute again; we bump this
	// so shouldAsyncFallback returns false on the inner call.
	// Uses atomic.Int32 because the Executor is a singleton
	// shared across all request goroutines — a plain int would
	// race under concurrent requests.
	asyncDepth atomic.Int32

	// ProviderSettings (Phase 3.2, 2026-06-21): resolver for provider-level
	// setting overrides. When non-nil, the executor checks provider-specific
	// compression.mode, cache.enabled, and format_conversion.enabled before
	// applying global defaults. Wired from main.go.
	ProviderSettings interface {
		GetString(ctx context.Context, providerID int, key string) (string, bool)
		GetBool(ctx context.Context, providerID int, key string) (bool, bool)
		GetInt64(ctx context.Context, providerID int, key string) (int64, bool)
	}

	// RecoveryCoord (v5, 2026-06-25): session-aware smart recovery coordinator.
	// When non-nil, context_length_exceeded 4xx recovery uses the smart sliding
	// window analyzer + session cache integration (incremental compression).
	// When nil, falls back to the legacy 3-tier recovery (mechanical → memora → llm).
	RecoveryCoord *compression.RecoveryCoordinator
	// RouteNodeRecorder records per-(credential, model) health outcomes.
	// Nil disables route-node health recording.
	Recorder RouteNodeRecorder

	// UnifiedProbeScheduler (2026-06-28): intelligent probe scheduler that
	// maintains accurate state for all credential×model combinations.
	// When non-nil, real-time request feedback is sent via OnRealRequest
	// to enable <30s failure detection and adaptive health tracking.
	// Nil disables real-time feedback (preserves legacy behavior).
	UnifiedProbeScheduler interface {
		OnRealRequest(ctx context.Context, credID int64, rawModel string, success bool, errMsg string)
	}

	// StateObserver (2026-07-01 Phase 2.x): credential state manager that
	// records real request outcomes (success/failure) and triggers adaptive
	// probing. When non-nil, UpdateOnSuccess/UpdateOnFailure are called with
	// classified error kinds. KindCanceled (user cancellation) is automatically
	// skipped by the manager to avoid false positives.
	// Nil disables credential state tracking (preserves legacy behavior).
	//
	// 2026-07-14: OnNoCandidates fans out per-candidate probes when the
	// router returns zero available nodes, closing the gap where
	// "no_candidate" failures produced no follow-up probe history
	// (minimax-m3 incident).
	StateObserver interface {
		UpdateOnSuccess(ctx context.Context, credID int, model string, latencyMs int, requestID string)
		UpdateOnFailure(ctx context.Context, credID int, model string, errKind errorsx.ErrorKind, requestID, tenantID, billingMode string)
		OnNoCandidates(ctx context.Context, sig credentialstate.NoCandidatesSignal)
	}

	// RoutingStateShadow observes the same request facts as StateObserver.
	// It is shadow-only: it never writes state, invalidates candidate caches,
	// or dispatches probes. Nil preserves all existing routing behaviour.
	RoutingStateShadow interface {
		ObserveState(evidence routingstate.Evidence)
		ObserveProbe(task routingstate.ProbeTask)
	}

	// URSMv2 (2026-07-21, URSM v2 plan T8): 旁路写新管线，影子/金丝雀模式下
	// 通过 RecordRequest 把结果回写到 v2 store，**不影响**任何现有决策路径。
	// 当 URSMv2 == nil、mode=off，或 shadow 未开启 double-write 时
	// Manager.RecordRequest 内部短路；本字段 nil 即保留所有旧行为。
	//
	// 2026-07-26 URSM v1→v2 统一: 旧字段 URSM (v1, domains/ursm.Manager) 已删除。
	// v1 在 main.go 中从未 wire，运行时恒为 nil。executor 唯一的请求状态写入
	// 入口是 URSMv2；authoritative 模式下它是唯一权威源。
	URSMv2 *ursmv2.Manager

	// DegradationTracker (2026-07-07 Phase 1): 追踪 FpSlot 降级模式请求
	// 用于监控和告警。Nil 时禁用该功能。
	DegradationTracker *DegradationTracker

	// 2026-07-16: 自适应超时与状态管理增强（使用接口避免循环依赖）
	TimeoutAdapter      TimeoutCalculator // 自适应超时计算器
	TTFBTracker         TTFBRecorder      // TTFB 历史追踪器
	PreRequestValidator RequestValidator  // 请求格式校验器
	PostExecutionHook   ExecutionRecorder // 执行后状态更新 hook

	// 2026-07-26: 诊断与监控组件（opt-in via env vars）
	// 这些组件在 Executor 初始化时根据环境变量创建，用于记录原始数据、
	// 异常和语义分析结果。Nil 时禁用对应功能。
	RawDataLogger    RawDataLogger    // 原始请求/响应数据记录器
	AnomalyReporter  AnomalyReporter  // 协议转换异常报告器
	SemanticAnalyzer SemanticAnalyzer // 语义分析器（工具调用/内容丢失检测）

	// PredictiveTTFBSkipper implements the default-off O-1 pre-skip policy.
	// It is checked at most once per Execute call and only when more than one
	// routed candidate remains, so the last candidate is always given a chance.
	PredictiveTTFBSkipper PredictiveTTFBSkipper

	// 2026-07-28: 模型质量探测（per-request 完整性事件）。
	// 由 main.go 注入；nil 时不调用任何完整性检测（旧行为）。
	// 该字段是接口而不是具体类型，executors 包不依赖 integrity 子包，
	// 注入的实例在底层是 *integrity.Detector 包装。
	IntegrityDetector IntegrityDetector

	// 2026-07-27 (M3, S-4): per-Executor TTL cache for the URSMv2
	// authoritative Ready() check. legacyWritersEnabled() is called 11+× per
	// request; without this cache each call did a 10ms Redis round-trip.
	// Ready() changes at most once per boot/recovery, so a 1s TTL is safe.
	// Guarded by authCacheMu (the Executor is shared across goroutines).
	authCacheMu  sync.Mutex
	authCacheVal bool
	authCacheAt  time.Time
}

// DefaultFallbackChain (Phase 2, 2026-07-19) returns a sensible default
// cross-provider model fallback map. These mappings cover the most commonly
// used models. The chain is opt-out — unset env means "use defaults".
func DefaultFallbackChain() map[string][]string {
	return map[string][]string{
		// Anthropic → OpenAI
		"claude-sonnet-4-20250514": {"gpt-4o-2024-11-20"},
		"claude-sonnet-4":          {"gpt-4o"},
		"claude-haiku-3-20240307":  {"gpt-4o-mini-2024-07-18"},
		"claude-haiku-3":           {"gpt-4o-mini"},
		"claude-opus-4-20250514":   {"gpt-4o-2024-11-20"},
		"claude-opus-4":            {"gpt-4o"},
		// OpenAI → Anthropic
		"gpt-4o-2024-11-20":      {"claude-sonnet-4-20250514"},
		"gpt-4o":                 {"claude-sonnet-4"},
		"gpt-4o-mini-2024-07-18": {"claude-haiku-3-20240307"},
		"gpt-4o-mini":            {"claude-haiku-3"},
		// DeepSeek → OpenAI (cheaper model → capable model, cost trade-off accepted)
		"deepseek-chat":     {"gpt-4o-mini"},
		"deepseek-reasoner": {"gpt-4o"},
	}
}

func NewExecutor(
	router *Router,
	cm *credential.Manager,
	lim *credential.Limiter,
	pools *pool.PoolManager,
	upstream *upstreampkg.Client,
	normalize NormalizerFunc,
	streamChat StreamHandler,
	auditor audit.Sink,
) *Executor {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	if normalize == nil {
		normalize = func(chunk []byte, isStream bool) []byte { return chunk }
	}
	return &Executor{
		Router:               router,
		Circuit:              cm,
		Limiter:              lim,
		Pools:                pools,
		Upstream:             upstream,
		Normalize:            normalize,
		StreamChat:           streamChat,
		Auditor:              auditor,
		StreamTimeout:        900 * time.Second,
		UpstreamTimeout:      120 * time.Second,
		StreamRetryThreshold: 50, // Default: allow stream failover if < 50 chunks sent
	}
}

func diagnosticRequestID(params *ExecParams) string {
	if params == nil {
		return ""
	}
	if params.RequestID != "" {
		return params.RequestID
	}
	if params.R != nil {
		return params.R.Header.Get("X-Request-Id")
	}
	return ""
}

func diagnosticProtocol(protocol, fallback string) string {
	if protocol != "" {
		return protocol
	}
	return fallback
}

// extractClientType (2026-07-27 Token 资源管理)
//
// 此函数是 streaming/client_fingerprint.go:extractClientType 的本地简化版，
// 仅覆盖 FpSlot holder 拼接所需的"header-only"路径（X-Gw-Client-Type 头
// + User-Agent 关键词匹配）。系统提示词语义补全留给 streaming.extractClientTypeWithPrompt。
//
// 之所以在 executors 包内重复一份：executor.go 历史上不 import streaming 父包
// （避免循环依赖，保持包边界单向）。两份实现必须保持行为一致：未识别时返回空串，
// 由调用方 clientTokenOf 兜底为 "unknown"。
func extractClientType(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ct := r.Header.Get("X-Gw-Client-Type"); ct != "" {
		return strings.ToLower(ct)
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "cursor/"), strings.Contains(ua, "cursor-"):
		return "cursor"
	case strings.Contains(ua, "claude-code/"), strings.Contains(ua, "claude-code-"):
		return "claude-code"
	case strings.Contains(ua, "opencode/"), strings.Contains(ua, "opencode-"):
		return "opencode"
	case strings.Contains(ua, "zcode/"), strings.Contains(ua, "zcode-"):
		return "zcode"
	case strings.Contains(ua, "codex/"), strings.Contains(ua, "codex-"):
		return "codex"
	case strings.Contains(ua, "roocode/"), strings.Contains(ua, "roo-code/"):
		return "roocode"
	case strings.Contains(ua, "vscode/"), strings.Contains(ua, "visual-studio-code/"):
		return "vscode"
	case strings.Contains(ua, "github-copilot/"), strings.Contains(ua, "copilot/"):
		return "copilot"
	case strings.Contains(ua, "windsurf/"):
		return "windsurf"
	case strings.Contains(ua, "zed/"):
		return "zed"
	case strings.Contains(ua, "jetbrains/"), strings.Contains(ua, "intellij/"),
		strings.Contains(ua, "pycharm/"), strings.Contains(ua, "webstorm/"):
		return "jetbrains"
	}
	return ""
}

// clientTokenOf (2026-07-27 Token 资源管理)
//
// 把 userKey 与 clientType 拼接为客户端 token，作为 FpSlot 与 Pin 的 holder。
//   - userKey 空 → "anon"
//   - clientType 空 → "unknown"
//
// 与 streaming/client_fingerprint.go:ClientTokenOf 行为一致；本地重复一份
// 是为了保持 executors 包零外部依赖。维护注意：两份逻辑需同步演进。
func clientTokenOf(userKey, clientType string) string {
	if userKey == "" {
		userKey = "anon"
	}
	if clientType == "" {
		clientType = "unknown"
	}
	return userKey + "|" + clientType
}

func clientTokenForMetrics(params *ExecParams) string {
	userKey := params.StickyKey
	if userKey == "" && params.R != nil {
		userKey = params.R.Header.Get("X-Request-Id")
	}
	clientType := extractClientType(params.R)
	return clientTokenOf(userKey, clientType)
}

func (e *Executor) logUpstreamRequest(params *ExecParams, protocol string, body []byte) {
	if e.RawDataLogger == nil {
		return
	}
	if aware, ok := e.RawDataLogger.(envelopeAwareUpstreamRequestLogger); ok {
		aware.LogUpstreamRequestWithEnvelope(diagnosticRequestID(params), protocol, body, "post_conversion", envelopeFromParams(params))
		return
	}
	logger, ok := e.RawDataLogger.(UpstreamRequestLogger)
	if !ok || logger == nil {
		return
	}
	if err := logger.LogUpstreamRequest(diagnosticRequestID(params), protocol, body); err != nil {
		slog.Warn("executor diagnostics: upstream request logging failed", "request_id", diagnosticRequestID(params), "error", err)
	}
}

func (e *Executor) logUpstreamResponse(params *ExecParams, protocol string, body []byte) {
	if e.RawDataLogger == nil {
		return
	}
	if aware, ok := e.RawDataLogger.(envelopeAwareUpstreamResponseLogger); ok {
		aware.LogUpstreamResponseWithEnvelope(diagnosticRequestID(params), protocol, body, "post_conversion", envelopeFromParams(params))
		return
	}
	if err := e.RawDataLogger.LogResponse(diagnosticRequestID(params), protocol, body, false); err != nil {
		slog.Warn("executor diagnostics: upstream response logging failed", "request_id", diagnosticRequestID(params), "error", err)
	}
}

func (e *Executor) logClientResponse(params *ExecParams, protocol string, body []byte) {
	if e.RawDataLogger == nil {
		return
	}
	if aware, ok := e.RawDataLogger.(envelopeAwareClientResponseLogger); ok {
		aware.LogClientResponseWithEnvelope(diagnosticRequestID(params), protocol, body, "post_conversion", envelopeFromParams(params))
		return
	}
	logger, ok := e.RawDataLogger.(ClientResponseLogger)
	if !ok || logger == nil {
		return
	}
	if err := logger.LogClientResponse(diagnosticRequestID(params), protocol, body); err != nil {
		slog.Warn("executor diagnostics: client response logging failed", "request_id", diagnosticRequestID(params), "error", err)
	}
}

type ExecParams struct {
	W         http.ResponseWriter
	R         *http.Request
	BodyBytes []byte
	IsStream  bool
	// PreStreamPrepared means the caller already committed a 200
	// text/event-stream response and may be emitting keep-alive comments
	// while the executor is still retrying upstream credentials. In this
	// mode Execute must not switch to JSON/202 fallback semantics.
	PreStreamPrepared bool
	// SurvivalAttempt (SR-05, doc 18 §17 Phase 0): the SurvivalCoordinator
	// owns this execution. Execute performs a single bounded pass — the
	// no-candidate probe hold, single-node 5xx re-execute, sync retry loop,
	// cross-provider model fallback and async fallback are all suppressed;
	// failure returns the aggregated ExecuteError and the coordinator decides
	// retry-now / wait-recovery / terminal. Copied by value into sub-Execute
	// calls so no nested path can re-enable them.
	SurvivalAttempt bool
	// OnStreamReady is called exactly once right before the executor hands
	// control to the normal stream writer. The caller uses it to stop any
	// pre-stream keepalive goroutine so no writes race with StreamChat.
	OnStreamReady func()
	// OnStreamStarted is called once when the upstream stream is ready, before
	// the shared protocol-specific stream writer starts writing bytes.
	OnStreamStarted func(ttfbMs int)
	// OnStreamCompleted is called after the stream writer returns with its
	// aggregate outcome, regardless of protocol bridge.
	OnStreamCompleted func(outcome StreamOutcome)
	// OnProbeHoldStart is invoked when the executor enters the synchronous
	// no-candidate hold. The handler wires this to its RequestLogContext so
	// trace/log entries record the probe_hold_start event. Optional.
	OnProbeHoldStart func()
	// OnProbeHoldEnd is invoked when the synchronous probe finishes, with
	// recovered=true iff at least one (cred,model) recovered. The handler
	// uses this to record probe_hold_end + duration in trace/log. Optional.
	OnProbeHoldEnd func(recovered bool)
	// OnPreStreamKeepalivePause is invoked when the executor enters the
	// probe hold AND params.PreStreamPrepared is true. The handler uses
	// this to suspend the keepalive SSE comment goroutine so the client
	// does not interpret a stale comment as a response-start signal during
	// the hold. The keepalive goroutine resumes naturally when stream
	// writing begins (or is stopped via OnStreamReady). Optional.
	OnPreStreamKeepalivePause func()
	// OnNodeJump is invoked when the executor is switching to the next
	// credential after a failure. The handler uses this to send a thinking
	// event (SSE event: thinking) to the client, displaying node failover
	// status without entering the conversation. Optional.
	OnNodeJump           func(message string)
	SuppressSuccessWrite bool
	// AttachmentMetadata carries the extractor's stored-attachment records
	// (MM-1) so the executor can swap inline base64 blocks for gateway URLs
	// per outbound candidate. nil on the legacy path (feature off).
	AttachmentMetadata []attachments.AttachmentMetadata
	ClientModel        string
	OutboundModel      string
	ClientID           identity.ClientIdentity
	Transform          *transformation.TransformResult
	Resolution         *resolve.Resolution
	Candidates         []provider.Candidate
	Policy             *provider.Policy
	AuditBuilder       *audit.EventBuilder
	Capture            *audit.StreamCapture
	StreamWrapper      StreamWrapperFunc
	// ToolsRequested indicates the upstream request body carried a non-empty
	// `tools` array. Some providers (Xiaomi MiMo, MiniMax M2.7) cannot emit
	// structured `tool_calls` and instead fall back to embedding
	// `<tool_call><function=...>...</tool_call>` XML inside the assistant
	// `content`. With this flag set, the stream and non-stream response
	// post-processors will coerce the XML into real `tool_calls` entries so
	// downstream agents (which only inspect the structured field) recognise
	// the call and dispatch the tool.
	ToolsRequested bool
	// ClientProtocol is the wire format the client used: "openai-completions"
	// for /v1/chat/completions, "anthropic-messages" for /v1/messages.
	// Empty defaults to "openai-completions". Used by executeAnthropic to
	// decide whether the body needs Q3 conversion (openai->anthropic).
	ClientProtocol string
	SessionKey     string
	StickyKey      string
	// SessionID is the X-Gw-Session-Id from the request (may be empty for non-session requests).
	// 2026-06-25: Used by multi-level sticky routing (L1: session+model).
	SessionID string
	// Model is the client-requested model name (after alias resolution).
	// 2026-06-25: Used by multi-level sticky routing (L2: client+model).
	Model string
	// KeyID is the API key ID from keyInfo.ID. Used for per-key concurrent limiting.
	KeyID int
	// KeyConcurrentLimit is the per-key concurrent limit from keyInfo.EffectiveConcurrent().
	// If 0, the per-key concurrent check is skipped.
	KeyConcurrentLimit int
	// TenantID is the tenant owning this request (from keyInfo.TenantID).
	// Round 47 (2026-06-18) compression v7 T13: used by enqueueMemoraWrite /
	// tryMemoraCompression to namespace Memora user_ids by tenant (per docs/
	// multi-tenant-standards.md §3.2 Pattern A). Empty means single-tenant
	// mode (legacy "default" tenant); the Memora user_id falls back to the
	// pre-v7 "k:<api_key_id>:<task_id>" format so existing tests stay green.
	TenantID string
	// RequestID is the per-request id used in request_logs.request_id and
	// surfaced on the dashboard swim lane. 2026-07-14: required so the
	// no-candidates fallback can hand it to ActiveProbeWorker as the
	// probe's parent_request_id — without this the live-stream probe row
	// would have no way to correlate with the failed business request.
	// The handler passes the same value it uses for request_logs insert;
	// executor does not generate one itself.
	RequestID string
	// clientTokenMetricsOwner marks the outermost Execute call. Recursive
	// failover/probe calls share the same request and must not double-count it.
	clientTokenMetricsOwner bool
	// AppID is the application ID from keyInfo.ApplicationID.
	// 2026-07-07: Used by multi-level sticky routing (L1/L2/L3).
	AppID *int
	// ApiKeyID is the API key ID from keyInfo.ID (same as KeyID but as pointer).
	// 2026-07-07: Used by multi-level sticky routing (L1/L2/L3).
	ApiKeyID *int

	// DispatchModelAlternatives carries request-scoped model fallback candidates
	// computed before Execute (for example auto-route CandidatesTop3 excluding
	// the chosen model). Dispatch V2 may use them only when
	// DispatchAllowModelChange is true.
	DispatchModelAlternatives []string
	DispatchAllowModelChange  bool
	// DispatchRequestModality is the modality detected by the handler when it
	// resolved the initial candidates. Dispatch V2 reuses it when lazily resolving
	// candidates for an alternate model.
	DispatchRequestModality string
	// DispatchAllowProviderChange permits dispatch V2 to leave the provider of
	// the first selected credential. When false it may still switch credentials
	// within the same provider.
	DispatchAllowProviderChange bool

	// PinCredentialID (2026-08-13, 需求 6 bullet 5) forces routing to a specific
	// credential. Set ONLY by trusted internal callers (the OriginMiddleware
	// strips X-LLM-Pin-Credential from non-system requests), used by self-check /
	// node-probe so a probe attributes its outcome to the exact (credential,
	// model) under test instead of letting the router pick any routable node.
	// Honored as a HARD filter in dispatchRoute and as the sticky source in the
	// legacy synchronous loop.
	PinCredentialID *int

	// InFallback (Phase 2, 2026-07-19) prevents infinite recursion when
	// the cross-provider model fallback chain triggers a recursive call
	// to Execute(). Set true before the recursive call so the inner
	// invocation skips its own fallback chain lookup.
	InFallback bool

	// RoutingTracker (2026-07-19) records每次 upstream 尝试的详情，用于
	// 解决用户困惑"为什么供应商泳道显示 A 但 upstream URL 显示 B"。
	// 在 handler.go 中创建，在 executor_chat.go 中填充，在 telemetry
	// 中写入 request_logs_hot.routing_attempts。可选，nil 表示不追踪。
	RoutingTracker *RoutingAttemptsTracker

	// 2026-07-28 §5.1: per-attempt correlation fields. Populated by
	// the handler when AuditContext is built, refreshed by the
	// executor when each candidate credential is selected. Used by
	// envelopeFromParams to populate RawCorrelationEnvelope for the
	// raw log writer.
	ClientRequestID  string
	GWTaskID         string
	ParentRequestID  string
	ProviderID       int
	CredentialID     int
	AttemptNo        int
	UpstreamEndpoint string
	TraceID          string
	SpanID           string
	// Audit (2026-07-28 §5.7) is the full AuditContext handle. When
	// non-nil, envelopeFromParams delegates to it (and ignores the
	// flat fields above). When nil, the flat fields above are used.
	// The handler always sets Audit; tests and async-retry paths that
	// construct ExecParams directly may set only the flat fields.
	Audit *AuditContext

	diagnosticsLogged bool
}

// discardResponseWriter is a write-only sink used when there is no live
// client to write to (async retry goroutine — see startAsyncRetry).
//
// 并发修复 2026-07-27：异步重试 goroutine 里 ExecParams.W 为 nil，但
// 下游的流式/非流式写函数（StreamChat / StreamResponse /
// WriteNonStreamResponse / 各 protocol bridge）都无条件解引用 writer。
// 与其在每个调用点分支，不如给它们一个丢弃 writer：upstream body 仍会
// 被完整读出并写入 params.Capture（PendingStore 依赖它拿到 body），
// 字节则直接丢掉。实现 http.Flusher 是必需的 —— 所有 SSE 写函数都做
// `w.(http.Flusher)` 断言，缺了它会走降级/报错分支。
type discardResponseWriter struct {
	header http.Header
}

func (d *discardResponseWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}

func (d *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardResponseWriter) WriteHeader(int)             {}
func (d *discardResponseWriter) Flush()                      {}

// responseSink returns the writer downstream write paths should use.
// It is params.W for a normal request-scoped call, or a discard writer
// when W is nil (async retry goroutine). Never returns nil, so callers
// can pass the result straight into the stream/response writers without
// a nil check.
func responseSink(params *ExecParams) http.ResponseWriter {
	if params != nil && params.W != nil {
		return params.W
	}
	return &discardResponseWriter{}
}

// SetTraceRecorder (2026-07-17) 注入请求链路追踪器,
// 用于记录 upstream_request / stream_start 等阶段事件。
func (e *Executor) SetTraceRecorder(rec gwtrace.Recorder) {
	if e != nil {
		e.traceRecorder = rec
	}
}

// SetLiveActions (2026-08-15, V3.3-OBS OBS-B1) 注入请求生命周期动作事件
// 发射器。传 nil 等价于禁用（Emit 对 nil receiver 是 no-op）。
func (e *Executor) SetLiveActions(em *liveactions.Emitter) {
	if e != nil {
		e.liveActions = em
	}
}

// DEPRECATED: isURSMv2Authoritative 将被 StateBackend 接口替代。
// 2026-07-24 Phase 1: 此方法已被 selectStateBackend() 统一入口替代。
// 每次调用都会触发 10ms 的 Ready() 检查，在热路径中造成不必要的开销。
// 保留用于向后兼容，新代码应使用 StateBackend.IsAuthoritative()。
//
// isURSMv2Authoritative 检查 URSM v2 是否处于 authoritative 模式且已 Ready。
// 用于在 authoritative 模式下跳过旧的状态管理系统，避免状态不一致。
//
// 2026-07-27 (M3, S-4): the Ready() result is now cached per-Executor for
// 1s (authoritativeCacheTTL). Ready() reflects whether the v2 pipeline has
// warmed up against Redis — a value that changes at most once per boot/
// recovery, never per-request — so a 1s TTL is safe and cuts the 10ms Redis
// round-trip from ~11×/request to ~1×/s. The cache is only consulted when
// Mode==Authoritative; in the default off/canary modes Mode() short-circuits
// before any Redis call.
func (e *Executor) isURSMv2Authoritative() bool {
	if e == nil || e.URSMv2 == nil {
		return false
	}
	if e.URSMv2.Mode() != ursmv2api.ModeAuthoritative {
		return false
	}
	// Per-instance TTL cache (safe across goroutines via the mutex below).
	e.authCacheMu.Lock()
	defer e.authCacheMu.Unlock()
	if time.Since(e.authCacheAt) < authoritativeCacheTTL {
		return e.authCacheVal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	v := e.URSMv2.Ready(ctx)
	e.authCacheVal = v
	e.authCacheAt = time.Now()
	return v
}

// authoritativeCacheTTL bounds how long a cached Ready() result is trusted.
// Ready flips at most once per boot/recovery, so 1s is conservative.
const authoritativeCacheTTL = 1 * time.Second

// legacyWritersEnabled 返回"是否应执行旧的状态写入路径"（FpSlots Recorder /
// credentialstate Observer / routingstate Shadow）。当 URSM v2 authoritative
// 模式生效时返回 false——这些旧路径会跳过，由 e.URSMv2.RecordRequest 接管。
//
// 2026-07-26 URSM v1→v2 统一：v1 (domains/ursm.Manager) 已迁入
// _to-be-deprecated/ursm/，executor 不再保留任何 v1 写入路径。剩下的
// "legacy writers"是 FpSlots/credentialstate/routingstate 这三套并行系统，
// 仅在 v2 非 authoritative 时参与状态同步。
//
// 2026-07-27 (M3, S-4): the 10ms Ready() cost is now 1s-TTL-cached inside
// isURSMv2Authoritative, so the 11+ hot-path calls per request collapse to
// ~1 Redis round-trip per second total.
func (e *Executor) legacyWritersEnabled() bool {
	return !e.isURSMv2Authoritative()
}

// emitTraceExec 是 Executor 内部使用的 trace 注入薄包装,避免热路径
// 重复写 nil-check。
func (e *Executor) emitTraceExec(ctx context.Context, requestID string, ev gwtrace.EventBuilder) {
	if e == nil || e.traceRecorder == nil || requestID == "" {
		return
	}
	ev.Append(ctx, e.traceRecorder, requestID)
}

func (e *Executor) stripVendorFields(body []byte, catalogCode string) []byte {
	// 2026-07-20 fix: Guard against empty/invalid catalogCode to prevent panic.
	// Third-party providers (provider_id 36/314/5917) have empty catalog_code,
	// causing nil pointer dereference when passed as empty string.
	if catalogCode == "" && len(body) == 0 {
		return body
	}

	code := strings.ToLower(strings.TrimSpace(catalogCode))

	// 2026-07-20: When catalog_code is empty (third-party providers),
	// auto-detect vendor fields in the response body to prevent downstream
	// parsing errors. This fixes 5xx errors for gpt-5.2/gpt-5.6-luna/Minimax-m3
	// from providers without catalog_code.
	if code == "" && len(body) > 0 {
		// Auto-detect minimax fields (nvext, base_resp, etc.)
		if bytes.Contains(body, []byte(`"nvext"`)) ||
			bytes.Contains(body, []byte(`"base_resp"`)) ||
			bytes.Contains(body, []byte(`"input_sensitive"`)) {
			if e.StripMinimaxFields != nil {
				stripped := e.StripMinimaxFields(body)
				slog.Info("stripVendorFields: auto-detected minimax fields",
					"catalog_code", catalogCode,
					"original_bytes", len(body),
					"stripped_bytes", len(stripped))
				return stripped
			} else {
				slog.Warn("stripVendorFields: detected minimax fields but StripMinimaxFields is nil",
					"catalog_code", catalogCode,
					"body_preview", string(body[:min(100, len(body))]))
			}
		}
		// Auto-detect zhipu fields (zhipu_request_id, web_search_results, etc.)
		if bytes.Contains(body, []byte(`"zhipu_request_id"`)) ||
			bytes.Contains(body, []byte(`"web_search_results"`)) {
			if e.StripZhipuFields != nil {
				stripped := e.StripZhipuFields(body)
				slog.Info("stripVendorFields: auto-detected zhipu fields",
					"catalog_code", catalogCode,
					"original_bytes", len(body),
					"stripped_bytes", len(stripped))
				return stripped
			} else {
				slog.Warn("stripVendorFields: detected zhipu fields but StripZhipuFields is nil",
					"catalog_code", catalogCode,
					"body_preview", string(body[:min(100, len(body))]))
			}
		}
		// Auto-detect deepseek fields (deepseek_request_id, model_type, etc.)
		if bytes.Contains(body, []byte(`"deepseek_request_id"`)) ||
			bytes.Contains(body, []byte(`"cache_hit_tokens"`)) {
			if e.StripDeepSeekFields != nil {
				stripped := e.StripDeepSeekFields(body)
				slog.Info("stripVendorFields: auto-detected deepseek fields",
					"catalog_code", catalogCode,
					"original_bytes", len(body),
					"stripped_bytes", len(stripped))
				return stripped
			} else {
				slog.Warn("stripVendorFields: detected deepseek fields but StripDeepSeekFields is nil",
					"catalog_code", catalogCode,
					"body_preview", string(body[:min(100, len(body))]))
			}
		}
		// Auto-detect doubao fields (doubao_request_id, seeddance_request_id, etc.)
		if bytes.Contains(body, []byte(`"doubao_request_id"`)) ||
			bytes.Contains(body, []byte(`"seeddance_request_id"`)) {
			if e.StripDoubaoFields != nil {
				stripped := e.StripDoubaoFields(body)
				slog.Info("stripVendorFields: auto-detected doubao fields",
					"catalog_code", catalogCode,
					"original_bytes", len(body),
					"stripped_bytes", len(stripped))
				return stripped
			} else {
				slog.Warn("stripVendorFields: detected doubao fields but StripDoubaoFields is nil",
					"catalog_code", catalogCode,
					"body_preview", string(body[:min(100, len(body))]))
			}
		}
		return body
	}

	// Explicit catalog_code routing
	switch code {
	case "minimax":
		if e.StripMinimaxFields != nil {
			return e.StripMinimaxFields(body)
		}
	case "zhipu":
		if e.StripZhipuFields != nil {
			return e.StripZhipuFields(body)
		}
	case "deepseek":
		if e.StripDeepSeekFields != nil {
			return e.StripDeepSeekFields(body)
		}
	case "doubao":
		if e.StripDoubaoFields != nil {
			return e.StripDoubaoFields(body)
		}
	}
	return body
}

type ExecuteResult struct {
	Response  *http.Response
	Candidate provider.Candidate
	LatencyMs int
	// CachedReplay means Execute already wrote a completed response from the
	// pending store; callers must skip normal upstream response rendering.
	CachedReplay bool
	// StickyHit records whether the chosen credential came from an existing
	// sticky binding (L1/L2/L3) rather than a fresh routing decision.
	// It is consumed by telemetry so request_logs / routing_decision_log can
	// answer whether cache-adjacent affinity was actually used.
	StickyHit *bool
	// RequestBody is the body sent to the upstream provider (may be
	// protocol-converted from the inbound body). Use InboundBody for the
	// original client request body.
	RequestBody []byte
	// InboundBody is the raw body received from the client, before any
	// protocol conversion. Set once at the first ExecuteResult creation
	// and never modified across retries. Use this for audit/日志 that
	// need the canonical client request.
	InboundBody  []byte
	ResponseBody []byte
	// IntegrityObserved is set by the executor after it records a successful
	// non-stream response. The handler uses it to avoid observing the same
	// logical response a second time during request-log finalization.
	IntegrityObserved bool
	Trace             *Trace
	// Round 47 compression v7 T-NEW-2: optional compression event captured
	// by handleContextLengthRecovery. Populated when a 4xx recovery rewrote
	// the body (mechanical trim / memora L1 / LLM summary). nil otherwise.
	//
	// relay/handler.go emitTelemetry reads these fields and writes them
	// into request_logs.compression_reason / compression_strategy /
	// compression_meta so operators can SQL-trace the parent-child chain
	// per v7 §6.
	CompressionReason   *string
	CompressionStrategy *string
	CompressionMeta     []byte // JSON-encoded v7 §3.2 schema
	// V3.1 dispatch queue timestamps (from QueuedRequest after Pipeline.Submit).
	// Nil when dispatch path is off or stage was never reached.
	T0ArrivedAt       *time.Time
	T1TotalEnqueuedAt *time.Time
	T2TotalDequeuedAt *time.Time
	T3ModelEnqueuedAt *time.Time
	T4ModelDequeuedAt *time.Time
	T5CredEnqueuedAt  *time.Time
	T6CredDequeuedAt  *time.Time
	T7ForwardStartAt  *time.Time
	T8ResponseStartAt *time.Time
	T9ResponseEndAt   *time.Time

	// ParentRequestID is the pre-compression request_id. Populated when
	// the retry leg (after 4xx recovery) is treated as a child of the
	// original attempt by the executor. Currently we emit a single
	// request_id per logical call (the retry reuses the same id), so this
	// stays nil — kept here so the v7 §6 "single-level chain" invariant
	// can be enforced in a future change without refactoring the struct.
	ParentRequestID *string

	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
	// QualityFlags is the per-request list of detected tool_call issues
	// (empty_tool_name, duplicate_tool_call_id, …). QualityFixActions
	// is the JSON-encoded per-flag {detected, renamed, dropped} tally.
	// QualityScore is 0..1 from computeScore. All three are populated
	// only when the chosen provider's quality_fix_mode is non-'off'.
	QualityFlags      []string
	QualityFixActions []byte // JSONB: {"empty_tool_name":{"detected":2,"renamed":1},...}
	QualityScore      *float64

	// 2026-07-19: 路由尝试追踪器（从 ExecParams 传递过来）
	// 包含所有 upstream 尝试的详细记录，供 handler 填充到 telemetry。
	RoutingTracker *RoutingAttemptsTracker
}

type AttemptRecord struct {
	ProviderID   int               `json:"provider_id"`
	CredentialID int               `json:"credential_id"`
	RawModel     string            `json:"raw_model"`
	Kind         errorsx.ErrorKind `json:"kind"`
	Reason       string            `json:"reason,omitempty"`
}

type ExecuteError struct {
	LastErr   error
	Tried     int
	Exhausted bool
	Trace     *Trace
	Attempts  []AttemptRecord
	LastKind  errorsx.ErrorKind

	// V3.1 dispatch queue timestamps when the failure went through Pipeline.Submit.
	// Handler copies these onto RequestLogContext so failure rows also persist T0–T9.
	T0ArrivedAt       *time.Time
	T1TotalEnqueuedAt *time.Time
	T2TotalDequeuedAt *time.Time
	T3ModelEnqueuedAt *time.Time
	T4ModelDequeuedAt *time.Time
	T5CredEnqueuedAt  *time.Time
	T6CredDequeuedAt  *time.Time
	T7ForwardStartAt  *time.Time
	T8ResponseStartAt *time.Time
	T9ResponseEndAt   *time.Time
}

// Trace records the per-candidate decision points during routing/execution.
// It is rendered as the `decision_trace` jsonb column in routing_decision_log
// so the admin UI can show planned candidates, blocked candidates, and the
// final chosen credential.
type Trace struct {
	PlannedCandidates []TraceCandidate `json:"planned_candidates,omitempty"`
	BlockedCandidates []TraceCandidate `json:"blocked_candidates,omitempty"`
	Chosen            *TraceCandidate  `json:"chosen,omitempty"`
	FailureReason     string           `json:"failure_reason,omitempty"`
	// FallbackFromModel (Phase 2, 2026-07-19) is the original client-requested
	// model when the executor fell back to an equivalent model from a different
	// provider. Empty means no fallback occurred.
	FallbackFromModel string `json:"fallback_from_model,omitempty"`
}

type TraceCandidate struct {
	ProviderID         int    `json:"provider_id"`
	CredentialID       int    `json:"credential_id"`
	ProviderName       string `json:"provider_name,omitempty"`
	RawModel           string `json:"raw_model,omitempty"`
	Tier               int    `json:"tier,omitempty"`
	Reason             string `json:"reason,omitempty"`
	PredictedAvgTTFBMs int64  `json:"predicted_avg_ttfb_ms,omitempty"`
	PredictedSamples   int    `json:"predicted_samples,omitempty"`
}

func (e *ExecuteError) Error() string {
	if e == nil {
		return "execution failed"
	}
	if !e.Exhausted && e.LastErr != nil {
		return e.LastErr.Error()
	}
	if e.LastErr != nil {
		return fmt.Sprintf("all %d candidates failed: %v", e.Tried, e.LastErr)
	}
	return fmt.Sprintf("all %d candidates failed", e.Tried)
}

func (e *ExecuteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.LastErr
}

// background context. This is critical: using params.R.Context() (which is
// already cancelled when the client disconnects) would cause the Redis
// release operation to fail with context.Canceled, leaking the slot
// permanently. The slot leak accumulates until the pool is saturated,
// producing "cred_fp_slot saturated" errors and blocking all traffic.
//
// Fix: 2026-07-09 GLM-5.2 outage — every Release call MUST go through this
// helper so the context-isolation fix is applied uniformly.
// OPT-1 (2026-07-12): releaseFpLease fires the Redis release on a background
// goroutine instead of blocking the request hot-path. The previous
// synchronous implementation added 0~150 ms latency per request (Release
// runs a Lua script and retries up to 3× on transient Redis errors with
// 50/100/150 ms backoff). Three call sites use this in defer blocks:
//
//   - line ~957 (circuit/limiter acquisition failure)
//   - line ~974 (limiter acquisition failure)
//   - line ~1006 (post-execute cleanup, success and error paths)
//
// All three are the LAST step of a request lifecycle — there is no caller
// waiting on the result. The Release script is idempotent and the worst
// case under failure is that the slot key keeps its current TTL and self-
// expires (≤30 min) — the same outcome the old code had on retry exhaustion.
//
// A buffered pending channel caps the queue; if Redis is so degraded that
// the worker falls behind, we drop the Release rather than leak goroutines.
// The slot key then auto-expires on its Redis-side TTL, which is the
// intended safety net.
func releaseFpLease(m *credentialfpslot.Manager, lease *credentialfpslot.Lease) {
	if lease == nil || m == nil || !m.Enabled() {
		return
	}
	select {
	case fpReleaseQueue <- fpReleaseJob{m: m, lease: lease}:
	default:
		// Queue full — fall back to synchronous release with a short
		// timeout. This branch should be rare; the queue is sized to
		// absorb the worst observed burst (1024 in-flight requests).
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		m.Release(ctx, lease)
	}
}

// freeCredentialsTolerateTransient reports whether a transient failure on a
// free-tier credential should be left to the soft-demote path
// (RecentSuccessRate → loadScore quality score) instead of hard-excluding the
// credential (credentialstate cooling / circuit-breaker OPEN).
//
// Product principle (2026-07-14): a free credential at ~50% success rate is
// still "better than nothing" under load. The existing soft-demote in
// router_scoring.go:calculateQualityScore already ranks it behind healthy
// paid credentials (quality 0.5 → score penalty), so we only need to stop the
// two hard-exclude paths from evicting it.
//
// Only transient/network kinds are tolerated. Permanent failures
// (auth/auth_revoked/model_not_found/quota_permanent) still hard-exclude even
// free credentials — a dead key or a wrong model must not drag the route down.
//
// billingMode "" or "per_token" (the common case) returns false, so paid
// credentials are unaffected.
func freeCredentialsTolerateTransient(billingMode string, kind errorsx.ErrorKind) bool {
	if billingMode != "free" {
		return false
	}
	switch kind {
	case errorsx.KindTimeout,
		errorsx.KindStreamTimeout,
		errorsx.KindNetwork,
		errorsx.KindRateLimit,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindTransient:
		return true
	}
	return false
}

// isTransientFailoverKind reports whether a failed candidate's kind is a
// transient upstream condition.
//
// Scope, precisely (corrected 2026-08-09): the `continue` this predicate guards
// is the LAST statement in the candidate-loop body, so it does not decide
// whether failover happens — falling off the end of the loop body advances to
// the next candidate either way. The two effects that are real:
//
//   - it records lastTransientCred for the sync retry loop's inline probe
//   - it emits the "trying next candidate" log line
//
// The previous comment here claimed this was "streaming's ONLY failover
// safeguard" and that a missing kind "silently becomes all_candidates_failed".
// That was wrong and actively misleading: KindNetwork and KindConcurrent are
// IsRetryable and absent from this list, yet they fail over correctly today —
// purely because of where the closing brace sits. Anyone adding a kind here
// expecting to switch failover on would be changing nothing.
//
// The flip side is the real hazard: appending any statement after that
// `continue` converts both it and the credential-fatal `continue` above it from
// no-ops into load-bearing control flow, and every kind NOT in this list (or in
// IsCredentialFatal) would start ending the walk early. TestCandidateLoopFailsOverForEveryRetryableKind
// in failover_completeness_test.go pins that invariant structurally.
func isTransientFailoverKind(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindTransient,
		errorsx.KindRateLimit,
		errorsx.KindTimeout,
		errorsx.KindStreamTimeout,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindEmptyResponse,
		errorsx.KindNoAvailableChannel:
		return true
	}
	return false
}

// kindDiagnosticRank scores how much an error kind explains a failed request.
// Higher wins when several candidates fail with different kinds.
//
//	3  credential-fatal (auth / quota) — names a specific broken credential and
//	   a concrete operator action (rotate the key, top up the account)
//	2  binding-level (model_not_found / model_deprecated) — the model is gone
//	   from this provider; actionable against the catalog
//	1  client-caused (context_length, content_filter, unsupported_feature,
//	   tool_call_id_mismatch) — the caller can fix the request itself
//	0  transient (5xx, timeout, rate limit, overload) — says only "try again"
//
// Rationale: the executor walks every candidate, so a request that fails
// entirely can carry several different kinds. Reporting whichever one happened
// to come last tells the client about an arbitrary candidate.
//
// Concretely, with candidates [quota-exhausted, transiently-down] the client
// used to be told "No available provider" (transient, ranked 0) and the real
// cause — every credential is out of quota — was dropped. The reverse also
// happened: one 5xx on the last candidate masked a genuine
// all-credentials-exhausted event.
func kindDiagnosticRank(kind errorsx.ErrorKind) int {
	switch {
	case errorsx.IsCredentialFatal(kind):
		return 3
	case kind == errorsx.KindModelNotFound, kind == errorsx.KindModelDeprecated:
		return 2
	case kind == errorsx.KindContextLength,
		kind == errorsx.KindContentFilter,
		kind == errorsx.KindUnsupportedFeature,
		kind == errorsx.KindToolCallIdMismatch:
		return 1
	default:
		return 0
	}
}

// recordLastKind folds a candidate's failure kind into the running lastKind,
// keeping the most diagnostic one (see kindDiagnosticRank).
//
// Ties keep the earlier kind: with two equally-ranked failures the first is
// the one that actually decided the request's fate for that candidate, and
// keeping it makes the reported kind stable across retry rounds.
//
// A kind that is empty is treated as "no information" and never overwrites a
// known one.
func recordLastKind(current, incoming errorsx.ErrorKind) errorsx.ErrorKind {
	if incoming == "" {
		return current
	}
	if current == "" {
		return incoming
	}
	if kindDiagnosticRank(incoming) > kindDiagnosticRank(current) {
		return incoming
	}
	return current
}

// fpReleaseJob pairs a Manager and a Lease for the background release worker.
type fpReleaseJob struct {
	m     *credentialfpslot.Manager
	lease *credentialfpslot.Lease
}

// fpReleaseQueue buffers background Release calls. Buffered (1024) so that
// a sudden burst does not synchronously block the hot path. When full,
// releaseFpLease falls back to a bounded synchronous call.
var fpReleaseQueue = make(chan fpReleaseJob, 1024)

// fpReleaseWorker drains fpReleaseQueue, calling Manager.Release on each
// job with an independent background context. A single worker goroutine
// is sufficient: Redis Release is a single Lua script (low cost), and
// Manager.Release has its own internal 3-attempt retry loop. If we ever
// observe queue depth > 100 sustained, bump the worker count or the
// queue size — but in practice Release latency is sub-millisecond.
var fpReleaseWorkerOnce sync.Once

func ensureFpReleaseWorker() {
	fpReleaseWorkerOnce.Do(func() {
		go func() {
			for job := range fpReleaseQueue {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				job.m.Release(ctx, job.lease)
				cancel()
			}
		}()
	})
}

func init() { ensureFpReleaseWorker() }

// buildEnhancedErrorContext creates a detailed context map for 5xx error analysis.
// It includes request dimensions (tokens, messages, body size) to help diagnose
// whether context length is related to transient errors.
// 2026-07-19: Added to analyze claude-fable-5/sonnet-5 5xx errors.
func buildEnhancedErrorContext(params *ExecParams, kind errorsx.ErrorKind, execErr error, candidateCount int, attemptIndex int) map[string]any {
	ctx := map[string]any{
		"client_model":    params.ClientModel,
		"attempt_kind":    string(kind),
		"err_msg":         execErr.Error(),
		"is_stream":       params.IsStream,
		"candidate_count": candidateCount,
		"attempt_index":   attemptIndex,
	}

	// Request body size
	if len(params.BodyBytes) > 0 {
		ctx["request_body_size"] = len(params.BodyBytes)
	}

	// Parse request body to extract context dimensions
	if len(params.BodyBytes) > 0 {
		var reqBody map[string]any
		if err := json.Unmarshal(params.BodyBytes, &reqBody); err == nil {
			// Message count
			if messages, ok := reqBody["messages"].([]any); ok {
				ctx["message_count"] = len(messages)

				// Calculate approximate input length
				totalLen := 0
				for _, msg := range messages {
					if m, ok := msg.(map[string]any); ok {
						if content, ok := m["content"].(string); ok {
							totalLen += len(content)
						}
					}
				}
				ctx["total_message_length"] = totalLen
			}

			// System prompt
			if system, ok := reqBody["system"].(string); ok && len(system) > 0 {
				ctx["system_prompt_length"] = len(system)
			}

			// max_tokens
			if maxTokens, ok := reqBody["max_tokens"]; ok {
				ctx["max_tokens"] = maxTokens
			}

			// temperature
			if temp, ok := reqBody["temperature"]; ok {
				ctx["temperature"] = temp
			}

			// tools count
			if tools, ok := reqBody["tools"].([]any); ok && len(tools) > 0 {
				ctx["tools_count"] = len(tools)
			}
		}
	}

	return ctx
}

func (e *Executor) Execute(params *ExecParams) (result *ExecuteResult, err error) {
	if !params.clientTokenMetricsOwner {
		params.clientTokenMetricsOwner = true
		holder := clientTokenForMetrics(params)
		outcome := "error"
		defer func() {
			if err == nil {
				outcome = "acquired"
			}
			credentialfpslot.RecordClientTokenRequest(params.TenantID, holder, outcome)
			// ── 2026-08-15 (V3.3-OBS OBS-B1): reply 动作事件（S9，终态）────
			// 只在最外层 Execute 帧发射（clientTokenMetricsOwner 与
			// sync-retry / fallback 的递归 Execute 共享同一 request_id，
			// 内层帧不再重复发射）。成功/失败均发射，失败带 error_kind。
			if e.liveActions != nil && params.RequestID != "" {
				ev := liveactions.ActionEvent{
					RequestID: params.RequestID,
					Action:    liveactions.ActionReply,
					Model:     params.ClientModel,
				}
				if err == nil && result != nil {
					ev.Detail = map[string]string{
						"status":     "success",
						"latency_ms": strconv.FormatInt(int64(result.LatencyMs), 10),
					}
				} else {
					kind := classifyExecError(err)
					if execErr, ok := err.(*ExecuteError); ok && execErr.LastKind != "" {
						kind = execErr.LastKind
					}
					ev.ErrorKind = string(kind)
					ev.Detail = map[string]string{"status": "failure"}
				}
				e.liveActions.Emit(context.Background(), ev)
			}
		}()
	}
	if params.R != nil && strings.TrimSpace(params.TenantID) != "" {
		params.R = params.R.WithContext(session.SetTenantID(params.R.Context(), params.TenantID))
	}

	// Keep the inbound body immutable across candidate failover. Per-candidate
	// protocol rendering works from this snapshot and never re-enters attachment extraction.
	params.BodyBytes = append([]byte(nil), params.BodyBytes...)
	if !params.diagnosticsLogged && e.RawDataLogger != nil {
		requestID := diagnosticRequestID(params)
		protocol := diagnosticProtocol(params.ClientProtocol, "openai-completions")
		if err := e.RawDataLogger.LogRequest(requestID, protocol, params.BodyBytes); err != nil {
			slog.Warn("executor diagnostics: client request logging failed", "request_id", requestID, "error", err)
		}
		params.diagnosticsLogged = true
	}

	// ── 2026-07-22: Session-aware continuation/retry detection ───────
	// 2026-08-06: exempt gateway-internal auto requests (auto title / auto
	// summary). Their corpus is the concatenated session transcript, which
	// routinely contains the literal "continue"/"retry" keyword; running
	// IsContinuationOrRetry here trims the user corpus message and sends an
	// empty-messages body upstream → Ark 400 InvalidParameter → circuit break.
	isAutoReq := params.R != nil &&
		strings.EqualFold(params.R.Header.Get("X-Gw-Is-Auto"), "true")
	// 2026-08-06: tool sessions skip continuation TRIM only, not retry
	// replay. Trim drops the last user+assistant turn, which destroys
	// tool_call/tool_result pairing in an agent loop. Retry replay is a
	// safe cache hit — it doesn't modify the body — so keeping it for
	// tool sessions is correct and matches the original intent.
	if params.SessionID != "" && e.PendingStore != nil && params.W != nil && len(params.BodyBytes) > 0 {
		hotCfg := LoadHotConfig()
		isContinue, isRetry := IsContinuationOrRetry(params.BodyBytes, hotCfg)
		// Trim is destructive: only auto requests and non-tool sessions
		// may enter this branch. Tool sessions skip straight to retry.
		if isContinue && !isAutoReq && !params.ToolsRequested {
			modified, err := trimOneMessageFromBody(params.BodyBytes)
			if err == nil && len(modified) > 0 {
				params.BodyBytes = modified
				slog.Info("executor: continue keyword detected, trimmed one message turn",
					"session_id", params.SessionID,
				)
			}
		}
		if isRetry {
			entry, requestID, found, _ := e.PendingStore.GetLatest(params.R.Context(), params.SessionID)
			if found && entry != nil && entry.Body != "" && entry.Status == pending.StatusCompleted {
				slog.Info("executor: retry keyword, replaying cached completed response",
					"session_id", params.SessionID,
					"request_id", requestID,
				)
				writeCachedResponse(params.W, entry)
				return &ExecuteResult{
					RequestBody:  params.BodyBytes,
					Candidate:    candidateFromEntry(entry),
					CachedReplay: true,
				}, nil
			}
			slog.Info("executor: retry keyword but no cached response, proceeding upstream",
				"session_id", params.SessionID,
			)
		}
	}

	// Layer 0: Global identity pool cap (if enabled).
	// Acquire a stable identity for this end-user. If the cap is reached,
	// the pool LRU-recycles an existing identity, so the request appears
	// to the upstream as a returning user (anti-rate-limit evasion).
	var globalIdentity interface{}
	if ratelimit.IsRateLimitEnabled() && e.IdentityPool != nil && e.IdentityPool.Enabled() {
		// Use the request fingerprint as the identity key. The identity
		// package extracts a stable hash from X-Device-Seed / User-Agent / IP.
		fpString := params.ClientID.IdentityHash
		if fpString == "" {
			// Fallback: if no fingerprint headers, use the sticky key (session ID).
			fpString = params.StickyKey
		}
		acquired, recycled, err := e.IdentityPool.Acquire(params.R.Context(), fpString)
		if err != nil {
			slog.Warn("identity_pool acquire failed, continuing without cap", "error", err)
		} else {
			globalIdentity = acquired
			if recycled {
				slog.Debug("identity_pool recycled LRU slot",
					"original", fpString,
					"recycled_to", acquired,
				)
			}
			// No explicit Release needed — the pool uses TTL-based LRU eviction.
		}
	}
	// If globalIdentity is non-nil, downstream providers see this user as
	// the recycled identity. We do NOT modify params.ClientID.IdentityHash
	// (that's the raw fingerprint for audit), but the egress identity builder
	// (identity.BuildEgressIdentity) should consult globalIdentity when
	// constructing the virtual IP/MAC. TODO: wire globalIdentity into
	// credentialfpslot.Lease so BuildEgressIdentity can read it.
	_ = globalIdentity // suppress unused warning until wired into credentialfpslot

	// 2026-07-07: Use multi-level sticky lookup (L1: session+model, L2: client+model, L3: client)
	// instead of the old single-level lookup (L3 only). This ensures different session IDs
	// get different credentials, fixing the load balancing issue. The pickStickyCredentialID
	// helper is also used by the sync-retry path so both legs stay consistent.
	//
	// AUDIT-3 (2026-07-12): when rate_limit.enabled is OFF, force sticky
	// to nil so every request goes through normal P2C + per-tier rotation.
	// Combined with the gate-off early-returns in Limiter / FpSlot / RPM,
	// this delivers the user semantic: "较均衡地将请求分发到所有综合最优
	// 的可用节点中" — no fingerprint pinning, no concurrency cap, no RPM
	// cap, and no session pinning; the router picks by score + rotation only.
	//
	// AUDIT-3.1 (2026-07-12): also skip pickStickyCredentialID entirely
	// when the gate is off to avoid wasted L1→L2→L3 lookups whose result
	// would be discarded. This also prevents stale entries from before a
	// gate-off interval from influencing the routing decision.
	var stickyCredID *int
	if ratelimit.IsRateLimitEnabled() {
		stickyCredID = e.pickStickyCredentialID(params)
	}
	if os.Getenv("STICKY_MULTILEVEL_DEBUG") == "1" {
		slog.Info("STICKY_PICK",
			"session_id", params.SessionID,
			"model", params.Model,
			"tenant", params.TenantID,
			"sticky_key", params.StickyKey,
			"found", stickyCredID != nil,
			"cred_id", func() int {
				if stickyCredID != nil {
					return *stickyCredID
				}
				return 0
			}(),
		)
	}

	candidates := e.Router.PlanCandidatesWithContext(
		params.R.Context(),
		params.Candidates,
		stickyCredID,
		params.Policy,
		egressPref(params.Transform),
		params.TenantID,
		params.ClientModel,
		params.RequestID,
	)

	// ── 2026-08-15 (V3.3-OBS OBS-B1): credential_selected 动作事件（S5）────
	// Router.PlanCandidates 输出即路由选定的凭据序（best-first），发射首个
	// 候选 + weight/tier（24 号 §2）。
	if len(candidates) > 0 {
		top := candidates[0]
		e.liveActions.Emit(params.R.Context(), liveactions.ActionEvent{
			RequestID:    params.RequestID,
			Action:       liveactions.ActionCredentialSelected,
			Model:        params.ClientModel,
			CredentialID: top.CredentialID,
			Detail: map[string]string{
				"weight":     strconv.Itoa(top.Weight),
				"tier":       strconv.Itoa(top.Tier),
				"candidates": strconv.Itoa(len(candidates)),
			},
		})
	}

	trace := &Trace{
		PlannedCandidates: make([]TraceCandidate, 0, len(params.Candidates)),
		BlockedCandidates: []TraceCandidate{},
	}
	for _, c := range params.Candidates {
		trace.PlannedCandidates = append(trace.PlannedCandidates, TraceCandidate{
			ProviderID:   c.ProviderID,
			CredentialID: c.CredentialID,
			RawModel:     candidateRawModel(c),
			Tier:         c.Tier,
		})
	}
	if len(candidates) == 0 {
		trace.FailureReason = "no_candidates_from_router"
		// Aggregate why every input candidate was filtered out so the next
		// "every provider failed simultaneously" outage is diagnosable from
		// this single log line.
		//
		// NOTE: this loop intentionally only consults the DB-derived
		// UnavailableReason(); the in-memory StateManager reason (e.g.
		// "state:empty_response") is captured by the upstream router log
		// ("router: all candidates unavailable", router.go:145) because
		// e.StateObserver is the narrow on-success/on-failure interface
		// and does not expose IsAvailable. Operators correlating the
		// two log lines (router + executor) get the full reason.
		reasonCounts := make(map[string]int, 8)
		// 2026-07-03: 不限制单轮候选数量，允许轮转完所有可用候选。
		// 死循环保护由 sync_retry 的轮数限制 (maxSyncRetryRounds) 提供。
		for _, c := range params.Candidates {
			reason := c.UnavailableReason()
			if reason == "" {
				// 2026-07-19: Infer reason instead of defaulting to "unknown"
				// to improve diagnostic clarity in request_logs
				if c.CredentialID == 0 {
					reason = "no_credential"
				} else if c.ProviderID == 0 {
					reason = "no_provider"
				} else {
					reason = "availability_check_failed"
				}
			}
			reasonCounts[reason]++
		}
		slog.Warn("executor: no candidates after router",
			"input_candidates", len(params.Candidates),
			"client_model", params.ClientModel,
			"reasons", reasonCounts,
		)
		// ── 2026-08-15 (V3.3-OBS OBS-B1): no_route 动作事件 ──────────────────
		// Router 过滤后无可用候选。blocked_reasons 摘要（reason:count）。
		{
			reasons := make([]string, 0, len(reasonCounts))
			for r, n := range reasonCounts {
				reasons = append(reasons, fmt.Sprintf("%s:%d", r, n))
			}
			sort.Strings(reasons)
			e.liveActions.Emit(params.R.Context(), liveactions.ActionEvent{
				RequestID: params.RequestID,
				Action:    liveactions.ActionNoRoute,
				Model:     params.ClientModel,
				Detail: map[string]string{
					"blocked_reasons": strings.Join(reasons, ","),
				},
			})
		}
		// 2026-07-14: previously the no-candidates path stopped here with
		// only a failed request_logs row to show for it. The realtime
		// dashboard would render the failure tile but no follow-up probe
		// ever fired, leaving operators to investigate the outage from
		// request_logs alone. We now fan the signal out to the
		// credential-state manager which will trigger ActiveProbeWorker
		// for each candidate with a real credential_id. Probes land in
		// request_logs with task_type='probe_triggered' and show up in
		// the live stream alongside the failed request.
		//
		// We attach the original failed request_id as the probe's
		// parent_request_id so /request-logs can correlate them. The
		// billing_mode is read off the first candidate (best effort) —
		// the manager doesn't currently need per-cred billing info to
		// dispatch the probe, only to decide the failure-handling
		// policy on the result.
		probeCandidates := params.Candidates
		if resolver, ok := e.Provider.(probeCandidateResolver); ok {
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			if allCandidates, err := resolver.GetProbeCandidates(probeCtx, params.ClientModel, params.ClientID.Fingerprint.ClientProfile, params.TenantID); err == nil && len(allCandidates) > 0 {
				probeCandidates = allCandidates
			} else if err != nil {
				slog.Warn("executor: full probe candidate lookup failed", "model", params.ClientModel, "error", err)
			}
			probeCancel()
		}
		// 2026-07-24: URSM v2 authoritative 模式下跳过 RoutingStateShadow
		if e.RoutingStateShadow != nil && e.legacyWritersEnabled() {
			for _, c := range probeCandidates {
				e.RoutingStateShadow.ObserveProbe(routingstate.ProbeTask{
					CredentialID:  c.CredentialID,
					RawModelName:  candidateRawModel(c),
					Scope:         routingstate.ScopeModel,
					Trigger:       routingstate.ProbeTriggerNoCandidates,
					CorrelationID: params.RequestID,
				})
			}
		}
		if e.StateObserver != nil && len(params.Candidates) > 0 && params.RequestID != "" {
			noCands := make([]credentialstate.NoCandidatesCandidate, 0, len(probeCandidates))
			for _, c := range probeCandidates {

				if c.CredentialID == 0 {
					continue
				}
				noCands = append(noCands, credentialstate.NoCandidatesCandidate{
					CredentialID: c.CredentialID,
					ProviderID:   c.ProviderID,
					RawModel:     candidateRawModel(c),
					BillingMode:  c.BillingMode,
				})
			}
			// Detached context: this runs on the request hot-path and
			// the upstream caller (handler.go) may cancel r.Context()
			// before probes finish dispatching. Use Background with a
			// short timeout so the manager has enough headroom to enqueue
			// all probes but never blocks longer than the network RTT.
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			e.StateObserver.OnNoCandidates(probeCtx, credentialstate.NoCandidatesSignal{
				ClientModel: params.ClientModel,
				TenantID:    params.TenantID,
				RequestID:   params.RequestID,
				Candidates:  noCands,
			})
			probeCancel()
		}
		// ── 2026-07-17 同步探测 hold: 把客户端请求暂停，并行探测同模型所有
		// 候选节点的供应商直连；首个直连成功 → 网关路由测试 → 重发用户请求。
		// 所有探测都失败 / 5s 超时 → fall through 到下方 503 返回。
		if e.SyncNoCandidateProbe && e.ProbeSync != nil && params.R != nil && !params.SurvivalAttempt {
			syncNoCands := make([]credentialstate.NoCandidatesCandidate, 0, len(probeCandidates))
			for _, c := range probeCandidates {
				if c.CredentialID == 0 {
					continue
				}
				syncNoCands = append(syncNoCands, credentialstate.NoCandidatesCandidate{
					CredentialID: c.CredentialID,
					ProviderID:   c.ProviderID,
					RawModel:     candidateRawModel(c),
					BillingMode:  c.BillingMode,
				})
			}
			if len(syncNoCands) > 0 {
				if params.OnPreStreamKeepalivePause != nil {
					params.OnPreStreamKeepalivePause()
				}
				if params.OnProbeHoldStart != nil {
					params.OnProbeHoldStart()
				}
				holdTimeout := e.SyncNoCandidateTimeout
				if holdTimeout <= 0 {
					holdTimeout = 5 * time.Second
				}
				holdCtx, holdCancel := context.WithTimeout(params.R.Context(), holdTimeout)
				holdStart := time.Now()
				recovered := e.ProbeSync(holdCtx, syncNoCands, params.TenantID, params.RequestID)
				holdCancel()
				if params.OnProbeHoldEnd != nil {
					params.OnProbeHoldEnd(recovered)
				}
				if recovered {
					// The initial candidate slice is a point-in-time snapshot and may
					// still carry Routable=false after the probe restored the node.
					// Reload candidates so the retry is based on the persisted/cache
					// state rather than re-filtering stale request data.
					retryCandidates := e.refreshedCandidatesAfterProbe(params)
					if len(retryCandidates) == 0 {
						retryCandidates = params.Candidates
					}
					var retrySticky *int
					if ratelimit.IsRateLimitEnabled() {
						retrySticky = stickyCredID
					}
					subCandidates := e.Router.PlanCandidatesWithContext(
						params.R.Context(), retryCandidates, retrySticky, params.Policy,
						egressPref(params.Transform), params.TenantID, params.ClientModel, params.RequestID,
					)

					if len(subCandidates) > 0 {
						e.asyncDepth.Add(1)
						subParams := *params
						subParams.Candidates = subCandidates
						result, retryErr := e.Execute(&subParams)
						e.asyncDepth.Add(-1)
						if retryErr == nil {
							slog.Info("sync_no_candidate_probe_recovered",
								"model", params.ClientModel,
								"request_id", params.RequestID,
								"hold_ms", time.Since(holdStart).Milliseconds(),
								"candidates", len(subCandidates),
							)
							return result, nil
						}
						if execErrTyped, ok := retryErr.(*ExecuteError); ok && execErrTyped.Trace != nil {
							trace.BlockedCandidates = append(
								trace.BlockedCandidates,
								execErrTyped.Trace.BlockedCandidates...,
							)
						}
					}
				}
			}
		}
		if params.AuditBuilder != nil {
			params.AuditBuilder.DecisionTrace(trace)
		}
		return nil, &ExecuteError{Tried: 0, Exhausted: true, Trace: trace}
	}

	// Token 资源管理（2026-07-27）：holder 现在按"客户端 token"维度拼接
	//   holder = clientTokenOf(userKey, clientType)
	// userKey 取 params.StickyKey —— 这是 buildRouteStickyKey 派生的
	// {tenant}:{app}:{apiKeyID}:{profile} 稳定身份（原有 holder 的语义），
	// fallback 到 X-Request-Id（无 sticky key 时的兜底，如探测请求）。
	//
	// 重要：不使用 params.ClientID.IdentityHash 作为 userKey。IdentityHash
	// 是设备/环境指纹哈希（PrimarySeed 优先取 DeviceSeed/MachineID，否则退化
	// 为 UA+OS+Arch+RuntimeName+RuntimeVersion 的组合哈希），会随客户端版本
	// 升级、系统更新等环境变化而漂移。若用它做 userKey，同一用户会在环境
	// 指纹变化时意外换 holder，丢失 24h Pin 复用，与"同用户复用同一身份槽"
	// 的设计目标相悖。IdentityHash 仍用于 IdentityPool（全局身份数量上限）
	// 与 Limiter（并发限流），与本 holder 拼接是两个独立维度。
	//
	// clientType 取自 extractClientType —— 未识别回退 "unknown"。
	// 这样同一用户在不同客户端（cursor / claude-code / unknown）下会获得
	// 独立的 FpSlot 与 Pin，互不挤占；同一 (userKey, clientType) 24h 内
	// 走 Pin 复用同一个 slot。
	//
	// 本实现刻意内联 helper，避免在 executors 包引入对 streaming 包的依赖
	// （executor.go 历史上不 import 父包，保持现有边界）。
	userKey := params.StickyKey
	if userKey == "" {
		userKey = params.R.Header.Get("X-Request-Id")
	}
	clientType := extractClientType(params.R)
	holder := clientTokenOf(userKey, clientType)
	// fpSlotDegraded is set when the pre-filter found every candidate's slot
	// pool saturated. In that mode the Acquire loop below tolerates a failed
	// Acquire and runs the request without a fingerprint slot rather than
	// rejecting it. See the 2026-06-23 minimax-m3 outage post-mortem.
	//
	// KILL-SWITCH (2026-07-12 incident): when settings.IsEnabled("fp_slot")
	// is false (env KILL_FP_SLOT=1), we skip the fingerprint prefilter
	// entirely so the full candidate set is always tried. This isolates
	// the 2026-07-12 minimax-m3 outage where every candidate got dropped
	// for failing health-probe.
	fpSlotDegraded := false
	fpSlotKilled := !settings.IsEnabled("fp_slot")
	if e.FpSlots != nil && e.FpSlots.Enabled() && !fpSlotKilled {
		filtered := make([]provider.Candidate, 0, len(candidates))
		for _, cand := range candidates {
			if e.FpSlots.RoutingEligibleForTenant(params.R.Context(), cand.CredentialID, cand.FpSlotLimit, holder, fpSlotTenantID(params)) {
				filtered = append(filtered, cand)
			} else {
				slog.Info("cred_fp_slot prefilter skip",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
				)
			}
		}
		// Degradation (added 2026-06-23): if every candidate's fingerprint
		// slot pool is saturated, fall back to the full pre-filter set
		// rather than rejecting the request outright. The fp_slot layer is
		// a fingerprint-isolation optimisation (spread distinct identities
		// across slots); when it cannot serve, routing availability takes
		// priority. This was the direct cause of the 2026-06-23 minimax-m3
		// outage: only one candidate was reachable (see the standardized_name
		// fix in provider/client.go), its 5 slots were held by long-lived
		// sessions, and every new session got "cred_fp_slot: all saturated"
		// instead of being routed. With a larger candidate pool (post
		// standardized_name fix) this fallback is rarely hit, but it
		// prevents a single saturated credential from taking down a whole
		// model.
		if len(filtered) == 0 && len(candidates) > 0 {
			slog.Warn("cred_fp_slot all saturated, degrading to full candidate set",
				"candidate_count", len(candidates),
				"client_model", params.ClientModel,
			)
			fpSlotDegraded = true

			// Phase 1: 记录降级模式
			if e.DegradationTracker != nil {
				e.DegradationTracker.RecordRequest(params.ClientModel, true)

				// 检查是否超过阈值
				ratio := e.DegradationTracker.GetDegradationRatio(params.ClientModel)
				if ratio > 0.10 { // 10% 阈值
					slog.Error("fp_slot_saturation_critical",
						"model", params.ClientModel,
						"degradation_ratio", ratio,
						"threshold", 0.10,
					)
				}
			}

			// Keep the original candidates slice (do NOT replace with the
			// empty filtered set).
		} else {
			// Phase 1: 记录正常模式
			if e.DegradationTracker != nil {
				e.DegradationTracker.RecordRequest(params.ClientModel, false)
			}

			candidates = filtered
			if len(candidates) == 0 {
				return nil, &ExecuteError{LastErr: fmt.Errorf("cred_fp_slot: all saturated"), Tried: 0, Exhausted: true}
			}
		}
	}

	tTotal := time.Now()
	retryPerCred := params.Policy.RetryPerCredential
	// 479: V2 multi-tier dispatch path (per-credential queue + peak-flattening
	// governor + tiered credential/model failover). Gated by the dispatch_v2
	// setting + a wired pipeline. When enabled, route through the pipeline and
	// return its result; otherwise fall through to the legacy synchronous
	// candidate loop below. See executor_dispatch.go and
	// docs/会话优化v2/57-多层队列调度架构设计方案.md.
	if dispatch.IsDispatchEnabled() && e.dispatchPipeline != nil {
		if res, derr := e.executeViaDispatch(params, candidates, holder, fpSlotDegraded, stickyCredID); res != nil || derr != nil {
			return res, derr
		}
	}
	var lastErr error
	// lastKind is the kind reported to the client and used to pick the sync
	// retry's sticky policy. It is NOT plain last-writer-wins: see
	// recordLastKind for why the most diagnostic kind wins instead of the
	// chronologically last one.
	var lastKind errorsx.ErrorKind
	var attempts []AttemptRecord
	// Track the last transient-failed credential so the sync retry loop
	// can inline-probe it before re-planning candidates, bypassing the
	// 5s ActiveProbeWorker backoff.
	var lastTransientCred credentialstate.NoCandidatesCandidate
	tried := 0

	// 2026-07-09: 会话级凭据黑名单（修复 NVIDIA NIM 连续失败不降级问题）
	// 单次会话中，同一凭据失败 2 次后强制跳过，避免 Sync Retry 反复打到同一凭据。
	sessionBlacklist := make(map[int]int) // credentialID -> consecutive failures in this session

	// OPT-3 (2026-07-12): per-provider content_filter short-circuit set.
	// When a candidate returns content_filter, all sibling candidates of
	// the SAME provider are skipped (the upstream content policy is shared).
	// Candidates from DIFFERENT providers are still tried because each
	// provider has its own content policy.
	contentFilterProviders := make(map[int]struct{})

	// 2026-07-22: NodeTracker for credential failover tracking and SSE.
	nodeTracker := NewNodeTracker(LoadHotConfig())

	// O-1 is request-local: at most one candidate is predictive-skipped.
	// Do not derive this from tried; tried counts only candidates that reached
	// the real execution path after earlier filters.
	predictiveDecisionMade := false

	// 2026-08-08 P0 Fix: when only ONE candidate was returned by the router,
	// every sibling failover path is gone. If the lone credential's circuit
	// is open (or the recent_success_rate hard-filter already dropped the
	// only other sibling), we must NOT skip this candidate — that would
	// produce an immediate 503 for every Claude/GPT request. We surface the
	// "sole candidate" flag here so the circuit-open branch below can
	// fail-open instead of continue.
	totalCandidates := len(candidates)
	for candidateIndex, cand := range candidates {
		// resolvedKeyIdx holds the multi-key rotator's chosen key index for this
		// candidate (-1 when single-key or not yet resolved). Used by the failure
		// path to RecordKeyFailure on the correct key.
		resolvedKeyIdx := -1
		// OPT-3: skip siblings of providers that already returned
		// content_filter. The credential is healthy; the content is
		// the problem. We do NOT update circuit / sticky / state —
		// see classifyContentFilterError below for the rationale.

		if _, hit := contentFilterProviders[cand.ProviderID]; hit {
			trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Tier:         cand.Tier,
				Reason:       "content_filter_same_provider",
			})
			continue
		}

		// 2026-07-09: 检查会话黑名单
		if sessionBlacklist[cand.CredentialID] >= 2 {
			slog.Warn("executor: credential blacklisted for this session",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"session_failures", sessionBlacklist[cand.CredentialID],
				"client_model", params.ClientModel,
			)
			trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Tier:         cand.Tier,
				Reason:       "session_blacklist:consecutive_failures",
			})
			continue // 跳过该凭据
		}

		// O-1: make one prediction decision after existing logical skips.
		// A non-slow first executable candidate consumes the decision too;
		// this prevents scanning all candidates for a slow one.
		candidateCount := predictiveCandidateCount(candidates, candidateIndex, contentFilterProviders, sessionBlacklist)
		if decision, skip := predictiveDecisionForCandidate(e.PredictiveTTFBSkipper, &predictiveDecisionMade, candidateCount, cand.CredentialID); skip {

			trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
				ProviderID:         cand.ProviderID,
				CredentialID:       cand.CredentialID,
				RawModel:           cand.RawModel,
				Tier:               cand.Tier,
				Reason:             decision.Reason,
				PredictedAvgTTFBMs: decision.AvgTTFB.Milliseconds(),
				PredictedSamples:   decision.SampleCount,
			})
			slog.Debug("executor: predictive TTFB candidate skip",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"avg_ttfb_ms", decision.AvgTTFB.Milliseconds(),
				"sample_count", decision.SampleCount,
			)
			continue
		}

		tried++

		nodeTracker.Record(cand)

		// ── 2026-08-15 (V3.3-OBS OBS-B1): upstream_request 动作事件（S7）──
		// 每个候选开始转发时发射；attempt=tried，tried>1 即同请求内的重试。
		e.liveActions.Emit(params.R.Context(), liveactions.ActionEvent{
			RequestID:    params.RequestID,
			Action:       liveactions.ActionUpstreamRequest,
			Model:        params.ClientModel,
			CredentialID: cand.CredentialID,
			Retry:        tried > 1,
			RetrySeq:     tried,
			Detail: map[string]string{
				"attempt":     strconv.Itoa(tried),
				"provider_id": strconv.Itoa(cand.ProviderID),
				"raw_model":   candidateRawModel(cand),
			},
		})

		// Reset the stream capture for this candidate so textContent, chunk
		// count, checksum, and the done/interrupted flags from a prior
		// failed attempt do not leak into this attempt's metrics. Without
		// this reset, a credential failover mid-stream would produce an
		// audit row with merged data from both credentials and logically
		// inconsistent flags (interrupted=true && done=true).
		if params.IsStream && params.Capture != nil && tried > 1 {
			params.Capture.Reset()
		}

		var fpLease *credentialfpslot.Lease
		if e.FpSlots != nil && e.FpSlots.Enabled() {
			lease, ok := e.FpSlots.Acquire(params.R.Context(), cand.CredentialID, cand.FpSlotLimit, holder, fpSlotTenantID(params))
			if !ok {
				if fpSlotDegraded {
					// Degradation path: all slot pools were saturated at the
					// pre-filter. Rather than rejecting the request, run it
					// without a fingerprint slot. The egress identity will
					// fall back to a default slot (identity.BuildEgressIdentity
					// with slot 0) or the identity-pool recycled identity.
					slog.Warn("cred_fp_slot acquire failed in degraded mode, running without slot",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
					)
					fpLease = nil
				} else {
					slog.Info("cred_fp_slot saturated",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
					)
					lastErr = fmt.Errorf("cred_fp_slot saturated for credential %d", cand.CredentialID)
					continue
				}
			} else {
				fpLease = lease
			}
		}

		// KILL-SWITCH (2026-07-12 incident): when circuit_degradation is
		// disabled (KILL_CIRCUIT_DEGRADATION=1), bypass the circuit
		// breaker entirely and always try the candidate. The DB-side
		// `credential_model_bindings.unavailable_recover_at` and
		// `v_routable_credential_models.is_routable` already provide
		// safety net — the kill-switch is for emergencies only.
		circuitOpen := !settings.IsEnabled("circuit_degradation")
		// 2026-07-03 incident fix: Allow() consumes the single half-open
		// probe slot. Track whether WE consumed it so early-exit paths
		// (limiter reject / mnf / client-bug / context-length /
		// content-filter / client_write_failed) can ReleaseProbe() instead
		// of leaking the slot and wedging the breaker in HALF_OPEN forever.
		probeConsumed := false
		if !circuitOpen {
			probeConsumed = e.Circuit.Allow(cand.ProviderID, cand.CredentialID)
			circuitOpen = !probeConsumed
		}
		if circuitOpen {
			if settings.IsEnabled("circuit_degradation") {
				// 2026-08-08 P0 Fix: fail-open for sole-candidate models.
				// When the router returned only ONE candidate, no sibling
				// credential can take this request. Skipping on circuit
				// produces an immediate 503 for every Claude/GPT request
				// (apiclaude/apigpt each have exactly one routable sibling
				// after manual_disabled tightened the candidate pool).
				// We honor the probe slot already consumed (probeConsumed)
				// and fall through to Limiter.AcquireAll + a real attempt,
				// letting node_probe_worker / circuit half-open recover the
				// route naturally on the next cycle.
				if totalCandidates == 1 {
					slog.Warn("executor: circuit open on sole candidate, failing open",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
						"raw_model", cand.RawModel,
						"client_model", params.ClientModel,
						"request_id", params.RequestID,
					)
					trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
						ProviderID:   cand.ProviderID,
						CredentialID: cand.CredentialID,
						RawModel:     cand.RawModel,
						Tier:         cand.Tier,
						Reason:       "circuit_open_sole_candidate_fail_open",
					})
					// intentionally fall through — Limiter + real attempt below
				} else {
					slog.Debug("executor: circuit open, skipping candidate",
						"credential_id", cand.CredentialID,
						"provider_id", cand.ProviderID,
					)
					lastErr = fmt.Errorf("circuit open for credential %d", cand.CredentialID)
					releaseFpLease(e.FpSlots, fpLease)
					continue
				}
			}
			// Kill-switch path: skip circuit and fall through to Limiter.AcquireAll.
		}

		release, acquireErr := e.Limiter.AcquireAll(
			params.R.Context(),
			cand.ProviderID,
			cand.CredentialID,
			params.ClientID.IdentityHash,
			params.KeyID,
			params.KeyConcurrentLimit,
			cand.RPMLimit, // 2026-07-15: per-credential RPM cap (migration 407)
		)
		if acquireErr != nil {
			slog.Debug("executor: concurrency or RPM limit, skipping candidate",
				"credential_id", cand.CredentialID,
				"err", acquireErr.Error(),
			)
			lastErr = acquireErr
			releaseFpLease(e.FpSlots, fpLease)
			// 2026-07-03 incident root cause: the probe slot was consumed by
			// Allow() above but never released here — the breaker stayed in
			// HALF_OPEN until process restart. Release it so the next request
			// can probe again.
			if probeConsumed && e.Circuit != nil {
				e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			}
			continue
		}

		var execErr error
		var result *ExecuteResult

		// PR-4 (T4 P0, 2026-06-23): stamp the per-attempt start so the
		// failure log (and OnError telemetry) can record how long this
		// single upstream call took, excluding the cost of candidate
		// selection and switching. Internal retry of the SAME credential
		// is included — that work belongs to the candidate, not streaming.
		// See db/migrations/300_candidate_failure_logs_per_attempt_latency.sql
		// for the column semantic.
		attemptStart := time.Now()

		// 2026-06-23 fix: wrap execute calls to ensure cleanup happens
		// even if executeAnthropic/executeOpenAI returns early (e.g.
		// executor_anthropic.go:468). Without this, resources leak.
		func() {
			// Track resources that must be released
			releasePeak := e.PeakCollector != nil
			if releasePeak {
				e.PeakCollector.Acquire(int64(cand.CredentialID), cand.RawModel)
			}

			// Ensure cleanup via defer (executes even on early return)
			defer func() {
				if releasePeak {
					e.PeakCollector.Release(int64(cand.CredentialID), cand.RawModel)
				}
				release()
				releaseFpLease(e.FpSlots, fpLease)
			}()

			// Execute the actual call
			// ── 2026-07-17: trace.upstream_request ────────────────────────────────
			// 在每个候选凭据真正向上游发出 HTTP 前, 记录目标 URL 与超时。
			// 若凭据全部失败(loop 退出), trace 会显示一连串失败注入,
			// 便于运维看到"试过哪几个、哪个先出错"。
			e.emitTraceExec(params.R.Context(), params.RequestID,
				gwtrace.UpstreamRequest(cand.BaseURL, retryPerCred, fpLease != nil).
					WithDetails(
						"protocol", cand.Protocol,
						"raw_model", cand.RawModel,
						"credential_id", cand.CredentialID,
					))

			// ── multi-key rotation (OmniRoute apiKeyRotator 对齐) ─────────────
			// 若 credential 挂了多个 key, 在 BuildRequest 前用 KeyRotator 选一个
			// healthy key (round-robin, 跳过 invalid/terminal), 重写 cand.APIKey.
			// 单 key credential (KeyRotator==nil) 走原路径, 零开销.
			// resolvedKeyIdx 在循环体作用域内声明, 失败路径用它 RecordKeyFailure.
			if cand.KeyRotator != nil {
				idx := cand.KeyRotator.ResolveKey(cand.CredentialID, -1)
				if idx < 0 {
					// 全部 key invalid/terminal → credential 整体不可用, 跳到下一候选.
					execErr = fmt.Errorf("keyrotator: all keys exhausted for credential %d", cand.CredentialID)
				} else if idx >= 1 && idx-1 < len(cand.APIKeys) {
					cand.APIKey = cand.APIKeys[idx-1] // rotator idx 0 = primary, 1..N = APIKeys[0..N-1]
				}
				resolvedKeyIdx = idx
			}

			if execErr == nil {
				// ── MM-1/MM-2 outbound attachment transforms ─────────────────
				// Per-candidate: derive the attempt body from the ORIGINAL
				// body so a failover from a URL-mode provider to a
				// data-URI-only provider never inherits rewritten URLs.
				execParams := params
				if e.AttachmentURLRewriter != nil && len(params.AttachmentMetadata) > 0 {
					var newBody []byte
					var n int
					if params.ClientProtocol == "anthropic-messages" {
						// E-P2-3 (doc 20): Anthropic-protocol clients bridged
						// to a URL-mode OpenAI provider — rewrite the
						// Anthropic base64 source blocks to url sources
						// before the bridge conversion maps them to
						// image_url. (For anthropic-messages upstreams the
						// matrix gives anthropic a data-URI preference, so
						// this is a no-op there.)
						newBody, n = e.AttachmentURLRewriter.RewriteAnthropicBody(
							params.BodyBytes, params.AttachmentMetadata, cand.CatalogCode)
					} else {
						newBody, n = e.AttachmentURLRewriter.RewriteOpenAIBody(
							params.BodyBytes, params.AttachmentMetadata, cand.CatalogCode)
					}
					if n > 0 {
						cp := *params
						cp.BodyBytes = newBody
						execParams = &cp
					}
				}
				// MM-2 (doc 19): URL 拉取回退——目标 provider 矩阵判定不
				// 支持 url source 而出站 body 以网关 URL 引用附件时，取回
				// 内容重新内联 base64。flag-off（nil）零开销直通。
				if e.AttachmentURLFetchFallback != nil {
					if newBody, n := e.AttachmentURLFetchFallback.InlineOpenAIBody(
						execParams.BodyBytes, cand.CatalogCode); n > 0 {
						cp := *execParams
						cp.BodyBytes = newBody
						execParams = &cp
					}
				}
				switch cand.Protocol {
				case "anthropic-messages":
					result, execErr = e.executeAnthropic(execParams, cand, retryPerCred, tTotal, fpLease)
				default:
					result, execErr = e.executeOpenAI(execParams, cand, retryPerCred, tTotal, fpLease)
				}
			}
		}()

		if execErr == nil {
			result.StickyHit = stickyHitForChosen(stickyCredID, cand.CredentialID)
			sideEffectCtx, sideEffectCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
			defer sideEffectCancel()
			e.restoreCredentialState(sideEffectCtx, cand.CredentialID, cand.StandardizedName)
			e.recordStickySuccess(params, cand.CredentialID)
			// 2026-07-24: URSM v2 authoritative 模式下跳过 fpSlotRecorder 写入
			// 统一状态管理到 URSM v2，避免多路径写入导致状态不一致
			if e.Recorder != nil && e.legacyWritersEnabled() {
				e.Recorder.RecordSuccess(sideEffectCtx, cand.CredentialID, cand.RawModel)
			}
			// multi-key: mark the resolved key healthy (clears warning/invalid
			// after a single success). No-op for single-key credentials.
			if cand.KeyRotator != nil && resolvedKeyIdx >= 0 {
				cand.KeyRotator.RecordKeySuccess(cand.CredentialID, resolvedKeyIdx)
			}
			// Record success for Bandit scoring (Thompson Sampling)
			e.recordBanditSuccess(cand.CredentialID, result.LatencyMs)
			// Clear node_probe_state failure gate when a real business
			// request succeeds on a credential that was previously
			// probe-failed. This prevents transient probe timeouts
			// (e.g. NVIDIA NIM cold-start >15s) from permanently
			// excluding healthy credentials from routing.
			if e.NodeProbeHealthy != nil && cand.RawModel != "" {
				_ = e.NodeProbeHealthy(sideEffectCtx, cand.CredentialID, cand.RawModel)
			}
			// Step 6 (2026-06-18): a successful response on this
			// credential clears its model_not_found streak. The next
			// request from this sticky session will not be tripped by
			// a stale counter from a prior intermittent failure.
			e.resetMnfStreak(params, cand.CredentialID)

			// Record successful call for health tracking
			if e.HealthTracker != nil {
				// PR-4 (T4 P0, 2026-06-23): use the real X-Request-Id
				// header so Redis callhist entries join back to
				// request_logs.request_id. The previous synthetic
				// "req_{cred}_{ns}" made correlation impossible. Fall
				// back to a labelled async id when the header is absent
				// (e.g. internal / synthetic calls with no HTTP request).
				requestID := params.R.Header.Get("X-Request-Id")
				if requestID == "" {
					requestID = "async-" + time.Now().Format("20060102T150405.000")
				}
				e.HealthTracker.OnSuccess(
					sideEffectCtx,
					cand.CredentialID,
					cand.StandardizedName,
					result.LatencyMs,
					requestID,
				)
			}

			// 2026-06-28: Real-time success feedback to UnifiedProbeScheduler.
			// Successful requests immediately mark suspicious/failing models
			// as healthy, reducing false negatives and unnecessary probes.
			if e.UnifiedProbeScheduler != nil {
				e.UnifiedProbeScheduler.OnRealRequest(
					sideEffectCtx,
					int64(cand.CredentialID),
					candidateRawModel(cand),
					true, // success
					"",
				)
			}

			// 2026-07-01 Phase 2.x: Record success in credential state manager.
			// This enables adaptive probing based on real request outcomes.
			// 2026-07-24: URSM v2 authoritative 模式下跳过 StateObserver
			if e.StateObserver != nil && e.legacyWritersEnabled() {
				requestID := params.R.Header.Get("X-Request-Id")
				if requestID == "" {
					requestID = "async-" + time.Now().Format("20060102T150405.000")
				}
				e.StateObserver.UpdateOnSuccess(
					sideEffectCtx,
					cand.CredentialID,
					candidateRawModel(cand),
					result.LatencyMs,
					requestID,
				)
			}

			// 2026-07-24: URSM v2 authoritative 模式下跳过 RoutingStateShadow
			if e.RoutingStateShadow != nil && e.legacyWritersEnabled() {
				e.RoutingStateShadow.ObserveState(routingstate.Evidence{
					CredentialID:     cand.CredentialID,
					RawModelName:     candidateRawModel(cand),
					CanonicalName:    params.ClientModel,
					Scope:            routingstate.ScopeModel,
					Source:           routingstate.SourceRequest,
					ObservedAt:       time.Now(),
					CorrelationID:    params.RequestID,
					BindingAvailable: boolPtrCompat(true),
				})
			}

			// 2026-07-26 URSM v1→v2 统一: 唯一的状态写入入口是 URSMv2。
			// v1 (domains/ursm) 已迁入 _to-be-deprecated/，executor 不再走
			// `shouldWriteLegacyURSM` 旁路。Manager.RecordRequest 内部根据
			// rollout 模式 (off/shadow/canary/authoritative) 自决是否真写。
			requestID := params.R.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = "async-" + time.Now().Format("20060102T150405.000")
			}
			if e.URSMv2 != nil {
				if err := e.URSMv2.RecordRequest(params.R.Context(), ursmv2api.RequestOutcome{
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					TenantID:     params.TenantID,
					BillingMode:  cand.BillingMode,
					Success:      true,
					LatencyMs:    result.LatencyMs,
					RequestID:    requestID,
				}); err != nil {
					slog.Warn("ursm.v2: record success failed",
						"error", err, "request_id", requestID, "credential_id", cand.CredentialID)
				}
			}

			trace.Chosen = &TraceCandidate{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Tier:         cand.Tier,
				Reason:       "succeeded",
			}
			result.Trace = trace
			if params.AuditBuilder != nil {
				params.AuditBuilder.DecisionTrace(trace)
			}
			return result, nil
		}

		if mnf, ok := execErr.(*modelNotFoundError); ok {
			mnfCtx, mnfCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
			defer mnfCancel()
			// ModelNotFound / ModelDeprecated is a provider/model compatibility
			// failure, not a client bug. Record it at model-binding scope
			// immediately so this credential/model pair leaves the candidate
			// pool while the probe worker determines whether the offer has
			// recovered. mnfKind distinguishes "model name unknown" (7-day
			// cooling) from "model permanently end-of-lifed" (30-day cooling).
			mnfKind := mnf.resolvedKind()
			e.recordModelNotFound(mnfCtx, mnf.credentialID, mnf.rawModel, mnf.body, mnf.status, mnfKind)
			e.writeCredentialStateOnError(mnfCtx, mnf.credentialID, cand.StandardizedName, mnfKind, execErr)
			// Step 6 (2026-06-18): MnfStreak — client hot-path break
			// for persistent (not intermittent) model_not_found. The
			// background probe consensus (bg/model_probe.go) owns
			// authoritative credential health; the streak owns the
			// user experience when the upstream has clearly gone
			// away.
			e.recordMnfStreak(params, cand.CredentialID)

			lastErr = execErr
			lastKind = recordLastKind(lastKind, mnfKind)
			attempts = append(attempts, AttemptRecord{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Kind:         mnfKind,
				Reason:       mnf.body,
			})
			// 2026-07-03 incident fix: mnf is a credential-healthy skip (no
			// RecordFailure), so release a consumed half-open probe here.
			if probeConsumed && e.Circuit != nil {
				e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			}
			continue
		}

		// 2026-06-15: IsClientBug kinds (tool_call_id_mismatch,
		// unsupported_feature, canceled) are upstream-side format
		// rejections. The credential is healthy, the candidate list is
		// correct, but THIS particular upstream doesn't support the
		// request shape (e.g. minimax-anthropic rejects the openai-
		// style tool wrapper the chat->anthropic Q3 converter emits).
		// Without this branch, the executor would record a sticky
		// failure, mark the credential cooling, and bubble 502 to
		// the client — even when a downstream openai-completions
		// candidate could have served the request via Q2 reverse
		// conversion. Skip all side effects and continue to the
		// next candidate so the next credential gets a turn.
		{
			kind := classifyExecError(execErr)
			if errorsx.IsClientBug(kind) {
				slog.Warn("executor: client-bug kind, trying next candidate",
					"kind", kind,
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"raw_model", cand.RawModel,
					"err", execErr.Error(),
				)
				lastErr = execErr
				lastKind = recordLastKind(lastKind, kind)
				attempts = append(attempts, AttemptRecord{
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					Kind:         kind,
					Reason:       execErr.Error(),
				})
				// 2026-07-03 incident fix: client-bug is a credential-healthy
				// skip (no RecordFailure), so release a consumed half-open probe.
				if probeConsumed && e.Circuit != nil {
					e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
				}
				continue
			}
		}

		// contextLengthExhaustedError: handleContextLengthRecovery
		// gave up after mechanical trim + the multi-model LLM-summary
		// chain. This is a property of the model the client asked for
		// (its context window is too small for the body), not of the
		// credential serving it. Skip the circuit / sticky / disable-
		// model-offer side effects so the credential stays routable,
		// and let the next candidate (different credential for the
		// same model) have a turn. If all candidates fail with this
		// same kind, the final 4xx bubbles up to the client with the
		// last credential's body as the reason.
		if cle, ok := execErr.(*contextLengthExhaustedError); ok {
			var ctxWindow int
			if cand.ContextWindow != nil {
				ctxWindow = *cand.ContextWindow
			}
			slog.Warn("candidate exhausted context window, trying next credential",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"raw_model", cand.RawModel,
				"context_window", ctxWindow,
				"status", cle.status,
				"body_preview", cle.body,
			)
			lastErr = execErr
			lastKind = recordLastKind(lastKind, errorsx.KindContextLength)
			attempts = append(attempts, AttemptRecord{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Kind:         errorsx.KindContextLength,
				Reason:       cle.body,
			})
			// 2026-07-03 incident fix: context-length is a credential-healthy
			// skip (no RecordFailure), so release a consumed half-open probe.
			if probeConsumed && e.Circuit != nil {
				e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			}
			continue
		}

		// Content moderation / safety-policy rejection (MiniMax 422
		// "new_sensitive (1026)", OpenAI "content_filter", etc).
		// Content-determined: the same prompt is rejected on every
		// sibling credential of the same provider, so short-circuit
		// within the same provider — do NOT waste upstream quota retrying
		// siblings that will also reject for the same content.
		//
		// OPT-3 (2026-07-12): the previous implementation used `break`
		// which exited the entire candidate loop. That was wrong when
		// the candidate list spans multiple providers: provider-2's
		// credentials do NOT share provider-1's content policy and may
		// serve the request. The fix is to skip only the remaining
		// candidates with the same ProviderID. If a different provider
		// later returns content_filter, that provider is short-circuited
		// independently. The handler still renders a 400 with the first
		// upstream reason — lastErr / lastKind are set below.
		//
		// Skip all side effects (circuit / sticky / state) — the
		// credential is healthy, the content is the problem. The
		// handler renders a 400 with the upstream reason + an
		// actionable hint via KindContentFilter.
		if kind, ok := classifyContentFilterError(execErr); ok {
			slog.Info("executor: content_filter rejection, short-circuiting same-provider candidates",
				"kind", kind,
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"raw_model", cand.RawModel,
				"tried", tried,
				"err", execErr.Error(),
			)
			lastErr = execErr
			lastKind = recordLastKind(lastKind, kind)
			// 2026-07-24: URSM v2 authoritative 模式下跳过 RoutingStateShadow
			if e.RoutingStateShadow != nil && e.legacyWritersEnabled() {
				e.observeRoutingStateFailure(params, cand, kind)
				if !errorsx.IsClientBug(kind) {
					e.observeRoutingStateProbe(params, cand, routingstate.ProbeTriggerRequestFailure)
				}
			}

			attempts = append(attempts, AttemptRecord{
				ProviderID:   cand.ProviderID,
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				Kind:         kind,
				Reason:       execErr.Error(),
			})
			// OPT-3: record the provider so remaining siblings can be
			// skipped without re-running classifyContentFilterError.
			contentFilterProviders[cand.ProviderID] = struct{}{}
			// 2026-07-03 incident fix: content_filter is a credential-healthy
			// skip (no RecordFailure), so release a consumed half-open probe.
			if probeConsumed && e.Circuit != nil {
				e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			}
			// Skip siblings of the same provider. Use continue-with-label
			// so the outer for loop can iterate to the next provider.
			continue
		}

		if sie, ok := execErr.(*streamInterruptedError); ok {
			if isClientStreamInterruption(sie.kind, sie.reason) {
				// The caller disconnected. Preserve the error for the handler, but
				// do not classify a broken pipe as an upstream failure or penalize
				// the credential that was serving it.
				slog.Info("executor: client disconnected during stream",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
				)
				// 2026-07-03 incident fix: no Record* on this path, so release a
				// consumed half-open probe before returning.
				if probeConsumed && e.Circuit != nil {
					e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
				}
				return nil, execErr
			}
			kind := sie.kind
			if kind == "" {
				kind = errorsx.KindStreamTimeout
			}
			failureCtx, failureCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
			defer failureCancel()
			e.recordStickyFailure(params, cand.CredentialID, kind)
			// 2026-07-24: URSM v2 authoritative 模式下跳过 fpSlotRecorder 写入
			if e.Recorder != nil && e.legacyWritersEnabled() {
				e.Recorder.RecordFailure(failureCtx, cand.CredentialID, cand.RawModel, kind)
			}

			// 2026-07-01 Phase 2.x: Record failure in credential state manager.
			// KindCanceled is automatically skipped by the manager.
			// 2026-07-24: URSM v2 authoritative 模式下跳过 StateObserver
			if e.StateObserver != nil && e.legacyWritersEnabled() {
				requestID := params.R.Header.Get("X-Request-Id")
				if requestID == "" {
					requestID = "async-" + time.Now().Format("20060102T150405.000")
				}
				e.StateObserver.UpdateOnFailure(
					failureCtx,
					cand.CredentialID,
					candidateRawModel(cand),
					kind,
					requestID,
					params.TenantID,
					cand.BillingMode,
				)
			}

			if e.RoutingStateShadow != nil && e.legacyWritersEnabled() {
				e.observeRoutingStateFailure(params, cand, kind)
				e.observeRoutingStateProbe(params, cand, routingstate.ProbeTriggerRequestFailure)
			}

			// 2026-07-26 URSM v1→v2 统一: 唯一的状态写入入口是 URSMv2。
			// v1 (domains/ursm) 已迁入 _to-be-deprecated/，executor 不再走
			// `shouldWriteLegacyURSM` 旁路。Manager.RecordRequest 内部根据
			// rollout 模式自决是否真写（off/shadow 短路，canary/authoritative 落 Redis）。
			requestID := params.R.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = "async-" + time.Now().Format("20060102T150405.000")
			}
			if e.URSMv2 != nil {
				if err := e.URSMv2.RecordRequest(params.R.Context(), ursmv2api.RequestOutcome{
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					TenantID:     params.TenantID,
					BillingMode:  cand.BillingMode,
					Success:      false,
					LatencyMs:    0,
					ErrorKind:    string(kind),
					RequestID:    requestID,
				}); err != nil {
					slog.Warn("ursm.v2: record failure failed",
						"error", err, "request_id", requestID,
						"credential_id", cand.CredentialID, "error_kind", kind)
				}
			}

			if mayRetryInterruptedStream(params, sie) {
				// Stream is resumable and no semantic output was committed - try next candidate.
				// The inner tryCandidate already wrote the credential state
				// with the correct kind; here we just record the failure on
				// the circuit to keep the counter consistent. For
				// KindConcurrent we also re-affirm by writing state again
				// because the executor's outer loop now drives the failover
				// to the next candidate, and we want the DB state to be
				// authoritative before that next lookup.
				// 2026-08-08 P0 Fix: sole-candidate models must NOT escalate
				// their own circuit breaker on every transient failure —
				// otherwise a single transient blip wedges the only Claude/
				// GPT route for the entire 30-minute cooling window. We
				// still write the credential-state row above (admins see
				// it), still record bandit context, but skip the breaker
				// escalation so the next request can try again.
				if !freeCredentialsTolerateTransient(cand.BillingMode, kind) && totalCandidates > 1 {
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
				} else {
					if probeConsumed && e.Circuit != nil {
						e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
					}
				}
				e.recordBanditFailure(cand.CredentialID, kind)
				if kind == errorsx.KindConcurrent {
					e.writeCredentialStateOnError(failureCtx, cand.CredentialID, cand.StandardizedName, kind, execErr)
					e.forceUnpinOnFatalKindForTenant(failureCtx, holder, cand.CredentialID, kind, params.TenantID)
				} else if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
					e.writeCredentialStateOnError(failureCtx, cand.CredentialID, cand.StandardizedName, kind, execErr)
					e.forceUnpinOnFatalKindForTenant(failureCtx, holder, cand.CredentialID, kind, params.TenantID)
				}

				lastErr = execErr
				lastKind = recordLastKind(lastKind, kind)
				attempts = append(attempts, AttemptRecord{
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
					RawModel:     cand.RawModel,
					Kind:         kind,
					Reason:       sie.reason,
				})
				slog.Warn("candidate stream interrupted (resumable), trying next",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"reason", sie.reason,
					"kind", kind,
					"node_jump_from_cred", cand.CredentialID,
					"node_jump_from_prov", cand.ProviderID,
					"node_jump_attempt", nodeTracker.Attempts(),
				)
				// Notify handler to send thinking event to client
				if params.OnNodeJump != nil {
					msg := fmt.Sprintf("正在切换节点 (%d/%d)...", nodeTracker.Attempts(), len(candidates))
					params.OnNodeJump(msg)
				}
				// V3.3-OBS OBS-B1 (2026-08-15): node_switch（流中断可恢复切换）。
				e.emitNodeSwitch(params, cand.CredentialID, nextCandidateCredentialID(candidates, candidateIndex), string(kind), nodeTracker.Attempts())
				continue
			} else {
				// Stream is not resumable (too many chunks sent) - return error.
				// The inner tryCandidate already wrote the credential state
				// with the correct kind; this branch keeps the circuit counter
				// consistent and ensures the kind is recorded.
				// 2026-08-08 P0 Fix: sole-candidate models must NOT escalate
				// their circuit breaker — see mirror comment above.
				if !freeCredentialsTolerateTransient(cand.BillingMode, kind) && totalCandidates > 1 {
					e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
				} else {
					if probeConsumed && e.Circuit != nil {
						e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
					}
				}
				e.recordBanditFailure(cand.CredentialID, kind)
				if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
					e.writeCredentialStateOnError(failureCtx, cand.CredentialID, cand.StandardizedName, kind, execErr)
					e.forceUnpinOnFatalKindForTenant(failureCtx, holder, cand.CredentialID, kind, params.TenantID)
				}

				slog.Warn("candidate stream interrupted (non-resumable), returning error",
					"credential_id", cand.CredentialID,
					"provider_id", cand.ProviderID,
					"reason", sie.reason,
					"kind", kind,
				)
				return nil, execErr
			}
		}

		// Record node jump for monitoring (structured log only, not SSE)
		slog.Warn("candidate_failed_trying_next",
			"credential_id", cand.CredentialID,
			"provider_id", cand.ProviderID,
			"node_jump_from_cred", cand.CredentialID,
			"node_jump_from_prov", cand.ProviderID,
			"node_jump_attempt", nodeTracker.Attempts(),
		)
		// Notify handler to send thinking event to client
		if params.OnNodeJump != nil {
			msg := fmt.Sprintf("正在切换节点 (%d/%d)...", nodeTracker.Attempts(), len(candidates))
			params.OnNodeJump(msg)
		}
		// V3.3-OBS OBS-B1 (2026-08-15): node_switch（候选失败切换下一节点）。
		e.emitNodeSwitch(params, cand.CredentialID, nextCandidateCredentialID(candidates, candidateIndex), string(classifyExecError(execErr)), nodeTracker.Attempts())
		lastErr = execErr
		// Prefer the typed Kind from *upstreampkg.Error if available, to
		// avoid re-classifying from the error text (which embeds the
		// Kind in [brackets] and can trigger false-positive matches on
		// the concurrentOverload regex). classifyExecError uses errors.As so
		// a wrapped *upstreampkg.Error (fmt.Errorf("...: %w", ...)) is still
		// reached — a plain type assertion misses the wrapped case and would
		// fall back to the message-only classifier, mis-classifying the error.
		kind := classifyExecError(execErr)
		lastKind = recordLastKind(lastKind, kind)

		// ── 2026-07-19: trace.upstream_failure 详细记录上游错误 ────────────
		// 记录 upstream.Error 的完整上下文（StatusCode + Body），不只是 5xx 标签。
		e.emitTraceExec(params.R.Context(), params.RequestID,
			gwtrace.UpstreamFailureWithBody(cand.BaseURL, execErr).
				WithDetails(
					"credential_id", cand.CredentialID,
					"raw_model", cand.RawModel,
					"error_kind", string(kind),
					"attempt_index", tried,
				))

		// 2026-06-23 Phase 2 (P1): log the per-candidate failure to
		// candidate_failure_logs so operators can see WHICH credentials
		// were tried, in what order, and what the vendor actually
		// returned. request_logs only carries the LAST candidate's error
		// (visible in ExecuteError.Error) which is useless when a
		// 3-credential chain all fail differently.
		if e.FailureLogger != nil {
			// PR-4 (T4 P0, 2026-06-23): populate per_attempt_latency_ms
			// from the attemptStart stamped just before the upstream
			// call. This is the single-attempt latency (excludes
			// candidate-switching overhead). See migration 300.
			perAttemptMs := int(time.Since(attemptStart).Milliseconds())
			e.FailureLogger.LogFailure(
				params.R.Header.Get("X-Request-Id"),
				tenantFromCtx(params.R),
				cand.CredentialID,
				cand.ProviderID,
				cand.RawModel,
				tried,
				execErr,
				nil, // latency_ms: end-to-end candidate latency, not yet tracked here
				&perAttemptMs,
				buildEnhancedErrorContext(params, kind, execErr, len(candidates), tried),
			)
		} else {
			// 2026-07-13: defensive log when FailureLogger is nil
			// (diagnosis aid for candidate_failure_logs write gaps).
			slog.Warn("executor: FailureLogger is nil, candidate failure not logged",
				"credential_id", cand.CredentialID,
				"raw_model", cand.RawModel,
				"error_kind", kind)
		}

		failureCtx, failureCancel := runctx.DetachedTimeout(params.R.Context(), 5*time.Second)
		defer failureCancel()

		// URSM v2 is the sole request-health writer in authoritative mode.
		// Keep this sidecar detached and bounded like the stream interruption
		// path so a client cancellation cannot drop a normal failed attempt.
		if e.URSMv2 != nil {
			requestID := params.R.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = "async-" + time.Now().Format("20060102T150405.000")
			}
			if err := e.URSMv2.RecordRequest(params.R.Context(), ursmv2api.RequestOutcome{
				CredentialID: cand.CredentialID,
				RawModel:     cand.RawModel,
				TenantID:     params.TenantID,
				BillingMode:  cand.BillingMode,
				Success:      false,
				LatencyMs:    int(time.Since(attemptStart).Milliseconds()),
				ErrorKind:    string(kind),
				RequestID:    requestID,
			}); err != nil {
				slog.Warn("ursm.v2: record failure failed",
					"error", err, "request_id", requestID,
					"credential_id", cand.CredentialID, "error_kind", kind)
			}
		}

		// Record failed call for health tracking
		if e.HealthTracker != nil {
			// PR-4 (T4 P0, 2026-06-23): real X-Request-Id so Redis
			// callhist entries join back to request_logs.request_id.
			// See OnSuccess path for the rationale on the async-*
			// fallback.
			requestID := params.R.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = "async-" + time.Now().Format("20060102T150405.000")
			}
			e.HealthTracker.OnError(
				failureCtx,
				cand.CredentialID,
				cand.StandardizedName,
				kind,
				requestID,
			)
		}

		// 2026-06-28: Real-time request feedback to UnifiedProbeScheduler.
		// Failures are marked as urgent priority for <30s re-validation,
		// preventing the 2-hour blind window of the legacy system.
		// Only report credential-level issues (not client bugs).
		if e.UnifiedProbeScheduler != nil && !errorsx.IsClientBug(kind) {
			e.UnifiedProbeScheduler.OnRealRequest(
				failureCtx,
				int64(cand.CredentialID),
				candidateRawModel(cand),

				false, // failure
				execErr.Error(),
			)
		}

		attempts = append(attempts, AttemptRecord{
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
			RawModel:     cand.RawModel,
			Kind:         kind,
			Reason:       execErr.Error(),
		})
		e.recordStickyFailure(params, cand.CredentialID, kind)

		// 2026-07-09: 更新会话黑名单计数
		sessionBlacklist[cand.CredentialID]++

		// 2026-07-24: URSM v2 authoritative 模式下跳过 fpSlotRecorder 写入
		if e.Recorder != nil && e.legacyWritersEnabled() {
			e.Recorder.RecordFailure(failureCtx, cand.CredentialID, cand.RawModel, kind)
		}

		// 2026-07-01 Phase 2.x: Record failure in credential state manager.
		// KindCanceled is automatically skipped by the manager.
		// 2026-07-24: URSM v2 authoritative 模式下跳过 StateObserver
		if e.StateObserver != nil && e.legacyWritersEnabled() {
			requestID := params.R.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = "async-" + time.Now().Format("20060102T150405.000")
			}
			e.StateObserver.UpdateOnFailure(
				failureCtx,
				cand.CredentialID,
				candidateRawModel(cand),

				kind,
				requestID,
				params.TenantID,
				cand.BillingMode,
			)
		}

		// 2026-07-14: 免费凭据 transient 错误不 RecordFailure（避免 circuit
		// breaker OPEN 硬剔），仅靠 RecentSuccessRate 软降权。永久错误仍记录。
		// 2026-08-08 P0 Fix: sole-candidate models also skip the breaker
		// escalation so a transient blip doesn't lock the only Claude/GPT
		// route for 30 minutes.
		//
		// 2026-08-10 multi-key (OmniRoute A3 guard): 先把失败记到 per-key 健康,
		// 只有当该 credential 的所有 key 都 invalid/terminal 时才上抛到 credential
		// 级 breaker. 这样一个 key 余额耗尽不会毒化整个 credential, 其他 key 继续
		// 服务 — N 倍放大免费额度的关键.
		propagateToBreaker := !freeCredentialsTolerateTransient(cand.BillingMode, kind) && totalCandidates > 1
		if cand.KeyRotator != nil && resolvedKeyIdx >= 0 {
			cand.KeyRotator.RecordKeyFailure(cand.CredentialID, resolvedKeyIdx, kind)
			if !cand.KeyRotator.AllKeysInvalid(cand.CredentialID) {
				propagateToBreaker = false
			}
		}
		if propagateToBreaker {
			e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
		} else {
			// 2026-07-03 incident fix: free-cred transient skips RecordFailure
			// (soft-downgrade only), so a consumed half-open probe would leak.
			if probeConsumed && e.Circuit != nil {
				e.Circuit.ReleaseProbe(cand.ProviderID, cand.CredentialID)
			}
		}
		e.recordBanditFailure(cand.CredentialID, kind)
		trace.BlockedCandidates = append(trace.BlockedCandidates, TraceCandidate{
			ProviderID:   cand.ProviderID,
			CredentialID: cand.CredentialID,
			RawModel:     cand.RawModel,
			Tier:         cand.Tier,
			Reason:       fmt.Sprintf("request_failed:%s", kind),
		})
		if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
			e.writeCredentialStateOnError(failureCtx, cand.CredentialID, cand.StandardizedName, kind, execErr)
			e.forceUnpinOnFatalKindForTenant(failureCtx, holder, cand.CredentialID, kind, params.TenantID)
		}

		// ── Fatal 凭证错误：透明切换到下一个候选人（前端无感知）────────
		// KindAuth/KindAuthRevoked/KindQuota* 等 fatal 错误表示 provider 侧问题
		//（如账户被封、配额耗尽），不影响其他候选人的可用性。
		// 对这些错误添加 continue，让主候选人循环尝试下一个 provider，
		// 配合 sync retry 循环（1s 间隔重试，最多 3 轮），实现对前端的透明切换。
		// 注意：流式请求不进入 sync retry 循环，所以这里的 continue 是流式的唯一保障。
		if errorsx.IsCredentialFatal(kind) {
			slog.Warn("executor: credential fatal error, trying next candidate",
				"kind", kind,
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"err", execErr.Error(),
			)
			continue
		}

		// 2026-07-14 fix: transient / rate_limit / timeout 也应透明 failover
		// 到下一个候选。之前这些错误会短路 break 进入 sync retry，但 sync
		// retry 重新推导的候选列表中同一凭据仍然 available → 无限重试同一
		// 凭据 → credential 23 (NVIDIA nvidia-latest) 永远不被尝试。
		//
		// 现在对这些 transient 类错误也 continue，让执行器遍历到下一个候选。
		// 场景：credential 21 (MiniMax prod-v2) rate_limit → credential 19
		// (NVIDIA endless) transient → credential 23 (NVIDIA nvidia-latest)
		// → 成功！
		if isTransientFailoverKind(kind) {

			// Track the last KindTransient credential for inline probe
			// in the sync retry loop. We specifically track KindTransient
			// (5xx overlay) because the sync retry loop's 1s window is
			// shorter than the ActiveProbeWorker's 5s backoff, so the
			// credential would still be in cooling state when re-planned.
			if kind == errorsx.KindTransient {
				lastTransientCred = credentialstate.NoCandidatesCandidate{
					CredentialID: cand.CredentialID,
					ProviderID:   cand.ProviderID,
					RawModel:     candidateRawModel(cand),
					BillingMode:  cand.BillingMode,
				}
			}

			slog.Warn("executor: transient error, trying next candidate",
				"kind", kind,
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"err", execErr.Error(),
			)
			continue
		}
	}

	trace.FailureReason = "all_candidates_failed"
	if params.AuditBuilder != nil {
		params.AuditBuilder.DecisionTrace(trace)
	}

	// ── 单节点5xx快速恢复：立即probe，成功则直接重试 ──────────
	// 2026-07-18: 单节点场景下，5xx后立即inline probe（不等1s），
	// 成功后直接递归重试原请求，避免进入sync retry loop的等待。
	// 这解决了单节点场景下前端超时导致credential被降级的问题。
	if len(candidates) == 1 && lastKind == errorsx.KindTransient &&
		e.ProbeSync != nil && lastTransientCred.CredentialID != 0 &&
		e.asyncDepth.Load() < 3 && !params.SurvivalAttempt {

		slog.Info("single_node_5xx_immediate_probe",
			"credential_id", lastTransientCred.CredentialID,
			"model", params.ClientModel,
			"raw_model", lastTransientCred.RawModel,
		)

		probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		recovered := e.ProbeSync(probeCtx, []credentialstate.NoCandidatesCandidate{lastTransientCred}, params.TenantID, params.RequestID)
		probeCancel()

		if recovered {
			slog.Info("single_node_recovered_immediate_retry",
				"credential_id", lastTransientCred.CredentialID,
				"model", params.ClientModel,
				"elapsed_ms", time.Since(tTotal).Milliseconds(),
			)
			// 递归重试（asyncDepth已限制深度避免无限递归）
			return e.Execute(params)
		}

		slog.Warn("single_node_probe_failed_fallback_to_sync_retry",
			"credential_id", lastTransientCred.CredentialID,
			"model", params.ClientModel,
		)
	}

	// ── 同步重试：非流式请求，全候选失败后保持连接继续重试 ──────────
	// 2026-06-21: 客户端在等待，不返回错误、不启动异步 goroutine，
	// 而是保持 HTTP 连接，继续同步重试候选。客户端断开时自动停止。
	// 2026-07-03: 增加最多 3 轮重试限制（加主循环共 4 轮），避免死循环。
	const maxSyncRetryRounds = 3
	if !params.PreStreamPrepared && !params.SurvivalAttempt && e.SyncRetryTimeout > 0 && tried > 0 {
		retried := 0
		retryRound := 0
		e.asyncDepth.Add(1)

		deadline := time.Now().Add(e.SyncRetryTimeout)

	syncRetryLoop:
		for time.Now().Before(deadline) && retryRound < maxSyncRetryRounds {
			retried++
			retryRound++

			// 检查客户端是否已断开
			if err := params.R.Context().Err(); err != nil {
				slog.Info("sync_retry_stopped",
					"model", params.ClientModel,
					"reason", "client_disconnect",
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
					"tried", tried,
					"retried", retried,
				)
				break syncRetryLoop
			}

			// 间隔等待（可被 ctx 中断）
			// 2026-07-03: Bug #12 fix - 降低重试间隔从5s到1s，减少白等时间
			// 2026-07-18: 进一步降低到500ms，配合单节点快速恢复机制
			select {
			case <-params.R.Context().Done():
				slog.Info("sync_retry_stopped",
					"model", params.ClientModel,
					"reason", "client_disconnect",
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
				)
				break syncRetryLoop
			case <-time.After(500 * time.Millisecond):
			}

			if err := params.R.Context().Err(); err != nil {
				break syncRetryLoop
			}

			// Inline probe: if the previous round had a KindTransient (5xx)
			// failure, synchronously probe that credential before
			// re-planning. This bridges the gap between the 5s
			// ActiveProbeWorker backoff and the 1s retry interval so the
			// credential can recover within the sync retry window.
			if e.ProbeSync != nil && lastTransientCred.CredentialID != 0 {
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
				recovered := e.ProbeSync(probeCtx, []credentialstate.NoCandidatesCandidate{lastTransientCred}, params.TenantID, params.RequestID)
				probeCancel()
				if recovered {
					slog.Info("sync_retry: transient credential recovered by inline probe",
						"credential_id", lastTransientCred.CredentialID,
						"raw_model", lastTransientCred.RawModel,
					)
				}
			}

			// 重新推导候选。
			// - 对于 credential-fatal 错误（quota_exceeded, auth_revoked），
			//   传 nil sticky 强制切换到其他凭据（避免重试 3 次都打到同一个已耗尽配额的凭据）
			// - 对于 transient/client-bug 错误，保留 sticky 维持会话连续性
			//
			// 2026-07-03: 修复配额耗尽后重试仍路由到同一凭据的问题。
			// 之前无条件保留 sticky（2026-06-24），导致 quota_exceeded 后
			// 重试 3 次都打到同一凭据。现在根据 lastKind 判断：
			// credential-fatal → 强制切换；其他 → 保留 sticky。
			var retryStickyID *int
			if !ratelimit.IsRateLimitEnabled() {
				retryStickyID = nil
			} else if errorsx.IsCredentialFatal(lastKind) {
				retryStickyID = nil // 强制切换
			} else {
				retryStickyID = e.pickStickyCredentialID(params) // 保留（与首次 Execute() 同口径：L1→L2→L3）
			}
			subCandidates := e.Router.PlanCandidatesWithContext(
				params.R.Context(), params.Candidates, retryStickyID, params.Policy,
				egressPref(params.Transform), params.TenantID, params.ClientModel, params.RequestID,
			)

			if len(subCandidates) == 0 {
				continue // 路由器也没候选，下一轮再试
			}

			// 2026-07-03: Bug #12 fix - 如果所有候选都是circuit open，提前退出
			// 避免白等3轮 × 1s = 3s（之前是15s）
			// 2026-07-03 incident fix: use PeekAllowed() (read-only, does NOT
			// consume the half-open probe slot) here — calling Allow() in this
			// pre-check would consume the probe and then the recursive
			// Execute() below would see it as still-open, wasting the probe.
			allCircuitOpen := true
			for _, cand := range subCandidates {
				if e.Circuit != nil && e.Circuit.PeekAllowed(cand.ProviderID, cand.CredentialID) {
					allCircuitOpen = false
					break
				}
			}
			if allCircuitOpen {
				// 2026-08-08 P0 Fix: do NOT break the sync retry when the
				// router only returned ONE candidate. Otherwise every
				// Claude/GPT request stalls inside a circuit that the
				// outer loop already failed-open for, but the sync-retry
				// loop refuses to admit the recursive attempt. With a
				// sole candidate, "all_circuit_open" is the only signal
				// we have — keep retrying via the recursive Execute so
				// the active_probe / node_probe_worker can drive recovery
				// on the next cycle instead of 503ing immediately.
				if len(subCandidates) > 1 {
					slog.Info("sync_retry_stopped",
						"model", params.ClientModel,
						"reason", "all_circuit_open",
						"elapsed_ms", time.Since(tTotal).Milliseconds(),
					)
					break syncRetryLoop
				}
				slog.Warn("sync_retry_continuing_sole_candidate_all_circuit_open",
					"model", params.ClientModel,
					"credential_id", subCandidates[0].CredentialID,
					"request_id", params.RequestID,
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
				)
			}

			// 递归执行 Execute()
			// asyncDepth>0 阻止内层触发 async 回退或嵌套 sync 重试
			subParams := *params
			subParams.Candidates = subCandidates
			result, retryErr := e.Execute(&subParams)
			if retryErr == nil {
				e.asyncDepth.Add(-1)
				slog.Info("sync_retry_succeeded",
					"model", params.ClientModel,
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
					"retried", retried,
				)
				return result, nil
			}
			// 更新 lastErr/LastKind（递归返回的是 ExecuteError 类型）。
			// LastKind 用 recordLastKind 折叠而不是直接覆写：子 Execute 内部
			// 已经做过同样的折叠，但父层这一轮可能已经见过更有诊断价值的
			// kind（例如父层 quota 耗尽、子层重试只碰到 transient 5xx），
			// 直接覆写会把真实原因降级成"稍后重试"。
			if execErrTyped, ok := retryErr.(*ExecuteError); ok {
				lastErr = execErrTyped.LastErr
				lastKind = recordLastKind(lastKind, execErrTyped.LastKind)
				attempts = append(attempts, execErrTyped.Attempts...)
				tried += execErrTyped.Tried
				if execErrTyped.Trace != nil {
					trace.BlockedCandidates = append(trace.BlockedCandidates, execErrTyped.Trace.BlockedCandidates...)
				}
			}
		}

		if params.R.Context().Err() == nil {
			slog.Warn("sync_retry_exhausted_fallthrough_async",
				"model", params.ClientModel,
				"elapsed_ms", time.Since(tTotal).Milliseconds(),
				"tried", tried,
				"retried", retried,
				"last_kind", lastKind,
			)
		}
		// 同步重试耗尽：客户端仍在等待 → 清除 asyncDepth 后走到 async 回退路径
		// 不再直接返回 ExecuteError（旧行为）
		e.asyncDepth.Add(-1)
		trace.FailureReason = "sync_retry_exhausted"
		if params.R.Context().Err() != nil {
			return nil, &ExecuteError{
				LastErr:   fmt.Errorf("sync retry exhausted after %v: %w", time.Since(tTotal), lastErr),
				Tried:     tried,
				Exhausted: true,
				Trace:     trace,
				Attempts:  attempts,
				LastKind:  lastKind,
			}
		}
	}

	// ── Cross-provider model fallback (Phase 2, 2026-07-19) ──
	// All primary candidates exhausted after sync retry. Before handing
	// off to async, try equivalent models from other providers. This is
	// the single biggest success-rate improvement: a full-provider outage
	// (e.g. Anthropic down) does not block the request — the executor
	// transparently retries on an equivalent model from a different
	// provider (e.g. gpt-4o).
	//
	// InFallback prevents infinite recursion; the chain only applies to
	// the outermost Execute call. Recursive Execute inherits
	// InFallback=true so it never re-enters this block.
	//
	// tried > 0 ensures we only fallback when real failures occurred,
	// not when no candidates were available (routing misconfiguration).
	if !params.InFallback && !params.SurvivalAttempt && tried > 0 && len(e.ModelFallbackChain) > 0 {
		fbModels := e.ModelFallbackChain[params.ClientModel]
		for _, fbModel := range fbModels {
			if params.R.Context().Err() != nil {
				break
			}
			fbCtx, fbCancel := runctx.DetachedTimeout(params.R.Context(), 2*time.Second)
			fbCandidates, _, fbErr := e.Provider.GetCandidates(
				fbCtx, fbModel,
				params.ClientID.Fingerprint.ClientProfile,
				params.TenantID,
			)
			fbCancel()
			if len(fbCandidates) == 0 || fbErr != nil {
				continue
			}
			slog.Warn("executor: trying fallback model",
				"client_model", params.ClientModel,
				"fallback_model", fbModel,
				"fallback_candidates", len(fbCandidates),
				"elapsed_ms", time.Since(tTotal).Milliseconds(),
			)
			// V3.3-OBS OBS-B1 (2026-08-15): model_switch 动作事件（24 号 §2：
			// from/to/reason；跨 provider 模型回退，不产生新 request_id）。
			e.liveActions.Emit(params.R.Context(), liveactions.ActionEvent{
				RequestID: params.RequestID,
				Action:    liveactions.ActionModelSwitch,
				Model:     fbModel,
				Detail: map[string]string{
					"from_model": params.ClientModel,
					"to_model":   fbModel,
					"reason":     "no_node_cross_provider",
				},
			})
			subParams := *params
			subParams.Candidates = fbCandidates
			subParams.InFallback = true
			subParams.ClientModel = fbModel
			e.asyncDepth.Add(1)
			result, fbExecErr := e.Execute(&subParams)
			e.asyncDepth.Add(-1)
			if fbExecErr == nil {
				result.Trace.FallbackFromModel = params.ClientModel
				// Clear stale FailureReason from sync retry phase — this
				// request ultimately succeeded via fallback, not failed.
				result.Trace.FailureReason = ""
				slog.Info("executor: fallback model succeeded",
					"client_model", params.ClientModel,
					"fallback_model", fbModel,
					"elapsed_ms", time.Since(tTotal).Milliseconds(),
				)
				return result, nil
			}
			// Accumulate failure details from the fallback attempt. LastKind is
			// folded rather than overwritten for the same reason as the sync
			// retry path above — the fallback model's transient failure must not
			// mask a credential-fatal kind already seen on the primary model.
			if execErrTyped, ok := fbExecErr.(*ExecuteError); ok {
				lastErr = execErrTyped.LastErr
				lastKind = recordLastKind(lastKind, execErrTyped.LastKind)
				attempts = append(attempts, execErrTyped.Attempts...)
				tried += execErrTyped.Tried
				if execErrTyped.Trace != nil {
					trace.BlockedCandidates = append(trace.BlockedCandidates, execErrTyped.Trace.BlockedCandidates...)
				}
			}
		}
	}

	// Track C C4 (2026-06-18): async fallback. If the synchronous
	// walk took longer than AsyncShortTimeout (default 15s) and
	// the request is session-bearing, hand off to a background
	// goroutine that continues trying with an independent context
	// (AsyncLongTimeout, default 300s). The handler will see the
	// returned AsyncPendingError, write 202 + X-Gw-Pending, and
	// the client can poll GET /v1/sessions/{id}/pending-response.
	//
	// Gating: PendingStore must be configured; the request must
	// have a session id; the long timeout must be > short timeout
	// (sanity); and at least one credential must have been tried
	// (otherwise there's nothing to demote — the failure was a
	// no_candidates condition, not a slow path).
	if e.shouldAsyncFallback(params, tTotal, tried, lastKind) {
		asyncErr := e.startAsyncRetry(params, trace, attempts, lastKind, lastErr)
		if asyncErr != nil {
			return nil, asyncErr
		}
	}

	return nil, &ExecuteError{LastErr: lastErr, Tried: tried, Exhausted: true, Trace: trace, Attempts: attempts, LastKind: lastKind}
}

// recordModelNotFound logs a single upstream model_not_found (or
// model_deprecated) 4xx to the model_probe_runs table so that the
// /api/routing/recent-model-failures admin endpoint and the probe history
// badge can surface it. The probe background worker will pick the binding up
// and run targeted probes (consensus + backoff) to decide whether to mark it
// broken_confirmed.
//
// This records the evidence row only. The executor's MNF branch separately
// writes the per-model binding state through the credential Writer; the probe
// worker remains responsible for confirming recovery or a persistent outage.
//
// 2026-08-05: status and errorCode are now passed in (previously hardcoded
// 404 / 'model_not_found') so a model_deprecated (HTTP 410) is recorded
// accurately rather than mislabeled as a 404.
func (e *Executor) recordModelNotFound(ctx context.Context, credentialID int, rawModel, body string, status int, kind errorsx.ErrorKind) {
	if e.DB == nil || !e.DB.Enabled() {
		return
	}
	preview := body
	if len(preview) > 500 {
		preview = preview[:500]
	}
	if status <= 0 {
		status = 404
	}
	errorCode := "model_not_found"
	if kind == errorsx.KindModelDeprecated {
		errorCode = "model_deprecated"
	}
	_, err := e.DB.Pool().Exec(ctx, `
		INSERT INTO model_probe_runs_hot
		    (tenant_id, credential_id, raw_model_name, status,
		     http_status, error_code, error_message, latency_ms,
		     state_change, state_applied, triggered_by)
		VALUES ($1, $2, $3, 'http_4xx', $4, $5, NULLIF($6, ''), 0,
		        'unchanged', FALSE, 'routing_4xx')
	`, "default", credentialID, rawModel, status, errorCode, preview)
	if err != nil {
		slog.Warn("record_model_not_found: insert failed",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"error", err)
	}

	// Self-healing: temporarily exclude this (credential, raw_model) pair
	// from routing so the gateway stops sending requests to a model the
	// upstream no longer serves. We write node_probe_state with
	// last_direct_ok=FALSE and a 5-minute next_retry_at window.
	//
	// Both refreshIndexSQL (autoroute/index.go) and filterCurrentlyAvailable
	// (autoroute/recommend_v2.go) filter on:
	//   nps.last_direct_ok = false AND nps.next_retry_at > now()
	// so the pair disappears from the candidate pool for 5 minutes. After
	// the window expires the pair is eligible again; if it still 404s,
	// this function re-arms the exclusion. The bg node-probe worker owns
	// the long-term retry ladder and will eventually set last_direct_ok=TRUE
	// when an upstream probe confirms the model is back.
	//
	// This is scoped to the specific (credential, model) pair — it does
	// NOT cool the entire credential, so other models on the same
	// credential remain routable.
	const mnfCoolWindow = 5 * time.Minute
	_, mnfErr := e.DB.Pool().Exec(ctx, `
		INSERT INTO node_probe_state (
			credential_id, raw_model_name,
			consecutive_failures, consecutive_successes,
			last_attempt_at, next_retry_at, next_retry_seconds,
			paused, in_flight_until,
			last_direct_ok, last_gateway_ok,
			last_err_code, last_err_detail,
			updated_at
		) VALUES (
			$1, $2,
			1, 0,
			now(), now() + $3::interval, EXTRACT(EPOCH FROM $3::interval)::int,
			FALSE, NULL,
			FALSE, FALSE,
			$4, NULL,
			now()
		)
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET last_direct_ok = FALSE,
		    last_gateway_ok = FALSE,
		    last_attempt_at = now(),
		    next_retry_at = now() + $3::interval,
		    next_retry_seconds = EXTRACT(EPOCH FROM $3::interval)::int,
		    consecutive_failures = node_probe_state.consecutive_failures + 1,
		    last_err_code = $4,
		    in_flight_until = NULL,
		    updated_at = now()
	`, credentialID, rawModel, mnfCoolWindow.String(), errorCode)
	if mnfErr != nil {
		slog.Warn("record_model_not_found: node_probe_state UPSERT failed",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"error", mnfErr)
	}
}

// recordMnfStreak (Step 6, 2026-06-18) increments the per-credential
// model_not_found counter for the current sticky session. The counter
// is used for observability and alerting only; it does NOT break the
// sticky binding.
//
// Why no sticky-break (2026-06-24):
//   - model_not_found is classified as a client-bug kind by errorsx
//     (errorsx.IsClientBug → true). It is the upstream telling us the
//     caller asked for a model it does not serve — not the credential
//     being broken.
//   - recordStickyFailure already exempts client-bug kinds from
//     unpinning, so the sticky TTL is preserved across intermittent
//     model_not_found. mnfStreak must agree, otherwise the two paths
//     disagree and a 3-streak silently re-picks a credential on the
//     very next request.
//   - Operators see "one normal / one failure" alternating in
//     request_logs when this rule fires: sticky re-pick changes the
//     egress identity (UA/TLS fingerprint slot), the new credential
//     succeeds once, then model_not_found again, then re-pick again.
//   - The circuit breaker (e.Circuit.Allow) and credential_availability
//     worker (probe + cooling) are the correct mechanisms for removing a
//     truly broken credential from routing — neither requires destroying
//     the sticky binding of an unrelated session.
//
// Counters are still incremented so /api/candidate-failures/stats and
// the alert ring can surface "this credential is returning model_not_found
// frequently" without affecting streaming. Threshold is now informational
// (logs the streak) rather than destructive.
//
// All guards are nil/disabled-aware:
//   - MnfStreakEnabled is false → no-op (feature flag)
//   - MnfStreak is nil → no-op (tests / older wiring)
//   - params.StickyKey == "" → no-op (stateless request, no sticky)
//   - threshold <= 0 → defaults to 3
func (e *Executor) recordMnfStreak(params *ExecParams, credentialID int) {
	if !e.MnfStreakEnabled || e.MnfStreak == nil {
		return
	}
	if params == nil || params.StickyKey == "" {
		return
	}
	threshold := e.MnfStickyBreakThreshold
	if threshold <= 0 {
		threshold = 3
	}
	key := BuildMnfStreakKey(params.StickyKey, credentialID)
	count := e.MnfStreak.Increment(key)
	if count >= threshold {
		// Informational only: surface the streak so operators see
		// "this session has hit model_not_found N times on this
		// credential" without breaking the sticky binding. The
		// sticky TTL continues to hold for the duration of the
		// session; routing stays on the pinned credential until
		// either the credential's circuit opens or the session's
		// natural sticky TTL expires.
		slog.Warn("mnf_streak_threshold_reached",
			"sticky_key", params.StickyKey,
			"credential_id", credentialID,
			"streak", count,
			"threshold", threshold,
			"sticky_kept", true,
		)
	}
}

// resetMnfStreak clears the per-credential model_not_found counter when
// a request succeeds on that credential. Counterpart to
// recordMnfStreak; the two are paired so a single success undoes
// accumulated intermittent failures.
func (e *Executor) resetMnfStreak(params *ExecParams, credentialID int) {
	if e.MnfStreak == nil || params == nil || params.StickyKey == "" {
		return
	}
	key := BuildMnfStreakKey(params.StickyKey, credentialID)
	e.MnfStreak.Reset(key)
}

func (e *Executor) observeRoutingStateFailure(params *ExecParams, cand provider.Candidate, kind errorsx.ErrorKind) {
	if e == nil || e.RoutingStateShadow == nil || params == nil || params.ClientModel == "" {
		return
	}
	e.RoutingStateShadow.ObserveState(routingstate.Evidence{
		CredentialID:     cand.CredentialID,
		RawModelName:     candidateRawModel(cand),
		CanonicalName:    params.ClientModel,
		Scope:            routingstate.ScopeModel,
		Source:           routingstate.SourceRequest,
		ObservedAt:       time.Now(),
		CorrelationID:    params.RequestID,
		BindingAvailable: boolPtrCompat(false),
		ErrorKind:        string(kind),
	})
}

func (e *Executor) observeRoutingStateProbe(params *ExecParams, cand provider.Candidate, trigger routingstate.ProbeTrigger) {
	if e == nil || e.RoutingStateShadow == nil || params == nil {
		return
	}
	e.RoutingStateShadow.ObserveProbe(routingstate.ProbeTask{
		CredentialID:  cand.CredentialID,
		RawModelName:  candidateRawModel(cand),
		Scope:         routingstate.ScopeModel,
		Trigger:       trigger,
		CorrelationID: params.RequestID,
	})
}

func candidateRawModel(candidate provider.Candidate) string {
	if candidate.OfferRawModel != "" {
		return candidate.OfferRawModel
	}
	return candidate.RawModel
}

func (e *Executor) refreshedCandidatesAfterProbe(params *ExecParams) []provider.Candidate {
	if e == nil || e.Provider == nil || params == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(params.R.Context(), 500*time.Millisecond)
	defer cancel()
	candidates, _, err := e.Provider.GetCandidates(
		ctx,
		params.ClientModel,
		params.ClientID.Fingerprint.ClientProfile,
		params.TenantID,
	)
	if err != nil {
		slog.Warn("executor: refreshed candidates after probe failed",
			"model", params.ClientModel,
			"tenant_id", params.TenantID,
			"error", err)
		return nil
	}
	slog.Info("executor: refreshed candidates after probe",
		"model", params.ClientModel,
		"tenant_id", params.TenantID,
		"candidates", len(candidates))
	return candidates
}

func (e *Executor) restoreCredentialState(ctx context.Context, credentialID int, canonicalModel string) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if err := e.State.RestoreOnSuccess(ctx, credentialID, canonicalModel); err != nil {
		slog.Debug("credential state restore failed", "credential_id", credentialID, "canonical_model", canonicalModel, "error", err)
	}
}

func (e *Executor) writeCredentialStateOnError(ctx context.Context, credentialID int, canonicalModel string, kind errorsx.ErrorKind, err error) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if !shouldWriteCredentialState(kind) {
		return
	}
	failure := credential.Failure{Kind: kind}
	if err != nil {
		failure.Detail = err.Error()
		var upstreamErr *upstreampkg.Error
		if errors.As(err, &upstreamErr) && upstreamErr != nil {
			failure.RetryAfter = upstreamErr.RetryAfter
		}
	}
	if err := e.State.WriteOnError(ctx, credentialID, canonicalModel, failure); err != nil {
		slog.Debug("credential state error write failed", "credential_id", credentialID, "kind", kind, "error", err)
		return
	}
	// OPT-5 (2026-07-12): per-credential cache invalidation. The previous
	// InvalidateAllCandidateCache() flushed every cached candidate for
	// every model, causing a thundering-herd against the DB when many
	// concurrent requests raced to refill the cache after a single
	// quota-exhausted credential was written. The per-credential scan
	// only touches cache entries whose PlanOrder includes this credential.
	provider.InvalidateCandidateCacheForCredential(credentialID)
}

// forceUnpinOnFatalKind clears the session pin for a credential whose kind
// indicates the credential is permanently dead (auth revoked, quota
// exhausted, etc.). Transient blip kinds (network/timeout/upstream-down),
// client-bug kinds (model_not_found, tool_call_id_mismatch), and
// provider-side congestion (KindConcurrent/KindStreamTimeout) are NOT fatal
// to the credential, so we keep the pin for them. Concurrent calls are safe
// because pin is keyed by (holder, credentialID).
func (e *Executor) forceUnpinOnFatalKind(ctx context.Context, holder string, credentialID int, kind errorsx.ErrorKind) {
	e.forceUnpinOnFatalKindForTenant(ctx, holder, credentialID, kind, session.GetTenantIDFromContext(ctx))
}

func (e *Executor) forceUnpinOnFatalKindForTenant(ctx context.Context, holder string, credentialID int, kind errorsx.ErrorKind, tenantID string) {
	if e.FpSlots == nil || !e.FpSlots.Enabled() {
		return
	}
	if !errorsx.IsCredentialFatal(kind) {
		return
	}
	e.FpSlots.ForceUnpinForTenant(ctx, holder, credentialID, tenantID)
}

func (e *Executor) stickyCredentialID(stickyKey string) *int {
	if e.Router == nil || e.Router.Sticky == nil || stickyKey == "" {
		return nil
	}
	credentialID, _, ok := e.Router.Sticky.GetEntry(stickyKey)
	if !ok {
		return nil
	}
	return &credentialID
}

// pickStickyCredentialID picks the sticky credential for the current request
// using the multi-level lookup (L1 → L2 → L3) when SessionID and Model are
// available, falling back to the L3-only lookup otherwise.
//
// 2026-07-07: Extracted so both the first Execute() pass and the sync-retry
// path invoke the same logic. Previously the retry path at line ~1494 only did
// L3, so a session-scoped request retried after a transient error could hop
// to a different credential than the first attempt.
func (e *Executor) pickStickyCredentialID(params *ExecParams) *int {
	// 2026-08-13: X-LLM-Pin-Credential takes absolute precedence over session
	// sticky. It is set only by trusted internal callers (OriginMiddleware
	// strips the header for everyone else), so it is safe to honor directly.
	if params.PinCredentialID != nil {
		return params.PinCredentialID
	}
	var stickyID *int
	var level string

	if params.SessionID != "" && params.Model != "" {
		stickyID = e.stickyCredentialIDMultiLevel(
			params.TenantID,
			params.AppID,
			params.ApiKeyID,
			params.ClientID.Fingerprint.ClientProfile,
			params.SessionID,
			params.Model,
		)
		level = "multi-level"
	} else {
		stickyID = e.stickyCredentialID(params.StickyKey)
		level = "sticky-key"
	}

	// 2026-07-16: Diagnostic logging for sticky routing debugging
	if stickyID != nil {
		slog.Debug("sticky_routing: credential locked",
			"credential_id", *stickyID,
			"model", params.Model,
			"session_id", params.SessionID,
			"sticky_key", params.StickyKey,
			"level", level,
			"request_id", params.RequestID,
		)

		// 2026-07-16: Health-aware sticky breaker
		// If the sticky credential has recent consecutive failures, break the stickiness
		if e.TTFBTracker != nil {
			stats := e.TTFBTracker.Get(*stickyID)
			// If no recent TTFB data (>5 min old or never recorded), it might be unhealthy
			if stats == nil {
				slog.Debug("sticky_routing: breaking stickiness due to no recent TTFB data",
					"credential_id", *stickyID,
					"model", params.Model,
					"reason", "no_recent_activity",
				)
				return nil // Break stickiness, allow re-selection
			}
		}
	}

	return stickyID
}

// stickyCredentialIDMultiLevel uses the multi-level sticky lookup (L1 → L2 → L3).
// L1 = session+model, L2 = client+model, L3 = client baseline.
// Returns the credentialID and the level that matched (for telemetry).
//
// 2026-06-25: This is the new primary sticky lookup. The old
// stickyCredentialID (L3 only) is kept for backward compatibility with
// any external callers and is used when the multi-level inputs are
// unavailable (e.g., model is empty).
//
// 2026-07-07: Now actively used by Execute() when SessionID and Model are available.
func (e *Executor) stickyCredentialIDMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
) *int {
	if e.Router == nil || e.Router.Sticky == nil {
		return nil
	}
	result := e.Router.Sticky.GetMultiLevel(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
	if !result.Found {
		return nil
	}
	return &result.CredentialID
}

func boolPtrCompat(v bool) *bool {
	return &v
}

func stickyHitForChosen(stickyCredentialID *int, chosenCredentialID int) *bool {
	if stickyCredentialID == nil {
		return nil
	}
	return boolPtrCompat(*stickyCredentialID == chosenCredentialID)
}

func (e *Executor) recordStickySuccess(params *ExecParams, credentialID int) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil {
		return
	}
	// When the rate-limit module is disabled, sticky routing is disabled too.
	// Do not create bindings during that interval that could affect routing
	// after the module is enabled again.
	if !ratelimit.IsRateLimitEnabled() {
		return
	}

	// 2026-07-07: Use multi-level recording when SessionID and Model are available
	if params.SessionID != "" && params.Model != "" {
		e.Router.Sticky.RecordSuccessMultiLevel(
			params.TenantID,
			params.AppID,
			params.ApiKeyID,
			params.ClientID.Fingerprint.ClientProfile,
			params.SessionID,
			params.Model,
			credentialID,
		)
		return
	}

	// Fallback to L3-only recording
	if params.StickyKey == "" || params.Policy == nil {
		return
	}
	// Policy.StickyTTLSeconds is in seconds (DB column `sticky_ttl_seconds`).
	// 2026-06-13: the previous code multiplied this value by Millisecond,
	// which collapsed the intended 1800s TTL to ~1.8s and the
	// minute-floor in this function masked the bug for years.
	stickyTTL := time.Duration(params.Policy.StickyTTLSeconds) * time.Second
	if stickyTTL < time.Minute {
		stickyTTL = time.Minute
	}
	e.Router.Sticky.RecordSuccess(params.StickyKey, credentialID, stickyTTL)
}

func (e *Executor) recordStickyFailure(params *ExecParams, credentialID int, kind errorsx.ErrorKind) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil {
		return
	}
	if !ratelimit.IsRateLimitEnabled() {
		return
	}

	// AUDIT-2: For credential-fatal errors, delete all sticky levels immediately
	if errorsx.IsCredentialFatal(kind) {
		if params.SessionID != "" && params.Model != "" {
			e.Router.Sticky.DeleteMultiLevel(
				params.TenantID,
				params.AppID,
				params.ApiKeyID,
				params.ClientID.Fingerprint.ClientProfile,
				params.SessionID,
				params.Model,
				credentialID,
			)
		} else if params.StickyKey != "" {
			e.Router.Sticky.Delete(params.StickyKey)
		}
		return
	}

	// Client cancellation, malformed/unsupported request shapes, and
	// context overflow are not node failures and must not trigger a
	// reroute. Network, timeout, upstream-down, rate-limit, concurrent,
	// and stream-timeout errors do indicate that the current route may
	// be unhealthy, so they count toward the two-failure decision.
	if kind == errorsx.KindCanceled ||
		kind == errorsx.KindContextLength ||
		errorsx.IsClientBug(kind) {
		return
	}

	// AUDIT-1 (2026-07-12): 默认阈值 2，对应"连续 2 次失败触发路由重选"。
	// 之前硬编码 5，与用户语义不符：用户期望"使用当前节点出错 10s 后
	// 再次出错即重走路由"，是 2 次而非 5 次。
	//
	// AUDIT-2 (2026-07-12): 移除了 GetEntry 前置检查。之前的检查逻辑使用
	// params.StickyKey (L3)，导致当 L1 存在但 L3 不存在时跳过失败记录。
	// RecordFailureMultiLevel 内部会检查每个级别是否指向当前 credentialID，
	// 不需要外部预检。
	if params.SessionID != "" && params.Model != "" {
		e.Router.Sticky.RecordFailureMultiLevel(
			params.TenantID,
			params.AppID,
			params.ApiKeyID,
			params.ClientID.Fingerprint.ClientProfile,
			params.SessionID,
			params.Model,
			credentialID,
			2,
		)
		return
	}

	// Fallback to L3-only when SessionID/Model unavailable
	if params.StickyKey != "" {
		// Verify this L3 entry still points to the failed credential
		boundID, _, ok := e.Router.Sticky.GetEntry(params.StickyKey)
		if ok && boundID == credentialID {
			e.Router.Sticky.RecordFailure(params.StickyKey, 2)
		}
	}
}

// recordBanditSuccess records a successful request in the Bandit scorer.
// This updates the Beta distribution parameters for Thompson Sampling.
func (e *Executor) recordBanditSuccess(credentialID int, latencyMs int) {
	if e.Router == nil || e.Router.Bandit == nil {
		return
	}
	credID := fmt.Sprintf("%d", credentialID)
	e.Router.Bandit.RecordSuccess(credID, int64(latencyMs))

	// Mark dirty for async flush to database
	if e.Router.BanditFlusher != nil {
		e.Router.BanditFlusher.MarkDirty(credID)
	}
}

// recordBanditFailure records a failed request in the Bandit scorer.
// This updates the Beta distribution parameters for Thompson Sampling.
func (e *Executor) recordBanditFailure(credentialID int, kind errorsx.ErrorKind) {
	if e.Router == nil || e.Router.Bandit == nil {
		return
	}

	// Skip client-side errors (not the credential's fault)
	if errorsx.IsClientBug(kind) {
		return
	}

	// Skip transient network errors. KindUpstreamOverloaded belongs here
	// for the same reason as KindUpstreamDown: the upstream shedding load
	// says nothing about this credential's quality, so a bandit penalty
	// would demote a healthy credential for a provider-side condition.
	if kind == errorsx.KindCanceled ||
		kind == errorsx.KindNetwork ||
		kind == errorsx.KindTimeout ||
		kind == errorsx.KindUpstreamDown ||
		kind == errorsx.KindUpstreamOverloaded {
		return
	}
	// Skip per-model permanent failures (model deprecated / not found). The
	// credential may serve other models fine; penalizing its bandit score
	// would unfairly demote a healthy credential for an upstream model-level
	// decision it cannot control.
	// 2026-08-09: KindNoAvailableChannel joins this group — a distributor
	// group having no channel for THIS model says nothing about the
	// credential's quality for other models.
	if kind == errorsx.KindModelDeprecated || kind == errorsx.KindModelNotFound || kind == errorsx.KindNoAvailableChannel {
		return
	}

	credID := fmt.Sprintf("%d", credentialID)
	e.Router.Bandit.RecordFailure(credID)

	// Record 429 rate limit hits separately for penalty tracking
	if kind == errorsx.KindRateLimit {
		e.Router.Bandit.RecordRateLimitHit(credID)
	}

	// Mark dirty for async flush to database
	if e.Router.BanditFlusher != nil {
		e.Router.BanditFlusher.MarkDirty(credID)
	}
}

// AsyncPendingError (Track C C4, 2026-06-18) is returned by Execute
// when the synchronous candidate walk has exceeded AsyncShortTimeout
// and the request is eligible for the async fallback. The handler
// (relay/handler.go) recognises this via errors.As and returns
//
//	HTTP 202 Accepted
//	X-Gw-Pending: {sessionID}
//	X-Gw-Pending-Request: {requestID}
//	body: {"status":"in_progress", ...}
//
// The client then polls GET /v1/sessions/{id}/pending-response
// (see sessions/handler.go C3) until the goroutine finishes and
// either writes a completed body or marks the entry failed.
//
// This is a *graceful degradation*, not an error. We use a
// distinct error type (rather than a sentinel error value) so
// callers can inspect the request key without re-parsing the
// message string.
type AsyncPendingError struct {
	SessionID string
	RequestID string
	// StartedAt is for observability; the handler does not need
	// it. Stored for the "how long has the async goroutine been
	// running" admin metric.
	StartedAt time.Time
}

func (e *AsyncPendingError) Error() string {
	return fmt.Sprintf("async_pending: session=%s request=%s started_at=%s",
		e.SessionID, e.RequestID, e.StartedAt.Format(time.RFC3339))
}

// shouldAsyncFallback (Track C C4, 2026-06-18) gates the async
// fallback path. Returns false (synchronous exhaustion, as before)
// if any precondition is missing. Each check is a one-liner so a
// regression in the gate is easy to spot.
//
// 2026-07-19 (Phase 1 success-rate priority):
//   - Fast path A: tried >= 3 candidates with recoverable errors →
//     allow async even if elapsed < AsyncShortTimeout.
//   - Fast path B: transient/timeout/network single failure →
//     allow async immediately (provider may recover in async window).
//   - Both paths skip the 15s timeout wait, prioritizing success rate
//     over latency symmetry.
func (e *Executor) shouldAsyncFallback(params *ExecParams, tTotal time.Time, tried int, lastKind errorsx.ErrorKind) bool {
	if params != nil && params.PreStreamPrepared {
		return false
	}
	// SR-05: survival-owned attempts never demote to the async 202 path —
	// the SurvivalCoordinator keeps the connection and retries in-band.
	if params != nil && params.SurvivalAttempt {
		return false
	}
	// Recursion guard: the async goroutine calls Execute again.
	// The inner call must take the synchronous exhaustion path
	// regardless of the time elapsed.
	if e.asyncDepth.Load() > 0 {
		return false
	}
	if e.PendingStore == nil {
		return false
	}
	// No session → no GET endpoint, no way for the client to
	// retrieve the cached body. Async is useless here.
	if params == nil || params.R == nil {
		return false
	}
	hasSession := false
	if v := params.R.Header.Get("X-Gw-Session-Id"); v != "" {
		hasSession = true
	}
	if !hasSession {
		if v := params.R.Header.Get("X-Session-Id"); v != "" {
			hasSession = true
		}
	}
	if !hasSession {
		return false
	}
	// Nothing was tried at all — that's a no_candidates condition,
	// not a slow path. Async would not help.
	if tried <= 0 {
		return false
	}
	// Sanity: long timeout must exceed short. If mis-configured
	// the goroutine would just bail immediately; easier to skip
	// the async detour and return the synchronous error.
	short := e.AsyncShortTimeout
	if short <= 0 {
		short = 15 * time.Second
	}
	long := e.AsyncLongTimeout
	if long <= 0 {
		long = 300 * time.Second
	}
	if long <= short {
		return false
	}

	// ── Fast paths (success-rate priority, skip 15s timeout) ──

	// Fast path A: 3+ candidates exhausted quickly.
	// Exclude errors where re-trying the same content/model won't help.
	if tried >= 3 &&
		lastKind != errorsx.KindContentFilter &&
		lastKind != errorsx.KindModelNotFound &&
		lastKind != errorsx.KindModelDeprecated &&
		!errorsx.IsClientBug(lastKind) {
		return true
	}

	// Fast path B: single/multi candidate with retryable error
	// (transient 5xx, timeout, network, upstream down, concurrent, stream timeout).
	// The provider may recover in the 300s async window.
	if errorsx.IsRetryable(lastKind) {
		return true
	}

	// ── Standard path: only async after AsyncShortTimeout (>15s) ──
	if time.Since(tTotal) < short {
		return false
	}

	return true
}

// startAsyncRetry (Track C C4, 2026-06-18) spawns a goroutine that
// continues retrying the remaining credentials with an independent
// context. The goroutine writes its outcome to PendingStore and
// the caller (handler) returns AsyncPendingError so the client
// polls. The goroutine uses context.Background() so a client
// disconnect on the original request does NOT cancel the
// vendor retry — the work to date is preserved.
//
// Max fallback cap (default 2): the goroutine tries the same
// set of candidates the synchronous loop tried, plus at most
// AsyncMaxFallbackCreds new candidates (i.e. any that were
// filtered out by circuit/limiter in the synchronous phase and
// might have recovered). This matches the design-doc target
// "primary + up to 2 fallback credentials = 3 total".
//
// Returns AsyncPendingError on successful hand-off, or nil if
// the goroutine could not be started (the caller falls back to
// the synchronous exhaustion path in that case).
func (e *Executor) startAsyncRetry(
	params *ExecParams,
	trace *Trace,
	attempts []AttemptRecord,
	lastKind errorsx.ErrorKind,
	lastErr error,
) *AsyncPendingError {
	sessionID := params.R.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = params.R.Header.Get("X-Session-Id")
	}
	requestID := params.R.Header.Get("X-Request-Id")
	if requestID == "" {
		// Async retry needs a stable key for the GET endpoint.
		// Synthesise one so the goroutine can still write the
		// entry; the client can poll by sessionID + GET latest.
		requestID = "async-" + time.Now().Format("20060102T150405.000")
	}
	startedAt := time.Now()
	tenantID := pendingTenant(params)
	if tenantID == "" {
		return nil
	}

	// Async semaphore guard: limit concurrent async retry goroutines.
	// When the semaphore is full (e.g. batch provider outage triggers
	// many concurrent async attempts), skip async and return nil so
	// the caller falls back to synchronous exhaustion. This prevents
	// goroutine explosion under systemic failure.
	if e.AsyncRetrySem != nil {
		select {
		case e.AsyncRetrySem <- struct{}{}:
		default:
			slog.Warn("async_retry_semaphore_full",
				"session_id", sessionID,
				"request_id", requestID,
			)
			return nil
		}
	}

	// Mark in_progress BEFORE the goroutine starts so a concurrent
	// GET immediately knows the work is in flight. Save is a no-op
	// if Redis is unavailable; we still proceed to spawn the
	// goroutine (it would just not write back). Better than
	// silently dropping the request.
	// Audit fix 2.5: use context.Background() with a short timeout
	// for MarkInProgress. The client's request context may already
	// be canceled (the client disconnected during the >15s sync
	// walk), which would cause MarkInProgress to fail silently and
	// the client's first poll would get 404 instead of 202.
	mipCtx, mipCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = e.PendingStore.MarkInProgress(mipCtx, &pending.Response{
		SessionID:   sessionID,
		TenantID:    tenantID,
		RequestID:   requestID,
		Status:      pending.StatusInProgress,
		Body:        "",
		ContentType: "",
		IsStream:    params.IsStream,
		CreatedAt:   startedAt.Unix(),
	})
	mipCancel()

	maxFallbacks := e.AsyncMaxFallbackCreds
	if maxFallbacks <= 0 {
		maxFallbacks = 2
	}
	longTimeout := e.AsyncLongTimeout
	if longTimeout <= 0 {
		longTimeout = 300 * time.Second
	}

	// Capture the params we need for the goroutine. We build a
	// synthetic *http.Request with context.Background() so the
	// async walk is NOT tied to the client connection. The
	// headers (X-Gw-Session-Id, X-Request-Id, etc.) are copied
	// from the original request so downstream routing logic
	// (Router.PlanCandidates, FpSlots, etc.) can still read them.
	//
	// Audit fix 2.4: the synthetic request's context is set to
	// the longTimeout ctx (created below in runAsyncRetry) so
	// the Execute() call is bounded by the long timeout, not
	// unbounded. We pass the ctx via a closure to runAsyncRetry
	// which sets it on the request before calling Execute.
	bgParams := *params
	// 并发修复 2026-07-27：detached goroutine 绝不能触碰客户端的
	// http.ResponseWriter 或它的回调。startAsyncRetry 返回
	// AsyncPendingError 后 handler 立即写 202 并返回，此时 W 已经失效
	// （net/http 会复用/回收底层连接对象）。原实现把 *params 整体拷贝进
	// bgParams，于是 W 和六个 On* 回调都被带进了后台 goroutine，
	// runAsyncRetry -> Execute -> executor_chat.go 的
	// `if !params.SuppressSuccessWrite { params.W.WriteHeader(...) }`
	// 会在 202 之后继续往死掉的 writer 上写，造成 "superfluous
	// WriteHeader" / 响应串包 / keepalive goroutine 竞争。
	//
	// 异步结果的唯一出口是 PendingStore（客户端轮询获取），所以这里
	// 置空 W + 打开 SuppressSuccessWrite，并清掉全部回调；下游需要
	// writer 才能工作的流式/非流式路径改用 responseSink() 的丢弃
	// writer（既能读完 upstream body 写入 Capture，又不碰客户端连接）。
	bgParams.W = nil
	bgParams.SuppressSuccessWrite = true
	bgParams.OnStreamReady = nil
	bgParams.OnStreamStarted = nil
	bgParams.OnStreamCompleted = nil
	bgParams.OnProbeHoldStart = nil
	bgParams.OnProbeHoldEnd = nil
	bgParams.OnPreStreamKeepalivePause = nil
	if params.R != nil {
		syntheticReq := httptest.NewRequest("POST", params.R.URL.Path, nil)
		syntheticReq.Header = params.R.Header.Clone()
		bgParams.R = syntheticReq // context will be set in runAsyncRetry
	} else {
		bgParams.R = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	}
	bgTrace := trace
	bgAttempts := append([]AttemptRecord(nil), attempts...)
	bgLastKind := lastKind
	bgLastErr := lastErr

	go e.runAsyncRetry(&bgParams, bgTrace, bgAttempts, bgLastKind, bgLastErr, sessionID, requestID, startedAt, longTimeout, maxFallbacks)

	return &AsyncPendingError{
		SessionID: sessionID,
		RequestID: requestID,
		StartedAt: startedAt,
	}
}

// runAsyncRetry is the body of the async retry goroutine. It is
// separated from startAsyncRetry so the latter stays small and
// obviously correct (it does only "kick off" work; this method
// does the work).
//
// Lifecycle:
//  1. Build a background context with AsyncLongTimeout deadline.
//  2. Re-derive candidates from scratch (the synchronous loop's
//     circuit / limiter state has moved on; the goroutine gets a
//     fresh chance with any credential that may have recovered).
//  3. Walk the candidates with the same per-credential retry policy.
//  4. On success → PendingStore.Save(completed, body).
//  5. On exhaustion → PendingStore.Save(failed, error_message).
//
// All resource cleanup (fpSlots, limiter) is handled inside
// executeOpenAI/executeAnthropic (defer release patterns), so
// this method does not need to manage them explicitly.
//
// Recursion guard: this method calls e.Execute(), which would
// itself try to demote to async if the walk is still slow. We
// pass suppressAsync=true via a package-internal flag so the
// inner call takes the synchronous exhaustion path. (See
// shouldAsyncFallback — when suppressAsync is set, the gate
// returns false.)
func (e *Executor) runAsyncRetry(
	params *ExecParams,
	trace *Trace,
	priorAttempts []AttemptRecord,
	lastKind errorsx.ErrorKind,
	lastErr error,
	sessionID, requestID string,
	startedAt time.Time,
	longTimeout time.Duration,
	maxFallbacks int,
) {
	tenantID := pendingTenant(params)
	// Phase 1 (2026-07-19): release async semaphore slot on completion.
	if e.AsyncRetrySem != nil {
		defer func() { <-e.AsyncRetrySem }()
	}
	defer func() {
		// Defensive: an async goroutine panic must NOT take the
		// process down. The request is already in_progress in
		// Redis, so the client will get a stale-pending error
		// on poll (the sweeper will mark it failed eventually).
		if r := recover(); r != nil {
			slog.Error("async_retry_panic",
				"session_id", sessionID,
				"request_id", requestID,
				"panic", r,
			)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = e.PendingStore.Save(ctx, &pending.Response{
				SessionID:    sessionID,
				TenantID:     tenantID,
				RequestID:    requestID,
				Status:       pending.StatusFailed,
				ErrorMessage: "async_retry_panic",
				CompletedAt:  time.Now().Unix(),
			})
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), longTimeout)
	defer cancel()

	// Re-derive candidates. We DO NOT reuse params.Candidates
	// directly because the synchronous loop's planCandidates may
	// have re-ordered them; we want the async goroutine to get a
	// fresh look at the current state.
	asyncParams := *params
	// Audit fix 2.4: bind the longTimeout ctx to the synthetic
	// request so Execute() and its upstream HTTP calls are bounded.
	if asyncParams.R != nil {
		asyncParams.R = asyncParams.R.WithContext(ctx)
	}
	candidates := e.Router.PlanCandidatesWithContext(
		asyncParams.R.Context(), asyncParams.Candidates, nil, asyncParams.Policy,
		egressPref(asyncParams.Transform), asyncParams.TenantID, asyncParams.ClientModel, asyncParams.RequestID,
	)

	if len(candidates) == 0 {
		// No candidates available — fail and write the reason.
		_ = e.PendingStore.Save(ctx, &pending.Response{
			SessionID:    sessionID,
			TenantID:     tenantID,
			RequestID:    requestID,
			Status:       pending.StatusFailed,
			ErrorMessage: "async_no_candidates",
			CompletedAt:  time.Now().Unix(),
		})
		return
	}
	// Cap to primary + maxFallbacks per the design doc.
	if len(candidates) > maxFallbacks+1 {
		candidates = candidates[:maxFallbacks+1]
	}
	// Audit fix 2.1: assign the re-derived candidates to asyncParams
	// so Execute() actually uses them. Without this, Execute() would
	// retry the original (already-failed) candidates.
	asyncParams.Candidates = candidates

	// Recursion guard: bump asyncDepth for the inner Execute call
	// so shouldAsyncFallback returns false there. Restore on
	// return. Audit fix 2.3: use Add/Sub instead of save/restore
	// to avoid lost updates under concurrent requests.
	e.asyncDepth.Add(1)
	defer func() { e.asyncDepth.Add(-1) }()

	result, execErr := e.Execute(&asyncParams) //nolint:gocritic // intentionally recursive: async walk uses same executor
	if execErr == nil {
		// Audit fix 2.2: for streaming responses, Execute() does
		// NOT set ResponseBody (the stream is consumed by the
		// StreamChat closure which writes to the capturer
		// directly). We must NOT overwrite the capturer's entry
		// with an empty body. For non-streaming, ResponseBody is
		// set and we write it to the pending store.
		if !params.IsStream && len(result.ResponseBody) > 0 {
			_ = e.PendingStore.Save(ctx, &pending.Response{
				SessionID:   sessionID,
				TenantID:    tenantID,
				RequestID:   requestID,
				Status:      pending.StatusCompleted,
				Body:        string(result.ResponseBody),
				ContentType: contentTypeFor(params.IsStream),
				IsStream:    params.IsStream,
				CompletedAt: time.Now().Unix(),
			})
		} else if params.IsStream {
			// For streaming, the capturer in main.go's StreamChat
			// closure already wrote the body to the pending store.
			// We only need to ensure the entry is marked completed
			// if the capturer missed it (e.g. panic). Best-effort:
			// check if an entry exists and is still in_progress.
			if entry, found, _ := e.PendingStore.Get(ctx, sessionID, requestID); found && entry.Status == pending.StatusInProgress {
				_ = e.PendingStore.Save(ctx, &pending.Response{
					SessionID:   sessionID,
					TenantID:    tenantID,
					RequestID:   requestID,
					Status:      pending.StatusCompleted,
					Body:        entry.Body,
					ContentType: contentTypeFor(true),
					IsStream:    true,
					CompletedAt: time.Now().Unix(),
				})
			}
		}

		// 2026-06-20: correct the request_logs row that the
		// synchronous phase left at "in_progress" (because the
		// handler returned 202 + AsyncPendingError without
		// calling emitTelemetry). Best-effort: nil-safe, no
		// failure path on writeback error.
		if e.RequestLogEmitter != nil && e.RequestLogEmitter.Enabled() {
			e.RequestLogEmitter.EmitRequestLogUpdate(e.buildAsyncSuccessEntry(
				requestID, sessionID, startedAt, result, params,
			))
		}
		return
	}

	// Failure path. Audit fix 2.6: use the async Execute()'s
	// error (execErr), not the synchronous walk's lastErr/lastKind.
	// The synchronous walk's error is misleading — it describes
	// why the FIRST attempt failed, not why the ASYNC retry failed.
	asyncErr := "async_exhausted"
	if execErr != nil {
		asyncErr = "async: " + truncateForStore(execErr.Error())
	}
	_ = e.PendingStore.Save(ctx, &pending.Response{
		SessionID:    sessionID,
		TenantID:     tenantID,
		RequestID:    requestID,
		Status:       pending.StatusFailed,
		ErrorMessage: truncateForStore(asyncErr),
		CompletedAt:  time.Now().Unix(),
	})

	_ = trace
	_ = priorAttempts
}

// buildAsyncSuccessEntry (2026-06-20) constructs a minimal
// RequestLogEntry for async-retry success writeback. Only the
// fields available in the executor's scope are populated:
//   - RequestID, sessionID, startedAt (from runAsyncRetry params)
//   - ClientModel, OutboundModel (from ExecParams)
//   - CredentialID, ProviderID, EgressProtocol (from result.Candidate)
//   - ResponsePreview (truncated, for admin UI inspection)
//   - IdentityHash (from params.ClientID)
//
// TenantID, APIKeyID, and tokens/cost are intentionally NOT
// populated — the sync-phase handler has these but the executor
// does not. A minimal success row is still far better than the
// pre-fix behavior of leaving the row at "in_progress".
//
// ErrorKind is explicitly set to "" (NOT nil) so the SQL CASE in
// the UPSERT / UPDATE writes NULL to the column. Without this,
// COALESCE would preserve any stale error_kind from a prior
// failure update.
//
// Returned entry has Op="" — the caller is expected to use
// EmitRequestLogUpdate which sets Op=RequestLogUpdate.
func (e *Executor) buildAsyncSuccessEntry(
	requestID, sessionID string,
	startedAt time.Time,
	result *ExecuteResult,
	params *ExecParams,
) *telemetry.RequestLogEntry {
	// Defensive nil check. In practice runAsyncRetry always passes
	// a non-nil params (it received it from the executor caller),
	// but staticcheck rightly flags the field accesses below.
	if params == nil {
		latencyMs := int(time.Since(startedAt).Milliseconds())
		status := telemetry.RequestStatusSuccess
		emptyKind := ""
		eventAt := startedAt.Add(time.Duration(latencyMs) * time.Millisecond)
		return &telemetry.RequestLogEntry{
			RequestID:     requestID,
			EventAt:       &eventAt,
			Success:       true,
			RequestStatus: &status,
			LatencyMs:     &latencyMs,
			ErrorKind:     &emptyKind,
			GwSessionID:   strPtr(sessionID),
		}
	}
	latencyMs := int(time.Since(startedAt).Milliseconds())
	success := true
	status := telemetry.RequestStatusSuccess
	emptyKind := ""
	eventAt := startedAt.Add(time.Duration(latencyMs) * time.Millisecond)

	entry := &telemetry.RequestLogEntry{
		RequestID:     requestID,
		EventAt:       &eventAt,
		TenantID:      tenantFromCtx(params.R),
		ClientModel:   strPtr(params.ClientModel),
		OutboundModel: strPtr(params.OutboundModel),
		Success:       success,
		RequestStatus: &status,
		LatencyMs:     &latencyMs,
		ErrorKind:     &emptyKind, // empty string → COALESCE writes NULL
		GwSessionID:   strPtr(sessionID),
	}
	// 2026-06-30 PR-5: thread client_request_id from X-Gw-Client-Request-Id
	// header on async-retry success path. Without this, async-retry
	// request_logs rows are missing client_request_id even when the
	// client supplied one (audit P0-8).
	if params.R != nil {
		if cid := params.R.Header.Get("X-Gw-Client-Request-Id"); cid != "" {
			entry.ClientRequestID = &cid
		} else if cid := params.R.Header.Get("X-Client-Request-Id"); cid != "" {
			entry.ClientRequestID = &cid
		}
	}
	if result != nil {
		if len(result.InboundBody) > 0 {
			entry.RequestBody = strPtr(string(result.InboundBody))
		}
		if len(result.ResponseBody) > 0 {
			entry.ResponseBody = strPtr(string(result.ResponseBody))
		}
		if result.Candidate.CredentialID != 0 {

			id := result.Candidate.CredentialID
			entry.CredentialID = &id
		}
		if result.Candidate.ProviderID != 0 {
			id := result.Candidate.ProviderID
			entry.ProviderID = &id
		}
		if result.Candidate.Protocol != "" {
			entry.EgressProtocol = strPtr(result.Candidate.Protocol)
		}
		if len(result.ResponseBody) > 0 {
			preview := truncateUTF8(string(result.ResponseBody), 200)
			if len(preview) < len(result.ResponseBody) {
				preview += "..."
			}
			entry.ResponsePreview = &preview
		}
		// 2026-06-20 audit enhancement: include the request body
		// preview (truncated) so operators can correlate async-retry
		// success with the original intent without joining against
		// usage_ledger. Bounded at 200 chars to match the
		// ResponsePreview policy.
		if len(result.RequestBody) > 0 {
			preview := truncateUTF8(string(result.RequestBody), 200)
			if len(preview) < len(result.RequestBody) {
				preview += "..."
			}
			entry.RequestPreview = &preview
		}
		// 2026-06-20 audit enhancement: forward Round 47 compression
		// metadata so the parent-child chain remains queryable for
		// async-retry successes. Without this, the chain breaks at
		// any async-retry that triggered a 4xx recovery.
		if result.CompressionReason != nil {
			entry.CompressionReason = result.CompressionReason
		}
		if result.CompressionStrategy != nil {
			entry.CompressionStrategy = result.CompressionStrategy
		}
		if len(result.CompressionMeta) > 0 {
			entry.CompressionMeta = result.CompressionMeta
		}
		// Note: result.Trace (planned candidates) and
		// result.Candidate.CanonicalID are NOT forwarded here.
		// RequestLogEntry doesn't have a trace field (it lives
		// on DecisionLogEntry, written separately by the sync
		// phase), and provider.Candidate has no CanonicalID.
		// The sync-phase emitTelemetry already populates these
		// via its dedicated decision log emit; we don't need to
		// duplicate here.
	}
	if params.ClientID.IdentityHash != "" {
		ih := params.ClientID.IdentityHash
		entry.IdentityHash = &ih
	}
	return entry
}

// strPtr is a small local helper for building *string fields in
// RequestLogEntry. Returns nil for empty input so the COALESCE
// pattern in the SQL UPDATE leaves the column unchanged.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fpSlotTenantID(params *ExecParams) string {
	if params != nil {
		if tenantID := strings.TrimSpace(params.TenantID); tenantID != "" {
			return tenantID
		}
		if params.R != nil {
			return tenantFromCtx(params.R)
		}
	}
	return "default"
}

// back to the literal "default" if unset. Uses the exported
// session.GetTenantIDFromContext (Track C C4 audit fix #5).
func pendingTenant(params *ExecParams) string {
	if params == nil {
		return ""
	}
	return strings.TrimSpace(params.TenantID)
}

func tenantFromCtx(r *http.Request) string {
	if r == nil {
		return "default"
	}
	return session.GetTenantIDFromContext(r.Context())
}

// contentTypeFor picks the canonical content type for a replay.
// Streaming responses are SSE; everything else is JSON.
func contentTypeFor(isStream bool) string {
	if isStream {
		return "text/event-stream"
	}
	return "application/json"
}

// truncateForStore clamps error strings to a Redis-friendly size
// so a 10KB vendor error body doesn't blow up the Hash.
func truncateForStore(s string) string {
	const max = 1024
	return truncateUTF8(s, max)
}

type modelNotFoundError struct {
	credentialID int
	rawModel     string
	body         string
	// status is the upstream HTTP status (typically 404, but 410 for
	// end-of-life / deprecated models) captured at the construction site.
	// Carried through Unwrap() into *upstreampkg.Error so the typed error
	// chain exposes the same (Kind, StatusCode, Body) triple as
	// contextLengthHTTPError / contextLengthExhaustedError.
	status int
	// kind is the precise errorsx kind this failure should be recorded as.
	// Defaults to KindModelNotFound (the historical behaviour) when the
	// upstream returned a plain 404 / unknown-model body. Set to
	// KindModelDeprecated when the upstream body matched modelDeprecatedRe
	// (HTTP 410 Gone + "end of life", 404/422 + "has been deprecated", etc.)
	// so the writer applies the longer 30-day cooling and the handler
	// surfaces HTTP 410 + code=model_deprecated to the client.
	kind errorsx.ErrorKind
}

func (e *modelNotFoundError) Error() string {
	return string(e.resolvedKind()) + ": " + e.rawModel
}

// resolvedKind returns the effective kind, defaulting to KindModelNotFound
// when the caller did not set one (back-compat for the many construction
// sites that rely on the zero value).
func (e *modelNotFoundError) resolvedKind() errorsx.ErrorKind {
	if e != nil && e.kind != "" {
		return e.kind
	}
	return errorsx.KindModelNotFound
}

// Unwrap surfaces the upstream (Kind, StatusCode, Body) triple so
// modelNotFoundError participates in the typed error chain like its
// siblings (retryableError / contextLengthHTTPError / contextLengthExhaustedError).
// This lets errors.As / extractUpstreamError reach *upstreampkg.Error when a
// model_not_found is the terminal failure — currently no active response path
// consumes it, but it keeps the chain consistent and ready for a future
// handler change that surfaces the real upstream body instead of a fixed 503.
func (e *modelNotFoundError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &upstreampkg.Error{
		Kind:       e.resolvedKind(),
		Body:       []byte(e.body),
		StatusCode: e.status,
		Message:    e.Error(),
	}
}

type streamInterruptedError struct {
	reason       string
	credentialID int
	resumable    bool              // Whether the stream can be resumed with a different credential
	kind         errorsx.ErrorKind // Errorsx kind to record on the circuit (defaults to KindStreamTimeout)
}

func (e *streamInterruptedError) Error() string {
	return "stream_interrupted: " + e.reason
}

func isClientStreamInterruption(kind errorsx.ErrorKind, reason string) bool {
	if kind == errorsx.KindCanceled {
		return true
	}
	switch reason {
	case "client_cancel", "client_write_failed", "client_disconnected":
		return true
	default:
		return false
	}
}

func mayRetryInterruptedStream(params *ExecParams, interrupted *streamInterruptedError) bool {
	// Only safe to retry when no assistant output has reached the client yet.
	// params.Capture is nil for non-streaming requests (the catch branch above
	// is unreachable for them) and for streaming requests without an active
	// capture hook; in both cases no chunk has been emitted, so retrying the
	// next candidate is safe.
	if interrupted == nil || !interrupted.resumable || params == nil || params.SurvivalAttempt {
		return false
	}
	if params.Capture != nil {
		if sent, _ := params.Capture.ChunkCountersSnapshot(); sent > 0 {
			return false
		}
	}
	return true
}

// upstreamRetryAfterHint extracts an upstream-requested retry delay from
// err, bounded by upstreampkg's in-flight cap. Returns 0 when err carries
// no usable hint, so callers keep their exponential backoff.
//
// The bound matters: upstream.Error.RetryAfter is clamped for a DB cooling
// window (up to 31 days). Sleeping on that value with the client's
// connection still open would hang the request, so the in-flight path needs
// its own much tighter ceiling.
func upstreamRetryAfterHint(err error) time.Duration {
	if err == nil {
		return 0
	}
	var ue *upstreampkg.Error
	if !errors.As(err, &ue) || ue == nil {
		return 0
	}
	return upstreampkg.ClampInFlightRetryAfter(ue.RetryAfter)
}

func isUpstreamOverloaded(err error) bool {
	if err == nil {
		return false
	}
	var ue *upstreampkg.Error
	return errors.As(err, &ue) && ue != nil && ue.Kind == errorsx.KindUpstreamOverloaded
}

type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }

func (e *retryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// contextLengthHTTPError signals the upstream rejected the request because
// the prompt exceeded the model context window. executeAnthropic uses this
// to attempt one client-side trim + retry before bubbling the 4xx up.
type contextLengthHTTPError struct {
	status  int
	body    []byte
	headers http.Header
}

func (e *contextLengthHTTPError) Error() string {
	return fmt.Sprintf("upstream %d context_length_exceeded", e.status)
}

func (e *contextLengthHTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &upstreampkg.Error{
		Kind:       errorsx.KindContextLength,
		Body:       append([]byte(nil), e.body...),
		StatusCode: e.status,
		RetryAfter: upstreampkg.RetryAfterFromHeaders(e.headers),
		Message:    e.Error(),
	}
}

// gave up after both phases (mechanical trim + LLM summary fallback
// chain). Carries the credential that was the last to fail so the outer
// Execute loop can decide which kind of failover to attempt next
// (e.g. try a different credential, or a bigger-context model), and
// crucially so the credential is NOT recorded as a circuit failure —
// hitting a context limit is a property of the model the client asked
// for, not of the credential serving it.
type contextLengthExhaustedError struct {
	credentialID int
	rawModel     string
	status       int
	body         string // first 200 bytes of upstream 4xx body
}

func (e *contextLengthExhaustedError) Error() string {
	return fmt.Sprintf("context_length_exhausted: %s (cred=%d, status=%d)", e.rawModel, e.credentialID, e.status)
}

func (e *contextLengthExhaustedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return &upstreampkg.Error{
		Kind:       errorsx.KindContextLength,
		Body:       []byte(e.body),
		StatusCode: e.status,
		Message:    e.Error(),
	}
}

// error. Returns (kind, true) when the error is a content-moderation /
// safety-policy rejection that should short-circuit the candidate loop.
func classifyContentFilterError(err error) (errorsx.ErrorKind, bool) {
	kind := classifyExecError(err)
	if errorsx.IsContentFilter(kind) {
		return kind, true
	}
	return "", false
}

// classifyExecError derives the canonical errorsx.ErrorKind from an
// executor-stage error. It uses errors.As (not a direct type assertion) so a
// *upstreampkg.Error that has been wrapped by fmt.Errorf("...: %w", ...) — the
// common case in the retry/routing layer — is still reached. Without this, the
// prior type-assertion fell through to errorsx.ClassifyError(err, nil) which
// only sees the message string (no HTTP status, no response body) and so
// mis-classified wrapped upstream errors as generic KindTransient. That
// mis-classification is a direct cause of accessible nodes being wrongly
// excluded: the wrong kind routes through the wrong writer branch and the
// wrong failover decision.
//
// Preference order: typed Kind on the upstream error → body-aware
// ClassifyErrorWithBody (uses StatusCode + Body) → message-only ClassifyError
// as the final fallback for non-upstream errors (network/cancel/timeout).
func classifyExecError(err error) errorsx.ErrorKind {
	if err == nil {
		return ""
	}
	var ue *upstreampkg.Error
	if errors.As(err, &ue) && ue != nil {
		if ue.Kind != "" {
			return ue.Kind
		}
		// Typed upstream error but no pre-assigned Kind: classify from the
		// captured status+body, which is strictly more accurate than the
		// message-only path.
		if ue.StatusCode > 0 {
			return errorsx.ClassifyErrorWithBody(ue.StatusCode, ue.Body)
		}
	}
	return errorsx.ClassifyError(err, nil)
}

func shouldWriteCredentialState(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindAuth, errorsx.KindAuthRevoked,
		errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent,
		errorsx.KindConcurrent, errorsx.KindRateLimit,
		errorsx.KindStreamTimeout, errorsx.KindModelNotFound, errorsx.KindModelDeprecated,
		errorsx.KindNoAvailableChannel:
		return true
	default:
		return false
	}
}

func (e *Executor) shouldWriteCredentialStateOnConfirmedFailure(providerID, credentialID int, kind errorsx.ErrorKind) bool {
	if !shouldWriteCredentialState(kind) {
		return false
	}
	// 2026-07-05 V23 fix: Quota/Auth errors should write state immediately
	// without waiting for circuit open. These are definitive failures that
	// need to propagate to other instances immediately to prevent repeated
	// failures across the cluster.
	//
	// Before V23: Auth/RateLimit/Concurrent errors waited for Circuit OPEN
	// → 3×RTT cross-instance propagation delay
	// After V23: These errors write DB immediately on first failure
	// → <30s propagation via candidate cache invalidation
	switch kind {
	case errorsx.KindQuota, errorsx.KindQuotaBalance, errorsx.KindQuotaPeriodic, errorsx.KindQuotaPermanent,
		errorsx.KindAuth, errorsx.KindAuthRevoked,
		errorsx.KindNoAvailableChannel:
		return true
	}
	// Other error kinds (RateLimit, Concurrent, StreamTimeout) still require
	// Circuit OPEN confirmation to avoid false positives
	if e.Circuit == nil {
		return true
	}
	b := e.Circuit.GetOrCreate(providerID, credentialID)
	state := b.State()
	if state == credential.StateOpen || state == credential.StateQuarantined {
		return true
	}
	slog.Warn("credential state write pending failure confirmation",
		"credential_id", credentialID,
		"provider_id", providerID,
		"kind", kind,
		"consecutive", b.ConsecutiveFailures(),
	)
	return false
}

func egressPref(tx *transformation.TransformResult) []string {
	if tx == nil || len(tx.EgressPreference) == 0 {
		return nil
	}
	return tx.EgressPreference
}

func replaceModelInRequestBody(body []byte, newModel string) []byte {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == newModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(" \"")
	buf.WriteString(newModel)
	buf.WriteByte('"')
	buf.Write(suffix)
	return buf.Bytes()
}

func replaceModelInResponseBody(body []byte, clientModel string) []byte {
	// Use raw byte matching to replace "model":"<old>" with the client model,
	// preserving original JSON key ordering.
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) < 2 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == clientModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(`"` + clientModel + `"`)
	buf.Write(suffix)
	return buf.Bytes()
}

func injectStreamOptions(body []byte) []byte {
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[len(body)-1] != '}' {
		return body
	}
	if bytes.Contains(body, []byte(`"stream_options"`)) {
		return body
	}
	end := len(body) - 1
	for end > 0 && body[end-1] <= ' ' {
		end--
	}

	streamOpts := `"include_usage":true`
	insert := `,"stream_options":{` + streamOpts + `}`

	var buf bytes.Buffer
	buf.Write(body[:end])
	buf.WriteString(insert)
	buf.Write(body[end:])
	return buf.Bytes()
}

func executorMustMarshal(v any) json.RawMessage { //nolint:unused
	b, _ := json.Marshal(v)
	return b
}

func injectCacheParams(body []byte, cacheMode, sessionKey string) ([]byte, error) {
	if cacheMode == "" || sessionKey == "" {
		return body, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, err
	}

	switch cacheMode {
	case "checkpoint":
		obj["cache_checkpoint"] = sessionKey
	case "tokens":
		meta, ok := obj["metadata"].(map[string]any)
		if !ok {
			meta = make(map[string]any)
		}
		if cc, ok := meta["cache_control"].(map[string]any); ok {
			cc["type"] = "ephemeral"
			meta["cache_control"] = cc
		} else {
			meta["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		obj["metadata"] = meta
	}

	return json.Marshal(obj)
}
