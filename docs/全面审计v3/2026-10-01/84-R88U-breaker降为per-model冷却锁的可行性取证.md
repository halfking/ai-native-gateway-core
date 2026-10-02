# 84-R88-u：breaker 降为 per-model 冷却锁的可行性取证（待裁决 30）

- 轮次：R88-u（R88 收尾项）
- HEAD 基线：`00a2e0b5b`
- 触发：待裁决 30「breaker 是否降为 per-model 冷却锁」——9router 跨项目借鉴中**唯一通过适用性检验**的推荐
- 本轮定位：把「唯一成立的推荐」从「一条结论」做成「带数字与约束的决策包」
- 结论：**建议做，但必须走方案 A（内存 per-model + DB 上卷汇总），不能直接改**。直接改会与 `credentials.circuit_state` 的 schema 语义脱节
- 改动：**零生产代码、零配置、零门**。仅本报告 + README 索引

---

## 1. 它到底解决什么问题

当前熔断器按 **(provider, credential)** 键（`domains/credential/breaker.go:215`）：

```go
key: fmt.Sprintf("%d/%d", providerID, credentialID),
```

⇒ **凭据上任意一个模型出问题，整个凭据被冷却，该凭据上所有模型一起被挡。**

这不是理论上的担心，误伤半径可以量出来（§2）。

---

## 2. 误伤半径（真库实测，2026-10-01）

模型名来自目录表 `public.model_offers` 的 `COALESCE(outbound_model_name, raw_model_name)`，**不是客户端原始输入**（`provider.Candidate.RawModel` 注释自陈 `mo.raw_model_name`）。

| 量 | 值 |
|---|---|
| `model_offers` 总行数 | 1994 |
| distinct 凭据 | 83 |
| distinct 生效模型名 | 974 |
| **(credential_id, 模型名) 组合** = 改造后 key 数 | **1991** |
| 对照：当前 (provider, credential) key 数 | **86** |
| **平均每凭据绑定模型数** | **23.99** |
| **单凭据最多绑定模型数** | **419** |
| 绑定 ≥2 个模型的凭据 | **75 / 83** |

⇒ **凭据上单个模型故障 ⇒ 平均 24 个模型、最多 419 个模型被一起挡。**

### 2.1 但影响面未实测（诚实登记）

同一时点 `credentials.circuit_state` 分布：

```
closed = 83
(null) = 3
open   = 0
```

⇒ **当前没有任何凭据处于冷却中**。所以：

- **结构性误伤成立**（半径 24×，机制清楚）；
- **「它已经在生产造成过损害」没有证据**。

按 R88-r 的同一纪律（可达性 ≠ 影响面），本报告**不**把它写成「正在丢流量」。真实发生频次需要 per-model 的失败归因数据，而当前 breaker 本身就不记录 model，**从现有数据里取不到**。

---

## 3. 改造面（可机械完成，不是拦路虎）

14 个生产调用点，分布在 4 个文件：

| 文件 | 点数 | model 来源 |
|---|---|---|
| `domains/streaming/executors/context_summarize.go` | 9 | `cand.RawModel` |
| `domains/streaming/executors/executor_dispatch.go` | 3 | `cand`（`:810` `Allow` 是**主 dispatch 热路径**） |
| `domains/streaming/executors/executor_nodehealth.go` | 2 | `decision.Node.Model` |
| `executor_ollama/anthropic/chat.go` → `recordProtocolCircuitSuccess` | 传递点 | `cand.RawModel` |

**我特意验证了「model 是不是拿不到」**——这是最可能的否决点，结论是**全部可得**：

- `executor_nodehealth.go:205` 同一行块里已经在用 `decision.Node.Model`（`:206`）；
- `recordProtocolCircuitSuccess(params, providerID, credentialID)` 的 7 个调用点**全部在 `cand` 作用域内**（如 `executor_ollama.go:1284`），`cand.RawModel` 直接可用。

