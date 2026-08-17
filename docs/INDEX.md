# LLM Gateway 文档索引

## 快速开始

### 审批通知系统 🔔
- **[5分钟快速上手](./approval-notification-quickstart.md)** - 最快上手审批通知功能
- **[完整配置指南](./approval-notification-setup.md)** - 生产环境详细配置

## 功能文档

### 会话审计与审批
- [审批通知系统配置](./approval-notification-setup.md) - 多渠道 IM 通知（飞书/钉钉/企业微信）
- [会话审计 24 小时报告](./2026-06-15-24h-audit-report.md)
- [审计报告 (6-20)](./2026-06-20-audit-report.md)

### 自动路由
- [自动路由模式设计](./2026-06-15-auto-route-mode-design.md)
- [自动路由运维指南](./2026-06-15-auto-route-mode-ops.md)

### 调优与反馈
- [调优反馈部署](./2026-06-15-tuning-feedback-deploy.md)

### 浏览器验证
- [V2 浏览器验证](./2026-06-15-v2-browser-verification.md)

## 平台规划
- [MaaS 平台计划](./2026-06-16-maas-platform-plan.md)

## 故障排查
- [事件总结 (6-16)](./2026-06-16-incident-wrapup.md)
- [Claude Opus 4.8 零输出根因分析](./2026-06-20-claude-opus-4-8-zero-output-root-cause.md)

## 部署报告
- [完整部署报告 (6-20)](./2026-06-20-complete-deployment-report.md)
- [压缩头部支持部署](./2026-06-30-compression-headroom-deploy.md)
- [恢复处理器集成](./2026-07-02-approval-resumehandler-integrate.md)
- [Headroom 集成文档](./compression-headroom-integration.md)

## API 参考
- [Session Compression API](./session-compression-api.md)

## 贡献者
查看 Git 提交记录了解贡献者信息。

## 归档（2026-08-17）

> 仓库根目录历史累积的 87 个过期过程文档（bugfix / audit / fix / phase / handoff / deploy-report / summary / report）已迁移至 `docs/archive/`。文件内容保持不变，可通过 `git revert` 100% 还原。详见 [docs/archive/README.md](./archive/README.md)。

### 活跃文档与目录速查

| 类别 | 入口 |
|---|---|
| API 参考 | [./API.md](./API.md) · [./api/](./api/) |
| 架构与 ADR | [./architecture/](./architecture/) · [./adr/](./adr/) |
| 部署与迁移 | [./DEPLOYMENT.md](./DEPLOYMENT.md) · [./MIGRATION_OPERATION_GUIDE.md](./MIGRATION_OPERATION_GUIDE.md) · [./migrations/](./migrations/) |
| 运维 | [./OPERATIONS.md](./OPERATIONS.md) · [./db-changelog.md](./db-changelog.md) · [./runbooks/](./runbooks/) |
| 变更日志 | [./CHANGELOG.md](../CHANGELOG.md) · [./changelogs/](./changelogs/) |
| 事故与复盘 | [./incidents/](./incidents/) · [./lessons-learned/](./lessons-learned/) |
| 重构规划 | [./refactor-plans/](./refactor-plans/) |
| 待办索引 | [./TODO_INDEX.md](./TODO_INDEX.md) |

### 已归档（按月份）

- [2026-07/](./archive/2026-07/) — 50 个过程文档（7-19~7-31 mtime）
- [2026-08/](./archive/2026-08/) — 37 个过程文档（8-02~8-13 mtime）

---

**最后更新**: 2026-08-17
**维护者**: LLM Gateway Team
