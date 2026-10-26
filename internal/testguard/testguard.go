// Package testguard decides whether a database target is safe for a test
// that destroys data, and refuses the ones that are not.
//
// # Why this package exists
//
// R87 轮实测到一类腿：它们读事实源（`scripts/audit/datasource_env.sh`）导出的
// DSN 名，然后直接 TRUNCATE / DROP 产品表，而且**从不对目标库做任何判定**。
// 其中三条读的名字叫 `TEST_AUDIT_ISOLATED_DB_URL` —— 名字的含义是「隔离审计库」
// （`scripts/audit/verify-outbox-reaper.sh` 给它的是隔离容器 `127.0.0.1:15433`），
// 而事实源却把它指向产品库 ⇒⇒ 名字与值互相矛盾，破坏性 SQL 于是落在产品库上。
//
// # The rule
//
// A target is acceptable for a destructive test when either
//
//   - its database name looks like a throwaway database
//     (scratch / e2e / tmp / bench / itgate as a whole word, or an `_it` suffix), or
//   - it is one of the repository's own local audit containers
//     (loopback, on a port that `scripts/audit/start-isolated-pg.sh` or the
//     offline-arm harness owns).
//
// The port rule exists because those containers host a database whose name is
// `llm_gateway` — the SAME name the product database has. A database-name rule
// alone would therefore reject the very target the test is written for, and a
// loopback rule alone would accept the local product PostgreSQL on 5432. Both
// halves are needed.
//
// # Skip, not Fail
//
// The check skips the test rather than failing it, because a non-isolated
// target is a statement about the ENVIRONMENT, not about the code under test:
// the honest outcome is "this leg produced no evidence here", which is what a
// skip means in this repository (`A skip is NOT evidence: …`). Failing would
// turn every ordinary run against a local product-shaped database into a red
// that says nothing about the code.
//
// The one case that DOES fail is a DSN that lies: the DSN names one database
// and the connection lands in another. That is a misconfiguration, not a
// missing environment.
//
// ★ The scratch-name pattern is deliberately the strict one that R86 轮 used
// in `bg/balance_floor_guard_integration_test.go` and
// `domains/routeincident/store_pending_lifecycle_integration_test.go`.
// `llm_gateway` (the product database) and `llm_gateway_test` (a long-lived
// shared database) are BOTH rejected.
package testguard

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AuditContainerPorts are the loopback ports owned by this repository's local
// audit PostgreSQL containers.
//
//   - 15433 — `scripts/audit/start-isolated-pg.sh` (kx-citus, the "isolated
//     audit" database that `TEST_AUDIT_ISOLATED_DB_URL` is named after);
//   - 55432 — the offline-arm kx-citus-pg17 container
//     (`deploy/sql/verify/supplier_errors_pg_test.go` documents the mapping).
//
// ★ Both containers host a database literally named `llm_gateway`, so the port
// is the only thing that distinguishes them from the product database.
// ★ Changing a port here means changing the script that owns it; the two
// lists must move together.
var AuditContainerPorts = map[int]bool{
	15433: true,
	55432: true,
}

// scratchDBName matches throwaway database names.
//
// ★ 口径与 R86 轮那两份守卫**逐字同款**（`bg/balance_floor_guard_integration_test.go`
// 与 `domains/routeincident/store_pending_lifecycle_integration_test.go`）：
// 独立词 {scratch, e2e, tmp, bench, itgate}，或库名含 `_it`。
// ⇒ `llm_gateway`（产品库）与 `llm_gateway_test`（长期库）**都不放行**。
var scratchDBName = regexp.MustCompile(`(?i)(^|_)(scratch|e2e|tmp|bench|itgate)(_|$)|_it(_|$)`)

// GuardFunctions lists the exported functions of this package that are
// **target-safety gates** — calling one of them is what makes a destructive
// leg decide whether it may run.
//
// ★ Why the declaration exists (R87 轮实测出来的洞):
//
//	The audit gate (`scripts/ursmcheck/destructive_target_guard_test.go`)
//	recognises "this leg has a target-safety guard" by looking for a call
//	into a package that performs a target check.  With package-level
//	granularity that is wrong: `RequireDatabaseMatchesDSN` also reads
//	`current_database()`, but it only **verifies** that the connection landed
//	in the database the DSN named — it never decides whether the leg may run.
//
//	⇒ ⇒ With package-level granularity, a mutation that removes
//	`RequireIsolatedTarget` (the actual gate) while keeping the verification
//	call still looks guarded ⇒ ⇒ **the audit gate would not notice**.
//	⇒ ⇒ So the guard set is declared **here**, per function, and the audit
//	gate resolves cross-package calls against this declaration.
//
// ★ Keep this list in sync with the functions that can gate a destructive leg.
//   A function that only *verifies* must NOT be listed here.
var GuardFunctions = []string{
	"RequireIsolatedTarget",
}

