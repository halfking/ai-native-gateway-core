# 115 号｜R89-AB：零导入包逐包定性（第 1 批，4 个）—— **三个直接命中 objective 的要求项，且全部是「能力已在、从未接线」**

- 日期：2026-10-01
- 轮次：R89-AB
- 起点：114 号 §四的已验证清单（29 个待定性），本轮做前 4 个最大的
- 纪律：逐包读原文 + 按 **§14** 先问「现役有没有等价物」，**不做批量判定**
- 零生产代码、零配置、零门

---

## 一、本轮 4 个包的定性结果

| 包 | LOC | 定性 | 对应 objective 要求项 |
|---|---|---|---|
| `domains/promptinjection/enhanced` | 808 | **B（且是真正的孤儿）** | 「注入检测」重点检查项 |
| `internal/requestfact` | 933 | **B** | 「把 `request_logs` 切到 `session_turns`」 |
| `adapter/unified` | 800 | **B** | 「对上游多个 llm 厂商的标准格式 / 自动切换协议」 |
| `internal/fsstore` | 976 | **B** | 「全量(pg+redis+memory+files) 与本地简化模式(sqlite+memory+files) 双重存储」 |

**4/4 都是 B 类**——**能力已写好、有测试、从未接线**。
**其中 3 个直接对应 objective 里点名要重点检查的能力。**
⇒ 「零导入」在本仓的形态高度一致：**不是没写，是写了没接。**

---

## 二、逐包证据

### 2.1 `domains/promptinjection/enhanced`（808 LOC，1 文件）—— 命名极具误导性

- 包内：`detector.go` 一个文件，提供 `DetectionLayer` / `AttackType` 分层检测、
  base64 / 编码变形检测、启发式 + LLM 检测、`sync/atomic` 计数器，
  配 `TestEnhancedDetectorConcurrent` / `TestLLMResponseErrorsDoNotPanic` / 两个 benchmark。
- **外部引用 = 0**（`grep -rn "promptinjection/enhanced"` 除自身外无命中）。
- **现役用的是父包**：
  - `domains/security/plugins/prompt_injection_enhanced.go:25` —
    `promptinjection "…/domains/promptinjection" // 复用核心检测器`
  - `cmd/gateway/main_pipeline.go:112` 装配的是 `domains/hooks/promptinjection`。

⚠️ **注意这个命名陷阱**：现役那个文件**本身就叫 `prompt_injection_enhanced.go`**，
用的却是**父包**的检测器。所以「enhanced」这个词在本仓被用在了两处，
**而真正叫 `enhanced` 的那个目录从未被用过**。
按注释承诺默认不成立的纪律（已 5 例），**不能因为名字对得上就认为它是增强版已被采用**。

**⇒ 下一轮必核**：`enhanced/detector.go` 与父包 `domains/promptinjection/detector.go`
的检测能力差在哪？若 enhanced 严格覆盖父包 ⇒ 父包是待删的旧版；
若各有覆盖面 ⇒ 需要一个明确的选型决策。**本轮不下结论。**

### 2.2 `internal/requestfact`（933 LOC，7 测试）—— 唯一实现，零引用

- 内容：`codec.go`（encode/decode + **payload hash 防篡改**：`sha256` + `subtle` 常量时间比较）、
  `content_builder.go`、`durable_projection.go`、`metadata_projection.go`、`types.go`、`wire.go`。
- 测试质量明显高于平均：`TestEncodeProducesDeterministicPayloadHash`、
  `TestBodySHA256IgnoresJSONFormatting`、`TestDecodeRejectsUnsupportedVersionAndTampering`、
  **`golden_roundtrip_corpus_test.go`（golden 语料往返）**、`session_v2_boundary_test.go`。
- **外部引用 = 0**；且我搜过 `requestfact` / `EncodeFact` / `Fact{`，
  **全仓没有第二套实现**（`domains/requestjourney/attempt_facts.go` 的 `AttemptFact` 是另一物，命名相近但不是同一格式）。

**⇒ 判读**：`request_logs → session_turns` 那次迁移的
**产物格式契约（版本化 + 确定性哈希 + 防篡改校验 + golden 语料）从未被任何生产代码使用。**
现役的 session_turns 用的是**另一种落库方式**。
**这是本轮信息量最大的一条**：不是「多了一套没人用的代码」，
而是「**当初设计的那个可验证的产物格式，从未落地**」。

### 2.3 `adapter/unified`（800 LOC）—— 与现役 `domains/transformation` 并存的第二套适配器

- 内容：`openai.go` / `anthropic.go`（`ToProviderRequest` / `FromProviderResponse` /
  `ValidateRequest` / `ExtractSystemMessage`）+ `registry.go`（`Register/Get/Has/Unregister` +
  `GlobalRegistry` / `ValidateAndGetAdapter`）。
- 测试覆盖适配器往返与注册表全路径。
- **外部引用 = 0**。现役的协议转换层是 **`domains/transformation`**（该包在用，不在零导入名单里）。

**⇒ 第二套适配器与现役并存**，能力面（OpenAI / Anthropic 双向）明显重叠。
下一轮需比对面，判断是「未切换的新版」还是「已被 `transformation` 覆盖的旧版」。

### 2.4 `internal/fsstore`（976 LOC）—— 双存储 fallback 开关，从未装配

- 包注释自带完整决策矩阵（日期 2026-08-26）：

```
startup ─► probe PG ── ok ────► primary = PG
                             └─ runtime PG error → Mode = "degraded"
                                                 (NOT auto-switched:
                                                  to avoid split-brain)
startup ─► PG down ──► primary = FS, log loud warning.
```

  并明写「**刻意保持很小**：调用方应显式传入 `*pgxpool.Pool` 和 `*fsstore.Store`」。
- 导出：`Probe`（5s 超时）、`NewFallback`、`Mode`、`Fallback`，含 `flock_unix/windows` 双平台文件锁。
- **外部引用 = 0**。
- 现役确有降级概念，但落在 `domains/streaming/executors/router.go` 与 `domains/session/session.go`
  （命中 degraded/本地简化模式相关标识），**不是这个包**。

**⇒ 下一轮需核**：现役的 degraded 路径与 `fsstore` 的 fallback 是否等价。
若不等价，则「PG 挂了自动切文件系统」这条设计**从未实现**。

---

## 三、本轮的方法论观察

- **4/4 同型**（写了、有测试、没接线）⇒ 后续 25 个包**可以预期高比例同型**，
  但**仍然必须逐个读**（107/108/113/114 号已四次证明：预期不能替代读原文）。
- **「零导入」的两种身份要分开登记**：
  - **被别的主程序消费**（`ursm/v2/migration` → 运维 CLI）⇒ 活的；
  - **无任何消费者**（本轮 4 个）⇒ 需要按 A/B/C 定性。
  114 号已把前一种从清单里剔除，剩的确实都是后一种。
- **命名不能当证据**：`prompt_injection_enhanced.go` 用父包、`enhanced/` 目录没人用——
  本仓「名字对得上」与「实际在用」**反复分离**。

---

## 四、编号与去向

- **无新增待裁决条目**；4 个包归 **B 类**，全部登记为「需产品/运维确认是否接线」。
- 下一轮必做（按信息量排序）：
  1. `promptinjection/enhanced` vs 父包的能力差与选型；
  2. `internal/requestfact` 的格式契约为何从未落地（与现役 session_turns 落库方式对比）；
  3. `adapter/unified` vs `domains/transformation` 的覆盖面对比；
  4. `internal/fsstore` vs 现役 degraded 路径是否等价。
- 剩余 **25 个包**待定性（29 − 本轮 4）。
