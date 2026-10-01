# 146 号｜R89-BH：收掉 145 号的**待抽查**（前序零导入结论成立）—— 并发现 **webcookie 的 schema 与框架都已就位、唯独没有接线**

- 日期：2026-10-01
- 轮次：R89-BH
- 起因：145 号结尾登记了一条**自查债务**：
  「既然『零导入』的口径在别处可能也用过同样的错前缀，
  则 114–120 号基于『零导入』得出的 B/C/D 类划分需要抽查复核」。
  **本轮把它走完，并顺手做了一次全仓权威重算。**
- **结论先行**：
  1. **✅ 145 号的担心不成立，114–120 号的零导入结论全部成立。**
     **根因很清楚：那几轮用的是 `go list` 反向图，而 `go list` 解析真实 import 图、天然免疫前缀写错。**
     本轮用 `go list` 反向图独立重算，其点名包（`session/preprocess`、`nodestatecache`、
     `orchestration/observ`、`cachemetrics`、`requestarchive`、`smartretry`、`ursm/v2/index`…）
     **全部仍落在零生产引用集合内。**
  2. **权威清单（全仓 367 个包）**：**37 个「生产零引用的库包」** + **87 个入口/工具/测试包**
     （后者天然应为 0，不计入缺陷）。⚠️ 这 37 个里 **30 个已在台账逐包定性过**。
  3. **⚠️ 7 个库包从未在台账登记**。其中 **5 个是「自包含守卫」**，
     零导入是**正确形态**（逻辑与 `guard_test.go` 同包）⇒ §22「零导入是设计约束不是事故」的又一例。
  4. **⚠️ 本轮最实的一条：`domains/streaming/executors/webcookie`（486 行）零引用，
     而 `db/db.go:7070` 已经在生产 DDL 里建了 `webcookie_sessions` 表 + 索引 + 状态约束
     （**真库实测：该表存在，行数 = 0**）。**
     ⇒ **schema 在、框架在、唯独接线不在（且从未有过数据）。**
     **这正好落在待产品裁决的「免费池是否走浏览器逆向路线（`webcookie`）」上：
     基础设施已建到一半，缺的是最后那一步接线。**

---

## 一、待抽查项的结论：**前序零导入结论成立**

### 1.1 为什么 145 号的担心不成立

| | 145 号的做法 | 114–120 号的做法 |
|---|---|---|
| 工具 | `grep` import 字符串 | **`go list` 反向依赖图** |
| 前缀写错的后果 | **全表假 0** | **无影响**（`go list` 从语法树解析，不看字符串） |
| 是否受 §39 影响 | ✅ 受影响 | ❌ **免疫** |

### 1.2 独立复核（`go list` 反查每个点名包的导入者）

```bash
go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./... \
  | grep "llm-gateway-go/internal/<pkg>$"
# 命中的只有该包自身那一行 ⇒ 无任何导入者
```
抽验 `internal/partguard` / `internal/routeguard` / `internal/metricguard` / `internal/sqlguard` /
`internal/sqlreadguard`：**输出为空** ⇒ 零引用属实。

### 1.3 追加一层：**历史上从未被引用过**

```bash
git log -S"internal/metricguard\"" --all -- '*.go'   # 空
git log -S"internal/partguard\""  --all -- '*.go'    # 空
git log -S"internal/routeguard\"" --all -- '*.go'    # 空
```
⇒ **不是「曾经接线、后来被删」，而是从未接线。**

## 二、权威清单（`go list` 反向图，367 个包）

| 类别 | 数量 | 说明 |
|---|---|---|
| 库包（排除 `cmd/` `tests/` `scripts/` `tools/` `deploy/` 等入口） | 277 | — |
| **生产零引用的库包** | **37** | 114–120 号的 B/C/D 划分对象 |
| 入口 / 工具 / 测试包 | 87 | **天然应为 0**（`main` 包），不计入缺陷 |

**37 个里 30 个已在台账逐包定性**（`adapter/unified`、`cachemetrics`、`credentialquota`、`hooks/*`、
`nodestatecache`、`orchestration/observ`、`session/preprocess`、`ursm/v2/index`、
`ursm/v2/integration`、`agent/wsclient`、`capabilityscore`、`ctxpool`、`errdiscard`、`fsstore`、
`orchestration`、`probe`、`requestarchive`、`rowsguard`、`smartretry`、`summary`、`tenant`、
`promptinjection/enhanced`、`remotecontrol`、`webcookie`、`logsearch`、`api/webhooks`…）。

## 三、⚠️ 7 个从未登记的库包 —— 分三类，**只有第 3 类是真候选**

### 第 1 类：5 个自包含守卫（**零导入是正确形态**）

| 包 | 行数 | 编码的规则 | 目录构成 |
|---|---|---|---|
| `internal/metricguard` | 572 | **R77 守卫**：不允许任何 prometheus 指标处于「已声明但生产代码从不记录」 | `scan.go` + `guard_test.go` + `scan_self_test.go` |
| `internal/partguard` | 611 | 分区父表全集的硬编码守卫 | `parents.go` + `scan.go` + `guard_test.go` + `scan_self_test.go` |
| `internal/routeguard` | 310 | **R87-f 守卫**：路由资格判定**不得**依赖内存熔断器的观测镜像 | `routeguard.go` + `guard_test.go` + `scan_self_test.go` |
| `internal/sqlguard` | 171 | SQL 字面量里写 Go 风格 `//` 注释 | **仅** `sql_literal_guard_test.go` |
| `internal/sqlreadguard` | 387 | SQL 读类同类守卫 | **仅** `guard_test.go` |

