# URSM v2 直接启动操作手册

> 默认路径是直接 `authoritative`：网关启动时会从 `node_probe_state` 全量导入 tenant-aware Redis 状态、发布 coverage manifest，并仅在校验成功后打开 ready gate。不需要 shadow 或 canary 灰度。
> 每次部署必须修改真实 systemd `EnvironmentFile` 或 Kubernetes Deployment，不使用 transient `systemctl set-environment`。Redis、PostgreSQL 和 system monitor 任一不可用时，网关会在监听端口前退出。

## 1. 模式语义

| 模式 | 生产状态权威 | outcome 写入 | 候选路由 |
|---|---|---|---|
| `off` | legacy credentialstate | 不写 v2 | legacy |
| `shadow` | legacy credentialstate | `URSM_V2_SHADOW_DOUBLE_WRITE=1` 时全量旁写 | 按 sample rate 异步双算，只观测 |
| `canary` | 命中请求使用 v2；未命中仍为 legacy | 命中请求写 v2 | 命中请求按 v2 排序，持续采样 diff |
| `authoritative` | URSM v2 | 全量写 v2 | 全量 v2；gate/Redis 异常时保护性拒绝 |

`URSM_V2_MODE` 缺省为 `authoritative`。`off` 保留为紧急回退开关；shadow 与 canary 仅保留给隔离诊断，不是上线前置流程。

## 2. 直接启动前置条件

所有条件必须满足：

1. PostgreSQL、Redis 已启用且网关启动账号可访问；Redis 地址通过 `LLM_GATEWAY_REDIS_ADDR` 配置。
2. 持久环境文件包含 `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true`。默认 `URSM_V2_MODE=authoritative`；可显式写入同值以便审计。
3. `go build ./...`、`go test -race ./domains/ursm/...`、`go test -race ./domains/streaming/executors` 通过。
4. `node_probe_state` 含有效的全租户状态。网关在启动中自动创建全局 coverage manifest；若需要预先审计，使用 `go run ./cmd/migrate-ursm-v2 --apply`，禁止带 `--tenant-id` 后直接启动权威模式。
5. Prometheus 已抓取使用管理员 Bearer token 的 `/metrics`，并记录部署前 24 小时错误率、无候选率和 P99 基线。

## 3. 直接权威启动

在目标服务的持久环境文件中设置：

```text
URSM_V2_MODE=authoritative
LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true
# 不设置 URSM_V2_SHADOW_DOUBLE_WRITE、URSM_V2_SHADOW_SAMPLE_RATE、URSM_V2_CANARY_*
```

重启 systemd 服务或滚动发布 Kubernetes Deployment。启动日志必须依次显示 manager ready gate 已关闭、legacy bootstrap 总量/写入/跳过计数，以及 coverage 校验后 gate 已打开。任一步失败都会在服务监听前退出；不要绕过或手工打开 `ursm:v2:meta:ready`。

启动完成后，确认 `meta:ready=1`、legacy credentialstate manager 未创建、`routing_state_source_total{source="authoritative"}` 增长。Redis 读取失败或 gate 未 ready 时请求保护性拒绝，不回退 legacy。

用只读 evidence 工具确认当前 scrape 的标签契约完整：

```bash
bash scripts/verify-ursm-v2-rollout.sh \
  --stage authoritative \
  --url http://127.0.0.1:8781 \
  --token "$LLM_GATEWAY_ADMIN_API_KEY"
```

`fallback` 非零、错误率或无候选率高于 24 小时基线、或 P99 增幅超过 5% 时立即执行回退。

## 4. 停止与回退

authoritative coverage/gate/recovery 异常或生产指标越线时，持久回退到 `off`。

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
