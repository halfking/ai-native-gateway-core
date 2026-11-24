package bg

// node_probe_multi_endpoint.go — 多 endpoint 探针补齐（2026-11-17）。
//
// ★ 背景：resolveDirectTarget 已对齐 provider_endpoint_protocols（协议匹配
//   的 primary），但状态键仍是 (credential_id, raw_model_name) —— 同一协议
//   下若有多个 pep 行，路由可能按 weight 打到未被探过的兄弟端点。
//
// ★ 现网：pep 行数 >1 的供应商 = 0 家 ⇒ 本文件路径在生产是空操作；
//   结构上先把「同协议全部端点都探一遍」补上，避免日后 NIM 多 endpoint
//   上线时再出现「探针绿、路由打红」的错位。
//
// ★ 边界（零迁移）：
//   · 状态键仍是 (cred, model) —— 逐 endpoint 状态键需 schema，另案；
//   · 任一兄弟端点失败 ⇒ 整轮失败（路由可能打到该兄弟）；
//   · 只在 primary 已成功时才补探兄弟（primary 失败本就足够翻红）。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// listMatchedEndpointBaseURLs returns every enabled pep base_url that matches
// the provider's protocol, primary first. Empty slice ⇒ caller keeps the
// resolveDirectTarget URL (providers.base_url fallback path).
func (w *NodeProbeWorker) listMatchedEndpointBaseURLs(ctx context.Context, providerID int, protocol string) ([]string, error) {
	if w == nil || w.db == nil || providerID <= 0 {
		return nil, nil
	}
	if protocol == "" {
		protocol = "openai-completions"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := w.db.Query(queryCtx, `
		SELECT pep.base_url
		  FROM provider_endpoint_protocols pep
		 WHERE pep.provider_id = $1
		   AND pep.enabled = TRUE
		   AND pep.protocol = $2
		 ORDER BY pep.is_primary DESC, pep.weight DESC, pep.id ASC
	`, providerID, protocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return out, err
		}
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, rows.Err()
}

// confirmSiblingEndpoints probes non-primary protocol-matched pep URLs after
// the primary direct round succeeded. Returns the (possibly failed) round.
func (w *NodeProbeWorker) confirmSiblingEndpoints(
	ctx context.Context,
	primary nodeProbeRoundResult,
	plain, protocol string,
	credID, providerID int,
	primaryBaseURL, bodyModel string,
	shape probeShape,
) nodeProbeRoundResult {
	if !primary.ok || w == nil {
		return primary
	}
	urls, err := w.listMatchedEndpointBaseURLs(ctx, providerID, protocol)
	if err != nil {
		slog.Warn("node_probe_worker: listMatchedEndpointBaseURLs failed; keeping primary result",
			"provider_id", providerID, "protocol", protocol, "error", err)
		return primary
	}
	if len(urls) <= 1 {
		return primary
	}
	primaryNorm := strings.TrimRight(strings.TrimSpace(primaryBaseURL), "/")
	ctx = withPlatformProbeProvider(ctx, providerID)
	for _, u := range urls {
		if u == primaryNorm {
			continue
		}
		sib := w.probeDirectBaseURL(ctx, plain, protocol, u, credID, bodyModel, shape)
		if sib.ok || sib.observationOnly {
			slog.Info("node_probe_worker: sibling endpoint ok",
				"provider_id", providerID, "base_url", u, "model", bodyModel)
			continue
		}
		sib.rootCause = classifyProbeRootCause(sib.errCode, sib.httpStatus, sib.responseBody)
		annotateRootCause(&sib)
		sib.errDetail = fmt.Sprintf(
			"sibling endpoint failed url=%s detail=%s (primary url=%s was ok)",
			u, sib.errDetail, primaryBaseURL)
		slog.Warn("node_probe_worker: sibling endpoint failed — marking binding unhealthy",
			"provider_id", providerID, "sibling", u, "primary", primaryBaseURL,
			"model", bodyModel, "err_code", sib.errCode)
		return sib
	}
	return primary
}

// probeDirectBaseURL runs one direct probe against an explicit base URL
// (no resolve / decrypt). Used for sibling pep endpoints.
func (w *NodeProbeWorker) probeDirectBaseURL(
	ctx context.Context,
	plain, protocol, baseURL string,
	credID int,
	bodyModel string,
	shape probeShape,
) (r nodeProbeRoundResult) {
	r = nodeProbeRoundResult{errCode: "none"}
	endpoint := upstreamurl.Build(baseURL, shape.endpoint)
	r.requestURL = endpoint
	r.requestBody = shape.body
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(shape.body))
	if err != nil {
		r.errCode = "request_build"
		r.errDetail = fmt.Sprintf("build request failed: %s (url=%s)", err.Error(), endpoint)
		return r
	}
	if strings.HasPrefix(protocol, "anthropic") {
		req.Header.Set("x-api-key", plain)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+plain)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLM-Origin-Stage", "node_probe")
	req.Header.Set("X-LLM-Origin-Actor", "node-probe-worker-sibling")
	start := time.Now()
	resp, err := platformProbeDo(ctx, w.probeClient, req, credID, bodyModel, "node_probe_sibling_endpoint")
	r.latencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		code, timedOut := classifyProbeNetworkError(err)
		r.errCode = code
		r.timedOut = timedOut
		r.errDetail = fmt.Sprintf("sibling upstream %s: %s (url=%s)", code, err.Error(), endpoint)
		return r
	}
	defer resp.Body.Close()
	r.httpStatus = resp.StatusCode
	response, readErr := readCredentialProbeBody(resp.Body)
	setNodeProbeResponse(&r, shape.wire, response, readErr)
	return r
}
