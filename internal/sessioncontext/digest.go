package sessioncontext

// digest.go —— 历史的分段抽取式摘要 + 分层合并（2026-11-23 大会话归约提速轮·第三步）。
//
// # 为什么需要它
//
// 归约此前给模型看的历史摘要是「best-16」：前 16 条非空消息先占位，后面的
// 消息只有查询词重叠得分**严格更高**才挤得掉它们。对一场几千条的 agent 会话，
// 这等于「会话最开头的 16 条 + 零星命中查询词的几行」——中段基本不可见。
//
// # 做什么
//
//   - 分段：把被压缩的历史按原始字节切成约 SegmentBytes 一段（不切开消息）；
//   - 每段一次抽取式摘要：按「稀有词占比 + 数字/路径/标识符 + 人类消息开头句 +
//     角色权重」给每一行打分，去掉与已选行高度重合的，取到段预算为止，按原文
//     顺序输出 `<role>: <行>`；
//   - 分层合并：段摘要总量超过 BudgetBytes 时，相邻两两再抽取一次，每层减半，
//     直到装进预算。每个分支都有名额 ⇒ 首/中/尾覆盖均衡，不会被开头或结尾垄断；
//   - 记录：段摘要按「段内消息内容哈希」缓存。会话是追加式的，前面的段一轮
//     轮不变 ⇒ 下一轮只有新段需要计算。
//
// # 边界
//
//   - 只使用**通过了历史守卫**的历史消息（与 best-16 摘录同一道闸）；受保护
//     消息原样保留，不进摘要；
//   - 只取 content/text/parts/output/result 里的字符串（plainText 的既有纪律：
//     工具调用参数与 id 不被当作文本）；
//   - >64KB 的消息不进摘要，只留一行显式的省略标记（它的内容由 best-16 摘录
//     那条路照旧处理）；
//   - 摘要不依赖查询词 ⇒ 可以跨轮缓存；与当前任务相关的那部分仍由 best-16 负责；
//   - 未在 ctx 里声明启用时，Reduce 的输出与此前逐字节相同。

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// DigestConfig 是分段摘要的配置（零值字段取默认）。
type DigestConfig struct {
	Enabled      bool
	SegmentBytes int // 分段目标大小（按原始消息字节累计）
	BudgetBytes  int // 摘要正文的总预算（不含头部说明行）
}

const (
	digestVersion = 1

	defaultDigestSegmentBytes = 64 << 10
	defaultDigestBudgetBytes  = 16 << 10
	minDigestSegmentBytes     = 8 << 10
	maxDigestSegmentBytes     = 256 << 10
	minDigestBudgetBytes      = 1 << 10
	maxDigestBudgetBytes      = 64 << 10

	// 每段的摘要预算 = 段大小 / digestPerSegmentDivisor，夹在 [min, max]。
	digestPerSegmentDivisor = 16
	digestPerSegmentMin     = 256
	digestPerSegmentMax     = 4096

	digestUnitMaxBytes = 240 // 单行最长（按 UTF-8 边界截断）
	digestMinUnitBytes = 12  // 更短的行不参与（括号、编号、空壳）
	// 单条消息最多取「前 digestUnitsHead + 后 digestUnitsTail」行，防止一条巨大的
	// 工具输出刷满一个段。
	digestUnitsHead = 120
	digestUnitsTail = 40

	digestCacheEntries = 4096

	// 两行的词项重合度达到这个值才算「重复」而跳过。取 0.9：只去掉几乎逐字重复的行。
	//
	// 为什么不取常见的 0.5~0.8：agent 会话里满是同一模板的行（「第 N 步：我去检查 X
	// 模块」「Finding N: …」），彼此只差一个数字或名字——那往往正是不同的事实。阈值
	// 0.8 时，树形合并的跨段去重会把用户第二次提的要求（「请检查 ledger 面板，第 42
	// 天」）当成第一次（「…第 2 天」）的重复丢掉（集成测试实测：用户消息只留在了前
	// 几段）。0.9 下只有真正的复读才会被去掉。
	digestRedundantJaccard = 0.9

	// 同一条消息在一段里至多留这么多行——把名额摊到更多轮次上。
	digestMaxLinesPerMessage = 2

	digestLargeMarker = "[message of "
	digestLargeSuffix = " bytes omitted from digest]"
)

