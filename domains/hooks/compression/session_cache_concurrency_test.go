package compression

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

func TestSessionCacheSetGetOwnsCopies(t *testing.T) {
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	body := []byte("original-body")
	state := &SessionState{
		SchemaVersion: schemaVersion,
		SystemPrompt:  "original-state",
		AlignmentMap: []AlignmentInfo{
			{OriginalIndex: 1, Hash: "original-alignment"},
		},
	}

	if err := cache.Set(ctx, "tenant", "session", state, body); err != nil {
		t.Fatal(err)
	}
	body[0] = 'X'
	state.SystemPrompt = "mutated-state"
	state.AlignmentMap[0].Hash = "mutated-alignment"

	gotState, gotBody, err := cache.GetOrLoad(ctx, "tenant", "session")
	if err != nil {
		t.Fatal(err)
	}
	if gotState.SystemPrompt != "original-state" || gotState.AlignmentMap[0].Hash != "original-alignment" {
		t.Fatalf("Set retained caller-owned state: %+v", gotState)
	}
	if !bytes.Equal(gotBody, []byte("original-body")) {
		t.Fatalf("Set retained caller-owned body: %q", gotBody)
	}

	gotBody[0] = 'Y'
	gotState.SystemPrompt = "mutated-return"
	gotState.AlignmentMap[0].Hash = "mutated-return-alignment"

	againState, againBody, err := cache.GetOrLoad(ctx, "tenant", "session")
	if err != nil {
		t.Fatal(err)
	}
	if againState.SystemPrompt != "original-state" || againState.AlignmentMap[0].Hash != "original-alignment" {
		t.Fatalf("GetOrLoad returned cache-owned state: %+v", againState)
	}
	if !bytes.Equal(againBody, []byte("original-body")) {
		t.Fatalf("GetOrLoad returned cache-owned body: %q", againBody)
	}
}

func TestSessionCacheUpdateSerializesSameSession(t *testing.T) {
	ctx := context.Background()
	cache := NewSessionCache(nil, nil)
	if err := cache.Set(ctx, "tenant", "session", &SessionState{SchemaVersion: schemaVersion}, []byte("body")); err != nil {
		t.Fatal(err)
	}

	const workers = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			err := cache.Update(ctx, "tenant", "session", func(state *SessionState, body []byte) (*SessionState, []byte, error) {
				state.StripsApplied++
				return state, body, nil
			})
			if err != nil {
				t.Errorf("Update: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	got, gotBody, err := cache.GetOrLoad(ctx, "tenant", "session")
	if err != nil {
		t.Fatal(err)
	}
	if got.StripsApplied != workers {
		t.Fatalf("lost concurrent updates: got %d want %d", got.StripsApplied, workers)
	}
	if !bytes.Equal(gotBody, []byte("body")) {
		t.Fatalf("Update did not preserve body: %q", gotBody)
	}
}
