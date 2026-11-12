// Package streaming — images_generations.go
//
// 2026-10-08 Phase C 骨架：POST /v1/images/generations（OpenAI 兼容）。
//
// 管理面与音频同构：鉴权 → GetCandidatesByModality(model, "image") →
// 逐候选透传上游 {base}/images/generations。routing_scope 硬隔离已由
// candidate SQL 保证（image 域只选 all|media_only）。
//
// 非目标（本骨架）：各厂商私有图生图桥接、异步 job、按张 MaaS 扣费
// （billing_unit=image 列已备好，扣费接入随首家实测补）。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

const (
	imageModalityRequest     = "image"
	maxImagesRequestBytes    = 8 << 20
	maxImagesResponseBytes   = 64 << 20
	maxImagesRequestDuration = 3 * time.Minute
)

type imageProviderResolver interface {
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
}

type imageKeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (*authentication.KeyInfo, error)
	CheckBudget(ctx context.Context, keyID int) error
}

// ImagesGenerationsHandler proxies OpenAI-compatible image generation.
type ImagesGenerationsHandler struct {
	provider    imageProviderResolver
	upstream    *upstream.Client
	keyVerifier imageKeyVerifier
	rateLimiter ratelimit.RPMLimiter
	// sanitizeInput / restoreHook 由 SetSanitizeGuard 注入（2026-11-13 脱敏域
	// 审计 P2 收口）：此前本端点完全绕过 SmartSaniGuard——prompt 明文出上游、
	// 响应占位符无从还原。nil 时行为与历史完全一致（零开销直通）。
	sanitizeInput func(http.Handler) http.Handler
	restoreHook   *sanitize.SanitizeRestoreInterceptor
}

func NewImagesGenerationsHandler(resolver imageProviderResolver, upstreamClient *upstream.Client) *ImagesGenerationsHandler {
	return &ImagesGenerationsHandler{provider: resolver, upstream: upstreamClient}
}

// SetSanitizeGuard 挂载 SmartSaniGuard 的请求侧中间件与响应侧占位符还原。
// 与 ChatHandler.SetSanitizeInputMiddleware 同一装配来源（installSmartSaniGuard）。
func (h *ImagesGenerationsHandler) SetSanitizeGuard(mw func(http.Handler) http.Handler, restore *sanitize.SanitizeRestoreInterceptor) {
	h.sanitizeInput = mw
	h.restoreHook = restore
}

// SanitizeGuardWired 装配完整探针：输入中间件与还原 hook **成对**在场才返回
// restore hook，否则 nil。装配序钉测用（半装配 = 只脱敏不还原 = 禁止态）。
func (h *ImagesGenerationsHandler) SanitizeGuardWired() *sanitize.SanitizeRestoreInterceptor {
	if h.sanitizeInput != nil && h.restoreHook != nil {
		return h.restoreHook
	}
	return nil
}

func (h *ImagesGenerationsHandler) SetAuth(keyVerifier *authentication.KeyVerifier, rateLimiter ratelimit.RPMLimiter) {
	h.keyVerifier = keyVerifier
	h.rateLimiter = rateLimiter
}

func (h *ImagesGenerationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "Method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}
	if h.provider == nil || h.upstream == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Image generation service unavailable", "server_error", "service_unavailable")
		return
	}

	keyInfo, ok := h.authenticate(w, r, requestID)
	if !ok {
		return
	}

	// SmartSaniGuard 入口（与 ChatHandler.ServeHTTP 同款形态）：租户身份先于
	// 中间件绑定（占位符映射按租户隔离，攻击者预置的租户头不被采信——
	// sanitize.WithAuthenticatedTenant 的契约），中间件改写 body 后
	// serveMediaInner 读到的已是脱敏后的请求。
	if h.sanitizeInput != nil {
		r = r.WithContext(sanitize.WithAuthenticatedTenant(r.Context(), mediaTenantID(keyInfo)))
		h.sanitizeInput(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			h.serveMediaInner(w, req, keyInfo)
		})).ServeHTTP(w, r)
		return
	}
	h.serveMediaInner(w, r, keyInfo)
}

// mediaTenantID 归一 media 面的租户身份：verifier 关停的本地形态与 chat 的
// no-verifier 模式同口径落 "default"，绝不采信请求头。
func mediaTenantID(keyInfo *authentication.KeyInfo) string {
	if keyInfo != nil && strings.TrimSpace(keyInfo.TenantID) != "" {
		return keyInfo.TenantID
	}
	return "default"
}

