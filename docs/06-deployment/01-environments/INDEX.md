# 环境文档索引

> 最后核对：2026-09-05

## 规范环境总览

- [环境总览](README.md) — 环境命名、目标映射、端口、健康检查、支持矩阵和证据等级。
- [客户安装总览](customer-install/README.md) — 客户 host/Docker 安装路径与限制。
- [本机部署](deployment/LOCAL-HOST-DEPLOY.md) — local host 开发机流程。
- [数据库环境隔离](deployment/DATABASE-ENVIRONMENT-SEPARATION.md) — local/test/生产数据边界。

## 按环境查找

| 环境 | 入口 |
| --- | --- |
| `local` / `dev` | [本机部署](deployment/LOCAL-HOST-DEPLOY.md)、[local-8782 环境状态快照](local-8782-env-state-20260905.md)、仓库 `docker-compose.dev-research.yml` |
| `test` / `pms-test` / 252 | [部署总入口](../README.md)、K8s 测试 manifest |
| `staging` / 245 / 154 | [154 生产放行清单](154-production-release-checklist-20260905.md)（含 245 预发晋升证据 §6）、[Session V2 staging 验证最终报告](../../staging-validation-final-report.md)、[部署切换审计](../../audit/2026-08-29-deployment-contract-audit.md) |
| `production` | [部署总入口](../README.md)；真实发布必须走授权 pipeline |
| `customer-host` / `customer-docker` | [客户安装总览](customer-install/README.md)及其平台子文档 |
| `k8s-test` | [deploy 总导航](../../../deploy/README.md)和 `deploy/k8s/` manifest |

## 状态说明

- `CURRENT`：现役入口，但仍需按目标环境取得验证证据。
- `PARTIAL`：有代码/脚本或局部验证，不能作为完整发布承诺。
- `LEGACY`：历史路径，仅用于兼容或迁移。
- `NOT_RELEASE_READY`：存在明确缺口，不能作为生产/客户发布入口。
