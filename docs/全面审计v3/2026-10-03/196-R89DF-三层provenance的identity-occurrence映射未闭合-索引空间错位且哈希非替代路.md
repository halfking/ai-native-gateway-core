# 196 号 · R89-DF —— 挂了三轮的「三层 provenance identity/occurrence 映射」**没有闭合**：索引空间错位，**哈希也不是替代路**

> **日期**：2026-10-03
> **轮次**：R89-DF（第 96 轮，审计第 196 号）
> **类型**：**闭合 objective 点名项**（195 号 §七第 1 条 / 194 号之前多轮顺位）
> **改动生产代码**：仅**注释**（`types.go` / `sanitize_info.go` 写明坐标系，零行为变更）
> **改动测试**：新增 `domains/hooks/compression/provenance_seam_pin_test.go`（4 条）
> **改动数据库**：无
> **上一轮**：195 号（输出合规 lane 漏两类载体，已根修）

---

## 〇、起手

195 号收尾时把这条列为下一轮第一顺位，理由是它**已挂三轮**且正落在 objective 的
原文要求上（"会话内容的三层缓存数据：original → sanitized → compressed 三层
provenance 的完整 identity/occurrence 映射"）。

**先查前轮是否已结论**（避免把已登记的当新发现）：

| 前轮 | 结论 | 与本轮的关系 |
|---|---|---|
| 80 号（R88N） | "生产链路与 V2 接入都存在、白名单设计正确，**未发现缺陷**" | 只核**接入**（写链 + 白名单），**没核坐标系** |
| 21 号（2026-09-29） | 四要素「identity/occurrence 元数据级映射」判为闭环；「缺口仅消费面级：**G2（P3 provenance 只写不读）**」 | 「只写不读」**已登记**，本轮不重复报 |
| 22 号 | 记 `SessionCache.Set` 是三层合并态的唯一咽喉 | 收口点，非坐标系 |

⇒ **「只写不读」不是新发现；「两套坐标能不能 join」是 80 号与 21 号都没碰过的问题。**
`git grep SanitizedIndex -- docs/` **零命中** ⇒ 全仓**没有任何文档**讨论过它。

---

## 一、🔴 F1：两个索引空间错位，**且偏移量没有公式可依赖**

### 1.1 两套坐标系

| 结构 | 字段 | 索引的是 | 证据 |
|---|---|---|---|
| `SanitizedMessageRef` | `RawIndex` / `SanitizedIndex` | **客户端请求体**的 `messages` 数组 | `security/sanitize/input_protocols.go:160-180` 逐条枚举客户端数组；`:188` `RawIndex: index, SanitizedIndex: index` **同一个循环计数器赋值** ⇒ 两值恒等，且 sanitize 只改内容**不改长度/顺序** |
| `AlignmentInfo` | `OriginalIndex` | **组装后出向体**的 `messages` 数组 | `session_compressor.go:591` / `:632` `before := outboundBody`；`outboundBody` 来自 `:380 BuildOutboundMessages(...)`；`diff.go:142-144` `merged = lastMsgs ++ deltaTail` |

⇒ **`outboundBody` 含客户端本轮没重发的缓存历史** ⇒ 两个坐标系没有任何换算关系。

### 1.2 偏移量（**实测**，不是推理）

偏移公式（由 `diff.go:141-144` + `findDeltaAnchor`（`diff.go:195-227`）推出）：

```
assembled(clientIndex) = len(lastMsgs) + (clientIndex - anchorEnd)
offset = assembled - client = len(lastMsgs) - anchorEnd
```

- **未压缩**（`lastMsgs` 是客户端数组前缀）：`anchorEnd = len(lastMsgs)` ⇒ **offset = 0**。
- **已压缩**（`lastMsgs` 含网关摘要，`findDeltaAnchor` 走 `:216-226` 那条「在客户端数组里找保留后缀」分支）：`anchorEnd` 是**保留后缀在客户端数组中的起点**，与 `len(lastMsgs)` 无固定关系 ⇒ **offset 随会话状态变化**。

**执行验证**（`provenance_seam_pin_test.go`，非流式最常见的「压缩后再来一轮」形态）：
客户端 7 条（m0..m6），缓存是上一轮的压缩体 `[smm_v1 摘要, m3, m4]`：

```
assembled = [smm_v1 摘要, m3, m4, m5, m6]     ← 5 条，缓存优先、增量在后
m5: client index 5 → assembled index 3        ← offset = −2
```

⇒ **按 `ref.SanitizedIndex == alignment.OriginalIndex` 做 join，会把 m5 的溯源
读到 m2 上**（client 3 = m2）。静默错位，不报错。

### 1.3 🔴 F2：哈希**也不是**替代路

「既然索引不可靠，按哈希 join 总行吧」——**也不行**。两边是**两个不同的函数**：

