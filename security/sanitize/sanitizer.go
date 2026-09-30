package sanitize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

// sensitiveSpan 是一个互不重叠、且已夹在 [0, textLen] 内的敏感区间。
type sensitiveSpan struct {
	start, end int
	typ        SensitiveType
}

// mergeFragmentSpans 把检测器返回的片段整理成有序、不重叠、并落在
// textLen 内的区间列表。
//
// 三件事在这里完成，任何一件放到调用方都太晚了：
//  1. **丢弃越界/空片段**：Detector 是可插拔接口，第三方实现给出
//     越界的 Start/End 会让下标切片 panic，把一次误报变成 500。
//  2. **排序**：Detector 不保证有序（CustomDetector 就不排序）。
//  3. **合并重叠**：同一起点的多个命中、以及互相嵌套/交叉的命中会
//     合成一个并集。合并用稳定排序，因此检测器给出的先后顺序决定
//     类型归属——起点相同时取跨度更大的那个类型（更具体的那类）。
//
// 合并只影响「发往上游的文本里哪些字节被占位符覆盖」；占位符→原文的
// 往返仍然精确，因为并集区间对应的就是原文的连续片段。
func mergeFragmentSpans(fragments []SensitiveFragment, textLen int) []sensitiveSpan {
	if len(fragments) == 0 {
		return nil
	}
	ordered := append([]SensitiveFragment(nil), fragments...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	spans := make([]sensitiveSpan, 0, len(ordered))
	for _, f := range ordered {
		start, end := f.Start, f.End
		if start < 0 {
			start = 0
		}
		if end > textLen {
			end = textLen
		}
		if start >= end {
			// 空片段或完全越界：无可脱敏内容。
			continue
		}
		if n := len(spans); n > 0 {
			last := &spans[n-1]
			if start < last.end {
				// 与前一个区间重叠：并入，并把区间撑到两者的并集。
				if end > last.end {
					last.end = end
				}
				if start == last.start && end > start && f.Type != "" {
					// 同一段文字命中多条规则时保留更具体的类型。
					last.typ = f.Type
				}
				continue
			}
		}
		spans = append(spans, sensitiveSpan{start: start, end: end, typ: f.Type})
	}
	return spans
}

// SanitizeInput 对输入文本进行脱敏处理：
//  1. 检测敏感信息
//  2. 替换为 {SENSITIVE:type:index} 占位符
//  3. 返回脱敏文本 + 映射表
//
// typeIndex 每轮局部自增，跨轮次会撞号（不同轮次都有 phone:1），
// 多轮会话应使用 SanitizeInputWithOffset。
func (s *Sanitizer) SanitizeInput(ctx context.Context, text string) (*SanitizeResult, error) {
	return s.sanitizeInput(ctx, text, nil)
}

// SanitizeInputWithOffset 与会话级偏移量配合使用，避免跨轮次占位符撞号。
//
// offset 为每类敏感信息从几开始编号（由调用方从 Redis 查询现有计数），
// 例如上一轮已用了 phone:1~phone:3，则 offset 传 map[TypePhone]4。
// offset 为 nil 或空时等效于 SanitizeInput。
func (s *Sanitizer) SanitizeInputWithOffset(ctx context.Context, text string, offset map[SensitiveType]int) (*SanitizeResult, error) {
	return s.sanitizeInput(ctx, text, offset)
}

// sanitizeInput 内部实现
func (s *Sanitizer) sanitizeInput(ctx context.Context, text string, offset map[SensitiveType]int) (*SanitizeResult, error) {
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

	// 检测器之间会互相重叠（18 位身份证的前 11 位同时命中手机号正则），
	// 而「丢掉被覆盖的片段」会让它超出前一片段的那一段**以明文留在
	// 脱敏结果里**并发往上游。因此先把重叠片段并成不交错的并集区间，
	// 每个并集只生成一个占位符。
	spans := mergeFragmentSpans(fragments, len(text))

	var sb strings.Builder
	sb.Grow(len(text))
	lastEnd := 0

	for _, sp := range spans {
		typeIndex[sp.typ]++
		idx := typeIndex[sp.typ]
		// 叠加会话级偏移量，确保跨轮次不撞号
		if offset != nil {
			if base, ok := offset[sp.typ]; ok {
				idx += base
			}
		}
		ph := Placeholder{Type: sp.typ, Index: idx}
		placeholderStr := ph.String()

		sb.WriteString(text[lastEnd:sp.start])
		sb.WriteString(placeholderStr)

		sanitizeMap[placeholderStr] = text[sp.start:sp.end]
		lastEnd = sp.end
	}

	sb.WriteString(text[lastEnd:])

	merged := make([]SensitiveFragment, 0, len(spans))
	for _, sp := range spans {
		merged = append(merged, SensitiveFragment{
			Type:  sp.typ,
			Value: text[sp.start:sp.end],
			Start: sp.start,
			End:   sp.end,
		})
	}

	result := &SanitizeResult{
		SanitizedText: sb.String(),
		SanitizeMap:   sanitizeMap,
		Fragments:     merged,
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
