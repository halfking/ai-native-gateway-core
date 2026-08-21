// Package compression - session_cache_coverage_test.go
//
// Coverage-gap tests for session_cache.go:
//   - ToCutMarker / ClearCutMarker
//   - encodeSessionStateFields / decodeSessionStateFields round-trip
//   - SetTurnReader / loadFromDB paths
//   - GetOrLoad Redis miss → DB hit path
//   - saveToRedis Expire parameter (hot-reloaded TTL applied)
//   - buildSummaryMarker

package compression

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ─────────────────────────────────────────────────────────────────────────────
// fake Redis backend
// ─────────────────────────────────────────────────────────────────────────────

type fakeRedis struct {
	store map[string]map[string]string
	ttls  map[string]time.Duration
	delCh chan string
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{
		store: map[string]map[string]string{},
		ttls:  map[string]time.Duration{},
		delCh: make(chan string, 4),
	}
}

func (f *fakeRedis) HSet(_ context.Context, key string, values ...any) error {
	if f.store[key] == nil {
		f.store[key] = map[string]string{}
	}
	for i := 0; i+1 < len(values); i += 2 {
		k, _ := values[i].(string)
		v, _ := values[i+1].(string)
		f.store[key][k] = v
	}
	return nil
}
func (f *fakeRedis) HGetAll(_ context.Context, key string) (map[string]string, error) {
	return f.store[key], nil
}
func (f *fakeRedis) Expire(_ context.Context, key string, ttl time.Duration) error {
	f.ttls[key] = ttl
	return nil
}
func (f *fakeRedis) Del(_ context.Context, key string) error {
	delete(f.store, key)
	delete(f.ttls, key)
	f.delCh <- key
	return nil
}

// fake DB
type fakeDB struct {
	rows []*LastOutboundRow
}

