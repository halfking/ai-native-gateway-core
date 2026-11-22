package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/internal/sessionid"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

const largeMemoryThreshold = 256 << 10

type LargeIngressConfig struct {
	MaxConcurrent int
	ContextBytes  int
	MinFreeBytes  uint64
}
type LargeIngress struct {
	handler *ChatHandler
	store   *largebody.Store
	config  LargeIngressConfig
	slots   chan struct{}
}
type largeArchiveKey struct{}
type largeArchive struct {
	Ref    largebody.Ref
	Report sessioncontext.Report
}

func LargeRequestArchive(ctx context.Context) (largebody.Ref, bool) {
	v, ok := ctx.Value(largeArchiveKey{}).(largeArchive)
	return v.Ref, ok
}
func NewLargeIngress(h *ChatHandler, store *largebody.Store, cfg LargeIngressConfig) *LargeIngress {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.ContextBytes <= 0 {
		cfg.ContextBytes = 1 << 20
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = 512 << 20
	}
	return &LargeIngress{handler: h, store: store, config: cfg, slots: make(chan struct{}, cfg.MaxConcurrent)}
}
func generationPath(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/v1/chat/completions", "/chat/completions", "/v1/completions", "/completions", "/v1/messages", "/messages", "/v1/responses", "/responses":
		return true
	}
	_, action, ok := extractGeminiPath(r.URL.Path)
	return ok && (action == "generateContent" || action == "streamGenerateContent")
}

