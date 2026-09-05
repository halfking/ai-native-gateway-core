# Phase 2-5 Handoff — Compression Strategy Pattern (2026-08-28)

> 写给后续接手者：Phase 1（Strategy 接口 + Registry + ManualSelector + Runner +
> 3 个 Adapter + 27 个测试）已落地（commit `98e2a2052`），后续 5 个 commit
> 修复了 3 个 critical bug（C1 数据竞争 / C2 Prometheus 漏计 / C3 silent off）
> + 3 个 non-critical（N3/N5/N7）。Phase 2-5 还有相当多工作未做，请按下面
> 的优先级列表推进。

## 1. 当前状态（2026-08-28 16:00 快照）

```
domains/hooks/compression/
├── strategy/                          # Phase 1 (commit 98e2a2052)
│   ├── strategy.go                    # Strategy interface + Registry
│   ├── selector.go                    # Selector + ManualSelector + ResolvePolicy
│   ├── runner.go                      # Runner + RunStats + defaultNeverWorse
│   ├── adapters.go                    # LiteAdapter / CavemanAdapter / ToolFocusedAdapter
│   └── strategy_test.go               # 22 单元测试
├── compressor_strategy_test.go        # 7 dispatcher 集成测试
├── compressor.go                      # 新增 RunStrategies/ParsePolicySpec（不破坏现有路径）
├── lite/                              # pre-existing (GW-05)
├── caveman/                           # pre-existing (GW-07)
├── toolfocused/                       # pre-existing (GW-09, 22 tests)
├── summary_client.go                  # T12+T13: DEBUG→WARN + 累积首个错误
└── (其他既有文件未变)
```

设计文档 `docs/design/compression-strategy-selector.md` §4.0 已修订，标记每个
Phase 的真实状态：Phase 1 ✅，Phase 2 部分完成，Phase 3-5 ⏳。

## 2. 关键修复记录（commit-by-commit）

| Commit | 修复 | 严重度 |
|--------|------|--------|
| `7d1a7bf91` | C1 Runner.guardNeverWorse 改为 atomic.Value；TruncatedBy → []string (N7) | critical |
| `2739a8972` | C3 测试覆盖 ResolvePolicy 拒绝 off/all 混用 | critical |
| `df2ded337` | C2 RunStrategies 注入 compression.NeverWorse，补 Prometheus 计数；N5 缓存策略 runner | critical |
| `d57a0617a` | T12+T13 summary_client.go DEBUG→WARN + 累积首个错误 wrap | high |
| `4e7c882c4` | 设计 doc §4.0-4.5 状态声明修订（避免误导） | medium |
| `b8ead62b8` / `3fca07b15` | WIP 修测试（已包含在最终提交里） | wip |
| `6aa7509f5` | gofmt 空格微调 | trivial |

## 3. Phase 2 待办：tool-focused adapter 字段接线 + 配置

### 3.1 ToolFocusedAdapter.Strategies / LiteAdapter.Options / CavemanAdapter.Config

**当前状态**：3 个 Adapter 都暴露了 `Strategies` / `Options` / `Config` 字段，
但 `defaultToolFocusedHandle()` / `defaultLiteHandle()` / `defaultCavemanHandle()`
都忽略这些字段，硬编码用 `toolfocused.DefaultStrategies()` / `lite.Options{}`
/ `caveman.DefaultConfig()`。

**修复路径**：
1. 让 default handles 读字段值（Handle func 闭包捕获字段）
2. 或在 adapter.Apply() 内根据字段决定调用哪个配置版本
3. 加测试：设字段 → 验证行为差异（如 `Strategies.FileContent: false` 应让
   fileContent 策略不命中）

**优先级**：P1（影响用户能否精细控制子策略，但默认配置已能工作）

### 3.2 IntelligentStrategy 设计 doc 期望的"智能"策略

