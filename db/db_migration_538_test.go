package db

import (
	"os"
	"strings"
	"testing"
)

func TestApplyMigrationsIncludesMigration538Ensure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureNodeProbeTriggerKindSchema(migCtx)",
		"func (d *DB) ensureNodeProbeTriggerKindSchema",
		"to_regclass('public.node_probe_runs')",
		"to_regclass('public.credential_probe_queue')",
		"node_probe_runs_trigger_kind_check",
		"credential_probe_queue_source_check",
		"'selfcheck'",
		"'external_async'",
		// 950（2026-11-07）：queue source 闭集第 7 值必须同时存在于
		// ensure 臂 —— 否则 init-local-db.sh 链（domain/349 → 5 值 →
		// boot 自愈）会把 950 的修复静默撤回（第 50 增量 F-2）。
		"'credential_recovery'",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing migration 538 contract %q", want)
		}
	}
}
