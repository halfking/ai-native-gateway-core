package mcp

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed capability_catalog.json
var capabilityCatalogJSON []byte

// CapabilityTool 是能力目录中的一个工具条目（字段与既有
// tools/mcp-gateway/src/capability-router.js 的 STATIC_TOOLS 一一对应）。
type CapabilityTool struct {
	Name            string   `json:"name"`
	Group           string   `json:"group"`
	Layer           string   `json:"layer"`
	Capabilities    []string `json:"capabilities"`
	Cost            int      `json:"cost"`
	LatencyP99MS    int      `json:"latency_p99_ms"`
	RateLimitPerMin int      `json:"rate_limit_per_min"`
	Description     string   `json:"description"`
	Keywords        []string `json:"keywords"`
}

// ToolPolicy 是按工具类别给出的执行策略（缓存/限流/超时/重试）。
type ToolPolicy struct {
	CacheTTLMS     int    `json:"cacheTTL"`
	RateLimit      int    `json:"rateLimit"`
	TimeoutMS      int    `json:"timeout"`
	Retries        int    `json:"retries"`
	Classification string `json:"classification,omitempty"`
}

type capabilityCatalogFile struct {
	Tools    []CapabilityTool      `json:"tools"`
	Policies map[string]ToolPolicy `json:"policies"`
}

// capabilityToTools 是「能力域 → 工具名」映射（移植自既有
// tools/mcp-gateway/src/gateway.js 的 CAPABILITY_TOOLS）。
var capabilityToTools = map[string][]string{
	"code": {
		"code_generate", "code_review", "code_explain", "code_refactor",
		"code_test", "code_debug", "code_translate",
	},
	"memory": {
		"search_memory", "save_memory", "list_memories",
		"delete_memory", "update_memory",
	},
	"agent": {
		"list_agents", "get_agent", "create_agent_task",
		"get_agent_status", "list_agent_tasks",
	},
	"wiki": {
		"search_wiki", "get_wiki_page", "list_wiki_pages",
		"create_wiki_page", "update_wiki_page",
	},
	"apikey": {
		"create_api_key", "list_api_keys", "revoke_api_key",
		"get_api_key_usage",
	},
	"federation": {
		"federation_register", "federation_list", "federation_status",
		"federation_forward",
	},
	"reasoning": {
		"reasoning_chain", "reasoning_step", "reasoning_evaluate",
	},
}

// capabilityOrder 是能力域的**稳定顺序**。
//
// ★ 为什么必须显式保序：既有 tools/mcp-gateway 的非法能力域报错文案是
// `Unknown capability: X. Valid: ${Array.from(VALID_CAPABILITIES).join(', ')}`
// 而 VALID_CAPABILITIES = new Set(Object.keys(CAPABILITY_TOOLS))
// ⇒ 文案顺序 = CAPABILITY_TOOLS 的**键插入顺序**。Go 的 map 迭代顺序随机，
// 照抄 map 会让同一错误的文案每次都变 ⇒ 这里显式列出与既有实现同序的清单。
var capabilityOrder = []string{
	"code", "memory", "agent", "wiki", "apikey", "federation", "reasoning",
}

// ValidCapability 报告是否为已知能力域。
func ValidCapability(cap string) bool {
	_, ok := capabilityToTools[cap]
	return ok
}

// ValidCapabilities 返回全部合法能力域（按稳定顺序，拷贝）。
func ValidCapabilities() []string {
	out := make([]string, len(capabilityOrder))
	copy(out, capabilityOrder)
	return out
}

// CapabilityTools 返回能力域对应的工具名列表（拷贝）。
func CapabilityTools(cap string) []string {
	list, ok := capabilityToTools[cap]
	if !ok {
		return nil
	}
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// CapabilityRouter 维护能力目录并提供策略查询与检索推荐。
//
// 语义与既有 tools/mcp-gateway 的 CapabilityRouter 对齐（分类判定、打分、
// 分层统计完全一致），只是运行在 Go 侧、随 llm-gateway-go 一起分发。
type CapabilityRouter struct {
	mu       sync.RWMutex
	tools    map[string]CapabilityTool
	policies map[string]ToolPolicy
}

// NewCapabilityRouter 构造能力路由器（加载内嵌目录）。
func NewCapabilityRouter() *CapabilityRouter {
	r := &CapabilityRouter{
		tools:    map[string]CapabilityTool{},
		policies: map[string]ToolPolicy{},
	}
	var file capabilityCatalogFile
	if err := json.Unmarshal(capabilityCatalogJSON, &file); err == nil {
		for _, t := range file.Tools {
			r.tools[t.Name] = t
		}
		for k, v := range file.Policies {
			r.policies[k] = v
		}
	}
	return r
}

// ToolCapability 返回工具的能力条目；未知工具返回 nil。
func (r *CapabilityRouter) ToolCapability(toolName string) *CapabilityTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[toolName]
	if !ok {
		return nil
	}
	return &t
}

// Classify 由能力标签推导工具类别（llm > write > read > unknown）。
//
// 判定顺序与既有实现一致：cost>=2 或含 llm 标签 → llm；含写类标签 → write；
// 含读类标签 → read；否则 unknown。
func (r *CapabilityRouter) Classify(t CapabilityTool) string {
	if t.Cost >= 2 || contains(t.Capabilities, "llm") {
		return "llm"
	}
	for _, c := range []string{"write", "create", "delete", "update", "execute"} {
		if contains(t.Capabilities, c) {
			return "write"
		}
	}
	for _, c := range []string{"read", "search", "list", "recall"} {
		if contains(t.Capabilities, c) {
			return "read"
		}
	}
	return "unknown"
}

