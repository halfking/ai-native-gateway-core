package autoroute

// patterns.go — compiled regex pattern layer for the heuristic classifier.
//
// This layer sits between the tool-based dispatch (channel 2) and the
// keyword-scoring channel (channel 3) in the classification priority chain.
//
// Motivation: keyword substring matching misses requests that express a
// task type via *structural* patterns rather than explicit vocabulary.
// The canonical failure case is the "水池问题" — a Chinese word-problem
// that contains no reasoning keyword ("求解"/"推导") but is unmistakably
// a multi-step math task via the pattern "每(分钟|小时).*(多少|几)".
//
// All patterns are compiled once at package init for zero per-request cost.
// The pattern set is deliberately small and high-precision — false
// positives here are more costly than false negatives because a pattern
// match (weight 0.55-0.65) overrides the keyword layer entirely.

import (
	"fmt"
	"regexp"
)

// PatternMatch is one compiled regex with its task-type attribution.
type PatternMatch struct {
	// TaskType is the classification assigned when this pattern matches.
	TaskType TaskType

	// pattern is the compiled regex (never nil for a valid entry).
	pattern *regexp.Regexp

	// Weight is the confidence assigned on match (0.5-0.7).
	// Kept below the tool-dispatch band (0.80-0.85) and above the
	// single-keyword-hit band (0.40) so the layer composes correctly.
	Weight float64

	// Reason is the human-readable explanation surfaced in admin UI.
	Reason string
}

// MatchString reports whether the pattern matches anywhere in text.
func (p PatternMatch) MatchString(text string) bool {
	if p.pattern == nil || text == "" {
		return false
	}
	return p.pattern.MatchString(text)
}

// compiledPatterns is the package-level singleton, built once at init.
var compiledPatterns []PatternMatch

func init() {
	compiledPatterns = buildDefaultPatterns()
}

