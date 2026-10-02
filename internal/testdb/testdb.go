// Package testdb hands a test its own throwaway PostgreSQL database, derived
// from an existing DSN, and drops it when the test finishes.
//
// Why this exists, and why it is a database and not a schema.
//
// internal/testschema isolates a test into its own schema, which is the right
// unit whenever the code under test names its objects unqualified: search_path
// then does the routing, and nothing outside the schema can see the fixture.
//
// Migration behaviour tests cannot use that. The SQL they execute is PRODUCTION
// code — sql/migrations/startup/*.sql — and it says `public.` explicitly
// throughout (ALTER TABLE public.request_logs, CREATE FUNCTION
// public.repair_…(), CREATE TABLE public.request_logs_default). That is not
// test code, so it cannot be edited to drop the qualifier, and unqualified
// DDL would create a different object than the one production ships. For this
// family the only honest unit of isolation is a whole database.
//
// The concrete failure this removes, measured 2026-10-02 on
// ./sql/migrations/startup with the audit harness (shape=installer, 435
// relations): five tests share one helper that had two branches —
//
//	if TEST_PG_URL is set -> connect to THAT database
//	else                   -> start a dedicated testcontainer
//
// The tests were written for the second branch, i.e. for a database that
// contains nothing but their own fixture. Injecting TEST_PG_URL silently
// selected the first branch and put them on the full production-shaped
// database, where their `public.`-qualified fixtures collide with the real
// tables:
//
//	705      CREATE TABLE public.request_logs        -> 42P07
//	706-708  CREATE TABLE public.session_turns_hot   -> 42P07
//	717      CREATE TABLE public.request_logs_hot    -> 42P07
//
// and the 541 pair additionally reached DROP TABLE
// public.credential_model_bindings, which on a populated database is
// destructive rather than merely redundant (2BP01 here, and on a real
// deployment it would be data loss). A dedicated database restores the
// invariant the fixtures were written against, and keeps a destructive
// teardown contained in something disposable.
package testdb

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var plainIdentifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// Create makes a fresh empty database reachable through baseDSN's server and
// registers its removal on t. The returned DSN points at the new database.
//
// The caller must have CREATEDB. A role that does not gets a clear failure
// naming the requirement, because the alternative — silently reusing the base
// database — is exactly the bug this package exists to remove.
func Create(t *testing.T, baseDSN string) string {
	t.Helper()

	scratch := scratchName(t, baseDSN)

	adminCtx, cancelAdmin := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelAdmin()

	admin, err := pgconn.Connect(adminCtx, baseDSN)
	if err != nil {
		t.Fatalf("testdb: connect to the base database: %v", err)
	}

	// Drop first: a previous run that was killed before its cleanup leaves the
	// name behind, and CREATE DATABASE on an existing name fails. Only ever
	// drops a name this helper derived, which is why the name is validated
	// before it reaches SQL.
	if _, err := admin.Exec(adminCtx, "DROP DATABASE IF EXISTS "+scratch+" WITH (FORCE)").ReadAll(); err != nil {
		t.Logf("testdb: pre-clean DROP DATABASE %s: %v", scratch, err)
	}
	if _, err := admin.Exec(adminCtx, "CREATE DATABASE "+scratch).ReadAll(); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("testdb: CREATE DATABASE %s: %v (this test needs a role with CREATEDB; it builds "+
			"and drops its own throwaway database rather than sharing the gate database)", scratch, err)
	}
	// Release the admin connection now rather than holding it for the whole
	// test. Cleanup opens its OWN connection below.
	_ = admin.Close(context.Background())

	// Cleanup must not reuse the connection closed just above. Holding one
	// open here under a defer looks equivalent and is not: the defer fires when
	// Create returns, t.Cleanup runs later, and every DROP then failed with
	// "conn closed" — so each test leaked its database. Measured 2026-10-02:
	// 10 scratch databases left behind by two gate runs, and a leak check whose
	// LIKE pattern (`%_t%_t%`) did not match the generated names reported
	// "0" the whole time.
	t.Cleanup(func() {
		if err := dropDatabase(baseDSN, scratch); err != nil {
			t.Logf("testdb: cleanup DROP DATABASE %s: %v", scratch, err)
		}
	})

	// Rewrite the database component through the parsed config rather than by
	// string surgery on the DSN: a base DSN whose path is not literally
	// "/postgres" would leave the replacement a no-op and hand back a DSN
	// pointing at the BASE database, which is the failure this package removes.
	base, err := pgconn.ParseConfig(baseDSN)
	if err != nil {
		t.Fatalf("testdb: parse base DSN: %v", err)
	}
	scratchCfg := *base
	scratchCfg.Database = scratch
	dsn := dsnFor(&scratchCfg)

	mustLandInScratch(t, dsn, scratch)
	return dsn
}

