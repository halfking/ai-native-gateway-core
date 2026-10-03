// Package streaming — audio_mcp.go
//
// POST /v1/mcp —— Model Context Protocol (streamable HTTP 传输) 的最小
// 服务端实现，把网关的音频能力（转写 + 合成）暴露成 MCP tools，供
// app / agent 以标准 MCP 客户端接入。
//
// 为什么值得有：网关此前对 MCP 只有路线图（README「MCP tool gateway:
// Partial」），而 app 侧（openpocket 等）已有 MCP 客户端形态。音频是
// 第一个「非 chat、结构化输入输出」的网关能力，正好用它落地一个真实
// 的 MCP 面：JSON-RPC 2.0、单 POST 请求-响应（无服务端推送通知——
// streamable HTTP 规范允许服务器对无通知请求直接回 application/json）。
//
// 支持的方法：
//
//	initialize            → 协议握手（protocolVersion 2025-06-18）
//	notifications/*       → 202 Accepted（通知无响应体）
//	tools/list            → transcribe_audio / synthesize_speech 两个工具
//	tools/call            → 复用 AudioService（与 HTTP 端点同一执行路径）
//
// 鉴权与 /v1/* 数据面同款（Bearer sk-*，KeyVerifier 校验 + 限流 + 预算）。
package streaming

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// mcpProtocolVersion 是本实现声明的 MCP 协议版本（streamable HTTP 传输
// 已稳定的修订版）。客户端在 initialize.params.protocolVersion 里带的
// 版本若不同，按规范仍以服务端声明为准。
const mcpProtocolVersion = "2025-06-18"

// AudioMCPHandler serves the MCP streamable-HTTP endpoint.
type AudioMCPHandler struct {
	svc *AudioService
}

func NewAudioMCPHandler(svc *AudioService) *AudioMCPHandler {
	return &AudioMCPHandler{svc: svc}
}

// jsonRPCRequest / jsonRPCResponse / jsonRPCError 是 JSON-RPC 2.0 的
// 最小信封。Batch 请求（数组信封）不支持，直接 -32600。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

const (
	mcpErrParse     = -32700
	mcpErrInvalidRq = -32600
	mcpErrMethod    = -32601
	mcpErrParams    = -32602
	mcpErrInternal  = -32603
)

// writeJSONRPCError 按 JSON-RPC 2.0 规范回**协议级**错误：HTTP 200 +
// {"jsonrpc":"2.0","id":<id|null>,"error":{"code":...,"message":...}}。
//
// id 传 nil 时序列化成 null（规范要求：无法确定 id 的解析错误用 null，
// 而不是省略——省略会让客户端把响应当成非错误帧丢弃）。
func writeJSONRPCError(w http.ResponseWriter, code int, message string, id json.RawMessage) {
	if id == nil {
		id = json.RawMessage("null")
	}
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: message}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *AudioMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)

	switch r.Method {
	case http.MethodPost:
		h.servePost(w, r, requestID)
	case http.MethodGet, http.MethodDelete:
		// streamable HTTP 的 GET 是服务端推送通道；本实现无推送通知，
		// 按规范返回 405 明示不可用，客户端应继续用 POST。
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "MCP push channel not offered; use POST", "invalid_request_error", "method_not_allowed")
	default:
		w.Header().Set("Allow", http.MethodPost)
		writeErrorJSON(w, http.StatusMethodNotAllowed, requestID, "Method not allowed", "invalid_request_error", "method_not_allowed")
	}
}

