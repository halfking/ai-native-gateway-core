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
}
