-- 891_session_turns_cost_negative_partial_idx.sql
-- 2026-10-20：给**会话族**（session_turns 分区父表 + session_turns_hot）建
-- `WHERE cost_usd < 0` 的部分索引 —— 根修
-- bg/routing_health_checks.go `recorded_cost_is_negative` 检查的**全表扫描**。
--
-- -----------------------------------------------------------------------
-- 缺陷本体：890 让 882 的索引**随表消亡**，而检查的源迁到了会话族
--
-- 882 给 v1 `_request_logs` 建过同款部分索引（根修同一条检查的重扫浪费）：
--     public.idx_request_logs_cost_negative
--         ON public._request_logs (ts, credential_id, outbound_model)
--         INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
--         WHERE cost_usd < 0
-- 而 **890 把 v1 族整体删掉**（真删表，含分区）⇒ **索引不可能活得比它的表久**
-- ⇒ 882 的那条索引随之消失。
--
-- 与此同时，检查的**源**随 890 迁到会话族
-- （session_turns_hot ∪ session_turns，30 天窗必须读两个存储面）。
--
-- ⇒ 迁移链上出现一个**空档**：检查换了源，源上**没有任何** cost_usd 索引。
--
-- -----------------------------------------------------------------------
-- 实测证据（2026-10-20 本地 PG 17.10 真库，非推理）
--
-- 检查形态（bg/routing_health_checks.go recorded_cost_is_negative）：
--     SELECT credential_id, raw_model_name, count(*), sum(cost_usd)
--       FROM session_turns_hot
--      WHERE cost_usd < 0 AND ts > now() - interval '30 days'
--      GROUP BY 1, 2
--
-- BEFORE 计划（建索引前）：**Seq Scan on session_turns_hot**
--     Filter: ((cost_usd < '0'::numeric) AND (ts > (now() - '30 days'::interval)))
--     Rows Removed by Filter: 497
--
-- BEFORE 计划（父表版）：Append → **Seq Scan on session_turns_2026_07 / _08 /
-- _10 / _11**（每个分区一次 Seq Scan，**没有任何索引可用**）
--
-- ⇒ 这条检查每 ~8-9 分钟跑一次（≈168 次/天），在生产上就是**全表扫描**；
--   这正是本轮目标里「不要有表扫描或慢 SQL」要消灭的东西。
--
-- -----------------------------------------------------------------------
-- 修法：两条**父表级 / 表级**部分索引
--
--   CREATE INDEX … ON public.session_turns (ts, credential_id, raw_model_name)
--     INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
--     WHERE cost_usd < 0
--   CREATE INDEX … ON public.session_turns_hot (ts, credential_id, raw_model_name)
--     INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
--     WHERE cost_usd < 0
--
--   • 键序：ts 在首位服务 30 天窗的**范围谓词**；credential_id / raw_model_name
--     服务 GROUP BY（本检查就按这两列分组）⇒ 索引顺序与分组顺序一致，
--     聚合可走 index-only。
--   • INCLUDE 三列（cost_usd / cache_read_tokens / prompt_tokens）：本检查的
--     聚合要读它们（sum(cost_usd)、sum(cache_read_tokens - prompt_tokens)）
--     ⇒ 放进 INCLUDE 才能让聚合**不回表**（index-only 的前提）。
--   • raw_model_name 才是会话族里 v1 `COALESCE(outbound_model, client_model)`
--     的**同义列**（会话族的 model 列落的是 client_model ⇒ 那是另一个问题）；
--     本索引用 raw_model_name，与检查侧的分组键逐字一致。
--   • credential_id 在会话族是 **text**（v1 是 bigint）。本索引用**原始 text
--     列**即可：分组键就是原样的 credential_id，不需要数值化。
--     （既有 `idx_session_turns_hot_credential_ts` 那种
--     `CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL END`
--     是给**数值化点查**用的惯例，与本索引的分组键语义不同，故不照抄。）
--   • 索引建在**父表**上 ⇒ 未来 `CREATE TABLE … PARTITION OF` 建的月分区
--     **自动继承**（877 台账订正实测口径，882/874 同款理由）⇒ 不需要改
--     ensure_session_turns_partition，也不需要逐分区 DO 循环。
--   • 生产 4 个分区 + hot 表**全是 heap**（relam=2 逐个实测）⇒ 没有列存
--     拒绝索引的面；fresh-install 的月分区（01-schema ensure 不带 USING columnar）
--     同样全 heap。
--   • **空索引很便宜**：条目数 = 负成本行数，本地实测
--     `count(*) WHERE cost_usd < 0` = 0（hot 与父表都是）⇒ 索引本体几页。
--     构建只需对每个分区做**一次**顺序扫描找匹配行（生产 session_turns
--     约 100 万行 / 3.8GB，秒到十秒级量级，874/862 同表同款）。
--     ★ 反过来说：**这个索引不会让负成本行「不存在」**，它只是把「扫描」
--       换成「探针」——负成本行若真的出现（import/回填路径），检查照样
--       报出来，canary 语义不变。
--
-- -----------------------------------------------------------------------
-- 顺序前提（fresh-install 路径必须成立）
--
--   本 up 体只引用两张表：**public.session_turns** 与 **public.session_turns_hot**。
--   两者都由**启动序列**建，不由 890 建：
--     · session_turns：基线 sql/schema/01-schema.sql 建父表 + 月分区
--       （v1 会话族 430/513/526 的底座），raw_model_name 由 **707** 补列；
--     · session_turns_hot：installer-only 终态资产
--       `session_turns_hot_bootstrap.sql`（526+636 投影）建，raw_model_name
--       同样由 **707** 补列（707 同时 ALTER 这两张表）。
--   ⇒ 891 的编号 > 707 / 513 / 526 / bootstrap ⇒ 在启动序列里**天然排在它们
--     之后**，两表与四个列（ts / credential_id / raw_model_name / cost_usd /
--     cache_read_tokens / prompt_tokens）到 891 执行时已在场。
--   ⇒ 本 up 体**不依赖 890 或任何其它未创建对象**。
--
-- -----------------------------------------------------------------------
-- 锁与时长
--
--   普通 CREATE INDEX（父表不支持 CONCURRENTLY）持 SHARE 锁到构建完成 ⇒
--   构建期间该表的**写被阻塞**（读照常）。session_turns 是**活写**分区父表
--   （874 在同一张表上建索引时记录了同款语义）。
--   lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑（882/878/876
--   同款；startup_ddl_lock_guard_gate_test 钉住 >870 的强锁 DDL 必须带
--   lock_timeout）。
--   statement_timeout=10min：索引构建本身可能很久，而 lock_timeout 只管锁等待
--   （849/855/856/862/874 同款：共享 PG 服务端 statement_timeout=30s 会掐长
--   索引构建，57014 击穿后迁移永不记账，成为永久卡点）。
--
-- 幂等：IF NOT EXISTS。Down：按名删两条索引（父表 DROP 级联删分区侧子索引）。
--
-- 台账/通道：canonical 与 installer embeddata 两副本 SHA 现场一致；
-- installer go:embed + embeddedSQLFiles map、dbinit StartupFiles、TSV、
-- 部署通道 files=()、.down.sql 均已就位。
-- 契约门：migration_891_session_turns_cost_negative_partial_idx_test.go
-- （钉两条部分索引的存在/谓词/键序/INCLUDE、lock_timeout、down 形态、
-- 两份字节一致，并带阳性对照）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_session_turns_cost_negative
    ON public.session_turns (ts, credential_id, raw_model_name)
    INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
    WHERE cost_usd < 0;

CREATE INDEX IF NOT EXISTS idx_session_turns_hot_cost_negative
    ON public.session_turns_hot (ts, credential_id, raw_model_name)
    INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)
    WHERE cost_usd < 0;

COMMIT;

-- POST_CONDITION:
--   SELECT indexname FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_turns_cost_negative',
--                         'idx_session_turns_hot_cost_negative');
--   -- 分区侧子索引按 pg_index.indisparent / 定义匹配核对（877 台账口径：
--   -- 不按名字 grep —— PG 用列名自动改名）
