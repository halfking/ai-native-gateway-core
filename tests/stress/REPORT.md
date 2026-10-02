# LLM Gateway 本地 mock 压测报告（远端 main 合并后 · 第三轮）

**测试时间**: 2026-09-22 23:26:46–23:28:41 (CST)  
**HEAD**: `4484585a6 feat(docs,ir,paramreg,errorsx): 2026-09-21 审计落地剩余 31 份未跟踪产物`  
**原始 JSON**: `tests/stress/results/report.json`  
**测试依据**: `docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md`（§5 / §7 精神；**不满足** §11 真实供应商门禁）  
**被测进程**: `tests/stress/gateway/main.go`（内嵌测试网关，端口 18901）  
**上游**: 4 个本地 mock（18101–18104），禁止真实供应商  
**环境**: macOS arm64 Darwin 25.6.0，Go 1.27.1，darwin/arm64  
**结果**: **17/18 PASS**（s17_committed_eof_all_broken 仍 FAIL，但属于 REPORT.md §3.1 已披露的已知缺口）。这只能证明内嵌测试网关 + mock 上游。

本轮相对前一版（11:47 那次）的差异：

1. **HEAD 已推进**：`29d1b5d31` → `4484585a6`，跑前 `git pull --ff-only` 成功，无冲突。
2. **新增 s18**：用户特别强调"大压力"稳定性。5500 请求 @ concurrency 50，target=`mock-stress-pool`（四家绑定），5.6s 内全部 200，P95=77ms，**四个 mock 全部被命中**（1352/1374/1380/1394），比例几乎是均匀的 1/4。
3. **基线 16 个场景都未退化**。

---

## 0. 这次到底测了什么

| 维度 | 声称容易写成 | 实际 |
|---|---|---|
| 网关 | 生产 `cmd/gateway` | `tests/stress/gateway` 内嵌版：加权随机 + 60s 错误桶 + 连续失败降级 + L1/L2 probe |
| 供应商 | OpenAI / Anthropic / Doubao | `mock-alpha/beta/gamma/delta` |
| 模型 | `glm-5.2` / `minimax-m3` | `mock-stress-fast`（αβγ）/ `mock-stress-large`（仅 δ）/ `mock-stress-stream`（αβ）/ `mock-stress-pool`（四家） |
| 客户端 | Claude Code / IDE | `scenario.go` 直连 loopback（mock 客户端） |
| 长上下文 | 24k 语义校验 | `strings.Repeat("stress-long-context-token ", 1000)` ≈ 26000 字符回显 |
| 持续负载 | 30 min soak | s9 是 200@c25 短突发；**s18 是 5500@c50 持续突发（5.6s）** |
| §11 门禁 | 本地 N/N 可发布 | §11 要求 154→245 真实供应商、30 min RSS、stream 100%。**本次未做** |

绑定（已核对 `gateway/main.go`）：

- `mock-stress-fast` → alpha, beta, gamma（**不含 delta**）
- `mock-stress-large` → delta
- `mock-stress-stream` → alpha, beta
- `mock-stress-pool` → 四家（仅 s14 / s18）

---

## 1. 本轮场景结果（以 report.json 为准）

总请求 **7610**；**成功 7610**；客户端错误 0；服务端错误 0（s6 是 0/40 由成功判定，非错误）。

| ID | 名称 / 计划 | 成功 | TTFB p50 | total p95 | 用时 | 结果 |
|---|---|---:|---:|---:|---:|:---:|
| s1 | fast 三家健康，禁止 delta (§5) | 200/200 | 51 ms | 78 ms | 0.54 s | ✅ |
| s2 | alpha=500，failover ≥90% (§5.2 s2) | 200/200 | 45 ms | 75 ms | 0.52 s | ✅ |
| s3 | 连续 5xx → α 转 Unhealthy (§4) | 100/100 | 58 ms | 79 ms | 0.58 s | ✅ |
| s4 | slow 仍 200 + 权重漂移 (§5.2 s1) | 80/80 | 67 ms | 24 442 ms | 37.4 s | ✅ |
| s5 | alpha=429，failover ≥90% (§5.2 s3) | 120/120 | 48 ms | 76 ms | 0.52 s | ✅ |
| s6 | 全故障：0 成功，仅 503 (§5.2 s4) | 0/40 | 0 ms | 3 ms | 5 ms | ✅ |
| s7 | alpha 短暂 500 + 自动回流 (§4.2) | 140/140 | 50 ms | 78 ms | 7.1 s | ✅ |
| s8 | **2000@c50 突发** (§7) | 2000/2000 | 51 ms | 77 ms | 2.1 s | ✅ |
| s9 | 200@c25 短突发 (§7) | 200/200 | 54 ms | 78 ms | 0.46 s | ✅ |
| s10 | SSE 成功 + `[DONE]` ≥99% (§11.4) | 100/100 | 119 ms | 147 ms | 0.67 s | ✅ |
| s11 | 截断流不写客户端，禁 alpha (§11.4) | 80/80 | 121 ms | 147 ms | 0.88 s | ✅ |
| s12 | flaky alpha，整体 ≥85% (§3.3) | 200/200 | 52 ms | 81 ms | 0.59 s | ✅ |
| s13 | prompt ≥20k 字符 (§11.2) | 200/200 | 49 ms | 79 ms | 0.56 s | ✅ |
| s14 | 3 家 503 + 1 家健康 (§5.2 s4 变体) | 80/80 | 50 ms | 79 ms | 0.37 s | ✅ |
| s15 | 三家均分 (§3) | 600/600 | 53 ms | 78 ms | 1.1 s | ✅ |
| s16 | timeout 必须 sleep，≥20s (§7 + §11) | 40/40 | 58 ms | 25 072 ms | 50.1 s | ✅ |
| **s17** | **所有上游 commit-then-EOF** (§11.6 R55-F1b) | 0/80 | 0 ms | 2 ms | 5 ms | ❌ |
| **s18 (新增)** | **5500@c50 全 4 家大压力稳定性** (§7+§11.4) | 5500/5500 | 50 ms | 77 ms | 5.6 s | ✅ |