// Wrap must be outside V2 dispatch and streamretry: both snapshot request bodies.
func (g *LargeIngress) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !generationPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := LargeRequestArchive(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		r, authorized := g.handler.prepareSanitizeRequest(r)
		if !authorized {
			g.reject(w, r, 401, "authentication_required", "authentication failed")
			return
		}
		tenantID := "default"
		if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.info != nil {
			tenantID = tenant(cached.info)
		}
		if requested := strings.TrimSpace(r.Header.Get("X-Tenant-ID")); requested != "" && requested != tenantID {
			g.reject(w, r, 403, "tenant_mismatch", "tenant does not match authenticated key")
			return
		}
		if r.ContentLength > largebody.LimitBytes {
			g.reject(w, r, 413, "prompt_too_large", "request exceeds 100000000 bytes", ingressRejection{
				clientMessage: i18n.T(r.Context(), i18n.MsgIngressPromptTooLarge, map[string]any{
					"Size": humanByteSize(r.ContentLength), "Limit": humanByteSize(largebody.LimitBytes),
				}),
				extra: map[string]any{"body_bytes": r.ContentLength, "limit_bytes": int64(largebody.LimitBytes)},
			})
			return
		}
		if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			g.reject(w, r, 415, "unsupported_content_encoding", "send uncompressed JSON")
			return
		}
		select {
		case g.slots <- struct{}{}:
		default:
			w.Header().Set("Retry-After", "1")
			g.reject(w, r, 429, "ingress_capacity_exceeded", "request processing capacity is full")
			return
		}
		released := false
		release := func() {
			if !released {
				<-g.slots
				released = true
			}
		}
		defer release()
		if g.store == nil {
			g.reject(w, r, 503, "session_archive_unavailable", "session archive is unavailable")
			return
		}
		free, err := archiveFreeBytes(g.store.RootPath())
		if err != nil || free < uint64(largebody.LimitBytes)+g.config.MinFreeBytes {
			g.reject(w, r, 503, "session_archive_capacity", "session archive has insufficient capacity")
			return
		}
		original := r.Body
		if original == nil {
			g.reject(w, r, 400, "invalid_request", "request body is required")
			return
		}
		defer original.Close()
		r = g.handler.prepareHistoryGuard(r)
		prepCtx, cancel := context.WithTimeout(r.Context(), largeIngressPrepBudget)
		defer cancel()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(largeIngressPrepBudget))
		stop := context.AfterFunc(prepCtx, func() {
			if controller.SetReadDeadline(time.Now()) != nil {
				_ = original.Close()
			}
		})
		defer stop()
		prefix, err := io.ReadAll(io.LimitReader(original, largeMemoryThreshold+1))
		if err != nil {
			g.reject(w, r, 400, "request_read_failed", "request body could not be read")
			return
		}
		// Client-selected identities are untrusted until the parsed body's
		// precedence and the existing session ownership gate have both run.
		// Rejected uploads remain recoverable in a server-generated namespace.
		sid := largebody.StagingSessionPrefix + uuid.NewString()
		ref, err := g.store.Capture(prepCtx, largebody.Identity{TenantID: tenantID, SessionID: sid}, io.MultiReader(bytes.NewReader(prefix), original))
		if err != nil {
			status, code, logMessage := 503, "session_archive_unavailable", "request could not be archived within the input limit"
			rej := ingressRejection{extra: map[string]any{"limit_bytes": int64(largebody.LimitBytes)}}
			if errors.Is(err, largebody.ErrTooLarge) {
				// 归档阶段才发现超限（没有 Content-Length 的分块上传）：说清是
				// 请求体超限、上限多少，而不是笼统的「could not be archived」。
				status, code = 413, "prompt_too_large"
				rej.clientMessage = i18n.T(r.Context(), i18n.MsgIngressPromptTooLarge, map[string]any{
					"Size": ">" + humanByteSize(largebody.LimitBytes), "Limit": humanByteSize(largebody.LimitBytes),
				})
			}
			g.reject(w, r, status, code, logMessage, rej)
			return
		}
		body := prefix
		report := sessioncontext.Report{SourceBytes: ref.Size, OutputBytes: len(body), SourceHash: ref.SHA256}
		// Rejections after RAW commit must retain an auditable recovery reference.
		r = r.WithContext(context.WithValue(r.Context(), largeArchiveKey{}, largeArchive{ref, report}))
		w.Header().Set("X-Gw-Archive-Id", ref.RequestID)
		if ref.Size > largeMemoryThreshold {
			reduceStarted := time.Now()
			body, report, err = sessioncontext.Reduce(prepCtx, g.store, ref, g.config.ContextBytes)
			observeContextReduce(prepCtx, reduceStarted, report, err)
			if err != nil {
				status, code := 422, "context_budget_exceeded"
				// logMessage 进请求日志/遥测：**稳定英文**（排障与看板按它检索）。
				logMessage := "request cannot produce a valid bounded context; split the protected input"
				// ★ 2026-11-22（211 事故，owner：「这类的错误要如实地返回给客户端，不要
				// 让人迷惑」）：给客户端的句子此前就是上面这句笼统英文——超时只说
				// 「exceeded its time limit」，既不说请求体多大、也不说重试有没有用。
				// 现在客户端拿到的是**事实**（请求体大小、时限、上限）+ **该怎么办**
				// （本地化），事实数据同时以旁键给机器（body_bytes /
				// preprocess_budget_seconds）。状态码与 retryable 语义不变
				//（超时＝网关侧处理能力，503 可重试；放不下＝输入问题，422
				// 不可重试——large_sanitize_test 钉的合同）。
				rej := ingressRejection{
					clientMessage: i18n.T(r.Context(), i18n.MsgIngressContextBudgetExceeded, map[string]any{
						"Limit": humanByteSize(int64(g.config.ContextBytes)),
					}),
					extra: map[string]any{"body_bytes": ref.Size},
				}
				if errors.Is(err, context.DeadlineExceeded) {
					status, code = 503, "request_processing_timeout"
					logMessage = "request preprocessing exceeded its time limit"
					rej.clientMessage = i18n.T(r.Context(), i18n.MsgIngressPreprocessTimeout, map[string]any{
						"Budget": prepBudgetLabel(), "Size": humanByteSize(ref.Size),
					})
					rej.extra["preprocess_budget_seconds"] = int64(largeIngressPrepBudget / time.Second)
					if report.Messages > 0 {
						rej.extra["messages"] = report.Messages
					}
				} else if errors.Is(err, context.Canceled) {
					status, code = 408, "request_canceled"
					logMessage, rej.clientMessage = "request preprocessing was canceled", ""
				} else if errors.Is(err, sanitize.ErrRuleBlocked) {
					status, code = 403, "sensitive_policy_blocked"
					logMessage, rej.clientMessage = "request blocked by sensitive information policy", ""
				} else if errors.Is(err, largebody.ErrInvalidJSON) || errors.Is(err, largebody.ErrStructureLimit) {
					status, code = 400, "invalid_request"
					logMessage, rej.clientMessage = "request body is not a valid JSON request", ""
				}
				g.reject(w, r, status, code, logMessage, rej)
				return
			}
		}
		resolved, updated, err := g.resolveArchiveSession(w, r.WithContext(prepCtx), body)
		if err != nil {
			if errors.Is(err, errClientSessionForbidden) {
				g.reject(w, r, 403, "session_forbidden", "session not owned by this api key")
			} else if errors.Is(err, largebody.ErrInvalidJSON) {
				g.reject(w, r, 400, "invalid_request", "request body must be a JSON object")
			} else {
				g.reject(w, r, 503, "session_unavailable", "session store unavailable")
			}
			return
		}
		body = updated
		if resolved != "" && resolved != ref.SessionID {
			ref, err = g.store.Rebind(prepCtx, ref, resolved)
			if err != nil {
				g.reject(w, r, 503, "session_archive_unavailable", "session archive could not bind the resolved session")
				return
			}
			sid = resolved
		}
		stop()
		cancel()
		_ = controller.SetReadDeadline(time.Time{})
		ctx := context.WithValue(r.Context(), largeArchiveKey{}, largeArchive{ref, report})
		ctx = contextbudget.WithSpec(ctx, contextbudget.Spec{}) // Buckets supply their own window later.
		r = r.Clone(ctx)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		r.Header.Del("Content-Length")
		r.Header.Set("X-Gw-Session-Id", sid)
		w.Header().Set("X-Gw-Archive-Id", ref.RequestID)
		if report.Reduced {
			w.Header().Set("X-Gw-Context-Reduced", "extractive_history")
		}
		release() // Preprocessing quota must not be held for the duration of an SSE response.
		g.serveArchivedResponse(next, w, r, ref)
	})
}

