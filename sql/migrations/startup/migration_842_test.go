package startup

// migration_842_test.go — 842 的契约门。
//
// 842 只加两个索引，不改任何查询。门钉的是「**该加的加了、且加在正确的表上**」：
// 事故形态是索引建对了名字却建在错的表上（hot 表那份漏了，两臂形状就不一致），
// 或者只建了父表那份 —— 门必须能抓住这两种。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const m842Base = "842_credential_model_index_latest_bucket_idx"
const m842 = m842Base + ".sql"

func read842(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// strip842SQLComments removes -- comment lines so a contract can assert on the
// executed statements rather than on prose. It only drops whole-line comments;
// an inline trailing comment would be kept, which is the conservative choice.
func strip842SQLComments(src string) string {
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestMigration842_AddsBothArms: the view is hot UNION ALL partitioned, so a
// single index leaves one arm on a sequential scan. Both must be present.
func TestMigration842_AddsBothArms(t *testing.T) {
	body := strip842SQLComments(read842(t, m842))

	require.Contains(t, body, "ON public.credential_model_index (credential_id, raw_model, bucket DESC)",
		"the partitioned parent needs the (credential_id, raw_model, bucket) shape — the existing "+
			"index leads with bucket and cannot serve GROUP BY credential_id, raw_model")
	require.Contains(t, body, "ON public.credential_model_index_hot (credential_id, raw_model, bucket DESC)",
		"the hot arm needs the same shape; the view is UNION ALL, so a one-armed index leaves "+
			"that side on a sequential scan")
}

// TestMigration842_Idempotent: the installer re-runs startup migrations on every
// boot. A bare CREATE INDEX would fail the second time and, depending on the
// runner, abort the remaining migrations behind it.
func TestMigration842_Idempotent(t *testing.T) {
	body := strip842SQLComments(read842(t, m842))
	createCount := strings.Count(body, "CREATE INDEX")
	require.NotZero(t, createCount)
	require.Equal(t, createCount, strings.Count(body, "CREATE INDEX IF NOT EXISTS"),
		"every CREATE INDEX in 842 must be IF NOT EXISTS — startup migrations re-run on every boot")
}

// TestMigration842_NoConcurrently: CREATE INDEX CONCURRENTLY is not supported on
// a partitioned parent in PostgreSQL; leaving it in would fail at apply time and
// take the rest of the startup sequence with it.
func TestMigration842_NoConcurrently(t *testing.T) {
	body := strip842SQLComments(read842(t, m842))
	require.NotContains(t, strings.ToUpper(body), "CONCURRENTLY",
		"CREATE INDEX CONCURRENTLY is unsupported on partitioned parents; the migration would "+
			"fail at apply time and block the remaining startup migrations")
}

// TestMigration842_DownOnlyDropsIndexes: rollback must not touch data.
func TestMigration842_DownOnlyDropsIndexes(t *testing.T) {
	down := strip842SQLComments(read842(t, m842Base+".down.sql"))
	up := strings.ToUpper(down)
	require.Contains(t, down, "DROP INDEX IF EXISTS public.credential_model_index_cred_model_bucket_idx")
	require.Contains(t, down, "DROP INDEX IF EXISTS public.credential_model_index_hot_cred_model_bucket_idx")
	for _, forbidden := range []string{"DELETE", "TRUNCATE", "DROP TABLE", "DROP COLUMN"} {
		require.NotContains(t, up, forbidden,
			"rollback must only drop indexes — %s would be irreversible data loss", forbidden)
	}
}

// TestMigration842_RegisteredInInstallerEmbeddata: the installer's map lookup
// reads from embeddata, not from sql/migrations. A migration that is only in
// the repo would silently never apply on a fresh install.
func TestMigration842_RegisteredInInstallerEmbeddata(t *testing.T) {
	src := read842(t, m842)
	cp := read842(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer",
		"embeddata", "startup", m842))
	require.Equal(t, src, cp, "installer embeddata copy of 842 must be byte-identical to the repo source")
}

func TestMigration842_RegisteredInAutoStartupSequence(t *testing.T) {
	mainGo := read842(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	runnerGo := read842(t, filepath.Join("..", "..", "..", "installer", "internal", "dbinit", "runner.go"))

	require.Contains(t, mainGo, "//go:embed embeddata/startup/"+m842, "842 must be embedded")
	require.Contains(t, mainGo, "credentialModelIndexLatestBucketIdx842 []byte")
	require.Contains(t, mainGo, `"startup/`+m842+`":`, "842 must be in embeddedSQLFiles")
	require.Contains(t, runnerGo, `"842_credential_model_index_latest_bucket_idx.sql"`,
		"842 must be in dbinit's startup file list, not just the embed map")
}
