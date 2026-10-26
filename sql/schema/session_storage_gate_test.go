package schema

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

type sessionDBContract struct {
	database, pkg, testPrefix, fixture string
}

// These fixtures DROP public. Registration means proving a separate required
// runner owns them, never injecting the generic populated gate database.
func dedicatedSessionDB(name string) (sessionDBContract, bool) {
	switch name {
	case "TEST_SESSION_INGEST_DATABASE_URL":
		return sessionDBContract{"session_ingest_test", "./internal/sessionv2mirror", "TestSessionIngest", "internal/sessionv2mirror/ingest_schema_realdb_test.go"}, true
	case "TEST_SESSION_SETTINGS_DATABASE_URL":
		return sessionDBContract{"session_settings_test", "./settings", "TestSessionWriteSettings", "settings/session_write_safety_realdb_test.go"}, true
	default:
		return sessionDBContract{}, false
	}
}

type sessionGateSources struct{ generic, runner, gate, verify, fixture string }

// destructiveGuardedDB names a DSN that must **never** be injected by the
// integration-gate fact source, and which protects itself instead: the test
// skips when the variable is unset, and a runtime guard rejects any database
// that does not look like a disposable scratch/e2e library.
//
// R86 轮 introduced REPORT_E2E_TEST_DATABASE_URL when it split the shared
// TEST_DATABASE_URL / TEST_DB_URL away from the destructive reportrollup legs,
// and registered it as `dsn_exclusive` — but this gate kept its own two-entry
// switch, so it demanded the exact injection that the registry forbids.
// The red was the gate being right about the shape and wrong about the
// bookkeeping.
//
// ★ Why this cannot be folded into dedicatedSessionDB: those two own a
// **required dedicated runner** (createdb + isolation opt-in + a -run filter).
// reportrollup has no runner on purpose — the 2026-09-29 incident (~978k
// usage_facts rows deleted because a developer library was passed as a
// scratch library) is why it stays opt-in and guard-enforced rather than
// being wired into a populated gate run.
var destructiveGuardedDB = map[string]string{
	"REPORT_E2E_TEST_DATABASE_URL": "domains/reportrollup/realdb_e2e_test.go",
}

// destructiveGuardedDBProblems asserts the "must stay out of the fact source"
// property. Registration is only half the claim; the guard has to keep
// rejecting real databases, otherwise dropping it would silently re-open the
// destructive path while the gate stayed green.
func destructiveGuardedDBProblems(name, fixture string, src sessionGateSources) []string {
	var problems []string
	require := func(ok bool, property string) {
		if !ok {
			problems = append(problems, property)
		}
	}
	require(!regexp.MustCompile(regexp.QuoteMeta(name)+`="\$GATE_URL"`).MatchString(active(src.generic)),
		"shared DSN injection")
	read := func(path string) string {
		data, err := os.ReadFile("../../" + path)
		if err != nil {
			problems = append(problems, "read fixture: "+err.Error())
			return ""
		}
		return string(data)
	}
	body := read(fixture)
	if body == "" {
		return problems
	}
	require(strings.Contains(body, `os.Getenv("`+name+`")`), "fixture DSN consumer")
	// The skip arm is what makes "unset" the safe state.
	require(regexp.MustCompile(`(?s)os\.Getenv\("`+regexp.QuoteMeta(name)+`"\)\s*\n\s*if [^{]*\{\s*\n?\s*t\.Skip\(`).MatchString(body),
		"unset skips instead of failing open")
	// The scratch-library guard is what makes "wrong DSN" fail loudly.
	//
	// ★ Must assert the guard is **called** on the resolved DSN, not merely
	// that its definition still sits in the file. Checking for the name alone
	// is satisfied by an uncalled helper — mutation M3 removed the single call
	// site and the gate stayed green, which is precisely the regression this
	// registration exists to catch.
	require(strings.Contains(body, "func guardDestructiveE2E(") &&
		strings.Contains(body, `REPORT_E2E_ALLOW_DESTRUCTIVE`), "destructive scratch guard")
	require(regexp.MustCompile(`guardDestructiveE2E\(\s*t\s*,\s*dsn\s*\)`).MatchString(body),
		"destructive guard call site")
	return problems
}

func sessionStorageGateSources(t *testing.T, contract sessionDBContract, generic string) sessionGateSources {
	t.Helper()
	read := func(path string) string {
		data, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatalf("read dedicated session gate %s: %v", path, err)
		}
		return string(data)
	}
	src := sessionGateSources{generic, "", "", "", ""}
	if contract == (sessionDBContract{}) {
		// A self-guarded destructive leg owns no dedicated runner: it has no
		// createdb, no isolation opt-in and no -run filter to prove. Reading
		// those files would assert nothing and only couple this guard to
		// unrelated scripts.
		return src
	}
	src.runner = read("scripts/audit/run-session-storage-stream.sh")
	src.gate = read("tests/session_storage_audit_gate.sh")
	src.verify = read("verify.sh")
	src.fixture = read(contract.fixture)
	return src
}

