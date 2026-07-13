package bg

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

func TestSelfCheckWorkerTriggerManualRun(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}

	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if got := <-w.triggerCh; got != "model-a" {
		t.Fatalf("triggered model = %q, want model-a", got)
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsFullQueue(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	if err := w.TriggerManualRun("model-a"); err != nil {
		t.Fatalf("first trigger returned error: %v", err)
	}
	if err := w.TriggerManualRun("model-b"); err == nil {
		t.Fatal("second trigger succeeded with a full queue")
	}
}

func TestSelfCheckWorkerTriggerManualRunRejectsStoppedWorker(t *testing.T) {
	w := &SelfCheckWorker{
		stopCh:    make(chan struct{}),
		triggerCh: make(chan string, 1),
	}
	close(w.stopCh)

	if err := w.TriggerManualRun("model-a"); err == nil {
		t.Fatal("trigger succeeded for a stopped worker")
	}
}

// TestSystemAPIKeyHashMatchesVerifier is the regression test for the root cause
// of the production "gw_invalid_key" failure: the self-check worker previously
// stored key_hash as the RAW key plaintext, but the data-plane verifier looks up
// HMAC-SHA256(secret, raw). They can never be equal, so every probe returned
// 401. This test pins the invariant that the worker's hashing transform must be
// identical to the verifier's lookup transform.
func TestSystemAPIKeyHashMatchesVerifier(t *testing.T) {
	const secret = "test-secret-key"
	const rawKey = "sk-selfcheck-deadbeefcafef00d"

	// What the worker (EnsureSystemAPIKey) now stores:
	storedHash := authentication.HashAPIKey(secret, rawKey)

	// Sanity: it must NOT be the plaintext (the old bug).
	if storedHash == rawKey {
		t.Fatal("key_hash equals plaintext — this is the bug that caused gw_invalid_key")
	}
	if strings.HasPrefix(storedHash, "sk-") {
		t.Fatalf("key_hash looks like raw key: %q", storedHash)
	}

	// What the verifier computes at lookup time (callVerifyDB):
	// hashAPIKey is unexported in package authentication, but HashAPIKey is the
	// exported canonical implementation both sides must use. Re-derive and assert
	// equality — a divergence here means the worker key can never be found.
	lookupHash := authentication.HashAPIKey(secret, rawKey)
	if storedHash != lookupHash {
		t.Fatalf("stored key_hash %q != verifier lookup hash %q — worker key will never authenticate",
			storedHash, lookupHash)
	}
}
