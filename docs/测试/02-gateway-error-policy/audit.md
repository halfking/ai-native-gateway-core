# 02 网关错误策略 — 审计

- Standards：最小补丁；新文件 kebab-case；无 secret；gofmt；L1 全绿。大文件（handler/executor/classify）只改决策点。
- Spec：对齐 15 类故障的重试/转移/报错与 `request_flow` 日志。未开 L2，未把 empty/overload 计入 80% 硬降级。
- 降级：Cursor 无并行 code-review sub-agent，本文件为轻量双轴自审。
- 部署：按用户要求只更新 154，不更新本地 `~/kaixuan`。
