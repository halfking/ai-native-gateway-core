package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/credentialstate"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/disguise"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/transform"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

const maxBodySize = 32 << 20

type NormalizerFunc func(chunk []byte, isStream bool) []byte

type StreamHandler func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm NormalizerFunc, capture *audit.StreamCapture)

type StreamWrapperFunc func(w http.ResponseWriter, resp *http.Response, norm NormalizerFunc, capture *audit.StreamCapture)

type Executor struct {
	Router        *Router
	Circuit       *circuit.Manager
	Limiter       *limiter.Limiter
	Pools         *pool.PoolManager
	Upstream      *upstreampkg.Client
	Normalize     NormalizerFunc
	StreamChat    StreamHandler
	Auditor       audit.Sink
	State         *credentialstate.Writer
	DB            *db.DB
	HeaderProfiles *HeaderProfileCache

	StreamTimeout   time.Duration
	UpstreamTimeout time.Duration
}

func NewExecutor(
	router *Router,
	cm *circuit.Manager,
	lim *limiter.Limiter,
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
		Router:          router,
		Circuit:         cm,
		Limiter:         lim,
		Pools:           pools,
		Upstream:        upstream,
		Normalize:       normalize,
		StreamChat:      streamChat,
		Auditor:         auditor,
		StreamTimeout:   900 * time.Second,
		UpstreamTimeout: 120 * time.Second,
	}
}

type ExecParams struct {
	W             http.ResponseWriter
	R             *http.Request
	BodyBytes     []byte
	IsStream      bool
	SuppressSuccessWrite bool
	ClientModel   string
	OutboundModel string
	ClientID      identity.ClientIdentity
	Transform     *transform.TransformResult
	Resolution    *resolve.Resolution
	Candidates    []provider.Candidate
	Policy        *provider.Policy
	AuditBuilder  *audit.EventBuilder
	Capture       *audit.StreamCapture
	StreamWrapper StreamWrapperFunc
	SessionKey    string
	StickyKey     string
}

type ExecuteResult struct {
	Response  *http.Response
	Candidate provider.Candidate
	LatencyMs int
	RequestBody []byte
	ResponseBody []byte
}

type ExecuteError struct {
	LastErr   error
	Tried     int
	Exhausted bool
}

func (e *ExecuteError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("all %d candidates failed: %v", e.Tried, e.LastErr)
	}
	return fmt.Sprintf("all %d candidates failed", e.Tried)
}

func (e *Executor) Execute(params *ExecParams) (*ExecuteResult, error) {
	candidates := e.Router.PlanCandidates(
		params.Candidates,
		e.stickyCredentialID(params.StickyKey),
		params.Policy,
		egressPref(params.Transform),
	)
	if len(candidates) == 0 {
		return nil, &ExecuteError{Tried: 0, Exhausted: true}
	}

	tTotal := time.Now()
	retryPerCred := params.Policy.RetryPerCredential
	var lastErr error
	tried := 0

	for _, cand := range candidates {
		tried++

		if !e.Circuit.Allow(cand.ProviderID, cand.CredentialID) {
			slog.Debug("executor: circuit open, skipping candidate",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
			)
			lastErr = fmt.Errorf("circuit open for credential %d", cand.CredentialID)
			continue
		}

		release, acquireErr := e.Limiter.AcquireAll(
			params.R.Context(),
			cand.ProviderID,
			cand.CredentialID,
			params.ClientID.IdentityHash,
		)
		if acquireErr != nil {
			slog.Debug("executor: concurrency limit, skipping candidate",
				"credential_id", cand.CredentialID,
			)
			lastErr = acquireErr
			continue
		}

		result, execErr := e.tryCandidate(params, cand, retryPerCred, tTotal)
		release()

		if execErr == nil {
			e.restoreCredentialState(params.R.Context(), cand.CredentialID)
			e.recordStickySuccess(params, cand.CredentialID)
			return result, nil
		}

		if mnf, ok := execErr.(*modelNotFoundError); ok {
			e.disableModelOffer(params.R.Context(), mnf.credentialID, mnf.rawModel)
			lastErr = execErr
			continue
		}

		lastErr = execErr
		kind := errorsx.ClassifyError(execErr, nil)
		e.recordStickyFailure(params, cand.CredentialID, kind)
		e.Circuit.RecordFailure(cand.ProviderID, cand.CredentialID, kind)
		if e.shouldWriteCredentialStateOnConfirmedFailure(cand.ProviderID, cand.CredentialID, kind) {
			e.writeCredentialStateOnError(params.R.Context(), cand.CredentialID, kind, execErr)
		}
	}

	return nil, &ExecuteError{LastErr: lastErr, Tried: tried, Exhausted: true}
}

