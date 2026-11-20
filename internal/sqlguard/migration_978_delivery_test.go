package sqlguard

// migration_978_delivery_test.go —— 迁移 978（pi_canonical_rule_set）的
// **投递六点齐备性 + 规则集保真 + 交付可达性**的门。
//
// # 背景（2026-11-17 245 部署后验收轮实测）
//
//	PI 检测的**规则层**（L1 basic + L2 advanced）的内容来自两条种子迁移：
//	**315**（20 条，含 privilege_escalation / command_injection）与
//	**364**（21 条 enhanced）。而这两件**都不在任何建库通道里**：
//
//	① **全新安装链**（installer/internal/dbinit/runner.go 的 StartupFiles
//	   ⇒ 其机器可读镜像 sql/schema/installed_startup_migrations.tsv）
//	   —— tsv 的 394 行里 prompt-injection 相关**只有 367 一条**。
//	② **存量升级通道**（deploy-lib/db-changelog.sh 的
//	   `_deploy_pending_startup_migrations`）的投递面 = 「≥ 对账地板 412 全部
//	   ＋ <412 且登记在 scripts/migration-low-version-delivery.list 的少数几条」
//	   —— 315 / 364 都 <412 且都**不在**那张白名单里。
//	③ 而且 315 **即使被投递也会炸**：它的规则 INSERT（315 第 70 行）**没有
//	   ON CONFLICT**，而它要插的两条 role_hijack_ignore_previous /
//	   role_hijack_you_are_now **正是**检测器 ensureRulesTable 预置的那两条
//	   ⇒ 唯一键冲突 ⇒ 23505 ⇒ 迁移失败。
//
//	⇒ 后果（两条**独立实测**，不是推断）：
//	  · **全新安装**：`prompt_injection_rules` 表由 01-schema 基线建出（空表），
//	    而 ensureRulesTable() **只在「表不存在」时**才触发 ⇒ 表在 ⇒ 永不触发
//	    ⇒ **规则表恒空**（mock 全新安装栈实测 count(*) = 0）。
//	  · **存量库（245 活库）**：23 条 = 364 的 21 ＋ ensureRulesTable 的 2
//	    ⇒ **缺 privilege_escalation / command_injection** ⇒ 977 的 F4 对这两条
//	    是 no-op ⇒ 「L2 规则 pattern 已订正」**只在 tool_abuse_exec 上成立**。
//
// # 本门钉住什么
//
//	① 978 的**六点位**逐点在位（canonical / embeddata 字节一致 / go:embed+var /
//	   embeddedSQLFiles map / dbinit StartupFiles / deploy 通道 files 数组 +
//	   ledger_artifact_probe / tsv / changelog）；
//	② **规则集保真**：978 种入的 25 条与它们的**来源**逐字一致
//	   （364 的 21 条**整行**照抄、2 条 basic 的 pattern 取自 315、
//	   privilege_escalation / command_injection 的 pattern 取自 977 的
//	   UPDATE 字面量）⇒ 「转录误差」与「把 977 刚修掉的假阳性又种回去」
//	   两种坏法都被挡住；
//	③ **幂等 + 非破坏**：up 件是 `ON CONFLICT (rule_name) DO NOTHING`、
//	   **不删**任何规则行；down 件**只删本件唯二引入的两条**；
//	④ **交付可达性**（本轮新增的核心不变式）：canonical 规则集必须有
//	   **一条可达的投递通道**（978 ∈ tsv ∧ StartupFiles ∧ deploy files 数组）
//	   ⇒ 防止将来有人「把 978 从安装器链里删掉」或「改回依赖 315/364」而
//	   **无人变红**（这正是本缺陷能长期存活的原因）。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	pi978UpFile   = "978_pi_canonical_rule_set.sql"
	pi978DownFile = "978_pi_canonical_rule_set.down.sql"

	pi978CanonicalDir = "../../sql/migrations/startup"
	pi978EmbedDir     = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	pi978InstallerGo  = "../../installer/cmd/llm-gw-installer/main.go"
	pi978DbinitRunner = "../../installer/internal/dbinit/runner.go"
	pi978Tsv          = "../../sql/schema/installed_startup_migrations.tsv"
	pi978Changelog    = "../../docs/db-changelog.md"
	pi978SeqScript    = "../../scripts/apply-db-revision-sequence.sh"

	// pi978Seed315 / pi978Seed364 是规则集的**来源**文件。
	//
	// ★ 364 的 21 条在 978 里是**整行照抄** ⇒ 本门比对的是**整行**，
	//   不是「比对 pattern 字段」⇒ 任何字段（category / severity / tags）
	//   的转录误差都会被抓到。
	pi978Seed315 = "../../sql/migrations/startup/315_prompt_injection_detection.sql"
	pi978Seed364 = "../../sql/migrations/startup/364_prompt_injection_enhanced.sql"
)