type digestKey struct{}

// WithDigest 在 ctx 里声明本次归约启用分段摘要。
func WithDigest(ctx context.Context, cfg DigestConfig) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, digestKey{}, cfg.Normalized())
}

// DigestConfigFrom 返回 ctx 声明的分段摘要配置；未声明 ⇒ ok=false。
func DigestConfigFrom(ctx context.Context) (DigestConfig, bool) {
	if ctx == nil {
		return DigestConfig{}, false
	}
	cfg, ok := ctx.Value(digestKey{}).(DigestConfig)
	return cfg, ok
}

// Normalized 把零值换成默认、越界值夹回区间（WithDigest 存进 ctx 的就是归一化之后的值）。
func (c DigestConfig) Normalized() DigestConfig {
	c.SegmentBytes = clampInt(c.SegmentBytes, defaultDigestSegmentBytes, minDigestSegmentBytes, maxDigestSegmentBytes)
	c.BudgetBytes = clampInt(c.BudgetBytes, defaultDigestBudgetBytes, minDigestBudgetBytes, maxDigestBudgetBytes)
	return c
}

func clampInt(v, def, lo, hi int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// 缓存：键 = sha256(版本 ‖ 参数 ‖ 段内各条消息的身份)，值 = 段摘要。
// ---------------------------------------------------------------------------

type digestCache struct {
	mu   sync.Mutex
	vals map[[32]byte]string
	ring [][32]byte
	next int
	cap  int
}

func newDigestCache(entries int) *digestCache {
	if entries < 16 {
		entries = 16
	}
	return &digestCache{vals: make(map[[32]byte]string, entries), ring: make([][32]byte, 0, entries), cap: entries}
}

// defaultDigestCache 是进程级的段摘要缓存（与守卫判决缓存同一纪律：丢了重算即可）。
var defaultDigestCache = newDigestCache(digestCacheEntries)

type digestCacheKey struct{}

// NewDigestCache 构造一个独立的段摘要缓存（给 WithDigestCache 用；生产用进程级默认的那份）。
func NewDigestCache(entries int) *digestCache { return newDigestCache(entries) }

// WithDigestCache 替换本次归约使用的段摘要缓存（测试隔离用）。传 nil ⇒ 用默认那份。
func WithDigestCache(ctx context.Context, c *digestCache) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, digestCacheKey{}, c)
}

func digestCacheFrom(ctx context.Context) *digestCache {
	if c, ok := ctx.Value(digestCacheKey{}).(*digestCache); ok && c != nil {
		return c
	}
	return defaultDigestCache
}

func (c *digestCache) get(key [32]byte) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vals[key]
	return v, ok
}

func (c *digestCache) put(key [32]byte, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.vals[key]; exists {
		return
	}
	if len(c.ring) >= c.cap {
		delete(c.vals, c.ring[c.next])
		c.ring[c.next] = key
		c.next++
		if c.next >= c.cap {
			c.next = 0
		}
	} else {
		c.ring = append(c.ring, key)
	}
	c.vals[key] = val
}

// ---------------------------------------------------------------------------
// Reduce 的接入点
// ---------------------------------------------------------------------------

// digestStats 是一次摘要的观测计数（进 Report）。
type digestStats struct {
	Segments  int // 段数
	CacheHits int // 直接命中缓存的段数
	Bytes     int // 摘要正文字节数
}

// digestInput 是 Reduce 交给摘要机的只读材料。
type digestInput struct {
	spans    []largebody.Span
	infos    []messageInfo
	eligible []bool                      // 被压缩且通过了守卫的历史消息
	msgHash  [][32]byte                  // ≤64KB 消息的内容哈希；大消息为零值
	read     func(i int) ([]byte, error) // 按下标读回一条 ≤64KB 的消息
}

type digestSegment struct {
	first, last int // 消息下标的闭区间
}

// segmentHistory 按原始字节累计切段；不 eligible 的消息不占字节也不打断段。
func segmentHistory(in digestInput, target int) []digestSegment {
	var out []digestSegment
	cur, size := digestSegment{}, 0
	for i := range in.spans {
		if !in.eligible[i] {
			continue
		}
		if size == 0 {
			cur.first = i
		}
		cur.last = i
		size += int(in.spans[i].Length)
		if size >= target {
			out = append(out, cur)
			size = 0
		}
	}
	if size > 0 {
		out = append(out, cur)
	}
	return out
}

