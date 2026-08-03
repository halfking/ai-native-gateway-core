# OmniRoute v2 实施报告：GW-00 / GW-01 / GW-03 / GW-04 / GW-05

> 本报告记录 omni-ref2 第一轮实施（5 个 `planned` 工作包）的产出、审计发现与后续待办。
> 状态标签对齐 `01-DELIVERY-MATRIX.md`。证据标签：`SOURCE-VERIFIED` / `TARGET-BOUNDARY`。

## 1. 交付总览

| ID | 工作包 | 状态 | 关键产出 |
|---|---|---|---|
| GW-00 | canonical metadata | done | `domains/events/canonical.go`、metrics `credential_id`→`provider_id`、低基数 guard test |
| GW-01 | provider catalog export | done | `provider/catalog/`（schema/validate/seed/protocol）+ snapshot 往返 test |
| GW-03 | strategy interface | done | `Strategy` 接口 + `p2cStrategy` + `Router.ShadowStrategy` + feature flag |
| GW-04 | cost/cache/context/headroom | done | 4 个 Strategy 实现，unknown 值惩罚有 table-driven test |
| GW-05 | Lite compression stage | done | `domains/hooks/compression/lite/` 5 stage + Compressor 接入 + feature flag |

全部 5 包：`go build ./...` / `go vet ./...` 干净；`go test -race` 全绿；零 DB 迁移；
所有 feature flag 默认关闭（回滚 = 关 flag）。

## 2. 审计发现的三处文档与源码不符（已按源码为准实施）

这三处在实施时以源码事实修正了 README 正文描述，避免错误翻译：

### 2.1 GW-05：Lite 实际不含保护块抽取，且零正则

- **README §C1 称**：Lite 含"保护块抽取/还原"，"RE2 lookbehind 必须改写"。
- **源码核对（`SOURCE-VERIFIED`，lite.ts commit c8f1d62de）**：
  - `lite.ts` **不 import `preservation.ts`**，不做保护块抽取——那是 Caveman（GW-07）的职责。
  - Lite 实际 5 步：whitespace 折叠、system prompt 去重、tool result 截断、redundant 移除、image_url 占位符。
  - `lite.ts` **零正则、零 lookbehind**，纯 charCode 循环（RE2-safe 原样翻译）。lookbehind 风险只存在于 Caveman 的 `preservation.ts:100`（`math_inline`），属 GW-07。
- **处理**：按 lite.ts 实际 5 步翻译，未引入 `regexp`。README §C1 描述需后续修正。

### 2.2 GW-00：高基数 `credential_id` 标签无生产调用方

- **README §5 Phase 0 称**：移除高基数 metrics label。
- **源码核对（`SOURCE-VERIFIED`）**：4 个 `scheduler_*` 指标在 `pool/scheduler/wrr.go` 里**根本没被调用**（scheduler 用自己的内存计数器），仅测试触发。
- **处理**：把 label 从 `credential_id` 改为 `provider_id`（调用点 `Credential.ProviderID` 是唯一可用的低基数维度）。**零生产破坏**。

### 2.3 GW-05：`Compressor.Compress` 当前未接入执行器

- **README §4 硬约束**：不得在 `executor_chat.go` 增加第三处 body transform。
- **源码核对（`SOURCE-VERIFIED`）**：执行器直接调 `transformation.CompressMessagesIfNeeded`，绕过 `hooks/compression.Compressor`。
- **处理**：把 Lite 接入 `Compressor.Compress`/`CompressAfter4xx`（在 mechanical trim 之前），**不碰执行器**。因为 `Compressor.Compress` 本来就没接执行器实时路径，Lite 接进去仍是离线/feature-flagged 状态，零线上风险。执行器接线留给 GW-08（Stacked/Pipeline.Apply）。

## 3. 各工作包实施细节

### GW-00：canonical metadata 基线

**新增**：`domains/events/canonical.go`
- `ProviderRef`：对外/审计/事件唯一允许的 provider 引用（catalog_code/protocol/tier/vendor/credential label，**无 secret/base_url/路径**）。
- `RoutingDecision`：路由决策的 canonical 解释（strategy/decision_version/provider/explanation_code/timestamp）。
- `CompressionEvent`：压缩结果字段（mode/stages/input_chars/output_chars/saved_chars/reason_code/timestamp）。
- `DecisionVersionV1 = "v1"` 常量。
- 与现有 `routeincident.RouteSnapshot` **互补不替换**：给 `RouteSnapshot` 加了可选 `Decision *events.RoutingDecision` 字段（`omitempty`，零破坏）。