// pi978ExpectedTSVIndex 是 978 在 installed_startup_migrations.tsv 里的
// **应用序**下标（0 起）。
const pi978ExpectedTSVIndex = 377

// pi978CanonicalRuleNames ＝ canonical 规则集（25 条）的规则名。
//
// ★ 21 条来自 364（逐字）＋ 2 条 ensureRulesTable 的 basic ＋ 2 条 977 订正后的。
//
// ★ 2026-11-18 12h 审计轮根修（P1）：`tool_abuse_exec` 从「364 的 21 条（逐字）」
//
//	这一桶**移出**，改归「977 订正后的」那一桶。
//	起因：364 的 `tool_abuse_exec` pattern 是 **977 订正前的旧形态**
//	（`system` + 无界 `.*` ⇒ "the system will call you back" severity 10 命中）。
//	978 原先「逐字取自 364」⇒ 把 977 刚修掉的假阳性**又种了回去**；
//	而 978 的登记序在 977 **之后**、全新安装时规则表是空的（315/364 都不在
//	建库通道里 ⇒ 977 的 UPDATE 命中 0 行）⇒ **每次全新部署都会装上这条假阳性**。
//	⇒ 取源改为 977 的订正后字面量（与 privilege_escalation / command_injection 同桶）。
var pi978CanonicalRuleNames = []string{
	// 364 的 21 条（★ tool_abuse_exec 已移出 ⇒ 此处实为 20 条）
	"instruction_override_ignore",
	"instruction_override_new_rules",
	"instruction_override_system_prompt",
	"data_exfiltration_http",
	"data_exfiltration_tool",
	"resource_exhaustion_repeat",
	"resource_exhaustion_long",
	"social_engineering_urgency",
	"social_engineering_authority",
	"unicode_homoglyph",
	"unicode_rtl_override",
	"unicode_zero_width",
	"tool_abuse_import",
	"tool_abuse_file",
	"multi_turn_setup",
	"multi_turn_reference",
	"payload_smuggling_base64",
	"payload_smuggling_markdown",
	"context_manipulation_role",
	"context_manipulation_persona",
	// ensureRulesTable 预置的 2 条 basic
	"role_hijack_ignore_previous",
	"role_hijack_you_are_now",
	// 977 订正后的 3 条（★ 2026-11-18 增补 tool_abuse_exec）
	"privilege_escalation",
	"command_injection",
	"tool_abuse_exec",
}

// pi978RuleRowRe ＝「一条规则行」的可执行面形态：
//
//	('rule_name', 'rule_type', ...),
//
// ★ 规则名里**可以有数字**（`payload_smuggling_base64`）⇒ 字符类必须含 0-9。
var pi978RuleRowRe = regexp.MustCompile(`(?m)^\s*\('([a-z0-9_]+)',`)

// pi978SeedRow ＝从种子文件里取出某条规则的**整行原文**（去首尾空白）。
func pi978SeedRow(t *testing.T, path, ruleName string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读种子文件 %s：%v", path, err)
	}
	prefix := "('" + ruleName + "',"
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return trimmed
		}
	}
	t.Fatalf("种子文件 %s 里找不到规则 %q 的种子行", path, ruleName)
	return ""
}

// pi978RowOf ＝从 978 的可执行面里取出某条规则的**整行原文**。
func pi978RowOf(t *testing.T, exec, ruleName string) string {
	t.Helper()
	for _, line := range strings.Split(exec, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "('"+ruleName+"',") {
			return trimmed
		}
	}
	t.Fatalf("978 的可执行面里找不到规则 %q 的行", ruleName)
	return ""
}

