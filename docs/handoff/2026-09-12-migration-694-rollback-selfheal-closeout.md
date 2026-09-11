# Handoff: 迁移 694 收口 — 回滚幂等修复 + 689 自愈收敛（2026-09-12）

## 1. 任务概要（Mission Summary）

承接 R13/R14 审计轮：迁移 694（分区 ensure 函数 Asia/Shanghai 时区钉扎）已由
59c5dda12/2895540c2 落地 canonical、installer 五点接线与升级脚本通道，但审计
确认两个阻断性缺口仍未闭合。本轮以单一逻辑单元修复并推送（055cecc78）。

## 2. 结论 / 根因（Findings & Root Cause）

| 级别 | 发现 | 根因 | 处置 |
|---|---|---|---|
| P1 | 694 down 迁移不可执行：13 个恢复定义中 11 个是裸 `CREATE FUNCTION` | up 已安装同签名函数，回滚在第一条语句即报 42710（function already exists），事务中止，`schema_migrations` 的 694 行永远删不掉 | 两份 down（canonical + installer 副本）全部改为 `CREATE OR REPLACE FUNCTION`，保持字节一致 |
| P1 | 689 启动自愈每次开机覆盖 694 的 candidate 函数 | `ensureCandidateFailureLogsHeapPartitionsSQL`（db.go）是 689 的 Go 镜像、每次 `db.Open` 无条件重放；其函数体仍是 pre-694 形态——月份推导写在 `DECLARE` 初始化器里（先于函数体求值），函数内 `SET LOCAL` 挽救不了它，也没有任何时区钉扎。UTC 会话下候选失败日志的 promote 链会在每月 1 日 00:00–08:00+08 把行路由到不存在的 +08 当月分区 | 镜像体收敛到 694 最终形态：钉扎作为 BEGIN 后第一条语句，月份推导移到其后；heap 语义与 `RETURNS text` 签名不变 |

已核实无需重复实现的项（远端/先前提交已完成）：
- 694 up 本身 13 个函数的钉扎顺序正确（59c5dda12）；
- installer 五点接线、`apply-db-revision-sequence.sh` 升级通道（2895540c2）；
- 695 重编号及其 installer 接线（本地另一会话 + 远端并行完成，已随本轮一起推送）。

## 3. 改动文件与关键行为（Changes）

提交 `055cecc78`，5 文件 +295/−25，pre-commit 门禁 PASS=4 FAIL=0：

| 文件 | 行为 |
|---|---|
| `sql/migrations/startup/694_partition_ensure_timezone.down.sql` | 11 处裸 `CREATE FUNCTION public.ensure_*` → `CREATE OR REPLACE FUNCTION`；ledger DELETE 与事务边界不变；down 有意不含 `SET LOCAL`（恢复 pre-694 语义） |
| `installer/cmd/llm-gw-installer/embeddata/startup/694_...down.sql` | 与 canonical 字节一致（`cmp` 前后校验）；installer 不执行 down，仅为镜像可审计 |
| `db/db.go` | `ensureCandidateFailureLogsHeapPartitionsSQL`：candidate 函数体收敛为 694 形态（`SET LOCAL TIME ZONE 'Asia/Shanghai';` 为首条语句，月份推导移至其后），附根因注释；防止每次启动覆盖 694 |
| `sql/migrations/startup/migration_694_test.go`（新增） | 三组合同：installer 镜像字节一致（up/down）；up 13 函数 × 13 钉扎、钉扎先于 `date_trunc`、无 DECLARE 初始化器、candidate 保持 heap/`RETURNS text`、ledger upsert；down 13 处 OR REPLACE、禁止裸 CREATE、ledger DELETE、无钉扎 |
| `db/db_migration_694_test.go`（新增） | 源契约：689 自愈镜像必须含钉扎且先于 `date_trunc`、禁止 DECLARE 初始化器回潮、heap/签名契约；防止镜像未来漂移回 pre-694 体 |

## 4. 测试命令与结果（Verification）

```bash
gofmt -l <changed .go files>                      # 无输出（格式干净）
go test ./sql/migrations/startup -run TestMigration694 -count=1   # 3 个测试 PASS
go test ./sql/migrations/startup -count=1         # ok 0.286s（全包）
go test ./db -count=1                             # ok 0.247s（全包）
go test ./internal/logging ./bg ./modelname -count=1  # ok（待推链上其他提交的包）
cd installer && go test ./cmd/llm-gw-installer ./internal/dbinit -count=1  # ok
go build ./...                                    # exit 0
git push origin main                              # d0d5b7e45..055cecc78，main 与 origin/main 同步
```

