package executors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/attachments"     //nolint:depguard // MM-1 outbound attachment URL rewrite
	"github.com/kaixuan/llm-gateway-go/domains/credential"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/memory"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/routingstate"
	"github.com/kaixuan/llm-gateway-go/domains/session"        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
	"github.com/kaixuan/llm-gateway-go/internal/irconv"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
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

// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type StreamHandler func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, catalogCode string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome

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
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type AnthropicPassthroughFunc func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

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
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type AnthropicToOpenAISSEFunc func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// AnthropicToResponsesSSEFunc is the streaming counterpart that reads
// Anthropic-format SSE upstream and writes OpenAI Responses API SSE to w.
// Used by executeAnthropic when ClientProtocol == "openai-responses"
// (Phase E, 2026-07-01). Same signature as AnthropicToOpenAISSEFunc
// so the executor wiring is symmetric.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type AnthropicToResponsesSSEFunc func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

// OpenAIToResponsesSSEFunc is the streaming counterpart that reads
// OpenAI chat.completion.chunk SSE upstream and writes OpenAI Responses
// API SSE to w. Used by executeOpenAI when ClientProtocol ==
// "openai-responses" (Phase E, 2026-07-01).
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type OpenAIToResponsesSSEFunc func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

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
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
type OpenAIToAnthropicSSEFunc func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any) StreamOutcome

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

