package sqlguard

// 918 / 925（2026-11-23 956/981 断链修复轮，★ 方案乙）投递门。
//
// ★★★ 这两道断链是什么（★ 本门要守的东西）：★★★
//
//	① **956 断链**：956_minimax_m31_flash_featured.sql 的守卫要求
//	   `models_canonical` 里已有 `canonical_name='minimax-m3.1-flash'` 且
//	   `status='active'` ⇒ 而这一行的**唯一创建者**是基线 `02-seed.sql`
//	   ⇒ ★ 新装库里 956 执行时这一行不存在 ⇒ ⇒ 956 的 RAISE EXCEPTION 必触发
//	   ⇒ ⇒ **全新安装链在 956 中止** ⇒ ⇒ 957 之后的全部迁移拿不到。
//	② **981 断链**：981 的 4 个物化视图都读 `SUM(ss.total_credits_charged)`
//	   ⇒ 而该列在仓内由 **990** 才 `ADD COLUMN IF NOT EXISTS` ⇒ ⇒ **981 < 990**
//	   ⇒ ⇒ 新装库跑到 981 时列不存在 ⇒ ⇒ 42P03 ⇒ ⇒ **全新安装链在 981 中止**。
//
// ★★ 为什么这两个断链**至今无人发现**（★ 本门的立论依据）：★★
//
//	① 「登记进安装器清单」≠「新装能拿到」：★ 955/956/957/…/981/982-985 **全都**
//	  登记在案、★ 门也全绿 ⇒ ⇒ ★ 而全新安装链**在 956 就断了** ⇒ ⇒ 其后的
//	  每一条迁移的「登记面」都是**登记了但拿不到**。
//	② ★ **台账连续性门判的是「有没有登记」**（★ `migration_registry_continuity_test`
//	  判 changelog 里有 sha）⇒ ⇒ **它判不出「登记了但排错序」**。
//	③ ★ 两条断链的**共性**是：**依赖方排在被依赖方之后**（918→956、925→981）
//	  ⇒ ⇒ **存在性判据（有没有登记）原理上看不见它们** ⇒ ⇒ 必须有**顺序判据**。
//
// ★★ 所以本门的核心不是「918/925 登记了没」（★ 那由 982-985 门同款十二面覆盖），
//    而是「**918 排在 956 之前、925 排在 981 之前**」（★ 三条通道各判一次）。
//
// ★★★ 这道门覆盖的交付面（★ 与 982-985 门同款十二面 ＋ ★ 新增的顺序面）：★★★
//
//	① canonical（sql/migrations/startup/）
//	② 幂等 DDL 形态（★ ON CONFLICT DO NOTHING / ADD COLUMN IF NOT EXISTS）
//	③ 台账自登记（≥952 纪律：VALUES ('918', now()) + ON CONFLICT）
//	④ down 脚本：只退自己引入的部分 ＋ 台账对偶 DELETE
//	⑤ embeddata 副本逐字节一致（.sql ＋ .down.sql）
//	⑥ installer main.go 的 go:embed ＋ embeddedSQLFiles map
//	⑦ dbinit runner.go 的 StartupFiles 登记
//	⑧ installed_startup_migrations.tsv 的登记行
//	⑨ 211 部署通道：files=(...) 条目 ＋ ledger_artifact_probe 产物探针
//	⑩ 离线升级通道副本（installer/migrations/）逐字节一致
//	⑪ docs/db-changelog.md 的台账行 ＋ sha 为文件实测值
//	⑫ ★★ **顺序面**：★ 918 < 956、925 < 981 ⇒ ★ 在 runner.go 的 StartupFiles、
//	    211 通道的 files=(...) 数组、installed_startup_migrations.tsv 三处各判一次
//	    （★ **三处都要判**：★ 少判一处 ⇒ 那条通道上断链依旧）

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// brkSHA256 ＝ 文件的 sha256 实测值（小写十六进制）。
func brkSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件算 sha 失败 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// brkFilesArrayBlock 取出 shell 脚本里 `files=(` … `)` 数组的**列表体**。
//
// ★ 为什么不能全文搜文件名：★ 注释里也提到过「files=(...)」（★ 例如
// 「★ 从通道 files=(...) 数组」这种说明句）⇒ ⇒ 全文搜会命中**注释里的**名字
// ⇒ ⇒ 顺序判据会拿注释位置当登记位置 ⇒ ⇒ 假绿。
//
// ★★ 为什么锚点必须按**行首**定位（★ 本门首版的坑）：★ 首版用
// `strings.Index(src, "files=(")` 找锚 ⇒ ★ 而**注释里**也出现过
// `files=(...)` 这个子串 ⇒ ⇒ 锚点可能落在**注释**上 ⇒ ⇒ 取出来的「列表体」
// 是注释区 ⇒ ⇒ 顺序判据在注释区里比位置 ⇒ ⇒ **假绿**。
// ⇒ ⇒ ★ 这里改成**逐行扫描 + 只认「整行就是 files=(」**、★ 并用**行偏移**
// 定位 ⇒ ⇒ 注释行（无论长什么样）永远不会被当成数组头。
func brkFilesArrayBlock(src string) string {
	offset := 0
	for _, ln := range strings.Split(src, "\n") {
		if strings.TrimSpace(ln) == "files=(" {
			rest := src[offset+len(ln):]
			j := strings.Index(rest, "\n)")
			if j < 0 {
				return rest
			}
			return rest[:j]
		}
		offset += len(ln) + 1 // ★ +1 ＝ 换行符本身
	}
	return ""
}

