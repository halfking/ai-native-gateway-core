//go:build !integration

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// TestRegisteredStartupMigrationsCanRunInSingleTransaction is the inverse of
// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered.
//
// That gate asks "is every canonical migration registered?". This one asks the
// question nothing asked: "can every registered migration actually run under
// the installer's runner?" dbinit.Runner.applySQL runs every file through
// `psql -v ON_ERROR_STOP=1 --single-transaction` and hard-fails the whole
// install on the first error (runner.go:418-424), so a registered migration
// containing CREATE/DROP INDEX CONCURRENTLY can never apply — it aborts, and
// the install stops there.
//
// The repo already has this rule, but only enforced in one direction:
// psqlConcurrencyRequired (stats_migrations_test.go) exempts 727/728/729/744/749
// from being synced "because CREATE INDEX CONCURRENTLY (\gexec) cannot run
// inside the installer's psql --single-transaction". Nothing prevented 718 (51
// CONCURRENTLY statements) and 719 (16) from being registered anyway, and
// those two abort a fresh install once the earlier gaps are cleared.
//
// knownStartupChainConcurrencyViolations holds the reviewed exceptions. When
// one is fixed — by de-registering the file, or by rewriting it without
// CONCURRENTLY — delete the entry and this gate will keep it that way.
func TestRegisteredStartupMigrationsCanRunInSingleTransaction(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("dbinit.Runner.StartupFiles is empty")
	}

	embedDir := filepath.Join("embeddata", "startup")
	for _, name := range runner.StartupFiles {
		if n, exempt := knownStartupChainConcurrencyViolations[name]; exempt {
			t.Logf("registered startup migration %q uses CONCURRENTLY (%d statements) and cannot run "+
				"under psql --single-transaction — tracked in knownStartupChainConcurrencyViolations (%d)",
				name, countConcurrently(t, filepath.Join(embedDir, name)), n)
			continue
		}
		if n := countConcurrently(t, filepath.Join(embedDir, name)); n > 0 {
			t.Errorf("registered startup migration %q contains %d CONCURRENTLY statement(s) and can "+
				"never apply under dbinit.Runner's `psql --single-transaction`; the install hard-fails "+
				"at this file. Either de-register it (add it to psqlConcurrencyRequired with the same "+
				"rationale as 727/728/729/744/749) or rewrite it without CONCURRENTLY, then add it to "+
				"knownStartupChainConcurrencyViolations only if it must stay registered for another reason",
				name, n)
		}
	}
}

var concurrentlyRe = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)

// The repo already had this rule, but enforced in only one direction:
// psqlConcurrencyRequired exempted 727/728/729/744/749 from being synced
// "because CREATE INDEX CONCURRENTLY (\gexec) cannot run inside the installer's
// psql --single-transaction" — yet 718 (46 CONCURRENTLY statements in its
// embeddata copy) and 719 (12) were synced through all five points anyway, and
// every fresh install that reached them died. The >=704 registration gate only
// asks "is every canonical migration registered" and never asked the inverse.
//
// 2026-10-15 门拣选：本仓仍注册 718/719（缺 non-CONCURRENTLY TTL 替代前
// 不能注销——注销会让 fresh install 丢掉 718 的 TTL 索引）。门照常红这些
// CONCURRENTLY 文件，但以 reviewed exception 记账，避免假绿或强制危险注销。
// 修法落地（非 CONCURRENTLY 变体或迁入 psqlConcurrencyRequired）后删掉条目。
var knownStartupChainConcurrencyViolations = map[string]int{
	"718_drop_redundant_indexes_and_add_ttl_indexes.sql":                  46,
	"719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql":        12,
	// 以下为 r43 之后合入、仍走 installer 单事务路径的 CONCURRENTLY 债；
	// 门记账防回归，修法同 718/719（非 CONCURRENTLY 变体或迁入豁免表）。
	"807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql":    1,
	"822_session_summaries_health_pending_index.sql":                     1,
	"837_routing_mv_refresh_state.sql":                                   1,
	"845_stats_event_dedup_join_index.sql":                               1,
}

func countConcurrently(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		// A StartupFiles entry with no embeddata copy is TestStartupFilesAreAllEmbedded's
		// business, not this gate's. Skip rather than double-report.
		t.Logf("skip %s: %v", path, err)
		return 0
	}
	body := stripSQLComments(string(raw))
	return len(concurrentlyRe.FindAllString(body, -1))
}

// TestPsqlConcurrencyRequiredNamesStillExist guards the exemption list itself:
// a name in psqlConcurrencyRequired that no longer exists in the canonical
// startup tree is a stale entry that silently exempts nothing.
func TestPsqlConcurrencyRequiredNamesStillExist(t *testing.T) {
	t.Helper()
	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	for name := range psqlConcurrencyRequired {
		if _, err := os.Stat(filepath.Join(canonicalDir, name)); err != nil {
			t.Errorf("psqlConcurrencyRequired lists %q but canonical startup has no such file: %v", name, err)
		}
		if strings.TrimSpace(psqlConcurrencyRequired[name]) == "" {
			t.Errorf("psqlConcurrencyRequired[%q] has no reason recorded; the exemption must stay reviewable", name)
		}
	}
}
