// Round 43: contract guards for scripts/audit/fresh-schema-from-migrations.sh.
//
// The script is an audit instrument, so its own failure modes are the
// dangerous part: an instrument that reports "complete" for a run that never
// happened is worse than no instrument. The previous version had three such
// holes, all found by running it rather than reading it:
//
//   - container/user hardcoded to kx-citus/kxuser, so it could not run on this
//     workstation at all;
//   - no population assertion, so a silently failed CREATE DATABASE yields
//     applied=0 failed=0 and a cheerful "complete";
//   - a file selector that matched only 464 of 777 migrations, silently
//     excluding every date-prefixed one such as
//     2026-07-13-multimodal-token-fields-hot.sql.
//
// These guards pin the properties that make it trustworthy. They read the
// script as text and assert on its structure, which is appropriate here: the
// subject IS a shell script, and the properties are about which paths and
// guards it contains.
package schema

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const auditScript = "../../scripts/audit/fresh-schema-from-migrations.sh"

func auditScriptSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(auditScript)
	if err != nil {
		t.Fatalf("read %s: %v", auditScript, err)
	}
	return string(b)
}

// activeLines drops comment lines so a guard is not satisfied by prose that
// merely mentions a path. Same rule as the dbinit no-transaction marker.
func activeLines(src string) []string {
	var out []string
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func active(src string) string {
	return strings.Join(activeLines(src), "\n")
}

// TestAuditScriptHasNoHardcodedContainerOrUser pins the parameterization.
// A hardcoded kx-citus/kxuser is not a style issue: it is why the script was
// unrunnable here for its entire life.
func TestAuditScriptHasNoHardcodedContainerOrUser(t *testing.T) {
	act := active(auditScriptSource(t))
	for _, bad := range []string{"kx-citus", "kxuser"} {
		if strings.Contains(act, bad) {
			t.Errorf("活跃行里仍硬编码 %q；本机容器是 llm-gateway-pg / llm_gateway，"+
				"硬编码会让脚本在本机根本无法运行（应走 PG_CONTAINER / PG_USER）", bad)
		}
	}
	for _, want := range []string{"PG_CONTAINER:-", "PG_USER:-"} {
		if !strings.Contains(act, want) {
			t.Errorf("脚本未提供 %s 默认值", want)
		}
	}
}

// TestAuditScriptAssertsPopulation guards the "假绿" hole. db_populated must
// exist AND be consulted in a way that aborts when the database is empty —
// a definition nobody calls would satisfy a naive presence check.
func TestAuditScriptAssertsPopulation(t *testing.T) {
	src := auditScriptSource(t)
	if !strings.Contains(src, "db_populated") {
		t.Fatal("脚本没有 db_populated 守卫：CREATE DATABASE 静默失败时会把 0 失败报成 complete")
	}
	if !regexp.MustCompile(`db_populated\s*\(\)\s*\{`).MatchString(src) {
		t.Error("db_populated 只被调用，未见其定义")
	}
	// It must be able to fail the run, not just print.
	if !strings.Contains(src, "db_populated || true") &&
		!strings.Contains(src, "if ! db_populated") &&
		!strings.Contains(src, "db_populated || die") {
		t.Error("db_populated 的返回值在所有调用点都被丢弃；空库不会被判为失败")
	}
	// And CREATE DATABASE must be checked, not assumed.
	if !strings.Contains(src, "refusing to report a result from a database that does not exist") {
		t.Error("CREATE DATABASE 失败未导致致命退出")
	}
}

// TestAuditScriptSelectionCoversDatePrefixedMigrations is the coverage
// regression: the old pattern required an underscore right after the numeric
// prefix, so `2026-07-13-multimodal-token-fields-hot.sql` never matched and
// only 464 of 777 files were considered.
func TestAuditScriptSelectionCoversDatePrefixedMigrations(t *testing.T) {
	src := auditScriptSource(t)
	m := regexp.MustCompile(`grep -E '([^']*)'`).FindAllStringSubmatch(src, -1)
	found := false
	for _, g := range m {
		pat := g[1]
		if !strings.HasPrefix(pat, "^") {
			continue
		}
		// A pattern anchored at ^ that allows a date-shaped prefix must not
		// demand an underscore immediately after the digits.
		if !regexp.MustCompile(`\^\[0-9\]\[a-z0-9\]\*_`).MatchString(pat) {
			found = true
		}
	}
	if !found {
		t.Error("迁移文件选择正则里仍存在要求「数字前缀后紧跟下划线」的模式；" +
			"它会静默漏掉 2026-07-13-… 这类日期前缀迁移（上一版 464/777）")
	}
	// The numeric prefix must be parsed leniently, or 328a aborts the loop.
	if !strings.Contains(src, `sed -E 's/^([0-9]+).*/`) {
		t.Error("未用正则提取纯数字前缀；328a / 2026-07-13-… 会让 $((10#$n)) " +
			"成为算术错误并中途终止循环，而脚本仍会打印 complete")
	}
}

// TestAuditScriptNoRepoRootSideEffects stops the .err files landing in the
// repository. Round 43's first version wrote 2>"$label.err" (a relative path)
// while reading /tmp/$label.err, so every failure printed an empty reason AND
// mig.err / prereqs.err were created in the repo root.
func TestAuditScriptNoRepoRootSideEffects(t *testing.T) {
	// Comment-aware: the script documents this very bug in a comment, so a
	// raw-text search for $label.err matches the explanation rather than code
	// and the guard would be permanently red. Strip comments first.
	act := active(auditScriptSource(t))
	if regexp.MustCompile(`2>"\$label\.err"`).MatchString(act) {
		t.Error("活跃行里仍把 psql 诊断写到相对路径 $label.err；那会在仓库根生成 .err 文件")
	}
	if !strings.Contains(act, "ERRFILE=/tmp/") {
		t.Error("诊断输出未统一到绝对的 ERRFILE 路径")
	}
	// One definition, read and written on the same path.
	if !strings.Contains(act, `2>"$ERRFILE"`) || !strings.Contains(act, `grep -i '^ERROR' "$ERRFILE"`) {
		t.Error("ERRFILE 的写入点与读取点不一致；失败原因会再次变空")
	}
}
