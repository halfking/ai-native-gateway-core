package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortedMapKeys returns a map's keys in a stable order so an echoed marker set
// does not depend on Go's randomised map iteration.
func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// hopTestRedis builds an interceptor whose session map holds entries.
func hopTestInterceptor(t *testing.T, tenant, session string, entries map[string]string) *SanitizeRestoreInterceptor {
	t.Helper()
	rdb := setupSaniGuardRedis(t)
	for token, value := range entries {
		require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session), token, value).Err())
	}
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	return it
}

// hopCtx builds a restore context carrying the inbound marker set, exactly as
// InterceptNonStream does from InterceptRequest.SanitizeHop.
func hopCtx(hop response.SanitizeHop) context.Context {
	if foreign := foreignMarkerSetFromHop(hop); foreign != nil {
		return WithForeignMarkers(context.Background(), foreign)
	}
	return context.Background()
}

func hopFrom(body string) response.SanitizeHop {
	return SanitizeHopFromRequest("downstream-nonce", []byte(body))
}

func TestCollectForeignMarkers(t *testing.T) {
	assert.Equal(t, []string{"{SENSITIVE:internal_ip:1}", "{SENSITIVE:secret:1}"},
		CollectForeignMarkers([]byte(`a {SENSITIVE:internal_ip:1} b {SENSITIVE:secret:1} c {SENSITIVE:secret:1}`)),
		"duplicates collapse and the result is sorted")

	assert.Nil(t, CollectForeignMarkers([]byte(`no markers here`)))
	assert.Nil(t, CollectForeignMarkers(nil))
	assert.Empty(t, CollectForeignMarkers([]byte(`{SENSITIVE:secret:X} degenerate only`)),
		"a span that does not parse names no canonical marker")
}

// TestSanitizeHopNeedsBothHeaderAndMarkers pins the gate: the header alone must
// not buy the exemption, or any client could set it and have its own forged
// markers echoed back unmasked.
func TestSanitizeHopNeedsBothHeaderAndMarkers(t *testing.T) {
	body := []byte(`{"m":"use {SENSITIVE:secret:1}"}`)

	assert.False(t, SanitizeHopFromRequest("", body).Enabled, "markers without a peer declaration")
	assert.False(t, SanitizeHopFromRequest("nonce", []byte(`{"m":"nothing here"}`)).Enabled,
		"a peer declaration with no markers grants nothing")
	assert.True(t, SanitizeHopFromRequest("nonce", body).Enabled)
}

// TestHopPassesThroughForeignMarker is the core regression: a marker a
// downstream gateway minted survives the upstream gateway untouched instead of
// being destroyed.
func TestHopPassesThroughForeignMarker(t *testing.T) {
	const tenant, session = "tenant-hop", "session-hop"
	it := hopTestInterceptor(t, tenant, session, nil)
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"connect to {SENSITIVE:internal_ip:1} now"}}]}`)

	result, err := it.InterceptNonStream(hopCtx(hop), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body, SanitizeHop: hop,
	})
	require.NoError(t, err)
	if result != nil {
		require.False(t, result.ShouldBlock, "a foreign marker must not fail the response closed")
		// Non-nil means the gateway touched the body; the peer's marker must
		// still be in it, verbatim, for the peer to restore.
		assert.Contains(t, string(result.ModifiedBody), "{SENSITIVE:internal_ip:1}")
		return
	}
	// Nil means the gateway had nothing to do at all, so the body reaches the
	// client byte-for-byte — which is the strongest form of pass-through.
	assert.Contains(t, string(body), "{SENSITIVE:internal_ip:1}")
}

// TestHopStillRestoresOwnMarkers proves the exemption is additive: this gateway
// keeps restoring the markers it issued even while a hop is active.
func TestHopStillRestoresOwnMarkers(t *testing.T) {
	const tenant, session = "tenant-hop-mixed", "session-hop-mixed"
	it := hopTestInterceptor(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "own-secret",
	})
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"mine {SENSITIVE:secret:1} theirs {SENSITIVE:internal_ip:1}"}}]}`)

	result, err := it.InterceptNonStream(hopCtx(hop), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body, SanitizeHop: hop,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	assert.Equal(t, "mine own-secret theirs {SENSITIVE:internal_ip:1}", got.Choices[0].Message.Content)
}