// segmentKey 是段的缓存键：版本、参数、段内各条消息的身份。
// 小消息的身份 = 内容哈希；大消息不进摘要，身份 = 角色 + 长度。
func (in digestInput) segmentKey(seg digestSegment, cfg DigestConfig) [32]byte {
	h := sha256.New()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], digestVersion)
	h.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], uint64(cfg.SegmentBytes))
	h.Write(buf[:])
	for i := seg.first; i <= seg.last; i++ {
		if !in.eligible[i] {
			continue
		}
		if in.spans[i].Length <= materializeItem {
			h.Write(in.msgHash[i][:])
			continue
		}
		h.Write([]byte{'B'})
		h.Write([]byte(in.infos[i].role))
		binary.LittleEndian.PutUint64(buf[:], uint64(in.spans[i].Length))
		h.Write(buf[:])
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// buildDigest 产出摘要正文（不含头部说明行）；没有可摘的行 ⇒ 空串。
func buildDigest(ctx context.Context, cfg DigestConfig, in digestInput, workers int) (string, digestStats, error) {
	var stats digestStats
	segs := segmentHistory(in, cfg.SegmentBytes)
	if len(segs) == 0 {
		return "", stats, nil
	}
	cache := digestCacheFrom(ctx)
	keys := make([][32]byte, len(segs))
	summaries := make([]string, len(segs))
	missing := make([]int, 0, len(segs))
	for k, seg := range segs {
		keys[k] = in.segmentKey(seg, cfg)
		if s, ok := cache.get(keys[k]); ok {
			summaries[k] = s
			stats.CacheHits++
		} else {
			missing = append(missing, k)
		}
	}
	stats.Segments = len(segs)
	perSegment := perSegmentBudget(cfg.SegmentBytes)
	if _, err := runIndexed(ctx, workers, len(missing),
		func(k int) (any, error) {
			// 在调用方协程里按下标顺序读回这一段的小消息（归档只有一个游标）。
			seg := segs[missing[k]]
			msgs := make([][]byte, seg.last-seg.first+1)
			for i := seg.first; i <= seg.last; i++ {
				if !in.eligible[i] || in.spans[i].Length > materializeItem {
					continue
				}
				raw, err := in.read(i)
				if err != nil {
					return nil, err
				}
				msgs[i-seg.first] = raw
			}
			return msgs, nil
		},
		func(k int, v any) error {
			k0 := missing[k]
			text, err := summarizeSegment(ctx, in, segs[k0], v.([][]byte), perSegment)
			if err != nil {
				return err
			}
			cache.put(keys[k0], text)
			summaries[k0] = text
			return nil
		},
	); err != nil {
		return "", stats, err
	}
	text := mergeDigestLayers(summaries, cfg.BudgetBytes)
	stats.Bytes = len(text)
	return text, stats, nil
}

// perSegmentBudget 是一段的摘要预算：段大小的 1/16，夹在 [256, 4096]。
//
// 这是「候选池」的大小，不是最终摘要的大小：第一层多留一些行（一行结论往往就
// 有 100~130 字节，预算太小时一段只能留下七八行，覆盖是被上限卡住而不是被打分
// 卡住），最终取舍交给带公平保证的树形合并（mergeDigestLayers）。上限 4096 限住
// 了缓存条目的大小（4096 条 × 4KB = 16MB）与合并时要看的行数。
func perSegmentBudget(segmentBytes int) int {
	return clampInt(segmentBytes/digestPerSegmentDivisor, digestPerSegmentMin, digestPerSegmentMin, digestPerSegmentMax)
}

// summarizeSegment 对一段历史做抽取式摘要。
func summarizeSegment(ctx context.Context, in digestInput, seg digestSegment, msgs [][]byte, budget int) (string, error) {
	units := make([]digestUnit, 0, 256)
	for i := seg.first; i <= seg.last; i++ {
		if !in.eligible[i] {
			continue
		}
		if e := ctx.Err(); e != nil {
			return "", e
		}
		label := digestLabel(in.infos[i])
		weight := digestLabelWeight(label)
		if in.spans[i].Length > materializeItem {
			// 大消息：只留一行显式的省略标记（低权重——段里还有别的行时不会被选中）。
			marker := fmt.Sprintf("%s%d%s", digestLargeMarker, in.spans[i].Length, digestLargeSuffix)
			units = append(units, digestUnit{text: marker, label: label, terms: digestTerms(marker), weight: weight * 0.3, msg: len(units) + 1000000})
			continue
		}
		off := i - seg.first
		if off < 0 || off >= len(msgs) || msgs[off] == nil {
			continue
		}
		for _, u := range messageUnits(msgs[off], label, weight) {
			u.msg = i
			units = append(units, u)
		}
	}
	return renderSelection(units, selectUnits(units, budget)), nil
}

// ---------------------------------------------------------------------------
// 打分与选择
// ---------------------------------------------------------------------------

type digestUnit struct {
	text   string
	terms  []string // 去重后的词项（小写）
	label  string   // 行首标签：user / assistant / tool_result / 其它角色
	weight float64
	first  bool // 所在消息的第一行
	last   bool // 所在消息的最后一行
	// msg 是所在消息在段内的下标（同一条消息至多留 digestMaxLinesPerMessage 行）；
	// 合并层从摘要行还原出来的单元没有这个信息 ⇒ 每行各给一个独立的下标。
	msg int
}

var digestStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "you": true, "are": true, "not": true, "but": true, "have": true,
	"was": true, "were": true, "will": true, "can": true, "its": true, "our": true,
	"has": true, "had": true, "been": true, "they": true, "them": true, "their": true,
	"what": true, "when": true, "which": true, "your": true, "about": true, "into": true,
	"than": true, "then": true, "there": true, "these": true, "those": true, "would": true,
	"should": true, "could": true, "also": true, "just": true, "only": true, "some": true,
	"more": true, "most": true, "other": true, "such": true, "each": true, "via": true,
	"per": true, "using": true, "used": true, "use": true, "how": true, "why": true,
	"on": true, "in": true, "at": true, "to": true, "of": true, "is": true, "it": true,
	"be": true, "as": true, "by": true, "or": true, "an": true, "if": true, "so": true,
	"do": true, "we": true, "all": true, "any": true, "up": true, "out": true, "no": true,
}

