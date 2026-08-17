# URSM v2 渐进切换操作手册

> 路径：`off → shadow（双轨核对）→ canary（1% → 5% → 10%）→ authoritative`。
> 252 gateway runtime 保持 deferred/fail-closed；仅沿 `local → dev → 245 → 154` 推进。每一阶段必须修改真实 systemd `EnvironmentFile` 或 Kubernetes Deployment，不使用 transient `systemctl set-environment`。

## 1. 模式语义

| 模式 | 生产状态权威 | outcome 写入 | 候选路由 |
|---|---|---|---|
| `off` | legacy credentialstate | 不写 v2 | legacy |
| `shadow` | legacy credentialstate | `URSM_V2_SHADOW_DOUBLE_WRITE=1` 时全量旁写 | 按 sample rate 异步双算，只观测 |
| `canary` | 命中请求使用 v2；未命中仍为 legacy | 命中请求写 v2 | 命中请求按 v2 排序，持续采样 diff |
| `authoritative` | URSM v2 | 全量写 v2 | 全量 v2；gate/Redis 异常时保护性拒绝 |

`URSM_V2_MODE` 缺省为 `off`。shadow 绝不改变 legacy 的生产路由或主写；不能以 `diff=0` 替代 outcome 旁写的成功证据。

## 2. 切换前置条件

所有条件必须满足：

1. Redis 已启用；authoritative 前 `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`。
2. Prometheus 已抓取使用管理员 Bearer token 的 `/metrics`，retention 不少于 8 天。
3. `go build ./...`、`go test -race ./domains/ursm/...`、`go test -race ./domains/streaming/executors` 通过。
4. 完成 migration CLI 的 dry-run、apply 和 coverage manifest 校验；authoritative 使用全量 tenant-aware coverage，不接受局部 tenant manifest。
5. 在每次模式变更前记录连续 24 小时的错误率、无候选率、P99 基线。
6. local 与 dev 已验证 default-off、shadow 启动、认证 metrics 抓取和 `off` 回退。

用只读 evidence 工具确认当前 scrape 的标签契约完整；该工具不替代 Prometheus 的跨实例趋势查询：

```bash
bash scripts/verify-ursm-v2-rollout.sh \
  --stage shadow \
  --url http://127.0.0.1:8781 \
  --token "$LLM_GATEWAY_ADMIN_API_KEY"
```

## 3. Stage 1：local、dev、245 Shadow

先在 local、dev 使用以下持久配置验证；通过后才在 245 使用相同配置：

```text
URSM_V2_MODE=shadow
URSM_V2_SHADOW_DOUBLE_WRITE=true
URSM_V2_SHADOW_SAMPLE_RATE=0.01
# 删除 URSM_V2_CANARY_PERCENT
```

Systemd 修改 `/etc/llm-gateway-go/env` 后重启；Kubernetes 使用 `kubectl set env` 并执行 `rollout status`。启动后必须确认日志包含 URSM v2 manager 和 legacy credentialstate manager 创建：shadow 下后者仍是唯一生产权威。

### 3.1 Shadow 双轨 GO 门槛

245 必须连续观察 7 天。进程 counter 会在重启归零，因此以 Prometheus 的跨实例聚合为准：

```promql
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="failed"}[7d])) == 0
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="recorded"}[7d])) > 0

sum(increase(ursm_shadow_diff_total{type="availability"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="order"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="top1"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type=~"error|not_ready"}[7d])) == 0
sum(increase(ursm_shadow_diff_total{type="identical"}[7d])) >= 10000
```

第一组是 **outcome 旁写证据**；第二组是 **候选路由 diff 证据**，两组缺一不可。代码实际 label 是 `availability` 和 `order`，禁止使用历史错误标签 `availability_mismatch`、`order_mismatch`。

性能门禁采用保守标准：相对本阶段前连续 24 小时基线，错误率与无候选率不得增加，P99 增幅不得超过 5%。任一项越线，不得进入 canary。

## 4. Stage 2：Canary 逐档扩容

仅当 245 shadow 的 7 天双轨证据和性能门禁全部通过时，按以下阶梯切换。保留 sample rate 以持续观察 v2/legacy 候选差异：

| 阶段 | 配置 | 最短观察期 |
|---|---|---|
| C1 | `URSM_V2_MODE=canary`、`URSM_V2_CANARY_PERCENT=1`、`URSM_V2_SHADOW_SAMPLE_RATE=0.01` | 24 小时 |
| C2 | `URSM_V2_CANARY_PERCENT=5` | 48 小时 |
| C3 | `URSM_V2_CANARY_PERCENT=10` | 7 天 |

每一档都必须满足以下条件，才能升到下一档：

```promql
sum(increase(llm_gateway_ursm_v2_shadow_records_total{result="failed"}[window])) == 0
sum(increase(ursm_shadow_diff_total{type=~"availability|order|top1|error|not_ready"}[window])) == 0
sum(increase(routing_state_source_total{source="canary"}[window])) > 0
sum(increase(routing_state_source_total{source="fallback"}[window])) == 0
```

同时应用第 3.1 节的保守性能门禁。每次变更后运行：

```bash
bash scripts/verify-ursm-v2-rollout.sh \
  --stage canary \
  --url http://127.0.0.1:8781 \
  --token "$LLM_GATEWAY_ADMIN_API_KEY"
```

工具若报告当前 `fallback` 或 sidecar `failed` 非零，会直接失败；跨实例趋势仍以该窗口的 `increase(...)=0` 为最终晋级依据。

完成 245 C3 的完整 7 天证据后，154 从 shadow 重新执行同一套流程。authoritative 不在本轮直接切换，须在 154 canary 证据完整后另行作出 GO 决策。

## 5. Authoritative 全量切换（另行审批）

只有完成全部 shadow 和 canary 证据、全量 tenant-aware coverage 完整、并通过 Redis restart/recovery 演练后才可提交切换审批：

```text
URSM_V2_MODE=authoritative
LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
```

启动必须先将 `ursm:v2:meta:ready=0`，全量 coverage 校验成功后才打开 gate。确认 `meta:ready=1`、legacy manager 已禁用、`routing_state_source_total{source="authoritative"}` 增长。Redis 读取失败或 gate 未 ready 时请求保护性拒绝，不回退 legacy。

## 6. 停止与回退

立即停止晋级并执行对应回退：

- shadow 任意 outcome sidecar `failed`、availability/order/top1/error/not-ready 增长，或性能门禁越线：保持 shadow 排查，必要时回 `off`。
- canary 任意 `fallback` 增长、双轨证据异常或性能门禁越线：将 `URSM_V2_MODE=shadow` 并删除 `URSM_V2_CANARY_PERCENT`。
- authoritative coverage/gate/recovery 异常：回 `off`。

回退必须修改真实部署配置并重启/滚动发布：

```text
URSM_V2_MODE=off
# 删除 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_PERCENT
```

验证 legacy credentialstate manager 创建、`routing_state_source_total{source="off"}` 增长、真实请求恢复 legacy 状态写入。保留 `ursm:v2:*` 和 coverage manifest 供复盘，禁止删除现场数据。

## 7. 245 Shadow 证据记录

每次 245 shadow 必须保留以下记录：

```text
版本/build：
部署时间与操作者：
24h 基线：错误率 / 无候选率 / P99（查询或截图链接）
每日 PromQL：outcome 旁写 / routing diff / 性能门禁
第 7 天结论：GO canary / 保持 shadow / 回 off
异常与回退记录：
```

154 仅在 245 记录完整且 local、dev、245 全部通过后才可开始；252 不在任何阶段部署 gateway。
