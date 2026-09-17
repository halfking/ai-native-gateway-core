-- 719: ensure 约束影蔽索引根治 + request_logs 父索引跨通道所有权归一
-- + tool_usage_stats_hot ASC/DESC 近重复收敛（R38，2026-09-17）
--
-- 承接 718 遗留三类（R37 §四#2）：
--
-- A. db.go/db_omnifree.go ensure 创建的 9 个约束影蔽索引——718 只登记不动，
--    因为 drop 会被每进程启动的 ensure 复活。本迁移与 ensure 侧删除联动
--    （同 commit 移除 CREATE），drop 后无创建者，根治写放大复活循环。
--    判等口径同 718（b=约束影蔽 / c=方向影蔽经 backward scan / d=partial ⊂
--    全量），逐个亲核：
--      idx_applications_tenant_code   (tenant_id,code) WHERE enabled ⊂ UNIQUE applications_tenant_id_code_key
--      idx_licenses_key               (license_key) ⊂ license_key UNIQUE
--      idx_oar_request                (request_id) ⊂ request_id UNIQUE
--      idx_releases_version           (version) ⊂ version UNIQUE
--      idx_cc_command_id              (command_id) ⊂ command_id UNIQUE
--      idx_isr_instance               (instance_id,timestamp DESC) ⊂ PK (instance_id,timestamp)
--      idx_keyless_providers_enabled  (provider_code) WHERE enabled ⊂ UNIQUE (provider_code,tenant_id) 最左前缀
--      idx_keyless_providers_auto_combo (provider_code) WHERE enabled AND allowlist ⊂ 同上
--      idx_free_resource_catalog_provider (provider_code) ⊂ UNIQUE (provider_code,model_id,tenant_id) 最左前缀
--
-- B. request_logs 父表索引所有权冲突对归一（R37 718-B2 撤回时实证）：
--    idx_request_logs_parent_ts（ensure db.go + startup/013 + 三份 baseline，
--    ON ONLY 父索引 + ATTACH 叶子）↔ idx_request_logs_parent_request_id
--    （deploy/V369 与 v3 手工通道 202609_01，同列同向同谓词但异名；两父索引
--    各挂 5 个 attached 叶子 = 每张月度叶子同一列组合双份索引存储）。
--    归一裁决：**parent_ts 通道胜出**（ensure+013+三份 baseline 三点持有，
--    分区轮转管理器按它走）；drop parent_request_id 整棵分区索引树
--    （父 + 5 叶子 ..._ts_idx1 族，plain DROP 级联）。
--    202609_01 的同名单列变体（若某环境跑过该通道）同样被本 drop 覆盖。
--
-- C. tool_usage_stats_hot 三对 ASC/DESC 近重复（718 登记待定夺，R38 定夺）：
--    表经 LIKE ... INCLUDING ALL 从 tool_usage_stats 继承了自动命名的 ASC 索引，
--    348 又显式建了 DESC 版本。查询面亲核（R38）：真实消费路径只有
--    registry/usage_stats.go 的 upsert（走 UNIQUE）与 promote 函数（日期范围
--    扫描）；toolexecution/postgres_store.go 的 tool_name/date 查询与活表列
--    集不符（tool_id/usage_date）且本机表为空，属死路径。全部查询为等值/
--    范围 + 单列 ORDER BY（backward scan 双向可服务），无混合方向多列排序，
--    ASC/DESC 三对互为冗余。**保留 348 显式 DESC 三件**（in-repo 确定性创建
--    者 + 语义与"最近优先"查询一致），drop LIKE 继承 ASC 三件：
--      tool_usage_stats_hot_tool_id_usage_date_idx  ⊂ tool_usage_stats_hot_tool_date_idx (DESC)
--      tool_usage_stats_hot_tenant_id_usage_date_idx ⊂ tool_usage_stats_hot_tenant_date_idx (DESC)
--      tool_usage_stats_hot_usage_date_idx          ⊂ tool_usage_stats_hot_date_idx (DESC)
--    （created_at 单列索引无对偶，保留。）
--
-- 锁说明：同 718——分区父表 DROP INDEX 不支持 CONCURRENTLY（元数据级短暂
-- ACCESS EXCLUSIVE，级联 detach/drop 叶子索引），非分区表一律 CONCURRENTLY。
-- 生产执行建议低峰；本机实测见 R38 轮文档。
-- down 为文档化 no-op（同 718 惯例：重建冗余索引只恢复写放大）。

-- ── A. ensure 根治的约束影蔽索引（CONCURRENTLY，非分区表）──────────────────
DROP INDEX CONCURRENTLY IF EXISTS idx_applications_tenant_code;
DROP INDEX CONCURRENTLY IF EXISTS idx_licenses_key;
DROP INDEX CONCURRENTLY IF EXISTS idx_oar_request;
DROP INDEX CONCURRENTLY IF EXISTS idx_releases_version;
DROP INDEX CONCURRENTLY IF EXISTS idx_cc_command_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_isr_instance;
DROP INDEX CONCURRENTLY IF EXISTS idx_keyless_providers_enabled;
DROP INDEX CONCURRENTLY IF EXISTS idx_keyless_providers_auto_combo;
DROP INDEX CONCURRENTLY IF EXISTS idx_free_resource_catalog_provider;

-- ── B. request_logs 父索引所有权归一（plain，分区父表级联叶子）──────────────
DROP INDEX IF EXISTS idx_request_logs_parent_request_id;

-- ── C. tool_usage_stats_hot ASC/DESC 收敛（CONCURRENTLY）────────────────────
DROP INDEX CONCURRENTLY IF EXISTS tool_usage_stats_hot_tool_id_usage_date_idx;
DROP INDEX CONCURRENTLY IF EXISTS tool_usage_stats_hot_tenant_id_usage_date_idx;
DROP INDEX CONCURRENTLY IF EXISTS tool_usage_stats_hot_usage_date_idx;
