# 存储结构优化方案（request_logs / session_bodies / session_turns / 列存轮转治理）

> 状态：P0 已落地（迁移 705，2026-09-14，本机实测验收通过）；P1/P2 已设计待排期。
> 实测基线：本机 llm-gateway-pg（PG17/citus 13.3-1），2026-09 分区，数据采集日 2026-09-14。
> 关联：`docs/03-design/04-data-design/migrations/`（分区架构）、交接会话实测数据（session-storage-merge 分析）。

## 0. 实测基线（2026-09 分区）

| 对象 | 规模 | 说明 |
|---|---|---|
| session_bodies_2026_09 | 253,526 行 / 4,478MB | request_delta 2,152MB + outbound_body 1,837MB + response_delta 89MB |
| request_logs_bodies_2026_09 | 304,663 行 / 841MB（columnar） | ≈2.8KB/行；持续接收 promote 正常 |
| request_logs_default（修复前） | 255,084 行 / 609MB | **P0 断链积压**，无 TTL |
| session_turns | 253K 行 / 519MB（索引 209MB） | 约 25 列镜像 request_logs（t0-t9/tokens/cost/model/status 等） |
| 会话轮次分布 | 96.8% 单轮（201,718/208,419） | 多轮仅 679 会话；前缀冗余因子 17x 但绝对浪费仅 883MB/月 |
| 正文重复 | 55%（139,693 行）session_bodies 行在 request_logs_bodies 有同内容副本 | |
| request_logs 正文 | outbound_body 写入 0/240,016；preview p90≈324B | request_logs 已不存正文 |
| gw_session_id | 仅 40% 行填充 | |

## 1. 目标态

1. **request_logs**：`hot（8h）→ 月分区（heap，ATTACHED）`两级；default 仅作未建分区月份的临时缓冲，长期为空；历史分区按策略转 columnar（P1b）。
2. **会话正文单一事实**：session_bodies 只存 `request_delta`（+当日可见的 `response_delta`）；上一轮外发快照改由 `sessions.last_full_request / last_full_response`（migration 456 已建、至今无写者的死列）承接；`session_bodies.outbound_body` 停写（P1a，省 41%≈1.8GB/月）。
3. **promote 幂等与轮转入链**：promote 不再依赖 `ON CONFLICT` 唯一索引（列存不支持的根因），历史月关闭后 heap→columnar 轮转由迁移链/ensure 收口，不再依赖手工技能（P1b）。
4. **session_turns 瘦身**：砍约 25 个 request_logs 镜像列，读端走普通 view JOIN `request_logs_with_current_month`；不建物化视图（PG 无增量刷新，且破坏分区裁剪）（P2）。
5. **当月分区保持 heap**：`request_logs` 有行级 UPDATE 面（claimSessionFinalSuccess 只写 hot，但 TTL/promote 链依赖 UPDATE/DELETE 能力），689/562 先例已确立"活跃分区 heap、历史才 columnar"。

### 非目标（明确不做）

- ❌ 物理合并 request_logs + session_turns（8 类 schema 硬冲突，见 session-storage-merge 分析）。
- ❌ 物化视图。
- ❌ session_bodies 按"纯 insert-only"直接转列存——hot 表有保护性 upsert（重试覆盖 response_delta/outbound_body；request_delta 不可变），列存会阻断 promote。
- ❌ 裸删 `session_bodies.outbound_body`——它是下一轮差集提取的上一轮外发快照（load-bearing），必须先由尾部快照承接（P1a）。

---

## 2. P0：request_logs promote 断链修复（✅ 已落地，迁移 705）

### 2.1 根因链（实测证实）

1. migration **337** DETACH 了 `request_logs_2026_07..2026_12`（当年为绕开 default 分区约束 23514 的写入失败）。
2. `ensure_request_logs_partition`（694 体）存在性检查只查 `pg_class WHERE relname`——DETACH 后的空壳表仍在 pg_class，ensure/每日 tick/promote 的 pre-ensure 全部误判"已存在"，从不重挂。
3. promote（602/688，hot→父表）`INSERT INTO request_logs` 按路由全落 `request_logs_default`：609MB / 255,084 行（全 2026-09）积压、无 TTL、无裁剪；月分区空壳（0 行，352-368kB 纯索引）。
4. 空壳还欠 3 个 detach 后父表新增的列（billed_despite_cancellation / request_depth / is_terminal），裸 ATTACH 会列漂移失败。

### 2.2 修复内容（`705_request_logs_reattach_detached_partitions.sql`）

