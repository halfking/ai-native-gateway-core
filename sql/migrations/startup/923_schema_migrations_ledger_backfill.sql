-- ===========================================================================
-- File:          sql/migrations/startup/923_schema_migrations_ledger_backfill.sql
-- Migration:     923
-- Database:      llm_gateway
-- Purpose:       把 900~920 这 20 条迁移补登记进 public.schema_migrations
--                 （运维人读台账）—— ★ **只补记账，不动任何 schema 对象**。
--
-- Status:        active
-- Idempotent:    YES（INSERT ... ON CONFLICT (version) DO NOTHING）
-- Dependencies:  无（只读 public.schema_migrations 一张已有的台账表）
--
-- ★★★ 为什么需要它（缺口本体：两个台账的差额，不是「没上」的证据）★★★
--
-- 本仓有**两个迁移台账**，它们记的是同一件事，却**记的不是同一段**：
--
--   ① public.schema_migrations（version / description / applied_at）
--      —— **人读视图**。运维、DBA、SRE 看的就是这张。
--      本地真库实测（**补记前**）：**74 行，max version = 899**。
--
--   ② public.gateway_db_revision_sequences
--      （sequence_name / content_sha256 / execution_sha256 /
--        runtime_adaptation / applied_at）
--      —— **部署通道的权威台账**。部署通道
--      scripts/apply-db-revision-sequence.sh 判「已应用 / 待应用 / 按内容重放」
--      读的就是它，写入点在同文件 :2857（INSERT ... ON CONFLICT (sequence_name)）。
--      本地真库实测：**287 行，900~920 全在里面（20 行，918 号不存在）**。
--
-- ⇒ ★★ **差额的性质**：900~920 **早已应用**，缺的是**记账**，不是**应用**。
--   逐条核对过每条迁移的特征产物（索引 / 约束 / 函数 / 种子行）：
--   scripts/apply-db-revision-sequence.sh 的 ledger_artifact_probe 段
--   对 900~920 的**每一条**迁移都配了产物探针（900~917 + 919~920；
--   921/922 另有同款探针），即这些迁移的产物在活库上都在场。
-- ⇒ ★★ 所以本条**只写台账**，**绝不重跑**那 20 条迁移（重跑不是幂等的等价物：
--   902 换 pkey、908 删索引、910 换视图体 —— 重放是**改 schema**，不是补记账）。
--
-- ★ 谁是权威、谁是人读视图（★★ 这条决定了本条为什么存在）★★
--
--   · gateway_db_revision_sequences = **权威台账**。部署通道按它判「已应用」，
--     且**安装器**（installer/internal/dbinit/runner.go 的 applySQL）**逐文件
--     幂等执行**、**不读 schema_migrations 决定跳过** ⇒ ⇒ **本条不会导致
--     任何一条迁移被重复应用**。
--   · schema_migrations = **人读视图**。它落后 ⇒ 运维读它会把「已应用」误判成
--     「待应用」（例如手工补跑时、或用「台账里没有」判断「这个库是不是新的」时）。
--     ⇒ 本条把这段差额补平，让人读视图重新说真话。
--
-- ★ 本仓惯例（为什么这条长成「迁移自己往台账插一行」的样子）★
--
--   迁移文件**自己**往 schema_migrations 插一行自登记，是本仓既有的定式
--   （695-705 起，见 899_session_bodies_promote_body_sink.sql:237 的同款写法：
--   INSERT INTO public.schema_migrations (version, description) VALUES (...)
--   ON CONFLICT (version) DO UPDATE ...）。
--   ⇒ ★ 本条就是**把 900~920 补成同样的自登记形态**：让「台账里有没有这一行」
--     继续是「这条迁移有没有被应用过」的可信信号。
--
-- ★ 幂等与不变量（每一条都可被机器复核）★
--
--   · ON CONFLICT (version) DO NOTHING：version 上的唯一键是主键
--     schema_migrations_pkey（基线 sql/schema/01-schema.sql 带；
--     生产侧由 scripts/repair-252-migration-ledger.sh 的同款断言恢复）
--     ⇒ 重复执行**不会写重行**，也**不会覆盖**运维手改过的 description。
--   · ★ **918 号不存在**（目录里 917 之后直接是 919）⇒ **不造 918 行**。
--     900~920 去掉 918 = **20 条**，本条插的就是这 20 条
--     （本地真库实测：权威台账 gateway_db_revision_sequences 里 900~920 的
--     标记行也是 **20 行** —— 两边应当一致，且本条落地后确实一致）。
--   · ★ **899 及以前的行一个字都不动**（它们已经在台账里）⇒ 本条只增不改。
--   · ★ **本条不自登记**：自登记会把 max(version) 顶到 923，把「schema 侧水位」
--     的读数口径污染（923 是一次记账动作，**不改变任何 schema 对象**）。
--     ⇒ 补完之后 max(version) 仍应是 **920**。
--     923 自身在部署通道的权威台账里有行（gateway_db_revision_sequences）。
--   · applied_at 取默认值 now() = **记账时刻**（各条迁移的**原始**应用时刻
--     已不可考 ⇒ 如实记为补记时刻，不伪造）。
--   · 本条是**纯 DML**：不改 schema、不拿强锁 ⇒ 不需要 lock_timeout；
--     也不带**逐文件事务豁免标记**（runner.go 的 noTransactionMarker —— 无
--     CONCURRENTLY ⇒ 走 installer 的默认事务通道，全文件原子）。
--
-- ★ 部署通道的对账（§ledger_artifact_probe）
--   scripts/apply-db-revision-sequence.sh 为本条配了产物探针，判据 =
--   「schema_migrations 里 900~920（去掉 918）这 20 个版本行都在」。
-- ===========================================================================

