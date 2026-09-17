-- 718: 冗余索引清理 + TTL/claim 缺失索引补齐（R37 SQL 专项审计，2026-09-17）
--
-- 背景：R37 六路子代理审计 + 本机 pg_index 逐对亲核发现，迁移历史与表创建 DDL
-- 叠加在同一批列上留下了多套等价索引（列集/谓词/排序方向函数等价），在最热的
-- 写入表上造成 N 倍写放大（每次 INSERT 维护多套同构 btree），并拖慢 autovacuum。
-- 每个被 drop 的索引均已核实：
--   ① 不支撑任何约束（pg_constraint.conindid 为空，或存在同列约束索引/唯一索引）；
--   ② 无 Go ensure 链创建者（db/db.go、db_omnifree.go 均不创建，drop 后不会被复活）；
--   ③ 无代码按名引用（除 credential_model_index_hot 保留 auto_index_refresher
--      错误信息中引用的 idx_credential_model_index_hot_unique）。
-- 判等口径（保守，全部为函数等价）：
--   a. 定义完全相同的重复对（多迁移交叠创建）；
--   b. UNIQUE 约束/PKEY 影蔽的同列普通索引；
--   c. 排序方向相反的等价 btree（backward scan 覆盖）；
--   d. 全量索引影蔽的更窄谓词 partial 索引。
-- 登记为遗留（本轮不动）：tool_usage_stats_hot 三对 ASC/DESC 近重复（混合排序
-- 语义需确认）；db.go ensure 创建的 8 个约束影蔽索引（applications/center_commands/
-- licenses/offline_activation_requests/releases/instance_status_reports/keyless/
-- free_resource_catalog）；request_logs 月度叶子分区上的局部重复索引（attached
-- 归属需逐叶确认，父表 idx_request_logs_ts_desc 的 drop 会级联清掉其 attached 副本）。
--
-- 补索引：request_envelope / sticky_sessions 两表此前零二级索引，cleaner 的
-- DELETE ... WHERE expires_at < now() 走 Seq Scan（EXPLAIN 实证）；新增
-- expires_at btree。session_aggregate_outbox 的 claim 查询（OR 双分支 +
-- ORDER BY next_retry_at）在 36 万 done 行存量上 Seq Scan，补 partial 索引
-- 同时覆盖两个分支并满足排序。
--
-- 锁说明：分区父表上的 DROP INDEX 不支持 CONCURRENTLY（PG 限制），持锁为
-- 元数据级短暂 ACCESS EXCLUSIVE；非分区表一律 CONCURRENTLY。生产执行建议低峰。
-- 下行脚本为文档化 no-op：重复索引不应被重建（见 down 文件）。

-- ── A. 分区父表（plain DROP，级联 attached 叶子副本）────────────────────────
DROP INDEX IF EXISTS idx_request_logs_ts_desc;             -- ts 方向影蔽于 idx_request_logs_ts
DROP INDEX IF EXISTS idx_ih_instance_ts;                   -- instance_heartbeats pkey 方向影蔽

-- ── B. 热路径表（CONCURRENTLY）───────────────────────────────────────────────
DROP INDEX CONCURRENTLY IF EXISTS idx_request_logs_hot_request_id;            -- pkey(request_id) 影蔽
DROP INDEX CONCURRENTLY IF EXISTS udx_request_wal_hot_request_id;             -- pkey(request_id) 影蔽
DROP INDEX CONCURRENTLY IF EXISTS request_wal_hot_tenant_id_created_at_idx;   -- 与 idx_request_wal_hot_tenant_created 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_routing_decision_log_hot_request_ts;    -- 约束 request_id_ts_key 影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_routing_decision_log_hot_ts;            -- 与 routing_decision_log_hot_ts_idx 同构
DROP INDEX CONCURRENTLY IF EXISTS routing_decision_log_hot_tenant_id_ts_idx;  -- partial ⊂ idx_..._tenant_ts 全量
DROP INDEX CONCURRENTLY IF EXISTS routing_decision_log_hot_request_id_idx;    -- 最左前缀 ⊂ (request_id, ts) 唯一
DROP INDEX CONCURRENTLY IF EXISTS idx_cfl_hot_session_ts;                     -- 与 candidate_failure_logs_hot_session_id_ts_idx 同构
DROP INDEX CONCURRENTLY IF EXISTS credential_model_index_hot_unique_key;                          -- 三重 UNIQUE 之一
DROP INDEX CONCURRENTLY IF EXISTS credential_model_index_hot_bucket_credential_id_raw_model_idx;  -- 三重 UNIQUE 之三（保留 idx_credential_model_index_hot_unique）
DROP INDEX CONCURRENTLY IF EXISTS idx_cmb_credential_provider_model;          -- 约束 cmb_unique_credential_model 影蔽
DROP INDEX CONCURRENTLY IF EXISTS credit_ledger_hot_created_idx;              -- 方向影蔽 _created_at_idx
DROP INDEX CONCURRENTLY IF EXISTS credit_ledger_hot_ref_idx;                  -- partial ⊂ _ref_type_ref_id_idx
DROP INDEX CONCURRENTLY IF EXISTS credit_ledger_hot_tenant_created_idx;       -- 方向影蔽 _tenant_id_created_at_idx
DROP INDEX CONCURRENTLY IF EXISTS idx_dae_hot_errors;                         -- partial ⊂ idx_dae_hot_timestamp

