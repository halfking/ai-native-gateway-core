# Changelog — R26 audit 收尾（只读复核 + 通道对账 + live binary 身份）

- 日期:2026-09-14
- 前置:R25(4ed97a213,703 通道补齐 + retry 注释漂移)、balance-floor guard 闭环(870fac658 / 77956aeb5 / 66a9f8e6a)、probe-recovery closeout(4ea8ab0af)

## 审计范围(本轮任务逐项对账)

| # | 任务 | 状态 | 证据 |
|---|------|------|------|
| 1 | 数据面单事务只读复核 | 通过(环境受限) | REPEATABLE READ 单事务,见下表 |
| 2 | 154/245 live binary 身份 + 24h 观察指标 | 部分(245 已核;154 未验证) | `/healthz` + image ID,见下表 |
| 3 | startup 存号 vs 通道清单成对核查 | 通过 | `git ls-tree origin/main` + `apply-db-revision-sequence.sh` diff,见下表 |
| 4 | MiniMax `/v1/token_plan/remains` 实测 | 维持留档 | 无真实订阅 key,无新进展 |
| 5 | git add -A 禁用 + 文件归属核对 | 已执行 | 见 commit 列表 |

## 1) 数据面单事务只读复核(PG 127.0.0.1:5432,REPEATABLE READ)

### 1.1 双账本(schema_migrations × gateway_db_revision_sequences)— 695..703 + 702 预留

| version | schema_migrations | sequences | drift(applied_at) | 含义 |
|---------|-------------------|-----------|-------------------|------|
| 695 | t (2026-09-12 03:10:46) | t (2026-09-12 04:12:19) | +01:01:33 | startup+upgrade 双通道,启动先跑 |
| 696 | t (2026-09-12 08:00:34) | t (2026-09-12 07:09:30) | -00:51:04 | startup+upgrade 双通道,upgrade 先跑 |
| 697 | t (2026-09-12 08:00:34) | t (2026-09-12 07:09:30) | -00:51:04 | startup+upgrade 双通道 |
| 698 | t (2026-09-12 08:24:26) | t (2026-09-12 08:24:26) | +0.067s | 同步 |
| 699 | t (2026-09-12 08:32:42) | t (2026-09-12 08:32:42) | +0.044s | 同步 |
| 700 | t (2026-09-12 08:00:34) | t (2026-09-12 08:44:22) | +43:48 | startup+upgrade 双通道 |
| 701 | t (2026-09-13 14:02:29) | **f** | — | 本机走 startup ensure 通道,upgrade 通道未跑 |
| 702 | f | f | — | 预留缺位正确(R25 验证已锁) |
| 703 | f | f | — | live binary c4da30d5 不含 R25 通道补齐;未升级到 4ed97a213 |

### 1.2 视图列数 + raw/fp 双暴露(canonical view)

- `request_logs_with_current_month` 列数 = **113**(确认)
- `raw_model_name` 暴露 = **1** ✓
- `system_fingerprint` 暴露 = **1** ✓
- 视图定义包含 `LEFT JOIN LATERAL (... UNION ALL request_logs ...)` 双源结构

### 1.3 raw_model_name / system_fingerprint 24h 填充率(本机 dev DB)

- 24h 行数 = 74962
- raw_model_name 非空 = 0 / 空 = 74962(**全 NULL**)
- system_fingerprint 非空 = 0 / 空 = 74962(**全 NULL**)

→ 这与 memory `request-logs-view-shape`(raw_model_name 实际填充 NULL)一致;本机 DB 无外部流量,5xx/正常请求都从本机 mock 路径走,未触发 fingerprint/raw_model_name 写入路径。**非 R26 修复目标,登记环境受限事实**。

## 2) 154/245 live binary 身份与 24h 观察指标

### 2.1 binary 身份

| 维度 | 245 live(8782) | 154 candidate(8781) |
|------|---------------|---------------------|
| container | `llm-gateway-local-8782` | **无 LISTEN** |
| image tag | `kx-llm-gateway-local:2.5.4.2101` | n/a |
| image ID(sha256) | `490f603aeeb8870b4a4c66b0c29c3c8404261f19765065d0c0eb2beccf46d22f` | n/a |
| `/healthz` | `{"git_sha":"c4da30d5","build_seq":2101,"build_date":"20260913","ready":true}` | n/a |
| git_sha 含义 | c4da30d5d = `docs(handoff): balance-floor guard 部署验证闭环`(2026-09-13 12:44 +0800) | n/a |
| 与 main HEAD 距离 | **落后 1 commit**(4ed97a213 R25 fix,2026-09-14 00:29) | n/a |
| 含义 | 245 容器不含 R25 的 apply-db-revision-sequence.sh 703 补齐 | n/a |

### 2.2 24h 观察指标(245 live,本机 dev DB)

- `model_probe_runs` 24h 行数 = **0**(本机 sweeper 24h 未触发探测)
- 当前 `model_probe_state` 分布:healthy=687, suspicious=100, recovering=10
- 凭据 floor 覆盖:77 条总凭据;balance_floor_usd 已设=1(50.00 USD),quota_floor 已设=3

### 2.3 154 24h 观察

- **未验证**(154 端口 8781 无 LISTEN 进程,无部署窗口;按用户约束"无部署窗口则如实记未验证")

