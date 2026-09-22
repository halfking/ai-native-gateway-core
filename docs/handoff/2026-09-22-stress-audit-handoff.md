# 本地 mock 压测审计 Handoff（2026-09-22 第二轮）

> 被测对象：`tests/stress/gateway` + mock 上游。**不是** `cmd/gateway`，**不是** 154/245。

## 本轮收口

- 上一轮（02:01，commit `a78fdfb27`）修了 timeout 空跑 / s7 alpha 回流 / 26k prompt / s14 pool，但 **s11 仍先写 HTTP 200 再截断**：80 条全是 200，8 条缺 `[DONE]`，靠客户端 `done_rate` 抓漏。方案 §11.6 未满足。
- 本轮把测试网关 SSE 改成 **缓冲到 `[DONE]`/EOF 再写客户端**。缺 `[DONE]` 不 committed，可 failover。s11 断言改为 `require_done` + 禁止 `mock-alpha` + 要求 `mock-beta`。
- 2026-09-22 11:47:59–11:49:18 全量 **16/16 PASS**（`tests/stress/results/report.json`）。关键：s7 alpha=15；s11 80/80 全 beta、done_rate=1；s13 prompt_chars=26000；s14 80/80 全 delta；s16 duration_ms=25390。总请求 4380 / 成功 4340 / 服务端错误 40（s6）。
- 文档：测试方案头部改为 v1.2；§11.7 写明缓冲只在测试网关；`REPORT.md` 按 11:47 数字重写。
- **未提交**：未接线优化（ring / minheap / ctxpool / prompt_compress / chunk_buffer）和 `tests/stress/handoff_b_s8s13_test.go`。

## 明确没有完成的事

- 生产 `cmd/gateway` **没有**这套 SSE 整包缓冲。真实供应商长流也不能整包缓冲后再写。本次 s11 PASS **不能**当成生产伪成功已修。
- 154/245、真实模型、30 min RSS、cgroup 容量、TPM 折算：都没做。
- 测试网关没有 L3/L4。文件头旧注释写 HTTP HEAD / L3 已删，避免再骗人。

## 下一轮入口（按优先级）

1. **生产流式伪成功**：在 `cmd/gateway` executor 上处理「HTTP 200 SSE 无 `[DONE]`」。不要把测试网关缓冲抄进生产热路径。
2. **Linux cgroup 容量**：在 154/245 跑 `capacity_matrix.sh`。macOS GOMAXPROCS 和 ×0.033 TPM 折算都未验证。
3. **§11.3 真实供应商门禁**：154 低并发 → 245 低并发 → 30 min → 故障注入。本地 mock 不算。
4. Handoff-B 七项优化：先接线到真实请求路径，再谈 s8/s13 吞吐。合成 in-process 测试不算。

## 复跑

```bash
go build -o /tmp/stress-mock ./tests/stress/mocks
go build -o /tmp/stress-gateway ./tests/stress/gateway
go build -o /tmp/scenario ./tests/stress/scripts/scenario.go
bash tests/stress/scripts/runner.sh restart
/tmp/scenario -gateway=http://127.0.0.1:18901 \
  -scenarios=tests/stress/scripts/scenarios.json \
  -results=tests/stress/results/report.json
```

约束：只用 mock；不改 `cmd/gateway`；不改 vendor/go.sum；`Proxy: nil`；`reset` tag；`mock-stress-large` 只绑 delta。
