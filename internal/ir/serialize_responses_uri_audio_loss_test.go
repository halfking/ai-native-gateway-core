package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 2026-10-29（Gemini 批判审计三轮遗留③）：Responses 侧损失上报点补齐。
//
// Gemini fileData 音频（IR Type="file_uri"，URI 载在 FileURI）在 Responses
// 线上无处安放 —— input_audio 只收 base64 data+format，
// buildResponsesContent 对 buildResponsesInputAudio 返回 nil 的块整块跳过。
// 跳过此前静默；本轮起与 OpenAI Chat 侧同款：跳过必须经
// reportSerializeResponsesLosses 留痕。document 块构不成 input_file 的
// 三种形态（nil source / url+data 双空 / 非法载体空 data）同轮补上。
// ---------------------------------------------------------------------------

func uriAudioLossRequest(src string) *InternalRequest {
	return &InternalRequest{
		Model:          "gpt-4o-transcribe",
		SourceProtocol: src,
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "audio", Audio: &MediaSource{Type: "file_uri", FileURI: "https://files.example.internal/audio.wav", Format: "wav"}},
					{Type: "text", Text: "transcribe this"},
				},
			},
		},
	}
}

func TestResponsesURIAudioLossReported(t *testing.T) {
	cap := resetDedupAndInstall(t)

	out, err := SerializeResponsesRequest(uriAudioLossRequest(ProtocolGeminiGenerate))
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}

	// The URI-only audio must be dropped, not emitted as a data-less block.
	var wire struct {
		Input []struct {
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("re-unmarshal serialized request: %v", err)
	}
	content := wire.Input[0].Content
	if len(content) != 1 || content[0]["type"] != "input_text" {
		t.Fatalf("URI-only audio block should be dropped, content now: %+v", content)
	}

	if !cap.hasEvent(AnomalyEvent{
		AnomalyType:    AnomalyProtocolLoss,
		FieldPath:      "messages[0].content[0].audio",
		TargetProtocol: ProtocolOpenAIResponses,
		Reason:         "loss",
	}) {
		t.Fatalf("expected ir_protocol_loss for the URI-only audio; events=%+v", cap.snapshot())
	}
}

// 同协议守卫：Responses 源的 IR 不报（函数顶部早退），与既有同族口径一致。
func TestResponsesURIAudioSameProtocolNotReported(t *testing.T) {
	cap := resetDedupAndInstall(t)

	if _, err := SerializeResponsesRequest(uriAudioLossRequest(ProtocolOpenAIResponses)); err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("same-protocol URI audio must not be reported, got %d events: %+v", got, cap.snapshot())
	}
}

func TestResponsesSourcelessDocumentLossReported(t *testing.T) {
	cap := resetDedupAndInstall(t)

	req := &InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ProtocolGeminiGenerate,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "document", Document: &DocumentBlock{Title: "report.pdf"}}},
			},
		},
	}
	out, err := SerializeResponsesRequest(req)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if strings.Contains(string(out), "input_file") {
		t.Fatalf("source-less document must be dropped, wire: %s", out)
	}
	if !cap.hasEvent(AnomalyEvent{
		AnomalyType:    AnomalyProtocolLoss,
		FieldPath:      "messages[0].content[0].document.source",
		TargetProtocol: ProtocolOpenAIResponses,
		Reason:         "loss",
	}) {
		t.Fatalf("expected ir_protocol_loss for the source-less document; events=%+v", cap.snapshot())
	}
}

func TestResponsesURLEmptyDocumentLossReported(t *testing.T) {
	cap := resetDedupAndInstall(t)

	req := &InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ProtocolGeminiGenerate,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "document", Document: &DocumentBlock{Source: &DocumentSource{Type: "url"}}}},
			},
		},
	}
	if _, err := SerializeResponsesRequest(req); err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if !cap.hasEvent(AnomalyEvent{
		AnomalyType:    AnomalyProtocolLoss,
		FieldPath:      "messages[0].content[0].document.source",
		TargetProtocol: ProtocolOpenAIResponses,
		Reason:         "loss",
	}) {
		t.Fatalf("expected ir_protocol_loss for the empty-url document; events=%+v", cap.snapshot())
	}
}

// file_id 双空 document 走既有分支（filename-only 块仍发出），不与本轮新分支双报。
func TestResponsesDoubleEmptyFileIDDocumentSingleReport(t *testing.T) {
	cap := resetDedupAndInstall(t)

	req := &InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ProtocolAnthropicMessages,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "document", Document: &DocumentBlock{Source: &DocumentSource{Type: "file"}}}},
			},
		},
	}
	if _, err := SerializeResponsesRequest(req); err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	events := cap.snapshot()
	matches := 0
	for _, ev := range events {
		if ev.FieldPath == "messages[0].content[0].document.file_id" {
			matches++
		}
		if ev.FieldPath == "messages[0].content[0].document.source" {
			t.Fatalf("double-empty file_id must NOT hit the new dropped-document branch: %+v", events)
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one document.file_id loss, got %d: %+v", matches, events)
	}
}

// URL 载体的 document 正常发出，不误报。
func TestResponsesURLDocumentNoFalseLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)

	req := &InternalRequest{
		Model:          "gpt-4o",
		SourceProtocol: ProtocolGeminiGenerate,
		Messages: []Message{
			{
				Role:    "user",
				Content: []ContentBlock{{Type: "document", Document: &DocumentBlock{Source: &DocumentSource{Type: "url", URL: "https://example.com/doc.pdf"}}}},
			},
		},
	}
	if _, err := SerializeResponsesRequest(req); err != nil {
		t.Fatalf("SerializeResponsesRequest failed: %v", err)
	}
	if got := len(cap.snapshot()); got != 0 {
		t.Fatalf("URL document must serialize losslessly, got %d events: %+v", got, cap.snapshot())
	}
}
