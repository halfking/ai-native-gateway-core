package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration851EnsureWiredAndMirrorsSQL 钉住「文件存在 ≠ 迁移会运行」。
//
// 2026-10-10 用户报告 IP 被压成 `*********` 且换不回来。三条遮蔽链路修了两条
// （security/sanitize 的 DetectionScope、输出合规的 ActionOnInternalIP 默认），
// 第三条是 316 播种的 pii_patterns.ip_address 规则——它由迁移 851 停用，而
// **851 从未被登记进安装器 StartupFiles**，于是每台全新安装的机器上这条规则
// 都还在跑。这条门钉的是它的两端：
//
//   - db.go 的启动自愈（覆盖存量部署：重启即收敛）
//   - sql/migrations/startup/851 本体仍然写着那条停用语句（改了文件不改自愈，
//     或反之，两边漂移时这里变红）
//
// 只测「db.go 里有这个函数名」是不够的：它可以在文件里却没被 applyMigrationsOnce
// 调用。因此这里断言的是**调用点存在**，且与 847 相邻（同属启动自愈链尾）。
func TestMigration851EnsureWiredAndMirrorsSQL(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	callIdx := strings.Index(text, "ensurePIIIPAddressRuleDisabled(migCtx)")
	if callIdx < 0 {
		t.Fatal("db.go must call ensurePIIIPAddressRuleDisabled in applyMigrationsOnce")
	}
	anchorIdx := strings.Index(text, "ensureBillingUnitAndAudioSeconds(migCtx)")
	if anchorIdx < 0 || anchorIdx > callIdx {
		t.Fatal("ensurePIIIPAddressRuleDisabled must be wired after ensureBillingUnitAndAudioSeconds")
	}

	const fnConst = "func (d *DB) ensurePIIIPAddressRuleDisabled("
	fnStart := strings.Index(text, fnConst)
	if fnStart < 0 {
		t.Fatal("db.go missing ensurePIIIPAddressRuleDisabled wrapper")
	}
	nextFn := strings.Index(text[fnStart+1:], "func (d *DB) ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensurePIIIPAddressRuleDisabled")
	}
	body := text[fnStart : fnStart+1+nextFn]

	// 幂等 + 按行判据：与 851 本体逐字对齐。
	if !strings.Contains(body, "WHERE pattern_name = 'ip_address'") {
		t.Fatal("ensure SQL must target the ip_address row by name, not by regex shape")
	}
	if !strings.Contains(body, "enabled IS DISTINCT FROM false") {
		t.Fatal("ensure SQL must stay idempotent (no-op when already disabled)")
	}
	// 表可能不存在（未装 316）：必须探测而不是把启动打成失败。
	if !strings.Contains(body, "to_regclass('public.pii_patterns')") {
		t.Fatal("ensure must probe pii_patterns existence before writing")
	}
	if !strings.Contains(body, "VALUES ('851',") {
		t.Fatal("ensure SQL must stamp schema_migrations version 851")
	}

	migBytes, err := os.ReadFile("../sql/migrations/startup/851_disable_overbroad_pii_ip_address_rule.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	for _, want := range []string{
		"UPDATE public.pii_patterns",
		"SET enabled = false",
		"WHERE pattern_name = 'ip_address'",
	} {
		if !strings.Contains(mig, want) {
			t.Fatalf("migration 851 altered (%q missing); keep ensurePIIIPAddressRuleDisabled mirrored", want)
		}
	}
	// 选 UPDATE 而非 DELETE 是刻意的（316 的幂等重放会把删掉的行重新种回来）。
	if strings.Contains(strings.ToUpper(mig), "DELETE FROM PUBLIC.PII_PATTERNS") {
		t.Fatal("851 must not DELETE the row: migration 316 replays and would re-seed it")
	}
}
