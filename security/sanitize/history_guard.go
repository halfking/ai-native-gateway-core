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

// historyPolicyVersion 进缓存键：复核逻辑变了，旧的判决（哪怕来自共享层）必须失效。
const historyPolicyVersion = 2

// historyVerifyMaxBytes 是复核时读进内存的上限。超过它的消息不复核、直接判不安全
// （与修复前一致——保守）。≤64KB 的消息本来就是读一次进内存的，实际几乎不会触到。
const historyVerifyMaxBytes = 1 << 20

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

// historyPolicy 是历史守卫的判定机：两段式。
//
//   - 快路径（matches）：预筛词项 + 按 (字段选择器, 是否 block) 分组的放宽正则，
//     流式匹配、不把消息读进内存。任一组命中只说明「可能」——正则本身有已知的
//     宽松（「tokens」「keyboard」都匹配密钥形状规则），输入面靠候选过滤链兜住。
//   - 慢路径（detect）：快路径说可能命中之后，把已解码的文本交给输入面**同一份**
//     检测主体（PatternDetector.detectPatterns）复核；过滤链之后仍成立的命中才算。
//
// ★ 2026-11-23 修复：此前只有快路径——正则命中即整条省略。实测（仓内规则）：
// 「The request used 1734 tokens.」「the keyboard shortcut…」「token count exceeded
// the limit」「run with Bearer authentication」全被判不安全，而输入面对它们毫无
// 命中。211 真实会话 3953 条里 2269 条因此被丢（agent 会话里 token 一词无处不在）。
type historyPolicy struct {
	matches []historyMatch
	// entries 是参与复核的规则（无字段选择器的那些），正则已放宽锚点；entryTerms 与
	// entries 同序，是该规则正则**必含**的字面量（任一出现才可能命中；空 ⇒ 不确定）
	// ——复核前据此跳过不可能命中的规则。blocks 是 action=block 的规则 id。
	entries    []patternEntry
	entryTerms [][][]byte
	blocks     map[string]bool
	// detect 为 nil（直接用 compileHistoryPolicy 构造的策略）⇒ 不复核，行为与修复前相同。
	detect func(ctx context.Context, text string, entries []patternEntry) (block, sensitive bool, err error)
	err    error
}