**⚠️ 我本轮差点把这 5 个报成「守卫从未运行」—— 那是错的。**
它们的**扫描逻辑与断言测试在同一个包内**（`scan.go` 被同包的 `guard_test.go` 调用），
**所以「零导入」正是它们应有的形态**，靠 `go test ./...` 直接执行。
`sqlguard` / `sqlreadguard` 更彻底：**整个目录只有一个 `*_test.go`**，
包本身就是那条守卫。

⇒ **这是 playbook §22「零导入是设计约束不是事故」的又一例**，
也印证了本会话早已记下的一条：**「守卫的价值不在它跑不跑，而在它拦住过什么」。**

### 第 2 类：`db/dbxmanifest`（648 行）—— **自测齐备，非死代码**

目录：`manifests.go` / `tenant_model_policies.go` / `manifests_test.go` / `pilot_integration_test.go`
包注释自陈：「internal/dbx 框架的**宿主侧接线点**，承载项目专属约定，
Phase 3 pilot 从 `tenant_model_policies` 开始」。
⚠️ **本轮未逐行核实 `db/db.go` 里的相关提及是 import 还是注释**（`grep` 命中但未判定），
**如实登记为待核**，不报缺陷。

### 第 3 类：⚠️ **`domains/streaming/executors/webcookie`（486 行）—— 真候选，且有实锤上下文**

**包能力**（包注释自陈）：
> 「browser-reverse-engineered ("web-cookie") free LLM providers — e.g. deepseek-web、
> chatgpt-web、gemini-web。这些供应商在浏览器里有免费聊天 UI 但没有官方免费 API；
> OmniRoute 通过逆向浏览器的 …」

**目录**：`base.go`（框架）+ `deepseek_web.go`（实现）+ `base_test.go`

**实锤**：**`db/db.go:7070` 起已经在生产 DDL 里建了这张表**：
```go
// ensureWebCookieSessionsSchema mirrors sql/migrations/077-webcookie-sessions.sql.
CREATE TABLE IF NOT EXISTS public.webcookie_sessions (
  ...
  CONSTRAINT webcookie_sessions_status_chk CHECK (status IN ('active','expired','banned','refreshing'))
);
CREATE INDEX IF NOT EXISTS idx_webcookie_sessions_provider ...
```

⇒ **三点同时成立**：
1. **表在生产 schema 里**（且有状态机约束：active/expired/banned/refreshing）；
2. **读写框架在仓里**（486 行，含 `deepseek_web.go` 具体实现）；
3. **没有任何生产代码 import 这个包。**

⚠️ **这正是 §13/§14 的第四种组合**：
前几种是「代码承诺了但没接线」/「能力缺失」/「实现了但漏接线」；
**这一种是「schema + 框架都就位，只差最后一步注入」** ——
**基础设施成本已经付掉了，缺的是把它接进 dispatch 链的那一处。**

⇒ **它直接落在待产品裁决的「免费池是否走浏览器逆向路线（`webcookie`，含 TLS 指纹伪装，合规前提）」上。**
**⇒ 建议先答产品问题，再决定接线还是连同表一起清理** ——
**在产品没定调之前不建议动**：删框架会留下孤立表，接框架则要先有合规结论。
**本代理不擅自动手。**

## 四、playbook §40 新增

> **判定「零引用 / 未使用」之前，先看目录构成；同一份工具输出只写一次解析。**
>
> **形态 A（本轮三次差点发错结论）**：一个目录里同时有 `scan.go` 与 `guard_test.go`，
> 而 `scan.go` 只被**同包的** `guard_test.go` 调用 ⇒
> **「零 import」恰恰是正确的形态**（守卫靠 `go test ./...` 执行，不靠被 import）。
> **本轮我在这个点上错了三次**：
> ① 差点把 5 个自包含守卫报成「守卫从未运行」；
> ② 差点把「仅测试引用」的守卫包报成未使用（`go list .Imports` **不含** `TestImports`）。
>
> **形态 B**：**同一份 `go list` 输出，我连写了三个解析变体**，
> 分别按空格 / `|` 分隔、按不同字段取，得到**三个互相矛盾**的清单
> （一次说 37 个、一次说 1 个、一次说 277 个）。
> ⇒ **纪律**：
> 1. **先固定输入文件与分隔符，再写一次解析，写完立刻用一个已知答案的包做 sanity check**；
> 2. **`len .Imports` 是「我引入了谁」，不是「谁引入了我」** ——
>    **方向搞反会得到一份「所有不依赖任何人的包」的荒谬清单**（本轮就出现过）；
> 3. **判定未使用要同时看三层**：生产 import / `TestImports` / `XTestImports`，
>    **再看一眼目录里有没有 `*_test.go` 自测**。
>
> **与 §39 的关系**：§39 是「检索形态坏了却输出整齐结论」；
> **本条是「同一份数据被三个坏解析读出三个矛盾结论」** ——
> **共同点：都产出干净、自洽、可信的清单，而它们都是错的。**
> **判据：一个分析结果若与同一份数据的另一次分析矛盾，先怀疑解析，不要先怀疑数据。**

**同族**：§22（零导入是设计约束）/ §31 / §35 / §36 / §37 / §39。

---

## 五、诚实边界

- **未改动任何生产代码、前端代码或数据库**（全部只读）。
- **`db/dbxmanifest` 与 `db/db.go` 的关系未判定**（import 还是注释）—— **如实登记为待核**。
- **`webcookie` 的「该接线」是本轮基于「表 + 框架 + 零引用」三点的推断**；
  **✅ 已补验：`webcookie_sessions` 表存在、行数 0 ⇒「从未接线」成立。**
- **37 个库包的行数分布未逐个统计**（只对 7 个未登记的做了）。
