// Package streaming — videos_generations.go
//
// 2026-10-08 Phase C：POST /v1/videos/generations 骨架（OpenAI 风格统一出口）。
// 多数上游视频 API 是异步 job；本骨架先做：
//   鉴权 → modality=video 候选（routing_scope=all|media_only）→
//   透传 {base}/videos/generations；若全员 404/无候选则 501 明示未就绪。
// 完整 async poll（/v1/videos/{id}）留后续切片。
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
	"github.com/kaixuan/llm-gateway-go/upstream"
)

const (
	videoModalityRequest     = "video"
	maxVideosRequestBytes    = 8 << 20
	maxVideosResponseBytes   = 64 << 20
	maxVideosRequestDuration = 5 * time.Minute
)

type videoProviderResolver interface {
	GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]provider.Candidate, *provider.Policy, error)
}

// VideosGenerationsHandler proxies video-generation requests when upstream
// exposes an OpenAI-shaped /videos/generations endpoint.
type VideosGenerationsHandler struct {
	provider    videoProviderResolver
	upstream    *upstream.Client
	keyVerifier imageKeyVerifier // same auth surface as images
	rateLimiter ratelimit.RPMLimiter
}

func NewVideosGenerationsHandler(resolver videoProviderResolver, upstreamClient *upstream.Client) *VideosGenerationsHandler {
	return &VideosGenerationsHandler{provider: resolver, upstream: upstreamClient}
}

func (h *VideosGenerationsHandler) SetAuth(keyVerifier *authentication.KeyVerifier, rateLimiter ratelimit.RPMLimiter) {
	h.keyVerifier = keyVerifier
	h.rateLimiter = rateLimiter
}

func (h *VideosGenerationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Video generation service unavailable", "server_error", "service_unavailable")
		return
	}

	// Reuse images auth helper shape via local copy (same checks).
	img := &ImagesGenerationsHandler{keyVerifier: h.keyVerifier, rateLimiter: h.rateLimiter}
	keyInfo, ok := img.authenticate(w, r, requestID)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxVideosRequestBytes+1))
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, requestID, "failed to read body", "invalid_request_error", "invalid_body")
		return
	}
	if len(body) > maxVideosRequestBytes {
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

	ctx, cancel := context.WithTimeout(r.Context(), maxVideosRequestDuration)
	defer cancel()

	candidates, _, cerr := h.provider.GetCandidatesByModality(ctx, parsed.Model, profile, tenantID, videoModalityRequest)
	if cerr != nil {
		writeErrorJSON(w, http.StatusBadGateway, requestID, "candidate resolve failed", "server_error", "upstream_error")
		return
	}
	if len(candidates) == 0 {
		writeErrorJSON(w, http.StatusNotImplemented, requestID,
			fmt.Sprintf("no video-capable provider for model %q; bind a modality=video model on a media_only|all credential", parsed.Model),
			"server_error", "no_provider")
		return
	}

	var lastErr error
	sawNotFound := 0
	for _, cand := range candidates {
		status, respBody, ct, perr := h.proxyGenerations(ctx, cand, body)
		if perr != nil {
			lastErr = perr
			continue
		}
		if status >= 200 && status < 300 {
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("X-Gw-Video-Transport", "videos-generations")
			w.Header().Set("X-Gw-Upstream-Model", cand.RawModel)
			w.WriteHeader(status)
			_, _ = w.Write(respBody)
			return
		}
		lastErr = fmt.Errorf("upstream status %d: %s", status, truncateForErr(respBody, 256))
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			sawNotFound++
			continue
		}
		if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
			writeErrorJSON(w, status, requestID, sanitizeAudioErrorMessage(lastErr.Error()), "invalid_request_error", "upstream_error")
			return
		}
	}
	if sawNotFound == len(candidates) {
		writeErrorJSON(w, http.StatusNotImplemented, requestID,
			"upstream has no /videos/generations; async video adapters not yet wired",
			"server_error", "not_implemented")
		return
	}
	msg := "all video candidates failed"
	if lastErr != nil {
		msg = sanitizeAudioErrorMessage(lastErr.Error())
	}
	writeErrorJSON(w, http.StatusBadGateway, requestID, msg, "server_error", "upstream_error")
}

func (h *VideosGenerationsHandler) proxyGenerations(ctx context.Context, cand provider.Candidate, body []byte) (int, []byte, string, error) {
	endpoint := strings.TrimRight(cand.BaseURL, "/") + "/videos/generations"
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
	upReq.Header.Set("X-Gateway-Internal-Purpose", "video_generation")

	resp, upErr := h.upstream.Do(upReq)
	if upErr != nil {
		return 0, nil, "", upErr
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := readLimitedResponse(resp.Body, maxVideosResponseBytes)
	if err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, respBody, resp.Header.Get("Content-Type"), nil
}
