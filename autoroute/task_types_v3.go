package autoroute

// task_types_v3.go defines the 10-category task classification system
// for AUTO_MODEL V3 optimization.
//
// Related: docs/03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 2.1
//
// This file introduces new task types that provide finer-grained classification
// compared to the legacy 8-category system. The V3 system separates:
//   - architecture (system design) from general reasoning
//   - audit (code review) from general code tasks
//   - debugging (bug investigation) from general code tasks
//   - refactoring (code improvement) from greenfield coding
//   - testing (test generation) from general code tasks
//   - devops (CI/CD, deployment) from general code tasks
//   - documentation (comments, README) from creative writing
//   - summary (code summarization) from documentation
//   - dependency (package management) from devops
//
// The new categories enable more accurate tier mapping:
//   - Tier-A (high-performance): architecture, audit, debugging
//   - Tier-B (standard): coding, refactoring, testing
//   - Tier-C (economy): devops, documentation, summary, dependency

// V3 Task Types (10 categories)
const (
	// TaskArchitecture covers system design, API design, technical proposals,
	// and architecture reviews. Requires deep reasoning about system structure,
	// trade-offs, and design patterns.
	//
	// Key signals:
	//   - "system design", "architecture", "design doc", "API design"
	//   - "technical proposal", "design pattern", "系统设计", "架构"
	//
	// Default tier: tier-a (requires high reasoning capability)
	// Fallback: tier-b
	TaskArchitecture TaskType = "architecture"

	// TaskAudit covers code review, security audit, PR review, and
	// vulnerability analysis. Requires careful analysis of existing code
	// for correctness, security, and best practices.
	//
	// Key signals:
	//   - "review", "audit", "security", "vulnerability", "PR review"
	//   - "代码审查", "安全审计", "review代码"
	//
	// Default tier: tier-a (requires thorough analysis)
	// Fallback: tier-b
	TaskAudit TaskType = "audit"

	// TaskDebugging covers bug investigation, root cause analysis, stack
	// trace debugging, and error diagnosis. Requires analytical thinking
	// to understand failure modes.
	//
	// Key signals:
	//   - "debug", "why", "error", "stack trace", "bug", "doesn't work"
	//   - "调试", "为什么", "错误", "bug", "不工作"
	//
	// Default tier: tier-a (requires strong reasoning, often urgent)
	// Fallback: tier-b
	TaskDebugging TaskType = "debugging"

	// TaskCoding covers greenfield development, feature implementation,
	// and API integration. Standard software development tasks.
	//
	// Key signals:
	//   - Code blocks + "create", "add", "build", "implement"
	//   - "实现", "创建", "构建", "开发"
	//
	// Default tier: tier-b (balanced quality/cost)
	// Fallback: tier-a (escalate if quality issues), tier-c (simple tasks)
	TaskCoding TaskType = "coding"

	// TaskRefactoring covers code restructuring, optimization, and clean-up.
	// Modifying existing code to improve quality without changing behavior.
	//
	// Key signals:
	//   - "refactor", "optimize", "improve", "clean up", "restructure"
	//   - "重构", "优化", "改进", "清理"
	//
	// Default tier: tier-b (requires understanding existing patterns)
	// Fallback: tier-a (complex refactoring), tier-c (simple cleanup)
	TaskRefactoring TaskType = "refactoring"

	// TaskTesting covers unit/integration test generation and test coverage.
	// Creating tests for existing or new code.
	//
	// Key signals:
	//   - "test", "coverage", "mock", "assert", "jest", "pytest"
	//   - "测试", "单元测试", "测试用例", "覆盖率"
	//
	// Default tier: tier-b (requires understanding code behavior)
	// Fallback: tier-c (simple test cases)
	TaskTesting TaskType = "testing"

	// TaskDevOps covers CI/CD, deployment, infrastructure scripting, and
	// container configuration. Operational tasks that are often template-based.
	//
	// Key signals:
	//   - "deploy", "CI/CD", "docker", "k8s", "terraform", "ansible"
	//   - "部署", "容器", "配置", "流水线"
	//
	// Default tier: tier-c (often template-based, high throughput)
	// Fallback: tier-b (complex infrastructure)
	TaskDevOps TaskType = "devops"

	// TaskDocumentation covers comments, README, API docs, and inline
	// documentation. Writing explanatory text about code.
	//
	// Key signals:
	//   - "document", "comment", "explain", "describe", "README"
	//   - "文档", "注释", "说明", "解释"
	//
	// Default tier: tier-c (straightforward explanatory writing)
	// Fallback: none (rarely escalates)
	TaskDocumentation TaskType = "documentation"

	// TaskSummary covers code summarization, session recap, and overview
	// generation. Condensing information into brief descriptions.
	//
	// Key signals:
	//   - "summarize", "explain what", "overview", "recap", "TLDR"
	//   - "总结", "概述", "摘要"
	//
	// Default tier: tier-c (extraction and condensing)
	// Fallback: none (rarely escalates)
	TaskSummary TaskType = "summary"

	// TaskDependency covers dependency analysis, upgrade planning, and
	// package management. Managing project dependencies.
	//
	// Key signals:
	//   - "dependency", "upgrade", "package", "version", "npm", "pip"
	//   - "依赖", "升级", "包管理", "版本"
	//
	// Default tier: tier-c (often straightforward version updates)
	// Fallback: tier-b (complex dependency conflicts)
	TaskDependency TaskType = "dependency"
)

