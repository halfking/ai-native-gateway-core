# LLM Gateway 本地 mock 压测报告（审计后）

**测试时间**: 2026-09-22 11:47:59–11:49:18 (CST)  
**原始 JSON**: `tests/stress/results/report.json`  
**测试依据**: `docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md`（§5 / §7 精神；**不满足** §11 真实供应商门禁）  
**被测进程**: `tests/stress/gateway/main.go`（内嵌测试网关，端口 18901）  
**上游**: 4 个本地 mock（18101–18104），禁止真实供应商  
**环境**: macOS arm64 Darwin 25.6.0，Go 1.27.1  
**结果**: 收紧 s11 后再跑 **16/16 PASS**。这只证明内嵌测试网关 + mock 上游，**不是**生产 `cmd/gateway`，也不是 154/245 实测。

上一版（02:01 那次）已经修了 timeout 空跑 / s7 alpha 回流 / 26k prompt / s14 pool，但 **s11 仍向客户端写 HTTP 200 再截断**（80 条全是 200，8 条缺 `[DONE]`，靠客户端 `done_rate` 抓漏）。本文件按 11:47 复跑重写：测试网关改为缓冲 mock SSE，缺 `[DONE]` 不 committed、failover 到 beta。**生产 executor 没有这套缓冲。**

---

## 0. 这次到底测了什么

| 维度 | 声称容易写成 | 实际 |
|---|---|---|
| 网关 | 生产 `cmd/gateway` | `tests/stress/gateway` 内嵌版：加权随机 + 60s 错误桶 + 连续失败降级 + L1/L2 probe |
| 供应商 | OpenAI / Anthropic / Doubao | `mock-alpha/beta/gamma/delta` |
| 模型 | `glm-5.2` / `minimax-m3` | `mock-stress-fast`（αβγ）/ `mock-stress-large`（仅 δ）/ `mock-stress-stream`（αβ）/ `mock-stress-pool`（四家，s14） |
| 客户端 | Claude Code / IDE | `scenario.go` 直连 loopback |
| 长上下文 | 24k 语义校验 | `strings.Repeat("stress-long-context-token ", 1000)` = **26000 字符回显**，无 token 计数、无截断语义 |
| 持续负载 | 30s soak | s9 是 200@c25 短突发，wall ≈ 0.46s |
| 容量 / TPM | 4 规格生产容量 | GOMAXPROCS 模拟 + 未验证折算，见 `CAPACITY_HANDOVER.md` |
| §11 门禁 | 本地 16/16 可发布 | §11 要求 154→245 真实供应商、30 min RSS、stream 100%。**本次未做** |

绑定（已核对 `gateway/main.go`）：

- `mock-stress-fast` → alpha, beta, gamma（**不含 delta**）
- `mock-stress-large` → delta
- `mock-stress-stream` → alpha, beta
- `mock-stress-pool` → 四家（仅 s14）

网关 HTTP 客户端 `Transport: &http.Transport{Proxy: nil}`。`scenarios.json` 的 Reset 字段 JSON tag 是 `reset`。

---

## 1. 本轮场景结果（以 report.json 为准）

总请求 4380；成功 4340；客户端错误 0；服务端错误 40（全部来自 s6 的 503）。健康场景 P95 ≈ mock 20–80 ms（流式缓冲后 s10/s11 TTFB ≈110–120 ms），不是真实 LLM 延迟。

| ID | 断言要点 | 成功 | 成功率 | 状态码 | 供应商分布 | P50 TTFB | P95 total | 用时 | 结果 |
|---|---|---:|---:|---|---|---:|---:|---:|:---:|
| s1 | fast 三家健康，禁止 delta | 200/200 | 100% | 200×200 | α71 β70 γ59 | 54 ms | 79 ms | 0.59 s | PASS |
| s2 | alpha=500，≥90% failover | 200/200 | 100% | 200×200 | β102 γ98 | 50 ms | 79 ms | 0.57 s | PASS |
| s3 | 连续 5xx 后不再打 alpha | 100/100 | 100% | 200×100 | β48 γ52 | 51 ms | 79 ms | 0.53 s | PASS |
| s4 | slow 仍 200，成功率 ≥80% | 80/80 | 100% | 200×80 | α21 β31 γ28 | 66 ms | 20641 ms | 32.8 s | PASS |
| s5 | alpha=429，≥90% failover | 120/120 | 100% | 200×120 | β55 γ65 | 54 ms | 78 ms | 0.54 s | PASS |
| s6 | 全故障：成功率必须 0，只要 503 | 0/40 | 0% | 503×40 | — | 0 ms | 1 ms | 2 ms | PASS |
| s7 | 两阶段；phase2 必须见到 alpha | 140/140 | 100% | 200×140 | α15 β52 γ73 | 48 ms | 79 ms | 7.08 s | PASS |
| s8 | 2000@c50，mock 突发 | 2000/2000 | 100% | 200×2000 | α624 β684 γ692 | 49 ms | 77 ms | 2.06 s | PASS |
| s9 | 200@c25 短突发，**不是 30s soak** | 200/200 | 100% | 200×200 | α62 β62 γ76 | 54 ms | 78 ms | 0.48 s | PASS |
| s10 | SSE 成功且 `[DONE]` ≥99% | 100/100 | 100% | 200×100 | α53 β47 | 112 ms | 144 ms | 0.66 s | PASS |
| s11 | 截断流不得到达客户端；done_rate=1；禁止 alpha | 80/80 | 100% | 200×80 | β80 | 122 ms | 143 ms | 0.86 s | PASS |
| s12 | flaky alpha，整体 ≥85% | 200/200 | 100% | 200×200 | α10 β101 γ89 | 56 ms | 80 ms | 0.59 s | PASS |
| s13 | prompt ≥20k 字符 | 200/200 | 100% | 200×200 | δ200 | 51 ms | 77 ms | 0.55 s | PASS |
| s14 | `mock-stress-pool`；200 体只能是 delta | 80/80 | 100% | 200×80 | δ80 | 48 ms | 76 ms | 0.38 s | PASS |
| s15 | 三家均出现，禁止 delta | 600/600 | 100% | 200×600 | α203 β193 γ204 | 51 ms | 78 ms | 1.06 s | PASS |
| s16 | timeout 必须 sleep；duration ≥20s | 40/40 | 100% | 200×40 | β16 γ24 | 52 ms | 25075 ms | 25.4 s | PASS |

