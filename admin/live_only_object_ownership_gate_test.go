//go:build !integration

package admin

// live_only_object_ownership_gate_test.go — 2026-10-05（审计 §9.207 / 决策 D31-a）。
//
// # 这道门为什么长这样
//
// §9.202 报了「5 个函数在本库存在、全仓 `.sql` 与生产 `.go` 都搜不到」，
// 并据此推断「全新安装有表但没 trigger ⇒ `updated_at` 停止维护，
// **而没有任何门会报**」——建议补进受追踪迁移。
//
// **实测下来那个推断有两处是错的。** 逐个查证（§9.207.2）：
//
//	conversation_history            表不在链内、trigger 不在链内、Go 零引用
//	llm_hourly_stats                表在链内（666/667）、trigger 不在链内、Go 零引用
//	memora_session_summaries        **另一个服务（memora）的表**，本仓不负责
//	memora_session_summaries_orphan 同上
//	ensure_handoff_logs_partitions  **刻意排除**（见 baseline_ensure_functions_contract_test.go:88）
//
// ⇒ 结论不是「5 个缺口」，而是「**0 个缺口**」：它们要么是别的服务的对象，
// 要么是链内已有另一种解法（667/668），要么两半都不在（一致缺席）。
//
// # 所以门不在「函数在不在」，在「**本仓有没有人用**」
//
// 缺 trigger 之所以有后果，前提是**有人依赖那个 trigger 的行为**。
// 而这 4 张表在全部**非测试** Go 代码里零引用 ⇒ trigger 缺不缺，**没有可观测后果**。
//
// ⇒ 这道门钉住的是那个**前提**：「这 4 张表本仓没有非测试调用方」。
// 哪天有人加了调用方，门变红，并在失败信息里指出**必须同时**补 trigger ——
// 因为到那时「缺 trigger」才第一次成为真缺口。
//
// 这比「把 5 个函数抄进迁移链」正确得多：后者会把**死代码**引进全新安装，
// 而死代码不会被任何门抓到，只会让下一个读 baseline 的人多一份困惑。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liveOnlyTables 是 §9.207 查证结论里的 4 张「表不在本仓可复现链内」的表。
// 与 §9.202 的名单一一对应，便于复核时对照。
var liveOnlyTables = []string{
	"conversation_history",
	"llm_hourly_stats",
	"memora_session_summaries",
	"memora_session_summaries_orphan",
}

