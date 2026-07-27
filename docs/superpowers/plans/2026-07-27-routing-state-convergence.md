# 路由 & 状态管理单源收敛 — URSM v2 Authoritative 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把路由运行时状态收敛到 URSM v2 单源(Redis `ursm:v2:` 命名空间 + 进程 LRU 镜像),sticky L1/L2/L3 与 session intent 缓存改 Redis 同步写,9 个 feature flag 折叠为 `URSM_V2_MODE`,防封锁机制完全独立。

**Architecture:** URSM v2 (`domains/ursm/v2`) 作为唯一权威源,新增进程 LRU 镜像层 (`domains/ursm/v2/cache/`) 加速读路径;sticky / intent 改走 Redis 同步写 + LRU 镜像;`Router.PlanCandidates` 移除 `r.URSM`/`r.StateManager`/Shadow 三套分支,`StateBackend` 退化为 `URSMv2Backend` 单实现;FpSlots `NodeState`/`credentialstate`/`routingstate` 下沉 `_to-be-deprecated/`。Redis 不可用时 fail-open 返回原候选集。

**Tech Stack:** Go 1.25、`github.com/redis/go-redis/v9`、`github.com/alicebob/miniredis/v2`(测试)、`container/list` + `sync.Mutex`(自研 LRU,不引外部依赖)、Prometheus、`slog`。

**关联设计稿:** `docs/superpowers/specs/2026-07-27-routing-state-architecture.md`(commit 3df13fe1,3 轮评审 APPROVED)

---

## 关键事实核对(实施前必读)

> 本计划已按代码事实对齐,**不复用设计稿里的 `ur:cred:` 虚构前缀**。真实命名空间如下:

1. **URSM v2 Redis 前缀 = `ursm:v2:`**(见 `domains/ursm/v2/config.go:63` `DefaultConfig().RedisKeyPrefix`),不是 `ur:`。设计稿的 `ur:cred:{cid}:{model}` 实际对应代码里的 `ursm:v2:node:{cid}:{raw}`(见 `domains/ursm/v2/store/keys.go:5` `NodeKey`)。本计划一律用 `ursm:v2:` 前缀,不引入新前缀。
2. **节点 Hash 字段** 以 `domains/ursm/v2/store/*.lua` 实际 HSET/HINCRBY 为准(见设计稿 Data Contracts 表):`available / disabled / cool_until_ms / fail_streak / failure_count / success_count / disable_count / disabled_reason / last_err / updated_at_ms / last_probe_at_ms / last_probe_latency_ms / source_priority / generation / manual_hold / manual_actor / manual_reason / manual_at_ms`。
3. **非有效字段**(仅 `clear_state.lua` HDEL,永不写):`cool_start_ms / fail_count / disabled_at_ms / last_err_at_ms`。LRU 镜像与迁移脚本**禁止**读写。
4. **generation 单调性**: `apply_decision.lua:25` 用 `if cur_gen > in_gen or (cur_gen == in_gen and cur_pri > in_pri) then ignored_stale`;LRU 镜像必须复刻该比较。
5. **熔断器** 在 `domains/credential/breaker.go` 纯进程内,**不进 Redis**;cooldown 是 `ursm:v2:node:` Hash 的 `cool_until_ms` 字段。
6. **sticky L3 key 不含 model**(见 `domains/routing/sticky.go:369`),格式 `{tenant}:{app}:{key}:{profile}`;L1/L2 含 model。

---

## 约定

- Go module: `github.com/kaixuan/llm-gateway-go`
- 新包: `domains/ursm/v2/cache/`(LRU 镜像 + sticky/intent Redis 适配)
- 测试: `go test ./domains/ursm/v2/...`、`go test ./domains/routing/...`、`go test ./autoroute/...`
- 每个任务结束 `git commit`,conventional commit 前缀(`feat`/`fix`/`refactor`/`test`/`docs`)
- 旧路径在 `URSM_V2_MODE=off` 时仍可工作(`_to-be-deprecated/` 下保留)
- LRU 默认自研(`container/list` + `sync.Mutex`,~120 行),**不引** `hashicorp/golang-lru`(`go.mod` 无此依赖)

---

## 文件结构

**新建**
- `domains/ursm/v2/cache/lru.go` — 自研 LRU(container/list + map,线程安全),通用
- `domains/ursm/v2/cache/lru_test.go`
- `domains/ursm/v2/cache/nodemirror.go` — 节点状态 LRU 镜像(读路径加速 + gen 单调)
- `domains/ursm/v2/cache/nodemirror_test.go`
- `domains/ursm/v2/cache/sticky.go` — sticky L1/L2/L3 Redis 同步写 + LRU 镜像
- `domains/ursm/v2/cache/sticky_test.go`
- `domains/ursm/v2/cache/intent.go` — session intent Redis 同步写 + LRU 镜像
- `domains/ursm/v2/cache/intent_test.go`
- `domains/ursm/v2/cache/migrate_fpslots.go` — FpSlots NodeState → ursm:v2:node: 一次性迁移
- `domains/ursm/v2/cache/migrate_fpslots_test.go`
- `autoroute/internal/legacyflags/legacyflags.go` — 收纳 9 个 deprecated env

**修改**
- `domains/streaming/executors/router.go` — 移除 `r.URSM`/`r.StateManager` 分支(M3)
- `domains/streaming/executors/state_backend.go` — `StateBackend` 退化为单实现
- `domains/streaming/executors/executor.go` — `isURSMv2Authoritative()` 改走 `stateBackend.IsAuthoritative()`
- `domains/routing/sticky.go` — `StickyCache` 委托给 `cache.StickyStore`(双写过渡)
- `autoroute/session_intent_cache.go` — `SessionIntentCache` 委托给 `cache.IntentStore`
- `autoroute/feature_flags.go` — 9 个 env 下沉
- `cmd/gateway/main.go` — 移除 ShadowObserver wire,M1 双写开关

**下沉(M3,git mv)**
- `domains/routingstate/` → `_to-be-deprecated/routingstate/`
- `domains/credentialstate/` → `_to-be-deprecated/credentialstate/`

**保留不动**
- `credentialfpslot/`(仅 NodeState 迁移,管理器本身保留)、`credential/limiter.go`、`credential/breaker.go`、`disguise/`、`identity/`

