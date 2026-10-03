# 229 号｜R89-ET：`adapter/unified` vs 现役适配层 —— 覆盖面是**真子集**，而它早在 2026-08-30 就已声明废弃

- 日期：2026-10-03
- 轮次：R89-ET
- 起点：执行 115 号（台账 §4.28 L1459）**明文列出的下一轮必做项 ③**
  「`adapter/unified` vs `domains/transformation` 覆盖面」
- **新增待裁决 2 条（95、96）+ 订正 115/148 号两处定性 + 文档订正 2 处 + 守卫新增 3 条测试**
- **零生产代码改动**（两个负控探针均已回收并复核工作树干净）

---

## 一、结论先行：覆盖面对比的答案是「unified 是真子集，删除不损失任何能力」

| | `adapter/unified`（800 行生产） | 现役 `internal/ir` + `domains/transformation` |
|---|---|---|
| 协议 | **只有 OpenAI + Anthropic** | **5 个**：anthropic / gemini / ollama / openai / responses（`internal/ir` 下 `parse_*.go` + `serialize_*.go` 齐备） |
| 方向 | 请求 + 响应（**各 2 方向**） | 请求 + 响应 + **流式** |
| 流式 | `StreamAdapter` 接口与 `UnifiedStreamChunk` 等类型**已定义但零实现**；`GetStreamAdapter(...)` 运行时**必返** `adapter does not support streaming`（`registry.go:135-138`） | `ParseGeminiStreamChunk` / `SerializeAnthropic` 等 + 活跃桥 `anthropic_bridge.go`/`responses_bridge.go` |
| 压缩/净化 | **无** | `ctx_compress.go`(718) / `responses_compress.go`(362) / `sanitizer.go`(755) |
| 依赖方向 | **既不 import `internal/ir`，也不 import `domains/transformation`** ⇒ 孤立的并列草图 | —— |

⇒ **无任何能力项是 unified 的补集** ⇒ 按 C 类（可删）处置，**不损失能力**。
⚠️ 这与 227 号 `promptinjection/enhanced` 的处置**方向相反**，原因也写清楚了：
那次是「能检测的那套零引用、现役那套缺能力」⇒ 修法是**搬能力**；
这次是「零引用的那套是现役的真子集」⇒ **删掉即可**。

---

## 二、⚠️ 订正 115 号：它不是 B 类，是 **C 类（已被否决的草图）**

115 号（台账 §4.28）把 `adapter/unified` 归为「**B（真孤儿）**：能力已在、从未接线」，
并把它排进「需确认是否接线」队列。

**B 类的含义是「值得接线」**。而 `interface.go` 的包头（全文 14 行，废弃声明占 1–13 行）写的是：

> `:1-2` 「Package unified was a **2025 sketch** of a second, provider-agnostic IR
> (Adapter / UnifiedRequest / UnifiedResponse) sitting beside internal/ir.」
> `:4` 「**Deprecated 2026-08-30: not the canonical IR.**」
> `:6` 「**The canonical protocol surface is `internal/ir` + `domains/transformation`.**」
> `:7-8` 「Production protocol work (**including the Responses SSE event stream**) must
> continue through those packages.」
> `:8-10` 「This package is retained only for regression tests …; no production code
> (cmd/gateway, internal/ir, domains/transformation) imports it.」

`registry.go:145-149` 另有一份：

> 「Deprecated: package unified is non-canonical IR. The init() side effect was
> left in place only so that historical test binaries that import this package
> can still resolve …」

⇒ **设计本身已被否决**（「not the canonical IR」），不是「能力写好了没人接」。
⇒ 115 号把这个包排进「是否接线」的队列，**等于把一个已否决的方案重新提上议程**。
⇒ 订正为 **C 类**，并入「死代码 7 包」（`parallel-implementations-comparison.md:34` 的 P-10）。

### ⚠️ 115 号漏掉的三处文档登记（它 0 次引用这些）

