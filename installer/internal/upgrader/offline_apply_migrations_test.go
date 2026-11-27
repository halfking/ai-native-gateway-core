package upgrader

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestForwardMigrationFilesRecursesAndSkipsDownMigrations(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		"startup/540_stats_event_inbox_consumer.sql",
		"startup/540_stats_event_inbox_consumer.down.sql",
		"startup/537_usage_facts.sql",
		"startup/552_request_journey_durable_outbox.sql",
		"startup/553_approval_resume_claim.sql",
		"startup/618_request_journey_snapshot_receipts.sql",
		"startup/618_request_journey_snapshot_receipts.down.sql",
		"legacy/100_old.sql",
	}
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("SELECT 1;"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := forwardMigrationFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "legacy/100_old.sql"),
		filepath.Join(root, "startup/537_usage_facts.sql"),
		filepath.Join(root, "startup/540_stats_event_inbox_consumer.sql"),
		filepath.Join(root, "startup/552_request_journey_durable_outbox.sql"),
		filepath.Join(root, "startup/553_approval_resume_claim.sql"),
		filepath.Join(root, "startup/618_request_journey_snapshot_receipts.sql"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forward files = %#v, want %#v", got, want)
	}
}

// TestForwardMigrationFilesNumericOrderAcrossThousandBoundary 钉住数值序：
// 纯字典序会把 "1000_..." 排到 "900_..." 之前（'1'<'9'），跨千位升级包
// 会乱序执行 —— 迁移依赖按编号递增设计。
func TestForwardMigrationFilesNumericOrderAcrossThousandBoundary(t *testing.T) {
	root := t.TempDir()
	names := []string{
		"startup/900_boundary_probe.sql",
		"startup/1000_next_thousand.sql",
		"startup/980_pi_analysis_detail_views.sql",
	}
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("SELECT 1;"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := forwardMigrationFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "startup/900_boundary_probe.sql"),
		filepath.Join(root, "startup/980_pi_analysis_detail_views.sql"),
		filepath.Join(root, "startup/1000_next_thousand.sql"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forward files = %#v, want %#v (numeric order required across 999→1000)", got, want)
	}
}

func TestForwardMigrationFilesOrdersReleased973AfterItsPrerequisite(t *testing.T) {
	root := t.TempDir()
	want := []string{
		filepath.Join(root, "983_request_metrics.sql"),
		filepath.Join(root, "983b_add_raw_model_name.sql"),
		filepath.Join(root, "983c_add_occurred_cursor_index.sql"),
		filepath.Join(root, "973_request_metrics_projection.sql"),
		filepath.Join(root, "983j_request_metrics_973_repair.sql"),
	}
	for _, name := range want {
		if err := os.WriteFile(name, []byte("SELECT 1;"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := forwardMigrationFiles(root)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("forward files = %v, want %v: %v", got, want, err)
	}
}
