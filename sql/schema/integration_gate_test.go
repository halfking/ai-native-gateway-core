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
	"os"
	"regexp"
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

// TestGateInjectsEveryDBCredentialName pins the multi-name problem. The suite
// reads the database URL under five distinct names; injecting only one leaves
// the other 66 files silently skipped. Adding a sixth name without adding it
// here would reintroduce exactly that.
func TestGateInjectsEveryDBCredentialName(t *testing.T) {
	act := active(gateSource(t))
	for _, name := range []string{
		"TEST_PG_URL", "TEST_DATABASE_URL", "TEST_DB_URL",
		"LLM_GATEWAY_PG_URL", "DATABASE_URL",
	} {
		if !regexp.MustCompile(name + `="\$GATE_URL"`).MatchString(act) {
			t.Errorf("harness 未注入 %s=$GATE_URL；只注入一部分名字会让其余 integration "+
				"文件全部 skip 而 CI 仍显示绿", name)
		}
	}
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

// TestWorkflowWiresTheGate pins that the harness is actually reachable from CI.
//
// A harness nobody invokes is not orchestration: it can sit in the tree looking
// complete while CI keeps running the vacuous form. This asserts the workflow
// names the script, and — more importantly — that it provisions a database and
// sets a PG_CONTAINER, because the pre-round-43 job ran
// `go test -tags=integration ./...` with no database service and no URL in the
// environment at all.
func TestWorkflowWiresTheGate(t *testing.T) {
	const wf = "../../.github/workflows/integration-testcontainers-ci.yml"
	b, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("read %s: %v", wf, err)
	}
	src := string(b)

	if !strings.Contains(src, "run-integration-gate.sh") {
		t.Error("CI workflow 未调用 scripts/audit/run-integration-gate.sh；" +
			"harness 存在但不可达，等于没有编排")
	}
	// A database must be started, not assumed.
	if !strings.Contains(src, "docker run -d --name") {
		t.Error("workflow 未启动数据库容器；没有库时 DB 门控的测试全部 skip，CI 仍显示绿")
	}
	// The stock-postgres trap: a vanilla image dies at CREATE EXTENSION citus,
	// so pinning the project's build is what keeps the gate meaningful.
	if !strings.Contains(src, "registry.internal.example.com/kx-citus-pg17") {
		t.Error("workflow 未钉住项目的 kx-citus-pg17 镜像；换 stock postgres 会让 " +
			"citus / citus_columnar 不可用，门禁转而测环境而非 schema")
	}
	// Preflight the extensions so a wrong image fails with one clear error
	// instead of a cascade of columnar assertion failures.
	if !strings.Contains(src, "pg_available_extensions") {
		t.Error("workflow 未预检扩展可用性；镜像不对时报错会淹没在逐条断言失败里")
	}
	// PG_CONTAINER must be defined, because the harness defaults to a local-only
	// container name and would then not find CI's.
	if !regexp.MustCompile(`PG_CONTAINER:\s*\S+`).MatchString(src) {
		t.Error("workflow 未设置 PG_CONTAINER；harness 会去找本机默认容器名并 die")
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
	// failure. It must be reported without failing the run.
	if !strings.Contains(act, "sf_failed+=") {
		t.Error("未收集未应用的启动迁移清单")
	}
	if !strings.Contains(act, "known fresh-install gaps") {
		t.Error("未把「启动迁移未应用」标为已知缺口而非门禁失败")
	}
}
