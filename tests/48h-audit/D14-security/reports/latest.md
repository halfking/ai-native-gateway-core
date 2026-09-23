# D14 — 敏感信息处理（占位符 → 真实值） mock 全面验证

> 轮次：T2 · 日期：2026-09-23
> 入口：docs/全面测试/README.md §敏感信息处理（占位符 → 真实值）契约

## 概要

D14 从「场景安全」扩到「场景安全 + 敏感信息处理」，对齐 docs/全面测试 v2 mock 全面验证要求：

1. **修复实现缺口**：`security/sanitize/smart_sani_guard.go` 增加 `restoreToolCallsArgs` + `restoreJSONRecursive`，把 OpenAI chat 的 `tool_calls[*].function.arguments`（非流式 + 流式）、Anthropic 的 `delta.input` / `delta.partial_json` 一并做占位符还原；之前只覆盖 `content`，下游工具调用会拿到 `{SENSITIVE:phone:1}` 这种占位符文本，无法拨通/查询/支付。
2. **补齐 mock 测试**：D14 域业务/数据/压力/安全 4 类测试钉死工具调用还原路径。
3. **方案文档升级**：`docs/全面测试/README.md` v2 增补「Mock 全面验证（核心契约）」 + 「敏感信息处理（占位符 → 真实值）契约」两节。

## 实现变更（commit 钉桩）

| 文件 | 变更 |
|---|---|
| `security/sanitize/smart_sani_guard.go` | 新增 `restoreToolCallsArgs(ctx, s, obj, key, sm)`；新增 `restoreJSONRecursive(ctx, s, m, sm)`；`restoreResponseBody` 接入 message + 顶层 tool_calls 还原；`restoreStreamOpenAIDelta` 接入 delta.tool_calls 还原；`restoreStreamAnthropicDelta` 接入 delta.input / delta.partial_json 还原 |

## 测试矩阵（mock 环境 / miniredis 内嵌 / 无外部依赖）

| 类别 | 测试 | 断言要点 |
|---|---|---|
| business | TestBusiness_ToolCallsArgs_Restore_NonStream | OpenAI chat 完成式 + 多 tool_calls + 含/不含占位符 arguments；JSON 反序列化后真实值落位 |
| business | TestBusiness_ToolCallsArgs_Restore_Stream | OpenAI chat 流式 delta.tool_calls.arguments 还原 + SSE 帧结构保留 |
| business | TestBusiness_ToolCallsArgs_UnknownPlaceholderMasked | 含已知 + 多个未知占位符的 arguments 还原 + mask |
| data | TestData_ToolCallsArgs_NestedJSONRestored | 嵌套 map + 数组内 map 字符串字段都还原；非字符串字段不变 |
| data | TestData_ToolCallsArgs_InvalidJSONFallbackToStringReplace | arguments 非合法 JSON 时退化为字符串占位符替换 |
| stress | TestStress_ToolCallsArgs_ConcurrentRestore | 1000 轮 / 16 goroutine 并发还原，无 race |
| stress | BenchmarkStress_ToolCallsArgs_Restore | 单次还原 b.N bench（mock 环境无网络） |
| safety | TestSafety_ToolCallsArgs_ForgedPlaceholderMasked | LLM 伪造占位符 → mask + `SanitizePlaceholderTamperingTotal` 计数自增 |
| safety | TestSafety_ToolCallsArgs_CrossTenantIsolated | 跨租户隔离（tool_calls.arguments 路径），A 拿不到 B 的真实值 |

## 验收门

```bash
go test -race -timeout 120s ./tests/48h-audit/D14-security/...   # 4/4 PASS
go test -race -timeout 120s ./security/sanitize/...               # 全 PASS（无回归）
```

## 已知缺口 / 后续

- **跨 chunk SSE 帧**：占位符被拆到两个独立 SSE 事件时不会被还原（`TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks` 已钉住）。修复路径在 `streaming.handler` 层加 SSE 帧缓冲，与本轮无关。
- **Anthropic partial_json 增量还原**：单 chunk 可能只含 `{` 或 `"key":"val` 的半截，PlaceholderPattern 不会匹配；需要 chatHandler 累计完整 JSON 后再做一轮还原（follow-up）。

## 下一步

- 把 D14 4 类测试接入 `bash docs/全面测试/48h-audit/scripts/run-all.sh`（已自动扫描 D14 目录）
- 在 R58/R59 48h 审计轮对 `streaming.handler` 跨 chunk SSE 缓冲做改造，把占位符拆分场景闭合
- 把 D14 mock 套件作为后续工具调用相关回归的回归锚