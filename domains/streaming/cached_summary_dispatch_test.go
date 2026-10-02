package streaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
)

// Check the actual HTTP -> sanitizer -> compressor -> dispatch boundary.
// A truncated, non-sensitive follow-up legitimately grows when cached history
// is assembled; the final NeverWorse check must retain that history.
func TestChatHandlerPreservesCachedSummaryForTruncatedCleanTurn(t *testing.T) {
	const tenantID, sessionID = "tenant-1", "gw_cached_clean_dispatch"
	mini, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mini.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	sanitizer, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(sanitizer, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cache := compression.NewSessionCache(nil, nil)

	// Establish the live dictionary via the actual input middleware, then seed
	// a compressed history that retains only the client's non-sensitive anchor.
	var generation string
	cachedBody := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"[smm_v1:old]\nprior important summary phone {SENSITIVE:phone:1}"},{"role":"assistant","content":"retained anchor"}]}`)
	seed := mw.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		info, ok := compression.SanitizeInfoFromContext(r.Context())
		if !ok || info.MapGeneration == "" {
			t.Fatal("seed did not establish a dictionary")
		}
		generation = info.MapGeneration
		if err := cache.Set(r.Context(), tenantID, sessionID, &compression.SessionState{
			SchemaVersion: 1, MsgCount: 2, SanitizeMapGeneration: generation,
		}, cachedBody); err != nil {
			t.Fatal(err)
		}
	}))
	seedReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"phone 13800138000"}]}`))
	seedReq.Header.Set("X-Gw-Session-Id", sessionID)
	seedReq = seedReq.WithContext(sanitize.WithAuthenticatedTenant(context.Background(), tenantID))
	seedRec := httptest.NewRecorder()
	seed.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed status = %d", seedRec.Code)
	}

	capture := &correlationCaptureExecutor{}
	handler := NewChatHandler(nil, nil, nil, nil, nil, nil)
	handler.SetSanitizeInputMiddleware(mw.Wrap)
	handler.SetSessionCompressor(compression.NewSessionCompressor(compression.SessionCompressorDeps{Cache: cache}))
	handler.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: tenantID, ApplicationID: 7}})
	handler.provider = durableEndpointResolver{}
	handler.executor = &executors.Executor{}
	handler.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	handler.survivalAttemptExec = capture
	clean := []byte(`{"model":"m","stream":true,"messages":[{"role":"assistant","content":"retained anchor"},{"role":"user","content":"next ordinary question"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(clean)))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Gw-Session-Id", sessionID)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if capture.params == nil {
		t.Fatalf("provider dispatch not reached: status=%d response=%s", recorder.Code, recorder.Body.String())
	}
	body := capture.params.BodyBytes
	var upstream struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil {
		t.Fatalf("invalid provider JSON: %v", err)
	}
	if len(upstream.Messages) != 3 {
		t.Fatalf("provider received %d messages, want cached summary + anchor + new turn; body=%s", len(upstream.Messages), body)
	}
	if upstream.Messages[0].Content != "[smm_v1:old]\nprior important summary phone {SENSITIVE:phone:1}" || upstream.Messages[1].Content != "retained anchor" || upstream.Messages[2].Content != "next ordinary question" {
		t.Fatalf("provider history altered: %+v", upstream.Messages)
	}
	if strings.Contains(string(body), "13800138000") {
		t.Fatal("raw phone leaked to provider")
	}
	if len(body) <= len(clean) {
		t.Fatal("fixture did not exercise legitimate cached-history expansion")
	}
	info, ok := compression.SanitizeInfoFromContext(capture.params.R.Context())
	if !ok || info.MapGeneration != generation || info.Stats.PlaceholderCount != 0 || info.RawSnapshot.MessageCount != 2 {
		t.Fatalf("clean-turn context lost identity/scope: %+v", info)
	}
	state, _, err := cache.GetOrLoad(context.Background(), tenantID, sessionID)
	if err != nil || state == nil || state.MsgCount != 3 || state.CompressionSourceSnapshot.MessageCount != 3 {
		t.Fatalf("final committed state does not describe provider history: state=%+v err=%v", state, err)
	}
	// Drain the captured request body only after examining provider payload;
	// no external provider or real credentials are involved.
	if capture.params.R.Body != nil {
		_, _ = io.Copy(io.Discard, capture.params.R.Body)
	}
}