// digestLabel 是消息在摘要行里的标签。Anthropic 形态下工具结果也挂在 user
// 角色上 ⇒ 用「带工具引用」把它区分出来，一律标成 tool_result——这既让它拿不到
// 人类消息的权重，合并层重新解析行首时也还能认出来。
func digestLabel(info messageInfo) string {
	switch {
	case len(info.refs) > 0:
		return "tool_result"
	case info.role == "user", info.role == "assistant":
		return info.role
	case info.role == "":
		return "assistant"
	default:
		return info.role
	}
}

// digestLabelWeight：人类消息（任务与意图）最重，助手消息次之，工具结果最轻。
func digestLabelWeight(label string) float64 {
	switch label {
	case "user":
		return 2.5
	case "assistant":
		return 1.5
	case "tool_result":
		return 0.35
	default:
		return 0.5
	}
}

// digestIsTool：工具结果的行排在人类/助手的行之后，并且受预算份额限制。
func digestIsTool(label string) bool { return label != "user" && label != "assistant" }

// messageUnits 把一条消息的文本拆成参与打分的行。
func messageUnits(raw []byte, label string, weight float64) []digestUnit {
	text := plainText([]json.RawMessage{raw})
	var units []digestUnit
	for rest := text; ; {
		line, next, ok := strings.Cut(rest, "\n")
		if !ok {
			rest = ""
		} else {
			rest = next
		}
		if u := newDigestUnit(line, label, weight); u != nil {
			units = append(units, *u)
		}
		if rest == "" {
			break
		}
	}
	if len(units) > digestUnitsHead+digestUnitsTail {
		copy(units[digestUnitsHead:], units[len(units)-digestUnitsTail:])
		units = units[:digestUnitsHead+digestUnitsTail]
	}
	if len(units) == 0 {
		return nil
	}
	units[0].first = true
	units[len(units)-1].last = true
	return units
}

