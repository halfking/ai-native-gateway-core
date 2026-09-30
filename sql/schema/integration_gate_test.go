// Round 43: contract guards for scripts/audit/run-integration-gate.sh.
//
// The point of this harness is to make two distinctions that the repo's
// standing discipline requires and that plain `go test -tags=integration ./...`
// cannot make:
//
//  1. "all green" vs "it actually ran" — a suite that skips every test exits
//     0 and reads as a pass.
//  2. "a disposable database" vs "some database" — a shared database makes
//     integration gates produce both false red and false green.
//
// Measured on round 43: the existing integration workflow injects no database
// URL at all, so 27 of 67 integration test files skip; the URL is read under
// five different names; and one file gates on nothing and never skips, so it
// hard-fails without a live database.
package schema

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const gateScript = "../../scripts/audit/run-integration-gate.sh"

func gateSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(gateScript)
	if err != nil {
		t.Fatalf("read %s: %v", gateScript, err)
	}
	return string(b)
}

// TestGateInjectsEveryDBCredentialName pins the multi-name problem, and — this
// is the part that matters — it derives the list from the repository instead of
// keeping a hand-written one.
//
// Round 43 shipped a five-name list (TEST_PG_URL, TEST_DATABASE_URL, TEST_DB_URL,
// LLM_GATEWAY_PG_URL, DATABASE_URL) with a comment claiming the suite reads the
// URL "under five different names". That claim was false. The repository
// actually reads ten, and the five missing ones each left files permanently
// skipped. The concrete casualty:
//
//	internal/dbx/vacuum_mutex_test.go has NO build tag at all. It resolves its
//	DSN from TEST_AUDIT_ISOLATED_DB_URL / TEST_PG_DSN / TEST_DATABASE_URL and
//	t.Skipf's when it cannot connect. So it ran — and skipped — in every default
//	`go test ./...` and in CI, and the report said "ok".
//
// Once the harness supplied TEST_DATABASE_URL the test finally executed, and
// it FAILED. A gate that only injects a subset of the names is not a weak gate;
// it is a gate that certifies nothing about the files it leaves asleep.
//
// Redis-shaped names are excluded on purpose: pointing TEST_REDIS_URL at a
// PostgreSQL DSN would be worse than leaving it unset.
func TestGateInjectsEveryDBCredentialName(t *testing.T) {
	act := active(gateSource(t))

	names, err := dbCredentialNamesInRepo(t)
	if err != nil {
		t.Fatal(err)
	}
	// The scanner itself must not rot into matching nothing. Same lesson as the
	// startup-parse guard: a derived set that silently becomes empty turns this
	// test into a permanent no-op.
	if len(names) < 5 {
		t.Fatalf("只从仓内扫到 %d 个 DB URL 变量名（%v），扫描器本身可能已失效；\n"+
			"  一个恒返回空的守卫等于没有守卫。请检查匹配规则是否还能命中真实的 "+
			"os.Getenv/getenv 调用。", len(names), names)
	}
	t.Logf("仓内 DB URL 变量名 %d 个：%v", len(names), names)

	for _, name := range names {
		if !regexp.MustCompile(regexp.QuoteMeta(name) + `="\$GATE_URL"`).MatchString(act) {
			t.Errorf("harness 未注入 %s=$GATE_URL；只注入一部分名字会让读该变量的 "+
				"integration 文件全部 skip 而门禁仍显示绿", name)
		}
	}
	// And the inverse: the harness must not invent a name nothing reads, since
	// that is how the list rots in the other direction.
	for _, m := range regexp.MustCompile(`\n\t([A-Z][A-Z0-9_]*)="\$GATE_URL"`).FindAllStringSubmatch(act, -1) {
		if !contains(names, m[1]) {
			t.Errorf("harness 注入了 %s=$GATE_URL，但仓内没有任何文件读它；"+
				"请从注入列表删掉，或确认它确实是新增的读取点", m[1])
		}
	}
}

