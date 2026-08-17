package requestjourney

import (
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
