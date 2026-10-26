//go:build integration

package settings

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func sessionSettingsFixture(t *testing.T) (*StoreDB, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_SESSION_SETTINGS_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_SESSION_SETTINGS_DATABASE_URL to isolated *_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated settings DB configuration")
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") || os.Getenv("TEST_SESSION_INGEST_ISOLATED") != "1" {
		t.Fatal("refusing mutating settings test outside isolated *_test database")
	}
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("open isolated settings DB failed")
	}
	t.Cleanup(db.Close)
	_, err = db.Exec(context.Background(), `DROP SCHEMA public CASCADE;CREATE SCHEMA public;
 CREATE TABLE public.settings_kv(key text PRIMARY KEY,value jsonb NOT NULL,value_type text,scope text,category text,updated_at timestamptz,prev_value jsonb,prev_updated_at timestamptz);`)
	if err != nil {
		t.Fatal(err)
	}
	prev := Global
	Global = NewRegistry()
	t.Cleanup(func() { Global = prev })
	store := NewStoreDB(db)
	Init(store)
	for _, sp := range PlatformSpecs() {
		Global.MustRegisterSpec(sp)
	}
	for _, key := range []string{KeyRequestLogsWriteEnabled, "sessions_v2.enabled", "sessions_v2.shadow_write"} {
		t.Setenv(EnvNameAuto(key), "")
	}
	return store, db
}

func seedSessionWriteState(t *testing.T, db *pgxpool.Pool, v1, enabled, shadow bool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `TRUNCATE public.settings_kv`); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]bool{KeyRequestLogsWriteEnabled: v1, "sessions_v2.enabled": enabled, "sessions_v2.shadow_write": shadow} {
		if _, err := db.Exec(context.Background(), `INSERT INTO public.settings_kv(key,value,value_type,scope,category,updated_at,prev_value) VALUES($1,to_jsonb($2::bool),'bool','platform','session',now(),to_jsonb(NOT $2::bool))`, key, value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionWriteSettingsRejectSourceLossOnSetAndRollback(t *testing.T) {
	store, db := sessionSettingsFixture(t)
	t.Run("explicit-v1-remains-a-source", func(t *testing.T) {
		seedSessionWriteState(t, db, true, false, false)
		if _, err := store.Set(ScopePlatform, KeyRequestLogsWriteEnabled, false); err == nil {
			t.Fatal("Set disabled the only default writer")
		}
		if !RequestLogsWriteEnabled() {
			t.Fatal("rejected update changed effective v1 writer")
		}
	})
	for _, key := range []string{KeyRequestLogsWriteEnabled, "sessions_v2.enabled", "sessions_v2.shadow_write"} {
		for _, operation := range []string{"set", "rollback"} {
			t.Run("reject-last-source-"+key+"-"+operation, func(t *testing.T) {
				v1 := key == KeyRequestLogsWriteEnabled
				seedSessionWriteState(t, db, v1, !v1, !v1)
				var err error
				if operation == "set" {
					_, err = store.Set(ScopePlatform, key, false)
				} else {
					_, err = store.Rollback(ScopePlatform, key, "")
				}
				if err == nil {
					t.Fatalf("%s closed the last write source", operation)
				}
				var current, previous bool
				if err := db.QueryRow(context.Background(), `SELECT value::bool,prev_value::bool FROM public.settings_kv WHERE key=$1`, key).Scan(&current, &previous); err != nil {
					t.Fatal(err)
				}
				if !current || previous {
					t.Fatalf("rejected mutation changed current=%v previous=%v", current, previous)
				}
			})
		}
	}

	t.Run("supported-transition-and-rollback", func(t *testing.T) {
		seedSessionWriteState(t, db, true, false, false)
		for _, key := range []string{"sessions_v2.enabled", "sessions_v2.shadow_write"} {
			if _, err := store.Set(ScopePlatform, key, true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Set(ScopePlatform, KeyRequestLogsWriteEnabled, false); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Rollback(ScopePlatform, KeyRequestLogsWriteEnabled, ""); err != nil {
			t.Fatal(err)
		}
		if !RequestLogsWriteEnabled() {
			t.Fatal("supported rollback did not restore v1 writer")
		}
	})
}

func TestSessionWriteSettingsConcurrentClosuresKeepAWriteSource(t *testing.T) {
	store, db := sessionSettingsFixture(t)
	// A second backend represents another gateway instance using the same DB.
	other := NewStoreDB(db)
	for attempt := 0; attempt < 10; attempt++ {
		seedSessionWriteState(t, db, true, true, true)
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; _, err := store.Set(ScopePlatform, KeyRequestLogsWriteEnabled, false); done <- err }()
		go func() { <-start; _, err := other.Set(ScopePlatform, "sessions_v2.shadow_write", false); done <- err }()
		close(start)
		rejected := 0
		for i := 0; i < 2; i++ {
			if <-done != nil {
				rejected++
			}
		}
		if rejected != 1 {
			t.Fatalf("attempt %d: rejected=%d; exactly one competing source closure must be rejected", attempt, rejected)
		}
		if !RequestLogsWriteEnabled() && !(GetPlatformBool("sessions_v2.enabled", false) && GetPlatformBool("sessions_v2.shadow_write", false)) {
			t.Fatalf("attempt %d: both write paths closed", attempt)
		}
	}
}

func TestSessionWriteSettingsConcurrentMissingRowsKeepAWriteSource(t *testing.T) {
	store, db := sessionSettingsFixture(t)
	t.Setenv("LLM_GATEWAY_SESSIONS_V2_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE", "true")
	// With absent keys, FOR UPDATE has no rows to lock. Delay the actual INSERT
	// so removing the group advisory lock reliably exposes the write skew.
	if _, err := db.Exec(context.Background(), `CREATE FUNCTION delay_setting_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.1); RETURN NEW; END $$;
 CREATE TRIGGER delay_setting_insert BEFORE INSERT ON public.settings_kv FOR EACH ROW EXECUTE FUNCTION delay_setting_insert()`); err != nil {
		t.Fatal(err)
	}
	other := NewStoreDB(db)
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := db.Exec(context.Background(), `TRUNCATE public.settings_kv`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; _, err := store.Set(ScopePlatform, KeyRequestLogsWriteEnabled, false); done <- err }()
		go func() { <-start; _, err := other.Set(ScopePlatform, "sessions_v2.shadow_write", false); done <- err }()
		close(start)
		rejected := 0
		for i := 0; i < 2; i++ {
			if <-done != nil {
				rejected++
			}
		}
		if rejected != 1 {
			t.Fatalf("absent-key attempt %d: rejected=%d; concurrent default/env closures must serialize", attempt, rejected)
		}
	}
}
