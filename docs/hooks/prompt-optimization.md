# Prompt Optimization Hook（提示词优化 Hook）

> 会话优化统一架构 Phase 1 · 子任务4
> 包路径：`domains/hooks/promptoptimization/`
> 状态：✅ 已实现并注册（默认关闭）

## 功能概述

在请求发送到 LLM 之前，自动把 system/user prompt 发送给
[prompt-optimizer-service](../../../prompt-optimizer-service) 优化，再把优化后的内容写回请求体。

- **默认关闭**：`PROMPT_OPTIMIZATION_ENABLED=1` 显式开启，对存量流量零影响
- **SHA256 结果缓存**：相同 prompt 不重复优化（目标命中率 >60%）
- **失败回退**：优化服务超时/5xx/响应异常时，原始 prompt 原样通过，主流程不受影响
- **审计**：优化前后 SHA256/字符数对比写入 `env.Metadata["prompt_optimization"]`，
  并输出结构化日志（`prompt_optimization: applied`）
- **字段保全**：请求体以 `json.Decoder.UseNumber` 解析、整体写回，
  `temperature`/`tools`/`stream` 等未知字段与大整数字面量不丢失

## Pipeline 位置

```
PhaseTransform:
  transform(passthrough, priority 50)
  → prompt_optimization(priority 90)   ← 本 Hook，压缩前执行
  → compression(priority 100)
```

先优化再压缩：优化基于原始语义进行，压缩产物不会被重复优化。

注册位置：`cmd/gateway/main_pipeline.go` 的 `buildV2DispatchPipeline()`，
stage 名 `prompt_optimization`。

## 配置项（环境变量）

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PROMPT_OPTIMIZATION_ENABLED` | `false` | 总开关（默认关闭） |
| `PROMPT_OPTIMIZER_URL` | `http://prompt-optimizer:8090` | 优化服务基地址 |
| `OPTIMIZATION_CACHE_TTL` | `24h` | 优化结果缓存 TTL |
| `OPTIMIZATION_TIMEOUT` | `5s` | 单次优化调用超时（p95 延迟目标 <500ms，超时即回退） |
| `OPTIMIZATION_MODE` | `both` | 优化范围：`system` / `user` / `both` |
| `OPTIMIZATION_MODEL_WHITELIST` | 空（全部允许） | 模型白名单，逗号分隔 |
| `OPTIMIZATION_MODEL_BLACKLIST` | 空 | 模型黑名单，优先于白名单 |
| `OPTIMIZATION_TENANTS` | 空（全部租户） | 租户白名单（租户级开关），逗号分隔 |

## 跳过条件（不调用优化服务）

按顺序判断，任一命中即跳过：

1. Hook 未启用（`PROMPT_OPTIMIZATION_ENABLED != 1`）
2. 请求体为空或不是 JSON object
3. 模型在黑名单 / 不在白名单
4. 租户不在 `OPTIMIZATION_TENANTS` 白名单
5. 按 `OPTIMIZATION_MODE` 过滤后无可优化消息
   （multimodal content parts、空白 content 均跳过）

## 回退条件（原始 prompt 照常通过）

- 优化服务连接失败 / 非 2xx / 响应解析失败 / 空结果
- 返回的 prompts 数量与提取数量不一致（防御）
- 写回时 re-marshal 失败

每次回退：`metrics.Fallbacks`/`OptimizationErrors` 计数 +1，
metadata 记录 `fallback=true` 与错误信息，日志输出
`prompt_optimization: optimizer call failed, fallback to original`。

## 审计日志

**metadata**（`env.Metadata["prompt_optimization"]`，供 audit stage 落库）：

| 字段 | 说明 |
|---|---|
| `hook` / `model` / `mode` | Hook 名、请求模型、优化模式 |
| `cache_hit` / `latency_ms` | 是否命中缓存、本阶段耗时 |
| `original_sha256` / `original_chars` | 优化前指纹（拼接全部目标 prompt） |
| `optimized_sha256` / `optimized_chars` | 优化后指纹（未变化时与 original 相同） |
| `error` / `fallback` | 仅失败回退时出现 |

**结构化日志**（`slog`）：`prompt_optimization: applied`，含
`changed_roles`、`original_tokens`/`optimized_tokens`、`optimization_id` 等。

## 与 prompt-optimizer-service 的契约

调用 `POST {OPTIMIZER_URL}/api/v1/prompts/optimize`：

```json
// 请求
{"tenant_id":"t1","model":"gpt-4o","prompts":[{"role":"system","content":"..."},{"role":"user","content":"..."}]}
// 响应 200
{"optimization_id":"opt_ab12cd34","cache_hit":false,"original_tokens":1200,"optimized_tokens":950,"latency_ms":320,
 "prompts":[{"role":"system","content":"优化后","changed":true},{"role":"user","content":"原内容","changed":false}]}
```

完整契约见 `ai-native-tools/docs/session-optimization-api-contract.md`；
Go SDK：`prompt-optimizer-service/pkg/client`。

## 性能目标

| 指标 | 目标 |
|---|---|
| 优化延迟（p95） | <500ms（超过 `OPTIMIZATION_TIMEOUT` 直接回退） |
| 缓存命中率 | >60% |
| 失败回退率 | <1% |
| 主流程影响 | 0（失败即回退，OnError 吞错） |

进程内指标可通过 `hook.Metrics.Snapshot()` 获取（optimizations / cache_hits /
cache_misses / optimization_errors / fallbacks），随结构化日志输出。

## 测试

```bash
go test ./domains/hooks/promptoptimization/... -count=1 -v
```

覆盖：默认关闭、env 解析、白名单/黑名单/租户开关、缓存命中与过期、
优化应用与字段保全、多模态跳过、失败回退、数量不一致防御、
OnError 吞错、大整数往返保真、端到端（未命中→命中→宕机回退）。

**语句覆盖率：84.6%**（`go test -cover`，满足验收标准 >80%）。