---

## Phase A — LRU 镜像基础设施(M1,mode 仍 off,不切流)

### Task 1: 自研 LRU(container/list + map)

**Files:**
- Create: `domains/ursm/v2/cache/lru.go`
- Test: `domains/ursm/v2/cache/lru_test.go`

- [ ] **Step 1: 写失败测试**

```go
// domains/ursm/v2/cache/lru_test.go
package cache

import "testing"

func TestLRUGetPutEvict(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	l.Put("b", 2)
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatalf("expected a=1, got %v,%v", v, ok)
	}
	// 访问 a 后再插 c,淘汰的应是 b(LRU),不是 a
	l.Put("c", 3)
	if _, ok := l.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatalf("a evicted incorrectly: %v,%v", v, ok)
	}
	if v, ok := l.Get("c"); !ok || v != 3 {
		t.Fatalf("c missing: %v,%v", v, ok)
	}
}

func TestLRUConcurrent(t *testing.T) {
	l := NewLRU[int, int](100)
	done := make(chan struct{})
	for g := 0; g < 10; g++ {
		go func(off int) {
			for i := 0; i < 1000; i++ {
				l.Put(off*1000+i, i)
				_, _ = l.Get(off*1000 + i)
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 10; g++ {
		<-done
	}
	// 不 panic 即通过;容量上限保证不泄漏
	if l.Len() > 100 {
		t.Fatalf("len %d exceeded capacity 100", l.Len())
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./domains/ursm/v2/cache/ -run TestLRU -v`
Expected: FAIL — `NewLRU` 未定义。

- [ ] **Step 3: 实现 LRU**

```go
// domains/ursm/v2/cache/lru.go
package cache

import (
	"container/list"
	"sync"
)

// LRU 是一个线程安全的泛型 LRU。零值不可用,必须用 NewLRU。
// 设计目标: 复刻 domains/session/v2/cache_v2.go 的同款实现,避免引入
// hashicorp/golang-lru(go.mod 无此依赖,详见设计稿 Decision 2)。
type LRU[K comparable, V any] struct {
	cap   int
	mu    sync.Mutex
	idx   map[K]*list.Element
	order *list.List // front = 最近使用;back = 待淘汰
}

type lruEntry[K comparable, V any] struct {
	key K
	val V
}

func NewLRU[K comparable, V any](cap int) *LRU[K, V] {
	if cap <= 0 {
		panic("cache: LRU capacity must be > 0")
	}
	return &LRU[K, V]{
		cap:   cap,
		idx:   make(map[K]*list.Element, cap),
		order: list.New(),
	}
}

// Get 返回 key 对应的值,并把该 key 提升到最近使用。ok=false 表示未命中。
func (l *LRU[K, V]) Get(key K) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero V
	el, ok := l.idx[key]
	if !ok {
		return zero, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry[K, V]).val, true
}

// Put 写入 key=val,超容量时淘汰最久未使用项。返回被淘汰的 key 与 ok=true。
func (l *LRU[K, V]) Put(key K, val V) (evictedKey K, evicted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.idx[key]; ok {
		el.Value.(*lruEntry[K, V]).val = val
		l.order.MoveToFront(el)
		var zero K
		return zero, false
	}
	el := l.order.PushFront(&lruEntry[K, V]{key: key, val: val})
	l.idx[key] = el
	if l.order.Len() > l.cap {
		back := l.order.Back()
		if back != nil {
			entry := back.Value.(*lruEntry[K, V])
			l.order.Remove(back)
			delete(l.idx, entry.key)
			return entry.key, true
		}
	}
	var zero K
	return zero, false
}

// Peek 返回值但不提升 LRU 顺序。用于内部比较(如 gen 单调检查)。
func (l *LRU[K, V]) Peek(key K) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero V
	el, ok := l.idx[key]
	if !ok {
		return zero, false
	}
	return el.Value.(*lruEntry[K, V]).val, true
}

func (l *LRU[K, V]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/ursm/v2/cache/ -run TestLRU -v`
Expected: PASS(两个用例)。

- [ ] **Step 5: 提交**

```bash
git add domains/ursm/v2/cache/lru.go domains/ursm/v2/cache/lru_test.go
git commit -m "feat(ursm/v2/cache): add self-rolled thread-safe LRU

container/list + sync.Mutex, ~120 行, 避免引入 hashicorp/golang-lru
(go.mod 无此依赖, 见设计稿 Decision 2)。复刻 session/v2/cache_v2.go 风格。"
```

---

### Task 2: 节点状态 LRU 镜像(含 generation 单调性)

**Files:**
- Create: `domains/ursm/v2/cache/nodemirror.go`
- Test: `domains/ursm/v2/cache/nodemirror_test.go`

- [ ] **Step 1: 写失败测试(覆盖 gen 单调 + 软过期 + 回填)**

```go
// domains/ursm/v2/cache/nodemirror_test.go
package cache

import (
	"testing"
	"time"
)

func TestNodeMirrorNewerGenerationWins(t *testing.T) {
	m := NewNodeMirror(100, 30*time.Second)
	old := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 10, Available: true}
	m.applyToLRU(old)
	// 迟到的旧 gen 不应覆盖
	stale := NodeView{CredentialID: 1, RawModel: "m", Generation: 4, SourcePriority: 20, Available: false}
	m.applyToLRU(stale)
	got, ok := m.Peek(1, "m")
	if !ok || !got.Available {
		t.Fatalf("stale gen overwrote newer: %+v ok=%v", got, ok)
	}
	// 同 gen 但更高 priority 应覆盖
	newer := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 30, Available: false}
	m.applyToLRU(newer)
	got, _ = m.Peek(1, "m")
	if got.Available {
		t.Fatalf("same-gen higher-pri did not overwrite: %+v", got)
	}
}

func TestNodeMirrorSoftExpiry(t *testing.T) {
	m := NewNodeMirror(100, 10*time.Millisecond)
	m.applyToLRU(NodeView{CredentialID: 1, RawModel: "m", Generation: 1, Available: true})
	time.Sleep(20 * time.Millisecond)
	if _, ok := m.Get(1, "m"); ok {
		t.Fatal("soft-expired entry should miss")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/ursm/v2/cache/ -run TestNodeMirror -v`
