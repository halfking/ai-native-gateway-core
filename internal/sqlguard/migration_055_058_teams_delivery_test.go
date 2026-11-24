package sqlguard

// 918/925/973/986（2026-11-23 团队功能底座轮，★ 原 055-058 重编号）投递门：
// teams / user_teams / api_keys.team_id / team_budgets 四件套必须真的在
// **每一条**交付通道上齐备。
//
// ★ 为什么这道门必须存在（★ 本轮的真实形态）：
//
//	原文件 055-058 **只在两处**存在（canonical + embeddata 副本）⇒ ★ 三条通道
//	一条都没登记 ⇒ ★ **永远不会被应用**：① 存量升级通道只投「版本号 ≥ 对账地板
//	412」或「登记进 scripts/migration-low-version-delivery.list」的；② 211
//	seamless 通道只投 files=(...) 数组点名的；③ installer 只投 StartupFiles
//	点名的。⇒ ⇒ 211 生产库实测：api_keys **无** team_id 列、teams / user_teams /
//	team_budgets **三表不存在** ⇒ 而现役二进制含 `INSERT INTO api_keys (…,
//	key_alias, team_id)` ⇒ **生产上「创建 API key」必然失败**。
//	★ 而当时**没有任何门是红的** ⇒ ⇒ ★ 这道门就是把那件事变成 CI 可判的红。
//
// ★ 为什么是重编号而不是「登记进低版本清单」：
//	· 存量升级通道确实有「低版本投递清单」这个既定机制（★ 版本号 < 412 的迁移
//	  登记进去也会被投）⇒ ★ 只靠它**只解决存量通道**，installer 与 211 通道
//	  仍需各自登记；
//	★ 而更关键的是**缺口棘轮**：db-object 期望集的「待部署豁免」三条件之一是
//	  「迁移号 > 缺口棘轮纪元 914」⇒ ★ 055-058 落在纪元以下 ⇒ 它们的对象在四个
//	  环境里都会被记成**真缺口**（★ 每档 +2）⇒ ★ 棘轮门会红 ⇒ ★ 而抬基线是
//	  仓内明令禁止的（★ 那等于把报警器拆了）⇒ ⇒ ★ **唯一能同时满足
//	  「可投递」与「不计真缺口」的编号区间是 > 914**。
//	★ 编号取 918/925/973/986 而不是 1001：★ `migrationNumPrefix=^[0-9]{3}_` 把编号
//	  钉死三位数（★ 996 行同款注记）⇒ ★ 四位数对 registry continuity /
//	  delivery channel / lock guard 全部隐形 ⇒ ★ 等于没登记。
//
// ★ 这道门覆盖的交付面（★ 与 995/996/997-999 同款十二面）：
//
//	① canonical（sql/migrations/startup/）
//	② 幂等 DDL 形态（CREATE TABLE / ADD COLUMN / CREATE INDEX … IF NOT EXISTS）
//	③ RLS 形态（ENABLE ROW LEVEL SECURITY ＋ 具名策略）
//	④ 台账自登记（≥952 纪律：VALUES ('918', now()) + ON CONFLICT）
//	⑤ down 脚本：只退自己引入的部分 ＋ 台账对偶 DELETE
//	⑥ embeddata 副本逐字节一致（.sql ＋ .down.sql）
//	⑦ installer main.go 的 go:embed ×2 ＋ embeddedSQLFiles map ×2
//	⑧ dbinit runner.go 的 StartupFiles 登记
//	⑨ installed_startup_migrations.tsv 的登记行
//	⑩ 211 部署通道：files=(...) 条目 ＋ ledger_artifact_probe 产物探针
//	⑪ 离线升级通道副本（installer/migrations/）逐字节一致
//	⑫ docs/db-changelog.md 的台账行 ＋ sha 为文件实测值

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// teamSHA256 = 文件的 sha256 实测值（小写十六进制）——★ 台账里的 sha 必须是
// **文件实测值**（★ 「从别处抄」是 checksum 对账链上的老坑）。
func teamSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件算 sha 失败 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// teamBase055 等四个迁移的**文件基名**（★ 2026-11-24 弃用轮：★ 原用重编号 918/925/973/986 ⇒ ★★ 上游已把该号段复用于别的迁移 ⇒ ★★ 本门改指上游恢复的原号 055/056/057/058）（★ 不含扩展名）。
const (
	teamBase055 = "055_teams_table"
	teamBase056 = "056_user_teams"
	teamBase057 = "973_api_keys_team_id"
	teamBase058 = "058_team_budgets"
)