**改动**：`metrics/prometheus.go` + `metrics/interface.go`
- 4 个 `scheduler_*` 指标 label `credential_id` → `provider_id`；指标名 `..._by_credential` → `..._by_provider`。
- 4 个 recording 方法签名 `(credentialID string,...)` → `(providerID string,...)`。
- `Recorder` 接口 + `NoopRecorder` 同步。

**门禁测试**：
- `domains/events/canonical_redaction_test.go`：序列化后 grep 不含 secret 模式；字段白名单锁定。
- `metrics/label_cardinality_guard_test.go`：反射 `Registry.Describe()` 收集所有声明的 label，断言不出现 `credential_id`/`tenant_id`/`request_id`/`prompt`/`token`/`model` 等高基数/高敏维度。已用故意破坏测试验证 guard 能 catch 回归。

### GW-01：provider catalog export

**新增**：`provider/catalog/`
- `protocol.go`：Protocol/Tier/Kind/Category/DiscoveryStrategy 常量（与 `provider_catalog.sql` CHECK 约束一致）。
- `schema.go`：`CatalogEntry` 结构（对齐 DDL 全列），`NewEntry(code)` 带默认值。
- `validate.go`：`Validate`（单行）+ `ValidateSet`（含重复 Code 检查）+ `validateBaseURL`（cloud 强制 https，local 允许 http，支持 `{resource}/{host}/{port}` 模板占位符）+ `validateNoSecret`（`sk-`/`Bearer `/`api_key=`/`secret=`/`password=` 模式扫描）。
- `seed.go`：`GenerateSeed` 产出幂等 `INSERT ... ON CONFLICT (code) DO UPDATE`，列名显式（不用 positional VALUES），时间戳用 `now()`。

**门禁测试**：
- `catalog_test.go`：Validate OK/reject（11 个负向 case）、duplicate code、seed 幂等形状、空 JSON→NULL。
- `contract_test.go`：catalog protocol 与 executor 路由的 protocol 集合一致；Protocol 常量与 DB CHECK 约束对齐；每个 protocol 至少 1 个 provider。
- `snapshot_test.go`：解析 `02-seed.sql` 的 37 个 provider_catalog INSERT → `ValidateSet` 全过 → `GenerateSeed` → 重新解析 → 深度相等（证明 generator 幂等且不丢字段）。

**不做（硬约束）**：不写 Go 常量块；不存 secret；不复制 OmniRoute TS provider 常量（`src/shared/constants/providers/*` 是 UI 身份域——icon/color/auth-hint，与 Go 的 protocol/pricing catalog 正交，无法 round-trip）。

### GW-03：Strategy 接口

**新增**：`domains/streaming/executors/strategy.go`
- `Strategy` 接口：`Name() string` + `Score(ctx, cand, StrategyInput) (float64, error)`，lower=better（与现有 P2C loadScore 一致方向）。
- `StrategyInput`：Policy/EgressPreference/TenantID/Canonical/RequestID/LoadScoreWeights（无 secret）。
- `p2cStrategy`：适配现有 `calculateLoadScore`，`Name()="p2c"`。
- `NewStrategyByName`：集中策略注册，main.go 只读 env 名字调用。
- `scoreWithShadow`：shadow diff——对 bucket 用 ShadowStrategy 独立评分，记录 agreed/disagreed metric，**不改变实际选中候选**。复用 URSM v2 shadow diff 观测模式。

**改动**：`router.go`
- `Router` 加 `ShadowStrategy Strategy` 字段（nil = 现状）。
- `planByTier` 签名加 `ctx` + `StrategyInput`；在 round-robin rotation 之前调 `scoreWithShadow`。

**改动**：`cmd/gateway/main.go`
- `LLM_GATEWAY_ROUTING_SHADOW_STRATEGY=p2c|cost-optimized|...` env 装配。

**新增**：`metrics_strategy.go`
- `llmgw_routing_shadow_strategy_outcomes_total{strategy,outcome}`，低基数 label。

### GW-04：cost / cache / context / headroom 策略

**新增**：`strategy_cost.go` / `strategy_cache.go` / `strategy_context.go` / `strategy_headroom.go`

| 策略 | 评分依据（Candidate 字段） | unknown 惩罚 |
|---|---|---|
| cost-optimized | `PriceInPer1M`+`PriceOutPer1M` | nil → `MaxFloat64`（不当作免费） |
| cache-optimized | `SupportsPromptCache` + `CacheReadPricePer1M` | 不支持 → `MaxFloat64`；支持但价未知 → 1e6 |
| context-aware | `ContextWindow` | nil/<=0 → `MaxFloat64`（不当作无限） |
| headroom | `calculateHeadroom` + `ConcurrencyLimit` | nil → 中性 0.5（不绕过健康过滤） |

**注册**：4 策略加入 `NewStrategyByName` switch。

