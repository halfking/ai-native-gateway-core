# URSM v2 切换操作手册

> 状态：P0-Z1 可执行基线。当前只批准 `off -> shadow`，不得在本次变更中切 `authoritative`。
> 最后更新：2026-08-16

## 1. 模式语义

| 模式 | 生产路由权威 | v2 outcome 写入 | v2 路由计算 |
|---|---|---|---|
| `off` | legacy `credentialstate.Manager` | 否 | 否 |
| `shadow` | legacy `credentialstate.Manager` | `URSM_V2_SHADOW_DOUBLE_WRITE=1` 时 100% 双写 | 按 `URSM_V2_SHADOW_SAMPLE_RATE` 采样，只比较不采用 |
| `canary` | 未命中走 legacy，命中请求采用 v2 | 命中请求写 v2 | `URSM_V2_CANARY_PERCENT` 控制 |
| `authoritative` | URSM v2 | 是 | 全量；legacy manager 不装配 |

`off` 仍会在 Redis 可用时构造一个 no-op v2 Manager。`shadow` 必须保留 legacy manager 的读写路径，不能删除旧装配。

## 2. 有效配置

```text
URSM_V2_MODE=off|shadow|canary|authoritative
URSM_V2_SHADOW_DOUBLE_WRITE=0|1
URSM_V2_SHADOW_SAMPLE_RATE=0..1        # 默认 0.01，只影响 diff 双算
URSM_V2_CANARY_PERCENT=0..100          # 按 tenant|model|requestID 稳定散列
LLM_GATEWAY_REDIS_ADDR=<host:port>
```

当前没有 `URSM_V2_CANARY_TENANTS` 或 `URSM_V2_CANARY_MODELS` 的环境变量接线。灰度必须使用 percent。

## 3. Shadow 前置门禁

进入 shadow 前必须同时满足：

1. 已验证 gateway Redis 连通，且不是与生产不一致的临时 Redis。
2. Prometheus 已抓取需要 admin Bearer token 的 `/metrics`，retention 至少 8 天。
3. `go build ./...` 与 `go test -race ./domains/ursm/...` 通过。
4. staging 已用真实候选验证 v2 tenant-aware key：`ursm:v2:node:<tenant>:<cid>:<raw>`。
5. 已记录切换前 24 小时请求错误率、无候选率和 P99 延迟基线。

指标查询示例：

```bash
curl -fsS -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  http://127.0.0.1:8781/metrics \
  | grep -E 'ursm_shadow_diff_total|llm_gateway_ursm_v2_shadow_records_total'
```

`ursm_shadow_diff_total` 是进程 counter，进程重启会归零。7 天验收必须使用 Prometheus 的 `increase(...[7d])`，不能读取单实例当前值代替。

## 4. Stage 0：保持 Off

配置：

```bash
URSM_V2_MODE=off
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
```

验收：

- 启动日志包含 `ursm.v2 manager constructed mode=off`。
- legacy credential state manager 正常创建。
- `routing_state_source_total{source="off"}` 随请求增长。
- v2 `recorded` 不增长。

## 5. Stage 1：Off -> Shadow

### 5.1 Systemd 切换

持久修改 `/etc/llm-gateway-go/env`：

```bash
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=1
URSM_V2_SHADOW_SAMPLE_RATE=0.01
```

然后执行：

```bash
sudo systemctl restart llm-gateway-go.service
sudo systemctl is-active llm-gateway-go.service
sudo journalctl -u llm-gateway-go.service -n 200 --no-pager \
  | grep -E 'ursm.v2 manager constructed|credential state manager created'
```

启动日志必须显示：

```text
mode=shadow ready=true shadow_double_write=true shadow_sample_rate=0.01
credential state manager created
```

### 5.2 Kubernetes 切换

```bash
kubectl -n <namespace> set env deployment/<deployment> \
  URSM_V2_MODE=shadow \
  URSM_V2_SHADOW_DOUBLE_WRITE=1 \
  URSM_V2_SHADOW_SAMPLE_RATE=0.01 \
  URSM_V2_CANARY_PERCENT-
kubectl -n <namespace> rollout status deployment/<deployment> --timeout=5m
```

### 5.3 Shadow 即时验收

切换后 15 分钟内：

