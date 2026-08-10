package autoroute

import (
	"context"
	"testing"
)

// classifier_prompt_matrix_test.go — 真实提示词分类回归矩阵。
//
// 目的：满足审计要求 #8（"通过不同的提示词进行任务的分类，然后选择模型执行，
// 要确保可用且正确"）的"分类正确性"层面。与 v4/v6_routing_matrix_test.go（验证
// 模型选择）配合，共同覆盖"提示词 → 分类 → 路由"全链路的静态可验证部分。
//
// 设计：
//   - 数据驱动 table test，每个 task type 配 3-4 条真实风格提示词（中/英/口语）
//   - minConfidence 验证分类置信度下限（hard override 类 ≥0.8，keyword 类 ≥0.4）
//   - "trap_*" 用例验证边界不回归（如"review my code"不能误判为 code_audit）
//
// 维护：新增任务类型或调整关键词后，先在此矩阵补用例再跑测试，把矩阵当作
// 分类行为的 golden 基线。任何分类逻辑改动导致矩阵用例失败，都是行为变更信号。

func TestPromptClassificationMatrix(t *testing.T) {
	// 分类矩阵：prompt → (期望 task, 最低置信度)。
	// minConfidence: 0 = 不校验（仅校验 task 正确性）。
	cases := []struct {
		name     string
		sigs     ClassificationSignals
		wantTask TaskType
		minConf  float64
	}{
		// ── chat（普通对话，无明确任务信号）──────────────────────────────
		{"chat_zh_greeting", ClassificationSignals{LastUserPrompt: "你好，今天天气怎么样", MessageCount: 2}, TaskChat, 0},
		{"chat_en_smalltalk", ClassificationSignals{LastUserPrompt: "thanks, that helps a lot", MessageCount: 4}, TaskChat, 0},
		{"chat_zh_opinion", ClassificationSignals{LastUserPrompt: "你觉得这个想法怎么样，有没有什么建议"}, TaskChat, 0},

		// ── reasoning（数学/逻辑/推理）──────────────────────────────────
		{"reasoning_zh_proof", ClassificationSignals{LastUserPrompt: "请证明根号2是无理数，给出完整的推导过程"}, TaskReasoning, 0.4},
		{"reasoning_en_math", ClassificationSignals{LastUserPrompt: "Solve this step by step: if 3x + 7 = 22, find x and explain why"}, TaskReasoning, 0.4},
		{"reasoning_zh_stats", ClassificationSignals{LastUserPrompt: "计算这组数据的期望值和标准差"}, TaskReasoning, 0.4},
		{"reasoning_zh_optimize", ClassificationSignals{LastUserPrompt: "求这个函数的最大值，给出最优解的推导"}, TaskReasoning, 0.4},

		// ── code（编程：关键词/代码块/IDE 指纹）─────────────────────────
		{"code_zh_func", ClassificationSignals{LastUserPrompt: "用 Python 写一个快速排序算法"}, TaskCode, 0.4},
		{"code_en_debug", ClassificationSignals{LastUserPrompt: "fix this bug in my function, it throws a nil pointer exception"}, TaskCode, 0.4},
		{"code_codeblock", ClassificationSignals{LastUserPrompt: "explain what this does", HasCodeBlock: true}, TaskCode, 0.8},
		{"code_ide_cursor", ClassificationSignals{LastUserPrompt: "分析这个函数的时间复杂度", ClientType: "cursor"}, TaskCode, 0.8},
		{"code_refactor", ClassificationSignals{LastUserPrompt: "帮我重构这段代码，提取公共方法"}, TaskCode, 0.4},
		// 中文编程任务盲区覆盖（pattern 层补丁）：动词 + 编程对象 / 语言名 + 动词
		{"code_zh_algo_quicksort", ClassificationSignals{LastUserPrompt: "用 Python 写一个快速排序"}, TaskCode, 0.6},
		{"code_zh_ds_tree", ClassificationSignals{LastUserPrompt: "用 Java 写一个二叉搜索树"}, TaskCode, 0.6},
		{"code_zh_ds_redblack", ClassificationSignals{LastUserPrompt: "写一个红黑树的插入"}, TaskCode, 0.6},
		{"code_zh_lru", ClassificationSignals{LastUserPrompt: "用 go 写一个 LRU 缓存"}, TaskCode, 0.6},
		{"code_zh_threadpool", ClassificationSignals{LastUserPrompt: "写一个线程池"}, TaskCode, 0.6},
		{"code_zh_react_component", ClassificationSignals{LastUserPrompt: "帮我用 React 做一个表单组件"}, TaskCode, 0.6},
		{"code_zh_sql", ClassificationSignals{LastUserPrompt: "用 SQL 查询最近七天的订单"}, TaskCode, 0.6},
		{"code_zh_script_colloquial", ClassificationSignals{LastUserPrompt: "写个脚本批量重命名文件"}, TaskCode, 0.6},

		// ── agent（多工具 + 工具结果）───────────────────────────────────
		{"agent_multitool", ClassificationSignals{ToolCount: 5, HasToolResults: true, LastUserPrompt: "search the web, read the file, then summarize"}, TaskAgent, 0.8},

		// ── creative（写作/翻译/总结，非超长）───────────────────────────
		{"creative_zh_write", ClassificationSignals{LastUserPrompt: "写一篇关于人工智能的博客文章"}, TaskCreative, 0.4},
		{"creative_en_translate", ClassificationSignals{LastUserPrompt: "translate this paragraph into English"}, TaskCreative, 0.4},
		{"creative_zh_summary_short", ClassificationSignals{LastUserPrompt: "帮我总结一下这段话的要点", EstimatedTokens: 800}, TaskCreative, 0.4},

		// ── long_context（超长纯文本，无编程信号）──────────────────────
		{"longcontext_zh_doc", ClassificationSignals{LastUserPrompt: "请总结这份会议纪要的要点", EstimatedTokens: 80_000}, TaskLongContext, 0.8},
		{"longcontext_en_paper", ClassificationSignals{LastUserPrompt: "summarise the attached research paper", EstimatedTokens: 60_000}, TaskLongContext, 0.8},

		// ── vision（含图片）─────────────────────────────────────────────
		{"vision_image", ClassificationSignals{HasImages: true, LastUserPrompt: "describe what's in this picture"}, TaskVision, 0.9},
		{"vision_image_zh", ClassificationSignals{HasImages: true, LastUserPrompt: "这张图里是什么"}, TaskVision, 0.9},

		// ── function_call（1-2 个工具，无工具结果）─────────────────────
		{"functioncall_single", ClassificationSignals{ToolCount: 1, LastUserPrompt: "what's the weather in Tokyo"}, TaskFunctionCall, 0.7},
		{"functioncall_double", ClassificationSignals{ToolCount: 2, LastUserPrompt: "check my calendar and set a reminder"}, TaskFunctionCall, 0.7},

		// ── code_audit（显式安全审计，收窄关键词后）────────────────────
		{"codeaudit_zh_security", ClassificationSignals{LastUserPrompt: "请对这段代码做安全审计，检查有没有 SQL 注入漏洞"}, TaskCodeAudit, 0.8},
		{"codeaudit_en_security", ClassificationSignals{LastUserPrompt: "perform a security audit and vulnerability scan on this module"}, TaskCodeAudit, 0.8},
		{"codeaudit_zh_quality", ClassificationSignals{LastUserPrompt: "做一次代码质量检查和代码审查，列出所有问题"}, TaskCodeAudit, 0.8},

		// ── intent_classification（意图分类/文本分类）─────────────────
		{"intent_zh_classify", ClassificationSignals{LastUserPrompt: "请对这条用户消息进行意图分类，判断是咨询、投诉还是建议"}, TaskIntentClassification, 0.8},
		{"intent_en_classify", ClassificationSignals{LastUserPrompt: "classify the intent of this user query into one of the predefined categories"}, TaskIntentClassification, 0.8},
		{"intent_zh_detect", ClassificationSignals{LastUserPrompt: "识别以下文本的意图并归类"}, TaskIntentClassification, 0.8},

		// ── planning（方案/规划/任务拆解，不含"然后实现"）──────────────
		{"planning_zh_arch", ClassificationSignals{LastUserPrompt: "帮我写一份微服务架构方案，拆解各模块任务和技术选型"}, TaskPlanning, 0.8},
		{"planning_zh_breakdown", ClassificationSignals{LastUserPrompt: "制定这个项目的实施计划，做任务拆解和工作分解"}, TaskPlanning, 0.8},
		{"planning_en_designdoc", ClassificationSignals{LastUserPrompt: "Write a technical design doc for the new billing system, break down the work into phases"}, TaskPlanning, 0.8},
		{"planning_en_roadmap", ClassificationSignals{LastUserPrompt: "create a roadmap and project plan for Q3, define milestones"}, TaskPlanning, 0.8},

		// ── trap_*：边界用例，验证不回归（wantTask 是"正确"归类）────────
		// "review my code" 应归 code，不应被误判为 code_audit（关键词收窄后）
		{"trap_review_my_code_is_code", ClassificationSignals{LastUserPrompt: "please review my code and suggest improvements"}, TaskCode, 0},
		// "先制定计划然后实现" 应归 code（strongCodingSignal 优先于 planning）
		{"trap_plan_then_implement_is_code", ClassificationSignals{LastUserPrompt: "实现一个用户认证系统，先制定详细的计划，然后逐步实现每个模块"}, TaskCode, 0},
		// 普通对话含"判断"，不应误判为 intent_classification（关键词收窄后）
		{"trap_judge_opinion_is_chat", ClassificationSignals{LastUserPrompt: "帮我判断一下这个决定对不对"}, TaskChat, 0},
		// 短总结应归 creative（token 未超 long_context 阈值）
		{"trap_short_summary_is_creative", ClassificationSignals{LastUserPrompt: "帮我总结一下这篇文章", EstimatedTokens: 2000}, TaskCreative, 0},
		// 超长编程请求应归 code（编程强信号优先于 long_context）
		{"trap_long_coding_is_code", ClassificationSignals{LastUserPrompt: "请帮我重构这段 Python 代码", EstimatedTokens: 60_000, HasCodeBlock: true}, TaskCode, 0.8},
		// code pattern 补丁的不误伤：含"写一个"但对象是故事/方案/博客，不能误判 code
		{"trap_write_story_is_creative", ClassificationSignals{LastUserPrompt: "写一个关于太空探索的故事"}, TaskCreative, 0},
		{"trap_write_blog_is_creative", ClassificationSignals{LastUserPrompt: "写一篇关于 AI 的博客文章"}, TaskCreative, 0},
		{"trap_write_plan_is_planning", ClassificationSignals{LastUserPrompt: "帮我写一份微服务架构方案，拆解各模块任务"}, TaskPlanning, 0},
	}

	c := NewHeuristicClassifier(DefaultHeuristicThresholds(), DefaultKeywords())
	ctx := context.Background()

	pass, fail := 0, 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.Classify(ctx, tc.sigs)
			if err != nil {
				fail++
				t.Fatalf("Classify error: %v", err)
			}
			if res.Primary != tc.wantTask {
				fail++
				t.Errorf("task = %q, want %q (confidence=%.2f, reason=%s)",
					res.Primary, tc.wantTask, res.Confidence, res.Reason)
				return
			}
			if tc.minConf > 0 && res.Confidence < tc.minConf {
				fail++
				t.Errorf("confidence %.2f < min %.2f for %q (reason=%s)",
					res.Confidence, tc.minConf, res.Primary, res.Reason)
				return
			}
			pass++
		})
	}

	// 汇总：失败数应在 t.Run 内已记录，这里仅做覆盖度健康检查。
	t.Logf("prompt matrix: %d pass, %d fail (total %d)", pass, fail, len(cases))
}
