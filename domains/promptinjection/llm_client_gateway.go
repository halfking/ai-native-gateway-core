// Package promptinjection - LLM 客户端实现
// 用途：集成网关已有的 LLM 调用能力，供错误分析器使用
//
// ★ 2026-11-17 审计轮 P0：原实现是**返回成功的空桩**：
//
//	func (c *GatewayLLMClient) Analyze(...) (string, int, error) {
//	    // TODO: 实现实际的 LLM 调用
//	    response := ""
//	    tokensUsed := 0
//	    return response, tokensUsed, nil        // ← 无错误！
//	}
//
//	⇒ parseResponse("") ⇒ extractJSON("") ⇒ 无 "{" ⇒ 返回 "" ⇒
//	「no valid JSON found in response」⇒ 每一条队列记录都会失败 3 次
//	并被标成 failed，而日志只有一行 Info「sending analysis request」，
//	看起来像在正常工作 ⇒ **静默全失败**。
//	⇒ 现实现真实调用；未配置时**显式报错**（绝不返回空成功）。
//
// ★ 为什么指向**外部**分析端点而不是网关自己（2026-11-17 审计轮 P1，递归风险）：
//
//	原设计（docs/pi-agent-*.md）建议「回环调用网关自己的 /v1/chat/completions」。
//	但分析提示词里装的是**攻击者可控的原始载荷** ⇒ 该请求对 PI 检测器而言
//	**本身就是攻击流量** ⇒
//	  · fail-closed 打开 ⇒ 分析请求被自己的防护阻断 ⇒ 分析永远失败；
//	  · fail-open（默认）⇒ 载荷被判为注入 ⇒ 记一条误报 ⇒ **被收集进队列**
//	    ⇒ 被分析 ⇒ 再次回环 ⇒ **自放大循环**（每轮多一条队列记录 + 一次 LLM 花费）。
//	⇒ 改用本仓**既有正典**约定（cmd/gateway/main_pipeline.go:1472-1476 的
//	LLMGATEWAY_ANALYSIS_BASE_URL / LLM_GATEWAY_ANALYSIS_API_KEY +
//	validateAnalysisEndpoint），即**外部**分析端点 ⇒ 结构性地不经过
//	网关自己的请求链路 ⇒ 没有递归面。
//	⇒ 端点合法性由**接线处**（cmd/gateway）用 validateAnalysisEndpoint 校验后
//	再传进来，本包不重复实现 SSRF 闸（但仍做一次最基本的存在性检查）。
package promptinjection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 分析器公共常量（原在 llm_client_impl.go；2026-11-18 死面清理随该文件迁入——
// HTTPLLMClient 是 GatewayLLMClient 的修复前旧拷贝（默认回环端点、无禁重定向/
// 空内容守卫），零调用方 ⇒ 按退役纪律删除，生产实现以本文件为准）。
const (
	defaultAnalyzerTimeout = 60 * time.Second
	maxResponseBytes       = 2 << 20 // 2MB
)

// GatewayLLMClient 使用外部分析端点的 LLM 客户端。
type GatewayLLMClient struct {
	model    string
	endpoint string // 例如：https://api.example.com/v1
	apiKey   string
	baseURL  string

	httpClient *http.Client

	// finishReasons 按**提示词**记最近一次回答的结束原因（finish_reason）。
	// ★ 为什么按提示词而不是「最近一次」：定向分析与后台 worker 可能并发共用同一个
	//   客户端 ⇒ 只存「最近一次」会把别的调用的结束原因记到这条分析头上。
	finishReasons sync.Map // prompt 哈希 → finish_reason
}

