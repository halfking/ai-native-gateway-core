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
// throughout (ALTER TABLE public.session_turns, CREATE FUNCTION
// public.repair_…(), CREATE TABLE public.session_turns_hot). That is not
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
//	705      CREATE TABLE public._request_logs        -> 42P07
//	706-708  CREATE TABLE public.session_turns_hot   -> 42P07
//	717      CREATE TABLE public._request_logs_hot    -> 42P07
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
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var plainIdentifier = regexp.MustCompile(`^[a-z0-9_]+$`)

// cleanupTimeout bounds every administrative statement this package issues
// against the base server (CREATE DATABASE / DROP DATABASE).
//
// Why 120s and not less: measured 2026-10-21 on PG 17.10 under heavy load —
// after pg_terminate_backend, DROP DATABASE still waits for the SIGTERM'd
// backend to finish, and that wait was observed at 4.28s, 38.60s and 89.37s.
// A shorter bound turns a legitimate cleanup into a failed one, and a failed
// cleanup is a leaked database — the opposite of what the bound is for. The
// bound still has to exist: an unbounded statement is the failure mode this
// package's cleanup exists to remove.
const cleanupTimeout = 120 * time.Second

// signalReraiseWindow 是「重发的信号」被投递的窗口。
//
// ★ 为什么需要它（2026-10-21 实测，Go 1.27.2 darwin/arm64）：
// `Process.Signal` 是**异步**投递 ⇒ 若紧接着就 os.Exit，**兜底总是赢得竞争**
// ⇒ 重发的信号从未被处理 ⇒ 进程是「退出码 143」而不是「死于信号」。
// 实测：不等待 ⇒ 5/5 exited-with-code(143)；等 1s ⇒ 5/5 died-by-signal(terminated)。
const signalReraiseWindow = time.Second

// signalDropBudget 是**信号路径上「全部 DROP」的总预算**。
//
// 2026-10-22 审计修正：此前各库的 DROP 串行、且每个各带一份 cleanupTimeout
// （120s）⇒ N 个库的最坏总时长 = 120s×N ⇒ 进程可能在 DROP 落库前被
// supervisor 的 SIGKILL 打死 ⇒ 库残留（恰是这个 handler 要防的形态）。
// ⇒ 信号路径：并发 DROP + 一个总预算。
//
// 为什么是 20s 而不是 cleanupTimeout 的 120s：信号路径的目标是
// 「在被打死**之前**把库清掉并退出」，而不是「无限期等数据库」。
// 预算耗尽 ⇒ 放弃剩余的并**如实报告**（stderr）—— 可见的残余
// 优于「进程占着 SIGTERM 不退、最后被 SIGKILL 静默打死」那种不可见的残余。
// 注意 DROP 的等待实测可达 89.37s ⇒ 20s 预算下**可能**真的清不掉；
// 这是有意的取舍：留痕比假装清干净重要。
const signalDropBudget = 20 * time.Second

// ── Signal-path cleanup ───────────────────────────────────────────────────
//
// t.Cleanup runs on the normal test path and on t.Fatal, but NOT when the
// process is terminated by a signal: the Go runtime does not run deferred
// functions on SIGTERM/SIGINT. Measured consequence: a test run killed by a CI
// timeout (or Ctrl-C) leaves its throwaway database behind — the exact case
// the cleanup exists to prevent.
//
// The handler below closes that path. It keeps a process-wide registry of the
// databases this package created, drops them when a signal arrives, then
// restores the default disposition and re-raises the signal, so the process
// still dies "because it was signalled" instead of exiting quietly with a
// different status.
//
// ★★ R99 轮 · §19.20.70 实测订正：上面那句「still dies 'because it was signalled'」
// 在 2026-10-21 之前是**失实**的 —— `Process.Signal` 异步投递 ⇒ 紧跟着的
// os.Exit(143) 总是赢得竞争 ⇒ 重发的信号从未生效 ⇒ 实际退出形态是
// 「exited-with-code(143)」而不是「died-by-signal」。
// ⇒ 现在重发之后有 signalReraiseWindow 的等待窗口 ⇒ 实测 5/5 died-by-signal ⇒
// ⇒ 注释与实现这才对齐（★ 端到端真机门：internal/testdb/signal_cleanup_realdb_test.go）。
//
// The registry is process-wide rather than per-test because signal.Notify
// installs a process-wide handler: one per test would let the last test's
// handler replace the first's, and the earlier databases would be orphaned.
var (
	dropMu      sync.Mutex
	dropTargets = map[string]string{} // database name -> base DSN
	dropOnce    sync.Once
)

// registerDropTarget records a database that must be dropped if this process
// is terminated by a signal.
func registerDropTarget(baseDSN, name string) {
	dropMu.Lock()
	dropTargets[name] = baseDSN
	dropMu.Unlock()
	dropOnce.Do(installSignalDrop)
}

// unregisterDropTarget forgets a database that has already been dropped.
func unregisterDropTarget(name string) {
	dropMu.Lock()
	delete(dropTargets, name)
	dropMu.Unlock()
}

