// Command prompt-optimizer-experiment 跑提示词优化的端到端效果实验。
//
// 实验设计（口径写在这里，报告里会原样回显）：
//
//	执行模型固定（默认本地 ollama 的内部小模型），温度 0 ⇒ 提示词是唯一自变量。
//	每个 (任务, 臂) 跑 N 次，按"通过率"统计。
//	判据是规则判定（正则/JSON 解析/计数），不依赖模型打分。
//
//	四个臂，各自只改一个变量：
//	  baseline        原始提示词，不做任何优化
//	  rule            规则结构化优化（structural + fewshot），不注入 harness
//	  rule_harness    规则结构化优化 + harness 要素注入（工具/安全/错误处理）
//	  llm_harness     LLM 驱动改写（内部小模型 + memora 历史）+ harness 要素注入
//
// 真实接线：
//   - 优化模型与执行模型都走 OpenAI 兼容协议（内部小模型即 ollama 的 /v1）；
//   - memora 走 v2 fail-closed 组（service JWT + project scope），
//     历史会话与优化产出真的会落库并能被检索回来。
//
// 密钥只从环境变量读，不接受命令行参数 —— 命令行参数会出现在 ps 输出里。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	promptcontext "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/context"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/evaluation"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/experiment"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/harness"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/strategies"
)