// TestMigration978DeliverySixPointsAreInPlace —— ① 六点位。
func TestMigration978DeliverySixPointsAreInPlace(t *testing.T) {
	// canonical
	canonical := pi978CanonicalDir + "/" + pi978UpFile
	upBytes, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("读 canonical %s：%v", canonical, err)
	}
	// embeddata 副本（**字节一致**）
	embed := pi978EmbedDir + "/" + pi978UpFile
	embedBytes, err := os.ReadFile(embed)
	if err != nil {
		t.Fatalf("读 embeddata 副本 %s：%v", embed, err)
	}
	if string(upBytes) != string(embedBytes) {
		t.Errorf("embeddata 副本与 canonical **不是逐字节一致**\n  canonical: %s\n  embeddata: %s",
			canonical, embed)
	}
	// down 件同样要有副本
	if _, err := os.ReadFile(pi978EmbedDir + "/" + pi978DownFile); err != nil {
		t.Errorf("读 embeddata down 副本 %s：%v", pi978EmbedDir+"/"+pi978DownFile, err)
	}

	// go:embed + var
	installerGo, err := os.ReadFile(pi978InstallerGo)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978InstallerGo, err)
	}
	installerText := string(installerGo)
	for _, want := range []string{
		"//go:embed embeddata/startup/" + pi978UpFile,
		"var piCanonicalRuleSet978 []byte",
		"//go:embed embeddata/startup/" + pi978DownFile,
		"var piCanonicalRuleSet978Down []byte",
		`"startup/` + pi978UpFile + `":`,
		"piCanonicalRuleSet978,",
		`"startup/` + pi978DownFile + `":`,
		"piCanonicalRuleSet978Down,",
	} {
		if !strings.Contains(installerText, want) {
			t.Errorf("安装器 main.go 缺少登记：%q", want)
		}
	}

	// dbinit StartupFiles
	runner, err := os.ReadFile(pi978DbinitRunner)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978DbinitRunner, err)
	}
	if !strings.Contains(string(runner), `"`+pi978UpFile+`"`) {
		t.Errorf("dbinit runner.go 的 StartupFiles 里没有 %q", pi978UpFile)
	}

	// tsv（应用序镜像）
	tsv, err := os.ReadFile(pi978Tsv)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978Tsv, err)
	}
	tsvLines := []string{}
	for _, l := range strings.Split(string(tsv), "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		tsvLines = append(tsvLines, strings.TrimSpace(l))
	}
	if pi978ExpectedTSVIndex >= len(tsvLines) {
		t.Fatalf("tsv 只有 %d 条，期望 978 在下标 %d", len(tsvLines), pi978ExpectedTSVIndex)
	}
	if got := tsvLines[pi978ExpectedTSVIndex]; got != "377\t"+pi978UpFile {
		t.Errorf("tsv 下标 %d 应为 %q，实际 %q", pi978ExpectedTSVIndex, "377\t"+pi978UpFile, got)
	}

	// deploy 通道：files 数组 + ledger_artifact_probe
	seq, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	seqText := string(seq)
	if !strings.Contains(seqText, "sql/migrations/startup/"+pi978UpFile) {
		t.Errorf("deploy 通道的 files 数组里没有 %s", pi978UpFile)
	}
	if !strings.Contains(seqText, `ledger_artifact_probe "`+pi978UpFile+`"`) {
		t.Errorf("deploy 通道没有 %s 的 ledger_artifact_probe", pi978UpFile)
	}

	// changelog 台账（sha 必须与 canonical 字节一致）
	changelog, err := os.ReadFile(pi978Changelog)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978Changelog, err)
	}
	if !strings.Contains(string(changelog), "`"+pi978UpFile+"`") {
		t.Errorf("db-changelog 里没有 %s 的登记行", pi978UpFile)
	}
}

