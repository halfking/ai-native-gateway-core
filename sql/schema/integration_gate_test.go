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
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

	// TEST_TENANT_DATABASE_URL is the one name that must NOT be $GATE_URL.
	//
	// Every other name wants the ordinary gate DSN. This one wants a DSN whose
	// role cannot bypass RLS, because its only consumers are tenant-isolation
	// probes. Pointing it at $GATE_URL handed those probes a superuser
	// (rolsuper=t, rolbypassrls=t), and
	// domains/requestjourney/observation_outbox_integration_test.go duly failed
	// with "alpha scope saw 1 beta rows, want 0 (RLS leak)" — a fabricated
	// security finding, since the policies in 552 are correct.
	//
	// So the assertion is inverted for this single name, and tightened at the
	// same time: it must be wired to a genuinely separate variable, and the
	// harness must verify that variable's role is non-bypass before exporting
	// it. See the [tenant] block in run-integration-gate.sh.
	const tenantVar = "TEST_TENANT_DATABASE_URL"
	for _, name := range names {
		if name == tenantVar {
			continue
		}
		if !regexp.MustCompile(regexp.QuoteMeta(name) + `="\$GATE_URL"`).MatchString(act) {
			t.Errorf("harness 未注入 %s=$GATE_URL；只注入一部分名字会让读该变量的 "+
				"integration 文件全部 skip 而门禁仍显示绿", name)
		}
	}

	if !regexp.MustCompile(regexp.QuoteMeta(tenantVar) + `="\$TENANT_DSN"`).MatchString(act) {
		t.Errorf("harness 未把 %s 接到 $TENANT_DSN（非 bypass 角色）；"+
			"把它接回 $GATE_URL 会让租户隔离断言测的是超级用户，"+
			"从而报出一个并不存在的「RLS 泄漏」", tenantVar)
	}
	// The role must actually be verified, not merely named. Without the
	// rolsuper/rolbypassrls probe, a misconfigured cluster would silently
	// resurrect exactly the false red this wiring exists to remove.
	for _, want := range []string{"rolbypassrls", "rolsuper"} {
		if !strings.Contains(act, want) {
			t.Errorf("harness 没有校验租户角色的 %s 属性；"+
				"只建角色而不验证它能否绕过 RLS，等于把假红原样放回去", want)
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
	// Same inverse check for the tenant variable, against $TENANT_DSN.
	for _, m := range regexp.MustCompile(`\n\t([A-Z][A-Z0-9_]*)="\$TENANT_DSN"`).FindAllStringSubmatch(act, -1) {
		if !contains(names, m[1]) {
			t.Errorf("harness 注入了 %s=$TENANT_DSN，但仓内没有任何文件读它；"+
				"请删掉，或确认它确实是新增的读取点", m[1])
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
	// Failure collection must reach the caller's array.
	//
	// This used to assert the bare substring "sf_failed+=" — which passed for
	// the right reason only by accident. Once the per-file apply was factored
	// out (two passes now share one helper) the append became
	// `eval "$arrvar+=(\"$entry\")"`, the literal disappeared, and the guard
	// went red on a correct script. A bare substring cannot tell a binding from
	// a mention, and it silently pins the IMPLEMENTATION rather than the
	// property. So: assert the call site binds the array, and that the helper
	// appends through the name it was given.
	if !strings.Contains(act, `apply_startup_file "$f" "" sf_ok sf_fail sf_missing sf_failed`) {
		t.Error("第一遍没有把 sf_failed 作为失败数组传给 apply_startup_file；" +
			"helper 若收集到别处，第一遍的缺口清单就是空的，ratchet 拿空集合当全部已登记")
	}
	if !strings.Contains(act, `eval "$arrvar+=`) {
		t.Error("apply_startup_file 没有通过调用方传入的数组名追加失败；" +
			"写死某个数组名会让两遍的失败混进同一份清单，分不清哪一遍坏了")
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
// knownStartupGapCount reads the count the manifest header claims, so the
// guard compares the list against a declared measurement instead of an integer
// baked into this test. Baking the integer in is what made the old `>= 19`
// check rot: when the 19 real gaps were fixed, the check would have kept
// demanding 19 entries that no longer existed.
//
// The header line this reads is deliberately machine-shaped:
//
//	#   STATUS AS OF <date> ...: **EMPTY — 0 known gaps.**
//
// The parser is strict on purpose — a header that stops matching must fail
// loudly here rather than silently defaulting to 0 and making the whole
// comparison vacuous.
func knownStartupGapCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("startup_known_gaps.tsv"))
	if err != nil {
		t.Fatalf("读不到已知缺口清单：%v", err)
	}
	re := regexp.MustCompile(`(?m)^#\s*STATUS AS OF .*?:\s*\*\*EMPTY — (\d+) known gaps\.\*\*`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("startup_known_gaps.tsv 头部找不到形如\n"+
			"  \"#   STATUS AS OF <date> ...: **EMPTY — N known gaps.**\"\n"+
			"  的声明行（已匹配 %d 条清单条目时尤须检查）。\n"+
			"  该行是清单内容与实测值的唯一对账锚；解析失败会让本守卫的计数比较变成空转。",
			countManifestEntries(string(data)))
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("清单头部声明的条数 %q 不是整数：%v", m[1], err)
	}
	return n
}

func countManifestEntries(s string) int {
	n := 0
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		n++
	}
	return n
}

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

	// The non-empty self-check, and why it was relaxed on 2026-10-01.
	//
	// This used to be `len(entries) == 0 => fatal` plus a `>= 19` floor, both
	// calibrated on Round 44's measurement of applied=154 / failed=19. Those 19
	// gaps are now fixed: the pre-478 migrations were added to StartupFiles,
	// and the installer path now measures applied=198 / failed=0. Pinning the
	// count at 19 would therefore have frozen a number that is no longer true,
	// and the empty-manifest fatal would have fired on the HEALTHY state.
	//
	// What must stay true is the direction of the ratchet, not the number: an
	// UNREGISTERED failure must still be fatal, and that is enforced by the
	// harness, not by this file. So the count is now asserted against the
	// measurement recorded in the manifest header rather than a hardcoded
	// integer, and a non-empty manifest is still required to be well-formed.
	//
	// A manifest that regains entries must not drift silently: every entry is
	// checked below to name a file that exists AND is registered, and the
	// harness reports any entry that did not reproduce as stale. Both are what
	// stop this list from rotting into a blanket exemption.
	if len(entries) != knownStartupGapCount(t) {
		t.Errorf("清单条目数 = %d，但文件头声明的实测值 = %d；两者必须一致——"+
			"请重跑 run-integration-gate.sh 读取 startup: applied=N failed=M 那行，"+
			"然后同步更新清单内容与本注释，不要让数量无声漂移",
			len(entries), knownStartupGapCount(t))
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
	// only a zero check. Measured: 200 registered startup migrations.
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

// TestIntegrationTaggedTreeCompiles is the guard for a failure mode this
// repository had no defence against for 59 days.
//
// tests/integration/protocol_e2e_test.go called transformation.NewIRTransport
// three times. That constructor was deliberately deleted in d206ca771
// ("refactor(transformation): 下线 IR/Legacy 传输层死代码工厂", 审计R3#1), whose
// cleanup list enumerated nine domains/transformation test files plus
// tests/integration/ir_default_switch_test.go — and missed this one file. See
// docs/adr/2026-09-09-ir-transport-layer-retirement.md.
//
// Why it survived so long, and why no other guard in this file caught it:
//
//   - Every integration test file is behind `//go:build integration`, so a
//     plain `go build ./...` and a plain `go vet ./...` never compile them.
//     They are invisible to the default build and to the default test run.
//   - The one CI job that does compile them
//     (.github/workflows/integration-testcontainers-ci.yml) has been
//     permanently red, and a permanently-red gate is observationally
//     indistinguishable from no gate at all.
//
// So the integration-tagged half of the tree could rot for two months and
// every other signal in the repo stayed green. This test closes that hole from
// the side that IS always run: it type-checks the tagged tree, including test
// files, and it carries no build tag itself, so `go test ./...` executes it.
//
// `go vet -tags=integration ./...` is used rather than `go test` because vet
// type-checks _test.go files without executing TestMain, linking a binary per
// package, or touching a database.
func TestIntegrationTaggedTreeCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the whole tagged tree; skipped in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// Scope floor. A `go vet` that fails to run (bad root, no packages, a
	// broken toolchain) exits non-zero with an empty error stream, and a guard
	// that only asserts on the stream would then pass for the wrong reason.
	// Count the packages first so a silently-empty run cannot go green.
	listCmd := exec.Command(goBin, "list", "./...")
	listCmd.Dir = root
	listOut, listErr := listCmd.Output()
	if listErr != nil || len(bytes.TrimSpace(listOut)) == 0 {
		t.Skipf("cannot enumerate packages from %s (err=%v); nothing to verify", root, listErr)
	}
	pkgCount := strings.Count(strings.TrimSpace(string(listOut)), "\n") + 1
	if pkgCount < 50 {
		t.Fatalf("`go list ./...` found only %d packages; the repo root looks wrong (%s), "+
			"so this guard would certify almost nothing", pkgCount, root)
	}
	t.Logf("type-checking %d packages with -tags=integration", pkgCount)

	cmd := exec.Command(goBin, "vet", "-tags=integration", "./...")
	cmd.Dir = root
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	runErr := cmd.Run()
	combined := stderr.String() + stdout.String()
	if runErr == nil {
		return
	}

	// Surface the actual breakage. `undefined: X` is the shape this guard
	// exists to catch — a tagged test file referencing something that was
	// renamed or deleted — so pull those lines out first and put them on top.
	var undefined []string
	for _, line := range strings.Split(combined, "\n") {
		if strings.Contains(line, "undefined:") || strings.Contains(line, "imported and not used") {
			undefined = append(undefined, strings.TrimSpace(line))
		}
	}

	t.Fatalf("integration-tagged tree does not compile (go vet -tags=integration ./...): %v\n\n"+
		"%d reference error(s) in the tagged tree:\n  %s\n\nfull output:\n%s",
		runErr, len(undefined), strings.Join(undefined, "\n  "), combined)
}

// TestGateSupportsPerPackageFixtureShapes pins the Round 44 §7 work.
//
// Round 44 found that the integration-only files fall into families that cannot
// share one database: some build their own schema and collide with a populated
// one (SQLSTATE 42P07), some assume an already-migrated one and fail on an
// empty one ("relation ... does not exist"). One harness could build one kind
// of database, so "integration 全绿" had no single answer.
//
// This asserts the mechanism exists and is honest about what it does NOT
// achieve. The negative result is the important half: measured 2026-10-01 by
// running all 27 packages on both shapes, NO package is green on either,
// because the two families are interleaved inside packages. A shape registry
// that let a reader assume otherwise would be worse than no registry, so the
// manifest carries a per-shape FAIL count and this guard pins that requirement.
func TestGateSupportsPerPackageFixtureShapes(t *testing.T) {
	act := active(gateSource(t))

	// 1. All three shapes are selectable, and the default is installer — so
	//    adding the registry cannot change an existing run on its own.
	for _, shape := range []string{"installer", "baseline", "prereqs"} {
		if !strings.Contains(act, shape) {
			t.Errorf("harness 不支持 GATE_DB_SHAPE=%s；三种形态缺一不可", shape)
		}
	}
	if !strings.Contains(act, `GATE_DB_SHAPE="${GATE_DB_SHAPE:-installer}"`) {
		t.Error(`harness 的形态默认值必须是 installer；否则加一张形态登记表就会` +
			`悄悄改变所有既有包的建库方式`)
	}

	// 2. The registry is read per package, not hardcoded in the script.
	//
	//    Mutation-verified, and the first version of this assertion was a FALSE
	//    GREEN for the reason Round 44 §2.3 recorded: it tested for the bare
	//    substring "integration_fixture_shapes.tsv", which also appears in an
	//    echo message and in comments. Repointing SHAPE_MANIFEST at /dev/null —
	//    i.e. the registry genuinely no longer read — left the guard green.
	//    A bare substring cannot tell "bound" from "mentioned". So assert the
	//    ASSIGNMENT form, and separately that it is actually consumed.
	if !regexp.MustCompile(`SHAPE_MANIFEST="\$REPO_ROOT/sql/schema/integration_fixture_shapes\.tsv"`).
		MatchString(act) {
		t.Error("harness 没有把 SHAPE_MANIFEST 绑到 integration_fixture_shapes.tsv；" +
			"形态被写死就等于要求每个人靠记忆选形态。" +
			"（注意：只查裸子串会被 echo 与注释满足——这是假绿，勿退回那种写法）")
	}
	if !strings.Contains(act, "SHAPE_MANIFEST\" ]") && !strings.Contains(act, `-f "$SHAPE_MANIFEST"`) {
		t.Error("SHAPE_MANIFEST 被赋值却从未被读取；绑了不用等于没绑")
	}

	// 3. A shape mismatch must be NAMED, not left as a raw 42P07 that reads
	//    like a product defect.
	for _, want := range []string{"already exists", "does not exist", "形态不匹配诊断"} {
		if !strings.Contains(act, want) {
			t.Errorf("harness 的形态不匹配诊断里没有 %q；"+
				"42P07 / 42P01 原样抛出会被读成产品缺陷，正是 R44 §7 让这个分裂"+
				"隐身的那个形状", want)
		}
	}

	// 4. The population floor must be per-shape. Applying the installer floor
	//    to a deliberately-nearly-empty prereqs database would make the
	//    self-building family ungateable for a reason unrelated to the code.
	//
	//    This too was a false green first time round. The original check looked
	//    for `GATE_DB_SHAPE" != "prereqs"`, which survives deleting the FLOOR
	//    block because the mismatch-diagnostics use the same condition twice.
	//    So assert the per-shape floor TABLE, which is the actual mechanism and
	//    has exactly one home.
	for _, want := range []string{
		`installer) GATE_MIN_RELATIONS=`,
		`baseline)  GATE_MIN_RELATIONS=`,
		`prereqs)   GATE_MIN_RELATIONS=`,
	} {
		if !strings.Contains(act, want) {
			t.Errorf("population floor 未按形态分档，缺少 %q；"+
				"对 prereqs 形态套用 installer 的地板值，会把「刻意为空」"+
				"误判成「起始库没建全」", want)
		}
	}

	// 5. The registry file exists, is parseable, and every row is well-formed
	//    AND carries both measured FAIL counts.
	//
	//    Scan-volume floor: a manifest with zero rows is legitimate today (it
	//    ships empty on purpose — see its header). The guard therefore checks
	//    FORMAT and the parse, not a row count. The thing that must never
	//    happen is a row that omits the measurement, because that is what turns
	//    "neither shape works" into a false green light.
	data, err := os.ReadFile(filepath.Join("integration_fixture_shapes.tsv"))
	if err != nil {
		t.Fatalf("读不到形态登记表：%v", err)
	}
	rows := 0
	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rows++
		parts := strings.Split(raw, "\t")
		if len(parts) != 5 {
			t.Errorf("第 %d 行应为 5 段「包<TAB>形态<TAB>installer失败数<TAB>prereqs失败数<TAB>原因」，"+
				"实得 %d 段：%q", i+1, len(parts), raw)
			continue
		}
		pkg, shape, failInst, failPre, reason := parts[0], parts[1], parts[2], parts[3], parts[4]
		switch shape {
		case "installer", "baseline", "prereqs":
		default:
			t.Errorf("第 %d 行形态 %q 不在 installer|baseline|prereqs 内：%q", i+1, shape, raw)
		}
		for label, v := range map[string]string{"installer 失败数": failInst, "prereqs 失败数": failPre} {
			if !regexp.MustCompile(`^\d+$`).MatchString(strings.TrimSpace(v)) {
				t.Errorf("第 %d 行的%s 必须是实测整数（缺了它，这一行就退化成"+
					"「某形态可以」的无声断言）：%q", i+1, label, raw)
			}
		}
		if strings.TrimSpace(pkg) == "" || strings.TrimSpace(reason) == "" {
			t.Errorf("第 %d 行有空的包名或原因：%q", i+1, raw)
		}
	}
	t.Logf("形态登记表 %d 行（当前刻意为空，见文件头）", rows)
}

// ---------------------------------------------------------------------------
// Re-runnability of the registered startup chain (§9.119, 2026-10-04)
// ---------------------------------------------------------------------------
//
// The ratchet above answers "which migrations fail on a FRESH install". This
// one answers a different question: "which fail when the chain is applied a
// SECOND time" — which is what re-running the installer against a machine that
// already has one actually does, because InitSchema
// (installer/internal/dbinit/runner.go:716) applies every StartupFiles entry
// unconditionally, with no per-file skip check, and runInstall has no
// "already installed?" probe.
//
// A single pass cannot see this class by definition. Measured on origin/main
// 2026-10-04: pass 1 = 217/217, pass 2 = 214 ok / 3 fail.
//
// These guards exist so the re-runnability list cannot rot the way an
// un-annotated one does — and so the harness cannot quietly stop enforcing it.

// TestStartupRerunGapsManifestIsAnnotated guards sql/schema/startup_rerun_known_gaps.tsv.
//
// Same three rot vectors the fresh-install manifest has, plus one specific to
// this list: an entry naming a file that is not registered grants an exemption
// to whatever later takes that name.
func TestStartupRerunGapsManifestIsAnnotated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("startup_rerun_known_gaps.tsv"))
	if err != nil {
		t.Fatalf("读不到不可重跑清单：%v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	type entry struct{ file, reason string }
	var entries []entry
	seen := map[string]bool{}
	var prev string
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
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
		// Sorted by filename, so a new entry goes where a human expects it and
		// a diff shows movement rather than a reordering.
		if prev != "" && f < prev {
			t.Errorf("条目未按文件名排序：%q 出现在 %q 之后", f, prev)
		}
		prev = f
		seen[f] = true
		entries = append(entries, entry{f, reason})
	}

	// A re-runnability list with zero entries is a legitimate future state, so
	// there is no non-empty requirement here (unlike the fresh-install list,
	// whose non-emptiness was a real invariant). What must always hold is that
	// the ratchet itself stays armed — see TestGateRatchetsUnlistedRerunFailures.

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
		t.Fatal("从 runner.go 解析出 0 条已注册启动迁移；解析失败会让下面的校验全部空转")
	}
	for _, e := range entries {
		if !registered[e.file] {
			t.Errorf("清单条目 %s 不在 runner.go 的 StartupFiles 里；它根本不会被应用，条目无意义", e.file)
		}
		if _, err := os.Stat(filepath.Join(startupDir, e.file)); err != nil {
			t.Errorf("清单条目 %s 在 embeddata/startup 下不存在：%v", e.file, err)
		}
		// A reason has to name a mechanism, not just restate the symptom. The
		// first version of this file carried reasons like "cannot drop columns
		// from view", which is the psql error text — it told a reader nothing
		// about which PG rule was hit or which other files share it. [M1]-style
		// tags make the grouping auditable instead.
		if !regexp.MustCompile(`\[M[0-9]+\]`).MatchString(e.reason) {
			t.Errorf("清单条目 %s 的原因里没有机制标签（如 [M1]）：%q\n"+
				"只有 psql 错误原文的理由无法回答「这条和别的条是不是同一类」，"+
				"而按机制分组正是这份清单存在的意义", e.file, e.reason)
		}
	}
}