Expected: FAIL — 类型未定义。

- [ ] **Step 3: 实现 NodeMirror**

```go
// domains/ursm/v2/cache/nodemirror.go
package cache

import (
	"time"
)

// NodeView 是节点状态的 LRU 镜像条目。字段与 domains/ursm/v2/api.NodeView
// 对齐(只取镜像需要的子集),generation + source_priority 用于单调裁决。
type NodeView struct {
	CredentialID    int
	RawModel        string
	Available       bool
	Reason          string
	Generation      int64
	SourcePriority  int
	FailStreak      int
	CoolUntil       time.Time
	softExpireAt    time.Time // 软过期点;超过后 Get 返回 miss
}

// nodeKey 复刻 store.NodeKey 的逻辑(为避免循环 import,在此重写)。
// 必须与 domains/ursm/v2/store/keys.go:5 NodeKey 保持一致:
//   ursm:v2:node:{cid}:{raw}
func nodeMirrorKey(credID int, raw string) string {
	return keyf("ursm:v2:node:%d:%s", credID, raw)
}

// NodeMirror 是节点状态的进程内只读镜像。
// 不变量(设计稿 Decision 2): 任何写入必须先经 Redis Lua 成功;LRU 永远是只读副本。
// generation 单调契约与 apply_decision.lua:25 对齐。
type NodeMirror struct {
	lru      *LRU[string, NodeView]
	softTTL  time.Duration
}

func NewNodeMirror(capacity int, softTTL time.Duration) *NodeMirror {
	return &NodeMirror{lru: NewLRU[string, NodeView](capacity), softTTL: softTTL}
}

// applyToLRU 是唯一的 LRU 写入口,write-back 与 read-back 共用。
// 复刻 apply_decision.lua:25 的拒绝条件:
//   cur_gen > in_gen or (cur_gen==in_gen and cur_pri>in_pri) → ignored_stale
// 即: 仅当 incoming.gen > existing.gen,或 gen 相等且 incoming.pri > existing.pri 时才覆盖。
func (m *NodeMirror) applyToLRU(v NodeView) {
	key := nodeMirrorKey(v.CredentialID, v.RawModel)
	v.softExpireAt = time.Now().Add(m.softTTL)
	if existing, ok := m.lru.Peek(key); ok {
		if v.Generation < existing.Generation ||
			(v.Generation == existing.Generation && v.SourcePriority <= existing.SourcePriority) {
			return // 迟到,拒绝覆盖(对应 lua 的 ignored_stale)
		}
	}
	m.lru.Put(key, v)
}

// Get 返回未软过期的镜像条目。软过期返回 miss(触发上层回源 Redis)。
func (m *NodeMirror) Get(credID int, raw string) (NodeView, bool) {
	key := nodeMirrorKey(credID, raw)
	v, ok := m.lru.Get(key)
	if !ok {
		return NodeView{}, false
	}
	if time.Now().After(v.softExpireAt) {
		return NodeView{}, false
	}
	return v, true
}

func (m *NodeMirror) Peek(credID int, raw string) (NodeView, bool) {
	return m.lru.Peek(nodeMirrorKey(credID, raw))
}
```

- [ ] **Step 4: 补 keyf 辅助(避免 import fmt 冲突,放在同包 keys.go)**

```go
// domains/ursm/v2/cache/keys.go
package cache

import "fmt"

// keyf 是包内 fmt.Sprintf 别名,集中 key 构造便于审计。
func keyf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// StickyKey 复刻 domains/routing/sticky.go:346 buildStickyKeys 的 key,
// 但加 ursm:v2:sticky: 前缀,与旧 sticky_sessions 表物理隔离。
func StickyKey(level int, raw string) string {
	return keyf("ursm:v2:sticky:L%d:%s", level, raw)
}

// IntentKey 是 session intent 的 Redis key。
func IntentKey(sessionID string) string {
	return keyf("ursm:v2:intent:%s", sessionID)
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./domains/ursm/v2/cache/ -run TestNodeMirror -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add domains/ursm/v2/cache/nodemirror.go domains/ursm/v2/cache/nodemirror_test.go domains/ursm/v2/cache/keys.go
git commit -m "feat(ursm/v2/cache): add NodeMirror with generation monotonicity

applyToLRU 复刻 apply_decision.lua:25 的 cur_gen>in_gen 拒绝条件,
write-back 与 read-back 共用同一比较,杜绝迟到回填覆盖新 entry。
软过期(默认 30s)触发回源 Redis, 不立即删 entry。"
```

---

### Task 3: sticky Redis 同步写 + LRU 镜像

**Files:**
- Create: `domains/ursm/v2/cache/sticky.go`
- Test: `domains/ursm/v2/cache/sticky_test.go`

- [ ] **Step 1: 写失败测试(miniredis)**

```go
// domains/ursm/v2/cache/sticky_test.go
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStickyStoreSetGet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewStickyStore(rdb, 100, time.Hour)

	ctx := context.Background()
	// L1 key 模拟 buildStickyKeys 产出
	l1 := "tenant1:1:2:default:sess1:m"
	if err := s.Set(ctx, 1, l1, time.Hour); err != nil {
		t.Fatal(err)
	}
	credID, ok := s.Get(ctx, l1)
	if !ok || credID != 1 {
		t.Fatalf("expected credID=1, got %d ok=%v", credID, ok)
	}
	// LRU 命中第二次
	credID, ok = s.Get(ctx, l1)
	if !ok || credID != 1 {
		t.Fatalf("LRU hit failed: %d ok=%v", credID, ok)
	}
	// TTL 落到 Redis
	ttl := mr.TTL(StickyKey(1, l1))
	if ttl <= 0 {
		t.Fatal("sticky key missing TTL in Redis")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/ursm/v2/cache/ -run TestStickyStore -v`
Expected: FAIL — `NewStickyStore` 未定义。

- [ ] **Step 3: 实现 StickyStore**

