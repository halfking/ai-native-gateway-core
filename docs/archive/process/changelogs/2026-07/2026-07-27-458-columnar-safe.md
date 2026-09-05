# 2026-07-27 — migration 458 columnar-safe + remote psql error capture

## TL;DR

Migration `458_request_logs_canonical_model` 在 245 部署（build_seq 1403）首次执行被拦截，
根因有两层：

1. SQL 在 Citus columnar 历史分区上跑 `UPDATE`（不支持）。
2. 部署脚本在调用方机器上读远端错误日志，真因被二次错误覆盖。

修复后 245 build_seq 1404 完成迁移，`request_logs_hot` 已回填 6846 行 `canonical_model`，
两个 partial index 已创建；`/api/system/background-tasks=401`（非 503）证明 DB 就绪。

## 触发链

1. 实时请求流按模型筛选要求加 `canonical_model` 列；migration 458 添加列 + 回填 + 索引。
2. 245 第一次部署（build_seq 1403）时 `apply_pending_migrations` 走到 458，psql 输出 `UPDATE 6575`
   之后被部署脚本判定失败。脚本同时报告 `tail: /tmp/_mig_err_458.log: No such file or directory`。
3. 排查发现 `tail` 是在调用方机器上执行的，但 psql 错误日志写在 245 远端 `/tmp`，
   真因被掩盖。同时回填脚本对分区父表 `request_logs` 整体 `UPDATE`，但历史月分区有
   Citus columnar 存储，Citus 拒绝 `ColumnarScan` 上的 UPDATE。

## 根因

- `458_request_logs_canonical_model.sql` 第 4 步：

  ```sql
  UPDATE request_logs rl
  SET canonical_model = mc.canonical_name
  FROM models_canonical mc
  WHERE rl.canonical_id = mc.id
    AND rl.canonical_model IS NULL;
  ```

  在列存历史分区上失败。请求流/历史分析只关心热表与最近一个分区，但 SQL 写法触发了
  分区父表的全表扫描。

- `scripts/deploy-lib/db-changelog.sh` 错误捕获路径：

  ```bash
  if grep -qiE 'already exists|duplicate key|relation .* already exists' "/tmp/_mig_err_${ver}.log" 2>/dev/null; then
    ...
  else
    tail -15 "/tmp/_mig_err_${ver}.log" >&2 || true
  fi
  ```

  远端 245 无 `/tmp/_mig_err_<ver>.log` 的本机视图（事实上 245 上是有的，但 `ssh` 调用和
  远端 `cat` 之间的边界有歧义，且失败时直接 `tail` 远端路径导致 245 上的 252 容器内的
  `tail` 命令在 ssh 双层包裹下被本地 shell 解释），真因被 `tail: ... No such file or directory`
  覆盖。

## 修复

### 1. Migration 458（sql/migrations/startup/458_request_logs_canonical_model.sql）

- 保留 `ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS canonical_model`。
- 保留 `ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS canonical_model`。
- 保留 `UPDATE request_logs_hot ... WHERE canonical_model IS NULL`（heap，OK）。
- **删除**对分区父表 `request_logs` 的整体 `UPDATE`，并在注释里写明：
  > Historical request_logs partitions may use Citus columnar storage, which
  > does not support UPDATE. Historical rows therefore remain NULL and are
  > resolved through the existing canonical_id join when queried.
- 保留两个 partial index（hot + parent）。

### 2. Migration 458 down（sql/migrations/startup/458_request_logs_canonical_model.down.sql）

- 删除不属于本迁移的 `agent_name / agent_type / client_protocol / virtual_client_id` 回滚。
  这些列是 migration 443 添加的，down 458 不应承担，否则误删后无法 `down 443` 一次恢复。
- 仅 `DROP COLUMN canonical_model` + `DROP INDEX` 本迁移创建的两个。

### 3. 部署脚本（scripts/deploy-lib/db-changelog.sh）

- 失败分支改为通过 `ssh ... cat` 把远端错误拉回本机，再 `<<<` 喂给 `grep`。
- 每次成功或失败后 `rm -f /tmp/_mig_err_<ver>.log`，避免远端 `/tmp` 累积。

## 验证

| 检查项 | 结果 |
|---|---|
| `bash -n scripts/deploy-lib/db-changelog.sh scripts/deploy-245.sh` | OK |
| `git diff --check` | OK |
| 245 部署 (build_seq 1404) | `[db] ✓ 迁移完成 (1 新应用 / 1 检查)` |
| `request_logs_hot WHERE canonical_model IS NOT NULL` | 6846（持续增长） |
| 两个 partial index | `idx_request_logs_hot_canonical_model_ts` / `idx_request_logs_canonical_model_ts` 已建 |
| `/api/system/version` | 200 |
| `/api/system/background-tasks` | 401（非 503，DB 就绪） |
| Go 单元测试 | 5 个包 OK |
| Go build + vet | OK |

## 后续

- 154 生产部署（build_seq 1405+）待 245 验证后执行。
- 实时请求流 `/api/admin/live-stream` 与详情 `/api/logs/{id}` 的客户端筛选/标准模型
  字段由 459 修复（commit `a00a224a1`）+ 458 字段组成。
- 文档：`docs/changelogs/2026-07-27-458-columnar-safe.md`、`CHANGELOG.md` Unreleased / Fixed 段。

## 审计发现与跟进（2026-07-27 第二轮）

### 审计范围

1. `2af4bc2e8 chore: bump version to 1410 and update changelogs` 的全部变更
2. `a00a224a1 fix(api): /api/logs/{id} 500 query failed after 443+458 view drift` 的事实复查
3. 与之配套的数据库脚本、CHANGELOG、归档文档
4. 仓库内 .gitignore / 敏感文件 / 部署链可观测性

### 修复（已落地）

| # | 项 | 文件 | 说明 |
|---|----|------|------|
| 1 | 远端错误日志为空时给出诊断 | `scripts/deploy-lib/db-changelog.sh:132-149` | `migration_error` 为空且 grep 非 idempotent 时改为提示"远端 /tmp 日志为空/不可读"+ 远端 `ls -la` 排查信息；之前只输出空 `tail -15`，无任何线索 |
| 2 | 文档归档 + CHANGELOG 补登 | `CHANGELOG.md` Unreleased / Fixed、`docs/changelogs/2026-07-27-458-columnar-safe.md` | rule 36 留痕 |

### 遗留项（不破坏任务边界，单独 PR 处理）

| # | 项 | 建议 | 阻塞 |
|---|----|------|------|
| 3 | `configs/env-local.sh` 入仓（PG_PASS 等本地开发凭据与容器名）| 加入 `.gitignore` + `git rm --cached`，保留本地文件 | 否（仓库内非生产凭据，但仍是规则 39 风险）|
| 4 | `2af4bc2e8` 是单 commit 多逻辑点（fix + docs + chore + local config） | 下次部署以 `fix()` + `chore()` + `docs()` 拆 commit | 否（已推，不可 force-push） |
| 5 | 458 up/down SQL 头部未按 rule 38 §4 标记 `Status:` / `Idempotent:` / `Changelog:` 字段 | 历史问题，不动已应用文件；新建 migration 时补齐 | 否 |
| 6 | 154 生产部署 458 | 245 验证通过后人工触发 | 否（任务边界）|
| 7 | `db-changelog.sh` line 89 `head -15 \| grep` SUPERSEDED 检查 | 无需变更 | — |
| 8 | `deploy-seamless.sh` 自动 commit（`2af4bc2e8` 由它产生）| 加 GPG 签名要求 + 拆 commit hook | 否 |