**门禁测试**：`strategy_gw04_test.go` 每策略一个 unknown 惩罚 table-driven test + `NewStrategyByName` 全注册验证 + shadow diff 排序稳定性。

### GW-05：Lite compression stage

**新增**：`domains/hooks/compression/lite/`（`lite.go` 单文件含 5 stage + helpers）
- 翻译自 `lite.ts`，纯 rune/charCode 循环，零 `regexp`。
- 5 stage：`collapseWhitespace`（折叠 ≤2 换行 + 行尾水平空白清理）、`dedupSystemPrompt`（前 200 字符 trim key）、`compressToolResults`（>2000 词边界截断 + `\n...[truncated]`）、`removeRedundantContent`（相邻同 role 同 content）、`replaceImageUrls`（非 vision 模型 `data:image/` → `[image: fmt]`）。
- `Apply(body, Options)` 端到端，fail-open（非法 JSON/无 messages 返回原 body）。
- `Options`：Model/SupportsVision(*bool)/PreserveSystemPrompt。

**改动**：`compressor.go`
- `StrategyLite = "lite"` 常量。
- `Compressor.LiteStageEnabled bool` 字段。
- `Compress`/`CompressAfter4xx` 在 `compressMechanical` 之前调 `lite.Apply`，经 `NeverWorse(GuardStageLite)` 守卫保证不增字节。
- `appendLiteNote` helper：mechanical 分支不覆盖已设的 Lite ReasonDetail。
- `GuardStageLite = "lite"` 常量（guard.go）。

**改动**：`cmd/gateway/main.go`
- `LLM_GATEWAY_COMPRESSION_LITE=true` env 装配，默认关。

**门禁测试**：
- `lite/lite_test.go`：5 stage 各自 table-driven + Apply 端到端 + fail-open + 顺序验证 + image format/backOff 边界。
- `compressor_lite_test.go`：Lite off 时不跑、on 时跑、NeverWorse 守卫、常量验证。

**不做（硬约束）**：不改 `executor_chat.go`/`executor_anthropic.go`；不翻译 `preservation.ts`；不实现 `Pipeline.Apply`；不接执行器实时路径。

## 4. feature flag 一览

| Flag | 默认 | 作用 | 回滚 |
|---|---|---|---|
| `LLM_GATEWAY_ROUTING_SHADOW_STRATEGY` | 空（关） | 选 shadow 路由策略（p2c/cost-optimized/cache-optimized/context-aware/headroom） | 设空/none |
| `LLM_GATEWAY_COMPRESSION_LITE` | false | 启用 Lite 压缩 stage | 设 false |

两个 flag 关闭时，现有线上行为完全不变（P2C golden diff 不变、executor 零改动）。

## 5. 不在本轮（blocked，已确认）

| ID | 工作包 | 阻塞原因 |
|---|---|---|
| GW-02 | auth resolver | 待 secret store 约定（跨切面） |
| GW-06 | RTK stage | 待 GW-05 完成（✓）+ compaction owner；schema validation/性能预算 |
| GW-07 | Caveman stage | 待 GW-05（✓）；含 `preservation.ts:100` 的 `math_inline` lookbehind（RE2 必须改写） |
| GW-08 | Stacked selector | 待 GW-05..07 + compaction owner；一个 request 一个 canonical decision |
| GW-09/10 | MCP | 待 auth/scope/audit contract |
| GW-11/12 | A2A | 待 auth + migration review |
| GW-13/14 | Fusion | 待 executor 行为 fixture + cost/latency budget |
| GW-15 | ASM event producer | 待 GW-00（✓）+ 跨仓 ASM schema + durable outbox |

## 6. 后续待办（本轮发现，非工作包）

1. **executor 里 protocol 字面量替换**：`executor_chat.go`/`executor_anthropic.go` 等处 `cand.Protocol == "anthropic-messages"` 字面量可替换为 `catalog.ProtocolAnthropicMessages` 常量（本轮已建常量，替换是独立小改）。
2. **metrics guard 覆盖面**：`label_cardinality_guard_test.go` 只扫描 `metrics` 包测试二进制注册的指标；`executors/metrics_pressure.go` 已有的 `model` label（高基数）在 metrics 测试里看不到。建议后续把 guard 改成扫描全二进制注册的指标，或单独给 executors 包加 guard。
3. **README §C1 描述修正**：按本报告 §2.1 更新 Lite 描述（5 步、零正则、不含保护块抽取）。
4. **执行器接线（GW-08 预研）**：GW-05 的 Lite 目前不接执行器实时路径；GW-08 Stacked/Pipeline.Apply 需要先决定执行器如何统一走 `Compressor`（替换 `transformation.CompressMessagesIfNeeded` 直调），避免第三处 body transform。