// buildDefaultPatterns compiles the curated regex set.
//
// Each pattern was chosen to cover a known misclassification gap
// discovered during multi-round testing (see model-routing-test-report).
// Patterns are case-insensitive; Chinese patterns are naturally
// case-insensitive (no ASCII folding needed).
func buildDefaultPatterns() []PatternMatch {
	type raw struct {
		expr   string
		task   TaskType
		weight float64
		reason string
	}
	specs := []raw{
		// ── Reasoning patterns ──────────────────────────────────────
		// Chinese math word-problem: "每分钟进水10升...需要多少分钟"
		// This is the exact pattern of the "水池问题" test failure.
		{
			expr:   `每(?:分钟|小时|天|秒).{0,40}(?:多少|几|需要多久|耗时)`,
			task:   TaskReasoning,
			weight: 0.65,
			reason: "pattern: chinese math word-problem (rate × time → quantity)",
		},
		// Multi-step conditional logic chain: "如果A那么B，如果B那么C"
		{
			expr:   `如果.{1,60}那么.{1,60}如果`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: multi-step conditional logic chain",
		},
		// Arithmetic expression containing operators: "2x + 5 = 13"
		{
			expr:   `\d+\s*[+\-*/]\s*\d+`,
			task:   TaskReasoning,
			weight: 0.55,
			reason: "pattern: arithmetic expression detected",
		},
		// Statistics / probability vocabulary (Chinese)
		{
			expr:   `排列|组合|概率|期望值|方差|标准差|正态分布|贝叶斯`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: statistics/probability terminology",
		},
		// Optimization phrasing: "求最值/最大/最小/最优"
		{
			expr:   `求(?:最大|最小|最优|最值)|最大化|最小化|最优解`,
			task:   TaskReasoning,
			weight: 0.60,
			reason: "pattern: optimization problem phrasing",
		},
		// ── Code patterns ───────────────────────────────────────────
		// 新增（需求 #1）：计划模式 pattern（编程任务的强子类型）
		{
			expr:   `(?:先|请).{0,10}(?:制定|给出|列出).{0,10}(?:计划|方案|步骤).{0,20}(?:再|然后|之后).{0,10}(?:实现|编码|写代码)`,
			task:   TaskCode,
			weight: 0.70, // 高权重，因为这是明确的编程计划模式
			reason: "pattern: plan-mode coding (create plan then implement)",
		},
		// 新增（需求 #1）：错误堆栈粘贴（IDE 场景常见）
		// Pattern 修正：匹配 Python/Java/JS 堆栈的多种形式
		{
			expr:   `(?i)(?:traceback|error|exception).{0,30}(?:file|at)\s+["\']?\w+\.\w+["\']?,?\s+line\s+\d+`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: stack trace / error at line N (IDE paste)",
		},
		// 新增（需求 #1）：代码审查请求
		{
			expr:   `(?:review|审查|检查).{0,10}(?:my|这段|这个).{0,5}(?:code|代码|pr|pull request|implementation|实现)`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: code review request",
		},
		// Function/class/method definition syntax (multi-language)
		{
			expr:   `(?:def|func|fn|function|class|interface|struct|enum)\s+\w+`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: function/class/method definition syntax",
		},
		// Import statements (Python/JS/Go/Java)
		{
			expr:   `(?:import|from|require|include)\s+[\w."'/{ ]+`,
			task:   TaskCode,
			weight: 0.55,
			reason: "pattern: import/include statement",
		},
		// Variable declaration with type annotation
		{
			expr:   `(?:var|let|const|public|private|protected)\s+\w+\s*[:=]`,
			task:   TaskCode,
			weight: 0.55,
			reason: "pattern: typed variable declaration",
		},
		// ── 中文编程任务 patterns（补关键词盲区）──────────────────────
		// 盲区：关键词层有"写一个函数/写一个类/写一段代码"，但"写一个快速排序"
		// "写一个红黑树""做一个表单组件"不命中任何 code 关键词，落到了 chat。
		// 用正则精确锁定"动词 + 编程对象"，避免误伤 creative(故事/诗) 和 planning(方案/计划)。
		//
		// P1: "写/做/实现 + 编程对象名"（算法/数据结构/组件/接口/服务/中间件）
		// 命中如"写一个快速排序""做一个表单组件""实现一个 LRU 缓存""写个线程池"。
		{
			expr:   `(?:写|做|实现|实现一个|写一个|写个|做个|编写)(?:一个|个|一段|一个简单的|一个完整)?\s*(?:快速排序|冒泡排序|归并排序|拓扑排序|二分查找|红黑树|二叉树|二叉搜索树|b\s*树|b\+|avl|图|哈希表|散列表|链表|栈|队列|堆|trie|布隆过滤器|线程池|连接池|内存池|缓存|lru|限流器|熔断器|负载均衡|中间件|路由|解释器|编译器|虚拟机|区块链|加密|解密|签名|鉴权|认证|授权|登录|注册|表单组件|对话框|编辑器|解析器|序列化|爬虫|脚本|小工具|控件|组件|插件|微服务|网关|代理|函数|类|方法|模块|接口|服务)`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: chinese coding task (verb + programming object)",
		},
		// P2: "用 + 编程语言/技术栈 + 动作动词"
		// 命中如"用 React 做一个表单组件""用 SQL 查询订单""用 go 写一个 LRU 缓存"。
		// 语言/框架名本身就是强编程信号，且不会出现在 creative/planning 请求里。
		{
			expr:   `用\s*(?:python|java|javascript|js|typescript|ts|go|golang|rust|c\+\+|c#|ruby|php|swift|kotlin|scala|sql|react|vue|angular|node|django|flask|spring|gin|echo|flutter|nextjs|nuxt|tailwind|html|css|shell|bash|powershell)\s*(?:写|做|实现|编写|开发|生成|查询|连接|调用|构建|搭建|写一个|做一个|实现一个)`,
			task:   TaskCode,
			weight: 0.65,
			reason: "pattern: chinese coding task (language/framework + action verb)",
		},
		// P3: "写个/做个 + 脚本/工具/函数"（口语变体，P1 的补充）
		{
			expr:   `(?:写个|做个|帮我写个|帮我做个|写一个简单)(?:.*?)(?:脚本|工具|函数|方法|程序|demo|示例|prototype|原型|demo)`,
			task:   TaskCode,
			weight: 0.60,
			reason: "pattern: chinese coding task (colloquial 'write a script/tool')",
		},
		// ── Creative patterns ───────────────────────────────────────
		// "写一个/写一段/写首" without an explicit code/algorithm target
		// (the code keyword "写代码" already covers the code case)
		{
			expr:   `写(?:一个|一段|一首|一篇).{0,20}(?:故事|诗|歌词|散文|读后感|观后感)`,
			task:   TaskCreative,
			weight: 0.60,
			reason: "pattern: creative writing request (story/poem/lyrics)",
		},
	}

	out := make([]PatternMatch, 0, len(specs))
	for _, s := range specs {
		re, err := regexp.Compile(`(?i)` + s.expr)
		if err != nil {
			// A broken regex here is a programmer error, not a runtime
			// error. Log via panic so it surfaces during development
			// but never reaches production (CI catches it).
			panic(fmt.Sprintf("autoroute: invalid pattern %q: %v", s.expr, err))
		}
		out = append(out, PatternMatch{
			TaskType: s.task,
			pattern:  re,
			Weight:   s.weight,
			Reason:   s.reason,
		})
	}
	return out
}

// matchPatterns scans text against all compiled patterns and returns
// the first match for each task type (highest priority = first defined).
//
// Returns a map of task_type → PatternMatch for all types that matched.
// The caller (Classify) picks the winner from the map using the same
// priority tiebreak as keyword scoring.
//
// Performance: O(patterns × text_length). With ~9 patterns and a 32 KiB
// text cap, worst case is ~0.3 ms — well within the <1 ms budget.
func matchPatterns(text string) map[TaskType]PatternMatch {
	if len(text) == 0 {
		return nil
	}
	hits := make(map[TaskType]PatternMatch, 4)
	for _, p := range compiledPatterns {
		if p.MatchString(text) {
			// Keep only the first (highest-weight) match per task type,
			// since patterns are ordered by specificity within each type.
			if _, exists := hits[p.TaskType]; !exists {
				hits[p.TaskType] = p
			}
		}
	}
	return hits
}

// DefaultPatterns returns the compiled default pattern set. Exposed for
// admin introspection and testing.
func DefaultPatterns() []PatternMatch {
	out := make([]PatternMatch, len(compiledPatterns))
	copy(out, compiledPatterns)
	return out
}
