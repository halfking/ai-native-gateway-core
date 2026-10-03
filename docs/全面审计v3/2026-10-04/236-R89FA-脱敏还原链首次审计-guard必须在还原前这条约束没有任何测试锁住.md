# 236 号 R89-FA —— 脱敏**还原**链首次审计：那条「guard 必须在还原前执行」的明文约束，**没有任何测试锁住**

> **日期**：2026-10-04
> **性质**：纯审计 + **纯测试**；**零生产代码改动**
> **起手**：objective 的核心强调里明写「敏感信息的**替换及恢复**」，而 235 号只覆盖了 provenance **映射**（替换侧），**还原侧从未被真正审计**
> **待裁决**：新增 **待裁决 101**（P3）；**零新增缺陷**（本轮主体是「设计正确但无保护」）
> **playbook**：§196-A / §196-B / §196-C

---

## §〇 结论先行

1. **🔴 F1（本轮唯一实质发现）：`security/sanitize/output_sensitive.go:21-23` 用散文明文写着
   「`NewOutputSensitiveInterceptor` **must run before restoration**」，但这条约束的实现位于
   `cmd/gateway/goal_control.go:578-585` 的**内联循环**里，而现存两个顺序测试
   （`goal_control_test.go:14` / `goal_control_restore_order_test.go:11`）**只覆盖内联循环之前的那段切片**
   ⇒ **把 guard 挪到任何位置，两个测试仍然全绿。**
   而一旦顺序被「整理」反了，后果是**静默的、且没有报错**：guard 会在还原注入合法原值**之后**扫描正文，
   把**合法还原出来的真实值**当成「模型自己生成的敏感值」mask/block 掉 ⇒ **整个还原功能对每个请求失效**。

2. **🟠 F2：`insertRestoreBeforeCompliance`（`output_compliance_control.go:165`）与
   `ensureOutputComplianceFallback`（`:191`）零生产调用方，却被 3 个测试覆盖。**
   其中 `TestLiteWithoutRedisStillInstallsOutputCompliance` 断言「lite 模式链里必须有 output compliance
   拦截器」——**这条产品行为没有任何生产代码在保证**。测试制造了「lite 已覆盖」的错觉。

3. **🟢 F3（正面发现）：脱敏与还原的主干是设计良好的 fail-closed，不是「一写就坏」。**
   - `maskUnrecognizedBody`（`smart_sani_guard.go:941-975`）：掩码后**仍残留保留占位符**、或什么都没变、
     或序列化失败 ⇒ 一律 `ShouldBlock: true`。**宁可阻断也不把带占位符的正文发给客户端。**
   - `RestoreOutputOrMask`（`sanitizer.go:260-280`）：表里没有的占位符 ⇒ 替换为 `[REDACTED]`，
     **防止上游伪造占位符文本骗网关替换成任意值**（这同时覆盖了 objective 里的「输出检测」）。
   - **且会泄漏内部 token 的那个函数 `RestoreOutput`（`sanitizer.go:238-254`，语义是「保留原占位符」）
     零生产调用方**（18 处命中全是测试 + 定义），生产 7 处全用 `RestoreOutputOrMask`。
     阳性对照：同法检索 `RestoreOutputOrMask(` 命中 7 个生产落点。

4. **🟠 F4：r59 报告的「三条降级路径 unlocked 继续跑」在当前工作树**不成立** —— 写入侧已改 fail-closed。**
   `acquireOffsetsLock` 失败 ⇒ `fmt.Errorf("acquire sanitize offsets: %w", err)`（`:308-311`）⇒ 请求不发给上游。
   `:414-415` 的注释「Redis-backed sessions never proceed unlocked」**与代码一致，不是残留**。
   ⇒ **r59 的这条负面结论已过期**（与 235 号订正的 r59 1c/F3 同一文件、同类失效）。

