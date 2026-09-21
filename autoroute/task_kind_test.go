package autoroute

// task_kind_test.go — R48（2026-09-20）细粒度任务分类单元测试：
// 中英双语关键词命中、通道优先级、英文词边界（"git"⊂"digit" 类子串
// 误伤防护）、TaskType 语义不受影响。

import "testing"

func kindFor(t *testing.T, lastUser, system string) TaskKind {
	t.Helper()
	return ClassifyTaskKind(ClassificationSignals{
		LastUserPrompt: lastUser,
		SystemPrompt:   system,
	})
}

func TestClassifyTaskKind_Channels(t *testing.T) {
	cases := []struct {
		name string
		zh   string
		en   string
		want TaskKind
	}{
		{"search", "帮我搜索一下 GC 调优的资料", "search for the best practice of go gc", KindSearch},
		{"summarize", "帮我总结一下这篇文章", "summarize the meeting notes below", KindSummarize},
		{"git_ops", "这次 git 提交包含哪些文件", "git commit and push the branch", KindGitOps},
		{"ops", "帮我重启服务并清理磁盘", "kubectl get pods and check disk usage", KindOps},
		{"analysis", "帮我分析一下接口慢的根因", "analyze the root cause of the latency spike", KindAnalysis},
		{"planning", "帮我做个任务拆解和排期", "create a plan with milestones for the migration", KindPlanning},
		{"solution", "帮我写一份技术方案", "propose a solution for the storage bottleneck", KindSolution},
	}
	for _, tc := range cases {
		if got := kindFor(t, tc.zh, ""); got != tc.want {
			t.Errorf("%s zh: ClassifyTaskKind(%q) = %q, want %q", tc.name, tc.zh, got, tc.want)
		}
		if got := kindFor(t, tc.en, ""); got != tc.want {
			t.Errorf("%s en: ClassifyTaskKind(%q) = %q, want %q", tc.name, tc.en, got, tc.want)
		}
	}
}

func TestClassifyTaskKind_SystemPromptFallback(t *testing.T) {
	// user 提示词无信号时扫 system 提示词（与 HeuristicClassifier 同源文本）。
	if got := kindFor(t, "", "你是一个 git 运维助手，负责合并分支"); got != KindGitOps {
		t.Fatalf("system prompt git: got %q, want git_ops", got)
	}
}

func TestClassifyTaskKind_WordBoundary(t *testing.T) {
	// "git" 是 "digit"/"digital" 的子串——必须按词边界匹配，否则搜索类
	// 英文文本会被误判成 git_ops（task_kind.go 的核心防误伤点）。
	cases := []struct {
		text string
		want TaskKind
	}{
		{"count the digits in this document", KindUnknown},
		{"digital library search is broken", KindSearch}, // digital≠git, search 词命中
		{"run git status for me", KindGitOps},
		{"the digit git workflow", KindGitOps},                 // 同句含真 git 词
		{"digitization roadmap for the archive", KindPlanning}, // digitization≠git, roadmap 词命中
	}
	for _, tc := range cases {
		if got := kindFor(t, tc.text, ""); got != tc.want {
			t.Errorf("ClassifyTaskKind(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestClassifyTaskKind_ChannelPriority(t *testing.T) {
	// 通道按"先具体后泛化"排序：git_ops 先于 ops，ops 先于 solution/
	// planning，solution/planning 先于 analysis（边界短语互串时取更具体
	// 的通道标签；同池（solution/planning 都在重量池）互串代价低）。
	cases := []struct {
		text string
		want TaskKind
	}{
		{"帮我提交代码并部署到测试环境", KindGitOps},   // git 先于 ops
		{"写一份部署方案的迁移计划", KindOps},        // "部署"(ops) 先于 solution/planning
		{"分析一下这个方案的可行性", KindAnalysis},   // 无 solution 短语命中，落 analysis
		{"分析一下这份解决方案的可行性", KindSolution}, // "解决方案" 命中 solution 通道（更先）
	}
	for _, tc := range cases {
		if got := kindFor(t, tc.text, ""); got != tc.want {
			t.Errorf("ClassifyTaskKind(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestClassifyTaskKind_Unknown(t *testing.T) {
	// 纯闲聊/空文本 → unknown（role 路由按用户口径走轻量兜底池）。
	for _, text := range []string{"", "今天天气不错", "hello there", "嗯嗯好的"} {
		if got := kindFor(t, text, ""); got != KindUnknown {
			t.Errorf("ClassifyTaskKind(%q) = %q, want unknown", text, got)
		}
	}
	// 全空信号。
	if got := ClassifyTaskKind(ClassificationSignals{}); got != KindUnknown {
		t.Fatalf("zero signals: got %q, want unknown", got)
	}
}

func TestClassifyTaskKind_DoesNotTouchTaskType(t *testing.T) {
	// 正交性回归：kind 分类不改变既有 TaskType 分类结果（同信号分别送两个
	// 分类器，TaskType 输出与 kind 字段存在与否无关——AgentRole 是新增
	// 字段，HeuristicClassifier 不读它）。
	sigs := ClassificationSignals{
		LastUserPrompt: "帮我搜索一下相关资料",
		AgentRole:      RoleWorker,
	}
	cls, err := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords()).Classify(nil, sigs)
	if err != nil {
		t.Fatalf("classify err: %v", err)
	}
	noRole := sigs
	noRole.AgentRole = ""
	cls2, err := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords()).Classify(nil, noRole)
	if err != nil {
		t.Fatalf("classify err: %v", err)
	}
	if cls.Primary != cls2.Primary {
		t.Fatalf("AgentRole must not affect TaskType: %q vs %q", cls.Primary, cls2.Primary)
	}
}
