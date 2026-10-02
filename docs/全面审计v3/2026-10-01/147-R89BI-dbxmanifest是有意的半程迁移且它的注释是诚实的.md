# 147 号｜R89-BI：收掉 146 号的待核项 —— `db/dbxmanifest` 的定性是**有意的半程迁移，且它的注释是诚实的**（与 145 号形成鲜明对照）

- 日期：2026-10-01
- 轮次：R89-BI
- 起因：146 号结尾把 `db/dbxmanifest` 与 `db/db.go` 的关系登记为**待核**（`grep` 命中过，
  但未判定是 import 还是注释）。
- **结论先行**：
  1. **待核项关闭：`db/db.go` 里根本没有 `dbxmanifest` 的提及** ——
     146 号那条 grep 命中的是同一行里的 **`webcookie`**（`db/db.go:7070` 的
     `ensureWebCookieSessionsSchema`），**我把它错认成了 dbxmanifest**。
     ⚠️ **全仓无任何文件 import `db/dbxmanifest`** ⇒ **它确实零生产引用。**
  2. **⚠️ 但它不是缺陷，而是「有意的半程迁移 + 诚实的文档」。**
     包注释**自己就写明了**：
     > *「**No production data path routes through the framework yet**: the registry is
     > consumed by shadow reads and the metadata gate, while the **legacy repositories
     > (e.g. `admin/model_policies.go`) stay authoritative**。」*
     **并点名了继任者是谁。**
  3. **配套事实链完整**：表 `tenant_model_policies` **存在且有 3 行**（迁移已执行到「建表+播种」）；
     框架本体 `internal/dbx` **在生产 import 图上**（`cmd/gateway/main.go`、
     `admin/data_lifecycle_storage.go`）—— **只是它上层的 manifest 层还没接。**
     ⇒ **「框架在、manifest 层未接、权威仍是 legacy 仓库」—— 这与注释的自我描述逐字吻合。**
  4. **⚠️ 本轮真正的洞察：同一仓里存在两种相反的注释风格，而本会话前几轮只记住了坏的那种。**
     | | `domains/hooks/promptoptimization`（145 号） | `db/dbxmanifest`（本轮） |
     |---|---|---|
     | 文档说什么 | 「设 `PROMPT_OPTIMIZATION_ENABLED=1` 即可启用」 | 「**目前没有生产数据路径走这个框架**」 |
     | 实际是什么 | **无人 import，环境变量是死的** | **确实无人 import，与其自述一致** |
     | 定性 | **文档承诺了不存在的接线（缺陷）** | **诚实声明 + 有意的半程迁移（不是缺陷）** |
     **⇒ 「注释承诺的不成立」是缺陷；「注释明说自己没成立」是本仓的正确做法。**
     **两者的区别不在「有没有接线」，而在「文档是否如实」。**

---

## 一、待核项的关闭过程

### 1.1 146 号那条 grep 命中是什么

146 号跑的是 `grep -rln --include=*.go "dbxmanifest\|webcookie"`，命中两个文件：
`db/db.go` 与 `db/ddl_definition_guard_contract_test.go`。
**本轮逐行看**：

```bash
grep -n "dbxmanifest" db/db.go db/ddl_definition_guard_contract_test.go
# ⇒ 空（0 命中）
grep -n "dbxmanifest\|webcookie" db/db.go
# db/db.go:7070:// ensureWebCookieSessionsSchema mirrors sql/migrations/077-webcookie-sessions.sql.
# db/db.go:7076:	CREATE TABLE IF NOT EXISTS public.webcookie_sessions (
# …
```
⇒ **命中的是 `webcookie`，不是 `dbxmanifest`。**
⚠️ **⇒ 146 号「`db/db.go` 里的相关提及未判定」这条待核，是因为我把一个 `\|` 交替的两个模式
当成了一个来读** —— 同一行的两个模式，一个是我要找的、一个是我上一轮找到的。

### 1.2 权威复核

```bash
grep -rn 'llm-gateway-go/db/dbxmanifest"' --include=*.go .   # ⇒ 0 命中
```
⇒ **`db/dbxmanifest` 确实零生产 import。**（与 146 号用 `go list` 得出的结论一致。）

## 二、定性：**不是缺陷，是有意的半程迁移**

### 2.1 包注释自陈（`db/dbxmanifest/manifests.go:6-9`）

```go
// No production data path routes through the framework yet: the registry is
// consumed by shadow reads and the metadata gate, while the legacy
// repositories (e.g. admin/model_policies.go) stay authoritative.
```

⚠️ **这句话与实测完全吻合**：
- 「没有生产数据路径」✅ —— 零 import 已证；
- 「shadow reads 与 metadata gate 消费 registry」✅ —— `internal/dbx` 确实在
  `cmd/gateway/main.go` 与 `admin/data_lifecycle_storage.go` 上；