| 文档 | 登记内容 |
|---|---|
| `docs/01-requirements/functional/FEATURES_CATALOG.md:22` | 「统一适配器（旧尝试）\| `adapter/unified` \| ⚪ **死代码**（0 外部引用，**2026-08-30 已声明废弃**）」 |
| `docs/03-design/01-architecture/architecture/ARCHITECTURE.md:120` | 「`adapter/unified` 是**已废弃旧尝试**（0 外部引用，`PARALLEL`→待删，见 P-01 附注）」 |
| `docs/03-design/01-architecture/parallel-implementations-comparison.md:34,111` | P-10 死代码 7 包之一，「2026-08-30 头注释已声明废弃」 |
| `docs/03-design/01-architecture/unified-optimization-prompts.md:37` | 「**特别核对 adapter/unified**：其 interface.go 头部 2026-08-30 已声明废弃；**若有非测试引用出现（说明期间被复活），停手登记**」 |

⇒ 最后那一条是**一条现成的、可机械执行的检查要求** ⇒ 本轮据此建门（§五）。

---

## 三、⚠️ 订正 148 号：两处定性都不成立

148 号用 147 号的 **§41 三分法**（① 承诺已接线＝缺陷 ② 明确声明未接线＝非缺陷 ③ 两者都没说＝需补文档）
扫了 24 个零引用包，结论是「bucket ① 的新发现 = **0**，基准率 1/25，**这种文档失真是个例**」。
它把 `adapter/unified` 归入 **bucket ③**，并附了一条 P3：

> 「**`adapter/unified` 的 `globalRegistry` 在运行时恒为空** —— 没有任何生产代码调用
> `unified.Register(...)`。将来接线时若忘记先注册，`GetAdapter` 会恒返回 not found。」

**两条都被推翻**（本人逐条复核）：

1. **「恒为空」是错的**：`registry.go:150-152` 有
   `func init() { Register(NewOpenAIAdapter()); Register(NewAnthropicAdapter()) }`
   ⇒ 全局注册表**启动时就预填 2 项**。
2. **bucket ③ 是错的**：它**明确声明了废弃**（bucket ② 的最强形态）。
   而 147 号立的 §41 要求「判零引用包时**先把包注释读完**」——
   废弃声明就在 `interface.go` 的**第 1–13 行**，不可能读不到。
   ⇒ **148 号对它违反了自己引用的方法**。
   ⇒ 连带影响：148 号「24 个包里只有 1 个 bucket ①、这是个例」的**基准率论证**建立在一次错误分桶之上。

⇒ 与 228 号 §十三（4.142 台账）那条并列，这是本项目第二次出现
**「按 §41/§153 引用了方法，但没有真正执行它」**。

---

## 四、⚠️ 订正 `docs/MODULES_GUIDE.md`（本轮已直接改，零逻辑）

`:305-321`（改动前）把**已废弃的草图**描述成现役适配层：

| 失真处 | 实际 |
|---|---|
| `:310-312` 列 `gemini.go` / `responses.go` / `converter.go` 为「关键文件」 | **三个文件都不存在**（`git ls-tree` 只有 5 个 `.go`） |
| `:318-321` 表格把 OpenAI/Anthropic/**Gemini**/**Responses** 四协议标为 **CURRENT** | 只有 **OpenAI + Anthropic**，且均标 **DEPRECATED** |
| 同表「SSE流式 ✅」×4 | **流式零实现**，`GetStreamAdapter` 必返不支持 |

⚠️ **危害不是美观问题**：objective 明确点名「适配多个通讯协议 chat/message/response」，
而这份模块指南会把读者**指向错误的包**去找 Gemini/Responses 适配
（真实位置是 `internal/ir/parse_gemini.go` / `parse_responses.go`）。

**已改为**：标注「已废弃，勿当现役适配层使用」+ 指向权威包 + 真实文件清单 + 真实支持范围表
（Gemini/Responses 明确标注「**本包无此能力**」）。

---

## 五、守卫：`internal/sqlguard` 新增 `deprecated_adapter_guard_test.go`（集合仍 14 包，零接线成本）

