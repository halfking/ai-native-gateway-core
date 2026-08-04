package compression

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type rehydrateRedis struct {
	fields map[string]map[string]string
}

func newRehydrateRedis() *rehydrateRedis {
	return &rehydrateRedis{fields: make(map[string]map[string]string)}
}

func (r *rehydrateRedis) HSet(_ context.Context, key string, values ...any) error {
	fields := make(map[string]string, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		name, ok := values[i].(string)
		if !ok {
			continue
		}
		fields[name] = values[i+1].(string)
	}
	r.fields[key] = fields
	return nil
}

func (r *rehydrateRedis) HGetAll(_ context.Context, key string) (map[string]string, error) {
	src := r.fields[key]
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out, nil
}

func (r *rehydrateRedis) Expire(context.Context, string, time.Duration) error { return nil }

func (r *rehydrateRedis) Del(_ context.Context, key string) error {
	delete(r.fields, key)
	return nil
}

type rehydrateDB struct {
	row   *LastOutboundRow
	calls int
}

func (d *rehydrateDB) LastOutboundForSession(context.Context, string, string) (*LastOutboundRow, error) {
	d.calls++
	return d.row, nil
}

func TestSessionCache_L2MetadataHitRehydratesBodyFromL3(t *testing.T) {
	ctx := context.Background()
	redis := newRehydrateRedis()
	body := json.RawMessage(`{"model":"gpt-test","messages":[{"role":"assistant","content":"[smm_v1:abc] summary"},{"role":"user","content":"next"}]}`)
	db := &rehydrateDB{row: &LastOutboundRow{
		OutboundBody:     body,
		OutboundMsgCount: 2,
		OutboundTokenEst: 42,
		CompressionMeta:  json.RawMessage(`{"summary_marker":"[smm_v1:abc]"}`),
	}}
	cache := NewSessionCache(redis, db)
	state := &SessionState{
		SchemaVersion:    schemaVersion,
		LastOutboundHash: sha256Hex(body),
		MsgCount:         2,
		TokenEstimate:    42,
		SummaryMarker:    "[smm_v1:abc]",
	}
	requireNoError(t, cache.saveToRedis(ctx, "tenant-1", "session-1", state))

	gotState, gotBody, err := cache.GetOrLoad(ctx, "tenant-1", "session-1")
	requireNoError(t, err)
	if gotState == nil {
		t.Fatal("expected Redis metadata state")
	}
	if string(gotBody) != string(body) {
		t.Fatalf("rehydrated body = %s, want %s", gotBody, body)
	}
	if gotState.SummaryMarker != state.SummaryMarker {
		t.Fatalf("summary marker = %q, want %q", gotState.SummaryMarker, state.SummaryMarker)
	}
	if db.calls != 1 {
		t.Fatalf("L3 calls = %d, want 1", db.calls)
	}

	// The hydrated body is now in L1, so a second lookup stays in-process.
	_, secondBody, err := cache.GetOrLoad(ctx, "tenant-1", "session-1")
	requireNoError(t, err)
	if string(secondBody) != string(body) {
		t.Fatalf("L1 body = %s, want %s", secondBody, body)
	}
	if db.calls != 1 {
		t.Fatalf("L3 calls after L1 hit = %d, want 1", db.calls)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