func (e *Executor) tryCandidate(
	params *ExecParams,
	cand provider.Candidate,
	maxRetries int,
	tTotal time.Time,
) (*ExecuteResult, error) {
	outboundModel := params.OutboundModel
	if outboundModel == "" {
		outboundModel = cand.RawModel
	}

	bodyBytes := params.BodyBytes
	if outboundModel != params.ClientModel {
		bodyBytes = replaceModelInRequestBody(bodyBytes, outboundModel)
	}
	if params.IsStream {
		bodyBytes = injectStreamOptions(bodyBytes)
	}
	if params.Transform != nil {
		bodyBytes = transform.ApplyRequestWhitelist(
			bodyBytes,
			params.Transform.PassthroughFields,
			params.Transform.StripRequestFields,
		)
	}
	if !transform.IsToolUseCapable(cand.CatalogCode) && transform.NeedsToolCollapse(bodyBytes) {
		bodyBytes = transform.CollapseToolHistory(bodyBytes)
	}
	bodyBytes = transform.ApplyCapabilitySanitizer(bodyBytes, cand.CatalogCode)
	bodyBytes = transform.MergeConsecutiveMessages(bodyBytes)

	if disguise.IsEnabled() && disguise.ShouldApply(bodyBytes) {
		profileName := ""
		if params.Transform != nil && params.Transform.DisguiseProfileID != "" {
			profileName = params.Transform.DisguiseProfileID
		} else if params.ClientID.Fingerprint.ClientProfile != "" {
			profileName = params.ClientID.Fingerprint.ClientProfile
		}
		if profileName != "" {
			bodyBytes, _ = disguise.Apply(bodyBytes, nil, nil, profileName, 0)
			slog.Debug("disguise layer applied", "profile", profileName)
		}
	}

	if params.SessionKey != "" && cand.SupportsPromptCache {
		bodyBytes, _ = injectCacheParams(bodyBytes, cand.CacheMode, params.SessionKey)
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(500*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-params.R.Context().Done():
				return nil, params.R.Context().Err()
			case <-time.After(delay):
			}
		}

		result, tryErr := func() (*ExecuteResult, error) {
			var reqPool *pool.Pool
			base := strings.TrimRight(cand.BaseURL, "/")
			upstreamURL := base + "/chat/completions"
			if cand.Protocol == "anthropic-messages" {
				upstreamURL = base + "/messages"
			}

			req, err := http.NewRequestWithContext(
				params.R.Context(),
				http.MethodPost,
				upstreamURL,
				bytes.NewReader(bodyBytes),
			)
			if err != nil {
				return nil, err
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+cand.APIKey)
			if params.IsStream {
				req.Header.Set("Accept", "text/event-stream")
			}
			req.Header.Set("X-Request-Id", params.R.Header.Get("X-Request-Id"))
			req.Header.Set("X-Virtual-Client-Id", params.ClientID.VirtualClientID)
			req.Header.Set("X-Virtual-IP", params.ClientID.VirtualIP)
			req.Header.Set("X-Virtual-MAC", params.ClientID.VirtualMAC)

			if params.Transform != nil {
				for _, h := range params.Transform.StripHeaders {
					req.Header.Del(h)
				}
				for k, v := range params.Transform.InjectHeaders {
					req.Header.Set(k, v)
				}
			}

			if e.HeaderProfiles != nil {
				prof := e.HeaderProfiles.load(params.R.Context(), cand.CatalogCode, cand.Protocol)
				if prof != nil {
					for k, v := range prof.Headers {
						req.Header.Set(k, v)
					}
				}
			}

			var httpClient *http.Client
			if e.Pools != nil {
				poolKey := pool.PoolKey{
					IdentityHash: params.ClientID.IdentityHash,
					ProviderID:   cand.ProviderID,
					CredentialID: cand.CredentialID,
				}
				if p := e.Pools.GetOrCreate(poolKey, ""); p != nil && p.State() == pool.PoolActive {
					reqPool = p
					httpClient = p.Client()
				}
			}
			if httpClient == nil {
				httpClient = http.DefaultClient
			} else if err := reqPool.Acquire(params.R.Context()); err != nil {
				return nil, err
			}
			if reqPool != nil {
				defer reqPool.Release()
			}

			timeout := e.UpstreamTimeout
			if params.IsStream {
				timeout = e.StreamTimeout
			}
			ctx, cancel := context.WithTimeout(params.R.Context(), timeout)
			defer cancel()
			req = req.WithContext(ctx)

			if e.Upstream != nil {
				req.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(bodyBytes)), nil
				}
			}

			var resp *http.Response
			var uErr *upstreampkg.Error
			if e.Upstream != nil {
				resp, uErr = e.Upstream.Do(req)
			} else {
				var doErr error
				resp, doErr = httpClient.Do(req)
				if doErr != nil {
					uErr = &upstreampkg.Error{Kind: errorsx.ClassifyError(doErr, nil), Message: doErr.Error(), Err: doErr}
				}
			}

			if uErr != nil && (resp == nil || resp.StatusCode >= 500) {
				errKind := uErr.Kind
				if errKind == errorsx.KindRateLimit {
					e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
					return nil, uErr
				}
				return nil, &retryableError{err: uErr}
			}

			if resp != nil && resp.StatusCode >= 400 {
				defer resp.Body.Close()
				body := make([]byte, 4096)
				n, _ := resp.Body.Read(body)
				errKind := errorsx.ClassifyError(nil, resp)

				if bodyKind := errorsx.ClassifyResponseBody(body[:n]); bodyKind == errorsx.KindModelNotFound {
					slog.Info("model_not_found skip offer",
						"credential_id", cand.CredentialID,
						"model", cand.RawModel,
						"status", resp.StatusCode,
						"body_preview", string(body[:min(n, 120)]),
					)
					return nil, &modelNotFoundError{
						credentialID: cand.CredentialID,
						rawModel:     cand.RawModel,
						body:         string(body[:n]),
					}
				}

				if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
					resp.StatusCode != 429 && resp.StatusCode != 401 &&
					resp.StatusCode != 403 && resp.StatusCode != 402 {
					e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
				} else if errKind == errorsx.KindRateLimit {
					e.Limiter.Shrink(cand.ProviderID, cand.CredentialID)
				}
				if !errorsx.IsRetryable(errKind) || attempt >= maxRetries {
					for k, vs := range resp.Header {
						for _, v := range vs {
							params.W.Header().Add(k, v)
						}
					}
					params.W.WriteHeader(resp.StatusCode)
					if n > 0 {
						params.W.Write(body[:n])
					}
					return nil, fmt.Errorf("upstream %d", resp.StatusCode)
				}
				return nil, &retryableError{err: fmt.Errorf("upstream %d", resp.StatusCode)}
			}

			e.Circuit.RecordSuccess(cand.ProviderID, cand.CredentialID)
			latencyMs := int(time.Since(tTotal).Milliseconds())

			if params.IsStream {
				if params.StreamWrapper != nil {
					params.StreamWrapper(params.W, resp, e.Normalize, params.Capture)
				} else if e.StreamChat != nil {
					e.StreamChat(params.W, resp, params.ClientModel, outboundModel, e.Normalize, params.Capture)
				}
				return &ExecuteResult{
					Response:    resp,
					Candidate:   cand,
					LatencyMs:   latencyMs,
					RequestBody: append([]byte(nil), bodyBytes...),
				}, nil
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodySize)+1))
			if err != nil {
				return nil, err
			}
			if len(respBody) > maxBodySize {
				slog.Warn("upstream response truncated", "size", len(respBody))
				respBody = respBody[:maxBodySize]
			}
			if params.ClientModel != "" {
				respBody = replaceModelInResponseBody(respBody, params.ClientModel)
			}
			if e.Normalize != nil {
				respBody = e.Normalize(respBody, false)
			}
			if !params.SuppressSuccessWrite {
				for k, vs := range resp.Header {
					for _, v := range vs {
						params.W.Header().Add(k, v)
					}
				}
				params.W.WriteHeader(resp.StatusCode)
				params.W.Write(respBody)
			}
			return &ExecuteResult{
				Response:     resp,
				Candidate:    cand,
				LatencyMs:    latencyMs,
				RequestBody:  append([]byte(nil), bodyBytes...),
				ResponseBody: append([]byte(nil), respBody...),
			}, nil
		}()

		if tryErr == nil {
			return result, nil
		}
		if _, ok := tryErr.(*retryableError); !ok {
			return nil, tryErr
		}
	}
	return nil, fmt.Errorf("exhausted %d retries for credential %d", maxRetries, cand.CredentialID)
}

