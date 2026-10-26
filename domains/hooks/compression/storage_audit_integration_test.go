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
	require.True(t, coherent, "must not bind unrelated V2 body to Redis metadata")
	require.Equal(t, string(oldBody), string(body), "acknowledged Redis body must win over newer/older PG until a new shared commit")
	require.Equal(t, "old-marker", state.SummaryMarker)
}

func TestStorageAuditTTLFailureLeavesPriorPairAndTTL(t *testing.T) {
	for _, denied := range []string{"pexpire", "hset", "del"} {
		t.Run(denied, func(t *testing.T) {
			r, _ := auditStorage(t)
			ctx := context.Background()
			tenant, id := "audit-ttl", fmt.Sprintf("audit-%d", time.Now().UnixNano())
			user := fmt.Sprintf("audit-cache-%d", time.Now().UnixNano())
			require.NoError(t, r.Do(ctx, "ACL", "SETUSER", user, "on", ">"+user, "~*", "+@all", "-"+denied).Err())
			t.Cleanup(func() {
				r.Do(ctx, "ACL", "DELUSER", user)
				r.Del(ctx, redisKey(tenant, id), redisKey(tenant, id)+":lease")
			})
			oldBody, newBody := []byte("old-body"), []byte("new-body")
			require.NoError(t, NewSessionCache(auditRedisBackend{r}, nil).Set(ctx, tenant, id, sharedState(oldBody, "old"), oldBody))
			before, err := r.HGetAll(ctx, redisKey(tenant, id)).Result()
			require.NoError(t, err)
			expiresBefore, err := r.Do(ctx, "PEXPIRETIME", redisKey(tenant, id)).Int64()
			require.NoError(t, err)
			require.Positive(t, expiresBefore)
			limited := redis.NewClient(&redis.Options{Addr: r.Options().Addr, Username: user, Password: user, MaxRetries: -1})
			t.Cleanup(func() { _ = limited.Close() })
			require.NoError(t, limited.Ping(ctx).Err())
			who, err := limited.Do(ctx, "ACL", "WHOAMI").Text()
			require.NoError(t, err)
			require.Equal(t, user, who, "limited ACL identity must actually be in use")
			args := []any{"EVAL", `return redis.acl_check_cmd(ARGV[1],KEYS[1],unpack(ARGV,2)) and 1 or 0`, 1, "audit:acl-denial-premise", denied}
			if denied == "pexpire" {
				args = append(args, "1000")
			} else if denied == "hset" {
				args = append(args, "field", "value")
			}
			allowed, err := limited.Do(ctx, args...).Int64()
			require.NoError(t, err)
			require.Zero(t, allowed, "denied command premise must be present")
			err = NewSessionCache(auditRedisBackend{limited}, nil).Set(ctx, tenant, id, sharedState(newBody, "new"), newBody)
			require.Error(t, err, "denied mutation must not acknowledge shared commit")
			after, err := r.HGetAll(ctx, redisKey(tenant, id)).Result()
			require.NoError(t, err)
			require.Equal(t, before, after, "rejected commit must leave all prior payload/revision fields unchanged")
			expiresAfter, err := r.Do(ctx, "PEXPIRETIME", redisKey(tenant, id)).Int64()
			require.NoError(t, err)
			require.Equal(t, expiresBefore, expiresAfter, "rejected commit must not remove or refresh prior TTL")
			st, body, err := NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, id)
			require.NoError(t, err)
			require.NotNil(t, st)
			require.Equal(t, "old", st.SanitizeMapGeneration)
			require.Equal(t, string(oldBody), string(body))
		})
	}
}

func TestStorageAuditInvalidateRetiresLegacyAndPreservesPairOnDenial(t *testing.T) {
	r, _ := auditStorage(t)
	ctx := context.Background()
	tenant, id := "audit-invalidate", fmt.Sprintf("audit-%d", time.Now().UnixNano())
	legacy, key := legacyRedisKey(tenant, id), redisKey(tenant, id)
	lockKey := key + ":lease"
	user := fmt.Sprintf("audit-invalidate-%d", time.Now().UnixNano())
	t.Cleanup(func() { r.Do(ctx, "ACL", "DELUSER", user); r.Del(ctx, legacy, key, lockKey) })
	body := []byte(`{"messages":[{"role":"user","content":"confirmed"}]}`)
	old := sharedState(body, "")
	old.ApprovalStatus = ApprovalStateApproved
	require.NoError(t, r.HSet(ctx, legacy, encodeSessionStateFields(old)...).Err())
	require.NoError(t, r.PExpire(ctx, legacy, time.Minute).Err())
	cache := NewSessionCache(auditRedisBackend{r}, nil)
	fresh := sharedState(body, "")
	fresh.ApprovalStatus = ApprovalStateRejected
	require.NoError(t, cache.Set(ctx, tenant, id, fresh, body))
	before := make([]map[string]string, 2)
	expires := make([]int64, 2)
	for i, k := range []string{legacy, key} {
		var err error
		before[i], err = r.HGetAll(ctx, k).Result()
		require.NoError(t, err)
		expires[i], err = r.Do(ctx, "PEXPIRETIME", k).Int64()
		require.NoError(t, err)
		require.Positive(t, expires[i])
	}
	// Exercise the actual invalidation script under a denied DEL identity;
	// lease acquisition has its own DEL permission check, so seed the token
	// separately to reach the invalidation preflight itself.
	token := "confirmed-owner"
	require.NoError(t, r.Set(ctx, lockKey, token, time.Minute).Err())
	require.NoError(t, r.Do(ctx, "ACL", "SETUSER", user, "on", ">"+user, "~*", "+@all", "-del").Err())
	limited := redis.NewClient(&redis.Options{Addr: r.Options().Addr, Username: user, Password: user, MaxRetries: -1})
	t.Cleanup(func() { _ = limited.Close() })
	allowed, err := limited.Eval(ctx, `return redis.acl_check_cmd('DEL',KEYS[1],KEYS[2]) and 1 or 0`, []string{legacy, key}).Int64()
	require.NoError(t, err)
	require.Zero(t, allowed, "denied DEL premise must actually be present")
	err = NewSessionCache(auditRedisBackend{limited}, nil).invalidateShared(ctx, tenant, id, token)
	require.Error(t, err)
	for i, k := range []string{legacy, key} {
		after, err := r.HGetAll(ctx, k).Result()
		require.NoError(t, err)
		require.Equal(t, before[i], after)
		expiry, err := r.Do(ctx, "PEXPIRETIME", k).Int64()
		require.NoError(t, err)
		require.Equal(t, expires[i], expiry, "denied invalidation must preserve both TTLs")
	}
	got, out, err := NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, id)
	require.NoError(t, err)
	require.Equal(t, ApprovalStateRejected, got.ApprovalStatus)
	require.Equal(t, string(body), string(out))
	require.NoError(t, r.Del(ctx, lockKey).Err())
	cache.Invalidate(ctx, tenant, id)
	exists, err := r.Exists(ctx, legacy, key).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
	got, out, err = NewSessionCache(auditRedisBackend{r}, nil).GetOrLoad(ctx, tenant, id)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Empty(t, out)
}
