# 237 号 R89-FB —— 结清 236 号的 F5：跨租户泄漏**不成立**，但查出 no-verifier 路径的**写读桶不匹配**

> **日期**：2026-10-04
> **性质**：纯审计 + **纯测试**；**零生产代码改动**
> **起手**：兑现 236 号 §六 F5 未闭环项（= 待裁决 101 的裁决前提），并把 r59 的 F4 订正收尾
> **待裁决**：**101 改写**（从「可能跨租户泄漏」改为「键不匹配导致的降级」）；**零新增缺陷**
> **playbook**：§197-A / §197-B / §197-C

---

## §〇 结论先行

1. **🟢 F1：跨租户泄漏向量不成立，236 号担心的最坏情况被证伪。**
   - 正常路径两侧**同源**：`prepareSanitizeRequest` 把验证结果 **memoize** 进 ctx
     （`domains/streaming/sanitize_auth.go:49-51`），handler 通过 `verifyRequestKey`（`:60-67`）
     复用**同一个 `*KeyInfo`** ⇒ 请求侧 `tenant(info)`（`:56`）与响应侧 `keyInfo.TenantID`
     （`handler.go:5606`）是同一个值。
   - 且**已有两个测试覆盖该向量**（§153，不重复登记）：
     `security/sanitize/cross_tenant_race_test.go:264`
     `TestSanitizeMiddleware_NilTenantHash_FallsBackToUnknown`、
     同文件 `:229` `TestSanitizeRestoreInterceptor_CrossTenant_DoesNotLeakMap`。

2. **🟠 F2（本轮唯一实质发现）：no-verifier / static-key 路径下，请求侧与响应侧落在
   **两个不同的 Redis 桶**。**
   - 请求侧：`sanitize_auth.go:38` 绑定字面量 `"default"` ⇒ 桶 `session:37a8eec1ce19687d:…:sanitize`
   - 响应侧：`handler.go:5604-5607` `tenantID := ""`，**仅当 `keyInfo != nil` 才取值**；
     而 `handler.go:2359-2372` 的 static-key 分支**从头到尾没有给 `keyInfo` 赋值**
     （该分支自己的注释就写着「downstream already handles the **keyInfo==nil shape**」）
     ⇒ `""` 经 `sanitizeMapKey`（`smart_sani_guard.go:1766-1768`）改写为 `"_unknown"`
     ⇒ 桶 `session:72c67438e9e86083:…:sanitize`。
   - 两个哈希**实算确认不同**（`sha256("default")[0:8]` vs `sha256("_unknown")[0:8]`）。
   - **后果是降级，不是泄漏**：同进程由 ctx 本地表兜住
     （`smart_sani_guard.go:256` 写入 `WithSanitizeMap`，`:712` `SanitizeMapFromContext` 读出）；
     跨进程或 ctx 被重建时 Redis 读空 ⇒ **fail-closed 掩码/阻断**，不会把别家明文吐出去。

3. **🟡 F3：`prepareSanitizeRequest` 有 3 个生产调用方（`handler.go:1901`、`messages.go:78`、
   `responses.go:111`）却零测试引用，且仓库里没有 `sanitize_auth_test.go`。**
   它是**唯一**决定「脱敏能否跑、跑在哪个租户命名空间」的地方。

4. **🟠 F4：r59 的 F4（offset 锁「三条降级路径 unlocked 继续跑」）已过期**，
   与 235 号订正的 r59 `1c`/`F3` 属同一文件的同类失效。**本轮已集中订正**（见 §五）。

5. **⚠️ F5（本轮的方法论代价）：我写了一条测不了自己要测的性质的判据，已删除。**
   `TestPrepareSanitizeRequest_NoVerifierBindsATenant` 试图跨包读取 `security/sanitize`
   里的**非导出** context key，其比较辅助函数按构造恒为真 ⇒ 判据**恒亮**、测不到任何东西。
   ⇒ 删掉，改用 **AST 级**判据（能看见调用表达式、看不见注释），并为它设计了两条负控（§六）。

---

## §一 追溯链（F2 的每一跳都亲读）