未执行：真库 UTC/Asia-Shanghai 月界行为测试（见遗留风险 #4）。

## 5. 遗留风险（Open Risks）

1. **三份 baseline 缺 2 个函数定义**（未修，按计划单列）：`sql/schema/01-schema.sql`、
   `deploy/sql/schemas/baseline/01-schema.sql`、installer `embeddata/01-schema.sql`
   均无 `ensure_tool_usage_stats_partition` / `ensure_credit_ledger_partition` 的
   CREATE，但 promote 函数会调用（deploy baseline :3134/:3731）。仅靠 baseline
   建库后未跑 475/694 迁移即 promote 会 42883。修法 = 以权威库重新 dump 或按
   694 定义手工补齐三份并加三方一致性测试；属生成工件/发布对等工作。
2. **installer embeddata/01-schema.sql 仍为旧函数体**（无钉扎、bodies/WAL-next
   仍 columnar、candidate 是 void 空壳）：fresh install 靠 StartupFiles 里的
   687/689/694/695 逐级收敛到最终态，中间态窗口内如果 689 之前失败会停在坏函数
   上。与 #1 同属 baseline 再生任务。
3. **非 694 范围的时区敏感面**：`ensure_supplier_errors_partition(timestamptz)`
   （V371，ensureSpecs 活跃调度）无钉扎；4 个 date 签名 ensure 的 `$1::date`
   转换发生在调用侧会话时区（函数内钉扎救不了）。需独立迁移 + Go 调用侧改
   `(now() AT TIME ZONE 'Asia/Shanghai')::date` 类显式推导。
4. **无真库行为测试**：静态契约已锁形状与顺序，但「UTC 会话调
   `2026-10-01 00:30+08` 建出 `*_2026_10` + 上海午夜边界」的行为断言需要
   testcontainers/临时 PG 夹具（仓库已有 integration 标签先例），本轮未建。
5. **建分区存在性检查的并发竞态**（pre-existing）：`IF NOT EXISTS` → CREATE
   非原子，多副本同时首次 ensure 可能撞 duplicate relation；656 已有
   advisory lock 先例，可作可靠性独立单元。

## 6. 下一轮提示词（Next-Round Prompt）

```
继续会话：llm-gateway-go 迁移审计轮（R15 后续）。HEAD=055cecc78，main 与
origin/main 同步；工作树可能含并行会话的 696/697 installer 接线改动（M
installer main.go/stats_migrations_test.go/runner.go + 版本生成文件），勿覆盖、
勿混入提交。

背景：迁移 694（分区 ensure 函数 Asia/Shanghai 钉扎）已全链路收口——up 可重放、
down 可执行（13 处 OR REPLACE）、installer 五点接线、689 Go 自愈镜像已收敛到
694 体并有源契约测试（055cecc78）。

优先级：
1. baseline 再生单元（上轮遗留 #1/#2）：三份 01-schema.sql 缺
   ensure_tool_usage_stats_partition / ensure_credit_ledger_partition 定义，
   installer 副本函数体整体陈旧（candidate void 空壳、bodies/WAL-next columnar）。
   以权威库 dump 或 694 定义补齐三份（保持 bodies/WAL-next=heap、archive 保持
   columnar），新增三方关键函数一致性静态测试；先 fetch 核对远端无并行提交。
2. 时区覆盖补全单元（#3）：为 ensure_supplier_errors_partition 增加钉扎迁移
   （fetch 核对下一个编号，当前已知 697 已被占用，从 698 起核对）；Go 调用侧
   4 个 date 签名 ensure 改显式上海日历日推导。
3. 真库行为测试（#4）：testcontainers 夹具验证 UTC 会话月界行为（694 全函数 +
   687 修复面回归），integration 标签。
4. 并发竞态（#5）：评估 ensure 函数 advisory lock 化（656 先例）。

门禁：触碰 sql/migrations → go test ./sql/migrations/startup -count=1；触碰 db →
go test ./db -count=1；触碰 installer → cd installer && go test ./...；提交走
pre-commit（6 项）；版本文件 hook 重生成属预期漂移随下次改动提交；逻辑单元
精确路径提交并 push。
```

## 7. 引用（References）

- 本轮提交：055cecc78（fix(sql,db): make migration 694 rollback replace-safe…）
- 前序：59c5dda12（694 主体）、2895540c2（升级通道 + installer 接线）
- docs/audit/2026-09-10-24h-audit-round9.md（候选 15 源头）
- docs/handoff/2026-09-12-closeout-free-whitelist-alias-hygiene-694-deploy.md
