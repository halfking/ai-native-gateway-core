package main

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestResolveWaterfallByRequestMemoryFirst(t *testing.T) {
	mem := dispatch.WaterfallRequest{RequestID: "r1", Result: "success"}
	got, source, ok := resolveWaterfallByRequest(context.Background(), mem, true, "r1", "")
	if !ok || source != "memory" || got.RequestID != "r1" {
		t.Fatalf("got=%+v source=%s ok=%v", got, source, ok)
	}
	_, source, ok = resolveWaterfallByRequest(context.Background(), dispatch.WaterfallRequest{}, false, "missing", "")
	if ok || source != "none" {
		t.Fatalf("miss source=%s ok=%v", source, ok)
	}
}

func TestMergeWaterfallLists(t *testing.T) {
	mem := []dispatch.WaterfallRequest{
		{RequestID: "m1", Model: "a"},
		{RequestID: "dup", Model: "a"},
	}
	db := []dispatch.WaterfallRequest{
		{RequestID: "dup", Model: "b"},
		{RequestID: "d1", Model: "c"},
		{RequestID: "d2", Model: "d"},
	}
	out, source := mergeWaterfallLists(mem, db, 3)
	if source != "memory+db" {
		t.Fatalf("source=%q want memory+db", source)
	}
	if len(out) != 3 {
		t.Fatalf("len=%d want 3", len(out))
	}
	if out[0].RequestID != "m1" || out[1].RequestID != "dup" || out[2].RequestID != "d1" {
		t.Fatalf("order/dedupe = %+v", out)
	}
}

func TestMergeWaterfallListsDBOnly(t *testing.T) {
	db := []dispatch.WaterfallRequest{{RequestID: "d1"}}
	out, source := mergeWaterfallLists(nil, db, 10)
	if source != "db" || len(out) != 1 {
		t.Fatalf("source=%q len=%d", source, len(out))
	}
}

func TestMergeWaterfallDBOnlyKeepsProjectionWiredState(t *testing.T) {
	snap := dispatch.WaterfallSnapshot{Wired: false}
	snap.Requests = []dispatch.WaterfallRequest{{RequestID: "db-only"}}
	snap.Source = "db"
	if snap.Wired {
		t.Fatal("historical DB samples must not mark the queue projection as wired")
	}
}

func TestMergeWaterfallListsNone(t *testing.T) {
	out, source := mergeWaterfallLists(nil, nil, 10)
	if source != "none" || len(out) != 0 {
		t.Fatalf("source=%q len=%d", source, len(out))
	}
}

func TestDurationMSPtr(t *testing.T) {
	start := mustParseTime(t, "2026-08-21T10:00:00Z")
	end := mustParseTime(t, "2026-08-21T10:00:01.500Z")
	if got := durationMSPtr(&start, &end); got != 1500 {
		t.Fatalf("duration=%d", got)
	}
	if durationMSPtr(nil, &end) != 0 {
		t.Fatal("nil start should be 0")
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
