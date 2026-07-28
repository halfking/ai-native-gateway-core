# 2026-07-29: 流式超时与重试阈值提升 — 修复长 reasoning 链中途被杀

> 摘要：commit `81e627ff1 chore(tuning): bump stream retry threshold 5→50, upstream timeout 180s→600s, adaptive timeout guard`
> 关联 PR：feature 分支 `fix/timeout-and-retry-thresholds` → main `2476176fd` (merge)
> 关联 issue：直连 vs 网关 tool-call 链稳定性差异的根因分析 (2026-07-29)

---

## 现象

通过 llm-gateway-go 的多步 tool call 链（含长 thinking / reasoning 模型）在 3 分钟
左右频繁中断；同样的请求直连供应商（OmniRoute / OpenCode / Claude Code）可以
稳定跑完。用户报告"任务直连能跑完，过网关就中途断"。

journald / `request_logs` 中可观察到大量：

- `error_kind=stream_timeout` / `upstream_timeout`（来源 `executor_chat.go:1588`
  的 `upstreamContext` 取消）
- `error_kind=first_byte_timeout`（thinking 模型首字节到达前超时）
- `error_kind=stream_non_resumable`（`StreamRetryThreshold=5` 已耗尽，后续中断不可恢复）

## 根因

经系统排查（rule 49 schema 真相 + rule 11 §14 段落级验证 + rule 37 §3 精准修改），
确认三层叠加：

### 1. 自适应超时上限过低

`config/timeout_config.go:153` 默认 `upstreamMaxSeconds: 180s`，DB 端的
`system_settings.timeout.upstream_max_seconds` 也是 180s。`calculateAdaptive`
在 `config/timeout_config.go:262-310` 的 `clamp`（line 313-321）把所有自适应结果
封顶在 `[20s, 180s]`。thinking 模型单次推理常超过 3 分钟。

### 2. 流式分支被自适应覆盖

`domains/streaming/executors/executor_chat.go:288-313`（fix 前）：

```go
timeout = e.StreamTimeout          // 900s
if e.TimeoutAdapter != nil {
    adaptiveTimeout := e.TimeoutAdapter.Calculate(...)
    timeout = adaptiveTimeout      // <-- 直接覆盖 StreamTimeout
}
```

即 180s 的自适应上限会**完全替换** StreamTimeout 的 15 分钟上限。这是流被中途杀掉
的直接原因。`NodeTimeout(120s)` floor（line 319-327）只在 adaptive < 120s 时生效，
对 180s 没有纠正作用。

### 3. StreamRetryThreshold 太小

`config/config.go:67` / `domains/streaming/executors/executor.go:913` 默认
`StreamRetryThreshold: 5`。`executor_chat.go:928` 用 `isResumable := streamOutcome.Resumable && streamOutcome.ChunkCount < e.StreamRetryThreshold`
判断是否可切 credential 重试。

工具调用链在前 5 个 chunk 即可耗尽此限额（status + role + tool_call id + tool_call
function name + 第一个 tool_call argument fragment）。之后任何中断都是
**永久失败**。

### 与 OmniRoute（直连）对比

| 维度                       | OmniRoute 直连              | llm-gateway-go（fix 前）|
| -------------------------- | --------------------------- | ------------------------ |
| 总请求超时                 | `FETCH_TIMEOUT_MS=600_000`  | 自适应 ≤ 180s             |
| chunk 间空闲超时           | `STREAM_IDLE_TIMEOUT=600s`  | `streamChunkTimeout=300s` |
| 首字节超时                 | `STREAM_READINESS=80s`      | `firstByteTimeout=120s`  |
| 跨租户 retry 阈值         | `STREAM_EARLY_EOF_MAX=1`    | `StreamRetryThreshold=5`  |

直连用 10 分钟作为单一上限，网关被切成多层紧超时 + 5 chunk 的脆弱阈值。

## Fix

### 1. 提高自适应上限（`config/timeout_config.go:153`）

```diff
-        upstreamMaxSeconds:     180,
+        upstreamMaxSeconds:     600,
```

对齐 OmniRoute 的 `FETCH_TIMEOUT_MS=600000ms`。DB 端的
`system_settings.timeout.upstream_max_seconds` 也需同步上调（操作手册待补充）。

### 2. 流式分支不再被自适应缩短（`executor_chat.go`）

重构：将超时选择从 `executeOpenAI` 抽到 `Executor.selectUpstreamTimeout`，
流式分支改为：

```diff
-            timeout = adaptiveTimeout
+            // Only use adaptive if it's longer than StreamTimeout —
+            // don't let it shorten long-running streaming responses.
+            if adaptiveTimeout > timeout {
+                timeout = adaptiveTimeout
+            }
```

不变量：

- 非流式：使用 `UpstreamTimeout(120s)`，自适应不介入
- 流式无 adapter：使用 `StreamTimeout(900s)`
- 流式有 adapter 且 `adaptiveTimeout > StreamTimeout`：用 adaptive（向上放宽）
- 流式有 adapter 且 `adaptiveTimeout <= StreamTimeout`：保留 `StreamTimeout(900s)`
- `NodeTimeout(120s)` 热配置保留为最高 floor（操作员可调）

### 3. 提高 StreamRetryThreshold（`config/config.go:255` + `executor.go:913`）

```diff
-        StreamRetryThreshold:               5,
+        StreamRetryThreshold:               50,
```

10× 放宽 failover 窗口。同步更新：