| 字段 | 实现 | 输入归一化 | 输出 |
|---|---|---|---|
| `AlignmentInfo.Hash` | `msgHash`（`diff.go:265-276`） | `json.Unmarshal` + `json.Marshal` ⇒ **Go 按字典序重排对象键** | sha256 的**前 16 字节** = **32 hex** |
| `SanitizedMessageRef.RawHash/SanitizedHash` | `MessageFingerprint`（`sanitize_info.go:94` = `sha256Hex`） | `json.Compact`（`input_protocols.go:196-200`）⇒ **只压空白、键序保持原样** | **完整 64 hex** |

⇒ **同一条消息，两个字符串，永不相等。** 差在两处：归一化方式与截断长度。
所以「按哈希 join」这条看似自然的替代路**同样走不通**，除非改其中一边的算法。

### 1.4 定级与影响面（诚实标注）

- **今天没有故障**：`SanitizedMessageRefs` **零生产消费方**（`git grep SanitizeMessageRefs -- "*.go" ":!*_test.go"` 只命中写路径与两处 `len()` 守卫），**这一条前轮已按 P3 登记**（21 号 G2），本轮不重复计级。
- **真实代价是「陷阱」而不是「故障」**：objective 点名的映射**没有闭合**，
  任何按索引或按哈希写 join 的后续实现都会**静默错位**。
  ⇒ 定 **P2**（不是 P1：无当前故障、无数据损坏；也不是 P3：它是一枚指向
  objective 明文要求的地雷，且**没有任何门会发现它**）。
  ⇒ **本轮按「无消费方 + 有陷阱 + 需设计决策」登记为待裁决 82，不擅自统一哈希算法**
  ——改 `msgHash` 的长度会影响 `AlignmentMap` 的持久化格式与既有 session 缓存条目，
  属行为/格式变更，不该由审计轮单方面决定。

---

## 二、本轮做了什么（把结论变成可执行的东西）

1. **两条事实各有钉测**（`provenance_seam_pin_test.go`，4 条），断言取**具体数值**
   （offset = −2、哈希长度 32/64、assembled 的完整消息序列），
   **不是「不相等」这种恒亮断言** ⇒ 任何一侧的真实变更都会转红。
2. **两处结构体补坐标系注释**（`types.go` / `sanitize_info.go`）——
   纯注释、零行为变更，作用是让下一个接手的人不必重新推一遍（我推了三轮才推完）。

### 2.1 变异验证（**其中一次抓出我自己判据的盲区**）

| 变异 | 期望 | 实测 |
|---|---|---|
| **M1** `msgHash` 改成完整 sha256（模拟「有人统一了哈希空间」） | 红 | ✅ `AlignmentInfo.Hash must stay a 32-hex short fingerprint, got 64 chars` |
| **M2** 合并顺序倒置（`deltaTail` 在前、`lastMsgs` 在后） | 红 | ❌ **第一次没红** |

**M2 没红的原因（这是本轮最值得记的一条）**：我最初的夹具里
`deltaTail = clientMsgs[anchorEnd:] = clientMsgs[5:]` 是**空的**
（客户端重发的内容正好等于缓存里已有的）⇒ `merged = lastMsgs ++ []`
与 `merged = [] ++ lastMsgs` **完全相同** ⇒ **合并顺序根本没参与计算**。

⇒ 又一次「**判据不在被测性质上**」。修法不是改报告，是**补一个非空增量的夹具**：
客户端追加两条新消息（m5/m6）⇒ `DeltaCount=2` ⇒ 顺序可观测 ⇒
断言 assembled 的**完整消息序列**（而不只是条数）⇒ M2 重做后精确转红：

```
assembled[0] = "m5", want "[smm_v1: earlier turns" — the assembly order is
cache-then-delta and it is what fixes the index offset
```

---

## 三、证伪与排除（含我自己差点发出的）

| # | 本来以为 | 查了什么 | 结论 |
|---|---|---|---|
| D1 | `SanitizedMessageRefs` 零消费方是新发现 | 21 号（2026-09-29）已按 **G2 P3** 登记 | ❌ **撤回**（不重复计级） |
| D2 | 80 号已核实过这一项，不必再查 | 80 号核的是**接入**（写链 + V2 白名单），`git grep SanitizedIndex -- docs/` 零命中 ⇒ 从未讨论坐标系 | ✅ 成立，本轮补的是它没覆盖的那一半 |
| D3 | 两套哈希「其实一样，只是截断显示不同」 | `msgHash` 走 Unmarshal+Marshal（**键序被 Go 重排**），`MessageFingerprint` 走 `json.Compact`（**键序原样**）⇒ 不只是长度差 | ✅ 成立，且是**两处**独立差异 |
| D4 | offset 只在「客户端只发增量」时出现 | 那种情况 `findDeltaAnchor` 返回 `(0,false)` ⇒ 回落到 `newSessionResult` ⇒ assembled = clientBody ⇒ **offset 恰好 0** | ❌ **撤回**。真正的错位场景是**压缩后**（含网关摘要）那条分支 |
| D5 | 三层 provenance 一致性校验（`threetier_hook.go` / `DetectMisalignment`）会抓到错位 | 它只比 **token/消息条数**（`align.go:64-105`），**不涉及任何索引对应** | ❌ **撤回**。这正是「没有门会发现它」的含义 |
| D6 | `offset` 有公式 ⇒ 可以运行时换算 | 公式依赖 `len(lastMsgs)` 与 `anchorEnd`，两者都是**当轮缓存状态**，`SanitizedMessageRef` 里**不存** | ❌ **撤回**「可换算」。存下来的 refs 无法重建自己所在的坐标系 |

