package dbinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const r1013IPRuleMigration = "851_disable_overbroad_pii_ip_address_rule.sql"

// TestStartupFilesRegisterIPRuleDisable 是「文件存在 ≠ 迁移会运行」的回归门。
//
// 2026-10-10：851 停用 pii_patterns.ip_address 的迁移躺在
// sql/migrations/startup/ 里，但从未登记进 StartupFiles——安装器只跑这张表，
// 于是每台全新安装的机器上那条把每个 IPv4 压成 `*********` 的规则都还在跑。
//
// 既有测试（TestStartupManifestMatchesStartupFiles / TestStartupFilesExistInEmbeddata）
// 只保证「表与文件互相一致」，删掉 851 两边一起消失时它们仍然全绿。这里钉的是
// **851 必须在这张表里**这一条具体事实。
func TestStartupFilesRegisterIPRuleDisable(t *testing.T) {
	registered := false
	for _, name := range runnerStartupFiles() {
		if name == r1013IPRuleMigration {
			registered = true
			break
		}
	}
	if !registered {
		t.Fatalf("%s 未登记进 StartupFiles：每台全新安装的机器都会继续把每个 IPv4 "+
			"单向遮成 ********* 且不可还原", r1013IPRuleMigration)
	}
	// 播种它的 316 不在这张表里，这一点是刻意的、也是本测试要记的事实：
	// 全新安装只建空的 pii_patterns 表（embeddata/01-schema.sql），不会种下
	// ip_address 行；316 是历史部署期由扫描器/手工通道落的。因此「851 排在
	// 316 之后」在安装器通道里没有对应物，真正兜底的是 db.go 的
	// ensurePIIIPAddressRuleDisabled（存量库重启即收敛）。
	// 这里只钉「851 必须在表里」+「内嵌副本与 SSOT 逐字一致」两件事。
	for _, name := range runnerStartupFiles() {
		if name == "316_output_compliance_monitoring.sql" {
			t.Fatalf("316 出现在 StartupFiles 里：若它被登记，播种与停用必须同表登记，"+
				"请重新核对本测试的顺序断言是否还成立（当前: %s)", name)
		}
	}

	// 内嵌副本必须与 SSOT 逐字一致：安装器读的是 embeddata，不是 sql/。
	ssot, err := os.ReadFile(filepath.Join("..", "..", "..", "sql", "migrations", "startup", r1013IPRuleMigration))
	if err != nil {
		t.Fatalf("read SSOT: %v", err)
	}
	embedded, err := os.ReadFile(filepath.Join("..", "..", "cmd", "llm-gw-installer", "embeddata", "startup", r1013IPRuleMigration))
	if err != nil {
		t.Fatalf("read embeddata copy: %v", err)
	}
	if string(ssot) != string(embedded) {
		t.Fatal("embeddata 副本与 sql/migrations/startup 的 SSOT 不一致")
	}
	if !strings.Contains(string(ssot), "WHERE pattern_name = 'ip_address'") {
		t.Fatal("851 内容被改写：必须按行名停用 ip_address 规则")
	}
}
