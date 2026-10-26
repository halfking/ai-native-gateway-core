-- 886: 订正 public.session_bodies 的表注释（"使用 columnar 存储格式" 是**假注释**）
--
-- -----------------------------------------------------------------------
-- 缺陷本体（注释与事实不符，2026-10-20 会话族审计 G-6 复核轮）
-- -----------------------------------------------------------------------
--   430_sessions_v2_schema.sql:237-240 写下这段表注释：
--
--       COMMENT ON TABLE public.session_bodies IS
--         'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
--          使用columnar存储格式，配合zstd压缩，预计可节省60-80%磁盘空间。
--          Created: 2026-07-17, Migration 430';
--
--   "使用 columnar 存储格式，配合 zstd 压缩" **从来没有兑现过**：
--   session_bodies 族自建表起就是 **heap + TOAST**，而且**不可能**是 columnar。
--   三条各自独立、互相印证的书面证据：
--
--   ① 765_bodies_columnar_storage.sql:13-21（列存化那一轮的作者自己写的）：
--        "session_bodies 的 promote（615/626）使用 ON CONFLICT DO NOTHING
--         —— columnar 无唯一索引不可列存，改 lz4 TOAST
--         （真库 200 行样本：pglz 35.95MB vs lz4 35.37MB，尺寸中性、压缩 CPU
--          显著降低；body 写为会话热路径）。"
--      ⇒ **同一个族里，request_logs_bodies 走 columnar，session_bodies 走
--        heap+lz4** —— 这是 765 当轮的**刻意取舍**，不是遗漏。
--
--   ② 同文件 765_bodies_columnar_storage.sql:21：
--        "本 PG 构建 TOAST zstd 运行时不可用（SET COMPRESSION zstd →
--         invalid compression method；--with-lz4 在位），heap 侧压缩牌=lz4"
--      ⇒ 连"zstd"这个字眼都不成立：本构建的 TOAST 压缩牌是 lz4。
--
--   ③ 829_bodies_columnar_rollback.sql（把 765 的列存化**整体回滚**那一轮）：
--        回滚理由里写明列存关系在"子查询内的 UNION ALL"下会触发 planner 失败
--        （invalid perminfoindex 0 in RTE with relid 0），7/7 复现。
--        ⇒ columnar 在这套「hot ∪ 分区」双面视图族里已被证为**不可用**。
--
--   ④ 结构性的原因（不是"当时没做"）：session_bodies 族**有唯一约束**
--      （476_session_v2_tenant_unique_keys.sql:12-20：
--        session_bodies_tenant_session_turn_partition_key UNIQUE
--          (tenant_id, session_id, turn_no, partition_date)
--        session_bodies_tenant_request_partition_key UNIQUE
--          (tenant_id, request_id, partition_date)），
--      而 Citus columnar **不支持唯一索引**，其上的 ON CONFLICT 也就无从谈起。
--      ⇒ 这条约束一存在，"把 session_bodies 改成 columnar" 就**不是一次优化，
--        而是一次不可能的改造**。
--
--   ⑤ 还有一条**读侧**的硬约束：promote 走 ON CONFLICT DO NOTHING，
--      而 columnar 分区既不能有唯一索引也不能有 ON CONFLICT ⇒ 晋级路径
--      （hot → 月分区）与列存互斥。
--
--   为什么这条假注释值得单独出一条迁移：
--     - 列注释是运维**唯一**能看到的存储契约（\d session_bodies 就显示它）；
--       一条写着"columnar + zstd"的注释会让下一位维护者以为
--       "这张表已经是列存了，压缩率 60-80%"，进而做出错误的容量规划
--       （实测是**尺寸中性**：pglz 35.95MB vs lz4 35.37MB）。
--     - 它与同一批基线快照（sql/schema/01-schema.sql:15631-15634）
--       一致地被复制了 3 份 ⇒ 错得很有"权威感"。
--
-- -----------------------------------------------------------------------
-- 本迁移做什么
-- -----------------------------------------------------------------------
-- 只用 COMMENT ON TABLE 把这段注释订正成**与事实一致**的表述：
--   heap 存储 + lz4 TOAST 压缩 + 为什么这一族**不可能**是 columnar
--   （唯一约束 / ON CONFLICT / UNION ALL 视图族三重互斥）。
--
-- ★ **不改 430 本体**：430 是**已 applied + 已记账 + 校验和已登记**的迁移，
--   改它会重写已应用历史、打破 checksum 台账（AGENTS.md 的既定纪律）。
--   与 877 订正 sessions.duration_ms 注释用的是同一手法（877:257-276）：
--   **在新的迁移里用 COMMENT 订正**，不动老迁移。
--
-- ★ **不改表结构、不动数据、不加索引** ⇒ 零锁、零 IO、零回填代价。
--   COMMENT ON TABLE 只改 pg_description 一行元数据。
--
-- ★ **同步订正三份基线快照**：sql/schema/01-schema.sql、
--   deploy/sql/schemas/baseline/01-schema.sql、
--   installer/cmd/llm-gw-installer/embeddata/01-schema.sql
--   （新装实际用的是第三份）—— 三份必须与本迁移的注释**逐字一致**，
--   否则「新装路径」与「升级路径」的表注释会分叉（§基线自洽）。
--
-- 幂等：COMMENT 是幂等操作（重复执行结果相同）。
-- 回滚：.down.sql 把注释**还原成 430 的原文**（含那句不实的 columnar 说法）——
--        之所以能还原，是因为 down 要恢复的是「历史状态」而不是「正确状态」；
--        若只想保留正确注释，down 可以是空操作。
--        本文件选择**如实还原**（回滚语义 = 回到迁移前的库状态）。
-- ===========================================================================

