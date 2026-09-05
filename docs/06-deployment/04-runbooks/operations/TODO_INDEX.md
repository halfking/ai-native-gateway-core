# Gateway TODO 索引

> 创建时间: 2026-07-21
> 范围: cmd/gateway, internal/, domains/, services/

## 1. 高优先级（影响生产）

无 — 阻塞性 TODO 已全部清理。

## 2. 中优先级（1-2 个迭代）

### 2.1 审批流程集成完成度

`docs/TODO_APPROVAL_INTEGRATION.md`（已存在）

剩余项：
- 端到端测试（带真实数据库）
- v2 pipeline 集成（当前只在 v1）
- 客户端 pending-response 轮询接口

### 2.2 Memory Service DLQ

`docs/TODO_MEMORY_DLQ.md`

依赖 Memora 接口扩展，估时 1-2 个迭代。

## 2.3 72小时消息/会话审计收尾（2026-08-08）

已完成：V2 outbound 协议兼容、结构化 content round-trip、锁内 delta 计算、最终 outbound 快照、租户唯一键/summary join、OmniFree worker tenant guard、RLS policy 对齐和 V2 cache shutdown。

待真实环境验证：
- PostgreSQL 执行 migration 476 up/down，并验证历史重复键处理与跨租户同 session/request ID。
- 启动 DDL 与 migration 075 的 RLS policy 等价性、BYPASSRLS 角色隔离测试。
- 同 session 并发请求的真实 DB 重建顺序与 `go test -race ./...` 全量结果。


### 3.1 Community Mode 完整实施

`docs/TODO_COMMUNITY_MODE.md`

社区模式租户限制需要 handler-layer 校验，估时 1 个迭代。

### 3.2 Unified Probe 清理

`cmd/gateway/main.go:1660-1661`

移除旧的 `modelProbe` 和 `suspiciousProbe`，
依赖 unifiedProbe 完全替代。
需要：
- 确认 unifiedProbe 覆盖所有功能
- admin handler 引用切换
- 配置文件清理

估时：1 个迭代（含回归测试）。

### 3.3 Session Logic 提取

`cmd/gateway/main_pipeline.go:313`

将 session 加载逻辑从 ChatHandler 提取到 session.Hook。
重构工作量较大，但属于架构优化。

## 4. 待评估（需要业务决策）

### 4.1 License Authority 小工具

`cmd/license-authority/update_handler.go:137`
`cmd/license-authority/manifest_handler.go:84`

TODO 是关于 size 字段和额外 image 解析。
需评估是否需要实现。

### 4.2 URSM Policy 实现

`domains/ursm/policy.go:6`
`domains/ursm/cost_scorer.go:17`
`domains/ursm/api_update.go:31/48/70`

URSM 模块的策略应用 + 成本评分 + 审计日志 + Provider 状态检查。
属于 v2 URSM 设计范畴。

### 4.3 Streaming 优化

`domains/streaming/handler.go:1679`
`domains/streaming/strip_deepseek_fields.go:8`
`domains/streaming/executors/executor.go:1285`
`domains/streaming/executors/executor_chat.go:103`

Streaming 模块的小优化项（policy loading, field validation, identity wiring）。
不影响功能。

## 5. 已完成清理（2026-07-21）

### 5.1 Restricted Mode Middleware 启用

`cmd/gateway/main.go` 添加 `gRestrictedMode` 全局标志，
license 校验失败时启用 `licensing.RestrictedModeMiddleware`。
参考 `licensing/restricted_mode.go` 的实现。

### 5.2 Token Refresh Daemon 启用

`cmd/gateway/main.go` 调用 `licensing.StartTokenRefreshDaemon`，
首次延迟 1 小时，之后每 6 天刷新。

### 5.3 Community Mode 占位

`cmd/gateway/main.go` 添加 `licensing.GetCommunityModeRestrictions()` 调用，
记录当前限制到日志。完整实现待后续迭代。

## 6. 维护建议

- 每周扫描一次 `cmd/ internal/ domains/` 下的 TODO 注释
- 新引入的 TODO 必须带 owner + 截止日期
- 高优先级 TODO 必须出现在本索引中
- 已完成的 TODO 在 git commit message 中引用本文件路径