// TestMigration978RuleSetIsByteFaithfulToItsSources —— ② 规则集保真。
func TestMigration978RuleSetIsByteFaithfulToItsSources(t *testing.T) {
	raw, err := os.ReadFile(pi978CanonicalDir + "/" + pi978UpFile)
	if err != nil {
		t.Fatalf("读 978：%v", err)
	}
	exec := pi977StripComments(string(raw))

	// ① 364 的 20 条：**整行**必须逐字一致（零转录误差）。
	//    ★ 2026-11-18：条数由 21 降为 20 —— tool_abuse_exec 移到 ③ 桶（取源 977）。
	for _, rule := range pi978CanonicalRuleNames[:20] {
		seedRow := pi978SeedRow(t, pi978Seed364, rule)
		gotRow := pi978RowOf(t, exec, rule)
		if gotRow != seedRow {
			t.Errorf("规则 %q 的行与 364 种子**不逐字一致**：\n  364: %s\n  978: %s", rule, seedRow, gotRow)
		}
	}

	// ② 2 条 basic：pattern 必须与 315 一致（其余字段按 6→9 列映射）。
	for _, rule := range []string{"role_hijack_ignore_previous", "role_hijack_you_are_now"} {
		seedPattern := pi977SeedPattern(t, pi978Seed315, rule)
		gotRow := pi978RowOf(t, exec, rule)
		if !strings.Contains(gotRow, "'"+seedPattern+"'") {
			t.Errorf("规则 %q 的 pattern 与 315 种子不一致：\n  315: %s\n  978: %s", rule, seedPattern, gotRow)
		}
	}

	// ③ privilege_escalation / command_injection / tool_abuse_exec：pattern 必须与
	//    977 的 UPDATE 字面量**逐字一致**（不许把 977 刚修掉的假阳性种回去）。
	//
	//    ★ 取源是 **977 那个文件**（不是 978 自己）—— 978 里只有 INSERT，
	//      UPDATE 字面量的真身在 977 ⇒ 必须回源文件取。
	//
	//    ★ 2026-11-18 12h 审计轮（P1）：tool_abuse_exec 归入本桶。
	//      起因：978 原先把它逐字取自 364 ⇒ 把 977 刚修掉的 `system`+无界`.*`
	//      假阳性**又种了回去**；而 978 的登记序在 977 之后、且全新安装时规则表
	//      是空的（977 的 UPDATE 命中 0 行）⇒ **每次全新部署都装上这条假阳性**
	//      （存量库上 ON CONFLICT DO NOTHING 不覆盖 ⇒ 不显现 ⇒「存量不红、新装必红」）。
	m977Raw, err := os.ReadFile(pi978CanonicalDir + "/" + pi977UpFile)
	if err != nil {
		t.Fatalf("读 977：%v", err)
	}
	for _, rule := range []string{"privilege_escalation", "command_injection", "tool_abuse_exec"} {
		gotRow := pi978RowOf(t, exec, rule)
		wantPattern := ""
		for _, mm := range pi977UpdateRe.FindAllStringSubmatch(string(m977Raw), -1) {
			if mm[2] == rule {
				wantPattern = mm[1]
				break
			}
		}
		if wantPattern == "" {
			t.Fatalf("977 里找不到规则 %q 的 UPDATE 字面量", rule)
		}
		if !strings.Contains(gotRow, "'"+wantPattern+"'") {
			t.Errorf("规则 %q 的 pattern 与 977 订正后的字面量不一致 ⇒ 把假阳性又种了回去\n  978: %s", rule, gotRow)
		}
	}

	// ④ 25 条必须**全部**在场（不多不少地覆盖 canonical 名单）。
	for _, rule := range pi978CanonicalRuleNames {
		pi978RowOf(t, exec, rule)
	}
	seeded := pi978RuleRowRe.FindAllStringSubmatch(exec, -1)
	if len(seeded) != len(pi978CanonicalRuleNames) {
		t.Errorf("978 的可执行面里有 %d 条规则行，期望 %d 条（canonical 名单 %d 条）",
			len(seeded), len(pi978CanonicalRuleNames), len(pi978CanonicalRuleNames))
	}
}

