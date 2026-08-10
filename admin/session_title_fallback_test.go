package admin

import (
	"strings"
	"testing"
)

func TestSessionTitleFallbackJoinSQLConstrainsTaskID(t *testing.T) {
	join := sessionTitleFallbackJoinSQL("s.session_id", "sd.task_id")

	checks := []string{
		"LEFT JOIN LATERAL",
		"FROM session_titles",
		"scoped_session_id = s.session_id",
		"scoped_session_id <> ''",
		"task_id = COALESCE(sd.task_id, '')",
		"task_id = 'auto'",
		"CASE WHEN task_id = COALESCE(sd.task_id, '') THEN 0 ELSE 1 END",
		"generated_at DESC",
		"LIMIT 1",
	}
	for _, check := range checks {
		if !strings.Contains(join, check) {
			t.Fatalf("join SQL missing %q\nSQL=%s", check, join)
		}
	}
}