| 测试 | 失败条件 | 为什么是这三条 |
|---|---|---|
| `TestUnifiedStaysUnimportedOutsideTests` | 出现**非测试** import 方 | 直接实现 `unified-optimization-prompts.md:37` 的「停手登记」要求。用 **`go/parser` 解析 import 块**而非文本搜索：废弃声明本身就在注释里点名了这个包，文本搜索会把「提到」与「导入」混为一谈 |
| `TestUnifiedDeprecationBannerSurvives` | 包头缺 `Deprecated 2026-08-30` / `not the canonical IR` / `internal/ir + domains/transformation` | 该包**无自身逻辑** ⇒ 注释是唯一的意图记录（§175）。删掉它，本包就重新变成「无解释的孤儿」——**这正是 115 号误归类的成因** |
| `TestModulesGuideDoesNotAdvertiseUnifiedAsCurrent` | 协议表数据行出现 `CURRENT`，或「关键文件」列出磁盘上不存在的 `.go` | 闭合 §四 的文档回路 |

### 两条负控（全部实测）

| 负控 | 手法 | 结果 |
|---|---|---|
| **NC-N** | 造 `cmd/gateway/zz_probe229.go` import 该包 | ✅ 转红，精确点名 `cmd/gateway/zz_probe229.go`，并引用 `unified-optimization-prompts.md:37` 提示「被复活、需人工裁决」；探针已回收 |
| **NC-O** | 删掉 `interface.go:4` 的废弃声明那一行 | ✅ 转红，逐条点名缺失的 2 个关键串；已还原，`git diff --stat` 复核为空 |

### ⚠️ 门的第一版红了，而红的是尺子

`TestModulesGuideDoesNotAdvertiseUnifiedAsCurrent` 第一版对**整个小节做子串搜索** `CURRENT`，
结果命中的是**我自己刚写的订正文字**：「此前误标为四协议 CURRENT 且全部 SSE ✅」——
**那句里的 CURRENT 是在描述被订正的错误，不是在宣传它。**

⇒ 修法：判据**收窄到 Markdown 表格数据行**（以 `|` 开头）——
  「宣传」只发生在协议支持表里，订正说明发生在正文里，两者形态不同。
⇒ 这与 228 号「拿接收者名比方法名」是同一族失效：
  **门转红的第一反应应当是「尺子/判据错了」，而不是「代码错了」**（§181）。

---

## 六、【P3 · 待裁决 95】ADR 承诺的配置面清除被部署脚本绕过

`docs/adr/2026-09-09-ir-transport-layer-retirement.md:82` 承诺
「`TRANSPORT_LAYER_IR_ENABLED` **从配置面消失**」。

**实测**（全仓不截断 grep，该标识符**无任何 Go 代码读取**，命中全在 `docs/`、`deploy/*.service`、`scripts/`）：

- `deploy/llm-gateway-go.service:13`、`deploy/llmgo-245.service:15`
  仍写 `Environment=TRANSPORT_LAYER_IR_ENABLED=true`；
- ⚠️ **`scripts/deploy-seamless.sh:764-773` 仍在主动向生产 env 文件注入该变量**，
  注释写「确保 TRANSPORT_LAYER_IR_ENABLED=true (spec §10.4.1)」，
  逻辑是「env 里没有就 `sed` 追加一行」。

⇒ `docker-compose.yml` 确实已删（`docs/audit/2026-09-09-24h-audit-round4.md:130` 标 ✓ gone），
  **但部署脚本换了个地方继续注入** ⇒ ADR 的承诺**未兑现**。
⇒ **无功能危害**（无消费者），但**有误导性**：该变量在 spec 里曾是
  「IR 作为协议转换主路径」的开关，**运维看到它被注入会以为 IR 传输层在生效**（实际已下线）。
⇒ 与 225 号（待裁决 91：下发给 UI 的文案教运维把 Key 写进一个没人读的文件）**同族**。
⇒ **不擅自动手**：改部署脚本属运维契约 ⇒ 登记待裁决。

## 七、【P3 · 待裁决 96】`lockfree_circuit_breaker.go` 整文件死代码，且文档仍在教人用它

- `domains/transformation/lockfree_circuit_breaker.go`：`LockFreeCircuitBreaker` /
  `NewLockFreeCircuitBreaker` / `GetErrorCount` 的全仓命中**全部在本文件内**，
  **无外部、无测试**（本人不截断 grep 复核）。