5. **🟡 F5（未闭环，登记为待裁决 101，P3）**：请求侧租户来自 `authenticatedTenant(ctx)`，
   响应侧来自 `keyInfo.TenantID`（多处回退到字面量 `"default"` 或 `""`）。若两者不同源，
   还原侧会去读**另一个租户桶**的同名 sessionID。目标 key 不存在 ⇒ 空表 ⇒ mask（**安全**）；
   两租户恰好用同一 sessionID ⇒ 有把对方明文还原进本租户响应的可能。
   **本轮未追完 chat 主路径取哪一行、`keyInfo` 是否可能为 nil** ⇒ 如实登记为未闭环，不定级为缺陷。

---

## §一 为什么这一轮做它

- objective 核心强调原文：「**需要注意检查会话中的敏感信息的替换及恢复的功能特性**」。
- 235 号覆盖的是**替换侧**的 provenance 映射（offset −2 / 哈希两套空间）。
- **前轮覆盖度实测**：`git grep -ln 'SanitizeRestoreInterceptor\|maskUnrecognizedBody\|insertSanitizeRestore' -- 'docs/全面审计v3/*'`
  → **全目录仅 1 个文件命中，且是 2026-09-29 的原始需求文档**（`07-原生代码自修复和代码重构方案.md`），不是审计结论。
  ⇒ **这是 objective 核心项上的真空档**，不是重复劳动（§153 已查）。

---

## §二 方法与工具纪律

| 纪律 | 本轮执行 |
|---|---|
| 否定结论须有阳性对照 | `unlocked` 残留、两个 helper 的调用方、RestoreOutput 的调用方，全部先证明工具能扫到已知符号 |
| 记录检索工具 | `git grep`（**大小写敏感**）为主；Grep 工具（ripgrep，**默认不敏感**）为辅 |
| 子代理承重结论一律亲验 | 子代理报的**全部 5 条**我都独立复核；**其中 3 条被我推翻**（见 §七） |
| 不推理，执行验证 | 链顺序不靠推导，**照抄插入逻辑跑了一遍真实结果**（§三） |
| 排除 vendor | 子代理首轮检索误含 vendor 已被我要求作废重跑；我自己所有检索均带 `':(exclude)vendor/**'` 或限定路径 |

**子代理分工**：一个查**接线与顺序**（流式/非流式、协议覆盖、fail-open/closed），一个查**数据一致性与租户隔离**（TTL、跨进程、offset 原子性、降级路径）。**子代理未跑任何测试、未连 Redis/PG**。

---

## §三 F1 详述：那条约束长什么样、为什么承重、我怎么证的

### 3.1 约束原文（被调方）

`security/sanitize/output_sensitive.go:21-23`：

```
// NewOutputSensitiveInterceptor must run before restoration. Model-generated
// sensitive values have no trusted provenance and are never inserted into the
// reversible input map. Unknown actions conservatively select masking.
```

**理由是充分的，不是形式主义**：guard 要拦的是**模型自己生成的**敏感值——它们没有可信溯源、
**永远不会**进还原表。而 restore 的工作恰恰是**把调用方的真实值注入正文**。
若 guard 排在 restore 之后，它扫到的文本里合法地含有那些真实值，会把它们当成模型生成内容拦下来。

### 3.2 执行顺序 = 切片顺序（依据）

`domains/hooks/response/chain.go:60` `for i, interceptor := range c.interceptors` **正向遍历**，
且 `:97` `nextReq.ResponseBody = result.ModifiedBody` 把改动逐个传给下一个
⇒ **索引小者先执行**，「before」＝「索引更小」。

### 3.3 真实顺序：**执行**出来的，不是推出来的

我把 `goal_control.go:578-585` 的插入循环**逐字抄进一个独立程序**跑（`go run`，不入仓库）：

```
resulting chain (index 0 executes first, per chain.go:60):
  0: goal
  1: audit
  2: output_sensitive_guard  <-- output sensitive guard
  3: sanitize_restore        <-- sanitize restore
  4: output_compliance
VERDICT: output_sensitive_guard executes BEFORE sanitize_restore
```

⚠️ **子代理给的顺序是 `[goal, audit, restore, outputGuard, compliance]`——错的。**
原因：`copy(ordered[index+1:], ordered[index:])` 是 memmove 语义，guard 被插到 restore 的下标，
**把 restore 往后挤了一位**。

