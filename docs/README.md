# docs/ — LLM Gateway 文档

> 重构时间：2026-08-17 · 重构分支：`chore/docs-archive-2026-08` · 重构策略：保守归档 + 主题重组  
> 最后更新：2026-09-17

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

## 🧹 2026-09-14 文档整理

- `MOCK_PROVIDER_SYSTEM_TEST_REPORT_20260907.md`（仓库根目录，2026-09-07 一次性全量测试报告，7/7 场景终局运行）→ [archive/2026-09/](./archive/2026-09/MOCK_PROVIDER_SYSTEM_TEST_REPORT_20260907.md)。
- `docs/merge-audit-2026-08-26-local-precedence.md`（一次性合并审计记录，仓内无引用，后续合并审计由 [audit/](./audit/) 承接）→ [archive/2026-08/](./archive/2026-08/merge-audit-2026-08-26-local-precedence.md)。
- 引用同步：[deployment/rate-limit-and-node-probe-semantics.md](./deployment/rate-limit-and-node-probe-semantics.md) 中指向测试报告的路径已更新；归档索引 [archive/INDEX.md](./archive/INDEX.md) 已补录上述两条。

---

## 🎯 核心文档（推荐阅读）

| 文档 | 说明 | 更新日期 |
|------|------|----------|
| **[项目总览](./PROJECT_OVERVIEW.md)** | 项目概述、架构设计、核心功能模块 ⭐ | 2026-09-06 |
| **[功能模块指南](./MODULES_GUIDE.md)** | 详细的模块功能说明与使用指南 ⭐ | 2026-09-06 |
| **[快速参考手册](./QUICK_REFERENCE.md)** | 常用命令、API端点、故障排查 ⭐ | 2026-09-06 |
| **[文档索引](./archive/2026-09/INDEX.md)** | 按主题组织的文档快速定位（2026-09-08 归档） | 2026-08-17 |

---

## 目录结构（现行）

> 2026-08-17 重构时的 27 目录结构已进一步演化：2026-09-07 起重组为 `01-requirements`～`07-reporting` 编号结构（详见 [archive/2026-09/INDEX.md](./archive/2026-09/INDEX.md)），原 `api/`、`domains/`、`features/`、`i18n/`、`legal/`、`model-iq/`、`model-quality/`、`modules/`、`ops/`、`partition/`、`pricing/`、`testing/`、`user-guide/` 等 13 个目录已并入编号结构，不再存在。当前 docs/ 顶层共约 60 个条目，主体如下：

```
docs/
├── README.md                ← 本文件：导览
│
├── 01-requirements/         需求（11 篇）
├── 02-resources/            资源（研究/合规等，28 篇；含 research/pricing、compliance/legal）
├── 03-design/               设计（151 篇；architecture/API 契约、feature-design/model-iq·model-quality·i18n、data-design/partition 等）
├── 04-implementation/       实现（38 篇；含 deliverables/user-guide）
├── 05-testing/              测试（18 篇）
├── 06-deployment/           部署/运维（50 篇）
├── 07-reporting/            报告（13 篇）
│
├── adr/ architecture/ design/ deploy/ deployment/ migrations/ operations/ runbooks/
│   troubleshooting/ security/ changelogs/ audit/ fixes/ screenshots/ images/
│   （2026-08 重构保留的活跃目录 + 9 月起演进新增的 agents/ benchmark/ database/ ml/
│     monitoring/ perf/ prompts/ 等专题目录）
│
└── archive/                 归档区（历史过程文档；README.md / INDEX.md / 2026-06～2026-09 / process/）
```

> 2026-08-17 时点的完整目录树与各目录文件数见 git 历史（本文件 v2026-09-14 前版本）。

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
| 架构决策、API 契约 | [architecture/](./architecture/) · [03-design/01-architecture/architecture/API.md](./03-design/01-architecture/architecture/API.md) · [adr/](./adr/) |
| 部署、迁移、配置 | [deployment/](./deployment/) · [deploy/](./deploy/) · [migrations/](./migrations/) · [06-deployment/](./06-deployment/) |
| 运维、Runbook、故障排查 | [operations/](./operations/) · [runbooks/](./runbooks/) · [troubleshooting/](./troubleshooting/) |
| 设计文档、设计方案 | [design/](./design/) · [03-design/](./03-design/)（含原 modules/features 专题） |
| 价格、分区、模型 | [02-resources/research/pricing/](./02-resources/research/pricing/) · [03-design/04-data-design/partition/](./03-design/04-data-design/partition/) · [03-design/02-feature-design/model-quality/](./03-design/02-feature-design/model-quality/) · [03-design/02-feature-design/model-iq/](./03-design/02-feature-design/model-iq/) |
| 安全、合规 | [security/](./security/) · [02-resources/compliance/legal/](./02-resources/compliance/legal/) |
| 测试、验证 | [05-testing/](./05-testing/) · [ui-verification/](./ui-verification/) |
| 用户指南、国际化 | [04-implementation/deliverables/user-guide/](./04-implementation/deliverables/user-guide/) · [03-design/02-feature-design/i18n/](./03-design/02-feature-design/i18n/) |
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

**最后更新**：2026-09-17（目录树/主题检索对齐编号结构实况；原 13 个已并入编号结构的目录条目移除）
**维护者**：LLM Gateway Team
## 归档（2026-09-08 整理）

根目录 19 篇一次性交付/测试/审计报告与 docs 根 10 篇过程文档已归档至 `archive/2026-{07,08,09}/`；`INDEX.md` 已归档至 [`archive/2026-09/INDEX.md`](./archive/2026-09/INDEX.md)；设计类文档（proxy-management-design、perf 基线、FEATURE-REQ×2）移至 `03-design/`。只归档不删除。
