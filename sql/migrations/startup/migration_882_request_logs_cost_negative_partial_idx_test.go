package startup

// migration_882_request_logs_cost_negative_partial_idx_test.go —— 迁移 882 的契约门。
//
// # 这道门守的是什么
//
// 882 给 `_request_logs`（860 改名的 request_logs 分区父表）建
// `WHERE cost_usd < 0` 的部分索引，让 bg/routing_health_checks.go 的
// `recorded_cost_is_negative` 检查从「每 8-9 分钟顺序扫 43 万行、24h 实测
// 158 次慢查 + 34 次超时击杀」变成索引探针。索引类迁移最容易出的错是
// **"索引与查询形态脱钩"**——查询谓词改了、索引没跟上，计划器静默退回
// 顺序扫描，浪费照旧且没人知道。所以本门钉四件事：
//
//  ① 迁移建的确实是**那条**索引：谓词 `cost_usd < 0`、键序 ts 在首位
//     （服务 30 天范围谓词）、INCLUDE 覆盖检查的聚合列（index-only 的前提）；
//  ② down 能删掉**同名**父表索引（父表 DROP 级联分区子索引，877 台账口径）；
//  ③ 检查侧（bg/routing_health_checks.go）的源与谓词**没有漂移**——
//     如果检查改了表或谓词，本索引的形态必须重新评估（cross-pin）；
//  ④ canonical 与 installer embeddata 两份字节一致（只改一份 = 装出来的库
//     与仓库的库分叉，881 同款门）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig882Name     = "882_request_logs_cost_negative_partial_idx.sql"
	mig882DownName = "882_request_logs_cost_negative_partial_idx.down.sql"
)

func mig882Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig882Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestMigration882_IndexShapeMatchesCheckPredicate 钉 ①：索引形态与查询对齐。
func TestMigration882_IndexShapeMatchesCheckPredicate(t *testing.T) {
	up := mig882Read(t, mig882Tree(t), filepath.Join("sql", "migrations", "startup", mig882Name))

	for _, want := range []string{
		"idx_request_logs_cost_negative",
		"ON public._request_logs",
		"(ts, credential_id, outbound_model)",
		"INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)",
		"WHERE cost_usd < 0",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("迁移文件缺少索引形态要素 %q —— 键序/谓词改动需要同步复核 bg/routing_health_checks.go 的查询形态", want)
		}
	}
	// 父表级联形态：不允许出现 532 式逐分区循环（未来的分区靠父表自动继承，
	// 若改回逐分片段，新分区会长不回索引——876 同款教训）。
	if strings.Contains(up, "pg_inherits") {
		t.Errorf("882 应建在分区父表上（自动级联+继承），不应出现逐分区 pg_inherits 循环")
	}
}

// TestMigration882_DownDropsSameIndex 钉 ②：down 删的是同名索引。
func TestMigration882_DownDropsSameIndex(t *testing.T) {
	down := mig882Read(t, mig882Tree(t), filepath.Join("sql", "migrations", "startup", mig882DownName))
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_request_logs_cost_negative") {
		t.Errorf("down 文件必须删除父表索引 idx_request_logs_cost_negative（级联分区子索引），实得: %s", down)
	}
}

// TestMigration882_CheckSidePredicatePinned 钉 ③：检查侧没有漂移。
//
// 索引只对「同源同谓词」的查询有用。若这条门红了，说明
// recorded_cost_is_negative 的表或谓词变了——按新形态重新评估 882 的
// 键序/INCLUDE/谓词，不要只改这一边的字符串让门转绿。
func TestMigration882_CheckSidePredicatePinned(t *testing.T) {
	check := mig882Read(t, mig882Tree(t), filepath.Join("bg", "routing_health_checks.go"))
	for _, want := range []string{
		"FROM public._request_logs r",
		"r.cost_usd < 0",
		"interval '30 days'",
	} {
		if !strings.Contains(check, want) {
			t.Errorf("recorded_cost_is_negative 检查缺少 %q —— 检查形态已漂移，882 索引必须重新评估", want)
		}
	}
}

// TestMigration882_EmbeddataCopyIdentical 钉 ④：两份字节一致。
func TestMigration882_EmbeddataCopyIdentical(t *testing.T) {
	root := mig882Tree(t)
	for _, name := range []string{mig882Name, mig882DownName} {
		canonical := mig882Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig882Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 的 canonical 与 installer embeddata 两份不一致 —— 只改一份会让装出来的库与仓库分叉", name)
		}
	}
}
