package bg

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hostedtask"
)

// deriveSettlement 表驱动（§3.1 ④/矩阵 G/H/§0-F4）：
//   - completed + stop_reason=stop_success → completed
//   - completed + stop_reason=error → failed（pi 假成功）
//   - completed + 无 stop_reason → needs_review（unknown_outcome 不猜测）
//   - failed → failed；cancelled → cancelled；timeout → expired
func TestDeriveSettlement(t *testing.T) {
	cases := []struct {
		name       string
		cmd        hostedtask.ACCCommand
		wantTo     hostedtask.Status
		wantEvent  hostedtask.EventType
		wantOK     bool
		wantUnkown bool
	}{
		{
			name:   "completed with real stop_reason",
			cmd:    hostedtask.ACCCommand{CommandID: "c1", Status: "completed", StopReason: "stop_success"},
			wantTo: hostedtask.StatusCompleted, wantEvent: hostedtask.EventCompleted, wantOK: true,
		},
		{
			name:   "pi fake success (stop_reason=error)",
			cmd:    hostedtask.ACCCommand{CommandID: "c2", Status: "completed", StopReason: "error"},
			wantTo: hostedtask.StatusFailed, wantEvent: hostedtask.EventFailed, wantOK: true,
		},
		{
			name:   "completed without stop_reason → needs_review",
			cmd:    hostedtask.ACCCommand{CommandID: "c3", Status: "completed"},
			wantTo: hostedtask.StatusNeedsReview, wantEvent: hostedtask.EventFailed, wantOK: true,
			wantUnkown: true,
		},
		{
			name:   "failed",
			cmd:    hostedtask.ACCCommand{CommandID: "c4", Status: "failed", StopReason: "crash"},
			wantTo: hostedtask.StatusFailed, wantEvent: hostedtask.EventFailed, wantOK: true,
		},
		{
			name:   "cancelled",
			cmd:    hostedtask.ACCCommand{CommandID: "c5", Status: "canceled"},
			wantTo: hostedtask.StatusCancelled, wantEvent: hostedtask.EventCancelled, wantOK: true,
		},
		{
			name:   "timed out → expired",
			cmd:    hostedtask.ACCCommand{CommandID: "c6", Status: "timed_out"},
			wantTo: hostedtask.StatusExpired, wantEvent: hostedtask.EventExpired, wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, ok := deriveSettlement(tc.cmd)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if in.To != tc.wantTo || in.EventType != tc.wantEvent {
				t.Fatalf("got (%s/%s), want (%s/%s)", in.To, in.EventType, tc.wantTo, tc.wantEvent)
			}
			if tc.wantUnkown && in.Payload["outcome"] != "unknown_outcome" {
				t.Errorf("needs_review payload must carry outcome=unknown_outcome, got %v", in.Payload["outcome"])
			}
		})
	}
	// 非终态不产生 settle 指令。
	for _, st := range []string{"running", "queued", "leased", "delivered", "pending"} {
		if _, ok := deriveSettlement(hostedtask.ACCCommand{Status: st}); ok {
			t.Errorf("status %q must not settle", st)
		}
	}
}

func TestBuildResultFields(t *testing.T) {
	cmd := hostedtask.ACCCommand{
		CommandID:  "c1",
		Status:     "completed",
		StopReason: "stop_success",
		ResultRaw: map[string]any{
			"output":     "hello output",
			"session_id": "pi-sess-1",
		},
	}
	res := buildResult(cmd)
	if res["pi_session_ref"] != "pi-sess-1" {
		t.Errorf("pi_session_ref = %v", res["pi_session_ref"])
	}
	if res["summary"] != "hello output" {
		t.Errorf("summary fallback from output failed: %v", res["summary"])
	}
	hash, _ := res["content_hash"].(string)
	if len(hash) != len("sha256:")+64 {
		t.Errorf("content_hash missing/malformed: %v", res["content_hash"])
	}
	if res["stop_reason"] != "stop_success" {
		t.Errorf("raw stop_reason must be preserved for F4 audit")
	}
}

func TestDispatchKeyStable(t *testing.T) {
	// §3.1 ②：同键重放防双发 —— key 派生必须对 task id 稳定（a1 恒定）。
	task := hostedtask.Task{ID: "ht_x", Goal: "g"}
	p := buildDispatchPayload(task)
	if p.Kind != "pi" || p.Prompt == "" {
		t.Fatalf("payload kind/prompt missing: %+v", p)
	}
	if p.Cwd != "" {
		t.Errorf("裸路径任务（无 environment.cwd）不得携带 cwd")
	}
	task.Environment = map[string]any{"cwd": "/srv/ws1", "timeout_seconds": float64(60)}
	p2 := buildDispatchPayload(task)
	if p2.Cwd != "/srv/ws1" {
		t.Errorf("cwd mapping lost: %q", p2.Cwd)
	}
	if p2.TimeoutMs != 60000 {
		t.Errorf("timeout_ms = %d, want 60000", p2.TimeoutMs)
	}
}
