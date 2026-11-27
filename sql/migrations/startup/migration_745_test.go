package startup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Migration 745 (对账报表设计切片, 2026-09-24) lands the report_snapshots
// daily-rollup snapshot table. The file previously sat DEAD at the repo's
// migrations/ top level — no delivery channel at all (five-point sync fully
// missing, R63 audit) — and its UNIQUE(scope, scope_key, report_date) had no
// model dimension, so the provider×model×day granularity of design §3 could
// not be stored. This contract pins the corrected shape:
//
//	C1  The migration is registered in the revision-sequence channel —
//	    together with 800 (provider_endpoint_protocols, parallel agent moves
//	    the file into this directory; registration leads the move) — and
//	    stays in non-decreasing order. A migration that never reaches
//	    apply-db-revision-sequence.sh is inert on upgrade databases (693/
//	    699/701/703 recurrence shape).
//	C2  up/down symmetry: down drops exactly the table up creates (indexes
//	    and the UNIQUE constraint die with it).
//	C3  Idempotency: CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT
//	    EXISTS, no CONCURRENTLY (plain table — transaction-safe, which is
//	    also why it CAN ride the installer's single-transaction channel,
//	    unlike the 727/728/729/744 family).
//	C4  Granularity contract: raw_model_name TEXT NOT NULL DEFAULT ''
//	    exists (daily_by_model rows carry the raw model name, every other
//	    row carries '' as the non-model-dimension sentinel) and the UNIQUE
//	    constraint spans (scope, scope_key, report_date, raw_model_name) —
//	    the old three-key shape would collide provider×model rows.
//	C5  Installer embeddata copy stays byte-identical to the canonical
//	    (five-point sync, 730 precedent); the SSOT object file declares the
//	    same named UNIQUE constraint.
func TestMigration745ReportSnapshotsContract(t *testing.T) {
	upBytes, err := os.ReadFile("745_report_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile("745_report_snapshots.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	down := string(downBytes)
	upCompact := normalizeSQL(up)

	// ── C1: revision-sequence channel registration (745 + 800, ordered) ─
	surfacePath := filepath.Join(t.TempDir(), "deployment-migrations")
	extract := exec.Command("bash", "-c",
		`source scripts/lib/deploy-surface.sh && deploy_surface_extract scripts/apply-db-revision-sequence.sh "$1"`,
		"deploy-surface", surfacePath)
	extract.Dir = filepath.Join("..", "..", "..")
	if output, err := extract.CombinedOutput(); err != nil {
		t.Fatalf("extract deployment migrations: %v\n%s", err, output)
	}
	seq, err := os.ReadFile(surfacePath)
	if err != nil {
		t.Fatalf("read extracted deployment migrations: %v", err)
	}
	seqEntries := strings.Split(string(seq), "\n")
	channelEntries := []string{
		"745_report_snapshots.sql",
		"800_provider_endpoint_protocols.sql",
	}
	lastPos := -1
	for _, required := range channelEntries {
		// ★ 2026-11-26 审计轮判据订正：★ 判据要问的是【**通道登记**的先后】——
		//   ★ 门自己的 C1 标题写的就是 "revision-sequence channel registration (745 + 800, ordered)"
		//   ★ ⇒ ⇒ 锚点必须取 files=() 里的**登记条目原文**，而不是【全文件里第一次出现的裸文件名】★★。
		//
		// ★ 为什么必须订正（★ 实测证据，不是推测 ★）：
		//   原写法用 strings.Index(全文件, 裸文件名) ⇒ ★ 而文件正文里有一段**注释**
		//   （★ 2026-11-26 12h 审计轮那段讲「同键文件重复登记」的说明 ★）在第 313 行
		//   就提到了 800_provider_endpoint_protocols.sql ⇒ ★★ 于是**数组里的真实顺序**
		//   （★ 745 在 1232 行、★ 800 在 1282 行 ⇒ **本来就是对的** ★）被完全遮蔽
		//   ⇒ ★★ 门报「800 appears out of sequence」是**假红**（★ 数组顺序没有任何问题 ★★）。
		//
		// ★★ 方向性安全（★ 判据没有变松 ★★）：★ 改后判据**只认登记条目** ⇒
		//   若数组里真把 800 排到 745 之前 ⇒ 门照样红（★ 已做变异自证：★ 临时对调两条登记、
		//   ★ 门如期判红、★ 随即还原 ★★）⇒ ⇒ 这不是「让门变绿」的放松，是让门问对问题。
		entry := `"$ROOT_DIR/sql/migrations/startup/` + required + `"`
		pos := strings.Index(seqBody, entry)
		if pos < 0 {
			t.Fatalf("apply-db-revision-sequence.sh does not carry migration %s — upgrade-database deployments would never apply it", required)
		}
		if pos < lastPos {
			t.Fatalf("migration channel order regressed: %s appears out of sequence", required)
		}
		lastPos = pos
	}

	// ── C2: up/down symmetry ────────────────────────────────────────────
	if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS report_snapshots") {
		t.Error("745 up must create report_snapshots")
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS report_snapshots") {
		t.Error("745 down must drop report_snapshots (indexes/constraint die with the table)")
	}
	for _, stray := range []string{"CREATE TABLE", "CREATE INDEX"} {
		if strings.Contains(down, stray) {
			t.Errorf("745 down must not create anything (%s found)", stray)
		}
	}

	// ── C3: idempotent + transaction-safe (no CONCURRENTLY) ────────────
	if !strings.Contains(upCompact, "CREATE TABLE IF NOT EXISTS REPORT_SNAPSHOTS") {
		t.Error("745 must use CREATE TABLE IF NOT EXISTS (idempotent replay contract)")
	}
	if !strings.Contains(upCompact, "CREATE INDEX IF NOT EXISTS IDX_REPORT_SNAPSHOTS_SCOPE_DATE ON REPORT_SNAPSHOTS (SCOPE, REPORT_DATE DESC)") {
		t.Error("745 must keep the (scope, report_date DESC) index")
	}
	if strings.Contains(upCompact, "CONCURRENTLY") {
		t.Error("745 must not use CONCURRENTLY — plain table rides the installer single-transaction channel")
	}
	// Date-leading index stays deleted: planned readers filter (scope,
	// report_date) (API) or the full unique key (worker ON CONFLICT).
	// (Match the CREATE statement, not the bare name — the header comment
	// documents the deletion and legitimately mentions the name.)
	if strings.Contains(upCompact, "CREATE INDEX IF NOT EXISTS IDX_REPORT_SNAPSHOTS_DATE") {
		t.Error("745 must not resurrect idx_report_snapshots_date — no date-leading reader exists; re-evaluate with the consumer (see header note ③)")
	}

	// ── C4: granularity contract (raw_model_name + four-key UNIQUE) ────
	if !strings.Contains(upCompact, "RAW_MODEL_NAME TEXT NOT NULL DEFAULT ''") {
		t.Error("745 must carry raw_model_name TEXT NOT NULL DEFAULT '' ('' = non-model-dimension sentinel)")
	}
	if !strings.Contains(upCompact, "CONSTRAINT REPORT_SNAPSHOTS_SCOPE_KEY_DATE_RAW_MODEL_KEY UNIQUE (SCOPE, SCOPE_KEY, REPORT_DATE, RAW_MODEL_NAME)") {
		t.Error("745 UNIQUE must span (scope, scope_key, report_date, raw_model_name) — the old three-key shape cannot hold provider×model×day rows")
	}
	for _, scope := range []string{"daily_total", "daily_by_provider", "daily_by_model", "internal_tenant"} {
		if !strings.Contains(up, scope) {
			t.Errorf("745 scope enum comment lost %q", scope)
		}
	}

	// ── C5: installer embeddata byte-equality + SSOT constraint name ───
	embedBytes, err := os.ReadFile(filepath.Join("..", "..", "..",
		"installer", "cmd", "llm-gw-installer", "embeddata", "startup", "745_report_snapshots.sql"))
	if err != nil {
		t.Fatalf("read installer embeddata copy: %v", err)
	}
	if !bytes.Equal(embedBytes, upBytes) {
		t.Error("installer embeddata/startup/745_report_snapshots.sql differs from canonical — five-point sync broken")
	}
	ssot, err := os.ReadFile(filepath.Join("..", "..", "..", "sql", "objects", "tables", "report_snapshots.sql"))
	if err != nil {
		t.Fatalf("read SSOT object file: %v", err)
	}
	if !strings.Contains(string(ssot), "CONSTRAINT report_snapshots_scope_key_date_raw_model_key") {
		t.Error("SSOT sql/objects/tables/report_snapshots.sql must declare the same named UNIQUE constraint as the startup migration")
	}
}
