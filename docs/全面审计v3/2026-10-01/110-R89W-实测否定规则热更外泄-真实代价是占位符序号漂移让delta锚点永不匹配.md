# 110 号｜R89-w：实测「sanitize 规则热更后旧轮次会不会被误复用」——否定答案，但成因链带出一个真实的口径缺陷

- 日期：2026-10-01
- 轮次：R89-w
- 起点：109 号留下的未证实项「实测 sanitize 变更后旧 Compressed 是否被误复用」（当时仅从键结构推断，未实测）
- 方法：生产链路复刻的临时实证测试（跑完即删）+ 真库交叉核对
- 结论一句话：**不会外泄；但让「不会外泄」成立的那条机制，会让含脱敏占位符的会话永远命中不了 `delta_append`，`compression_strategy` 列因此在两类会话上口径不同。**

---

## 一、原始问题与实测前的推断

问的是：运维在会话进行中热更了敏感规则（新增一条规则），**此前轮次已经脱敏并缓存的出站体，会不会被原样复用、把新规则本该拦下的明文继续发给上游？**

109 号从键结构推断的答案是「会」：`SessionState.LastOutboundHash = sha256(outboundBody)`
（`session_cache.go:151`、`session_compressor.go:999`），而这个哈希是由**被存进去的那同一份字节**算出来的
——`CommitFinal` 在 `session_compressor.go:976-986` 先把 `finalBody` 写进 `res.OutboundBody`，
再 `sha256Hex(outboundBody)` 存进 state。**自洽的哈希永远校验通过，它在结构上不可能发现
「产生这些字节的脱敏器已经不是当前那个了」。** 当时据此准备按泄漏方向定级。

---

## 二、实测：否定答案

### 2.1 实验设计

复刻生产链路（`domains/streaming/handler.go:3934-3940`）：

```
客户端 body
  → SanitizeInputMiddleware（脱敏，占位符序号按会话级 offset 递增）
  → SessionCompressor.Prepare（diff/合并/压缩）
  → CommitFinal(res.OutboundBody)
  → 转发
```

- Turn 1：消息里含手机号 + 一个**当时还不是敏感信息**的工牌号 `BADGE-778899`。
- 运维热更：往 `configs/sensitive_patterns.yaml` 加一条 `BADGE-\d{6}` 规则，调
  `PatternDetector.ReloadFromFile()`（等价于 `admin/sensitive_words_handler.go:46-52` 的热更接口）。
- Turn 2：客户端重发全量会话 + 一条新消息。
- 决定性断言：转发给上游的 merged body 里，`BADGE-778899` 是否仍为明文。

**规则集在生产确实可热更**（这条是读代码坐实的，不是假设）：
`cmd/gateway/goal_control.go:525` 用 `configs/sensitive_patterns.yaml` 构造生产 detector；
`admin/sensitive_words_handler.go:52` 在管理接口里调 `ReloadFromFile()`。

### 2.2 实测输出

```
TURN1 sanitized = {"messages":[{"content":"my phone is {SENSITIVE:phone:1} and my badge is BADGE-778899",...}]}
TURN1 forwarded = （同上）

PROBE new rule live, sanitized = badge {SENSITIVE:secret:1}      ← 反向对照：新规则确实生效
TURN2 sanitized = {"messages":[{"content":"my phone is {SENSITIVE:phone:2} and my badge is {SENSITIVE:secret:1}"},{"role":"user","content":"and my email is a@b.com"}]}
TURN2 forwarded = （与 TURN2 sanitized 逐字节相同）
```

**不外泄。** 转发体里工牌号已被新规则替换为占位符。

### 2.3 为什么成立——真正的机制

关键不在压缩层做了什么，而在**占位符序号跨轮递增**：

- `sanitizer.go:141-162`：`typeIndex` 每轮从 1 重新自增，再叠加调用方传入的会话级 `offset`。
- 中间件传入的 offset 来自 Redis 的会话已有计数 ⇒ **同一段历史文本，第 1 轮是
  `{SENSITIVE:phone:1}`，第 2 轮重新脱敏后变成 `{SENSITIVE:phone:2}`**。
- `diff.go:210` 的 delta 锚点是**逐字节序列比对**（`sameMessageSequence`）⇒
  含占位符的历史消息**每轮都对不上** ⇒ `findDeltaAnchor` 返回 false
  ⇒ `BuildOutboundMessages` 走 `newSessionResult`（`diff.go:188-193`，置 `IsNewSess=true`）
  ⇒ 转发的是**本轮重新脱敏过的完整客户端 body**。

**⇒ 「规则热更后旧轮次被重新脱敏」这个安全属性，是序号漂移的副产品，不是任何一处显式设计。**

---

## 三、顺着成因链走出来的真实后果

拿到否定答案后没有停在结论上（见 playbook §18），而是问「让这个否定答案成立的机制，还带来什么别的后果」。

### 3.1 P2：`compression_strategy` 在两类会话上口径不同

`IsNewSess=true` 会让 `session_compressor.go:401 / 450 / 644` 三处
`else if !diffResult.Unchanged && !diffResult.IsNewSess` 分支全部跳过 ⇒
阈值以下不触发窗口压缩时，`res.CompressionStrategy` 为空。

实测（B 臂，带反向对照）：

| 臂 | 会话内容 | turn2 `outbound_len` | turn2 `strategy` |
|---|---|---|---|
| 实验组 | 含手机号/邮箱占位符 | 0 | `""` |
| 反向对照 | 结构相同、无占位符 | 145 | `delta_append` |

