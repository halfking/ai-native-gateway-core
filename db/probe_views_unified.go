package db

import "fmt"

// probe_views_unified.go — 节点状态派生的单一事实源(SQL 片段)。
//
// 2026-09-17 统一数据源改造:所有"探测健康"展示面(probe-health 视图族、
// 凭据列表 probe_state、热力图 node_status)必须从同一派生规则读取
// node_probe_state,与路由判定(last_direct_ok=false AND next_retry_at>now()
// 剔除)保持一致。旧 model_probe_state 在 useNewProbeMode 下停更,曾导致
// probe-health 显示冻结的 healthy 而热力图/真实请求已是 410/403。
//
// 修改派生规则时:
//   - 本函数是唯一改动点;
//   - sql/migrations/startup/716_unify_probe_health_views.sql 与
//     sql/objects/views/v_node_probe_state_compat.sql 需同步
//     (TestNodeProbeStateCaseSQL_MigrationSync 会拦截漂移)。

// NodeProbeStateCaseSQL returns the canonical node-state derivation CASE
// expression referencing the given table alias (e.g. "nps"). Derived
// vocabulary (兼容旧 model_probe_state 展示词汇):
//
//	unknown           手动下线(model-toggle manual_offline,paused 且标记)
//	probing           探测在途(in_flight_until > now)或尚无 direct 结果
//	healthy_confirmed last_direct_ok = true(探测或真实请求成功回写)
//	broken_confirmed  last_direct_ok = false 且连续失败 >= 3(路由已剔除)
//	suspicious        last_direct_ok = false 且连续失败 < 3
func NodeProbeStateCaseSQL(alias string) string {
	return fmt.Sprintf(`CASE
		WHEN %[1]s.paused = TRUE AND %[1]s.last_err_code = 'manual_offline' THEN 'unknown'
		WHEN %[1]s.in_flight_until IS NOT NULL AND %[1]s.in_flight_until > NOW() THEN 'probing'
		WHEN %[1]s.last_direct_ok IS NULL THEN 'probing'
		WHEN %[1]s.last_direct_ok THEN 'healthy_confirmed'
		WHEN %[1]s.consecutive_failures >= 3 THEN 'broken_confirmed'
		ELSE 'suspicious'
	END`, alias)
}

// NodeProbeCompatViewSQL returns the full CREATE OR REPLACE VIEW statement for
// v_node_probe_state_compat — node_probe_state 投影成旧 model_probe_state 列
// 词汇(state/last_status/total_attempts/...),旧读取方改表名即可迁移。
func NodeProbeCompatViewSQL() string {
	return `CREATE OR REPLACE VIEW v_node_probe_state_compat AS
		SELECT
		    nps.credential_id,
		    nps.raw_model_name,
		    ` + NodeProbeStateCaseSQL("nps") + ` AS state,
		    CASE
		        WHEN nps.consecutive_failures >= 3 THEN 'urgent'
		        WHEN nps.consecutive_failures >= 1 THEN 'suspicious'
		        ELSE 'watchdog'
		    END AS probe_priority,
		    nps.consecutive_successes,
		    nps.consecutive_failures,
		    (nps.consecutive_successes + nps.consecutive_failures) AS total_attempts,
		    nps.last_attempt_at,
		    nps.next_retry_at,
		    nps.last_err_code AS last_status,
		    nps.updated_at AS last_state_change_at,
		    nps.last_run_id AS last_state_change_run,
		    nps.paused,
		    nps.in_flight_until,
		    nps.last_direct_ok,
		    nps.last_gateway_ok,
		    nps.last_err_detail,
		    nps.updated_at
		FROM node_probe_state nps`
}
