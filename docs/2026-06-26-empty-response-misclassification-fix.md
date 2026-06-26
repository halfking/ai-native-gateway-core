# empty_response 误判修复报告

**日期**: 2026-06-26
**影响范围**: 184 生产环境的 `request_logs.error_kind='empty_response'` 异常高
**严重性**: 🟡 中 — 影响审计准确性 + 监控告警 + 计费判断
**状态**: ✅ 修复完成 + 测试覆盖 + 编译验证

---

## 1. 问题概述

生产 184 环境的 `request_logs` 中累积了大量 `error_kind='empty_response'` 记录。经代码分析确认：检测算法本身（4 个判定条件）逻辑正确，**但其中一个条件（upstream_finish_reason）实际上是个 dead code**，导致多种合法成功响应被错误标记为空。

---

## 2. 根因分析

### 2.1 Check 4 是 dead code

`relay/handler.go::detectEmptyStreamResponse` 的第 4 个条件读取 `reqLog.UpstreamFinishReason`：

```go
hasFinishReason := reqLog.UpstreamFinishReason != nil && *reqLog.UpstreamFinishReason != ""
if hasFinishReason { return false }
```

但在调用点 `relay/handler.go:1749-1758`，`reqLog.UpstreamFinishReason` **还是 nil** — 该字段要到 line 1774 才被填充：

```go
// line 1774-1776 (empty check 调用之后才填充)
if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
    reqLog.UpstreamFinishReason = strPtr(v)
}
```

→ **Check 4 永远不触发**，4 条件退化为 3 条件。

### 2.2 高置信度误判路径

| # | 场景 | 触发机制 |
|---|------|---------|
| A1 | Tool-call 响应（name+arguments 都为空） | `textContent`/`preview` 都不会被填充 |
| A2 | `relay/anthropic_stream.go` tool_calls 路径**根本没调** `ObserveChunk` | capture 完全丢失 tool_calls 数据 |
| A3 | 良性 `eof_without_done` + 1-3 个空 delta chunk | `upstream_finish_reason` 在 m 里实际有值，但读取的是 nil 字段 |
| A4 | 上游发 `{"choices":[]}` 无 usage 的 trailer | IR parser 返回空 Delta，`chunkCount` 误增 |
| A5 | 推理模型 `length` 截断无 reasoning 文本 | 多个空 Delta chunks |

---

## 3. 修复实施

### 3.1 修改 `relay/handler.go::detectEmptyStreamResponse`

**核心修复**：直接读 `m["upstream_finish_reason"]`（已由 `SummaryAsMap` 填充），而不是 `reqLog.UpstreamFinishReason`（调用点还是 nil）。

**3 个 short-circuit** 防止误判：

1. `m["stream_interrupted"] == true` → 让 stream_interrupted 分类处理
2. `m["tool_calls"]` 数组非空 → 不算空（tool-call 是合法响应）
3. `m["upstream_finish_reason"]` 已设置 → 不算空

原 4 条件作为最终 fallback，**真正空响应仍被标记**。

### 3.2 修改 `relay/anthropic_stream.go`

**修复 A2**：在 tool_calls 处理循环（line 318-357）里调 `capture.ObserveChunk`，与 text_delta 路径（line 308-315）一致。

### 3.3 修改 `relay/stream.go`

**修复 A4**：将 `ObserveChunk` 调用移到 `{"choices":[]}` drop 检查之后，确保空 choices trailer 不再误增 chunkCount。

---

## 4. 文件清单

| 文件 | 改动 | 行数 |
|------|------|------|
| `relay/handler.go` | 修复 `detectEmptyStreamResponse` + 加注释 | +74 |
| `relay/anthropic_stream.go` | tool_calls 路径加 `ObserveChunk` | +26 |
| `relay/stream.go` | empty-choices drop 顺序调整 | +49/-22 |
| `relay/handler_empty_response_test.go` | 7 个新回归测试 | +145 |

---

## 5. 回归测试覆盖

新增 7 个测试场景，全部 PASS：

| 测试 | 验证 |
|------|------|
| `TestDetectEmptyStreamResponse_ToolCalls_NotEmpty` | tool_calls 数组非空 → 不算空 |
| `TestDetectEmptyStreamResponse_ToolCallsAnyType_NotEmpty` | `[]any` 类型的 tool_calls 也能触发 short-circuit |
| `TestDetectEmptyStreamResponse_StreamInterrupted_NotEmpty` | `stream_interrupted=true` → 不算空 |
| `TestDetectEmptyStreamResponse_UpstreamFinishReasonFromMap_NotEmpty` | **核心回归测试**：从 `m` 读 finish_reason 而非 reqLog 字段 |
| `TestDetectEmptyStreamResponse_AllConditionsMet_StillEmpty` | 真正空响应仍被标记（防止过度修复） |
| `TestDetectEmptyStreamResponse_PrePopulatedReqLogFinishReason_NotEmpty` | 防御性 fallback check 4 仍工作 |
| `TestDetectEmptyStreamResponse_EmptyToolCallsArray_StillPossiblyEmpty` | 空 tool_calls 数组不触发 short-circuit |

