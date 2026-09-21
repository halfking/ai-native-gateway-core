package modelquality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// buildQuestionPrompt 把一道选择题构造成发给模型的 prompt。
// 抽成包级函数，供 Gateway/Direct 调用器复用（2026-08-10：接口签名改为传 Question 后，
// 调用器需要自己生成 prompt 文本）。
func buildQuestionPrompt(q Question) string {
	var sb strings.Builder
	sb.WriteString("Answer the following multiple choice question by selecting the correct option (A, B, C, or D).\n\n")
	sb.WriteString("Question: ")
	sb.WriteString(q.Question)
	sb.WriteString("\n\nOptions:\n")
	for i, opt := range q.Options {
		letter := string(rune('A' + i))
		sb.WriteString(fmt.Sprintf("%s. %s\n", letter, opt))
	}
	sb.WriteString("\nPlease respond with ONLY the letter of the correct answer (A, B, C, or D). Do not include any explanation.")
	return sb.String()
}

// MockModelInvoker 模拟的模型调用器(用于测试)
//
// 2026-08-10 修复：旧实现从 prompt 里"推断正确答案"实际是随机哈希取字母，
// 导致模拟准确率恒为 ~25%（4 选 1 瞎蒙），与配置的 BaseAccuracy 无关。
// 现在直接拿 Question.Answer 作为基准答案，按 BaseAccuracy 概率返回正确答案，
// 这样 mock 准确率才会随 ProviderQuality.BaseAccuracy 变化，simulate-drop 才可信。
type MockModelInvoker struct {
	// 模拟不同供应商的质量特征
	providerQuality map[string]*ProviderQuality

	// sleepFn 可注入的延迟函数。默认 time.Sleep，测试可覆盖以跳过真实等待。
	sleepFn func(d time.Duration)

	// rng 可注入的随机源（测试用固定种子可复现）。nil 时用全局 rand。
	rng *rand.Rand
}

// ProviderQuality 供应商质量特征
type ProviderQuality struct {
	BaseAccuracy  float64 // 基础准确率 0-1
	BaseLatency   int64   // 基础延迟(毫秒)
	ErrorRate     float64 // 错误率 0-1
	LatencyJitter int64   // 延迟抖动(毫秒)
}

// NewMockModelInvoker 创建模拟调用器
func NewMockModelInvoker() *MockModelInvoker {
	return &MockModelInvoker{
		sleepFn: time.Sleep,
		providerQuality: map[string]*ProviderQuality{
			// 模拟高质量供应商(如OpenAI GPT-4)
			"openai": {
				BaseAccuracy:  0.92,
				BaseLatency:   1200,
				ErrorRate:     0.01,
				LatencyJitter: 300,
			},
			// 模拟中等质量供应商(如某国产模型)
			"domestic_a": {
				BaseAccuracy:  0.78,
				BaseLatency:   800,
				ErrorRate:     0.05,
				LatencyJitter: 200,
			},
			// 模拟质量下降的供应商(渗水场景)
			"domestic_b": {
				BaseAccuracy:  0.65, // 准确率较低
				BaseLatency:   2500, // 延迟较高
				ErrorRate:     0.12, // 错误率高
				LatencyJitter: 800,
			},
			// 模拟稳定供应商
			"claude": {
				BaseAccuracy:  0.90,
				BaseLatency:   1000,
				ErrorRate:     0.02,
				LatencyJitter: 150,
			},
		},
	}
}

// randFloat 返回 [0,1) 随机数，优先用注入的 rng（便于测试复现）。
func (m *MockModelInvoker) randFloat() float64 {
	if m.rng != nil {
		return m.rng.Float64()
	}
	return rand.Float64()
}

func (m *MockModelInvoker) randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	if m.rng != nil {
		return m.rng.Intn(n)
	}
	return rand.Intn(n)
}

