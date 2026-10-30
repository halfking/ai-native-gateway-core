// probe.go — 身份探测：直连上游发一次极小的请求，取回三类指纹信号。
//
// probe.go — identity probing: one tiny direct request to the upstream,
// harvesting three fingerprint signals from a single round-trip.
//
// # 为什么只发一次请求
//
// 身份自洽、请求签名（prompt token 计数）、响应头指纹这三类信号，
// 在同一个响应里都能拿到 ⇒ 一发请求取三样。
// 能力维度**不在这里**测（它复用已有的 MMLU 智商实测，见 reference.go），
// 因为能力测试要 50 道题、成本高一个数量级，不该每个扫描周期都重跑。
//
// # 标定提示词（CalibrationPrompt）
//
// 这条提示词是**请求签名**的来源。它的三个性质都是必须的：
//
//  1. 逐字节固定 —— 每次探测发同一条，prompt token 计数才可比。
//  2. 不触发拒绝 —— 拒绝回复的模型可能不返回 usage，签名就断了。
//  3. 极短 —— 输出 1 个词，max_tokens 才有意义。
//
// ★ 注意：prompt token 计数**不是**模型的常量属性，它还包含上游的
// chat template 开销与中转站注入的系统提示词。但这不影响本包的用法：
// 我们要的不是「这个模型应该是多少 token」，而是「**同一个节点
// 前后两次是不是同一个后端**」⇒ 自参照漂移检测。
// 跨族换模型会换分词器 ⇒ 计数变；同族降级（Sonnet→Haiku）分词器不变
// ⇒ 计数不变 ⇒ 该场景由能力维度（reference.go）负责。
package modelcredibility

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// CalibrationPrompt 是身份探测用的标定提示词。
//
// ★ 逐字节固定：改动这个字符串会让历史签名全部失效（所有节点会被判成
// 「签名漂移」）。要改必须同时作废已有签名基线。
const CalibrationPrompt = "Reply with exactly one word: ready"

// CalibrationPromptTokens 是标定提示词在**参考分词器**下的 token 计数估计。
//
// ★ 它**不是**判据，只作为展示用的参考值（让运维看到「这次是 23，
// 上次也是 23」）。真实判据是与**本节点上一次**的实测值比对。
const CalibrationPromptTokens = 9

// probeMaxTokens 是探测请求的输出预算。
//
// ★ 复用 providercap.ProbePingMaxTokens（1024）而不是另定一个数：
// 该常量是仓内用实测换来的教训（2026-10-27 R151/R152 轮）——
// 32 token 对推理模型必然不够，探针拿不到 assistant 正文 ⇒ 判不出成功。
// 「同一个数字在探针与用户请求两处含义不同」——探针要的是「有没有正常
// 应答」，所以取探针那一档。
const probeMaxTokens = providercap.ProbePingMaxTokens

// OriginActor 是本探针在请求头里声明的 origin_actor。
//
// ★ 必须与 internal/probekind.Writers 里的登记项一致，否则
// probekind 的「每个真实写入者都要在册」门会红。
const OriginActor = "model-credibility-worker"

// OriginStage 是本探针声明的 origin_stage。
const OriginStage = "self_check"

// NodeTarget 是一次探测的目标节点。
type NodeTarget struct {
	CredentialID  int64
	ProviderID    int64
	ProviderName  string
	BaseURL       string
	APIKey        string
	Protocol      string
	CatalogCode   string
	RawModelName  string // 节点身份键（provider_models.raw_model_name）
	OutboundModel string // 实际发往上游的模型名
	CanonicalID   int64
	CanonicalName string
}

// outbound 返回应当写进请求体的模型名。
func (t NodeTarget) outbound() string {
	if strings.TrimSpace(t.OutboundModel) != "" {
		return t.OutboundModel
	}
	return t.RawModelName
}

// Prober 发起身份探测。抽成接口是为了让判定逻辑可以在不碰网络的前提下被测。
type Prober interface {
	// Probe 直连上游发一次标定请求。
	Probe(ctx context.Context, target NodeTarget) (Observed, error)
}

// DirectProber 直连上游的探测实现。
type DirectProber struct {
	Client *http.Client
}

// NewDirectProber 创建直连探测器。timeout<=0 时用 60s。
func NewDirectProber(timeout time.Duration) *DirectProber {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &DirectProber{Client: &http.Client{Timeout: timeout}}
}

