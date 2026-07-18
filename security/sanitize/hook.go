package sanitize

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

const (
	// MetadataKeySanitizeMap 是 PipelineRequest.Metadata 中存储 SanitizeMap 的 key
	MetadataKeySanitizeMap = "sanitize_map"
)

// SanitizerInputHook 在 pre-routing 阶段对请求体做可逆脱敏。
//
// 位置：PhasePreRouting，priority 10（最先执行，在 InputGuard 之前）
//
// 将敏感信息（手机号/身份证等）替换为 {SENSITIVE:type:index} 占位符，
// 把占位符→原始值映射存入 Metadata["sanitize_map"]。
// 输出侧 hook 从这个 map 还原。
type SanitizerInputHook struct {
	sanitizer *Sanitizer
}

var _ pipeline.Hook = (*SanitizerInputHook)(nil)

func NewSanitizerInputHook(s *Sanitizer) (*SanitizerInputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer input hook: %w", ErrNilSanitizer)
	}
	return &SanitizerInputHook{sanitizer: s}, nil
}

func (h *SanitizerInputHook) Name() string { return "sanitizer.input" }

func (h *SanitizerInputHook) Priority() int { return 10 }

func (h *SanitizerInputHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	return env != nil && len(env.TransformedRequest) > 0
}

func (h *SanitizerInputHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h == nil || h.sanitizer == nil {
		return nil
	}
	if env == nil || len(env.TransformedRequest) == 0 {
		return nil
	}

	input := string(env.TransformedRequest)
	result, err := h.sanitizer.SanitizeInput(ctx, input)
	if err != nil {
		return fmt.Errorf("sanitizer input: %w", err)
	}

	if len(result.SanitizeMap) == 0 {
		return nil
	}

	env.TransformedRequest = []byte(result.SanitizedText)

	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata[MetadataKeySanitizeMap] = result.SanitizeMap

	return nil
}

func (h *SanitizerInputHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}

// SanitizerOutputHook 在 post-upstream 阶段对 LLM 响应做占位符还原。
//
// 位置：PhasePostUpstream，priority 190（在 OutputGuard 之后执行还原）
//
// 从 Metadata["sanitize_map"] 读取脱敏映射表，
// 将 LLM 输出中的 {SENSITIVE:type:index} 精确还原为原始值。
type SanitizerOutputHook struct {
	sanitizer *Sanitizer
}

var _ pipeline.Hook = (*SanitizerOutputHook)(nil)

func NewSanitizerOutputHook(s *Sanitizer) (*SanitizerOutputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer output hook: %w", ErrNilSanitizer)
	}
	return &SanitizerOutputHook{sanitizer: s}, nil
}

func (h *SanitizerOutputHook) Name() string { return "sanitizer.output" }

func (h *SanitizerOutputHook) Priority() int { return 190 }

func (h *SanitizerOutputHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	if env == nil || len(env.UpstreamResponse) == 0 {
		return false
	}
	_, ok := env.Metadata[MetadataKeySanitizeMap]
	return ok
}

func (h *SanitizerOutputHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h == nil || h.sanitizer == nil {
		return nil
	}
	if env == nil || len(env.UpstreamResponse) == 0 {
		return nil
	}

	smRaw, ok := env.Metadata[MetadataKeySanitizeMap]
	if !ok {
		return nil
	}
	sm, ok := smRaw.(SanitizeMap)
	if !ok {
		return nil
	}

	output := string(env.UpstreamResponse)
	result, err := h.sanitizer.RestoreOutput(ctx, output, sm)
	if err != nil {
		return fmt.Errorf("sanitizer output: %w", err)
	}

	env.UpstreamResponse = []byte(result)
	return nil
}

func (h *SanitizerOutputHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}
