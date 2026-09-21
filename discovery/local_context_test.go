package discovery

import (
	"encoding/json"
	"testing"
)

// 2026-09-07 本地托管供应商 context window 解析测试。

func TestParseContextFieldsFromModelsJSON(t *testing.T) {
	// vLLM 风格: max_model_len
	vllm := []byte(`{"object":"list","data":[
		{"id":"qwen3-8b","max_model_len":32768},
		{"id":"llama-7b","max_model_len":4096}
	]}`)
	got := parseContextFieldsFromModelsJSON(vllm)
	if got["qwen3-8b"] != 32768 || got["llama-7b"] != 4096 {
		t.Fatalf("vLLM parse wrong: %v", got)
	}

	// llama.cpp 风格: context_length
	llamacpp := []byte(`{"object":"list","data":[{"id":"main","context_length":8192}]}`)
	if got = parseContextFieldsFromModelsJSON(llamacpp); got["main"] != 8192 {
		t.Fatalf("llama.cpp parse wrong: %v", got)
	}

	// 通用命名: context_window
	generic := []byte(`{"data":[{"id":"m1","context_window":16384}]}`)
	if got = parseContextFieldsFromModelsJSON(generic); got["m1"] != 16384 {
		t.Fatalf("context_window parse wrong: %v", got)
	}

	// ollama 原生 models 数组（少见但容错）
	ollama := []byte(`{"models":[{"name":"qwen:8b","context_length":40960}]}`)
	if got = parseContextFieldsFromModelsJSON(ollama); got["qwen:8b"] != 40960 {
		t.Fatalf("ollama models parse wrong: %v", got)
	}

	// 空体/坏 JSON 安全
	if got = parseContextFieldsFromModelsJSON(nil); len(got) != 0 {
		t.Fatalf("nil body should yield empty map: %v", got)
	}
	if got = parseContextFieldsFromModelsJSON([]byte("not json")); len(got) != 0 {
		t.Fatalf("bad json should yield empty map: %v", got)
	}
}

func TestParseOllamaShowContextLength(t *testing.T) {
	doc := map[string]any{
		"model_info": map[string]any{
			"qwen3.context_length":   float64(40960),
			"qwen3.embedding_length": float64(4096),
		},
	}
	data, _ := json.Marshal(doc)
	if got := parseOllamaShowContextLength(data); got != 40960 {
		t.Fatalf("ollama show parse = %d, want 40960", got)
	}

	// 字符串数值容错
	doc2 := map[string]any{"model_info": map[string]any{"llama.context_length": "8192"}}
	data2, _ := json.Marshal(doc2)
	if got := parseOllamaShowContextLength(data2); got != 8192 {
		t.Fatalf("string value parse = %d, want 8192", got)
	}

	// 无该字段
	doc3 := map[string]any{"model_info": map[string]any{"llama.embedding_length": float64(4096)}}
	data3, _ := json.Marshal(doc3)
	if got := parseOllamaShowContextLength(data3); got != 0 {
		t.Fatalf("missing context_length should be 0, got %d", got)
	}
}

func TestLocalDefaultContextWindow(t *testing.T) {
	caps := []byte(`{"hosting_type":"mlx-lm","default_context_window":8192}`)
	if got := localDefaultContextWindow(caps); got != 8192 {
		t.Fatalf("default context = %d, want 8192", got)
	}
	if got := localHostingType(caps); got != "mlx-lm" {
		t.Fatalf("hosting type = %q, want mlx-lm", got)
	}
	if got := localDefaultContextWindow([]byte(`{}`)); got != 0 {
		t.Fatalf("empty caps should yield 0, got %d", got)
	}
}

func TestIsLocalProviderKind(t *testing.T) {
	if !isLocalProviderKind("local") || !isLocalProviderKind("Local ") {
		t.Fatal("local kind should match case-insensitively")
	}
	if isLocalProviderKind("cloud") {
		t.Fatal("cloud must not match local")
	}
}