// TestHopDoesNotExemptForgedMarkers is the security test. Passthrough is
// granted per marker that arrived inbound, never to a marker this gateway is
// merely unable to resolve — so a model that invents one is still redacted.
func TestHopDoesNotExemptForgedMarkers(t *testing.T) {
	const tenant, session = "tenant-hop-forged", "session-hop-forged"
	it := hopTestInterceptor(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "real-secret",
	})
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"peer {SENSITIVE:internal_ip:1} forged {SENSITIVE:secret:99} and {SENSITIVE:phone:7}"}}]}`)

	result, err := it.InterceptNonStream(hopCtx(hop), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body, SanitizeHop: hop,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	content := got.Choices[0].Message.Content
	assert.Contains(t, content, "{SENSITIVE:internal_ip:1}", "the peer's marker passes")
	assert.Contains(t, content, "[REDACTED]")
	assert.NotContains(t, content, "SENSITIVE:secret:99", "a marker that never arrived is still redacted")
	assert.NotContains(t, content, "SENSITIVE:phone:7")
}

// TestHopWithoutPeerDeclarationIsUnchanged pins the default: with no hop signal
// the upstream behaves exactly as before, foreign-looking markers and all.
func TestHopWithoutPeerDeclarationIsUnchanged(t *testing.T) {
	const tenant, session = "tenant-nohop", "session-nohop"
	it := hopTestInterceptor(t, tenant, session, nil)

	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"connect to {SENSITIVE:internal_ip:1}"}}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, string(result.ModifiedBody), "[REDACTED]")
	assert.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:internal_ip:1}")
}

// TestHopDoesNotExemptExpiredOwnMapping protects the fail-closed guarantee: a
// marker this gateway minted is not in the inbound set, so losing its mapping
// still fails closed instead of leaking the internal token to the client.
func TestHopDoesNotExemptExpiredOwnMapping(t *testing.T) {
	const tenant, session = "tenant-hop-expired", "session-hop-expired"
	it := hopTestInterceptor(t, tenant, session, nil)
	// The peer declared a different marker; ours never arrived inbound.
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"{SENSITIVE:secret:1}"}}]}`)

	result, err := it.InterceptNonStream(hopCtx(hop), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body, SanitizeHop: hop,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, string(result.ModifiedBody), "[REDACTED]",
		"an unresolvable marker of ours must still be masked")
}

