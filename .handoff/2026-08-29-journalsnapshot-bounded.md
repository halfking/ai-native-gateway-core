# 2026-08-29 JournalSnapshot Bounded Consumer 实现完成

**交接时间:** 2026-08-29 14:00 +0800  
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`  
**当前分支:** `main`  
**最新 commit:** `f1ae7226b` (已 push 到 origin/main)

---

## §0 任务来源

承接 `.handoff/2026-08-29-section4-execution.md` §6 行动项，实现 JournalSnapshot bounded consumer + truncation metadata（ADR 2026-08-28-requestjourney-journal-snapshot.md §Decision point 3）。

---

## §1 本轮交付

### 1.1 实现概览

**提交:** `0814e774d feat(dispatch): implement JournalSnapshot bounded consumer with truncation metadata`

**核心变更:**

1. **新增常量** `maxJournalSnapshotEvents = 50` (domains/dispatch/observation.go:267)
   - 限制消费者接收的最大事件数
   - 低于 journalCapacity (128)，确保即使满容量 journal 也会截断

2. **扩展 JournalSnapshot 结构** (domains/dispatch/observation.go:278-295)
   ```go
   type JournalSnapshot struct {
       TenantID       string
       RequestID      string
       Entries        []JournalEntry
       Truncated      bool  // 新增：是否发生截断
       TruncatedCount int   // 新增：被丢弃的条目数
   }
   ```

3. **实现截断逻辑** (domains/dispatch/pipeline.go:1523-1565)
   - `emitJournalSnapshot` 现在检查 journal 长度
   - 当超过 `maxJournalSnapshotEvents` 时，保留**最近的 50 条**（包括 terminal entry）
   - 设置 `Truncated=true` 和 `TruncatedCount`（被丢弃的条目数）
   - 显式截断元数据，符合 ADR "Truncation is explicit in the returned metadata rather than silently dropping events"

4. **更新测试** (domains/dispatch/journal_snapshot_contract_test.go:94-158)
   - `TestJournalSnapshot_LargeJournal_CurrentBehavior` 从 baseline 测试改为验证测试
   - 验证 101 条 journal（100 retries + 1 terminal）→ 截断为 50 条
   - 验证 `Truncated=true` 和 `TruncatedCount=51`
   - 验证 terminal entry 保留在最后
   - 验证最旧的条目被丢弃（第一条是 seq 52）

---

## §2 设计决策

### 2.1 为什么选择"保留最近 N 条"而不是"保留最旧 N 条"？

**理由:**
- **调试价值:** terminal entry 和其前驱最有价值（最终失败/成功的上下文）
- **事件顺序:** 最近的决策反映当前状态，最旧的决策可能已过时
- **一致性:** terminal entry **必须**包含在快照中（ADR §Decision），保留最近的条目自然满足

### 2.2 为什么设置 maxJournalSnapshotEvents = 50？

**权衡:**
- **低于 journalCapacity (128):** 确保截断在消费侧发生，不依赖 ring buffer 边界
- **足够覆盖正常请求:** 正常请求 < 10 attempts，50 条足够覆盖 5x 异常重试场景
- **防御病理路径:** 100+ retries 属于异常，完整 trace 对调试意义不大（中间重复的 rate_limit 不如看首尾）

---

## §3 ADR 合规性检查

| ADR §Decision Point | 实现状态 | 验证 |
|---|---|---|
| 1. Bounded consumer boundary | ✅ **已实现** | `emitJournalSnapshot` 通过 Pipeline 边界交付，不扫描 Redis |
| 2. Read-model source | ✅ **已满足** | 快照从 `qr.AttemptJournal` 读取（单一来源） |
| 3. Max event count + truncation metadata | ✅ **本次实现** | `maxJournalSnapshotEvents=50`, `Truncated`, `TruncatedCount` |
| 4. Authorization | ❌ **未实现** | 无 caller context 验证，无 cross-tenant 保护 |
| 5. Persistence failure isolation | ✅ **已实现** | `cmd/gateway/main_dispatch_observation.go:109-115` |
| 6. Idempotency (snapshot_version) | ❌ **未实现** | 无 version 字段，重复调用会重新 apply |

**当前进度:** 3/6 核心功能已实现

---

## §4 验证

| 命令 | 结果 |
|---|---|
| `go test -v -run TestJournalSnapshot_LargeJournal ./domains/dispatch/` | PASS (0.00s) |
| `go test -v -run TestJournalSnapshot ./domains/dispatch/` | 2 pass, 2 skip (expected) |
| `go test ./domains/dispatch/ -count=1 -timeout 60s` | ok (24.756s) |
| `git push` | f1ae7226b → origin/main |

---

## §5 后续工作

### 5.1 短期（P2，可立即执行）

1. **添加小 journal 测试用例** (10 entries < 50)
   - 验证 `Truncated=false` 和 `TruncatedCount=0`
   - 确保截断逻辑不影响正常请求

2. **性能基准测试**
   - 测量 101-entry → 50-entry 截断的 slice 操作开销
   - 预期：可忽略（单次 slice 操作，terminal path）

### 5.2 中期（P2，需设计）

3. **实现 Authorization layer** (§4, ADR Decision point 4)
   - 添加 `AuthorizedJournalConsumer` 接口
   - 在 adapter 中验证 caller context
   - 返回 not-found-shaped error（避免 existence leak）

4. **实现 Idempotency (snapshot_version)** (§4, ADR Decision point 6)
   - 添加 `SnapshotVersion` 字段（SHA256 or monotonic seq）
   - 在 adapter 中检查 `(tenant, request, version)` 是否已处理
   - 防止重复 apply

### 5.3 长期（可选）

5. **添加 serialized size 限制**
   - 当前只限制 event count (50)
   - 未来可添加 max size (e.g., 10KB) 二次截断
   - 处理单条巨大 event（e.g., 大 error message）

6. **Accept ADR** (Status: Proposed → Accepted)
   - 当前实现覆盖 3/6 核心功能
   - 建议等 4/6 完成后再 Accept（authorization + idempotency 是关键防护）

---

## §6 当前状态

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`
- 分支：`main`，HEAD `f1ae7226b`
- 与 `origin/main`：一致
- 工作树：clean
- 未提交：无