// dbCredentialNamesInRepo walks the Go TEST sources and collects every
// environment variable that names a PostgreSQL DSN. The rule is a suffix
// match, not a fixed list, so a new `FOO_DATABASE_URL` is picked up without
// editing this file.
//
// Two scoping decisions, both of which were wrong in the first attempt:
//
//   - Only `_test.go` files. A repo-wide scan also picks up production mains
//     (cmd/migrate-ursm-v2 reads LLM_GATEWAY_DATABASE_URL) and test tools
//     (cmd/test_sql reads LLM_GATEWAY_TEST_PG_URL). The harness's job is to
//     make tests run, and injecting a DSN into a main's runtime config is
//     noise, not coverage.
//   - Two passes, not one. A file may pass the name indirectly —
//     internal/dbx/vacuum_mutex_test.go does `for _, env := range
//     []string{"TEST_AUDIT_ISOLATED_DB_URL", "TEST_PG_DSN", ...}` and then
//     calls getenv(env) — so a scan of call arguments alone misses it. Any
//     quoted ALL-CAPS literal in a file that contains an env lookup counts.
func dbCredentialNamesInRepo(t *testing.T) ([]string, error) {
	t.Helper()
	root := "../.."
	seen := map[string]bool{}
	callRe := regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv|getenv|getenvImpl)\s*\(`)
	argRe := regexp.MustCompile(`(?:os\.Getenv|os\.LookupEnv|getenv|getenvImpl)\(\s*"([A-Z][A-Z0-9_]*)"`)
	litRe := regexp.MustCompile(`"([A-Z][A-Z0-9_]{3,})"`)
	isDBName := func(n string) bool {
		for _, s := range []string{"_DATABASE_URL", "_DB_URL", "_PG_URL", "_ISOLATED_DB_URL"} {
			if strings.HasSuffix(n, s) {
				return true
			}
		}
		return n == "DATABASE_URL" || n == "TEST_PG_DSN"
	}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable path (permission, race with a build) — skip
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		src := string(b)
		if !callRe.MatchString(src) {
			return nil
		}
		for _, m := range argRe.FindAllStringSubmatch(src, -1) {
			if isDBName(m[1]) {
				seen[m[1]] = true
			}
		}
		// Second pass for names reached through a slice literal.
		for _, m := range litRe.FindAllStringSubmatch(src, -1) {
			if isDBName(m[1]) {
				seen[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestGateRefusesVacuousRun is the "真的跑了" enforcement. Without it the
// harness would happily report success for a run where every test skipped.
func TestGateRefusesVacuousRun(t *testing.T) {
	src := gateSource(t)
	if !strings.Contains(src, "VACUOUS RUN") {
		t.Error("harness 未区分「0 个测试通过」与「全部 skip」")
	}
	act := active(src)
	if !regexp.MustCompile(`NPASS\s*==\s*0`).MatchString(act) {
		t.Error("未按 PASS==0 判空跑")
	}
	if !strings.Contains(act, "ALLOW_VACUOUS") {
		t.Error("缺少显式的 ALLOW_VACUOUS 逃生口，审计时会误杀刻意取样的 skip 统计")
	}
	// Skips alongside passes must be reported, not swallowed.
	if !strings.Contains(act, "green with skips") {
		t.Error("未在「有通过也有 skip」时给出区别性告警")
	}
}

// TestGateUsesDisposableDatabase pins the shared-database hazard. A gate that
// accepted an externally supplied URL would reintroduce the false red/green
// problem it exists to prevent.
func TestGateUsesDisposableDatabase(t *testing.T) {
	src := gateSource(t)
	act := active(src)
	if !strings.Contains(act, "CREATE DATABASE") || !strings.Contains(act, "DROP DATABASE IF EXISTS") {
		t.Error("harness 未自行创建并回收一次性数据库")
	}
	if !strings.Contains(act, "trap cleanup EXIT") {
		t.Error("清理未挂到 EXIT trap 上；测试失败时数据库会残留")
	}
	// The database name must stay clear of PostgreSQL's 63-byte identifier cap.
	if !strings.Contains(act, "GATE_DB") || !strings.Contains(act, ":0:30") {
		t.Error("未对一次性库名做长度截断；超 63 字节会让 CREATE DATABASE 静默失败")
	}
}

// TestGateAssertsPopulation guards the假绿 shape: a suite that "ran" against an
// empty database has proven nothing. This is the same assertion that caught the
// citus_columnar schema error in round 43.
func TestGateAssertsPopulation(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "[populated]") {
		t.Error("harness 未输出库填充情况")
	}
	if !regexp.MustCompile(`RELS\s*<=\s*0`).MatchString(act) {
		t.Error("未在填充断言失败时致命退出")
	}
	if !strings.Contains(act, "00-prereqs.sql") {
		t.Error("未先 apply 00-prereqs；缺 citus_columnar 时每个 columnar 断言都会假失败")
	}
}

// TestGateReportsFailureReason guards the harness against being half a tool: a
// gate that says FAIL without saying why wastes the reader's time. In
// `go test -v` the assertion detail precedes the "--- FAIL:" marker, so
// context must be taken from before it.
func TestGateReportsFailureReason(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "failing test output") {
		t.Error("harness 未输出失败测试的上下文")
	}
	if regexp.MustCompile(`grep\s+-A\d+\s+"\^\\s\*---\s+FAIL"`).MatchString(act) {
		t.Error("用 -A（向后）抓取 --- FAIL 之后的行；go test -v 把断言细节印在标记" +
			"之前，向后抓只能得到空报告")
	}
}

// TestWorkflowDoesNotShipAnUnrunnableGateJob is the re-enable gate.
//
// Round 43 added a `integration-gate` job that drove this harness from CI. A
// self-audit found the job was not merely untested but **guaranteed to fail on
// its first run**, for three independent reasons:
//
//  1. it pinned `kx-citus-pg17:offline-arm64`, a tag that does not exist — the
//     tag actually in the registry is `13.3.0-vector-arm64` (verified with
//     `docker image inspect` on the live local container);
//  2. that image is linux/arm64 while `ubuntu-latest` runners are x64, so
//     `docker run` fails with an exec-format error;
//  3. the container was started with POSTGRES_PASSWORD but PG_PASSWORD was
//     never exported, so the harness built a passwordless DSN and died at its
//     own DSN precheck (measured: a passwordless TCP connection to the local
//     container fails with `fe_sendauth: no password supplied`).
//
// The job was removed rather than "fixed", because fixing it needs an amd64
// build of the Citus image, which does not exist.
//
// The contract this test pins, via a machine-readable status line in the
// preconditions document:
//
//	ci-gate-preconditions: NOT-SATISFIED  → the job must NOT be in the workflow
//	ci-gate-preconditions: SATISFIED      → the job MUST be in the workflow
//
// Editing that one line to SATISFIED is therefore not a note — it is a
// commitment, and this test goes red until the job comes back. That inverts
// the usual failure mode, where a broken job is re-added quietly and nobody
// notices for weeks.
//
// The guard strips YAML comments before looking for the job. Without that it
// is green precisely when the job is present: the removal note left in this
// workflow explains why the job is gone and names run-integration-gate.sh in
// prose. That "guard satisfied by a comment" bug was caught by mutation —
// adding the job back left the test passing.
func TestWorkflowDoesNotShipAnUnrunnableGateJob(t *testing.T) {
	const (
		wf  = "../../.github/workflows/integration-testcontainers-ci.yml"
		pre = "../../docs/audit/2026-10-01-integration-gate-ci-preconditions.md"
	)
	b, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("read %s: %v", wf, err)
	}
	wired := strings.Contains(active(string(b)), "run-integration-gate.sh")

	doc, err := os.ReadFile(pre)
	if errors.Is(err, os.ErrNotExist) {
		if wired {
			t.Errorf("%s 里有 job 调用 run-integration-gate.sh，但 %s 不存在。\n"+
				"  该 harness 需要一个 amd64 的 kx-citus-pg17 镜像；本机在用的 "+
				"13.3.0-vector-arm64 是 arm64-only，ubuntu-latest 是 x64，"+
				"docker run 会 exec-format 失败。另需在 job env 里导出 PG_PASSWORD"+
				"（容器设了 POSTGRES_PASSWORD，无密码 DSN 实测报 fe_sendauth）。\n"+
				"  没有前置条件文档就没有任何东西记录这些约束，别直接加 job。",
				wf, filepath.Base(pre))
		}
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", pre, err)
	}

	m := regexp.MustCompile(`(?m)^ci-gate-preconditions:\s*(NOT-SATISFIED|SATISFIED)\s*$`).
		FindStringSubmatch(string(doc))
	if m == nil {
		t.Fatalf("%s 里没有 `ci-gate-preconditions: NOT-SATISFIED|SATISFIED` 状态行；\n"+
			"  本守卫靠这一行判断该 job 应不应该存在。缺了它守卫只能退化成单边检查。",
			filepath.Base(pre))
	}
	switch m[1] {
	case "SATISFIED":
		if !wired {
			t.Errorf("%s 已标记 SATISFIED，但 %s 仍没有调用 run-integration-gate.sh 的 job。\n"+
				"  请在同一次提交里把 job 加回去；若其实还没满足，改回 NOT-SATISFIED。",
				filepath.Base(pre), wf)
		}
	case "NOT-SATISFIED":
		if wired {
			t.Errorf("%s 标记 NOT-SATISFIED，因此 %s 里不应有调用 "+
				"run-integration-gate.sh 的 job —— 那个 job 三个独立原因必败"+
				"（镜像 tag 不存在 / arm64 镜像跑在 x64 runner / 缺 PG_PASSWORD）。\n"+
				"  若前置条件已落实，请把状态行改成 SATISFIED。",
				filepath.Base(pre), wf)
		}
	}
}

// TestGateAppliesStartupMigrations pins the starting-schema scope.
//
// A gate database built from the baseline alone is not what the installer
// produces. Measured round 43: baseline = 328 relations; baseline + the
// registered startup migrations = 421. The difference is not cosmetic —
// session_aggregate_outbox (migration 630) and usage_facts (537) exist only
// because of those migrations, so db.ensure*() tests for them failed with
// "relation does not exist" against a baseline-only database. That reads like
// a product defect and is not one.
func TestGateAppliesStartupMigrations(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "GATE_APPLY_STARTUP") {
		t.Error("harness 未提供 GATE_APPLY_STARTUP 开关；起始库口径不可控")
	}
	// The default must be ON. A gate database that silently defaults to the
	// stale baseline reproduces the "relation does not exist" failures this
	// whole path exists to eliminate, and does so invisibly.
	if !strings.Contains(act, `GATE_APPLY_STARTUP:-1`) {
		t.Error("GATE_APPLY_STARTUP 默认值不是 1；门禁库会静默退回陈旧基线（328 relations）")
	}
	if !strings.Contains(act, "embeddata/startup") {
		t.Error("harness 未从 embeddata/startup 应用启动迁移")
	}
	// The per-file transaction marker must be honoured, or the loop reports
	// two failures (DROP INDEX CONCURRENTLY) the installer would not have.
	if !strings.Contains(act, "dbinit:no-transaction") {
		t.Error("harness 应用启动迁移时未识别 dbinit:no-transaction 标记；" +
			"718/719 会因 CONCURRENTLY 落在事务块里而假失败")
	}
	// A migration that does not apply is a known fresh-install gap, not a gate
	// failure — but ONLY if it is enumerated in the known-gaps manifest. Round
	// 43 collected the list and printed it, which made it neither a gate nor a
	// record: the run stayed green at any list length, so the list could grow
	// silently. Round 44 turns it into a ratchet. The enforcement itself is
	// guarded by TestGateRatchetsUnlistedStartupGaps; this only asserts the
	// collection still happens.
	if !strings.Contains(act, "sf_failed+=") {
		t.Error("未收集未应用的启动迁移清单")
	}
	if !strings.Contains(act, "startup_known_gaps.tsv") {
		t.Error("未把「启动迁移未应用」与已知缺口清单 sql/schema/startup_known_gaps.tsv 对账；" +
			"不对账就只能靠人肉读列表，列表会静默增长")
	}
}

// TestGateRatchetsUnlistedStartupGaps is the guard for the ratchet itself.
//
// The failure this prevents is subtle and worth stating: an allowlist of known
// gaps is a ratchet ONLY if adding a new gap is fatal. If the harness merely
// prints failures, then every future fresh-install gap is absorbed into the same
// green run, and the list becomes a blanket exemption that nobody re-reads.
//
// The three required properties, each of which was faked in a first draft of
// this change:
//   - the manifest is actually read (not hardcoded in the shell),
//   - an unlisted failure is FATAL, not a warning,
//   - a listed-but-not-reproduced entry is surfaced, so the list can be retired
//     instead of accumulating dead exemptions.
func TestGateRatchetsUnlistedStartupGaps(t *testing.T) {
	act := active(gateSource(t))

	// The manifest must be READ FROM THE FILE, not hardcoded in the shell.
	//
	// This assertion is deliberately narrower than it looks. A first draft
	// checked only `strings.Contains(act, "GAP_MANIFEST")`, and the mutation
	// suite proved it fake: renaming the variable to UNUSED_MANIFEST left the
	// guard green, because the string "GAP_MANIFEST" still occurs inside two
	// die() messages. A bare substring cannot tell a binding from a mention.
	// So assert the assignment itself, and separately assert the variable is
	// consumed by the reader.
	if !strings.Contains(act, `GAP_MANIFEST="$REPO_ROOT/sql/schema/startup_known_gaps.tsv"`) {
		t.Error("harness 未把 GAP_MANIFEST 绑定到 sql/schema/startup_known_gaps.tsv；" +
			"只检查变量名出现过是不够的——die 消息里提到它就能满足那种判据")
	}
	// Consumed, not merely named: the reader must pass the variable to sed.
	if !strings.Contains(act, `"$GAP_MANIFEST" | sort -u`) {
		t.Error("harness 没有真正用 GAP_MANIFEST 读取清单内容；" +
			"绑定存在但清单没被读，缺口比对会对空集合静默放行")
	}

	// The exemptions are enumerated, which is what makes an unlisted failure
	// detectable. Assert the comparison is exact-match on the filename, not a
	// substring: a substring match would let 622 match 6221_whatever.
	if !strings.Contains(act, "grep -qxF") {
		t.Error("缺口比对未使用整行精确匹配（grep -qxF）；子串匹配会让一条缺口" +
			"意外豁免另一个前缀相同的迁移")
	}
	if !strings.Contains(act, "gap_unlisted+=") {
		t.Error("未收集「未登记」的启动迁移失败；没有这个集合就无法与已知缺口区分")
	}
	if !strings.Contains(act, "gap_stale+=") {
		t.Error("未检测清单中已不复现的条目；清单只进不出会变成永久豁免")
	}

	// The load-bearing assertion: unlisted => die. Checking only that the words
	// "unlisted"/"gap" appear would be satisfied by an echo.
	//
	// Two halves, because either alone is weak. The non-empty test alone is
	// satisfied by a block that only prints; the die message alone could sit
	// outside the conditional entirely. Together they pin "when the unlisted set
	// is non-empty, control reaches this specific die".
	//
	// The repeat bound is 1000, not 4000: Go's regexp rejects a repeat count
	// above 1000 at compile time, which panics the whole test binary rather than
	// failing one test. Measured distance from the test to the die is ~250 chars.
	unlistedFatal := regexp.MustCompile(`\$\{#gap_unlisted\[\@\]\} > 0[\s\S]{0,1000}?\bdie\b`)
	if !unlistedFatal.MatchString(act) {
		t.Error("未登记的启动迁移失败没有致命退出（未在 gap_unlisted 非空时 die）；" +
			"只要它不致命，这份清单就只是一份可以无限变长的免责声明")
	}
	// Anchor on the message text too, so the block cannot be satisfied by an
	// unrelated `die` that happens to fall inside the window.
	if !strings.Contains(act, "未登记为已知缺口") {
		t.Error("未登记缺口的 die 消息缺失或被改写；" +
			"守卫必须锚定这条具体诊断文案，否则窗口内的任意 die 都能满足判据")
	}
}

// TestKnownStartupGapsManifestIsAnnotated guards the manifest file itself.
//
// A hand-maintained list rots. Three separate ways this one could rot silently:
// a line with no reason (so nobody can triage it later), a line naming a file
// that no longer exists or is no longer registered (so the entry is fiction),
// and the file being emptied or deleted (so every gap becomes "unlisted" and
// the harness dies with a misleading reason).
func TestKnownStartupGapsManifestIsAnnotated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("startup_known_gaps.tsv"))
	if err != nil {
		t.Fatalf("读不到已知缺口清单：%v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	type entry struct{ file, reason string }
	var entries []entry
	seen := map[string]bool{}
	for i, raw := range lines {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		parts := strings.Split(raw, "\t")
		if len(parts) != 2 {
			t.Errorf("第 %d 行不是「文件名<TAB>原因」两段：%q", i+1, raw)
			continue
		}
		f, reason := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if f == "" || reason == "" {
			t.Errorf("第 %d 行有空字段：%q", i+1, raw)
			continue
		}
		if seen[f] {
			t.Errorf("重复条目：%s", f)
		}
		seen[f] = true
		entries = append(entries, entry{f, reason})
	}

	// The non-empty self-check. A guard over an empty file passes trivially, and
	// an empty manifest makes every startup failure "unlisted" — the harness
	// would then die for a reason that has nothing to do with the real cause.
	// Measured round 44 on the installer's own path: 154 of 173 applied, 19 did not.
	if len(entries) == 0 {
		t.Fatal("已知缺口清单为空；实测有 19 条启动迁移在全新安装路径上不落地。" +
			"空清单会让任何未应用迁移都被当成「新回归」而致命退出，诊断方向会被带偏")
	}
	if len(entries) < 19 {
		t.Errorf("已知缺口只有 %d 条，实测为 19 条；清单被削减过——"+
			"若确有迁移被修复，请连带更新本注释与清单，而不是让数量无声漂移", len(entries))
	}

	// Every entry must name a file that is both present and registered. An entry
	// pointing at a nonexistent file is worse than no entry: it silently grants
	// an exemption to whatever gets added at that name later.
	registered := map[string]bool{}
	startupDir := filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")
	runner, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读不到 runner.go：%v", err)
	}
	for _, m := range regexp.MustCompile(`"[0-9a-zA-Z_]+\.sql"`).FindAllString(string(runner), -1) {
		registered[strings.Trim(m, `"`)] = true
	}
	if len(registered) == 0 {
		t.Fatal("从 runner.go 解析出 0 条已注册启动迁移；解析失败会让下面的校验全部变成空转")
	}
	for _, e := range entries {
		if !registered[e.file] {
			t.Errorf("清单条目 %s 不在 runner.go 的 StartupFiles 里；它不会被应用，条目无意义", e.file)
		}
		if _, err := os.Stat(filepath.Join(startupDir, e.file)); err != nil {
			t.Errorf("清单条目 %s 在 embeddata/startup 下不存在：%v", e.file, err)
		}
	}
}

// TestGateRejectsSilentZeroStartupParse is the guard for the false-green this
// harness used to have.
//
// The startup list is extracted by matching a Go source literal with sed. If
// runner.go is reformatted the match returns ZERO lines and prints no error,
// the loop does nothing, and the gate database silently reverts to the stale
// baseline (328 relations instead of 421). db.ensure*() tests then fail with
// "relation does not exist" — which reads like a product defect and is not one.
//
// Mutation-verified: replacing the anchor with a non-matching one makes the
// loop report applied=0 failed=0 missing=0, and this guard's two required
// assertions are what turn that into a fatal error.
func TestGateRejectsSilentZeroStartupParse(t *testing.T) {
	act := active(gateSource(t))
	if !regexp.MustCompile(`sf_total\s*=\s*\$\(\(sf_ok \+ sf_fail \+ sf_missing\)\)`).MatchString(act) {
		t.Error("harness 未统计启动迁移的解析总数（applied+failed+missing）；" +
			"没有这个总数就无法区分「没有启动迁移」与「sed 锚点失配解析出 0 条」")
	}
	if !regexp.MustCompile(`sf_total\s*==\s*0`).MatchString(act) {
		t.Error("解析出 0 条启动迁移时未致命退出；这会让门禁库静默退回陈旧基线")
	}
	// A partial parse is just as bad as a total one, so there is a floor, not
	// only a zero check. Measured: 173 registered startup migrations.
	if !regexp.MustCompile(`sf_total\s*<\s*100`).MatchString(act) {
		t.Error("缺少启动迁移条数地板；只判 0 的话，Go 源码部分重排会解析出十几条" +
			"并被当成正常，门禁库变成半个")
	}
}

// TestGateHasARelationFloor is the second half of the population assertion.
//
// "relations > 0" is satisfied by a database holding one stray table, and — the
// shape that mattered — it is still satisfied when the startup loop silently
// parsed zero files. The floor is what distinguishes a real installer-shaped
// database from a nearly-empty one. Measured: baseline only = 328, baseline +
// startup = 421.
func TestGateHasARelationFloor(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "GATE_MIN_RELATIONS") {
		t.Error("填充断言没有地板值；RELS>0 会被「只建了基线」的残缺库满足")
	}
	if !strings.Contains(act, "GATE_MIN_RELATIONS:-400") {
		t.Error("relations 地板默认值不是 400；实测基线+启动迁移 = 421，地板需留出余量又不能被基线单独满足")
	}
	if !regexp.MustCompile(`RELS\s*<\s*GATE_MIN_RELATIONS`).MatchString(act) {
		t.Error("地板值未参与比较；声明了地板但不比较等于没设")
	}
}

