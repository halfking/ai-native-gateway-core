# 容量评估交接（降级版）

> 生成时间：2026-09-22 02:10 CST  
> 只交接**已跑通的 mock 内嵌网关**和**未验证的折算公式**。  
> 上一版把 2c4G/4c8G 写成「真实峰值 TPM 147K/294K」，并把 Handoff-B 七项优化写成可落地目标。那些数字和优化**都没有接到生产 `cmd/gateway`**。

## 1. 能写进交接的事实

- 被测进程是 `tests/stress/gateway`，不是 `cmd/gateway`。
- 上游是 20–80 ms mock，不是 1.5 s 真实 LLM。
- 本机 macOS 没有 cgroup / taskset。`capacity_matrix.sh` 只用 `GOMAXPROCS` 限制 P 数量；`ulimit -v` 在 Darwin 上 `Invalid argument`。
- 2026-09-21 的 `results/capacity/matrix.json` 是**收紧断言之前**的 GOMAXPROCS 观察值。s13 当时没有 `min_prompt_chars=20000`，s9 名称还写成 30s soak。**不得当作本轮 16 场景的容量结论**。
- 本机 host 内存紧张时，`-simulate-prod-mem` 在 4c8G/4c16G 会被 OOM kill。没有「4 规格全验证完成」。

## 2. GOMAXPROCS 观察值（旧 matrix.json，仅作吞吐参考）

这些 rps 是 mock 50 ms 突发，不是生产吞吐：

| 规格（仅 GOMAXPROCS） | s8 rps | s9 rps | s13 rps | s15 rps | 备注 |
|---|---:|---:|---:|---:|---|
| 2c4G | 778.8 | 212.1 | 225.7 | 428.6 | s8 peak_rss_mb=0.0（采集失败） |
| 2c8G | 824.7 | 222.0 | 212.8 | 413.2 | s9 P95=139 ms |
| 4c8G | 673.9 | 260.1 | 220.0 | 403.8 | s8 P95=160 ms，rps 反而低于 2c |
| 4c16G | 831.9 | 241.3 | 84.9 | 428.6 | s13 rps 异常低（wall 2356 ms），未复现根因 |

本轮全量（无 GOMAXPROCS 限制，2026-09-22 11:47）s8 = 2000/2.057s ≈ **973 req/s**。这仍然是 mock loopback。

## 3. TPM 折算 — 未验证，禁止当实测

上一版混用了两个系数：

- `CAPACITY_HANDOVER.md`：mock 50 ms → 真实 1.5 s → **×0.028**
- `REPORT.md` §7.5：同一假设写成 **×0.033**

两者都是「假设真实请求 1.5s、每请求 180 tokens」的纸面乘法，**没有在 154/245 对过账**。本文件不再给出「2c4G = 147K TPM」这种表。

若下一轮要估算，公式必须单列、带假设、带「未验证」标签：

```
假设（未验证）：
  mock_latency ≈ 50 ms
  real_llm_latency ≈ 1500 ms
  scale = 50/1500 = 1/30 ≈ 0.033     # 不要再写 0.028
  tokens_per_req = 180               # 未验证
  estimated_tpm = mock_peak_rps * scale * tokens_per_req * 60
```

在 154/245 用真实模型跑出 peak_rps 之前，这只是草稿纸。

## 4. 内存预算 — 配置建议，不是本机实测

PG 池 5 MB/conn、File cache 10 GB、会话 256 B 等数字来自生产配置推算，**不是**本次压测 RSS。本次 gateway RSS ≈ 25 MB（无 PG/Redis/WAL）。详见 `MEMORY_PROFILE.md`。

4c8G 作为生产甜点规格：**保留为配置建议**，本机没有在 cgroup 8G 上限下跑通。

## 5. 优化优先级（状态要诚实）

| 项 | 仓库里有什么 | 状态 |
|---|---|---|
| KindThreshold 分层 | 生产 `credentialhealth` 有；内嵌网关仅部分 429/5xx 窗口 | 内嵌压测未启用生产实现 |
| MinHeapTopK | `domains/routing/minheap_topk.go` 未跟踪/未接线 | **未完成** |
| ColdNodeActiveProber | 内嵌网关有 2s L1+L2 probe；生产 ColdProbe 未接线到本 harness | 不能写「冷节点 5xx ↓70%」 |
| ctx pool | `internal/ctxpool/` 未跟踪/未接线 | **未完成** |
| ring error counter | `credentialhealth/error_detector_ring.go` 未跟踪/未接线 | **未完成** |
| 8KB chunk buffer | `domains/streaming/executors/chunk_buffer.go` 未跟踪/未接线 | **未完成** |
| IR prompt 压缩 | `internal/ir/prompt_compress.go` 未跟踪/未接线 | **未完成** |

`tests/stress/handoff_b_s8s13_test.go` 是合成 in-process 热路径，**依赖上述未接线包**。不要提交，也不要用它宣称 s8≥900 / s13≥500。

## 6. 下一轮真正该做的事

### Handoff-A：Linux cgroup 容量（尚未做）

在 154/245 用 `cgexec`/`taskset` 跑 `capacity_matrix.sh`。macOS GOMAXPROCS 数字不得写入生产容量表。约束：`scenario.go` tag=`reset`；`mock-stress-large` 只绑 delta；网关 `Proxy: nil`；供应商必须是 mock。

### Handoff-B：优化（尚未做）

先接线到 `cmd/gateway` 或明确「仅测试网关」，再单测 + 真实 harness 场景。禁止用未 import 的文件冒充完成。

### Handoff-C：§11.3 发布门禁（尚未做）

本地 mock 16/16 **不能**代替：154 低并发 → 245 低并发 → 245 30 min → 245 故障注入。真实供应商、真实凭据、SSOT `env-injector`。