**当前状态**：Phase 1 没有 `IntelligentStrategy`。现有 `Compressor.Compress`
仍是硬编码 if-feature-flag 链，没有走 `Strategy.Runner`。

**修复路径**：
1. 把 `Compressor.Compress` 的 lite → caveman → toolfocused → mechanical 四段
   重构为 `IntelligentStrategy.Apply()`，调用现有 Runner
2. 把 mechanical trim 也包装为 Adapter（`NameMechanical = "mechanical_trim"`）
3. 测试：对比新旧 Compress 输出 bytes 字段必须一致

**优先级**：P2（架构整理，不改行为）

## 4. Phase 3 待办：自动选择器 (RuleBasedSelector + CEL)

### 4.1 SessionFeatures 特征提取

**当前状态**：未实现。

**修复路径**：在 `domains/hooks/compression/selector/features.go` 实现：
```go
type SessionFeatures struct {
    MessageCount       int
    EstimatedTokens    int
    HasToolCalls       bool
    ToolCallCount      int
    ToolCallRatio      float64    // 工具消息占比
    AvgMessageLength   int
    HasCodeBlocks      bool
    CodeBlockRatio     float64
    IdleMinutes        int
    SessionAge         time.Duration
}
```
提取函数接受当前 session 的 `[]Message`，与 design doc §3.3 对齐。

**优先级**：P0（Phase 3 入口）

### 4.2 RuleBasedSelector + CEL evaluator

**当前状态**：未实现。

**修复路径**：
1. `domains/hooks/compression/selector/rule_based_selector.go`：实现 Selector
   接口，基于 `[]Rule{Name, Condition (CEL), Strategy}` 评估
2. `domains/hooks/compression/selector/cel_evaluator.go`：引入
   `github.com/google/cel-go` 评估 CEL 表达式，feature 必须在 enabled 项内
   才引入，避免额外 vendor
3. `domains/hooks/compression/selector/default_rules.go`：默认 4 条规则
   （tool-heavy / latency-sensitive / cost-sensitive / quality-first）

**优先级**：P0（设计 doc §4.3）

### 4.3 设计 doc §5.3 的 4 个 Selector 测试场景

需要在测试中验证：
- 工具密集型 (ToolCallRatio=0.5) → tool-focused
- 纯文本长对话 + 质量要求高 → intelligent
- 延迟敏感 (MaxLatency < 200ms) → rule-based
- 默认 → intelligent

## 5. Phase 4 待办：配置 + 租户/会话级覆盖

### 5.1 Config 扩展

**当前状态**：`Compressor.ParsePolicySpec` / `NewManualSelectorFromSpec` 已
落地薄封装，main.go 可用。但 `settings.Global.Spec("compression.policy")`
未接。

**修复路径**：
1. 在 `settings/` 增加 `compression.policy` 全局键（string，默认 "all"）
2. 在 `cmd/gateway/main.go` 启动期读这个键，构造 `Compressor.NewManualSelectorFromSpec`
3. 注入到 `routingExec.Compressor.selector`（需先在 Compressor 加字段）

**优先级**：P1

### 5.2 租户级配置

**当前状态**：未实现。

**修复路径**：
1. `settings/` 加 `tenant.{tenantID}.compression.policy` 键
2. 读请求时由 executor 解析 `X-Tenant-ID` header，构造对应 selector
3. 优先级：tenant > session > global

**优先级**：P2

### 5.3 会话级 hint

**当前状态**：未实现。

**修复路径**：
1. HTTP header `X-Compression-Strategy` 解析为 `*string`
2. 透传到 `Compressor.RunStrategies`（已支持 sel 参数）
3. 优先级：session hint > tenant > global

**优先级**：P2

### 5.4 配置热重载

**当前状态**：`Compressor.strategyRunner()` 在每次 RunStrategies 调用时重建
Registry（N5 修复后变 init-time 一次性 rebuild）。真正的热重载未实现。

