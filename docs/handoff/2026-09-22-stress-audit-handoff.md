# 本地 mock 压测审计 Handoff（2026-09-22）

> 被测对象：`tests/stress/gateway` + mock 上游。**不是** `cmd/gateway`，**不是** 154/245。

## 本轮收口

- 对照 `comprehensive-test-plan.md` 审计后，修了空跑：timeout 真 sleep 130s；`/healthz` 不把 timeout/slow/broken_stream 当 L2 失败；failover `maxAttempts=max(3,len(binding))`；s14 用 `mock-stress-pool`；s7 两阶段且要求 alpha 回流；stream 成功必须 `[DONE]`；长 prompt 26000 字符。
- 2026-09-22 02:01–02:03 全量 **16/16 PASS**（`tests/stress/results/report.json`）。关键：s7 alpha=18；s11 72/80 done_rate=0.90；s13 prompt_chars=26000；s14 80/80 全 delta；s16 duration_ms=25306。
- 文档降级：`REPORT.md` / `CAPACITY_HANDOVER.md` / `MEMORY_PROFILE.md`；测试方案 v1.2 增加 §11.7，明确 mock 16/16 不能过 §11 发布门禁。
- **未提交**：未接线优化（ring / minheap / ctxpool / prompt_compress / chunk_buffer）和 `tests/stress/handoff_b_s8s13_test.go`。误入的 `Users/` 已 gitignore。

## 下一轮入口（按优先级）

1. **s11 伪成功**：流式先写 HTTP 200 再截断，committed 后无法 failover。方案 §11.6 未满足。要改测试网关（或生产 executor）在缺 `[DONE]` 时不要让客户端当成 200 成功。
2. **Linux cgroup 容量**：在 154/245 跑 `capacity_matrix.sh`。macOS GOMAXPROCS 数字和 ×0.033 TPM 折算都未验证。
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