// ToolPolicy 返回工具的执行策略；未知工具走 unknown 档。
func (r *CapabilityRouter) ToolPolicy(toolName string) ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[toolName]
	if !ok {
		p := r.policies["unknown"]
		p.Classification = "unknown"
		return p
	}
	classification := r.classifyLocked(t)
	p, ok := r.policies[classification]
	if !ok {
		p = r.policies["unknown"]
	}
	p.Classification = classification
	return p
}

func (r *CapabilityRouter) classifyLocked(t CapabilityTool) string {
	if t.Cost >= 2 || contains(t.Capabilities, "llm") {
		return "llm"
	}
	for _, c := range []string{"write", "create", "delete", "update", "execute"} {
		if contains(t.Capabilities, c) {
			return "write"
		}
	}
	for _, c := range []string{"read", "search", "list", "recall"} {
		if contains(t.Capabilities, c) {
			return "read"
		}
	}
	return "unknown"
}

// AllCapabilities 返回全部能力条目。
func (r *CapabilityRouter) AllCapabilities() []CapabilityTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CapabilityTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CapabilityStats 返回按层统计（total + by_layer）。
func (r *CapabilityRouter) CapabilityStats() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type layerStat struct {
		Count int      `json:"count"`
		Tools []string `json:"tools"`
	}
	byLayer := map[string]*layerStat{}
	for _, t := range r.tools {
		st, ok := byLayer[t.Layer]
		if !ok {
			st = &layerStat{Tools: []string{}}
			byLayer[t.Layer] = st
		}
		st.Count++
		st.Tools = append(st.Tools, t.Name)
	}
	out := make(map[string]any, len(byLayer))
	for layer, st := range byLayer {
		sort.Strings(st.Tools)
		out[layer] = map[string]any{"count": st.Count, "tools": st.Tools}
	}
	return map[string]any{"total": len(r.tools), "by_layer": out}
}

// ToolRecommendation 是一条检索推荐。
type ToolRecommendation struct {
	Name          string `json:"name"`
	Group         string `json:"group"`
	Layer         string `json:"layer"`
	Description   string `json:"description"`
	Score         int    `json:"score"`
	TokensMatched int    `json:"tokens_matched"`
}

var capabilityStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"to": true, "of": true, "in": true, "on": true, "for": true, "and": true, "or": true,
	"的": true, "了": true, "是": true, "在": true, "我": true, "你": true,
	"他": true, "她": true, "它": true, "们": true,
}

// Tokenize 对查询串做切分（与既有实现同口径：长度≥2 且非停用词）。
//
// 相对既有实现的一处增强：CJK 串没有空格边界，长词（如「搜索记忆」）整体
// 作为 token 时命中率极低，因此额外产出**二元切分**（bigram）token。
// 例：「搜索记忆」⇒ 「搜索记忆」「搜索」「索记」「记忆」。
func Tokenize(query string) []string {
	if query == "" {
		return nil
	}
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '.', ';', '!', '?', '，', '。', '；', '！', '？', '、', '-', '_', '(', ')', '[', ']', '{', '}':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		if len([]rune(f)) < 2 || capabilityStopWords[f] {
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
		for _, bg := range cjkBigrams(f) {
			if !seen[bg] {
				seen[bg] = true
				out = append(out, bg)
			}
		}
	}
	return out
}

// cjkBigrams 对含 CJK 字符的 token 产出二元切分。
func cjkBigrams(token string) []string {
	runes := []rune(token)
	hasCJK := false
	for _, r := range runes {
		if isCJK(r) {
			hasCJK = true
			break
		}
	}
	if !hasCJK || len(runes) < 3 {
		return nil
	}
	out := make([]string, 0, len(runes)-1)
	for i := 0; i+1 < len(runes); i++ {
		out = append(out, string(runes[i:i+2]))
	}
	return out
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 兼容表意文字
		return true
	default:
		return false
	}
}

// ScoreTool 对工具打分（名称 5 / 关键词 3 / 描述 2 / 能力标签 1）。
func ScoreTool(tokens []string, t CapabilityTool) int {
	if len(tokens) == 0 {
		return 0
	}
	score := 0
	lowerName := strings.ToLower(t.Name)
	lowerDesc := strings.ToLower(t.Description)
	for _, tok := range tokens {
		if strings.Contains(lowerName, tok) {
			score += 5
		}
		matchedKeyword := false
		for _, kw := range t.Keywords {
			if strings.Contains(strings.ToLower(kw), tok) {
				matchedKeyword = true
				break
			}
		}
		if matchedKeyword {
			score += 3
		}
		if lowerDesc != "" && strings.Contains(lowerDesc, tok) {
			score += 2
		}
		for _, c := range t.Capabilities {
			if strings.Contains(strings.ToLower(c), tok) {
				score++
				break
			}
		}
	}
	return score
}

// RecommendTools 按查询推荐工具（Top-K）。
func (r *CapabilityRouter) RecommendTools(query string, topK int) []ToolRecommendation {
	if topK <= 0 {
		topK = 5
	}
	tokens := Tokenize(query)
	scored := make([]ToolRecommendation, 0, 8)
	for _, t := range r.AllCapabilities() {
		s := ScoreTool(tokens, t)
		if s <= 0 {
			continue
		}
		scored = append(scored, ToolRecommendation{
			Name:          t.Name,
			Group:         t.Group,
			Layer:         t.Layer,
			Description:   t.Description,
			Score:         s,
			TokensMatched: (s + 2) / 3,
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Name < scored[j].Name
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
