# 2026-11-11 211 日志复扫 · 错误深度分析与「多厂商适应」架构隔离（SRP/OCP/抽象工厂）

> 用户指令：再次验证 211 log，检查错误、深度分析；用单一职责（SRP）、开闭（OCP）、
> 抽象工厂等模式把变更隔离，确保将来对不同原厂（厂商 wire 方言）、智能体（客户端
> 会话形态）、供应商（凭据/路由特性）的适应不侵入核心路径。
> 本轮交付：①错误清单与深度分析；②变化点地图与模式指派；③第一片隔离重构落地
> （`internal/tooldialect` 工具参数流方言抽象工厂，行为保持、全测试绿、已部署验证）。

## 1. 日志复扫证据（211 槽 a，版本 199c20a0，窗口 ~35min / 26,269 行）

| # | 形态 | 条数 | 定性 |
|---|---|---|---|
| 1 | `ir.anomaly`（thinking 374 / enable_thinking 374 / reasoning 142） | 879 | **架构信号**：厂商 reasoning 扩展字段在跨方言转换中被逐字段丢弃，每厂商一套 if-else |
| 2 | `validate_and_fix: role alternation violations`（not auto-fixed） | 544 | **架构信号**：智能体（ZCode/MiniMaxCode）会话形态 × 厂商校验器的适配散在通用修复器 |
| 3 | `node probe failed: protocol mismatch`（+ upstream fault 168） | 292+168 | 探针方言 × 厂商 API 形态 |
| 4 | `successful request log missing response body` | 117 | 观测面断链（另线） |
| 5 | `openai_empty_response` 流中断（glm-5.3-flash 32 / minimax-m3.1-flash 12 / sensenova / gemma…） | 43+ | **架构信号**：flash 档厂商空流的恢复策略按厂商散落 |
| 6 | `modelbinding: ambiguous normalized model binding` | 48 | **架构信号**：厂商命名归一化无单一权威（minimax-m3.1-flash → 4 归一形 × 2 候选） |
| 7 | `anthropic_message_fix: merged consecutive same-role` | 13 | 同 #2（agent 形态适配） |
| 8 | `context_budget_exceeded` / `tool_json_invalid` | **0 / 0** | 本会话前两轮根修已闭环 ✔ |

## 2. 深度分析：三个「特性泄漏进通用代码」的缺口

三天的三起生产事故/报障全部同构——**某一方的特有语义被复制（或漏复制）进通用路径**：

1. **tool_json_invalid（已修）**：anthropic 的「`{}` 占位符」wire 语义写在三处
   （IR 解析器内联、chat 桥 `!= "{}"` 守卫、responses 桥**缺失**）。缺失的那处
   就是事故。→ *同一条厂商语义存在 N 份实现，就可能有 N+1 处遗漏*。
2. **预算门（已修）**：「max_tokens 是输出上界」的语义按厂商不同（Anthropic 校验
   prompt ≤ window−max_tokens；OpenAI 不校验），网关按单一假设执法。
3. **ir.anomaly 879 条/35min**：thinking/reasoning/enable_thinking 的厂商拼写与
   语义差异（enabled|adaptive|disabled vs 布尔 vs 多档 budget）在 IR 投影时逐字段
   丢弃——**每接一个新厂商就多一组散落条件**，且丢弃本身成为常态噪声（真实语义
   损失被 WARN 淹没）。

结论：问题的共同根是**变化点没有被显式建模**。厂商/智能体/供应商的差异以
「通用代码里的特判」形态存在，违反 SRP（语义多主）与 OCP（新厂商=改核心）。

## 3. 变化点地图与模式指派

| 变化点 | 现状 | 模式指派 | 迁移切片 |
|---|---|---|---|
| A. 上游 wire 方言·工具参数流（占位符/续拼/修复） | 3 处内联（1 处曾缺失） | **SRP+抽象工厂（本轮落地）** `internal/tooldialect` | ✅ Slice 1 |
| B. 上游 wire 方言·reasoning 字段族 | IR 投影逐字段 if + anomaly 噪声 | 方言对象（同 A 家族）：`ReasoningDialect`（投影/回注/丢弃策略） | Slice 2 |
| C. 供应商命名/归一化 | `normalizeModelKey` 一处、归一歧义 WARN | 策略 + 单一归一权威（归一函数注册表，按供应商族） | Slice 3 |
| D. 智能体会话形态（role 交替/连续同角色/tool_result 配对） | 通用修复器内特判 | 形态归一化器（按 client family 的 `ConversationShapeNormalizer`） | Slice 4 |
| E. 厂商空流/恢复策略 | 按错误码散落 failover 判定 | 恢复策略对象（per-vendor `RecoveryPolicy`） | Slice 5 |
| F. 容量语义（窗口来源/预留/估计器标定） | contextbudget 已分层但标定全局 | 容量方言（per-vendor 校准系数 + 预留语义） | Slice 6 |