// InvokeModel 模拟调用模型回答一道选择题。
// 以 q.Answer 为正确答案，按供应商 BaseAccuracy 概率返回正确/错误答案。
func (m *MockModelInvoker) InvokeModel(ctx context.Context, provider string, modelName string, q Question) (response string, tokenUsage int, latency time.Duration, err error) {
	// 获取供应商质量特征
	quality, exists := m.providerQuality[provider]
	if !exists {
		// 默认中等质量
		quality = &ProviderQuality{
			BaseAccuracy:  0.75,
			BaseLatency:   1500,
			ErrorRate:     0.08,
			LatencyJitter: 400,
		}
	}

	// 模拟网络延迟
	latencyMs := quality.BaseLatency + int64(m.randIntn(int(quality.LatencyJitter*2))) - quality.LatencyJitter
	if latencyMs < 100 {
		latencyMs = 100
	}

	// 检查是否模拟错误
	if m.randFloat() < quality.ErrorRate {
		m.sleepFn(time.Duration(latencyMs) * time.Millisecond)
		return "", 0, time.Duration(latencyMs) * time.Millisecond, fmt.Errorf("模拟错误: 模型调用失败 (provider=%s)", provider)
	}

	correct := strings.ToUpper(strings.TrimSpace(q.Answer))
	if correct == "" || correct[0] < 'A' || correct[0] > 'D' {
		// 题目本身无合法答案，无法判定，返回 A 兜底
		correct = "A"
	}
	// 选项字母集（按 q.Options 长度，至少 A-D）
	letters := []string{"A", "B", "C", "D"}
	maxLen := len(q.Options)
	if maxLen < 1 || maxLen > 4 {
		maxLen = 4
	}
	letters = letters[:maxLen]

	var answer string
	if m.randFloat() < quality.BaseAccuracy {
		// 返回正确答案
		answer = correct
	} else {
		// 返回一个错误选项
		wrong := make([]string, 0, len(letters)-1)
		for _, l := range letters {
			if l != correct {
				wrong = append(wrong, l)
			}
		}
		if len(wrong) > 0 {
			answer = wrong[m.randIntn(len(wrong))]
		} else {
			answer = "A"
		}
	}

	// 模拟处理时间
	m.sleepFn(time.Duration(latencyMs) * time.Millisecond)

	// 模拟token使用量
	tokenUsage = 100 + m.randIntn(50)

	return answer, tokenUsage, time.Duration(latencyMs) * time.Millisecond, nil
}

// SetProviderQuality 动态设置供应商质量(用于模拟质量变化)
func (m *MockModelInvoker) SetProviderQuality(provider string, quality *ProviderQuality) {
	m.providerQuality[provider] = quality
}

// SetRNG 注入可复现随机源（测试用）。
func (m *MockModelInvoker) SetRNG(rng *rand.Rand) {
	m.rng = rng
}

// SimulateQualityDrop 模拟质量下降(渗水场景)
func (m *MockModelInvoker) SimulateQualityDrop(provider string, accuracyDrop float64, latencyIncrease int64) {
	if quality, exists := m.providerQuality[provider]; exists {
		quality.BaseAccuracy -= accuracyDrop
		quality.BaseLatency += latencyIncrease
		quality.ErrorRate += accuracyDrop * 0.5

		if quality.BaseAccuracy < 0 {
			quality.BaseAccuracy = 0.1
		}
		if quality.ErrorRate > 0.3 {
			quality.ErrorRate = 0.3
		}

		slog.Info("mock model quality drop simulated",
			"provider", provider,
			"accuracy", quality.BaseAccuracy,
			"latency_ms", quality.BaseLatency)
	}
}

