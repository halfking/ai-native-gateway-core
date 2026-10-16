package trace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

// Opt-in only: an explicitly supplied loopback PG17 instance, never application DSNs.
func retirementPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TRACE_RETIREMENT_TEST_DSN")
	if dsn == "" {
		t.Skip("TRACE_RETIREMENT_TEST_DSN not supplied")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated PG configuration")
	}
	localSchema := os.Getenv("TRACE_RETIREMENT_LOCAL_SCHEMA") == "1" && cfg.ConnConfig.Port == 54318
	if (cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost") || (!strings.HasPrefix(cfg.ConnConfig.Database, "trace_retirement") && !localSchema) {
		t.Fatal("requires loopback trace_retirement database or explicitly authorized local 54318 schema isolation")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var version int
	if err := admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version/10000 != 17 {
		t.Fatalf("requires PG17: version=%d err=%v", version, err)
	}
	t.Logf("isolated PostgreSQL version_num=%d", version)
	schema := fmt.Sprintf("trace_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"session_turns_hot", "session_turns", "_request_logs_hot", "_request_logs"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (request_id text, ts timestamptz DEFAULT now(), request_status text, trace_events jsonb)"); err != nil {
			t.Fatal(err)
		}
	}
	_, err = pool.Exec(ctx, `CREATE TABLE request_stage_events (
 tenant_id text, request_id text, seq int, stage text, module text, event_timestamp timestamptz,
 duration_ms int, status text, error_message text, http_status int, response_body text,
 failure_hint text, details jsonb, snapshot jsonb, UNIQUE(request_id, seq))`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

type retirementSettings bool

func (b retirementSettings) Get(_ settings.Scope, key string) ([]byte, error) {
	if key == settings.KeyRequestLogsWriteEnabled {
		return []byte(fmt.Sprint(bool(b))), nil
	}
	return nil, nil
}
func (retirementSettings) Set(settings.Scope, string, any) ([]byte, error) { return nil, nil }
func (retirementSettings) GetTenant(string, string) ([]byte, error)        { return nil, nil }
func (retirementSettings) SetTenant(string, string, any) ([]byte, error)   { return nil, nil }
func retirementGate(t *testing.T, enabled bool) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	reg := settings.NewRegistry()
	for _, spec := range settings.StorageSpecs() {
		reg.MustRegisterSpec(spec)
	}
	reg.RegisterBackend(settings.ScopePlatform, retirementSettings(enabled))
	settings.Global = reg
	if settings.RequestLogsWriteEnabled() != enabled {
		t.Fatal("gate fixture not effective")
	}
}