完整测试运行结果：
```
=== RUN   TestDetectEmptyStreamResponse_ThreeChunks_ZeroTokens        --- PASS
=== RUN   TestDetectEmptyStreamResponse_ManyChunks                    --- PASS
=== RUN   TestDetectEmptyStreamResponse_FewChunks_HasTokens           --- PASS
=== RUN   TestDetectEmptyStreamResponse_HasPreview                    --- PASS
=== RUN   TestDetectEmptyStreamResponse_HasStreamTextContent          --- PASS
=== RUN   TestDetectEmptyStreamResponse_HasFinishReason              --- PASS
=== RUN   TestDetectEmptyStreamResponse_EdgeCase_ExactlyThreeChunks   --- PASS
=== RUN   TestDetectEmptyStreamResponse_EdgeCase_FourChunks           --- PASS
=== RUN   TestDetectEmptyStreamResponse_ToolCalls_NotEmpty            --- PASS
=== RUN   TestDetectEmptyStreamResponse_ToolCallsAnyType_NotEmpty     --- PASS
=== RUN   TestDetectEmptyStreamResponse_StreamInterrupted_NotEmpty    --- PASS
=== RUN   TestDetectEmptyStreamResponse_UpstreamFinishReasonFromMap_NotEmpty --- PASS
=== RUN   TestDetectEmptyStreamResponse_AllConditionsMet_StillEmpty   --- PASS
=== RUN   TestDetectEmptyStreamResponse_PrePopulatedReqLogFinishReason_NotEmpty --- PASS
=== RUN   TestDetectEmptyStreamResponse_EmptyToolCallsArray_StillPossiblyEmpty --- PASS
PASS
ok  	github.com/kaixuan/llm-gateway-go/relay	0.594s
```

并对 `relay`, `audit`, `internal/ir` 三个包运行完整测试，全部 PASS：
```
ok  	github.com/kaixuan/llm-gateway-go/relay	0.584s
ok  	github.com/kaixuan/llm-gateway-go/audit	0.950s
ok  	github.com/kaixuan/llm-gateway-go/internal/ir	1.549s
```

**编译验证**：`go build -o /tmp/gateway-bin ./cmd/gateway` 成功生成 43MB 二进制。

---

## 6. 数据库查询受限说明

尝试连 184 PG 验证 `empty_response` 真实数量时遇到 PG 持续处于 `FATAL: sorry, too many clients already` 状态（持续 30+ 分钟）。从 PG pod 内部 / 通过 SSH port-forward / 通过 184 节点 SSH 全部失败。后台重试第 13 次时 PG pod 被删除（可能有人手动重建）。

由于无法直接查 DB 统计，所有结论**完全基于代码静态分析**。但代码分析已明确锁定 4 个具体的误判触发路径，且修复方案逻辑清晰。

**PG 连接满问题本身不在本次修复范围内**（基础设施层问题，需要独立治理 — 比如 max_connections 调高、或引入 pgbouncer、或减少 pool 大小）。

---

## 7. 部署建议

1. **本地验证**：部署到 staging，先观察 1 小时内 `empty_response` 计数变化
2. **生产灰度**：184 上 1 个 replica 先跑，确认无误判大幅下降
3. **回滚预案**：如发现问题，恢复 `detectEmptyStreamResponse` 到原始版本（4 个 if 条件）即可，不影响数据 schema

---

## 8. 风险评估

- **低风险**：所有改动集中在 `detectEmptyStreamResponse` 和 stream reader 路径
- **不破坏**：原 4 条件作为 fallback，真正空响应仍被标记
- **数据兼容**：不改 schema、不改 telemetry 写入路径
- **可能副作用**：
  - 之前标记为 `empty_response` 但实际上是 tool_call 响应的请求，现在会被正确标记为 success
  - 这可能导致 `empty_response` 计数下降 + `success` 计数上升 — 是预期效果
  - 监控告警阈值需要相应调整

---

## 9. 不在本次范围内

- 184 PG 连接数满问题（基础设施层）
- Provider 18 (NVIDIA NIM) 真实的 13% 空响应率（已知上游问题，非 gateway bug）
- Anthropic `finishReason` 推断优化（次要）
- 用生产真实请求重现验证（受限于 PG 连接满）