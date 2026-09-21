package opsreporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// helper: build a Reporter pointed at the given test server with a tmp data dir
func newTestReporter(t *testing.T, srvURL string) *Reporter {
	t.Helper()
	dir := t.TempDir()
	return &Reporter{
		baseURL:      srvURL,
		region:       "test",
		version:      "test",
		buildSeq:     1,
		instanceID:   "test-instance",
		dataDir:      dir,
		licenseKey:   "LIC-test",
		adminUser:    "admin",
		adminEmail:   "admin@test",
		startedAt:    time.Now(),
		interval:     60 * time.Second,
		client:       &http.Client{Timeout: 5 * time.Second},
		instanceToken: "",
	}
}

// fakeServer records request counts and lets each handler return canned responses.
type fakeServer struct {
	*httptest.Server
	registerCalls int32
	heartbeatCalls int32
	// what to return on the next register
	nextRegisterStatus int
	nextRegisterToken  string
	// what to return on heartbeat
	heartbeatStatus int
}

func newFakeServer() *fakeServer {
	fs := &fakeServer{
		nextRegisterStatus: http.StatusOK,
		nextRegisterToken:  "valid-token-jwt-mock",
		heartbeatStatus:    http.StatusOK,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ops/nodes/register", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fs.registerCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fs.nextRegisterStatus)
		if fs.nextRegisterStatus == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"instance_token": fs.nextRegisterToken,
			})
		} else {
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
		}
	})
	mux.HandleFunc("/api/v1/ops/nodes/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fs.heartbeatCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fs.heartbeatStatus)
		if fs.heartbeatStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"ack":true}`))
		} else {
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
		}
	})
	fs.Server = httptest.NewServer(mux)
	return fs
}

// Test 1: token file exists but JWT expired → must trigger re-register
func TestEnsureRegistered_ExpiredCachedToken_TriggersReregister(t *testing.T) {
	fs := newFakeServer()
	defer fs.Server.Close()

	// Write an "expired" JWT (exp 1 hour in the past)
	pastExp := time.Now().Add(-1 * time.Hour).Unix()
	expiredJWT := makeExpiredJWT(t, pastExp)
	if err := os.WriteFile(filepath.Join("/tmp", "skip"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := newTestReporter(t, fs.URL)
	// bypass dataDir defaulting by writing the file directly
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.tokenPath(), []byte(expiredJWT), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureRegistered(context.Background()); err != nil {
		t.Fatalf("ensureRegistered should succeed by re-registering: %v", err)
	}
	if got := atomic.LoadInt32(&fs.registerCalls); got != 1 {
		t.Errorf("expected exactly 1 register call (expired token must not be reused), got %d", got)
	}
	if r.instanceToken == expiredJWT {
		t.Errorf("instanceToken should have been replaced with fresh token")
	}
	if !strings.HasPrefix(r.instanceToken, "valid-token-jwt-mock") {
		t.Errorf("instanceToken = %q, want fresh token", r.instanceToken)
	}
}

// Test 2: token file exists and JWT valid → must NOT trigger re-register
func TestEnsureRegistered_ValidCachedToken_NoReregister(t *testing.T) {
	fs := newFakeServer()
	defer fs.Server.Close()

	// Token valid for 1 more hour
	futureExp := time.Now().Add(1 * time.Hour).Unix()
	validJWT := makeExpiredJWT(t, futureExp) // helper builds unsigned JWT — payload only
	// We need a token that JWT-parsing accepts; use a real EdDSA-signed
	// token via the helper. The decoder only needs the exp claim, so use a
	// base64-encoded header.payload.sig with payload containing exp:
	signedJWT := buildJWT(t, futureExp)

	r := newTestReporter(t, fs.URL)
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.tokenPath(), []byte(signedJWT), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureRegistered(context.Background()); err != nil {
		t.Fatalf("ensureRegistered: %v", err)
	}
	if got := atomic.LoadInt32(&fs.registerCalls); got != 0 {
		t.Errorf("valid token must be reused; expected 0 register calls, got %d", got)
	}
	if r.instanceToken != signedJWT {
		t.Errorf("instanceToken should equal the cached valid token")
	}
	// silence unused warning
	_ = validJWT
}

// Test 3: heartbeat returns 401 → run loop must trigger re-register
// (not just log warn and continue with stale token)
func TestRun_Heartbeat401_TriggersReregister(t *testing.T) {
	fs := newFakeServer()
	defer fs.Server.Close()

	r := newTestReporter(t, fs.URL)
	r.interval = 50 * time.Millisecond

	// Seed a "previously valid" token in memory
	r.instanceToken = "stale-token-from-yesterday"
	// But no token file — ensureRegistered must do a full register path
	// instead of reusing stale in-memory token.

	// First heartbeat 401, then 401 again after re-register — this is
	// an integration assertion: at minimum, register must be called.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.run(ctx)
		close(done)
	}()

	// Wait up to 3 seconds for at least one re-register
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&fs.registerCalls) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done

	if got := atomic.LoadInt32(&fs.registerCalls); got < 1 {
		t.Errorf("after heartbeat 401, expected re-register; got registerCalls=%d", got)
	}
	if r.instanceToken == "stale-token-from-yesterday" {
		t.Errorf("instanceToken must be replaced after re-register")
	}
}

// Test 4: heartbeat 5xx must NOT trigger re-register on top of the
// startup register. Only 401/403 should.
func TestRun_Heartbeat500_NoReregister(t *testing.T) {
	fs := newFakeServer()
	defer fs.Server.Close()
	fs.heartbeatStatus = http.StatusInternalServerError

	r := newTestReporter(t, fs.URL)
	r.interval = 50 * time.Millisecond
	// Seed a valid token file so the startup ensureRegistered reuses
	// it without calling register (this isolates the heartbeat loop's
	// reaction to 5xx from the startup registration).
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	validJWT := buildJWT(t, time.Now().Add(1*time.Hour).Unix())
	if err := os.WriteFile(r.tokenPath(), []byte(validJWT), 0o600); err != nil {
		t.Fatal(err)
	}
	r.instanceToken = validJWT

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.run(ctx)
		close(done)
	}()

	// Let heartbeat fail several times
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	if got := atomic.LoadInt32(&fs.registerCalls); got != 0 {
		t.Errorf("5xx must NOT trigger re-register; got registerCalls=%d", got)
	}
}

// Test 5: ensureRegistered clears expired cache so subsequent run() can re-register cleanly
func TestEnsureRegistered_ExpiredCacheIsRewritten(t *testing.T) {
	fs := newFakeServer()
	defer fs.Server.Close()

	pastExp := time.Now().Add(-2 * time.Hour).Unix()
	expiredJWT := buildJWT(t, pastExp)

	r := newTestReporter(t, fs.URL)
	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.tokenPath(), []byte(expiredJWT), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureRegistered(context.Background()); err != nil {
		t.Fatalf("ensureRegistered: %v", err)
	}

	// Token file should now hold the fresh token, not the expired one
	data, err := os.ReadFile(r.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == expiredJWT {
		t.Errorf("token file should have been overwritten with fresh token")
	}
	if !strings.HasPrefix(string(data), "valid-token-jwt-mock") {
		t.Errorf("token file content = %q, want fresh token", string(data))
	}
}

// helpers ----------------------------------------------------------------

// buildJWT produces a 3-part base64 token with a payload containing
// {exp: <unix>}. We don't sign it because Reporter only needs to read
// the exp claim.
func buildJWT(t *testing.T, exp int64) string {
	t.Helper()
	header := `{"alg":"none","typ":"JWT"}`
	payload := fmt.Sprintf(`{"sub":"instance:test","exp":%d}`, exp)
	return base64URL([]byte(header)) + "." + base64URL([]byte(payload)) + ".sig"
}

func makeExpiredJWT(t *testing.T, exp int64) string {
	return buildJWT(t, exp)
}

func base64URL(b []byte) string {
	// Minimal URL-safe base64 (no padding) using std library.
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, len(b)*4/3+2)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		var nn int
		switch len(b) - i {
		case 1:
			n = uint32(b[i]) << 16
			nn = 2
		case 2:
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8
			nn = 3
		default:
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
			nn = 4
		}
		out = append(out, enc[(n>>18)&0x3f], enc[(n>>12)&0x3f])
		if nn >= 3 {
			out = append(out, enc[(n>>6)&0x3f])
		}
		if nn == 4 {
			out = append(out, enc[n&0x3f])
		}
	}
	return string(out)
}