| # | 环节 | 落点 | 读到什么 |
|---|---|---|---|
| 1 | 请求侧入口 | `handler.go:1901`、`messages.go:78`、`responses.go:111` | 三条入口都调 `prepareSanitizeRequest` |
| 2 | 租户绑定 | `sanitize_auth.go:56` | `sanitize.WithAuthenticatedTenant(ctx, tenant(info))` |
| 3 | `tenant()` | `messages.go:1755-1760` | `ki != nil → ki.TenantID`；否则 `"default"` |
| 4 | 验证结果 memoize | `sanitize_auth.go:49-51` + `:60-67` | handler 复用**同一个 `*KeyInfo`** ⇒ 与响应侧同源 |
| 5 | no-verifier 回退 | `sanitize_auth.go:28-38` | 绑定字面量 **`"default"`**；注释：`Local no-verifier mode has one fixed tenant. Never use a caller header.` |
| 6 | 响应侧取租户 | `handler.go:5604-5607` | `tenantID := ""`；`if keyInfo != nil { tenantID = keyInfo.TenantID }` |
| 7 | static-key 分支 | `handler.go:2359-2372` | **不赋值 `keyInfo`**；注释：`downstream already handles the keyInfo==nil shape` |
| 8 | key 归一 | `smart_sani_guard.go:1765-1777` | `tenantID == ""` → 改写为 `"_unknown"`，再 `HashTenant` |
| 9 | ctx 本地表 | `smart_sani_guard.go:256` / `:712` | 同进程可绕开 Redis |
| 10 | 桶实算 | `sha256` | `"default"`→`37a8eec1ce19687d`；`"_unknown"`→`72c67438e9e86083`；**不同** |

⇒ **链条每一跳都闭合**，没有「我以为」的环节。

---

## §二 为什么跨租户泄漏不成立（三条独立理由）

1. **正常路径两侧同源**（§一 #4）——不存在「读错桶」的前提。
2. **写入侧从不使用 `"_unknown"` 之外的匿名桶给已认证请求**：
   已认证请求写 `tenant(info)`，未认证但过了 static-key 的写 `"default"`；
   `_unknown` 桶只会被「认证通过但 `KeyInfo.TenantID == ""`」的 key 写入。
3. **即使读空，后果是 fail-closed**：`loadMap` 失败 / 读空 ⇒
   `InterceptNonStream:737-741`（native 形态直接 `ShouldBlock`）、
   `:744-746` + `maskUnrecognizedBody:941-975`（legacy 走掩码，残留 marker 仍 block）。

⇒ **「把 A 租户的明文还原到 B 租户响应」这条路径，今天没有可达形态。**
（这与 236 号 §六 的初步怀疑相反 —— 236 号当时明确标注「未闭环、不定级」，本轮把它追完了。）

---

## §三 F2 的准确定级与修法建议（登记待裁决，**不擅自动手**）

- **定级：P3，冗余缺口，非安全缺陷。**
  - 不是安全问题：失败方向是 fail-closed。
  - 是可靠性问题：**no-verifier 部署（lite / 无 DB 降级启动）下，跨进程或 ctx 重建时还原必然失败**，
    客户端看到的是 `[REDACTED]` 或 403，而不是自己的真实值。
- **最小修法（一行）**：把 `handler.go:5604` 的 `tenantID := ""` 与 `sanitize_auth.go:38` 对齐
  （同为 `"default"`），或反向把 `:38` 改成 `""`。**两侧必须同时改**（桶不同，改一侧只会换一种不匹配）。
- **为什么不改**：这会改变生产行为，且触及 Redis key 布局（既有会话的替换表将读不到），
  属「改生产行为/对外契约 ⇒ 登记待裁决」。**本轮不动。**

---

## §四 §153 核对：哪些**不是**新发现

| 怀疑 | 前轮是否已覆盖 | 处置 |
|---|---|---|
| `""` 与 `"_unknown"` 同桶 | ✅ `cross_tenant_race_test.go:264` 已有专门用例 | **不登记** |
| 跨租户还原泄漏 | ✅ 同文件 `:229` 已有专门用例 | **不登记** |
| r59 F4「三条 unlocked 降级」 | 已被后续修复推翻，但**文档未订正** | 订正文档（§五） |
| `sanitize_map_ref` 写侧缺席（r59 F1） | ✅ 早已修复（`request_log_pipeline.go:1537`、`session_compressor.go:966`），80 号核实过 | **不重复计级** |

---

## §五 r59 F4 订正（与 235 号的 1c/F3 集中整理）

**被推翻的原文**（`docs/audit/2026-09-23-r59-s4-cache-compress-findings.md` F4）：
「sanitize offset 锁为 best-effort，**三条降级路径**重新打开跨进程 race」。

**当前代码（逐条亲验）**：

