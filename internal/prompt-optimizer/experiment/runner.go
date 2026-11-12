package experiment

// runner.go 实现效果实验的运行器。
//
// 口径：
//   - 同一个执行模型、同一个温度（默认 0）跑所有臂 ⇒ **提示词是唯一自变量**；
//   - 每个 (任务, 臂) 组合跑 Repetitions 次，统计通过率；
//   - 执行失败（模型不可达）单独计入 Errors，不计入分母的"失败"，
//     但会在报告里显式列出，避免把"跑挂了"读成"做错了"。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Executor 是执行模型通道。
type Executor interface {
	// Complete 给定提示词，返回模型输出。
	Complete(ctx context.Context, prompt string) (string, error)
	// Model 返回执行模型名（写进报告，便于复现）。
	Model() string
}

// Arm 是实验的一个臂（一种提示词来源）。
type Arm struct {
	// Name 臂名，如 "baseline" / "rule_optimized" / "llm_optimized"。
	Name string
	// Build 为任务构造该臂使用的提示词。
	Build func(ctx context.Context, t Task) (string, error)
}

// Result 是单个 (任务, 臂, 次) 的结果。
type Result struct {
	TaskID    string `json:"task_id"`
	TaskTitle string `json:"task_title"`
	Arm       string `json:"arm"`
	Repeat    int    `json:"repeat"`

	Prompt string `json:"prompt"`
	Output string `json:"output"`

	Passed bool   `json:"passed"`
	Reason string `json:"reason"`

	LatencyMS   int64 `json:"latency_ms"`
	PromptChars int   `json:"prompt_chars"`
	OutputChars int   `json:"output_chars"`

	// Error 非空表示执行失败（模型不可达等），此时 Passed 无意义。
	Error string `json:"error,omitempty"`
}

// ArmSummary 是单臂汇总。
type ArmSummary struct {
	Arm      string  `json:"arm"`
	Total    int     `json:"total"`
	Passed   int     `json:"passed"`
	Errors   int     `json:"errors"`
	Accuracy float64 `json:"accuracy"`

	AvgLatencyMS   float64 `json:"avg_latency_ms"`
	AvgPromptChars float64 `json:"avg_prompt_chars"`
	AvgOutputChars float64 `json:"avg_output_chars"`
}

// TaskArmStat 是单个 (任务, 臂) 的明细统计。
type TaskArmStat struct {
	TaskID string `json:"task_id"`
	Arm    string `json:"arm"`
	Total  int    `json:"total"`
	Passed int    `json:"passed"`

	// DistinctOutputs 是该组合下**不同输出**的个数。
	//
	// 它是"量具噪声"的直接读数：同一个提示词跑多次，若输出不完全相同，
	// 说明执行侧本身有随机性，n=1 的臂间差异就不能当作效果差异。
	DistinctOutputs int `json:"distinct_outputs"`
}