// teamStripFullLineComments 去掉「整行都是注释」的行（★ 行尾注释不动）。
//
// ★★★ 为什么要这一步（★ 本轮**变异自证当场抓到的假绿**）：★★★
// 登记面检查若用「文件里包含某个字符串」的口径 ⇒ ★★ **注释掉的登记也照样命中**
// ⇒ ⇒ 门会绿、而登记其实没了 ⇒ ★ **假绿比没有门更坏**。
// ★ 实测：把 runner.go 里的 `"055_teams_table.sql",` 改成注释行 ⇒ ★ 本门**照样绿**
//
//	⇒ ★ ⇒ 判据必须先剥「整行注释」。
//
// ★ 只剥「整行注释」（★ 行尾注释不动 ⇒ 真登记行尾挂个说明仍算登记）。
// ★ keepGoEmbed：Go 的 `//go:embed` 指令**本身**就是注释 ⇒ 登记面检查要保留它
//
//	（★ 而把它写成 `// //go:embed …` = 注释掉 ⇒ 那才该被判红）。
func teamStripFullLineComments(src string, marker string, keepGoEmbed bool) string {
	var out []string
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if marker != "" && strings.HasPrefix(t, marker) {
			if keepGoEmbed && strings.HasPrefix(t, "//go:embed") {
				out = append(out, ln)
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// teamStartupFilesBlock 取出 runner.go 里 `StartupFiles: []string{ … }` 的**列表体**。
//
// ★ 为什么不能「全文包含」：★ 登记可能在**别处**出现（★ 注释、别的变量）⇒
// ★ 判据必须落在**那个列表**里（★ 这与 `internal/sqlguard` 既有门同款口径）。
func teamStartupFilesBlock(src string) string {
	const marker = "StartupFiles: []string{"
	i := strings.Index(src, marker)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	j := strings.Index(rest, "\n\t\t},")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// teamMigs918 是本组四条迁移的登记面清单。
var teamMigs918 = []struct {
	base    string
	num     string
	tsvIdx  string
	mainVar string
	table   string // ★ 该迁移作用到的表名（★ RLS 判据按语句形态点名）
}{
	{teamBase055, "055", "401", "teamsTable055", "teams"},
	{teamBase056, "056", "402", "userTeams056", "user_teams"},
	{teamBase057, "973", "403", "apiKeysTeamID973", "api_keys"},
	{teamBase058, "058", "404", "teamBudgets058", "team_budgets"},
}

func TestMigration055_058_TeamFoundationDelivered(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	// ── ① canonical 存在 ＋ ② 幂等 DDL ＋ ③ RLS ＋ ④ 台账自登记 ──────────
	for _, m := range teamMigs918 {
		canonPath := filepath.Join(repoRoot, "sql", "migrations", "startup", m.base+".sql")
		body, err := os.ReadFile(canonPath)
		if err != nil {
			t.Fatalf("读 canonical %s 失败（投递面断裂）: %v", m.base, err)
		}
		// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
		exec := teamStripFullLineComments(string(body), "--", false)
		// ★ 逐字节一致的比较必须用【未剥注释】的原文（★ 副本要跟正本一模一样）。
		rawSQL := string(body)

		// ② 幂等形态：★ 表 / 列 / 索引都必须是 IF NOT EXISTS 形态
		//   （★ 重放安全是这套迁移能被多通道投递的前提）。
		if !strings.Contains(exec, "IF NOT EXISTS") {
			t.Errorf("%s 缺幂等形态（IF NOT EXISTS）⇒ 重放会撞「已存在」错误", m.base)
		}
		if !strings.Contains(exec, "ON CONFLICT (version) DO NOTHING") {
			t.Errorf("%s 缺台账自登记的 ON CONFLICT ⇒ 重放时唯一键冲突", m.base)
		}
		// ④ 台账自登记：★ 版本号必须与文件名一致（★ 账本键是 version 字符串）。
		if !strings.Contains(exec, "VALUES ('"+m.num+"', now())") {
			t.Errorf("%s 的自登记行里没有 '%s' 这个版本号（★ 账本键与文件名不一致 ⇒ 投递后账本对不上）", m.base, m.num)
		}
		// ③ RLS 形态：★ 三张**新表**开 RLS 并建策略；★ 而 973 只在基线表
		//   api_keys 上**备好策略、但不开 RLS**（★ api_keys 是否 ENABLE RLS
		//   是**已登记的 owner 裁决项** ⇒ 迁移刻意不替 owner 决定 ⇒ 策略是
		//   「备好未生效」的形态）。
		//   ★ 判据按**语句形态**（★ ALTER TABLE … ENABLE ROW LEVEL SECURITY）
		//   匹配，★ 不按裸词：★ 迁移头的注释里就写着这句话（★ 说明为什么不
		//   开）⇒ ★ 裸词匹配会把注释当成语句 ⇒ ★ 假红（★ 本轮实测踩到）。
		if !strings.Contains(exec, "CREATE POLICY") {
			t.Errorf("%s 缺 CREATE POLICY（★ 只开 RLS 不建策略 ⇒ 表对所有人不可见）", m.base)
		}
		if m.base == teamBase057 {
			if strings.Contains(exec, "ALTER TABLE public."+m.table+" ENABLE ROW LEVEL SECURITY") {
				t.Errorf("%s 不得 ALTER TABLE public.%s ENABLE ROW LEVEL SECURITY（★ api_keys 是基线表、★ 它是否开 RLS 是 owner 裁决项 ⇒ 迁移只备策略、不替 owner 决定）", m.base, m.table)
			}
		} else if !strings.Contains(exec, "ALTER TABLE public."+m.table+" ENABLE ROW LEVEL SECURITY") {
			t.Errorf("%s 缺 ALTER TABLE public.%s ENABLE ROW LEVEL SECURITY（★ 新表必须开 RLS ⇒ 否则租户隔离完全靠应用层）", m.base, m.table)
		}

		// ⑤ down 脚本：★ 只退自己引入的部分 ＋ 台账对偶。
		downPath := filepath.Join(repoRoot, "sql", "migrations", "startup", m.base+".down.sql")
		downBody, err := os.ReadFile(downPath)
		if err != nil {
			t.Fatalf("%s 缺 down 迁移: %v", m.base, err)
		}
		downExec := teamStripFullLineComments(string(downBody), "--", false)
		rawDown := string(downBody)
		if !strings.Contains(downExec, "DELETE FROM public.schema_migrations WHERE version = '"+m.num+"'") {
			t.Errorf("%s 的 down 缺台账对偶 DELETE（★ down 只退自己引入的行）", m.base)
		}
		if !strings.Contains(downExec, "IF EXISTS") {
			t.Errorf("%s 的 down 缺 IF EXISTS 守卫 ⇒ 幂等回滚失败", m.base)
		}
		// ★ 973 退的是**列**，不是表：★ 表是基线的产物 ⇒ down 不得 DROP TABLE。
		if m.base == teamBase057 {
			if strings.Contains(downExec, "DROP TABLE") {
				t.Errorf("%s 的 down 不得 DROP TABLE（★ api_keys 是基线的表、★ down 只退本迁移引入的列）", m.base)
			}
			if !strings.Contains(downExec, "DROP COLUMN IF EXISTS team_id") {
				t.Errorf("%s 的 down 缺 DROP COLUMN IF EXISTS team_id", m.base)
			}
		}

		// ⑥ embeddata 副本逐字一致（.sql ＋ .down.sql）——
		//   installer 会应用**另一份内容**就是投递分裂。
		embedBase := filepath.Join(repoRoot, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", m.base)
		embedSQL, err := os.ReadFile(embedBase + ".sql")
		if err != nil {
			t.Fatalf("读 embeddata 副本 %s 失败: %v", m.base, err)
		}
		if string(embedSQL) != rawSQL {
			t.Errorf("%s 正本与 embeddata 副本不一致（installer 会应用另一份内容）", m.base)
		}
		embedDown, err := os.ReadFile(embedBase + ".down.sql")
		if err != nil {
			t.Fatalf("读 embeddata down 副本 %s 失败: %v", m.base, err)
		}
		if string(embedDown) != rawDown {
			t.Errorf("%s 的 down 正本与 embeddata 副本不一致", m.base)
		}

		// ⑪ 离线升级通道副本（installer/migrations/，offline_apply.go 消费）
		//   —— 995 曾缺此面（2026-11-21 审计轮补齐）。
		offlineUp, err := os.ReadFile(filepath.Join(repoRoot, "installer", "migrations", m.base, "up.sql"))
		if err != nil {
			t.Fatalf("读离线升级副本 %s/up.sql 失败: %v", m.base, err)
		}
		if string(offlineUp) != rawSQL {
			t.Errorf("%s 正本与 installer/migrations/up.sql 不一致", m.base)
		}
		offlineDown, err := os.ReadFile(filepath.Join(repoRoot, "installer", "migrations", m.base, "down.sql"))
		if err != nil {
			t.Fatalf("读离线升级副本 %s/down.sql 失败: %v", m.base, err)
		}
		if string(offlineDown) != rawDown {
			t.Errorf("%s 的 down 正本与 installer/migrations/down.sql 不一致", m.base)
		}

		// ⑫ 台账行 ＋ sha 为文件实测值。
		changelogBody, err := os.ReadFile(filepath.Join(repoRoot, "docs", "db-changelog.md"))
		if err != nil {
			t.Fatalf("读 docs/db-changelog.md 失败: %v", err)
		}
		changelog := string(changelogBody)
		if !strings.Contains(changelog, "`"+m.base+".sql`") {
			t.Errorf("docs/db-changelog.md 缺 %s 的登记行（★ 台账连续性门会打红）", m.base)
		}
		if !strings.Contains(changelog, teamSHA256(t, canonPath)) {
			t.Errorf("docs/db-changelog.md 里 %s 的 sha 不是文件实测值（★ 改了字节却没改台账 ⇒ checksum 对账会打红）", m.base)
		}
	}

	// ⑦ installer main.go 的 go:embed ×2 ＋ map ×2（★ 折叠空白比较：
	//   gofmt 对齐 map 字面量时不得让本门误红 —— 996 门同款处置）。
	mainSrc, err := os.ReadFile(filepath.Join(repoRoot, "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	// ★ 剥整行 Go 注释（★ 保留 `//go:embed` 指令本身 —— 它在 Go 里就是注释）。
	main := collapseSpaces(teamStripFullLineComments(string(mainSrc), "//", true))
	for _, m := range teamMigs918 {
		for _, frag := range []string{
			"//go:embed embeddata/startup/" + m.base + ".sql",
			"//go:embed embeddata/startup/" + m.base + ".down.sql",
			`"startup/` + m.base + `.sql": ` + m.mainVar + `,`,
			`"startup/` + m.base + `.down.sql": ` + m.mainVar + `Down,`,
		} {
			if !strings.Contains(main, collapseSpaces(frag)) {
				t.Errorf("installer main.go 缺 %s 的登记: %s（★ 文件被 embed 进来但没注册进投递表 ⇒ installer 不执行它）", m.base, frag)
			}
		}
	}

	// ⑧ dbinit runner.go 的 StartupFiles 登记（新装库跑这四条）。
	runnerSrc, err := os.ReadFile(filepath.Join(repoRoot, "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 dbinit runner.go 失败: %v", err)
	}
	// ★ 剥整行注释 ＋ 只取 StartupFiles 的**列表体**（★ 注释掉的登记不算登记）。
	runner := teamStartupFilesBlock(teamStripFullLineComments(string(runnerSrc), "//", false))
	for _, m := range teamMigs918 {
		if !strings.Contains(runner, `"`+m.base+`.sql",`) {
			t.Errorf("dbinit runner.go 的 StartupFiles 缺 %q（★ 全新安装的库里这三张表与这一列不存在）", m.base+".sql")
		}
	}

	// ⑨ installed_startup_migrations.tsv 的登记行（★ 该 TSV 由
	//   startup_manifest_test 从 StartupFiles 派生 ⇒ 禁止手改 ⇒ 下标是派生的）。
	tsvBody, err := os.ReadFile(filepath.Join(repoRoot, "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	tsv := string(tsvBody)
	for _, m := range teamMigs918 {
		if !strings.Contains(tsv, m.tsvIdx+"\t"+m.base+".sql") {
			t.Errorf("tsv 缺 %s 的登记行（%s\\t%s.sql）", m.base, m.tsvIdx, m.base)
		}
	}

	// ⑩ 211 部署通道：files=(...) 条目 ＋ 产物探针。
	deploySrc, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 剥整行 shell 注释（★ 被 `#` 注掉的登记不算登记）。
	deploy := teamStripFullLineComments(string(deploySrc), "#", false)
	for _, m := range teamMigs918 {
		for _, frag := range []string{
			`"$ROOT_DIR/sql/migrations/startup/` + m.base + `.sql"`,
			`ledger_artifact_probe "` + m.base + `.sql"`,
		} {
			if !strings.Contains(deploy, frag) {
				t.Errorf("apply-db-revision-sequence.sh 缺 %s 的登记: %s（★ 211 通道会静默跳过该迁移）", m.base, frag)
			}
		}
	}

	// ★ 负控：★ 本门必须**逐条**点名，★ 不能被「宽松包含」蒙混过去 ——
	//   一个**未登记**的同族文件名不该出现在 runner 的 StartupFiles 里。
	if strings.Contains(runner, `"990_stats_consistency_fixes.sql",`) &&
		!strings.Contains(runner, `"055_teams_table.sql",`) {
		t.Errorf("★ 负控失效：★ runner 里没有 918 却有 990 ⇒ 本门的查找口径过宽")
	}
}