func newDigestUnit(line, label string, weight float64) *digestUnit {
	line = strings.TrimSpace(line)
	if len(line) < digestMinUnitBytes || !hasLetterOrDigit(line) {
		return nil
	}
	if len(line) > digestUnitMaxBytes {
		line = utf8Prefix(line, digestUnitMaxBytes)
	}
	terms := digestTerms(line)
	if len(terms) == 0 {
		return nil
	}
	return &digestUnit{text: line, terms: terms, label: label, weight: weight}
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// digestTerms 分词：ASCII 词（字母/数字/下划线，长度 ≥2）与汉字两字组。
func digestTerms(s string) []string {
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 16)
	add := func(t string) {
		if digestStopwords[t] {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	var word strings.Builder
	flush := func() {
		w := word.String()
		// 字母词至少 2 个字符；纯数字不限长度——数字恰恰是事实之间的区别
		// （「retries 4」与「retries 9」不是同一件事）。
		if len(w) >= 2 || (len(w) == 1 && w[0] >= '0' && w[0] <= '9') {
			add(strings.ToLower(w))
		}
		word.Reset()
	}
	prevHan := rune(0)
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf && (isASCIIAlnum(byte(r)) || r == '_'):
			word.WriteRune(r)
			prevHan = 0
		case unicode.Is(unicode.Han, r):
			flush()
			if prevHan != 0 {
				add(string([]rune{prevHan, r}))
			}
			prevHan = r
		default:
			flush()
			prevHan = 0
		}
	}
	flush()
	return out
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// unitSignal 给「带具体信息」的行加分：数字、路径/标识符、大小写混写的名字。
func unitSignal(text string) float64 {
	digits, mixed, path := 0, false, false
	var prev byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case (c == '/' || c == '_' || c == '.' || c == '-') && isASCIIAlnum(prev):
			path = true
		case c >= 'A' && c <= 'Z' && prev >= 'a' && prev <= 'z':
			mixed = true
		}
		prev = c
	}
	signal := 0.0
	if digits >= 2 {
		signal += 0.5
	}
	if path {
		signal += 0.5
	}
	if mixed {
		signal += 0.5
	}
	return signal
}

// selectUnits 打分并选出要保留的行，返回升序下标。
//
// 分数 = (稀有词占比 + 信息信号 + 长度因子) × 角色权重，消息首行 ×1.5、末行
// ×1.15。按分数从高到低装进预算；与已选行近乎重复（词项重合度 ≥ 0.8）的跳过；
// 平分时下标小者优先 ⇒ 结果确定。
func selectUnits(units []digestUnit, budget int) []int {
	return selectUnitsExcluding(units, budget, nil)
}

// selectUnitsExcluding 是 selectUnits 的通用形态：external 是**别处已经选中的行**
// 的词项集合——与它们近乎重复的行一并跳过（合并时跨段去重）。
//
// 装填规则：按 unitOrder 的次序（先人类/助手、后工具结果；同类按分数）逐行尝试，
// 放得下就留——于是人类/助手的行总是先用预算，工具结果的行只拿剩余额度；与已选
// 行近乎重复的跳过；同一条消息已有 digestMaxLinesPerMessage 行的跳过。
func selectUnitsExcluding(units []digestUnit, budget int, external [][]string) []int {
	if len(units) == 0 || budget <= 0 {
		return nil
	}
	selected := make([]int, 0, 16)
	used := 0
	perMsg := make(map[int]int, 16)
	for _, i := range unitOrder(units) {
		cost := len(unitLine(units[i])) + 1
		if used+cost > budget || perMsg[units[i].msg] >= digestMaxLinesPerMessage {
			continue
		}
		redundant := false
		for _, terms := range external {
			if jaccard(units[i].terms, terms) >= digestRedundantJaccard {
				redundant = true
				break
			}
		}
		for _, j := range selected {
			if redundant {
				break
			}
			if jaccard(units[i].terms, units[j].terms) >= digestRedundantJaccard {
				redundant = true
			}
		}
		if redundant {
			continue
		}
		selected = append(selected, i)
		perMsg[units[i].msg]++
		used += cost
	}
	sort.Ints(selected)
	return selected
}