// Report 是实验报告。
type Report struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`

	ExecutorModel       string   `json:"executor_model"`
	ExecutorTemperature float64  `json:"executor_temperature"`
	Repetitions         int      `json:"repetitions"`
	Arms                []string `json:"arms"`

	Results   []Result      `json:"results"`
	Summaries []ArmSummary  `json:"summaries"`
	TaskStats []TaskArmStat `json:"task_stats"`

	// Notes 是写进报告的人工说明（口径声明等）。
	Notes []string `json:"notes,omitempty"`
}

// Config 是运行配置。
type Config struct {
	// Tasks 任务集。
	Tasks []Task
	// Arms 实验臂。
	Arms []Arm
	// Executor 执行模型通道。
	Executor Executor
	// Repetitions 每个 (任务, 臂) 的重复次数，<=1 时按 1 处理。
	Repetitions int
	// Temperature 执行温度，写进报告。
	Temperature float64
	// Notes 报告说明。
	Notes []string
	// Progress 进度回调（可为 nil）。
	Progress func(res Result)
	// Concurrency 并发执行的臂数；<=1 时串行。
	//
	// 默认串行：同一个执行模型并发打分会让温度=0 的确定性假设失效，
	// 也会把机器负载差异混进延迟统计。
	Concurrency int
}

// Run 执行实验。
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if len(cfg.Tasks) == 0 {
		return nil, fmt.Errorf("experiment: tasks must not be empty")
	}
	if len(cfg.Arms) == 0 {
		return nil, fmt.Errorf("experiment: arms must not be empty")
	}
	if cfg.Executor == nil {
		return nil, fmt.Errorf("experiment: executor must not be empty")
	}

	reps := cfg.Repetitions
	if reps <= 0 {
		reps = 1
	}

	report := &Report{
		StartedAt:           time.Now().UTC(),
		ExecutorModel:       cfg.Executor.Model(),
		ExecutorTemperature: cfg.Temperature,
		Repetitions:         reps,
		Notes:               cfg.Notes,
	}
	for _, arm := range cfg.Arms {
		report.Arms = append(report.Arms, arm.Name)
	}

	type job struct {
		arm  Arm
		task Task
	}

	var jobs []job
	for _, arm := range cfg.Arms {
		for _, task := range cfg.Tasks {
			jobs = append(jobs, job{arm: arm, task: task})
		}
	}

	results := make([]Result, 0, len(jobs)*reps)

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		sem    = make(chan struct{}, concurrency)
		runErr error
	)

	for _, j := range jobs {
		for rep := 1; rep <= reps; rep++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(j job, rep int) {
				defer wg.Done()
				defer func() { <-sem }()

				res := runOne(ctx, cfg, j.arm, j.task, rep)

				mu.Lock()
				results = append(results, res)
				if cfg.Progress != nil {
					cfg.Progress(res)
				}
				mu.Unlock()
			}(j, rep)
		}
	}
	wg.Wait()

	if runErr != nil {
		return nil, runErr
	}

	// 稳定排序：按臂顺序 → 任务顺序 → 重复序号，便于逐行比对。
	armIndex := map[string]int{}
	for i, a := range cfg.Arms {
		armIndex[a.Name] = i
	}
	taskIndex := map[string]int{}
	for i, t := range cfg.Tasks {
		taskIndex[t.ID] = i
	}
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i], results[j]
		if armIndex[ri.Arm] != armIndex[rj.Arm] {
			return armIndex[ri.Arm] < armIndex[rj.Arm]
		}
		if taskIndex[ri.TaskID] != taskIndex[rj.TaskID] {
			return taskIndex[ri.TaskID] < taskIndex[rj.TaskID]
		}
		return ri.Repeat < rj.Repeat
	})

	report.Results = results
	report.FinishedAt = time.Now().UTC()
	report.Summaries = summarize(cfg.Arms, results)
	report.TaskStats = taskStats(cfg.Arms, cfg.Tasks, results)
	return report, nil
}

// runOne 跑单个 (臂, 任务, 重复)。
func runOne(ctx context.Context, cfg Config, arm Arm, task Task, rep int) Result {
	res := Result{
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Arm:       arm.Name,
		Repeat:    rep,
	}

	prompt, err := arm.Build(ctx, task)
	if err != nil {
		res.Error = "build prompt: " + err.Error()
		return res
	}
	res.Prompt = prompt
	res.PromptChars = len([]rune(prompt))

	start := time.Now()
	output, err := cfg.Executor.Complete(ctx, prompt)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = "execute: " + err.Error()
		return res
	}

	res.Output = output
	res.OutputChars = len([]rune(output))

	verdict := task.Judge(output)
	res.Passed = verdict.Passed
	res.Reason = verdict.Reason
	return res
}

// taskStats 汇总每个 (任务, 臂) 的通过数与输出变体数。
func taskStats(arms []Arm, tasks []Task, results []Result) []TaskArmStat {
	type bucket struct {
		total, passed int
		outputs       map[string]struct{}
	}
	buckets := map[string]*bucket{}

	for _, r := range results {
		key := r.Arm + "|" + r.TaskID
		b := buckets[key]
		if b == nil {
			b = &bucket{outputs: map[string]struct{}{}}
			buckets[key] = b
		}
		b.total++
		if r.Error == "" && r.Passed {
			b.passed++
		}
		if r.Error == "" {
			b.outputs[r.Output] = struct{}{}
		}
	}

	out := make([]TaskArmStat, 0, len(arms)*len(tasks))
	for _, arm := range arms {
		for _, task := range tasks {
			b := buckets[arm.Name+"|"+task.ID]
			if b == nil {
				out = append(out, TaskArmStat{TaskID: task.ID, Arm: arm.Name})
				continue
			}
			out = append(out, TaskArmStat{
				TaskID:          task.ID,
				Arm:             arm.Name,
				Total:           b.total,
				Passed:          b.passed,
				DistinctOutputs: len(b.outputs),
			})
		}
	}
	return out
}

// summarize 汇总各臂。
func summarize(arms []Arm, results []Result) []ArmSummary {
	type acc struct {
		total, passed, errors int
		latency               int64
		promptChars           int
		outputChars           int
	}
	buckets := map[string]*acc{}
	for _, r := range results {
		b := buckets[r.Arm]
		if b == nil {
			b = &acc{}
			buckets[r.Arm] = b
		}
		b.total++
		if r.Error != "" {
			b.errors++
			continue
		}
		if r.Passed {
			b.passed++
		}
		b.latency += r.LatencyMS
		b.promptChars += r.PromptChars
		b.outputChars += r.OutputChars
	}

	out := make([]ArmSummary, 0, len(arms))
	for _, arm := range arms {
		b := buckets[arm.Name]
		if b == nil {
			out = append(out, ArmSummary{Arm: arm.Name})
			continue
		}
		s := ArmSummary{
			Arm:    arm.Name,
			Total:  b.total,
			Passed: b.passed,
			Errors: b.errors,
		}
		// 分母是"成功执行"的次数：执行失败单独报，不当"做错"计入。
		graded := b.total - b.errors
		if graded > 0 {
			s.Accuracy = float64(b.passed) / float64(graded)
			s.AvgLatencyMS = float64(b.latency) / float64(graded)
			s.AvgPromptChars = float64(b.promptChars) / float64(graded)
			s.AvgOutputChars = float64(b.outputChars) / float64(graded)
		}
		out = append(out, s)
	}
	return out
}

// Markdown 渲染成 markdown 报告。
func (r *Report) Markdown() string {
	var b strings.Builder

	b.WriteString("# 提示词优化端到端效果实验报告\n\n")
	b.WriteString("## 一、实验设置\n\n")
	b.WriteString("| 项 | 值 |\n|---|---|\n")
	b.WriteString(fmt.Sprintf("| 执行模型 | `%s` |\n", r.ExecutorModel))
	b.WriteString(fmt.Sprintf("| 执行温度 | %g |\n", r.ExecutorTemperature))
	b.WriteString(fmt.Sprintf("| 重复次数 | %d |\n", r.Repetitions))
	b.WriteString(fmt.Sprintf("| 实验臂 | %s |\n", strings.Join(r.Arms, ", ")))
	b.WriteString(fmt.Sprintf("| 开始时间 | %s |\n", r.StartedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("| 结束时间 | %s |\n", r.FinishedAt.Format(time.RFC3339)))
	b.WriteString("| 判据口径 | 规则判定（正则/JSON 解析/计数），不依赖模型打分 |\n\n")

	if len(r.Notes) > 0 {
		b.WriteString("口径声明：\n\n")
		for _, n := range r.Notes {
			b.WriteString("- " + n + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## 二、各臂汇总\n\n")
	b.WriteString("| 臂 | 执行数 | 通过 | 执行错误 | 正确率 | 平均延迟(ms) | 平均提示词字符 | 平均输出字符 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, s := range r.Summaries {
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %d | %.1f%% | %.0f | %.0f | %.0f |\n",
			s.Arm, s.Total, s.Passed, s.Errors, s.Accuracy*100,
			s.AvgLatencyMS, s.AvgPromptChars, s.AvgOutputChars))
	}
	b.WriteString("\n正确率分母 = 执行数 − 执行错误数；执行错误（模型不可达等）不计入分母，单独列出。\n\n")

	b.WriteString("## 三、逐任务 × 逐臂 通过矩阵\n\n")
	b.WriteString("| 任务 |")
	for _, arm := range r.Arms {
		b.WriteString(" " + arm + " |")
	}
	b.WriteString("\n|---|---|")
	for range r.Arms {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, task := range r.taskOrder() {
		b.WriteString("| " + task + " |")
		for _, arm := range r.Arms {
			stat, ok := r.statFor(task, arm)
			if !ok {
				b.WriteString(" – |")
				continue
			}
			b.WriteString(fmt.Sprintf(" %d/%d |", stat.Passed, stat.Total))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n括号内为「通过次数 / 执行次数」。\n\n")

	b.WriteString("## 四、逐任务结果\n\n")
	for _, arm := range r.Arms {
		b.WriteString("### 臂：" + arm + "\n\n")
		b.WriteString("| 任务 | 通过 | 判定理由 | 输出(截断) |\n|---|---|---|---|\n")
		for _, res := range r.Results {
			if res.Arm != arm {
				continue
			}
			mark := "❌"
			reason := res.Reason
			if res.Error != "" {
				mark = "⚠️"
				reason = res.Error
			} else if res.Passed {
				mark = "✅"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | `%s` |\n",
				res.TaskTitle, mark, escapeCell(reason), escapeCell(truncate(res.Output, 120))))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// taskOrder 返回报告中出现过的任务顺序（按首次出现）。
func (r *Report) taskOrder() []string {
	seen := map[string]bool{}
	var out []string
	for _, res := range r.Results {
		if !seen[res.TaskID] {
			seen[res.TaskID] = true
			out = append(out, res.TaskID)
		}
	}
	return out
}

// statFor 查某个 (任务, 臂) 的明细统计。
func (r *Report) statFor(taskID, arm string) (TaskArmStat, bool) {
	for _, s := range r.TaskStats {
		if s.TaskID == taskID && s.Arm == arm {
			return s, true
		}
	}
	return TaskArmStat{}, false
}

// escapeCell 转义表格单元格里的竖线与换行。
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// truncate 截断字符串。
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Rescore 用**当前**判据重新判定一份已有报告，并重算汇总。
//
// 为什么需要它：判据口径改动后，重跑模型既慢又没有必要 ——
// 报告 JSON 里保存了每一条的原始输出，重算判据是纯离线操作。
//
// 这条能力同时是**纪律工具**：判据一改，就能立刻看到"改判据把结论改成什么样"，
// 而不是重跑一遍再挑一个好看的数字。旧判据的结果与新判据的结果必须同时披露。
//
// 输出中的 Output / LatencyMS 等原始观测保持不变，只有 Passed/Reason 被重算。
func Rescore(report *Report, tasks []Task) *Report {
	if report == nil {
		return nil
	}
	taskByID := map[string]Task{}
	for _, t := range tasks {
		taskByID[t.ID] = t
	}

	out := *report
	out.Results = make([]Result, len(report.Results))
	copy(out.Results, report.Results)

	for i := range out.Results {
		t, ok := taskByID[out.Results[i].TaskID]
		if !ok {
			// 任务集里没有这个任务 ⇒ 保留原判定，但标注出来。
			out.Results[i].Reason = "（任务不在当前任务集中，保留原判定）" + out.Results[i].Reason
			continue
		}
		if out.Results[i].Error != "" {
			continue // 执行错误不参与判定
		}
		verdict := t.Judge(out.Results[i].Output)
		out.Results[i].Passed = verdict.Passed
		out.Results[i].Reason = verdict.Reason
	}

	arms := make([]Arm, 0, len(report.Arms))
	for _, name := range report.Arms {
		arms = append(arms, Arm{Name: name})
	}
	out.Summaries = summarize(arms, out.Results)
	out.TaskStats = taskStats(arms, tasks, out.Results)
	return &out
}
