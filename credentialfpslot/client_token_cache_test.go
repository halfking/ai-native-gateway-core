package credentialfpslot

import (
	"container/list"
	"fmt"
	"testing"
	"time"
)

func TestClientTokenHolderCacheEvictsIdleAndLeastRecentEntries(t *testing.T) {
	clientTokenStateMu.Lock()
	originalHolders := clientTokenHolders
	originalLRU := clientTokenLRU
	originalLastCleanup := clientTokenLastCleanup
	clientTokenHolders = make(map[string]*clientTokenHolder)
	clientTokenLRU = list.New()
	clientTokenLastCleanup = time.Time{}
	clientTokenStateMu.Unlock()
	t.Cleanup(func() {
		clientTokenStateMu.Lock()
		clientTokenHolders = originalHolders
		clientTokenLRU = originalLRU
		clientTokenLastCleanup = originalLastCleanup
		clientTokenStateMu.Unlock()
	})

	now := time.Now()
	clientTokenStateMu.Lock()
	for i := 0; i < clientTokenHolderMaxEntries+1; i++ {
		key := fmt.Sprintf("tenant\x00user-%d", i)
		entry := &clientTokenHolder{key: key, clientType: "unknown", lastSeen: now}
		entry.lru = clientTokenLRU.PushFront(entry)
		clientTokenHolders[key] = entry
	}
	cleanupClientTokenStateLocked(now)
	if got := len(clientTokenHolders); got != clientTokenHolderMaxEntries {
		clientTokenStateMu.Unlock()
		t.Fatalf("holder cache length = %d, want %d", got, clientTokenHolderMaxEntries)
	}

	oldest := &clientTokenHolder{key: "tenant\x00idle", clientType: "unknown", lastSeen: now.Add(-clientTokenHolderIdleTTL - time.Second)}
	oldest.lru = clientTokenLRU.PushBack(oldest)
	clientTokenHolders[oldest.key] = oldest
	cleanupClientTokenStateLocked(now)
	_, present := clientTokenHolders[oldest.key]
	clientTokenStateMu.Unlock()
	if present {
		t.Fatal("idle holder was not evicted")
	}
}
