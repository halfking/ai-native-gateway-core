# 131 号｜R89-AS：双重存储架构在「费用计帐」维度上**lite 侧是空的** —— 无台账等价物，且 `TurnDetails` 只写不读

- 日期：2026-10-01
- 轮次：R89-AS
- 起因：objective 明写的一条要求，本会话覆盖最弱 ——
  **「注意检查全量(pg+redis+memory+files)与本地简化模式(sqlite+memory+files)的双重存储架构设计的实现」**，
  且 objective 同时要求「内部费用计帐…可以细化到一个 apikey 或凭据或一个模型」。
- 结论先行：
  1. **lite 模式不写 `usage_ledger`，且这是设计声明不是疏漏**（包注释明写「不参与双写对账」）。
  2. **lite 侧把成本写进了 `TurnDetails.CostUSD`，但 `TurnsStore` 接口根本没有读 details 的方法** ⇒ **只写不读**。
  3. ⇒ **lite 模式下成本数据既没有聚合源、也没有读端**。这不是「功能弱」，是**结构上不存在**。
  4. ⚠️ 这与 130 号的结论叠加后，两种存储模式**都没有「数据完整性」这道闸**：
     full 模式是「台账写失败零信号」，lite 模式是「压根没有台账」。

---

## 一、lite 侧写了什么（`cmd/gateway/lite_telemetry_sink.go`）

`liteRequestLogSink.PersistRequestLog` 落四类数据：

| 落点 | 内容 | 门控 |
|---|---|---|
| SQLite `request_logs` | 请求日志行 | `storage.request_logs_write_enabled`（`:141`） |
| SQLite `sessions` | 会话行 | 无 |
| SQLite `session_turns`（meta） | 轮号/时间/token 数/压缩策略 | 无 |
| FileBodies | `turn_N.json.gz` 请求+响应原文 | 无 |
| SQLite `session_turn_details` | **模型/路由/质量/分类 + `CostUSD`** | 无 |

包注释（第 17-21 行）对**语义边界**写得很明确：

> 与 full 模式（PG sessionv2mirror 影子写）的语义差异：…
> **lite 面向单机审计与回放，不参与双写对账。**

⇒ **「lite 不写 `usage_ledger`」是明确的设计声明**，全文件内**无任何 `usage_ledger` 字样**。
（129/130 号量到的 `usage_ledger` 对 `request_logs` ≈1:1 的覆盖率，是 full 模式的性质。）

## 二、成本写进去了，但**没有任何读端**

`lite_telemetry_sink.go:231` 把成本写进特征层：

```go
if s.detailsWriter != nil {
    s.detailsWriter.WriteTurnDetails(ctx, &storage.TurnDetails{
        ..., CostUSD: floatPtrValue(entry.CostUSD), ...   // :231
    })
}
```

而 `storage/interfaces.go` 的 **`TurnsStore` 接口只有两个方法**：

```go
type TurnsStore interface {
    WriteTurnMeta(ctx context.Context, meta *TurnMeta) error
    GetTurnsMeta(ctx context.Context, tenantID, sessionID string) ([]*TurnMeta, error)
}
```

**没有 `GetTurnDetails` / `ListTurnsWithDetails` / 任何 details 读方法。**

复核过程（两次检索形态，因为「0 命中」是危险信号）：

1. `grep -rn "TurnDetails" ... | grep -iE "read|list|get|query|scan"` → **0 命中**；
2. 改读接口定义本身（`TurnsStore` 全量方法）+ 全仓 `TurnDetails` 出现点
   ⇒ 命中只有两类：**写入**（`lite_telemetry_sink.go:231`、`storage/sqlite/turns_store.go:158` 序列化）
   与 **PG 侧的** `domains/session/v2/details_writer.go`（属 full 模式的 733 details 家族，
   由 `cmd/gateway/session_v2_init.go:55` 装配）——**与 lite 读路径无关**。

⇒ **`session_turn_details` 在 lite 侧是一张只写不读的表。**
（`main_livestream.go:151/208` 里的 `CostUSD` 属于 full 模式的实时流推送，不是 lite 读端。）

## 三、并且 lite 侧没有任何 request-log 查询端点

