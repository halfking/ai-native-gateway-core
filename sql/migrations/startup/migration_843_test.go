package startup

// migration_843_test.go — 843 的契约门。
//
// 843 的缺陷是「该族没有任何以 ts 打头的索引」，所以门要钉的不只是
// 「建了索引」，而是**索引的形状对不对**：首列必须是 ts、必须非 partial。
// 建对了名字却建在错的列上、或建成 partial，两种都让缺陷原样留存，
// 而所有「索引存在」类断言照样绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const m843Base = "843_candidate_failure_logs_ts_desc_idx"
const m843 = m843Base + ".sql"

func read843(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// strip843SQLComments drops whole-line -- comments so a contract can assert on
// executed statements rather than on prose (same conservative rule as 842:
// an inline trailing comment is kept).
func strip843SQLComments(src string) string {
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestMigration843_IndexLeadsWithTs is the assertion that pins the actual defect.
// PostgreSQL only optimizes MIN/MAX on a btree's **leading** column; ts sitting
// in second position is exactly what makes max(ts) scan the whole index. A gate
// that only checked "an index was created" would stay green while the defect
// survived, so this asserts the column order.
func TestMigration843_IndexLeadsWithTs(t *testing.T) {
	body := strip843SQLComments(read843(t, m843))

	for _, tbl := range []string{
		"public.candidate_failure_logs",
		"public.candidate_failure_logs_hot",
	} {
		re := regexp.MustCompile(`(?i)ON\s+` + regexp.QuoteMeta(tbl) + `\s*\(ts\s+DESC\)`)
		require.Regexp(t, re, body,
			"%s must get an index that LEADS with ts DESC — a MIN/MAX shortcut only "+
				"applies to the leading column, and ts sitting in second position is the "+
				"defect this migration exists to remove", tbl)
	}
}

// TestMigration843_NotPartial: a partial index reproduces the defect it is meant
// to fix. The production `request_logs` family carries exactly that shape
// (idx_request_logs_discard_events_ts, WHERE discard_events IS NOT NULL) and it
// does not serve a predicate-free max(ts) — so pin it out here.
func TestMigration843_NotPartial(t *testing.T) {
	body := strip843SQLComments(read843(t, m843))

	// Split on statement boundaries, NOT line by line: the column list and its
	// WHERE clause sit on the line *after* CREATE INDEX, so a line-oriented
	// scan never sees the WHERE at all and the check is permanently true.
	for _, stmt := range strings.Split(body, ";") {
		if !strings.Contains(strings.ToUpper(stmt), "CREATE INDEX") {
			continue
		}
		require.NotContains(t, strings.ToUpper(stmt), "WHERE",
			"a partial index cannot serve max(ts), which has no WHERE clause to match: "+
				strings.Join(strings.Fields(stmt), " "))
	}
}

// TestMigration843_BothArms: the view is hot UNION ALL partitioned. Indexing one
// arm leaves the other to a full index scan, which is the failure being fixed.
func TestMigration843_BothArms(t *testing.T) {
	body := strip843SQLComments(read843(t, m843))

	require.Contains(t, body, "ON public.candidate_failure_logs (ts DESC)",
		"the partitioned parent arm needs the (ts DESC) shape")
	require.Contains(t, body, "ON public.candidate_failure_logs_hot (ts DESC)",
		"the hot arm needs the same shape; the view is UNION ALL, so a one-armed "+
			"index leaves that arm scanning its whole index")
}

// TestMigration843_Idempotent: the installer re-runs startup migrations on every
// boot; a bare CREATE INDEX would fail the second time and take the rest of the
// startup sequence with it.
func TestMigration843_Idempotent(t *testing.T) {
	body := strip843SQLComments(read843(t, m843))
	createCount := strings.Count(body, "CREATE INDEX")
	require.NotZero(t, createCount)
	require.Equal(t, createCount, strings.Count(body, "CREATE INDEX IF NOT EXISTS"),
		"every CREATE INDEX in 843 must be IF NOT EXISTS — startup migrations re-run on every boot")
}

// TestMigration843_NoConcurrently: CREATE INDEX CONCURRENTLY is rejected outright
// on a partitioned parent (measured, PG 17.11: "cannot create index on partitioned
// table concurrently"). Leaving it in would fail at apply time.
func TestMigration843_NoConcurrently(t *testing.T) {
	body := strip843SQLComments(read843(t, m843))
	require.NotContains(t, strings.ToUpper(body), "CONCURRENTLY",
		"CREATE INDEX CONCURRENTLY is unsupported on partitioned parents; the migration "+
			"would fail at apply time and block the remaining startup migrations")
}

// TestMigration843_DownOnlyDropsIndexes: rollback must not touch data.
func TestMigration843_DownOnlyDropsIndexes(t *testing.T) {
	down := strip843SQLComments(read843(t, m843Base+".down.sql"))
	up := strings.ToUpper(down)
	require.Contains(t, down, "DROP INDEX IF EXISTS public.candidate_failure_logs_ts_desc_idx")
	require.Contains(t, down, "DROP INDEX IF EXISTS public.candidate_failure_logs_hot_ts_desc_idx")
	for _, forbidden := range []string{"DELETE", "TRUNCATE", "DROP TABLE", "DROP COLUMN"} {
		require.NotContains(t, up, forbidden,
			"rollback must only drop indexes — %s would be irreversible data loss", forbidden)
	}
}

// TestMigration843_RegisteredInInstallerEmbeddata: the installer's map lookup
// reads from embeddata, not from sql/migrations. A migration only in the repo
// would silently never apply on a fresh install.
func TestMigration843_RegisteredInInstallerEmbeddata(t *testing.T) {
	src := read843(t, m843)
	cp := read843(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer",
		"embeddata", "startup", m843))
	require.Equal(t, src, cp, "installer embeddata copy of 843 must be byte-identical to the repo source")
}

// TestMigration843_RegisteredInAutoStartupSequence: embed + map + dbinit list.
func TestMigration843_RegisteredInAutoStartupSequence(t *testing.T) {
	mainGo := read843(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	runnerGo := read843(t, filepath.Join("..", "..", "..", "installer", "internal", "dbinit", "runner.go"))

	require.Contains(t, mainGo, "//go:embed embeddata/startup/"+m843, "843 must be embedded")
	require.Contains(t, mainGo, "candidateFailureLogsTsDescIdx843 []byte")
	require.Contains(t, mainGo, `"startup/`+m843+`":`, "843 must be in embeddedSQLFiles")
	require.Contains(t, runnerGo, `"843_candidate_failure_logs_ts_desc_idx.sql"`,
		"843 must be in dbinit's startup file list, not just the embed map")
}

// TestMigration843_RegisteredInUpgradeChannel pins the leg that has now been
// missed eight times (693/699/701/703, then 815/816, then 817, then 837-841,
// then 842). A migration can have a complete installer leg and still never reach
// databases upgraded from an older revision — fresh installs get it, everyone
// else silently does not. Once a newer migration lands, an unregistered one
// becomes permanently invisible to the highest-number guard.
func TestMigration843_RegisteredInUpgradeChannel(t *testing.T) {
	seq := read843(t, filepath.Join("..", "..", "..", "scripts", "apply-db-revision-sequence.sh"))

	// A live array entry looks like:
	//     "$ROOT_DIR/sql/migrations/startup/<name>.sql"
	// so the match is on the path + filename, NOT on a bare quoted filename —
	// the char before the number is '/', so `"<name>"` never appears verbatim.
	// Commented-out entries are skipped, same as the channel's own gate.
	var registered bool
	for _, line := range strings.Split(seq, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "sql/migrations/startup/") && strings.Contains(line, m843) {
			registered = true
			break
		}
	}
	require.True(t, registered,
		"843 must be registered in apply-db-revision-sequence.sh's files array — "+
			"without it, databases upgraded from an older revision never receive this migration")
}
