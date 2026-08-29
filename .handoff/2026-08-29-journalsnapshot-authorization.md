# 2026-08-29 JournalSnapshot Authorization Layer 实现完成

**交接时间:** 2026-08-29 16:30 +0800  
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`  
**当前分支:** `main`  
**最新 commit:** `c4be55aa0` (已 commit 到本地 main)

---

## §0 任务来源

承接 `.handoff/2026-08-29-journalsnapshot-bounded.md` §7 行动项 (P2)，实现 JournalSnapshot authorization layer（ADR 2026-08-28 §Decision point 4）。

---

## §1 本轮交付

### 1.1 实现概览

**提交:** `c4be55aa0 feat(dispatch): implement JournalSnapshot authorization layer (ADR DP4)`

**核心变更:**

1. **新增错误类型** `ErrJournalNotFound` (domains/dispatch/observation.go:11-14)
   - 用于 not-found-shaped 错误响应
   - 防止跨租户存在性泄露（cross-tenant existence leak）
   - 模仿 `requestjourney.ErrJourneyNotFound` 的语义

2. **新增接口** `AuthorizedJournalConsumer` (domains/dispatch/observation.go:315-338)
   ```go
   type AuthorizedJournalConsumer interface {
       ConsumeSnapshot(ctx context.Context, callerTenant, requestID string) (JournalSnapshot, error)
   }
   ```
   - Pull-based 查询接口（与现有 push-based `JournalSink` 互补）
   - 签名接受 `callerTenant` 参数，显式要求调用者身份
   - 返回 `(JournalSnapshot, error)` 而非 void

3. **实现演示存储** `InMemoryJournalStore` (domains/dispatch/journal_consumer.go)
   - 实现 `AuthorizedJournalConsumer` 接口
   - 授权逻辑核心：
     ```go
     key := callerTenant + ":" + requestID
     snap, found := s.snapshots[key]
     if !found {
         return JournalSnapshot{}, ErrJournalNotFound // 统一返回 not-found
     }
     ```
   - 跨租户访问、快照不存在、空 caller 都返回相同的 `ErrJournalNotFound`
   - 提供 `Store()` / `Count()` / `Clear()` 测试辅助方法

4. **新增测试** `TestJournalSnapshot_Authorization` (domains/dispatch/journal_snapshot_contract_test.go:267-366)
   - 取代原有的 `TestJournalSnapshot_Authorization_NotImplemented` (现跳过并注明 "replaced by TestJournalSnapshot_Authorization")
   - 6 个子测试用例：
     1. `same_tenant_access_succeeds`: 同租户访问成功
     2. `cross_tenant_access_returns_not_found`: 跨租户访问返回 not-found
     3. `missing_snapshot_returns_not_found`: 缺失快照返回 not-found
     4. `empty_caller_tenant_returns_not_found`: 空 caller 返回 not-found
     5. `empty_request_id_returns_not_found`: 空 requestID 返回 not-found
     6. `indistinguishable_errors`: 验证所有拒绝场景返回完全相同的错误（包括指针相等性检查）

---

## §2 设计决策

### 2.1 为什么是 pull-based 接口而不是增强 JournalSink？

**理由:**
- **ADR DP1 语境:** "consumer **requests** a snapshot ... through the existing requestjourney service boundary"
- **推送 vs 拉取语义:**
  - `JournalSink.ApplyJournalSnapshot`: pipeline 在 terminal 时推送给 sink，无外部调用者
  - `AuthorizedJournalConsumer.ConsumeSnapshot`: 外部调用者（admin API、诊断工具）主动查询
- **授权边界:** push 路径中 `snap.TenantID` 由 pipeline 内部保证可信；pull 路径需验证**调用者**身份

### 2.2 为什么返回错误而不是 void？

**当前 JournalSink 设计:**
```go
type JournalSink interface {
    ApplyJournalSnapshot(context.Context, JournalSnapshot)  // void 返回
}
```

**新接口设计:**
```go
type AuthorizedJournalConsumer interface {
    ConsumeSnapshot(ctx, callerTenant, requestID string) (JournalSnapshot, error)  // 返回错误
}
```

**理由:**
- **查询语义需要错误信号:** 调用者需要区分成功、not-found、基础设施故障
- **拒绝必须可见:** void 返回会强制 sink 只能默默丢弃未授权请求
- **匹配现有模式:** `requestjourney.QueryService.Detail` 也返回 `(DetailResult, error)`

### 2.3 为什么用 InMemoryJournalStore 而不是集成 requestjourney？

**当前实现:** 简单的内存 map 存储  
**生产方向:** 应通过 `requestjourney.QueryService` 或专用持久化存储

**理由:**
- **架构边界:** `requestjourney` 存储的是转换后的 `JourneyEvent`（无法反向重建 `JournalEntry`）
- **概念验证:** 当前实现满足 ADR 契约测试，演示授权逻辑
- **未来扩展:** 生产路径需要：
  1. **选项 A:** 在 `requestjourney.QueryService.Detail` 上添加 tenant 验证（HTTP handler 层）
  2. **选项 B:** 持久化原始 `JournalSnapshot` 到新存储（需添加序列化/持久化层）
  3. **选项 C:** 从 `requestjourney.JourneyEvent` 重建等价快照（需逆向转换逻辑）

---

## §3 ADR 合规性检查

| ADR §Decision Point | 实现状态 | 本轮变更 |
|---|---|---|
| 1. Bounded consumer boundary | ✅ **已实现** | 新增 pull-based 接口满足 DP1 "consumer requests" 语义 |
| 2. Read-model source | ✅ **已满足** | 快照从 `qr.AttemptJournal` 读取 |
| 3. Max event count + truncation metadata | ✅ **已实现** | `maxJournalSnapshotEvents=50`, `Truncated`, `TruncatedCount` |
| 4. Authorization | ✅ **本次实现** | `AuthorizedJournalConsumer` + not-found-shaped errors |
| 5. Persistence failure isolation | ✅ **已实现** | `cmd/gateway/main_dispatch_observation.go:109-115` |
| 6. Idempotency (snapshot_version) | ❌ **未实现** | 无 version 字段，重复调用会重新 apply |

**当前进度:** 5/6 核心功能已实现（DP6 idempotency 待实现）

---

## §4 验证

| 命令 | 结果 |
|---|---|
| `go test -v -run TestJournalSnapshot_Authorization ./domains/dispatch/` | PASS (0.00s), 6 subtests pass |
| `go test -v -run TestJournalSnapshot ./domains/dispatch/` | 4 pass, 2 skip (expected) |
| `go test ./domains/dispatch/ -count=1 -timeout 60s` | ok (25.245s) |
| `git log --oneline -1` | `c4be55aa0 feat(dispatch): implement JournalSnapshot authorization layer (ADR DP4)` |

---

## §5 架构注记

### 5.1 授权检查位置

当前实现在 **domain layer** (dispatch package) 提供接口契约，具体授权逻辑在实现类中。

**生产路径可能的位置:**

1. **HTTP adapter 层** (`admin/` 包)
   - 使用 `admin.GetTenantID(r)` 提取调用者租户
   - 使用 `admin.requireSessionTaskAccess` 模式做前置检查
   - 返回 `http.StatusNotFound` (404) 而非 403

2. **requestjourney adapter 层** (`cmd/gateway/main_dispatch_observation.go`)
   - 在 `dispatchJourneyJournalAdapter.ApplyJournalSnapshot` 中添加 tenant 白名单
   - 但这是 push 路径，当前无"caller"概念

3. **新增 query service** (e.g., `admin/journal_handlers.go`)
   - 暴露 HTTP API：`GET /api/admin/dispatch/journal/:tenant/:request_id`
   - 调用 `InMemoryJournalStore.ConsumeSnapshot(ctx, GetTenantID(r), requestID)`
   - 将 `ErrJournalNotFound` 转换为 404 响应

### 5.2 与 requestjourney.Detail 的关系

**相似性:**
- 都是 tenant-scoped 查询接口
- 都返回 not-found-shaped 错误
- 都通过 `(tenant_id, request_id)` 定位

**差异:**
- `Detail` 返回 `RequestJourney` (合并的 `JourneyEvent` 列表 + divergence 元数据)
- `ConsumeSnapshot` 返回 `JournalSnapshot` (原始 `JournalEntry` 列表 + truncation 元数据)

**未来可能统一:** 扩展 `requestjourney.QueryService` 添加 `DetailWithJournal(ctx, tenantID, requestID)` 方法，同时返回两者。

---

## §6 后续工作

### 6.1 短期（P1，安全关键）

1. **在 HTTP layer 暴露查询接口** (优先级 P1)
   - 添加 `admin/journal_handlers.go`
   - 实现 `GET /api/admin/dispatch/journal/:tenant/:request_id`
   - 集成 `admin.GetTenantID(r)` 授权检查
   - 返回 404 for unauthorized/missing

2. **持久化 JournalSnapshot** (选项 B，如果需要查询历史快照)
   - 当前 `InMemoryJournalStore` 只在进程生命周期内有效
   - 生产需持久化到 Redis/PostgreSQL
   - 需添加序列化逻辑（JSON or protobuf）

### 6.2 中期（P2，完整性）

3. **实现 Idempotency (snapshot_version)** (§ADR DP6)
   - 添加 `JournalSnapshot.SnapshotVersion` 字段（SHA256 or monotonic seq）
   - 在 `dispatchJourneyJournalAdapter` 中检查 `(tenant, request, version)` 是否已处理
   - 防止重复 apply

4. **集成 requestjourney.QueryService** (选项 A)
   - 在 `requestjourney.QueryService.Detail` 返回值中添加授权检查
   - 或新增 `requestjourney.QueryService.AuthorizedDetail(ctx, callerTenant, requestID)`
   - 避免维护两套查询接口

### 6.3 长期（可选）

5. **Accept ADR** (Status: Proposed → Accepted)
   - 当前实现覆盖 5/6 核心功能
   - 建议等 6/6 完成后再 Accept（idempotency 是幂等性保障）

6. **性能监控**
   - 添加 authorization rejection metrics（按原因分类：cross-tenant, missing, empty-caller）
   - 监控 `ErrJournalNotFound` 的调用频率，识别潜在的扫描攻击

---

## §7 当前状态

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`
- 分支：`main`，HEAD `c4be55aa0`
- 与 `origin/main`：本地领先 1 commit (需 push)
- 工作树：clean
- 未提交：无

