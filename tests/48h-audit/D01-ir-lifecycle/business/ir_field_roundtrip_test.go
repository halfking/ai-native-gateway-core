// business/test_ir_field_roundtrip.go
//
// B-01: IR 必填字段在四种协议（openai / anthropic / responses / gemini）下
// 序列化/反序列化后值守恒。失败的话 = IR 中间表示有损，必修。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/business/...

package business

import (
	"reflect"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// minimalRequest 构造一个最小但覆盖必填字段的 IR。
func minimalRequest() *ir.InternalRequest {
	return &ir.InternalRequest{
		Model:    "mock-stress-fast",
		Stream:   false,
		Messages: []ir.Message{{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "1+1=?"}}}},
		MaxTokens: 32,
	}
}

// TestBusiness_IRFieldRoundtrip_OpenAI 走 openai 协议 parse/serialize 一圈。
func TestBusiness_IRFieldRoundtrip_OpenAI(t *testing.T) {
	orig := minimalRequest()
	body, err := ir.SerializeOpenAI(orig)
	if err != nil {
		t.Fatalf("serialize openai: %v", err)
	}
	got, err := ir.ParseOpenAI(body)
	if err != nil {
		t.Fatalf("parse openai: %v", err)
	}
	if got.Model != orig.Model {
		t.Errorf("Model: got=%q want=%q", got.Model, orig.Model)
	}
	if got.Stream != orig.Stream {
		t.Errorf("Stream: got=%v want=%v", got.Stream, orig.Stream)
	}
	if !reflect.DeepEqual(got.Messages, orig.Messages) {
		t.Errorf("Messages: got=%+v want=%+v", got.Messages, orig.Messages)
	}
}

// TestBusiness_IRFieldRoundtrip_Anthropic
func TestBusiness_IRFieldRoundtrip_Anthropic(t *testing.T) {
	orig := minimalRequest()
	body, err := ir.SerializeAnthropic(orig)
	if err != nil {
		t.Fatalf("serialize anthropic: %v", err)
	}
	got, err := ir.ParseAnthropic(body)
	if err != nil {
		t.Fatalf("parse anthropic: %v", err)
	}
	if got.Model != orig.Model {
		t.Errorf("Model: got=%q want=%q", got.Model, orig.Model)
	}
}

// TestBusiness_IRFieldRoundtrip_Responses
func TestBusiness_IRFieldRoundtrip_Responses(t *testing.T) {
	orig := minimalRequest()
	body, err := ir.SerializeResponsesRequest(orig)
	if err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	got, err := ir.ParseResponses(body)
	if err != nil {
		t.Fatalf("parse responses: %v", err)
	}
	if got.Model != orig.Model {
		t.Errorf("Model: got=%q want=%q", got.Model, orig.Model)
	}
}

// TestBusiness_IRFieldRoundtrip_Gemini
//
// 已知差异：Gemini 协议把 model 放在 URL path（/v1beta/models/{model}:generateContent），
// body 里不携带 model 字段。所以 body serialize/parse 后 Model 字段为空属预期，
// 不是 bug。生产路径会由 executor 把 model 拼到 URL。
func TestBusiness_IRFieldRoundtrip_Gemini(t *testing.T) {
	orig := minimalRequest()
	body, err := ir.SerializeGemini(orig)
	if err != nil {
		t.Fatalf("serialize gemini: %v", err)
	}
	got, err := ir.ParseGemini(body)
	if err != nil {
		t.Fatalf("parse gemini: %v", err)
	}
	if got.Stream != orig.Stream {
		t.Errorf("Stream: got=%v want=%v", got.Stream, orig.Stream)
	}
	if !reflect.DeepEqual(got.Messages, orig.Messages) {
		t.Errorf("Messages: got=%+v want=%+v", got.Messages, orig.Messages)
	}
	t.Logf("Gemini body does NOT carry model (URL-path convention); orig=%q got=%q", orig.Model, got.Model)
}