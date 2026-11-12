package experiment

import (
	"strings"
	"testing"
)

// 测试的定位：判据是这套实验的**量具**。
// 量具本身必须先被审计 —— 判据写错，整份实验报告就是精确的错误。

func TestJudgeJSONArray(t *testing.T) {
	judge := judgeJSONArray(3)

	pass := []string{
		`[{"name":"苹果","price":3.5},{"name":"香蕉","price":2},{"name":"橙子","price":4}]`,
		"```json\n[{\"name\":\"苹果\"},{\"name\":\"香蕉\"},{\"name\":\"橙子\"}]\n```",
		"[1,2,3]",
	}
	for _, out := range pass {
		if v := judge(out); !v.Passed {
			t.Errorf("should pass: %q -> %s", out, v.Reason)
		}
	}

	fail := []string{
		`好的，这是结果：[{"name":"苹果"},{"name":"香蕉"},{"name":"橙子"}]`,
		`[{"name":"苹果"},{"name":"香蕉"}]`,
		`[{"name":"苹果"},{"name":"香蕉"},{"name":"橙子"},{"name":"梨"}]`,
		"苹果 3.5、香蕉 2、橙子 4",
		"",
	}
	for _, out := range fail {
		if v := judge(out); v.Passed {
			t.Errorf("should fail: %q", out)
		}
	}
}

func TestJudgeBulletCount(t *testing.T) {
	judge := judgeBulletCount(3)

	pass := []string{
		"- 定期做全量备份\n- 异地保存一份\n- 定期演练恢复",
		"建议如下：\n- 定期做全量备份\n- 异地保存一份\n- 定期演练恢复",
		"1. 定期做全量备份\n2. 异地保存一份\n3. 定期演练恢复",
	}
	for _, out := range pass {
		if v := judge(out); !v.Passed {
			t.Errorf("should pass: %q -> %s", out, v.Reason)
		}
	}

	fail := []string{
		"- 定期做全量备份\n- 异地保存一份\n- 定期演练恢复\n- 加密存储\n- 权限最小化",
		"- 定期做全量备份\n- 异地保存一份",
	}
	for _, out := range fail {
		if v := judge(out); v.Passed {
			t.Errorf("should fail: %q", out)
		}
	}
}

func TestJudgeContainsAll(t *testing.T) {
	judge := judgeContainsAll([][]string{
		{"周六", "星期六"},
		{"凌晨 2 点", "凌晨2点", "凌晨两点", "2 点", "2点", "两点"},
		{"1 小时", "1小时", "一小时"},
	})

	if v := judge("本周六凌晨 2 点维护，预计持续 1 小时。"); !v.Passed {
		t.Errorf("should pass: %s", v.Reason)
	}
	// 同义写法要能命中，否则判据会因书写差异误杀。
	if v := judge("星期六凌晨两点停机，大约一小时后恢复。"); !v.Passed {
		t.Errorf("synonym forms should pass: %s", v.Reason)
	}
	// 漏事实必须判负。
	if v := judge("本周六维护，预计持续一小时。"); v.Passed {
		t.Error("missing '凌晨2点' should fail")
	}
}

func TestJudgePythonEvenFilter(t *testing.T) {
	judge := judgePythonEvenFilter()

	good := "def pick_evens(xs):\n    return [x for x in xs if x % 2 == 0]"
	if v := judge(good); !v.Passed {
		t.Errorf("should pass: %s", v.Reason)
	}
	if v := judge("以下是实现：\n" + good); v.Passed {
		t.Error("preamble should fail")
	}
	if v := judge("用列表推导式过滤偶数即可。"); v.Passed {
		t.Error("no code should fail")
	}
	if v := judge("def f(xs):\n    return xs"); v.Passed {
		t.Error("no even-filter logic should fail")
	}
}

func TestJudgeDateISO(t *testing.T) {
	judge := judgeDateISO("2026-06-13")

	if v := judge("2026-06-13"); !v.Passed {
		t.Errorf("should pass: %s", v.Reason)
	}
	if v := judge("往后推 100 天是 2026-06-13。"); !v.Passed {
		t.Errorf("should pass with sentence: %s", v.Reason)
	}
	if v := judge("2026年6月13日"); v.Passed {
		t.Error("non-ISO format should fail")
	}
	if v := judge("2026-06-14"); v.Passed {
		t.Error("wrong date should fail")
	}
}

func TestJudgeNoFabrication(t *testing.T) {
	judge := judgeNoFabrication(
		[]string{"登录", "样式", "错位", "修复"},
		[]string{"性能", "安全", "数据库", "崩溃"},
	)

	if v := judge("本次发布仅修复了登录页的样式错位问题。"); !v.Passed {
		t.Errorf("should pass: %s", v.Reason)
	}
	if v := judge("本次发布修复了登录页样式问题，同时显著提升了性能并加强了安全。"); v.Passed {
		t.Error("fabricated topics should fail")
	}
	if v := judge("本次发布上线了新功能。"); v.Passed {
		t.Error("missing original fact should fail")
	}
}

func TestDefaultTasksJudgesRejectTheirOwnBaselineShape(t *testing.T) {
	// 防回归：每个任务的判据必须能判负"典型的不合格输出"，
	// 否则判据就是恒真的假门。
	badOutputs := map[string]string{
		"json_array_3":     "苹果 3.5、香蕉 2、橙子 4",
		"three_bullets":    "- a\n- b\n- c\n- d\n- e",
		"notice_facts":     "各位好，服务器将于近期维护，请注意。",
		"python_code_only": "你可以用列表推导式过滤偶数。",
		"date_iso":         "往后推 100 天是 2026年6月13日。",
		"no_fabrication":   "本次发布大幅提升了性能并优化了架构。",
	}

	tasks := DefaultTasks()
	if len(tasks) == 0 {
		t.Fatal("DefaultTasks returned empty")
	}
	for _, task := range tasks {
		bad, ok := badOutputs[task.ID]
		if !ok {
			t.Fatalf("no bad-output fixture for task %s", task.ID)
		}
		if v := task.Judge(bad); v.Passed {
			t.Errorf("task %s: judge accepted a clearly non-compliant output", task.ID)
		}
		if task.Judge == nil {
			t.Errorf("task %s: judge is nil", task.ID)
		}
		if strings.TrimSpace(task.Prompt) == "" {
			t.Errorf("task %s: prompt must not be empty", task.ID)
		}
	}
}
