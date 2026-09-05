# 05 — 数据结构转换（IR / Provider 变换）

> 主题：消息在 provider 协议（OpenAI/Anthropic/Gemini/Responses）间的转换、净化、角色归一化、provider 适配。本仓库的 `internal/ir` **超集 hub-and-spoke + Raw 透传 + 净化顺序修复**已优于 omniroute 的 `Record<string,unknown>` 变换；omniroute 的**声明式系统变换 DSL** 值得吸收。

## 1. 现状对比

| 维度 | llm-gateway-go | omniroute |
|---|---|---|
| **IR 架构** | 超集 hub-and-spoke：Parsers(4) → `InternalRequest`(协议超集) → Serializers(4)，加协议 O(N) 非 O(N²)（`ir/types.go:4-17`） | translator 注册表 from:to（`translator/registry.ts:20`）—— O(N²) 对，但每对独立 |
| **未知字段** | `Extensions map[string]json.RawMessage`（`:162-169`）+ `ToolDefinition.Raw`/`ContentBlock` 透传 → **无损往返** | `Record<string,unknown>` 全程，shape 错误运行时才暴露 |
| **消息净化** | `ValidateAndFixRequest` 5 步：removeEmptyMessages → SanitizeToolMessages → fixToolCallStructure → enforceRoleAlternation(warn) → normalizeSystemMessages（`validate_and_fix.go:25-44`） | `responsesInputSanitizer.ts:152`（Responses 专用）+ roleNormalizer |
| **净化顺序修复** | 2026-08-06：removeEmptyMessages **先于** SanitizeToolMessages（`:52-67` 注释），修复 Vercel AI SDK 空 user message 误删 tool_result | 无对应顺序问题（无 removeEmptyMessages） |
| **工具 orphan 处理** | `SanitizeToolMessages`（`:28-109`）：tool 消息须引用**紧邻前一条** assistant 的 tool_calls.id；orphan 删除 | `fixToolPairs`（`contextManager.ts:608`）事后修补，跨格式 |
| **角色归一化** | IR 内 + provider 序列化分支（MiniMax 用 tool_call_id 等，`types.go:171-178`） | `roleNormalizer.ts:269`：model→assistant、developer→system(allowlist)、system 折叠；**GLM 版本感知**（5.1+ 保留，`:65`） |
| **provider 适配** | 零散：各 serializer 内 if/else；`sanitize.go` | **声明式 DSL** `TransformOp`（drop_paragraph/replace_text/inject_billing_header/...）（`ccBridgeTransforms.ts:33`）+ per-provider registry（`systemTransforms.ts:69`） |
| **Responses 净化** | `parse_responses.go`/`serialize_responses.go` | `responsesInputSanitizer.ts:42/64/83/99`（input_image/output_text 角色不对称、server-item-id 前缀） |

## 2. omniroute 的优点（可吸收）

1. **声明式 provider 变换 DSL（`TransformOp` 判别联合）**（`ccBridgeTransforms.ts:33`、`systemTransforms.ts:69`）：新 provider 适配是**配置**而非代码；执行器幂等纯函数，可单测。本仓库 provider 适配散在各 serializer 的 if/else——**强烈建议吸收**（把“段落删除/文本替换/系统块注入”类适配收敛为声明式 op）。
2. **GLM 版本感知角色归一化**（`roleNormalizer.ts:65`）：GLM 5.1+ 接受 system，≤5.0 折叠到首条 user。本仓库 GLM 适配需核对是否有同等版本感知——若有版本分支则保留，若无则**吸收**。
3. **Responses 输入净化的细节**（`responsesInputSanitizer.ts:42/64/83/99`）：`input_image`↔`output_text`↔`input_text` 在 assistant/non-assistant 角色间的不对称、server-item-id 前缀（`fc_|msg_|rs_`）、函数名净化（非 `[a-zA-Z0-9_-]`→`_`，截断 128）。本仓库 Responses 处理需逐项核对——**吸收缺失项**（防 OpenAI Responses 400）。

## 3. 本仓库的优点（保留）

