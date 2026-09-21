# 154 生产放行清单（2026-09-05）

> 状态：**未执行**（本文档仅为放行前检查单与验证合同，154 生产不自动晋升）
> 候选版本：`2.5.0.1945`（git_sha=`f6ea47da`，main 提交 `f6ea47da6`）
> 245 预发证据：见 §6；晋升顺序合同 `local -> 245 -> 154`（llm-gateway-deploy skill）
> 关联：`docs/audit/2026-09-05-stale-binary-deploy.md`、`scripts/deploy-seamless.sh`

## 1. 晋升前置条件（逐项勾选后方可执行）

- [ ] **预发观察期**：245 自 2026-09-05 16:20 (+08) 起运行 1945 无 P0/P1 告警
      （观察时长由放行人确定；建议 ≥24h，覆盖一个业务高峰）。
- [ ] **main 未漂移**：执行时 `git rev-parse origin/main` 包含 `f6ea47da6`；若 main
      已前进，须确认增量提交已先在 245 完成一轮完整晋升（不得跳过预发直接晋升新提交）。
- [ ] **多 clone 纪律**：在与 origin/main `pull --ff-only` 同步的 clone 中构建
      （2026-09-05 陈旧二进制事故 §5 条目 4）。
- [ ] **凭据注入**：`env-injector inject aliyun-gateway-154`（禁止打印/落盘 secret 值）。
- [ ] **并发检查**：本机无进行中的 `deploy-seamless`/`deploy-154`；252/154 无残留部署锁
      （冲突时等待，绝不覆盖）。
- [ ] **禁用路径**：不使用 SSHPASS、legacy `deploy.sh`/`deploy-to-154.sh`、Docker Hub 镜像。
- [ ] **共享 PG 认知**：154 与 245 共享同一 PG——154 部署的切换前迁移应为
      **0 个 pending**（658 已由 245 于 2026-09-05 应用）；若出现 pending，先核对
      迁移来源再继续。

## 2. 执行流程

```bash
env-injector inject aliyun-gateway-154
bash scripts/deploy-154.sh --dry-run     # 非变更预检
bash scripts/deploy-154.sh               # 委托 deploy-seamless.sh deploy 154
```

## 3. 验证合同（全部通过才算晋升成功）

| # | 检查 | 通过标准 |
|---|---|---|
| 1 | bundle 完整性 | `[6/9] verify bundle (sha256)` 通过（含 manifest 非空 + 二进制条目前置校验，A1 加固） |
| 2 | 切换前迁移 | `0 新应用`（若 >0 需说明来源）；DB 就绪 |
| 3 | 金丝雀预热 | `/healthz`、`/readyz` 200；`/version` 的 git_sha/build_seq 与 staged identity 一致 |
| 4 | 切流 | nginx 原子切流成功（失败时不得 restart，旧 release 继续服务——A1 加固语义） |
| 5 | 凭据解密门（step 9.2） | `creds=N failed=0`；key_mask_error 全失败即自动回滚，partial 仅 WARN |
| 6 | **部署后身份核验** | `grep -a 'vcs.revision=' current/gateway` == 该 release `version.json` git_sha == 本次部署 HEAD；`run|releases` 下无 mtime 早于部署时间的 gateway.build 复用 |
| 7 | 鉴权证据 | `GET /v1/models` 无凭据 → 401；带数据面 key → 200 |
| 8 | 模型证据 | `/v1/models` 模型数与 245 同级（245 实测 685）；admin 登录 → background-tasks 200 |
| 9 | 日志留证 | 部署日志含 `VERIFY_PASS=1` 语义（seamless 目标=154），路径登记到本文档 §6 |

## 4. 回滚预案

1. **二进制回滚**：`bash scripts/deploy-seamless.sh rollback 154`
   ——仅允许回滚到 verified release；回滚决策点：step 9.2 全失败（自动）、
   身份核验失败（手动）、切流后业务指标劣化（手动）。
2. **边界**：二进制回滚**不回滚数据库 schema**。本轮候选含 migration 658
   （auto_route structured features，纯增量列/视图，已随 245 应用）；
   如需schema 级回退须单独评审 `658_*.down.sql` 并停机窗口执行。
3. **链路语义**：154/245 共享 PG 与 Redis；二进制回滚不影响凭据密钥
   （`LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 未变，旧二进制可解密现有凭据）。
4. **切流失败即停**：符号链接切换失败时部署已中止且不 restart（A1 加固），
   此时旧 release 仍在服务，无需回滚动作，仅需排障。

## 5. 放行记录（执行时填写）

| 项 | 值 |
|---|---|
| 放行人 / 执行会话 | |
| 执行时间 | |
| 部署 build_seq / git_sha | |
| 身份核验结果 | |
| 9.2 解密门结果 | |
| 日志路径 | |

## 6. 245 预发证据快照（2026-09-05）

- 晋升：`2.5.0.1945`（sha=`f6ea47da`），deploy-seamless 全 9 步通过，`DEPLOY_245_RC=0`。
- 身份：`current → releases/1945-f6ea47da`；bundle version.json git_sha=`f6ea47da`；
  二进制内嵌 `vcs.revision=f6ea47da6a886e17a96ff3f8e51026a8afcde1a8`（与推送 HEAD 一致）。
- 健康：`/healthz` ok、`/readyz` 200、DB 就绪 1s、authenticated background-tasks 200。
- 凭据解密门：`providers creds=15 failed=0`。
- 模型/鉴权：`/v1/models` 带 key 200（685 模型）、无凭据 401。
- 迁移：658（可重放修复后）在共享 PG 成功应用 `1 新应用 / 1 检查`。
- 事故留证：首次晋升尝试因 658 迁移缺陷 fail-closed（未切符号链接，245 全程服务旧
  release），修复提交 `f6ea47da6` 后重试成功——「构建/迁移失败不得静默」合同双实证。