**修复路径**：
1. 在 Compressor 加 `policyVersion atomic.Uint64`
2. settings 变更回调递增版本号
3. RunStrategies 入口检查版本变化，必要时重建 runner

**优先级**：P3

### 5.5 docs/configuration/compression-strategy.md

**当前状态**：未创建。

**修复路径**：写一份用户文档，说明：
- 怎么改 `compression.policy` (`"off"` / `"all"` / `"lite,caveman"`)
- 怎么加 tenant / session hint
- 怎么读 `compression_regressed_total` Prometheus 计数器
- 怎么写新 Strategy Adapter

**优先级**：P2（必须做，但代码优先）

## 6. Phase 5 待办：rule-based 策略

### 6.1 评估 Caveman 与 RuleBasedStrategy 的关系

**当前状态**：`domains/hooks/compression/caveman/` 已经是 8 语言 306 规则的
规则引擎。`CavemanAdapter` 包装为 `Strategy` 后，Phase 5 期望的"独立
RuleBasedStrategy" 实际与 Caveman 重复。

**修复路径**：
1. Phase 5 重新命名为 "OmniRoute caveman lite 规则"：从 caveman 抽出前 20-30
   条规则作为独立 `domains/hooks/compression/cavemanlite/` 包，暴露更窄 API
2. 或：撤销 Phase 5，把 caveman 包当 RuleBasedStrategy 实现，document 说明

**优先级**：P3

## 7. 工具与监控待办

### 7.1 245 log benchmark 真实数据接入

**当前状态**：`scripts/compression-benchmark.sh` + `cmd/compression-benchmark/main.go`
已落地框架，但只用 mock 数据，未接真实日志。

**修复路径**：
1. 实现 JSONL 解析器（`internal/loaders/sessions.go`）
2. 接 `toolfocused.Apply` / `lite.Apply` / `caveman.Compress` 真实调用
3. 输出 density / loss / fidelity 指标（design §7.1）
4. 生成 HTML 报告

**优先级**：P2

### 7.2 Prometheus 指标接入 RunStrategies

**当前状态**：RunStrategies 通过 `runner.SetGuard(compression.NeverWorse)`
复用 Prometheus 计数器（修复 C2）。但 `compression_strategy_selections_total`
/ `compression_duration_seconds` / `compression_savings_percent` 等 Phase 1
设计文档期望的指标未实现。

**修复路径**：
1. 在 `Compressor.RunStrategies` 加指标埋点
2. metrics 包注册新 HistogramVec / CounterVec
3. dashboards/compression-dashboard.json 加新面板

**优先级**：P2

### 7.3 OpenTelemetry spans

**当前状态**：未实现。

**修复路径**：design §7.3 列了完整 span tree。短期价值低，可延后。

**优先级**：P3

## 8. 已知风险与已知未做的事

### 8.1 admin/logs.go 在本审计期间被他人的 WIP 改动

工作区曾有 `admin/logs.go` 的未提交改动（RequestClass/DueAt 重复扫描修复），
后续被独立 commit `e3d569ed6` 处理。本审计未触碰该文件。

### 8.2 Runner 没有 panic-recover

如果自定义 `Strategy.Apply` panic，整个请求挂掉。`lite`/`caveman`/`toolfocused`
三个现有 adapter 内部都有 recover，但用户自注册 Strategy 没有保护。
**建议**：在 `Runner.RunWithBody` 加 `defer recover()`，panic 时记日志 + 跳过
该 strategy 不中断 chain。优先级 P2。

### 8.3 Strategy 接口 5 个方法 Phase 1 偏多

`Description()` / `Enabled()` 用得少。Phase 2 可考虑缩到 3 个
（Name / GuardStage / Apply），但属于 API breaking change，需协调所有现有
Adapter。

### 8.4 Adapter 字段未接线

详见 §3.1。

## 9. 回归测试基线

