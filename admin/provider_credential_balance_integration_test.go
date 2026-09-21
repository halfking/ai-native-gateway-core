package admin

// Integration test for POST /api/providers/{pid}/credentials/{cid}/refresh-balance
// (migration 721, 2026-09-18). Follows the provider_access_test.go
// TEST_DATABASE_URL convention: skipped unless TEST_DATABASE_URL points at a
// schema that already contains migration 721's balance_source/balance_error
// columns; `go test ./admin` stays green in environments without a database.
//
// Coverage (the three handler branches the R40 round left untested):
//   1. 400  — catalog without a balance API (zhipu): configuration fact, no
//             balance_error write, row untouched.
//   2. 200 success=false — vendor endpoint fails: fail-open, previous
//             balance_usd preserved, balance_error stamped, source untouched.
//   3. 200 success=true — deepseek-shaped payload: balance_usd/source/error
//             updated; seeds balance_source='manual' to lock the documented
//             "explicit operator click overwrites the manual stamp" semantic.
//
// The vendor control plane is an httptest loopback server (EgressBlocked
// allows loopback by default), so the probe goes through the real
// FetchBalanceUSD HTTP path — including the Authorization header produced by
// decryptCredStr on the stored ciphertext.
//
// The handler is invoked directly (same package); the providerConsole
// middleware matrix is covered by provider_access_test.go and intentionally
// out of scope here.

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
)

