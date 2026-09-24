// Package mock 提供进程内 mock 供应商（mock-fast / mock-slow）的 HTTP
// handler（2026-09-24，docs/design/2026-09-23-mock-probe-channel）。
//
// 设计要点：
//   - 不注册到 provider catalog / providers 表（Provider Store 无 Invoke
//     契约，注册即造伪数据）；handler 直接挂主 mux 的 /mock/v1/* 路径；
//   - 行为差异只有延迟：fast 立即返回；slow 模拟 800ms±200ms 抖动
//     （设计 §3.4），流式按 chunk 写出；
//   - 请求体提取 messages/contents，粗估 token（len/4）；图片附件
//     （OpenAI image_url / Anthropic source base64）回显接收字节数；
//   - 响应 ID 携带 "mock-" 前缀，探测客户端据此回填 request_id。
//
// 本包只做协议模拟，不做鉴权（鉴权由 internal/auth.MockEndpoint 守卫
// 在 mux 装配点完成）。
package mock

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// mockMaxBodyBytes 上限 4 MiB：探测载荷极小，4 MiB 足够容纳带 base64
// 图片附件的回显测试，同时拒绝异常大包（对齐 v2 端点家族的硬上限思路）。
const mockMaxBodyBytes = 4 << 20

// Supplier path segment mapping（/mock/v1/chat/completions/{fast|slow}）。
func Segment(supplier string) (string, bool) {
	switch supplier {
	case CodeFast:
		return "fast", true
	case CodeSlow:
		return "slow", true
	}
	return "", false
}

// IsSupplier 判断 code 是否为本包供应商（供装配层与测试交叉校验）。
func IsSupplier(code string) bool {
	_, ok := Segment(code)
	return ok
}

// Suppliers 返回全部供应商 code（顺序稳定：fast 在前）。
func Suppliers() []string { return []string{CodeFast, CodeSlow} }

// delayFunc 返回本次请求的模拟上游延迟（fast 恒 0）。
type delayFunc func() time.Duration

// ── 请求解析 ─────────────────────────────────────────────────────────────

type probeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string | []contentPart
}

