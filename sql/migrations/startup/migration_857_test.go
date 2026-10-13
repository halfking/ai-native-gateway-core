package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 857 的判据刻意做成**跨文件**的：这份函数体有五份活副本，
// 少改任何一份，它都会在某个时刻把新体覆盖回旧体（§10.109 的教训）。
//
// 只测「857 迁移文件里有闸门」是不够的——那样把另外四份改回去照样全绿。

const staleGateMarker = "pg_stat_all_tables t WHERE t.relid = c.oid"
const staleGateThreshold = "interval '6 hours'"

// liveCopies 是必须与 857 迁移保持一致的函数体副本。
// db/db.go 每次启动都会 CREATE OR REPLACE，是最容易被忘掉、后果最直接的一份。
var liveCopies = []string{
	"sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql",
	"db/db.go",
	"sql/schema/01-schema.sql",
	"deploy/sql/schemas/baseline/01-schema.sql",
	"installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatalf("读不到 %s: %v", rel, err)
	}
	return string(b)
}

// Test857ColumnarStalenessGate_ExistsInMigration：迁移本身必须真的带门。
func Test857ColumnarStalenessGate_ExistsInMigration(t *testing.T) {
	up := repoFile(t, "sql/migrations/startup/857_analyze_columnar_staleness_gate.sql")
	for _, m := range []string{staleGateMarker, staleGateThreshold} {
		if !strings.Contains(up, m) {
			t.Fatalf("857 迁移缺 %q —— 陈旧度闸门没写进去", m)
		}
	}
	// 闸门必须挂在**列存分支**里，而不是别处：靠 relam<>heap 这一行做锚。
	colBranch := "AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')"
	ci := strings.Index(up, colBranch)
	gi := strings.Index(up, staleGateMarker)
	if ci < 0 || gi < 0 || gi < ci {
		t.Fatalf("闸门必须出现在列存分支（relam<>heap）之后：colBranch@%d gate@%d", ci, gi)
	}
}

// Test857DownRemovesTheGate：down 必须真的把门拆掉（假回滚比不回滚更坏）。
func Test857DownRemovesTheGate(t *testing.T) {
	down := repoFile(t, "sql/migrations/startup/857_analyze_columnar_staleness_gate.down.sql")
	if strings.Contains(down, staleGateMarker) || strings.Contains(down, staleGateThreshold) {
		t.Fatalf("down 迁移里还留着陈旧度闸门 —— 回滚等于没回滚")
	}
	// 但 down 仍必须保留「从未分析过」那条判据，否则回滚会漏掉 838 的兜底。
	if !strings.Contains(down, "NOT EXISTS (SELECT 1 FROM pg_statistic") {
		t.Fatalf("down 迁移丢了「从未分析过」判据（838 的兜底），回滚会造成新分区永不被 analyze")
	}
}

// Test857GatePresentInEveryLiveCopy：五份副本一份都不能漏。
func Test857GatePresentInEveryLiveCopy(t *testing.T) {
	for _, rel := range liveCopies {
		src := repoFile(t, rel)
		if !strings.Contains(src, staleGateMarker) {
			t.Errorf("%s 没有陈旧度闸门 —— 它会在启动/安装时把 857 的新体覆盖回旧体", rel)
		}
		if !strings.Contains(src, staleGateThreshold) {
			t.Errorf("%s 有闸门但没有 6 小时阈值 —— 判据不完整", rel)
		}
	}
}

// Test857EmbeddataCopyIsByteIdenticalToCanonical：embeddata 副本必须与迁移逐字一致。
// embeddata 是 installer 从磁盘读的那一份，漂了就等于装了旧体。
func Test857EmbeddataCopyIsByteIdenticalToCanonical(t *testing.T) {
	up := repoFile(t, "sql/migrations/startup/857_analyze_columnar_staleness_gate.sql")
	emb := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/857_analyze_columnar_staleness_gate.sql")
	if up != emb {
		t.Fatalf("embeddata 副本与 canonical 迁移不逐字一致 —— installer 会装上另一个版本")
	}
}
