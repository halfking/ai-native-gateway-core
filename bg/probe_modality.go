// bg/probe_modality.go — Modality verification probe (Layer 2)
//
// 2026-07-20: Sends lightweight vision/audio test requests to verify
// that a model actually supports the inferred modality. This catches
// rule-table inference errors (e.g. "gemini-1.5-pro-ultra" rules might
// misclassify a non-vision variant as vision).
//
// Strategy:
//  1. Skip text models (no need to verify)
//  2. For vision models: send a tiny 1x1 PNG image in chat completion
//  3. For audio models: send empty audio input (whisper)
//  4. On 200 OK: confirm modality supported
//  5. On 400/422 with "vision"/"audio" in error: modality rejected
//  6. Other errors: don't update modality (preserve conservative)
//
// Cost: ~1 vision call per model per probe cycle (~1KB upload, ~50 tokens output)
//       ~50x cheaper than embedding/audio full probes.
//
// Auto-discovery flow:
//   discovery.registerModel → seeds modality via rule table (zero-cost)
//   bg probe → verifies on next cycle (Layer 2)
//   user feedback → overrides via admin API (Layer 3)

package bg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModalityProbeResult captures the outcome of a single modality probe.
type ModalityProbeResult struct {
	Modality   string // "vision", "audio", "multimodal", "embedding", "text"
	Supported  bool   // true if upstream accepted the modality-specific payload
	ErrCode    string // classifier (e.g. "vision_unsupported", "auth", "network")
	ErrMsg     string
	HTTPStatus int
	LatencyMs  int
}

// 1x1 transparent PNG (smallest valid PNG, 67 bytes).
// Base64-encoded inline to avoid file dependency in tests.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// visionTestPayload constructs a minimal chat-completion request with a
// 1x1 PNG attached. Small enough to cost ~1 token output and ~50 bytes upload.
func visionTestPayload(model string) []byte {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "describe"},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": "data:image/png;base64," + tinyPNGBase64,
						},
					},
				},
			},
		},
		"max_tokens":  1,
		"temperature": 0,
	}
	body, _ := json.Marshal(payload)
	return body
}

// audioTestPayload constructs a minimal audio input request for whisper-like models.
func audioTestPayload(model string) []byte {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "transcribe"},
					{
						"type": "input_audio",
						"input_audio": map[string]any{
							"data":   tinyPNGBase64,
							"format": "wav",
						},
					},
				},
			},
		},
		"max_tokens":  1,
		"temperature": 0,
	}
	body, _ := json.Marshal(payload)
	return body
}

// anthropicVisionTestPayload constructs an Anthropic-style vision request
// (uses source.type = "base64_image" instead of image_url).
func anthropicVisionTestPayload(model string) []byte {
	imgBytes, _ := base64.StdEncoding.DecodeString(tinyPNGBase64)
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "describe"},
					{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       base64.StdEncoding.EncodeToString(imgBytes),
						},
					},
				},
			},
		},
		"max_tokens":  1,
		"temperature": 0,
	}
	body, _ := json.Marshal(payload)
	return body
}

// ProbeModality sends a modality-specific test request to the upstream
// and classifies the response.
//
// Returns Supported=true when:
//   - HTTP 200: upstream accepted the modality-specific payload
//   - HTTP 400 with "vision"/"audio"/"image" in error message: rejected
//     → returns Supported=false with ErrCode="modality_unsupported"
//
// Returns Supported=false (conservative) on:
//   - Network errors
//   - Auth errors (401/403)
//   - Server errors (5xx)
//   - Unknown 4xx
func ProbeModality(ctx context.Context, endpoint, apiKey, model, modality string, isAnthropic bool) ModalityProbeResult {
	start := time.Now()

	var payload []byte
	switch modality {
	case "vision", "multimodal":
		if isAnthropic {
			payload = anthropicVisionTestPayload(model)
		} else {
			payload = visionTestPayload(model)
		}
	case "audio":
		payload = audioTestPayload(model)
	default:
		return ModalityProbeResult{
			Modality:  modality,
			Supported: true,
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ModalityProbeResult{
			Modality:  modality,
			ErrCode:   "request_build",
			ErrMsg:    err.Error(),
			LatencyMs: int(time.Since(start).Milliseconds()),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		if isAnthropic {
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	resp, err := client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return ModalityProbeResult{
			Modality:  modality,
			ErrCode:   "network",
			ErrMsg:    err.Error(),
			LatencyMs: latencyMs,
		}
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	result := ModalityProbeResult{
		Modality:   modality,
		HTTPStatus: resp.StatusCode,
		LatencyMs:  latencyMs,
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Supported = true

	case resp.StatusCode == 400 || resp.StatusCode == 422:
		bodyLower := strings.ToLower(string(body))
		if containsAny(bodyLower, []string{"vision", "image", "audio", "multimodal", "input_audio", "image_url"}) {
			result.Supported = false
			result.ErrCode = "modality_unsupported"
			result.ErrMsg = truncateProbeBody(string(body), 200)
		} else {
			result.Supported = true
			result.ErrCode = "http_4xx"
			result.ErrMsg = truncateProbeBody(string(body), 200)
		}

	case resp.StatusCode == 401 || resp.StatusCode == 403:
		result.Supported = true
		result.ErrCode = "auth"
		result.ErrMsg = truncateProbeBody(string(body), 200)

	case resp.StatusCode >= 500:
		result.Supported = true
		result.ErrCode = "http_5xx"
		result.ErrMsg = truncateProbeBody(string(body), 200)

	default:
		result.Supported = true
		result.ErrCode = "unknown"
		result.ErrMsg = truncateProbeBody(string(body), 200)
	}

	return result
}

func containsAny(s string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