### 3.4 真实生产链的端到端证据

我新写的测试直接调用**真实生产函数** `installSmartSaniGuard(handler, nil, nil)`（nil Redis 是
`goal_control.go:535` 声明支持的生产配置），实测输出：

```
smart_sani_guard: restore interceptor wired chain_length=3 output_sensitive_action=mask
chain order verified: [output_compliance_typed -> sanitize_restore -> output_compliance_typed]
```

⇒ **顺序是对的**（guard 在前、compliance 在后），**这是正确设计，不是缺陷**。
本轮的发现是：**它对，但是无保护的**。

### 3.5 为什么现有测试锁不住

- `insertSanitizeRestoreBeforeOutputCompliance`（`goal_control.go:596`，生产在 `:571` 调用）
  只负责把 restore 插到 compliance 之前；**guard 的插入不在这个函数里**。
- `goal_control_test.go:14-26` 与 `goal_control_restore_order_test.go:11-24` 测的都是**插入之后、guard 之前**的那段切片。
- ⇒ **guard 可以在不弄红任何测试的情况下被挪到 restore 之后**，而那会静默摧毁还原功能。

---

## §四 F2 详述：三个测试锁着一个生产不调用的函数

全量检索（阳性对照同法）：

| 函数 | 定义 | 生产调用方 | 测试调用方 |
|---|---|---|---|
| `insertSanitizeRestoreBeforeOutputCompliance` | `goal_control.go:596` | ✅ `goal_control.go:571` | `goal_control_test.go:20` |
| `insertRestoreBeforeCompliance` | `output_compliance_control.go:165` | ❌ **0** | `goal_control_restore_order_test.go:16,20,45` |
| `ensureOutputComplianceFallback` | `output_compliance_control.go:191` | ❌ **0** | `goal_control_restore_order_test.go:28,33` |

⚠️ 我修正了子代理的一处过头结论：它说「生产用的那个函数零测试覆盖」——**不对**，
`goal_control_test.go:20` 覆盖了它。**准确的说法是：被 3 个测试覆盖的是那个生产不调用的孪生函数。**

`TestLiteWithoutRedisStillInstallsOutputCompliance`（`:26-37`）断言
「lite 模式响应链里有 output compliance 拦截器」——但**没有任何生产代码调用
`ensureOutputComplianceFallback` 去装它**。这条断言目前是**对着一个空承诺点头**。

---

## §五 F4 详述：又一条被后续修复推翻、却没订正的负面结论（与 235 号同族）

r59 报告原文写：sanitizer 跨进程 offset 锁有三条降级路径（token rand 失败 / Redis 错误 / 竞争超时）
都会「unlocked 继续跑，只 Warn」，并据此记为 F4（低）。

**当前代码**：

| 场景 | 现状 | 落点 |
|---|---|---|
| 锁获取失败 | `fmt.Errorf("acquire sanitize offsets: %w", err)` | `:308-311` |
| offsets 加载失败 | `fmt.Errorf("load sanitize offsets: %w", err)` | `:313-316` |
| 提交失败（含租约丢失） | `fmt.Errorf("persist sanitize mapping: %w", err)` | `:383-385` |
| 租约 token 生成失败 | 直接 `return ..., err` | `:422-424` |
| 注释是否与代码一致 | ✅「Redis-backed sessions never proceed unlocked」**正确** | `:414-415` |

⇒ **fail-closed 成立**（错误上抛 ⇒ 503 ⇒ 原文不上游）。
「三条降级路径 unlocked」是 r59 写下时的形态，**已被后续修复推翻，而文档未订正**。
（235 号已订正同一文件的 1c/F3；**本条 F4 同属该文件的同类失效**，本轮一并登记。）

---

## §六 F5 详述：租户来源可能不同源（未闭环，如实登记）