1. **IR 超集 + Raw 透传 = 无损往返**：omniroute `Record<string,unknown>` 无类型保证。本仓库对未知/扩展字段（如 Anthropic `CacheControl`、`Documents`、`MCPServers`、Gemini `GenerationConfig`）有显式字段 + Extensions 兜底。**保留**。
2. **净化顺序修复（removeEmptyMessages 先）**：精确诊断 + 内联安全不变量注释（never 删带 tool_call_id/tool_calls 的消息）。**保留**。
3. **`SanitizeToolMessages` 前置 scope 规则** 优于 omniroute 事后 `fixToolPairs`：本仓库是“orphan 不产生”（删前判断 scope），omniroute 是“orphan 产生后修”。
4. **MiniMax 等 provider 的序列化变体**（`tool_call_id` vs `tool_use_id`，`types.go:171-178`）由 `SourceProtocol`/`TargetProvider` 驱动，集中。

## 4. 缺口 / 债务

| 编号 | 债务 | 证据 | 影响 |
|---|---|---|---|
| E1 | **provider 适配散在 serializer if/else，无声明式收敛** | 各 serialize_*.go | 新 provider/新适配改动散、难测 |
| E2 | **`generateToolCallID` 仅 10 个、易碰撞、写法晦涩** | `validate_and_fix.go:318-320`（`string([]byte{...})`、`index%10`） | >10 tool_calls 时 id 碰撞 → provider 混乱 |
| E3 | **`enforceRoleAlternation` 仅 warn 不修** | `validate_and_fix.go:237-269` | 严格交替的 provider 仍 400 |
| E4 | **GLM 版本感知是否完整待核** | 本仓库 GLM 适配分支待核 | 5.0/5.1 行为可能错 |
| E5 | **Responses 净化细节待核**（input_image/output_text 不对称、item-id 前缀、函数名） | 本仓库 `parse_responses.go` 待逐项核对 | OpenAI Responses 边缘 400 |
| E6 | **三套消息模型**（`ir.Message` 强 / `v2.Message` 弱 / `session.Session` 隐式） | 见 03-A5 | 转换有损、多模态/工具块丢信息 |

## 5. 优化建议（带优先级）

### E2（P0）修复 tool_call_id 生成器 `NEW-DESIGN`
把 `generateToolCallID` 改为单调/唯一（如 `fmt.Sprintf("call_gen_%d_%d", turnSeq, idx)` 或 UUID 短形），不限于 10 个。低风险、高收益（防碰撞）。

### E1（P1）声明式 provider 变换 DSL `NEW-DESIGN`（吸收 omniroute）
新建 `internal/ir/transforms/`：定义 `TransformOp` 判别联合（`DropParagraphIfContains`/`ReplaceText`/`ReplaceRegex`/`PrependSystemBlock`/`InjectHeader`/...）+ 纯函数执行器（幂等、可单测）+ per-provider 注册表。把现有 serializer 中“段落级系统提示改写/头部注入”类适配迁移为 op。
- **不吸收** omniroute 的 CCH 混淆/计费头伪装/ZWJ obfuscation（对抗性、脆弱、合规风险）——仅吸收**结构性变换 op**。
- 验收：新增一个 provider 的系统提示适配 = 加一条配置，不改 serializer 代码。

### E3（P1）enforceRoleAlternation 可选修复 `NEW-DESIGN`
对声明严格交替的 provider（如部分 OpenAI 模型），从 warn 升级为“合并连续同角色消息”的修复（可配）。默认仍 warn，按 provider 开关。

### E4/E5（P1）核对并补齐 GLM 版本感知 + Responses 净化 `TARGET-BOUNDARY` + `NEW-DESIGN`
逐项核对：
- GLM：是否有 5.1+ 版本分支；若无，吸收 omniroute `isGlmWithoutSystemRole` 逻辑。
- Responses：核对 `input_image`/`output_text` 角色不对称、server-item-id 前缀、函数名净化是否已覆盖；补齐缺失。
验收：用 omniroute sanitizer 的测试用例回归。

### E6（P1）统一消息模型 `NEW-DESIGN`（同 03-A5）
V2 `Message` 复用 `ir.Message`；`sessionv2mirror` 转换无损。前置于 V2 读全量放开。

## 6. 不做（明确非目标）

- **不引入 omniroute 的 translator O(N²) 注册表**：本仓库超集 hub-and-spoke 更优。
- **不吸收 CC-bridge 的对抗性变换**（身份注入、计费头伪装、ZWJ 混淆）：合规与稳定性风险。
- **不把 IR 改为 `map[string]any`**：保留强类型 + Raw 透传。

## 7. 与其他主题的依赖

- E6 同 03-A5，是 V2 读放开（03-A1）的前置。
- E1（DSL）独立，可先行。
- E2（id 修复）独立，P0 先行。