---

## 四、登记不修

| 编号 | 级别 | 内容 | 为什么不修 |
|---|---|---|---|
| **待裁决 82** | **P2** | 三层 provenance 的 identity/occurrence 映射**未闭合**：① 索引空间错位且偏移不可由 refs 重建；② 哈希函数不同 ⇒ 哈希 join 也不通 | 闭合它要**选一条路**：(a) 统一指纹算法（改 `msgHash` 长度 ⇒ 影响 `AlignmentMap` 持久化格式与既有缓存条目）；(b) 让 sanitizer 按**出向空间**产出 refs（需在组装后重算）。两者都是**格式/语义变更**，须与「是否真的要一个消费方」一起裁决——**在第一个消费方出现之前，这笔账可能根本不必付** |
| P3 | P3 | `domains/hooks/compression/` 有 **3 个 HEAD 既有的 gofmt 脏文件**（`alignment_reverse_test.go` / `responses_alignment.go` / `threetier_hook_test.go`） | 与本轮改动零关联，且并发会话活跃时批量改格式只会制造合并摩擦。本轮**只保证自己碰过的 3 个文件 gofmt 净** |
| P3 | P3 | 本轮**未起真进程 / 未跑真库 e2e / 未做部署验证** | 同 195 号：结论来自代码 + 可执行判据，**未**经部署面验证 |

---

## 五、验证命令与结果（可重放）

```bash
go test ./domains/hooks/compression/ -run ProvenanceSeam -count=1   # ok（4 条）
gofmt -l domains/hooks/compression/types.go \
       domains/hooks/compression/sanitize_info.go \
       domains/hooks/compression/provenance_seam_pin_test.go         # 净
go vet ./domains/hooks/compression/                                  # exit 0
go test ./domains/hooks/compression/ -count=1                        # ok
go build ./...                                                       # exit 0

# 坐标系取证（可重放）
git grep -n "SanitizedIndex\|OriginalIndex" -- "*.go" ":!*_test.go"
git grep -n "SanitizeMessageRefs"  -- "*.go" ":!*_test.go"   # 只命中写路径 + 两处 len()
git grep -n "SanitizedIndex" -- "docs/"                        # 零命中：全仓从未讨论过

# 变异 M1（应红）
#   msgHash: fmt.Sprintf("%x", h[:16]) → h[:]
#   ⇒ AlignmentInfo.Hash must stay a 32-hex short fingerprint, got 64 chars
# 变异 M2（应红，需非空增量夹具才可见）
#   diff.go:142-144 两行 append 顺序对调
#   ⇒ assembled[0] = "m5", want "[smm_v1: earlier turns"
```

**未在本机执行**：无 Redis / 无 PG / 无 Docker ⇒ 未起真进程、未跑真库 e2e、
**未做部署级验证**（诚实登记，不假装覆盖）。

---

## 六、下一轮顺位

1. **待裁决 82 的裁决前提**：先问「**谁会是第一个消费方**」。
   若观测面（`request_logs.compression_meta` → `sessions_v2.metadata`）确实要按消息
   粒度回溯 sanitize↔compression，则必须先统一坐标系；
   若只做条数级观测，**当前状态已够用，不付这笔账**。⇒ **先有需求，再有修法。**
2. **待裁决 81 取数**（195 号登记）：`cachedBodyPassesGuard` 失配率，真库。
3. **待裁决 79 产品裁决**（195 号登记）：messages / responses / gemini 三面补不补预算闸。
4. **待裁决 80**（195 号登记）：抓一次上游 Responses SSE 的 `output_item.*` 帧序列。
5. **提交态测试**（195 号登记）：`3483152cb` 需在独立 worktree checkout 后复跑。

---

## 七、playbook 新增

- **§89 一个字段的坐标系是它契约的一部分**——
  `RawIndex` / `SanitizedIndex` / `OriginalIndex` 三个名字都像「消息下标」，
  但**分属两个数组**。写「消息级映射」时必须回答三问：
  **① 索引哪个数组？② 那个数组是谁拼的（缓存优先还是增量优先）？③ 偏移量能否由存下来的数据重建？**
  本例第③问的答案是**不能** ⇒ 所以「存下来了」不等于「以后能 join」。
- **§90 夹具要让被测性质真正参与计算**——
  本轮 M2 变异**没让判据转红**，原因是夹具的 `deltaTail` 为空，
  被测的「合并顺序」在空集合上是**不可观测的**。
  ⇒ 写「顺序/拼接/优先级」类判据时，先问一句「**我的夹具里这个量非零吗**」；
  再用一次变异反查。**判据全绿 + 变异不红 = 判据没在测那个性质。**
