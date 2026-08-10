package admin

import "fmt"

// sessionTitleFallbackJoinSQL returns the canonical JOIN used by admin session
// endpoints to surface auto-generated/manual session titles.
//
// session_titles has no tenant_id column; its isolation key is the pair
// (task_id, scoped_session_id). Always constrain both: prefer the real task_id
// from session_dim, then allow legacy auto_title_generator rows with
// task_id='auto'. LATERAL + LIMIT 1 avoids duplicating the outer session row
// when both rows exist.
func sessionTitleFallbackJoinSQL(sessionIDExpr, taskIDExpr string) string {
	return fmt.Sprintf(`LEFT JOIN LATERAL (
			SELECT title FROM session_titles
			WHERE scoped_session_id = %s
			  AND scoped_session_id <> ''
			  AND (task_id = COALESCE(%s, '') OR task_id = 'auto')
			ORDER BY CASE WHEN task_id = COALESCE(%s, '') THEN 0 ELSE 1 END,
			         generated_at DESC
			LIMIT 1
		) st ON true`, sessionIDExpr, taskIDExpr, taskIDExpr)
}
