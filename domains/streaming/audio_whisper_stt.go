// Package streaming — audio_whisper_stt.go
//
// whisper.cpp server 的 /inference 传输形态（2026-10-26 多模态标准接口轮，
// 本机 whisper-server 1.9.5 实测）：
//
//	POST {base}/inference   multipart：file + response_format（json/text）
//	                       （+ language / prompt，服务端认这两个字段）
//	json → {"text":"..."}    text → 纯文本
//
// 标准 OpenAI 路径 {base}/audio/transcriptions 在该上游 404（响应体
// "File Not Found (/v1/audio/transcriptions)"），所以 whisper.cpp 候选
// 直接钉 /inference，不做 404 回落的空往返（与 MiniMax speech_to_text
// 钉死同款决策）。
//
// 该上游只收 16kHz 单声道 WAV（whisper.cpp 原生约束），网关不做转码
// （与整条音频面「无编解码转换」的边界一致）；调用方按 OpenAI 契约发
// multipart，音频格式错误由上游如实报错。
//
// 流式（stream=true）：whisper.cpp 无 SSE，走 handler 的非 SSE 兜底——
// 单个 transcript.text.done 事件携带全文（SSE 契约不变）。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// AudioTransportWhisperInference 是 whisper.cpp /inference 的传输形态名，
// 经 X-Gw-Audio-Transport 响应头与 SSE transcript.transport 事件透出。
const AudioTransportWhisperInference = "whisper-inference"

// whisperInferenceCatalogCodes 是「上游说话 whisper.cpp /inference 方言」
// 的 catalog code 集合。与 preferSpeechToText 同款判据来源：catalog_code
// 由 provider 目录写入，不猜 host——绝不按 base_url 或模型名猜（云端
// Groq 等也有 whisper-* 模型名，但它们说的是标准 OpenAI 方言）。
var whisperInferenceCatalogCodes = map[string]bool{
	"whisper-cpp":       true,
	"whisper-cpp-large": true,
}

// preferWhisperInference 报告该候选应直接走 whisper.cpp /inference。
func preferWhisperInference(cand provider.Candidate) bool {
	return whisperInferenceCatalogCodes[strings.ToLower(strings.TrimSpace(cand.CatalogCode))]
}

// transcribeViaWhisperInference 把音频以 multipart 形态发给 whisper.cpp
// server 的 /inference 端点。URL = {base}/inference：注册该类 provider 时
// base_url 不带 /v1（whisper.cpp 的端点全部挂在根路径上）。
func (s *AudioService) transcribeViaWhisperInference(ctx context.Context, cand provider.Candidate, req TranscribeRequest) (*TranscribeResult, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", req.Filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(req.File); err != nil {
		return nil, err
	}
	_ = mw.WriteField("model", cand.RawModel)
	if req.Language != "" {
		_ = mw.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		_ = mw.WriteField("prompt", req.Prompt)
	}
	// whisper.cpp 认 json/text/srt/vtt/lrc/csv；网关契约里的 verbose_json
	// 它没有——回落 json（响应 {text}，丢 segments 细节比 400 好，且
	// verbose_json 在这条方言上本就无从兑现）。
	rf := req.ResponseFormat
	switch rf {
	case "text", "srt", "vtt":
	default:
		rf = "json"
	}
	_ = mw.WriteField("response_format", rf)
	if err := mw.Close(); err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(cand.BaseURL, "/") + "/inference"
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Authorization", "Bearer "+cand.APIKey)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upReq.Header.Set("X-Gateway-Internal-Purpose", "audio_transcription")

	resp, upErr := s.upstream.Do(upReq)
	if upErr != nil {
		return nil, upErr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusUnsupportedMediaType {
		return nil, &audioTransportError{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, newAudioUpstreamError(resp.StatusCode, raw)
	}

	body, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	res := &TranscribeResult{
		Transport:     AudioTransportWhisperInference,
		UpstreamModel: cand.RawModel,
		RelayBody:     body,
	}
	if rf != "json" {
		res.RelayIsTextOnly = true
		res.Text = string(body)
		return res, nil
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if jerr := json.Unmarshal(body, &parsed); jerr == nil {
		res.Text = parsed.Text
	} else {
		// 上游 2xx 但不是 JSON：如实带原文给调用方，不静默清空。
		return nil, fmt.Errorf("whisper /inference returned non-JSON body (http %d): %.200s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return res, nil
}
