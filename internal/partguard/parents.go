package partguard

// partitionParents 是仓内 PostgreSQL 分区父表的全集。
//
// 硬编码而非从 DDL 解析：这些声明散落在 sql/schema/01-schema.sql 与
// sql/migrations/startup/** 二十余个文件里，形态跨行且带
// IF NOT EXISTS / schema 限定，逐文件正则解析的脆弱度高于它防住的风险。
// 代价是本列表可能随新迁移漂移——由 TestParentsAreDeclaredInDDL 兜住：
// 列表里任何名字必须在仓内 DDL 中确实以 PARTITION BY 声明，否则门报红
// （既防拼错，也防"把不存在的表写进白名单"）。
//
// 本机 PG 17.10 + citus 13.3 实测该 24 个父表 relkind='p'（R78 复核）。
var partitionParents = []string{
	"auto_route_selections",
	"cache_metrics",
	"candidate_failure_logs",
	"credential_model_index",
	"credential_model_index_archive",
	"credit_ledger",
	"dashboard_access_events",
	"handoff_logs",
	"instance_heartbeats",
	"mock_probe_history",
	"model_probe_runs",
	"request_logs",
	"request_logs_archive",
	"request_logs_bodies",
	"request_wal",
	"request_wal_archive",
	"routing_decision_log",
	"routing_decision_log_archive",
	"session_bodies",
	"session_censors",
	"session_memora",
	"session_module_executions",
	"session_tools",
	"session_turn_details",
	"session_turns",
	"sessions",
	"stats_event_inbox",
	"supplier_errors",
	"system_probe_runs",
	"tool_usage_stats",
	"usage_facts",
	"usage_ledger",
	// 201 号由 TestPartitionParentsAreExhaustive 反向抓出：`sql/migrations/local/
	// 641_local_shared_platform_schema_fixup.sql:41` 声明了
	// `CREATE TABLE IF NOT EXISTS platform.platform_outbox ( … ) PARTITION BY RANGE
	// (created_at)`，此前**不在**本清单里 ⇒ 守卫看不见它上面的写操作。
	//
	// 定 P3 而非缺陷：**实测全仓 Go 代码零处写它**（只有本门测试与一个既有审计
	// 测试提到名字），所以今天没有违规；补进来是为了让清单与 DDL 一致，并让
	// 将来任何一处新增写都被本门接住。
	//
	// 注意它属 `platform` schema（与 workflow / integration 同属 local 迁移那套
	// 本地 schema），与 public 下的同名语义无关；清单按**不带 schema** 的表名登记。
	"platform_outbox",
	// 2026-10-05 R44 由 TestPartitionParentsAreExhaustive 反向抓出：
	// `sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql.skip`
	// 声明了 ursm_node_snapshot_min 的 `PARTITION BY RANGE (snapshot_ts)`，
	// 此前不在本清单里 ⇒ 守卫看不见它上面的写操作。
	//
	// 定 P3 而非缺陷：`ursm_node_snapshot_min` 的写面只有两条，且都不是
	// 父表直写 —— ① 迁移/留存路径按 `ursm_node_snapshot_min_YYYYMMDD` 分区名
	// 逐分区 DROP（`bg/` 留存器，见该迁移 M32 变异门）；② 生产写入由
	// `ensure_ursm_node_snapshot_min_daily_partition` 预建当日分区后写入分区
	// 本身。`bg/partition_825_contract_test.go` 用 9 条变异钉住这个契约。
	// 补进来是为了让清单与 DDL 一致，并让将来任何一处新增父表写都被本门接住。
	"ursm_node_snapshot_min",
}
