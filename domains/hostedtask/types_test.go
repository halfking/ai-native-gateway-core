package hostedtask

import (
	"strings"
	"testing"
	"time"
)

// 状态机纯函数矩阵（§4.2）。表驱动：from → 合法出边全集。
func TestCanTransitionMatrix(t *testing.T) {
	all := []Status{
		StatusDelegated, StatusDispatching, StatusRunning,
		StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled, StatusExpired,
	}
	allowed := map[Status]map[Status]bool{
		StatusDelegated:   {StatusDispatching: true, StatusCancelled: true, StatusExpired: true},
		StatusDispatching: {StatusRunning: true, StatusFailed: true, StatusCancelled: true, StatusExpired: true},
		StatusRunning: {
			StatusCompleted: true, StatusFailed: true,
			StatusNeedsReview: true, StatusCancelled: true, StatusExpired: true,
		},
		StatusCompleted:   {},
		StatusFailed:      {},
		StatusNeedsReview: {},
		StatusCancelled:   {},
		StatusExpired:     {},
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s → %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	terminal := []Status{StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled, StatusExpired}
	active := []Status{StatusDelegated, StatusDispatching, StatusRunning}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
		if ValidStatus(s) != true {
			t.Errorf("%s should be a valid status", s)
		}
	}
	for _, s := range active {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	// 终态无出边（sticky）。
	for _, s := range terminal {
		for _, to := range allStatuses() {
			if CanTransition(s, to) {
				t.Errorf("terminal %s must not transition to %s (sticky)", s, to)
			}
		}
	}
}

func TestAPIStatusNeedsReviewExposedAsFailed(t *testing.T) {
	// §3.1：needs_review P0 暴露为 failed(unknown)。
	if got := StatusNeedsReview.APIStatus(); got != "failed" {
		t.Errorf("needs_review APIStatus = %q, want failed", got)
	}
	if got := StatusCompleted.APIStatus(); got != "completed" {
		t.Errorf("completed APIStatus = %q", got)
	}
}

func TestEventIDFormat(t *testing.T) {
	// §6.1：event_id = hosted_<id>_ev<seq> 固定（接收方幂等键）。
	if got := EventID("ht_abc", 7); got != "hosted_ht_abc_ev7" {
		t.Errorf("EventID = %q", got)
	}
}

func TestHashRequestStableAndSensitive(t *testing.T) {
	a := HashRequest("goal", "dw", map[string]any{"summary": "s"}, map[string]any{"cwd": "/x"}, "https://cb")
	b := HashRequest("goal", "dw", map[string]any{"summary": "s"}, map[string]any{"cwd": "/x"}, "https://cb")
	c := HashRequest("goal-changed", "dw", map[string]any{"summary": "s"}, map[string]any{"cwd": "/x"}, "https://cb")
	d := HashRequest("goal", "dw", map[string]any{"summary": "s"}, map[string]any{"cwd": "/x"}, "https://cb2")
	if a != b {
		t.Error("same input must hash equal (idempotent replay)")
	}
	if a == c || a == d {
		t.Error("goal/callback change must change hash (同键异体 409)")
	}
}

func TestBuildPromptSections(t *testing.T) {
	in := CreateInput{
		Goal:     "修复构建",
		DoneWhen: "三门全绿",
		Context: map[string]any{
			"summary":   "背景信息",
			"memora":    map[string]any{"project_id": "p1"},
			"artifacts": []any{"a.txt"},
		},
		Deadline: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
	}
	p := BuildPrompt(in)
	for _, want := range []string{"修复构建", "完成判定", "三门全绿", "上下文摘要", "背景信息", "Memora", "关联产物引用", "2026-09-15T12:00:00Z"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// 最小输入不炸。
	if p2 := BuildPrompt(CreateInput{Goal: "x"}); !strings.Contains(p2, "x") {
		t.Errorf("minimal prompt broken: %q", p2)
	}
}

func allStatuses() []Status {
	return []Status{
		StatusDelegated, StatusDispatching, StatusRunning,
		StatusCompleted, StatusFailed, StatusNeedsReview, StatusCancelled, StatusExpired,
	}
}
