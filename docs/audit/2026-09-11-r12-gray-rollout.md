# R12 Raw Sink 阶段4灰度记录

- 日期：2026-09-11
- 仓库：`Z:\workspace\ai-native-tools\syncfield\llm-gateway-go-4`
- 目标：`LLM_GATEWAY_RAW_LOG_SINK=buffered` 的生产灰度与可回滚验证
- 状态：**代码/观测与回滚门禁已补齐；真实环境三阶段灰度未执行（BLOCKED）**

## 1. 完成判据与证据索引

阶段4要求：测试环境 → 低流量实例 → 全量；每阶段观察 `rawaudit_write_failed_total`、flush 耗时、实际 batch 大小、dropped、close drain 耗时/超时、frame correlation 成功率；一个完整业务周期无新增写失败且 P99 无回归；完成 buffered → legacy 回滚并验证 JSONL 连续可读。

本次代码证据：

| 项目 | 证据 |
|---|---|
| Linux race CI | `.github/workflows/sessionforensics-ci.yml` 的 `raw-sink-race` job，直接执行 `go test -race -count=1 -timeout=600s ./internal/logging/... ./domains/streaming/...` |
| 六项运行时指标 | `metrics/interface.go`、`metrics/prometheus.go`；Async/Buffered flush、drop、Close、LookupFrame 路径接线 |
| raw write/rotate/sync failure | `internal/logging/raw_data_logger.go` 现有/同步批次 failure 计数路径；告警 `deploy/prometheus/alerts/shadow-write-failures.yaml` |
| 规则加载 | `deploy/prometheus/prometheus.yml` 显式加载 `/etc/prometheus/alerts/*.yaml` 与 `*.yml`；Compose 挂载 `./alerts` |
| Grafana | `deploy/prometheus/grafana/r12-raw-sink-rollout.json`，已由 Compose 挂载到 provisioning 目录 |
| 回滚脚本 | `scripts/r12-raw-sink-rollback.sh`，要求调用方显式提供 env、log、restart，可选 health 命令 |

指标查询（按实例/环境附加筛选）：

```promql
increase(llm_gateway_rawaudit_write_failed_total[5m])
histogram_quantile(0.99, sum by (le, sink) (rate(llm_gateway_raw_sink_flush_duration_seconds_bucket[15m])))
histogram_quantile(0.95, sum by (le, sink) (rate(llm_gateway_raw_sink_flush_batch_size_bucket[15m])))
increase(llm_gateway_raw_sink_dropped_total[5m])
histogram_quantile(0.99, sum by (le, sink) (rate(llm_gateway_raw_sink_close_drain_duration_seconds_bucket[1h])))
increase(llm_gateway_raw_sink_close_drain_timeout_total[15m])
sum by (sink) (rate(llm_gateway_raw_sink_frame_lookup_total{result="hit"}[15m])) / clamp_min(sum by (sink) (rate(llm_gateway_raw_sink_frame_lookup_total[15m])), 1e-9)
```

## 2. CI 与本机验证

- 本机为 Windows/ARM64，不能运行 Go race detector；Linux 实跑由上述 GitHub Actions job 承接。
- 本机完成：`CGO_ENABLED=0 go test -mod=vendor ./metrics ./internal/logging -run '^TestPrometheusRecorder_R12RawSinkMetrics$|^$'` 通过。
- 本机完成：受影响 streaming 包使用仓库配方的 `CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc.exe go test ... -run '^$'` 编译通过。
- 全仓 Windows 编译仍有已知、与 R12 无关的 `internal/fsstore` Unix-only `syscall.Flock/LOCK_EX/LOCK_UN` 限制；不作为 R12 回归。

## 3. 灰度步骤（真实环境）

### 3.1 测试环境 — BLOCKED / NOT EXECUTED

- 预期动作：设置 `LLM_GATEWAY_RAW_LOG_SINK=buffered`，重启测试实例，运行一个完整业务周期；保存发布版本、实例、重启时间和 Prometheus 查询结果。
- 阻塞原因：当前工作区没有测试环境地址、实例清单、部署凭据或 operator sign-off；仓库内脚本不会猜测目标或主动重启生产服务。
- 结论：无真实指标证据，不判定通过。

### 3.2 低流量实例 — BLOCKED / NOT EXECUTED

- 预期门禁：测试环境通过后，仅低流量实例启用 buffered；观察上述六项，比较 legacy 基线 P50/P95/P99；任何新增 write failure、dropped、close timeout 或持续 P99 回归立即回滚。
- 阻塞原因：无已授权低流量实例/流量切分入口与观测端点。
- 结论：未执行，不虚构灰度结果。

### 3.3 全量 — BLOCKED / NOT EXECUTED

- 预期门禁：低流量实例至少完成一个完整业务周期且无新增 write failure、无 dropped/close timeout，P99 不劣于批准的 legacy 基线；然后分批扩大至 100%。
- 阻塞原因：没有全量发布授权、生产实例清单或 operator sign-off。
- 结论：未执行，阶段4生产验收仍未完成。

## 4. 回滚演练 — PASS（本地 fixture）

执行命令（临时目录，不触碰真实服务）：

```bash
root=$(mktemp -d)
mkdir -p "$root/logs"
printf 'LLM_GATEWAY_RAW_LOG_SINK=buffered\n' > "$root/env"
printf '%s\n' '{"request_id":"before"}' > "$root/logs/raw_data_before.jsonl"
printf '%s\n' '{"request_id":"after"}' > "$root/logs/raw_data_after.jsonl"
bash scripts/r12-raw-sink-rollback.sh \
  --env-file "$root/env" --log-dir "$root/logs" \
  --restart-cmd true --health-cmd true
```

观测输出：

```text
R12 rollback: current=buffered target=legacy dry_run=false
set LLM_GATEWAY_RAW_LOG_SINK=legacy
JSONL continuity PASS files=2 rows=2
R12 rollback rehearsal PASS: legacy restart and JSONL continuity verified
LLM_GATEWAY_RAW_LOG_SINK=legacy
```

演练验证了：env 从 buffered 改回 legacy、显式 restart/health 命令被执行、跨文件逐行 JSON 对象可解析、成功后 env 保持 legacy。生产回滚仍须由 operator 使用真实 env/service/health 参数执行并保存输出。

## 5. 当前结论与签核

- 代码、CI race 门禁、六项指标、告警接线、Grafana 面板和可复现回滚脚本已提交为 R12 阶段4候选变更。
- **生产灰度不能在缺少目标与权限时宣称完成。** 测试环境、低流量、全量三项均为 `BLOCKED/NOT EXECUTED`，因此“完整业务周期无新增写失败、P99 无回归”的生产验收尚无证据。
- Operator sign-off：`PENDING — 需要提供已授权环境目标和 Prometheus 证据`。