## 3) startup 存号 vs 通道清单成对核查(origin/main)

### 3.1 startup 目录文件(`sql/migrations/startup/`,690..799 范围)

```
690_session_summaries_archived_ttl_index.sql
691_proxy_region_policy.{down,}.sql
692_session_summaries_user_intent_widen.{down,}.sql
693_provider_models_canonical_cleared_at.{down,}.sql
694_partition_ensure_timezone.{down,}.sql
695_request_logs_promote_final_success_self_heal.sql
696_request_logs_view_system_fingerprint.sql
697_request_logs_promote_system_fingerprint.sql
698_promote_hot_partition_timezone_pin.sql
699_supplier_errors_ensure_timezone_pin.{down,}.sql
700_request_logs_view_raw_model_name.sql
701_credential_balance_floor.sql
703_supplier_errors_promote_timezone_pin.{down,}.sql
```

### 3.2 upgrade 通道(`apply-db-revision-sequence.sh`)startup 条目(690..799)

```
693, 694, 695, 696, 697, 698, 699, 700, 701, 703
```

### 3.3 对账结果

| version | startup 目录 | upgrade 通道 | 解释 |
|---------|--------------|--------------|------|
| 690-692 | ✓ | ✗ | startup ensure-only(早期启动迁移,符合 R17 之前的策略) |
| 693 | ✓(+ .down) | ✓ | R25 前已补;双通道 |
| 694 | ✓(+ .down) | ✓ | 双通道 |
| 695 | ✓ | ✓ | 双通道 |
| 696 | ✓ | ✓ | 双通道 |
| 697 | ✓ | ✓ | 双通道 |
| 698 | ✓ | ✓ | 双通道 |
| 699 | ✓(+ .down) | ✓ | 双通道 |
| 700 | ✓ | ✓ | 双通道 |
| 701 | ✓ | ✓ | 双通道 |
| **702** | **✗** | **✗** | **预留缺位正确(advisory-lock 占用)** ✓ |
| 703 | ✓(+ .down) | ✓ | R25 fix 已补 |
| 704-799 | ✗ | ✗ | 无撞号 ✓ |

→ **702 预留缺位 / 704+ 撞号 检查全部通过**;`TestMigration700ViewRawModelName` PASS(已加 703 条目与非降序校验)。

## 4) MiniMax `/v1/token_plan/remains` 实测

- **环境受限维持留档**:`.env.local` / `bin/current/env` / `~/kaixuan/llm-gateway-go/bin/current/env` 均无真实 MiniMax subscription key。
- fail-open 分支沿用(701 migration + 1a89c32fe 后已稳定),无新增修复需求。
- 真实响应字段解析、currency pass A/B/C 的 live 闭环均留待 R27+ 配合运营报障窗口。

## 测试命令与结果

```
go test ./sql/migrations/startup/ -run TestMigration700 -count=1 -v
# PASS: TestMigration700ViewRawModelName (0.00s)
# ok  github.com/kaixuan/llm-gateway-go/sql/migrations/startup  0.605s

psql -h 127.0.0.1 -p 5432 -U llm_gateway -d llm_gateway --single-transaction -v ON_ERROR_STOP=1 \
  < r26-readonly-review.sql
# 9 个 section 全部按预期返回,ROLLBACK

git ls-tree -r --name-only origin/main -- sql/migrations/startup/ | wc -l
# 与 origin/main 通道条目一致,无 704+ 撞号
```

## 结论与遗留

- **R26 无新增代码修复**(基线 R25 已稳固,本轮仅做只读复核 + 对账)
- **遗留风险**:
  1. live 245 容器落后 main 1 commit(R25 fix)——下次重启/重建才会带 703 通道补齐到 upgrade DB
  2. raw_model_name / system_fingerprint 在本机 DB 24h 写入路径未触发——属环境受限(无外部流量),非代码缺陷
  3. 154 24h 观察未做——无部署窗口,留待 R27
  4. MiniMax `/v1/token_plan/remains` 真实响应——无订阅 key,继续 fail-open

## 下一轮(R27)提示词

> 本轮(R26)已闭:R25 audit 只读复核、702 预留/704+ 撞号核查、245 live binary 身份(c4da30d5,落后 main 1 commit)、MiniMax 实测留档。
> 下一轮(R27):
> (1) 若 154 部署窗口或运营报障开启,执行 154 重建并核对 image ID + `/healthz`,记录 24h ProbeNow failed / 429 / 403 / recovering / probe_backoff 五指标;否则维持未验证标注。
> (2) live 245 容器若在此期间未升级到 4ed97a213+,R27 必须先 rebuild 245 并复核 upgrade 通道对 703 的承载,记录 schema_migrations 行号 + sequences 行号两侧对齐情况。
> (3) MiniMax `/v1/token_plan/remains` 仍等真实订阅 key;如有运营提供,实测响应字段与 parse;无则维持留档与 fail-open。
> (4) 复审 R26 changelog 与 R25 fix 的边界:703 upgrade 通道补齐在升级型 DB(共享 252 生产 PG)首次跑时的序列连贯性,无 trailing-sequence guard 退化。
> (5) 严禁 git add -A;提交前按文件核对归属;验证如实区分通过/环境受限/未验证。
