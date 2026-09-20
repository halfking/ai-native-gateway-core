// task_kind.go — R48（2026-09-20）细粒度任务分类 TaskKind。
//
// 与既有 21 个 TaskType（chat/code/reasoning/... 粗粒度）**正交**：
// TaskKind 回答"这类活是搜索还是总结还是 git 操作"，TaskType 回答
// "这是代码还是推理还是创作"。TaskKind 仅供 role_llm_router 选择
// 轻量/重量池（用户口径：搜索/总结/git/运维 → 轻量池；分析/规划/
// 方案编写 → 重量池），不写入 request_logs.task_type、不进 admin
// TaskType UI、不改变任何既有 TaskType 的语义。
//
// 代码风格对齐 task_types_ext.go：中英双语关键词 + 有序通道判定，
// 无状态纯函数。关键词刻意收窄（多词短语优先），接受低召回换取
// 高精度——误把重任务分到轻量池的代价高于漏判（漏判走 unknown 兜底，
// 也是轻量池，但那是用户口径明确要求的默认）。
package autoroute

import "strings"

// TaskKind 是与 TaskType 正交的细粒度任务类型。取值与 730 迁移
// role_task_llm_mapping.task_kind 的 CHECK 约束一致。
type TaskKind string

const (
	// KindSearch 搜索/检索/查找类。
	KindSearch TaskKind = "search"
	// KindSummarize 总结/摘要/概括类。
	KindSummarize TaskKind = "summarize"
	// KindGitOps git 操作类（提交/分支/合并/回滚）。
	KindGitOps TaskKind = "git_ops"
	// KindOps 运维类（部署/重启/日志排查/磁盘清理）。
	KindOps TaskKind = "ops"
	// KindAnalysis 分析/根因/评估类。
	KindAnalysis TaskKind = "analysis"
	// KindPlanning 规划/计划/排期/任务拆解类。
	KindPlanning TaskKind = "planning"
	// KindSolution 方案编写/解决方案设计类。
	KindSolution TaskKind = "solution"
	// KindUnknown 无法判定（兜底，按用户口径走轻量池默认）。
	KindUnknown TaskKind = "unknown"
)

// AllTaskKinds 是合法 TaskKind 全集（校验 + admin UI 用）。
var AllTaskKinds = []TaskKind{
	KindSearch, KindSummarize, KindGitOps, KindOps,
	KindAnalysis, KindPlanning, KindSolution, KindUnknown,
}

