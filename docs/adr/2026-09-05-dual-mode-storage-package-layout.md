# ADR-0019: 双模式存储架构的包布局（storage / monitoring 根级包例外）

- **Status**: Accepted
- **Date**: 2026-09-05
- **Related**: [ADR-0002-target-go-package-layout.md](ADR-0002-target-go-package-layout.md)、
  [../dual-storage-implementation-tasks.md](../dual-storage-implementation-tasks.md)、
  [../storage/README.md](../storage/README.md)

## Context

双模式存储架构（full = PostgreSQL + Redis，lite = SQLite + 本地文件 + 内存）需要新增
一批 Go 包。ADR-0002 的目标是把根目录约 45 个平铺 Go 包收敛到约 8 个分层目录
（`internal/platform/...` 等），本期新增包如果不加说明地落在根目录，会与该收敛
方向冲突，形成迁移债。

本期实际新增的包：

| 包 | 内容 |
|----|------|
| `storage/` | 存储接口、数据类型、哨兵错误（纯 Go，无实现依赖） |
| `storage/sqlite`、`storage/file`、`storage/memory` | lite 模式实现子包，反向依赖根 `storage` 包 |
| `storage/factory` | 双模式工厂（依赖根包接口 + 三个实现包） |
| `monitoring/` | 存储层指标（atomic 计数 + JSON 端点数据源，零内部依赖） |

## Decision

1. **本期允许 `storage/`、`monitoring/` 作为根级包存在**，视为 ADR-0002 收敛完成前的
   受控例外。理由：
   - 规划文档（`docs/dual-storage-implementation-tasks.md`）锁定了这些路径，多个
     并行交付物（文档、脚本、runbook）引用该布局；
   - 依赖方向干净：`storage/{sqlite,file,memory} → storage ← storage/factory`，
     `monitoring` 零内部依赖，`cmd/gateway → 全部`。不存在反向依赖，不加重
     ADR-0002 所治理的耦合环；
   - 工厂必须独立于根包（根包若 import 实现子包即构成 import cycle），已按
     `storage/factory` 子包落位。
2. **不新增第五个 lite 实现子包到根级**；后续如需 full 模式 PG 实现，放入
   `storage/pg`（同样依赖根包接口）。
3. **迁移路径**：ADR-0002 分层收敛启动时，`storage/` 树整体迁移至
   `internal/platform/storage/`（纯 import 路径替换，包间相对结构不变），
   `monitoring/` 迁至 `internal/platform/monitoring/`。本期不预付这笔改动。

## Consequences

- 短期：根目录 Go 包数量 +2（`storage/`、`monitoring/`），与 ADR-0002 的收敛
  目标存在张力，本 ADR 即为该例外的记录与边界。
- 长期：`storage/` 树内部结构（接口/实现/工厂三层）与 `internal/platform`
  分层兼容，迁移成本为一次性 import 替换。
- 约束：`domains/` 下的包只允许依赖 `storage`（接口）与 `monitoring`，不允许
  直接依赖 `storage/factory` 及各实现子包——工厂仅在 `cmd/gateway` 装配层使用。