// dropDatabase connects, drops and disconnects in one shot, so the caller
// never owns a connection it might close before using it. WITH (FORCE)
// terminates leftover backends; without it a connection the test forgot to
// close turns cleanup into a silent leak and every later run pays for the
// accumulating databases.
func dropDatabase(baseDSN, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgconn.Connect(ctx, baseDSN)
	if err != nil {
		return fmt.Errorf("connect for DROP: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)").ReadAll(); err != nil {
		return err
	}
	return nil
}

// mustLandInScratch asserts the returned DSN really resolves to the database
// this helper created. The rewrite above is a two-line operation whose only
// failure mode is silent, and a test that quietly ran against the shared gate
// database would report the shared database's result as its own.
func mustLandInScratch(t *testing.T, dsn, scratch string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("testdb: connect to the scratch database %s: %v", scratch, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	var got string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&got); err != nil {
		t.Fatalf("testdb: current_database(): %v", err)
	}
	if got != scratch {
		t.Fatalf("testdb: DSN resolves to database %q, want the scratch database %q — the test would "+
			"otherwise run against the shared gate database", got, scratch)
	}
}

// dsnFor serialises a parsed config back into a URL. pgconn.Config has no
// ConnString method, so the connection parameters are written out explicitly.
// Only the fields a test DSN actually carries are reproduced; anything exotic
// is dropped rather than guessed at, and mustLandInScratch fails loudly if the
// result does not reach the scratch database.
//
// The target database comes from cfg.Database and from nowhere else. An
// earlier version also took a `database string` argument and used that one,
// silently ignoring cfg.Database — two sources of truth for the same value.
// That is not a style complaint: a mutation of `scratchCfg.Database` then
// landed in the file, showed up in the diff, and changed nothing, which reads
// exactly like a passing guard. One source, no argument.
func dsnFor(cfg *pgconn.Config) string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))),
		Path:   "/" + cfg.Database,
	}
	if cfg.User != "" {
		if cfg.Password != "" {
			u.User = url.UserPassword(cfg.User, cfg.Password)
		} else {
			u.User = url.User(cfg.User)
		}
	}
	q := url.Values{}
	if cfg.TLSConfig == nil {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// scratchName derives a unique, plain-identifier database name. It is capped
// at 63 bytes because PostgreSQL truncates longer names, and two names that
// collide after truncation would silently share a database.
func scratchName(t *testing.T, baseDSN string) string {
	t.Helper()
	cfg, err := pgconn.ParseConfig(baseDSN)
	if err != nil {
		t.Fatalf("testdb: parse base DSN: %v", err)
	}
	base := cfg.Database
	if base == "" {
		base = "postgres"
	}
	// Compute the uniquifying suffix ONCE. Truncating a prefix against a
	// separately recomputed suffix would measure against a different nanosecond
	// than the one actually appended, and the two could straddle a digit.
	suffix := fmt.Sprintf("_t%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	// Keep the base name only as a readable prefix; the pid plus nanosecond
	// suffix is what makes concurrent packages and repeated runs distinct.
	name := sanitize(base) + suffix
	if len(name) > 63 {
		// Truncate the readable prefix, never the uniquifying suffix.
		name = sanitize(base)[:63-len(suffix)] + suffix
	}
	if !plainIdentifier.MatchString(name) {
		t.Fatalf("testdb: derived scratch database name %q is not a plain identifier", name)
	}
	return name
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