- ⚠️ `docs/archive/2026-07/CONCURRENCY_OPTIMIZATION.md:460` 仍示范
  （⚠️ **文件名订正，234 号**：本行原写作 `CONCURRENCY_OPTIMIZER.md`，**少 "ATI"**；
  真实文件名是 `CONCURRENCY_OPTIMIZATION.md` —— 与本报告 `:230` 处一致）
  `transformation.NewLockFreeCircuitBreaker(3, time.Minute, time.Minute)`
  ⇒ **文档仍在教人调用一个死代码**（同 225 号族）。
- 同类：`anthropic/anthropic_to_chat_request.go`（`ConvertAnthropicRequestToChat`）
  13 处命中 = 定义 2 + 测试 11，**零外部引用**。
⇒ 登记为独立删除批次候选，**本轮不动**（改生产代码需裁决）。

---

## 八、⚠️ ADR 自身的三处陈述已过期（已写 2026-10-03 补记进 ADR 文件）

`2026-09-09-ir-transport-layer-retirement.md` 的删除清单**已全部真实执行**（提交 `d206ca771` /
`db64c6bdf`，工作树零残留），但：

1. **`:83` 的「孤儿清单」已自行过期**：它点名的 5 个流式函数
   今天**在本包内已不存在**，被 `97aa179ab`、`b7c8d6f39` 两个后续提交删除；
   同条「必须保留」的 `IsAnthropicStreamEmpty` **全仓已无定义**
   （`domains/streaming/anthropic_bridge.go:439` 注释自述「retired in the D2/D3」）。
   ⇒ **孤儿不是被解决，是被顺带删掉了。**
2. **`:82` 的配置面承诺未兑现**（见 §六）。
3. **`:20` 的接线行号漂移**：`main.go:1537` → 今天 `cmd/gateway/main.go:1690`。

**另订正 ADR 自己的一句** `:21-22`「**所有** integration 测试文件都带 `//go:build integration`」
**不准确**：`tests/integration/` 10 个文件里 **4 个无 tag**
（`helpers_test.go` / `dual_mode_test.go` / `benchmark_test.go` / `stream_state_machine_cancel_test.go`，
其中 `helpers_test.go:4-7` 注释说明这是刻意的）。
⇒ 但 **`:47-66` 那段「59 天断编译」的收口是真的**：`tests/integration/protocol_e2e_test.go`
已由 `ca4c828f4` 删除；`go vet -tags=integration ./tests/integration/` 退出码 0；
且 `sql/schema/integration_gate_test.go:769` 的 `TestIntegrationTaggedTreeCompiles`
经 `.github/workflows/audit-guards-ci.yml:100-101` 的 `make guards` **在 CI 生效**
（本人实跑：`type-checking 374 packages` PASS，22.30s）。
⇒ **这是本仓少见的「防复发守卫有 CI 登记」的正例**，与 211 号那批
「本地绿但没人叫它跑」的门形成对照。

---

## 九、诚实边界

- **远端 CI 当前红绿无法从仓库内证实**：仓内无 badge、无状态缓存、无 PR 状态记录。
  9 个 workflow 均已列出（`audit-guards` / `installer` / `integration-testcontainers` /
  `ml-training` / `multimodal` / `release` / `security-scan` / `sessionforensics` / `verify`），
  但**「存在 workflow 文件」不能推断「它在运行」**。
  ADR 补记里说的「那条 job 常年红」**今天是否仍红，本轮无法判定**。
- **未起真进程、未连 PG/Redis/Docker、未执行 SQL**。
- `domains/transformation` 的能力矩阵由子代理给出、**本人复核了关键 5 项**
  （`TRANSPORT_LAYER_IR_ENABLED` 零代码读取 / `LockFreeCircuitBreaker` 零引用 /
  `cmd/gateway` 的 import / `IsAnthropicStreamEmpty` 不存在 / `internal/ir` 五协议文件树），
  **但未逐行读完 18 个文件**。
- ⚠️ **已修正子代理 B 的一处定性**：它称「Transformer 管线在主二进制不可达」，
  实测 `cmd/gateway/main.go:73` **确实 import 了 `domains/integration`**
  （带 `//nolint:depguard // clientprofile worker wiring`）。
  准确说法是：**`BuildFullPipeline` 这个函数无非测试调用方**，
  而非「该包在主二进制不可达」。
