package startup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig928File     = "928_probe_traffic_physical_lane.sql"
	mig928DownFile = "928_probe_traffic_physical_lane.down.sql"
	mig928TSVSeq   = "328"
	mig928SHA      = "31bec328e87f207f273f8dfb17ac65a77b5c1e5a208976d74ff985257b5b3eac"
)

func mig928Read(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读 %s：%v", rel, err)
	}
	return string(raw)
}

func TestMigration928RegisteredEverywhere(t *testing.T) {
	canonical := mig928Read(t, mig928File)
	downCanonical := mig928Read(t, mig928DownFile)

	sum := sha256.Sum256([]byte(canonical))
	if got := hex.EncodeToString(sum[:]); got != mig928SHA {
		t.Fatalf("canonical sha mismatch: got %s want %s（改了 SQL 必须同步本常量与 db-changelog）", got, mig928SHA)
	}

	embedUp := mig928Read(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig928File)
	if embedUp != canonical {
		t.Error("installer embeddata 副本（up）必须与 canonical 逐字节一致")
	}
	embedDown := mig928Read(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig928DownFile)
	if embedDown != downCanonical {
		t.Error("installer embeddata 副本（down）必须与 canonical 逐字节一致")
	}

	installerGo := mig928Read(t, "../../../installer/cmd/llm-gw-installer/main.go")
	runnerGo := mig928Read(t, "../../../installer/internal/dbinit/runner.go")
	seqSh := mig928Read(t, "../../../scripts/apply-db-revision-sequence.sh")
	tsv := mig928Read(t, "../../../sql/schema/installed_startup_migrations.tsv")
	changelog := mig928Read(t, "../../../docs/db-changelog.md")

	points := []struct {
		label  string
		body   string
		needle string
	}{
		{"main.go go:embed", installerGo, "//go:embed embeddata/startup/" + mig928File},
		{"main.go map", installerGo, `"startup/` + mig928File + `"`},
		{"runner StartupFiles", runnerGo, `"` + mig928File + `"`},
		{"deploy files=()", seqSh, `startup/` + mig928File + `"`},
		{"TSV", tsv, mig928TSVSeq + "\t" + mig928File},
		{"db-changelog sha", changelog, "`" + mig928SHA + "`"},
	}
	for _, p := range points {
		n := strings.Count(p.body, p.needle)
		if n != 1 {
			t.Errorf("%s: needle %q 出现 %d 次（要恰好 1）", p.label, p.needle, n)
		}
	}

	if !strings.Contains(canonical, "session_turns_probe_hot") {
		t.Error("up migration must create session_turns_probe_hot")
	}
	if !strings.Contains(canonical, "request_stats_probe_minute") {
		t.Error("up migration must create request_stats_probe_minute")
	}
	if !strings.Contains(canonical, "BEGIN;") || !strings.Contains(canonical, "COMMIT;") {
		t.Error("migration must wrap SET LOCAL in an explicit transaction")
	}
	if !strings.Contains(downCanonical, "DROP TABLE IF EXISTS public.session_turns_probe_hot") {
		t.Error("down must drop session_turns_probe_hot")
	}
}
