// Package discovery — local_context.go
//
// 2026-09-07 本地托管供应商（providers.kind='local'）的 context window 回填。
//
// 背景：migration 671 引入本地托管目录（local-ollama / local-mlx-lm /
// local-mlx-dspark / local-llamacpp / local-lmstudio / local-vllm）。本地
// 推理服务的 /v1/models 大多不携带上下文长度，而网关的请求校验、auto 路由
// 长上下文判定都依赖 credential_model_bindings.context_window_override。
// 本文件在 discovery 拉取模型清单后，按优先级解析上下文并回填：
//
//  1. /v1/models 原始响应字段（vLLM: max_model_len；部分服务:
//     context_length / context_window）
//  2. ollama: POST {base}/api/show 逐模型读取 model_info.<arch>.context_length
//  3. provider_catalog.capabilities.default_context_window 兜底
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// localContextWindowSource 写入 credential_model_bindings.context_window_source，
// 与 'manual'（运营手工设置）区分，便于审计与后续覆盖策略。
const localContextWindowSource = "local-discovery"

// localShowProbeLimit 限制 ollama /api/show 逐模型查询次数，避免模型很多时
// discovery 变慢（本地 ollama 常驻模型一般 < 10 个）。
const localShowProbeLimit = 12

// localShowTimeout 单次 /api/show 查询超时。
const localShowTimeout = 3 * time.Second

// PGLike 是 ApplyLocalContextWindows 需要的最小 DB 接口
// （*pgxpool.Pool 与 pgx.Tx 都满足）。
type PGLike interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// defaultHTTPClient ollama /api/show 探测复用独立短超时客户端，
// 不与 discovery 主客户端共享连接池。
var defaultHTTPClient = &http.Client{Timeout: localShowTimeout}

func isLocalProviderKind(kind string) bool {
	return strings.TrimSpace(strings.ToLower(kind)) == "local"
}

// applyLocalContextWindows 为本地供应商的全部绑定模型回填 context window。
// 全程 best-effort：任何失败只记日志，不影响 discovery 主流程。
func (s *Service) applyLocalContextWindows(ctx context.Context, cred credential, models []string, rawModelsJSON []byte) {
	ApplyLocalContextWindows(ctx, s.db, cred.ProviderKind, cred.CatalogCapabilities, cred.BaseURL, cred.ID, models, rawModelsJSON)
}