// taskKindChannels 是有序判定通道：先具体后泛化。
// git_ops / search / summarize 的短语高度特异，放前面；analysis 的
// "分析"最泛化，放最后兜底。planning 与 solution 同属重量池，即便
// 边界短语互串（"制定方案"同时命中两通道）代价也只是同池换主选模型。
var taskKindChannels = []struct {
	kind    TaskKind
	phrases []string
	enWords []string // 英文单词按词边界匹配（避免 "git"⊂"digit" 类子串误伤）
}{
	{
		kind: KindGitOps,
		phrases: []string{
			// 中文（含 "git" 前缀的中文混写）
			"git操作", "git命令", "git提交", "git仓库", "提交代码", "推送代码",
			"合并分支", "切换分支", "创建分支", "删除分支", "合并冲突", "解决冲突",
			"提交记录", "版本回退", "回滚提交", "撤销提交", "暂存区", "拉取代码",
			"打标签", "分支管理", "代码提交",
			// 英文多词短语（含空格，子串匹配安全）
			"pull request", "merge request", "cherry-pick", "git stash",
			"git commit", "git push", "git pull", "git merge", "git rebase",
			"git branch", "git checkout", "git log", "git diff", "git status",
			"git tag", "commit message", "merge conflict", "git flow",
		},
		enWords: []string{"git", "rebase", "revert"},
	},
	{
		kind: KindSearch,
		phrases: []string{
			// 中文
			"搜索", "检索", "查找", "查一下", "搜一下", "找一下", "帮我找",
			"查资料", "查文档", "哪里有", "有没有相关", "相关资料", "文献检索",
			// 英文多词短语
			"search for", "look up", "find out", "search the", "google for",
			"search documentation", "search the web", "web search",
		},
		enWords: []string{"search", "grep", "ripgrep"},
	},
	{
		kind: KindSummarize,
		phrases: []string{
			// 中文
			"总结一下", "做个总结", "写一份总结", "摘要", "概括", "归纳",
			"小结", "提炼要点", "列出要点", "要点总结", "会议纪要", "速览",
			// 英文多词短语
			"summarize the", "summarise the", "write a summary",
			"give me a summary", "tldr", "tl;dr", "key takeaways",
		},
		enWords: []string{"summarize", "summarise", "tldr"},
	},
	{
		kind: KindOps,
		phrases: []string{
			// 中文
			"运维", "部署", "发版", "上线", "重启服务", "重启一下", "扩容",
			"缩容", "监控告警", "巡检", "清理磁盘", "磁盘空间", "磁盘清理",
			"证书续期", "端口占用", "服务启动", "服务停止", "服务状态",
			"日志排查", "故障排查", "日志清理", "服务排查", "变更单", "工单",
			// 英文多词短语
			"health check", "troubleshoot the", "restart the service",
			"deploy the", "deployment checklist", "roll out", "rollout plan",
			"disk usage", "clean up disk", "certificate renewal",
		},
		enWords: []string{"deploy", "kubectl", "systemctl", "nginx", "devops", "sre"},
	},
	{
		kind: KindSolution,
		phrases: []string{
			// 中文
			"解决方案", "技术方案", "实施方案", "设计方案", "整改方案",
			"备选方案", "选型方案", "迁移方案", "方案设计", "出个方案",
			"出一份方案", "给个方案", "给一套方案", "方案文档",
			// 英文多词短语
			"solution design", "propose a solution", "design a solution",
			"write a solution", "solution document", "remediation plan",
		},
		enWords: nil,
	},
	{
		kind: KindPlanning,
		phrases: []string{
			// 中文
			"规划", "制定计划", "工作计划", "项目计划", "任务拆解", "拆解任务",
			"拆分任务", "任务划分", "任务分解", "工作分解", "排期", "里程碑",
			"路线图", "优先级排序", "迭代计划", "冲刺计划",
			// 英文多词短语
			"create a plan", "write a plan", "project plan", "work breakdown",
			"task breakdown", "break down the", "milestone plan", "sprint plan",
		},
		enWords: []string{"roadmap", "milestones", "prioritize"},
	},
	{
		kind: KindAnalysis,
		phrases: []string{
			// 中文
			"分析一下", "做个分析", "写一份分析", "深入分析", "对比分析",
			"根因分析", "性能分析", "竞品分析", "数据分析", "瓶颈分析",
			"可行性", "评估一下", "权衡", "调研一下",
			// 英文多词短语
			"root cause", "analyze the", "analysis of", "compare the",
			"trade-off analysis", "tradeoff analysis", "feasibility study",
			"profiling the", "bottleneck analysis",
		},
		enWords: []string{"analyze", "analyse", "profiling", "benchmark"},
	},
}

// ClassifyTaskKind 从分类信号判定细粒度任务类型。只扫 SystemPrompt +
// LastUserPrompt（与 HeuristicClassifier 同源的规范化文本），按
// taskKindChannels 顺序首个命中的通道胜出；全不命中返回 KindUnknown。
//
// 纯函数、无副作用、零分配热路径（仅一次 text 规范化复用）。
// kind 不参与 TaskType 分类，也不影响 LLM 兜底——只有 role_llm_router
// 消费它（R48，AUTO_ROLE_ROUTING_ENABLED 开启时）。
func ClassifyTaskKind(sigs ClassificationSignals) TaskKind {
	text := normaliseForKeyword(sigs.LastUserPrompt, sigs.SystemPrompt)
	if strings.TrimSpace(text) == "" {
		return KindUnknown
	}
	for _, ch := range taskKindChannels {
		if containsAnyPhrase(text, ch.phrases) {
			return ch.kind
		}
		for _, w := range ch.enWords {
			if containsASCIIMatchWord(text, w) {
				return ch.kind
			}
		}
	}
	return KindUnknown
}

// containsASCIIMatchWord 报告 ASCII 单词 w 是否在已小写文本中按"词边界"
// 出现：前后字符都不是 ASCII 字母/数字。中文文本没有词边界概念，中文
// 短语一律走 phrases 子串通道；本函数只服务英文单词，避免 "git" 命中
// "digit"、"deploy" 命中 "deployment is fine" 之外的 "redeploying" 类
// 子串误伤（"redeploying" 词首是 'r'，按边界匹配不命中 "deploy"——
// 这是刻意保守：前缀变形词让 ops 通道过宽）。
func containsASCIIMatchWord(loweredText, word string) bool {
	if word == "" || loweredText == "" {
		return false
	}
	for i := 0; i+len(word) <= len(loweredText); i++ {
		if loweredText[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isASCIIWordByte(loweredText[i-1]) {
			continue
		}
		if end := i + len(word); end < len(loweredText) && isASCIIWordByte(loweredText[end]) {
			continue
		}
		return true
	}
	return false
}

func isASCIIWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