// installSignalDrop wires the single process-wide signal handler.
func installSignalDrop() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig, ok := <-ch
		if !ok {
			return
		}
		dropAllForSignal()
		// Restore the default disposition and re-raise, so the process still
		// dies from the signal — the supervisor sees a signalled death, not a
		// clean exit that looks like a successful test run.
		//
		// ★★★ R99 轮 · §19.20.70 实测订正（★ 这段注释在改之前是**失实**的）：
		//   `Process.Signal` 是【**异步**投递】⇒ 紧跟着的 os.Exit(143) **总是赢得竞争**
		//   ⇒ 兜底先跑完 ⇒ 重发的信号**从未被处理** ⇒ 进程实际是「**退出码 143**」，
		//   而不是「死于信号」（Go 的 exec 能分：ExitCode()==143 vs ExitCode()==-1
		//   且 WaitStatus.Signaled()==true；★ bash 的 wait **分不出** ⇒ 别用 shell 观测）。
		//   ⇒ 修法：重发之后给信号一个【被投递的窗口】—— 实测等 1s ⇒ 5/5 变成
		//   died-by-signal(terminated) ⇒ 这时上面那句注释才与实现对齐。
		signal.Stop(ch)
		if s, ok := sig.(syscall.Signal); ok {
			signal.Reset(s)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(s)
			}
		}
		// ★ Bounded window for the re-raised signal to actually be delivered.
		//   Without it the fallback below always wins the race (measured
		//   2026-10-21: 5/5 exited-with-code(143), 0/5 died-by-signal).
		time.Sleep(signalReraiseWindow)
		// Fallback for a signal that could not be re-raised: still exit
		// non-zero, never 0 — a killed run must not look like a passing one.
		//
		// 2026-10-22 审计修正：退出码按**实际信号**取 128+signo
		// （惯例：SIGINT⇒130、SIGTERM⇒143）。此前恒用 143 ⇒ 触发方是
		// SIGINT 时退出码谎报成 SIGTERM，调用方分辨不出是哪一类中断。
		exitCode := 143
		if s, ok := sig.(syscall.Signal); ok && s == syscall.SIGINT {
			exitCode = 130
		}
		os.Exit(exitCode)
	}()
}

// dropAllForSignal drops every registered database.
//
// 2026-10-22 审计修正：此前**串行**、且每个库的 DROP 各自带 120s 超时
// ⇒ N 个库的最坏总时长 = 120s×N + 重发窗口 ⇒ 进程可能在 DROP 落库前被
// supervisor 的 SIGKILL 打死 ⇒ **库残留** —— 恰是这个 handler 要防的形态。
// 实测单库 DROP 等待已达 4.28 / 38.60 / 89.37s（见本文件 cleanupTimeout 注释）。
// ⇒ 改成：**并发** DROP + 一个**总预算** signalDropBudget。
// 预算耗尽 ⇒ 放弃剩余的并**如实报告**（stderr）—— 宁可留可见的残余，
// 也不要「进程无限期占着 SIGTERM、最后被 SIGKILL 静默打死」那种看不见的残余。
func dropAllForSignal() {
	dropMu.Lock()
	targets := make(map[string]string, len(dropTargets))
	for k, v := range dropTargets {
		targets[k] = v
	}
	dropMu.Unlock()
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), signalDropBudget)
	defer cancel()
	var wg sync.WaitGroup
	for name, dsn := range targets {
		wg.Add(1)
		go func(name, dsn string) {
			defer wg.Done()
			if err := dropDatabaseCtx(ctx, dsn, name); err != nil {
				fmt.Fprintf(os.Stderr, "testdb: signal cleanup DROP DATABASE %s: %v\n", name, err)
			}
		}(name, dsn)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(os.Stderr,
			"testdb: signal cleanup budget %s exhausted — some scratch databases may survive\n",
			signalDropBudget)
	}
}

// GuardFunctions lists the exported functions of this package that are
// **target-safety gates**.
var GuardFunctions = []string{
	"Create",
}

// Create makes a fresh empty database reachable through baseDSN's server and
// registers its removal on t. The returned DSN points at the new database.
//
// The caller must have CREATEDB. A role that does not gets a clear failure
// naming the requirement, because the alternative — silently reusing the base
// database — is exactly the bug this package exists to remove.
func Create(t *testing.T, baseDSN string) string {
	t.Helper()

	scratch := scratchName(t, baseDSN)

	// ★ Register before the CREATE so a signal arriving mid-setup still finds
	//   the name registered. DROP DATABASE IF EXISTS makes a registration for a
	//   database that never came up harmless.
	registerDropTarget(baseDSN, scratch)

	adminCtx, cancelAdmin := context.WithTimeout(context.Background(), cleanupTimeout)
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
			// ★ 2026-10-22 12h 审计轮（C5）：**DROP 失败时不得摘掉信号兜底**。
			//   原形态：无论 DROP 成没成功都 `unregisterDropTarget(scratch)`
			//   ⇒ DROP 失败（超时/连不上）⇒ **库已经泄漏** ⇒ 而它**恰好**
			//     被从兜底注册表里移除 ⇒ 最需要信号路径兜住的那个库失去了兜底。
			//   ⇒ 修法：只在成功时摘。
			t.Logf("testdb: cleanup DROP DATABASE %s: %v (保留信号兜底登记)", scratch, err)
			return
		}
		unregisterDropTarget(scratch)
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
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return dropDatabaseCtx(ctx, baseDSN, name)
}

// dropDatabaseCtx 是 dropDatabase 的 ctx 感知版本：信号路径用它把
// 「全部 DROP」压进一个**总预算**（signalDropBudget），而不是每个库各吃
// 一份 cleanupTimeout。
func dropDatabaseCtx(ctx context.Context, baseDSN, name string) error {
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