func (h *AudioMCPHandler) servePost(w http.ResponseWriter, r *http.Request, requestID string) {
	if h.svc == nil || h.svc.provider == nil || h.svc.upstream == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, requestID, "Audio service unavailable", "server_error", "service_unavailable")
		return
	}
	if _, ok := h.svc.authenticate(w, r, requestID); !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAudioUploadBytes))
	if err != nil {
		writeErrorJSON(w, http.StatusRequestEntityTooLarge, requestID, "Request body too large", "invalid_request_error", "request_too_large")
		return
	}
	body = bytesTrimBOM(body)
	trimmed := strings.TrimSpace(string(body))
	// JSON-RPC 2.0 规范：**协议级**错误（解析失败、非法 Request）仍然用
	// JSON-RPC 错误信封回，且传输层是 HTTP 200 —— 200 表示「JSON-RPC
	// 往返成功，错误在信封里」。此前这三条早失败路径回的是 OpenAI 形态
	// 错误信封 + HTTP 400，MCP 客户端按规范解析会拿不到 error.code
	// （TestMCPEarlyErrorsUseJsonRPCEnvelope 自 2026-10-03 起就是红的，
	// 其失败信息本身在描述当时的行为）。
	//
	// 鉴权/服务不可用/体积超限不属于协议级错误：它们发生在协议成立之前，
	// 保持传输层 4xx（客户端连信封都还没资格收）。
	if trimmed == "" {
		writeJSONRPCError(w, mcpErrParse, "Empty JSON-RPC body", nil)
		return
	}
	if trimmed[0] == '[' {
		writeJSONRPCError(w, mcpErrInvalidRq, "JSON-RPC batch requests are not supported", nil)
		return
	}
	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, mcpErrParse, "Invalid JSON-RPC body", nil)
		return
	}

	// 通知（无 id）：按规范回 202 空体，不做处理。
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.dispatch(r, requestID, req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *AudioMCPHandler) dispatch(r *http.Request, requestID string, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "llm-gateway-audio",
				"version": "1.0.0",
				"title":   "LLM Gateway Audio Tools",
			},
		}}
	case "ping":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": mcpAudioTools()}}
	case "tools/call":
		return h.toolsCall(r, requestID, req)
	default:
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
			Code: mcpErrMethod, Message: "Method not found: " + req.Method,
		}}
	}
}

// mcpAudioTools 声明暴露的两个工具。schema 保持最小必填集，默认值都
// 留给网关解析（model 必填以驱动候选路由）。
func mcpAudioTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "transcribe_audio",
			"description": "把一段录音（wav/mp3 等原始字节）转写为文字。适合会议录音高精度全量转写与短语音输入；服务端自动按模型选择上游传输形态。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"audio_base64": map[string]any{"type": "string", "description": "音频文件内容的 base64（标准编码）"},
					"format":       map[string]any{"type": "string", "description": "音频容器格式：wav / mp3 / webm / m4a 等；小米上游仅支持 wav 与 mp3", "default": "wav"},
					"model":        map[string]any{"type": "string", "description": "网关音频模型名，如 mimo-v2.5-asr"},
					"language":     map[string]any{"type": "string", "description": "ISO-639-1 语种提示（如 zh）；chat-audio 桥接形态下映射为上游 asr_options.language（小米只认 zh/en/auto，无法映射时按自动识别处理）"},
				},
				"required": []string{"audio_base64", "model"},
			},
		},
		{
			"name":        "synthesize_speech",
			"description": "把文本合成为语音（TTS），返回音频字节（小米 mimo-v2.5-tts 系，默认 24kHz WAV）。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":   map[string]any{"type": "string", "description": "要合成的文本"},
					"model":  map[string]any{"type": "string", "description": "网关 TTS 模型名，如 mimo-v2.5-tts"},
					"voice":  map[string]any{"type": "string", "description": "音色；小米 mimo-v2.5-tts：mimo_default/冰糖/茉莉/苏打/白桦/Mia/Chloe/Milo/Dean，非法值自动归一默认音色。mimo-v2.5-tts-voicedesign：这里传音色设计描述（如「温柔清亮的女声」）。mimo-v2.5-tts-voiceclone：这里传样本 DataURL（data:audio/wav;base64,...）"},
					"format": map[string]any{"type": "string", "description": "期望音频格式：wav（默认）/mp3/opus/aac/flac/pcm", "default": "wav"},
				},
				"required": []string{"text", "model"},
			},
		},
	}
}

