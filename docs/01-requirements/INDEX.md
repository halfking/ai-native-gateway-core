# 需求 · 索引

> 最后更新：2026-10-01（全码审计轮手工重建，替代 2026-08-18 docs-pm-structure 自动生成的空壳索引）

## 文件清单

| 文件 | 类型 | 状态 | 最后修改 |
|---|---|---|---|
| [SYSTEM_REQUIREMENTS.md](SYSTEM_REQUIREMENTS.md) | **系统需求规格说明书（SRS，权威总纲）** | 现行 | 2026-10-01 |
| [README.md](README.md) | 目录说明 | 现行 | 2026-08-18 |
| [functional/FEATURES_CATALOG.md](functional/FEATURES_CATALOG.md) | 功能特性目录（需求→实现映射） | 现行 | 2026-10-01 |
| [functional/FR-selfcheck-timely-recovery.md](functional/FR-selfcheck-timely-recovery.md) | 单特性 FR（自检及时恢复，格式模板） | 已实现 | 2026-09-08 |

## 结构与职责

- **SYSTEM_REQUIREMENTS.md**（2026-10-01 全码审计重生）：系统定位/角色/边界/FR×19 域/NFR×13/全局不变量。此前的需求散落在 `03-design/FEATURE-REQ-*.md`、`freediscovery-requirements.md`、`GOAL_CLIENT_SIGNALS.md`，本轮起统一收编入口为本文件，散落文档保留为单特性细化。
- **functional/**：FR 级文档。FEATURES_CATALOG 为全功能实现映射（含 🔴 新旧并行/⚪ 死代码状态标注）；FR-*.md 为单特性深化模板。
- **non-functional/**：NFR 汇总于 SYSTEM_REQUIREMENTS §5，本目录留作单特性 NFR 拆分。
- **user-stories/**：预留（用户故事可从 SYSTEM_REQUIREMENTS §2 角色展开）。
- **changes/**：需求变更记录（按需登记）。

## 统计

- 总文件：4（此前索引显示"总文件：0"为自动生成缺陷，已修正）