// ApplyLocalContextWindows 是本地供应商 context window 回填的导出入口，
// 供 discovery 周期任务与 admin refresh-models 两条管道共用。
// rawModelsJSON 允许为 nil（admin 管道暂不透传原始响应），此时跳过
// /models 字段解析，仍走 ollama /api/show 与 catalog 兜底。
func ApplyLocalContextWindows(
	ctx context.Context,
	db PGLike,
	providerKind string,
	catalogCapabilities []byte,
	baseURL string,
	credentialID int,
	models []string,
	rawModelsJSON []byte,
) {
	if !isLocalProviderKind(providerKind) || len(models) == 0 {
		return
	}
	def := localDefaultContextWindow(catalogCapabilities)
	hosting := localHostingType(catalogCapabilities)

	perModel := parseContextFieldsFromModelsJSON(rawModelsJSON)
	// ollama 的 /v1/models 不带上下文，逐模型 /api/show 查询。
	if hosting == "ollama" {
		probeOllamaContextLengths(ctx, defaultHTTPClient, baseURL, models, perModel)
	}

	updated := 0
	for _, rawName := range models {
		ctxLen := perModel[normalizeModelKey(rawName)]
		if ctxLen <= 0 {
			ctxLen = def
		}
		if ctxLen <= 0 {
			continue
		}
		tag, err := db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET context_window_override = $1,
			    context_window_source = $2,
			    context_window_updated_at = NOW()
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND cmb.credential_id = $3
			  AND pm.raw_model_name = $4
			  AND (cmb.context_window_source IS NULL
			       OR cmb.context_window_source NOT IN ('manual'))
		`, ctxLen, localContextWindowSource, credentialID, rawName)
		if err != nil {
			slog.Warn("local discovery: context window update failed",
				"credential_id", credentialID, "model", rawName, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}
	slog.Info("local discovery: context windows applied",
		"credential_id", credentialID,
		"provider_kind", providerKind,
		"hosting_type", hosting,
		"default_context_window", def,
		"updated", updated,
		"models", len(models),
	)
}

// localHostingType 读取 catalog capabilities.hosting_type。
func localHostingType(capabilitiesJSON []byte) string {
	var caps struct {
		HostingType string `json:"hosting_type"`
	}
	if len(capabilitiesJSON) > 0 {
		if err := json.Unmarshal(capabilitiesJSON, &caps); err == nil {
			return strings.TrimSpace(strings.ToLower(caps.HostingType))
		}
	}
	return ""
}

// localDefaultContextWindow 读取 catalog capabilities.default_context_window。
func localDefaultContextWindow(capabilitiesJSON []byte) int {
	var caps struct {
		DefaultContextWindow int `json:"default_context_window"`
	}
	if len(capabilitiesJSON) > 0 {
		if err := json.Unmarshal(capabilitiesJSON, &caps); err == nil {
			return caps.DefaultContextWindow
		}
	}
	return 0
}

// normalizeModelKey 统一 model id 匹配键（小写、去空白）。
func normalizeModelKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// parseContextFieldsFromModelsJSON 扫描 /v1/models 响应里的 context 字段。
// 兼容三种形态：
//
//	{"data": [{"id": "m", "max_model_len": 32768}]}           — vLLM
//	{"data": [{"id": "m", "context_length": 32768}]}          — llama.cpp
//	{"data": [{"id": "m", "context_window": 32768}]}          — 通用命名
//	{"models": [{"name": "m", ...}]}                          — ollama 原生（少见）
func parseContextFieldsFromModelsJSON(data []byte) map[string]int {
	out := map[string]int{}
	if len(data) == 0 {
		return out
	}
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			MaxModelLen   int    `json:"max_model_len"`
			ContextLength int    `json:"context_length"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
		Models []struct {
			Name          string `json:"name"`
			MaxModelLen   int    `json:"max_model_len"`
			ContextLength int    `json:"context_length"`
			ContextWindow int    `json:"context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out
	}
	for _, m := range doc.Data {
		key := m.ID
		if key == "" {
			key = m.Name
		}
		if v := firstPositive(m.MaxModelLen, m.ContextLength, m.ContextWindow); v > 0 && key != "" {
			out[normalizeModelKey(key)] = v
		}
	}
	for _, m := range doc.Models {
		if v := firstPositive(m.MaxModelLen, m.ContextLength, m.ContextWindow); v > 0 && m.Name != "" {
			out[normalizeModelKey(m.Name)] = v
		}
	}
	return out
}

func firstPositive(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// probeOllamaContextLengths 调 ollama POST /api/show 读取每个模型的
// context_length（model_info 键形如 "qwen3.context_length"）。
// 结果写回 perModel（就地更新），失败静默跳过。
func probeOllamaContextLengths(ctx context.Context, client *http.Client, baseURL string, models []string, perModel map[string]int) {
	if client == nil {
		client = http.DefaultClient
	}
	showURL := strings.TrimRight(baseURL, "/") + "/api/show"
	probed := 0
	for _, rawName := range models {
		if probed >= localShowProbeLimit {
			break
		}
		if perModel[normalizeModelKey(rawName)] > 0 {
			continue // /models 已给出，无需再查
		}
		probed++
		pctx, cancel := context.WithTimeout(ctx, localShowTimeout)
		body, _ := json.Marshal(map[string]string{"model": rawName})
		req, err := http.NewRequestWithContext(pctx, http.MethodPost, showURL, bytes.NewReader(body))
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		// 本地服务不校验 Authorization；占位头保持请求形态与其他客户端一致。
		req.Header.Set("Authorization", "Bearer local")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		respBody := buf.Bytes()
		_ = resp.Body.Close()
		cancel()

		if resp.StatusCode != 200 {
			continue
		}
		if cl := parseOllamaShowContextLength(respBody); cl > 0 {
			perModel[normalizeModelKey(rawName)] = cl
		}
	}
}

// parseOllamaShowContextLength 从 /api/show 响应中提取 context_length。
// 响应形如 {"model_info": {"qwen3.context_length": 40960, ...}, ...}。
func parseOllamaShowContextLength(data []byte) int {
	var doc struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0
	}
	for k, v := range doc.ModelInfo {
		if !strings.HasSuffix(strings.ToLower(k), ".context_length") && k != "context_length" {
			continue
		}
		switch n := v.(type) {
		case float64:
			if n > 0 {
				return int(n)
			}
		case string:
			var parsed int
			if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &parsed); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}
