# 专家类型识别（Expert Type Detection）

> 日期：2026-08-25
> 作者：claude-code (claude-opus-4-1)
> 范围：sessionmeta.Extract → 增加 Expert 字段

## 1. 问题

`sessionmeta.Result` 当前已包含 `Title / Agent / Client / WorkTypes / Project / Features / Evidence / Provenance`，但**没有"专家类型"概念**。下游报表、计费、路由决策常需要按"做什么类型的工作"切分（如 software_engineering / data_science / security 等）。

`AgentIdentity.Role` 字段（`coding_agent / ide_client / other_agent`）粒度太粗，不能区分：
- 同样用 Cursor，一个是写前端（software_engineering），一个是做数据可视化（data_science）
- 同样用 ZCode，一个是 debug（debugging ∈ WorkTypes），一个是写测试（testing）

## 2. 方案

### 2.1 新增字段

`Result.Expert ExpertIdentity` —— 与 `Agent` 平级：
- `Type` —— 专家类型字符串（如 `software_engineering` / `data_science` / `security`）
- `Source` —— `header` / `system_prompt` / `unknown`
- `Confidence` —— 0..1

### 2.2 专家类型枚举（13 类 + general + unknown）

| 类型 | 触发模式（system prompt 关键词） |
|------|-----------------------------------|
| `security` | security researcher / penetration tester / 安全研究员 / 渗透测试 |
| `data_science` | data scientist / ml engineer / 数据科学家 / 机器学习 |
| `devops` | devops engineer / sre / 运维工程师 |
| `testing` | qa engineer / sdet / 测试工程师 |
| `design` | ui designer / ux designer / UI 设计师 |
| `research` | researcher / academic researcher / 研究员 |
| `product_management` | product manager / pm / 产品经理 |
| `documentation` | technical writer / 技术写作 |
| `customer_support` | customer support / 客服 |
| `finance` | financial analyst / 财务分析师 |
| `legal` | legal advisor / 法务 |
| `marketing` | marketing manager / 营销经理 |
| `translation` | translator / 翻译 |
| `software_engineering` | coding assistant / software engineer / 软件工程师（catch-all，但排在 general 之前）|
| `general` | helpful assistant / 通用助手（兜底）|
| `unknown` | 默认 |

### 2.3 优先级

specific roles (security/data_science/devops/...) → `software_engineering` (catch-all) → `general` → `unknown`

> 顺序很重要：`security` 必须排在 `software_engineering` 之前，否则 "you are a security engineer" 会被误判为 software_engineering（因为 software_engineering 包含 "you are an engineer"）。

### 2.4 输入字段：`Input.Expert string`

显式传入优先；未传则从 system prompt 探测。

### 2.5 与现有架构的关系

| 已有 | 关系 |
|------|------|
| `telemetry.DetectAgentFromSystemPrompt` | 同类（识别 agent name）；本规范是"识别 expert type" |
| `sessionmeta.AgentIdentity.Role` (`coding_agent`/`ide_client`) | 粒度太粗；expert 是更细的"做什么工作" |
| `sessionmeta.extractWorkTypes()` | 识别"当前任务"（debug/refactor/feature...）；本规范识别"专家身份" |
| `provisionalTitle()` 加 `[agent]` 前缀 | **不动**，避免标题过长 |

## 3. 不做

- ❌ 不改 auto_title_generator.go（保留向后兼容）
- ❌ 不改 DB schema（Result 是 JSON 入 session_metadata 表）
- ❌ 不改 existing fields（`Agent / Client / WorkTypes`）；新字段 additive
- ❌ 不动 prompt 截断阈值（沿用 `MaxSystemRunes=8192`，足够覆盖 system prompt 头部的 agent/expert 自描述句子）

## 4. 风险

| 风险 | 缓解 |
|------|------|
| 专家类型判断错（false positive） | pattern 优先级严格 specific-first + confidence 字段；显式 `Input.Expert` 可覆盖 |
| 新增 expert 类型需要改代码 | 提供 `RegisterExpertPattern()` 运行时注册；默认注册表写死在 `defaultExpertPatterns()` |
| system prompt 截断后丢失 expert 信息 | `MaxSystemRunes=8192` 足够；expert 自描述总在 prompt 开头 |

## 5. 验收

- ✅ `go build ./...` 通过
- ✅ `go test ./domains/analysis/sessionmeta/...` 全绿
- ✅ 13 个 agent + 13 个 expert 类型测试覆盖
- ✅ 优先级测试：security 不会被 software_engineering 截胡
- ✅ header > system prompt 优先级
- ✅ JSON 输出包含 `"expert"` 字段（omitempty）

## 6. 文件清单

| 文件 | 类型 | 说明 |
|------|------|------|
| `domains/analysis/sessionmeta/extractor_expert.go` | 新增 | expert pattern registry + detector + types |
| `domains/analysis/sessionmeta/extractor_expert_test.go` | 新增 | 单元测试（覆盖率 ≥ 90%） |
| `domains/analysis/sessionmeta/extractor.go` | 修改 | Result 加 Expert 字段 + Extract 调用 extractExpert + Input 加 Expert override |
| `telemetry/request_metadata.go` | 修改 | cursor patterns 扩展（`powered by composer` / `operate in cursor` / `cursor's ai`） |

行数预算：新增 ~210 行 + 修改 ~10 行 + 测试 ~150 行，合计 ~370 行

## 7. 设计文档与实现一致性说明（审计补遗，2026-08-25）

审计时发现 §6 初版曾描述"修改 `extractor_rules.go`：Expert 字段挂到 provisionalTitle 入参"，
但实际未实施，且与 §3 不做清单（"不改 auto_title_generator.go，保留向后兼容"）+ §2.5
（"`provisionalTitle()` 加 `[agent]` 前缀 — 不动，避免标题过长"）明确冲突。删除该行，
让 §6 与 §3 / §2.5 / 实际代码三者一致。