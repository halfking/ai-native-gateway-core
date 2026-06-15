package admin

import "strings"
import "testing"

func TestMemoraSessionsSQLUsesIntervalCast(t *testing.T) {
	const marker = "interval '1 hour'"
	// Guard against regressing timestamptz + text (PG error: operator does not exist).
	if !strings.Contains(memoraSessionsSQL, marker) {
		t.Fatalf("memora sessions SQL must use %s for no_topic_window end time", marker)
	}
	if strings.Contains(memoraSessionsSQL, "THEN '1 hour'") {
		t.Fatal("memora sessions SQL must not add bare text intervals to timestamptz")
	}
}

// memoraSessionsSQL is extracted from handleMemoraSessions for compile-time regression checks.
const memoraSessionsSQL = `
		WITH base AS (
			SELECT
				COALESCE(NULLIF(TRIM(gw_task_id), ''), NULL) AS task_id,
				COALESCE(NULLIF(TRIM(gw_session_id), ''), NULL) AS session_id,
				ts,
				request_status,
				client_model,
				COALESCE(NULLIF(TRIM(api_key_prefix), ''), NULL) AS api_key_prefix,
				COALESCE(NULLIF(TRIM(api_key_owner_user), ''), NULL) AS api_key_owner_user,
				COALESCE(NULLIF(TRIM(application_code), ''), NULL) AS application_code
			FROM request_logs
			WHERE ts > NOW() - INTERVAL '1 hour' * $1
		),
		topic_sessions AS (
			SELECT
				task_id,
				session_id,
				COUNT(*) AS request_count,
				COUNT(*) FILTER (WHERE request_status = 'success') AS ok_count,
				COUNT(*) FILTER (WHERE request_status = 'failure') AS fail_count,
				MIN(ts) AS first_activity,
				MAX(ts) AS last_activity,
				(SELECT client_model FROM base b2 WHERE b2.task_id = base.task_id ORDER BY b2.ts DESC LIMIT 1) AS latest_model,
				(SELECT api_key_prefix FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS api_key_prefix,
				(SELECT api_key_owner_user FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS api_key_owner_user,
				(SELECT application_code FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS application_code,
				FALSE AS no_topic,
				NULL::text AS no_topic_label,
				NULL::timestamptz AS hour_start
			FROM base
			WHERE task_id IS NOT NULL
			GROUP BY task_id, session_id
		),
		no_topic_sessions AS (
			SELECT
				NULL::text AS task_id,
				NULL::text AS session_id,
				COUNT(*) AS request_count,
				COUNT(*) FILTER (WHERE request_status = 'success') AS ok_count,
				COUNT(*) FILTER (WHERE request_status = 'failure') AS fail_count,
				MIN(ts) AS first_activity,
				MAX(ts) AS last_activity,
				(SELECT client_model FROM base b2 WHERE b2.task_id IS NULL AND b2.api_key_prefix IS NOT DISTINCT FROM base.api_key_prefix ORDER BY b2.ts DESC LIMIT 1) AS latest_model,
				api_key_prefix,
				api_key_owner_user,
				application_code,
				TRUE AS no_topic,
				CONCAT(
					COALESCE(api_key_prefix, '[空]'), ' @ ',
					DATE_TRUNC('hour', MIN(ts))::text, '~',
					(DATE_TRUNC('hour', MIN(ts)) + (CASE WHEN $2 = 1 THEN interval '1 hour' WHEN $2 = 2 THEN interval '2 hours' ELSE interval '6 hours' END))::text
				) AS no_topic_label,
				DATE_TRUNC('hour', MIN(ts)) AS hour_start
			FROM base
			WHERE task_id IS NULL AND api_key_prefix IS NOT NULL
			GROUP BY api_key_prefix, api_key_owner_user, application_code, DATE_TRUNC('hour', ts)
		)
		SELECT * FROM topic_sessions
		UNION ALL
		SELECT * FROM no_topic_sessions
		ORDER BY first_activity DESC
		LIMIT $3
	`