**原则**：每个变化点一个显式抽象（接口），实现按「谁拥有语义」归包（SRP）；
新厂商 = 新实现 + 注册（OCP，核心零改动）；跨协议取用走注册表式抽象工厂
（`ForProtocol`/`ForVendorFamily`）。**不做大爆炸重构**：一片一片迁移，每片
行为保持 + 既有测试全绿 + 新增契约门。

## 4. Slice 1 落地记录（本轮代码变更，行为保持）

`internal/tooldialect/tooldialect.go`（新包，叶子、零外部依赖）：

- `Dialect` 接口：`Name()` / `StartArguments(raw []byte) string`（`""`=占位符）
  / `MergeContinuation(start, buffered) (merged, ok)`（整文档校验+保守修复，
  `ok=false` ⇒ 丢弃尾部、保留已发 start——不回撤已发字节）。
- `AnthropicDialect`：占位符规则（`{}` ⇒ ""，与历史守卫逐字对齐）；`Validator`
  注入点（生产装配 = `anthropictransform.ValidateStreamingToolArgs`）。
- **注册表式抽象工厂**：`Register(protocol, dialect)` / `ForProtocol(protocol)`；
  **内置种子**保证「未装配即正确」（单元测试等不经装配的消费者也拿到占位符
  语义——2026-11-11 事故的教训正是「语义靠装配/下游过滤的假设静默失效」）；
  未注册协议回落**直通方言**（中立、不替厂商做假设）。

消费方迁移（三个现场 → 一个语义源）：

- `internal/ir/stream.go` ParseAnthropicStreamEvent：内联 `if arguments == "{}"`
  → `tooldialect.ForProtocol(ProtocolAnthropicMessages).StartArguments(...)`。
- `domains/streaming/anthropic_bridge.go` content_block_start：内联
  `!= "{}"` 守卫 → `toolArgsDialect.StartArguments(InputRaw)`。
- 同文件 content_block_stop：`anthropictransform.ValidateStreamingToolArgs` 直调
  → `toolArgsDialect.MergeContinuation(start, buffered)`（语义逐字保持：合法/
  修复 ⇒ 透传整文档由调用方切增量；非法 ⇒ 丢弃尾部 + `tool_args_continuation_
  discarded` 留痕）。
- 生产装配 `domains/streaming/toolargs_dialect_wiring.go`：注册带完整校验器的
  方言，桥直接持有实例（免查表、不受后续注册影响）。

### 4.1 门（有牙）

- `internal/tooldialect/tooldialect_test.go`：
  - **协议键契约门**（tooldialect ↔ ir 常量同值，漂移即红——字符串字面量对齐
    避免循环依赖的代价必须有门）；
  - **事故语义门**（`{}` 占位符 ⇒ ""、中继真实半截参数保留）；
  - **未装配兜底门**（种子方言 json.Valid 合并/丢弃尾部语义）；
  - **注入校验器门**（整文档透传含修复形状、失败 ⇒ ok=false）；
  - **OCP 演示门**（虚构厂商方言：不同占位符约定（`null`）+ 不同续拼策略，
    注册即生效、核心零改动；未注册协议直通不吞输入）。
- **变异验证**：把种子方言的占位符排除临时退化为直通 ⇒
  `TestAnthropicPlaceholderSemantics` 红 + 端到端
  `TestStreamAnthropicSSEToResponses_ToolUse_EmptyInputPlaceholderNotArgs` 红
  （2026-11-11 事故门跨包咬住方言层）⇒ 恢复全绿。
- 回归：`internal/tooldialect` / `internal/ir` / `domains/streaming`（155s 全量）
  全绿；`go build ./...` rc=0。

## 5. 验证（211）

- 部署（见 §6 时间线）后：公网版本指纹对拍 OK、`readyz` ready、
  `context_budget_exceeded` 与 `tool_json_invalid` 保持 0、真实流量 200 正常。
- 方言迁移为行为保持重构，211 实测与迁移前同形（tool-call 流量正常完成）。

## 6. 边界（本轮明确没做）

- Slice 2-6 未实施（本文档仅指派模式与切片顺序；每片独立一轮，行为保持）。
- `ir.anomaly` 的 thinking 噪声本轮不静默（那是 Slice 2 的正题：语义化投影，
  而不是把 WARN 关掉）。
- registry 无撤销接口（当前无撤销需求；加之前先加门）。
