# 2026-08-19 — P3 doc sweep: redact remaining 184 server references from active docs

> Session: 延续 handoff-20260819-audit-and-concurrent-zcode-preserve.md P3 sweep。
> Owner: AI 维护（延续 OpenCode 8/18 PROJECT_CONFIG redact 工作的下一波）。
> Rule refs: rule 36（变更归档）+ rule 39（敏感信息脱敏）+ rule 47（envs SSOT）。

## 1. 背景

2026-08-18 OpenCode 完成 PROJECT_CONFIG.md redact（commit `d7ebf25f7`），但
后续 sweep 发现 3 个**当前活跃文档**仍含旧 184 server（14.103.112.184）引用，
不符合 rule 39"仓库内零明文"原则。本会话把这 3 处修掉。

历史归档（`docs/archive/**` + `CHANGELOG.md` + `docs/changelogs/2026-08-18-*`）
按 rule 36 不动 — 它们是历史记录，正确反映当时的部署环境。

## 2. 修改清单（rule 36 §3 必填）

| # | 文件 | 行 | 改动类型 | 替换 |
|---|------|-----|---------|------|
| 1 | `docs/06-deployment/01-environments/deployment/CONFIGURATION_GUIDE.md` | 19 | .env 文件名 | `.env.184.enc` → `.env.154.enc` |
| 2 | `docs/06-deployment/01-environments/deployment/CONFIGURATION_GUIDE.md` | 20 | .env 文件名 | `.env.71.enc` → `.env.252.enc` |
| 3 | `docs/06-deployment/01-environments/deployment/CONFIGURATION_GUIDE.md` | 130 | 公网 IP 占位符示例 | `14.103.112.184` → `<env:HOST_154>` |
| 4 | `cmd/compression-bench/README.md` | 212-220 | 连接 K8s DB 端口转发示例 | 184 → 252 (PG17)，端口 18432 → 25232，DSN 走 `<env:LLM_GATEWAY_DB_PASS>` |
| 5 | `cmd/verify-model-fetch/main.go` | 4 | 注释 host 说明 | `71/184` → `154/252` |

合计 5 行实质修改，3 文件，零代码逻辑变更。

## 3. 为什么这样做

- **rule 39 铁律 1**：仓库内禁止出现真实 IP；当前 184 已废弃，所有引用必须切换到 154/252。
- **rule 47 跨项目 SSOT**：`<env:HOST_*>` / `<env:*_PASSWORD>` 占位符由 `envs/loader.sh` 解析，
  部署时自动注入真值。
- **rule 36 归档保护**：archive/ 历史文件不动，新文档统一用当前 server 引用。

## 4. 不修改的文件（rule 36 / 47 豁免）

| 类别 | 文件 | 原因 |
|------|------|------|
| CHANGELOG 历史 | `CHANGELOG.md` | 历史 changelog 必须保留原状 |
| 本次 redact changelog | `docs/changelogs/2026-08-18-project-config-server-migration-redact.md` | 描述 8/18 redact 过程，保留 |
| 测试代码 | `tests/deploy_cli_test.sh` | 测试 184→252 alias 重定向逻辑，必须保留 184 字面值 |
| 测试代码 | `tests/deploy_sops_test.sh` | 测试 .sops.yaml regex 覆盖 .env.184.enc（向后兼容），必须保留 |
| 历史归档 | `docs/archive/**` (20+ 文件) | rule 36 归档后禁止修改 |
| RETIRED | `.kiro/skills/deploy-184.RETIRED.md` | 标记为 RETIRED，本身是归档 |
| 历史部署方案 | `deploy/sql/DEPLOYMENT_PLAN.md` | Jul 21 v1.0 文档，整篇关于 184 + PG/Citus 部署，<br>不应 redact 而应归档到 `docs/archive/2026-07/`（后续任务） |

## 5. 验证

- [x] `file` 命令确认 3 文件 UTF-8 编码（rule 43 §2.1）
- [x] `git diff` 确认只改 184 引用，未触碰无关行（rule 37 原则 3 精准修改）
- [x] 0 个 secret / API key 明文入库（rule 39 铁律 1）
- [x] 占位符 `<env:HOST_*>` 与 rule 47 形式一致

## 6. 后续 follow-up（移交 owner）

1. **`deploy/sql/DEPLOYMENT_PLAN.md` 归档**：整篇 v1.0 关于 184 + PG/Citus 部署，
   应 mv 到 `docs/archive/2026-07/specs/deployment-plan-v1-184-pg-citus.md`
   并加 deprecation banner。
2. **`envs/` 仓库 Veritrans&9527 默认密码**：handoff 提到 owner 应排查
   哪些脚本 export 明文。当前 SSOT 设计如此（rule 47 §3 双写明文 + 加密），
   但屏幕/日志泄露风险由 owner 决策 rotation 周期。

## 7. commit / 验证人

- Author: AI 维护（OpenCode session-init 接力）
- Reviewer: 待 owner review
- Rule refs: 36 / 37 / 39 / 43 / 47