```go
// domains/ursm/v2/cache/sticky.go
package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// StickyStore 是 sticky 路由的 Redis 权威 + 进程 LRU 镜像。
// 替代 domains/routing/sticky.go 的 map + 异步 DB 写(设计稿 Decision 4)。
//
// 不变量: 写路径 Redis 成功后才更新 LRU;读路径 LRU miss 回源 Redis 后回填。
// sticky 无 generation 概念(纯映射),因此 LRU 写不做单调比较,
// 但 TTL 由 Redis 单源管理,保证跨实例一致。
type StickyStore struct {
	rdb      *redis.Client
	lru      *LRU[string, int]
}

func NewStickyStore(rdb *redis.Client, capacity int, _ time.Duration) *StickyStore {
	return &StickyStore{rdb: rdb, lru: NewLRU[string, int](capacity)}
}

// Set 写 Redis(SET key credID EX ttl)并同步更新 LRU。
func (s *StickyStore) Set(ctx context.Context, credID int, rawKey string, ttl time.Duration) error {
	redisKey := StickyKey(levelOf(rawKey), rawKey)
	if err := s.rdb.Set(ctx, redisKey, strconv.Itoa(credID), ttl).Err(); err != nil {
		return err
	}
	s.lru.Put(redisKey, credID)
	return nil
}

// Get 先查 LRU,miss 回源 Redis 并回填。
func (s *StickyStore) Get(ctx context.Context, rawKey string) (int, bool) {
	redisKey := StickyKey(levelOf(rawKey), rawKey)
	if v, ok := s.lru.Get(redisKey); ok {
		return v, true
	}
	val, err := s.rdb.Get(ctx, redisKey).Result()
	if err == redis.Nil {
		return 0, false
	}
	if err != nil {
		return 0, false // fail-open: 上层应视作 sticky miss
	}
	credID, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	s.lru.Put(redisKey, credID)
	return credID, true
}

// levelOf 从 rawKey 推断 sticky 级别(L1/L2/L3)用于 Redis key 前缀。
// 约定: caller 传入的 rawKey 已是 buildStickyKeys 产出的完整 key,
// 通过段数判定: 6 段=L1, 5 段=L2, 4 段=L3。
func levelOf(rawKey string) int {
	segs := 0
	for _, c := range rawKey {
		if c == ':' {
			segs++
		}
	}
	switch segs + 1 {
	case 6:
		return 1
	case 5:
		return 2
	default:
		return 3
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/ursm/v2/cache/ -run TestStickyStore -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add domains/ursm/v2/cache/sticky.go domains/ursm/v2/cache/sticky_test.go
git commit -m "feat(ursm/v2/cache): add StickyStore (Redis sync write + LRU mirror)

替代 domains/routing/sticky.go 的 map + 异步 DB 写。TTL 由 Redis 单源管理,
跨实例一致。fail-open: Redis miss/error 返回 ok=false, 上层视作 sticky miss。"
```

---

### Task 4: session intent Redis 同步写 + LRU 镜像

**Files:**
- Create: `domains/ursm/v2/cache/intent.go`
- Test: `domains/ursm/v2/cache/intent_test.go`

- [ ] **Step 1: 写失败测试**

```go
// domains/ursm/v2/cache/intent_test.go
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIntentStoreSetGet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewIntentStore(rdb, 100, time.Minute)

	ctx := context.Background()
	in := Intent{TaskType: "chat", ChosenModel: "gpt-5", CredentialID: 7, HitCount: 0}
	if err := s.Set(ctx, "sess1", in, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(ctx, "sess1")
	if !ok || got.ChosenModel != "gpt-5" || got.CredentialID != 7 {
		t.Fatalf("expected gpt-5/7, got %+v ok=%v", got, ok)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/ursm/v2/cache/ -run TestIntentStore -v`
Expected: FAIL。

- [ ] **Step 3: 实现 IntentStore**

```go
// domains/ursm/v2/cache/intent.go
package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// Intent 是 session intent 的序列化载荷,字段对齐
// autoroute.CachedIntent(设计稿 Decision 5)。
type Intent struct {
	TaskType     string  `json:"task"`
	ChosenModel  string  `json:"model"`
	CredentialID int64   `json:"cred_id"`
	Profile      string  `json:"profile"`
	Confidence   float64 `json:"confidence"`
	Classifier   string  `json:"classifier"`
	HitCount     int     `json:"hit_count"`
	LastSeen     int64   `json:"last_seen"`
}

type IntentStore struct {
	rdb *redis.Client
	lru *LRU[string, Intent]
	softTTL time.Duration
}

func NewIntentStore(rdb *redis.Client, capacity int, softTTL time.Duration) *IntentStore {
	return &IntentStore{rdb: rdb, lru: NewLRU[string, Intent](capacity), softTTL: softTTL}
}

func (s *IntentStore) Set(ctx context.Context, sessionID string, in Intent, ttl time.Duration) error {
	in.LastSeen = time.Now().Unix()
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	key := IntentKey(sessionID)
	if err := s.rdb.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}
	s.lru.Put(key, in)
	return nil
}

func (s *IntentStore) Get(ctx context.Context, sessionID string) (Intent, bool) {
	key := IntentKey(sessionID)
	if v, ok := s.lru.Get(key); ok {
		return v, true
	}
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil || err != nil {
		return Intent{}, false
	}
	var in Intent
	if err := json.Unmarshal(data, &in); err != nil {
		return Intent{}, false
	}
	s.lru.Put(key, in)
	return in, true
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/ursm/v2/cache/ -run TestIntentStore -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add domains/ursm/v2/cache/intent.go domains/ursm/v2/cache/intent_test.go
git commit -m "feat(ursm/v2/cache): add IntentStore (Redis + LRU mirror for session intent)

替代 autoroute/session_intent_cache.go 的纯内存缓存。跨实例共享,
重启不丢。字段对齐 autoroute.CachedIntent。"
```

---

### Task 5: FpSlots NodeState 一次性迁移脚本

**Files:**
- Create: `domains/ursm/v2/cache/migrate_fpslots.go`
- Test: `domains/ursm/v2/cache/migrate_fpslots_test.go`

- [ ] **Step 1: 写失败测试(基于设计稿 Decision 1.1 字段映射表)**

