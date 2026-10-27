package ursm_test

// PG 慢 SQL 观测设施门（r1041 轮，2026-10-28）
//
// # 这道门补的是「观测设施本身缺失」这一族
//
// 仓内已有 ≥19 个门以 `pg_stat_statements` 为事实源（§143.77 / §143.78 /
// §143.79 / §143.80 / §143.81 / §143.95 / §143.97 / §143.98 / §143.99 …
// 以及 scripts/ursmcheck 下的 §143.19 / §143.21 / §143.22 / §143.29 /
// §143.30 / §143.32 / index_reader_census / unused_index_report 等）。
//
// ★ 但 `pg_stat_statements` **必须由 postmaster preload**（`shared_preload_libraries`）
// 才可用 —— 只 `CREATE EXTENSION` 的话，每次查询都报：
//
//	ERROR: pg_stat_statements must be loaded via "shared_preload_libraries"
//
// ⇒ ⇒ 那些门**永远拿不到数据** ⇒ 它们在本机上是**恒 SKIP 或恒绿** = 假绿。
//
// # 本轮实测到的实况（2026-10-28）
//
//   扩展：已安装（`pg_extension` 里有 `pg_stat_statements`）
//   preload：`shared_preload_libraries = 'citus'` ⇒ **缺 pg_stat_statements**
//   慢查询日志：`log_min_duration_statement = -1`（关）
//   日志落盘：`logging_collector = off`（⇒ 即使开了阈值也只在 stderr，容器日志里找不到）
//
// ⇒ ⇒ 三重缺口 ⇒ 「慢 SQL」这个**整个类别**在本机不可观测。
// ⇒ 而仓内文档（docs/06-deployment/02-database/local-pg-sync-from-252.md）
//   写的期望值本来就是 `shared_preload_libraries='citus,citus_columnar,pg_stat_statements'`
//   ⇒ ★ **文档与实况漂移**，且没有任何东西会报警。
//
// # 这道门为什么能挡住它
//
// 两层：
//
//	① **静态层**：仓内生产口径 compose（installer/templates + embeddata 副本）
//	   必须显式带上 `shared_preload_libraries=…,pg_stat_statements`
//	   与 `log_min_duration_statement` ⇒ 新建实例不再退化。
//	② **运行时层**（有 DSN 时）：真库上 `shared_preload_libraries` 必须含
//	   `pg_stat_statements`、`log_min_duration_statement` 必须 > -1、
//	   且 `pg_stat_statements` **必须可查**（这是最终判据 ——
//	   「装了扩展」不等于「能用」）。
//
// ★ 运行时层没 DSN 时 **SKIP 而非 PASS**：仓内既有惯例（§143.97 等）即如此；
//   ★ 但 ★ **静态层永远有效** ⇒ ⇒ 至少有一层在本机是跑过的。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// ① 静态层：仓内生产口径 compose 必须带慢查询观测设施
// ---------------------------------------------------------------------------

// productionComposeFiles = 仓内「会被装到客户实例上」的 compose 模板。
// ★ 只收生产口径的两份；测试用 compose（docker-compose.deploy-test.yml 等）
//   刻意排除 ⇒ 它们是 CI 一次性环境，不承担观测职责。
var productionComposeFiles = []string{
	"../../installer/templates/compose.yml",
	"../../installer/cmd/llm-gw-installer/embeddata/compose.yml",
}

func TestProductionComposeEnablesPgSlowQueryObservability(t *testing.T) {
	for _, rel := range productionComposeFiles {
		path, err := filepath.Abs(rel)
		if err != nil {
			t.Fatalf("%s：取绝对路径失败：%v", rel, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s：读取失败：%v", rel, err)
		}
		s := string(raw)

		// ★ 判据一：`shared_preload_libraries` 必须显式出现，且其**参数值**里
		//   必须含 `pg_stat_statements` ⇒ 否则 §143.77~§143.99 那一族门全是假绿。
		//
		// ★★ 这里**必须解析参数值**，不能只判「字符串出现过」——
		//   ★ 首版就是这么写的（`strings.Contains(s, "pg_stat_statements")`）
		//   ★★ 变异臂 A 实测：把参数值里的 `pg_stat_statements` 删掉后，
		//   ★★ 门**仍然 PASS** ⇒ 因为那个字符串还出现在**本文件的注释里**
		//   ⇒ ⇒ 「出现过」这种判据 = 恒真 = 门没牙。
		//   ⇒ 现在改成：抽出 `shared_preload_libraries=<value>` 的**值**，再按
		//   **逗号分段**逐段比对 ⇒ 注释里的出现不再能冒充。
		preloadVal, ok := composeParamValue(s, "shared_preload_libraries")
		if !ok {
			t.Errorf("%s：没有配置 shared_preload_libraries ⇒ "+
				"pg_stat_statements 不可用 ⇒ 仓内 ≥19 个以它为事实源的门恒假绿", rel)
			continue
		}
		if !commaListContains(preloadVal, "pg_stat_statements") {
			t.Errorf("%s：shared_preload_libraries 的值 = %q ⇒ 不含 pg_stat_statements\n"+
				"  ⇒ 「装了扩展」不等于「能用」⇒ 慢 SQL 观测设施缺失\n"+
				"  ⇒ ★ 判据按逗号分段逐段比，不按「字符串出现过」比",
				rel, preloadVal)
		}

		// ★ 判据二：慢查询阈值必须显式开启（默认 -1 = 关）。
		minDur, ok := composeParamValue(s, "log_min_duration_statement")
		if !ok {
			t.Errorf("%s：没有配置 log_min_duration_statement ⇒ "+
				"PG 默认 -1（关闭）⇒ 慢查询日志从不开", rel)
		} else if strings.TrimSpace(minDur) == "-1" {
			t.Errorf("%s：log_min_duration_statement = -1 ⇒ 慢查询日志关闭 ⇒ "+
				"「慢 SQL」这个类别完全不可观测", rel)
		}

		// ★ 判据三：日志必须落盘（logging_collector 默认 off ⇒ 只进 stderr）。
		collector, ok := composeParamValue(s, "logging_collector")
		if !ok {
			t.Errorf("%s：没有配置 logging_collector ⇒ "+
				"PG 默认 off ⇒ 慢查询日志只写 stderr，容器日志里查不到", rel)
		} else if !strings.EqualFold(strings.TrimSpace(collector), "on") {
			t.Errorf("%s：logging_collector = %q ⇒ 不是 on ⇒ "+
				"慢查询日志只写 stderr，容器日志里查不到", rel, collector)
		}
	}
}

// composeParamValue 从 compose 文本里抽出 `key=value` 形态的**参数值**。
//
// ★ 只认 compose `command:` 列表里的 `- key=value` 形态
//   （★ 这是本仓生产 compose 的实际写法）⇒ ⇒ 注释里的同名文本不会被抽到。
//
// ★★ 关键：**返回值必须是「值」而不是「出现过的位置」** ——
//   首版判据写成 `strings.Contains(s, "pg_stat_statements")` ⇒
//   变异臂 A（删掉参数值里的那个库）实测仍 PASS ⇒ 门没牙
//   ⇒ ★ 教训：★ 判据必须指名道姓到【值】，不能停在【出现过】。
func composeParamValue(s, key string) (string, bool) {
	prefix := key + "="
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		// 去掉行首的 "- "（compose command 列表形态）
		trimmed = strings.TrimPrefix(trimmed, "- ")
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		// 去掉可能的行尾注释（但不要动引号内的内容）
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, "\"'")
		return val, true
	}
	return "", false
}

