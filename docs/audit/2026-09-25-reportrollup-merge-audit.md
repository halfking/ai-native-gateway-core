# 对账报表例行审计轮 —— R65 + ca8ded60c 合并后核查

日期:2026-09-25(09:21 ca8ded60c 合并后首轮审计)
范围:domains/reportrollup + bg/report_rollup_worker.go + admin/report_rollup.go + 745/746 迁移
基线:本地 main @ ca8ded60c,origin/main 已领先(cc497a42d merge 252 SQL 审计第八轮 a0e4d9c5 / 迁移 747 / b21c499c 等)

## 结论速览

| 审计项 | 结论 |
| --- | --- |
| ① 两轮合并语义双修/漏修 | 代码侧双修无冲突、无覆盖;**发现 2 处 SQL 头注编码描述漏修(本轮已修)** |
| ② 真库 E2E(scratch 库) | PASS,含 internal_person 跨租户双桶断言 |
| ③ 245/154 实机首日快照 | **已生成但残缺**:09-24 有 221 行,缺 internal_person/internal_model;**NUL 缺陷实机实证**;根修版未部署 |
| ④ retention 演进阈值 | **未到触发点**(221 行/240 kB,距分区演进 2-3 个数量级) |

## ① 合并语义审查

提交谱系:9635b9b17(落地)→ 114beda22(R65 审计,NUL 版 scope_key)→ c5514087b(追赶/事务/解耦)→ 02da86168(NUL 根修:长度前缀 len(tenant):tenant:person)。ca8ded60c 为 merge(git combined diff 不含 reportrollup 文件 = 无手工冲突改写)。

逐项核对 R65 修复在 HEAD 的在位性:scope_key 编码租户(长度前缀终态)✓、daily_total 折叠变体删除 ✓、providerModelDaySQL COALESCE ✓、internal 视角 tenant 过滤 days 序列 ✓、admin 三端点 nil-pool 503/export degraded/错误不透传 ✓、xlsx 1048576 行上限 ✓、qualityWidths 与 kinds 同源 ✓、provider_id/tenant_id 过滤与视角的非法组合在 admin 层 400 挡住(report.go 里 internal 行 provider_id 恒 NULL、provider 行 tenant_id 恒 NULL 的边界组合不可达)✓。

### 本轮修复(漏修)

02da86168 根修只改了 Go 侧,**两处 SQL 头注仍描述 NUL 编码**,SSOT 与实码漂移:

- `sql/objects/tables/report_snapshots.sql`(SSOT,745 头注自声明「终态以 SSOT 为准」):internal_person 行 scope_key 描述仍为 `tenant_id + '\x00' + end_user_id` → 改为长度前缀 + NUL 根修注记。
- `sql/migrations/startup/745_report_snapshots.sql` R65 勘误段(c)同理 → 改为长度前缀;installer embeddata 副本 cmp 同步(746 未动)。

新读者按旧 SSOT 实现会再次踩 PG TEXT 拒绝 NUL(22021)。

## ② 真库 E2E

本地 docker pg17(kx-citus-pg17:offline-arm64, 127.0.0.1:5432)建一次性 scratch 库 `reportrollup_e2e_20260925`,灌 536/537/745/746(全部零错应用),TEST_DATABASE_URL 指向 scratch:

- `TestReportRollup_RealDB_E2E` PASS(0.82s):六 scope 断言含 internal_person 跨租户同人双桶(tenantA/alice=5 行、tenantB/alice=1 行,personRows==2,按精确长度前缀 scope_key 查库)。
- `TestReportRollupWorker_CatchUp_RealDB` PASS:missing_probed=2 / days_backfilled=1,skip-yesterday 语义正确。
- bg 包其余 2 个 RealDB 测试 FAIL 为 scratch 库缺 hot 表族(dashboard_access_events_hot 等),与 reportrollup 无关,预期环境局限。
- 纯单测 ./domains/reportrollup + ./bg + ./sql/... 全绿;go build 全仓 OK。E2E 后 scratch 库已 DROP。