// AllTaskTypesV3 is the canonical list of V3 task types.
// This replaces AllTaskTypes when V3 classification is enabled.
var AllTaskTypesV3 = []TaskType{
	// Tier-A: High-performance tasks
	TaskArchitecture,
	TaskAudit,
	TaskDebugging,

	// Tier-B: Standard tasks
	TaskCoding,
	TaskRefactoring,
	TaskTesting,

	// Tier-C: Economy tasks
	TaskDevOps,
	TaskDocumentation,
	TaskSummary,
	TaskDependency,
}

// V3KeywordSet extends KeywordSet with keywords for the new task categories.
type V3KeywordSet struct {
	Architecture  []string `yaml:"architecture" json:"architecture"`
	Audit         []string `yaml:"audit" json:"audit"`
	Debugging     []string `yaml:"debugging" json:"debugging"`
	Coding        []string `yaml:"coding" json:"coding"`
	Refactoring   []string `yaml:"refactoring" json:"refactoring"`
	Testing       []string `yaml:"testing" json:"testing"`
	DevOps        []string `yaml:"devops" json:"devops"`
	Documentation []string `yaml:"documentation" json:"documentation"`
	Summary       []string `yaml:"summary" json:"summary"`
	Dependency    []string `yaml:"dependency" json:"dependency"`
}