func (h *ImagesGenerationsHandler) serveMediaInner(w http.ResponseWriter, r *http.Request, keyInfo *authentication.KeyInfo) {
	requestID := w.Header().Get("X-Request-Id")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImagesRequestBytes+1))
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "failed to read body", "invalid_request_error", "invalid_body")
		return
	}
	if len(body) > maxImagesRequestBytes {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "request body too large", "invalid_request_error", "request_too_large")
		return
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || strings.TrimSpace(parsed.Model) == "" {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "model is required", "invalid_request_error", "missing_model")
		return
	}

	tenantID := "default"
	profile := ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		if keyInfo.DefaultClientProfile != nil {
			profile = *keyInfo.DefaultClientProfile
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxImagesRequestDuration)
	defer cancel()

	candidates, _, cerr := h.provider.GetCandidatesByModality(ctx, parsed.Model, profile, tenantID, imageModalityRequest)
	if cerr != nil {
		writeErrorJSON(w, http.StatusBadGateway, requestID, "candidate resolve failed", "server_error", "upstream_error")
		return
	}
	if len(candidates) == 0 {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
			fmt.Sprintf("no image-capable provider for model %q (check modality + routing_scope=media_only|all)", parsed.Model),
			"server_error", "no_provider")
		return
	}

	var lastErr error
	for _, cand := range candidates {
		status, respBody, ct, perr := h.proxyGenerations(ctx, cand, body)
		if perr != nil {
			lastErr = perr
			continue
		}
		if status >= 200 && status < 300 {
			// 响应侧占位符还原（输入侧脱敏的对称出口）。fail-closed：
			// 还原通道报错（含残余保留标记）时 502 拒单，绝不把内部
			// 标记或半还原原文漏给客户端；未启用时零开销直通。
			if h.restoreHook != nil {
				restored, changed, rerr := h.restoreHook.RestoreMediaBody(r.Context(), respBody)
				if rerr != nil {
					writeErrorJSON(w, http.StatusBadGateway, requestID, "response restore blocked", "server_error", "sanitize_restore_blocked")
					return
				}
				if changed && restored != nil {
					respBody = restored
				}
			}
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("X-Gw-Image-Transport", "images-generations")
			w.Header().Set("X-Gw-Upstream-Model", cand.RawModel)
			w.WriteHeader(status)
			_, _ = w.Write(respBody)
			return
		}
		lastErr = fmt.Errorf("upstream status %d: %s", status, truncateForErr(respBody, 256))
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			continue
		}
		// Non-retryable client errors: surface immediately.
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			writeErrorJSON(w, status, requestID, sanitizeAudioErrorMessage(lastErr.Error()), "invalid_request_error", "upstream_error")
			return
		}
	}
	msg := "all image candidates failed"
	if lastErr != nil {
		msg = sanitizeAudioErrorMessage(lastErr.Error())
	}
	writeErrorJSON(w, http.StatusBadGateway, requestID, msg, "server_error", "upstream_error")
}

func (h *ImagesGenerationsHandler) proxyGenerations(ctx context.Context, cand provider.Candidate, body []byte) (int, []byte, string, error) {
	endpoint := strings.TrimRight(cand.BaseURL, "/") + "/images/generations"
	// Rewrite model to upstream-facing name while preserving other fields.
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, nil, "", err
	}
	payload["model"] = cand.RawModel
	out, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, "", err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(out))
	if err != nil {
		return 0, nil, "", err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("X-Gateway-Internal-Purpose", "image_generation")

	resp, upErr := h.upstream.Do(upReq)
	if upErr != nil {
		return 0, nil, "", upErr
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := readLimitedResponse(resp.Body, maxImagesResponseBytes)
	if err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, respBody, resp.Header.Get("Content-Type"), nil
}

func (h *ImagesGenerationsHandler) authenticate(w http.ResponseWriter, r *http.Request, requestID string) (*authentication.KeyInfo, bool) {
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		return nil, true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Missing API key", "authentication_error", "missing_api_key")
		return nil, false
	}
	keyInfo, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil {
		writeErrorJSON(w, http.StatusUnauthorized, requestID, "Invalid API key", "authentication_error", "invalid_api_key")
		return nil, false
	}
	if keyInfo.Status == "throttled" {
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Key throttled", "rate_limit_error", "key_throttled")
		return nil, false
	}
	if outcome := checkGatewayRateLimit(r.Context(), keyInfo, h.rateLimiter, nil); !outcome.Skipped {
		writeErrorJSON(w, http.StatusTooManyRequests, requestID, "Rate limit exceeded", "rate_limit_error", "rate_limited")
		return nil, false
	}
	if err := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); err != nil {
		writeErrorJSON(w, http.StatusPaymentRequired, requestID, "Budget exceeded", "insufficient_quota", "budget_exceeded")
		return nil, false
	}
	return keyInfo, true
}

func truncateForErr(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
