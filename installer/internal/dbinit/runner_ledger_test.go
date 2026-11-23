package dbinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embeddataStartupDir 返回安装器实际投递的 startup 资产目录
// （★ 复用同包既有的 embeddataRoot 辅助函数 ⇒ ★ 不重复造路径口径）。
func embeddataStartupDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(embeddataRoot(t), "startup")
}

// contractPhrase 是「安装器专属资产」在文件头里的**契约声明**。
//
// ★ 这条短语是 freshInstallOnlyAssets 的**唯一**判据来源 ⇒ ★ 资产自己
// ★ 声明「不得应用到已有部署」⇒ 安装器增量路径就必须跳过它。
const contractPhrase = "must not be applied to an existing deployment"

// TestFreshInstallOnlyAssetsMatchFileContract 双向核对「安装器专属资产」名单：
//
//	正向：★ 凡 embeddata 里**自带契约声明**的 startup 资产 ⇒ 必须登记在
//	      freshInstallOnlyAssets 里（★ 否则增量路径会执行它 ⇒ 撞已下线对象）。
//	反向：★ 名单里的每个文件 ⇒ 必须真的自带那句声明（★ 防止名单变成
//	      「随手加的跳过清单」⇒ ★ 那样就变成「跳过一切」的暗门）。
//
// ★ 为什么这道门必要：★ 名单是**代码里的跳过清单**⇒ ★ 一旦与文件契约
// ★ 脱节 ⇒ ★ 要么漏跳（炸）⇒ ★ 要么乱跳（静默不投递）⇒ ★ 两个方向都要有牙。
func TestFreshInstallOnlyAssetsMatchFileContract(t *testing.T) {
	startupDir := embeddataStartupDir(t)
	entries, err := os.ReadDir(startupDir)
	if err != nil {
		t.Fatalf("读取 embeddata startup 目录失败: %v", err)
	}
	declared := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(startupDir, e.Name()))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", e.Name(), err)
		}
		if strings.Contains(normalizeSQLText(string(content)), contractPhrase) {
			declared[e.Name()] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("没有任何 startup 资产自带契约声明 ⇒ 契约短语或资产内容变了 ⇒ 请复核 freshInstallOnlyAssets 的判据")
	}
	for name := range declared {
		if _, ok := freshInstallOnlyAssets[name]; !ok {
			t.Errorf("资产 %s 自带「%s」契约声明 ⇒ 必须在 freshInstallOnlyAssets 里登记（★ 否则增量路径会执行它）", name, contractPhrase)
		}
	}
	for name := range freshInstallOnlyAssets {
		if !declared[name] {
			t.Errorf("freshInstallOnlyAssets 登记了 %s ⇒ 但它没有自带「%s」契约声明（★ 名单不得变成随手加的跳过清单）", name, contractPhrase)
		}
	}
}

// TestStartupSkipReason 三条跳过判据（★ 逐条钉住）：
//
//	① 安装器专属资产（★ 有契约声明）⇒ 跳过；
//	② 版本号已在台账 ⇒ 跳过；
//	③ 部署通道权威台账命中 ⇒ 跳过（★ 同一指纹 / ★ 遗留无指纹行）；
//	④ 部署通道指纹与当前文件不同（★ 内容被加固过）⇒ **不跳过**（★ 同部署通道口径）；
//	⑤ 无前缀且不在专属名单里（★ 361b prelude）⇒ **不跳过**。
func TestStartupSkipReason(t *testing.T) {
	runner := NewRunner("citus", "user", "db", embeddataRoot(t))

	// ① 安装器专属资产
	if reason, skip := runner.startupSkipReason("session_turns_hot_bootstrap.sql", map[int]bool{}, nil); !skip {
		t.Error("bootstrap 资产在增量路径必须跳过（★ 契约声明：不得应用到已有部署）")
	} else if reason == "" {
		t.Error("跳过必须带可读理由")
	}

	// ② 版本已在台账
	if _, skip := runner.startupSkipReason("392_candidate_failure_logs_monthly_partition.sql", map[int]bool{392: true}, nil); !skip {
		t.Error("版本已在台账的文件必须跳过")
	}

	// ③ 部署通道权威台账：同一指纹 ⇒ 跳过
	sha, err := runner.fileSHA256("startup/392_candidate_failure_logs_monthly_partition.sql")
	if err != nil {
		t.Fatalf("计算内容指纹失败: %v", err)
	}
	deploy := map[string]string{
		"392_candidate_failure_logs_monthly_partition.sql": sha,
		"990_stats_consistency_fixes.sql":                  "", // 遗留无指纹记账行
	}
	if _, skip := runner.startupSkipReason("392_candidate_failure_logs_monthly_partition.sql", map[int]bool{}, deploy); !skip {
		t.Error("部署通道已按同一内容指纹应用的文件必须跳过（★ 两条通道口径一致）")
	}
	if _, skip := runner.startupSkipReason("990_stats_consistency_fixes.sql", map[int]bool{}, deploy); !skip {
		t.Error("部署通道的遗留无指纹记账行必须按「已应用」跳过（★ 同部署通道口径）")
	}

	// ④ 指纹不同 ⇒ 不跳过（★ 内容加固 ⇒ 重放）
	deployChanged := map[string]string{
		"392_candidate_failure_logs_monthly_partition.sql": "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if _, skip := runner.startupSkipReason("392_candidate_failure_logs_monthly_partition.sql", map[int]bool{}, deployChanged); skip {
		t.Error("部署通道指纹与当前文件不同（★ 内容被加固过）⇒ 不得跳过（★ 同部署通道口径：内容加固 ⇒ 重放）")
	}

	// ⑤ 无前缀但不在专属名单里 ⇒ 不跳过（★ 361b 是 host-first prelude，★ 必须照跑）
	if _, skip := runner.startupSkipReason("361b_provider_models_source_prelude.sql", map[int]bool{}, nil); skip {
		t.Error("361b prelude 不在专属名单里 ⇒ 增量路径必须照跑（★ 它是 host-first 部署的必需列补齐）")
	}
}

// TestRecordAppliedVersionSQLIsIdempotent 钉住补记 SQL 的形状：
// ★ 只在缺行时补一行（★ WHERE NOT EXISTS）⇒ ★ 重复调用不写重行；
// ★ 且不覆盖运维手改过的 description。
func TestRecordAppliedVersionSQLIsIdempotent(t *testing.T) {
	src := readSourceFile(t, "runner.go")
	if !strings.Contains(src, "WHERE NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version") {
		t.Error("补记 SQL 必须用 WHERE NOT EXISTS 守卫（★ 幂等：★ 不写重行、★ 不覆盖手改过的 description）")
	}
	if !strings.Contains(src, "INSERT INTO public.schema_migrations (version, description)") {
		t.Error("补记 SQL 必须写 version + description 两列（★ 与本仓台账形状一致）")
	}
}

// readSourceFile 读本包同目录下的源文件（★ 供 SQL 形状类断言使用）。
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(content)
}

// normalizeSQLText 把 SQL 文本里的注释标记与换行压成空格（★ 契约声明句在
// 文件头里**跨行**⇒ ★ 字面匹配必然落空 ⇒ ★ 必须先归一化再比对）。
func normalizeSQLText(s string) string {
	s = strings.ReplaceAll(s, "--", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.Join(strings.Fields(s), " ")
}