---

## §7 下一会话行动项（优先级排序）

| 优先级 | 项 | 状态 | 预估 |
|---|---|---|---|
| P1 | §4.2 §5.3 outcome 分类方向决定 + 实现 | **Blocked**（需业务方向决策） | 半天 |
| P2 | §4.4 `success && response_body missing` 观测性设计 + 实现 | **Blocked**（需设计评审） | 1天 |
| P2 | 添加小 journal 测试用例（验证 Truncated=false） | 可执行 | 15分钟 |
| P2 | 实现 JournalSnapshot authorization layer | 可执行 | 半天 |
| P2 | 实现 JournalSnapshot idempotency (snapshot_version) | 可执行 | 半天 |
| P3 | Accept ADR 2026-08-28-requestjourney-journal-snapshot.md | 等待 4/6 完成 | 5分钟 |

**建议顺序:**
1. 快速添加小 journal 测试用例（补充当前实现的覆盖）
2. 实现 authorization layer（安全关键）
3. 实现 idempotency（幂等性保障）
4. Accept ADR（当 4/6 完成时）
5. 等待 §4.2 和 §4.4 的业务/设计决策

---

## §8 引用

- 上游 handoff：`.handoff/2026-08-29-section4-execution.md` §6
- ADR：`docs/adr/2026-08-28-requestjourney-journal-snapshot.md`
- 实现文件：
  - `domains/dispatch/observation.go:267-295` (常量 + 结构定义)
  - `domains/dispatch/pipeline.go:1523-1565` (截断逻辑)
  - `domains/dispatch/journal_snapshot_contract_test.go:94-158` (验证测试)
- 测试命令：`go test -v -run TestJournalSnapshot ./domains/dispatch/`
