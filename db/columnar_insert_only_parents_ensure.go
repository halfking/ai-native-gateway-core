// Package db — columnar_insert_only_parents_ensure.go
//
// 2026-10-01（252 SQL 日志审计第十七轮 §〇/§二.1 防复发）：库内
// columnar_insert_only_parents() 名单曾被手改成 21 族大名单，
// enforce_columnar_trigger（ddl_command_end 事件触发器）据此在每次
// CREATE TABLE 后把清单内所有父表的 heap 分区批量转列存——ON CONFLICT/
// UPDATE 族（不可列存）被无差别转换，打断 sessions/stats_event_inbox/
// usage_facts/session_turns 全部核心写路径（252 生产 2026-10-01
// 03:0x-06:44 事故，见 docs/audit/2026-10-01-252-sql-log-audit-round17.md）。
//
// 仓库正典 = 单族 routing_decision_log（唯一真 insert-only，
// sql/objects/functions/columnar_insert_only_parents.sql）。本 ensure 在
// 启动路径把库内实态收敛回正典，拔掉「库函数漂移 → DDL 事件放大」的
// 复发源。名单变更必须改正典文件并同步本处（双侧契约测试钉住）。
package db

import "context"

// columnarInsertOnlyParentsCanonicalBody 与
// sql/objects/functions/columnar_insert_only_parents.sql 的正典体逐字同义；
// 契约测试（db/columnar_insert_only_parents_contract_test.go 与
// sql/migrations/startup 包）双侧钉住，改名单必须两处一起改。
const columnarInsertOnlyParentsCanonicalBody = `CREATE OR REPLACE FUNCTION public.columnar_insert_only_parents() RETURNS text[]
    LANGUAGE sql STABLE
    AS $$
    SELECT ARRAY['routing_decision_log'];
$$;`

// ensureColumnarInsertOnlyParentsCanonical 幂等收敛库内名单到正典单族。
// CREATE OR REPLACE：函数体与正典一致时为 no-op；漂移时启动即自愈。
func (db *DB) ensureColumnarInsertOnlyParentsCanonical(ctx context.Context) error {
	_, err := db.pool.Exec(ctx, columnarInsertOnlyParentsCanonicalBody)
	return err
}