- 「legacy 仓库仍是权威」✅ —— 它自己点名了 `admin/model_policies.go`。

### 2.2 迁移确实执行到了中途（而不是「压根没开始」）

| 事实 | 值 |
|---|---|
| 表 `tenant_model_policies` | **存在** |
| 该表行数 | **3** |
| 框架本体 `internal/dbx` 的生产引用 | **2 个文件**（`cmd/gateway/main.go`、`admin/data_lifecycle_storage.go`） |
| manifest 层 `db/dbxmanifest` 的生产引用 | **0** |

⇒ **建表 ✅ 播种 ✅ 框架接入 ✅ manifest 层接入 ❌ 切换 ❌**
—— **这是一个停在「双轨并行、legacy 仍权威」阶段的有意迁移。**

### 2.3 定性

**不是缺陷。** 理由三条：
1. **它自己说清楚了**（不夸大、不承诺）；
2. **它点名了继任者**（`admin/model_policies.go` 仍是权威），下游不会误判；
3. **表与框架都已在生产**，说明这不是「写完就扔」的草稿，而是**推进到一半的切换**。

⚠️ **但仍有一条值得登记的运维事实（P3）**：
**「停在双轨并行」是一种需要人来推进的状态，不是终态。**
若长期不推进，仓里会同时存在两套读写路径（legacy 权威 + dbx 影子读），
**未来任何一处改动都必须同时问「哪套是权威」**——
这个歧义是真实存在的维护成本。**建议登记为「迁移待收口」条目，由产品/架构确认是继续切换还是回退。**

## 三、⚠️ 本轮的真正洞察：两种相反的注释风格

| | `domains/hooks/promptoptimization`（145 号） | `db/dbxmanifest`（本轮） |
|---|---|---|
| 文档说什么 | 「设 `PROMPT_OPTIMIZATION_ENABLED=1` 即可启用」（还给了环境变量表与触发条件） | 「**目前没有生产数据路径走这个框架**」 |
| 实际是什么 | **无人 import；该环境变量在包外 0 处读取** | **确实无人 import，与其自述一致** |
| 定性 | **P2：文档承诺了不存在的接线** | **不是缺陷：诚实声明 + 有意半程迁移** |

**⇒ 判别式（本轮最值得带走的一条）**：

> **「注释承诺的不成立」是缺陷；「注释明说自己还没成立」是本仓的正确做法。**
> **两者的区别不在「有没有接线」，而在「文档是否如实」。**

⚠️ **这条修正了一个容易走偏的倾向**：本会话前几轮累计了多例
「注释承诺的接线从未落地」（§13），若不把对照写清楚，
很容易滑成「**零引用 + 有注释 = 缺陷**」的机械判断 ——
**而 `db/dbxmanifest` 正是这一机械判断的反例。**

## 四、playbook §41 新增

> **给「注释承诺的接线从未落地」定级时，先问一句：这条注释是「承诺它有」还是「声明它没有」？**
>
> - **承诺它有**（`promptoptimization`：「设这个变量即可启用」）
>   ⇒ **文档与事实不符 = 缺陷**，且面向使用者时影响更大。
> - **声明它没有**（`dbxmanifest`：「No production data path routes through the framework yet」）
>   ⇒ **文档与事实相符 = 不是缺陷**，最多是「迁移待收口」的运维事实。
>
> **落地**：判「零引用包」时，**先把它的包注释读完并分类**：
> ① 承诺已接线（查文档在哪、有没有像 `docs/hooks/prompt-optimization.md` 那样的成文手册）；
> ② 明确声明未接线（并点名继任者/权威方）；
> ③ 两者都没说（**最危险的一种** —— 下游无法判断该信什么）。
> **只有 ① 才是缺陷，③ 需要人工补文档。**
>
> **同族**：§13（注释承诺不成立）、§14（缺 X 先问仓里有没有）、
> §22（零导入是设计约束不是事故）、§39 / §40。
> **共同点：都在提醒「别把『看起来不对』直接读成『缺陷』」。**

---

## 五、诚实边界

- **未改动任何生产代码或数据库**（全部只读）。
- **`admin/model_policies.go` 是否真的仍是权威，本次未逐行核实** ——
  结论基于 `dbxmanifest` 包注释的自述 + `db/dbxmanifest` 零 import 两项。
- **「迁移待收口」是运维建议，不是缺陷判定**；是否继续切换属产品/架构决策。
- ⚠️ **146 号那条「`db/db.go` 的提及未判定」是本轮因 `\|` 交替模式读错而产生的** ——
  **已在 §4.1 记录，不回改 146 号正文**（按约定只在回灌时加警示，本条为同作者同会话的当轮修正，
  已在 146 号 §4.1 段落中如实呈现该更正来源）。