⇒ 加一个 `model string` 参数是**机械改动**，不涉及「某些站点拿不到 model」这类难办情况。

**爆炸半径确认是真实的**，不是只在 compaction 上：`executor_dispatch.go:810` 的 `Allow` 在主 dispatch 路径上。

---

## 4. 真正的约束：DB 侧 schema 表达不了 per-model

这是本轮最有价值的发现，也是**「不能直接改」的根据**。

`domains/credential/state_sync.go` 把熔断迁移异步镜像进 **`credentials.circuit_state` 这一列**（`:136-155`）：

```sql
UPDATE credentials
SET circuit_state = $1, circuit_opened_at = $2, cooling_until = $3
WHERE id = $4            -- $4 = sc.CredentialID
```

⇒ **列长在凭据行上，主键维度只有 credential**。schema 层面**无法表达 per-model 状态**。

而这一列的真实消费面（我逐个回原处核实，**不采信注释**）：

| 消费面 | 位置 | 核实 |
|---|---|---|
| admin 凭据详情/诊断接口 | `cmd/gateway/main_v32_wiring.go:100` | ✅ 代码实读 `COALESCE(c.circuit_state,'closed') AS circuit_state` |
| live-stream 节点面板 | `cmd/gateway/main_livestream.go:503` | ✅ 代码实读 |
| route incident DTO | `domains/routeincident/types.go:153` | ✅ 字段存在 |
| ~~`routing_health_checks` 的 `circuit_open` 检查~~ | `state_sync.go:25` **注释所述** | ❌ **不存在** —— 见下 |

（`Manager.Stats()` 本身只在 `breaker_test.go:371` 用，**未经 HTTP 暴露** ⇒ 不存在「无界响应」风险。这一点我不夸大。）

⇒ **如果只把内存里的 breaker 改成 per-model，DB 这一列会静默变成「某个模型坏了但凭据看起来是好的」**，上面三个消费面全部读到错误的健康度。**这比不改更糟**——它把一个显式的误伤换成了一个隐性的错误读数。

### 4.1 【新发现 P3】`state_sync.go:25` 注释描述了一个不存在的消费面

我本来照该注释把消费面记成「四个」，落笔前核实发现第四个**根本不存在**：

- **schema 层**：`information_schema.columns` 查 `routing_health_checks` 中含 `circuit`/`open` 的列 —— **0 命中**，该表没有 `circuit_open` 列；
- **代码层**：`admin/health_check_handlers.go` 对该表的全部读取只涉及 `status` / `entity_type` / `entity_id` —— **没有任何一处读熔断相关列**；
- 唯一出现 `circuit_open` 字样的地方是**注释**（`state_sync.go:25`、`main.go:2071`）与 `domains/routeincident/observer.go:252` 的一个**故障原因标签表**（`"circuit_open": {}`，是 reason 枚举值，不是查询）。

⇒ 这与本会话已登记的同型问题一致：`live-stream-record-dropped.yaml` 幽灵路径（R87-m）、以及「代码说自己被谁调用，grep 说没有」（R87-j 的 `DeleteOlderThan`）。**注释描述的是一个已消失或从未存在的消费面。**

**处理**：按本会话既定纪律「**不回改历史**」——该注释记录的是三十七轮审计**当时**的发现，改写它等于伪造历史。**本轮只在报告里登记现状，不动代码注释。** 建议后续单独立项订正（把「三个消费面」改为「两个 + 一个 route incident DTO」，并说明 `routing_health_checks` 从无熔断检查）。

**对本次决策的影响**：消费面是 **3 个而非 4 个**，方案 A 的迁移成本判断不变（仍是零迁移），但**爆炸半径的估算应按 3 个面计**。

---

## 5. 两个自洽方案

### 方案 A（**本轮推荐**）：内存 per-model + DB 上卷汇总，**无迁移**

