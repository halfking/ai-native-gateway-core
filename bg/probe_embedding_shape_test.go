package bg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

// 2026-11-02 embedding 探针轮 —— 事故回归钉桩。
//
// 实测（211 部署，provider 19 = 本机 ollama，2026-11-02 01:24:16）：
// 探针对 embedding 模型发的是 **chat** 形态（一条 user 消息 + 端点
// /chat/completions）⇒ Ollama 回 HTTP 400 ⇒ updateBindingAvailability 把
// 这个 binding 写成 available=FALSE / unavailable_reason='probe_http_400'
// ⇒ 5 分钟内 /v1/embeddings 对所有租户返 503。
//
// 触发它的是一次 /v1/chat/completions 误请求（拿 embedding 模型当 chat 模型
// 问）——探针形态用错被记成了「上游不健康」。

// TestInferModalityEmbeddingModels pins the name→modality inference the
// probe shape dispatch falls back to. If this regresses, the embedding probe
// silently goes back to the chat shape.
func TestInferModalityEmbeddingModels(t *testing.T) {
	for _, m := range []string{
		"qwen3-embedding:0.6b",
		"qwen3-embedding:8b",
		"text-embedding-3-small",
	} {
		if got := modelname.InferModality(m); got != "embedding" {
			t.Errorf("InferModality(%q) = %q, want %q", m, got, "embedding")
		}
	}
}

// TestProbeShapeForModalityUsesDBModality pins that the **authoritative**
// modality (models_canonical.modality) drives the probe shape even when the
// model NAME does not say "embedding".
//
// bge-m3 is the concrete case: its name carries no embedding token, so
// modelname.InferModality("bge-m3") = "text" — a name-only probe would send
// the chat shape to an embedding model and get HTTP 400 from the upstream,
// which the probe then books as "upstream unhealthy" and evicts the binding.
func TestProbeShapeForModalityUsesDBModality(t *testing.T) {
	if got := modelname.InferModality("bge-m3"); got != "text" {
		t.Fatalf("precondition: InferModality(\"bge-m3\") = %q, want %q (name inference misses it — that is the whole point)", got, "text")
	}
	shape := probeShapeForModality("bge-m3", "openai-completions", "embedding")
	if shape.endpoint != upstreamurl.EpEmbeddings {
		t.Fatalf("endpoint = %v, want %v", shape.endpoint, upstreamurl.EpEmbeddings)
	}
	if shape.wire != "embeddings" {
		t.Fatalf("wire = %q, want %q", shape.wire, "embeddings")
	}
	if !strings.Contains(shape.body, `"input"`) {
		t.Fatalf("body must be the embeddings shape: %s", shape.body)
	}
}

// TestProbeShapeForModalityTextDoesNotOverrideInference pins the merge rule:
// a stale "text" in the DB must NOT push an embedding-named model back to the
// chat shape. Measured on the 211 deployment: provider_models.modality was
// still 'text' for qwen3-embedding:* while models_canonical.modality was
// already 'embedding' — the stale column must not win.
func TestProbeShapeForModalityTextDoesNotOverrideInference(t *testing.T) {
	shape := probeShapeForModality("qwen3-embedding:0.6b", "openai-completions", "text")
	if shape.endpoint != upstreamurl.EpEmbeddings {
		t.Fatalf("endpoint = %v, want %v (a stale 'text' must not override the name inference)", shape.endpoint, upstreamurl.EpEmbeddings)
	}
}

// TestProbeShapeForModalityAudioWins pins that a positive DB modality is
// honoured for audio models too (the 2026-10-03 audio dispatch).
func TestProbeShapeForModalityAudioWins(t *testing.T) {
	shape := probeShapeForModality("some-asr-model", "openai-completions", "audio")
	if shape.endpoint != upstreamurl.EpChatCompletions {
		t.Fatalf("endpoint = %v, want %v (audio keeps the chat endpoint)", shape.endpoint, upstreamurl.EpChatCompletions)
	}
	if !strings.Contains(shape.body, "input_audio") {
		t.Fatalf("audio body must keep the input_audio shape: %s", shape.body)
	}
}

// TestProbeShapeForEmbedding pins the endpoint/body/wire triple for embedding
// models: embeddings endpoint, embeddings body, embeddings wire format.
func TestProbeShapeForEmbedding(t *testing.T) {
	for _, m := range []string{"qwen3-embedding:0.6b", "qwen3-embedding:8b"} {
		shape := probeShapeFor(m, "openai-completions")
		if shape.endpoint != upstreamurl.EpEmbeddings {
			t.Errorf("probeShapeFor(%q).endpoint = %v, want %v", m, shape.endpoint, upstreamurl.EpEmbeddings)
		}
		if shape.wire != "embeddings" {
			t.Errorf("probeShapeFor(%q).wire = %q, want %q", m, shape.wire, "embeddings")
		}
		if strings.Contains(shape.body, "messages") {
			t.Errorf("probeShapeFor(%q).body still carries a chat `messages` field: %s", m, shape.body)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(shape.body), &body); err != nil {
			t.Fatalf("probeShapeFor(%q).body is not valid JSON: %v", m, err)
		}
		if body["model"] != m {
			t.Errorf("probeShapeFor(%q).body model = %v, want %q", m, body["model"], m)
		}
		if _, ok := body["input"]; !ok {
			t.Errorf("probeShapeFor(%q).body must carry an `input` field: %s", m, shape.body)
		}
	}
}