func TestTraceRetirementFlushPG17(t *testing.T) {
	pool := retirementPG(t)
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	rec := NewRedisRecorder(rdb)
	raw := `{"events":[{"seq":1,"stage":"request_complete","module":"handler","timestamp":"2026-10-15T00:00:00Z","status":"success"}],"final_status":"success"}`
	for _, tc := range []struct {
		name, table string
		gate        bool
	}{
		{"hot", "session_turns_hot", false}, {"cold", "session_turns", false},
		{"enabled_session_priority", "session_turns_hot", true}, {"legacy_compatibility", "_request_logs_hot", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retirementGate(t, tc.gate)
			if _, err := pool.Exec(ctx, "INSERT INTO "+tc.table+" (request_id) VALUES ($1)", tc.name); err != nil {
				t.Fatal(err)
			}
			if tc.table != "_request_logs_hot" {
				if _, err := pool.Exec(ctx, `INSERT INTO _request_logs_hot(request_id,trace_events) VALUES($1,'{"events":[],"final_status":"legacy"}')`, tc.name); err != nil {
					t.Fatal(err)
				}
			}
			if err := rdb.Set(ctx, keyFor(tc.name), raw, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			if err := rec.FlushToPG(ctx, pool, tc.name); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := pool.QueryRow(ctx, "SELECT trace_events->>'final_status' FROM "+tc.table+" WHERE request_id=$1", tc.name).Scan(&status); err != nil || status != "success" {
				t.Fatalf("session trace not persisted: status=%q err=%v", status, err)
			}
			if mini.Exists(keyFor(tc.name)) {
				t.Fatal("persisted trace retained in Redis")
			}
			if tc.table != "_request_logs_hot" {
				if err := pool.QueryRow(ctx, `SELECT trace_events->>'final_status' FROM _request_logs_hot WHERE request_id=$1`, tc.name).Scan(&status); err != nil || status != "legacy" {
					t.Fatalf("legacy row changed: %q %v", status, err)
				}
			}
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM request_stage_events WHERE request_id=$1", tc.name).Scan(&n); err != nil || n != 1 {
				t.Fatalf("stage events: %d %v", n, err)
			}
		})
	}
	t.Run("zero_rows_keeps_redis_then_retries", func(t *testing.T) {
		retirementGate(t, false)
		if _, err := pool.Exec(ctx, `INSERT INTO _request_logs_hot(request_id,trace_events) VALUES('late','{"final_status":"legacy"}')`); err != nil {
			t.Fatal(err)
		}
		_ = rdb.Set(ctx, keyFor("late"), raw, time.Minute).Err()
		if err := rec.FlushToPG(ctx, pool, "late"); !errors.Is(err, ErrTraceParentNotFound) {
			t.Fatalf("zero rows error = %v", err)
		}
		if !mini.Exists(keyFor("late")) {
			t.Fatal("zero rows deleted Redis")
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT trace_events->>'final_status' FROM _request_logs_hot WHERE request_id='late'`).Scan(&status); err != nil || status != "legacy" {
			t.Fatalf("stop-write changed orphan legacy: %q %v", status, err)
		}
		_, err := pool.Exec(ctx, `INSERT INTO session_turns_hot(request_id) VALUES('late')`)
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.FlushToPG(ctx, pool, "late"); err != nil {
			t.Fatal(err)
		}
		if mini.Exists(keyFor("late")) {
			t.Fatal("retry failed to delete persisted trace")
		}
	})
	t.Run("cancel_preserves_redis", func(t *testing.T) {
		retirementGate(t, false)
		_ = rdb.Set(ctx, keyFor("cancel"), raw, time.Minute).Err()
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := rec.FlushToPG(canceled, pool, "cancel"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled flush did not honor caller context: %v", err)
		}
		if !mini.Exists(keyFor("cancel")) {
			t.Fatal("canceled flush deleted Redis")
		}
	})
	t.Run("stage_failure_preserves_redis", func(t *testing.T) {
		retirementGate(t, false)
		if _, err := pool.Exec(ctx, `INSERT INTO session_turns_hot(request_id) VALUES('stage_retry'); ALTER TABLE request_stage_events RENAME TO unavailable_stage_events`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `ALTER TABLE unavailable_stage_events RENAME TO request_stage_events`) })
		if err := rdb.Set(ctx, keyFor("stage_retry"), raw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rec.FlushToPG(ctx, pool, "stage_retry"); err == nil {
			t.Fatal("stage failure was swallowed")
		}
		if !mini.Exists(keyFor("stage_retry")) {
			t.Fatal("stage failure deleted Redis before normalized persistence")
		}
		got, err := LoadFromPG(ctx, pool, "stage_retry")
		if err != nil || got == nil || got.FinalStatus != FinalSuccess {
			t.Fatalf("primary session persistence lost: %+v %v", got, err)
		}
	})
}

func TestTraceRetirementLoadPG17(t *testing.T) {
	pool := retirementPG(t)
	ctx := context.Background()
	for i, table := range []string{"session_turns_hot", "session_turns", "_request_logs_hot", "_request_logs"} {
		id := fmt.Sprintf("read_%d", i)
		_, err := pool.Exec(ctx, "INSERT INTO "+table+"(request_id,trace_events) VALUES($1,$2::jsonb)", id, `{"events":[],"final_status":"`+table+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := LoadFromPG(ctx, pool, id)
		if err != nil || got == nil || string(got.FinalStatus) != table {
			t.Fatalf("%s read=%+v err=%v", table, got, err)
		}
	}
	for _, table := range []string{"session_turns_hot", "session_turns"} {
		id := "empty_" + table
		_, err := pool.Exec(ctx, "INSERT INTO "+table+"(request_id) VALUES($1)", id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO _request_logs_hot(request_id,trace_events) VALUES($1,'{"final_status":"stale"}')`, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := LoadFromPG(ctx, pool, id)
		if err != nil || got != nil {
			t.Fatalf("empty session fell back to stale legacy: %+v %v", got, err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO session_turns_hot(request_id,trace_events) VALUES('priority','{"final_status":"hot"}'); INSERT INTO session_turns(request_id,ts,trace_events) VALUES('priority',now()+interval '1 day','{"final_status":"cold"}'); INSERT INTO _request_logs_hot(request_id,trace_events) VALUES('priority','{"final_status":"legacy"}')`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadFromPG(ctx, pool, "priority")
	if err != nil || got == nil || got.FinalStatus != "hot" {
		t.Fatalf("hot priority: %+v %v", got, err)
	}
	got, err = LoadFromPG(ctx, pool, "missing")
	if err != nil || got != nil {
		t.Fatalf("missing: %+v %v", got, err)
	}
}
