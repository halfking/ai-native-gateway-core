package admin

import "strings"
import "testing"

func TestMemoraSessionsSQLUsesIntervalCast(t *testing.T) {
	const marker = "interval '1 hour'"
	sql, _ := buildMemoraSessionsSQL(nil, 24, 1, 50)
	if !strings.Contains(sql, marker) {
		t.Fatalf("memora sessions SQL must use %s for no_topic_window end time", marker)
	}
	if strings.Contains(sql, "THEN '1 hour'") {
		t.Fatal("memora sessions SQL must not add bare text intervals to timestamptz")
	}
}