-- ── B2. 复核补漏（718 首跑后对账发现，R37 真库验证批）────────────────────────
-- request_logs 分区父表 ON ONLY 上的 partial 同构对——会作为 attached 索引
-- 传播到每一个月度叶子，是最热表上的双份维护成本。
DROP INDEX IF EXISTS idx_request_logs_parent_ts;           -- 与 idx_request_logs_parent_request_id 逐字同构
DROP INDEX CONCURRENTLY IF EXISTS idx_quality_profiles_provider;       -- 与 idx_pqp_provider_id 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_quality_profiles_quality_score;  -- 与 idx_pqp_quality_score 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_request_logs_bodies_hot_ts;      -- 与 request_logs_bodies_hot_ts_idx 同构

-- ── C. 中频/配置表（CONCURRENTLY）────────────────────────────────────────────
DROP INDEX CONCURRENTLY IF EXISTS idx_credential_probe_model_log_created;     -- 与 ..._created_at 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_analysis_events_claimable;              -- 与 idx_analysis_events_unprocessed 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_cache_metrics_default_tenant_ts;        -- 与分区原生名同构
DROP INDEX CONCURRENTLY IF EXISTS idx_cache_metrics_2026_08_event_type;       -- 同上（存量叶子）
DROP INDEX CONCURRENTLY IF EXISTS idx_cache_metrics_2026_09_event_type;       -- 同上（存量叶子）
DROP INDEX CONCURRENTLY IF EXISTS idx_gray_release_rules_release_id;          -- 与 idx_gray_rules_release 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_instance_release_status_status;         -- 与 ensure 管理的 idx_instance_status_status 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_irs_status;                             -- 同上（三重之第三）
DROP INDEX CONCURRENTLY IF EXISTS idx_instance_release_status_release_id;     -- 与 idx_instance_status_release 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_irs_release;                            -- partial ⊂ 同列全量
DROP INDEX CONCURRENTLY IF EXISTS idx_kba_tenant;                             -- 与 idx_knowledge_base_acl_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_kb_tenant;                              -- 与 idx_knowledge_bases_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_ke_tenant;                              -- 与 idx_knowledge_entities_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_kl_tenant;                              -- 与 idx_knowledge_lineages_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_km_tenant;                              -- 与 idx_knowledge_metadata_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_kr_tenant;                              -- 与 idx_knowledge_relations_tenant 同构
DROP INDEX CONCURRENTLY IF EXISTS idx_goal_sessions_session;                  -- 约束 goal_sessions_session_id_key 影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_approval_requests_request_id;           -- 非约束 UNIQUE 影蔽于约束 _key
DROP INDEX CONCURRENTLY IF EXISTS idx_approval_configs_tenant;                -- 约束影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_doc_tools_tasks_token;                  -- 约束影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_document_chunks_document;               -- 约束影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_dedup_stats_date;                       -- uq_dedup_stats 方向影蔽
DROP INDEX CONCURRENTLY IF EXISTS idx_intent_config_tenant;                   -- partial ⊂ intent_classifier_config_unique_tenant
DROP INDEX CONCURRENTLY IF EXISTS idx_gi_license_key_hash;                    -- partial ⊂ idx_gi_license
DROP INDEX CONCURRENTLY IF EXISTS idx_hosted_tasks_dispatch_scan;             -- 窄 partial ⊂ idx_hosted_tasks_deadline 同列宽 partial

-- ── D. 缺失索引补齐（CONCURRENTLY）───────────────────────────────────────────
-- request_envelope / sticky_sessions：此前零二级索引，cleaner 全表扫（R37 实证）。
CREATE INDEX CONCURRENTLY IF NOT EXISTS request_envelope_expires_at_idx
    ON public.request_envelope (expires_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS sticky_sessions_expires_at_idx
    ON public.sticky_sessions (expires_at);
-- session_aggregate_outbox：claim 查询（pending 到期 OR claimed 租约过期，
-- ORDER BY next_retry_at LIMIT 1）在 done 行主导的存量上 Seq Scan；
-- partial 索引同时服务两个分支、排除 done、并满足排序。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_aggregate_outbox_claimable
    ON public.session_aggregate_outbox (next_retry_at)
    WHERE status IN ('pending', 'claimed');