```
$ go test -count=1 ./domains/hooks/compression/...
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression            1.6s
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression/caveman    0.5s
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression/lite       0.9s
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression/strategy   1.8s
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary    2.2s
ok    github.com/kaixuan/llm-gateway-go/domains/hooks/compression/toolfocused 2.6s
```

合计 6 包，约 100+ 测试用例，新增 7 个集成测试 + 22 个 strategy 单元测试。
`-race` 跑器通过。

## 10. 引用

- 设计：`docs/design/compression-strategy-selector.md`（已修订 §4.0）
- 研究：`docs/research/omniroute-compression-comparison.md`
- 审计：`docs/audit/2026-08-27-session-compression-algorithm-audit.md`
- 当前 main：`6aa7509f5 style(compression): gofmt whitespace in RunStrategies comment`
- Phase 1 commit：`98e2a2052 feat(compression): GW-10 Phase 1 strategy pattern + manual selector`

## 11. 子代理执行清单（可并行）

下表列出可并行执行的子代理任务（每个任务独立、可被独立 commit）：

### 子任务 A：ToolFocused/Lite/Caveman adapter 字段接线（P1）

```
你在 /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go 工作。

任务：让 3 个 Adapter 的配置字段真正生效。当前：
- strategy/adapters.go:defaultToolFocusedHandle() 硬编码 toolfocused.DefaultStrategies()
- strategy/adapters.go:defaultLiteHandle() 硬编码 lite.Options{}
- strategy/adapters.go:defaultCavemanHandle() 硬编码 caveman.DefaultConfig()

修复：
1. 让 default handles 读字段（推荐：Handle 闭包捕获字段，或在 Apply() 内
   根据字段决定）
2. Adapter.Apply() 入口根据字段是否非零决定用字段值还是默认
3. 加测试：
   - TestToolFocusedAdapter_StrategiesField：设 Strategies.FileContent=false
     验证 fileContent 策略不再命中
   - TestLiteAdapter_OptionsField：设 Options.SupportsVision=true 验证行为
   - TestCavemanAdapter_ConfigField：设 Config.Intensity="ultra" 验证行为

要求：
- 不破坏现有测试（22 strategy + 7 integration）
- gofmt -l 干净
- go vet 干净
- 提交信息：feat(strategy): wire adapter config fields (audit N1)
```

### 子任务 B：IntelligentStrategy 重构 + mechanical adapter（P2）

```
任务：把 Compressor.Compress 现有的 lite → caveman → toolfocused → mechanical
四段重构为 IntelligentStrategy.Apply()，调用 strategy.Runner。

1. 创建 MechanicalAdapter（strategy/adapters.go 新增）
   - Handle 调 transform.CompressMessagesIfNeeded / CompressAnthropicMessagesIfNeeded
   - GuardStage 留空（mechanical 自身已保证不增字节）
2. 创建 IntelligentStrategy（strategy/intelligent.go）
   - 内部组装 Registry = {lite, caveman, toolfocused, mechanical}
   - Apply() 直接调 Runner.RunWithBody(ctx, sel, body)
3. Compressor.Compress 改为调 c.intelligentStrategy().Apply(...)
4. 测试：对比新旧 Compress 输出 Meta 的所有字段（reason / strategy / bytes_after /
   dropped_messages / tokens_after 等）必须一致

要求：
- 现有 30+ compressor_test.go 不能失败
- 提交信息：refactor(compression): IntelligentStrategy wraps 4-stage pipeline
```

### 子任务 C：SessionFeatures 提取 + RuleBasedSelector（P0）

