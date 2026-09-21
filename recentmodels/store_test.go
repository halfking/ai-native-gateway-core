package recentmodels

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRecordAndRead(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	ctx := context.Background()
	Record(ctx, client, "tenant-a", "GPT-4O", false)
	Record(ctx, client, "tenant-a", "gpt-4o", false)
	Record(ctx, client, "tenant-a", "claude-4", false)
	Record(ctx, client, "tenant-a", "probe-only", true)

	got := Read(ctx, client, "tenant-a", 10)
	if len(got) != 2 || got[0] != (Entry{Model: "gpt-4o", Count: 2}) || got[1] != (Entry{Model: "claude-4", Count: 1}) {
		t.Fatalf("Read()=%+v", got)
	}
	if server.TTL(Key("tenant-a")) <= 0 {
		t.Fatal("seven-day TTL was not set")
	}
	if got := Read(ctx, client, "tenant-b", 10); len(got) != 0 {
		t.Fatalf("tenant ranking leaked: %+v", got)
	}
}