// TestGateMeasuresBaselineRerunToo guards the stage that the first version of
// the re-runnability pass was missing entirely.
//
// InitSchema applies 00-prereqs / 01-schema / 02-seed BEFORE the 217
// migrations. Measured on a real database, re-applying 01-schema.sql produces
// 1665 ERROR lines — it is a bare pg_dump baseline with no IF NOT EXISTS
// anywhere — so a re-run of the installer dies THERE, long before the chain.
// The first version of the pass re-applied only the 217 and reported "3 files
// not re-runnable", which was a property of a synthetic startup-only database,
// not of the product.
//
// A gate that measures the second half of InitSchema's order and not the first
// half is measuring something the product never does.
func TestGateMeasuresBaselineRerunToo(t *testing.T) {
	act := active(gateSource(t))

	// The baseline trio has to be re-applied INSIDE the same second pass, and
	// the anchor is the loop over the three names — a die() message that merely
	// names the budget file would satisfy a weaker check. The names carry the
	// .sql suffix because they are compared against budget entries and against
	// the reported failure names; the first version of this pass looped over
	// bare "01-schema" and every comparison silently failed to match.
	if !strings.Contains(act, "for bf in 00-prereqs.sql 01-schema.sql 02-seed.sql; do") {
		t.Error("第二遍没有重跑 baseline 三件套；InitSchema 先跑它们再跑链，" +
			"漏掉它们等于门在测产品从不执行的顺序（重跑 installer 的第一现场是 01-schema.sql）。" +
			"另：循环变量必须带 .sql 后缀，否则与预算清单、与失败条目的比对全部匹配不上")
	}
	if !strings.Contains(act, `BR_BUDGET="$REPO_ROOT/sql/schema/baseline_rerun_budget.tsv"`) {
		t.Error("harness 未把 BR_BUDGET 绑定到 sql/schema/baseline_rerun_budget.tsv")
	}
	// The budget has to be a CEILING, not a membership list. Baseline failures
	// are 1665 lines of the same "already exists" shape; enumerating them would
	// be unreadable, and a membership list would be a blanket exemption. The
	// direction that matters: exceeding the ceiling must be fatal.
	if !strings.Contains(act, "actual > budget") {
		t.Error("预算没有上界比较；只有「登记/未登记」而没有上界的话，" +
			"往 01-schema.sql 里新增任何一条裸 CREATE 都会被吸收进同一个绿跑")
	}
	// …and the other direction: a baseline file that newly fails while the
	// budget only mentions some OTHER file must be fatal. Comparing only the
	// files the budget lists would pass a brand-new offender.
	if !strings.Contains(act, "重跑失败但不在预算清单里") {
		t.Error("没有「失败文件不在预算里」的致命检查；" +
			"只逐条比较预算里提到的文件，会让新出现的不可重跑文件静默通过")
	}
	if !strings.Contains(act, "baseline 的不可重跑规模超出登记预算") {
		t.Error("超预算时缺少致命退出的诊断文案")
	}
	// Deleting the budget file must make baseline re-run failures fatal rather
	// than permissive, otherwise the whole line can be switched off by removing
	// one file.
	if !strings.Contains(act, "但没有错误预算清单 ${BR_BUDGET}") {
		t.Error("缺少「预算清单不存在即致命」的分支；删掉清单会让这一类缺口变成静默放行")
	}
	// The gate and the installer must be reading the same bytes, or the whole
	// measurement is about a different file. The installer embeds
	// embeddata/*.sql; the gate reads sql/schema/*.sql. They are byte-identical
	// today, and TestBaselineFilesAreNotDivergent is what keeps them that way.
	if !strings.Contains(act, `"$REPO_ROOT/sql/schema/$bf"`) {
		t.Error("第二遍读的路径不对；应与第一遍同一份 baseline 源")
	}
	// The count has to be measured with ON_ERROR_STOP=0. The installer runs
	// with ON_ERROR_STOP=1 and therefore only ever sees the FIRST error per
	// file, which makes "1" a constant for every broken baseline — a ceiling
	// ratcheted on 1 can never fire, however much is added to the dump.
	if !strings.Contains(act, "-q -v ON_ERROR_STOP=0 < \"$REPO_ROOT/sql/schema/$bf\"") {
		t.Error("baseline 重跑没有用 ON_ERROR_STOP=0 计数；" +
			"用 1 的话每个坏掉的 baseline 都只数出 1 条，预算上界永远不可能触发")
	}
}