- `adapter/unified` 的 5 个文件**逐行读过**（子代理给出清单，本人复核文件树与包头）。
- `go test ./...` 全量未跑；`core.hooksPath` 未设 ⇒ 未经 pre-push 门。
- 本轮**未改任何生产代码**；改的是 1 份模块指南 + 1 份 ADR 补记（均为文档，零逻辑）。

---

## 十、编号与去向

- **新增待裁决第 95 条（P3）**：`scripts/deploy-seamless.sh:764-773` + 两个 `deploy/*.service`
  仍注入 `TRANSPORT_LAYER_IR_ENABLED=true`，而该变量**无任何消费者** ⇒ ADR 承诺的配置面清除被绕过，
  运维会误以为 IR 传输层在生效。
- **新增待裁决第 96 条（P3）**：`domains/transformation/lockfree_circuit_breaker.go`
  整文件零引用（含测试），且 `docs/archive/2026-07/CONCURRENCY_OPTIMIZATION.md:460`
  仍在示范调用它；同类 `anthropic/anthropic_to_chat_request.go` 零外部引用。
- **订正 115 号**：`adapter/unified` 由 B 类（待接线）改为 **C 类（已废弃草图，可删）**。
- **订正 148 号**：「`globalRegistry` 恒为空」错（`registry.go:150-152` 预填 2 项）；
  bucket ③ 分桶错（实为 bucket ②）⇒ 其「基准率 1/25」论证受影响。
- **订正 `docs/MODULES_GUIDE.md`**（已改）：3 个不存在的文件 + 四协议 CURRENT + SSE 全 ✅。
- **订正 ADR `2026-09-09`**（已补记）：孤儿清单已过期、配置面承诺未兑现、行号漂移、
  「所有 integration 文件带 tag」不准确。
- **115 号必做项 ③ 关闭**：覆盖面 = unified ⊂ `internal/ir` + `domains/transformation`，
  且两者无依赖关系 ⇒ 删除无能力损失。
- **守卫**：`internal/sqlguard` 新增 3 条测试，集合仍 14 包。
- **playbook**：§188–§189。

---

## 十一、playbook §188–§189

**§188 「零引用」的分类（B/C）取决于设计有没有被否决，而不取决于有没有接线。**

115 号把 `adapter/unified` 归为 B 类（能力已在、从未接线 ⇒ 值得接线），
而它 `interface.go:4` 写的是「**not the canonical IR**」、`:6` 写明权威面是别的包。
⇒ **B 类与 C 类的分界不是「有没有生产调用方」，而是「设计是否仍然成立」。**
  一个包可以完全实现、测试齐全、接口漂亮，而它的**设计已被否决** ⇒ C 类。
⇒ 判别式：判「零引用包」的类别时，**先找它的设计裁定**（包头 banner / ADR / 特性目录），
  再看有没有调用方。**顺序反了，就会把「已否决的方案」重新提上议程**——
  本轮 115 号就是这样把一个 2026-08-30 就废弃的草图排进了「是否接线」队列。
⇒ 与 §153（先查再怀疑）互补：§153 说「别把已登记当新发现」；
  这条说「**已登记的类别本身也可能是错的**」⇒ 查登记时要连**理由**一起查
  （同 228 号 §十三那条）。
⇒ 顺带一条成本极低的对策：**零引用包的分桶必须基于「读完包注释」**。
  148 号引用了 147 号的 §41 三分法并声称已应用，却把废弃声明（`interface.go:1-13`）
  归进「两者都没说」⇒ **引用方法 ≠ 执行方法**。

**§189 门转红的第一反应是「尺子错了」，尤其当被检对象是刚被你改过的文档。**

本轮 `TestModulesGuideDoesNotAdvertiseUnifiedAsCurrent` 第一版红了，
而它命中的是**我自己刚写的订正文字**：
「此前误标为四协议 CURRENT 且全部 SSE ✅」——
那句里的 `CURRENT` 是在**描述被订正的错误**。

⇒ 三重叠加让它必然误报：
  ① **判据是纯子串搜索**（不看形态）；
  ② **被检对象是我刚刚编辑过的文档**（所以必然含描述错误的话）；
  ③ **订正文本与被订正文本用同一个词**（「CURRENT」既是错误本身，也是描述它的词）。
