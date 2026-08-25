# 2026-08-26 会话总结识别智能体类型 / 专家类型 / 标签（migration 606）

## 背景

用户（老板）要求：审计"请求客户端类型识别（智能体）及专家类型"的准确识别。会话中的系统提示词可直接识别智能体与专家类型：

| 系统提示词首句 | 智能体 | 专家类型 |
|---|---|---|
| "You are an AI coding assistant, powered by Composer. You operate in Cursor..." | Cursor | software_engineering |
| "You are ZCode, an interactive coding agent. You are an agent for ZCode CLI." | ZCode | software_engineering |
| "You are opencode, an interactive CLI tool that helps users with software engineering tasks." | opencode | software_engineering |

## 问题诊断（现状差距）

| 层 | 现状 | 缺口 |
|---|---|---|
| 规则引擎 | `telemetry.DetectAgentFromSystemPrompt`（16 类 agent）、`sessionmeta.DetectExpertFromSystemPrompt`（14 类 expert）已就绪 | 只在到达态 provisional 元数据（sessionmeta.Extract）中用，未进总结流程 |
| 总结 LLM prompt | `buildSummaryPrompt` / `buildRollingPrompt` 只塞用户/助手消息 | **完全不含系统提示词**，LLM 无从判断智能体/专家 |
| 输出 schema | `{title, summary, key_topics, user_intent}` | 无 `agent_type` / `expert_type` / `tags` |
| 持久化 | `session_summaries` 表 + `summarystore.Upsert` | 无对应列 |

## 方案（5 处改动）

1. **Migration 606** — `session_summaries` 加 `agent_type TEXT NOT NULL DEFAULT ''` / `expert_type TEXT NOT NULL DEFAULT ''` / `tags TEXT[] NOT NULL DEFAULT '{}'`（幂等 `IF NOT EXISTS` + 单事务；非分区、无 VIEW 挂载，rule 49 §9.2 不适用）。
2. **`domains/sessionsummary/system_prompt_prefix.go`（新）** — 可选能力接口 `SystemPromptSource`；V1 实现取该会话首个请求的 `request_logs_bodies.request_body`（兼容 OpenAI Chat `messages[].role=system` / Anthropic `system`（string|block 数组）/ Responses `instructions`），V2 实现取首个 `session_bodies.request_delta` 中的 system 消息；统一 rune 边界安全截断到 4096 字节 + `secretmask.MaskSecrets`。
3. **`summarizer.go`** — `GenerateSummary` / `GenerateRollingSummary` 取系统提示词前缀并传入 prompt builder；两个 builder 输出 schema 扩展 `agent_type` / `expert_type` / `tags`，并有"智能体身份在系统提示词开头"的提示 + `<system_prompt>` XML 包裹防注入；`parseSummaryResponse` 解析新字段（unknown 归并为空）；`enrichSessionIdentity` 用规则引擎兜底补齐 LLM 未识别值；tags 规整（trim/lower/dedupe/cap 8×32）。
4. **`internal/summarystore/store.go`** — `Summary` 加三字段；`Upsert` / `UpsertCAS` 的 INSERT/UPDATE 双路径同步落新列；新增 `normalizeIdentity`（UTF-8 清洗 + tags nil→empty 兜底，防 TEXT[] NOT NULL 违约束）。
5. **测试** — `system_prompt_prefix_test.go`：三协议提取、超长截断 UTF-8 安全、secret 脱敏、新字段解析、legacy 兼容、规则兜底不覆盖 LLM 值。

## 设计决策

- **可选能力接口而非扩展 MessageSource**：`MessageSource` 是公共契约，直接加方法会破坏现有实现与测试 fake；类型断言启用，取不到静默降级（向后兼容）。
- **规则兜底而非纯 LLM**：LLM 可能拒答或输出 unknown；规则引擎是既有 SSOT（16+14 类模式），准确率可控、零成本、零延迟。
- **tags 用 TEXT[] 而非 JSONB**：与 `key_topics` 一致、pgx 原生 marshal、查询简单。
- **4096 字节上限**：主流 coding agent 的身份自述全在前几百字节（Cursor/ZCode/opencode 实测），4KB 留足余量且不明显放大 token 消耗。
- **admin/auto_summary_generator 不在本次改动**：其 system prompt 走 `adminLLMTask` 配置（`auto_summary_specs`），属配置侧变更，单独 follow-up。

## 验证

- `go build ./...` exit 0（仅 vendor cgo 警告，与本次无关）
- `go vet ./domains/sessionsummary ./internal/summarystore` exit 0
- `go test ./domains/sessionsummary ./internal/summarystore ./domains/analysis/sessionmeta ./telemetry` 全绿（含新增 6 组测试 14 个用例）
- Migration 606 语法按既有 startup migrations 模板（`\set ON_ERROR_STOP on` + BEGIN/COMMIT + IF NOT EXISTS + down 脚本）

## 遗留与后续

- `admin/auto_summary_generator.go`（on-request auto summary）的 prompt 由 `adminLLMTask` 配置驱动，要输出同样字段需改配置侧 system prompt；新列其 Upsert 路径天然兼容（空值即空串，列有 DEFAULT）。
- SQL 部署需走 rule 44 六阶段流水线（245 → 154 门禁）把 migration 606 推进生产。

## 影响文件

- `sql/migrations/startup/606_session_summaries_agent_expert_tags.{sql,down.sql}`（新）
- `internal/summarystore/store.go`（改）
- `domains/sessionsummary/system_prompt_prefix.go`（新）
- `domains/sessionsummary/summarizer.go`（改）
- `domains/sessionsummary/system_prompt_prefix_test.go`（新）
- `CHANGELOG.md`（Unreleased > Added）