func (g *LargeIngress) resolveArchiveSession(w http.ResponseWriter, r *http.Request, body []byte) (string, []byte, error) {
	verified, _ := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey)
	keyInfo := verified.info
	resolved, _ := sessionid.Resolve(r.Header, body)
	hasBodyID := sessionid.FromBody(body) != ""
	if keyInfo == nil || g.handler.sessionGetter == nil {
		// A missing session authority is a supported local/degraded mode,
		// not permission to place caller data in another session's archive.
		resolved = ""
	} else if resolved == "" {
		// Preserve the existing recent-session/auto-create policy; a staging
		// id must never masquerade as an explicit client session header.
		assignment, err := g.handler.assignGatewaySession(r.Context(), body, r, keyInfo, "", nil, clientProfileFromKey(keyInfo))
		if err != nil {
			// 2026-11-11（ZCode 事故轮 · 可诊断性修复）：原本只回一个笼统的
			// errClientSessionUnavailable，**底层错误被整个丢掉** ⇒ 生产日志
			// 里查不到「为什么分配会话会失败」。⇒ 补一条带成因的日志；
			// 返回语义不变（仍是 503，不放行未验证的会话）。
			slog.Warn("gateway session assignment failed",
				"error", err)
			return "", nil, errClientSessionUnavailable
		}
		if assignment != nil {
			resolved = assignment.SessionID
			if assignment.Resumed {
				w.Header().Set("X-Gw-Session-Id-Resume", resolved)
				w.Header().Set("X-Gw-Session-Reused", "true")
			}
			if assignment.AutoCreated {
				w.Header().Set("X-Gw-Session-Id-Resume", resolved)
				w.Header().Set("X-Gw-Session-Auto", "true")
			}
		}
	}
	sid := resolved
	if resolved == "" {
		sid = generateSystemSessionID()
	} else {
		verifiedID, info, err := normalizeAndRegisterClientSession(r, resolved, g.handler.sessionGetter, keyInfo)
		if err != nil {
			return "", nil, err
		}
		sid = verifiedID
		if sid != resolved && info == nil {
			// Legacy stores without atomic claim receive a real fresh session,
			// so the inner handler cannot replace the helper's fallback again.
			deviceSeed := r.Header.Get("X-Device-Seed")
			if deviceSeed == "" {
				deviceSeed = r.Header.Get("X-Machine-Id")
			}
			if deviceSeed == "" {
				deviceSeed = "default"
			}
			created, err := g.handler.sessionGetter.CreateV2(r.Context(), keyInfo.ID, keyInfo.TenantID, deviceSeed, sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id")))
			if err != nil || created == nil || created.SessionID == "" {
				return "", nil, errClientSessionUnavailable
			}
			sid = created.SessionID
		}
	}
	if hasBodyID {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(body, &envelope) != nil || envelope == nil {
			return "", nil, largebody.ErrInvalidJSON
		}
		encoded, _ := json.Marshal(sid)
		// Canonical root aliases outrank nested metadata. Replace alternative
		// spellings of the highest-priority alias as well, avoiding map-order
		// ambiguity when the client supplied both gwSessionId and session_id.
		normalize := strings.NewReplacer("-", "", "_", "", ".", "", " ", "")
		for name := range envelope {
			if strings.ToLower(normalize.Replace(strings.TrimSpace(name))) == "gwsessionid" {
				envelope[name] = encoded
			}
		}
		envelope["gw_session_id"], envelope["session_id"] = encoded, encoded
		updated, err := json.Marshal(envelope)
		if err != nil {
			return "", nil, err
		}
		body = updated
	}
	return sid, body, nil
}