- `sync_partition_columns(parent, partition)`：按父表动态补齐缺失列（类型/默认值/NOT NULL 保真），通用修复工具。
- `repair_request_logs_detached_partitions()`：一次性修复。**先 DETACH default → 逐空壳（补列 → ATTACH → 该月行从独立 default 经父表路由搬入）→ 重挂 default**。搬数严格在 ATTACH 成功之后，失败的月留在 default，无"行游离于所有分区之外"窗口；逐壳 WARNING 不致命；幂等可重跑。
- `ensure_request_logs_partition` 重写：存在性改查 `pg_inherits`（attached 才算存在）；空壳自动补列重挂（失败 WARNING 并保持 default 兜底路由）；fresh CREATE 撞 23514（default 已有该月行，473 类缝隙）时**自愈**——detach default → 建分区 → 搬该月行 → 重挂 default；advisory lock 防并发 tick 竞态；heap + 双 GIN trgm 索引保持 694 语义。
- 全程 `SET LOCAL TIME ZONE 'Asia/Shanghai'` 钉扎（防 687 类边界漂移）；双账本自登记。
- 读路径零影响：`request_logs_with_current_month` = `hot UNION ALL 父表`，搬走的行经父表分支照常可见；claimSessionFinalSuccess 只写 hot。

### 2.3 本机验收（2026-09-14 实测）

```
应用前基线：default=255,084 行/609MB；四空壳各 0 行；hot=13,971；view=269,055（= hot + default ✔）
应用耗时 93.8s；NOTICE: attached 2026_07/08/09/10；repair 返回 drained=255,084（精确守恒）
standalone_shells=0；default_bound=DEFAULT 且 0 行；request_logs_2026_09=255,084 行；view=269,071（+16 为活流量 ✔）
promote 冒烟：hot 插入 9h 龄合成行 → promote('8 hours') → 行落入 2026_09 分区、default 0 落、hot 清空 ✔
磁盘：default 614MB → VACUUM FULL 后 448kB；2026_09 分区 537MB
双账本：schema_migrations=705 ✔；gateway_db_revision_sequences 补登记 ✔
行为测试：go test -tags integration -run TestMigration705（testcontainers PG16）覆盖补列重挂/按月搬数/幂等/自愈/UTC 安全 ✔
```

### 2.4 回滚

- `705_request_logs_reattach_detached_partitions.down.sql`：恢复 694 旧 ensure 体（应急回退路由逻辑）；**数据不回退**（分区保持 attached，重开断链才是事故）。辅助函数保留（无副作用）。

### 2.5 生产（154/245 共享 252 PG）应用注意

- 生产 `request_logs_default` 的月份分布需先复核（`SELECT date_trunc('month', min(ts)), date_trunc('month', max(ts)) FROM request_logs_default`）——repair 按空壳名动态搬数，不依赖静态月份；若生产空壳**非空**，repair 的 ATTACH 仍安全（壳内行随挂载保留），但应在变更窗口复核行数守恒（§6）。
- 705 已登记 `scripts/apply-db-revision-sequence.sh` 通道；文件幂等，通道重放安全。
- 单事务持父表 ACCESS EXCLUSIVE（本机 94s）；生产数据量相近时建议低峰执行。

---

## 3. P1a：激活 sessions 尾部快照，停写 session_bodies.outbound_body（迁移 706，待排期）

### 3.1 设计

- **激活死列**：migration 456 已建 `sessions.last_full_request JSONB / last_full_response JSONB / last_full_payload_at timestamptz`，全仓库无写者。706 不建新表，直接激活：
  - 写点：`domains/session/v2/session_aggregator.go` 的会话 upsert（终态聚合处）同事务覆盖三列（last-write-wins，总是存**最新完整**请求/回复）。
  - `outbound_body` 语义 = "上一轮外发快照"，由 `last_full_response` 承接后自然被覆盖。
- **停写 outbound_body**：`domains/session/v2/bodies_writer.go` 的两处 upsert（`:288/:338` 列清单与 `:304-307` 的 CASE 守卫）将 `outbound_body` 从写入集移除（列保留，历史可读）。加 settings 开关 `lifecycle.session_outbound_body_enabled`（默认 false）做一键回切。
- **读端改造**：差集提取读点在 `bodies_writer.go:413-506`（turn_reader 路径，"unmarshal outbound_body at turn %d"）与 `outbound_builder.go`——改为：先读 `sessions.last_full_response`（命中即返回）；未命中（停写前的历史行/异常路径）回退读 `session_bodies.outbound_body` 旧值。读函数签名不变。
- **收效**：session_bodies 月增 −1,837MB（41%）；sessions 表行数 20 万级，单行 +两快照（≈8-10KB）增量 ≈+2GB 总量、且不再随分区复制，**净省 ≈1.8GB/月**。

