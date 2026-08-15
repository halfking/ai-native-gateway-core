package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDurableTaskSchemaIsNilSafe(t *testing.T) {
	var d *DB
	if err := d.ensureDurableTaskSchema(context.Background()); err != nil {
		t.Fatalf("nil DB bootstrap returned error: %v", err)
	}
}

func TestDurableTaskMigrationDefinesDocument18Schema(t *testing.T) {
	migration := readDurableTaskMigration(t)

	required := []string{
		"create table if not exists public.durable_llm_tasks",
		"create table if not exists public.durable_llm_task_events",
		"create table if not exists public.durable_pending_outbox",
		"tenant_id text not null",
		"request_snapshot_ciphertext text not null",
		"snapshot_version integer not null",
		"encryption_key_id text not null",
		"semantic_content_committed boolean not null default false",
		"commit_state text not null default 'none'",
		"result_ciphertext text",
		"result_object_ref text",
		"result_hash text",
		"result_version bigint not null default 0",
		"policy jsonb not null default '{}'::jsonb",
		"constraint durable_llm_tasks_status_check",
		"constraint durable_llm_tasks_commit_state_check",
		"constraint durable_llm_tasks_checkpoint_not_runnable_check",
	}
	for _, fragment := range required {
		if !strings.Contains(migration, fragment) {
			t.Errorf("migration missing contract fragment %q", fragment)
		}
	}
}

func TestDurableTaskDownMigrationRemovesOwnedObjects(t *testing.T) {
	path := filepath.Join("..", "sql", "migrations", "startup", "down", "515_durable_llm_tasks.down.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable task down migration: %v", err)
	}
	down := strings.ToLower(string(b))
	for _, fragment := range []string{
		"drop table if exists public.durable_pending_outbox",
		"drop table if exists public.durable_llm_task_events",
		"drop table if exists public.durable_llm_tasks",
		"drop function if exists public.reject_durable_task_event_mutation()",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("down migration missing fragment %q", fragment)
		}
	}
}

func readDurableTaskMigration(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "sql", "migrations", "startup", "515_durable_llm_tasks.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read durable task migration: %v", err)
	}
	return strings.ToLower(string(b))
}
