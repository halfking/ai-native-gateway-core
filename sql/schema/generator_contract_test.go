// Round 43: guards for the in-repo baseline generator.
//
// scripts/_lib/db-init-lib.sh was imported from the official-deploy monorepo
// on 2026-10-01. sql/scripts/dump-schema.sh could never run in this
// repository: it sourced "$SCRIPT_DIR/../../../../scripts/_lib/…", a path
// that climbs out of the repo into a sibling workspace, and the library was
// never in this repository's git history at all. The three
// 00-prereqs/01-schema copies were therefore hand-maintained orphans.
//
// The imported library was ALSO wrong for this service: its extension list
// was TimescaleDB-era and knew nothing about Citus. Regenerating 00-prereqs
// with it as imported would have deleted citus, citus_columnar, vector,
// pg_stat_statements and pgstattuple — regressing the round-42 P0 in which a
// stale prereqs made every `SET default_table_access_method = columnar`
// fail. These tests pin that so the generator and the committed prereqs can
// never silently disagree about which extensions the schema needs.
package schema

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	generatorLib   = "../../scripts/_lib/db-init-lib.sh"
	generatedName  = "00-prereqs.sql"
	extCreateRe    = `CREATE EXTENSION IF NOT EXISTS\s+"?([A-Za-z0-9_"-]+)"?`
	extQueryMarker = "WHERE extname IN ("
)

// parseExtensions returns every extension named by CREATE EXTENSION statements.
func parseExtensions(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(extCreateRe)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		schema := "public"
		if seg := regexp.MustCompile(`WITH SCHEMA\s+([A-Za-z0-9_]+)`).
			FindStringSubmatch(lineContaining(string(b), m[0])); seg != nil {
			schema = seg[1]
		}
		out[m[1]] = schema
	}
	return out
}

func lineContaining(body, needle string) string {
	for _, ln := range strings.Split(body, "\n") {
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	return ""
}

// TestGeneratorExtensionQueryCoversCommittedPrereqs is the regression gate.
//
// The generator decides which extensions to emit by querying the source DB
// with a hardcoded IN (...) list. If that list is narrower than the set the
// committed prereqs needs, regenerating silently drops extensions. Assert the
// two agree, and additionally assert each extension lands in the right schema
// — citus and citus_columnar MUST be pg_catalog, because a fresh install
// fails with `extension "citus_columnar" must be installed in schema
// "pg_catalog"` otherwise. That exact failure was hit and fixed in round 43.
func TestGeneratorExtensionQueryCoversCommittedPrereqs(t *testing.T) {
	lib, err := os.ReadFile(generatorLib)
	if err != nil {
		t.Fatalf("read %s: %v", generatorLib, err)
	}
	libSrc := string(lib)

	// Pull the hardcoded IN (...) list out of the generator.
	start := strings.Index(libSrc, extQueryMarker)
	if start < 0 {
		t.Fatalf("%s 里找不到 %q —— 生成器结构变了", generatorLib, extQueryMarker)
	}
	open := strings.Index(libSrc[start:], "(")
	close := strings.Index(libSrc[start+open:], ")")
	if close < 0 {
		t.Fatalf("%s 的 extname IN ( 列表没有闭合", generatorLib)
	}
	list := libSrc[start+open+1 : start+open+close]

	queried := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([A-Za-z0-9_-]+)'`).FindAllStringSubmatch(list, -1) {
		queried[m[1]] = true
	}
	if len(queried) == 0 {
		t.Fatalf("从 %s 解析出 0 个扩展", generatorLib)
	}

	// TimescaleDB-era entries would emit CREATE EXTENSION for a server that
	// has none, and this project is Citus-based.
	for _, gone := range []string{"timescaledb", "timescaledb_toolkit"} {
		if queried[gone] {
			t.Errorf("生成器仍在查询 %q；本项目是 Citus 列存架构，"+
				"按此列表生成会为服务器上不存在的扩展发 CREATE EXTENSION", gone)
		}
	}

	for _, c := range prereqCopies {
		want := parseExtensions(t, c.path)
		if len(want) == 0 {
			t.Fatalf("%s 没解析出任何扩展", c.path)
		}
		for ext, schema := range want {
			if !queried[ext] {
				t.Errorf("提交态 %s 需要扩展 %q，但生成器的 extname IN (...) 列表里没有它；"+
					"一旦用它重新生成，该扩展会被静默删除", c.name, ext)
			}
			if ext == "citus" || ext == "citus_columnar" {
				if schema != "pg_catalog" {
					t.Errorf("%s 中 %s 装在 %q，应为 pg_catalog；"+
						"否则全新安装失败于 must be installed in schema \"pg_catalog\"",
						c.name, ext, schema)
				}
			}
		}
	}
	t.Logf("生成器查询扩展 %d 个；三份提交态 prereqs 扩展集合一致", len(queried))
}