写入侧是通的（`handler.go:4003-4004` → `request_log_pipeline.go:1388-1389`），
所以这不是「没接线」，而是**同一个列在两类会话上装的不是同一种事实**：
无 PII 会话记「本轮做了一次增量追加」，含 PII 会话什么都不记。

后果：任何按 `compression_strategy` 做策略分布 / 压缩率的面板，
会**系统性把全部含 PII 的会话排除在外**，且排除得毫无痕迹——
这些会话在压缩看板上是不可见的，不是「压缩失败」，是「没进统计」。
按 objective 的「反馈闭环」，这批会话在压缩维度上闭环断开。

### 3.2 被证伪的假设：不是性能劣化

曾推测「每轮重算全量比对 ⇒ 长会话 O(n) 劣化」。**实测不成立**（120 条消息历史，末轮耗时）：

```
pii=true   last_turn=543.708µs
pii=false  last_turn=805.333µs     ratio=0.68x
```

含 PII 的臂**更快**——因为锚点在第一条消息就失配并立刻返回，
而无 PII 的臂要老老实实走完 120 条的序列比对。**假设被数据推翻，不作为结论。**
（`handler.go:3288` 记录的「800+ 消息让 Prepare 花 5-10 秒」是另一条路径，本轮不据此下结论。）

### 3.3 被推翻的第二版猜测：`outbound_body` 并不会因 PII 丢失

一度怀疑「`res.OutboundBody` 为空 ⇒ `logCtx.OutboundBody` 为空 ⇒ 出站体证据不入库」。
读全链后推翻：`handler.go:4183-4186` 在 `CommitFinal` 成功分支里用
`bodyBytes`（实际转发的那份）**覆盖**了 3988 行的赋值。
（这正是 conventions §10「查调用方 / 读全链」拦下的一次错误结论。）

残留一个窄口子，登记为 P3：`CommitFinal` **失败**时只 `slog.Warn`
（`handler.go:4178-4181`），不走覆盖分支，`logCtx.OutboundBody` 保留 3988 行的空值，
且不像同函数里 tools 还原失败那样调 `recordDataLoss` ⇒ **出站体证据静默丢失且无数据丢失告警**。

---

## 四、真库核对：本实例无法作为量化依据

```
strategy      | rows
(空)          | 2467          ← compression_strategy 100% 为空
total=2467  pii_rows=0  session_rows=874
```

- `sanitizer_mutations` 非空行数 = **0** ⇒ 本实例**没有任何 PII 流量**，
  §3.1 的两条臂在真库上不可复现。
- `compression_strategy` 2467 行全空，连 874 个有 `gw_session_id` 的行也没有 `delta_append`。
  写入代码是通的（§3.1），所以**只能记为本实例事实**（开发实例、会话短、未过阈值），
  **不报「生产也是这样」**。生产上 `delta_append` 的实际占比需真实负载数据，登记为待跟进。

---

## 五、对待裁决 35 的直接影响（最重要的一条）

待裁决 35 已被两次收窄为「只需确认两个保证」，第一个是「**sanitize 变更 → 产物失效**」。

**本轮把这个保证的成色查清楚了：它目前是靠副作用成立的。**

- 现状安全，是因为占位符序号跨轮递增把 delta 锚点顶掉了，于是每轮重新脱敏。
- 也就是说：**只要有人「优化」delta 锚点**——改成忽略序号、按语义匹配、或按
  `SanitizedMessageRefs` 做稳定身份比对——这个安全属性会**静默消失**，
  缓存复用就会重新变成「按旧规则脱敏的字节继续外发」。
- 这是一次**性能/正确性优化会打开数据泄漏面**的典型形状，而且不会有任何告警。

⇒ 因此对 35 的第一个保证，必须从「确认它成立」升级为「**把它做成显式契约**」：
在 `SessionState` 里落一个 sanitizer 规则集指纹（规则文件内容哈希 / 版本号），
与 `LastOutboundHash` 并列参与复用判定；指纹不一致即强制整轮重新脱敏。
这样它就不再依赖 delta 锚点的副作用，也就不受后续优化影响。

（35 属待裁决项，本代理不擅自动手，仅补上这条定性依据。）

---

## 六、编号与去向

- 本轮**不新增**待裁决项；§5 是对既有**待裁决 35** 的定性补强，不是新条目。
- §3.3 的 P3 窄口子并入台账 §4.2 待跟进（`CommitFinal` 失败无 `recordDataLoss`）。
- §4 的「生产 `delta_append` 占比」并入 §4.2 待跟进。

---

## 七、本轮方法论产出（playbook §18）

**实测拿到否定结论时，从「成因链」继续走，不要在原问题上打转。**

- 事件：问「规则热更后旧 Compressed 会不会被误复用」。实测：**不会**。
- 两次错误后续：
  1. 凭键结构推断「会外泄」并准备据此定级（未跑就说）；
  2. 拿到否定答案后改口成「`outbound_body` 丢失」和「每轮 O(n) 性能劣化」——
     前者只读到代码的一半（漏了 `handler.go:4183` 的覆盖写），
     后者根本没测就估了方向。
- 正确做法：拿到否定答案后问「**让这个否定答案成立的机制是什么，它还带来什么别的后果**」。
  本例机制是占位符序号跨轮递增，它同时导致 delta 锚点失配 ⇒ `compression_strategy` 口径差（§3.1）。
- **判据：实测得到的否定结论，价值在于它锁死了哪条机制链。找不到这条链，说明测的是个孤立现象。**
- 与既有条目的关系：§15.2 讲「异步实验的 0.00s 耗时是实验没跑起来」，
  §18 讲「实验真跑了、结论为负之后该往哪走」——两者合起来才是一条完整的方法。
