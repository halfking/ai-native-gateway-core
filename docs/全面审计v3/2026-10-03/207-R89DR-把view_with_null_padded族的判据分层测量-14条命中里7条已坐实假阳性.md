# 207 号 · R89-DR —— 把 `view_with_null_padded` 族的判据**分层测量**：14 条命中里 **7 条已坐实是假阳性**，并逼出一个前提没成立的方法论

> 变更面：`admin/null_padded_trigger_evidence_test.go`（**新门**，含测量 + 单向可证的棘轮 + 两条负控）。
> **零生产改动**。`sourceFamilyOf` / `nullPaddedPredicateHit` **一个字没动**（理由见 §五·2）。
> 本轮还的是 206 号 §六 欠下的那笔账：**「那 14 个文件里假阳性到底占多少」**。

---

## 〇、起手：206 号把「我被误判了一次」登记成了一笔**没量的账**

206 号把 `admin/memora_handlers.go` 双腿化之后，族计数实测位移是 `view_with_null_padded` 13 → **14**，而那一笔的触发经逐行确认是**假阳性**（补位列 `id` 命中的是 `:107-108` 的注释与 `:168`/`:663` 的 JSON 响应键）。206 号当时的结论是「该族假阳性已知、占比未量 ⇒ 登记为下一轮顺位」。

本轮去量。**先量后判**——因为任何「该不该改分类器」的判断都依赖这个数。

---

## 一、判据分层：span 级（现有）vs 字面量级（新增）

`nullPaddedPredicateHit` 的实现（`request_logs_stop_write_classification_test.go:1457-1483`）：

```go
for _, sp := range spans {                       // span = 函数体 或 包级字面量
    body := raw[sp.lo:sp.hi]
    if !strings.Contains(low, name) { continue }  // 读视图？
    for col := range present {
        if nullPaddedColWordRE(col).MatchString(body) { return true, col }  // 列名出现过？
    }
}
```

⇒ 它只要求补位列名**出现在同一个 span 内**。而 span 是**函数体** ⇒ 里面既有 SQL 字面量、也有 Go 代码（map 键、结构体字段、`//` 注释）。`\bid\b` 分不清 `client_model` 那种 SQL 列引用与 `"id": fact.ID` 那种 JSON 键。

本门新增的**字面量级**判据：列名出现在一个**形似 SQL 的字符串字面量**里（正文含 `SELECT|FROM|WHERE|GROUP BY|JOIN|ORDER BY|LIMIT|INSERT|UPDATE|DELETE`）。

⚠️ **两层刻意不合并**。span 级是现有分类器用的那层，**不改动**；字面量级只用来**暴露证据**。

### 1.1 ⚠️ 本轮自造的一次假通过：拿 A 列的证据替 B 列背书

第一版报告判 `SQL-LIT` 的条件是「文件里**任一**补位列有字面量级证据」。结果：

```
SQL-LIT  admin/credential_monitor_heatmap.go  via=credits_rate_multiplier  字面量级=id
```

`via` 是 `credits_rate_multiplier`（只在**注释**里，见 §三·2），却被文件里**另一列** `id` 的存在判成了 SQL-LIT。

⇒ **判据必须与被测量对象同源**：应当拿 `via` 列**自己**去问字面量级证据。修正后 `via=credits_rate_multiplier` 正确落进 NON-SQL-ONLY。

（这是 playbook §118 的来源；**它是一次真实发生的假通过**，不是假设。）

---

## 二、测量结果（14 条命中，`PARSE-FAIL = 0`）

| 证据层 | 数量 | 含义 |
|---|---|---|
| **SQL-LIT** | **10** | `via` 列出现在形似 SQL 的字面量里 |
| **NON-SQL-ONLY** | **4** | `via` 列**在任何** SQL 形字面量里都没出现 |
| PARSE-FAIL | 0 | 无解析失败（解析失败会单独报出，**不**当成「无证据」） |

