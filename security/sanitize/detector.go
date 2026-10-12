package sanitize

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
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

// DetectionScope selects which rules a Detect call is allowed to match.
//
// The split exists because "should this byte be replaced before the request
// leaves the gateway" and "could this address be an exfiltration target" are
// different questions with different answers, and 2026-10-10 forced them
// apart. Address-shaped text (http_proxy=http://127.0.0.1:7890, deploy
// commands against 10.x, k8s service DNS) is ordinary operational context:
// masking it turns a correct, copy-pasteable command into a broken one, and
// the user-visible loss is paid on every request to gain nothing. Excluding
// addresses from redaction is therefore the default, and it is what
// ScopeRedact means.
//
// Public routable addresses appearing in model-generated tool arguments are a
// genuinely different risk: there the address is a *destination* the model
// chose, and blocking it is the only thing standing between a session and a
// data exfiltration path (audit 154 / r1008). ScopeEgress keeps that rule
// while ScopeRedact drops it, so widening the redaction scope never silently
// widens the egress surface, and narrowing it never silently disarms the
// guard.
type DetectionScope int

const (
	// ScopeRedact is the default scope: everything except address-shaped
	// rules. Used by input sanitization and output text redaction.
	ScopeRedact DetectionScope = iota
	// ScopeEgress additionally matches address rules. Used only by the
	// tool-argument egress guard.
	ScopeEgress
)

// scopedType reports whether a fragment type belongs to the address family
// that ScopeRedact excludes. Kept as a type check rather than a per-entry
// flag so a YAML-loaded rule (server_ip / server_ipv6 / internal_ip) is
// classified identically to the built-in fallback — the production detector
// and the fallback must not drift apart on this decision.
func scopedType(typ SensitiveType) bool {
	switch typ {
	case TypeServerIP, TypeInternalIP, typeServerIPv6:
		return true
	}
	return false
}

// typeServerIPv6 mirrors the `server_ipv6` YAML rule name. It is not a
// SensitiveType constant because an IPv6 literal can never allocate a
// placeholder (it is excluded from ScopeRedact), so no token is ever minted
// with this type — but the config loader still needs to name it.
const typeServerIPv6 SensitiveType = "server_ipv6"

// patternEntry 内部模式条目
type patternEntry struct {
	sType SensitiveType
	regex *regexp.Regexp
	group int // Optional submatch containing only the value, preserving labels.
	// vendorKeyShape marks the built-in sk-/ak- rule, whose pattern matches
	// a bare shape and whose whole-string qualification lives in
	// credibleSecretCandidate. Rules loaded from a YAML config own their
	// own regex semantics and are never subject to that filter — a config
	// author writing `KEY-[A-Z]{4}` means it literally.
	vendorKeyShape bool
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

// Detect runs the redaction scope: address-shaped rules are excluded.
func (d *PatternDetector) Detect(ctx context.Context, text string) ([]SensitiveFragment, error) {
	return d.DetectScope(ctx, text, ScopeRedact)
}

// DetectScope runs the detector under an explicit scope. ScopeRedact is what
// Detect does; ScopeEgress additionally matches address rules and is reserved
// for the tool-argument egress guard.
func (d *PatternDetector) DetectScope(_ context.Context, text string, scope DetectionScope) ([]SensitiveFragment, error) {
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
		if scope == ScopeRedact && scopedType(p.sType) {
			continue
		}
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
			if p.vendorKeyShape && !credibleSecretCandidate(value) {
				continue
			}
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

// vendorKeyShapeSource is the shape the built-in rule and the shipped
// `secret.api_key` rule share. Detection is by pattern text rather than by
// rule name so a deployment that names its own short-token rule `api_key`
// keeps literal semantics.
//
// The prefix set is the union of what each side used to cover: the built-in
// fallback had `sk|ak`, the shipped YAML had `sk|key|token|api|bearer`.
// Narrowing either one would silently drop keys the other already caught.
const vendorKeyShapeSource = `\b(?:sk|ak|key|token|api|bearer)[-_]?[A-Za-z0-9]+(?:[-_][A-Za-z0-9]+)*\b`

func isVendorKeyShapeRegex(pattern string) bool {
	return pattern == vendorKeyShapeSource
}

// minSecretBodyLen is the floor the pre-244 rule expressed as `{16,}` on the
// key body. It is preserved here because the pattern can no longer carry it.
const minSecretBodyLen = 16

// credibleSecretCandidate filters the shape-only secret match down to things
// that look like an actual key.
//
// The secret pattern deliberately matches a bare `sk-`/`ak-` prefix plus
// hyphen-joined alphanumerics, because the infix hyphens are what real
// vendor keys carry (audit 244 F2). On its own that shape also matches
// ordinary hyphenated English, so two whole-string rules are applied here —
// both are properties of the entire candidate, which RE2 cannot express
// without lookahead assertions that Go's regexp rejects:
//
//  1. the body must be at least minSecretBodyLen characters, which keeps
//     `sk-test` / `ak-short` / `sk-or-v1` out;
//  2. the body must contain a digit, which keeps pure-alphabetic prose
//     ("api-design-patterns", "bearer-of-responsibility",
//     "token-bucket-algorithm") out. Real vendor keys mix letters and
//     digits; those three phrases do not.
//
// Both rules deliberately mirror what the old `{16,}` pattern already
// implied for the non-hyphenated case, so nothing that used to be detected
// stops being detected.
func credibleSecretCandidate(value string) bool {
	body := value
	if i := strings.IndexByte(body, '-'); i >= 0 {
		body = body[i+1:]
	}
	if len(body) < minSecretBodyLen {
		return false
	}
	return strings.ContainsFunc(body, func(r rune) bool { return r >= '0' && r <= '9' })
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
		// 2026-10-10: audit 244 F2 fix — the key body now admits infix
		// hyphens, so the real vendor formats (sk-proj-…, sk-ant-api03-…,
		// sk-or-v1-…) match instead of stopping at the first hyphen. The
		// old `[a-zA-Z0-9]{16,}` class made every hyphen-bearing key a
		// silent miss, forwarding the plaintext key upstream and persisting
		// it in request_logs_bodies.
		//
		// Shape only. The 16-character floor and the "contains a digit"
		// rule are whole-string properties that RE2 cannot express without
		// a lookahead assertion (which Go's regexp rejects outright), so
		// they live in credibleSecretCandidate below. Keeping them in Go
		// rather than in the pattern is also what lets the same rule serve
		// the YAML-loaded production detector.
		{sType: TypeSecret, regex: regexp.MustCompile(vendorKeyShapeSource), vendorKeyShape: true},
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
