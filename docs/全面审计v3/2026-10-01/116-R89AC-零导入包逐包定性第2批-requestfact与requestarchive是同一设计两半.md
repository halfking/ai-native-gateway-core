# 116 号｜R89-AC：零导入包逐包定性（第 2 批，4 个）—— **`requestfact` + `requestarchive` 是同一设计的两半，1,691 行整套从未落地**

- 日期：2026-10-01
- 轮次：R89-AC
- 起点：115 号末的「剩余 25 个待定性」，本轮做 4 个
- 纪律：逐包读原文 + 按 §14 先问现役等价物
- 零生产代码、零配置、零门

---

## 一、本轮定性

| 包 | LOC | 定性 | 关键点 |
|---|---|---|---|
| `internal/requestarchive` | 758 | **B** | **与 115 号的 `requestfact` 是同一设计的两半** |
| `domains/credentialquota` | 782 | **B** | 凭据级配额/限流，**连影子模式都没跑过** |
| `domains/hooks/promptoptimization` | 789 | **B** | 提示词优化 Hook + TTL 缓存 |
| `domains/hooks/memoraauto` | 700 | **B** | 空闲会话自动沉淀（**不是 C 类**：README 无「等集成者」表述、无 kill-switch） |

---

## 二、本轮最重的一条：`requestfact` 与 `requestarchive` 是同一套设计

`internal/requestarchive/archive.go` 的包注释**自己写明了所有权边界**：

> **Ownership boundaries (why this package exists beside requestfact):**
> `requestfact` owns the versioned wire contract and codec;
> **this package owns local lifecycle state** — atomic file placement,
> stage transitions, crash recovery, and quarantine.
> The package **never talks to the database** (the caller performs the persist
> and only then advances the archive), **never encrypts**, and **never touches the network**.

**两半的职责**：

| 半 | LOC | 负责 | 测试覆盖（读到的） |
|---|---|---|---|
| `internal/requestfact`（115 号） | 933 | 版本化 wire 契约 + codec + payload hash 防篡改 | golden roundtrip corpus、篡改拒绝、v1 兼容 |
| `internal/requestarchive`（本轮） | 758 | 本地生命周期：原子落盘、阶段推进、**崩溃恢复**、**隔离区** | 崩溃残留 tmp 恢复、**不可读文档隔离**、**崩溃窗口重复解析**、确认失败保留可重试、并发写者 |

**⇒ 合计 1,691 行，两半都零引用，整套「进程重启后可恢复的请求事实归档」从未落地。**

**这不是「多了一套没人用的代码」**，而是：
115 号已确认「`request_logs → session_turns` 迁移设计的**可验证产物格式契约**从未被任何生产代码使用」；
本轮补上另一半——**配套的崩溃恢复与幂等确认机制同样从未存在**。
两者合起来说明：那套设计**整体停在设计阶段**，不是「做完了没接」。

**顺带一条安全相关的观察**：`requestarchive` 的注释明写
「**never encrypts（encryption belongs to the durable_llm_tasks domain）」**
——即请求事实的**加密职责被显式委派给 `durable_llm_tasks`**。
若将来接线，必须**先确认 `durable_llm_tasks` 的加密真的覆盖归档文件**，
否则会把未加密的请求体（含 prompt 原文、可能的敏感信息）落到本地磁盘。
**这是接线的前置条件，不是现役缺陷。**

---

## 三、`domains/credentialquota`（782 LOC）—— 测得很扎实，但**连影子模式都没跑过**

包内：`metrics.go`（prometheus `credential_client_quota_acquire_total` /
`release_total`）、`postgres.go` + `redis.go` 双实现、`resolver.go`（策略热重载）、
`normalize.go`、`service.go`、`types.go`。

**测试把几个关键语义钉得很死**（读到的用例名即证据）：