// observeContextReduce 记录一次归约的耗时与消息数（并行/串行、成功/失败分列）。
// 失败的指标仍然要记：那正是最需要被看见的形态（超时）。
func observeContextReduce(ctx context.Context, started time.Time, report sessioncontext.Report, err error) {
	if metrics.SessionContextReduceDuration == nil {
		return
	}
	parallel, outcome := "sequential", "ok"
	if sessioncontext.ReduceWorkers(ctx) > 1 {
		parallel = "parallel"
	}
	if err != nil {
		outcome = "err"
	}
	metrics.SessionContextReduceDuration.WithLabelValues(parallel, outcome).Observe(time.Since(started).Seconds())
	metrics.SessionContextReduceMessages.WithLabelValues(outcome).Observe(float64(report.Messages))
}

// largeIngressPrepBudget 是入口预处理（读体 + 归档 + 归约 + 会话绑定）的时限。
// 行为（超时取消）与给客户端看的文案（「{{.Budget}}」）共用这一个真源。
const largeIngressPrepBudget = 120 * time.Second

// ingressRejection 是入口拒绝里「只给客户端、不进日志」的那一部分。
//
// ★ 为什么分两份：reject 的 message 同时进请求日志与遥测——那是排障和看板的
// 检索锚点，必须是**稳定英文**；给客户端的句子要按请求语言本地化、要带
// 请求体大小等事实。两者混成一个变量，要么日志跟着客户端语言漂，要么
// 客户端永远只能看英文。
type ingressRejection struct {
	// clientMessage 是客户端可见的本地化句子；空 ⇒ 用日志那句。
	clientMessage string
	// extra 并入错误信封旁键（覆盖同名默认值）；事实数据，如 body_bytes。
	extra map[string]any
}

// ingressErrorType 按状态码给出口径一致的错误 type。
//
// ★ 2026-11-22：此前入口拒绝**一律**标 `invalid_request_error`——503 预处理
// 超时、413 请求体超限、429 入口满载、401 鉴权失败全都顶着「请求不合法」
// 的名义出去（211 事故的 A 测试实录：503 + invalid_request_error +「exceeded
// its time limit」）。type 是客户端分类的第一信号，必须跟状态码说的是同一
// 件事。
func ingressErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusRequestTimeout:
		return "request_timeout"
	case status == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "server_error"
	}
	return "invalid_request_error"
}

// ingressClientFacing 从日志句子与可选覆盖里算出「客户端看到什么」。纯函数：
// 日志句子（logMessage）原样留给日志侧，这里不碰；没有覆盖时客户端看到
// 的就是日志那句，旁键只有既有的 stage / retryable。
func ingressClientFacing(logMessage string, status int, rej []ingressRejection) (clientMessage string, extras map[string]any) {
	clientMessage = logMessage
	extras = map[string]any{"stage": "ingress", "retryable": status == 429 || status == 503}
	for _, rj := range rej {
		if rj.clientMessage != "" {
			clientMessage = rj.clientMessage
		}
		for k, v := range rj.extra {
			extras[k] = v
		}
	}
	return clientMessage, extras
}

