package intentanalysis

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TestNewIntentAnalysisHook 测试Hook创建
func TestNewIntentAnalysisHook(t *testing.T) {
	hook := NewIntentAnalysisHook(nil, nil)
	if hook == nil {
		t.Fatal("Expected hook to be created")
	}
	
	if hook.Name() != "intent_analysis" {
		t.Errorf("Expected name 'intent_analysis', got %s", hook.Name())
	}
	
	if hook.enabled {
		t.Error("Expected hook to be disabled when analyzer is nil")
	}
}

func TestExtractUserContent(t *testing.T) {
	tests := []struct {
		name    string
		req     *domain.PipelineRequest
		want    string
	}{
		{
			name: "从metadata提取",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"user_content": "Hello world",
				},
			},
			want: "Hello world",
		},
		{
			name: "从messages提取",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"messages": []any{
						map[string]any{"role": "system", "content": "You are helpful"},
						map[string]any{"role": "user", "content": "Hello"},
					},
				},
			},
			want: "Hello",
		},
		{
			name: "从prompt提取",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"prompt": "Test prompt",
				},
			},
			want: "Test prompt",
		},
		{
			name: "空请求",
			req:  &domain.PipelineRequest{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractUserContent(tt.req)
			if got != tt.want {
				t.Errorf("extractUserContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectImages(t *testing.T) {
	tests := []struct {
		name string
		req  *domain.PipelineRequest
		want bool
	}{
		{
			name: "包含图片",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"messages": []any{
						map[string]any{
							"role": "user",
							"content": []any{
								map[string]any{"type": "text", "text": "What's in this image?"},
								map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://..."}},
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "不包含图片",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"messages": []any{
						map[string]any{"role": "user", "content": "Hello"},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectImages(tt.req)
			if got != tt.want {
				t.Errorf("detectImages() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountTools(t *testing.T) {
	tests := []struct {
		name string
		req  *domain.PipelineRequest
		want int
	}{
		{
			name: "有工具",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{
					"tools": []any{
						map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
						map[string]any{"type": "function", "function": map[string]any{"name": "get_stock"}},
					},
				},
			},
			want: 2,
		},
		{
			name: "无工具",
			req: &domain.PipelineRequest{
				Metadata: map[string]any{},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countTools(tt.req)
			if got != tt.want {
				t.Errorf("countTools() = %v, want %v", got, tt.want)
			}
		})
	}
}