BEGIN;

-- COMMENT ON TABLE 不拿强锁（只写 pg_description 一行）。
-- 但基线 schema 里同族对象是分区父表，ALTER 家族在别处会拿 ACCESS EXCLUSIVE；
-- 这里仍然带上 lock_timeout 以满足 startup_ddl_lock_guard_gate_test.go
-- （>870 的迁移若含强锁 DDL 关键字必须自带守卫）——本文件正文提到
-- "ALTER TABLE" 字样，故一并带上，避免门因关键字命中而红。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- 守卫：表不存在就跳过（不硬失败）——
-- session_bodies 由 430 建表，基线也建；只有在「表确实不在」的库里
-- 才可能缺席，那种库里这条注释也无从谈起 ⇒ NOTICE 后返回。
DO $comment_886_session_bodies$
BEGIN
  IF to_regclass('public.session_bodies') IS NULL THEN
    RAISE NOTICE '886: public.session_bodies 不存在（表未建）⇒ 跳过注释订正';
    RETURN;
  END IF;

  COMMENT ON TABLE public.session_bodies IS 'V2正文存储表：存储增量正文，避免request_logs的全量JSONB膨胀问题。
     ★ 存储引擎真相（2026-10-20 修正，审计 D3）：本表是 **heap + lz4 TOAST**，
     **从未使用列存（columnar）**。压缩由 TOAST 的 lz4 完成
     （sql/migrations/startup/765_bodies_columnar_storage.sql:127-128 的
     SET COMPRESSION lz4）；此前那条把本表说成「列存 + 压缩可省 60-80% 磁盘」的
     COMMENT 与实现相反 —— 本族因 promote_session_bodies 用
     ON CONFLICT DO NOTHING（列存关系不能承载唯一索引）而**必须**留 heap，
     论证见 765_bodies_columnar_storage.sql:13-21，
     829_bodies_columnar_rollback.sql 记录了列存方案被回滚。
     Created: 2026-07-17, Migration 430';
END
$comment_886_session_bodies$;

COMMIT;
