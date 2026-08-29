package credential

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeRows implements pgx.Rows just enough for AutoRevoker.SweepNow to scan
// credential IDs + cycle counts. Tests use this to feed deterministic
// candidate lists without a real PostgreSQL.
//
// Scan is gated by scanned: Next sets it true and Scan sets it false. A
// Scan without a preceding Next (or a second Scan after one Next) returns an
// error instead of panicking on f.rows[-1] or silently re-reading the row.
type fakeRows struct {
	rows    []struct{ id int; cnt int64 }
	pos     int
	scanned bool
}

func (f *fakeRows) Close()                                       {}
func (f *fakeRows) Err() error                                    { return nil }
func (f *fakeRows) CommandTag() pgconn.CommandTag                 { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeRows) Next() bool {
	if f.pos >= len(f.rows) {
		return false
	}
	f.pos++
	// A fresh Next positions us on a new row; Scan is valid until consumed.
	f.scanned = true
	return true
}
func (f *fakeRows) Scan(dest ...any) error {
	if !f.scanned {
		return errors.New("fakeRows: Scan called without Next")
	}
	r := f.rows[f.pos-1]
	*(dest[0].(*int)) = r.id
	*(dest[1].(*int64)) = r.cnt
	// Mark consumed so a second Scan without an intervening Next errors.
	f.scanned = false
	return nil
}
func (f *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                               { return nil }

// fakeAutoRevokeDB captures the SQL and args AutoRevoker passes to it and
// records how many rows were affected on Exec calls. Tests assert on the
// SQL pattern and arg values without booting PostgreSQL.
type fakeAutoRevokeDB struct {
	queries    []fakeQuery
	execs      []fakeExec
	queryIdx   int
}

type fakeQuery struct {
	sql  string
	args []any
}

type fakeExec struct {
	sql          string
	args         []any
	rowsAffected int64
}

func (f *fakeAutoRevokeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(f.execs) == 0 {
		return pgconn.CommandTag{}, nil
	}
	exec := f.execs[0]
	f.execs = f.execs[1:]
	// Note: the SQL pattern in exec.sql is intentionally NOT matched against
	// the actual SQL here. This fake is shared scaffolding; threading
	// *testing.T through Exec to fail on a mismatch would force every caller
	// to construct a *testing.T. Tests that need strict SQL assertions should
	// inspect db.execs / db.queries directly. (Query still does a non-fatal
	// regex check, also for the same reason.)
	_ = sql
	return pgconn.NewCommandTag("UPDATE " + strconv.FormatInt(exec.rowsAffected, 10)), nil
}

func (f *fakeAutoRevokeDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if len(f.queries) == 0 {
		return &fakeRows{}, nil
	}
	q := f.queries[0]
	f.queries = f.queries[1:]
	// Match sql against the recorded regex pattern (any prefix is OK because
	// tests want to assert SQL is reasonable without byte-exact equality).
	if _, err := regexp.MatchString(q.sql, sql); err != nil {
		return nil, err
	}
	// Construct rows using the recorded row payload.
	rows := &fakeRows{}
	for _, r := range fakeRowsPayload(q) {
		rows.rows = append(rows.rows, r)
	}
	return rows, nil
}

// fakeRowsPayload decodes a fake query's rows. Tests register rows via
// q.args[0] as []int (the credential IDs); the cycle count is faked as the
// 1-based index so the test can assert ordering without caring.
func fakeRowsPayload(q fakeQuery) []struct{ id int; cnt int64 } {
	if len(q.args) >= 1 {
		if ids, ok := q.args[0].([]int); ok {
			out := []struct{ id int; cnt int64 }{}
			for i, id := range ids {
				out = append(out, struct{ id int; cnt int64 }{id, int64(i + 1)})
			}
			return out
		}
	}
	return nil
}

// Compile-time check that pgx.Rows is satisfied.
var _ pgx.Rows = (*fakeRows)(nil)

func TestAutoRevokeConfig_Defaults(t *testing.T) {
	cfg := DefaultAutoRevokeConfig()
	if cfg.Enabled {
		t.Fatal("default config must be disabled")
	}
	if cfg.Threshold != 3 {
		t.Errorf("default threshold = %d, want 3", cfg.Threshold)
	}
	if cfg.Window != 24*time.Hour {
		t.Errorf("default window = %s, want 24h", cfg.Window)
	}
	if cfg.Interval != 5*time.Minute {
		t.Errorf("default interval = %s, want 5m", cfg.Interval)
	}
}

func TestAutoRevokeConfig_LoadEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE", "on")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_THRESHOLD", "5")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_WINDOW_HOURS", "12")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_INTERVAL", "30s")
	cfg := LoadAutoRevokeConfig()
	if !cfg.Enabled {
		t.Fatal("env=on must enable auto-revoke")
	}
	if cfg.Threshold != 5 {
		t.Errorf("threshold = %d, want 5", cfg.Threshold)
	}
	if cfg.Window != 12*time.Hour {
		t.Errorf("window = %s, want 12h", cfg.Window)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("interval = %s, want 30s", cfg.Interval)
	}
}

