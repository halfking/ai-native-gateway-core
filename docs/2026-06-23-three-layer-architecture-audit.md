# 三层架构审计与修正 (2026-06-23)

## 📋 审计发现的关键问题

### 问题 1: 概念混淆 (concurrency_limit 被当作 fp_slot_limit)

**症状**: 代码中将 `concurrency_limit` 同时用作：
- Limiter 包的并发控制参数（in-flight 请求数）
- credentialfpslot 包的指纹池大小（distinct virtual identities）

这两个概念**完全不同**：
- 并发限制：瞬时占用，请求结束立即释放
- 指纹池：长期持有，24小时身份稳定

**根因**: `credentialfpslot.EffectiveLimit()` 接受 `concurrency_limit` 参数，
将其映射为指纹池大小，导致两个概念被错误地等同。

**修复**:
- 数据库新增 `credentials.fp_slot_limit` 列（独立于 `concurrency_limit`）
- 新增 `credentialfpslot.EffectiveFpSlotLimit()` 函数（接受 fp_slot_limit）
- 保留旧的 `EffectiveLimit()` 但标记为 deprecated
- 修复 `routing/executor.go` 中所有把 `cand.ConcurrencyLimit` 传给 `FpSlots` 的调用

### 问题 2: 全局终端用户总量控制缺失

**症状**: 用户提到"超过量的用户可以复用之前的用户的指纹"，但代码中没有实现。

**根因**: 现有架构只有两个层级（每凭据 fp_slot_limit + 每凭据 concurrency_limit），
没有全局的用户身份池限制。

**修复**:
- 新增 `identitypool` 包（Layer 0 全局身份池）
- 数据库新增 `system_identity_pool` 单行表（`max_identities` 配置）
- Lua 脚本原子实现：
  - 新用户：分配新身份（INCR counter，未超限）
  - 已存在用户：刷新 TTL
  - 容量满：LRU 回收最久未用的身份（"假装"成旧用户）
- 新增 admin API `/api/admin/identity-pool/{stats,max}`

### 问题 3: 数据库 schema 与代码不同步

**症状**: `credentials` 表没有 `fp_slot_limit` 列，但 Go 结构体中有 `FpSlotLimit` 字段，
且 `EffectiveLimit` 错误地使用 `concurrency_limit` 填充这个字段。

**修复**:
- 新增 migration `db/migrations/036_fp_slot_limit.sql`
- `credentials.fp_slot_limit INT NOT NULL DEFAULT 5`
- 添加 CHECK 约束防止非法值
- 添加 `system_identity_pool` 单例表

## 🏗️ 三层架构（修正后）

```
┌─────────────────────────────────────────────────────────────┐
│  Layer 0: Global Identity Pool (identitypool package)        │
│  ─────────────────────────────────────────────────────────  │
│  Scope: GLOBAL (跨所有 provider/credential)                   │
│  Controls: 总的 distinct end-user fingerprint 数量          │
│  Cap: system_identity_pool.max_identities (default 10000)   │
│  Overflow: LRU recycle (新用户复用旧用户身份)                │
│  Lifetime: 24h LRU window                                    │
└─────────────────────────────────────────────────────────────┘
                              ↓ 每个请求在 Layer 0 获得一个身份
┌─────────────────────────────────────────────────────────────┐
│  Layer 1: Per-Credential Fingerprint Pool                    │
│  (credentialfpslot package)                                  │
│  ─────────────────────────────────────────────────────────  │
│  Scope: 每个 credential 独立                                  │
│  Controls: 该凭据能模拟多少个 distinct virtual user          │
│  Cap: credentials.fp_slot_limit (default 5)                  │
│  Overflow: 路由到其他凭据或返回饱和错误                      │
│  Lifetime: 24h slot TTL + session pin                        │
└─────────────────────────────────────────────────────────────┘
                              ↓ 请求被路由到某个凭据
┌─────────────────────────────────────────────────────────────┐
│  Layer 2: Per-Credential Concurrency Limit                    │
│  (limiter package)                                            │
│  ─────────────────────────────────────────────────────────  │
│  Scope: 每个 credential 独立                                  │
│  Controls: 该凭据同时能处理多少个 in-flight 请求            │
│  Cap: credentials.concurrency_limit (default 10)             │
│  Overflow: 路由到其他凭据                                    │
│  Lifetime: 请求期间 (request → response)                     │
└─────────────────────────────────────────────────────────────┘
```

## 📊 关键场景示例

### 场景 A: 用户首次接入
1. Layer 0: 新指纹 → 分配新身份（INCR counter from 9999→10000）
2. Layer 1: 路由到凭据 X → 在凭据 X 占用 slot 0
3. Layer 2: 凭据 X 的 Limiter 占用 permit
4. 请求结束 → Layer 2 释放 permit；Layer 1 保留 slot 0；Layer 0 保留身份

### 场景 B: 同一用户再次接入（24h 内）
1. Layer 0: 已知身份 → 刷新 TTL（counter 不增）
2. Layer 1: 路由到凭据 X → 复用 slot 0（pin 机制）
3. Layer 2: 占用 permit → 请求结束 → 释放

### 场景 C: 超过全局 cap 的新用户（counter == max）
1. Layer 0: 新指纹 → 容量满 → LRU 回收最久未用的旧身份
   - 新用户被分配**旧身份字符串**，下游看到的就是旧用户
