package pending

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPendingRestartReplaysResultWithoutUpstreamExecution(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first := NewStore(client, time.Minute)
	response := &Response{
		TenantID: "tenant-a", SessionID: "session-a", RequestID: "request-a",
		Status: StatusCompleted, Body: "data: complete\n\n", ContentType: "text/event-stream",
		RequestHash: "hash-a", IsStream: true,
	}
	if err := first.Save(context.Background(), response); err != nil {
		t.Fatalf("save pending result: %v", err)
	}

	// A new Store models a restarted process. This store-level operation only
	// reconstructs the completed result; execution recovery belongs to durable.
	second := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Minute)
	t.Cleanup(func() { _ = second.rdb.Close() })
	got, ok, err := second.Get(context.Background(), "session-a", "request-a")
	if err != nil || !ok {
		t.Fatalf("replay result: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.Body != response.Body || got.TenantID != response.TenantID {
		t.Fatalf("replayed response = %+v, want %+v", got, response)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("replayed status = %q, want completed", got.Status)
	}
}
