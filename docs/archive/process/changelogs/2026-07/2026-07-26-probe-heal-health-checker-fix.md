---
title: 探针自愈 + 路由健康检查查询修复
date: 2026-07-26
author: ACC Agent
scope: llm-gateway-go, bg/, domains/streaming/executors/, cmd/gateway/
---

# 探针自愈 + 路由健康检查查询修复

## 改动清单

### 1. 探针自愈：业务请求成功后清除 node_probe_state 门控

**问题**：`v_routable_credential_models` 视图的 `node_probe_state.last_direct_ok=FALSE` 硬门控将探针失败（含超时）的凭据排除出路由视图，但 streaming executor 上的真实业务请求成功后没有任何路径清除该门控。探针失败一次，凭据即被排除 5s～6h，即使该凭据对真实业务请求完全健康。

**修改**：
- `domains/streaming/executors/executor.go`: 新增 `NodeProbeHealthyFunc` 回调类型 + `Executor.NodeProbeHealthy` 字段，在 `Execute()` 成功路径上调用（`domains/streaming/executors/executor.go:1924`）
- `cmd/gateway/main.go`: 注入 `bg.MarkNodeProbeHealthy` 闭包
- `bg/node_probe.go:230,241` + `bg/credential_probe_v2.go:402`: HTTP timeout 15s→30s，减少 NVIDIA NIM 冷启动等慢 endpoint 的探测失败误报

**联动**：`MarkNodeProbeHealthy`（`bg/node_probe.go:486`）是 upsert：清除 `consecutive_failures`、设置 `last_direct_ok=TRUE`、`next_retry_at = now()+1h`。

### 2. 路由健康检查查询修复

**问题**：`bg/routing_health_checks.go:25` 的 `credential_active_not_routable` 查询引用了 `c.name`，但 credentials 表已将 `name` 列重命名为 `label`；同时引用了不存在的 `c.provider_name`。该查询自重构后就持续报错 `column c.name does not exist`，导致路由健康检查静默失效。

**修改**：
- `bg/routing_health_checks.go:25`: `c.name` → `c.label`；移除 `c.provider_name`，改为 `JOIN providers p ON p.id = c.provider_id` 并从 `p.display_name` 获取供应商名称。

## 提交历史

```
ae942196e fix(probe): heal node_probe_state on real request success, increase timeout to 30s
79f37c222 fix(health): fix credential_active_not_routable query — c.name -> c.label, add providers JOIN
```

## 验证

- ✅ `go build ./...` + `go vet ./...` 通过
- ✅ 245 预发布部署（seq=1394, 1395）通过 L1-L4
- ✅ 154 生产部署（seq=1396）通过，真实流量正常（`gpt-5.6-luna` upstream 200）
- ✅ `minimax-m3`、`NVIDIA NIM` 等模型正常路由
- ✅ `routing_health_checker: run completed (critical=0, warning=4)` 查询不再崩溃