// writeIngressError 写入口拒绝的信封。
//
// OpenAI 面：与共享的 writeErrorJSONWithKind 同形（message / type / code /
// kind / request_id + gateway_debug）。
//
// Anthropic 面：type / message + **code** + gateway_debug。★ 共享的
// writeErrorAnthropic 只拿 code 推导 type、并不输出 code——Anthropic 协议的
// 客户端（ZCode 就是）此前拿不到机器可读的错误码，只能读句子猜。
func writeIngressError(proto string, w http.ResponseWriter, status int, requestID, msg, code string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errType := ingressErrorType(status)
	var body map[string]any
	if proto == "anthropic" {
		errObj := map[string]any{
			"type":    anthropicErrorType(errType, code),
			"message": msg,
			"code":    code,
		}
		if debug != nil {
			errObj["gateway_debug"] = debug
		}
		body = map[string]any{"type": "error", "error": errObj}
	} else {
		errObj := map[string]any{
			"message":    msg,
			"type":       errType,
			"code":       code,
			"kind":       code,
			"request_id": requestID,
		}
		if debug != nil {
			errObj["gateway_debug"] = debug
		}
		body = map[string]any{"error": errObj}
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(body)
}

// humanByteSize 把字节数写成「7.4 MiB (7757461 bytes)」——量级给人看、精确值
// 给排障对账。
func humanByteSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB (%d bytes)", float64(n)/(1<<10), n)
	default:
		return fmt.Sprintf("%.1f MiB (%d bytes)", float64(n)/(1<<20), n)
	}
}

func prepBudgetLabel() string {
	return fmt.Sprintf("%ds", int64(largeIngressPrepBudget/time.Second))
}

// reject 写出入口拒绝响应。message 进日志/遥测（稳定英文）；客户端看到的
// 句子与旁键由可选的 rej 覆盖（见 ingressRejection）。
func (g *LargeIngress) reject(w http.ResponseWriter, r *http.Request, status int, code, message string, rej ...ingressRejection) {
	// The normal handler is bypassed on ingress rejection. Record metadata only;
	// a failed auth/upload must never trigger another read of the network body.
	rid := r.Header.Get("X-Request-Id")
	if rid == "" {
		rid = uuid.NewString()
	}
	safe := r.Clone(r.Context())
	safe.Body = http.NoBody
	safe.ContentLength = 0
	safe.GetBody = nil
	logCtx := g.handler.NewRequestLogContext(safe, rid, time.Now())
	if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.info != nil {
		logCtx.SetKey(cached.info)
	}
	logCtx.SetClientModel("<unknown>")
	logCtx.SetClientStatus(status)
	if client := g.handler.telemetryClient; client != nil && client.Enabled() {
		state := telemetry.RequestStatusFailure
		if status == http.StatusTooManyRequests {
			state = telemetry.RequestStatusRateLimited
		}
		client.EmitRequestLogInsert(logCtx.buildEntry(code, message, nil, nil, state))
	}
	if status == http.StatusTooManyRequests {
		logCtx.EmitRateLimited(code, message, nil, nil)
	} else {
		logCtx.failAndMark(code, message, nil, nil, status)
	}

	clientMessage, extras := ingressClientFacing(message, status, rej)

	write := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := extractGeminiPath(r.URL.Path); ok {
			writeGeminiError(w, status, clientMessage)
			return
		}
		writeIngressError(protocolOfRequest(r), w, status, r.Header.Get("X-Request-Id"), clientMessage, code, extras)
	})
	if ref, ok := LargeRequestArchive(r.Context()); ok {
		g.serveArchivedResponse(write, w, r, ref)
	} else {
		write.ServeHTTP(w, r)
	}

}

// Archive identity is merged into existing metadata. Neither raw bytes nor local
// absolute paths enter the database, Redis, logs or durable execution snapshots.
func applyLargeArchiveMetadata(entry *telemetry.RequestLogEntry, ctx context.Context) {
	raw := largeArchiveJSON(ctx)
	if len(raw) == 0 || entry == nil {
		return
	}
	meta := map[string]json.RawMessage{}
	if len(entry.CompressionMeta) > 0 {
		if json.Unmarshal(entry.CompressionMeta, &meta) != nil {
			return
		}
	}
	meta["large_request"] = raw
	if b, err := json.Marshal(meta); err == nil {
		entry.CompressionMeta = b
	}
}

// Keep the context metadata encoding a small, independently versioned record.
func largeArchiveJSON(ctx context.Context) json.RawMessage {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Value(largeArchiveKey{}).(largeArchive)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"archive": v.Ref, "reduction": v.Report, "client_response_request_id": v.Ref.RequestID + "-response", "version": 1})
	return b
}
