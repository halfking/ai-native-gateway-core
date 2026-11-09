package sanitize

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
)

const maxHistoryPolicyBytes = 64 << 10

type historyPolicyRule struct {
	Pattern string
	Fields  []string
	Block   bool
}
type historyMatch struct {
	selector, pattern           *regexp.Regexp
	selectorTerms, patternTerms [][]byte
	block                       bool
}
type historyPolicy struct {
	matches []historyMatch
	err     error
}

// HistoryGuard screens complete decoded sources before extraction can remove a
// credential's label or multiline boundary. Field selectors keep catch-all
// credential rules local to their JSON keys. Tool constraints are conservative:
// an old tool result can carry an ID without the earlier tool name.
// Compiled policies have a byte cap and an eight-entry cache per detector.
func (d *PatternDetector) HistoryGuard(ctx context.Context) func(context.Context, func() io.Reader) (bool, error) {
	if d == nil {
		return historyPolicy{err: ErrNilDetector}.guard
	}
	var rules []historyPolicyRule
	size := 0
	add := func(re *regexp.Regexp, typ SensitiveType, action string, fields []string) {
		if re == nil || scopedType(typ) || action == "log" || size > maxHistoryPolicyBytes {
			return
		}
		size += len(re.String())
		for _, field := range fields {
			size += len(field) + 4
		}
		if size > maxHistoryPolicyBytes {
			return
		}
		rules = append(rules, historyPolicyRule{re.String(), fields, action == "block"})
	}
	if manager := d.RuleManager(); manager != nil {
		for _, rule := range manager.SnapshotFor(ctx).Rules {
			if rule.Enabled && slices.Contains(rule.Scopes, "input") && (rule.TenantID == "" || rule.TenantID == authenticatedTenant(ctx)) {
				add(rule.Regex, SensitiveType(rule.Type), rule.Action, rule.Fields)
			}
		}
	} else {
		d.mu.RLock()
		for _, pattern := range d.patterns {
			if pattern.appliesScope(ScopeInputDeclarations) {
				add(pattern.regex, pattern.sType, pattern.action, nil)
			}
		}
		d.mu.RUnlock()
	}
	if size > maxHistoryPolicyBytes {
		return historyPolicy{err: errors.New("history policy exceeds bounded compilation capacity")}.guard
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return historyPolicy{err: err}.guard
	}
	key := sha256.Sum256(encoded)
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if policy, ok := d.historyPolicies[key]; ok {
		return policy.guard
	}
	policy := compileHistoryPolicy(rules)
	if len(d.historyPolicies) >= 8 {
		clear(d.historyPolicies)
	}
	if d.historyPolicies == nil {
		d.historyPolicies = make(map[[32]byte]historyPolicy)
	}
	d.historyPolicies[key] = policy
	return policy.guard
}

func compileHistoryPolicy(rules []historyPolicyRule) historyPolicy {
	type group struct {
		fields   []string
		block    bool
		patterns []string
	}
	groups := make(map[string]*group)
	for _, rule := range rules {
		fields := slices.Clone(rule.Fields)
		slices.Sort(fields)
		key := strings.Join(fields, "\x00")
		if rule.Block {
			key += "\x01"
		}
		g := groups[key]
		if g == nil {
			g = &group{fields: fields, block: rule.Block}
			groups[key] = g
		}
		pattern, err := historyPatternSuperset(rule.Pattern)
		if err != nil {
			return historyPolicy{err: err}
		}
		g.patterns = append(g.patterns, "(?:"+pattern+")")
	}
	var policy historyPolicy
	for _, g := range groups {
		match := historyMatch{block: g.block}
		var err error
		match.pattern, err = regexp.Compile(strings.Join(g.patterns, "|"))
		if err != nil {
			return historyPolicy{err: err}
		}
		parsed, err := syntax.Parse(match.pattern.String(), syntax.Perl)
		if err != nil {
			return historyPolicy{err: err}
		}
		match.patternTerms = historyRequiredTerms(parsed)
		if len(g.fields) > 0 {
			names := make([]string, len(g.fields))
			for i, field := range g.fields {
				names[i] = regexp.QuoteMeta(field)
			}
			match.selector, err = regexp.Compile(`(?i)"(?:` + strings.Join(names, "|") + `)"\s*:`)
			if err != nil {
				return historyPolicy{err: err}
			}
			parsed, err := syntax.Parse(match.selector.String(), syntax.Perl)
			if err != nil {
				return historyPolicy{err: err}
			}
			match.selectorTerms = historyRequiredTerms(parsed)
		}
		policy.matches = append(policy.matches, match)
	}
	// A mask must not hide a later input block.
	slices.SortStableFunc(policy.matches, func(a, b historyMatch) int {
		if a.block == b.block {
			return 0
		}
		if a.block {
			return -1
		}
		return 1
	})
	return policy
}

// Input rules normally inspect individual text leaves. The history stream
// includes their JSON envelope, so keep a superset by dropping text/line anchors
// in its private copy. Literal carets/dollars and character classes remain exact.
func historyPatternSuperset(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	var widen func(*syntax.Regexp)
	widen = func(node *syntax.Regexp) {
		switch node.Op {
		case syntax.OpBeginText, syntax.OpEndText, syntax.OpBeginLine, syntax.OpEndLine:
			node.Op = syntax.OpEmptyMatch
		}
		for _, child := range node.Sub {
			widen(child)
		}
	}
	widen(re)
	return re.String(), nil
}

func (p historyPolicy) guard(ctx context.Context, open func() io.Reader) (bool, error) {
	if err := errors.Join(p.err, ctx.Err()); err != nil {
		return false, err
	}
	match := func(re *regexp.Regexp, terms [][]byte) (bool, error) {
		possible, err := historyMayMatch(ctx, open, terms)
		if err != nil || !possible {
			return false, err
		}
		r := &historyGuardReader{ctx: ctx, reader: open()}
		matched := re.MatchReader(bufio.NewReaderSize(r, 32<<10))
		return matched, errors.Join(r.err, ctx.Err())
	}
	for _, rule := range p.matches {
		if rule.selector != nil {
			found, err := match(rule.selector, rule.selectorTerms)
			if err != nil {
				return false, err
			}
			if !found {
				continue
			}
		}
		found, err := match(rule.pattern, rule.patternTerms)
		if err != nil {
			return false, err
		}
		if found {
			if rule.block {
				return false, ErrRuleBlocked
			}
			return false, nil
		}
	}
	return true, nil
}

// regexp treats reader errors as EOF, so retain them and fail closed afterward.
type historyGuardReader struct {
	ctx    context.Context
	reader io.Reader
	err    error
}

func (r *historyGuardReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}
