# D07 — 热+分区存储

> 域知识库：[docs/audit/playbook/domains/D07-hot-columnar.md](../../../docs/audit/playbook/domains/D07-hot-columnar.md)
> R73 改动面：request_logs archive cadence 与 migration 754 调用边界。
> 状态：**S-01 已由「环境未提供」改为真库实测并完成**（2026-09-29，PostgreSQL 17.10）。
> 实测推翻了此前三轮关于该链路「已接线可用」的结论——详见 §8。

## 1. 审计要点

- 核对 hot 保留、月分区扫描、归档表 RLS/租户字段、幂等与定时器相位。
- `archiveOldRequestLogs` 使用 30m Go context；migration 754 为单长事务、分批 INSERT、无 executable DELETE/COMMIT。
- **已覆盖（原「未覆盖」三项）**：真实 role timeout → 调用方 `SET LOCAL statement_timeout='30min'` 已实测触发；
  历史数据量 → 真库 2,125,857 行月分区已灌数复刻；执行计划 → 已 EXPLAIN 实测并据此修复。

## 2. 业务测试

- [x] B-01：archive cadence 多启动相位定向测试通过

## 3. 数据测试

- [x] D-01：migration 754 function/installer/embed/caller 形状核对（**形状全对、内容全错——见 §8**）
- [x] D-02（2026-09-29 新增）：归档投影列必须存在于基线 DDL 的 request_logs 列集合（跨源交叉校验）
- [x] D-03（2026-09-29 新增）：canonical 与 delivery 两份 754 必须字节一致

## 4. 压力测试

- [x] S-01：兼容 PG EXPLAIN / 大分区实测 —— **发现并修复两个 P1**，见 §8 与 reports/latest.md

## 5. 安全测试

- [x] SF-01：源分区直查、独立归档表、无删除的静态守卫通过

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D07-hot-columnar/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
# ① 归档接线（R73 五条）：每日一次闸门、分钟不敏感、闸门真被调用、
#    statement_timeout 钉在事务内、由小时清理循环驱动。
go test -race -timeout 120s ./bg -run 'TestShouldRunRequestLogsArchive_|TestArchiveOldRequestLogs_' -count=1
# ②a 只读目录门（列名交叉校验 / 双副本字节一致 / promote_* 批游标首列索引 /
#     死函数引用不存在的表）。只读，DSN 指向被审计库本身。数据门不需要该 DSN
#     也能跑（前三道），后两道需要。
D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
  go test -timeout 120s ./tests/48h-audit/D07-hot-columnar/data/... -count=1
# ②b 归档 SQL 真跑 + 计划形状（2026-09-29 新增，见 §8）。会 CREATE/DROP 一次性
#     scratch 库，故**必须**用维护 DSN（路径为 /postgres、角色有 CREATEDB），
#     且刻意不读 ②a 的 D07_S01_PG_URL——一个变量服务两种角色，会让只读门在
#     误配时悄悄变成写门。无库自动 skip。
D07_S01_ADMIN_URL=postgres://admin:pass@127.0.0.1:5432/postgres?sslmode=disable \
  go test -timeout 300s ./tests/48h-audit/D07-hot-columnar/stress/... -count=1
