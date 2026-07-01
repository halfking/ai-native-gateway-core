package assets

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestIntentAggregate_NoopStore(t *testing.T) {
	s := NoopIntentAggregateStore{}
	if err := s.Increment(context.Background(), "t1", analysis.IntentChat, 1); err != nil {
		t.Fatalf("noop Increment err: %v", err)
	}
	got, err := s.Get(context.Background(), "t1")
	if err != nil {
		t.Fatalf("noop Get err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("noop Get should return empty, got %+v", got)
	}
}

func TestPGIntentAggregateStore_NilSafe(t *testing.T) {
	var s *PGIntentAggregateStore
	if err := s.Increment(context.Background(), "t1", analysis.IntentChat, 1); err == nil {
		t.Fatal("nil store Increment should error")
	}
	if _, err := s.Get(context.Background(), "t1"); err == nil {
		t.Fatal("nil store Get should error")
	}
}

func TestPGIntentAggregateStore_RejectsEmptyTenant(t *testing.T) {
	s := &PGIntentAggregateStore{} // nil pool 也应被拦截
	if err := s.Increment(context.Background(), "", analysis.IntentChat, 1); err == nil {
		t.Fatal("empty tenant should error")
	}
}

func TestPGIntentAggregateStore_NonPositiveDeltaIsNoop(t *testing.T) {
	// delta <= 0 应直接 return nil（避免无意义的 DB 调用）
	s := &PGIntentAggregateStore{}
	if err := s.Increment(context.Background(), "t1", analysis.IntentChat, 0); err != nil {
		t.Fatalf("delta=0 should be no-op, got %v", err)
	}
	if err := s.Increment(context.Background(), "t1", analysis.IntentChat, -5); err != nil {
		t.Fatalf("delta<0 should be no-op, got %v", err)
	}
}
