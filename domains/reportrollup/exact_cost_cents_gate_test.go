package reportrollup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exact_cost_cents_gate_test.go —— 993（F4 落实）的口径门。
//
// 背景（docs/audit/2026-11-20-看板对账口径对齐-主档.md §五 L-cost）：
// estimated_cost_cents 曾是 bigint + 写面**逐分组** ROUND ⇒
// Σ round(分组) ≠ round(Σ 分组)，对账页总费用与看板（usage_facts 一次
// 求和）每天差 ≤3¢。993 起：列 numeric(24,6)、写面存精确分值、读面展示
// 一次取整 ⇒ 两面总费用逐字相等。
//
// 本门钉四件事，任何一件被「顺手改回去」都会红：
//
//	① reportrollup 全部聚合 SQL 不再对 cost_amount 逐分组取整；
//	② 六段聚合都产出 numeric(24,6) 的精确分值；
//	③ Go 类型链（Bucket/Totals/grainScanRow）是 float64——int64 会把
//	  numeric 扫描炸成 42883（numeric→int64 无隐式转换）；
//	④ 迁移 993 的五处登记齐全（canonical/embeddata/installer-migrations/
//	  dbinit runner/apply-db 序列——少一处就是某个通道不投递，211 的
//	  docker 通道吃过这个亏：AGENTS ★② 的六处登记纪律）。
func TestCostCentsAreExactNotPerGroupRounded(t *testing.T) {
	for _, file := range []string{"rollup.go", "grain.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(raw)
		if strings.Contains(src, "ROUND(SUM(f.cost_amount)") ||
			strings.Contains(src, "ROUND(SUM(cost_amount)") {
			t.Errorf("%s 仍有对 cost_amount 的逐分组 ROUND——Σ round(分组) ≠ round(Σ)，"+
				"对账总计会重新和看板差出几分钱（993/F4 修的就是这个）。", file)
		}
		// 六段聚合（providerModel/total/internalTenant/internalPerson/internalModel/grain）
		// 的 cost_cents 投影必须都落在 numeric(24,6)。
		if got := strings.Count(src, "::numeric(24,6) AS cost_cents") + strings.Count(src, "::numeric(24,6),"); got < 1 {
			t.Errorf("%s 里找不到 numeric(24,6) 的 cost_cents 投影——bigint 投影会把"+
				"写面重新变回取整语义。", file)
		}
	}

	// ③ 类型链：int64 字段名换回 int64 会先在编译期炸（scan 目标类型不符），
	// 这里钉字段类型本身，防止有人把 SQL 端改回 ::bigint 来「修」编译错。
	typeJSON := map[string]string{
		"rollup.go":      "CostCents          float64",
		"report.go":      "EstimatedCostCents float64 `json:\"estimated_cost_cents\"`",
		"grainreport.go": "costCents                  float64",
	}
	for file, want := range typeJSON {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s 缺 %q —— cost 链路必须全程 float64（numeric 列），"+
				"int64 会让 pgx 扫描 numeric 直接报错。", file, want)
		}
	}
}

func TestMigration993RegisteredEverywhere(t *testing.T) {
	const name = "993_report_snapshots_exact_cost_cents"
	// 从包目录上到仓根。
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	points := map[string]string{
		"canonical sql":   filepath.Join(root, "sql", "migrations", "startup", name+".sql"),
		"canonical down":  filepath.Join(root, "sql", "migrations", "startup", name+".down.sql"),
		"embeddata sql":   filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name+".sql"),
		"embeddata down":  filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name+".down.sql"),
		"installer up":    filepath.Join(root, "installer", "migrations", "993_report_snapshots_exact_cost_cents", "up.sql"),
		"installer down":  filepath.Join(root, "installer", "migrations", "993_report_snapshots_exact_cost_cents", "down.sql"),
		"dbinit runner":   filepath.Join(root, "installer", "internal", "dbinit", "runner.go"),
		"deploy sequence": filepath.Join(root, "scripts", "apply-db-revision-sequence.sh"),
	}
	for label, path := range points {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("迁移 993 的登记点缺失：%s（%s）: %v", label, path, err)
			continue
		}
		if label == "dbinit runner" || label == "deploy sequence" {
			if !strings.Contains(string(raw), name+".sql") {
				t.Errorf("%s 里没有登记 %s.sql —— 该通道不会投递本迁移"+
					"（211 docker 通道的 979/981 都吃过这个亏）。", label, name)
			}
		}
	}
	// canonical 与 embeddata 必须逐字一致（漂移 = 两条通道跑出不同 schema）。
	canon, _ := os.ReadFile(points["canonical sql"])
	embed, _ := os.ReadFile(points["embeddata sql"])
	if string(canon) != string(embed) {
		t.Error("canonical 与 installer embeddata 的 993 SQL 不一致——" +
			"两条通道会投递出不同的 schema。")
	}
}