// TestGateRejectsPackagesWithoutIntegrationTests closes a second false green.
//
// The vacuity check keys on NPASS==0. A package whose tests are all ordinary
// unit tests reports plenty of PASS, so NPASS==0 never fires and the run reads
// as a green gate while covering nothing.
//
// Measured: internal/trace has zero files that the integration build tag
// introduces, yet `go test -tags=integration ./internal/trace` yields 31 PASS.
// Round 43's CI package list included it.
//
// The check must be derived from `go list`, not from grepping the source for
// `//go:build integration`: a negated constraint such as `//go:build
// !nintegration` contains that substring and would be counted as coverage.
// The guard therefore requires the set-difference formulation.
func TestGateRejectsPackagesWithoutIntegrationTests(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "GATE_ITEST_COUNT") {
		t.Error("harness 未统计「仅由 integration tag 引入」的测试文件数；" +
			"没有它，一个零 integration 覆盖的包会靠普通单测的 PASS 报成门禁通过")
	}
	if !strings.Contains(act, "comm -13") {
		t.Error("未用「带 tag 的文件集 − 不带 tag 的文件集」求差；" +
			"改成 grep 源码里的 //go:build integration 会被 !nintegration 这类否定约束骗过")
	}
	if !regexp.MustCompile(`GATE_ITEST_COUNT\s*==\s*0`).MatchString(act) {
		t.Error("零 integration 测试文件时未致命退出")
	}
	// The assertion that keeps this honest: the harness must NOT fall back to
	// source-level tag grepping.
	if regexp.MustCompile(`grep\s+-qE?\s+'\^//go:build`).MatchString(act) {
		t.Error("harness 又开始用 grep 解析 //go:build；否定约束（!nintegration）会被误判为有覆盖")
	}
}

