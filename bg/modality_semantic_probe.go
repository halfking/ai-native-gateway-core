// bg/modality_semantic_probe.go — 语义级「真能读」探测（迁移 825）
//
// 与 bg/probe_modality.go 的关系：后者是**结构级**探针（「上游收不收
// 这个模态的内容块」），且把 2xx 直接当支持。本文件是它的上级：在它
// 判定「载得下」之后，才发一张带随机色块编码的挑战图，并要求模型把
// 编码读出来。
//
// 为什么要分两级而不是合成一个 bool：
//
//	carry=accepted 且 read=negative
//
// 这一类恰好就是「接口收下了但模型看不见」——也就是今天把 200 当证据
// 会系统性误标为多模态的形态。合成一个 bool 会把它记成 true。
//
// # 三态而非两态
//
// read_level 有 unknown 这一态，纪律与 bg/capability_backfill.go 的第 2 条
// 不可让步约束同源：网络错、鉴权失败、5xx、回答被截断、模型拒答——
// 这些情形**一律不下结论**。把「没探到」写成 negative 等于用一个默认值
// 关掉一个可能完全正常的绑定，而 false negative 的代价是 vision 模型被
// 降级成 text、被候选过滤排除、图片请求 503 no_candidate。
//
// # 语音没有语义挑战（诚实边界，不是遗漏）
//
// 判据需要一个可程序化生成的正确答案。图像用随机色块造得出来；语音
// 造不出来——仓里没有 TTS 依赖，也不该为探针引入一个。所以 audio 只落
// carry_level，read_level 恒为 unknown，直到有一份带标注的语音语料。
package bg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// 分级判词的取值。与迁移 825 的 CHECK 约束一一对应。
const (
	ModalityLevelUnknown   = "unknown"
	ModalityLevelAccepted  = "accepted"
	ModalityLevelRejected  = "rejected"
	ModalityLevelConfirmed = "confirmed"
	ModalityLevelNegative  = "negative"
)

// semanticProbeMaxTokens 是挑战请求的 max_tokens。
//
// 既有结构探针用 1，那对语义判别是错的：答案本身就有 10 个 token
// 上下（"red, green, blue, yellow"），截断后拿到的半截回答既不是
// 「答对」也不是「答错」，会被判成 inconclusive 于是每次核实都白跑。
// 推理模型还会把预算花在 reasoning 上，所以给到 64。
const semanticProbeMaxTokens = 64

// semanticProbeTimeout 单次挑战请求的墙钟上限。
const semanticProbeTimeout = 20 * time.Second

// semanticProbeBodyLimit 是响应体读取上限。挑战的答案只有几十字节，
// 读太多只是给上游一个把大 body 塞回来的机会。
const semanticProbeBodyLimit = 8192

// SemanticProbeResult 是一次语义判别的完整结论。
type SemanticProbeResult struct {
	// Carry 是结构级结论：accepted / rejected / unknown。
	Carry string
	// Read 是语义级结论：confirmed / negative / unknown。
	Read string

	Expected  []string
	Mentioned []string
	Score     int
	Answer    string

	// resolvedModel 是这一发实际用的模型名（outbound 名）。出网用
	// outbound 名、证据行按 raw 名归位，所以要把「实际发出去的名字」记
	// 在证据里——否则一条映射型中转的失败会看起来像模型名写错了。
	resolvedModel string

	HTTPStatus int
	ErrCode    string
	ErrMsg     string
	LatencyMs  int
}

// ProbeVisionSemantics 对一个 (凭证, 模型) 跑一次语义判别。
//
// 顺序是刻意的：先跑结构探针，载不下的绑定**不再发挑战**——那一次
// 请求是纯浪费，而且载都载不下的上游不可能读出图里的编码。
// modalityVerifyProbeModality 是**唯一实现了探针的那个模态**，也是
// modalityVerifyAdmit 唯一放行的那个。
//
// 为什么要有这个常量：ProbeVisionSemantics 的签名里**没有 modality 参数**，
// 探针模态在函数体里写死成 "vision"（下面 ProbeModality 的第三个实参）。
// 于是"合法模态"（vision/audio/video）与"可探模态"（只有 vision）是两件事，
// 而准入闸初版把它们当成一件——那会给 ASR/TTS 记上假否定证据
// （见 modality_verify_admit 的注释与 2026-10-05 的实测清单）。
//
// ★ 这个常量必须与 ProbeVisionSemantics 里那个写死的 "vision" 一起改。
//
//	加一种探针实现时：在这里换/扩展，同时放开 modalityVerifyAdmit 的 switch，
//	并给它配判据（否则那个分支没人跑过）。
const modalityVerifyProbeModality = "vision"

