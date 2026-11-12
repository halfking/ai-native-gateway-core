package experiment

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeExecutor 是假执行模型。
type fakeExecutor struct {
	// reply 按提示词前缀返回；找不到就用 fallback。
	reply    map[string]string
	fallback string
	err      error
	calls    int
}

func (f *fakeExecutor) Model() string { return "fake-model" }

func (f *fakeExecutor) Complete(_ context.Context, prompt string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	for prefix, out := range f.reply {
		if strings.HasPrefix(prompt, prefix) {
			return out, nil
		}
	}
	return f.fallback, nil
}

// prefixedArm 构造一个"提示词带固定前缀"的臂，用来区分不同臂的输出。
func prefixedArm(name, prefix string) Arm {
	return Arm{
		Name: name,
		Build: func(_ context.Context, t Task) (string, error) {
			return prefix + t.ID, nil
		},
	}
}

func TestRunSummariesAndAccuracy(t *testing.T) {
	exec := &fakeExecutor{
		reply: map[string]string{
			// 只有 baseline 臂（提示词以 BASE: 开头）输出合格结果。
			"BASE:t1": `[{"a":1},{"a":2},{"a":3}]`,
		},
		fallback: `[{"a":1}]`,
	}

	tasks := []Task{{
		ID:     "t1",
		Title:  "任务一",
		Prompt: "原始",
		Judge:  judgeJSONArray(3),
	}}

	report, err := Run(context.Background(), Config{
		Tasks: tasks,
		Arms: []Arm{
			prefixedArm("baseline", "BASE:"),
			prefixedArm("optimized", "OPT:"),
		},
		Executor:    exec,
		Repetitions: 1,
		Temperature: 0,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(report.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Results))
	}
	if len(report.Summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(report.Summaries))
	}

	byArm := map[string]ArmSummary{}
	for _, s := range report.Summaries {
		byArm[s.Arm] = s
	}
	base := byArm["baseline"]
	if base.Total != 1 || base.Passed != 1 || base.Accuracy != 1 {
		t.Errorf("baseline summary = %+v", base)
	}
	opt := byArm["optimized"]
	if opt.Total != 1 || opt.Passed != 0 || opt.Accuracy != 0 {
		t.Errorf("optimized summary = %+v", opt)
	}
	if report.ExecutorModel != "fake-model" {
		t.Errorf("executor model = %q", report.ExecutorModel)
	}
}

func TestRunCountsErrorsSeparatelyFromFailures(t *testing.T) {
	exec := &fakeExecutor{err: errors.New("model unreachable")}
	tasks := []Task{{
		ID:    "t1",
		Title: "任务一",
		Judge: func(string) Verdict { return Verdict{true, "always pass"} },
	}}

	report, err := Run(context.Background(), Config{
		Tasks:       tasks,
		Arms:        []Arm{prefixedArm("a", "A:")},
		Executor:    exec,
		Repetitions: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := report.Summaries[0]
	if s.Errors != 2 {
		t.Errorf("errors = %d, want 2", s.Errors)
	}
	// 执行错误不计入正确率分母：分母为 0 时正确率必须是 0 而不是 NaN。
	if s.Total != 2 || s.Passed != 0 {
		t.Errorf("summary = %+v", s)
	}
	if s.Accuracy != 0 {
		t.Errorf("accuracy = %v, want 0 when everything errored", s.Accuracy)
	}
}

func TestRunBuildErrorIsReportedNotPanicked(t *testing.T) {
	badArm := Arm{
		Name: "bad",
		Build: func(context.Context, Task) (string, error) {
			return "", errors.New("boom")
		},
	}
	report, err := Run(context.Background(), Config{
		Tasks:    []Task{{ID: "t1", Title: "任务一", Judge: func(string) Verdict { return Verdict{true, "ok"} }}},
		Arms:     []Arm{badArm},
		Executor: &fakeExecutor{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(report.Results[0].Error, "boom") {
		t.Errorf("error not propagated: %+v", report.Results[0])
	}
	if report.Summaries[0].Errors != 1 {
		t.Errorf("errors = %d", report.Summaries[0].Errors)
	}
}

func TestRunValidatesConfig(t *testing.T) {
	if _, err := Run(context.Background(), Config{Arms: []Arm{prefixedArm("a", "A:")}, Executor: &fakeExecutor{}}); err == nil {
		t.Error("expected error for empty tasks")
	}
	if _, err := Run(context.Background(), Config{Tasks: []Task{{ID: "t"}}, Executor: &fakeExecutor{}}); err == nil {
		t.Error("expected error for empty arms")
	}
	if _, err := Run(context.Background(), Config{Tasks: []Task{{ID: "t"}}, Arms: []Arm{prefixedArm("a", "A:")}}); err == nil {
		t.Error("expected error for nil executor")
	}
}

func TestMarkdownReportContainsArmsAndTasks(t *testing.T) {
	exec := &fakeExecutor{fallback: `[{"a":1}]`}
	report, err := Run(context.Background(), Config{
		Tasks:       DefaultTasks(),
		Arms:        []Arm{prefixedArm("baseline", "BASE:")},
		Executor:    exec,
		Repetitions: 1,
		Notes:       []string{"口径声明：判据只检查原始提示词隐含的要求"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	md := report.Markdown()
	for _, want := range []string{
		"## 一、实验设置", "## 二、各臂汇总",
		"## 三、逐任务 × 逐臂 通过矩阵", "## 四、逐任务结果",
		"baseline", "fake-model", "口径声明",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}