// TestMigration978IsIdempotentAndNonDestructive —— ③ 幂等 + 非破坏。
func TestMigration978IsIdempotentAndNonDestructive(t *testing.T) {
	raw, err := os.ReadFile(pi978CanonicalDir + "/" + pi978UpFile)
	if err != nil {
		t.Fatalf("读 978：%v", err)
	}
	exec := pi977StripComments(string(raw))

	if !strings.Contains(exec, "ON CONFLICT (rule_name) DO NOTHING") {
		t.Errorf("978 的可执行面缺少 `ON CONFLICT (rule_name) DO NOTHING` ⇒ 不幂等（重放会炸或会覆盖）")
	}
	if strings.Contains(exec, "DELETE FROM public.prompt_injection_rules") {
		t.Errorf("978 的可执行面**删除了规则行** ⇒ 破坏性（补种件不得删既有规则）")
	}
	if !strings.Contains(exec, "to_regclass('public.prompt_injection_rules') IS NULL") {
		t.Errorf("978 缺少「规则表不在场 ⇒ 跳过」的存在性守卫 ⇒ 裸 INSERT 会 42P01 ⇒ 部署 die")
	}
	if !strings.Contains(exec, "INSERT INTO public.schema_migrations") {
		t.Errorf("978 缺少台账自登记")
	}

	// down 件：只删本件唯二引入的两条。
	downRaw, err := os.ReadFile(pi978CanonicalDir + "/" + pi978DownFile)
	if err != nil {
		t.Fatalf("读 978 down：%v", err)
	}
	downExec := pi977StripComments(string(downRaw))
	for _, rule := range []string{"privilege_escalation", "command_injection"} {
		if !strings.Contains(downExec, "'"+rule+"'") {
			t.Errorf("978 的 down 件没有回退规则 %q", rule)
		}
	}
	for _, rule := range pi978CanonicalRuleNames[:21] {
		if strings.Contains(downExec, "'"+rule+"'") {
			t.Errorf("978 的 down 件**删除了 364 时代就有的规则** %q ⇒ 回退超出本件效果范围（破坏性）", rule)
		}
	}
}

// TestMigration978DeliveryIsReachable —— ④ 交付可达性（本轮新增的核心不变式）。
//
// ★ 这条门钉的是**缺陷的根因**，不是某个文件的形态：
//
//	「canonical 规则集必须有一条**可达**的投递通道」⇒
//	· 978 ∈ 安装器 tsv ∧ StartupFiles ∧ deploy files 数组；
//	· 且 **315 / 364 仍然不在**任何通道里（它们不可投递）⇒
//	  ⇒ 门明确禁止「改回依赖 315/364」这种看似更省事的修法。
func TestMigration978DeliveryIsReachable(t *testing.T) {
	tsv, err := os.ReadFile(pi978Tsv)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978Tsv, err)
	}
	runner, err := os.ReadFile(pi978DbinitRunner)
	if err != nil {
		t.Fatalf("读 %s：%v", pi978DbinitRunner, err)
	}
	seq, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}

	if !strings.Contains(string(tsv), "\t"+pi978UpFile) {
		t.Errorf("安装器 tsv 里没有 %s ⇒ **全新安装拿不到规则集**（本缺陷复现）", pi978UpFile)
	}
	if !strings.Contains(string(runner), `"`+pi978UpFile+`"`) {
		t.Errorf("dbinit StartupFiles 里没有 %s ⇒ **全新安装拿不到规则集**（本缺陷复现）", pi978UpFile)
	}
	if !strings.Contains(string(seq), "sql/migrations/startup/"+pi978UpFile) {
		t.Errorf("deploy 通道 files 数组里没有 %s ⇒ **存量库拿不到规则集**（本缺陷复现）", pi978UpFile)
	}

	// 315 / 364 仍然**不可投递** ⇒ 规则集必须靠 978，而不是靠它们。
	for _, seed := range []string{pi978Seed315, pi978Seed364} {
		base := seed[strings.LastIndex(seed, "/")+1:]
		if strings.Contains(string(tsv), "\t"+base) {
			t.Errorf("tsv 里出现了 %s ⇒ 它**不可投递**（315 的 INSERT 无 ON CONFLICT ⇒ 会 23505；364 同样未登记）⇒ 规则集必须由 978 承载", base)
		}
		if strings.Contains(string(runner), `"`+base+`"`) {
			t.Errorf("StartupFiles 里出现了 %s ⇒ 它不在任何通道的投递面 ⇒ 不可依赖", base)
		}
	}
}