---

## §8 下一会话行动项（优先级排序）

| 优先级 | 项 | 状态 | 预估 |
|---|---|---|---|
| P1 | 在 HTTP layer 暴露查询接口（admin/journal_handlers.go） | 可执行 | 1-2小时 |
| P1 | Push commit `c4be55aa0` to origin/main | 可执行 | 1分钟 |
| P2 | 实现 JournalSnapshot idempotency (snapshot_version) | 可执行 | 半天 |
| P2 | 决定持久化策略（Redis / PostgreSQL / 集成 requestjourney） | 需设计评审 | 半天 |
| P3 | Accept ADR 2026-08-28-requestjourney-journal-snapshot.md | 等待 6/6 完成 | 5分钟 |

**建议顺序:**
1. Push 当前 commit 到 origin/main
2. 暴露 HTTP 查询接口（演示完整的授权流程）
3. 实现 idempotency（完成 ADR 6/6）
4. Accept ADR
5. 根据使用场景决定持久化策略

---

## §9 引用

- 上游 handoff：`.handoff/2026-08-29-journalsnapshot-bounded.md` §7
- ADR：`docs/adr/2026-08-28-requestjourney-journal-snapshot.md`
- 实现文件：
  - `domains/dispatch/observation.go:11-14` (ErrJournalNotFound)
  - `domains/dispatch/observation.go:315-338` (AuthorizedJournalConsumer 接口)
  - `domains/dispatch/journal_consumer.go` (InMemoryJournalStore 实现)
  - `domains/dispatch/journal_snapshot_contract_test.go:267-366` (授权测试)
- 测试命令：`go test -v -run TestJournalSnapshot ./domains/dispatch/`
- Commit：`c4be55aa0 feat(dispatch): implement JournalSnapshot authorization layer (ADR DP4)`
