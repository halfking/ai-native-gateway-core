# cache/delta/ — Plan (B3)

> **状态**: 待实现
> **作者**: opencode (承接 Phase 0 handoff)
> **日期**: 2026-07-01
> **关联 spec**: `docs/产品方案/2026-07-01-next-steps-after-phase0.md` §6
> **关联上游**: `cache/prefix/`（消息稳定化）、`cache/kv/`（SHA256 指纹 + Store）
> **工作量**: 3 人日
> **风险**: 中

---

## 1. 背景

LLM 网关在 chat-completions 流量中存在天然的**前缀共享模式**：同一会话内连续 N 个请求的请求体有 90%+ 字节相同（system prompt + 历史 turns），仅最后 1–2 个 turn 变化。当前的 `cache/kv/` 为每个完整请求体计算 SHA256 指纹并按 (key → payload) 存储，**没有利用前缀共享**：

- 同一会话内 N 个请求各自产生独立的 KV 键（N 份 payload）
- 实际存储占用 ≈ N × 完整请求体长度
- 命中率按"完全相同 key"算很低

**B3 增量编码**：对前缀共享的 payload，只存"父 payload + 增量 delta"，重建时通过父链回放 — 将存储占用压缩到 O(变更的尾部) + O(共享前缀)。

---

## 2. 目标

> **Goal**: 实现 `cache/delta/`，提供 payload 间增量编码 + 重建能力，通过 `DeltaStore` 包装 `kv.Store`，使聊天场景典型连续请求序列的存储占用压缩 ≥50%（人造 trace 验证），并对外完全兼容 `kv.Store` 接口。

---

## 3. 边界（Out of Scope）

- ❌ **不做**：流式响应（SSE chunk）的增量压缩 — 那是另一个领域（streaming/）
- ❌ **不做**：跨会话、跨用户的全局去重 — 由 `cache/semantic/` 处理
- ❌ **不做**：修改 `cache/kv/` 的 key 计算逻辑 — kv/ 是纯函数，B3 在它之上叠加
- ❌ **不做**：自动选 parent 的相似度算法（embedding 等）— v1 由调用方显式提供 ParentKey，未来可扩展
- ❌ **不做**：磁盘/RocksDB 后端 — 仅复用 `kv.Store` 接口（先支持内存 + 任何实现了 `kv.Store` 的实现）
- ❌ **推迟到 v1.1**：二进制 diff 算法（vdelta / bsdiff）— v1 用 JSON 行级 diff（chat 场景足够）

---

## 4. 设计

### 4.1 数据模型

```go
package delta

// Op 是增量操作的种类。
type Op uint8

const (
    OpAppend      Op = iota // 在 parent 末尾追加 bytes
    OpReplaceTail            // 替换 parent 最后 N 个字节为 bytes
    OpFull                   // 不压缩，直接存完整 payload（fallback）
)

// Delta 是存储的增量条目。
type Delta struct {
    ParentKey string // 父条目的 kv.Store key；空字符串表示这是根
    Op        Op
    Payload   []byte // OpAppend/OpReplaceTail 的增量字节
    Cutoff    int    // OpReplaceTail: parent[Cutoff:] 被替换
}
```

### 4.2 编码算法（v1：行级 JSON diff）

针对 chat-completions body 的两个版本：

```
parent = {"messages":[m1,m2,...mK], "model":"x"}
new    = {"messages":[m1,m2,...mK+1], "model":"x"}
```

1. 解析成 JSON 结构，比较 `messages` 数组
2. 若 `new.messages` 是 `parent.messages` 的**前缀 + 1 个新 turn**（append 模式）：
   - delta = `{"op":"append","msgs":[mK+1]}`
3. 若 `new.messages` 与 `parent.messages` 完全相同、仅尾部 last-N 个 turn 被替换：
   - delta = `{"op":"replace_tail","cutoff":K-N,"msgs":[mK-N+1',...,mK']}`
4. 若差异超过阈值（>50% 字节变化 或 结构差异大）：
   - 标记 `OpFull`，存完整 payload（fallback）

**为什么行级而非二进制 diff**：
- chat-completions body 是文本 JSON，行级足够
- 二进制 diff 算法复杂度高、依赖多，v1 不到瓶颈
- 行级 diff 易于调试 + 跨语言兼容

### 4.3 Store 集成

```go
// DeltaStore 包装 kv.Store，自动按 prefix-similarity 选择 parent 并存增量。
type DeltaStore struct {
    inner     kv.Store       // 底层存储（内存或 Redis）
    parentMap sync.Map       // newKey -> parentKey（v1 由调用方显式 put）
}

// Lookup 拉取条目并按需通过 parent 链回放重建。
func (s *DeltaStore) Lookup(ctx context.Context, key string) ([]byte, bool, error)

// Store 存条目；若 parentMap 中有 parentKey 且 Encode 返回 (delta, true) 则存增量；
// 否则 fallback 存完整 payload。
func (s *DeltaStore) Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error
```

**对外接口**：`DeltaStore` 完全实现 `kv.Store` 接口，可直接替换现有调用点（**调用方零改动**）。

### 4.4 调用方约定（v1）

调用方需在 Store 之前显式调用 `s.SetParent(newKey, parentKey)` 提示父子关系：

```go
deltaStore.SetParent(req2Key, req1Key)  // req2 大概率与 req1 共享前缀
deltaStore.Store(ctx, req2Key, req2Body, ttl)
```

v1 简单显式；v1.1 可加自动 parent 发现（LRU 最近 key + 前缀字节比对）。

---

## 5. Plan（实现切片 + 验收）

### Acceptance Criteria