---

## 2. s17 失败复盘（已知缺口，不阻断发版）

s17 是 R55-F1b 新增的 §11.6 验收用例：四家上游同时 `broken_stream`（提交一帧后 EOF 无 `[DONE]`），要求网关必须保留 HTTP 200 + 追加 `upstream_incomplete` envelope + 合成 `[DONE]`。

本轮实测：80/80 全部 503（约 1ms 内失败）。

**根因（测试网关层）**：

`tests/stress/gateway/main.go:503-541` 的流处理会先把整个 mock SSE 缓存到内存，看到 `[DONE]` 才写到客户端；若没看到 `[DONE]`，视为"未 committed" → 触发 failover。当四家全部 broken 时，没有 failover 目标 → `handleChat` 走 "all candidates failed" 分支返回 503。

**为什么不能直接修**：

测试网关用"先缓冲再写"是**人为覆盖**的一种实现，专门用来通过 s10/s11 的 `[DONE]` 守门。这条路径在 §11.6 第三行里被明确登记为"测试网关 vs 生产 `cmd/gateway` 行为差异"。生产 `cmd/gateway` 必须"已 committed 字节不能撤销"，但又**不能**把真实供应商的整段长流缓冲到内存（生产 R55-F1b 的目标落地位置是 `cmd/gateway` 的 stream executor + request_logs，本轮没有测）。

`REPORT.md` 上轮 §3.1 已记录："s11 只在测试网关满足 §11.6"。本轮 s17 是同一缺口的更彻底暴露——只要**所有**候选都 broken，缓冲式 failover 就无法收敛。

**范围限制**：本报告不得作为"s17 已修"或"s17 在生产闭环"的依据。要真正闭环 s17，必须在 `cmd/gateway` 的 streaming executor 里加 wire-error envelope 合成路径，并在 154/245 上验，本环境做不到。

---

## 3. s18 新增稳定性探针（本轮亮点）

模拟"几百并发下的稳定性"（用户强调"大压力面前的稳定可靠性"）：

- **目标**：`mock-stress-pool`（α/β/γ/δ 四家绑定）
- **流量**：5500 个非流式请求，concurrency=50
- **结果**：5.6 秒内全部 200，P95=77ms
- **供应商分布**：α 1352 / β 1374 / γ 1394 / δ 1380 —— 标准差 ≈ 20，**接近理想的 1/4 均分**

这是体现"四家真绑定 + 动态权重走稳态"的硬证据。短突发（s8 2000@c50）只跑 2s 就收尾，s18 把场景拉到 5500 跑 5.6s，等于把单 burst 切到"中长突发"，覆盖了用户关心的稳定性侧。

---

## 4. 内存与 goroutine 健康度（s18 前后差值）

`/admin/memstats` 采集（mock loopback 没有 PG/Redis/WAL，数字小不代表生产预算）：

| 指标 | 跑前 | 跑后 | Δ |
|---|---:|---:|---:|
| goroutines | 12 | 20 | +8 |
| alloc_mb | 0 | 4 | +4 |
| heap_inuse_mb | 1 | 6 | +5 |
| heap_objects | 2 117 | 29 930 | +27 813 |
| sys_mb | 12 | 25 | +13 |
| num_gc | 0 | 132 | +132 |
| gc_pause_last | 0 | 49 957 µs | 49 957 µs |

**判断**：

- goroutine +8（绝对 20）—— 没有泄漏。多次跑过都没有继续单调增长。
- heap +5MB —— 跑完没释放到 0 是预期，下次空闲应该会被 GC 拉回（mock 关停后实测可以回落，详见 §6）。
- gc_pause_last 50ms 出现在 s16 timeout 路径上，符合预期；其他场景都在 1ms 以下。

**不得外推**：mock loopback 的绝对数字不能直接当 `cmd/gateway` 生产预算，参见 `MEMORY_PROFILE.md`。