// HistoryGuard screens complete decoded sources before extraction can remove a
// credential's label or multiline boundary. Field selectors keep catch-all
// credential rules local to their JSON keys. Tools constraints are conservative:
// an old tool result can carry an ID without the earlier tool name.
//
// 规则范围 = 作用于输入声明面、非纯记录（log）、非地址族、非 review_combination 的
// 规则——与修复前相同。变的是「命中」的判据：以输入面的候选过滤链为准。
func (d *PatternDetector) HistoryGuard(ctx context.Context) func(context.Context, func() io.Reader) (bool, error) {
	if d == nil {
		return historyPolicy{err: ErrNilDetector}.guard
	}
	var (
		rules      []historyPolicyRule
		entries    []patternEntry
		entryTerms [][][]byte
		blocks     = map[string]bool{}
		size       = 0
	)
	add := func(e patternEntry, fields []string, action string) error {
		if e.regex == nil {
			return nil
		}
		widened, err := historyPatternSuperset(e.regex.String())
		if err != nil {
			return err
		}
		re, err := regexp.Compile(widened)
		if err != nil {
			return err
		}
		size += len(re.String())
		for _, field := range fields {
			size += len(field) + 4
		}
		if size > maxHistoryPolicyBytes {
			return errors.New("history policy exceeds bounded compilation capacity")
		}
		e.regex = re
		rules = append(rules, historyPolicyRule{e.regex.String(), fields, action == "block"})
		var terms [][]byte
		if len(fields) == 0 {
			// 复核前逐条预筛用的必含字面量（与快路径分组用的同一个函数）。
			parsed, err := syntax.Parse(widened, syntax.Perl)
			if err != nil {
				return err
			}
			terms = historyRequiredTerms(parsed)

			// 字段规则（选择器 + 任意值）不进复核：输入面对它们是按「字段上下文 +
			// 叶子值」逐个判定的，整条消息的解码文本复现不了这个上下文 ⇒ 它们保持
			// 修复前的行为（键名出现即省略）。造成大面积误伤的是无字段的泛匹配规则。
			entries = append(entries, e)
			entryTerms = append(entryTerms, terms)
		}
		if action == "block" {
			blocks[e.id] = true
		}
		return nil
	}
	// 规则范围与修复前相同：作用于输入声明面、非地址族、非纯记录。review_combination
	// 只在输入面记台账、不产生片段，进复核只会白跑，一并排除。
	//
	// ★ 范围判断用**规则自己的 scopes**：托管规则在这里现建的 patternEntry 不带
	// scopes 字段，patternEntry.appliesScope 对空 scopes 恒真——首版据此过滤，把
	// 只作用于输出面的规则也放了进来（钉测 TestHistoryGuardManagedBaseline…抓到）。
	inScope := func(typ SensitiveType, action, kind string) bool {
		return !scopedType(typ) && action != "log" && kind != "review_combination"
	}
	if manager := d.RuleManager(); manager != nil {
		for _, rule := range manager.SnapshotFor(ctx).Rules {
			if !(rule.Enabled && slices.Contains(rule.Scopes, "input") && (rule.TenantID == "" || rule.TenantID == authenticatedTenant(ctx))) {
				continue
			}
			if !inScope(SensitiveType(rule.Type), rule.Action, rule.Kind) {
				continue
			}
			e := patternEntry{
				sType: SensitiveType(rule.Type), regex: rule.Regex, group: rule.Group, id: rule.ID, kind: rule.Kind, action: rule.Action,
				vendorKeyShape: rule.Kind == "vendor_key_shape", bearerShape: isBearerRegex(rule.Regex.String()), exemptions: rule.Exemptions,
			}
			if err := add(e, rule.Fields, rule.Action); err != nil {
				return historyPolicy{err: err}.guard
			}
		}
	} else {
		d.mu.RLock()
		for _, pattern := range d.patterns {
			if !pattern.appliesScope(ScopeInputDeclarations) || !inScope(pattern.sType, pattern.action, pattern.kind) {
				continue
			}
			if err := add(pattern, nil, pattern.action); err != nil {
				d.mu.RUnlock()
				return historyPolicy{err: err}.guard
			}
		}
		d.mu.RUnlock()
	}
	policy := compileHistoryPolicy(rules)
	policy.entries, policy.entryTerms, policy.blocks = entries, entryTerms, blocks
	policy.detect = func(ctx context.Context, text string, candidates []patternEntry) (bool, bool, error) {
		frags, err := d.detectPatterns(ctx, text, candidates, ScopeInputDeclarations, nil)
		if err != nil {
			if errors.Is(err, ErrRuleBlocked) {
				return true, true, nil
			}
			return false, false, err
		}
		for _, f := range frags {
			if blocks[f.RuleID] {
				return true, true, nil
			}
		}
		return false, len(frags) > 0, nil
	}
	encoded, err := json.Marshal(struct {
		V     int
		Rules []historyPolicyRule
	}{historyPolicyVersion, rules})
	if err != nil {
		return historyPolicy{err: err}.guard
	}
	key := sha256.Sum256(encoded)
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if cached, ok := d.historyPolicies[key]; ok {
		return memoizeHistoryGuard(cached.guard, key, d.verdictMemo(), len(cached.matches) > 0)
	}
	if len(d.historyPolicies) >= 8 {
		clear(d.historyPolicies)
	}
	if d.historyPolicies == nil {
		d.historyPolicies = make(map[[32]byte]historyPolicy)
	}
	d.historyPolicies[key] = policy
	return memoizeHistoryGuard(policy.guard, key, d.verdictMemo(), len(policy.matches) > 0)
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
	// 快路径：逐组（选择器 + 放宽正则）匹配。
	//
	//   - 字段规则（带选择器的组）命中：维持修复前的行为——block 组拒绝、mask 组
	//     省略，立即返回；
	//   - 泛匹配规则（无选择器的组）命中：只说明「可能」，记下来等复核；有复核器
	//     时继续看完剩下的组（后面可能还有字段规则），没有复核器时按修复前处理。
	possible := false
	for _, rule := range p.matches {
		if rule.selector != nil {
			// 选择器（敏感键名）没出现 ⇒ 这组字段规则与这条消息无关，看下一组。
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
		if !found {
			continue
		}
		if rule.selector != nil || p.detect == nil {
			if rule.block {
				return false, ErrRuleBlocked
			}
			return false, nil
		}
		possible = true
	}
	if !possible {
		return true, nil
	}
	// 慢路径：把已解码的文本读进内存（有界），交给输入面同一份检测主体复核。
	data, err := readAllBounded(open(), historyVerifyMaxBytes)
	if err != nil {
		if errors.Is(err, errVerdictTooLarge) {
			return false, nil // 太大复核不了 ⇒ 保守省略
		}
		return false, err
	}
	// 逐条预筛：规则必含的字面量一个都没出现 ⇒ 这条规则不可能命中，不必扫。
	// 账号密码配对、邮箱、Bearer、JWT、AWS、URL 这几条较重的规则在绝大多数消息上
	// 都能这样跳过（211 真实会话实测：复核占冷路径 62% 的 CPU，预筛后约省一半）。
	folded := historyFoldASCII(append([]byte(nil), data...))
	candidates := make([]patternEntry, 0, len(p.entries))
	for i, e := range p.entries {
		if historyTextMayMatch(folded, p.entryTerms[i]) {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		// 按理走不到（快路径的组正则就是这些规则拼起来的）；真走到了按「全部复核」
		// 处理，不因为预筛放行。
		candidates = p.entries
	}
	block, sensitive, err := p.detect(ctx, string(data), candidates)
	if err != nil {
		return false, err
	}
	if block {
		return false, ErrRuleBlocked
	}
	return !sensitive, nil
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