**覆盖下限**必带：判据若退化成「集合为空」，「没有可疑命中」会与「什么都没抽到」同步为真（201 号 §102 的教训）⇒ 本门断言补位列清单非空、分类表非空、该族**非空**。

---

## 三、🔴 F1：`NON-SQL-ONLY` 那 4 条**逐条坐实是假阳性**

| 文件 | `via` | 命中位置（逐条打开确认） |
|---|---|---|
| `admin/attachments_routes.go` | `id` | `:65` **注释散文**「the request id is in the path」 |
| `admin/credential_monitor_heatmap.go` | `credits_rate_multiplier` | `:306-318` **注释块**（解释 815 三列与 §9.27 自纠）；`:317` 该文件自陈「代码用的是共享谓词、始终正确——错的只是这段描述」 |
| `admin/memora_handlers.go` | `id` | `:107-108` **注释** + `:168`/`:663` **Go map 键**（`"id": fact.ID`，L1 Memora 事实的 JSON 响应字段） |
| `admin/route_incidents.go` | `id` | `:8`/`:118` 路由文档 `{id}`、`:132` `id := parts[0]`、各 handler 的 `id string` 形参 —— 此处 `id` 是 **route incident** 这个**另一个实体**的标识符 |

## 四、抽查 3 条 `SQL-LIT`：**同样是假阳性**，形态是「别的表的 id」

| 文件 | 位置 | 那里的 `id` 属于 |
|---|---|---|
| `admin/top_problems.go` | `:195` `LEFT JOIN credentials c ON c.id = rl.credential_id` | **credentials** |
| `admin/usage.go` | `:341`/`:342` `ak.id`/`app.id`、`:456`/`:457` `c.id`/`p.id`、`:460` `GROUP BY p.id` | **api_keys / applications / credentials / providers** |
| `admin/probe_history.go` | `:110-111` `c.id`/`p.id`、`:167`/`:232`/`:346`/`:378` `SELECT id FROM credentials`、`:540` `mc.id` | **credentials / providers / models_canonical** |
| `admin/credential_monitor.go` | `:283` `c.id`、`:306`/`:367` `p.id`、`:333`/`:336` `c.id` | **credentials / providers** |

⇒ 这 4 个文件对 `request_logs_with_current_month` 的读点（`probe_history.go:480`、`credential_monitor.go:360` 的关联子查询）**只用 `rl.credential_id` / `outbound_model` / `ts` / `request_status` / `error_kind` / `failure_detail_code`，从不向视图要 `id`**。

**⇒ 已坐实 7 / 14 是假阳性**（4 条 NON-SQL-ONLY + 3 条抽查的跨表 SQL-LIT）。剩下 7 条 `SQL-LIT` 未逐个打开，但 `via` 全是 `id`，形态与已验的 3 条同源。

### 4.1 根因：`id` 是**通用列名**，而判据只到「列名级」、不到「列-表绑定级」

`db/request_logs_view_padded_columns.go` 对 `id` 的裁决是 `verdictDifferentThing`（`same-name-different-thing`）：会话臂的 `id` 是 **turn id**、v1 的 `id` 是**请求行 id**，真库按 `request_id` 配对 1,515,984 组**命中 0 次** ⇒ 保持 NULL 补位。

也就是说：**没有任何读点应该引用视图的 `id`**（引用它就等于拿到恒 NULL 或错语义的值）。而 `id` 恰好是最容易在别处出现的列名 —— 于是**通用列名**成了这一族假阳性的主要来源。

---

## 五、🔴 F2：206 号引用的那条方法论，**它的前提在本仓没成立**

206 号（playbook §116）写的是：

> 保守方向（假阳性 ⇒ 逼你写具名论证）⇒ **可接受，别动**。

本轮量完之后必须修正这句话。**「逼你写具名论证」在本仓没有任何机制落实**：

