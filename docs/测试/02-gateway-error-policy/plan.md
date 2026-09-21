# 02 网关错误策略与请求流日志 — 测试方案

## 目标

核实 15 类模型调用故障的分类、节点标注、重试/转移/向客户端报错是否与策略一致；补齐请求流日志，便于 154 现场按 `request_id` 还原。

## 范围

- `empty_response` / overload / network / EPIPE / survival `committed_output` 的有效可恢复投影
- 过载节点：模型范围冷却 + 入队探测（不再静默跳过 probe）
- MiniMax `minimax[>[...]<]` 工具文本泄漏的 unwrap + 宽松 `<tool_call>` 识别
- 不稳定模型 holdback 前缀匹配（`minimax-m3*` / `glm-5.2*`）
- `request_flow` 结构化日志（classify / action / survival / failover）

## 验证层

| 层 | 方式 |
|---|---|
| L1 单测 | `go test` 覆盖 errorsx / vendorstrip / streaming / executors / requestflow |
| L2 契约 | 源码钉死 retryable 投影、overload probe、terminal reason |
| L3 运行 | 不更新本地 `~/kaixuan` 服务；部署脚本验证 154 |

## 不做

- 不重启本机正在使用的网关
- 不默认打开 L2 aligned replay
- 不把 empty_response / overload 计入 80% 凭据硬降级