func (e *Executor) restoreCredentialState(ctx context.Context, credentialID int) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if err := e.State.RestoreOnSuccess(ctx, credentialID); err != nil {
		slog.Debug("credential state restore failed", "credential_id", credentialID, "error", err)
	}
}

func (e *Executor) disableModelOffer(ctx context.Context, credentialID int, rawModel string) {
	if e.DB == nil || !e.DB.Enabled() {
		slog.Warn("disable_model_offer: no db pool available")
		return
	}
	pool := e.DB.Pool()
	tag, err := pool.Exec(ctx,
		"UPDATE model_offers SET available = FALSE WHERE credential_id = $1 AND raw_model_name = $2 AND available = TRUE",
		credentialID, rawModel,
	)
	if err != nil {
		slog.Warn("disable_model_offer failed", "credential_id", credentialID, "model", rawModel, "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("model_offer_disabled", "credential_id", credentialID, "model", rawModel)
	}
}

func (e *Executor) writeCredentialStateOnError(ctx context.Context, credentialID int, kind errorsx.ErrorKind, err error) {
	if e.State == nil || !e.State.Enabled() {
		return
	}
	if !shouldWriteCredentialState(kind) {
		return
	}
	failure := credentialstate.Failure{Kind: kind}
	if err != nil {
		failure.Detail = err.Error()
	}
	if err := e.State.WriteOnError(ctx, credentialID, failure); err != nil {
		slog.Debug("credential state error write failed", "credential_id", credentialID, "kind", kind, "error", err)
	}
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

func (e *Executor) recordStickySuccess(params *ExecParams, credentialID int) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil || params.StickyKey == "" || params.Policy == nil {
		return
	}
	stickyTTL := time.Duration(params.Policy.StickyTTLMilliseconds) * time.Second
	if stickyTTL < time.Minute {
		stickyTTL = time.Minute
	}
	e.Router.Sticky.RecordSuccess(params.StickyKey, credentialID, stickyTTL)
}

func (e *Executor) recordStickyFailure(params *ExecParams, credentialID int, kind errorsx.ErrorKind) {
	if e.Router == nil || e.Router.Sticky == nil || params == nil || params.StickyKey == "" {
		return
	}
	boundID, _, ok := e.Router.Sticky.GetEntry(params.StickyKey)
	if !ok || boundID != credentialID {
		return
	}
	if errorsx.IsCredentialFatal(kind) {
		e.Router.Sticky.Delete(params.StickyKey)
		return
	}
	if kind == errorsx.KindCanceled {
		return
	}
	e.Router.Sticky.RecordFailure(params.StickyKey, 3)
}

type modelNotFoundError struct {
	credentialID int
	rawModel     string
	body         string
}

func (e *modelNotFoundError) Error() string {
	return "model_not_found: " + e.rawModel
}

type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }

func shouldWriteCredentialState(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindAuth, errorsx.KindAuthRevoked,
		errorsx.KindQuota, errorsx.KindQuotaPeriodic, errorsx.KindQuotaBalance, errorsx.KindQuotaPermanent,
		errorsx.KindConcurrent, errorsx.KindRateLimit:
		return true
	default:
		return false
	}
}

func (e *Executor) shouldWriteCredentialStateOnConfirmedFailure(providerID, credentialID int, kind errorsx.ErrorKind) bool {
	if !shouldWriteCredentialState(kind) {
		return false
	}
	if e.Circuit == nil {
		return true
	}
	b := e.Circuit.GetOrCreate(providerID, credentialID)
	state := b.State()
	if state == circuit.StateOpen || state == circuit.StateQuarantined {
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

func egressPref(tx *transform.TransformResult) []string {
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

func executorMustMarshal(v any) json.RawMessage {
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