// DispatchModelRecommender is the narrow autoroute contract required by the
// dispatch failure path.
type DispatchModelRecommender interface {
	RecommendModelAlternatives(context.Context, autoroute.ModelAlternativeRequest) ([]string, error)
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
	dispatchPipeline         *dispatch.Pipeline
	dispatchModelRecommender DispatchModelRecommender
	capacityAwareSortOn      bool
	capacityAwareSnapFn      func(int) (dispatch.SnapshotState, bool)
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
	// NodeOutcomeReducer is the dispatch-attempt node-health authority. Main may
	// inject a process-shared reducer; when nil, the executor constructs one
	// lazily on first dispatch attempt. NodeHealthAdapter is optional; when nil,
	// the executor adapts its existing circuit/state/URSM/probe dependencies.
	NodeOutcomeReducer *nodehealth.OutcomeReducer
	NodeHealthAdapter  nodehealth.Adapter
	nodeHealthMu       sync.Mutex
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

	// PendingStore (Track C, 2026-06-18) is the durable pending-response
	// cache. The executor reads it for the session retry-replay branch
	// (a "retry" keyword with a cached completed response is replayed
	// instead of hitting upstream); the approval flow writes to it
	// independently. The async 202 demotion machinery that also used it
	// was retired with the legacy sync candidate loop (AUDIT_24H B2b) —
	// the Async* timeout/semaphore fields went with it.
	PendingStore *pending.Store

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

	// 2026-07-28: 模型质量探测（per-request 完整性事件）。
	// 由 main.go 注入；nil 时不调用任何完整性检测（旧行为）。
	// 该字段是接口而不是具体类型，executors 包不依赖 integrity 子包，
	// 注入的实例在底层是 *integrity.Detector 包装。
	IntegrityDetector IntegrityDetector
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
		return clienttype.Normalize(ct)
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
func clientTokenOf(userKey, clientTypeValue string) string {
	if userKey == "" {
		userKey = "anon"
	}
	return userKey + "|" + clienttype.Normalize(clientTypeValue)
}

func clientTokenForMetrics(params *ExecParams) string {
	userKey := params.StickyKey
	if userKey == "" && params.R != nil {
		userKey = params.R.Header.Get("X-Request-Id")
	}
	clientType := extractClientType(params.R)
	return clientTokenOf(userKey, clientType)
}

func (e *Executor) beginUpstreamAttempt(params *ExecParams, cand provider.Candidate, protocol string, body []byte) error {
	attempt, err := consumeUpstreamAttempt(params)
	if err != nil {
		return err
	}
	params.ProviderID = cand.ProviderID
	params.CredentialID = cand.CredentialID
	params.AttemptNo = attempt
	params.UpstreamEndpoint = cand.BaseURL
	params.Audit = AuditContextFromAttempt(params.Audit, cand.ProviderID, cand.CredentialID, cand.BaseURL, attempt)
	e.liveActions.Emit(params.R.Context(), liveactions.ActionEvent{
		RequestID:    params.RequestID,
		Action:       liveactions.ActionUpstreamRequest,
		Model:        params.ClientModel,
		CredentialID: cand.CredentialID,
		Retry:        attempt > 1,
		RetrySeq:     attempt,
		Detail: map[string]string{
			"attempt":     strconv.Itoa(attempt),
			"provider_id": strconv.Itoa(cand.ProviderID),
			"raw_model":   candidateRawModel(cand),
		},
	})
	e.logUpstreamRequest(params, protocol, body)
	return nil
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
	// ForceCompression bypasses only the auto-threshold gate for an already
	// enabled strategy runner. It never enables a disabled compression policy.
	ForceCompression bool
	// StreamSurvivesClientCancel explicitly grants the stream a lifetime beyond
	// the client connection (session capture or durable/survival ownership).
	// Provisional correlation session IDs must not set this flag.
	StreamSurvivesClientCancel bool
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
	// DispatchAttempt means the dispatch mover owns same-node retries. Protocol
	// executors must not add their legacy minimum retry underneath that owner.
	DispatchAttempt bool
	// DispatchAttemptID is stable for one real dispatch ForwardFunc invocation
	// and is the idempotency key for node-health reduction.
	DispatchAttemptID string
	// FirstSemanticByteCallback is bound to the current dispatch attempt. Stream
	// bridges invoke it only for the first real content/tool SSE frame; non-stream
	// execution invokes it after the complete successful response is available.
	FirstSemanticByteCallback func()
	// OnStreamReady is called exactly once right before the executor hands
	// control to the normal stream writer. Kept for compatibility; heartbeat
	// owners now remain active until the request reaches a terminal outcome.
	OnStreamReady func()
	// OnStreamHeartbeat emits one protocol-safe transport frame through the
	// request's serialized writer. It must not update semantic capture counters.
	OnStreamHeartbeat func() error
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
	// ClientType is the normalized request-boundary client type. Empty is only
	// allowed for legacy direct executor callers, which use header/UA fallback.
	ClientType string
	// UpstreamAttempts bounds the number of upstream HTTP calls this request
	// can issue across all candidate attempts. Optional; nil disables the
	// per-request budget.
	UpstreamAttempts *UpstreamAttemptBudget
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
	// RequestJourney state is allocated by the protocol handler and shared with
	// dispatch so handler and pipeline events remain one monotonic sequence.
	JourneyGatewayInstanceID string
	JourneySeq               *atomic.Int64
	JourneyTerminal          *atomic.Bool
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
	DispatchAutoTask          string
	DispatchAutoProfile       string
	DispatchAutoWorkType      string
	DispatchAutoSignals       autoroute.ClassificationSignals
	// DispatchRequestModality is the modality detected by the handler when it
	// resolved the initial candidates. Dispatch V2 reuses it when lazily resolving
	// candidates for an alternate model.
	DispatchRequestModality string
	// DispatchDueAt schedules future execution (v6 G-Ⅱ, 定时请求). Zero =
	// immediate. Populated from the X-Gw-Due-At request header by the
	// handler; the pipeline parks the request in its due heap until the
	// time elapses. Beyond the pipeline's schedule-ahead cap the Submit call
	// fails with dispatch.ErrScheduleTooFar (mapped to a client-error kind).
	DispatchDueAt time.Time
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
	// The handler always sets Audit; tests that construct ExecParams
	// directly may set only the flat fields.
	Audit *AuditContext

	diagnosticsLogged bool
}

// discardResponseWriter is a write-only sink used when there is no live
// client to write to (params.W == nil; see responseSink).
//
// 并发修复 2026-07-27（原为异步重试 goroutine 引入，该机制已随
// AUDIT_24H B2b 退役；responseSink 的 nil-W 防护仍需要本类型）：
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
	var writer http.ResponseWriter = &discardResponseWriter{}
	if params != nil && params.W != nil {
		writer = params.W
	}
	if params != nil && params.IsStream && params.FirstSemanticByteCallback != nil {
		if target, ok := writer.(interface{ SetFirstSemanticByteCallback(func()) }); ok {
			target.SetFirstSemanticByteCallback(params.FirstSemanticByteCallback)
			return writer
		}
		return &firstSemanticResponseWriter{ResponseWriter: writer, callback: params.FirstSemanticByteCallback}
	}
	return writer
}