同文件第 138-140 行已有一处 R78 订正记录：

> 原文此处写「日志读端走 session_logs_view」不成立——该视图**零生产消费者**，
> **lite 侧没有任何 request-log 查询端点**（`/api/lite/sessions` 只返回会话列表）。

⇒ 即便有人想查成本，**也没有端点可查**；`/api/lite/sessions` 只给会话列表。

## 四、结论：双重存储架构在「费用计帐」这一维度上，**lite 侧是空的**

objective 同一段里有两个要求：

- 「注意检查全量(pg+redis+memory+files)与本地简化模式(sqlite+memory+files)的**双重存储架构设计的实现**」
- 「检查内部费用计帐及与供应商进行对帐的功能…**可以细化到一个 apikey 或凭据或一个模型**」

**在 full 模式**，第二个要求有载体：`usage_ledger`（带 `tenant_id` / `api_key_id` /
`credential_id` / `provider_id` / `canonical_id`，天然可按这四个维度切），
读端是 `MonthlyUsageByProvider` 等对账/统计查询。

**在 lite 模式**：

| 能力 | full | lite |
|---|---|---|
| 成本落库 | `usage_ledger`（≈1:1 覆盖 request_logs，130 号实测） | **无台账等价物**（设计声明不参与对账） |
| 成本可按 apikey/凭据/模型聚合 | ✅ 列已具备 | **❌ 无聚合源** |
| 成本读端 | 对账/统计查询 | **❌ 无端点**（`/api/lite/sessions` 只给列表） |
| 逐轮成本 | — | `TurnDetails.CostUSD` **写了但无读方法** |

⇒ **不是「lite 的计帐能力弱于 full」，而是「lite 在计帐这个维度上没有任何读取路径」。**
单机会话审计与回放（lite 的定位）确实不需要它——**但这是 objective 明确要求的能力，
在 lite 部署下就是缺失的，且缺失方式对使用者不可见**（不报错、不降级、UI 上没有这一项）。

## 五、⚠️ 与 130 号叠加：两种模式**都没有「数据完整性」这道闸**

- **full 模式**（130 号）：台账写在自己的事务里，失败时**零信号**；
  「表里有行」与「表里该有的行都在」之间**没有任何对账机制**。
- **lite 模式**（本条）：**压根没有这张表**，也**没有读端**能发现任何缺失。

⇒ 两种模式下，「成本数据是否完整」都**无法从系统自身获得答案**。
⇒ 130 号建议的「对账前先断言覆盖前提」**在 lite 模式没有落点**——
**那里连一个可以对账的源都不存在**。

## 六、定性与建议（**未改动任何生产代码**）

| 项 | 定性 |
|---|---|
| lite 无台账等价物 | **设计声明**，非缺陷 ⇒ 不建议「补一个 lite 台账」（会推翻既有定位） |
| `TurnDetails` 只写不读 | **P2（新）**：`TurnsStore` 接口缺 details 读方法 ⇒ 已落盘的逐轮成本不可被读取 |
| lite 无成本查询端点 | **P2（新）**：与 objective 的「细化到 apikey/凭据/模型」直接冲突 |
| 「完整性无闸」跨两种模式 | **P2（新，结构性）**：登记为一条跨模式的不变式缺失 |

**建议方向（待裁决，不擅自动手）**：

1. **先确认产品意图**（这是前置，不该由我替产品决定）：
   **lite 模式是否需要成本可见？** 若不需要（维持「单机审计与回放」定位），
   则本条应**降级为「文档/UI 未声明能力边界」**——建议在 lite 的管理端**显式标注
   「本模式不提供费用统计」**，因为现在它是**静默缺失**，使用者会以为「没有数据=没花钱」。
2. 若需要，则最小实现是**给 `TurnsStore` 补一个 details 读方法 + 一个聚合查询端点**
   （数据已经在库里，不需要迁移、不需要新表）。
3. 无论选哪条，**130 号的「覆盖前提断言」都应同时覆盖 lite 侧**
   ——否则 lite 部署会静默地通过一个它根本无法满足的检查。

**本会话累计自我更正 8 处，本条不涉及更正。**
