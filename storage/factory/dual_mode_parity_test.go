package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D06 双存储模式 —— 同一 fixture 的 full / lite 等价性门。
//
// 缺口由来（三十五轮清点 D06）：验收标准的主干句是「同一 fixture 在
// full(PG/Redis/files) 与 lite(SQLite/memory/files) 得到同一 API 可见会话、
// 轮次、摘要、用量」，但既有测试**从不产生可比对的共同断言面**——
//
//   - TestFactoryFullModeDispatch 只做 assert.IsType 类型分派断言；
//   - TestLiteFactoryStoresFunctional 只做单模式功能断言；
//
// 类型分派保证「full 拿到的是 pgSessionStore」，**不保证「full 和 lite 对
// 同一个 fixture 给出同一个结果」**。而这正是 D06 基线 R39/R40 反复记录的
// 事故形态：能力不存在而跳过、ensure 局部 skip 进程未传导——都是「类型对、
// 行为不对」。
//
// 门的设计：同一份 fixture 跑两个模式，逐字段比对 API 可见结果。
//   - lite 恒跑（SQLite + t.TempDir，无外部依赖）；
//   - full 需要真 PG，读 TEST_PG_URL（仓库既有约定，见 domains/routeincident、
//     taskprofile、domains/hooks/handoff 的 *_integration_test.go）。
//     无该环境变量时**显式 t.Skip 并说明原因**，绝不静默跳过——静默跳过正是
//     本轮清点反复标记的「零 FAIL 对 integration 门不构成证据」。

// dualModeObservation is everything D06 says must be identical across modes.
// Time fields are compared only for "was populated", never for equality: the
// two stores stamp CreatedAt themselves and clocks/precision legitimately
// differ between SQLite and PostgreSQL.
type dualModeObservation struct {
	SessionID    string
	TenantID     string
	UserID       string
	SessionMeta  map[string]any
	CreatedAtSet bool

	BodyTurnNo  int
	BodyRequest string
	BodyRaw     string

	StateValue string

	TurnCount      int
	TurnCompression string
}

// dualModeFixture is the single source of truth both modes are replayed
// through. Any divergence in what it returns is, by construction, a full/lite
// behavioural difference rather than a per-mode assertion being written twice.
func dualModeFixture(t *testing.T, f *StorageFactory) dualModeObservation {
	t.Helper()
	ctx := context.Background()

	const (
		tenant   = "tenant-1"
		session  = "sess-dual"
		stateKey = "rate:dual:key"
	)

	ss := f.NewSessionStore()
	require.NoError(t, ss.CreateSession(ctx, &storage.Session{
		ID: session, TenantID: tenant, UserID: "user-1",
		Metadata: map[string]any{"scene": "chat", "lang": "zh"},
	}))
	got, err := ss.GetSession(ctx, tenant, session)
	require.NoError(t, err, "session must be readable right after create")
	require.NotNil(t, got)

	obs := dualModeObservation{
		SessionID:    got.ID,
		TenantID:     got.TenantID,
		UserID:       got.UserID,
		SessionMeta:  got.Metadata,
		CreatedAtSet: !got.CreatedAt.IsZero(),
	}

	bs := f.NewBodiesStore()
	require.NoError(t, bs.Write(ctx, &storage.SessionBody{
		TenantID: tenant, SessionID: session, TurnNo: 1,
		Timestamp: time.Now(),
		Request:   json.RawMessage(`{"q":"hi"}`),
		Response:  json.RawMessage(`{"a":"yo"}`),
	}))
	body, err := bs.Read(ctx, tenant, session, 1)
	require.NoError(t, err, "body must be readable right after write")
	require.NotNil(t, body)
	obs.BodyTurnNo = body.TurnNo
	obs.BodyRequest = string(body.Request)
	obs.BodyRaw = string(body.Response)

	st := f.NewStateStore()
	require.NoError(t, st.Set(ctx, stateKey, "v", time.Minute))
	v, err := st.Get(ctx, stateKey)
	require.NoError(t, err)
	obs.StateValue = fmt.Sprintf("%v", v)

	turns := f.NewTurnsStore()
	require.NoError(t, turns.WriteTurnMeta(ctx, &storage.TurnMeta{
		TenantID: tenant, SessionID: session, TurnNo: 1,
		CompressionStrategy: "none", PromptTokens: 11, CompletionTokens: 7,
	}))
	metas, err := turns.GetTurnsMeta(ctx, tenant, session)
	require.NoError(t, err, "turn meta must be readable right after write")
	obs.TurnCount = len(metas)
	for _, m := range metas {
		obs.TurnCompression = m.CompressionStrategy
	}
	return obs
}

