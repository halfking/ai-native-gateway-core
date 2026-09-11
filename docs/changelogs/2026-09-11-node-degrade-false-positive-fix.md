# 2026-09-11 节点强制启用后误降级修复（探活梯子直连腿驱动 + passive 探测读面修复）

## 背景

2026-09-10 12:35–13:01，hzx-2（cred 42）与 minimax-prod-v2（cred 21）在管理员强制启用后随即显示降级。minimax-m3 直连与网关 ping 正常（延时约 3s+）。详证见 [docs/audit/2026-09-11-node-degrade-false-positive-fix.md](../audit/2026-09-11-node-degrade-false-positive-fix.md)。

## 根因（两处，均与 3s+ 延时无关）

1. **节点探活失败梯子用直连+网关复合结果**（`bg/node_probe.go` runOne `success := direct.ok && gw.ok`）。网关腿是按 model 名经网关路由的复合请求，失败不必然归属被测节点：cred 42 直连腿 5×200 的同时网关腿 5×401 invalid_key，健康节点在强启后数分钟内被踩到 `consecutive_failures=5`。sync 路径 `probeRecovered` 与 2026-08-18 URSM 回写早已确立"直连腿权威"原则，梯子未同步。
2. **passive 探测三处近期窗口查询读裸 `request_logs` 父表**（仅冷数据，154 上 max(ts) 滞后一天+）：成功重置、窗口总数刷新、评审成功检查全部"失明"——cred 21 评审窗口内 hot 表有 472 条成功仍被判"零成功"→ 误标 unreachable。

## 修复

- `bg/node_probe.go`：梯子与 `node_probe_runs.success` 改由直连腿驱动；成功分支真实记录网关腿结果（`last_gateway_ok/last_err_code/last_err_detail` 参数化），网关异常保持可见但不再降级节点；`emitSyncAudit` 同步统一语义。
- `bg/passive_probe_listener.go`：三处读面统一改 `request_logs_with_current_month`；评审成功检查模型名比较加 `LOWER()`。
- 回归测试：`bg/node_probe_ladder_regression_test.go`、`bg/passive_probe_recent_surface_test.go`（结构断言，沿用仓库先例）。

## 验证

`go build ./cmd/...` 通过；`go test ./bg/ -count=1` 全绿（新增回归 PASS）。

## 影响与部署

- 修复需随下一轮部署（Local→245→154 门禁）到达生产后生效。
- 部署后观测：cred 42 / cred 21 相关节点不再因网关腿 401 或 passive 评审误降级；`last_gateway_ok=false` 仍会在面板/状态中暴露网关 E2E 异常（P1 跟进项：网关热路径 key 供给失配根因未解）。