type firstSemanticResponseWriter struct {
	http.ResponseWriter
	callback func()
}

func (w *firstSemanticResponseWriter) FirstSemanticByteCallback() func() { return w.callback }

func (w *firstSemanticResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *firstSemanticResponseWriter) FlushError() error {
	if flusher, ok := w.ResponseWriter.(interface{ FlushError() error }); ok {
		return flusher.FlushError()
	}
	w.Flush()
	return nil
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

func (e *Executor) SetCapacityAwareSort(on bool, snapFn func(int) (dispatch.SnapshotState, bool)) {
	if e != nil {
		e.capacityAwareSortOn = on
		e.capacityAwareSnapFn = snapFn
	}
}

// legacyWritersEnabled 返回"是否应执行旧的状态写入路径"（FpSlots Recorder /
// credentialstate Observer / routingstate Shadow）。仅由 URSM_V2_MODE 配置决定：
// 非 authoritative 模式（off / shadow / canary 观察）下旧路径参与状态同步，
// 保留 scripts/rollback/ursm_v2_to_legacy.sh 的配置级回滚能力。
//
// AUDIT_24H B2b（2026-08-17）：移除了原 isURSMv2Authoritative() 的运行时
// Ready() 降级——authoritative 模式下即使 Redis 短暂不可用也不再回退旧
// writers，该窗口内的状态写入由 URSMv2 进程内 LRU mirror 兜底、恢复后由
// probe 体系校正（所有者已接受此行为变更）。
func (e *Executor) legacyWritersEnabled() bool {
	return e == nil || e.URSMv2 == nil || e.URSMv2.Mode() != ursmv2api.ModeAuthoritative
}

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

func (e *Executor) Execute(params *ExecParams) (result *ExecuteResult, err error) {
	if params.UpstreamAttempts == nil {
		params.UpstreamAttempts = NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit)
	}
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

	candidates := e.Router.PlanCandidatesPinned(
		params.R.Context(),
		params.Candidates,
		stickyCredID,
		params.PinCredentialID,
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
					subCandidates := e.Router.PlanCandidatesPinned(
						params.R.Context(), retryCandidates, retrySticky, params.PinCredentialID, params.Policy,
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

	// AUDIT_24H B2b (2026-08-17): dispatch_v2 是唯一执行路径。旧同步候选循环
	// （及其 async 202 兜底）已退役；dispatch_v2.enabled kill-switch 不再绕开
	// pipeline。pipeline 为 nil 属装配错误（main_dispatch.go 无条件 wire）。
	if e.dispatchPipeline == nil {
		return nil, &ExecuteError{
			LastErr:   fmt.Errorf("dispatch pipeline not wired"),
			Tried:     0,
			Exhausted: true,
			LastKind:  errorsx.KindTransient,
		}
	}
	return e.executeViaDispatch(params, candidates, holder, fpSlotDegraded, stickyCredID)

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

func tenantFromCtx(r *http.Request) string {
	if r == nil {
		return "default"
	}
	return session.GetTenantIDFromContext(r.Context())
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
	statusCode   int
	rawError     string
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