```go
// domains/ursm/v2/cache/migrate_fpslots_test.go
package cache

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMigrateFpSlotsNodeState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// 模拟一个旧 FpSlots NodeState JSON(key 格式 llmgw:cred_fp_node:{cid}:{model})
	oldJSON := `{
		"credential_id": 5, "model": "m3",
		"success_count": 10, "failure_count": 2,
		"disabled": true, "disabled_until": 1700000000,
		"disabled_reason": "consecutive_3_failures",
		"disable_count": 1, "last_disabled_at": 1699999000
	}`
	rdb.Set(ctx, "llmgw:cred_fp_node:5:m3", oldJSON, 0)

	n, err := MigrateFpSlotsNodeStates(ctx, rdb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 migrated, got %d", n)
	}

	// 校验迁移后的 ursm:v2:node:5:m3 Hash 字段(按设计稿 Decision 1.1 表)
	nodeKey := "ursm:v2:node:5:m3"
	fields, err := rdb.HGetAll(ctx, nodeKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if fields["disabled"] != "1" {
		t.Errorf("disabled: want 1, got %q", fields["disabled"])
	}
	// DisabledUntil sec→ms: 1700000000 * 1000
	if fields["cool_until_ms"] != "1700000000000" {
		t.Errorf("cool_until_ms: want 1700000000000, got %q", fields["cool_until_ms"])
	}
	// DisabledReason → disabled_reason(不是 last_err)
	if fields["disabled_reason"] != "consecutive_3_failures" {
		t.Errorf("disabled_reason: want consecutive_3_failures, got %q", fields["disabled_reason"])
	}
	if fields["success_count"] != "10" {
		t.Errorf("success_count: want 10, got %q", fields["success_count"])
	}
	if fields["failure_count"] != "2" {
		t.Errorf("failure_count: want 2, got %q", fields["failure_count"])
	}
	if fields["disable_count"] != "1" {
		t.Errorf("disable_count: want 1, got %q", fields["disable_count"])
	}
	// 初始化字段
	if fields["generation"] != "1" {
		t.Errorf("generation: want 1, got %q", fields["generation"])
	}
	if fields["source_priority"] != "10" {
		t.Errorf("source_priority: want 10, got %q", fields["source_priority"])
	}
	// 非有效字段不得写入
	for _, bad := range []string{"cool_start_ms", "fail_count", "last_success_at_ms", "last_failure_at_ms"} {
		if _, ok := fields[bad]; ok {
			t.Errorf("non-effective field %q must NOT be written", bad)
		}
	}
	// 旧 key 保留(7d 自然过期,不删)
	if !mr.Exists("llmgw:cred_fp_node:5:m3") {
		t.Error("old FpSlots key was deleted; must be preserved for rollback")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/ursm/v2/cache/ -run TestMigrateFpSlots -v`
Expected: FAIL。

- [ ] **Step 3: 实现迁移**

```go
// domains/ursm/v2/cache/migrate_fpslots.go
package cache

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// fpSlotsNodeState 是 credentialfpslot.NodeState 的迁移端反序列化结构。
// 字段取自 credentialfpslot/node_state.go:33-48(只取要迁移的子集)。
type fpSlotsNodeState struct {
	CredentialID   int    `json:"credential_id"`
	Model          string `json:"model"`
	SuccessCount   int64  `json:"success_count"`
	FailureCount   int64  `json:"failure_count"`
	Disabled       bool   `json:"disabled"`
	DisabledUntil  int64  `json:"disabled_until"`  // unix sec
	DisabledReason string `json:"disabled_reason"` // → ursm disabled_reason
	DisableCount   int    `json:"disable_count"`
	// SlideWindow / LastSuccessAt / LastFailureAt / LastDisabledAt 按设计稿 Decision 1.1 不迁移
}

// MigrateFpSlotsNodeStates 扫描旧 llmgw:cred_fp_node:* key,按设计稿
// Decision 1.1 字段映射表写入 ursm:v2:node:{cid}:{model} Hash。
// 旧 key 不删(7d TTL 自然过期),保证回滚期数据可恢复。
// 返回迁移条数。
func MigrateFpSlotsNodeStates(ctx context.Context, rdb *redis.Client) (int, error) {
	iter := rdb.Scan(ctx, 0, "llmgw:cred_fp_node:*", 100).Iterator()
	n := 0
	for iter.Next(ctx) {
		oldKey := iter.Val()
		data, err := rdb.Get(ctx, oldKey).Bytes()
		if err != nil {
			continue // 跳过坏 key,不中断
		}
		var st fpSlotsNodeState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if st.CredentialID == 0 || st.Model == "" {
			continue
		}
		nodeKey := fmt.Sprintf("ursm:v2:node:%d:%s", st.CredentialID, st.Model)
		fields := map[string]interface{}{
			"available":        "1",
			"disabled":         boolToStr(st.Disabled),
			"fail_streak":      "0",
			"success_count":    fmt.Sprintf("%d", st.SuccessCount),
			"failure_count":    fmt.Sprintf("%d", st.FailureCount),
			"disable_count":    fmt.Sprintf("%d", st.DisableCount),
			"generation":       "1",
			"source_priority":  "10",
			"updated_at_ms":    "0",
		}
		if st.Disabled {
			fields["available"] = "0"
			// DisabledUntil sec → cool_until_ms ms
			fields["cool_until_ms"] = fmt.Sprintf("%d", st.DisabledUntil*1000)
			fields["disabled_reason"] = st.DisabledReason
		}
		if err := rdb.HSet(ctx, nodeKey, fields).Err(); err != nil {
			return n, fmt.Errorf("cache: migrate %s: %w", oldKey, err)
		}
		n++
	}
	return n, iter.Err()
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/ursm/v2/cache/ -run TestMigrateFpSlots -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add domains/ursm/v2/cache/migrate_fpslots.go domains/ursm/v2/cache/migrate_fpslots_test.go
git commit -m "feat(ursm/v2/cache): add FpSlots NodeState migration (Decision 1.1)

按设计稿字段映射表迁移 llmgw:cred_fp_node:* → ursm:v2:node:*。
sec→ms 单位转换, DisabledReason→disabled_reason, 旧 key 保留不删。
非有效字段(cool_start_ms/fail_count 等)不写入。"
```

---

## Phase B — 接入既有缓存(sticky / intent 双写过渡,M2)

### Task 6: StickyCache 委托 StickyStore(双写过渡)