type probeRequest struct {
	Model    string         `json:"model"`
	Stream   bool           `json:"stream"`
	Messages []probeMessage `json:"messages"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// OpenAI: {"type":"image_url","image_url":{"url":"data:image/png;base64,..."|https://..."}}
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
	// Anthropic: {"type":"image","source":{"type":"base64","data":"...","media_type":"..."}}
	Source *struct {
		Type string `json:"type"`
		Data string `json:"data"`
	} `json:"source"`
}

// parsedRequest 是跨协议归一后的探测请求摘要。
type parsedRequest struct {
	Model       string
	Stream      bool
	TextRunes   int
	ImageCount  int
	ImageBytes  int // base64 图片解码后累计字节数（远程 URL 图片计 0）
	RemoteImage int // 非 data-URI 的 image_url 数量
}

// parseProbeRequest 严格解码（单 JSON 值 + 体积上限）并归一提取摘要。
func parseProbeRequest(w http.ResponseWriter, r *http.Request) (parsedRequest, bool) {
	var parsed parsedRequest
	if r.Body == nil {
		return parsed, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, mockMaxBodyBytes)
	var req probeRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writePlainError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", mockMaxBodyBytes))
			return parsed, false
		}
		writePlainError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return parsed, false
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil && len(trailing) > 0 {
		writePlainError(w, http.StatusBadRequest, "body must contain a single JSON value")
		return parsed, false
	}
	parsed.Model = req.Model
	parsed.Stream = req.Stream
	if parsed.Model == "" {
		parsed.Model = "mock-model"
	}
	for _, m := range req.Messages {
		parseContent(m.Content, &parsed)
	}
	return parsed, true
}

// parseContent 归一 string 与 []contentPart 两种 content 形态（OpenAI 与
// Anthropic 同构，字段名不同：image_url vs source）。
func parseContent(raw json.RawMessage, out *parsedRequest) {
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			out.TextRunes += len([]rune(s))
		}
		return
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return // 非 text/array 形态（如工具调用复杂结构）容忍跳过
	}
	for _, p := range parts {
		switch {
		case p.Type == "text" || p.Text != "":
			out.TextRunes += len([]rune(p.Text))
		case p.ImageURL != nil && p.ImageURL.URL != "":
			out.ImageCount++
			if data := dataURIPayload(p.ImageURL.URL); data != "" {
				out.ImageBytes += base64DecodedLen(data)
			} else {
				out.RemoteImage++
			}
		case p.Source != nil && p.Source.Data != "":
			out.ImageCount++
			out.ImageBytes += base64DecodedLen(p.Source.Data)
		}
	}
}

// dataURIPayload 提取 data:image/...;base64,<payload> 的 payload 部分；
// 非 data URI（http/https）返回空串。
func dataURIPayload(url string) string {
	if !strings.HasPrefix(url, "data:") {
		return ""
	}
	if i := strings.Index(url, ","); i >= 0 {
		return url[i+1:]
	}
	return ""
}

// base64DecodedLen 按 base64 长度估算解码后字节数（len*3/4，忽略 padding
// 误差——回显用途足够）。
func base64DecodedLen(s string) int { return len(s) * 3 / 4 }

// estimateTokens 粗略 token 计数（len/4，至少 1）——设计 §3.4。
func estimateTokens(runes int) int {
	if runes < 4 {
		return 1
	}
	return runes / 4
}

// buildReply 构造 mock 回复文本：供应商标记 + 粗估 prompt tokens +
// 图片接收回显（设计 §3.4："已接收图片 <size> 字节"）。
func buildReply(supplier string, p parsedRequest) string {
	promptTokens := estimateTokens(p.TextRunes)
	reply := fmt.Sprintf("[%s] mock reply: approx %d prompt tokens", supplier, promptTokens)
	switch {
	case p.ImageBytes > 0:
		reply += fmt.Sprintf("; received %d image(s), ~%d bytes", p.ImageCount, p.ImageBytes)
	case p.ImageCount > 0:
		reply += fmt.Sprintf("; received %d image(s)", p.ImageCount)
	}
	return reply
}

// ── 响应构造 ─────────────────────────────────────────────────────────────

// nextID 生成携带 "mock" 标记的响应 ID（非加密唯一，仅用于 request_id
// 回填与关联）。前缀按协议区分。
func nextID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand 失败时退化为全 0，仍有 nano 兜底
	n := binary.BigEndian.Uint64(b[:])
	return fmt.Sprintf("%s-mock-%d-%d", prefix, time.Now().UnixNano(), n%100000)
}

// writePlainError 以 JSON envelope 输出错误（mock 端点自身的 4xx）。
func writePlainError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// sleepOrAbort 模拟上游延迟；请求上下文取消时立即中止（防客户端断连后
// goroutine 悬挂）。
func sleepOrAbort(w http.ResponseWriter, r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-r.Context().Done():
		return false
	case <-time.After(d):
		return true
	}
}

// randRange 返回 [min,max] 均匀整数（slow 延迟抖动用）。
func randRange(min, max int) int {
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}

// ── OpenAI Chat Completions 协议引擎 ────────────────────────────────────

// ChatCompletions 返回 supplier 的 OpenAI Chat Completions mock handler
// （流式/非流式由请求体 stream 字段决定）。
func ChatCompletions(supplier string, delay delayFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := parseProbeRequest(w, r)
		if !ok {
			return
		}
		if !sleepOrAbort(w, r, delay()) {
			slog.Debug("mock probe request aborted before response", "supplier", supplier)
			return
		}
		if p.Stream {
			writeChatCompletionStream(w, supplier, p)
			return
		}
		writeChatCompletion(w, supplier, p)
	}
}

func writeChatCompletion(w http.ResponseWriter, supplier string, p parsedRequest) {
	reply := buildReply(supplier, p)
	promptTokens := estimateTokens(p.TextRunes)
	completionTokens := estimateTokens(len([]rune(reply)))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      nextID("chatcmpl"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   p.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	})
}

// writeChatCompletionStream 按 OpenAI SSE 契约写出 chunk 流并以 [DONE]
// 收尾。flusher 缺失（理论上 httptest/真服务都有）时退化为一次性写出。
func writeChatCompletionStream(w http.ResponseWriter, supplier string, p parsedRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	reply := buildReply(supplier, p)
	id := nextID("chatcmpl")
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) {
		payload := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": p.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	for _, part := range splitReply(reply) {
		chunk(map[string]any{"content": part}, nil)
	}
	chunk(map[string]any{}, "stop")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// splitReply 把回复切成 2~3 段，模拟真实流式的多 chunk 形态。
func splitReply(reply string) []string {
	runes := []rune(reply)
	switch n := len(runes); {
	case n == 0:
		return []string{""}
	case n <= 12:
		return []string{reply}
	case n <= 40:
		mid := n / 2
		return []string{string(runes[:mid]), string(runes[mid:])}
	default:
		third := n / 3
		return []string{string(runes[:third]), string(runes[third : 2*third]), string(runes[2*third:])}
	}
}

// ── Anthropic Messages 协议引擎 ─────────────────────────────────────────

// AnthropicMessages 返回 supplier 的 Anthropic Messages mock handler。
func AnthropicMessages(supplier string, delay delayFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := parseProbeRequest(w, r)
		if !ok {
			return
		}
		if !sleepOrAbort(w, r, delay()) {
			slog.Debug("mock probe request aborted before response", "supplier", supplier)
			return
		}
		if p.Stream {
			writeAnthropicStream(w, supplier, p)
			return
		}
		writeAnthropicMessage(w, supplier, p)
	}
}

func writeAnthropicMessage(w http.ResponseWriter, supplier string, p parsedRequest) {
	reply := buildReply(supplier, p)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":   nextID("msg"),
		"type": "message", "role": "assistant",
		"model":       p.Model,
		"content":     []any{map[string]any{"type": "text", "text": reply}},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  estimateTokens(p.TextRunes),
			"output_tokens": estimateTokens(len([]rune(reply))),
		},
	})
}

// writeAnthropicStream 按 Anthropic SSE 契约写出 message_start →
// content_block_* → message_delta → message_stop 事件序列。
func writeAnthropicStream(w http.ResponseWriter, supplier string, p parsedRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	reply := buildReply(supplier, p)
	event := func(name string, payload any) {
		b, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": nextID("msg"), "type": "message", "role": "assistant", "model": p.Model,
			"content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": estimateTokens(p.TextRunes), "output_tokens": 0},
		},
	})
	event("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	for _, part := range splitReply(reply) {
		event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": part},
		})
	}
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": estimateTokens(len([]rune(reply)))},
	})
	event("message_stop", map[string]any{"type": "message_stop"})
}
