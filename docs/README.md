# docs/ — LLM Gateway 文档

> 重构时间：2026-08-17 · 重构分支：`chore/docs-archive-2026-08` · 重构策略：保守归档 + 主题重组  
> 最后更新：2026-09-07

---

## 🧹 2026-09-07 根目录文档整理

按项目文档规范清理仓库根目录（此前累积了 ~100 个过程文档）：

- **根目录只保留约定文件**：`README.md`、`CHANGELOG.md`、`LICENSE`、`NOTICE`、`CONTRIBUTING.md`、`SECURITY.md`、`CODE_OF_CONDUCT.md`、`SUPPORT.md`、`ROADMAP.md`。
- **仍有效的指南/参考迁入 docs/**（重命名为 kebab-case）：

  | 原位置（根目录） | 新位置 |
  |---|---|
  | PROJECT_CONFIG.md | [project-config.md](./project-config.md) |
  | BLUEGREEN_QUICKSTART.md | [deployment/bluegreen-quickstart.md](./deployment/bluegreen-quickstart.md) |
  | LOCAL_DEPLOY_OPTIMIZATION.md | [deployment/local-deploy-optimization.md](./deployment/local-deploy-optimization.md) |
  | DEPLOYMENT_QUICK_REFERENCE.md | [deployment/local-deploy-quick-reference.md](./deployment/local-deploy-quick-reference.md) |
  | DEPLOYMENT_GUIDE.md（热力图专项） | [deployment/credential-monitor-heatmap-deployment-guide.md](./deployment/credential-monitor-heatmap-deployment-guide.md) |
  | LOCAL_DEPLOYMENT_TEST_GUIDE / TESTING_INDEX / TESTING_README | [deployment/local-deployment-test-guide.md](./deployment/local-deployment-test-guide.md) 等 |
  | CONTEXT_WINDOW_OVERRIDE_GUIDE / QUICK_GUIDE | [operations/context-window-override-guide.md](./operations/context-window-override-guide.md) 等 |
  | RELEASE_CHECKLIST.md | [operations/release-checklist.md](./operations/release-checklist.md) |
  | 154-service-status-and-monitoring.md | [operations/154-service-status-and-monitoring.md](./operations/154-service-status-and-monitoring.md) |
  | SCREENSHOT_GUIDE.md | [operations/screenshot-guide.md](./operations/screenshot-guide.md) |
  | git-best-practices.md | [standards/git-best-practices.md](./standards/git-best-practices.md) |
  | README_MOCK_TESTING_FRAMEWORK.md / E2E_QUICK_REFERENCE.md | [05-testing/](./05-testing/) |
  | TROUBLESHOOTING-routing-analytics / CREDENTIAL_CHECK_QUICK_FIX | [troubleshooting/](./troubleshooting/) |
  | AUDIT_CREDENTIAL_DECRYPT_FIX_20260905.md | [audit/2026-09-05-credential-decrypt-fix-audit.md](./audit/2026-09-05-credential-decrypt-fix-audit.md) |
  | QUICK-START.md（GLM-5.2 事故 Runbook） | archive/process/incidents/2026-09/ |

- **过期过程文档已删除**：FINAL_* / COMPLETION_* / SELFCHECK_* / HANDOFF_* / 修复与验收报告、`.acc-task-stop-summary*`、`.handoff/`、`.artifacts/` 等 AI 会话产物（git 历史可恢复）；docs/ 顶层的重复验证报告（ERROR-EVIDENCE-MATRIX-20260904/05、LOCAL-VERIFICATION-REPORT-20260904/05 等）一并清理。
- **docs/ 顶层审计/修复/设计文档归位**：`audit-2026-09-*.md` → [audit/](./audit/)、`2026-09-06-*-fix.md` → [fixes/](./fixes/)、热力图需求/实施 → [design/](./design/)。
- **敏感信息脱敏**：全部活跃文档中的真实服务器 IP / SSH 密码 / 测试令牌替换为 `<env:HOST_154_IP>` 等占位符（对照 `.env.example`，真实值放本地环境变量）。

---

## 🎯 核心文档（推荐阅读）

| 文档 | 说明 | 更新日期 |
|------|------|----------|
| **[项目总览](./PROJECT_OVERVIEW.md)** | 项目概述、架构设计、核心功能模块 ⭐ | 2026-09-06 |
| **[功能模块指南](./MODULES_GUIDE.md)** | 详细的模块功能说明与使用指南 ⭐ | 2026-09-06 |
| **[快速参考手册](./QUICK_REFERENCE.md)** | 常用命令、API端点、故障排查 ⭐ | 2026-09-06 |
| **[文档索引](./INDEX.md)** | 按主题组织的文档快速定位 | 2026-08-17 |

---

## 目录结构（重构后）

```
docs/
├── README.md                ← 本文件：导览
├── INDEX.md                 ← 主题索引（按功能/模块）
│
├── adr/                     架构决策记录（2 个 ADR）
├── api/                     API 契约（OpenAPI / YAML，2 份）
├── architecture/            活跃架构文档（14 份，含 REPO_LAYOUT.md 仓库布局权威地图）
├── changelogs/              近 14 天变更日志（55 份，按日）
├── deploy/                  部署相关设计（1 份）
├── deployment/              部署/运维指南（14 份）
├── design/                  活跃设计/规格（16 份）
├── domains/                 领域注册表（1 份）
├── features/                功能模块设计（1 份）
├── i18n/                    国际化指南（1 份）
├── images/                  通用图片资源（3 份）
├── legal/                   法务合规（1 份）
├── migrations/              迁移配置说明（1 份）
├── model-iq/                模型质量评估设计（1 份）
├── model-quality/           模型质量监控（5 份）
├── modules/                 模块设计（4 份）
├── operations/              运维/操作手册（10 份）
├── ops/                     运维 Runbook（2 份）
├── partition/               分区表管理规范（11 份）
├── pricing/                 价格数据/导入脚本（CSV + 脚本）
├── runbooks/                紧急操作 Runbook（1 份）
├── screenshots/             截图归档（19 份）
├── security/                安全合规（3 份）
├── testing/                 测试计划与策略（4 份）
├── ui-verification/         UI 验证截图（10 份）
├── user-guide/              用户指南（1 份）
├── 会话优化v4/              会话优化 V4 设计（9 份，活跃版本）
│
└── archive/                 归档区（历史过程文档）
    ├── README.md            归档说明
    ├── INDEX.md             归档索引（按月统计）
    ├── 2026-06/             2026-06 归档（按子目录组织）
    ├── 2026-07/             2026-07 归档
    ├── 2026-08/             2026-08 归档
    └── process/             过程类归档（59 个子目录）
        ├── audits/          按月审计报告
        ├── fixes/           按月修复记录
        ├── incidents/       按月事故复盘
        ├── process/         按月过程文档（summary/report/handoff/phase）
        ├── specs/           按月设计规范
        ├── changelogs/      14 天以上的历史 changelogs
        ├── session-optimization-v2/  会话优化 V2（历史）
        ├── session-optimization-v3/  会话优化 V3（历史）
        ├── omnifree/        omnifree 实施方案（历史）
        ├── omni-ref{1,2,3}/  历史 omni-ref 资料
        ├── omniroute-ref/   omniroute 资料（历史）
        ├── revision-0811/   8 月 11 日密集审计集（39 份）
        ├── ...              其余 39 个优化轨 / 主题归档
```

## 重构成果（2026-08-17）

| 指标 | 重构前 | 重构后 | 变化 |
|---|---|---|---|
| docs/ 顶层子目录数 | 78 | 27 | -65% |
| docs/ 根目录散落文件 | 307 | 0 | -100% |
| 活跃主题文件总数 | 1,900+ | 335 | -82% |
| 归档文件总数 | 224 | 1,635 | +1,411 |
| 顶层时间戳散落文档 | 144 | 0 | -100% |
| 版本化重复目录（v2/v3/omni-ref*） | 12 | 0 | -100% |

## 主题检索

| 想找什么 | 看这里 |
|---|---|
| 架构决策、API 契约 | [architecture/](./architecture/) · [api/](./api/) · [adr/](./adr/) |
| 部署、迁移、配置 | [deployment/](./deployment/) · [deploy/](./deploy/) · [migrations/](./migrations/) |
| 运维、Runbook、故障排查 | [operations/](./operations/) · [ops/](./ops/) · [runbooks/](./runbooks/) · [troubleshooting/](./troubleshooting/) |
| 设计文档、设计方案 | [design/](./design/) · [modules/](./modules/) · [features/](./features/) |
| 价格、分区、模型 | [pricing/](./pricing/) · [partition/](./partition/) · [model-quality/](./model-quality/) · [model-iq/](./model-iq/) |
| 安全、合规 | [security/](./security/) · [legal/](./legal/) |
| 测试、验证 | [testing/](./testing/) · [ui-verification/](./ui-verification/) |
| 用户指南、国际化 | [user-guide/](./user-guide/) · [i18n/](./i18n/) |
| 近期变更日志 | [changelogs/](./changelogs/)（≤14 天） |
| 会话优化当前设计 | [会话优化v4/](./会话优化v4/) |
| 历史过程文档 | [archive/](./archive/) |

## 历史过程文档去哪里了

按用户确认（2026-08-17），所有过程类文档（audit / fix / bugfix / handoff / phase / deploy-report / summary / report）**全部归档**到 `docs/archive/process/<category>/<YYYY-MM>/`：

- `audits/` 按月分组审计报告
- `fixes/` 按月分组修复记录
- `incidents/` 按月分组事故复盘
- `process/` 按月分组其他过程文档（summary / report / handoff / phase / completion）
- `specs/` 按月分组历史设计规范

完整索引：[docs/archive/INDEX.md](./archive/INDEX.md) · 归档说明：[docs/archive/README.md](./archive/README.md)

## 恢复某条文档（按需）

```bash
# 从归档恢复单条文档
git checkout HEAD -- docs/archive/process/process/2026-07/FINAL_SUMMARY.md

# 重新归类（如需）
# 把 docs/archive/process/process/2026-07/FINAL_SUMMARY.md 移到 docs/operations/

# 全量回滚本次重构
git revert <commit-sha>
```

## 本次重构未做的事

- `.archive-backup-20260817-190606/`（备份目录，gitignored，保留用于回滚）
- `docs/screenshots/` 与 `docs/ui-verification/` 内的 PNG 文件（截图，按用途分类保留）
- 7+ 月以上的 active 设计文件（如有需要可重新归类）

---

**最后更新**：2026-08-17
**维护者**：LLM Gateway Team
**分支**：`chore/docs-archive-2026-08`（未推送）