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
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forward files = %#v, want %#v", got, want)
	}
}