// Probe 直连上游发一次标定请求并解析指纹信号。
//
// 返回的 Observed 在任何情况下都尽量携带已拿到的信息（HTTPStatus 等），
// 错误只是附加说明 —— 调用方据此区分「没测到」与「测到且不对」。
func (p *DirectProber) Probe(ctx context.Context, target NodeTarget) (Observed, error) {
	obs := Observed{}
	desc := providercap.Resolve(target.Protocol, target.CatalogCode)
	endpoint := desc.ChatProbeEndpoint
	if endpoint == upstreamurl.EpGeminiGenerate || endpoint == upstreamurl.EpOllamaChat {
		// 这两个协议的响应形态与 OpenAI 不兼容，本包只解析 OpenAI/Anthropic
		// 两种形态 ⇒ 明确报不支持，而不是猜。
		return obs, fmt.Errorf("protocol %q not supported by identity probe", target.Protocol)
	}
	url := providercap.ProbeEndpointURL(target.BaseURL, desc)
	body, err := buildProbeBody(endpoint, target.outbound())
	if err != nil {
		return obs, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return obs, fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	providercap.ApplyAuthHeaders(req, desc, target.APIKey)
	// 探针来源声明：让这条请求在统计面被归到探测流量而不是业务流量。
	req.Header.Set("X-LLM-Origin-Stage", OriginStage)
	req.Header.Set("X-LLM-Origin-Actor", OriginActor)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	start := time.Now()
	resp, err := client.Do(req)
	obs.LatencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		return obs, fmt.Errorf("probe request failed: %w", err)
	}
	defer resp.Body.Close()
	obs.HTTPStatus = resp.StatusCode
	obs.SystemFingerprint = strings.TrimSpace(resp.Header.Get("X-System-Fingerprint"))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return obs, fmt.Errorf("read probe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return obs, fmt.Errorf("probe upstream status %d: %s", resp.StatusCode, truncateForError(raw))
	}
	parseProbeResponse(endpoint, raw, &obs)
	return obs, nil
}

// buildProbeBody 按协议构造探测请求体。
func buildProbeBody(endpoint upstreamurl.Endpoint, model string) ([]byte, error) {
	switch endpoint {
	case upstreamurl.EpMessages:
		// Anthropic Messages API：max_tokens 必填。
		return json.Marshal(map[string]any{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": CalibrationPrompt}},
			"max_tokens": probeMaxTokens,
			"stream":     false,
		})
	case upstreamurl.EpResponses:
		// OpenAI Responses API：入参是 input，不是 messages。
		return json.Marshal(map[string]any{
			"model":             model,
			"input":             CalibrationPrompt,
			"max_output_tokens": probeMaxTokens,
			"stream":            false,
		})
	default:
		// OpenAI Chat Completions（含绝大多数中转）。
		//
		// ★ 不带 temperature：推理模型（o 系列 / gpt-5）对 temperature≠1 会
		// 直接 400（仓内 reqprobe 学的就是这件事）。不带更安全。
		return json.Marshal(map[string]any{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": CalibrationPrompt}},
			"max_tokens": probeMaxTokens,
			"stream":     false,
		})
	}
}

// parseProbeResponse 从响应体解析指纹信号。
//
// ★ 三种协议形态都要认（OpenAI chat / Anthropic messages / OpenAI responses），
// 因为供应商的 protocol 字段是事实源，而中转常常三种都兼容。
func parseProbeResponse(endpoint upstreamurl.Endpoint, raw []byte, obs *Observed) {
	// OpenAI chat / Anthropic messages 共用 "model" + usage 命名空间。
	var common struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
			InputTokens      *int `json:"input_tokens"`
			OutputTokens     *int `json:"output_tokens"`
		} `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		// Anthropic messages：content 是块数组。
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// OpenAI responses：output 是数组，output_text 是聚合文本。
		//
		// ★★ 2026-10-30 12h 审计轮订正：★★ 真实 Responses 响应的正文在
		//   **output[].content[].text** ⇒ ★★ 而 output[].text 这个字段在真实
		//   响应里**不存在** ⇒ ★★ 原实现两处都取不到 ⇒ ★★ 每个 openai-responses
		//   节点恒为 obs.Content == "" ⇒ ★★ 落库恒为 status='partial' + 假异常
		//   identity_probe_empty_response ⇒ ★★ 「该供应商可信度」这一维整列恒残。
		//
		// ★ 权威形状（本仓自有事实源）：★ cmd/gateway-v2/main.go 的
		//   「OpenAI Responses API 格式」注释 + cmd/gateway-v2/e2e_test.go 的
		//   `resp.Output[0].Content[0].Type == "output_text"` ★
		Output []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// Content 是真实 Responses 形态的正文载体。
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		OutputText string `json:"output_text"`
	}
	if err := json.Unmarshal(raw, &common); err != nil {
		return
	}
	obs.ReportedModel = strings.TrimSpace(common.Model)
	if common.Usage.PromptTokens != nil {
		obs.PromptTokens = *common.Usage.PromptTokens
		obs.HasUsage = true
	} else if common.Usage.InputTokens != nil {
		obs.PromptTokens = *common.Usage.InputTokens
		obs.HasUsage = true
	}
	// 正文抽取：任一形态拿到非空正文即可。
	for _, c := range common.Choices {
		if c.Message.Content != "" {
			obs.Content = c.Message.Content
			break
		}
		if c.Text != "" {
			obs.Content = c.Text
			break
		}
	}
	if obs.Content == "" {
		for _, c := range common.Content {
			if c.Text != "" {
				obs.Content = c.Text
				break
			}
		}
	}
	if obs.Content == "" {
		// ★★ Responses 真实形态：★ 正文在 output[].content[].text ★★
		for _, o := range common.Output {
			for _, ct := range o.Content {
				if ct.Text != "" {
					obs.Content = ct.Text
					break
				}
			}
			if obs.Content != "" {
				break
			}
			// 兼容非标准形态（output[].text）：真实协议没有它，但保留兜底。
			if o.Text != "" {
				obs.Content = o.Text
				break
			}
		}
	}
	if obs.Content == "" && common.OutputText != "" {
		obs.Content = common.OutputText
	}
}

// truncateForError 截断错误体，避免把上游的长文本塞进日志。
func truncateForError(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 256 {
		return s[:256] + "…"
	}
	return s
}