INSERT INTO public.schema_migrations (version, description)
VALUES
  ('900', '把 FreeDiscovery 数据模型（084 + 087）再投递给升级通道库。'),
  ('901', '给分区父表 public.usage_ledger 补 (api_key_id, ts DESC) 部分索引。'),
  ('902', 'stats_event_dedup 索引合并 —— pkey 换体为 UNIQUE(event_id) INCLUDE(occurred_at)，删除严格冗余的 (event_id, occurred_at) 复合索引（845 所建）。'),
  ('903', '给分区父表 public.sessions 补 (updated_at DESC, session_id DESC) 索引。'),
  ('904', '给 inspector-stats 的两条「恒扫全表」聚合腿补部分索引。'),
  ('905', '给「会话列表的轮次级过滤」补 model 维度的索引（父表 + hot 表）。'),
  ('906', '给「会话列表的 api_key 回填」补部分索引（父表 + hot 表）。'),
  ('907', '`routing_analytics_source` 换成「会话族两腿 + LATERAL 回定位」⇒ 消除刷新链里 45 万行 `session_turn_details` 顺序扫。'),
  ('908', '删除 route_incident_events 的零消费方索引 idx_route_incident_events_type_created (event_type, created_at DESC)。'),
  ('909', '给 session_summaries 的三个 LIKE/ILIKE 查询列补 GIN trigram 索引，让 admin 的会话搜索（catalogSearchSQL）不再对全表顺序扫描。'),
  ('910', '`routing_analytics_source` 父表腿从 LATERAL 逐行回定位换成「普通 LEFT JOIN + partition_date 日界下推」⇒ details 侧可被规划成 Hash Join，消掉 907 形态残留的 ~3 万次/刷新 逐行探针。'),
  ('911', '本地音频（ASR/TTS）供应商 catalog 三条目（2026-10-26 多模态标准接口轮）。'),
  ('912', 'W1.5: session body trash + hidden ledger for trash/restore/purge.'),
  ('913', 'MaaS monthly bills + coupons (+ invoice placeholder).'),
  ('914', '把 v1 族退役时丢掉的 origin_stage DB 级 CHECK 补回会话族。'),
  ('915', 'recent_cache_hit_rate —— P1-B-③：给路由分补 prompt-cache 亲和维度（(credential, model) 历史 cached_tokens 的 token 加权占比）。'),
  ('916', '给会话族的 api_key_id 归一表达式建索引 —— 让「按 api_key 过滤会话」能用上【索引 seek】而不是【逐行过滤】。'),
  ('917', '输出策略拦截审核与放行的持久化（2026-10-29 拦截持久化轮）。'),
  ('919', 'P11 第二期 —— 给 ursm_snapshot_slot_daily 补上 active_minutes（每桶内有数据的「分钟子单位」数），并把填充函数升级为同步产出它。'),
  ('920', '给 node_probe_runs 补 (credential_id, completed_at) 覆盖索引，承接 INV-4「双模型探测成功门」的 24h 关联子查询。')
ON CONFLICT (version) DO NOTHING;