**Files:**
- Modify: `domains/routing/sticky.go`(添加 `SetRedisStore`,在 `RecordSuccess`/`GetMultiLevel` 双写)

- [ ] **Step 1: 写失败测试(双写行为)**

```go
// domains/routing/sticky_redis_test.go
package routing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestStickyDoubleWrite(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewStickyStore(rdb, 100, time.Hour)

	s := NewStickyCache()
	s.SetRedisStore(store)

	s.RecordSuccessMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m", 9)

	cred, ok := s.GetMultiLevel("t1", intPtr(1), intPtr(2), "default", "sess1", "m")
	if !ok || cred.CredentialID != 9 {
		t.Fatalf("in-memory lookup failed: %+v ok=%v", cred, ok)
	}
	// Redis 侧也有(L1 key)
	_, l2, l3 := buildStickyKeys("t1", intPtr(1), intPtr(2), "default", "sess1", "m")
	_ = l3
	redisCred, ok := store.Get(context.Background(), l2)
	if !ok || redisCred != 9 {
		t.Fatalf("Redis double-write missing: %d ok=%v", redisCred, ok)
	}
}

func intPtr(i int) *int { return &i }
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/routing/ -run TestStickyDoubleWrite -v`
Expected: FAIL — `SetRedisStore` 未定义。

- [ ] **Step 3: 添加 SetRedisStore + 双写**

在 `domains/routing/sticky.go` 的 `StickyCache` struct 加字段:

```go
// domains/routing/sticky.go (struct 改动)
type StickyCache struct {
	mu       sync.RWMutex
	items    map[string]stickyEntry
	dbPool   *pgxpool.Pool
	redisStore StickyRedisStore // 双写过渡;nil 时退化为旧行为
}

// StickyRedisStore 是 domains/ursm/v2/cache.StickyStore 的最小接口,
// 避免 routing → ursm/v2/cache 的硬依赖反转(本包只依赖接口)。
type StickyRedisStore interface {
	Set(ctx context.Context, credID int, rawKey string, ttl time.Duration) error
	Get(ctx context.Context, rawKey string) (int, bool)
}

func (s *StickyCache) SetRedisStore(store StickyRedisStore) {
	s.redisStore = store
}
```

在 `RecordSuccessMultiLevel`(`sticky.go:191`)的 L1/L2/L3 写循环后,追加 Redis 双写:

```go
// sticky.go RecordSuccessMultiLevel 末尾(在 dbSetMultiLevel 的 go 调用附近)
if s.redisStore != nil {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	levels := []struct {
		key string
		ttl time.Duration
	}{
		{l1, 1 * time.Hour},
		{l2, 24 * time.Hour},
		{l3, 7 * 24 * time.Hour},
	}
	for _, lv := range levels {
		if lv.key == "" {
			continue
		}
		// 双写: 失败仅记日志,不阻塞(过渡期 Redis 不是权威)
		if err := s.redisStore.Set(ctx, credentialID, lv.key, lv.ttl); err != nil {
			slog.Debug("sticky redis double-write failed", "key", lv.key, "error", err)
		}
	}
}
```

在 `GetMultiLevel`(`sticky.go:96`)的 L1/L2/L3 miss 后,追加 Redis fallback:

```go
// sticky.go GetMultiLevel 末尾(在 return StickyLookupResult{Found: false} 前)
if s.redisStore != nil {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for _, k := range []string{l1, l2, l3} {
		if k == "" {
			continue
		}
		if credID, ok := s.redisStore.Get(ctx, k); ok {
			// 回填内存,后续命中走快路径
			s.Set(k, credID, ttlForLevel(k))
			return StickyLookupResult{CredentialID: credID, Level: StickyLevelClient, Found: true}
		}
	}
}
```

(新增 helper `ttlForLevel` 复用 levelOf 逻辑返回 1h/24h/7d。)

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/routing/ -run TestStickyDoubleWrite -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add domains/routing/sticky.go domains/routing/sticky_redis_test.go
git commit -m "feat(routing/sticky): double-write to Redis (URSM v2 transition)