// TestProbeShapeForTextUnchanged pins that a plain chat model keeps the chat
// endpoint and chat body — the embedding fix must not move anything else.
func TestProbeShapeForTextUnchanged(t *testing.T) {
	shape := probeShapeFor("qwen3.5:4b", "openai-completions")
	if shape.endpoint != upstreamurl.EpChatCompletions {
		t.Errorf("endpoint = %v, want %v", shape.endpoint, upstreamurl.EpChatCompletions)
	}
	if shape.wire != "chat" {
		t.Errorf("wire = %q, want %q", shape.wire, "chat")
	}
	if !strings.Contains(shape.body, "messages") {
		t.Errorf("body must keep the chat `messages` field: %s", shape.body)
	}
}

// TestProbeShapeForAudioUnchanged pins that the 2026-10-03 audio dispatch is
// untouched: audio models keep the chat ENDPOINT (mimo serves ASR/TTS there)
// but swap the body.
func TestProbeShapeForAudioUnchanged(t *testing.T) {
	shape := probeShapeFor("whisper-large-v3", "openai-completions")
	if shape.endpoint != upstreamurl.EpChatCompletions {
		t.Errorf("endpoint = %v, want %v (audio keeps the chat endpoint)", shape.endpoint, upstreamurl.EpChatCompletions)
	}
	if !strings.Contains(shape.body, "input_audio") {
		t.Errorf("audio body must keep the input_audio shape: %s", shape.body)
	}
}

// TestDirectProbeEndpointForEmbedding pins the built URL: an embedding model
// must land on /embeddings, never on /chat/completions.
func TestDirectProbeEndpointForEmbedding(t *testing.T) {
	got := directProbeEndpoint("http://192.168.31.30:11434/v1", "qwen3-embedding:0.6b", "openai-completions")
	want := "http://192.168.31.30:11434/v1/embeddings"
	if got != want {
		t.Fatalf("directProbeEndpoint = %q, want %q", got, want)
	}
	gotChat := directProbeEndpoint("http://192.168.31.30:11434/v1", "qwen3.5:4b", "openai-completions")
	wantChat := "http://192.168.31.30:11434/v1/chat/completions"
	if gotChat != wantChat {
		t.Fatalf("directProbeEndpoint(chat) = %q, want %q", gotChat, wantChat)
	}
}

// TestEmbeddingsProbeHasOutput pins the embeddings wire-format validator:
// a real embeddings response counts as output; an error body and an empty
// embedding array do not.
func TestEmbeddingsProbeHasOutput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "real embeddings response",
			body: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2,0.3]}],"model":"qwen3-embedding:0.6b","usage":{"prompt_tokens":1,"total_tokens":1}}`,
			want: true,
		},
		{
			name: "empty embedding array",
			body: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[]}]}`,
			want: false,
		},
		{
			name: "no data at all",
			body: `{"object":"list","data":[]}`,
			want: false,
		},
		{
			name: "error body",
			body: `{"error":{"message":"does not support chat","type":"invalid_request_error"}}`,
			want: false,
		},
		{
			name: "foreign object type",
			body: `{"object":"chat.completion","data":[{"embedding":[0.1]}]}`,
			want: false,
		},
		{
			name: "malformed json",
			body: `{not json`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddingsProbeHasOutput([]byte(tc.body)); got != tc.want {
				t.Fatalf("embeddingsProbeHasOutput = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCredentialProbeHasOutputEmbeddingsWire pins that the shared
// deliverability entry point routes the embeddings wire format to the
// embeddings validator (and does NOT fall through to the chat arm, which
// would reject the embeddings envelope as an unknown format).
func TestCredentialProbeHasOutputEmbeddingsWire(t *testing.T) {
	body := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]}],"usage":{"total_tokens":1}}`
	if !credentialProbeHasOutput("embeddings", []byte(body)) {
		t.Fatal("credentialProbeHasOutput(embeddings) = false, want true")
	}
	if credentialProbeHasOutput("chat", []byte(body)) {
		t.Fatal("credentialProbeHasOutput(chat) on an embeddings envelope = true, want false (chat arm must not accept it)")
	}
}
