# URSM v2 一次性全量切换操作手册

> 路径：`off → shadow 核对 → authoritative 全量`。不使用 canary。
> 当前切换模型是**严格 authoritative**：v2 gate 未 ready、覆盖不完整或 Redis 状态读取失败时，路由拒绝该请求；不会放行未经过 v2 状态过滤的候选，也不会回退到 legacy 状态源。

## 1. 模式语义

| 模式 | 生产状态权威 | outcome 写入 | 路由比较/决策 |
|---|---|---|---|
| `off` | legacy credentialstate | 不写 v2 | legacy |
| `shadow` | legacy credentialstate | `URSM_V2_SHADOW_DOUBLE_WRITE=1` 时全量旁写 | 采样双算，只观测 |
| `authoritative` | URSM v2 | 全量写 v2 | 全量 v2；gate 异常保护性拒绝 |

`canary` 代码为兼容保留，不属于生产切换路径，也不得设置 `URSM_V2_CANARY_PERCENT`。

## 2. 切换前置条件

所有条件必须满足：

1. Redis 已启用，且 `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`。authoritative 缺少该变量时 gateway 拒绝启动。
2. Prometheus 已抓取带管理员 Bearer token 的 `/metrics`，retention 不少于 8 天。
3. `go build ./...`、`go test -race ./domains/ursm/...` 与 `go test -race ./domains/streaming/executors` 通过。
4. 完成 migration CLI 的 dry-run、apply 和 coverage manifest 校验。
5. 已记录切换前 24 小时错误率、无候选率、P99 作为基线。

```bash
curl -fsS -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  http://127.0.0.1:8781/metrics \
  | grep -E 'ursm_shadow_diff_total|llm_gateway_ursm_v2_shadow_records_total|routing_state_source_total'
```

## 3. Tenant-aware migration 与 coverage manifest

先 dry-run。CLI 从 `node_probe_state JOIN credentials` 取得真实 tenant，写入与运行时相同的 key：

```text
字符串 tenant：ursm:v2:node:<tenant>:<credential>:<model>
纯数字 tenant：ursm:v2:node:t:<tenant>:<credential>:<model>
```

```bash
./bin/migrate-ursm-v2 --pg="$LLM_GATEWAY_DATABASE_URL" \
  --redis="$REDIS_URL" \
  --key-prefix=ursm:v2:
```

确认 dry-run 中 `expected coverage keys` 等于读取的 legacy node 行数，再实际写入：

```bash
./bin/migrate-ursm-v2 --apply --pg="$LLM_GATEWAY_DATABASE_URL" \
  --redis="$REDIS_URL" \
  --key-prefix=ursm:v2:
```

apply 不带 `--tenant-id` 时会重写 Redis set `ursm:v2:meta:coverage`。其中每个 member 都是一个 expected tenant-aware node key。带 `--tenant-id` 的局部 migration 只写独立 `ursm:v2:meta:coverage:tenant:<tenant>` manifest，不能替代全量 authoritative coverage。不要手工用旧的 `ursm:v2:node:<credential>:<model>` key 替代全局 coverage manifest。

```bash
redis-cli --raw SCARD ursm:v2:meta:coverage
redis-cli --scan --pattern 'ursm:v2:node:*' | head -20
```

每个 manifest member 必须实际存在，并至少含 `generation`、`available` 字段。CAS 跳过 live/admin 状态时仍会写入该 key 到 manifest；apply 后必须重新运行 CLI 或逐项修复缺失 key，不能把缺失 coverage 当作可切换状态。

## 4. Stage 1：Off → Shadow（七天核对）

### Systemd

持久修改 `/etc/llm-gateway-go/env`：

```bash
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=1
URSM_V2_SHADOW_SAMPLE_RATE=0.01
# 确保没有 URSM_V2_CANARY_PERCENT
```

```bash
sudo systemctl restart llm-gateway-go.service
sudo systemctl is-active llm-gateway-go.service
sudo journalctl -u llm-gateway-go.service -n 200 --no-pager \
  | grep -E 'ursm.v2 manager constructed|credential state manager created'
```

必须看到 legacy credentialstate manager 创建；shadow 下它仍是生产读写权威。

### Kubernetes

```bash
kubectl -n <namespace> set env deployment/<deployment> \
  URSM_V2_MODE=shadow \
  URSM_V2_SHADOW_DOUBLE_WRITE=1 \
  URSM_V2_SHADOW_SAMPLE_RATE=0.01 \
  URSM_V2_CANARY_PERCENT-
kubectl -n <namespace> rollout status deployment/<deployment> --timeout=5m
```