- `TestEnforceAcquireLimitRenewAndIdempotentRelease` —— acquire/release + **释放幂等**
- `TestLeaseExpiresAfterFiveMinutesUsingRedisClock` —— 租约 5 分钟，**用 Redis 时钟**（避免多机时钟漂移）
- **`TestRedisFailureIsDistinctAndAcquireFailsOpen`** —— Redis 故障被**单独分类**且 acquire **fail-open**
- `TestShadowAllowsButStillRecords` —— **影子模式放行但仍记账**
- `TestUnlimitedOrMissingPolicy` —— 策略缺失/无限制
- `TestResolverReloadAndNormalize` —— 策略热重载

`types.go:13/56` 确有 `ModeShadow` 与 `OutcomeShadow`。

**⇒ 判读**：这是一套**为凭据级并发/配额限制准备的、设计相当完整的组件**，
直接对应 objective 的「多层队列的处理，解决前端的并发与出口 llm 的限流与并发的处理」。
**外部引用 = 0** ⇒ **凭据级配额从未生效**。
且因为**影子模式也从未跑过**，**生产上没有任何数据能告诉我们「这套配额策略上线后会怎么限流」**——
即：即使现在决定接线，也**没有影子期数据可参考**，必须先跑影子模式。

**⇒ 建议（属待裁决范畴，本代理不动手）**：若确定要凭据级限流，
**先单独装配影子模式**（`ModeShadow` 已实现）跑一段观察期，再切 enforce。

---

## 四、另两个

### 4.1 `domains/hooks/promptoptimization`（789 LOC）→ B

`OptimizationCache`（TTL + `maxSize` LRU）、`CacheKey(model, mode, prompts)` =
`model + mode + prompts 拼接后 sha256`、`Mode` 枚举、`Config`/`DefaultConfig`/`FromEnv`
（**配置走环境变量**）、`hook.go` + `integration_test.go`。
零引用 ⇒ 从未装配。配置外置 + 缓存键设计合理，接线成本低，但**无 objective 直接对应项**
（objective 提到「优化流程的可观测性」，未点名提示词优化）。

### 4.2 `domains/hooks/memoraauto`（700 LOC）→ **B，不是 C**

有 `README.md` + `config.example.yaml`，但读 README 后确认**不属 108 号定义的 C 类**：
README 通篇是**功能说明**（✅ 列表 + 架构图 + 组件树），
**没有「等集成者 / 不接生产管线」的表述，也没有 kill-switch**。
⇒ 按 108 号的 C 类三要件（有设计文档明写等集成者 + 有 kill-switch + 前置未完成），
**这里只满足「有文档」，另两条不满足** ⇒ 判 **B 类**。

功能：空闲会话检测（>1h 且请求数 ≥3）→ PhasePostResponse 异步沉淀到 kxmemory，
指数退避重试最多 3 次。零引用 ⇒ 从未装配。

---

## 五、本轮累计（两批共 8 个）

| 批次 | 包 | 结论 |
|---|---|---|
| 115 号 | `promptinjection/enhanced`、`requestfact`、`adapter/unified`、`fsstore` | 4 × B |
| 116 号 | `requestarchive`、`credentialquota`、`promptoptimization`、`memoraauto` | 4 × B |

**8/8 是 B 类。** 已出现两个「整套设计从未落地」的组合：
- **`requestfact` + `requestarchive` = 1,691 行**（格式契约 + 崩溃恢复归档）
- `promptinjection` 的 **enhanced 分支**（增强检测从未被选用）

**剩余 21 个待定性。**

**但 8/8 同型仍不构成批量跳过的理由**——115 号已记录：
107/108/113/114 号四次证明**预期不能替代读原文**。

---

## 六、编号与去向

- **无新增待裁决条目**；4 个归 B 类并登记「需确认是否接线」。
- **新登记的前置条件**：`requestarchive` 接线前必须确认 `durable_llm_tasks`
  的加密覆盖归档文件（注释已把加密职责显式委派出去）。
- **新登记的建议**：`credentialquota` 若要接线，**先跑影子模式**（已实现 `ModeShadow`），
  因为从未跑过 ⇒ 无任何生产数据可参考限流效果。
- 剩余 **21 个**待定性。
