//go:build integration

package compression

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type auditRedisBackend struct{ client *redis.Client }

func (r auditRedisBackend) HSet(ctx context.Context, key string, args ...any) error {
	return r.client.HSet(ctx, key, args...).Err()
}
func (r auditRedisBackend) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return r.client.HGetAll(ctx, key).Result()
}
func (r auditRedisBackend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.client.Expire(ctx, key, ttl).Err()
}
func (r auditRedisBackend) Del(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

func (r auditRedisBackend) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return r.client.Eval(ctx, script, keys, args...).Result()
}

type observingAuditRedisBackend struct {
	auditRedisBackend
	contended chan struct{}
	once      sync.Once
}

func (r *observingAuditRedisBackend) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	result, err := r.auditRedisBackend.Eval(ctx, script, keys, args...)
	if err == nil && result == int64(0) && strings.Contains(script, "session-cache-acquire") {
		r.once.Do(func() { close(r.contended) })
	}
	return result, err
}

func auditStorage(t *testing.T) (*redis.Client, *pgxpool.Pool) {
	t.Helper()
	addr, dsn := os.Getenv("SESSION_AUDIT_REDIS_ADDR"), os.Getenv("TEST_PG_URL")
	if addr == "" || dsn == "" {
		t.Skip("set SESSION_AUDIT_REDIS_ADDR and TEST_PG_URL to disposable loopback services")
	}
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.True(t, ip != nil && ip.IsLoopback())
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	ip = net.ParseIP(u.Hostname())
	require.True(t, ip != nil && ip.IsLoopback())
	r := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = r.Close() })
	require.NoError(t, r.Ping(context.Background()).Err())
	db, err := pgxpool.New(context.Background(), testdb.Create(t, dsn))
	require.NoError(t, err)
	t.Cleanup(db.Close)
	// Minimal query-shaped fixture, not a full migration/production schema gate.
	_, err = db.Exec(context.Background(), `CREATE TABLE public.session_bodies_unified (
	 tenant_id text, session_id text, turn_no int, ts timestamptz, kind text,
	 outbound_body jsonb, request_delta jsonb, response_delta jsonb)`)
	require.NoError(t, err)
	return r, db
}

func TestStorageAuditRealColdRead(t *testing.T) {
	r, db := auditStorage(t)
	ctx := context.Background()
	tenant, session := "audit-cold", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { r.Del(ctx, redisKey(tenant, session)) })
	_, err := db.Exec(ctx, `INSERT INTO session_bodies_unified VALUES ($1,$2,1,now(),'final_full',$3,NULL,NULL)`, tenant, session, `[{"role":"user","content":"{SENSITIVE:phone:1}"}]`)
	require.NoError(t, err)
	writer := NewSessionCache(auditRedisBackend{r}, nil)
	old := &SessionState{SchemaVersion: schemaVersion, HasCutMarker: true, CutIndex: 2, SanitizeMapGeneration: "old", SensitiveDetected: true, SanitizeStats: SanitizeStats{PhoneCount: 3}, ToolsHash: "tools", SystemPrompt: "old system", FullSessionHash: "full", LastStripAt: 1, StripsApplied: 2, CompletedTasks: 3, CompressionMode: "smart", MessagesAfterStrip: 4}
	require.NoError(t, writer.Set(ctx, tenant, session, old, nil))
	fresh := &SessionState{SchemaVersion: schemaVersion, MsgCount: 1, SanitizeMapGeneration: "new"}
	freshBody := []byte(`{"messages":[{"role":"user","content":"{SENSITIVE:phone:1}"}],"exact_large_integer":9007199254740993}`)
	fresh.LastOutboundHash = sha256Hex(freshBody)
	require.NoError(t, writer.Set(ctx, tenant, session, fresh, freshBody))
	reader := NewSessionCache(auditRedisBackend{r}, nil)
	reader.SetTurnReader(v2.NewTurnReader(db))
	state, body, err := reader.GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "new", state.SanitizeMapGeneration)
	require.False(t, state.HasCutMarker)
	require.Zero(t, state.CutIndex)
	require.False(t, state.SensitiveDetected)
	require.Zero(t, state.SanitizeStats.PhoneCount)
	require.Equal(t, string(freshBody), string(body))
	require.Equal(t, sha256Hex(body), state.LastOutboundHash)
	require.Empty(t, state.ToolsHash)
	require.Empty(t, state.SystemPrompt)
	require.Empty(t, state.FullSessionHash)
	require.Zero(t, state.LastStripAt)
	require.Zero(t, state.StripsApplied)
	require.Zero(t, state.CompletedTasks)
	require.Empty(t, state.CompressionMode)
	require.Zero(t, state.MessagesAfterStrip)
	ttl, err := r.TTL(ctx, redisKey(tenant, session)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	t.Logf("real cold Redis + PG TurnReader: fields reset; ttl=%s", ttl)
}