// Target is the parsed destination of a DSN.
type Target struct {
	Host     string
	Port     int
	Database string
	DSN      string
}

// ParseTarget parses a DSN into the host, port and database it names.
//
// ★ It uses pgconn.ParseConfig rather than string surgery: a DSN whose path is
// not literally "/dbname" would leave a naive replacement a no-op and produce
// a Target that does not describe the database the connection really reaches.
func ParseTarget(dsn string) (Target, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return Target{}, fmt.Errorf("parse dsn: %w", err)
	}
	return Target{
		Host:     cfg.Host,
		Port:     int(cfg.Port),
		Database: cfg.Database,
		DSN:      dsn,
	}, nil
}

// String renders the target without the credentials.
func (t Target) String() string {
	return fmt.Sprintf("%s:%d/%s", t.Host, t.Port, t.Database)
}

// IsIsolated reports whether the target is acceptable for a destructive test,
// and says why when it is not.
func IsIsolated(t Target) (bool, string) {
	if t.Database == "" {
		return false, "DSN 没有指名数据库（无法判断目标是不是隔离库）"
	}
	if scratchDBName.MatchString(t.Database) {
		return true, ""
	}
	if IsLoopback(t.Host) && AuditContainerPorts[t.Port] {
		return true, ""
	}
	return false, fmt.Sprintf("目标 %s 既不是一次性库（库名需含独立词 scratch/e2e/tmp/bench/itgate 或 _it），"+
		"也不是本仓的本地审计容器（回环地址 + 端口 %v）",
		t.String(), sortedPorts())
}

// IsLoopback reports whether the host is a loopback address.
func IsLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	switch h {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return strings.HasPrefix(h, "127.")
}

// RequireIsolatedTarget skips the test when the DSN does not point at an
// acceptable target, and returns the parsed target otherwise.
//
// ★ It runs BEFORE the connection is opened: a destructive leg must never
// reach the point where it is about to clear a table it was not allowed to
// touch, and a pre-flight check needs no server to be up.
//
// ★ It skips rather than fails — see the package comment for why.
func RequireIsolatedTarget(t *testing.T, dsn string) Target {
	t.Helper()
	target, err := ParseTarget(dsn)
	if err != nil {
		t.Skipf("跳过破坏性真库腿：DSN 解析失败（%v）—— 目标库无法判定 ⇒ 不执行任何破坏性 SQL", err)
	}
	ok, why := IsIsolated(target)
	if !ok {
		t.Skipf("跳过破坏性真库腿：%s；这条腿会清空目标库里的表，而目标不是隔离库 ⇒ "+
			"（跳过不构成证据：请把 DSN 指向一次性库，或指向本仓的本地审计容器）", why)
	}
	return target
}

// RequireDatabaseMatchesDSN fails when the connected database is not the one
// the DSN named.
//
// ★ This is the one case that fails rather than skips: a DSN that names one
// database and a connection that lands in another is a misconfiguration, and
// silently running against a different database than the one that was
// approved is exactly what the guard exists to prevent.
func RequireDatabaseMatchesDSN(t *testing.T, pool pgxpoolQuerier, want Target) {
	t.Helper()
	if pool == nil {
		return
	}
	var got string
	if err := pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&got); err != nil {
		t.Fatalf("守卫：读取当前库名失败：%v", err)
	}
	if got != want.Database {
		t.Fatalf("守卫：DSN 指名的库是 %q，而连接落在 %q ⇒ "+
			"目标库与被批准的库不一致，拒绝执行破坏性 SQL", want.Database, got)
	}
}

// pgxpoolQuerier is the slice of *pgxpool.Pool this package needs.
//
// ★ An interface rather than the concrete pool type: the callers pass a
// *pgxpool.Pool, but the guard only needs one query, and keeping the
// dependency to a single method keeps this package importable from tests that
// use a different pool flavour.
type pgxpoolQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sortedPorts renders the allowed container ports deterministically.
func sortedPorts() []int {
	out := make([]int, 0, len(AuditContainerPorts))
	for p := range AuditContainerPorts {
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// PortString renders a port for messages.
func PortString(p int) string { return strconv.Itoa(p) }