- `TestStopWriteEffectAgreesWithSourceFamily` 只**按族约束档位**（`familyViewNullPadded` 只约束 `unaffected`），它**不要求**族里每个命中有具名论证；
- 结果：14 个文件在这族里，**零个**有具名论证，门照样绿。

⇒ **一个「保守的误报」只有在它的代价被机制强制收取时才是可接受的代价。** 否则它就是纯噪音，而纯噪音会做两件坏事：① 让整族失去意义（族标签不再携带信息）；② **误导后来人**——206 号自己就被它误导过（把一个正确的文件推进了这族）。

### 5.1 本轮只把**单向可证**的那一半升为致命

新门 `TestNullPaddedFamilyHitsAreVisible` 的棘轮**只对 NON-SQL-ONLY 生效**，因为那一半**单向可证**：

> 「该列在**任何**形似 SQL 的字面量里都没出现」⇒ 它在本文件里**不可能**被任何 SQL 语句引用 ⇒ 作为「谓词级空」触发**必是**假阳性。

**不存在假阴性方向** ⇒ playbook §106 的前置条件（先证不会误伤）自动满足 ⇒ 升致命是安全的。

反之，**SQL-LIT 那半绝不下结论**——`top_problems.go` 的 `c.id` 与 `probe_history.go` 的视图读点都证明「出现在 SQL 里」与「是视图的那一列」是两回事。要收口它得把判据升到「列-表绑定级」，而那**必须先证不会放进真触发** ⇒ 本轮不做，登记为下一轮。

### 5.2 双向棘轮（新增报错致命 / 登记过期致命）

| 情形 | 判据 | 处置指引 |
|---|---|---|
| NON-SQL-ONLY 文件**未登记** | 致命 | (a) 确实是假阳性 ⇒ 登记并写清命中位置；(b) 其实是真触发 ⇒ **放宽本门判据并登记该形态**，**不要**直接把它从族里踢出去 |
| 登记项**已不再**是 NON-SQL-ONLY | 致命 | 移除即可 |
| 登记理由为**空串** | 致命 | 具名论证不能是空串 |

---

## 六、验证

**负控两条，都在未修复的（真实）状态上跑过，绿是改完才拿到的：**

| 负控 | 手段 | 结果 |
|---|---|---|
| A：模拟新增一条未登记命中 | 摘掉 `admin/route_incidents.go` 的具名论证 | 🔴 `补位列命中在任何 SQL 字面量里都不出现（admin/route_incidents.go 此前未登记）` + 二选一处置指引 |
| B：登记项过期 | 把实测为 SQL-LIT 的 `admin/usage.go` 登记成假阳性 | 🔴 `nullPaddedNonSqlOnlyJustified 里有条目已不再成立（移除即可）：admin/usage.go` |

两条**同时**转红（一次运行里各报一条）⇒ 棘轮两个方向都有牙。

**其余**：10 个已登记守卫 `ok ×10 / 0 FAIL`；`go test ./admin/` 全包 ok；本轮新增文件 gofmt/vet 干净。

---

## 七、证伪与未 settle

**证伪 2 条**

1. 撤回「206 号说的『这一族假阳性 3 真 2 假』可以继续挂着不管」—— 那是另一时点另一批的旧实测（5 个文件）；**当前 14 条的实测是：4 条 NON-SQL-ONLY 全部坐实假阳性，另抽查 3 条 SQL-LIT 也全是跨表假阳性**。
2. 撤回「我写的第一版证据报告（按『任一补位列有字面量证据』判 SQL-LIT）是可用的」—— 它让 `via=credits_rate_multiplier` 的文件被 `id` 的证据背书成 SQL-LIT，是**一次真实发生的假通过**。

**未 settle（如实登记）**