- **key 构造**：`sanitizeMapKey`（`:1765-1770`）= `session:{HashTenant(tenant)}:{sessionID}:sanitize`；
  `HashTenant("")` 返回 `""`，而 `sanitizeMapKey` 会先把空 tenant 改写成 `"_unknown"`（`:1766-1768`）
  ⇒ **`""` 与 `"_unknown"` 落在同一个桶**。设计意图在 `:1762-1764` 的注释里写得很清楚
  （「一旦知道租户，存取都必须用租户化形式以防跨租户还原」）。
- **请求侧**租户来自 `authenticatedTenant(ctx)`（`security/sanitize/input_protocols.go:24-27`）。
- **响应侧**租户来自 `keyInfo.TenantID`，而 `keyInfo` 为 nil 时回退到字面量 `"default"`
  （`domains/streaming/handler.go:3194`、`:4072`）或 `""`（`:3383`、`:4834`）。
- **风险形态**：若两侧不同源，还原侧会读**另一个租户桶**的同名 sessionID。
  目标 key 不存在 ⇒ 空表 ⇒ `[REDACTED]`/mask（**安全**）；
  两租户恰好用同一 sessionID ⇒ 可能把对方明文还原进本租户响应。sessionID 来自客户端 header，
  **无全局唯一性保证**。
- ⇒ **新增待裁决 101（P3）**：需一轮**接线调查**确认 chat 主路径实际取哪一行、`keyInfo` 是否可能为 nil。
  **本轮不追完 ⇒ 不定级为缺陷，不擅自加兜底。**

---

## §七 我把子代理的结论推翻了三处（全部亲验）

| # | 子代理结论 | 我的复核 | 判定 |
|---|---|---|---|
| 1 | 链顺序 = `[goal, audit, restore, outputGuard, compliance]` | 照抄插入循环**实际执行**，guard 落在 index 2、restore 在 index 3 | ❌ **推翻**（顺序结论反了） |
| 2 | 生产用的 `insertSanitizeRestoreBeforeOutputCompliance` 零测试覆盖 | `goal_control_test.go:20` 就在调它 | ❌ **推翻**（过头） |
| 3 | `:415` 的 "unlocked" 是**残留注释** | `:414-415` 原文是「never proceed unlocked」，**与代码一致** | ❌ **推翻**（误读；真正过时的是 r59 报告） |

⇒ 这三条**全部朝同一个方向**：子代理倾向于把「看起来可疑」升级成结论。
**这正是我坚持亲验承重项的回报**——若直接采信，本报告会出现一个**方向相反的链顺序结论**，
而它会被后来的人当成「已验证」。

**我自己也栽了四次**：① 首轮 `Select-Object -First 30` 截断 grep（§186，第 N 次）；
② `node -e` 内嵌引号又被 PowerShell 破坏；③ 探针里 `insertFn == nil` 函数比 nil 是编译错；
④ 初稿在诚实边界里引用了子代理给的行号（`:336-379` 等），**复核发现已漂移到 `:308-385`**，
  未经我自己确认就写进正文 ⇒ 全部改为我亲验过的行号。

---

## §八 本轮产出

### 8.1 新增 `cmd/gateway/sanitize_guard_order_test.go`（2 个测试）

1. `TestOutputSensitiveGuardRunsBeforeSanitizeRestore`
   —— **直接调用真实生产函数** `installSmartSaniGuard`，断言最终切片里
   **restore 的前一位与后一位各是一个 `*OutputComplianceInterceptor`**。
   - **反空跑断言**（`complianceCount != 2` 即失败）：链里必须真的有 guard 与 compliance 两个可区分对象，
     否则位置断言是空的。
   - 不用「复刻插入逻辑」来测——那测的是副本不是代码（判据存在、被测绿、没接线，三者可同时成立）。
2. `TestDeadOrderHelpersHaveNoProductionCaller`
   —— 记录 F2 的事实，作为将来它们获得生产调用方时的提醒点。

### 8.2 负控（**实测转红**）

| 负控 | 做法 | 实测 |
|---|---|---|
| **NC-E3** | 在 `goal_control.go:582` 后加一行把 guard 与 restore 交换 | ✅ 转红，报 `sanitize restore is at index 0: nothing can run before it ... chain=[sanitize_restore -> output_compliance_typed -> output_compliance_typed]` |