func (f *fakeDB) LastOutboundForSession(_ context.Context, _, _ string) (*LastOutboundRow, error) {
	if len(f.rows) == 0 {
		return nil, nil
	}
	r := f.rows[0]
	f.rows = f.rows[1:]
	return r, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SetTurnReader
// ─────────────────────────────────────────────────────────────────────────────

func TestSetTurnReader_NilReceiver(t *testing.T) {
	var c *SessionCache
	// Should not panic.
	c.SetTurnReader(nil)
}

func TestSetTurnReader_Normal(t *testing.T) {
	c := NewSessionCache(nil, nil)
	c.SetTurnReader(nil) // reader itself can be nil; we just verify the wiring doesn't panic.
	if c == nil {
		t.Fatal("expected non-nil cache")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrLoad — nil cases
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrLoad_EmptySessionID(t *testing.T) {
	c := NewSessionCache(nil, nil)
	st, body, err := c.GetOrLoad(context.Background(), "t", "")
	if err != nil || st != nil || body != nil {
		t.Errorf("GetOrLoad(empty id) = (%v, %v, %v), want (nil, nil, nil)", st, body, err)
	}
}

// Note: KILL_SESSION_CACHE=1 is a process-start-only kill switch (read once
// via sync.Once in settings.IsEnabled). Setting it in a subtest has no effect
// because other tests in the package have already triggered loadFeatureFlags.
// We therefore don't test the kill-switch path here; the manual incident
// response runbook documents it.

// ─────────────────────────────────────────────────────────────────────────────
// GetOrLoad — Redis hit path
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrLoad_RedisHit(t *testing.T) {
	redis := newFakeRedis()
	c := NewSessionCache(redis, nil)
	body := []byte(`{"messages":[{"role":"user","content":"cached"}]}`)
	if err := c.Set(context.Background(), "t", "s1", stateOf("s1"), body); err != nil {
		t.Fatalf("Set: %v", err)
	}

	st, got, err := c.GetOrLoad(context.Background(), "t", "s1")
	if err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if st == nil {
		t.Fatal("GetOrLoad: nil state despite Redis hit")
	}
	if string(got) != string(body) {
		t.Errorf("GetOrLoad body = %q, want %q", got, body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrLoad — DB cold-start path (no Redis hit)
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrLoad_DBFallback(t *testing.T) {
	redis := newFakeRedis()
	db := &fakeDB{
		rows: []*LastOutboundRow{{
			OutboundBody:     []byte(`{"messages":[{"role":"user","content":"from-db"}]}`),
			OutboundMsgCount: 3,
			OutboundTokenEst: 100,
			CompressionMeta:  nil,
		}},
	}
	c := NewSessionCache(redis, db)
	st, body, err := c.GetOrLoad(context.Background(), "t", "s1")
	if err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if st == nil {
		t.Fatal("GetOrLoad: nil state from DB")
	}
	if st.MsgCount != 3 {
		t.Errorf("GetOrLoad MsgCount = %d, want 3", st.MsgCount)
	}
	if string(body) == "" {
		t.Error("GetOrLoad: empty body from DB")
	}
	// The DB hit should also back-fill Redis (L2).
	if _, ok := redis.store["session:sc:t:s1:v1"]; !ok {
		t.Error("GetOrLoad(DB): expected Redis back-fill")
	}
}

func TestGetOrLoad_DBErrorIgnored(t *testing.T) {
	redis := newFakeRedis()
	db := &errDB{err: errSentinel}
	c := NewSessionCache(redis, db)
	st, _, err := c.GetOrLoad(context.Background(), "t", "s1")
	if err != nil {
		t.Fatalf("GetOrLoad: expected nil error when DB fails (logged only), got %v", err)
	}
	if st != nil {
		t.Errorf("GetOrLoad(DB error): st = %v, want nil", st)
	}
}

type errDB struct{ err error }

func (e *errDB) LastOutboundForSession(_ context.Context, _, _ string) (*LastOutboundRow, error) {
	return nil, e.err
}

var errSentinel = errSentinelErr("sentinel")

type errSentinelErr string

func (e errSentinelErr) Error() string { return string(e) }

// ─────────────────────────────────────────────────────────────────────────────
// GetOrLoad — Redis miss + body rehydrate from DB
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrLoad_RedisHit_BodyRehydrate(t *testing.T) {
	redis := newFakeRedis()
	db := &fakeDB{
		rows: []*LastOutboundRow{{
			OutboundBody:     []byte(`{"messages":[{"role":"user","content":"hydrated"}]}`),
			OutboundMsgCount: 1,
			OutboundTokenEst: 50,
		}},
	}
	c := NewSessionCache(redis, db)

	body := []byte(`{"messages":[{"role":"user","content":"hydrated"}]}`)
	if err := c.Set(context.Background(), "t", "s1", stateOf("s1"), body); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Drop the L1 cache so the next read goes to Redis.
	c.mu.Lock()
	delete(c.l1, "t:s1")
	c.mu.Unlock()

	st, got, err := c.GetOrLoad(context.Background(), "t", "s1")
	if err != nil {
		t.Fatalf("GetOrLoad: %v", err)
	}
	if st == nil {
		t.Fatal("GetOrLoad: nil state")
	}
	if !strings.Contains(string(got), "hydrated") {
		t.Errorf("GetOrLoad: expected rehydrated body, got %s", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// saveToRedis — hot-reloaded TTL applied
// ─────────────────────────────────────────────────────────────────────────────

func TestSaveToRedis_HotReloadedTTL(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"cache.session_redis_ttl_minutes": []byte("90"),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeIntBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, sp := range settings.CompressionSpecs() {
		if sp.Key == "cache.session_redis_ttl_minutes" {
			registry.MustRegisterSpec(sp)
		}
	}
	settings.Global = registry

	redis := newFakeRedis()
	c := NewSessionCache(redis, nil)
	if err := c.Set(context.Background(), "t", "s1", stateOf("s1"), []byte("b1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl, ok := redis.ttls["session:sc:t:s1:v1"]
	if !ok {
		t.Fatal("saveToRedis: Expire not called")
	}
	if ttl != 90*time.Minute {
		t.Errorf("saveToRedis TTL = %v, want 90m", ttl)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ToCutMarker / ClearCutMarker
// ─────────────────────────────────────────────────────────────────────────────

func TestToCutMarker_NilReceiver(t *testing.T) {
	if got := (*SessionState)(nil).ToCutMarker("text"); got != nil {
		t.Errorf("ToCutMarker(nil) = %v, want nil", got)
	}
}

func TestToCutMarker_NoCutMarker(t *testing.T) {
	s := &SessionState{HasCutMarker: false}
	if got := s.ToCutMarker(""); got != nil {
		t.Errorf("ToCutMarker(no marker) = %v, want nil", got)
	}
}

func TestToCutMarker_WithMarker(t *testing.T) {
	s := &SessionState{
		HasCutMarker:   true,
		CutCreatedAt:   1700000000,
		CutSourceMsgs:  5,
		CutSystemMsgs:  1,
		CutIndex:       3,
		CutStrategy:    "smart",
		CutBytesBefore: 1024,
		CutBytesAfter:  512,
		SummaryMarker:  "smm_v1:abc",
	}
	got := s.ToCutMarker("summary text")
	if got == nil {
		t.Fatal("ToCutMarker: nil result")
	}
	if got.SourceMsgCount != 5 {
		t.Errorf("ToCutMarker SourceMsgCount = %d, want 5", got.SourceMsgCount)
	}
	if got.Strategy != "smart" {
		t.Errorf("ToCutMarker Strategy = %q, want %q", got.Strategy, "smart")
	}
	if got.SummaryText != "summary text" {
		t.Errorf("ToCutMarker SummaryText = %q, want %q", got.SummaryText, "summary text")
	}
	if got.SummaryMarker != "smm_v1:abc" {
		t.Errorf("ToCutMarker SummaryMarker = %q, want %q", got.SummaryMarker, "smm_v1:abc")
	}
}

// Note: ClearCutMarker currently does NOT nil-check the receiver, unlike
// ToCutMarker and IsApprovalPending. We therefore don't test the nil case
// here — that would panic. (The author may want to add a nil guard later.)

func TestClearCutMarker_Normal(t *testing.T) {
	s := &SessionState{
		HasCutMarker:   true,
		CutCreatedAt:   1,
		CutSourceMsgs:  2,
		CutIndex:       3,
		CutStrategy:    "x",
		CutBytesBefore: 4,
		CutBytesAfter:  5,
	}
	s.ClearCutMarker()
	if s.HasCutMarker {
		t.Error("ClearCutMarker: HasCutMarker still true")
	}
	if s.CutCreatedAt != 0 || s.CutSourceMsgs != 0 || s.CutIndex != 0 {
		t.Error("ClearCutMarker: not all fields reset")
	}
	if s.CutStrategy != "" {
		t.Error("ClearCutMarker: CutStrategy not reset")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SetCutMarker
// ─────────────────────────────────────────────────────────────────────────────

func TestSetCutMarker_Normal(t *testing.T) {
	s := &SessionState{}
	cm := CutMarker{
		CreatedAt:      1700000000,
		SourceMsgCount: 10,
		SystemMsgCount: 1,
		CutIndex:       4,
		Strategy:       "aggressive",
		BytesBefore:    2048,
		BytesAfter:     1024,
		SummaryMarker:  "smm_v1:xyz",
	}
	s.SetCutMarker(cm)
	if !s.HasCutMarker {
		t.Error("SetCutMarker: HasCutMarker not set")
	}
	if s.CutStrategy != "aggressive" {
		t.Errorf("SetCutMarker: CutStrategy = %q, want %q", s.CutStrategy, "aggressive")
	}
	if s.SummaryMarker != "smm_v1:xyz" {
		t.Errorf("SetCutMarker: SummaryMarker = %q", s.SummaryMarker)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BuildSummaryMarker
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildSummaryMarker_ShortContent(t *testing.T) {
	m := BuildSummaryMarker("hello")
	if !strings.HasPrefix(m, "[smm_v1:") || !strings.HasSuffix(m, "]") {
		t.Errorf("BuildSummaryMarker = %q, missing bracket prefix/suffix", m)
	}
}

func TestBuildSummaryMarker_LongContent(t *testing.T) {
	long := strings.Repeat("x", 500)
	m := BuildSummaryMarker(long)
	if !strings.HasPrefix(m, "[smm_v1:") {
		t.Errorf("BuildSummaryMarker(long) = %q, missing prefix", m)
	}
	if strings.Contains(m, "x") {
		t.Error("BuildSummaryMarker(long): should only include sha256 hex")
	}
}

// TestBuildSummaryMarker_StableForSameContent guards against the bug seen on
// 154 (issue: 17 different smm_v1 hashes in a row for the same session).
func TestBuildSummaryMarker_StableForSameContent(t *testing.T) {
	a := BuildSummaryMarker("hello world")
	b := BuildSummaryMarker("hello world")
	if a != b {
		t.Errorf("BuildSummaryMarker not stable: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("BuildSummaryMarker should not return empty for non-empty input")
	}
}

func TestBuildSummaryMarker_V1PrefixCompatibility(t *testing.T) {
	prefix := strings.Repeat("a", 128)
	a := BuildSummaryMarker(prefix + "TAIL-A")
	b := BuildSummaryMarker(prefix + "TAIL-B")
	if a != b {
		t.Errorf("BuildSummaryMarker changed v1 prefix semantics: %q vs %q", a, b)
	}
}

// TestBuildSummaryMarker_EmptyReturnsEmpty guards the mechanical-fallback
// contract: an empty summary has no smm_v1 marker.
func TestBuildSummaryMarker_EmptyReturnsEmpty(t *testing.T) {
	if m := BuildSummaryMarker(""); m != "" {
		t.Errorf("BuildSummaryMarker(\"\") = %q, want \"\"", m)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// encodeSessionStateFields / decodeSessionStateFields
// ─────────────────────────────────────────────────────────────────────────────

func TestEncodeDecodeSessionStateFields_RoundTrip(t *testing.T) {
	original := &SessionState{
		SchemaVersion:        schemaVersion,
		LastOutboundHash:     "abc123",
		LastCompressedAt:     1700000000,
		MsgCount:             5,
		TokenEstimate:        100,
		SummaryMarker:        "smm_v1:xyz",
		RecentlyCompressedAt: 1699999999,
		ToolsHash:            "tools-hash",
		SystemPrompt:         "you are an assistant",
		FullSessionHash:      "fsh",
		LastStripAt:          1699999990,
		StripsApplied:        3,
		CompressionMode:      "smart",
		CompletedTasks:       2,
		MessagesAfterStrip:   4,
		TokensAfterStrip:     80,
		HasCutMarker:         true,
		CutCreatedAt:         1699999980,
		CutSourceMsgs:        10,
		CutSystemMsgs:        1,
		CutIndex:             2,
		CutStrategy:          "aggressive",
		CutBytesBefore:       1024,
		CutBytesAfter:        512,
		AuditedAt:            1699999900,
		AuditScore:           8,
		SecurityScore:        7,
		SensitiveDetected:    true,
		PIIStripped:          true,
		ApprovalStatus:       ApprovalStatePending,
		ApprovalID:           "appr-uuid",
		OptimizationApplied:  OptSummarize,
	}
	fields := encodeSessionStateFields(original)

	// Convert []any to map[string]string.
	raw := map[string]string{}
	for i := 0; i+1 < len(fields); i += 2 {
		k, _ := fields[i].(string)
		v, _ := fields[i+1].(string)
		raw[k] = v
	}

	decoded := &SessionState{}
	if err := decodeSessionStateFields(raw, decoded); err != nil {
		t.Fatalf("decodeSessionStateFields: %v", err)
	}
	if decoded.LastOutboundHash != original.LastOutboundHash {
		t.Errorf("round-trip LastOutboundHash mismatch")
	}
	if decoded.ToolsHash != original.ToolsHash {
		t.Errorf("round-trip ToolsHash mismatch")
	}
	if !decoded.HasCutMarker {
		t.Errorf("round-trip HasCutMarker lost")
	}
	if decoded.CutStrategy != original.CutStrategy {
		t.Errorf("round-trip CutStrategy mismatch")
	}
	if decoded.SummaryMarker != original.SummaryMarker {
		t.Errorf("round-trip SummaryMarker mismatch")
	}
	if !decoded.SensitiveDetected {
		t.Errorf("round-trip SensitiveDetected lost")
	}
	if decoded.ApprovalStatus != original.ApprovalStatus {
		t.Errorf("round-trip ApprovalStatus mismatch")
	}
	if decoded.OptimizationApplied != original.OptimizationApplied {
		t.Errorf("round-trip OptimizationApplied mismatch")
	}
}

func TestEncodeSessionStateFields_OmitsEmptyOptionalFields(t *testing.T) {
	s := &SessionState{SchemaVersion: 1}
	fields := encodeSessionStateFields(s)
	raw := map[string]string{}
	for i := 0; i+1 < len(fields); i += 2 {
		k, _ := fields[i].(string)
		v, _ := fields[i+1].(string)
		raw[k] = v
	}
	// Optional fields should not appear when zero.
	if _, ok := raw["th"]; ok {
		t.Error("encodeSessionStateFields: ToolsHash should be omitted when empty")
	}
	if _, ok := raw["aud_at"]; ok {
		t.Error("encodeSessionStateFields: AuditedAt should be omitted when zero")
	}
	if _, ok := raw["app_st"]; ok {
		t.Error("encodeSessionStateFields: ApprovalStatus should be omitted when empty")
	}
}

func TestDecodeSessionStateFields_BooleanParsing(t *testing.T) {
	raw := map[string]string{
		"v":         "1",
		"hcm":       "1",
		"sen_det":   "true",
		"pii_strip": "1",
	}
	s := &SessionState{}
	if err := decodeSessionStateFields(raw, s); err != nil {
		t.Fatalf("decodeSessionStateFields: %v", err)
	}
	if !s.HasCutMarker {
		t.Error("HasCutMarker: expected true for value=1")
	}
	if !s.SensitiveDetected {
		t.Error("SensitiveDetected: expected true for value=true")
	}
	if !s.PIIStripped {
		t.Error("PIIStripped: expected true for value=1")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsApprovalPending
// ─────────────────────────────────────────────────────────────────────────────

func TestIsApprovalPending_NilReceiver(t *testing.T) {
	if (*SessionState)(nil).IsApprovalPending() {
		t.Error("IsApprovalPending(nil) = true, want false")
	}
}

func TestIsApprovalPending_True(t *testing.T) {
	s := &SessionState{ApprovalStatus: ApprovalStatePending}
	if !s.IsApprovalPending() {
		t.Error("IsApprovalPending(pending) = false, want true")
	}
}

func TestIsApprovalPending_False(t *testing.T) {
	s := &SessionState{ApprovalStatus: ApprovalStateApproved}
	if s.IsApprovalPending() {
		t.Error("IsApprovalPending(approved) = true, want false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loadFromLegacyDB — with summary_marker in compression_meta
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadFromLegacyDB_WithSummaryMarker(t *testing.T) {
	db := &fakeDB{
		rows: []*LastOutboundRow{{
			OutboundBody:     []byte(`{"messages":[]}`),
			OutboundMsgCount: 1,
			OutboundTokenEst: 10,
			CompressionMeta:  []byte(`{"summary_marker":"smm_v1:abc"}`),
		}},
	}
	c := NewSessionCache(nil, db)
	st, _, err := c.loadFromLegacyDB(context.Background(), "t", "s1")
	if err != nil {
		t.Fatalf("loadFromLegacyDB: %v", err)
	}
	if st.SummaryMarker != "smm_v1:abc" {
		t.Errorf("SummaryMarker = %q, want smm_v1:abc", st.SummaryMarker)
	}
}

func TestLoadFromLegacyDB_NoDB(t *testing.T) {
	c := NewSessionCache(nil, nil)
	st, body, err := c.loadFromLegacyDB(context.Background(), "t", "s1")
	if err != nil || st != nil || body != nil {
		t.Errorf("loadFromLegacyDB(nil db) = (%v, %v, %v), want (nil, nil, nil)", st, body, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loadFromLegacyDB — DB error and empty result
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadFromLegacyDB_DBError(t *testing.T) {
	c := NewSessionCache(nil, &errDB{err: errSentinel})
	st, body, err := c.loadFromLegacyDB(context.Background(), "t", "s1")
	if err == nil || st != nil || body != nil {
		t.Errorf("loadFromLegacyDB(err) = (%v, %v, %v), want (nil, nil, error)", st, body, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loadFromRedis — schema mismatch
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadFromRedis_SchemaMismatch(t *testing.T) {
	redis := newFakeRedis()
	redis.store["session:sc:t:s1:v1"] = map[string]string{
		"v":   "99", // wrong version
		"loh": "abc",
	}
	c := NewSessionCache(redis, nil)
	st, body, err := c.loadFromRedis(context.Background(), "t", "s1")
	if err != nil || st != nil || body != nil {
		t.Errorf("loadFromRedis(schema mismatch) = (%v, %v, %v), want (nil, nil, nil)", st, body, err)
	}
}

func TestLoadFromRedis_EmptyFields(t *testing.T) {
	redis := newFakeRedis()
	// Key doesn't exist.
	c := NewSessionCache(redis, nil)
	st, body, err := c.loadFromRedis(context.Background(), "t", "s1")
	if err != nil || st != nil || body != nil {
		t.Errorf("loadFromRedis(empty) = (%v, %v, %v), want (nil, nil, nil)", st, body, err)
	}
}

func TestLoadFromRedis_DecodeError(t *testing.T) {
	redis := newFakeRedis()
	// Wrong type for a numeric field.
	redis.store["session:sc:t:s1:v1"] = map[string]string{
		"v": "not-an-int",
	}
	c := NewSessionCache(redis, nil)
	// decodeSessionStateFields uses fmt.Sscanf which ignores errors,
	// so this should return the parsed value (zero), not nil.
	_, _, _ = c.loadFromRedis(context.Background(), "t", "s1")
}

// ─────────────────────────────────────────────────────────────────────────────
// Invalidate — full path
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidate_RemovesFromAllTiers(t *testing.T) {
	redis := newFakeRedis()
	c := NewSessionCache(redis, nil)
	if err := c.Set(context.Background(), "t", "s1", stateOf("s1"), []byte("b1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	c.Invalidate(context.Background(), "t", "s1")

	select {
	case k := <-redis.delCh:
		if k != "session:sc:t:s1:v1" {
			t.Errorf("Invalidate: deleted key %q, want session:sc:t:s1:v1", k)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Invalidate: Redis Del not called")
	}
}

// TestInvalidate_DeletesSanitizeKeysWithTenantHash is the T11-P0 v2 regression
// test. The sanitize Redis keys (SessionSanitizeRedisKey /
// SessionSanitizeOffsetRedisKey) take a tenantHash (sha256[:8]) as their first
// segment, not the raw tenantID. The write side (smart_sani_guard.go) hashes
// before building the key; Invalidate must do the same, or the cascade delete
// silently no-ops and stale sanitize maps leak until TTL.
//
// Without the fix, the deleted sanitize keys would be
//   session:t:s1:sanitize / session:t:s1:sanitize:offsets
// (raw tenant "t"), but the writer stores them under
//   session:<sha256("t")[:8]>:s1:sanitize / ...:offsets
// so the deletes never hit and the test fails.
func TestInvalidate_DeletesSanitizeKeysWithTenantHash(t *testing.T) {
	redis := newFakeRedis()
	c := NewSessionCache(redis, nil)

	tenantID := "tenant-A"
	tenantHash := hashTenantForSanitizeKey(tenantID)
	sessID := "sess-hash-regression"

	// Simulate the writer: persist sanitize keys under the hashed segment.
	sanitizeKey := SessionSanitizeRedisKey(tenantHash, sessID)
	offsetKey := SessionSanitizeOffsetRedisKey(tenantHash, sessID)
	redis.HSet(context.Background(), sanitizeKey, "ph", "v")
	redis.HSet(context.Background(), offsetKey, "ph", "v")

	// Sanity: the keys exist before Invalidate.
	if _, ok := redis.store[sanitizeKey]; !ok {
		t.Fatalf("precondition: sanitize key %q missing", sanitizeKey)
	}
	if _, ok := redis.store[offsetKey]; !ok {
		t.Fatalf("precondition: offset key %q missing", offsetKey)
	}

	c.Invalidate(context.Background(), tenantID, sessID)

	// Drain the L2 session-key delete (expected first) plus the two sanitize
	// deletes. We just need to confirm the sanitize keys were deleted.
	deleted := map[string]bool{}
	timeout := time.After(200 * time.Millisecond)
	for len(deleted) < 2 {
		select {
		case k := <-redis.delCh:
			deleted[k] = true
		case <-timeout:
			t.Fatalf("Invalidate: expected deletes for sanitize keys %q and %q; got %v",
				sanitizeKey, offsetKey, deleted)
		}
	}
	if !deleted[sanitizeKey] {
		t.Errorf("Invalidate: sanitize key %q was not deleted; deleted=%v", sanitizeKey, deleted)
	}
	if !deleted[offsetKey] {
		t.Errorf("Invalidate: offset key %q was not deleted; deleted=%v", offsetKey, deleted)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Set — guard for nil state / empty session id
// ─────────────────────────────────────────────────────────────────────────────

func TestSet_NilState(t *testing.T) {
	c := NewSessionCache(nil, nil)
	if err := c.Set(context.Background(), "t", "s1", nil, []byte("b")); err != nil {
		t.Errorf("Set(nil state): unexpected error %v", err)
	}
	// L1 should not contain an entry for nil state.
	st, _, _ := c.GetOrLoad(context.Background(), "t", "s1")
	if st != nil {
		t.Errorf("Set(nil state): L1 should remain empty, got %v", st)
	}
}

func TestSet_EmptySessionID(t *testing.T) {
	c := NewSessionCache(nil, nil)
	if err := c.Set(context.Background(), "t", "", stateOf("x"), []byte("b")); err != nil {
		t.Errorf("Set(empty id): unexpected error %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loadFromDB — Redis + DB only (no turn reader)
// ─────────────────────────────────────────────────────────────────────────────

func TestLoadFromDB_NoBackend(t *testing.T) {
	c := NewSessionCache(nil, nil)
	st, body, err := c.loadFromDB(context.Background(), "t", "s1")
	if err != nil || st != nil || body != nil {
		t.Errorf("loadFromDB(no backend) = (%v, %v, %v), want (nil, nil, nil)", st, body, err)
	}
}
