// Package streaming — audio_minimax_stt.go
//
// MiniMax asr-1.0 的 speech_to_text 传输形态（2026-10-06 直连实测
// api.minimaxi.com）：
//
//	POST {base}/speech_to_text   multipart：file + model（+stream）
//	非流式 → {"text":"...","duration":9.44,"trace_id":"..."}
//	流式   → data: {"index":0,"delta":"各","finish":false}
//	         data: {"index":1,"delta":"位好，…","finish":false}
//	         data: {"index":2,"delta":"方案，…","finish":true,"duration":9.44}
//	         （私有形状：无 [DONE] 哨兵，终帧才带 duration；index 是顺序
//	         分段号，delta 是**新增**片段，全文 = 按 index 顺序拼接）
//
// 标准路径 /v1/audio/transcriptions 在该上游是 404（"404 page not found"），
// 所以 minimax 候选直接钉死 speech_to_text，不做 404 回落的空往返（与
// 小米钉 chat-audio 同款决策，见 preferChatAudioBridge）。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// AudioTransportSpeechToText 是 MiniMax asr-1.0 的非标传输形态名，
// 经 X-Gw-Audio-Transport 响应头与 MCP structuredContent.transport 透出。
const AudioTransportSpeechToText = "speech-to-text"

// preferSpeechToText 报告该候选应直接走 MiniMax speech_to_text（跳过
// multipart 透传的 404 往返）。与 preferChatAudioBridge 同款判据来源：
// catalog_code 由 provider 目录写入，不猜 host。
func preferSpeechToText(c provider.Candidate) bool {
	return strings.EqualFold(strings.TrimSpace(c.CatalogCode), "minimax")
}

// minimaxSTTFrame 是 speech_to_text 流式的一帧（私有形状，非 OpenAI 事件）。
type minimaxSTTFrame struct {
	Index    int      `json:"index"`
	Delta    string   `json:"delta"`
	Finish   bool     `json:"finish"`
	Duration *float64 `json:"duration,omitempty"`
}

// transcribeViaSpeechToText 把音频以 multipart 形态发给 MiniMax
// /speech_to_text。流式时把每帧 delta 通过 emitDelta 下发（顺序即 index 序），
// 最终文本 = 全部 delta 拼接。
func (s *AudioService) transcribeViaSpeechToText(ctx context.Context, cand provider.Candidate, req TranscribeRequest, format string, emitDelta func(string)) (*TranscribeResult, error) {
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
	// stream 与 verbose_json/srt/vtt 互斥（上游契约，openpocket 调研 2026-10-01
	// 同口径）；我们只有 json/text 两类 response_format，stream 只在前者携带。
	if req.Stream && (req.ResponseFormat == "" || req.ResponseFormat == "json") {
		_ = mw.WriteField("stream", "true")
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(cand.BaseURL, "/") + "/speech_to_text"
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
		body, _ := readLimitedResponse(resp.Body, 8<<10)
		return nil, newAudioUpstreamError(resp.StatusCode, body)
	}

	res := &TranscribeResult{
		Transport:     AudioTransportSpeechToText,
		UpstreamModel: cand.RawModel,
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		text, dur, perr := parseMinimaxSTTSSE(resp.Body, emitDelta)
		if perr != nil {
			return nil, perr
		}
		res.Text = text
		res.DurationSeconds = dur
		return res, nil
	}

	body, err := readLimitedResponse(resp.Body, maxAudioResponseBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Text     string   `json:"text"`
		Duration *float64 `json:"duration"`
		TraceID  string   `json:"trace_id"`
	}
	if jerr := json.Unmarshal(body, &parsed); jerr != nil {
		return nil, fmt.Errorf("speech_to_text response is not JSON: %w", jerr)
	}
	res.Text = parsed.Text
	res.DurationSeconds = parsed.Duration
	return res, nil
}

// parseMinimaxSTTSSE 逐帧解析 speech_to_text 的私有 SSE 形状。该上游没有
// [DONE] 哨兵，以 EOF / finish=true 帧收尾；duration 只在终帧出现。
// delta 按**到达顺序**拼接（上游按 index 顺序发帧；乱序帧不做重排——
// 实测从未出现，重排反而会掩盖上游契约变化）。
// SSE 分帧复用 chat-audio 桥接的 sseLineReader（同一包内最小子集实现）。
func parseMinimaxSTTSSE(body io.Reader, emitDelta func(string)) (string, *float64, error) {
	var full strings.Builder
	var dur *float64
	lr := newSSELineReader(context.Background(), body)
	for lr.Next() {
		_, payload, ok := lr.Event()
		if !ok || payload == "" || payload == "[DONE]" {
			continue
		}
		var frame minimaxSTTFrame
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			// 容忍心跳/注释帧：解析失败的帧跳过而不是整条流失败——
			// 与 chat-audio 桥接的容错口径一致。
			continue
		}
		if frame.Delta != "" {
			full.WriteString(frame.Delta)
			if emitDelta != nil {
				emitDelta(frame.Delta)
			}
		}
		if frame.Duration != nil {
			d := *frame.Duration
			dur = &d
		}
		if frame.Finish {
			break
		}
	}
	if err := lr.Err(); err != nil {
		return "", nil, err
	}
	return full.String(), dur, nil
}
