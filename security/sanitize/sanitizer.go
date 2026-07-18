package sanitize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

var (
	ErrNilSanitizer = errors.New("sanitize: nil sanitizer")
	ErrNilDetector  = errors.New("sanitize: nil detector")
	ErrNoFragments  = errors.New("sanitize: no sensitive fragments detected")
)

// SanitizeResult 脱敏处理结果
type SanitizeResult struct {
	SanitizedText string              `json:"sanitized_text"`
	SanitizeMap   SanitizeMap         `json:"-"`
	Fragments     []SensitiveFragment `json:"fragments,omitempty"`
}

// Sanitizer 智能脱敏还原器
type Sanitizer struct {
	detector Detector
	logger   *slog.Logger
}

var _ interface{ Name() string } = (*Sanitizer)(nil)

// NewSanitizer 创建脱敏还原器
func NewSanitizer(detector Detector) (*Sanitizer, error) {
	if detector == nil {
		return nil, ErrNilDetector
	}
	return &Sanitizer{
		detector: detector,
		logger:   slog.Default().With("component", "sanitize"),
	}, nil
}

func (s *Sanitizer) Name() string { return "sanitize" }

// SanitizeInput 对输入文本进行脱敏处理：
//  1. 检测敏感信息
//  2. 替换为 {SENSITIVE:type:index} 占位符
//  3. 返回脱敏文本 + 映射表
func (s *Sanitizer) SanitizeInput(ctx context.Context, text string) (*SanitizeResult, error) {
	if s == nil {
		return nil, ErrNilSanitizer
	}

	fragments, err := s.detector.Detect(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("sanitize: detect failed: %w", err)
	}
	if len(fragments) == 0 {
		return &SanitizeResult{
			SanitizedText: text,
			SanitizeMap:   make(SanitizeMap),
		}, nil
	}

	typeIndex := make(map[SensitiveType]int)
	sanitizeMap := make(SanitizeMap, len(fragments))

	var sb strings.Builder
	sb.Grow(len(text))
	lastEnd := 0

	for _, f := range fragments {
		if f.Start < lastEnd {
			continue
		}

		typeIndex[f.Type]++
		idx := typeIndex[f.Type]
		ph := Placeholder{Type: f.Type, Index: idx}
		placeholderStr := ph.String()

		sb.WriteString(text[lastEnd:f.Start])
		sb.WriteString(placeholderStr)

		sanitizeMap[placeholderStr] = f.Value
		lastEnd = f.End
	}

	sb.WriteString(text[lastEnd:])

	result := &SanitizeResult{
		SanitizedText: sb.String(),
		SanitizeMap:   sanitizeMap,
		Fragments:     fragments,
	}

	s.logger.DebugContext(ctx, "sanitize input",
		"fragments", len(fragments),
		"placeholder_count", len(sanitizeMap),
	)

	return result, nil
}

// RestoreOutput 将输出文本中的占位符还原为原始值。
//
// 只对完全匹配的占位符（{SENSITIVE:type:index}）进行还原。
// 如果占位符在映射表中不存在，保留原样（不猜测不报错）。
func (s *Sanitizer) RestoreOutput(_ context.Context, text string, sm SanitizeMap) (string, error) {
	if s == nil {
		return "", ErrNilSanitizer
	}
	if len(sm) == 0 {
		return text, nil
	}

	result := PlaceholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		if original, ok := sm[match]; ok {
			return original
		}
		return match
	})

	return result, nil
}

// RestoreOutputOrMask 还原输出，对无法还原的占位符做 mask 处理。
//
// 与 RestoreOutput 的区别：如果占位符在映射表中不存在，
// 会用 [REDACTED] 替换而非保留原占位符文本（用于面向最终用户的场景）。
func (s *Sanitizer) RestoreOutputOrMask(ctx context.Context, text string, sm SanitizeMap) (string, error) {
	if s == nil {
		return "", ErrNilSanitizer
	}
	if len(sm) == 0 {
		result := PlaceholderPattern.ReplaceAllString(text, "[REDACTED]")
		return result, nil
	}

	result := PlaceholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		if original, ok := sm[match]; ok {
			return original
		}
		s.logger.WarnContext(ctx, "sanitize: placeholder not found in map, masking",
			"placeholder", match,
		)
		return "[REDACTED]"
	})

	return result, nil
}

// NewNoopSanitizer 创建一个无操作的脱敏器（用于测试/降级）
func NewNoopSanitizer() *Sanitizer {
	return &Sanitizer{
		detector: &noopDetector{},
		logger:   slog.Default().With("component", "sanitize"),
	}
}

type noopDetector struct{}

func (d *noopDetector) Detect(_ context.Context, _ string) ([]SensitiveFragment, error) {
	return nil, nil
}

func (d *noopDetector) Name() string { return "noop" }