### 3.2 迁移 706（SQL 侧）

- 无 DDL（列已存在）；仅 COMMENT 更新 + 双账本登记 + `schema_migrations` 行。行为面全部在 Go（写点/读点/开关），遵守"hot 链路 DDL 最小化"惯例。
- 回滚：开关回 true 即恢复写 outbound_body；706.down = 恢复 COMMENT。

### 3.3 验收 SQL

```sql
-- 1) 尾部快照有写者且新鲜（上线后 T+1h）
SELECT count(*) FILTER (WHERE last_full_payload_at IS NOT NULL) AS snapshotted,
       count(*) FILTER (WHERE last_full_payload_at > now() - interval '1 hour') AS fresh
FROM public.sessions;

-- 2) outbound_body 停写生效（上线后新行该列全空）
SELECT count(*) AS new_rows_with_outbound
FROM public.session_bodies_2026_09
WHERE created_at > now() - interval '1 hour' AND outbound_body IS NOT NULL;

-- 3) 差集读端命中率（回退路径应趋近 0）
-- 应用日志 observe: outbound_snapshot_source=tail vs =legacy_body 比例
```

---

## 4. P1b：promote 幂等改造 + 历史分区 heap→columnar 轮转入链（迁移 707，待排期）

### 4.1 现状与缺口

- `promote_session_bodies_hot_to_partition`（638 守卫版）以 `ON CONFLICT (id, partition_date) DO NOTHING` 幂等 → **列存分区不支持唯一索引**，导致该表历史分区永远不能转 columnar（request_logs_bodies 的 528 版无此依赖，其 2026_09 已是 columnar 且 promote 正常——本会话实证 301,592→304,663 行）。
- 本机已 columnar 的分区是此前技能**手工**轮转产物；`ensure_sessions_v2_partitions / ensure_request_logs_bodies` 等仍按 **heap** 建新分区——若分区被 TTL 重建会回退 heap（治理缺口）。
- 工具链事实：`columnar_heal()`/`enforce_columnar_partition()` 可转换但**无年龄过滤**、白名单漂移（对象文件仅 routing_decision_log，phase-23 旧脚本含 credential_model_index），误调会转错分区；`auto_rotate_to_columnar()` 非 dry-run 仅返回 SKIP 占位；`columnar_insert_only_parents()` 白名单仅 routing_decision_log；`scripts/pg-columnar-rotate.sh` **不可执行**（默认容器 r112_postgres、AGE_DAYS 未生效、跨事务、DETACH 失败被忽略、末尾 DROP CASCADE）。

### 4.2 设计

1. **promote 幂等去 ON CONFLICT 化**（对 session_bodies 通道）：
   - 幂等改为 **批前存在性反连接**：`WHERE NOT EXISTS (SELECT 1 FROM session_bodies s WHERE s.id = h.id AND s.partition_date = h.partition_date)` 的批 SELECT + 原子 CTE（与 528/602 同款模式），彻底解除对唯一索引的依赖。
   - 语义差异：ON CONFLICT 是行级"插入时去重"，反连接是批级"搬前过滤"；并发双跑由 promote 的 advisory 键串行化（640 session_turns 版已有先例），残余风险与 528 版 request_logs_bodies 相同——已被实证可接受。
2. **轮转入链（月关闭后轮转，当月永 heap）**：
   - 新增 `public.enforce_columnar_partition_aged(p_parent regclass, p_min_age_months int)`：仅转换**完全关闭**（`range_upper < date_trunc('month', now() - make_interval(months => p_min_age_months))`）的 heap 月分区，白名单显式传父表，幂等（已 columnar 跳过），空分区先 DETACH→`USING columnar` 重建→ATTACH（columnar 不能原地改 AM）。
   - 挂点：`bg/partition_manager.go` ensure tick 每月 1-3 日窗口调用（复用 archiveSpecs 的 day 调度模式），先只接 `session_bodies` + `session_turns`（request_logs_bodies 已 columnar 无需）。
   - `columnar_heal()` 保持禁用状态并在注释标明白名单漂移风险。

### 4.3 迁移 707（SQL 侧）

- `CREATE OR REPLACE promote_session_bodies_hot_to_partition`（反连接版，函数体自 638 派生）+ `enforce_columnar_partition_aged` + 双账本登记；Go 侧 ensure tick 挂点随同提交。
- 回滚：707.down = 恢复 638 promote 体（重新引入 ON CONFLICT 依赖；此时必须保证目标分区仍 heap）。

### 4.4 验收 SQL