2. Layer 1: 用旧身份对应的凭据 slot
3. Layer 2: 占用 permit

### 场景 D: 凭据 fp_slot_limit 饱和
1. Layer 0: 分配身份 OK
2. Layer 1: 该凭据所有 slot 都被占用 → 路由失败 → 尝试其他凭据
3. Layer 2: 在选中的备用凭据占用 permit

## 🔧 修改清单

### 新增文件

1. **`identitypool/pool.go`** — 全局身份池（Layer 0）
2. **`identitypool/pool_test.go`** — 7 个测试用例
3. **`admin/identity_pool.go`** — admin API endpoints
4. **`db/migrations/036_fp_slot_limit.sql`** — DB schema 变更

### 修改文件

1. **`credentialfpslot/slot.go`**:
   - 新增 `EffectiveFpSlotLimit()` 函数
   - 保留 `EffectiveLimit()` 但标记为 deprecated
2. **`routing/executor.go`**:
   - `cand.ConcurrencyLimit` → `cand.FpSlotLimit`（line 589, 626）
3. **`provider/client.go`**:
   - `Candidate` 结构体新增 `FpSlotLimit *int` 字段
   - SELECT 子句新增 `c.fp_slot_limit`
   - Scan 调用新增 `&cand.FpSlotLimit`
4. **`admin/provider_credential.go`**:
   - `listCredentials` SELECT 新增 `c.fp_slot_limit`
   - `addCredential` 接受 `fp_slot_limit` 参数
   - `updateCredential` 支持 `fp_slot_limit` 字段
   - `resetCredentialFpSlots` 使用 `fp_slot_limit`
   - `getCredentialFpSlotStats` 使用 `fp_slot_limit`
5. **`admin/handler.go`**:
   - 新增 `identityPool` 字段
   - 注册路由 `/api/admin/identity-pool/{stats,max}`
6. **`credentialfpslot/slot_test.go`**:
   - 修复 `TestAcquireReleaseMemory`（反映长期占用语义）
   - 修复 `TestRelease_AllowsMigration_WhenContended` → 改名为 `TestRelease_LongTermOccupancy_PreventsSteal`
7. **`credentialfpslot/slot_concurrent_test.go`**:
   - 修复 `TestAcquireReleaseMemory_Concurrent`
   - 修复 `TestAcquireReleaseRedis_Mock`

## 🧪 测试结果

```
=== identitypool ===
PASS: TestPool_Disabled
PASS: TestPool_AcquireMemory_BelowCap
PASS: TestPool_AcquireMemory_CapReached_Recycle
PASS: TestPool_AcquireMemory_RepeatUser
PASS: TestPool_AcquireMemory_Stats
PASS: TestPool_AcquireMemory_LRUEviction
PASS: TestHashIdentity_Stable

=== credentialfpslot ===
PASS: TestEffectiveLimit
PASS: TestAcquireReleaseMemory (updated semantics)
PASS: TestRoutingEligible
PASS: TestRelease_KeepsPin_ForNextAcquire
PASS: TestRelease_LongTermOccupancy_PreventsSteal (renamed)
PASS: TestForceUnpin_RemovesPin_ForNewAcquire
PASS: TestAcquire_Sticky_AcrossReleases
... all 30+ tests pass
```

## 🚀 部署步骤

1. 应用 DB migration（自动，pod 启动时执行）：
   ```bash
   kubectl exec -n pms-test deploy/llm-gateway-go-deployment -- \
     psql -U stockuser -d llm_gateway -f /app/migrations/036_fp_slot_limit.sql
   ```
2. 部署到 184：`./scripts/deploy-llm-gateway-go-184.sh`
3. 部署到 71：`./scripts/deploy-llm-gateway-go-71.sh`
4. 配置全局身份池上限：
   ```bash
   curl -X POST https://llmgo.kxpms.cn/api/admin/identity-pool/max \
     -H "Authorization: Bearer <token>" \
     -d '{"max_identities": 10000}'
   ```
5. 监控：
   ```bash
   curl https://llmgo.kxpms.cn/api/admin/identity-pool/stats
   ```

## 📚 相关文档

- [`identitypool/pool.go`](../identitypool/pool.go) — 三层架构注释
- [`credentialfpslot/slot.go`](../credentialfpslot/slot.go) — Layer 1 实现
- [`limiter/limiter.go`](../limiter/limiter.go) — Layer 2 实现
- [`db/migrations/036_fp_slot_limit.sql`](../db/migrations/036_fp_slot_limit.sql) — schema 变更
- [`admin/identity_pool.go`](../admin/identity_pool.go) — 管理 API

## ⚠️ 重要约束

1. **`fp_slot_limit` ≠ `concurrency_limit`**：永远不要混用这两个值
2. **三个层级独立失败**：每一层都有独立的容量和饱和行为
3. **LRU 回收的影响**：被回收的用户会"看起来"是其他用户，下游可能看到不同的行为模式
4. **admin 操作谨慎**：`setIdentityPoolMax` 提高上限会增加上游被封号的风险

---

**审计时间**: 2026-06-23
**修复人员**: AI Agent
**审核状态**: 已修复，等待部署验证