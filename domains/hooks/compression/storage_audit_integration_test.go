//go:build integration

package compression

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
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
	old := &SessionState{SchemaVersion: schemaVersion, HasCutMarker: true, CutIndex: 2, SanitizeMapGeneration: "old", SensitiveDetected: true, SanitizeStats: SanitizeStats{PhoneCount: 3}}
	require.NoError(t, writer.Set(ctx, tenant, session, old, nil))
	fresh := &SessionState{SchemaVersion: schemaVersion, MsgCount: 1, SanitizeMapGeneration: "new"}
	require.NoError(t, writer.Set(ctx, tenant, session, fresh, nil))
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
	require.Contains(t, string(body), "{SENSITIVE:phone:1}")
	ttl, err := r.TTL(ctx, redisKey(tenant, session)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	t.Logf("real cold Redis + PG TurnReader: fields reset; ttl=%s", ttl)
}

// Observational probes report coherence gaps; their PASS is successful
// measurement, not evidence that distributed cache consistency is satisfied.
func TestStorageAuditObserveCrossInstanceState(t *testing.T) {
	r, _ := auditStorage(t)
	ctx := context.Background()
	tenant, session := "audit-coherence", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	t.Cleanup(func() { r.Del(ctx, redisKey(tenant, session)) })
	a, b := NewSessionCache(auditRedisBackend{r}, nil), NewSessionCache(auditRedisBackend{r}, nil)
	require.NoError(t, a.Set(ctx, tenant, session, &SessionState{SchemaVersion: schemaVersion, SanitizeMapGeneration: "old"}, []byte("old-body")))
	_, _, err := b.GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.NoError(t, a.Set(ctx, tenant, session, &SessionState{SchemaVersion: schemaVersion, SanitizeMapGeneration: "new"}, []byte("new-body")))
	stale, _, err := b.GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	cold, _, err := NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	require.Equal(t, "new", cold.SanitizeMapGeneration)
	t.Logf("OBSERVATION warm_generation=%s cold_generation=%s coherent=%v", stale.SanitizeMapGeneration, cold.SanitizeMapGeneration, stale.SanitizeMapGeneration == cold.SanitizeMapGeneration)
	// Force two read-modify-write callbacks to load the same initial value.
	loaded, resume := make(chan struct{}, 2), make(chan struct{})
	var wg sync.WaitGroup
	for _, cache := range []*SessionCache{a, b} {
		wg.Add(1)
		go func(c *SessionCache) {
			defer wg.Done()
			err := c.Update(ctx, tenant, session, func(s *SessionState, body []byte) (*SessionState, []byte, error) {
				loaded <- struct{}{}
				<-resume
				s.StripsApplied++
				return s, body, nil
			})
			if err != nil {
				t.Error(err)
			}
		}(cache)
	}
	<-loaded
	<-loaded
	close(resume)
	wg.Wait()
	final, _, err := NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, session)
	require.NoError(t, err)
	t.Logf("OBSERVATION cross_instance_updates=2 persisted_count=%d coherent=%v", final.StripsApplied, final.StripsApplied == 2)
	if os.Getenv("SESSION_AUDIT_REQUIRE_COHERENCE") == "1" {
		require.Equal(t, "new", stale.SanitizeMapGeneration)
		require.Equal(t, 2, final.StripsApplied)
	}
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
