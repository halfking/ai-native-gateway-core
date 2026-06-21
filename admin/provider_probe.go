package admin

// provider_probe.go — extracted from providers.go (2026-06-21 audit §3
// single-file-bloat remediation). The probe helpers (probeURLResult +
// probeResult + chatResult types and isAcceptableStatus / doProbeRequest /
// doChatProbe functions) form a self-contained cluster: they only depend
// on stdlib (context / fmt / io / net/http / strings / time / encoding/json
// / bytes) plus internal/probeutil. Splitting them out brings
// admin/providers.go from 4555 lines down to 4305 (a 250-line cut) and
// gives the probe code its own natural home for future tests (probe logic
// is the single most-touchy area, per the v6.0 audit "ModelsTab.vue 6/21
// single-day 6 commits" finding).
//
// All unexported types (probeResult / chatResult / probeURLResult) stay in
// the admin package — they have callers in providers.go (handleProviders
// / doProbeURL / diagnoseProvider etc.) that reference them by short name.
// A future pass can move them to internal/probe if the call graph grows.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probeutil"
)

// probeURLResult is the response shape for both probe-url endpoints.
type probeURLResult struct {
	Reachable    bool     `json:"reachable"`
	Protocol     string   `json:"protocol,omitempty"`
	HTTPStatus   int      `json:"http_status,omitempty"`
	ModelsCount  int      `json:"models_count,omitempty"`
	SampleModels []string `json:"sample_models,omitempty"`
	AuthOK       bool     `json:"auth_ok,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type probeResult struct {
	statusCode   int
	modelCount   int
	sampleModels []string
	authOK       bool   // whether the credential is authoritative for this base
	probeURL     string // which URL candidate succeeded
}

// isAcceptableStatus mirrors the Python probe loop: 200/401/403 indicate
// the endpoint exists and responds. 401/403 are accepted (auth needed);
// the caller's apiKey presence determines whether that means "ok".
func isAcceptableStatus(code int) bool {
	return code == http.StatusOK || code == http.StatusUnauthorized || code == http.StatusForbidden
}

// doProbeRequest tries each candidate URL in turn and returns the first
// one whose response is 200/401/403. Mirrors free_pool_probe.probe_openai_compatible_base
// in the Python llm-gateway. Returns the last transport error if all
// candidates fail.
//
// authOK semantics:
//   - response 200 + non-empty apiKey         → true
//   - response 200 + empty apiKey              → true  (endpoint reachable, no auth challenge)
//   - response 401/403 + non-empty apiKey     → false (endpoint exists, but credential rejected)
//   - response 401/403 + empty apiKey         → true  (endpoint exists, requires auth)
func doProbeRequest(ctx context.Context, urls []string, apiKey string) (*probeResult, error) {
	var lastErr error
	hasKey := strings.TrimSpace(apiKey) != ""

	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if hasKey {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if !isAcceptableStatus(resp.StatusCode) {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}

		result := &probeResult{
			statusCode: resp.StatusCode,
			probeURL:   u,
		}
		if resp.StatusCode == http.StatusOK {
			// Parse models list (tolerate alternative shapes like {models:[...]})
			var modelsResp struct {
				Data   []map[string]any `json:"data"`
				Models []map[string]any `json:"models"`
			}
			if json.Unmarshal(body, &modelsResp) == nil {
				rows := modelsResp.Data
				if len(rows) == 0 {
					rows = modelsResp.Models
				}
				result.modelCount = len(rows)
				limit := 3
				if len(rows) < limit {
					limit = len(rows)
				}
				for i := 0; i < limit; i++ {
					if id, ok := rows[i]["id"].(string); ok {
						result.sampleModels = append(result.sampleModels, id)
					} else if name, ok := rows[i]["name"].(string); ok {
						result.sampleModels = append(result.sampleModels, name)
					}
				}
			}
		}

		// authOK
		switch resp.StatusCode {
		case http.StatusOK:
			result.authOK = true
		case http.StatusUnauthorized, http.StatusForbidden:
			result.authOK = !hasKey // requires auth but no key supplied = expected
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate URLs")
	}
	return nil, lastErr
}

type chatResult struct {
	statusCode      int
	modelInResponse string
	// errorCode is set when the 404 body indicates the provider requires
	// an endpoint ID (outbound_model_name) rather than a raw model name.
	// The diagnose UI uses this to render a friendly hint.
	errorCode string
}

func doChatProbe(ctx context.Context, url, apiKey, model string) (*chatResult, error) {
	payload := map[string]any{
		"model":      model,
		"max_tokens": 5,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	result := &chatResult{statusCode: resp.StatusCode}

	if resp.StatusCode == http.StatusOK {
		var chatResp struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(respBody, &chatResp) == nil {
			result.modelInResponse = chatResp.Model
		}
	} else if resp.StatusCode == http.StatusNotFound &&
		probeutil.IsEndpointIDRequiredError(string(respBody)) {
		// 404 + "InvalidEndpointOrModel" / "endpoint not found" — the chosen
		// probe model needs an endpoint ID.  Surface this so the diagnose UI
		// can render a hint instead of a misleading "404 model not found".
		result.errorCode = probeutil.EndpointIDRequiredErrCode
	}

	return result, nil
}
