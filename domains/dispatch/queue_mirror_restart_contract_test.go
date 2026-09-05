package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestQueueMirrorRestartRebuildIsMetadataOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first := NewQueueMirror(client)
	first.ObserveQueue(QueueObservation{Kind: QueueModelDepth, Model: "m", Depth: 2})
	first.MirrorRetryAt("request-a", time.Unix(1700000123, 0).UTC())
	first.Flush()
	first.Close()

	// RebuildMetadata is deliberately a read-only projection operation; it has
	// no execution callback and cannot resume an upstream attempt.
	secondClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = secondClient.Close() })
	second := NewQueueMirror(secondClient)
	t.Cleanup(second.Close)
	metadata, err := second.RebuildMetadata(context.Background())
	if err != nil {
		t.Fatalf("rebuild mirror metadata: %v", err)
	}
	if len(metadata.Models) != 1 || metadata.Models[0].Key != "m" || metadata.Models[0].Depth != 2 {
		t.Fatalf("rebuilt model metadata = %+v", metadata.Models)
	}
	if len(metadata.Retries) != 1 || metadata.Retries[0].RequestID != "request-a" {
		t.Fatalf("rebuilt retry metadata = %+v", metadata.Retries)
	}
}