// commaListContains 判断逗号分隔列表里是否**逐段**等于 target。
func commaListContains(list, target string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == target {
			return true
		}
	}
	return false
}

// TestProductionComposeCopiesStayIdentical：两份 compose 必须逐字一致
// （embeddata 那份是打进安装器里的副本 ⇒ 漂移 = 模板改了但安装器没跟上）。
func TestProductionComposeCopiesStayIdentical(t *testing.T) {
	a, err := os.ReadFile("../../installer/templates/compose.yml")
	if err != nil {
		t.Fatalf("读 templates/compose.yml 失败：%v", err)
	}
	b, err := os.ReadFile("../../installer/cmd/llm-gw-installer/embeddata/compose.yml")
	if err != nil {
		t.Fatalf("读 embeddata/compose.yml 失败：%v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("两份生产 compose 不一致 ⇒ 模板与安装器内嵌副本已漂移\n"+
			"  templates/compose.yml          = %d bytes\n"+
			"  embeddata/compose.yml          = %d bytes",
			len(a), len(b))
	}
}

// ---------------------------------------------------------------------------
// ② 运行时层：真库上的观测设施必须真的可用
// ---------------------------------------------------------------------------

func TestLivePgSlowQueryObservabilityIsActuallyUsable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证观测设施" +
			"（★ 这道腿在 CI 里恒 SKIP ⇒ 静态层必须独立有效）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连真库失败：%v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// ★ 判据一：preload 里必须有 pg_stat_statements。
	var preload string
	if err := conn.QueryRow(ctx, "SHOW shared_preload_libraries").Scan(&preload); err != nil {
		t.Fatalf("读 shared_preload_libraries 失败：%v", err)
	}
	if !strings.Contains(preload, "pg_stat_statements") {
		t.Errorf("真库 shared_preload_libraries = %q ⇒ 不含 pg_stat_statements\n"+
			"  ⇒ 仓内 ≥19 个以 pg_stat_statements 为事实源的门在本机恒假绿\n"+
			"  ⇒ 修法：postgresql.auto.conf 写 shared_preload_libraries 后 restart\n"+
			"  ⇒  ★ 逗号列表的引号在 ALTER SYSTEM 下是已知脚枪 ⇒ 直接写 auto.conf",
			preload)
	}

	// ★ 判据二：慢查询阈值必须 > -1（-1 = 关）。
	var minDur string
	if err := conn.QueryRow(ctx, "SHOW log_min_duration_statement").Scan(&minDur); err != nil {
		t.Fatalf("读 log_min_duration_statement 失败：%v", err)
	}
	if strings.TrimSpace(minDur) == "-1" {
		t.Errorf("真库 log_min_duration_statement = -1 ⇒ 慢查询日志关闭 ⇒ "+
			"「慢 SQL」这个类别完全不可观测")
	}

	// ★★ 判据三（最终判据）：★ **能查到** ⇒ 而不只是「装了扩展」。
	//   这一条最重要：扩展在 pg_extension 里 ≠ 能用（preload 缺失时
	//   每条查询都报错）⇒ ⇒ 必须真的执行一次查询。
	var n int64
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_stat_statements").Scan(&n); err != nil {
		t.Errorf("★ 查 pg_stat_statements 失败（%v）\n"+
			"  ⇒ 扩展虽然已安装，但**没被 preload** ⇒ 所有以它为事实源的门都是假绿",
			err)
	}
}