// TestGateDoesNotRequireHostPsql keeps the precheck honest about its own
// dependency. The first version called `psql "$DSN"` unconditionally, so a host
// without a psql binary died in a branch whose message blamed the password —
// sending the reader after the wrong cause.
func TestGateDoesNotRequireHostPsql(t *testing.T) {
	act := active(gateSource(t))
	if !strings.Contains(act, "command -v psql") {
		t.Error("未探测宿主是否有 psql；缺二进制时不应被当成密码问题")
	}
	if !strings.Contains(act, "PG_CLIENT_IMAGE") {
		t.Error("无宿主 psql 时没有回退镜像；该回退只需跑 SELECT 1，故可用多架构的 stock postgres 镜像")
	}
	// Every die() message that embeds a variable must brace it. Under `set -u`
	// a bare $VAR followed by a CJK character aborts the script: bash absorbs
	// the multibyte character into the variable name.
	// Reproduced: `bash -c 'set -u; X=5; echo "$X。"'` → "X?: unbound variable".
	if m := regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7f]`).FindString(act); m != "" {
		t.Errorf("脚本里有未加花括号的变量展开紧跟非 ASCII 字符：%q\n"+
			"  set -u 下 bash 会把该字符并入变量名并报「未绑定的变量」。"+
			"请一律写成 ${VAR}。", m)
	}
}
