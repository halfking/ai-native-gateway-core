# MiniMax-M3 网关稳定性修复总结

**执行时间**: 2026-07-29 01:14 CST
**服务器**: 154 (llm.kxpms.cn) + 245 (llmgo.kxpms.cn)
**版本**: 2.4.9-5bb2e99e → 本次修复 patch
**状态**: ✅ P1 代码修复 + 单元测试通过，待部署 245/154 + P0 调参

---

## 1. 症状复盘（从 154 当日日志中提取）

通过 SSH 抓取 `/opt/llm-gateway-go/logs/gateway.log`（275k 行）发现两条独立根因：

### 症状 A：首字节超时
4 次 `minimax-m3` 请求 `stream_ttfb_ms=0, stream_chunks=0, success=false, latency_ms=670–881ms`，全部出现在一次 `stream_timeout`（135s）切断同 credential 大请求后。

### 症状 B：任务执行一半自动停止
65 次 `executor: stream interrupted`，全部命中 Provider 14 / Credential 21（MiniMax-M3 直连）：
- 63 次 `reason=eof_without_done, classified_as=stream_timeout, benign_eof=true`
- 2 次真正的 `stream_chunk_timeout`（884/770 个 chunk）

直连 `api.minimaxi.com` 正常（curl TCP 0.072s，ping 11ms），NVIDIA NIM Provider 18 完全不受影响。

---

## 2. 根因分析

| 根因 | 触发条件 | 现象 |
|---|---|---|
| `eof_without_done` 被错误归类为 `stream_read_error` → 污染失败统计 | MiniMax 上游正常关闭但不发 `[DONE]` 哨兵 | operator 看到的失败率虚高 |
| 真正 chunk 超时（hit 600s 上限）| 长 thinking 流 > 600s 无 chunk | 流被强制切断 |
| Circuit breaker + degraded mode + `candidates_count=1` | 一次 stream_timeout 后熔断；同 credential 后续请求被强制接受 | 首字节到达前 fast-fail |
| Hint 文案 `"default 60s"` 已过时 | 运维误判调参基线 | 增加认知负担 |

---

## 3. 修复内容

### 修复 1（P1-A）：让 `eof_without_done` 不再污染 request_logs error_kind

**文件**：`domains/streaming/handler.go:5273-5294`

将 `streamErrorKindForDetailCode` 的 `eof_without_done` 从 `stream_read_error` 桶中拆出：

```go
case "eof_without_done":
    // 2026-07-29: 拆出 stream_read_error 桶，让 operator 看到的失败率不再
    // 包含 MiniMax 良性 EOF。chunks > 0 由 executor_chat.go isBenignEOF 短路
    // (RecordSuccess) 走成功路径；chunks == 0 仍然是真实失败，但有独立桶。
    return "eof_without_done"
case "read_error", "stream_read_error", "stream_panic":
    return "stream_read_error"
```

**同步测试**：`domains/streaming/stream_error_kind_test.go:21` 改为 `{"eof_without_done", "eof_without_done"}`。

**为什么是"防御性"修复**：
- `executor_chat.go:949-977` 的 `isBenignEOF` 已经正确识别 `eof_without_done+chunks>0` 并走 `RecordSuccess`，**不会**进入 `streamErrorKindForDetailCode`
- `classifyStreamInterruption` (`handler.go:5253-5258`) 已经把 `eof_without_done+chunks>0` 判为 `isError=false`
- 此修改保证未来 `isBenignEOF` 漏判时仍走正确的桶；同时让文档注释与代码一致

### 修复 2（P1-B）：更新过时 hint 文案

**文件**：`domains/streaming/stream.go:735`

```go
// 旧
"hint", "if timeout occurs frequently with chunks received, consider increasing llmgw_node_timeout_seconds (default 60s)"

// 新
"hint", "if timeout occurs frequently with chunks received, consider increasing llmgw_node_timeout_seconds (current default 120s, hotconfigurable via admin/settings)"
```

实际默认值 120s（`node_failover.go:130`），hint 文案陈旧误导运维。

### 修复 3（回归测试）：补 `chunks=0` 反向断言

**文件**：`domains/streaming/stream_eof_test.go` 新增 `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks`

驱动方式：使用 `data: not-valid-json\n\n`（非合法 SSE JSON）作为 firstLine，触发"eof_without_done + 0 valid chunks"路径。验证 `streamErrorKindForDetailCode("eof_without_done") == "eof_without_done"`，防止未来回归到 stream_read_error。

### 修复 4（P0）：154 生产 hotconfig 调参（待执行）

**方式**：`PUT /api/admin/settings/llmgw_node_timeout_seconds = 300`（JSON int, scope=platform）

**理由**：
- 真正 chunk 超时 2 次（884/770 chunk）发生在当前默认 120s 上限附近
- 提升至 300s 给长 thinking 流（MiniMax/Claude）足够缓冲
- `clampInt(hotCfg.GetInt("llmgw_node_timeout_seconds", 120), 10, 600)`，300 合法
- 30s 轮询 reload，无需重启

---

## 4. 部署与回滚

### 部署顺序
1. 本地 `make test-short` ✅
2. 推送 origin/main（pre-push hooks）
3. 走 245 → 154 晋级路径（`llm-gateway-deploy-test`）
4. 154 hotconfig PUT 调参
5. 30 分钟审计窗口：观察 stream interrupted / first_byte_timeout / chunk_count 分布

### 回滚
- 代码：单 commit 可 `git revert`
- hotconfig：单独 `PUT llmgw_node_timeout_seconds=120` 回滚

---

## 5. 关键文件清单

| 文件 | 行号 | 修改 |
|---|---|---|
| `domains/streaming/handler.go` | 5262-5296 | error_kind 映射 + 注释 |
| `domains/streaming/stream.go` | 735 | hint 文案 |
| `domains/streaming/stream_error_kind_test.go` | 21 | 测试断言 |
| `domains/streaming/stream_eof_test.go` | 41-90 | 新增 ZeroChunks 反向测试 |

---

## 6. 风险

| 风险 | 缓解 |
|---|---|
| error_kind 值改变影响现有 dashboard SQL 过滤 | 监控规则同步添加 `error_kind='eof_without_done'` 桶（良性，不应触发告警） |
| operator 误把 `eof_without_done` 桶当失败率指标 | 文档说明 + dashboard 标注"benign" |

---

## 7. 未在本次范围

- nginx / systemd 配置（已 OK）
- minimax-m3 备用 credential 路由（需备用 key，暂无）
- 老式 60s 默认值注释的历史追溯修复（保持不动）