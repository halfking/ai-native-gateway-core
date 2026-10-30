-- 927: routing_policy 单例化（去重 + UNIQUE (tenant_id)）—— 2026-10-31 12h 审计轮。
--
-- # 缺口本体
--
-- routing_policy 是**单例配置表**：id 上有 CHECK (id = 1)，tenant_id 默认 'default'。
-- ★★ 但它**没有 PRIMARY KEY，也没有任何 UNIQUE 约束** ⇒ ★★ 「单例」只是**约定**，
-- ★★ 数据库层面零保证。
--
-- ★★★ 后果链（本轮 12h 审计轮实锤，三环相扣）：
--
--   ① **重复行的来源**：sql/schema/02-seed.sql:935 用
--      `INSERT INTO public.routing_policy VALUES (...) ON CONFLICT DO NOTHING`
--      —— ★ **没有冲突目标**，★ 而表上没有唯一约束可供冲突 ⇒ ★★
--      ★★ **ON CONFLICT 子句是空操作** ⇒ ★★ 每次全新安装/升级都**再插一行 id=1**
--      ⇒ ★★ 211 生产实测已堆到 **4 行**（★ 这就是「4 行物理重复」的出处）。
--
--   ② **重复行的放大**：bg/quality_node_source.go 与 bg/model_credibility_source.go
--      用 `CROSS JOIN routing_policy pol` 取候选 ⇒ ★★ 4 行 ⇒ 同一个
--      (credential, model) 对被放大成 **4 份** targets ⇒ ★★ 同一轮里串行重探 4 次
--      （★ inFlight gate 只防并发、不防同批重复行）⇒ ★★ 白烧 4 倍上游调用。
--      ⇒ ★★ 这两处本轮已用 SELECT DISTINCT 修好（**消费侧自保**），
--      ★★ 但★ **根因仍在** ⇒ ★★ 下一个消费者忘记加 DISTINCT 就复发。
--
--   ③ **写路径放大**：admin/routing.go 的三处写路径都是
--      `UPDATE routing_policy SET ... WHERE tenant_id = 'default'`
--      —— ★ **没有 id 谓词** ⇒ ★★ 每次管理面改配置都会**同时改 4 行**。
--
-- # 本迁移做两件事（★ 都可重入 ⇒ 重复执行不写重行）
--
--   ① **去重**：每个 tenant_id 只保留**一行**。
--      ★★ **不猜**：★ 若同一 tenant 的多行**内容不一致** ⇒ ★★ 直接报错、让维护者裁决
--      ★★ （★★ 内容不一致意味着「有的行比别的行新」⇒ 那是**人的判断**，
--      ★★ 不是迁移能替的 ⇒ ★★ 迁移替人选 = 悄悄改路由行为）。
--      ★★ 内容一致时保留 **ctid 最小**（最早插入）的那一行：★ 管理面写路径
--      ★★ `UPDATE ... WHERE tenant_id='default'` 会把**所有**该租户的行一起改
--      ★★ ⇒ ★★ 最老的一行承载了**最长的调优历史** ⇒ ★★ 它才是权威行。
--
--   ② **加 UNIQUE (tenant_id) 约束**：★ 有了它，① 里提到的 02-seed.sql 的
--      `ON CONFLICT DO NOTHING` **才第一次真正生效** ⇒ ★★ 后续安装**不再堆重复行**
--      ★★ （★★ 这才是根因的闭环，★ 而不是消费侧那两条 DISTINCT）。
--
-- ★ 为什么约束建在 tenant_id 上而不是 id 上：★ id 被 CHECK (id = 1) 锁死，
-- ★ 而 **tenant_id 才是这张表的真正区分列**（默认 'default'）⇒ UNIQUE (tenant_id)
-- ★ 既能挡住重复行，★ 又**不妨碍将来加租户级路由策略**
-- （★ 若建在 id 上 ⇒ 多租户会直接撞 CHECK/UNIQUE ⇒ 把「将来要用的那个维度」锁死）。
--
-- 锁守卫（startup_ddl_lock_guard_gate_test.go）：含 DELETE 回填 + ADD CONSTRAINT
-- ⇒ 必须自带 lock_timeout 与 statement_timeout（917/921/926 同款纪律）。
--
-- ★★★ **2026-10-31 12h 审计轮修：★ 本文件必须【自带事务】★★★ ★ 原因：
-- ★ `SET LOCAL` **只在事务块内生效** ⇒ ⇒ ★★ 而本文件原先**没有** BEGIN/COMMIT
-- ★★ ⇒ ⇒ ★★ ⇒ 部署通道 `scripts/apply-db-revision-sequence.sh` 的 `psql_stream`
-- ★★ （★ **不**加 --single-transaction）⇒ ⇒ ★★ ⇒ 整份文件按「每条语句一个隐式
-- ★★ 事务」执行 ⇒ ⇒ ★★ ⇒ **顶部的 SET LOCAL 落在事务块外 ⇒ ⇒ PG 只发一条
-- ★★ WARNING ⇒ ⇒ ★★ ⇒ ★★ ★★ **lock_timeout / statement_timeout 两个守卫实际
-- ★★ ★★ 都没生效** ⇒ ⇒ ★★ ⇒ ★★ ⇒ 而 ADD CONSTRAINT UNIQUE 要拿 ACCESS
-- ★★ ★★ EXCLUSIVE 锁 ⇒ ⇒ ★★ ⇒ 可以无限期等锁 ⇒ ⇒ ★★ ⇒ 部署卡死。
-- ★★ ⇒ ★★ 同理，步骤 1/2/3 各自独立提交 ⇒ ⇒ ★★ ⇒ 「行已删、约束未建」的
-- ★★ 半应用状态是可能的。
-- ★★★ ⇒ ⇒ ★★ 修法（★ 与 856/855 等既有迁移同款约定）：★ 文件自带
-- ★★★ BEGIN; … COMMIT; ⇒ ⇒ ★★ 于是 ① SET LOCAL 生效（★ 任何通道都生效）、
-- ★★★ ② 三步原子（★ 任一步失败 ⇒ 整份回滚）⇒ ⇒ ⇒ ★★ installer 通道那边
-- ★★★ （★ --single-transaction）⇒ ⇒ BEGIN 会给一条「已有事务」的 WARNING、
-- ★★★ COMMIT 会提交外层事务 ⇒ ⇒ ★★ 效果与本意一致（★ 仓内已有 70+ 个同款文件）。
BEGIN;

SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

-- 步骤 1：同一租户的多行若**内容不一致** ⇒ 报错，不猜。
-- ★ 比较时排除 updated_at（易变时间戳，不是配置内容）。
-- ★ to_jsonb(rp) 取整行 ⇒ 一次覆盖**所有**列 ⇒ 将来给表加列也自动纳入比较。
DO $$
DECLARE
  bad record;
BEGIN
  FOR bad IN
    SELECT rp.tenant_id,
           count(*) AS total_rows,
           count(DISTINCT (to_jsonb(rp) - 'updated_at')) AS distinct_rows
    FROM public.routing_policy rp
    GROUP BY rp.tenant_id
    HAVING count(DISTINCT (to_jsonb(rp) - 'updated_at')) > 1
  LOOP
    RAISE EXCEPTION
      'routing_policy: tenant % 有内容不一致的重复行（% 行 / % 份不同内容）⇒ 本迁移不猜，需人工裁决',
      bad.tenant_id, bad.total_rows, bad.distinct_rows;
  END LOOP;
END $$;

-- 步骤 2：内容一致 ⇒ 每租户保留 ctid 最小（最早插入 ⇒ 调优历史最长）的一行。
-- ★ 只用 tid 的 < 比较（tidlt）⇒ 不依赖 min(tid) 聚合（tid 没有默认 btree 聚合）。
-- ★ DELETE 内部的相关子查询读的是**语句开始时的快照** ⇒ 同语句内自洽。
DELETE FROM public.routing_policy rp
WHERE EXISTS (
  SELECT 1
  FROM public.routing_policy k
  WHERE k.tenant_id = rp.tenant_id
    AND k.ctid < rp.ctid
);

-- 步骤 3：加 UNIQUE (tenant_id) 约束（★ 可重入：先查 pg_constraint）。
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'routing_policy_tenant_id_key'
      AND conrelid = 'public.routing_policy'::regclass
  ) THEN
    ALTER TABLE public.routing_policy
      ADD CONSTRAINT routing_policy_tenant_id_key UNIQUE (tenant_id);
  END IF;
END $$;

COMMIT;