// brkOrderIn ＝ 两条登记在**同一段文本**里谁在前（★ 谁在前返回 true）。
//
// ★ 只用 strings.Index 的第一次出现 ⇒ ★ 「同名多处出现」时以第一次为准
//
//	（★ 对本门的三处判据都是对的：★ StartupFiles / files=(...) 数组里
//	★ 每条迁移**只登记一次** ⇒ 第一次出现就是登记位置）。
func brkOrderIn(block string, earlier string, later string) (bool, bool) {
	i := strings.Index(block, earlier)
	j := strings.Index(block, later)
	if i < 0 || j < 0 {
		return false, false
	}
	return i < j, true
}

func TestMigration918925BreakChainFixed(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	// ── ① canonical ＋ ② 幂等形态 ＋ ③ 台账自登记 ＋ ④ down 对偶 ─────────
	for _, m := range []struct {
		base       string
		num        string
		tsvIdx     string
		mainVar    string
		idempotent string // ★ 幂等形态的判据串（★ 剥整行注释后仍须命中）
	}{
		{
			base: "918_models_canonical_minimax_m31_seed", num: "918",
			tsvIdx: "356", mainVar: "modelsCanonicalMinimaxM31Seed918",
			idempotent: "ON CONFLICT (canonical_name) DO NOTHING",
		},
		{
			base: "925_session_summaries_total_credits_charged", num: "925",
			tsvIdx: "381", mainVar: "sessionSummariesTotalCreditsCharged925",
			idempotent: "ADD COLUMN IF NOT EXISTS total_credits_charged",
		},
	} {
		canonPath := filepath.Join(repoRoot, "sql", "migrations", "startup", m.base+".sql")
		body, err := os.ReadFile(canonPath)
		if err != nil {
			t.Fatalf("读 canonical %s 失败（投递面断裂）: %v", m.base, err)
		}
		// ★ 剥整行 SQL 注释（★ 「注释掉的语句」不该被当成已登记）。
		exec := sgStripSQLComments(string(body))
		rawSQL := string(body)

		// ② 幂等形态（★ 重放安全是这套迁移能被多通道投递的前提）。
		if !strings.Contains(exec, m.idempotent) {
			t.Errorf("%s 缺幂等形态（%s）⇒ 重放会撞「已存在」/ 唯一键错误", m.base, m.idempotent)
		}
		// ③ 台账自登记：★ 版本号必须与文件名一致（★ 账本键是 version 字符串）。
		if !strings.Contains(exec, "ON CONFLICT (version) DO NOTHING") {
			t.Errorf("%s 缺台账自登记的 ON CONFLICT ⇒ 重放时唯一键冲突", m.base)
		}
		// ★★ 判据放宽到「两种既有形态都算」（★ 本门首版只认 `(version, applied_at)
		//   VALUES ('918', now())` ⇒ 把 956/990/989 同款的 `(version, description)
		//   VALUES ('918', '…')` 判红 ⇒ ★ **假红**：★ 基线
		//   `public.schema_migrations` 的 applied_at 是 `timestamp with time zone
		//   DEFAULT now()`（★ 可空、有默认值）⇒ ⇒ 两种形态都合法
		//   ⇒ ⇒ 判据只该钉「版本号字面量」这一件事。
		selfReg := regexp.MustCompile(`VALUES\s*\(\s*'` + m.num + `'\s*,`)
		if !selfReg.MatchString(exec) {
			t.Errorf("%s 的自登记行里没有 '%s' 这个版本号（★ 账本键与文件名不一致 ⇒ 投递后账本对不上）", m.base, m.num)
		}
		// ★★ 918 的**语义前提**：★ 它播种的那一行必须是 956 守卫要找的那一行
		//   （canonical_name 逐字一致）⇒ ★ 否则 918 装了、956 还是 abort。
		if m.num == "918" {
			if !strings.Contains(exec, "'minimax-m3.1-flash'") {
				t.Errorf("918 没有播种 'minimax-m3.1-flash' ⇒ ★ 956 的守卫仍然找不到它 ⇒ 断链未修")
			}
			if !strings.Contains(exec, "'active'") {
				t.Errorf("918 播种的行缺 status='active' ⇒ ★ 956 的守卫要求 status='active' ⇒ 断链未修")
			}
		}
		// ★★ 925 的**语义前提**：★ 它必须建**那个列名**（★ 981 的视图读的是它）。
		if m.num == "925" {
			if !strings.Contains(exec, "ALTER TABLE public.session_summaries") {
				t.Errorf("925 不得改别的表（★ 981 的视图读的是 public.session_summaries）")
			}
		}

		// ④ down 脚本：★ 只退自己引入的部分 ＋ 台账对偶。
		downPath := filepath.Join(repoRoot, "sql", "migrations", "startup", m.base+".down.sql")
		downBody, err := os.ReadFile(downPath)
		if err != nil {
			t.Fatalf("%s 缺 down 迁移: %v", m.base, err)
		}
		downExec := sgStripSQLComments(string(downBody))
		rawDown := string(downBody)
		if !strings.Contains(downExec, "DELETE FROM public.schema_migrations WHERE version = '"+m.num+"'") {
			t.Errorf("%s 的 down 缺台账对偶 DELETE（★ down 只退自己引入的行）", m.base)
		}
		if !strings.Contains(downExec, "BEGIN;") || !strings.Contains(downExec, "COMMIT;") {
			t.Errorf("%s 的 down 缺显式事务包裹（★ 失败即回滚 ⇒ 「要么全退、要么全不退」才成立）", m.base)
		}
		// ★★ 918 的 down 只能删**自己播种**的那一行（★ source='seed-standard-rollout'）
		//   ⇒ ★ 若它无差别 DELETE，生产上 discovery 同步进来的那行会被误删。
		if m.num == "918" {
			if !strings.Contains(downExec, "source = 'seed-standard-rollout'") {
				t.Errorf("918 的 down 缺 source 限定（★ 生产那行是 discovery 同步进来的 ⇒ 无差别删会误删生产数据）")
			}
			if strings.Contains(downExec, "DELETE FROM public.models_canonical") &&
				!strings.Contains(downExec, "canonical_name = 'minimax-m3.1-flash'") {
				t.Errorf("918 的 down 缺 canonical_name 限定（★ down 只退自己引入的那一行）")
			}
		}
		// ★★ 925 的 down **不得**删列（★ 该列由 925 与 990 共有 ⇒ 删它会破 981/994 的视图）。
		if m.num == "925" {
			if strings.Contains(downExec, "DROP COLUMN") {
				t.Errorf("925 的 down 不得 DROP COLUMN（★ 该列由 925 与 990 共有 ⇒ 删它会破 981/994 的物化视图）")
			}
		}

		// ⑤ embeddata 副本逐字一致（★ installer 会应用**另一份内容**就是投递分裂）。
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

		// ⑩ 离线升级通道副本（installer/migrations/，offline_apply.go 消费）。
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

		// ⑪ 台账行 ＋ sha 为文件实测值。
		changelogBody, err := os.ReadFile(filepath.Join(repoRoot, "docs", "db-changelog.md"))
		if err != nil {
			t.Fatalf("读 docs/db-changelog.md 失败: %v", err)
		}
		changelog := string(changelogBody)
		if !strings.Contains(changelog, "`"+m.base+".sql`") {
			t.Errorf("docs/db-changelog.md 缺 %s 的登记行（★ 台账连续性门会打红）", m.base)
		}
		if !strings.Contains(changelog, brkSHA256(t, canonPath)) {
			t.Errorf("docs/db-changelog.md 里 %s 的 sha 不是文件实测值（★ 改了字节却没改台账 ⇒ checksum 对账会打红）", m.base)
		}
	}

	// ⑥ installer main.go 的 go:embed ＋ embeddedSQLFiles map（★ 折叠空白比较：
	//   gofmt 对齐 map 字面量时不得让本门误红 —— 982-985 门同款处置）。
	mainSrc, err := os.ReadFile(filepath.Join(repoRoot, "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	// ★ 剥整行 Go 注释（★ 保留 `//go:embed` 指令本身 —— 它在 Go 里就是注释）。
	main := collapseSpaces(sgStripGoCommentsKeepEmbed(string(mainSrc)))
	for _, m := range []struct {
		base    string
		mainVar string
	}{
		{"918_models_canonical_minimax_m31_seed", "modelsCanonicalMinimaxM31Seed918"},
		{"925_session_summaries_total_credits_charged", "sessionSummariesTotalCreditsCharged925"},
	} {
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

	// ⑦ dbinit runner.go 的 StartupFiles 登记（★ 新装库跑这两条）。
	runnerSrc, err := os.ReadFile(filepath.Join(repoRoot, "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 dbinit runner.go 失败: %v", err)
	}
	// ★ 剥整行注释 ＋ 只取 StartupFiles 的**列表体**（★ 注释掉的登记不算登记）。
	runner := teamStartupFilesBlock(sgStripFullLineComments(string(runnerSrc), "//", false))
	for _, base := range []string{
		"918_models_canonical_minimax_m31_seed.sql",
		"925_session_summaries_total_credits_charged.sql",
	} {
		if !strings.Contains(runner, `"`+base+`",`) {
			t.Errorf("dbinit runner.go 的 StartupFiles 缺 %q（★ 全新安装的库里断链仍在）", base)
		}
	}

	// ⑧ installed_startup_migrations.tsv 的登记行（★ 该 TSV 由
	//   startup_manifest_test 从 StartupFiles 派生 ⇒ 禁止手改 ⇒ 下标是派生的）。
	tsvBody, err := os.ReadFile(filepath.Join(repoRoot, "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	tsv := string(tsvBody)
	for _, m := range []struct {
		base   string
		tsvIdx string
	}{
		{"918_models_canonical_minimax_m31_seed", "356"},
		{"925_session_summaries_total_credits_charged", "381"},
	} {
		if !strings.Contains(tsv, m.tsvIdx+"\t"+m.base+".sql") {
			t.Errorf("tsv 缺 %s 的登记行（%s\\t%s.sql）", m.base, m.tsvIdx, m.base)
		}
	}

	// ⑨ 211 部署通道：files=(...) 条目 ＋ 产物探针。
	deploySrc, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	// ★ 剥整行 shell 注释（★ 被 `#` 注掉的登记不算登记）。
	deploy := sgStripShellComments(string(deploySrc))
	for _, m := range []struct {
		base string
		frag string
	}{
		{"918_models_canonical_minimax_m31_seed", `"$ROOT_DIR/sql/migrations/startup/918_models_canonical_minimax_m31_seed.sql"`},
		{"925_session_summaries_total_credits_charged", `"$ROOT_DIR/sql/migrations/startup/925_session_summaries_total_credits_charged.sql"`},
	} {
		if !strings.Contains(deploy, m.frag) {
			t.Errorf("apply-db-revision-sequence.sh 缺 %s 的 files=(...) 登记（★ 211 通道会静默跳过该迁移）", m.base)
		}
	}
	for _, frag := range []string{
		`ledger_artifact_probe "918_models_canonical_minimax_m31_seed.sql"`,
		`ledger_artifact_probe "925_session_summaries_total_credits_charged.sql"`,
	} {
		if !strings.Contains(deploy, frag) {
			t.Errorf("apply-db-revision-sequence.sh 缺产物探针: %s（★ 账本认了账、产物没了 ⇒ 无人报警）", frag)
		}
	}

	// ⑫ ★★★ **顺序面**（★ 本门的核心）：★ 918 必须排在 956 之前、925 必须排在
	//   981 之前 ⇒ ★ **三处通道各判一次**（★ 少判一处 ⇒ 那条通道上断链依旧）。
	type orderCase struct {
		desc    string
		block   string
		earlier string
		later   string
	}
	cases := []orderCase{
		{
			desc:    "installer StartupFiles（★ 新装库）",
			block:   runner,
			earlier: `"918_models_canonical_minimax_m31_seed.sql",`,
			later:   `"956_minimax_m31_flash_featured.sql",`,
		},
		{
			desc:    "installer StartupFiles（★ 新装库）",
			block:   runner,
			earlier: `"925_session_summaries_total_credits_charged.sql",`,
			later:   `"981_session_analytics_foundation.sql",`,
		},
		{
			desc:    "211 部署通道 files=(...) 数组",
			block:   brkFilesArrayBlock(deploy),
			earlier: `"$ROOT_DIR/sql/migrations/startup/918_models_canonical_minimax_m31_seed.sql"`,
			later:   `"$ROOT_DIR/sql/migrations/startup/956_minimax_m31_flash_featured.sql"`,
		},
		{
			desc:    "211 部署通道 files=(...) 数组",
			block:   brkFilesArrayBlock(deploy),
			earlier: `"$ROOT_DIR/sql/migrations/startup/925_session_summaries_total_credits_charged.sql"`,
			later:   `"$ROOT_DIR/sql/migrations/startup/981_session_analytics_foundation.sql"`,
		},
		{
			desc:    "installed_startup_migrations.tsv（★ StartupFiles 的机器可读镜像）",
			block:   tsv,
			earlier: "\t918_models_canonical_minimax_m31_seed.sql",
			later:   "\t956_minimax_m31_flash_featured.sql",
		},
		{
			desc:    "installed_startup_migrations.tsv（★ StartupFiles 的机器可读镜像）",
			block:   tsv,
			earlier: "\t925_session_summaries_total_credits_charged.sql",
			later:   "\t981_session_analytics_foundation.sql",
		},
	}
	for _, c := range cases {
		ok, both := brkOrderIn(c.block, c.earlier, c.later)
		if !both {
			t.Errorf("★ 顺序判据无法判定（%s）：★ 找不到 %s 或 %s ⇒ ★ 登记面本身已断", c.desc, c.earlier, c.later)
			continue
		}
		if !ok {
			t.Errorf("★ 顺序错（%s）：★ %s 必须排在 %s **之前**（★ 否则断链依旧：★ 依赖方先跑 ⇒ 前提不在场）",
				c.desc, c.earlier, c.later)
		}
	}

	// ★★ 负控：★ 本门必须**逐条**点名，★ 不能被「宽松包含」蒙混过去 ——
	//   一个**未登记**的同族文件名不该出现在登记面里。
	if strings.Contains(runner, `"990_stats_consistency_fixes.sql",`) &&
		!strings.Contains(runner, `"918_models_canonical_minimax_m31_seed.sql",`) {
		t.Errorf("★ 负控失效：★ runner 里没有 918 却有 990 ⇒ 本门的查找口径过宽")
	}
}