| 场景 | 现状 | 落点 |
|---|---|---|
| 锁获取失败 | `fmt.Errorf("acquire sanitize offsets: %w", err)` ⇒ 503 | `smart_sani_guard.go:308-311` |
| offsets 加载失败 | `fmt.Errorf("load sanitize offsets: %w", err)` | `:313-316` |
| 提交失败（含租约丢失） | `fmt.Errorf("persist sanitize mapping: %w", err)` | `:383-385` |
| 租约 token 生成失败 | `return sanitizeOffsetLease{}, err` | `:422-424` |
| 无租约的唯一情形 | `redis == nil \|\| sessionID == ""` ⇒ 不写 Redis，映射只活在同请求 ctx | `:417-419`、`:255-256` |

⇒ **fail-closed 成立**（错误上抛 ⇒ 503 ⇒ 原文不上游）。
`:414-415` 的注释「Redis-backed sessions **never** proceed unlocked」**与代码一致，不是残留**。

**订正方式**：在 r59 文档追加「订正 2」章节，与 235 号的「订正 1」并列，
写明①结论已过期及逐条证据；②时序（r59 写于 2026-09-23，后续轮次改为 fail-closed）。

---

## §六 本轮产出与负控

### 6.1 新增测试（3 个文件 / 6 个断言面，**零生产代码改动**）

| 文件 | 测试 | 钉什么 |
|---|---|---|
| `security/sanitize/map_key_normalisation_test.go` | `TestSanitizeMapKeyNormalisation` | 租户隔离赖以成立的 key 归一契约：`""` 与 `"_unknown"` **必须**同桶（是改写实现的别名，不是空串巧合）；真实租户**不得**与无身份桶碰撞；租户化 key 形态 |
| 同上 | `TestSanitizeMapKeyFallbacksDisagree` | **tripwire**：断言当前两侧回退**确实分歧**（`"default"` vs `""`），并在有人统一它们时 `t.Skip` 提示同步更新报告与待裁决 101 |
| `domains/streaming/sanitize_auth_fallback_test.go` | `TestPrepareSanitizeRequest_StaticKeyGate`（4 子用例） | 该函数**唯一的安全属性**：错误静态密钥 / 缺 Bearer 头 ⇒ 在 sanitizer 能持久化攻击者可控映射**之前**拒绝（`sanitize_auth.go:30-35`） |
| `domains/streaming/sanitize_auth_literal_pin_test.go` | `TestPrepareSanitizeRequestBindsDefaultTenantLiteral` | **AST 级**钉住 no-verifier 分支传入的字面量租户含 `"default"` |

### 6.2 负控（**两条全实测转红**）

| 负控 | 做法 | 实测 |
|---|---|---|
| **NC-F1** | 把 `sanitize_auth.go:38` 的 `"default"` 改成 `"d3fault"` | ✅ 转红：`实际是 [d3fault]`，并打出两个桶 `session:4eac81f00de6ccc2:sid:sanitize` vs 读桶 |
| **NC-F2** | **删掉整个 `WithAuthenticatedTenant` 调用，只留一条含 `"default"` 的注释** | ✅ 转红：`没有任何一个 WithAuthenticatedTenant 调用传字面量租户` |

⇒ **NC-F2 是本轮最有价值的一条负控**：它证明这条判据**不能被注释满足**，
而这正是「拼写级判据」与「AST 级判据」的分界。
探针已回收（`git diff domains/streaming/sanitize_auth.go` 为空）。

### 6.3 验证

- `security/sanitize` 整包：**全绿**（12.0s）。
- **14 包守卫**（`Makefile:87` 白名单）：**全绿**。
- `domains/streaming` 整包：**隔离跑 0 失败**。
- `gofmt -l` 与 `go vet` 对三个新文件均干净。

### 6.4 一条**如实登记的疑似 flaky**（非本轮引入，但必须留档）

在一次「16 个包并发跑」中，`domains/streaming` 出现 2 个红：
`TestStorageAuditSSEVisibleTextLatency/{terminal,EOF}`。

- **实测证据**：
  - 并发跑时：`heartbeat=0s visible_text=151.9309ms`；
  - 隔离重跑同一测试：**通过**（`heartbeat=4.9998ms visible_text=156.8671ms`）。
  - 读 `domains/streaming/storage_stream_audit_test.go:74` 的判据
    `if heartbeat == 0 || text < 150*time.Millisecond || text <= heartbeat`
    ⇒ 失败的是 **`heartbeat == 0`**（流式 chunk 的观测竞态），
    **不是** 150ms 延迟阈值——151.9ms 本应通过。
