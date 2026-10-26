package sanitize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/metrics"
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
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start == ordered[j].Start {
			return ordered[i].End > ordered[j].End
		}
		return ordered[i].Start < ordered[j].Start
	})

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
	return s.sanitizeInputStable(ctx, text, offset, nil)
}

// existing is request-local and was loaded under the session allocation lease.
// Reusing the oldest token for the same type/value keeps replay fingerprints
// stable without exposing a plaintext reverse index to another cache tier.
func (s *Sanitizer) sanitizeInputStable(ctx context.Context, text string, offset map[SensitiveType]int, existing sanitizeReuseIndex) (*SanitizeResult, error) {
	if s == nil {
		return nil, ErrNilSanitizer
	}

	var fragments []SensitiveFragment
	var err error
	if scoped, ok := s.detector.(interface {
		DetectScope(context.Context, string, DetectionScope) ([]SensitiveFragment, error)
	}); ok {
		fragments, err = scoped.DetectScope(ctx, text, ScopeInputDeclarations)
	} else {
		fragments, err = s.detector.Detect(ctx, text)
	}
	if err != nil {
		return nil, fmt.Errorf("sanitize: detect failed: %w", err)
	}
	if len(fragments) == 0 {
		return &SanitizeResult{
			SanitizedText: text,
			SanitizeMap:   make(SanitizeMap),
		}, nil
	}

	nextOffset := make(map[SensitiveType]int, len(offset))
	for typ, index := range offset {
		nextOffset[typ] = index
	}
	// Literal markers in client text must not collide with newly allocated
	// tokens, even when there was no prior map.
	for _, token := range PlaceholderPattern.FindAllString(text, -1) {
		if p, ok := ParsePlaceholder(token); ok && p.Index > nextOffset[p.Type] {
			nextOffset[p.Type] = p.Index
		}
	}
	reserveMutatedPlaceholders(text, nextOffset)
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
		placeholderStr, allocErr := allocateSensitivePlaceholder(sp.typ, text[sp.start:sp.end], nextOffset, existing)
		if allocErr != nil {
			return nil, allocErr
		}

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

type sensitiveValue struct {
	typ   SensitiveType
	value string
}
type sanitizeReuseIndex map[sensitiveValue]string

func allocateSensitivePlaceholder(typ SensitiveType, value string, offset map[SensitiveType]int, reuse sanitizeReuseIndex) (string, error) {
	key := sensitiveValue{typ, value}
	if token, ok := reuse[key]; ok {
		return token, nil
	}
	if offset[typ] < 0 || offset[typ] == math.MaxInt {
		return "", errors.New("sanitize: placeholder index exhausted")
	}
	offset[typ]++
	token := (Placeholder{Type: typ, Index: offset[typ]}).String()
	if reuse != nil {
		reuse[key] = token
	}
	return token, nil
}

// restoreText 是两级还原的共同实现：精确阶段与容错阶段都走同一个 resolver。
//
// 两级共用 resolver 是关键，不是简化。精确阶段的 token 先被解析成
// (type, index) 再查表，而不是拿整串 token 直接当 key——否则一个补零的
// "{SENSITIVE:secret:01}" 在精确阶段查不到、会被立刻 mask，容错阶段就再也
// 看不到它了。流式 tool arguments lane 需要把还原值按 JSON 字符串字面量重新
// 编码，而容错阶段解析出的 (type, index) 正好就是 resolver 的入参。
func (s *Sanitizer) restoreText(ctx context.Context, text string, resolve placeholderResolver, policy looseUnknownPolicy) string {
	foreign := foreignMarkersFromContext(ctx)
	result := s.restoreExactMarkers(ctx, text, resolve, foreign, policy)

	stats := restoreLooseMarkers(ctx, result, resolve, foreign, policy)
	if stats.recovered > 0 {
		metrics.SanitizePlaceholderMutationTotal.WithLabelValues("recovered").Add(float64(stats.recovered))
		// Debug, not Info: the streaming path calls this once per delta, and
		// the metric is the aggregate signal an operator actually graphs.
		s.logger.DebugContext(ctx, "sanitize: restored re-spelled placeholder", "restored", stats.recovered)
	}
	if stats.unresolved > 0 && policy == looseMaskUnknown {
		metrics.SanitizePlaceholderMutationTotal.WithLabelValues("unresolved").Add(float64(stats.unresolved))
		s.logger.WarnContext(ctx, "sanitize: re-spelled placeholder not found in map, masking",
			"unresolved", stats.unresolved)
	}
	return stats.text
}

// restoreExactMarkers 是**精确阶段**（规范拼写 `{SENSITIVE:type:index}`）的实现。
//
// 2026-10-20 补（r1039）：过去这里是 ReplaceAllStringFunc，回调里**拿不到偏移**，
// 于是「模型多写的一对花括号」无法处理——"{{SENSITIVE:secret:1}}" 里的内层
// 规范拼写被本阶段消费掉，外层那一对花括号留在 span 之外，客户端看到的是
// "{s3cr3t}"。
//
// 为什么不交给容错阶段：容错阶段（restoreLooseMarkers）**已经**会吸收这一对
// 花括号（见 placeholder_loose.go 的 absorbMarkerDelimiters），但规范拼写
// 由**本阶段先行消费**，容错阶段根本看不到那个标记 ⇒ 两阶段的顺序决定了
// 这个残渣必然出现。改成 FindAllStringIndex + 手工拼接，才能在**同一处**
// 把属于标记自己的定界花括号吸进来。
//
// 吸收条件与容错阶段**逐条一致**（widenExactMarkerSpan）：
//   - **两侧**都多出一对同类的开/闭花括号（模型把花括号写重了）；
//   - 吸收后**核心**（去掉全部花括号后的 marker 文本）不变；
//   - 左边那一对花括号必须还没被前面的替换消费掉（floor）。
//
// ⇒ 普通 JSON 嵌套（`{"a":{"SENSITIVE:secret:1}}`，左侧紧邻的是冒号而不是
// 开花括号）不受影响；单侧多一个花括号也不吸收（保持左右对称）。
func (s *Sanitizer) restoreExactMarkers(ctx context.Context, text string, resolve placeholderResolver, foreign foreignMarkerSet, policy looseUnknownPolicy) string {
	matches := PlaceholderPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range matches {
		matchStart, matchEnd := m[0], m[1]
		match := text[matchStart:matchEnd]

		// A token observed in the inbound body belongs to the downstream
		// hop even when this gateway can also see the same session mapping
		// (for example, when both instances share Redis). Keep it intact so
		// only the issuer restores it at the end of the chain.
		//
		// foreign 判定用的是**内层规范拼写**（match）：token 的身份是那串规范
		// 文本，多出来的那对花括号不是它的一部分。外来 token **不吸收**花括号
		// ——本轮不改动下游 hop 负责的那段文本。
		if foreign.isForeign(match) {
			b.WriteString(text[last:matchEnd])
			last = matchEnd
			continue
		}

		// 属于标记自己的定界花括号（模型写重了）要一并吸进来。
		spanStart, spanEnd := widenExactMarkerSpan(text, matchStart, matchEnd, last)

		var replacement string
		var parsedType SensitiveType
		resolved := false
		if parsed, ok := ParsePlaceholder(match); ok {
			parsedType = parsed.Type
			if resolve != nil {
				// hit 与 original 分开判断：还原值本身**可以是空串**，
				// 用 replacement=="" 当「没解析出来」的哨兵会把空串误判成未命中。
				original, hit := resolve(parsed.Type, parsed.Index)
				if hit {
					replacement = original
					resolved = true
				}
			}
			if !resolved && resolve != nil {
				s.logger.WarnContext(ctx, "sanitize: placeholder not found in map",
					"placeholder", match)
			}
		}
		if !resolved {
			if policy == looseMaskUnknown {
				// The marker parsed, so its type is known and the mask can say so.
				// A non-parsing match leaves parsedType empty and falls back to the
				// bare form rather than guessing.
				//
				// maskType 把「模型自造的类型」收敛成 unknown（与容错阶段一致）：
				// 网关没有这个类型的规则，声称一个不存在的类别是错的。
				replacement = RedactionMarker(maskType(parsedType))
			} else {
				replacement = match
			}
		}

		b.WriteString(text[last:spanStart])
		b.WriteString(replacement)
		last = spanEnd
	}
	b.WriteString(text[last:])
	return b.String()
}

// widenExactMarkerSpan 把**属于标记自己的**定界花括号并进 span。
//
// 与 placeholder_loose.go 的 absorbMarkerDelimiters 是同一条规则，这里是它在
// 精确阶段（brace-delimited 形态）的对应实现：只吸收**两侧**都多出来的那一对，
// 且吸收后核心不变。
//
// floor 是已经被上一次替换消费掉的文本末尾：左边那一对花括号若已经不在
// 未消费的文本里（被前一个标记吃掉了），就不能再吸收。
func widenExactMarkerSpan(text string, s, e, floor int) (int, int) {
	coreS, coreE := markerCore(text, s, e)
	if coreS >= coreE {
		return s, e
	}
	// 左侧：紧邻 span 之前的那个字符必须是开花括号，且还没被消费掉。
	lr, lstart, lok := runeBeforeStart(text, s)
	if !lok || lstart < floor || !isOpenBraceRune(lr) {
		return s, e
	}
	// 右侧：紧邻 span 之后的那个字符必须是闭花括号。
	rr, rend, rok := runeAtSpan(text, e)
	if !rok || !isCloseBraceRune(rr) {
		return s, e
	}
	// 吸收后核心必须不变，否则吸进来的就不是这个标记的花括号了。
	ns, ne := lstart, rend
	cs, ce := markerCore(text, ns, ne)
	if cs != coreS || ce != coreE {
		return s, e
	}
	return ns, ne
}

// RestoreOutput 将输出文本中的占位符还原为原始值。
//
// 还原两遍：先是完全匹配的 {SENSITIVE:type:index}，再是被模型改写过的
// 变体（占位符文法见 placeholder_loose.go）。如果占位符在映射表中不存在，
// 保留原样（不猜测不报错）。
func (s *Sanitizer) RestoreOutput(ctx context.Context, text string, sm SanitizeMap) (string, error) {
	if s == nil {
		return "", ErrNilSanitizer
	}
	if len(sm) == 0 {
		return text, nil
	}
	return s.restoreText(ctx, text, sm.resolverFor(), looseKeepUnknown), nil
}

// RestoreOutputOrMask 还原输出，对无法还原的占位符做 mask 处理。
//
// 与 RestoreOutput 的区别：如果占位符在映射表中不存在，
// 会用 [REDACTED] 替换而非保留原占位符文本（用于面向最终用户的场景）。
// 该口径同样适用于被改写过的变体：客户端可见文本里不留任何内部标记。
//
// 网关套网关时有一条例外（见 hop.go）：请求里确实带着、由下游同款网关签发的
// 占位符原样放行，由持有映射的那一跳去还原。判据是"入站观察到过"，不是
// "本实例还原不了"——伪造的标记没入站过，仍然照常 mask。
func (s *Sanitizer) RestoreOutputOrMask(ctx context.Context, text string, sm SanitizeMap) (string, error) {
	if s == nil {
		return "", ErrNilSanitizer
	}
	if len(sm) == 0 {
		return s.restoreText(ctx, text, nil, looseMaskUnknown), nil
	}
	return s.restoreText(ctx, text, sm.resolverFor(), looseMaskUnknown), nil
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