// TestLiveOnlyTablesHaveNoProductionGoCaller is the tripwire described above.
//
// ⚠ 判据本身必须**指名**它量的是什么：只查 `.go`、排除 `_test.go` 与 vendor。
// 若将来判定口径变了（例如开始用 SQL 直写、或经变量拼表名），
// 这道门会**恒绿** —— 那比红更坏。所以口径写在失败信息里。
func TestLiveOnlyTablesHaveNoProductionGoCaller(t *testing.T) {
	root := repoRootFromCaller(t)

	for _, table := range liveOnlyTables {
		var hits []string
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "vendor" || info.Name() == ".git" ||
					info.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			// 纯字面量匹配：这道门量的是「有没有人提到这个名字」，
			// 不是「有没有人真的查了它」。字面量是**上界**，
			// 所以「零命中」是可靠的否定，而「有命中」需要人工看上下文。
			b, rerr2 := os.ReadFile(path)
			if rerr2 != nil {
				return nil
			}
			for i, line := range strings.Split(string(b), "\n") {
				// ⚠ 必须先剥掉「迁移文件名」再判。
				// 第一版没剥，于是 667/668 的**文件名**被判成表引用：
				// `//go:embed embeddata/startup/667_llm_hourly_stats_timestamp_fix.sql`
				// 与 StartupFiles 里的 "667_llm_hourly_stats_timestamp_fix.sql"
				// 共 6 处命中，全是**清单条目**而不是访问。
				// 那是「五点同步」对迁移链的正常引用，与 trigger 缺不缺无关。
				// 判据不做这个区分，就只能靠人工逐条记例外，
				// 而靠人记的例外一定会烂。
				stripped := stripMigrationFilename(line, table)
				if strings.Contains(stripped, table) {
					hits = append(hits, fmt.Sprintf("%s:%d", rel, i+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", table, err)
		}
		if len(hits) == 0 {
			continue
		}
		t.Errorf("表 `%s` 现在有 %d 个**非测试** .go 文件提到它，而它的 trigger "+
			"`%s` **不在可复现迁移链内** ⇒ 全新安装上该 trigger 不存在。\n"+
			"  命中：%s\n"+
			"  处置：要么这个引用不产生依赖（纯常量/注释）⇒ 写进本门并注明理由；\n"+
			"        要么它真的依赖该 trigger ⇒ **补进受追踪迁移**（与 828 同款五点同步），\n"+
			"        否则「缺 trigger」此刻才第一次成为真缺口。\n"+
			"  ⚠ 本门只匹配 `.go` 且排除 `_test.go`/vendor：若该表改为经 SQL 直写或变量拼名，\n"+
			"     本门会恒绿 —— 那时请扩展口径，别让「门还绿着」被读成「没问题」。",
			table, len(hits), tableTriggerName(table), strings.Join(hits, ", "))
	}
}

// stripMigrationFilename removes occurrences of `table` that are part of a
// migration **filename** (i.e. followed only by name characters and `.sql`),
// so that listing a migration in go:embed / StartupFiles is not mistaken for
// the code accessing the table that migration happens to mention.
//
// Deliberately narrow: it strips `<table>[A-Za-z0-9_]*\.sql` and nothing else.
// A bare occurrence — `SELECT … FROM conversation_history`, a struct field, a
// string key — is left intact, which is exactly what should be reported.
//
// ⚠⚠ 第一版把这个函数**写反了**：处理非文件名出现时它用 "\x00" 就地替换，
// 于是下一次 Index 找不到该名字、函数返回**一个不再含该名字**的字符串。
// 结果：它抹掉的是**全部**出现，不止文件名那一种 ⇒ 门对真实 SQL 访问完全失明，
// 而仍然报 PASS。
// **变异测试当场抓住**（加一条 `SELECT … FROM conversation_history` 门仍绿）。
// ⇒ 这是本次任务里又一次「恒真的门」：不是读错对象，是**判据把自己的信号删了**。
func stripMigrationFilename(line, table string) string {
	var b strings.Builder
	rest := line
	for {
		i := strings.Index(rest, table)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		after := rest[i+len(table):]
		j := 0
		for j < len(after) {
			c := after[j]
			if c != '_' && !(c >= 'a' && c <= 'z') &&
				!(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				break
			}
			j++
		}
		if strings.HasPrefix(after[j:], ".sql") {
			// A filename token: drop the whole thing (name chars + ".sql").
			rest = after[j+len(".sql"):]
			continue
		}
		// A bare occurrence: keep it, so the caller still sees it.
		b.WriteString(table)
		rest = after
	}
}

func tableTriggerName(table string) string {
	switch table {
	case "conversation_history":
		return "trigger_conversation_updated_at / update_conversation_updated_at"
	case "llm_hourly_stats":
		return "trg_llm_hourly_stats_normalize_hour / llm_hourly_stats_normalize_hour_trigger"
	case "memora_session_summaries":
		return "trigger_memora_session_summaries_updated_at"
	case "memora_session_summaries_orphan":
		return "trigger_session_summaries_updated_at"
	}
	return "(unknown)"
}

// TestGitRepoIsReachable is a guard on this file's own premise: the walk above
// depends on the repository root being right. If repoRootFromCaller ever starts
// resolving somewhere empty, every table trivially reports "no caller" and the
// gate goes **恒绿** — the worst possible failure for a gate whose only job is
// to stay honest about a negative.
func TestGitRepoIsReachable(t *testing.T) {
	root := repoRootFromCaller(t)
	if root == "" {
		t.Fatal("repoRootFromCaller returned an empty path — the walk-based gate " +
			"would report every table as 'no caller' and go permanently green")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s does not look like the repo root (no go.mod): %v", root, err)
	}
	// Sanity: the walk must actually be able to see Go source here. Count one
	// file that definitely exists, so a Walk that silently matches nothing is
	// caught here rather than being read as "no callers anywhere".
	out, err := exec.Command("sh", "-c",
		"find . -name '*.go' -not -path './vendor/*' -not -name '*_test.go' | head -5").
		Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		t.Fatalf("premise check: expected to find non-test .go files under %s "+
			"(err=%v, out=%q). A walk that matches nothing makes this gate 恒绿 — "+
			"it would report 'no caller' for everything, including tables that "+
			"have callers.", root, err, strings.TrimSpace(string(out)))
	}
}