## ③ 245/154 实机核对

### 部署谱系(审计时刻)

| 节点 | 实例 | build | git_sha | 对账报表语义 |
| --- | --- | --- | --- | --- |
| 245 | canary@8781(active) | 2245 | a0e4d9c5 | 9635b9b17+114beda22 在、c5514087b/02da86168 **不在**;bg_mode=data-plane,worker 块整体跳过 |
| 245 | canary@8782(failed) | 2250 | b21c499c | 不含对账报表;09:58:45 蓝绿轮换 SIGKILL |
| 154 | canary@8781(active) | 2248 | b21c499c | 不含对账报表 |
| 154 | canary@8782(failed) | — | (NUL 版构建) | **唯一实际跑过 worker 的实例**,进程已死 |
| 本地 | llm-gateway-local-8782 | 2249 | b21c499c | 不含对账报表 |

**根修版(02da86168/c5514087b)未部署到任何节点。**

### NUL 缺陷实机实证(154 canary@8782)

```
10:02:27 INFO  report rollup worker started
10:02:54 WARN  report rollup worker run failed trigger=startup-backfill
               error: upsert internal_person/default\x00anonymous/: SQLSTATE 08P01
```

08P01(invalid message format)= pgx 在 wire 层拒绝含 NUL 的字符串参数——与 E2E 的 22021 是同一根因的两个暴露面(extended protocol vs text 路径)。该进程为 NUL 版(114beda22 线)构建。

### 首日快照状态(共享 PG 172.16.2.210)

- 表已建(245 8781 启动日志 "report_snapshots ensured (745+746)" + 迁移已落)。
- report_snapshots = 221 行,report_date 全部 = 2026-09-24:daily_by_model 207 + daily_by_provider 11 + daily_total 1 + internal_tenant 2;**internal_person 0、internal_model 0**。
- updated_at 集中在 10:07:40.208–46.375(≈6.2s),顺序与 RollupDay upsert 循环一致,止于 tenant 面——person 桶处中断的残缺轨迹(逐行 autocommit 版语义)。10:07:19 154 侧有 stats minute rollup deadlock 记录,精确写入进程无法归因(collector/pre-prod/两节点 canary 均已逐一排除),但其行为与 NUL 版代码一致,不影响审计结论。

### 残缺快照的次生发现:追赶无法自愈,需部署后手动补

`MissingRollupDates` 以 daily_total 哨兵行判断「该日聚合发生过」(设计决定:零流量日也写一行)。09-24 已有 daily_total 行 → 追赶永远不会重跑该日 → **internal_person/internal_model 的 09-24 行将永久缺失**。评估:不修探测逻辑——c5514087b 单事务版下中途失败=全回滚,不会再产生残缺日;当前残缺是一次性历史包袱。**登记部署后动作:根修版上线后 `POST /api/admin/report-rollup/run {"date":"2026-09-24"}` 手动补齐(ON CONFLICT 幂等)。**

## ④ retention 演进阈值评估

实测(首日):221 行 / 240 kB,167 个 distinct model 行。日增 ~200-400 行(随 model 数)、<1 MB/日;年化 ≈10 万行/150-200 MB(即使 model 数翻倍)。745 演进注记③(热区+月分区+索引加密)的合理触发点:

- 表行数 > 500 万 或表大小 > 2 GB → 启动分区演进;
- 单日聚合轮耗时 > 5 min → 启动索引加密。

**当前距触发点 2-3 个数量级,未到触发点,维持 R65「登记不处置」。**

## 遗留与建议(优先级序)

1. **P1|部署**:ca8ded60c(或 origin/main 最新)尚未上 245/154——154 唯一跑过 worker 的实例是 NUL 版且已死,当前全网对账报表 worker 零活跃;下一部署轮携带,部署后手动 /run 补 09-24。
2. **P3|登记**:MissingRollupDates 若未来出现第二种残缺路径(如手写部分数据),再评估按 scope 完整性探测;当前单事务已根治。
3. retention 维持观察,阈值见 ④。
