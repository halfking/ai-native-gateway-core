package startup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	mig928File     = "928_probe_traffic_physical_lane.sql"
	mig928DownFile = "928_probe_traffic_physical_lane.down.sql"
	mig928TSVSeq   = "328"
	mig928SHA      = "31bec328e87f207f273f8dfb17ac65a77b5c1e5a208976d74ff985257b5b3eac"
)

// mig928DeployRoundHeaderRe 识别 db-changelog 里的**部署轮记录**小节。
//
// ★★ 为什么要把「部署轮记录」从计数范围里剔出去（★ 2026-11-04 12h 审计轮）：
//
//	docs/db-changelog.md 里两类记录的**语义不同**：
//	  · **迁移登记记录**（`## <日期> — <说明> (migration NNN)`）
//	    ⇒ 「这个迁移是什么、登记在哪些地方」⇒ **登记有且只有一处**。
//	  · **部署轮记录**（`## <ISO 时间戳>Z — deploy <主机> build_seq <号> (<sha>)`）
//	    ⇒ 「这一轮部署实际应用了哪些迁移、各自的字节指纹是多少」
//	    ⇒ ★ 它**必然**要重复列出各迁移的 sha256 —— 那正是它的价值
//	      （历史指纹：文件后来改了，这里记的仍是「当时应用的那份」的指纹）。
//	⇒ ⇒ 「sha 在 db-changelog 里恰好出现 1 次」这条判据，**在迁移被部署之后**
//	  必然失效 ⇒ ⇒ 判据的**计数范围**太粗，不是台账错了。
//
//	★ 实测（本次）：台账里共 100 个部署轮小节；928/929/936 三个 sha 的
//	  「全文出现次数 = 2、登记段出现次数 = 1、部署轮段出现次数 = 1」
//	  ⇒ ⇒ 把计数范围收到登记段，判据即重新成立，且**不放宽**（登记段仍要求恰好 1）。
//
//	★ 判别式必须**锚定**：`^## <ISO 时间戳>Z — deploy `。
//	  台账里另有 14 个小节只是**顺带提到** deploy（如「local dev (pending deploy)」）
//	  ⇒ 宽松匹配会把它们误当部署轮 ⇒ ⇒ 登记段会被误删 ⇒ 判据反而失真。
var mig928DeployRoundHeaderRe = regexp.MustCompile(`^## \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z — deploy `)

// mig928ChangelogRegistrationSections 把 db-changelog 拆成
// （登记段全文, 部署轮段全文）。
func mig928ChangelogRegistrationSections(t *testing.T, body string) (registration, deployRounds string) {
	t.Helper()
	var reg, dep strings.Builder
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if mig928DeployRoundHeaderRe.MatchString(cur[0]) {
			dep.WriteString(strings.Join(cur, ""))
		} else {
			reg.WriteString(strings.Join(cur, ""))
		}
		cur = nil
	}
	for _, ln := range strings.SplitAfter(body, "\n") {
		if strings.HasPrefix(ln, "## ") && len(cur) > 0 {
			flush()
		}
		cur = append(cur, ln)
	}
	flush()
	return reg.String(), dep.String()
}

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
	changelogRegistration, changelogDeployRounds := mig928ChangelogRegistrationSections(t, changelog)

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
		{"db-changelog sha（**登记段**）", changelogRegistration, "`" + mig928SHA + "`"},
	}
	for _, p := range points {
		n := strings.Count(p.body, p.needle)
		if n != 1 {
			t.Errorf("%s: needle %q 出现 %d 次（要恰好 1）", p.label, p.needle, n)
		}
	}

	// ★★ 量具自检（★ 2026-11-04 12h 审计轮）：上面那条「登记段恰好 1 次」
	//   只有在**真的**做了范围切分、且切分**确实排除了本 sha 的那一处**时才有意义。
	//   ⇒ 三件事必须同时成立，否则本门会退化成「在空集里数 1 次 ⇒ 恒绿」：
	//   ① 全文里本 sha 至少出现 1 次（否则台账压根没登记）；
	//   ② 部署轮段里**确实**出现了本 sha（否则切分没排掉任何东西 ⇒ 切分判别式失效）；
	//   ③ 登记段里**确实**出现了本 sha（否则切分把登记那一处也删了 ⇒ 假绿）。
	needle := "`" + mig928SHA + "`"
	if n := strings.Count(changelog, needle); n < 1 {
		t.Errorf("db-changelog 全文里 needle %q 出现 %d 次（至少要有 1 次的登记记录）", needle, n)
	}
	if n := strings.Count(changelogDeployRounds, needle); n < 1 {
		t.Errorf("db-changelog 的部署轮段里 needle %q 出现 %d 次（期望 >=1：本次审计正是它把「恰好 1 次」顶破了）\n"+
			"★ 若这里为 0，说明切分判别式没匹配到真正的部署轮小节 ⇒ 上面那条判据的切分可能是空转",
			needle, n)
	}
	if n := strings.Count(changelogRegistration, needle); n < 1 {
		t.Errorf("db-changelog 的登记段里 needle %q 出现 %d 次（期望 >=1）\n"+
			"★ 若这里为 0，说明切分把登记记录也删掉了 ⇒ 上面那条判据是假绿", needle, n)
	}
	// ④ 部署轮小节必须被认出来（判别式没有整体失灵）。
	if strings.Count(changelogDeployRounds, "\n## ") == 0 {
		t.Errorf("db-changelog 的部署轮段里一个 `## ` 小节都没有 ⇒ 判别式整体失灵")
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
