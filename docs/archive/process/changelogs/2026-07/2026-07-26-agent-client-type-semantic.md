---
title: 智能体客户端类型语义检测 + 可扩展模式库
date: 2026-07-26
author: ACC Agent
scope: telemetry/, domains/streaming/, autoroute/
---

# 智能体客户端类型语义检测 + 可扩展模式库

## 改动清单

### 1. 智能体语义检测：补齐缺失 Agent，用系统提示词兜底

**问题**：opencode/zcode/codex 等客户端在 HTTP 头的识别不完整（`ExtractAgentName` / `ExtractAgentType` / `isIDEClient` 均缺少），且某些 Agent 使用通用 User-Agent 头（如 Node.js `undici`），无法从 HTTP 头识别客户端类型，导致 `client_type` 统计将智能体归类为 `api`/`bot`。

**修改**：

- `telemetry/request_metadata.go`:
  - `ExtractAgentName()` 新增 opencode/zcode/codex 识别
  - `ExtractAgentType()` 新增 opencode/zcode/codex 识别
  - 新增 `DetectAgentFromSystemPrompt(systemPrompt string) string`：把 `agentSystemPromptPatterns` 从硬编码变量升级为 `sync.RWMutex` 保护的并发安全注册表，支持 `RegisterAgentPattern()` 运行时扩展
  - 新增 `EnrichAgentNameFromSystemPrompt(headerName, systemPrompt string) string`：合并 header 检测 + 语义兜底，供 session 级 `agent_name` 写入点二次 enrich

- `domains/streaming/client_fingerprint.go`:
  - `extractClientType()` 新增 opencode/zcode/codex 的 User-Agent 匹配
  - 新增 `extractClientTypeWithPrompt(r, systemPrompt)` — 先 header 检测，header 为 `api`/`bot` 时用系统提示词语义兜底
  - 新增 `client_fingerprint_test.go` 测试文件

- `autoroute/classifier_ide.go`:
  - `isIDEClient()` 新增 opencode/zcode/codex

- `domains/streaming/auto_route.go` + `auto_route_nonchat.go`:
  - 3 处 call site 把 `extractClientType(r)` 替换为 `extractClientTypeWithPrompt(r, sigs.SystemPrompt)` 实现语义兜底

### 2. 可扩展模式注册表

**问题**：`agentSystemPromptPatterns` 是包级硬编码变量，新增 Agent 需修改源代码。

**修改**：

- `telemetry/request_metadata.go`:
  - 新增 `RegisterAgentPattern(name string, patterns ...string)` — 运行时注册自定义模式，写入即生效，并发安全
  - 新增 `ResetAgentPatterns()` — 恢复出厂默认模式（测试隔离用）
  - 新增配套测试：`TestRegisterAgentPattern`、`TestRegisterAgentPattern_DuplicateName`、`TestRegisterAgentPattern_EmptyName`、`TestResetAgentPatterns`、`TestEnrichAgentNameFromSystemPrompt`

## 提交历史

```
6a74db9e7 feat(autoroute): 添加 OpenCode/ZCode/Codex 智能体识别 + 系统提示词语义检测
34dcd3877 feat(telemetry): 智能体客户端类型语义检测 + 可扩展模式注册表
```

## 验证

- ✅ `go build ./...` + `go vet ./...` 通过
- ✅ `go test ./telemetry/ ./domains/streaming/ ./autoroute/` 全部通过
- ✅ 20 个新增/修改的测试用例覆盖语义检测、模式注册、并发安全、空值边界
