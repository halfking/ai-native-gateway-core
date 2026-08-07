package sanitize

import (
	"context"
	"fmt"
	"log/slog"

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
	sanitizer      *Sanitizer
	sessionManager *SessionSanitizeManager // 会话级映射表管理器（可选）
}

var _ pipeline.Hook = (*SanitizerInputHook)(nil)

func NewSanitizerInputHook(s *Sanitizer) (*SanitizerInputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer input hook: %w", ErrNilSanitizer)
	}
	return &SanitizerInputHook{sanitizer: s}, nil
}

// NewSanitizerInputHookWithSessionManager 创建带会话管理器的输入Hook
func NewSanitizerInputHookWithSessionManager(s *Sanitizer, mgr *SessionSanitizeManager) (*SanitizerInputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer input hook: %w", ErrNilSanitizer)
	}
	return &SanitizerInputHook{
		sanitizer:      s,
		sessionManager: mgr,
	}, nil
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

	// 【新增】合并到会话级Redis缓存
	if h.sessionManager != nil && env.SessionID != "" && env.TenantID != "" {
		if err := h.sessionManager.MergeSanitizeMap(ctx, env.TenantID, env.SessionID, result.SanitizeMap); err != nil {
			// 不阻断流程，只记录告警
			// 注意：这里使用 slog 而不是 h.logger，因为原Hook没有logger字段
			slog.WarnContext(ctx, "failed to merge sanitize map to session cache",
				"error", err,
				"session_id", env.SessionID,
				"tenant_id", env.TenantID,
			)
		} else {
			// 延长TTL（用户继续对话）
			_ = h.sessionManager.ExtendTTL(ctx, env.TenantID, env.SessionID)
		}
	}

	return nil
}

func (h *SanitizerInputHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}

// SanitizerOutputHook 在 post-upstream 阶段对 LLM 响应做占位符还原。
//
// 位置：PhasePostUpstream，priority 50（在 OutputCompliance 之前执行还原）
//
// 从 Metadata["sanitize_map"] 或会话级Redis读取脱敏映射表，
// 将 LLM 输出中的 {SENSITIVE:type:index} 精确还原为原始值。
type SanitizerOutputHook struct {
	sanitizer      *Sanitizer
	sessionManager *SessionSanitizeManager // 会话级映射表管理器（可选）
}

var _ pipeline.Hook = (*SanitizerOutputHook)(nil)

func NewSanitizerOutputHook(s *Sanitizer) (*SanitizerOutputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer output hook: %w", ErrNilSanitizer)
	}
	return &SanitizerOutputHook{sanitizer: s}, nil
}

// NewSanitizerOutputHookWithSessionManager 创建带会话管理器的输出Hook
func NewSanitizerOutputHookWithSessionManager(s *Sanitizer, mgr *SessionSanitizeManager) (*SanitizerOutputHook, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitizer output hook: %w", ErrNilSanitizer)
	}
	return &SanitizerOutputHook{
		sanitizer:      s,
		sessionManager: mgr,
	}, nil
}

func (h *SanitizerOutputHook) Name() string { return "sanitizer.output" }

func (h *SanitizerOutputHook) Priority() int { return 50 } // 从190改为50，在OutputCompliance之前执行

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

	var sm SanitizeMap

	// 1. 优先从请求级Metadata读取（当前轮次）
	smRaw, ok := env.Metadata[MetadataKeySanitizeMap]
	if ok {
		sm, _ = smRaw.(SanitizeMap)
	}

	// 2. 【新增】从会话级Redis加载（跨轮次还原）
	if h.sessionManager != nil && (sm == nil || len(sm) == 0) && env.SessionID != "" && env.TenantID != "" {
		sessionSM, err := h.sessionManager.GetSanitizeMap(ctx, env.TenantID, env.SessionID)
		if err != nil {
			slog.WarnContext(ctx, "failed to load session sanitize map",
				"error", err,
				"session_id", env.SessionID,
				"tenant_id", env.TenantID,
			)
		} else {
			sm = sessionSM
		}
	}

	if len(sm) == 0 {
		return nil // 没有映射表，无需还原
	}

	// 3. 还原输出
	output := string(env.UpstreamResponse)
	result, err := h.sanitizer.RestoreOutput(ctx, output, sm)
	if err != nil {
		return fmt.Errorf("sanitizer output: %w", err)
	}

	env.UpstreamResponse = []byte(result)
	
	// 【新增】记录还原统计
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["sanitize_restored"] = true
	env.Metadata["sanitize_map_size"] = len(sm)

	return nil
}

func (h *SanitizerOutputHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}
