package admin

// Integration test for POST /api/admin/diagnostics/routing-blocked/fix —
// the 待裁决 85 closeout (R46, 2026-10-05).
//
// The DB half of that endpoint was always covered; the fix under test is the
// in-process recovery chain (step 5): every credential whose DB gates the
// WHERE matches must ALSO get its in-process breaker reset / legacy
// credentialstate recovered, or the endpoint keeps returning HTTP 200 while
// the router filters those exact nodes for up to ~5 minutes.
//
// The stubs below record calls so the test can assert the chain ran for the
// blocked credential and — the negative control — did NOT run for the healthy
// sibling (same-source-with-WHERE semantics of the step-0 enumeration).
//
// TEST_DATABASE_URL convention: skipped without a database; `go test ./admin`
// stays green in DB-less environments.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

type routingFixCircuitStub struct {
	mu     sync.Mutex
	called map[[2]int]int // (providerID, credentialID) → count
}

func (s *routingFixCircuitStub) Reset(providerID, credentialID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.called == nil {
		s.called = map[[2]int]int{}
	}
	s.called[[2]int{providerID, credentialID}]++
}

type routingFixCredStateStub struct {
	mu    sync.Mutex
	seen  map[int]int // credentialID → UpdateFromProbe calls
	avail map[int]bool
}

func (s *routingFixCredStateStub) UpdateFromProbe(_ context.Context, st *credentialstate.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[int]int{}
		s.avail = map[int]bool{}
	}
	s.seen[st.CredentialID]++
	s.avail[st.CredentialID] = st.Available && st.LastSuccessAt != nil
}

func TestRoutingBlockedFixResetsInProcessStateIntegration(t *testing.T) {
	pool := setupTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uniq := time.Now().UnixNano()

	var providerID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, protocol, base_url, catalog_code, enabled)
		VALUES ('default', $1, $1, 'openai-completions', 'http://it.invalid', 'zhipu', TRUE)
		RETURNING id`, fmt.Sprintf("it-routingfix-%d", uniq)).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	enc := &Handler{db: pool, encKey: testFernetKey(t)}
	envelope, err := enc.encryptCred([]byte("sk-it-routingfix-key"))
	if err != nil {
		t.Fatalf("encryptCred: %v", err)
	}

	insertCred := func(label string, circuit string, fails int) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, label, status, secret_ciphertext,
			                         availability_state, circuit_state, consecutive_failures)
			VALUES ($1, $2, 'active', $3, 'ready', $4, $5)
			RETURNING id`, providerID, label, []byte(envelope), circuit, fails).Scan(&id); err != nil {
			t.Fatalf("insert credential %s: %v", label, err)
		}
		return id
	}
	blockedCred := insertCred(fmt.Sprintf("it-routingfix-blocked-%d", uniq), "open", 3)
	healthyCred := insertCred(fmt.Sprintf("it-routingfix-healthy-%d", uniq), "closed", 0)
	// A binding model for the blocked credential: resetInMemoryNodeState
	// enumerates binding models to drive credentialstate/fpslot recovery —
	// with zero bindings that layer is legitimately a no-op, which would make
	// the assertion below vacuous instead of meaningful.
	var pmID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name)
		VALUES ($1, $2, $2) RETURNING id`,
		providerID, fmt.Sprintf("it-routingfix-model-%d", uniq)).Scan(&pmID); err != nil {
		t.Fatalf("insert provider model: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credential_model_bindings (credential_id, provider_model_id)
		VALUES ($1, $2)`, blockedCred, pmID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	t.Cleanup(func() {
		bgCtx := context.Background()
		if _, err := pool.Exec(bgCtx, `DELETE FROM credential_model_bindings WHERE credential_id = ANY($1)`,
			[]int{blockedCred, healthyCred}); err != nil {
			t.Errorf("cleanup bindings: %v", err)
		}
		if _, err := pool.Exec(bgCtx, `DELETE FROM provider_models WHERE raw_model_name LIKE $1`,
			fmt.Sprintf("it-routingfix-model-%d", uniq)); err != nil {
			t.Errorf("cleanup provider models: %v", err)
		}
		if _, err := pool.Exec(bgCtx, `DELETE FROM credentials WHERE label LIKE $1`,
			fmt.Sprintf("it-routingfix-%%-%d", uniq)); err != nil {
			t.Errorf("cleanup credentials: %v", err)
		}
		if _, err := pool.Exec(bgCtx, `DELETE FROM providers WHERE code LIKE $1`,
			fmt.Sprintf("it-routingfix-%d", uniq)); err != nil {
			t.Errorf("cleanup providers: %v", err)
		}
	})

	circuit := &routingFixCircuitStub{}
	credState := &routingFixCredStateStub{}
	h := &Handler{
		db:                 pool,
		circuitResetter:    circuit,
		credStateRecoverer: credState,
		// fpSlots intentionally nil: the fpslot Manager is a concrete type and
		// its layer is pinned by the healthstateguard symbol gate; this test
		// covers the two injectable layers end to end.
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/diagnostics/routing-blocked/fix",
		strings.NewReader(fmt.Sprintf(`{"provider_id":%d}`, providerID)))
	req.Header.Set("Content-Type", "application/json")
	h.handleRoutingBlockedFix(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fix should be 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		InMemoryResetCredentials int `json:"in_memory_reset_credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.InMemoryResetCredentials != 1 {
		t.Fatalf("in_memory_reset_credentials: want exactly the 1 blocked credential, got %d "+
			"(0 ⇒ the chain never ran — 待裁决 85 regression; 2 ⇒ step-0 enumeration lost its WHERE)",
			resp.InMemoryResetCredentials)
	}

	circuit.mu.Lock()
	blockedCalls := circuit.called[[2]int{providerID, blockedCred}]
	healthyCalls := circuit.called[[2]int{providerID, healthyCred}]
	circuit.mu.Unlock()
	if blockedCalls == 0 {
		t.Fatalf("in-process breaker was NOT reset for blocked credential %d — "+
			"the endpoint again promises recovery while the router keeps filtering", blockedCred)
	}
	if healthyCalls != 0 {
		t.Fatalf("breaker reset ran for healthy credential %d — enumeration is broader than the reset WHERE", healthyCred)
	}

	credState.mu.Lock()
	availOK := credState.avail[blockedCred]
	healthySeen := credState.seen[healthyCred]
	credState.mu.Unlock()
	if !availOK {
		t.Fatalf("legacy credentialstate not recovered with probe-success semantics for blocked credential %d", blockedCred)
	}
	if healthySeen != 0 {
		t.Fatalf("credentialstate recovery ran for healthy credential %d", healthyCred)
	}

	// DB half still works: the blocked credential's gates are actually cleared.
	var circuitAfter string
	var failsAfter int
	if err := pool.QueryRow(ctx, `
		SELECT circuit_state, consecutive_failures FROM credentials WHERE id = $1`,
		blockedCred).Scan(&circuitAfter, &failsAfter); err != nil {
		t.Fatalf("re-read blocked credential: %v", err)
	}
	if circuitAfter != "closed" || failsAfter != 0 {
		t.Fatalf("DB gates not cleared: circuit_state=%s consecutive_failures=%d", circuitAfter, failsAfter)
	}
}
