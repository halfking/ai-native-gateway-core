package sanitize

import (
	"context"
	"fmt"
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
}

// PatternDetector 基于正则模式匹配的检测器
type PatternDetector struct {
	patterns []patternEntry
}

// NewPatternDetector 创建包含默认 PII 模式的检测器
func NewPatternDetector() *PatternDetector {
	return &PatternDetector{
		patterns: []patternEntry{
			{TypePhone, regexp.MustCompile(`1[3-9]\d{9}`)},
			{TypeIDCard, regexp.MustCompile(`\d{17}[\dXx]`)},
			{TypeEmail, regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)},
			{TypeCreditCard, regexp.MustCompile(`\b(?:4\d{12,15}|5[1-5]\d{14}|3[47]\d{13}|6(?:011|5\d{2})\d{12})\b`)},
			{TypeSecret, regexp.MustCompile(`(sk|ak)-[a-zA-Z0-9]{16,}`)},
			{TypeInternalIP, regexp.MustCompile(`(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})`)},
		},
	}
}

func (d *PatternDetector) Name() string { return "pattern" }

func (d *PatternDetector) Detect(_ context.Context, text string) ([]SensitiveFragment, error) {
	if d == nil {
		return nil, nil
	}
	var fragments []SensitiveFragment
	seen := make(map[string]bool)

	for _, p := range d.patterns {
		matches := p.regex.FindAllStringIndex(text, -1)
		for _, m := range matches {
			value := text[m[0]:m[1]]
			key := fmt.Sprintf("%s:%d:%d", p.sType, m[0], m[1])
			if seen[key] {
				continue
			}
			seen[key] = true
			fragments = append(fragments, SensitiveFragment{
				Type:  p.sType,
				Value: value,
				Start: m[0],
				End:   m[1],
			})
		}
	}

	sort.Slice(fragments, func(i, j int) bool {
		return fragments[i].Start < fragments[j].Start
	})
	return fragments, nil
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
		fragments []SensitiveFragment
		err       error
	}

	ch := make(chan result, len(d.detectors))
	var wg sync.WaitGroup

	for _, det := range d.detectors {
		wg.Add(1)
		det := det
		go func() {
			defer wg.Done()
			fragments, err := det.Detect(ctx, text)
			ch <- result{fragments: fragments, err: err}
		}()
	}

	wg.Wait()
	close(ch)

	var all []SensitiveFragment
	for r := range ch {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.fragments...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Start < all[j].Start
	})
	return deduplicateFragments(all), nil
}

// deduplicateFragments 去重：按起始位置去重，保留先出现的类型
func deduplicateFragments(fragments []SensitiveFragment) []SensitiveFragment {
	if len(fragments) == 0 {
		return fragments
	}
	result := make([]SensitiveFragment, 0, len(fragments))
	for _, f := range fragments {
		if len(result) == 0 || f.Start != result[len(result)-1].Start {
			result = append(result, f)
		}
	}
	return result
}