func main() {
	var (
		executorBase  = flag.String("executor-base-url", envOr("EXECUTOR_BASE_URL", "http://127.0.0.1:11434/v1"), "执行模型 OpenAI 兼容端点")
		executorModel = flag.String("executor-model", envOr("EXECUTOR_MODEL", "qwen2.5:7b"), "执行模型名")
		executorKey   = flag.String("executor-api-key", os.Getenv("EXECUTOR_API_KEY"), "执行模型 API Key")
		executorTemp  = flag.Float64("executor-temperature", 0, "执行温度（默认 0，保证提示词是唯一自变量）")
		executorToks  = flag.Int("executor-max-tokens", 1024, "执行输出上限")

		optimizerBase  = flag.String("optimizer-base-url", envOr("OPTIMIZER_BASE_URL", *executorBase), "优化模型 OpenAI 兼容端点")
		optimizerModel = flag.String("optimizer-model", envOr("OPTIMIZER_MODEL", *executorModel), "优化模型名")
		optimizerKey   = flag.String("optimizer-api-key", os.Getenv("OPTIMIZER_API_KEY"), "优化模型 API Key")

		memoraBase    = flag.String("memora-base-url", envOr("MEMORA_BASE_URL", "http://127.0.0.1:12113"), "Memora 服务地址")
		memoraProject = flag.String("memora-project", envOr("MEMORA_PROJECT_ID", "prompt-optimizer"), "Memora 项目作用域")
		memoraActor   = flag.String("memora-actor", envOr("MEMORA_ACTOR_ID", "local-ops"), "Memora 授权主体（ACL 判定对象）")
		memoraTenant  = flag.String("memora-tenant", envOr("MEMORA_TENANT_ID", "default"), "Memora 租户")
		memoraSecret  = flag.String("memora-jwt-secret-file", os.Getenv("MEMORA_JWT_SECRET_FILE"), "Memora HS256 密钥文件（也可用环境变量 MEMORA_JWT_SECRET）")

		armsFlag  = flag.String("arms", "baseline,rule,rule_harness,llm_harness", "要跑的臂，逗号分隔")
		reps      = flag.Int("repetitions", 1, "每个 (任务,臂) 的重复次数")
		outDir    = flag.String("out", "", "报告输出目录（默认 docs/audit/prompt-optimizer-experiment-<时间戳>）")
		writeBack = flag.Bool("write-back", false, "把优化产出与实验结论写回 Memora")
		useMemora = flag.Bool("use-memora", true, "优化阶段是否检索 Memora 历史")
		dryRun    = flag.Bool("dry-run", false, "只打印将要跑的臂，不调模型")
		rescoreIn = flag.String("rescore", "", "只对已有报告 JSON 用当前判据重算（不调模型），写入 --out 目录")
	)
	flag.Parse()

	// 重算模式：不调任何模型，只按当前判据重新判定并重出报告。
	//
	// 用途：判据口径改了以后，重跑模型既慢又没必要 —— 报告里存着每条的
	// 原始输出，重算判据是纯离线操作。这样"改判据把结论改成什么样"是可查的。
	if *rescoreIn != "" {
		if err := runRescore(*rescoreIn, *outDir); err != nil {
			fail("重算失败: " + err.Error())
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	notes := []string{
		"判据只检查**原始提示词里已经隐含**的要求（格式/数量/事实/纯净度），不发明新需求。",
		"判据是规则判定（正则 / JSON 解析 / 计数），不依赖模型打分，可复算。",
		fmt.Sprintf("执行模型 %s，温度 %g —— 提示词是唯一自变量。", *executorModel, *executorTemp),
		"正确率分母 = 执行数 − 执行错误数；执行错误单独列出，不当作「做错」。",
	}

	tasks := experiment.DefaultTasks()
	selected, err := parseArms(*armsFlag)
	if err != nil {
		fail(err.Error())
	}

	fmt.Printf("任务数 %d · 臂 %s · 重复 %d · 执行模型 %s\n",
		len(tasks), strings.Join(selected, ", "), *reps, *executorModel)

	// Memora 客户端（密钥只来自环境变量/文件）。
	var memoraClient *memora.Client
	secret, err := resolveSecret(*memoraSecret)
	if err != nil {
		fmt.Printf("⚠️  Memora 不可用：%v\n（继续跑实验，历史增强将被跳过）\n", err)
		notes = append(notes, "本次运行**未接入 Memora**（密钥不可得），优化阶段无历史增强。")
	} else {
		memoraClient, err = memora.New(memora.Config{
			BaseURL:   *memoraBase,
			JWTSecret: secret,
			Issuer:    envOr("MEMORA_V2_JWT_ISSUER", memora.DefaultIssuer),
			Audience:  envOr("MEMORA_V2_JWT_AUDIENCE", memora.DefaultAudience),
			TenantID:  *memoraTenant,
			ActorID:   *memoraActor,
			ProjectID: *memoraProject,
		})
		if err != nil {
			fmt.Printf("⚠️  Memora 客户端构造失败：%v\n", err)
			notes = append(notes, "本次运行**未接入 Memora**（客户端构造失败），优化阶段无历史增强。")
			memoraClient = nil
		}
	}

	// 优化模型通道。
	optClient, err := llm.New(llm.Config{
		BaseURL: *optimizerBase,
		APIKey:  *optimizerKey,
		Model:   *optimizerModel,
	})
	if err != nil {
		fail("优化模型通道初始化失败: " + err.Error())
	}

	// 构造各臂。
	arms := make([]experiment.Arm, 0, len(selected))
	for _, name := range selected {
		arm, err := buildArm(name, optClient, memoraClient, *useMemora)
		if err != nil {
			fail(err.Error())
		}
		arms = append(arms, arm)
	}

	if *dryRun {
		for _, arm := range arms {
			fmt.Printf("臂 %s 已就绪\n", arm.Name)
		}
		return
	}

	// 执行模型。
	exec, err := experiment.NewLLMExecutor(experiment.ExecutorConfig{
		BaseURL:     *executorBase,
		APIKey:      *executorKey,
		Model:       *executorModel,
		Temperature: *executorTemp,
		MaxTokens:   *executorToks,
	})
	if err != nil {
		fail(err.Error())
	}

	start := time.Now()
	report, err := experiment.Run(ctx, experiment.Config{
		Tasks:       tasks,
		Arms:        arms,
		Executor:    exec,
		Repetitions: *reps,
		Temperature: *executorTemp,
		Notes:       notes,
		Progress: func(res experiment.Result) {
			mark := "❌"
			if res.Error != "" {
				mark = "⚠️"
			} else if res.Passed {
				mark = "✅"
			}
			fmt.Printf("  %s %-14s %-22s %6dms  %s\n", mark, res.Arm, res.TaskID, res.LatencyMS, res.Reason)
		},
	})
	if err != nil {
		fail("实验运行失败: " + err.Error())
	}

	// 输出汇总。
	fmt.Println("\n=== 各臂汇总 ===")
	for _, s := range report.Summaries {
		fmt.Printf("%-14s 执行 %d · 通过 %d · 错误 %d · 正确率 %.1f%% · 平均延迟 %.0fms\n",
			s.Arm, s.Total, s.Passed, s.Errors, s.Accuracy*100, s.AvgLatencyMS)
	}

	// 写回 Memora（可选）：把优化产出与实验结论落库，下一轮可被检索回来。
	if *writeBack && memoraClient != nil {
		if err := writeBackToMemora(ctx, memoraClient, report, tasks); err != nil {
			fmt.Printf("⚠️  写回 Memora 失败：%v\n", err)
			notes = append(notes, "写回 Memora 失败："+err.Error())
		} else {
			fmt.Println("\n已写回 Memora（优化产出 + 实验结论）")
		}
	}

	// 落报告。
	dir := *outDir
	if dir == "" {
		dir = filepath.Join("docs", "audit", "prompt-optimizer-experiment-"+time.Now().Format("20060102-150405"))
	}
	if err := writeReports(dir, report); err != nil {
		fail("写报告失败: " + err.Error())
	}
	fmt.Printf("\n报告已写入 %s （总耗时 %s）\n", dir, time.Since(start).Round(time.Second))
}

// runRescore 对已有报告用当前判据重算。
//
// 这是纪律工具：判据口径一改，立刻能看到结论怎么变，
// 不需要重跑模型（报告里存着每条的原始输出）。
func runRescore(inPath, outDir string) error {
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("读取报告失败: %w", err)
	}
	var report experiment.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return fmt.Errorf("解析报告失败: %w", err)
	}
	rescored := experiment.Rescore(&report, experiment.DefaultTasks())
	if rescored == nil {
		return fmt.Errorf("报告为空")
	}
	rescored.Notes = append(rescored.Notes,
		"本报告由 --rescore 离线重算：沿用原报告的模型输出，只用当前判据重新判定。")

	if outDir == "" {
		outDir = filepath.Join(filepath.Dir(inPath), "rescored")
	}
	if err := writeReports(outDir, rescored); err != nil {
		return err
	}
	fmt.Println("重算结果：")
	for _, s := range rescored.Summaries {
		fmt.Printf("%-14s 执行 %d · 通过 %d · 正确率 %.1f%%\n",
			s.Arm, s.Total, s.Passed, s.Accuracy*100)
	}
	fmt.Printf("\n已写入 %s\n", outDir)
	return nil
}