// TestHopNativeResponsesLane covers the native Responses envelope, where the
// restore path differs from legacy chat choices.
func TestHopNativeResponsesLane(t *testing.T) {
	const tenant, session = "tenant-hop-native", "session-hop-native"
	it := hopTestInterceptor(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "own-secret",
	})
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"object":"response","output":[{"type":"message","content":[` +
		`{"type":"output_text","text":"peer {SENSITIVE:internal_ip:1} mine {SENSITIVE:secret:1} forged {SENSITIVE:secret:42}"}]}]}`)

	result, err := it.InterceptNonStream(hopCtx(hop), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body, SanitizeHop: hop,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	assert.Contains(t, string(result.ModifiedBody), "{SENSITIVE:internal_ip:1}")
	assert.Contains(t, string(result.ModifiedBody), "own-secret")
	assert.NotContains(t, string(result.ModifiedBody), "SENSITIVE:secret:42")
}

// TestHopStreamPassesThroughForeignMarker covers the streamed path, including
// the degenerate-marker mask pass that 9c3548691 introduced.
func TestHopStreamPassesThroughForeignMarker(t *testing.T) {
	const tenant, session = "tenant-hop-stream", "session-hop-stream"
	it := hopTestInterceptor(t, tenant, session, nil)
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)
	meta := &response.StreamMeta{
		SessionID: session, TenantID: tenant,
		State: response.NewStreamState(), SanitizeHop: hop,
	}

	var wire strings.Builder
	for _, delta := range []string{
		`{"choices":[{"delta":{"content":"connect to {"}}]}`,
		`{"choices":[{"delta":{"content":"SENSITIVE:internal_ip:1}"}}]}`,
		`{"choices":[{"delta":{"content":" and forged {SENSITIVE:secret:9}"}}]}`,
	} {
		chunk := []byte("data: " + delta + "\n\n")
		result, err := it.InterceptStreamChunk(hopCtx(hop), chunk, meta)
		require.NoError(t, err)
		if result != nil {
			require.False(t, result.ShouldBlock, delta)
			if result.ModifiedChunk != nil {
				wire.Write(result.ModifiedChunk)
				continue
			}
		}
		wire.Write(chunk)
	}

	out := wire.String()
	require.NotContains(t, out, "SENSITIVE:secret:9", "a forged marker must not survive the stream")
	require.Contains(t, out, "SENSITIVE:internal_ip:1", "the peer's marker must come back verbatim")
}

// TestValidatePlaceholdersIgnoresForeignMarkers keeps the tamper metric honest:
// a marker that crossed a hop is not model forgery and must not be counted.
func TestValidatePlaceholdersIgnoresForeignMarkers(t *testing.T) {
	const tenant, session = "tenant-hop-observe", "session-hop-observe"
	it := hopTestInterceptor(t, tenant, session, map[string]string{
		"{SENSITIVE:secret:1}": "own",
	})
	hop := hopFrom(`{"m":"{SENSITIVE:internal_ip:1}"}`)

	body := []byte(`{"choices":[{"message":{"content":"peer {SENSITIVE:internal_ip:1} forged {SENSITIVE:secret:99}"}}]}`)
	assert.Equal(t, []string{"{SENSITIVE:secret:99}"},
		it.validatePlaceholders(hopCtx(hop), body, SanitizeMap{"{SENSITIVE:secret:1}": "own"}))
}

// TestTwoGatewayRoundTrip is the end-to-end proof: a downstream gateway sanitizes
// a real request, an upstream gateway of the same build passes the markers
// through untouched, and the downstream restores the original values.
func TestTwoGatewayRoundTrip(t *testing.T) {
	const (
		hostIP   = "10.24.7.91"
		password = "Sup3rS3cret!"
	)
	rdb := setupSaniGuardRedis(t)

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// Downstream gateway: sanitize a client request that carries a host address
	// and a password, exactly as production does.
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)
	var forwarded []byte
	handler := mw.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		forwarded, _ = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"messages":[{"role":"user","content":"host: `+hostIP+`; password: `+password+`"}]}`))
	req.Header.Set("X-Gw-Session-Id", "two-hop")
	req.Header.Set("X-Gw-Tenant-Id", "tenant-two-hop")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.Contains(t, string(forwarded), "{SENSITIVE:", "downstream must have sanitized")
	require.NotContains(t, string(forwarded), hostIP)
	require.NotContains(t, string(forwarded), password)

	downstreamMap, err := rdb.HGetAll(context.Background(),
		sanitizeMapKey("", "two-hop")).Result()
	require.NoError(t, err)
	require.NotEmpty(t, downstreamMap)

	// The downstream declares itself a hop on the upstream request, the way the
	// executor now stamps it.
	upstreamReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(forwarded))
	upstreamReq.Header.Set("X-Gw-Session-Id", "two-hop-upstream")
	upstreamReq.Header.Set("X-Gw-Tenant-Id", "tenant-two-hop")
	upstreamReq.Header.Set(SanitizeHopHeader, "downstream-instance")

	// Upstream gateway: its own input stage sees the peer's markers.
	upstreamMW, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)
	var upstreamSeen []byte
	upstreamHandler := upstreamMW.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		upstreamSeen, _ = io.ReadAll(r.Body)
	}))
	upstreamHandler.ServeHTTP(httptest.NewRecorder(), upstreamReq)
	require.Equal(t, string(forwarded), string(upstreamSeen),
		"the upstream must not rewrite the peer's markers")

	// The model echoes the markers that arrived, plus one it invented.
	echoed := strings.Join(sortedMapKeys(downstreamMap), " and ")
	modelReply := []byte(`{"choices":[{"message":{"role":"assistant","content":"` +
		echoed + ` and {SENSITIVE:secret:77}"}}]}`)

	upstreamHop := SanitizeHopFromContext(upstreamReq.Context())
	require.True(t, upstreamHop.Enabled, "the upstream must have detected the hop")

	upstreamRestore, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	upstreamCtx := WithForeignMarkers(context.Background(), foreignMarkerSetFromHop(upstreamHop))
	result, err := upstreamRestore.InterceptNonStream(upstreamCtx, &response.InterceptRequest{
		TenantID: "tenant-two-hop", SessionID: "two-hop-upstream",
		ResponseBody: modelReply, SanitizeHop: upstreamHop,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "the upstream must not block a hop response")

	// Downstream restores the values on the way home.
	downstreamRestore, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	// TenantID stays empty here: the test request carries no authenticated
	// tenant, so the input stage filed the map under the unknown-tenant scope.
	final, err := downstreamRestore.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "two-hop", ResponseBody: result.ModifiedBody,
	})
	require.NoError(t, err)
	require.NotNil(t, final)
	require.False(t, final.ShouldBlock)

	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(final.ModifiedBody, &got))
	content := got.Choices[0].Message.Content
	assert.Contains(t, content, hostIP, "the host address must survive the round trip")
	assert.Contains(t, content, password, "the password must survive the round trip")
	assert.NotContains(t, content, "SENSITIVE:secret:77", "the forged marker stays redacted")
}