// TestDualModeSameFixtureSameAPIResult is the D06 gate proper: replay one
// fixture through lite and full and require the API-visible results to match.
func TestDualModeSameFixtureSameAPIResult(t *testing.T) {
	ctx := context.Background()

	liteFactory, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = liteFactory.Close() })
	liteObs := dualModeFixture(t, liteFactory)

	// The lite half is itself a contract check: if lite drifts, the gate must
	// still say something useful rather than failing only on the full side.
	require.True(t, liteObs.CreatedAtSet, "lite must populate CreatedAt on create")
	assert.Equal(t, `{"q":"hi"}`, liteObs.BodyRequest, "lite must return the request body it stored")

	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set: full-mode half needs a real PostgreSQL. " +
			"This is NOT evidence of full/lite equivalence — see 05-可执行审计检测矩阵 D06. " +
			"Lite half ran and passed.")
	}

	fullCfg := &storage.StorageConfig{
		Mode:           storage.StorageModeFull,
		PostgresURL:    dsn,
		RedisURL:       "redis://127.0.0.1:6379",
		MaxConnections: 2,
		Timeout:        5 * time.Second,
	}
	fullFactory, err := NewStorageFactory(fullCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fullFactory.Close() })
	_ = ctx
	fullObs := dualModeFixture(t, fullFactory)

	assert.Equal(t, liteObs.SessionID, fullObs.SessionID, "session id must be identical across modes")
	assert.Equal(t, liteObs.TenantID, fullObs.TenantID, "tenant id must be identical across modes")
	assert.Equal(t, liteObs.UserID, fullObs.UserID, "user id must be identical across modes")
	assert.Equal(t, liteObs.SessionMeta, fullObs.SessionMeta, "session metadata must round-trip identically")
	assert.Equal(t, liteObs.CreatedAtSet, fullObs.CreatedAtSet, "CreatedAt auto-fill must exist in BOTH modes")

	assert.Equal(t, liteObs.BodyTurnNo, fullObs.BodyTurnNo, "body turn number must be identical")
	assert.Equal(t, liteObs.BodyRequest, fullObs.BodyRequest, "stored request body must be byte-identical")
	assert.Equal(t, liteObs.BodyRaw, fullObs.BodyRaw, "stored response body must be byte-identical")

	assert.Equal(t, liteObs.StateValue, fullObs.StateValue, "state store value must be identical")

	assert.Equal(t, liteObs.TurnCount, fullObs.TurnCount, "turn count must be identical across modes")
	assert.Equal(t, liteObs.TurnCompression, fullObs.TurnCompression, "turn compression strategy must be identical across modes")
}

// TestLiteModeSessionIsolationIsReal guards the property the dual-mode gate
// depends on: if lite's tenant scoping were broken, "both modes return the same
// thing" could be true for the wrong reason (both returning the wrong row).
//
// NOTE on session ids: both schemas key sessions by `id TEXT PRIMARY KEY`
// (storage/sqlite/schema.go:23 and sql/init-complete-minimal.sql:110), i.e. a
// session id is globally unique BY DESIGN in both modes. This test therefore
// uses a distinct id per tenant and asserts cross-tenant reads do not leak —
// which is the isolation property that actually exists.
func TestLiteModeSessionIsolationIsReal(t *testing.T) {
	ctx := context.Background()
	f, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	ss := f.NewSessionStore()
	require.NoError(t, ss.CreateSession(ctx, &storage.Session{ID: "s-a", TenantID: "tenant-a", UserID: "ua"}))
	require.NoError(t, ss.CreateSession(ctx, &storage.Session{ID: "s-b", TenantID: "tenant-b", UserID: "ub"}))

	a, err := ss.GetSession(ctx, "tenant-a", "s-a")
	require.NoError(t, err)
	require.NotNil(t, a)
	assert.Equal(t, "ua", a.UserID)

	b, err := ss.GetSession(ctx, "tenant-b", "s-b")
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "ub", b.UserID)

	// Cross-tenant read must not return the other tenant's row.
	leak, err := ss.GetSession(ctx, "tenant-a", "s-b")
	if err == nil {
		require.Nil(t, leak, "tenant-a must not read tenant-b's session")
	}

	// And tenant listing must be scoped.
	list, err := ss.ListSessions(ctx, "tenant-a", nil)
	require.NoError(t, err)
	for _, s := range list {
		assert.Equal(t, "tenant-a", s.TenantID, "ListSessions must not cross tenant boundaries")
	}
}

// TestLiteSessionsIDIsGloballyUnique pins the cross-mode schema contract: a
// session id is the PRIMARY KEY in BOTH modes, so the same id under two
// tenants is rejected identically. If this ever diverges, one mode silently
// accepts a write the other rejects — the exact full/lite divergence D06 exists
// to catch. The reverse direction is also asserted: the same id re-created
// under the SAME tenant is rejected.
func TestLiteSessionsIDIsGloballyUnique(t *testing.T) {
	ctx := context.Background()
	f, err := NewStorageFactory(liteModeConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	ss := f.NewSessionStore()
	require.NoError(t, ss.CreateSession(ctx, &storage.Session{ID: "s-dup", TenantID: "tenant-a", UserID: "u1"}))

	err = ss.CreateSession(ctx, &storage.Session{ID: "s-dup", TenantID: "tenant-b", UserID: "u2"})
	require.Error(t, err, "sessions.id is the PRIMARY KEY in both modes; cross-tenant reuse must be rejected")

	err = ss.CreateSession(ctx, &storage.Session{ID: "s-dup", TenantID: "tenant-a", UserID: "u3"})
	require.Error(t, err, "same-tenant duplicate must also be rejected")
}

var _ = filepath.Join // keep filepath import used across build tags