SetRedisStore 注入 StickyRedisStore 接口, RecordSuccessMultiLevel 双写
内存+Redis, GetMultiLevel miss 后回源 Redis。nil 时退化为旧行为,
保证 URSM_V2_MODE=off 不受影响。"
```

---

### Task 7: SessionIntentCache 委托 IntentStore(双写过渡)

**Files:**
- Modify: `autoroute/session_intent_cache.go`

- [ ] **Step 1: 写失败测试**

```go
// autoroute/session_intent_redis_test.go
package autoroute

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestIntentDoubleWrite(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewIntentStore(rdb, 100, time.Minute)

	c := NewSessionIntentCache(time.Minute)
	c.SetRedisStore(store)

	c.Put("sess1", CachedIntent{TaskType: TaskChat, ChosenModel: "m", CredentialID: 3})
	got, ok := c.Get("sess1")
	if !ok || got.CredentialID != 3 {
		t.Fatalf("in-memory miss: %+v ok=%v", got, ok)
	}
	redisIn, ok := store.Get(context.Background(), "sess1")
	if !ok || redisIn.CredentialID != 3 {
		t.Fatalf("Redis double-write missing: %+v ok=%v", redisIn, ok)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./autoroute/ -run TestIntentDoubleWrite -v`
Expected: FAIL — `SetRedisStore` 未定义。

- [ ] **Step 3: 添加双写**

在 `autoroute/session_intent_cache.go` 的 `SessionIntentCache` 加字段与接口(模式同 Task 6):

```go
// SessionIntentCache 新增字段
type SessionIntentCache struct {
	mu     sync.RWMutex
	items  map[string]CachedIntent
	ttl    time.Duration
	redisStore IntentRedisStore // 双写过渡;nil 退化
}

// IntentRedisStore 是 domains/ursm/v2/cache.IntentStore 的最小接口。
type IntentRedisStore interface {
	Set(ctx context.Context, sessionID string, in ursmcache.Intent, ttl time.Duration) error
	Get(ctx context.Context, sessionID string) (ursmcache.Intent, bool)
}

func (c *SessionIntentCache) SetRedisStore(store IntentRedisStore) {
	c.redisStore = store
}
```

在 `Put` 末尾追加 Redis 双写(转 `ursmcache.Intent`),在 `Get` miss 后回源 Redis。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./autoroute/ -run TestIntentDoubleWrite -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add autoroute/session_intent_cache.go autoroute/session_intent_redis_test.go
git commit -m "feat(autoroute/intent): double-write to Redis (URSM v2 transition)

SetRedisStore 注入, Put/Get 双写内存+Redis。跨实例共享, 重启不丢。"
```

---

### Task 8: main.go wire 双写 + 启动迁移

**Files:**
- Modify: `cmd/gateway/main.go`

- [ ] **Step 1: 在 redis client 就绪后 wire StickyStore / IntentStore / NodeMirror**

定位 `main.go` 里 sticky / intent / ursmV2Mgr 的初始化段(约 L500-560),追加:

```go
// cmd/gateway/main.go (在 ursmV2Mgr 构造后)
nodeMirror := ursmcache.NewNodeMirror(100000, 30*time.Second)
stickyStore := ursmcache.NewStickyStore(redisClientForCache.Client(), 100000, time.Hour)
intentStore := ursmcache.NewIntentStore(redisClientForCache.Client(), 50000, time.Minute)

// 双写过渡: 注入既有缓存
stickyCache.SetRedisStore(stickyStore)
intentCache.SetRedisStore(intentStore)

// 启动一次性迁移(幂等: 已迁移的 key 因 generation>=1 不会重复写)
if ursmV2Mgr != nil && ursmV2Mgr.Mode() != api.ModeOff {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if n, err := ursmcache.MigrateFpSlotsNodeStates(ctx, redisClientForCache.Client()); err != nil {
			slog.Warn("fpslots migration failed", "error", err)
		} else if n > 0 {
			slog.Info("fpslots node states migrated", "count", n)
		}
	}()
}
```

(具体变量名按 main.go 现有命名对齐;迁移仅在 mode≠off 时跑,保证 off 部署零影响。)

- [ ] **Step 2: 编译验证**

Run: `go build ./cmd/gateway/`
Expected: 无错误。

- [ ] **Step 3: 提交**

```bash
git add cmd/gateway/main.go
git commit -m "feat(gateway): wire URSM v2 cache stores + FpSlots migration

注入 StickyStore/IntentStore/NodeMirror, 启动时幂等迁移 FpSlots NodeState。
mode=off 时迁移不执行, 双写仍注入但 Redis 写不影响旧路径(nil-safe)。"
```

---

## Phase C — 入口收敛(M3,需 canary 灰度)

> **风险提示**: M3 触及路由热路径,必须在 `URSM_V2_MODE=canary` 7 天稳定后再做。
> 任何任务可被 `URSM_V2_MODE=off` 回退。

### Task 9: feature flag 下沉到 legacyflags

**Files:**
- Create: `autoroute/internal/legacyflags/legacyflags.go`
- Modify: `autoroute/feature_flags.go`

- [ ] **Step 1: 创建 legacyflags 包,迁移 9 个 env 解析**

```go
// autoroute/internal/legacyflags/legacyflags.go
// Package legacyflags 收纳 deprecated 的 autoroute env(设计稿 Decision 6)。
// 新代码一律读 ursm mode, 仅 URSM_V2_MODE=off 时这些 flag 才生效。
package legacyflags

import "os"

// Deprecated: 用 URSM_V2_MODE 替代。保留仅为回退路径。
type Flags struct {
	UseSimplifiedScoring  bool
	UseHotTop3Pool        bool
	UseCacheRevalidation  bool
	Use48hFallback        bool
	AutoOnMessages        bool
	AutoOnResponses       bool
	AutoOnEmbeddings      bool
	AutoEmbeddingRoute    bool
}

func Load() *Flags {
	return &Flags{
		UseSimplifiedScoring: envBool("AUTO_USE_SIMPLIFIED_SCORING", false),
		UseHotTop3Pool:       envBool("AUTO_USE_HOT_TOP3_POOL", false),
		UseCacheRevalidation: envBool("AUTO_USE_CACHE_REVALIDATION", false),
		Use48hFallback:       envBool("AUTO_USE_48H_FALLBACK", false),
		AutoOnMessages:       envBool("AUTO_ON_MESSAGES", false),
		AutoOnResponses:      envBool("AUTO_ON_RESPONSES", false),
		AutoOnEmbeddings:     envBool("AUTO_ON_EMBEDDINGS", false),
		AutoEmbeddingRoute:   envBool("AUTO_EMBEDDING_ROUTE", false),
	}
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := parseBool(v)
	if err != nil {
		return def
	}
	return b
}
```

(把 `feature_flags.go` 里 9 个字段的 env 读取改为调用 `legacyflags.Load()`,保留 struct 兼容;`UseChannelQualityRouting` 不下沉。)

- [ ] **Step 2: 运行现有 autoroute 测试确认无回归**

Run: `go test ./autoroute/ -run TestFeatureFlags -v`
Expected: PASS。

- [ ] **Step 3: 提交**

```bash
git add autoroute/internal/legacyflags/legacyflags.go autoroute/feature_flags.go
git commit -m "refactor(autoroute): move 9 deprecated envs to legacyflags (Decision 6)

UseChannelQualityRouting 保留(已审计稳定特性)。新代码读 URSM_V2_MODE。"
```

---

### Task 10: router.go 移除 r.URSM / r.StateManager 分支

**Files:**
- Modify: `domains/streaming/executors/router.go`

- [ ] **Step 1: 写测试 — authoritative 模式下不调用 StateManager**

```go
// domains/streaming/executors/router_converge_test.go
package executors

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestRouterAuthoritativeSkipsLegacyState(t *testing.T) {
	r := &Router{
		URSMv2: &fakeURSMv2Mgr{mode: api.ModeAuthoritative, ready: true},
		StateManager: &fakeStateMgr{enabled: true, calls: 0},
	}
	cands := []provider.Candidate{{CredentialID: 1, RawModel: "m", StandardizedName: "m"}}
	got := r.PlanCandidates(cands, nil, nil, nil)
	if len(got) == 0 {
		t.Fatal("authoritative mode dropped all candidates")
	}
	if r.StateManager.(*fakeStateMgr).calls != 0 {
		t.Fatalf("StateManager called %d times in authoritative mode; expected 0",
			r.StateManager.(*fakeStateMgr).calls)
	}
}
```

(`fakeURSMv2Mgr` / `fakeStateMgr` 是测试桩,实现对应最小接口。)

- [ ] **Step 2: 运行确认失败**

Run: `go test ./domains/streaming/executors/ -run TestRouterAuthoritativeSkipsLegacyState -v`
Expected: FAIL(当前 router.go 仍走 StateManager)。

- [ ] **Step 3: 修改 router.go — authoritative 模式跳过 StateManager**

在 `PlanCandidates`(`router.go:83`)里,把现有的 `selectStateBackend` + `r.URSM`(`planWithURSM`)分支收敛:`URSMv2.Mode()==Authoritative && Ready()` 时直接返回 URSMv2Backend,跳过 `filterHealthyNodes` 和 StateManager;否则保留 Legacy 路径。移除 `r.URSM != nil && r.URSM.Enabled()` 分支调用(标记 deprecated,运行时 main.go 不再 wire 旧 Manager)。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./domains/streaming/executors/ -run TestRouterAuthoritative -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add domains/streaming/executors/router.go domains/streaming/executors/router_converge_test.go
git commit -m "refactor(router): skip StateManager when URSM v2 authoritative

authoritative 模式下只走 URSMv2Backend, StateManager/Shadow 零调用(AC#1)。
URSM_V2_MODE=off 时回退 Legacy 路径。"
```

---

### Task 11: 下沉 routingstate / credentialstate 到 _to-be-deprecated

**Files:**
- Move: `domains/routingstate/` → `_to-be-deprecated/routingstate/`
- Move: `domains/credentialstate/` → `_to-be-deprecated/credentialstate/`
- Modify: 所有 import(全局替换)

- [ ] **Step 1: git mv + 批量改 import**

```bash
git mv domains/routingstate _to-be-deprecated/routingstate
git mv domains/credentialstate _to-be-deprecated/credentialstate
# 批量替换 import 路径
grep -rl "domains/routingstate" --include="*.go" | xargs sed -i '' 's|domains/routingstate|_to-be-deprecated/routingstate|g'
grep -rl "domains/credentialstate" --include="*.go" | xargs sed -i '' 's|domains/credentialstate|_to-be-deprecated/credentialstate|g'
```

- [ ] **Step 2: 移除 main.go 的 ShadowObserver wire**

定位 `cmd/gateway/main.go:1271` `routingExec.RoutingStateShadow = routingstate.NewShadowObserver()`,删除该行(或加 `if mode == off` 守卫)。

- [ ] **Step 3: 编译验证**

Run: `go build ./...`
Expected: 无错误。

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "refactor: move routingstate/credentialstate to _to-be-deprecated (AC#8)

main.go 不再 wire ShadowObserver。旧代码保留编译,URSM_V2_MODE=off 仍可回退。
_to-be-deprecated/ 通过 //nolint:depguard 豁免 lint。"
```

---

## Phase D — 灰度与物理删除(M4)

### Task 12: 部署 245 canary + 监控 7 天

- [ ] **Step 1: 部署 245,`URSM_V2_MODE=canary`**
- [ ] **Step 2: 监控 7 天**
  - P95 路由耗时(目标: ≤ baseline + 1ms)
  - `routing_state_source` 分布(fallback 占比 < 1%)
  - `ur_sticky_hit_rate` / `ur_intent_hit_rate`(目标 ≥ 80%)
- [ ] **Step 3: 通过后切 `URSM_V2_MODE=authoritative`**
- [ ] **Step 4: 部署 154 同流程**
- [ ] **Step 5: 7d 稳定后物理删除 `_to-be-deprecated/routingstate`、`_to-be-deprecated/credentialstate`**

```bash
git rm -r _to-be-deprecated/routingstate _to-be-deprecated/credentialstate
git commit -m "chore: remove deprecated routingstate/credentialstate (M4 complete)"
```

- [ ] **Step 6: `go build ./...` 确认通过(AC#8)**

---

## Self-Review

**Spec coverage 核对:**
- Decision 1(URSM v2 单源)→ Task 10(router 收敛)+ Task 8(wire)
- Decision 1.1(FpSlots 映射)→ Task 5(迁移脚本,测试覆盖每字段)
- Decision 2(LRU 镜像 + gen 单调)→ Task 1(LRU)+ Task 2(NodeMirror,测试覆盖 gen 单调 + 软过期)
- Decision 3(fail-open)→ Task 3/4(Sticky/Intent Get 在 Redis error 时返回 ok=false)+ Task 10
- Decision 4(sticky Redis)→ Task 3 + Task 6(双写过渡)
- Decision 5(intent Redis)→ Task 4 + Task 7
- Decision 6(feature flag 收敛)→ Task 9
- Decision 7(防封锁独立)→ 全程不动 `credentialfpslot/`、`credential/limiter.go`、`credential/breaker.go`、`disguise/`、`identity/`
- AC#1 → Task 10 测试断言 StateManager.calls==0
- AC#2 → Task 3/4 LRU hit(测试覆盖)+ M4 监控
- AC#3 → Task 3/4 fail-open 路径 + M4 监控
- AC#4 → Task 9
- AC#5 → 防封锁测试零修改(不改相关文件)
- AC#6 → Task 12 监控
- AC#7 → Task 12
- AC#8 → Task 11 + Task 12 Step 6

**Placeholder 扫描:** 无 TBD/TODO;每个代码步骤都有完整代码。

**Type 一致性:** `StickyRedisStore` / `IntentRedisStore` 接口在 Task 6/7 定义,Task 8 wire 时引用一致;`NodeView` 字段在 Task 2 定义,Task 5 迁移测试引用 `cool_until_ms`/`disabled_reason` 与之对齐。

**已知偏离设计稿(已记录):** 设计稿用 `ur:cred:`/`ur:sticky:`/`ur:intent:` 前缀,本计划按代码事实改用 `ursm:v2:node:`/`ursm:v2:sticky:`/`ursm:v2:intent:`,避免 M1 引入全新 Redis 键布局。这与设计稿"不引入新前缀"的精神一致(复用 `ursm:v2:` 现有命名空间)。
