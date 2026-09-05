# llm-gateway-go 文档归档

> 归档基线：2026-08-17（重构）
> 归档分支：`chore/docs-archive-2026-08`
> 归档策略：保守归档 + 主题重组（保留 git 历史）

## 目的

`docs/` 目录历史累积了大量 bugfix / audit / fix / phase / handoff / deploy-report / summary 类过程文档，
与活跃设计文档混杂，且包含多个版本的重复目录（会话优化 v2/v3、omni-ref1/2/3、omnifree 等）。
本次重构把所有过程文档迁移到 `archive/` 统一管理，并按主题重组活跃文档。

## 归档目录总览

```
docs/archive/
├── README.md                ← 本文件
├── INDEX.md                 ← 按月份的归档索引（自动生成）
│
├── 2026-06/                 ← 2026 年 6 月归档（按子目录组织）
├── 2026-07/                 ← 2026 年 7 月归档
├── 2026-08/                 ← 2026 年 8 月归档（部分子目录）
│
└── process/                 ← 过程类归档（59 个子目录，按类别+月份）
    ├── audits/<YYYY-MM>/    ← 审计报告
    ├── fixes/<YYYY-MM>/     ← 修复记录
    ├── incidents/<YYYY-MM>/ ← 事故复盘
    ├── process/<YYYY-MM>/   ← 其他过程文档（summary/report/handoff/phase）
    ├── specs/<YYYY-MM>/     ← 历史设计规范
    ├── changelogs/<YYYY-MM>/← 14 天以上的变更日志
    │
    ├── session-optimization-v2/  ← 会话优化 V2（历史）
    ├── session-optimization-v3/  ← 会话优化 V3（历史）
    ├── omnifree/                  ← omnifree 实施方案（历史）
    ├── omni-ref/  omni-ref2/  omni-ref3/  omniroute-ref/  ← 历史 omni 项目
    ├── revision-0811/             ← 8 月 11 日密集审计集
    │
    ├── audit-collection/  audits-collection/  bugfix-collection/  ← 命名空间合并
    ├── fix-collection/  fixes-collection/  handoff-collection/
    ├── impl-summaries/  incidents-collection/  issues-collection/
    ├── lessons-learned/  session-logs-collection/  ui-audit-collection/
    │
    ├── analysis/  approval/  diagnostics/  notes/  refactor-plans/  ← 主题类过程
    │
    ├── base-optimization/  decay-optimization/  pricing-optimization/  ← 优化轨
    ├── pool-optimization/  pool-state-optimization/  routing-optimization-v3/
    ├── self-check-feature/  self-check-optimization/  full-optimization-v1/
    ├── param-compat/  ir-format-optimization/  format-conversion/
    │
    ├── vendor-profile/  vendor-management/  incident-records/  ← 供应商/事故
    │
    ├── new-arch-0703/  r112/  ursm-routing-redesign/  ← 架构方案（已完成）
    │
    ├── distribution-activation/  pending-tasks/  comprehensive-testing/  ← 综合
    ├── ui-optimization/  multimodal-testing/  llm-gateway-go-collection/
    │
    └── audit-20260808-002500/  runbook-2026-07-09-request-logging-252/  ← 特殊
        architecture-diagrams/  schema-history/  superpowers/
```

## 检索

```bash
# 按月份定位（月份桶）
ls docs/archive/2026-07/audits/ | head
ls docs/archive/2026-08/process/

# 按主题定位（主题桶）
ls docs/archive/process/session-optimization-v3/
ls docs/archive/process/revision-0811/

# 全量搜内容
grep -RE "swimlane" docs/archive/

# 按文件名
find docs/archive/ -name "*ROUTING*"

# 找特定日期的审计
find docs/archive/process/audits/ -name "2026-07*"
```

## 回滚 / 恢复

整批操作用 `git mv`（或 `mv` + `git add`），保留完整历史。可通过 git 恢复：

```bash
# 1. 回滚整个 commit
git revert <commit-sha>

# 2. 恢复单条文档到原位置
git checkout HEAD~1 -- <original-path>

# 3. 重新归类（如需）
# 把 docs/archive/process/process/2026-07/FINAL_SUMMARY.md 移动到 docs/operations/
mv docs/archive/process/process/2026-07/FINAL_SUMMARY.md docs/operations/
```

## 重构后保留的活跃文档（白名单）

### 顶层文档

- `README.md`、`CHANGELOG.md`、`SECURITY.md`、`CONTRIBUTING.md`、`PROJECT_CONFIG.md`、`LICENSE`
- `QUICK_REFERENCE.txt`、`SUMMARY.md`、`AUDIT_24H_20260817.md`
- Go 元数据 `go.mod`、`go.sum`、`VERSION`、`version.json`、`build_seq`
- 部署/构建配置 `.env.example`、`config.example.yaml`、`Dockerfile`、`Makefile`
- `docs/` 子目录、`_to_be_deleted/`、deploy 脚本

### 主题目录（保留）

| 目录 | 保留文件数 | 主题 |
|---|---|---|
| `architecture/` | 13 | 活跃架构设计 |
| `design/` | 16 | 活跃设计/规格 |
| `deployment/` | 14 | 部署/运维指南 |
| `operations/` | 10 | 运维手册 + TODO 索引 |
| `testing/` | 4 | 测试计划与策略 |
| `changelogs/` | 55 | 近 14 天变更日志 |
| `会话优化v4/` | 9 | 会话优化最新设计 |
| `partition/` | 11 | 分区表管理 |
| `pricing/` | 8 + 脚本 | 价格数据 |
| 其他 | ~30 | adr / api / modules / models / security / features / i18n / user-guide 等 |

## 相关索引

- [../README.md](../README.md) — `docs/` 顶层导览
- [../INDEX.md](../INDEX.md) — 主题索引
- [./INDEX.md](./INDEX.md) — 归档索引（按月份统计）
- [../../CHANGELOG.md](../../CHANGELOG.md) — 项目变更日志

---

**最后更新**：2026-08-17（全面重构）
**维护者**：LLM Gateway Team