// DefaultV3Keywords returns the built-in keyword set for V3 classification.
// Keywords are curated for precision to minimize misclassification.
func DefaultV3Keywords() V3KeywordSet {
	return V3KeywordSet{
		Architecture: []string{
			// English - strong signals
			"system design", "architecture", "design doc", "API design",
			"technical proposal", "design pattern", "system architecture",
			"architecture review", "design review", "high-level design",
			"architectural", "microservices", "service design",
			// English - weaker but still good
			"design a system", "architect a", "architectural decision",
			"design principles", "scalability design", "distributed system",
			// 中文 - strong signals
			"系统设计", "架构", "设计文档", "API设计", "技术方案",
			"设计模式", "架构评审", "架构设计", "高层设计",
			// 中文 - weaker
			"设计系统", "架构决策", "设计原则", "可扩展性设计",
		},

		Audit: []string{
			// English - strong signals
			"code review", "review code", "review this", "review my code",
			"security audit", "security review", "vulnerability",
			"PR review", "pull request review", "audit this code",
			"review for security", "security check", "code audit",
			// English - weaker
			"check for bugs", "review for issues", "look for problems",
			"security issues", "vulnerabilities", "review the code",
			// 中文 - strong signals
			"代码审查", "审查代码", "review代码", "安全审计",
			"安全审查", "漏洞", "PR审查", "审核代码",
			// 中文 - weaker
			"检查代码", "查看问题", "安全问题", "代码审核",
		},

		Debugging: []string{
			// English - strong signals
			"debug", "stack trace", "traceback", "error at line",
			"why doesn't this work", "why is this failing",
			"fix this bug", "fix the bug", "what's wrong",
			"doesn't work", "not working", "failing", "fails",
			"exception", "crash", "error message", "runtime error",
			// English - weaker
			"investigate", "root cause", "why is", "what causes",
			"troubleshoot", "diagnose", "figure out why",
			// 中文 - strong signals
			"调试", "错误堆栈", "堆栈跟踪", "为什么不工作",
			"为什么失败", "修复bug", "修复这个bug", "哪里错了",
			"不工作", "不起作用", "失败了", "异常", "崩溃",
			"错误信息", "运行错误",
			// 中文 - weaker
			"调查", "根本原因", "为什么", "什么导致", "排查", "诊断",
		},

		Coding: []string{
			// English - strong signals
			"implement", "implement a", "implement the", "create a",
			"build a", "write a function", "write a class",
			"develop a", "code a", "create function", "add feature",
			"implement feature", "build this", "create this",
			// English - weaker (overlap with other categories)
			"write code", "program", "software", "application",
			// 中文 - strong signals
			"实现", "实现一个", "实现以下", "创建", "构建",
			"写一个函数", "写一个类", "开发", "编写代码",
			"实现功能", "添加功能", "构建这个",
			// 中文 - weaker
			"写代码", "编程", "软件", "应用",
		},

		Refactoring: []string{
			// English - strong signals
			"refactor", "refactoring", "restructure", "reorganize",
			"improve this code", "optimize this", "clean up",
			"simplify this", "make this better", "improve the code",
			// English - weaker
			"optimize", "improve", "enhance", "modernize",
			// 中文 - strong signals
			"重构", "重构代码", "重组", "整理", "优化这段代码",
			"改进代码", "清理代码", "简化代码", "让代码更好",
			// 中文 - weaker
			"优化", "改进", "增强", "现代化",
		},

		Testing: []string{
			// English - strong signals
			"unit test", "test case", "test cases", "write test",
			"write tests", "test coverage", "integration test",
			"add tests", "create tests", "test this", "test for",
			// Test frameworks (strong signals)
			"jest", "mocha", "pytest", "unittest", "junit",
			"vitest", "cypress", "playwright",
			// English - weaker
			"test", "testing", "mock", "assert", "expect",
			// 中文 - strong signals
			"单元测试", "测试用例", "写测试", "测试覆盖率",
			"集成测试", "添加测试", "创建测试", "测试这个",
			// 中文 - weaker
			"测试", "测试代码", "mock", "断言",
		},

		DevOps: []string{
			// English - strong signals
			"deploy", "deployment", "CI/CD", "continuous integration",
			"continuous deployment", "docker", "dockerfile",
			"kubernetes", "k8s", "terraform", "ansible",
			"jenkins", "github actions", "gitlab ci",
			"container", "orchestration", "helm", "kubectl",
			// English - weaker
			"infrastructure", "pipeline", "build script", "automation",
			// 中文 - strong signals
			"部署", "持续集成", "持续部署", "容器", "编排",
			"流水线", "自动化部署", "CI/CD流程",
			// 中文 - weaker
			"基础设施", "构建脚本", "自动化",
		},

		Documentation: []string{
			// English - strong signals
			"write documentation", "add comments", "document this",
			"explain this code", "add docstring", "add javadoc",
			"write readme", "api documentation", "generate docs",
			// English - weaker (overlap with summary)
			"comment", "documentation", "explain", "describe",
			// 中文 - strong signals
			"写文档", "添加注释", "记录这个", "解释代码",
			"添加文档字符串", "写README", "API文档", "生成文档",
			// 中文 - weaker
			"注释", "文档", "解释", "说明",
		},

		Summary: []string{
			// English - strong signals
			"summarize", "summarize this", "tldr", "tl;dr",
			"give me a summary", "overview", "recap", "brief",
			"explain what this does", "what does this do",
			"high-level overview", "quick summary",
			// English - weaker
			"summary", "outline", "brief description",
			// 中文 - strong signals
			"总结", "总结一下", "概述", "简要说明",
			"这个做什么", "快速总结", "概要", "摘要",
			// 中文 - weaker
			"简介", "大纲", "简短描述",
		},

		Dependency: []string{
			// English - strong signals
			"upgrade dependencies", "update dependencies",
			"dependency upgrade", "package upgrade", "npm upgrade",
			"pip upgrade", "update packages", "dependency conflict",
			"resolve dependencies", "dependency management",
			// Package managers (strong signals)
			"package.json", "requirements.txt", "go.mod", "Cargo.toml",
			"pom.xml", "build.gradle",
			// English - weaker
			"dependency", "dependencies", "package", "version",
			// 中文 - strong signals
			"升级依赖", "更新依赖", "依赖升级", "包升级",
			"依赖冲突", "解决依赖", "依赖管理",
			// 中文 - weaker
			"依赖", "依赖项", "包", "版本",
		},
	}
}

// TaskTypeTierMapping defines the default tier for each V3 task type.
// This is used as a fallback when task_type_tier_config table is not available.
var TaskTypeTierMapping = map[TaskType]string{
	// Tier-A: High-performance (deep reasoning, complex analysis)
	TaskArchitecture: "tier-a",
	TaskAudit:        "tier-a",
	TaskDebugging:    "tier-a",

	// Tier-B: Standard (balanced quality/cost)
	TaskCoding:      "tier-b",
	TaskRefactoring: "tier-b",
	TaskTesting:     "tier-b",

	// Tier-C: Economy (template-based, high throughput)
	TaskDevOps:        "tier-c",
	TaskDocumentation: "tier-c",
	TaskSummary:       "tier-c",
	TaskDependency:    "tier-c",
}

// MinConfidenceThresholds defines minimum confidence thresholds for each task type.
// Below these thresholds, the classifier should escalate to LLM re-classification.
var MinConfidenceThresholds = map[TaskType]float64{
	TaskArchitecture:  0.70,
	TaskAudit:         0.70,
	TaskDebugging:     0.65, // Lower threshold (often urgent, prefer action)
	TaskCoding:        0.75,
	TaskRefactoring:   0.70,
	TaskTesting:       0.75,
	TaskDevOps:        0.80, // Higher threshold (misclassification costly)
	TaskDocumentation: 0.85, // Higher threshold (overlap with summary)
	TaskSummary:       0.85, // Higher threshold (overlap with documentation)
	TaskDependency:    0.75,
}