// analyzerMaxTokens 是分析回答的输出额度。
//
//	★ 2026-11-26 实测调大（4000 → 8000；211 上 glm-5.3-flash 经网关对 4000/8000/16000 均接受）：
//	  分析模型带推理，**推理过程的 token 计入输出额度**（回一个「ok」就用掉 51 个推理
//	  token）⇒ 4000 里先扣推理、剩下的才轮到正式 JSON；本轮又让模型多输出结构化修正
//	  方案 ⇒ 211 首批 6 条新分析里 2 条在额度处被截断（结构化方案那一段丢失）。
const analyzerMaxTokens = 8000

// NewGatewayLLMClient 创建网关 LLM 客户端。
//
// ★ baseURL 必须是**已经过 validateAnalysisEndpoint 校验**的外部分析端点
// （接线处负责）；本构造函数只做存在性检查。
func NewGatewayLLMClient(model, endpoint, apiKey, baseURL string) *GatewayLLMClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "/chat/completions"
	}
	return &GatewayLLMClient{
		model:    model,
		endpoint: endpoint,
		apiKey:   apiKey,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout: defaultAnalyzerTimeout,
			// ★ 2026-11-17 第57增量：**禁跟随重定向**。
			//
			//	validateAnalysisEndpoint 只校验**初始 URL**（禁私网/回环、强制
			//	https）；默认 http.Client 会把带 Authorization 头的请求跟着
			//	30x 转到**任意主机**（含私网）⇒ 端点校验被绕过 + API key 泄给
			//	重定向目标。fail-closed：不跟随，3xx 落到非 2xx 错误分支。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// SetTimeout 设置超时时间。
func (c *GatewayLLMClient) SetTimeout(timeout time.Duration) {
	if timeout > 0 && c.httpClient != nil {
		c.httpClient.Timeout = timeout
	}
}

// Analyze 调用 LLM 进行分析。
//
// ★ 契约：未配置端点/未配置 key 时**必须返回错误**，
//
//	绝不能返回 ("", 0, nil)（空成功是本仓最危险的一种桩）。
func (c *GatewayLLMClient) Analyze(ctx context.Context, prompt string) (string, int, error) {
	if c == nil {
		return "", 0, fmt.Errorf("gateway llm client: nil client")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return "", 0, fmt.Errorf("gateway llm client: analysis endpoint not configured")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return "", 0, fmt.Errorf("gateway llm client: analysis api key not configured")
	}
	if strings.TrimSpace(c.model) == "" {
		return "", 0, fmt.Errorf("gateway llm client: analysis model not configured")
	}

	reqBody := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.3, // 降低随机性，提高一致性
		"max_tokens":  analyzerMaxTokens,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + c.endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// ★ 内部标记：便于上游/网关日志区分「这是 PI 错误分析请求」，
	//   也便于将来给 PI 检测面加豁免通道时精确定位这一类请求。
	req.Header.Set("User-Agent", "llm-gateway-pi-analyzer/1.0")
	req.Header.Set("X-Llmgw-Pi-Analyzer", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("analysis request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("analysis endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", 0, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return "", 0, fmt.Errorf("no choices in response")
	}

	content := apiResp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		// 空内容不是合法分析结果 ⇒ 必须报错，否则下游会把它当
		// 「解析失败」再重试 3 次，浪费 3 倍调用。
		return "", 0, fmt.Errorf("analysis endpoint returned empty content")
	}

	c.finishReasons.Store(promptFinishKey(prompt), strings.ToLower(strings.TrimSpace(apiResp.Choices[0].FinishReason)))

	return content, apiResp.Usage.TotalTokens, nil
}

// FinishReasonForCall 返回某次调用（按提示词）得到的结束原因；没有记录 ⇒ 空串。
// 它是分析端可选扩展 finishReasonForCaller 的实现。
func (c *GatewayLLMClient) FinishReasonForCall(prompt string) string {
	if c == nil {
		return ""
	}
	if v, ok := c.finishReasons.Load(promptFinishKey(prompt)); ok {
		reason, _ := v.(string)
		return reason
	}
	return ""
}

// promptFinishKey 是提示词的哈希（提示词很长，不原样留作键）。
func promptFinishKey(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}