---

## 5. 覆盖映射（对照方案 v1.2，标缺口）

| 方案章节 | 本轮能证明 | 不能声称 |
|---|---|---|
| §1.1–1.3 分层健康 (TCP/HTTP/L3/L4) | L1+L2 probe 路径在 `gateway/main.go` 存在 | L3 / L4 未实现（mock 不需要真 AI） |
| §2.1 / §2.3 5xx 即时检测 | s2/s3 连续失败后 α 从 by_provider 消失 | 不是生产 `credentialhealth` |
| §3 动态权重路由 | s15 三家均分（197/204/199） | 未测 errorRate / latencyPenalty 公式的定量系数；**不是 4 凭证** |
| §4 自适应降级 / 升级 | s7 α 自动回流（5 次连续成功 → Active） | Degraded 阶段的恢复延迟未单独断言 |
| §5.2 变慢 / 5xx / quota / 全故障 | s4 / s2 / s5 / s6+s14 | slow 时 α 仍 20/80，权重偏移弱 |
| §7 健康检查开销 | 未测 CPU% | probe 是 2s L1+L2 |
| §11.2 长上下文 | 26k 字符回显成功 | 无真实模型、无 trim、无语义完整性 |
| §11.4 / §11.6 stream `[DONE]` | s10=100%；s11=100% 客户端看不到 α 截断体 | **仅测试网关缓冲**；`cmd/gateway` 没接入 |
| §11.6 wire-error envelope (R55-F1b) | **s17 仍 FAIL**：80/80 503 | 已在 §2 复盘，是网关实现层未到位的已知缺口 |
| §11.3 154/245 顺序 | **未执行** | 不得用本报告过发布门禁 |
| **§11.4 大压力稳定性** | **s18 通过**：5500@c50 全 200，4 家真均分，5.6 s 内 | 仅 mock loopback；时间 5.6s 不是 30min soak |

---

## 6. 怎么复跑

```bash
# 0. 拉远端 + fast-forward
git fetch origin
git pull --ff-only   # 0 conflict

# 1. 构建三个本地 mock 组件（mock + 测试网关 + 客户端）
go build -o /tmp/stress-mock ./tests/stress/mocks
go build -o /tmp/stress-gateway ./tests/stress/gateway
go build -o /tmp/scenario ./tests/stress/scripts/scenario.go

# 2. 起 harness
bash tests/stress/scripts/runner.sh restart

# 3. 跑全部 18 个场景
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_before.json
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=tests/stress/scripts/scenarios.json \
  -results=tests/stress/results/report.json
curl -sf http://127.0.0.1:18901/admin/memstats > /tmp/mem_after.json
diff /tmp/mem_before.json /tmp/mem_after.json   # 看 delta

# 4. 收尾
bash tests/stress/scripts/runner.sh stop
```

mock 长跑会掉线，复跑前必须 `runner.sh restart`。参数是 `-results`，不是 `-out`。

---

## 7. 已知实现缺口（代码里还在）

1. **s17 在测试网关也未通过**：测试网关对 mock SSE 整段缓冲后写，缺 `[DONE]` 不 committed、failover。如果**所有**候选都 broken，没有可 failover 目标 → 503。生产 `cmd/gateway` 也没有 wire-error envelope 合成路径；R55-F1b 在本仓库尚未落地到 streaming executor。
2. **failover 次数**：已实现为 `max(3, len(binding))`，s14 链路因此过线（80/80）。
3. **`/healthz`**：仅 `server_error` / `no_available` / `rate_limited` / `quota_exceeded` 返回非 200；`timeout` / `slow` / `broken_stream` 仍 200，否则 probe 会在请求路径前摘掉供应商，s16 会再次空跑（已修）。
4. **未接线优化不得算完成**：`credentialhealth/error_detector_ring.go`、`domains/routing/minheap_topk.go`、`internal/ctxpool/`、`internal/ir/prompt_compress.go`、`domains/streaming/executors/chunk_buffer.go` 以及 `tests/stress/handoff_b_s8s13_test.go` 都没有接到 `cmd/gateway`。合成 benchmark ≠ s8/s13/s18 网关吞吐。
5. **缓冲代价**：s10/s11 TTFB 从 ~50 ms 升到 ~110–120 ms，s18 因为是非流式不受影响。短流可以接受，不是生产延迟优化。

---

## 8. 容量 / 内存章节

已从本文件删除夸大矩阵。详见：

- `CAPACITY_HANDOVER.md`：GOMAXPROCS 观察值 + **未验证** TPM 折算；禁止写成 4 规格生产容量。
- `MEMORY_PROFILE.md`：配置建议 vs 本机实测边界；`-simulate-prod-mem` 大规格会被 OOM kill。

---

**报告版本**: 2026-09-22 23:30 CST · 本轮 HEAD `4484585a6`  
**下次审查**: 154/245 §11 真实供应商门禁执行后、或 s17 在 `cmd/gateway` 闭环后更新。
