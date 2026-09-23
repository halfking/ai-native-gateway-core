package bg

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hostedtask"
)

// TestRefreshStreamTaskNilDefense 是 R63 健壮性钉桩：SSE 重连前的任务刷新
// （bg/hosted_task_reconciler.go refreshStreamTask）对 store.GetTask 的任何
// "拿不到存活任务"形态都必须终止订阅，尤其 (nil, nil)——R63 之前此处直接
// fresh.Status 解引用，store 假件/实现漂移返回 (nil,nil) 即 panic。
func TestRefreshStreamTaskNilDefense(t *testing.T) {
	running := hostedtask.Task{ID: "ht_x", Status: hostedtask.StatusRunning}
	cancelled := hostedtask.Task{ID: "ht_x", Status: hostedtask.StatusCancelled}

	tcs := []struct {
		name   string
		fresh  *hostedtask.Task
		err    error
		wantOK bool
		want   hostedtask.Status
	}{
		{"(nil,nil) 防御核心：必须停而非 panic", nil, nil, false, ""},
		{"err 非空必须停", &running, errors.New("db down"), false, ""},
		{"err 非空且 fresh 为 nil 必须停", nil, hostedtask.ErrNotFound, false, ""},
		{"fresh 终态必须停", &cancelled, nil, false, hostedtask.StatusCancelled},
		{"fresh 存活必须续并采用快照", &running, nil, true, hostedtask.StatusRunning},
	}
	for _, tc := range tcs {
		got, ok := refreshStreamTask(running, tc.fresh, tc.err)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if ok && got.Status != tc.want {
			t.Errorf("%s: status = %s, want %s", tc.name, got.Status, tc.want)
		}
	}
}