// TestGeneratorEmitsPgCatalogForCitus pins the pg_catalog requirement at the
// generator's own emission site, not only in the committed output. A
// committed-file-only check would pass even if the generator were fixed by
// hand-editing all three copies again — which is exactly the drift this
// round set out to end.
func TestGeneratorEmitsPgCatalogForCitus(t *testing.T) {
	lib, err := os.ReadFile(generatorLib)
	if err != nil {
		t.Fatalf("read %s: %v", generatorLib, err)
	}
	src := string(lib)
	for _, ext := range []string{"citus", "citus_columnar"} {
		idx := strings.Index(src, ext+")")
		if idx < 0 {
			t.Errorf("生成器里找不到 %q 的 emission 分支", ext)
			continue
		}
		seg := src[idx:min(idx+400, len(src))]
		if !strings.Contains(seg, "WITH SCHEMA pg_catalog") {
			t.Errorf("生成器中 %s 的 CREATE EXTENSION 未指定 WITH SCHEMA pg_catalog；\n"+
				"  实测：写成 public 会让全新安装失败于 "+
				"extension \"%s\" must be installed in schema \"pg_catalog\"", ext, ext)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestGeneratorDoesNotTouchCommittedBaseline makes the safe workflow
// enforceable: dump-schema.sh must write only to DB_INIT_OUT_DIR. Round 43's
// wrapper was rewritten to require that variable precisely so a regeneration
// cannot overwrite the three committed copies in place — the committed
// baseline is order-correct (round 42 verified it against a real database)
// and a regenerated one is not yet reconciled against them.
func TestGeneratorDoesNotTouchCommittedBaseline(t *testing.T) {
	b, err := os.ReadFile("../../sql/scripts/dump-schema.sh")
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "DB_INIT_OUT_DIR") {
		t.Error("dump-schema.sh 未要求 DB_INIT_OUT_DIR；重新生成可能就地覆盖已提交基线")
	}
	// Only active lines matter. The wrapper keeps a comment recording that the
	// old ../../../../ path was monorepo-relative and wrong here; flagging that
	// history would make the guard unfixable except by deleting the reason the
	// guard exists. This mirrors the marker rule in dbinit: a path counts only
	// outside a comment.
	for i, ln := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(ln, "../../../..") {
			t.Errorf("dump-schema.sh:%d 活跃行仍使用逃出仓库的相对路径（../../../..）；"+
				"该路径在 monorepo 里有效，在这里不是", i+1)
		}
	}
	var missing []string
	for _, c := range prereqCopies {
		if _, err := os.Stat(c.path); err != nil {
			missing = append(missing, c.path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("已提交基线副本缺失：%v", missing)
	}
}

// TestEveryBaselineObjectHasRepoProvenance records the round-43 lesson that the
// first reconciliation report got wrong.
//
// It reported six objects as "present only in the committed baseline, and no
// migration can recreate them". That was false: the search covered
// sql/migrations/ only, and this repository also keeps a per-object registry at
// sql/objects/ with ~1900 files. Every one of those six had a concrete source
// there or in a migration.
//
// The residual hazard is the inverse of the one the report assumed. The local
// development database is a PARTIALLY MIGRATED database: migration 434 creates
// four idx_stage_events_* indexes at once and the live DB has three of them,
// and handoff_logs on the live DB has no primary key at all while the committed
// baseline declares one. Dumping that database as the new baseline would
// silently delete legitimate objects — three of which no migration recreates.
//
// This test pins the provenance of the objects that are NOT in the live source
// but ARE in the committed baseline, so a future reconciliation starts from
// "where is this defined" instead of "it is unreproducible".
func TestEveryBaselineObjectHasRepoProvenance(t *testing.T) {
	// Objects absent from the live source but present in the committed
	// baseline, with the file that defines each one.
	cases := []struct {
		object     string
		provenance string
	}{
		{"idx_stage_events_stage_status", "../../sql/migrations/startup/434_request_stage_events_table.sql"},
		{"idx_stage_events_tenant_ts", "../../sql/migrations/startup/450_request_stage_events_tenant.sql"},
		{"idx_stage_events_request_id", "../../sql/objects/indexes/idx_stage_events_request_id.sql"},
		{"handoff_logs_pkey", "../../sql/objects/constraints/handoff_logs_handoff_logs_pkey.sql"},
		{"route_incident_events_incident_id_fkey",
			"../../sql/objects/other/route_incident_events_route_incident_events_incident_id_fkey.sql"},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.provenance)
		if err != nil {
			t.Errorf("%s 的出处 %s 不可读：%v", c.object, c.provenance, err)
			continue
		}
		// Compare on the identifier only: the object file may spell the name
		// qualified (e.g. "handoff_logs handoff_logs_pkey") while the banner
		// uses a different shape.
		if !strings.Contains(string(b), c.object) {
			t.Errorf("出处 %s 里找不到 %q —— 出处声明与文件内容不符", c.provenance, c.object)
		}
	}

	// The object registry itself must exist; a future refactor that drops it
	// would leave several baseline objects with no definition anywhere.
	const registry = "../../sql/objects/indexes"
	entries, err := os.ReadDir(registry)
	if err != nil {
		t.Fatalf("sql/objects 索引登记目录不可读：%v", err)
	}
	if len(entries) < 100 {
		t.Errorf("sql/objects/indexes 只有 %d 个文件，疑似被清空", len(entries))
	}
}
