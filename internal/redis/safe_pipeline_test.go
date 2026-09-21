package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestSafeHGetAllPipelineClassifiesMixedKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.HSet(ctx, "hash", "field", "value").Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, "string", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}

	got, err := SafeHGetAllPipeline(ctx, client.Pipeline(), []string{"hash", "missing", "string"})
	if err != nil {
		t.Fatalf("pipeline read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("result count = %d, want 3", len(got))
	}
	if got[0].Err != nil || got[0].Fields["field"] != "value" {
		t.Fatalf("hash result = %+v, want fields", got[0])
	}
	if !errors.Is(got[1].Err, ErrKeyNotFound) {
		t.Fatalf("missing error = %v, want ErrKeyNotFound", got[1].Err)
	}
	var typed *TypedError
	if !errors.As(got[2].Err, &typed) || !errors.Is(got[2].Err, ErrWrongType) {
		t.Fatalf("string error = %v, want TypedError/ErrWrongType", got[2].Err)
	}
}