// unitOrder 给出尝试次序：先人类/助手的行，再工具结果的行；同类之内按分数从高到低
// （平分时下标小者优先 ⇒ 结果确定）。
//
// 分数 = (稀有词占比 + 信息信号 + 长度因子) × 角色权重，消息首行 ×1.5、末行
// ×1.15——首行加成只给非工具消息：工具输出的第一行通常只是表头。稀有词占比在
// **本组行**内部统计：一段里人人都说的词不算信息。
func unitOrder(units []digestUnit) []int {
	order := make([]int, len(units))
	if len(units) == 0 {
		return order
	}
	df := make(map[string]int, 256)
	for _, u := range units {
		for _, t := range u.terms {
			df[t]++
		}
	}
	n := float64(len(units))
	score := make([]float64, len(units))
	for i, u := range units {
		order[i] = i
		if len(u.terms) == 0 {
			continue
		}
		sum := 0.0
		for _, t := range u.terms {
			sum += math.Log(1 + n/float64(df[t]))
		}
		avg := sum / float64(len(u.terms))
		s := (avg + 0.4*unitSignal(u.text) + 0.3*math.Min(1, float64(len(u.text))/80)) * u.weight
		if u.first && !digestIsTool(u.label) {
			s *= 1.5
		}
		if u.last {
			s *= 1.15
		}
		score[i] = s
	}
	class := func(i int) int {
		if digestIsTool(units[i].label) {
			return 1
		}
		return 0
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if class(x) != class(y) {
			return class(x) < class(y)
		}
		return score[x] > score[y]
	})
	return order
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	set := make(map[string]struct{}, len(small))
	for _, t := range small {
		set[t] = struct{}{}
	}
	inter := 0
	for _, t := range large {
		if _, ok := set[t]; ok {
			inter++
		}
	}
	if union := len(a) + len(b) - inter; union > 0 {
		return float64(inter) / float64(union)
	}
	return 0
}

func unitLine(u digestUnit) string {
	if u.label == "" {
		return u.text
	}
	return u.label + ": " + u.text
}

