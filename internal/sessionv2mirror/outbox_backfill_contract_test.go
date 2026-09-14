package sessionv2mirror

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestBackfillProjectionKeysMatchEntryTags pins the GAP-2 backfill
// contract: every jsonb_build_object key in scripts/audit/
// mirror_outbox_backfill.sql must be a real RequestLogEntry json tag.
//
// Why: the reaper unmarshals outbox payloads straight into the struct, so
// a key that drifts from a tag (column renamed, tag renamed) is silently
// ignored and that field is lost from every backfilled row — exactly the
// silent-drift class the storage gate exists to catch.
//
// Known-good exceptions: the backfill projects request_logs.ts as
// 'event_at' (the entry has no ts tag) and is_final_success as 'success'.
func TestBackfillProjectionKeysMatchEntryTags(t *testing.T) {
	const sqlPath = "../../scripts/audit/mirror_outbox_backfill.sql"
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read backfill SQL: %v", err)
	}

	allowed := map[string]bool{
		"request_id": true, // projected verbatim from the column of the same name
		"event_at":   true, // ts → event_at rename
		"success":    true, // is_final_success → success rename
	}

	tags := map[string]bool{}
	typ := reflect.TypeOf(telemetry.RequestLogEntry{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			tags[name] = true
		}
	}

	keyRe := regexp.MustCompile(`'([a-z0-9_]+)',\s*$`)
	var bad []string
	for _, line := range strings.Split(string(raw), "\n") {
		m := keyRe.FindStringSubmatch(strings.TrimSpace(strings.TrimRight(line, ",")))
		if m == nil {
			continue
		}
		key := m[1]
		if allowed[key] || tags[key] {
			continue
		}
		bad = append(bad, key)
	}
	if len(bad) > 0 {
		t.Fatalf("backfill payload keys that are not RequestLogEntry json tags: %v", bad)
	}
}