| ID | 描述 | 验证方式 |
|---|---|---|
| AC-1 | `Encode(parent, new)` 对 chat 场景 append 模式返回 `OpAppend` + delta < `len(new)` | `encode_test.go::TestEncode_AppendChatTurn` |
| AC-2 | `Apply(parent, delta)` 是 `Encode` 的精确反函数（round-trip 字节完全相同） | `encode_test.go::TestApply_RoundTrip` |
| AC-3 | `DeltaStore` 完全实现 `kv.Store` 接口（compile-time check + 接口测试） | `store_test.go::TestDeltaStore_SatisfiesKVStore` |
| AC-4 | `DeltaStore.Lookup` 对 delta 存储条目返回正确字节 | `store_test.go::TestDeltaStore_LookupDeltaEntry` |
| AC-5 | 人造 trace（10 个连续 chat 请求）：存储占用压缩 ≥50% | `store_test.go::TestDeltaStore_CompressionRatio` |
| AC-6 | 全部现有 `cache/kv/`、`cache/prefix/`、`cache/semantic/` 测试通过（零回归） | `go test ./cache/...` |
| AC-7 | 覆盖率 ≥ 80% | `go test -cover ./cache/delta/` |
| AC-8 | 并发安全（100 goroutines × 1000 ops）无 race | `go test -race ./cache/delta/` |
| AC-9 | 空输入、畸形 JSON、超大 payload、parent 缺失等边界场景不 panic，按设计 fallback | `edge_cases_test.go` |
| AC-10 | `go vet ./cache/delta/` + `golangci-lint run ./cache/delta/` 无 issue | CI |

### Implementation Slices

| Slice | 描述 | 文件 | 估计 LOC | 依赖 |
|---|---|---|---|---|
| **S1** | 数据模型 + Op 常量 + Delta struct | `delta.go` | ~50 | — |
| **S2** | `Encode(parent, new) → (Delta, bool)` 行级 diff 实现 | `encode.go` | ~150 | S1 |
| **S3** | `Apply(parent, delta) → []byte` 重建实现 | `apply.go` | ~80 | S1 |
| **S4** | `DeltaStore` 包装 `kv.Store`，实现 5 个接口方法 | `store.go` | ~200 | S2, S3 |
| **S5** | `SetParent(key, parentKey)` parent 关系注册 + 线程安全 | `store.go` (续) | ~30 | S4 |
| **S6** | `Stats` 扩展：FullStores / DeltaStores / Reconstructions / CompressionBytesSaved | `store.go` (续) | ~50 | S4 |
| **S7** | 单元测试 (encode/apply round-trip, edge cases) | `encode_test.go`, `apply_test.go`, `edge_cases_test.go` | ~300 | S2-S6 |
| **S8** | 集成测试 (DeltaStore + 真实 chat trace) | `store_test.go` | ~250 | S4-S6 |
| **S9** | `design_intent_test.go`：人类可读的设计意图文档测试 | `design_intent_test.go` | ~120 | S1-S6 |

**总估算**：~1230 LOC（含测试）

### Test Strategy

| 层 | 目标 | 工具 |
|---|---|---|
| 单元 | 每个函数 + 每个分支 + 每个边界 | `go test` |
| 集成 | DeltaStore + 真实 chat trace | `go test -run Integration` |
| 属性 | Apply(Encode(x, y), y) == y（fuzz 10000 case）| `go test -fuzz` |
| Race | 并发读写无 data race | `go test -race` |
| 性能 | 10K entry Store + Lookup < 100ms | `go test -bench` |
| 静态 | go vet + golangci-lint | CI |

### Risks

| 风险 | 缓解 |
|---|---|
| JSON 行级 diff 在高度 interleaved 的请求上失效 | 显式 fallback 到 OpFull + 监控 compression ratio |
| parent 链过长导致 Lookup O(N) 重建 | parent 链 ≤ 3（典型场景）；超时则 fallback |
| 旧条目 TTL 过期导致重建失败 | Apply 返回 ErrMissingParent → Lookup 返回 cache miss（不 panic）|
| 与 opencode-agent 活跃工作冲突 | B3 是 cache/delta/ 新目录，零交叉；commit 后 git pull rebase |

---

## 6. Verify (完成前 checklist)

- [ ] AC-1 ~ AC-10 全部自动化测试通过
- [ ] `go test ./cache/delta/ -cover -count=1` 覆盖率 ≥ 80%
- [ ] `go test -race ./cache/delta/` 无 race
- [ ] `go vet ./cache/delta/` 无 issue
- [ ] `golangci-lint run ./cache/delta/` 通过（依赖 C2 baseline，Round 40 后阻塞）
- [ ] `go test ./cache/...` 零回归
- [ ] `go build ./...` 退出码 0
- [ ] PR 描述包含：设计摘要 + 测试报告 + 性能 benchmark
- [ ] 主仓 bump llm-gateway-go 到本仓新 HEAD
- [ ] Phase 0 handoff 中 B3 状态更新为 ✅

---

## 7. 上线

不在本任务范围内（实施后由独立部署任务执行）。B3 是底层包，对外通过 `DeltaStore` 暴露，需要一个独立的小型集成 PR 把生产 `kv.Store` 调用点切换到 `DeltaStore`（评估影响后再做）。

---

## 8. 参考

- `cache/kv/`：`cache/kv/key.go` + `cache/kv/store.go` + `cache/kv/memory.go`（B2 已完成，最接近的现有模式）
- `cache/prefix/`：`cache/prefix/prefix.go`（消息稳定化的 boundary）
- 上游 spec：`docs/产品方案/2026-07-01-next-steps-after-phase0.md` §6
- handoff：`docs/产品方案/2026-07-01-session-handoff-phase0-closure.md` §6
- 架构上下文：`docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md` §2.2 ⑦