func renderSelection(units []digestUnit, selected []int) string {
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	for k, i := range selected {
		if k > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(unitLine(units[i]))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 分层合并：保留合并树，自顶向下分配预算
// ---------------------------------------------------------------------------

// digestNode 是合并树的节点：叶子 = 一个段的摘要行；内部节点 = 相邻两个节点。
type digestNode struct {
	units []digestUnit
	a, b  *digestNode
	// demand 是子树里所有行的总成本（叶子不超过段预算）。分预算时按它折算：
	// 需求小的子树拿够即止，余额给另一边。
	demand int
	// floor 是子树里每个叶子「最优一行」的成本之和：只要总预算负担得起，每个
	// 段至少留下它最好的一行——否则预算紧时较长的行（往往正是人类的消息）会被
	// 更短的行按长度顶掉。
	floor int
}

// buildDigestTree 把按原文顺序排列的叶子两两合并成一棵平衡树（奇数时最后一个
// 原样进入上一层）。
func buildDigestTree(leaves []*digestNode) *digestNode {
	for len(leaves) > 1 {
		next := make([]*digestNode, 0, (len(leaves)+1)/2)
		for i := 0; i < len(leaves); i += 2 {
			if i+1 == len(leaves) {
				next = append(next, leaves[i])
				continue
			}
			next = append(next, &digestNode{a: leaves[i], b: leaves[i+1], demand: leaves[i].demand + leaves[i+1].demand, floor: leaves[i].floor + leaves[i+1].floor})
		}
		leaves = next
	}
	return leaves[0]
}

// mergeDigestLayers 把各段摘要合并到 budget 以内，行序保持原文顺序。
//
// 为什么是「树上分预算」而不是「相邻两节压成一个字符串、逐层减半」：后一种做法
// 每一层都在节点内部重新按分数竞争——两段内容同构时（agent 会话里满是这样做
// 的行），一段的分数会因为无关紧要的差别（比如编号多一位 ⇒ 行长一点点）整体
// 略高，「按分数填满预算」就把名额全给它，几层之后弱的段一行不剩（单元测试
// TestMergeDigestLayersFitsBudgetAndKeepsEveryRegion 抓到过两次）。改成自顶向下
// 分配之后，每一段的份额由它在树里的位置决定，与内容无关。
func mergeDigestLayers(summaries []string, budget int) string {
	leaves := make([]*digestNode, 0, len(summaries))
	for _, s := range summaries {
		if s == "" {
			continue
		}
		if units := parseDigestLines(s); len(units) > 0 {
			leaves = append(leaves, newDigestLeaf(units))
		}
	}
	if len(leaves) == 0 {
		return ""
	}
	var external [][]string
	selected := selectNode(buildDigestTree(leaves), budget, &external)
	var b strings.Builder
	for k, u := range selected {
		if k > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(unitLine(u))
	}
	return b.String()
}

// selectNode 在 budget 内从子树里选行。叶子内部才按分数选；内部节点把预算分给
// 两个子树——**按需求比例**分（需求 = 子树里所有行的总成本，拿够即止）：
//
//   - 需求相同 ⇒ 对半；
//   - 需求小的子树拿到它的全部需求，余额归另一边。
//
// 左边选完后，右边拿走全部余额（左边因去重省下的也归它）；右边仍没用完、而左边
// 当时是被额度卡住的（还有行没选）⇒ 左边拿剩下的额度补选一次。两个方向都能
// 流转，不存在「余额总是先流向某一侧」的偏置。
//
// external 累积「整棵树里已经选中的行」的词项，跨段去重用。
func selectNode(nd *digestNode, budget int, external *[][]string) []digestUnit {
	if nd.a == nil {
		sel := selectUnitsExcluding(nd.units, budget, *external)
		out := make([]digestUnit, 0, len(sel))
		for _, i := range sel {
			out = append(out, nd.units[i])
			*external = append(*external, nd.units[i].terms)
		}
		return out
	}
	leftBudget := splitBudget(budget, nd.a, nd.b)
	mark := len(*external)
	left := selectNode(nd.a, leftBudget, external)
	right := selectNode(nd.b, budget-unitsCost(left), external)
	if spare := budget - unitsCost(left) - unitsCost(right); spare > 0 && unitsCost(left) == leftBudget && leftBudget < nd.a.demand {
		// 右边没用完，而左边当时是被额度卡住的 ⇒ 左边补选（同样的输入、更大的
		// 预算 ⇒ 结果是刚才那批的超集）。
		*external = (*external)[:mark]
		left = selectNode(nd.a, leftBudget+spare, external)
		for _, u := range right {
			*external = append(*external, u.terms)
		}
	}
	return append(left, right...)
}

// newDigestLeaf 是一个段：需求 = 全部行的成本，保底 = 最优一行的成本。
func newDigestLeaf(units []digestUnit) *digestNode {
	order := unitOrder(units)
	floor := 0
	if len(order) > 0 {
		floor = len(unitLine(units[order[0]])) + 1
	}
	return &digestNode{units: units, demand: unitsCost(units), floor: floor}
}

// splitBudget 把 budget 分给左右两个子树，返回左边的份额：
//
//   - 按需求比例分（四舍五入；差 1 字节的份额可能正好放不下一行），需求小的至多
//     拿到它的需求；
//   - 两个子树的保底之和负担得起时，左边不低于它的保底、右边（拿余额）也不低于
//     它的保底——每个段至少留下最好的一行。
func splitBudget(budget int, a, b *digestNode) int {
	if a.demand <= 0 {
		return 0
	}
	if b.demand <= 0 {
		return budget
	}
	total := int64(a.demand + b.demand)
	share := int((int64(budget)*int64(a.demand) + total/2) / total)
	if a.floor+b.floor <= budget {
		if share < a.floor {
			share = a.floor
		}
		if budget-share < b.floor {
			share = budget - b.floor
		}
	}
	if share > a.demand {
		share = a.demand
	}
	return share
}

func unitsCost(units []digestUnit) int {
	total := 0
	for _, u := range units {
		total += len(unitLine(u)) + 1
	}
	return total
}

// parseDigestLines 把已渲染的摘要行还原成打分单元（行首的 `<label>: ` 给出权重）。
func parseDigestLines(text string) []digestUnit {
	units := make([]digestUnit, 0, 32)
	for _, line := range strings.Split(text, "\n") {
		label, body := "", line
		if i := strings.Index(line, ": "); i >= 0 && i <= 16 && !strings.ContainsAny(line[:i], " \t") {
			label, body = line[:i], line[i+2:]
		}
		u := newDigestUnit(body, label, digestLabelWeight(label))
		if u == nil {
			continue
		}
		u.msg = len(units) // 合并层：每行各算一条「消息」⇒ 不受每条消息行数上限约束
		units = append(units, *u)
	}
	return units
}
