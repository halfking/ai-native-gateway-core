package requestjourney

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestFIFOSnapshotsEvictOldestIndependently(t *testing.T) {
	total := NewTotalRequestFIFOSnapshot(2)
	model := NewModelFIFOSnapshot("model-a", 2)
	node := NewNodeFIFOSnapshot("model-a", 7, 11, 2)

	for i := 1; i <= 3; i++ {
		item := RequestSnapshot{
			RequestID: fmt.Sprintf("request-%d", i), RequestedModel: "auto",
			ResolvedModel: "model-a", CurrentStage: StageModelQueue,
			UpdatedAt: time.Unix(int64(i), 0).UTC(),
		}
		total.Append(item)
		model.Append(item)
		node.Append(item)
	}

	for name, requests := range map[string][]RequestSnapshot{
		"total": total.Requests,
		"model": model.Requests,
		"node":  node.Requests,
	} {
		if len(requests) != 2 {
			t.Fatalf("%s FIFO length = %d, want 2", name, len(requests))
		}
		if requests[0].RequestID != "request-2" || requests[1].RequestID != "request-3" {
			t.Fatalf("%s FIFO requests = %#v", name, requests)
		}
	}

	if total.Capacity != 2 || model.Capacity != 2 || node.Capacity != 2 {
		t.Fatalf("capacities = %d/%d/%d", total.Capacity, model.Capacity, node.Capacity)
	}
	if total.Requests[0].RequestedModel != "auto" || total.Requests[0].ResolvedModel != "model-a" || total.Requests[0].CurrentStage != StageModelQueue {
		t.Fatalf("snapshot display fields = %#v", total.Requests[0])
	}
	if model.Model != "model-a" {
		t.Fatalf("model key = %q", model.Model)
	}
	if node.ProviderID != 7 || node.CredentialID != 11 {
		t.Fatalf("node key = provider %d credential %d", node.ProviderID, node.CredentialID)
	}
}

func TestFIFOSnapshotNonPositiveCapacityUsesDefault(t *testing.T) {
	if got := NewTotalRequestFIFOSnapshot(0).Capacity; got != DefaultTotalRequestCapacity {
		t.Fatalf("capacity = %d, want %d", got, DefaultTotalRequestCapacity)
	}
}

// ⚠️ 这条判据钉的是**已上线的缺陷**（2026-10-13 真机发现）：
// 移动端首页读 total_snapshot.pending/in_flight/completed，而本结构体
// 曾经没有这三个字段 ⇒ 真机上「实时请求队列」永远显示三个「—」。
//
// ⚠️ 变异方式一：把 MarshalJSON 去掉（回到只有 capacity/requests 的线上形态）
// ⚠️ 变异方式二：把 LifecycleCounts 里的 case 删掉一个
func TestTotalSnapshotJSONCarriesLifecycleTally(t *testing.T) {
	snapshot := &TotalRequestFIFOSnapshot{
		Capacity: 100,
		Requests: []RequestSnapshot{
			{RequestID: "a", LifecycleState: LifecyclePending},
			{RequestID: "b", LifecycleState: LifecyclePending},
			{RequestID: "c", LifecycleState: LifecycleInFlight},
			{RequestID: "d", LifecycleState: LifecycleCompleted},
			{RequestID: "e", LifecycleState: LifecycleCompleted},
			{RequestID: "f", LifecycleState: LifecycleCompleted},
		},
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Capacity  int `json:"capacity"`
		Pending   int `json:"pending"`
		InFlight  int `json:"in_flight"`
		Completed int `json:"completed"`
		Requests  []RequestSnapshot
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Pending != 2 || decoded.InFlight != 1 || decoded.Completed != 3 {
		t.Fatalf("tally = pending %d / in_flight %d / completed %d, want 2 / 1 / 3 (%s)",
			decoded.Pending, decoded.InFlight, decoded.Completed, encoded)
	}
	if decoded.Capacity != 100 || len(decoded.Requests) != 6 {
		t.Fatalf("wire shape lost capacity/requests: %+v", decoded)
	}
}

// 计数必须**恒等于**当前 Requests，不能靠某一条路径去维护它
// ⇒ 字面量构造（admin/request_journey.go:49 与 :194 都这么做）也要算对。
func TestTotalSnapshotTallyIsDerivedNotStored(t *testing.T) {
	literal := &TotalRequestFIFOSnapshot{
		Capacity: 10,
		Requests: []RequestSnapshot{
			{RequestID: "only", LifecycleState: LifecycleInFlight},
		},
	}
	pending, inFlight, completed := literal.LifecycleCounts()
	if pending != 0 || inFlight != 1 || completed != 0 {
		t.Fatalf("literal-built snapshot tally = %d/%d/%d, want 0/1/0", pending, inFlight, completed)
	}

	built := NewTotalRequestFIFOSnapshot(4)
	for _, state := range []LifecycleState{LifecyclePending, LifecycleCompleted} {
		built.Append(RequestSnapshot{RequestID: string(state), LifecycleState: state})
	}
	pending, inFlight, completed = built.LifecycleCounts()
	if pending != 1 || inFlight != 0 || completed != 1 {
		t.Fatalf("append-built snapshot tally = %d/%d/%d, want 1/0/1", pending, inFlight, completed)
	}

	var nilSnapshot *TotalRequestFIFOSnapshot
	if p, i, c := nilSnapshot.LifecycleCounts(); p != 0 || i != 0 || c != 0 {
		t.Fatalf("nil snapshot tally = %d/%d/%d, want 0/0/0", p, i, c)
	}
}