// Both warm and cold readers must observe the exact acknowledged pair. The
// callback ordering is bounded: only A waits, while B must wait for A's lease.
func TestStorageAuditObserveCrossInstanceState(t *testing.T) {
	r, _ := auditStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tenant, session := "audit-coherence", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { r.Del(context.Background(), redisKey(tenant, session), redisKey(tenant, session)+":lease") })
	observedB := &observingAuditRedisBackend{auditRedisBackend: auditRedisBackend{r}, contended: make(chan struct{})}
	a, b := NewSessionCache(auditRedisBackend{r}, nil), NewSessionCache(observedB, nil)
	oldBody := []byte(`{"model":"m","messages":[{"role":"user","content":"old"}],"precision":9007199254740993}`)
	newBody := []byte(`{"model":"m","messages":[{"role":"user","content":"new"}],"precision":9007199254740993}`)
	require.NoError(t, a.Set(ctx, tenant, session, sharedState(oldBody, "old"), oldBody))
	_, warmBody, err := b.GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.Equal(t, string(oldBody), string(warmBody))
	require.NoError(t, a.Set(ctx, tenant, session, sharedState(newBody, "new"), newBody))
	for _, reader := range []*SessionCache{b, NewSessionCache(auditRedisBackend{r}, nil)} {
		state, body, err := reader.GetOrLoad(ctx, tenant, session)
		require.NoError(t, err)
		require.NotNil(t, state)
		require.Equal(t, "new", state.SanitizeMapGeneration)
		require.Equal(t, string(newBody), string(body))
		require.Equal(t, sha256Hex(body), state.LastOutboundHash)
	}
	enteredA, releaseA, enteredB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	var callsA, callsB atomic.Int32
	go func() {
		done <- a.Update(ctx, tenant, session, func(s *SessionState, body []byte) (*SessionState, []byte, error) {
			callsA.Add(1)
			close(enteredA)
			select {
			case <-releaseA:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			s.StripsApplied++
			return s, body, nil
		})
	}()
	select {
	case <-enteredA:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		done <- b.Update(ctx, tenant, session, func(s *SessionState, body []byte) (*SessionState, []byte, error) {
			callsB.Add(1)
			close(enteredB)
			s.StripsApplied++
			return s, body, nil
		})
	}()
	select {
	case <-observedB.contended:
	case <-ctx.Done():
		close(releaseA)
		t.Fatal("B never reached shared lease contention")
	}
	early := false
	select {
	case <-enteredB:
		early = true
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
	}
	close(releaseA)
	for range 2 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.False(t, early, "second callback entered before first commit")
	require.EqualValues(t, 1, callsA.Load())
	require.EqualValues(t, 1, callsB.Load())
	final, body, err := NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.Equal(t, 2, final.StripsApplied)
	require.Equal(t, string(newBody), string(body))
	require.Equal(t, sha256Hex(body), final.LastOutboundHash)
	t.Logf("real Redis cross-instance full pair; callback counts=%d/%d persisted_updates=%d", callsA.Load(), callsB.Load(), final.StripsApplied)
}

func TestStorageAuditObserveV2MetadataBodyMismatch(t *testing.T) {
	r, db := auditStorage(t)
	ctx := context.Background()
	tenant, session := "audit-body", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { r.Del(ctx, redisKey(tenant, session)) })
	oldBody := []byte(`{"messages":[{"role":"user","content":"old"}]}`)
	writer := NewSessionCache(auditRedisBackend{r}, nil)
	require.NoError(t, writer.Set(ctx, tenant, session, &SessionState{SchemaVersion: schemaVersion, LastOutboundHash: sha256Hex(oldBody), MsgCount: 1, SummaryMarker: "old-marker"}, oldBody))
	_, err := db.Exec(ctx, `INSERT INTO session_bodies_unified VALUES ($1,$2,2,now(),'final_full',$3,NULL,NULL)`, tenant, session, `[{"role":"user","content":"new"},{"role":"assistant","content":"different-turn"}]`)
	require.NoError(t, err)
	reader := NewSessionCache(auditRedisBackend{r}, nil)
	reader.SetTurnReader(v2.NewTurnReader(db))
	state, body, err := reader.GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	coherent := state.LastOutboundHash == sha256Hex(body) && state.MsgCount == countMessages(body)
	t.Logf("OBSERVATION redis_msg_count=%d pg_msg_count=%d old_hash_with_new_body=%v coherent=%v", state.MsgCount, countMessages(body), !bytes.Equal(body, oldBody) && state.LastOutboundHash == sha256Hex(oldBody), coherent)
	if os.Getenv("SESSION_AUDIT_REQUIRE_COHERENCE") == "1" {
		require.True(t, coherent, "must not bind unrelated V2 body to Redis metadata")
	}
}