// parseArms 解析臂列表。
func parseArms(raw string) ([]string, error) {
	known := map[string]bool{
		"baseline": true, "rule": true, "rule_harness": true, "llm_harness": true,
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !known[name] {
			return nil, fmt.Errorf("未知臂 %q（可选: baseline, rule, rule_harness, llm_harness）", name)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("至少要选一个臂")
	}
	return out, nil
}

// buildArm 构造一个实验臂。
//
// 每个臂只改一个变量，这样对比结果能归因到具体机制。
func buildArm(name string, optClient *llm.Client, memClient *memora.Client, useMemora bool) (experiment.Arm, error) {
	build := func(strats []optimizer.Strategy, opts optimizer.OptimizeOptions) func(context.Context, experiment.Task) (string, error) {
		return func(ctx context.Context, t experiment.Task) (string, error) {
			o := optimizer.NewOptimizer(
				optimizer.WithStrategies(strats...),
				optimizer.WithHarnessInjector(harness.NewInjector(
					harness.ProductionInjectorConfig(),
					harness.NewMockToolRegistry(),
					harness.NewMockSafetyRules(),
				)),
				optimizer.WithContextManager(contextManager(memClient)),
				optimizer.WithEvaluator(evaluation.NewEvaluator(evaluation.DefaultEvaluatorConfig(), evaluation.DefaultMetrics(), nil)),
			)
			res, err := o.Optimize(ctx, t.Prompt, optimizer.OptimizeOptions{
				TaskType:      t.TaskType,
				Domain:        t.Domain,
				UseMemora:     opts.UseMemora,
				InjectHarness: opts.InjectHarness,
			})
			if err != nil {
				return "", err
			}
			return res.Optimized, nil
		}
	}

	switch name {
	case "baseline":
		return experiment.Arm{
			Name: name,
			Build: func(_ context.Context, t experiment.Task) (string, error) {
				return t.Prompt, nil
			},
		}, nil

	case "rule":
		return experiment.Arm{
			Name: name,
			Build: build([]optimizer.Strategy{
				strategies.NewStructural(strategies.DefaultStructuralConfig()),
				strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil),
			}, optimizer.OptimizeOptions{UseMemora: useMemora}),
		}, nil

	case "rule_harness":
		return experiment.Arm{
			Name: name,
			Build: build([]optimizer.Strategy{
				strategies.NewStructural(strategies.DefaultStructuralConfig()),
				strategies.NewFewShot(strategies.DefaultFewShotConfig(), nil),
			}, optimizer.OptimizeOptions{UseMemora: useMemora, InjectHarness: true}),
		}, nil

	case "llm_harness":
		return experiment.Arm{
			Name: name,
			Build: build([]optimizer.Strategy{
				strategies.NewLLMRewrite(strategies.DefaultLLMRewriteConfig(), optClient),
			}, optimizer.OptimizeOptions{UseMemora: useMemora, InjectHarness: true}),
		}, nil
	}
	return experiment.Arm{}, fmt.Errorf("未知臂 %q", name)
}

// contextManager 构造上下文管理器（memora 不可用时返回 nil ⇒ 无历史增强）。
func contextManager(memClient *memora.Client) optimizer.ContextManager {
	if memClient == nil {
		return nil
	}
	cfg := promptcontext.DefaultConfig()
	cfg.Enabled = true
	cfg.UserID = memClient.ActorID()
	return promptcontext.NewManager(memClient, cfg)
}

