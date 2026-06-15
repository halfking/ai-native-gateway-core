package memora

import "testing"

func TestExtractFromPreviews_SkipsNoise(t *testing.T) {
	turns := []PreviewTurn{
		{Direction: "user", PromptPreview: "short"},
		{Direction: "user", PromptPreview: "You are a helpful assistant. Follow ALL instructions."},
		{Direction: "user", PromptPreview: `{"tool_call": {"name": "grep"}}`},
		{Direction: "user", PromptPreview: "184 部署 llm-gateway-go 完成，healthz 返回 200，镜像 tag 1.0.0-abc"},
	}
	st := ExtractFromPreviews(turns, nil, false)
	if len(st.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %v", len(st.Candidates), st.Candidates)
	}
	if st.SkippedNoise < 3 {
		t.Fatalf("expected >=3 skipped noise, got %d", st.SkippedNoise)
	}
}

func TestExtractFromPreviews_DedupExisting(t *testing.T) {
	fact := "184 部署 llm-gateway-go 完成，healthz 返回 200"
	turns := []PreviewTurn{
		{Direction: "user", PromptPreview: fact},
	}
	st := ExtractFromPreviews(turns, []string{fact}, false)
	if len(st.Candidates) != 0 {
		t.Fatalf("expected duplicate skip, got %v", st.Candidates)
	}
	if st.SkippedDuplicate != 1 {
		t.Fatalf("expected 1 duplicate skip, got %d", st.SkippedDuplicate)
	}
}

func TestExtractFromPreviews_IncludesResponse(t *testing.T) {
	turns := []PreviewTurn{
		{
			Direction:       "assistant",
			ResponsePreview: "根因是 nginx upstream 指向了旧 Python 网关，已改回 Go 8781 端口。",
		},
	}
	st := ExtractFromPreviews(turns, nil, true)
	if len(st.Candidates) != 1 {
		t.Fatalf("expected response candidate, got %v", st.Candidates)
	}
}
