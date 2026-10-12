package admin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func testNetBirdLock() func(context.Context, string) (func(), error) {
	ch := make(chan struct{}, 1)
	return func(ctx context.Context, _ string) (func(), error) {
		select {
		case ch <- struct{}{}:
			return func() { <-ch }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
func TestMobileNodeConfigAndSignature(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("LLMGW_NODE_ID", "gateway-1")
	t.Setenv("LLMGW_NODE_NAME", "Gateway")
	t.Setenv("LLMGW_PUBLIC_ORIGIN", "https://[::1]:443")
	t.Setenv("LLMGW_NETBIRD_ORIGIN", "")
	s, err := NewMobileNodeDescriptorServiceFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s.publicOrigin != "https://[::1]" {
		t.Fatalf("IPv6 origin: %q", s.publicOrigin)
	}
	raw, _, err := s.Create(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(raw, ".")
	data, _ := base64.RawURLEncoding.DecodeString(parts[1])
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), data, signature) {
		t.Fatal("invalid descriptor signature")
	}
	for _, nodeID := range []string{"preset:test", "legacy:1", strings.Repeat("x", 129), "../node"} {
		t.Setenv("LLMGW_NODE_ID", nodeID)
		if _, err := NewMobileNodeDescriptorServiceFromEnv(); err == nil {
			t.Fatalf("accepted %q", nodeID)
		}
	}
	t.Setenv("LLMGW_NODE_ID", "gateway-1")
	key[63] ^= 1
	t.Setenv("LLMGW_NODE_DESCRIPTOR_PRIVATE_KEY", base64.StdEncoding.EncodeToString(key))
	if _, err := NewMobileNodeDescriptorServiceFromEnv(); err == nil {
		t.Fatal("accepted mismatched private/public key")
	}
	for _, origin := range []string{"http://localhost", "https://user:pass@host", "https://host/path", "https://host:99999"} {
		if _, err := normalizeGatewayOrigin(origin); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}
func TestNetBirdReconciliationPreflightAndIdempotence(t *testing.T) {
	for _, mode := range []string{"unmanaged", "missing-source", "range", "duplicate", "managed", "new"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			writes := []string{}
			present, created := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" {
					writes = append(writes, r.Method+" "+r.URL.Path)
				}
				switch r.URL.Path {
				case "/api/groups/gateway":
					if r.Method == "PUT" {
						present = true
						w.WriteHeader(204)
						return
					}
					peers := []any{}
					if present {
						peers = append(peers, map[string]string{"id": "peer"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "gateway", "name": "Gateway", "peers": peers})
				case "/api/groups/mobile":
					if mode == "missing-source" {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "mobile", "name": "Operators"})
				case "/api/peers/peer":
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "peer"})
				case "/api/policies":
					if r.Method == "POST" {
						created = true
						w.WriteHeader(204)
						return
					}
					policies := []any{}
					if mode != "new" || created {
						rule := map[string]any{"name": "Mobile HTTPS", "enabled": true, "action": "accept", "bidirectional": false, "protocol": "tcp", "ports": []string{"443"}, "sources": []any{map[string]string{"id": "mobile"}}, "destinations": []any{map[string]string{"id": "gateway"}}}
						desc := mobilePolicyMarker + " node=node"
						if mode == "unmanaged" {
							desc = "operator owned"
						}
						if mode == "range" {
							rule["port_ranges"] = []any{map[string]int{"start": 1, "end": 65535}}
						}
						policy := map[string]any{"id": "policy", "name": "mobile-policy", "description": desc, "enabled": true, "source_posture_checks": []string{"posture"}, "rules": []any{rule}}
						policies = append(policies, policy)
						if mode == "duplicate" {
							policies = append(policies, policy)
						}
					}
					_ = json.NewEncoder(w).Encode(policies)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			access := &NetBirdGatewayAccess{apiURL: server.URL, token: "test", gatewayGroupID: "gateway", mobileAdminGroupID: "mobile", policyName: "mobile-policy", client: server.Client(), lock: testNetBirdLock()}
			if mode != "managed" && mode != "new" {
				if err := access.EnsureGatewayAccess(context.Background(), "node", "peer"); err == nil {
					t.Fatal("preflight unexpectedly succeeded")
				}
				if len(writes) != 0 {
					t.Fatalf("mutated before validation: %v", writes)
				}
				return
			}
			// Concurrent QR generation must create at most one policy and one membership update.
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := access.EnsureGatewayAccess(context.Background(), "node", "peer"); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			expected := 1
			if mode == "new" {
				expected = 2
			}
			if len(writes) != expected {
				t.Fatalf("writes = %v", writes)
			}
			if mode == "new" && writes[0] != "POST /api/policies" {
				t.Fatalf("membership changed before policy: %v", writes)
			}
		})
	}
}
func TestNetBirdRejectsRedirectAndTrailingResponse(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/redirect" {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		_, _ = w.Write([]byte(`{"id":"one"} {"id":"two"}`))
	}))
	defer source.Close()
	t.Setenv("NETBIRD_API_TOKEN", "test")
	t.Setenv("NETBIRD_GATEWAY_GROUP_ID", "gateway")
	t.Setenv("NETBIRD_MOBILE_ADMIN_GROUP_ID", "mobile")
	t.Setenv("NETBIRD_API_URL", source.URL)
	t.Setenv("NETBIRD_MOBILE_POLICY_NAME", "mobile-policy")
	access, err := NewNetBirdGatewayAccessFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := access.request(context.Background(), "GET", "/redirect", nil, nil); err == nil || leaked {
		t.Fatal("followed redirect")
	}
	var group netBirdGroup
	if err := access.request(context.Background(), "GET", "/invalid", nil, &group); err == nil {
		t.Fatal("accepted trailing JSON")
	}
}

func TestNetBirdDatabaseLock_Live(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lock := netBirdDatabaseLock(pool)
	release, err := lock(context.Background(), "test-mobile-lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if undo, err := lock(ctx, "test-mobile-lock"); err == nil {
		undo()
		release()
		t.Fatal("concurrent database lock acquired")
	}
	release()
	undo, err := lock(context.Background(), "test-mobile-lock")
	if err != nil {
		t.Fatal(err)
	}
	undo()
}
func TestMobileKeyManagementRequiresRole(t *testing.T) {
	h := &Handler{}
	for _, path := range []string{"/api/keys", "/api/keys/1/reveal", "/api/keys/1/disable", "/api/keys/1/enable"} {
		r := SetAuthContext(httptest.NewRequest("POST", path, nil), &AuthContext{Role: "user", TenantID: "default"})
		w := httptest.NewRecorder()
		if path == "/api/keys" {
			h.handleKeysRoot(w, r)
		} else {
			h.handleKeys(w, r)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
}