func (h *AudioMCPHandler) toolsCall(r *http.Request, requestID string, req jsonRPCRequest) jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{
			Code: mcpErrParams, Message: "tools/call requires a tool name",
		}}
	}

	rpcErr := func(code int, msg string) jsonRPCResponse {
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: code, Message: msg}}
	}
	// rpcAudioErr 按与 HTTP 面 writeAudioError 相同的口径分流：调用方参数
	// 问题回 -32602（Invalid params），其余回 -32603。否则客户端会把
	// 「格式不支持」「voiceclone 没给样本」当成网关内部故障去重试。
	rpcAudioErr := func(err error) jsonRPCResponse {
		if isAudioClientInputErr(err) {
			return rpcErr(mcpErrParams, sanitizeAudioErrorMessage(err.Error()))
		}
		var upErr *audioUpstreamStatusError
		if errors.As(err, &upErr) && upErr.clientFault() {
			return rpcErr(mcpErrParams, sanitizeAudioErrorMessage(upErr.Error()))
		}
		return rpcErr(mcpErrInternal, sanitizeAudioErrorMessage(err.Error()))
	}

	switch params.Name {
	case "transcribe_audio":
		var args struct {
			AudioBase64 string `json:"audio_base64"`
			Format      string `json:"format"`
			Model       string `json:"model"`
			Language    string `json:"language"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return rpcErr(mcpErrParams, "invalid arguments: "+err.Error())
		}
		if args.AudioBase64 == "" || args.Model == "" {
			return rpcErr(mcpErrParams, "audio_base64 and model are required")
		}
		audio, derr := base64.StdEncoding.DecodeString(args.AudioBase64)
		if derr != nil {
			return rpcErr(mcpErrParams, "audio_base64 is not valid base64: "+derr.Error())
		}
		format := strings.ToLower(strings.TrimSpace(args.Format))
		if format == "" {
			format = "wav"
		}
		res, terr := h.svc.Transcribe(r.Context(), TranscribeRequest{
			Model: args.Model, Language: args.Language,
			File: audio, Filename: "audio." + format, ContentType: "audio/" + format,
		}, nil)
		if terr != nil {
			return rpcAudioErr(terr)
		}
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{
				"type": "text", "text": res.Text,
			}},
			"structuredContent": map[string]any{
				"text":       res.Text,
				"transport":  res.Transport,
				"model":      res.UpstreamModel,
				"duration_s": res.DurationSeconds,
			},
			"isError": false,
		}}
	case "synthesize_speech":
		var args struct {
			Text   string `json:"text"`
			Model  string `json:"model"`
			Voice  string `json:"voice"`
			Format string `json:"format"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return rpcErr(mcpErrParams, "invalid arguments: "+err.Error())
		}
		if args.Text == "" || args.Model == "" {
			return rpcErr(mcpErrParams, "text and model are required")
		}
		res, serr := h.svc.Synthesize(r.Context(), SynthesizeRequest{
			Model: args.Model, Input: args.Text, Voice: args.Voice, ResponseFormat: args.Format,
		})
		if serr != nil {
			return rpcAudioErr(serr)
		}
		return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{
				"type":     "audio",
				"data":     base64.StdEncoding.EncodeToString(res.Audio),
				"mimeType": res.ContentType,
			}},
			"structuredContent": map[string]any{
				"transport": res.Transport,
				"model":     res.UpstreamModel,
				"bytes":     len(res.Audio),
				"mimeType":  res.ContentType,
			},
			"isError": false,
		}}
	default:
		return rpcErr(mcpErrParams, "unknown tool: "+params.Name)
	}
}

// bytesTrimBOM 去掉 UTF-8 BOM（部分 MCP 客户端会带）。
func bytesTrimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