- **剩下 7 条 `SQL-LIT` 未逐个打开**。已验的 3 条全是跨表假阳性、`via` 全是 `id`，形态同源；但**没有全部查** ⇒ **不宣称「14/14 全是假阳性」**，只报「7 条已坐实、7 条形态同源但未逐个确认」。
- **不宣称 `view_with_null_padded` 这个族可以整体废弃**。要废弃/收紧必须先把判据升到「列-表绑定级」（把 `c.id` 与 `rl.id` 区分开），而那要先证不会放进真触发（playbook §106）。
- ⚠️ `db/request_logs_view_padded_columns.go:48-49` 的文档漂移（写「现网 6 列」却只列 5 个名字）**仍未修**（206 号已登记，同样刻意未改）。
- **未连 PG**；`go test ./...` 全量未跑；CI 未运行；本机 `core.hooksPath` 未设置 ⇒ 本次推送未经 pre-push 门。
- 债务仍剩 **20** 条（207 号未还债）。

---

## 八、下一轮顺位

1. **把判据升到「列-表绑定级」**（区分 `c.id` 与 `rl.id`），先把剩下 7 条 `SQL-LIT` 逐个查清，再决定是否收紧 —— 这是唯一能真正消掉这一族噪音的路径。
2. 修 `db/request_logs_view_padded_columns.go:48-49` 的文档漂移（先确认第 6 列是谁）。
3. 继续还债（`admin/quality_correlations.go` / `admin/provider_models.go` / `admin/session_sanitize_matches.go`）。

---

## 九、playbook 新增

### §117 一个「保守的误报」**只有在它的代价被机制强制收取时**才是可接受的代价

常见的说法是「分类器偏保守（假阳性）比偏激进（假阴性）好，所以别动它」。这句话**只在代价被收取时成立**。

⇒ 判据不是「它偏哪个方向」，而是「**偏保守的代价有没有人真的付**」：
  - 若下游**强制**要求具名论证 ⇒ 假阳性变成「多写一段话」⇒ 可接受；
  - 若下游只是**按族约束**、没人要求论证 ⇒ 假阳性**零成本** ⇒ 它就是纯噪音 ⇒ 而纯噪音比无门更坏：**族标签会被当结论引用**（本仓 206 号自己就被误导过）。
⇒ **How to apply**：审计任何「保守近似」分类器时，先去找「**谁在为它的假阳性付钱**」，找不到就是没在付 ⇒ 那它不是保守，是没实现。

### §118 判据分层时，**证据必须与被测量对象同源**

把判据拆成「粗层 + 细层」做分层报告时，极易犯的错是**用粗层/另一列的证据去支撑细层的结论**。

本轮实例：判 `SQL-LIT` 的条件写成「文件里**任一**补位列有字面量级证据」，于是 `via=credits_rate_multiplier`（只在注释里）的文件被文件内**另一列** `id` 的证据判成了 SQL-LIT。

⇒ 规则：**每一条判定都必须针对它声称的那个对象自己去取证**，不许借用。
⇒ 附带判据：**A 列的证据不能替 B 列背书**。要报「多列」时，逐列判定后聚合，**不要**先聚合成一个布尔值再回填到每列。

### §119 升级判据前，先问「这个方向**有没有**假阴性可能」——这是 §106 的可操作化

playbook §106 说「把判据改严之前先证它不会误伤」，但没说怎么证。可操作的切法是**按方向切**：

- **单向外证**的判据 ⇒ 可以放心升致命。本轮 `NON-SQL-ONLY`：「该列在任何形似 SQL 字面量里都没出现」⇒ **不可能**被任何 SQL 语句引用 ⇒ 假阳性是**必然**的，不是概率的 ⇒ 无假阴性方向。
- **需要双向区分**的判据 ⇒ 不能升。本轮 `SQL-LIT`：「出现在 SQL 里」**推不出**「是视图的那一列」（`c.id` 反例）⇒ 双向不可分 ⇒ 保持只报告、不判定。

⇒ **How to apply**：写门时先写下「我这个判据的假阳性是**必然**还是**可能**」；必然 ⇒ 升致命；可能 ⇒ 只能报告 + 要求具名论证。
