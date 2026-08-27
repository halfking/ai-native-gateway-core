package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	t.Cleanup(func() {
		client.Close()
	})
	return client, mr
}

func TestSafeHGetAll_Success(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: create a hash
	mr.HSet("user:1", "name", "alice")
	mr.HSet("user:1", "age", "30")

	data, err := SafeHGetAll(ctx, client, "user:1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(data))
	}
	if data["name"] != "alice" {
		t.Errorf("expected name=alice, got %v", data["name"])
	}
}

func TestSafeHGetAll_KeyNotFound(t *testing.T) {
	client, _ := setupTestRedis(t)
	ctx := context.Background()

	data, err := SafeHGetAll(ctx, client, "missing:key")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data for missing key, got %v", data)
	}
}

func TestSafeHGetAll_WrongType(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: create a string key (not a hash)
	mr.Set("user:1", "not-a-hash")

	data, err := SafeHGetAll(ctx, client, "user:1")
	if !errors.Is(err, ErrWrongType) {
		t.Fatalf("expected ErrWrongType, got %v", err)
	}

	var typedErr *TypedError
	if !errors.As(err, &typedErr) {
		t.Fatalf("expected TypedError, got %T", err)
	}
	if typedErr.Expected != "hash" {
		t.Errorf("expected type=hash, got %v", typedErr.Expected)
	}
	if typedErr.Actual != "string" {
		t.Errorf("expected actual=string, got %v", typedErr.Actual)
	}
	if data != nil {
		t.Errorf("expected nil data for wrong type, got %v", data)
	}
}

func TestSafeSMembers_Success(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: create a set
	mr.SetAdd("tags", "redis", "golang", "testing")

	members, err := SafeSMembers(ctx, client, "tags")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(members))
	}
}

func TestSafeSMembers_WrongType(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: create a hash (not a set)
	mr.HSet("tags", "field", "value")

	members, err := SafeSMembers(ctx, client, "tags")
	if !errors.Is(err, ErrWrongType) {
		t.Fatalf("expected ErrWrongType, got %v", err)
	}

	var typedErr *TypedError
	if errors.As(err, &typedErr) {
		if typedErr.Actual != "hash" {
			t.Errorf("expected actual=hash, got %v", typedErr.Actual)
		}
	}
	if members != nil {
		t.Errorf("expected nil members for wrong type, got %v", members)
	}
}

func TestSafeLRange_Success(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: create a list
	mr.Lpush("queue", "task1")
	mr.Lpush("queue", "task2")
	mr.Lpush("queue", "task3")

	items, err := SafeLRange(ctx, client, "queue", 0, -1)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}

func TestSafeMGet_NoBatching(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup
	mr.Set("key1", "val1")
	mr.Set("key2", "val2")

	keys := []string{"key1", "key2", "key3"}
	vals, err := SafeMGet(ctx, client, keys, 0)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("expected 3 values, got %d", len(vals))
	}
	if vals[0].(string) != "val1" {
		t.Errorf("expected val1, got %v", vals[0])
	}
	if vals[2] != nil {
		t.Errorf("expected nil for missing key, got %v", vals[2])
	}
}

func TestSafeMGet_WithBatching(t *testing.T) {
	client, mr := setupTestRedis(t)
	ctx := context.Background()

	// Setup: 5 keys, batch size 2
	for i := 1; i <= 5; i++ {
		mr.Set("key"+string(rune('0'+i)), "val"+string(rune('0'+i)))
	}

	keys := []string{"key1", "key2", "key3", "key4", "key5"}
	vals, err := SafeMGet(ctx, client, keys, 2)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(vals) != 5 {
		t.Fatalf("expected 5 values, got %d", len(vals))
	}
}