func ProbeVisionSemantics(ctx context.Context, baseURL, apiKey, model, protocol string) SemanticProbeResult {
	start := time.Now()
	desc := probeDescriptorFor(protocol)
	endpoint := upstreamurl.Build(baseURL, desc.ChatProbeEndpoint)
	isAnthropic := desc.Protocol == "anthropic-messages"

	res := SemanticProbeResult{
		Carry:      ModalityLevelUnknown,
		Read:       ModalityLevelUnknown,
		HTTPStatus: 0,
	}

	// ---- 第一级：可承载 -------------------------------------------------
	carry := ProbeModality(ctx, endpoint, apiKey, model, "vision", isAnthropic)
	res.HTTPStatus = carry.HTTPStatus
	res.LatencyMs = int(time.Since(start).Milliseconds())
	switch {
	case carry.ErrCode == "" && carry.Supported:
		res.Carry = ModalityLevelAccepted
	case carry.ErrCode == "modality_unsupported":
		res.Carry = ModalityLevelRejected
		res.ErrCode = carry.ErrCode
		res.ErrMsg = carry.ErrMsg
		return res
	default:
		// 鉴权/网络/5xx/未知 4xx：一律 unknown，不写结论。
		res.ErrCode = carry.ErrCode
		res.ErrMsg = carry.ErrMsg
		return res
	}

	// ---- 第二级：真能读 -------------------------------------------------
	challenge, err := NewVisionChallenge(newVisionChallengeRNG())
	if err != nil {
		res.ErrCode = "challenge_build"
		res.ErrMsg = err.Error()
		return res
	}
	res.Expected = challenge.Colors

	payload := visionChallengePayload(model, challenge, isAnthropic)
	answer, status, perr := postVisionChallenge(ctx, endpoint, apiKey, payload, isAnthropic)
	res.HTTPStatus = status
	res.LatencyMs = int(time.Since(start).Milliseconds())
	if perr != nil {
		res.ErrCode = perr.code
		res.ErrMsg = perr.msg
		return res
	}
	res.Answer = answer

	matched, score, mentioned := GradeVisionAnswer(answer, challenge)
	res.Score = score
	res.Mentioned = mentioned

	switch {
	case matched:
		res.Read = ModalityLevelConfirmed
	case len(mentioned) >= visionChallengeBlocks:
		// 说了 4 个以上颜色却没对上 ⇒ 模型确实答了、且答错了。
		res.Read = ModalityLevelNegative
	default:
		// 颜色词不足 4 个：拒答、截断、或推理模型把预算花光。
		// inconclusive，不是 negative。
		res.Read = ModalityLevelUnknown
		res.ErrCode = "inconclusive"
	}
	return res
}

// visionChallengePayload 构造挑战请求体。isAnthropic 走 base64 image
// 源，其余走 image_url 的 data URL——与 probe_modality.go 里
// visionTestPayload / anthropicVisionTestPayload 的分歧同源。
func visionChallengePayload(model string, ch VisionChallenge, isAnthropic bool) []byte {
	question := visionChallengePrompt
	if isAnthropic {
		imgBytes, _ := base64.StdEncoding.DecodeString(
			strings.TrimPrefix(ch.DataURL, "data:image/png;base64,"))
		payload := map[string]any{
			"model":       model,
			"max_tokens":  semanticProbeMaxTokens,
			"temperature": 0,
			"messages": []map[string]any{{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": question},
					{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": "image/png",
							"data":       base64.StdEncoding.EncodeToString(imgBytes),
						},
					},
				},
			}},
		}
		body, _ := json.Marshal(payload)
		return body
	}

	payload := map[string]any{
		"model":       model,
		"max_tokens":  semanticProbeMaxTokens,
		"temperature": 0,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": question},
				{
					"type":      "image_url",
					"image_url": map[string]any{"url": ch.DataURL},
				},
			},
		}},
	}
	body, _ := json.Marshal(payload)
	return body
}

// challengeErr 是带分类的探针错误，分类与结构探针的 ErrCode 同一套词汇。
type challengeErr struct {
	code string
	msg  string
}

func (e *challengeErr) Error() string { return e.code + ": " + e.msg }

// postVisionChallenge 发一次挑战请求并取出答案文本。
func postVisionChallenge(ctx context.Context, endpoint, apiKey string, payload []byte, isAnthropic bool) (string, int, *challengeErr) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", 0, &challengeErr{"request_build", err.Error()}
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

	client := &http.Client{Timeout: semanticProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, &challengeErr{"network", err.Error()}
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close
	body, _ := io.ReadAll(io.LimitReader(resp.Body, semanticProbeBodyLimit))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 结构探针刚判过 accepted，这里再被拒就是新信息（载荷形状）。
		// 但不写 read 结论——载得下却答不出，与答错不是一回事。
		return "", resp.StatusCode, &challengeErr{
			"challenge_http_" + strconv.Itoa(resp.StatusCode),
			truncateProbeBody(string(body), 200),
		}
	}
	return extractAnswerText(body), resp.StatusCode, nil
}

// extractAnswerText 从三种上游响应形状里取答案文本：
//
//   - chat completions：choices[0].message.content（string 或 content 数组）
//   - responses：output[].content[].text
//   - anthropic messages：content[].text
//
// 刻意不读 reasoning_content / reasoning 字段：推理模型的思维链里
// 可能出现「这张图应该是红绿蓝黄」这类**它自己推断**的内容，拿它当
// 答案等于让模型猜。语义判据只认最终答案。
func extractAnswerText(body []byte) string {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}

	// chat completions
	if len(envelope.Choices) > 0 && len(envelope.Choices[0].Message.Content) > 0 {
		if txt := rawContentToText(envelope.Choices[0].Message.Content); txt != "" {
			return txt
		}
	}
	// responses
	for _, out := range envelope.Output {
		for _, c := range out.Content {
			if c.Text != "" {
				return c.Text
			}
		}
	}
	// anthropic messages
	for _, c := range envelope.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// rawContentToText 处理 content 字段的两种形态：纯字符串，或
// [{type:text,text:...}] 数组。
func rawContentToText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			b.WriteString(p.Text)
			b.WriteString(" ")
		}
	}
	return strings.TrimSpace(b.String())
}