```

**为什么拆成两个 DSN**：②a 是只读目录审计，②b 要建/删自己的库。用同一个变量时，
把只读 DSN 配给 ②b 会让 ②b 尝试在真库上 CREATE DATABASE；把 ②b 的 DSN 配给 ②a
则让一个只读门挂在一条预期会写的连接上。两者都不是能靠「配的人小心」解决的问题，
所以在代码里互不读取。

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`bg/partition_manager.go`；`installer/cmd/llm-gw-installer/embeddata/startup/754_archive_request_logs_default.sql`
- R79（2026-09-29）：`sql/migrations/startup/756_request_logs_id_index.sql`（新增，含 down）

## 8. 2026-09-29 S-01 实测结论（本计划项此前的「环境未提供」标注已过期）

计划项原标注「环境未提供」，实测时本机 PostgreSQL 17.10 已健康运行 34 小时，
标注属过期。改为实做后，**两项此前登记为「已通过」的结论被推翻**：

| # | 级别 | 发现 | 处置 |
|---|---|---|---|
| P1-a | 致命 | 754 的 `archive_request_logs_default()` **自落地起从未成功执行过一次**。candidates CTE 投影的 11 列里有 2 个在 request_logs 上不存在（真名 `gw_session_id` / `upstream_status_code`），首跑即 SQLSTATE 42703。因函数体是 `format()+EXECUTE` 动态 SQL，CREATE 阶段不校验列，六轮审计 + D-01 形状核对 + SF-01 静态守卫全部放行 | ✅ 已修（canonical + delivery 双副本字节同步）；新增 data 跨源列名门 + stress 真跑门，均过变异检验 |
| P1-b | 致命 | 754 的批游标 `WHERE id > :last ORDER BY id LIMIT 1000` 在 request_logs 上**没有任何可用索引**（无主键，唯一索引仅 `(request_id, ts)`），每批次退化为全分区并行顺序扫描。EXPLAIN 实测：取 1000 行读 **531,262** 缓冲块（≈4.2 GB）；成本 O(rows²/1000)，2.1M 行 = 2126 批 ≈ 8.9 TB 缓冲读 | ✅ 新增迁移 756 建 `request_logs(id)` 索引；新增 stress 计划形状门（自带「建索引前必为 Seq Scan」对照） |

**实测数字**（本机 PG 17.10，2,125,857 行 / 6070 MB 月分区，无其他活动会话）：

| 场景 | 结果 |
|---|---|
| 冷归档（无 id 索引） | **30:00.028 被 statement_timeout 击杀，整笔 ROLLBACK，0 行归档** |
| 冷归档（建索引后） | **25.96s，2,125,857 行全部归档** |
| 热重跑（幂等） | **8.18s，rows_archived=0，归档表仍 2,125,857 行（无重复）** |
| 单批计划（索引前） | Parallel Seq Scan + top-N Sort，531,262 buffers |
| 单批计划（索引后） | Index Scan，230 buffers，1.294 ms |
| 建索引耗时 | 2.33s（2.1M 行分区） |
| 归档表体积 | 497 MB / 源 6070 MB ≈ **8.2%**（754 头注称「≈5% 级」，实测略高，不影响结论） |

**两条必须一起上线的耦合**：只修 P1-a 不修 P1-b，链路会从「毫秒级失败」退化成
「每晚烧 30 CPU 分钟、到点被击杀、整笔回滚、次日重来」的永久活锁——即 R72 对 753
首扫的诊断在生产规模上的复刻，而 R73 的 30min 抬升并不能兜住它。

**顺带登记（未修，owner 裁决）**：归档月表 `request_logs_archive_YYYY_MM` 是 754 建的
**独立 heap 表**，未 ATTACH 到父表 `request_logs_archive`。该父表是 `PARTITION BY
RANGE (ts)` 但**分区数为 0**（唯一会 ATTACH 的旧函数 `archive_request_logs(date)`
已被迁移 331 移除，且 331 本身未进 installer startup 通道）。后果：任何未来读方写
`SELECT ... FROM request_logs_archive` 会静默得到 0 行而不是报错。现状「无任何读方」
属实，但这是个静默陷阱。

## 9. R79 续：promote_* 全族普查（S-01 方法的推广）

S-01 只修了 `request_logs` 一族。本轮把「批游标列必须有首列索引」这条推到 hot→partition
的 `promote_*` 全族（真库 29 个函数 / 27 个批游标 / 18 个活函数），结果：

- **Gate A（活函数缺索引）**：18 个活函数里 16 个索引齐全，2 个没有 ——
  `candidate_failure_logs_hot(ts)`（无任何 ts 首列索引）与 `auto_route_selections_hot(ts)`
  （仅有 promote 谓词推不出的部分索引）。**定级 P2 潜在，不是 P1**：按实测摄入速率
  （24 行/h 与 359 行/h），8h 窗口只有 ~190 / ~2,900 行，**都不到一个 5000 行的批次**，
  二次方代价今天根本没被支付。
- **Gate B（死函数引用不存在的表）**：3 个 `promote_*_default_batch` 零调用方，
  且其父表根本没有 DEFAULT 分区。**定级 P3 文档债**（上了膛的枪：接线即 42P01）。
  登记而不删——删 schema 对象是迁移决策。

**一条被证伪的假设**：初见 `candidate_failure_logs_hot` 有 8h47m 的行未超窗被 promote，
判「8h 不变式已破」；追日志后确认上一轮 promote 跑于 23:04:50、截止线 15:04:50，
当时最老行 15:05:32 **尚未到期**，按小时周期的正常滞留上界是 8h+1h。
**不变式没破，是我在验证前就开始归因。**

两道门均**过变异检验**：白名单塞假条目 → 自收缩检查红；Gate A 索引查找改成 `WHERE false`
→ 报出全部 16 个活函数（证明不是象征性抽查）。两道门都 **fail-closed**：
活函数解析不出批游标即红，而不是跳过——这条设计在写门当天就顶出了抽取器只覆盖 7/26 的坑。

详见 reports/latest.md「R79 续」。

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D07-hot-columnar/）。
知识库入口：docs/audit/playbook/domains/D07-hot-columnar.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§8
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