- `/healthz` 返回 200，业务错误率和 P99 不劣于基线。
- legacy manager 创建日志存在，证明生产路由仍由旧路径决定。
- `increase(llm_gateway_ursm_v2_shadow_records_total{result="recorded"}[15m]) > 0`。
- `increase(llm_gateway_ursm_v2_shadow_records_total{result="failed"}[15m]) = 0`。
- `increase(ursm_shadow_diff_total{type=~"identical|availability_mismatch|order_mismatch"}[15m]) > 0`。
- `increase(ursm_shadow_diff_total{type=~"error|not_ready"}[15m]) = 0`。
- Redis tenant-aware node key 数量随真实流量增长；不要使用 `KEYS`，用 `SCAN`。

```bash
redis-cli --scan --pattern 'ursm:v2:node:*' | head
```

### 5.4 Shadow 七天验收门槛

连续运行满 7 个自然日，并以 Prometheus 全实例聚合：

```promql
sum(increase(ursm_shadow_diff_total{type="availability_mismatch"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="order_mismatch"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type=~"error|not_ready"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="identical"}[7d])) >= 10000
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="failed"}[7d])) == 0
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="recorded"}[7d])) > 0
```

同时要求请求错误率、无候选率、P99 延迟没有超过基线告警阈值。任何一项不满足都保持 shadow，不进入 canary。

### 5.5 Shadow 回滚

Systemd：

```bash
# 修改 /etc/llm-gateway-go/env
URSM_V2_MODE=off
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
sudo systemctl restart llm-gateway-go.service
```

Kubernetes：

```bash
kubectl -n <namespace> set env deployment/<deployment> \
  URSM_V2_MODE=off \
  URSM_V2_SHADOW_DOUBLE_WRITE- \
  URSM_V2_SHADOW_SAMPLE_RATE- \
  URSM_V2_CANARY_PERCENT-
kubectl -n <namespace> rollout status deployment/<deployment> --timeout=5m
```

回滚不删除 `ursm:v2:*`，保留现场供分析。不要依赖 `scripts/rollback/ursm_v2_to_legacy.sh`：它不能持久更新所有部署系统的环境配置。

## 6. Stage 2：Shadow -> Canary

此阶段只能在第 5.4 节全部通过并由变更审批确认后执行。

配置：

```text
URSM_V2_MODE=canary
URSM_V2_CANARY_PERCENT=<1|5|10|25|50|100>
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE
```

按 `1% -> 5% -> 10% -> 25% -> 50% -> 100%` 逐级推进。1/5/10/25/50 每级至少观察 1 小时，100% 至少观察 24 小时。每级要求：

- 错误率不高于基线 `1.2x`，无候选率不增加。
- P99 不高于基线 `1.2x`。
- `routing_state_source_total{source="canary"}` 与配置比例同量级。
- NodeMirror fallback、Redis error 和 not-ready 均为 0。
- v2 node key 覆盖所有该级实际命中的 tenant/model/candidate。

Canary 回 shadow：

```text
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=1
URSM_V2_SHADOW_SAMPLE_RATE=0.01
# 删除 URSM_V2_CANARY_PERCENT
```

紧急回 off 使用第 5.5 节命令。

## 7. Stage 3：Canary -> Authoritative（本次禁止执行）

以下仅记录未来门禁，不构成本次切换授权。除了 shadow 7 天 diff 全为 0 和 canary 100% 稳定 24 小时，还必须关闭这些 NO-GO 项：

1. tenant-aware 迁移覆盖率经独立审计为 100%，不能只验证“至少一个 node key”。
2. Redis 重启、ready 关闭、warmup、ready 重开演练通过。
3. authoritative 未 ready 和 Redis 读失败时的实际 fallback 语义已完成演练。
4. v2 snapshot writer 在目标模式连续成功，恢复数据可用。
5. `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`，故障关闸与恢复已验证。
6. 单独变更审批明确允许禁用 legacy manager。

未来配置形式：

```text
URSM_V2_MODE=authoritative
# 删除 URSM_V2_CANARY_PERCENT、URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE
```

本 P0-Z1 会话不得应用该配置。

## 8. 立即停止条件

任一阶段出现以下情况立即回到 shadow 或 off：

- 错误率或无候选率超过门槛。
- `ursm_shadow_diff_total{type=~"availability_mismatch|order_mismatch|error|not_ready"}` 增长。
- `llm_gateway_ursm_v2_shadow_records_total{result="failed"}` 增长。
- Redis 延迟/错误异常或 tenant 数据串读迹象。
- legacy manager 在 shadow 模式未装配。