探针已回收（`git diff --stat cmd/gateway/goal_control.go` 为空），工作树只剩新测试文件。

### 8.3 文档订正

**本轮未改 r59 文档**（F4 属该文件，但 235 号刚在同文件追加过「订正 1」；
本轮把 F4 作为**新证据**写进报告与台账，留给下一轮统一整理，避免同文件连续追加两段订正）。
⇒ 这是**有意留待下一轮**的，不是遗漏。

---

## §九 诚实边界

- **无 PG / Redis / Docker，未起真进程、未执行任何 SQL**；全部为静态代码取证 + 本地单测。
- **零生产代码改动**；唯一新增文件是测试。
- **子代理未跑任何测试、未连 Redis/PG**；其 5 条结论我全部独立复核，**推翻 3 条**（§七）。
- **F5 未闭环**：未追完 chat 主路径的租户取哪一行、`keyInfo` 是否可能为 nil ⇒ 不定级为缺陷。
- **「零生产调用方」≠ 已排除动态引用**（`import()`、字符串拼路径、运行时注册的工厂不在静态扫描覆盖内）。
  本条链是显式构造函数 + `SetResponseInterceptor`，属静态可判定形态。
- **`installSmartSaniGuard` 在测试中以 nil Redis 调用**：这与 `goal_control.go:535` 声明支持的生产配置一致，
  但我**没有**在带 Redis 的真实进程里验证过还原链。
- **A4 侧的 handoff 链**（235 号 §七）与本轮无关，未重查。
- **`go test ./...` 全量未跑**；**CI 从未运行**。
- **`cmd/gateway` 包不在 `GUARD_PACKAGES` 内**（沿用待裁决 100 的口径：`Makefile:87` 白名单只有 14 个包），
  故本轮新增的钉测**不在快速守卫门内**——这与待裁决 100 是同一件事，不重复登记。

---

## §十 playbook

### §196-A **「不推理，执行一遍」在有插入/拼接逻辑时是不可替代的**

链顺序我推导过、子代理也推导过，**两个结论相反**。
把它们都写进一个独立小程序跑一遍，成本 30 秒，结论唯一。
⇒ **How to apply**：凡涉及「切片重排 / 元素插入 / 索引移位」的顺序问题，
  推导只用来**提出假设**，判定必须来自**执行**。
`copy(dst[index+1:], src[index:])` 是 memmove 语义——这类代码的直觉错误率极高，
而它恰好决定「哪个拦截器先看到正文」这种安全属性。

### §196-B **子代理的「看起来可疑」要按「还没验证」处理，尤其当它与我的推导一致时**

我推翻的三条里，有两条（`insertSanitizeRestoreBeforeOutputCompliance` 零测试覆盖、`:415` 残留注释）
**看起来非常有说服力**，而且我在写报告前已经先信了半句。
真正的教训是：**子代理报告里凡是带「零」「残留」「从未」这类词的断言，一律按待验证处理**——
它们是全文最容易被过度概括的部分。

### §196-C **「设计正确」和「有保护」是两件事，本轮的主体发现属于后者**

本轮 F1 的最终定性是：**顺序是对的**（执行验证 + 被调方有明文理由），
**缺陷在于没有任何测试锁住它**，而破坏它的后果是**静默的**（还原功能整体失效、无报错）。
⇒ **How to apply**：审计一个「看起来设计良好」的机制时，除了确认设计，
  还要单独问一句 **「哪条不变量一旦被破坏会静默劣化，而它现在靠什么保护？」**
  答不上「靠什么保护」，就补一个钉测——**哪怕当前是绿的**。

### §196-D 一条不该登记的「发现」：**已过期的负面结论要与「残留代码」分开判**

子代理把 `:414-415` 的 "unlocked" 判成「残留注释」。实际那句话是
「Redis-backed sessions **never** proceed unlocked」——**是当前正确行为的描述**。
真正过时的是 r59 报告里的同一件事。
⇒ **How to apply**：看到关键词命中，先读完整那一句再定性；
「代码里有这个词」与「代码里残留着这个词的旧语义」是两回事。