// TestBaselineRerunBudgetIsAnnotated guards sql/schema/baseline_rerun_budget.tsv.
//
// Two properties that a count-only file can silently lose:
//   - every file named must be one InitSchema actually applies,
//   - the number must be an integer a ratchet can compare against. A
//     hand-edited "1665 条" or a "same as before" prose would parse as zero
//     and turn the ceiling into a permanent exemption.
func TestBaselineRerunBudgetIsAnnotated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("baseline_rerun_budget.tsv"))
	if err != nil {
		t.Fatalf("读不到 baseline 重跑预算清单：%v", err)
	}
	// The three files InitSchema applies, per runner.go's own list.
	applied := map[string]bool{"00-prereqs.sql": true, "01-schema.sql": true, "02-seed.sql": true}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	seen := map[string]bool{}
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(raw, "\t")
		if len(parts) < 2 {
			t.Errorf("第 %d 行不是「文件名<TAB>预算条数」：%q", i+1, raw)
			continue
		}
		f, budget := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !applied[f] {
			t.Errorf("第 %d 行的 %q 不在 InitSchema 应用的三件套里（00-prereqs / 01-schema / 02-seed）", i+1, f)
		}
		if seen[f] {
			t.Errorf("重复条目：%s", f)
		}
		seen[f] = true
		n, err := strconv.Atoi(budget)
		if err != nil {
			t.Errorf("条目 %s 的预算 %q 不是整数；比较会退化成 0，整条线变成永久豁免：%v", f, budget, err)
			continue
		}
		if n <= 0 {
			t.Errorf("条目 %s 的预算为 %d；0 错误不该登记（登记了的话「失败文件不在清单里」这条 die 就再也触发不了）", f, n)
		}
	}
	if len(seen) == 0 {
		t.Fatal("预算清单里一条数据都没有；实测 01-schema.sql 重跑 1665 条 ERROR，解析失败会让校验全部空转")
	}
}

