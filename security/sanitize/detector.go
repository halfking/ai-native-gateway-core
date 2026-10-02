package sanitize

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"sync"
)

// SensitiveFragment 检测到的敏感信息片段
type SensitiveFragment struct {
	Type  SensitiveType
	Value string // 原始敏感值
	Start int    // 在输入文本中的起始位置
	End   int    // 在输入文本中的结束位置
}

// Detector 敏感信息检测器接口
type Detector interface {
	Detect(ctx context.Context, text string) ([]SensitiveFragment, error)
	Name() string
}

// patternEntry 内部模式条目
type patternEntry struct {
	sType SensitiveType
	regex *regexp.Regexp
	group int // Optional submatch containing only the value, preserving labels.
}

// PatternDetector 基于正则模式匹配的检测器
type PatternDetector struct {
	mu         sync.RWMutex
	patterns   []patternEntry
	configPath string
}

// NewPatternDetector 创建包含默认 PII 模式的检测器
func NewPatternDetector() *PatternDetector {
	return &PatternDetector{
		patterns: defaultPatternEntries(),
	}
}

func (d *PatternDetector) Name() string { return "pattern" }

func (d *PatternDetector) Detect(_ context.Context, text string) ([]SensitiveFragment, error) {
	if d == nil {
		return nil, nil
	}
	d.mu.RLock()
	patterns := append([]patternEntry(nil), d.patterns...)
	d.mu.RUnlock()
	var fragments []SensitiveFragment
	seen := make(map[string]bool)
	reserved := PlaceholderPattern.FindAllStringIndex(text, -1)

	for _, p := range patterns {
		matches := p.regex.FindAllStringSubmatchIndex(text, -1)
		for _, m := range matches {
			start, end := m[2*p.group], m[2*p.group+1]
			if p.group > 0 && end-start >= 2 && ((text[start] == '"' && text[end-1] == '"') || (text[start] == '\'' && text[end-1] == '\'')) {
				start++
				end--
			}
			if start < 0 || start >= end {
				continue
			}

			value := text[start:end]
			typ := p.sType
			if typ == TypeServerIP || typ == TypeInternalIP {
				address, err := netip.ParseAddr(value)
				if err != nil {
					continue
				}
				if address.IsPrivate() {
					typ = TypeInternalIP
				}
			}
			// Exempt only marker bytes, never the surrounding secret/private key.
			for _, span := range outsideReservedSpans(start, end, reserved) {
				key := fmt.Sprintf("%s:%d:%d", typ, span[0], span[1])
				if seen[key] {
					continue
				}
				seen[key] = true
				fragments = append(fragments, SensitiveFragment{Type: typ, Value: text[span[0]:span[1]], Start: span[0], End: span[1]})
			}
		}
	}

	sort.Slice(fragments, func(i, j int) bool {
		return fragments[i].Start < fragments[j].Start
	})
	return fragments, nil
}

func outsideReservedSpans(start, end int, reserved [][]int) [][2]int {
	var spans [][2]int
	for _, marker := range reserved {
		if marker[1] <= start {
			continue
		}
		if marker[0] >= end {
			break
		}
		if marker[0] > start {
			spans = append(spans, [2]int{start, marker[0]})
		}
		if marker[1] > start {
			start = marker[1]
		}
		if start >= end {
			return spans
		}
	}
	if start < end {
		spans = append(spans, [2]int{start, end})
	}
	return spans
}

func defaultPatternEntries() []patternEntry {
	return append([]patternEntry{
		{sType: TypePhone, regex: regexp.MustCompile(`1[3-9]\d{9}`)},
		{sType: TypeIDCard, regex: regexp.MustCompile(`\d{17}[\dXx]`)},
		{sType: TypeEmail, regex: regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)},
		{sType: TypeCreditCard, regex: regexp.MustCompile(`\b(?:4\d{12,15}|5[1-5]\d{14}|3[47]\d{13}|6(?:011|5\d{2})\d{12})\b`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`(sk|ak)-[a-zA-Z0-9]{16,}`)},
	}, operationalPatternEntries()...)
}

// CustomDetector 用户自定义检测器
type CustomDetector struct {
	name     string
	patterns []struct {
		sType SensitiveType
		regex *regexp.Regexp
	}
}

func NewCustomDetector(name string) *CustomDetector {
	return &CustomDetector{name: name}
}

func (d *CustomDetector) Name() string { return d.name }

func (d *CustomDetector) AddPattern(sType SensitiveType, pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("custom pattern compile failed: %w (type=%s, pattern=%s)", err, sType, pattern)
	}
	d.patterns = append(d.patterns, struct {
		sType SensitiveType
		regex *regexp.Regexp
	}{sType: sType, regex: re})
	return nil
}

func (d *CustomDetector) Detect(_ context.Context, text string) ([]SensitiveFragment, error) {
	if d == nil {
		return nil, nil
	}
	var fragments []SensitiveFragment
	for _, p := range d.patterns {
		matches := p.regex.FindAllStringIndex(text, -1)
		for _, m := range matches {
			fragments = append(fragments, SensitiveFragment{
				Type:  p.sType,
				Value: text[m[0]:m[1]],
				Start: m[0],
				End:   m[1],
			})
		}
	}
	return fragments, nil
}

// CompositeDetector 组合多个检测器
type CompositeDetector struct {
	detectors []Detector
}

func NewCompositeDetector(detectors ...Detector) *CompositeDetector {
	return &CompositeDetector{detectors: detectors}
}

func (d *CompositeDetector) Name() string { return "composite" }

func (d *CompositeDetector) AddDetector(det Detector) {
	d.detectors = append(d.detectors, det)
}

func (d *CompositeDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	if d == nil {
		return nil, nil
	}

	type result struct {
		index     int
		fragments []SensitiveFragment
		err       error
	}

	ch := make(chan result, len(d.detectors))
	var wg sync.WaitGroup

	for index, det := range d.detectors {
		wg.Add(1)
		det := det
		go func() {
			defer wg.Done()
			fragments, err := det.Detect(ctx, text)
			ch <- result{index: index, fragments: fragments, err: err}
		}()
	}

	wg.Wait()
	close(ch)

	results := make([][]SensitiveFragment, len(d.detectors))
	for r := range ch {
		if r.err != nil {
			return nil, r.err
		}
		results[r.index] = r.fragments
	}
	var all []SensitiveFragment
	for _, fragments := range results {
		all = append(all, fragments...)
	}

	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Start < all[j].Start
	})
	return all, nil // Preserve overlaps for the sanitizer's union merge.
}