// writeBackToMemora 把优化产出与实验结论写回 Memora。
func writeBackToMemora(ctx context.Context, client *memora.Client, report *experiment.Report, tasks []experiment.Task) error {
	// 按 (臂, 任务) 聚合成一条记忆：记忆里应该是"结论"，不是上百条原始记录。
	type aggKey struct{ arm, taskID string }
	type aggVal struct {
		title   string
		total   int
		passed  int
		reasons []string
	}
	aggs := map[aggKey]*aggVal{}
	var order []aggKey
	for _, res := range report.Results {
		k := aggKey{arm: res.Arm, taskID: res.TaskID}
		v, ok := aggs[k]
		if !ok {
			v = &aggVal{title: res.TaskTitle}
			aggs[k] = v
			order = append(order, k)
		}
		v.total++
		if res.Error == "" && res.Passed {
			v.passed++
		}
		if res.Error == "" && len(v.reasons) < 3 {
			v.reasons = append(v.reasons, res.Reason)
		}
	}

	items := make([]memora.IngestItem, 0, len(order))
	for _, k := range order {
		v := aggs[k]
		items = append(items, memora.IngestItem{
			SourceType: "session_summary",
			SourceID:   fmt.Sprintf("po-exp-%s-%s", k.arm, k.taskID),
			ContentRef: fmt.Sprintf("prompt-optimizer/experiments/%s/%s", k.arm, k.taskID),
			ContentPreview: fmt.Sprintf("[提示词优化实验][臂=%s][任务=%s] 通过 %d/%d ｜ 判定理由样例：%s",
				k.arm, v.title, v.passed, v.total, strings.Join(v.reasons, " / ")),
			Tags:     []string{"prompt-optimizer", "experiment", k.arm, k.taskID},
			Metadata: map[string]any{"arm": k.arm, "task_id": k.taskID, "passed": v.passed, "total": v.total},
			RunID:    "prompt-optimizer-experiment",
			TaskID:   k.taskID,
		})
	}
	if len(items) == 0 {
		return nil
	}
	if _, err := client.Ingest(ctx, "gateway", items); err != nil {
		return err
	}

	// 会话分析：把本轮实验的结论作为一条"会话摘要"投递，
	// memora 会产出 fact/experience/manifest，下一轮检索即可读到。
	summary := fmt.Sprintf("提示词优化效果实验完成：%d 个任务 × %d 个臂。各臂正确率：%s",
		len(tasks), len(report.Arms), summarizeArms(report))
	if _, err := client.PostSessionAnalysis(ctx, memora.SessionAnalysisPayload{
		AnalysisKind:    "session_summary",
		DispatchID:      fmt.Sprintf("po-exp-%d", time.Now().UTC().Unix()),
		Title:           "提示词优化效果实验",
		Summary:         summary,
		Intent:          "prompt_optimization_experiment",
		Outcome:         "completed",
		KeyPoints:       []string{summary},
		SourceSessionID: fmt.Sprintf("po-exp-%d", time.Now().UTC().Unix()),
	}); err != nil {
		return err
	}
	return nil
}

// summarizeArms 生成一行各臂正确率摘要。
func summarizeArms(report *experiment.Report) string {
	parts := make([]string, 0, len(report.Summaries))
	for _, s := range report.Summaries {
		parts = append(parts, fmt.Sprintf("%s=%.1f%%", s.Arm, s.Accuracy*100))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// writeReports 落 JSON + Markdown 报告。
func writeReports(dir string, report *experiment.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	jsonBuf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "experiment-report.json"), append(jsonBuf, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "experiment-report.md"), []byte(report.Markdown()), 0o644)
}

// resolveSecret 读取 Memora 密钥。
//
// 来源优先级：环境变量 MEMORA_JWT_SECRET → --memora-jwt-secret-file 指定的文件。
// 刻意**不提供**命令行明文参数：命令行会出现在 ps 输出与 shell 历史里。
func resolveSecret(path string) (string, error) {
	if s := strings.TrimSpace(os.Getenv("MEMORA_JWT_SECRET")); s != "" {
		return s, nil
	}
	if path != "" {
		buf, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读取密钥文件失败: %w", err)
		}
		s := strings.TrimSpace(string(buf))
		if s == "" {
			return "", fmt.Errorf("密钥文件 %s 是空的", path)
		}
		return s, nil
	}
	return "", fmt.Errorf("未提供 Memora 密钥（环境变量 MEMORA_JWT_SECRET 或 --memora-jwt-secret-file）")
}

// envOr 读取环境变量，缺省时返回 fallback。
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// fail 打印错误并退出。
func fail(msg string) {
	fmt.Fprintf(os.Stderr, "错误: %s\n", msg)
	os.Exit(1)
}