func sessionStorageGateProblems(name string, contract sessionDBContract, src sessionGateSources) []string {
	var problems []string
	require := func(ok bool, property string) {
		if !ok {
			problems = append(problems, property)
		}
	}
	// Join only shell continuations; comments and echo lines cannot satisfy an
	// anchored command check. Require execution, not a mention of the package.
	runner := strings.ReplaceAll(active(src.runner), "\\\n", " ")
	line := func(command string) string { return `(?m)^` + regexp.QuoteMeta(command) + `$` }
	create := `docker exec "$pg_id" createdb -U postgres ` + contract.database
	export := `export ` + name + `="postgres://postgres@$pg_addr/` + contract.database + `?sslmode=disable"`
	isolate := `export TEST_SESSION_INGEST_ISOLATED=1`
	testCommand := regexp.MustCompile(`(?m)^go test [^\n]*` + regexp.QuoteMeta(contract.pkg) + ` [^\n]*$`).FindString(runner)
	require(!regexp.MustCompile(regexp.QuoteMeta(name)+`="\$GATE_URL"`).MatchString(active(src.generic)), "shared DSN injection")
	require(regexp.MustCompile(line(create)).MatchString(runner), "dedicated database creation")
	require(regexp.MustCompile(line(export)).MatchString(runner), "dedicated database export")
	require(regexp.MustCompile(line(isolate)).MatchString(runner), "isolation opt-in")
	require(testCommand != "" && strings.Contains(testCommand, "-tags=integration") &&
		strings.Contains(testCommand, "-run '^"+contract.testPrefix+"'") &&
		strings.Contains(testCommand, "-count=1") && !strings.Contains(testCommand, "||"), "fixture execution")
	if testCommand != "" {
		testAt := strings.Index(runner, testCommand)
		require(strings.Index(runner, create) >= 0 && strings.Index(runner, create) < strings.Index(runner, export) &&
			strings.Index(runner, export) < testAt && strings.Index(runner, isolate) >= 0 && strings.Index(runner, isolate) < testAt,
			"isolation before fixture execution")
	}
	guard := `(?m)^\s*if !strings\.HasSuffix\(cfg\.ConnConfig\.Database, "_test"\) \|\| os\.Getenv\("TEST_SESSION_INGEST_ISOLATED"\) != "1" \{\s*t\.Fatal\(`
	require(strings.Contains(src.fixture, `dsn := os.Getenv("`+name+`")`), "fixture DSN consumer")
	require(regexp.MustCompile(guard).MatchString(src.fixture), "fixture isolation guard")
	require(regexp.MustCompile(line(`bash scripts/audit/run-session-storage-stream.sh --require-coherence "$@"`)).MatchString(active(src.gate)) &&
		strings.Contains(active(src.gate), "set -euo pipefail"), "required storage runner")
	verify := active(src.verify)
	entry := `bash tests/session_storage_audit_gate.sh`
	require(regexp.MustCompile(line(entry)).MatchString(verify) &&
		strings.Index(verify, entry) < strings.Index(verify, `if [[ "$RUN_WEB" == true ]]`), "default verification entry")
	return problems
}

// Mutation checks protect the special registration from becoming a known-ignore
// list: losing any execution or isolation edge must fail its own property.
func TestDedicatedSessionDBGateRejectsMissingIsolationAndExecution(t *testing.T) {
	for _, name := range []string{"TEST_SESSION_INGEST_DATABASE_URL", "TEST_SESSION_SETTINGS_DATABASE_URL"} {
		t.Run(name, func(t *testing.T) {
			contract, ok := dedicatedSessionDB(name)
			if !ok {
				t.Fatal("dedicated fixture has no contract")
			}
			src := sessionStorageGateSources(t, contract, gateSource(t))
			if problems := sessionStorageGateProblems(name, contract, src); len(problems) != 0 {
				t.Fatalf("real required runner is incomplete: %v", problems)
			}
			for _, tc := range []struct {
				name, property string
				mutate         func(*sessionGateSources)
			}{
				{"shared-database", "shared DSN injection", func(s *sessionGateSources) { s.generic += "\n" + name + `="$GATE_URL"` }},
				{"no-createdb", "dedicated database creation", func(s *sessionGateSources) {
					s.runner = strings.Replace(s.runner, `docker exec "$pg_id" createdb -U postgres `+contract.database, "# removed", 1)
				}},
				{"no-export", "dedicated database export", func(s *sessionGateSources) {
					s.runner = strings.Replace(s.runner, "export "+name+"=", "# removed "+name+"=", 1)
				}},
				{"shared-export", "dedicated database export", func(s *sessionGateSources) {
					s.runner = strings.Replace(s.runner, "/"+contract.database+"?sslmode=disable", "/postgres?sslmode=disable", 1)
				}},
				{"no-isolation-opt-in", "isolation opt-in", func(s *sessionGateSources) {
					s.runner = strings.Replace(s.runner, "export TEST_SESSION_INGEST_ISOLATED=1", "# removed", 1)
				}},
				{"no-fixture-guard", "fixture isolation guard", func(s *sessionGateSources) {
					s.fixture = strings.Replace(s.fixture, `if !strings.HasSuffix`, `if false && !strings.HasSuffix`, 1)
				}},
				{"wrong-fixture-dsn", "fixture DSN consumer", func(s *sessionGateSources) {
					s.fixture = strings.Replace(s.fixture, `dsn := os.Getenv("`+name+`")`, `dsn := os.Getenv("TEST_DB_URL")`, 1)
				}},
				{"no-fixture-execution", "fixture execution", func(s *sessionGateSources) {
					s.runner = strings.Replace(s.runner, contract.pkg+" -run", "./wrong-package -run", 1)
				}},
				{"optional-runner", "required storage runner", func(s *sessionGateSources) {
					s.gate = strings.Replace(s.gate, "bash scripts/audit/run-session-storage-stream.sh", "# removed", 1)
				}},
				{"no-default-entry", "default verification entry", func(s *sessionGateSources) {
					s.verify = strings.Replace(s.verify, "bash tests/session_storage_audit_gate.sh\n", "# removed\n", 1)
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					mutated := src
					tc.mutate(&mutated)
					if problems := sessionStorageGateProblems(name, contract, mutated); !contains(problems, tc.property) {
						t.Fatalf("mutation did not fail %q: %v", tc.property, problems)
					}
				})
			}
		})
	}
}