// GatewayModelInvoker 网关模型调用器(真实实现)
// 通过HTTP调用网关 /v1/chat/completions 进行测试（网关按负载均衡选凭据节点，
// 因此测出的是"该供应商某个随机节点"的智商，不带 CredentialID 维度）。
type GatewayModelInvoker struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewGatewayModelInvoker 创建网关模型调用器
// timeout: HTTP客户端超时，0则使用默认30秒
func NewGatewayModelInvoker(baseURL, apiKey string, timeout time.Duration) *GatewayModelInvoker {
	if baseURL == "" {
		baseURL = "http://localhost:8787" // 默认本地网关
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &GatewayModelInvoker{
		baseURL: strings.TrimSpace(baseURL),
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				MaxConnsPerHost:     64,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// InvokeModel 调用网关模型回答一道选择题
func (g *GatewayModelInvoker) InvokeModel(ctx context.Context, provider string, modelName string, q Question) (response string, tokenUsage int, latency time.Duration, err error) {
	startTime := time.Now()
	prompt := buildQuestionPrompt(q)

	// 构造OpenAI格式的请求
	payload := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  50,  // 质量测试只需要简短回答
		"temperature": 0.1, // 低温度保证稳定性
		"stream":      false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("marshal request: %w", err)
	}

	// 构造请求URL
	url := g.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("X-Gateway-Quality-Test", "true") // 标识为质量测试请求
	// 2026-08-10: 打 origin 头，让网关侧 request_logs 把 IQ 测试请求归到
	// origin_stage=self_check（而非默认 business），自检记录可与业务流量区分。
	// 需配合 owner_user='model-quality-worker' 的系统 key 才被网关信任。
	req.Header.Set("X-LLM-Origin-Stage", "self_check")
	req.Header.Set("X-LLM-Origin-Actor", "model-quality-worker")

	return doChatCompletion(g.httpClient, req, startTime)
}

// CredentialNode 单个凭据节点的直连信息（由调用方从 DB 取好填入，
// modelquality 包本身不依赖 DB / 解密 key，保持纯净）。
type CredentialNode struct {
	CredentialID int
	Provider     string // 供应商名（仅用于展示/分组）
	Label        string // 凭据标签（仅用于展示）
	BaseURL      string // 该节点 providers.base_url
	APIKey       string // 解密后的 api key
	RawModel     string // 该节点请求体里实际使用的 model 名（可能是 outbound_model_name）
	RawModelName string // provider_models.raw_model_name，作为节点历史记录身份键
}

// DirectNodeInvoker 直连凭据节点调用器（绕过网关）。
//
// 用于测量"单个凭据节点"的模型智商：直接 POST 到该节点的 BaseURL，
// 不经过网关负载均衡，因此能区分同一供应商下不同 key 的质量差异。
// 节点信息(CredentialNode)由调用方提前从 DB 取好并解密，本结构只负责 HTTP 调用。
type DirectNodeInvoker struct {
	httpClient *http.Client
}

// NewDirectNodeInvoker 创建直连节点调用器。timeout<=0 时默认 30s。
func NewDirectNodeInvoker(timeout time.Duration) *DirectNodeInvoker {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &DirectNodeInvoker{
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				MaxConnsPerHost:     64,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// InvokeModel 直连指定节点回答一道选择题。
// provider/modelName 参数在此实现里不参与请求构造（请求用的是 node.BaseURL/node.RawModel），
// 仅用于结果归属记录。
func (d *DirectNodeInvoker) InvokeModel(ctx context.Context, node CredentialNode, q Question) (response string, tokenUsage int, latency time.Duration, err error) {
	startTime := time.Now()
	if node.BaseURL == "" {
		return "", 0, 0, fmt.Errorf("direct invoker: node.BaseURL is empty (credential_id=%d)", node.CredentialID)
	}
	if node.APIKey == "" {
		return "", 0, 0, fmt.Errorf("direct invoker: node.APIKey is empty (credential_id=%d)", node.CredentialID)
	}
	model := node.RawModel
	if model == "" {
		return "", 0, 0, fmt.Errorf("direct invoker: node.RawModel is empty (credential_id=%d)", node.CredentialID)
	}

	prompt := buildQuestionPrompt(q)
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  50,
		"temperature": 0.1,
		"stream":      false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(node.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("X-Gateway-Quality-Test", "true")

	return doChatCompletion(d.httpClient, req, startTime)
}

// doChatCompletion 公共的 POST + 解析逻辑，供 Gateway / Direct 调用器复用。
func doChatCompletion(client *http.Client, req *http.Request, startTime time.Time) (response string, tokenUsage int, latency time.Duration, err error) {
	resp, err := client.Do(req)
	latency = time.Since(startTime)
	if err != nil {
		return "", 0, latency, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		const maxErrorBody = 4096
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return "", 0, latency, fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, latency, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", 0, latency, fmt.Errorf("no choices in response")
	}
	response = strings.TrimSpace(result.Choices[0].Message.Content)
	tokenUsage = result.Usage.TotalTokens
	return response, tokenUsage, latency, nil
}