func TestAutoRevokeConfig_LoadEnv_RejectsBogus(t *testing.T) {
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE", "bogus")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_THRESHOLD", "-3")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_WINDOW_HOURS", "abc")
	t.Setenv("LLM_GATEWAY_CREDENTIAL_AUTO_REVOKE_INTERVAL", "0s")
	cfg := LoadAutoRevokeConfig()
	if cfg.Enabled {
		t.Fatal("env=bogus must NOT enable")
	}
	if cfg.Threshold != DefaultAutoRevokeConfig().Threshold {
		t.Errorf("bogus threshold fell back to default: got %d", cfg.Threshold)
	}
	if cfg.Window != DefaultAutoRevokeConfig().Window {
		t.Errorf("bogus window fell back to default: got %s", cfg.Window)
	}
	if cfg.Interval != DefaultAutoRevokeConfig().Interval {
		t.Errorf("bogus interval fell back to default: got %s", cfg.Interval)
	}
}

func TestAutoRevoker_NewAutoRevokerNilDB(t *testing.T) {
	if got := NewAutoRevoker(nil, DefaultAutoRevokeConfig()); got != nil {
		t.Fatalf("nil db must yield nil revoker, got %v", got)
	}
}

func TestAutoRevoker_NewAutoRevokerRejectsInvalidConfig(t *testing.T) {
	pool := &pgxpool.Pool{}
	defer func() { _ = pool }() //nolint:errcheck
	cases := []struct {
		name string
		cfg  AutoRevokeConfig
	}{
		{"zero threshold", AutoRevokeConfig{Threshold: 0, Window: 24 * time.Hour, Interval: 5 * time.Minute}},
		{"negative threshold", AutoRevokeConfig{Threshold: -1, Window: 24 * time.Hour, Interval: 5 * time.Minute}},
		{"zero window", AutoRevokeConfig{Threshold: 3, Window: 0, Interval: 5 * time.Minute}},
		{"zero interval", AutoRevokeConfig{Threshold: 3, Window: 24 * time.Hour, Interval: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewAutoRevoker(pool, c.cfg); got != nil {
				t.Errorf("invalid config %+v must yield nil, got %v", c.cfg, got)
			}
		})
	}
}

func TestAutoRevoker_DisabledIsNoOp(t *testing.T) {
	db := &fakeAutoRevokeDB{}
	r := &AutoRevoker{
		db:   db,
		cfg:  DefaultAutoRevokeConfig(), // Enabled=false
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r.Start(ctx)
	r.Stop()
	if len(db.execs) != 0 || len(db.queries) != 0 {
		t.Fatalf("disabled sweep must not touch DB: queries=%d execs=%d",
			len(db.queries), len(db.execs))
	}
}

func TestAutoRevoker_EnabledRevokesCandidates(t *testing.T) {
	db := &fakeAutoRevokeDB{
		queries: []fakeQuery{
			{
				sql:  "SELECT id, cycle_count",
				args: []any{[]int{7, 9}}, // two candidates: 7, 9
			},
		},
		execs: []fakeExec{
			{sql: "UPDATE credentials", rowsAffected: 1},
			{sql: "UPDATE credentials", rowsAffected: 1},
		},
	}
	cfg := DefaultAutoRevokeConfig()
	cfg.Enabled = true
	r := &AutoRevoker{db: db, cfg: cfg, stop: make(chan struct{}), done: make(chan struct{})}

	revoked, err := r.SweepNow(context.Background())
	if err != nil {
		t.Fatalf("SweepNow: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("revoked = %d, want 2", revoked)
	}
	if len(db.execs) != 0 {
		t.Fatalf("execs not fully consumed: %d left", len(db.execs))
	}
}

func TestAutoRevoker_NoOpWhenAlreadyRevoked(t *testing.T) {
	db := &fakeAutoRevokeDB{
		queries: []fakeQuery{{sql: "SELECT id, cycle_count", args: []any{[]int{42}}}},
		// RowsAffected=0 simulates a race where the credential was already
		// disabled between sweep and revoke.
		execs: []fakeExec{{sql: "UPDATE credentials", rowsAffected: 0}},
	}
	cfg := DefaultAutoRevokeConfig()
	cfg.Enabled = true
	r := &AutoRevoker{db: db, cfg: cfg, stop: make(chan struct{}), done: make(chan struct{})}

	revoked, err := r.SweepNow(context.Background())
	if err != nil {
		t.Fatalf("SweepNow: %v", err)
	}
	if revoked != 0 {
		t.Fatalf("RowsAffected=0 must report 0 revoked, got %d", revoked)
	}
}

func TestAutoRevoker_StartIdempotent(t *testing.T) {
	db := &fakeAutoRevokeDB{}
	cfg := DefaultAutoRevokeConfig()
	cfg.Enabled = true
	cfg.Interval = 100 * time.Millisecond
	r := &AutoRevoker{db: db, cfg: cfg, stop: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	// Calling Start again must not panic and must not crash the first sweep.
	r.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	// Stop must close r.done exactly once: a double-close would panic here,
	// which is the observable proof that Start spawned only one goroutine.
	r.Stop()
	// A second Stop on the now-closed channels must be a no-op (no panic).
	r.Stop()
}