关键行为（相对上一轮 02:01 报告）：

- **s7**：alpha=15。probe 把 Unhealthy 升到 Degraded 后，请求路径重新打到 alpha。
- **s11**：80/80 HTTP 200 **全部来自 beta**，`done_rate=1`，`by_provider` 禁止 alpha。测试网关缓冲 mock SSE，缺 `[DONE]` 不写客户端、failover。02:01 那次仍是 200×80 + 8 条截断体。
- **s13**：`prompt_chars=26000`。
- **s14**：80/80 全在 delta。
- **s16**：25.4s，P95=25075ms，by_provider 只有 β/γ。

跑完后 `/admin/memstats`：alloc_mb=1，goroutines=18，sys_mb=28。`ps` RSS：gateway ≈25.5 MB，每个 mock ≈18–20 MB。这是 loopback mock、无 PG/Redis/WAL 的数字。

---

## 2. 覆盖映射（对照方案，标缺口）

| 方案章节 | 本次能证明 | 不能声称 |
|---|---|---|
| §2.1 5xx 检测 | s2/s3：连续失败后 alpha 从 by_provider 消失 | 不是生产 `credentialhealth` |
| §2.3 / §4.2 恢复 | s7：alpha 回流 15 次 | 升级阈值（5 次连续成功）未单独断言 |
| §3 动态权重 | s15：三家均分（203/193/204） | 未测错误率/延迟惩罚公式的定量系数；**不是 4 凭证** |
| §5.2 变慢 / 5xx / quota / 全故障 | s4 / s2 / s5 / s6+s14 | s4 的 slow 仍返回 200，权重偏移弱（α 仍 21/80） |
| §7 健康检查开销 | 未测 CPU%；probe 2s L1+L2 | L3/L4 不存在 |
| §11.2 长上下文 | 26k 字符回显成功 | 无真实模型、无 trim、无语义完整性 |
| §11.4 / §11.6 stream `[DONE]` | s10=100%；s11=100% 且客户端看不到 alpha 截断体 | **仅测试网关缓冲实现**；生产 `cmd/gateway` 仍可能先写 200 |
| §11.3 154/245 顺序 | 未执行 | 不得用本报告过发布门禁 |

---

## 3. 已知实现缺口（代码里还在）

1. **s11 只在测试网关满足 §11.6**：`tests/stress/gateway` 对 mock SSE 先缓冲到 `[DONE]`/EOF，缺则 failover，客户端看不到截断 200。**生产 `cmd/gateway` 没有这套缓冲**，真实供应商长流也不能整包缓冲。不得把本次 PASS 写成生产已修伪成功。
2. **failover 次数**：已改为 `max(3, len(binding))`。固定 3 次时 s14 曾 79/80（盖不住第 4 个健康凭证）。
3. **`/healthz`**：仅 `server_error` / `no_available` / `rate_limited` / `quota_exceeded` 返回非 200。`timeout` / `slow` / `broken_stream` 仍 200，否则 probe 会在请求路径前摘掉供应商，s16 再次空跑。
4. **分层 4xx/5xx**：内嵌网关有 429/5xx 窗口，但不是生产 `KindThreshold`。
5. **未接线优化不得算完成**：`credentialhealth/error_detector_ring.go`、`domains/routing/minheap_topk.go`、`internal/ctxpool/`、`internal/ir/prompt_compress.go`、`domains/streaming/executors/chunk_buffer.go` 以及 `tests/stress/handoff_b_s8s13_test.go` **都没有接到 `cmd/gateway`**。合成 benchmark ≠ s8/s13 网关吞吐。
6. **缓冲代价**：s10/s11 TTFB 从 ~50 ms 升到 ~110–120 ms，因为测试网关等完整 SSE 才写客户端。这是 mock 短流可接受的测试手段，不是生产延迟优化。

---

## 4. 怎么复跑

```bash
go build -o /tmp/stress-mock ./tests/stress/mocks
go build -o /tmp/stress-gateway ./tests/stress/gateway
go build -o /tmp/scenario ./tests/stress/scripts/scenario.go
bash tests/stress/scripts/runner.sh restart
/tmp/scenario \
  -gateway=http://127.0.0.1:18901 \
  -scenarios=tests/stress/scripts/scenarios.json \
  -results=tests/stress/results/report.json
```

mock 长跑会掉线，复跑前必须 `runner.sh restart`。参数是 `-results`，不是 `-out`。

---

## 5. 容量 / 内存章节

已从本文件删除夸大矩阵。见：

- `CAPACITY_HANDOVER.md`：GOMAXPROCS 观察值 + **未验证** TPM 折算；禁止写成 4 规格生产容量。
- `MEMORY_PROFILE.md`：配置建议 vs 本机实测边界；`-simulate-prod-mem` 大规格会被 OOM kill。
