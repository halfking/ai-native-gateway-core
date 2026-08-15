package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// liveActionsRecorder wires a liveactions.Emitter backed by miniredis so
// pipeline instrumentation can be asserted end-to-end (V3.3-OBS OBS-B1).
type liveActionsRecorder struct {
	emitter *liveactions.Emitter
	rdb     *redis.Client
}

func newLiveActionsRecorder(t *testing.T) *liveActionsRecorder {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	e := liveactions.NewEmitter(rdb, 0)
	t.Cleanup(e.Close)
	return &liveActionsRecorder{emitter: e, rdb: rdb}
}

// actions waits for at least min events, then returns all recorded events
// for the request, in Redis list order (newest first).
func (rec *liveActionsRecorder) actions(t *testing.T, requestID string, min int) []liveactions.ActionEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		vals, err := rec.rdb.LRange(context.Background(), liveactions.RedisKey, 0, -1).Result()
		if err != nil {
			t.Fatalf("LRange: %v", err)
		}
		matched := make([]liveactions.ActionEvent, 0, len(vals))
		for _, v := range vals {
			var ev liveactions.ActionEvent
			if err := json.Unmarshal([]byte(v), &ev); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if ev.RequestID == requestID {
				matched = append(matched, ev)
			}
		}
		if len(matched) >= min {
			// Redis list is newest-first (LPUSH); reverse to emit order.
			for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
				matched[i], matched[j] = matched[j], matched[i]
			}
			return matched
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d action events for %s", min, requestID)
	return nil
}

// TestPipelineEmitsEnqueueActions: Submit 成功路径必须发射 model_enqueued 与
// node_enqueued（S4/S6，docs/会话优化v3/24 §2/§6）。
func TestPipelineEmitsEnqueueActions(t *testing.T) {
	rec := newLiveActionsRecorder(t)
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline()
	p.SetLiveActions(rec.emitter)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("req-live-1", "tenant", "gpt4", context.Background(), nil)
	out, err := p.Submit(context.Background(), qr)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out == nil {
		t.Fatal("Submit returned nil result")
	}

	events := rec.actions(t, "req-live-1", 3)
	var gotModelEnqueued, gotNodeEnqueued, gotNodeSelected bool
	for _, ev := range events {
		switch ev.Action {
		case liveactions.ActionModelEnqueued:
			gotModelEnqueued = true
			if ev.Model != "gpt4" {
				t.Errorf("model_enqueued model = %q, want gpt4", ev.Model)
			}
		case liveactions.ActionNodeEnqueued:
			gotNodeEnqueued = true
			if ev.CredentialID != 1 {
				t.Errorf("node_enqueued credential_id = %d, want 1", ev.CredentialID)
			}
		case liveactions.ActionNodeSelected:
			gotNodeSelected = true
			if ev.CredentialID != 1 {
				t.Errorf("node_selected credential_id = %d, want 1", ev.CredentialID)
			}
		}
	}
	if !gotModelEnqueued || !gotNodeEnqueued || !gotNodeSelected {
		t.Fatalf("missing actions: model_enqueued=%v node_enqueued=%v node_selected=%v (events: %+v)",
			gotModelEnqueued, gotNodeEnqueued, gotNodeSelected, events)
	}
	// seq 单调递增（24 号 §2）。
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("seq not monotonic: %v", events)
		}
	}
}

// TestPipelineEmitsFailoverActions: 凭据重试/切换、模型切换与 no_route 终态
// 都必须作为 node_switch / model_switch / no_route 动作事件发射。
func TestPipelineEmitsFailoverActions(t *testing.T) {
	rec := newLiveActionsRecorder(t)
	f := &fakeDeps{
		refsByModel: map[string][]CredentialRef{
			"m1": {cred(1, ModeConcurrency, 5)},
			// m2 resolves to nothing → exhausts → model-change ladder ends
			// in a terminal no_route.
		},
		altsByModel:  map[string][]string{"m1": {"m2"}},
		forwardCalls: map[int]int{},
		allowChange:  true,
		forwardFn: func(ctx context.Context, qr *QueuedRequest, cred CredentialRef) ForwardOutcome {
			return ForwardOutcome{Err: errors.New("upstream 503")}
		},
	}
	p := f.pipeline()
	p.SetLiveActions(rec.emitter)
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("req-live-2", "tenant", "m1", context.Background(), nil)
	qr.RetryPerCredential = 1  // one same-credential retry before switching
	qr.AllowModelChange = true // enable the Tier-1 model-change escape hatch
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatal("Submit should fail (all forwards fail)")
	}

	events := rec.actions(t, "req-live-2", 6)
	byAction := map[liveactions.Action][]liveactions.ActionEvent{}
	for _, ev := range events {
		byAction[ev.Action] = append(byAction[ev.Action], ev)
	}

	// node_switch: 至少一次 retry=true（同凭据重试）。
	var gotRetrySwitch bool
	for _, ev := range byAction[liveactions.ActionNodeSwitch] {
		if ev.Retry {
			gotRetrySwitch = true
			if ev.Detail["from_credential_id"] != ev.Detail["to_credential_id"] {
				t.Errorf("retry switch from=%s to=%s, want identical",
					ev.Detail["from_credential_id"], ev.Detail["to_credential_id"])
			}
		}
	}
	if !gotRetrySwitch {
		t.Errorf("no node_switch with retry=true in %+v", byAction[liveactions.ActionNodeSwitch])
	}

	// model_switch: m1 → m2, reason no_node.
	if sw := byAction[liveactions.ActionModelSwitch]; len(sw) == 0 {
		t.Fatalf("no model_switch event in %+v", events)
	} else {
		if sw[0].Detail["from_model"] != "m1" || sw[0].Detail["to_model"] != "m2" || sw[0].Model != "m2" {
			t.Errorf("model_switch detail = %+v, want m1→m2", sw[0])
		}
	}

	// no_route 终态（所有模型与凭据耗尽）。
	if nr := byAction[liveactions.ActionNoRoute]; len(nr) == 0 {
		t.Fatalf("no no_route terminal event in %+v", events)
	}

	// seq 单调递增。
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("seq not monotonic: %+v", events)
		}
	}
}

// TestPipelineNilLiveActionsIsNoOp: 未注入发射器时 Submit 行为不变（其他
// 测试零依赖零破坏）。
func TestPipelineNilLiveActionsIsNoOp(t *testing.T) {
	f := &fakeDeps{
		refsByModel:  map[string][]CredentialRef{"gpt4": {cred(1, ModeConcurrency, 5)}},
		forwardCalls: map[int]int{},
	}
	p := f.pipeline() // no SetLiveActions → nil emitter
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("req-live-3", "tenant", "gpt4", context.Background(), nil)
	out, err := p.Submit(context.Background(), qr)
	if err != nil || out == nil {
		t.Fatalf("Submit with nil emitter: out=%v err=%v", out, err)
	}
}