### 七天 GO 门槛

进程 counter 会重启归零，必须用 Prometheus 跨实例聚合：

```promql
sum(increase(ursm_shadow_diff_total{type="availability_mismatch"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="order_mismatch"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type=~"error|not_ready"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="identical"}[7d])) >= 10000
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="failed"}[7d])) == 0
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="recorded"}[7d])) > 0
```

另要求错误率、无候选率与 P99 不超过预先登记的基线阈值。`ursm_shadow_diff_total` 是 legacy/v2 路由比较；通用 routing strategy shadow 指标不是切换证据。

Shadow 期间 snapshot writer 会在 double-write 开启时运行，用于审计。它不是 Redis 故障恢复来源。

## 5. Stage 2：Shadow → Authoritative 全量

只在第 2、3、4 节全部满足后执行一次性切换。

### Systemd

```bash
# 修改 /etc/llm-gateway-go/env
URSM_V2_MODE=authoritative
LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
sudo systemctl restart llm-gateway-go.service
sudo systemctl is-active llm-gateway-go.service
```

### Kubernetes

```bash
kubectl -n <namespace> set env deployment/<deployment> \
  URSM_V2_MODE=authoritative \
  LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true \
  URSM_V2_SHADOW_DOUBLE_WRITE- \
  URSM_V2_SHADOW_SAMPLE_RATE- \
  URSM_V2_CANARY_PERCENT-
kubectl -n <namespace> rollout status deployment/<deployment> --timeout=5m
```

### 启动验收

authoritative 启动会先同步设置 `ursm:v2:meta:ready=0`，不会继承 shadow 的 ready。只有 `ursm:v2:meta:coverage` 中每个 tenant-aware key 存在且字段完整后，启动日志才会出现：

```text
ursm.v2: authoritative gate opened after coverage validation
```

若出现 `coverage validation failed`，gate 必须保持关闭；此时请求是保护性拒绝，不能继续接流。修复 migration/manifest 后重启或重新执行 coverage 验证。

切换后检查：

```bash
redis-cli GET ursm:v2:meta:ready
redis-cli --raw SCARD ursm:v2:meta:coverage
curl -fsS -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  http://127.0.0.1:8781/metrics | grep routing_state_source_total
```

验收：

- `meta:ready` 为 `1`。
- 日志包含 `credentialstate.Manager disabled in URSM v2 authoritative mode`。
- `routing_state_source_total{source="authoritative"}` 增长。
- `fallback`、无候选率、错误率和 P99 不得超过基线。
- snapshot writer 正常 flush。
- 运行 Redis restart/recovery 演练：gate 关闭、恢复后 coverage/state 重新验证才允许开门。Redis 读取失败期间的请求被明确拒绝；这是设计行为，不是 legacy fallback。

## 6. 紧急回退到 Off

回退只改变真实部署配置并重启/滚动发布；不要依赖 transient systemd environment 或旧 rollback 脚本。

### Systemd

```bash
# 修改 /etc/llm-gateway-go/env
URSM_V2_MODE=off
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
sudo systemctl restart llm-gateway-go.service
```

### Kubernetes

```bash
kubectl -n <namespace> set env deployment/<deployment> \
  URSM_V2_MODE=off \
  URSM_V2_SHADOW_DOUBLE_WRITE- \
  URSM_V2_SHADOW_SAMPLE_RATE- \
  URSM_V2_CANARY_PERCENT-
kubectl -n <namespace> rollout status deployment/<deployment> --timeout=5m
```

回退验收：legacy credentialstate manager 创建、`routing_state_source_total{source="off"}` 增长、真实请求恢复 legacy 状态写入。保留 `ursm:v2:*` 与 coverage manifest 供复盘；不要删除现场数据。

## 7. 立即停止条件

立即回 off 或保持 shadow，禁止 authoritative 继续接流：

- coverage manifest 缺 key、含 legacy-only key，或 ready 无法打开。
- `availability_mismatch`、`order_mismatch`、`error`、`not_ready` 或 sidecar `failed` 在 shadow 观察窗口增长。
- authoritative 出现 `fallback`、错误率/无候选率/P99 超过阈值。
- Redis 健康监控未启用，或 recovery gate 演练失败。
- 发现跨 tenant 状态串读、数字 tenant key 未使用 `t:` 格式，或 snapshot tenant 解析异常。