```
任务：Phase 3 落地。

1. domains/hooks/compression/selector/features.go：
   - SessionFeatures struct（design §3.3）
   - ExtractFeatures(messages []Message) *SessionFeatures
2. domains/hooks/compression/selector/cel_evaluator.go：
   - 引入 github.com/google/cel-go
   - Wrap(feature, expr) (bool, error)
3. domains/hooks/compression/selector/rule_based_selector.go：
   - 实现 Selector 接口
   - 默认 4 条规则：tool-heavy / latency-sensitive / cost-sensitive / quality-first
4. 单元测试：5 个 selector 测试覆盖 4 个场景 + 边界
5. 集成测试：Compressor.NewRuleBasedSelectorFromSpec() factory

要求：
- 引入 cel-go 到 go.mod（先 vendor check 是否在 vendor/ 里）
- 如果不在 vendor：拒绝引入，改用简单表达式解析器（expr-lang/expr 或自写）
- gofmt -l / go vet 干净
- 提交信息：feat(selector): RuleBasedSelector with CEL rules (Phase 3)
```

### 子任务 D：配置 + 文档（P2）

```
任务：把 Compressor.ParsePolicySpec / NewManualSelectorFromSpec 接到 settings。

1. settings/ 加 compression.policy 全局键（默认 "all"）
2. cmd/gateway/main.go 启动期：
   - 读 compression.policy
   - 构造 selector 注入 routingExec.Compressor
3. Compressor 加 Selector 字段 + setter
4. Compressor.Compress 改为优先用 selector（如已设）；未设时退回 feature-flag 链
5. docs/configuration/compression-strategy.md 写完整用户文档
6. 测试：settings.Global 变更 → 重建 selector

要求：
- 不破坏现有 Compress 行为（默认无 selector 走老路径）
- 文档包含：spec 语法、租户/会话级 hint、Prometheus 指标、新 Strategy 编写示例
- 提交信息：feat(config): wire compression.policy setting + user docs (Phase 4)
```

### 子任务 E：策略 Runner 加 panic-recover（P2）

```
任务：审计 §N6 修复。

strategy/runner.go:Runner.RunWithBody 的 strategy.Apply() 调用：
- 当前没有 defer recover()
- 自定义 Strategy panic 会让请求挂掉

修复：
1. 在 for 循环内对每个 strategy.Apply() 加 defer recover
2. panic 时：slog.Error("strategy.Apply panic", "strategy", s.Name(), "panic", recover())
3. 行为：跳过该 strategy 继续 chain，不中断整个请求
4. 测试：mock 一个会 panic 的 strategy，验证不中断 chain

提交信息：fix(strategy): Runner recover from Strategy.Apply panic (audit N6)
```

### 子任务 F：245 log benchmark 真实数据接入（P2）

```
任务：cmd/compression-benchmark 接真实日志。

1. 实现 JSONL 解析器：cmd/compression-benchmark/loader.go
   - 每行一条会话 {session_id, messages, estimated_tokens, tool_call_count}
2. 替换 cmd/compression-benchmark/main.go 的 mock 数据
3. 实现真实指标：
   - 每策略的 density = (orig_tokens - compressed_tokens) / orig_tokens
   - loss rate = heuristic（tool calls 完整性 + code blocks 保留率 + 压缩比）
   - fidelity = 1 - loss
4. 输出 HTML 报告（可选）
5. 测试：用 100 行样本数据（fixtures）

提交信息：feat(benchmark): real 245 log data integration
```

## 12. 风险评估

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| cel-go 引入拖慢构建 | 中 | 低 | 子任务 C 决策：能 vendor 就用，否则自写 |
| IntelligentStrategy 重构破坏现有 Compress 行为 | 中 | 高 | 子任务 B 要求所有 30+ compressor_test 通过 |
| Adapter 字段接线破坏现有用户 | 低 | 低 | 子任务 A 默认值与现有硬编码一致 |
| Panic-recover 掩盖真实 bug | 低 | 中 | 子任务 E 强制 slog.Error 记录 |

---

**当前 main**：`6aa7509f5`  
**Phase 1 锚定 commit**：`98e2a2052`  
**审计日期**：2026-08-28  
**预计 Phase 2 总工时**：8-12 小时（含子任务 A+B+C）