⇒ **判别式**：写文档类门时，判据必须锚在**该事实的权威形态**上。
  「某协议被标为 CURRENT」这件事**只可能出现在表格数据行**里，
  而「说明它曾经被误标」发生在正文/引用里 ⇒ 判据收窄到 `|` 开头的行。
⇒ 推论：**凡是「我改完文档后立刻建门」的场合，门的第一版大概率会误报自己的订正文字**。
  这不是偶发，是结构性的——**订正必然要提到被订正的错误**。
⇒ 与 §181（会误报正确代码的门比没有门更坏）同源，
  这条给出**最容易触发该失效的场景**：**自证式修改 + 立刻建门**。

---

## 十二、⚠️ 收回 228 号的一条订正：`session_writer_v2.go:297` 的注释是**对的**，是我比错了对象

228 号登记了一条「注释订正」：

> `domains/session/v2/session_writer_v2.go:297` 把 `RequestChecksum` 列入「缺源字段」
> **与实测（3 个生产调用方）不符**。

**本轮核实后收回。** 那条注释的原文（`:294-299`）是：

> 「── 存储优化方案 v2 S1a（migration 706/707）：request_logs 独有数据补采。
> 数据源是 **`telemetry.RequestLogEntry`**（mirror bridge `entryToProcessedRequest` 逐一拷贝）；
> 四组列全部可空、零值即 NULL。
> **缺源字段（TraceEvents/SearchText/RequestChecksum/RawModelName 在
> `RequestLogEntry` 上不存在）**保留列位、暂为 NULL」

⇒ 注释断言的是「**`telemetry.RequestLogEntry` 上没有 `RequestChecksum`**」，
而我 228 号拿「**`audit.Event` 上有 `RequestChecksum`**（`audit.go:40`，
三个协议 handler 生产调用）」去反驳它。
**这两句可以同时为真**——它们说的是链路上**两个不同结构体**。

**实测**（`domains/hooks/observability/telemetry/client.go`）：

| | `RequestLogEntry`（`:253-525`，271 行 / 121 字段） | `audit.Event`（`audit.go:40`） |
|---|---|---|
| `IdentityHash` | ✅ `:309` | ✅ |
| `ResponseChecksum` | ✅ `:325` | ✅ |
| **`RequestChecksum`** | ❌ **该文件内 0 命中** | ✅ |

⇒ **注释完全正确**，而且它描述的正是**中间那一跳断了**：
`audit.Event.RequestChecksum` 被正确计算并落进 `request_logs`，
但 **mirror bridge 的源结构 `RequestLogEntry` 没有这个字段** ⇒ 拷不到
⇒ `session_turns.request_checksum` **确实很可能在 live turn 上恒 NULL**。

⇒ 这**加强**而非削弱 228 号的核心结论（`response_checksum` 语义分叉），
并把 228 号留在诚实边界里的那句「`request_checksum` 在真实 live turn 上的填充率未测」
从「也许没事」提升为「**代码路径上已经断了一截，填充率很可能为 0**」。
⇒ ⚠️ 但**仍需真库 fill-rate 才能坐实**（未起真进程）⇒ 结论保持 P3 登记，不升级。

### 判别式（本轮第四次「拿错误的对照物下结论」）

**「X 在 A 上存在」不能推翻「X 在 B 上不存在」，当待判结论涉及的是 A→B 的传递链。**
本轮四个错误里，这是第四个形态：

| 轮次 | 我拿来当证据的 | 真正该比的 | 失效名 |
|---|---|---|---|
| 228 | `audit.Event` 有 `RequestChecksum` | `telemetry.RequestLogEntry` 有没有 | **比错了对象** |
| 228 | `ComputeRequestChecksum` 名字含 `RequestChecksum` | 两个是不同函数 | 名字包含 |
| 229 | `cmd/gateway` import 了 `domains/integration` | `BuildFullPipeline` 有没有调用方 | 包可达 ≠ 函数可达 |
| 229 | 子代理：「Transformer 管线在主二进制不可达」 | 同上 | 同上 |

⇒ 链条型断言（值从 A 传到 B）必须**逐跳检查**，
  而「某一跳的源有该字段」**不蕴含**「该字段会到达终点」。
⇒ 与 §185（否定结论要附命中数与归类）互补：那管单跳，这管**跳与跳之间**。