- **与本轮改动的关系**：本轮新增的三个测试是**纯函数/AST 判据，无任何 I/O 与计时**，
  且不修改任何生产代码 ⇒ 不构成因果。
- 🔴 **基线对照已做，但结果不足以定论（如实记录）**：
  把本轮三个测试文件移出后，跑**完全相同的 16 包并发组合**：
  `domains/streaming` **ok 122.942s，未复现失败**。
  ⇒ 但这构成的是 **「带我的文件：1 次失败；不带：1 次通过」**，
  **1 : 1 的样本对 flaky 判定没有任何统计意义**。
  ⇒ **本轮既不能断言「是我的」，也不能断言「与我的无关」**，
  只能确定三件事：① 失败条件是 `heartbeat == 0` 这个**流式观测竞态**，不是 150ms 延迟阈值；
  ② 隔离运行该测试稳定通过（156.9ms / 151.7ms）；③ 本轮新增测试无 I/O、无计时。
  ⇒ **建议下一轮做 N≥5 次的重复对照**（而不是再多跑一次），
  这也是我给自己立的规矩：**一次对照只够排除「必然相关」，不够排除「偶发相关」**。

---

## §七 诚实边界

- **无 PG / Redis / Docker，未起真进程、未执行任何 SQL**；全部为静态代码取证 + 本地单测。
- **零生产代码改动**；唯一新增文件是测试。
- **`domains/streaming` 的 AST 判据是「源级」的，不是运行级的**：它读 `sanitize_auth.go` 的 AST，
  能看见调用、看不见注释（NC-F2 已证），但**不执行**该函数。
  静态密钥门另有 4 个行为级子用例覆盖。
- **tripwire 是「断言当前分歧存在」的反常写法**，我明确承认这一点：
  它转红时含义是「有人只改了一侧，需要连带改另一侧」，**不是「有 bug」**。
- **`keyInfo` 是否在其它路径上可能为 nil**：我只核了 static-key 分支（`:2359-2372`）与
  响应侧构造点（`:5604-5607`）；`messages.go:973` / `responses.go:973` 两个 native 构造点
  **未逐行核其租户来源**。
- **ctx 本地表能否在所有响应路径上取到，未验证**（`:256` 写入、`:712` 读出已确认，
  但中间是否被重建未追）。这正是 F2 只定 P3 的原因之一。
- **§153 核对基于全量 `git grep` 的分类阅读，不是逐函数通读**。
- `go test ./...` 全量未跑；**CI 从未运行**。
- `security/sanitize` 与 `domains/streaming` **均不在 `GUARD_PACKAGES` 内**
  （沿用**待裁决 100** 的口径，不重复登记）。

---

## §八 playbook

### §197-A **「读不到」不是「测不了」——但要先证明你确实读不到**

我想断言 `prepareSanitizeRequest` 绑定的租户值，跨包读不到
（`security/sanitize/input_protocols.go:24 authenticatedTenant` 非导出）。
我没停在这儿，而是把「读不到」这件事本身变成了可测的东西：
**用 AST 去看调用表达式的实参**。实参是字面量，AST 看得见；是变量，AST 看不见并如实报告。
⇒ 跨包的可见性缺口，**常常可以用「换一个观察层」补上**，而不是只能记成诚实边界。

### §197-B **判据按构造恒为真时，必须删掉而不是「修一修」**

我写的 `sameContextValue` 用了另一个包的私有 `marker{}` 类型做 key，
两边都取不到 ⇒ `aHas == bHas` 恒真 ⇒ 断言恒成立。
这类判据的特征是：**第一次跑就绿，而且怎么改代码都绿**。
⇒ 与「装饰性规则」同族，但更隐蔽：它至少会**执行**。
⇒ **判别法**：写完先问「如果被测性质**完全不存在**，这条断言会红吗？」
答不上来就删。本轮实测它确实红不了 ⇒ 删除。

### §197-C **负控要覆盖「删掉接线只留注释」这一形态**

NC-F2 是我这一轮唯一**主动设计**的负控：把调用删掉、只留一条含同样字面量的注释。
如果判据是文本匹配，它会**继续绿** ⇒ 门已经没牙了而无人知晓。
⇒ **凡是「新写了某处调用/赋值」的判据，都要问一句：把这行删掉、只留一条描述它的注释，它还会红吗？**
⇒ 这条负控比「改字面量」（NC-F1）更值钱，因为它测的是**判据的观察层选得对不对**，
  而不是「判据的内容对不对」。