- `domains/streaming/executors/executor_common_test.go:34` 测试结构体
- `domains/streaming/executors/executor.go:607` 注释 `(default 5) → (default 50)`
- `domains/streaming/stream.go:112` 注释 `StreamRetryThreshold(5) → (50)`

## 验证

### 单元测试（rule 17 测试门禁）

`domains/streaming/executors/executor_chat_test.go` 新增 4 个测试钉死流式超时
不变量：

| 测试                                                         | 覆盖场景                                              |
| ------------------------------------------------------------ | ----------------------------------------------------- |
| `TestSelectUpstreamTimeout_NonStreamUsesUpstreamTimeout`     | 非流式不受自适应影响                                  |
| `TestSelectUpstreamTimeout_AdaptiveNeverShortensStream`      | 自适应永远不能缩短流式超时（核心 fix）                |
| `TestSelectUpstreamTimeout_AdaptiveCanExtendStream`          | 自适应仍可向上放宽（如慢节点）                        |
| `TestSelectUpstreamTimeout_NoAdapterUsesStreamTimeout`       | 无 adapter 时退化到 StreamTimeout(900s)               |

结果：

```
=== RUN   TestSelectUpstreamTimeout_NonStreamUsesUpstreamTimeout
--- PASS
=== RUN   TestSelectUpstreamTimeout_AdaptiveNeverShortensStream
--- PASS
=== RUN   TestSelectUpstreamTimeout_AdaptiveCanExtendStream
--- PASS
=== RUN   TestSelectUpstreamTimeout_NoAdapterUsesStreamTimeout
--- PASS
PASS
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors	10.383s
```

### Audit（rule 11 §10 任务完成总结）

- `go build ./...`：✅ 无输出
- `go vet ./...`：✅ 无输出
- `scripts/scan-secrets.sh --mode=strict --paths=config,domains/streaming/executors`：
  ✅ 0 findings
- `go test ./config/ -run TestTimeoutConfig -count=1`：✅ 8 subtests 全过
- `go test ./domains/streaming/ -count=1`：✅ 全部通过
- `go test ./domains/streaming/executors/ -count=1`：✅ 全部通过（含新增 4 个）

### 部署验证（待）

尚未部署到 71 / 245 / 154。需要：

1. `system_settings.timeout.upstream_max_seconds` 同步从 180 → 600（DB 端热配）
2. L4 业务链路：触发一次长 reasoning 流（如 Claude extended thinking ≥ 5 分钟），
   验证中途不再触发 `stream_timeout`
3. 7 天 `llm_gateway_stream_timeout_total` / `first_byte_timeout_total` 监控

## 改动清单

| 文件                                                          | 行数变化 | 类型     |
| ------------------------------------------------------------- | -------- | -------- |
| `config/config.go`                                            | +1/-1    | Modified |
| `config/timeout_config.go`                                    | +1/-1    | Modified |
| `domains/streaming/executors/executor.go`                     | +1/-1    | Modified |
| `domains/streaming/executors/executor_chat.go`                | +90/-62  | Refactored（提取 `selectUpstreamTimeout`） |
| `domains/streaming/executors/executor_common.go`              | +3/-13   | Comment 重排 |
| `domains/streaming/executors/executor_common_test.go`         | +1/-1    | Modified |
| `domains/streaming/executors/executor_chat_test.go`           | +87/-3   | Added 4 tests |
| `domains/streaming/stream.go`                                 | +1/-1    | Comment |
| `CHANGELOG.md`                                                | +25/-0   | Added entry |
| `docs/changelogs/2026-07-29-stream-timeout-and-retry-thresholds.md` | new (this file) | Archive |

总计：6 个生产文件改动 + 2 个测试/归档文件新增。

## 遗留与风险

- **风险 1（高）**：DB 端 `system_settings.timeout.upstream_max_seconds` 可能
  仍为 180（生产 DB 由 `TimeoutConfig.ReloadFromDB` 热加载，覆盖 defaults）。
  需同步上调，否则 fix 1 仅在 DB 不可达时生效。
- **风险 2（中）**：自适应放宽到 600s 后，慢 provider 上的 retry cost 可能上升
  （每个失败的最长上游调用延后到 600s）。建议监控 `llm_gateway_stream_timeout_total`
  7 天。
- **风险 3（中）**：`StreamRetryThreshold=50` 放宽后，credential failover 频率
  可能上升。需监控 `llm_gateway_credential_failover_total{kind="stream_non_resumable"}`。
- **未触及**：`upstream/client.go:119` `ResponseHeaderTimeout=120s` 和
  `streamChunkTimeout=300s`（`stream_runtime.go:39`）暂未调整。`firstByteTimeout`
  在 2026-07 已从 60s 上调到 120s，目前不再构成瓶颈。

## 下一步建议

1. **同步 DB 热配置**：在 deploy 前对 71 / 245 / 154 三个环境的
   `system_settings` 表执行：
   ```sql
   UPDATE system_settings
   SET value = '"600"'::jsonb
   WHERE key = 'timeout.upstream_max_seconds';
   ```
2. **部署顺序**：245 → 154（参考 deploy-245 skill 顺序）。
3. **回归观察**：7 天监控以下指标，确认长 reasoning 场景的中断率下降。
4. **后续优化（v2）**：考虑将 `StreamRetryThreshold` 改为基于时间的判断
   （如"30s 内可重试，30s 后仅记 success"），更贴合"已开始生成 = 不应中断"的语义。