// TestGateRatchetsUnlistedRerunFailures guards the ratchet itself.
//
// Anchors on the ASSIGNMENT, not on a mention: the existing fresh-install guard
// documented that a bare substring cannot tell a binding from a die() message
// that merely names the variable — renaming GAP_MANIFEST left the old guard
// green for exactly that reason. Same failure mode would apply here, so the
// assignment and the reader are asserted separately.
func TestGateRatchetsUnlistedRerunFailures(t *testing.T) {
	act := active(gateSource(t))

	if !strings.Contains(act, `RR_MANIFEST="$REPO_ROOT/sql/schema/startup_rerun_known_gaps.tsv"`) {
		t.Error("harness 未把 RR_MANIFEST 绑定到 sql/schema/startup_rerun_known_gaps.tsv；" +
			"只检查变量名出现过是不够的——die 消息里提到它就能满足那种判据")
	}
	if !strings.Contains(act, `"$RR_MANIFEST" | sort -u`) {
		t.Error("harness 没有真正用 RR_MANIFEST 读取清单内容；" +
			"绑定存在但清单没被读，第二遍失败比对会对空集合静默放行")
	}
	// The second pass has to be ON by default, or the whole apparatus is dead
	// weight that only runs for whoever remembers to pass the flag.
	if !strings.Contains(act, `GATE_APPLY_STARTUP_TWICE="${GATE_APPLY_STARTUP_TWICE:-1}"`) {
		t.Error("GATE_APPLY_STARTUP_TWICE 未默认开启；第二遍是这一类缺陷唯一能被看见的时机，" +
			"默认关掉等于门形同虚设")
	}
	if !strings.Contains(act, `[[ "$GATE_APPLY_STARTUP_TWICE" == "1" ]]`) {
		t.Error("第二遍没有独立的开关判据；上面那行默认值将无人读取")
	}
	// An UNREGISTERED second-pass failure must be fatal — that is the direction
	// of the ratchet, and it is enforced here rather than by the manifest.
	if !strings.Contains(act, "不可重跑且未登记为已知缺口") {
		t.Error("未登记的「第二遍失败」缺少致命退出的诊断文案；" +
			"守卫必须锚定这条具体文案，否则窗口内的任意 die 都能满足判据")
	}
	// A listed entry that did not fail must be surfaced, so a real fix retires
	// its entry instead of the list becoming a permanent blanket exemption.
	if !strings.Contains(act, "rr_stale") {
		t.Error("没有对「清单里登记了但本轮未复现」的条目做上报；" +
			"少了这一条，修复之后条目会留在清单里变成永久豁免")
	}
	// Both passes must go through the SAME apply helper. The first version of
	// the re-runnability pass copied the loop and drifted from the original by
	// the --single-transaction rule — precisely the kind of difference that
	// makes a harness report a defect that isn't there.
	if strings.Count(act, "apply_startup_file ") < 2 {
		t.Errorf("两遍没有共用同一个 apply 函数（出现 %d 次）；"+
			"复制循环会让两遍在事务规则上漂移，而漂移出来的差异会被当成产品缺陷",
			strings.Count(act, "apply_startup_file "))
	}
	if !strings.Contains(act, "apply_startup_file() {") {
		t.Error("缺少 apply_startup_file 函数定义")
	}
	if !strings.Contains(act, `apply_startup_file "$f" "rerun:"`) {
		t.Error("第二遍没有给条目打 rerun: 标记；缺了它就无法把两遍的失败清单分开")
	}
}