// TestSanitizeHopForOutboundRequiresRealSanitizing pins the security-relevant
// half of the outbound signal: the hop header is derived from what this
// instance actually sanitized, never from an inbound header. Otherwise a client
// could put a peer-looking request in front of an upstream instance and collect
// the passthrough exemption.
func TestSanitizeHopForOutboundRequiresRealSanitizing(t *testing.T) {
	assert.Empty(t, SanitizeHopForOutbound(context.Background()),
		"no sanitize map means nothing to declare")

	empty := WithSanitizeMap(context.Background(), SanitizeMap{})
	assert.Empty(t, SanitizeHopForOutbound(empty), "an empty map sanitized nothing")

	sanitized := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:1}": "v"})
	nonce := SanitizeHopForOutbound(sanitized)
	assert.NotEmpty(t, nonce)
	assert.Equal(t, nonce, SanitizeHopNonce(), "the nonce is this process's stable label")

	// A client-supplied hop header on the inbound request must not influence it.
	forged, err := http.NewRequestWithContext(sanitized, http.MethodPost, "/", nil)
	require.NoError(t, err)
	forged.Header.Set(SanitizeHopHeader, "attacker")
	assert.Equal(t, nonce, SanitizeHopForOutbound(forged.Context()),
		"the outbound signal comes from our own sanitizing, not from a request header")
}

// TestAdoptSanitizeHopGate covers the inbound gate end to end at context level.
func TestAdoptSanitizeHopGate(t *testing.T) {
	body := []byte(`{"m":"{SENSITIVE:secret:1}"}`)

	assert.False(t, SanitizeHopFromContext(AdoptSanitizeHop(context.Background(), "", body)).Enabled)
	assert.False(t, SanitizeHopFromContext(AdoptSanitizeHop(context.Background(), "nonce", []byte(`{}`))).Enabled)
	assert.True(t, SanitizeHopFromContext(AdoptSanitizeHop(context.Background(), "nonce", body)).Enabled)

	// An unusable hop must not leave a half-armed context behind.
	ctx := AdoptSanitizeHop(context.Background(), "nonce", body)
	require.True(t, SanitizeHopFromContext(ctx).Enabled)
	assert.NotNil(t, foreignMarkersFromContext(WithForeignMarkers(ctx, foreignMarkerSetFromHop(SanitizeHopFromContext(ctx)))))
	assert.Nil(t, foreignMarkersFromContext(context.Background()),
		"no hop means no exemption at all")
}

// TestHopMalformedMarkerStillFailsClosed covers the residual probe's own edge: a
// marker prefix with no closing brace cannot be parsed, so it cannot be shown to
// be foreign — and the exemption must not stretch to cover it.
func TestHopMalformedMarkerStillFailsClosed(t *testing.T) {
	hop := hopFrom(`{"m":"{SENSITIVE:secret:1}"}`)
	ctx := hopCtx(hop)

	assert.True(t, containsNonForeignMarker(ctx, []byte(`a {SENSITIVE:secret:1`)),
		"an unterminated span is not a foreign marker")
	assert.True(t, containsNonForeignMarker(ctx, []byte(`a {SENSITIVE:secret:X}`)),
		"a non-numeric index is not a foreign marker")
	assert.False(t, containsNonForeignMarker(ctx, []byte(`a {SENSITIVE:secret:1} b`)),
		"the peer's own marker is not this gateway's residue")
	assert.False(t, containsNonForeignMarker(ctx, []byte(`nothing here`)))
}