```sql
-- 1) promote 幂等：同一 hot 行手工双跑 promote，分区行数不翻倍
SELECT count(*) FROM public.session_bodies WHERE id = '<probe_id>';   -- 恒 1

-- 2) 轮转白名单与年龄闸门
SELECT c.relname, c.relam = (SELECT oid FROM pg_am WHERE amname='columnar') AS is_columnar
FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = 'public.session_bodies'::regclass ORDER BY 1;
-- 期望：当月 heap(false)，min_age 之外的历史月 true

-- 3) 无手工轮转回退：TTL 重建当月分区后 ensure 产出仍为 heap、历史月不被触碰
```

---

## 5. P2：session_turns 瘦身（迁移 708，待排期）

### 5.1 设计

- 砍 `session_turns` 约 25 个 request_logs 镜像列（t0-t9 / tokens ×4 / cost / model / provider / status / latency 等，清单以 `session_turns` 与 `request_logs` 列交集 ∩ 写端确认镜像的列为准）；保留会话维度自有列（turn_no/parent_request_id/compression/verdicts/digest 等）。
- 读端：改走普通 view `session_turns_enriched`（`session_turns JOIN request_logs_with_current_month ON request_id`）——**不建物化视图**；JOIN 键 request_id 热表有索引，分区裁剪按 ts 保留。
- 顺序硬约束：**先**上读端 view 切换（灰度读新源），**再**发 708 砍列（`ALTER TABLE ... DROP COLUMN`，分区父表级联各月分区，锁窗口用低峰）；索引重规划随砍列同步（镜像列上的 209MB 索引大部分随之消失）。
- 收效估算：519MB/月 → ≈300MB/月（数据）+索引 209MB → ≈60MB。

### 5.2 迁移 708 与回滚

- 708 = DROP COLUMN 批（父表一条 ALTER 多子句）+ view 定义 + 双账本登记；down = 重新 ADD COLUMN（数据**不可恢复**——down 仅恢复 schema 形状，砍列前该表已有 request_logs 全量镜像，数据可追溯性由 request_logs 承担）。
- 验收：`session_turns_enriched` 与旧宽表抽样 JOIN 比对（turn 级 t0/tokens/cost 全等）；砍列后 promote_session_turns_hot_to_partition（640 版）列清单同步收敛。

---

## 6. 全局验收 SQL（每步迁移后运行）

```sql
-- A. 行数守恒（P0 已验）：hot + default + 各月分区 = 视图行数（活流量下允许小幅漂移）
SELECT (SELECT count(*) FROM public.request_logs_hot)
     + (SELECT count(*) FROM public.request_logs_default) AS hot_default,
   (SELECT count(*) FROM public.request_logs_with_current_month) AS view_total;

-- B. 拓扑不变量：无游离空壳、default 在位
SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname ~ '^request_logs_[0-9]{4}_[0-9]{2}$' AND c.relkind='r'
  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid AND i.inhparent='public.request_logs'::regclass);  -- = 0

-- C. 双账本对齐
SELECT (SELECT count(*) FROM schema_migrations WHERE version='705') AS sm,
       (SELECT count(*) FROM gateway_db_revision_sequences WHERE sequence_name LIKE '%:705_%') AS seq;

-- D. 当月 heap 不变量（P1b 后持续）
SELECT c.relname, am.amname FROM pg_inherits i
JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_am am ON am.oid=c.relam
WHERE i.inhparent='public.request_logs'::regclass
  AND c.relname = 'request_logs_' || to_char(now() AT TIME ZONE 'Asia/Shanghai', 'YYYY_MM');
-- 期望：heap
```

## 7. 风险登记

| 风险 | 缓解 |
|---|---|
| repair/自愈搬数事务持父表 ACCESS EXCLUSIVE（本机 94s） | 低峰执行；promote/tick 排队无错 |
| 生产 default 月份分布与本机不同 | repair 按空壳名动态搬数 + §6-A 守恒校验；空壳非空时 ATTACH 仍安全 |
| `columnar_heal()` 无年龄过滤 + 白名单漂移 | 继续禁用；轮转仅走 `enforce_columnar_partition_aged`（显式父表 + 年龄闸门） |
| `scripts/pg-columnar-rotate.sh` 危险缺陷 | 已标注不可执行；任何轮转前用 `GET /api/admin/data-lifecycle/partitions` + `hot/jobs` 核实 |
| P1a 停写后历史 outbound_body 读依赖 | 读端保留 legacy 回退 + settings 一键回切 |
| P2 砍列不可逆 | 先切读端 view 再砍列；数据由 request_logs 镜像可追溯 |
| 本机列存分区为手工产物，ensure 建分区回退 heap | P1b 落地前：勿删历史 columnar 分区；TTL 重建仅影响当月（保持 heap 是既定策略） |