// TestGateRerunPassBumpsItsOwnCounters guards the failure mode that made the
// first version of the re-runnability pass worthless.
//
// The helper hardcoded `sf_*`, the re-runnability ratchet tested `rr_fail`, so
// rr_fail stayed 0 forever: the run printed "startup rerun: applied=0 failed=0
// missing=0" and the gate that LOOKED armed could never fire. The static
// guards could not catch it — they read the script as text, and the text said
// `rr_fail > 0`.
//
// So this one exercises the helper instead: source the function out of the
// script, run it against a real temp file, and assert the counter the caller
// NAMED is the one that moves. Two different counter names, two different
// totals — that is the property the bug broke.
func TestGateRerunPassBumpsItsOwnCounters(t *testing.T) {
	src := gateSource(t)

	// Pull out just the function definition.
	start := strings.Index(src, "apply_startup_file() {")
	if start < 0 {
		t.Fatal("脚本里没有 apply_startup_file 函数；两遍共用一个 apply 是防止漂移的关键")
	}
	end := strings.Index(src[start:], "\n  }")
	if end < 0 {
		t.Fatal("找不到 apply_startup_file 的函数体结尾；解析失败会让本守卫空转")
	}
	fnBody := src[start : start+end+len("\n  }")]

	// Give it what it reads from the enclosing scope.
	harness := `
SF_DIR="$1"
sf_failed=()
rr_failed=()
sf_ok=0; sf_fail=0; sf_missing=0
rr_ok=0; rr_fail=0; rr_missing=0
# The helper reaches psql through "docker exec ... < "$SF_DIR/$f"", i.e. the
# migration arrives on stdin and its FILENAME IS NEVER PASSED AS AN ARGUMENT.
# The stub therefore has to decide on the payload, not on "$*" — an earlier
# version matched *bad.sql* there, never matched, and the fail counter stayed
# at 0 while the test still looked wired up.
docker() { local data=""; [[ ! -t 0 ]] && data=$(cat)
           case "$data" in *SELECT\ bad*) return 1;; esac; return 0; }
` + fnBody + `
# Two SEPARATE arrays, on purpose. An earlier version of this harness collected
# into a hardcoded sf_failed and asserted only on that one array, so it stayed
# green while the re-runnability pass collected nothing — which is how a real
# run counted 11 failures, graded an empty list, and exited PASS=7 FAIL=0.
# Each pass must be visible in ITS OWN list and NOT in the other's.
apply_startup_file "good.sql"  ""        sf_ok sf_fail sf_missing sf_failed
apply_startup_file "bad.sql"   ""        sf_ok sf_fail sf_missing sf_failed
apply_startup_file "absent.sql" "rerun:" rr_ok rr_fail rr_missing rr_failed
echo "sf=$sf_ok,$sf_fail,$sf_missing rr=$rr_ok,$rr_fail,$rr_missing"
echo "sfentries=${#sf_failed[@]} rrentries=${#rr_failed[@]}"
echo "--- sf_failed ---"; for e in "${sf_failed[@]}"; do echo "ENTRY|$e"; done
echo "--- rr_failed ---"; for e in "${rr_failed[@]}"; do echo "ENTRY|$e"; done
`
	dir := t.TempDir()
	good := filepath.Join(dir, "good.sql")
	if err := os.WriteFile(good, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// bad.sql must EXIST: the helper checks for the file before it calls psql,
	// so a missing bad.sql would take the missing-file branch instead of the
	// failure branch, and the fail counter would never move. It is the docker
	// stub, not this file's content, that decides the outcome.
	bad := filepath.Join(dir, "bad.sql")
	if err := os.WriteFile(bad, []byte("SELECT bad;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Note there is no absent.sql on disk: the missing-file branch is one of
	// the two we need to count, so the file must genuinely not exist.
	scriptPath := filepath.Join(dir, "harness.sh")
	if err := os.WriteFile(scriptPath, []byte(harness), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the dir: the file names are baked into the harness, because the
	// helper is exercised for its counters and its collection, not its args.
	out, err := exec.Command("bash", scriptPath, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("跑取出来的 helper 失败：%v\n%s", err, out)
	}
	out_s := string(out)
	if !strings.Contains(out_s, "sf=1,1,0") || !strings.Contains(out_s, "rr=0,0,1") {
		t.Fatalf("helper 没有递增调用方指定的计数器：\n%s\n"+
			"期望 sf=1,1,0（good 成功 / bad 失败）且 rr=0,0,1（第二遍的缺失文件）。"+
			"若 rr 恒为 0，说明第二遍的 ratchet 永远不会触发——"+
			"这正是第一版的缺陷：helper 写死 sf_*，而 ratchet 判 rr_fail",
			out_s)
	}
	// THE defect that a real run caught and this test did not: the counters
	// were caller-named while the failure LIST was not. Each pass must land in
	// its own array, and the cross-contamination direction matters — a second
	// pass appending into the first pass's list is how the 2026-10-04 run
	// counted 11 failures, iterated an empty rr_failed, and passed.
	if !strings.Contains(out_s, "sfentries=1 rrentries=1") {
		t.Fatalf("helper 没有把两遍的失败分别收进调用方各自的数组：\n%s\n"+
			"期望 sfentries=1 rrentries=1。rrentries=0 而 rr_fail>0 正是"+
			"「计数与清单不同源」的形态：ratchet 拿到空集合会把任何真实失败判成全部已登记。",
			out_s)
	}
	// Split the two sections apart and check each one on its own, so an entry
	// that ends up in the wrong list is attributed to the wrong pass.
	var sfEntries, rrEntries []string
	section := ""
	for _, line := range strings.Split(out_s, "\n") {
		switch {
		case strings.HasPrefix(line, "--- sf_failed ---"):
			section = "sf"
		case strings.HasPrefix(line, "--- rr_failed ---"):
			section = "rr"
		case strings.HasPrefix(line, "ENTRY|"):
			entry := strings.TrimPrefix(line, "ENTRY|")
			if section == "sf" {
				sfEntries = append(sfEntries, entry)
			} else if section == "rr" {
				rrEntries = append(rrEntries, entry)
			}
		}
	}
	if len(sfEntries) != 1 || !strings.HasPrefix(sfEntries[0], "bad.sql") {
		t.Errorf("第一遍的清单不对，期望恰好 1 条 bad.sql 失败：%q", sfEntries)
	}
	// The label has to survive into the collection, otherwise the two passes'
	// failures cannot be told apart afterwards — which is the whole reason the
	// label exists.
	if len(rrEntries) != 1 || !strings.HasPrefix(rrEntries[0], "rerun:") {
		t.Errorf("第二遍的清单不对，期望恰好 1 条带 rerun: 标记的缺失文件：%q", rrEntries)
	}
}