- 内存：breaker 键改为 `(credentialID, model)`，`key` 格式随之改；
- DB：**列不变、语义明确化**——`circuit_state` 表示「**该凭据的任一模型处于开路**」的上卷值。即任一 `(credential, model)` 开路 → 写 `open`；全部关闭 → 写 `closed`；
- **三个**消费面（§4，不是四个）**语义不变**（凭据级健康度本来就是它们要的粒度），**零迁移**；
- 代价：需要定义「上卷」在 `HALF_OPEN`/`QUARANTINED` 下如何取值（建议：`open` 优先，其次 `half_open`，全闭才 `closed`）；需要一个能回答「该凭据是否还有任一模型开着」的查询面（内存里扫该凭据的 model 键即可，1991 个键的量级完全可接受）。

### 方案 B：完整 per-model 持久化

- 新表（如 `credential_model_circuit_state`，键 `(credential_id, model)`）+ 迁移；
- **三个**消费面改为聚合查询；
- 代价大得多，且**消费面语义要重新裁决**（admin 详情页现在展示的是凭据级信号，改成 per-model 是产品可见变化）。

⇒ **先 A 后 B**：A 解决热路径误伤（真实痛点），B 只在确实需要 per-model 持久可观测性时才做。

---

## 6. 待裁决 30 的状态更新

| 项 | 现状 | 方案 A 之后 |
|---|---|---|
| 熔断键 | (provider, credential) | (credential, model) |
| 内存 key 数（真库实测） | 86 | 1991（≈23×，**约 0.5MB，不是问题**） |
| 误伤半径 | 平均 24 个模型 / 最多 419 | **1 个模型** |
| `credentials.circuit_state` | 凭据级 | 凭据级（**语义明确为上卷**），无迁移 |
| 生产调用点改动 | — | 14 处 + 5 个 Manager 方法签名，全为机械改动 |
| **实际影响证据** | **无（当前 0 open）** | 同左；改造后应能首次观测到 per-model 归因 |

### 6.1 我修正了原先的成本判断

R88-g 当时把代价记为「键空间膨胀 + 清理责任」。**实测后这条基本不成立**：

- **膨胀可忽略**：86 → 1991 个 Breaker，约 0.5MB；
- **不是无界的**：`Manager.GetOrCreate`（`breaker.go:743`）确实是**只增不删**的 map（无 TTL、无淘汰），但模型名来自目录表 `model_offers`（1994 行）⇒ **基数由管理员的目录维护驱动，不是客户端输入驱动**。这与 `credential_model_index` 月分区的既有基数是同一量级，属正常增长。

⇒ 真正的成本不在内存，在 §4 的 **DB 语义一致性**。

---

## 7. 明确未做

- **未改任何生产代码**（本项是裁决项，且涉及 DB 列语义）；
- **未量化误伤的历史发生频次**——当前数据取不到（breaker 不记 model，见 §2.1）；
- **未复核 9router 侧 per-model 锁的确切实现**——该结论来自 R88-g，本轮未重读参照项目（若要落地应重读，确认语义对齐而非仅模式相似）；
- **未设计 `HALF_OPEN`/`QUARANTINED` 的上卷取值细则**（§5 给了建议，未落成代码级规则）；
- **未验证 `state_sync.go:25` 注释所述的第四个消费面** ⇒ 已证**不存在**（§4.1），并按「不回改历史」登记为新 P3；
- **未验证三个真实消费面对「上卷语义」的实际容忍度**（例如 admin 页面是否会因语义变化产生误读）。

---

## 8. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/84-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |

**零生产代码、零配置、零门、零 CI 行为变化。**

---

## 9. 交叉引用

- 74 号 R88-g：9router 三条推荐的适用性检验（本报告是那条唯一幸存推荐的深化）
- `domains/credential/breaker.go`、`state_sync.go` — 熔断本体与跨进程状态镜像
- 82 号 §4：同型判据「跨函数/跨层看不见的结构」，本报告 §4 是「跨层 schema 表达力」的版本