func TestRefreshCredentialBalanceIntegration(t *testing.T) {
	pool := setupTestDB(t)

	// Vendor mock: deepseek /user/balance shape, switchable between success
	// and failure, recording the last Authorization header it saw.
	var (
		mu       sync.Mutex
		fail     bool
		lastAuth string
		authOK   bool
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		lastAuth = r.Header.Get("Authorization")
		failNow := fail
		mu.Unlock()
		if r.URL.Path != "/user/balance" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if lastAuth != "Bearer sk-it-balance-key" {
			authOK = false
		} else {
			authOK = true
		}
		if failNow {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// DeepSeek returns string amounts (ExtractJSONPath must accept them).
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`))
	}))
	t.Cleanup(srv.Close)

	h := &Handler{db: pool, encKey: testFernetKey(t)}
	envelope, err := h.encryptCred([]byte("sk-it-balance-key"))
	if err != nil {
		t.Fatalf("encryptCred: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uniq := time.Now().UnixNano()

	insertProvider := func(catalog string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO providers (tenant_id, code, display_name, protocol, base_url, catalog_code, enabled)
			VALUES ('default', $1, $1, 'openai-completions', $2, $3, TRUE)
			RETURNING id`, fmt.Sprintf("it-balance-%d-%s", uniq, catalog), srv.URL, catalog).Scan(&id); err != nil {
			t.Fatalf("insert %s provider: %v", catalog, err)
		}
		return id
	}
	dsProvider := insertProvider("deepseek")
	zpProvider := insertProvider("zhipu")

	insertCred := func(providerID int, source, label string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, label, status, secret_ciphertext, balance_usd, balance_source)
			VALUES ($1, $2, 'active', $3, 1.00, NULLIF($4, ''))
			RETURNING id`, providerID, label, []byte(envelope), source).Scan(&id); err != nil {
			t.Fatalf("insert credential: %v", err)
		}
		return id
	}
	dsCred := insertCred(dsProvider, "", fmt.Sprintf("it-failopen-%d", uniq))
	zpCred := insertCred(zpProvider, "", fmt.Sprintf("it-unsupported-%d", uniq))
	manualCred := insertCred(dsProvider, "manual", fmt.Sprintf("it-manual-%d", uniq))
	t.Cleanup(func() {
		// Key off the uniq stamp rather than captured ids: a mid-test t.Fatalf
		// (e.g. insert failure) must still sweep every row that made it in,
		// and credential rows must go before providers or the FK blocks them.
		// Exec errors are checked — a silently failed cleanup leaves rows in a
		// shared test database.
		bgCtx := context.Background()
		if _, err := pool.Exec(bgCtx, `DELETE FROM credentials WHERE label LIKE $1`, fmt.Sprintf("it-%%-%d", uniq)); err != nil {
			t.Errorf("cleanup credentials: %v", err)
		}
		if _, err := pool.Exec(bgCtx, `DELETE FROM providers WHERE code LIKE $1`, fmt.Sprintf("it-balance-%d-%%", uniq)); err != nil {
			t.Errorf("cleanup providers: %v", err)
		}
	})

	fetchRow := func(credID int) (usd float64, src, balErr *string, checked *time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT balance_usd, balance_source, balance_error, balance_last_checked_at
			FROM credentials WHERE id = $1`, credID).Scan(&usd, &src, &balErr, &checked); err != nil {
			t.Fatalf("re-read credential %d: %v", credID, err)
		}
		return
	}

	call := func(pid, cid int) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/providers/%d/credentials/%d/refresh-balance", pid, cid), nil)
		h.refreshCredentialBalance(rr, req, pid, cid)
		return rr
	}

	decodeBody := func(rr *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode response %q: %v", rr.Body.String(), err)
		}
		return m
	}

	t.Run("400 vendor without balance API leaves row untouched", func(t *testing.T) {
		rr := call(zpProvider, zpCred)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
		}
		usd, src, balErr, _ := fetchRow(zpCred)
		if usd != 1.00 || src != nil || balErr != nil {
			t.Fatalf("row must be untouched on 400: usd=%v src=%v err=%v", usd, src, balErr)
		}
	})

	t.Run("200 success=false fail-open preserves balance and stamps error", func(t *testing.T) {
		mu.Lock()
		fail = true
		mu.Unlock()

		rr := call(dsProvider, dsCred)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200 on probe failure (fail-open), got %d body=%s", rr.Code, rr.Body.String())
		}
		body := decodeBody(rr)
		if body["success"] != false {
			t.Fatalf("expected success=false, got %v", body["success"])
		}
		if msg, _ := body["error"].(string); !strings.HasPrefix(msg, "balance probe failed") {
			t.Fatalf("expected error message, got %v", body["error"])
		}
		usd, src, balErr, checked := fetchRow(dsCred)
		if usd != 1.00 {
			t.Fatalf("fail-open must preserve previous balance_usd, got %v", usd)
		}
		if src != nil {
			t.Fatalf("fail-open must not stamp balance_source, got %q", *src)
		}
		if balErr == nil || !strings.HasPrefix(*balErr, "balance probe failed") {
			t.Fatalf("balance_error must be stamped, got %v", balErr)
		}
		if checked == nil {
			t.Fatal("balance_last_checked_at must advance on failure too")
		}
	})

	t.Run("200 success=true overwrites manual stamp with api reading", func(t *testing.T) {
		mu.Lock()
		fail = false
		mu.Unlock()

		rr := call(dsProvider, manualCred)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
		}
		body := decodeBody(rr)
		if body["success"] != true {
			t.Fatalf("expected success=true, got %v", body["success"])
		}
		if got, _ := body["balance_usd"].(float64); got != 12.34 {
			t.Fatalf("expected balance_usd=12.34 (string amount accepted), got %v", body["balance_usd"])
		}
		usd, src, balErr, checked := fetchRow(manualCred)
		if usd != 12.34 {
			t.Fatalf("balance_usd must be updated, got %v", usd)
		}
		if src == nil || *src != "api" {
			t.Fatalf("balance_source must become 'api' (explicit click overwrites manual), got %v", src)
		}
		if balErr != nil {
			t.Fatalf("balance_error must be cleared on success, got %q", *balErr)
		}
		if checked == nil {
			t.Fatal("balance_last_checked_at must advance")
		}
		mu.Lock()
		sentAuth, ok := lastAuth, authOK
		mu.Unlock()
		if !ok || sentAuth != "Bearer sk-it-balance-key" {
			t.Fatalf("vendor must receive decrypted key via Authorization header, got %q", sentAuth)
		}
	})
}
