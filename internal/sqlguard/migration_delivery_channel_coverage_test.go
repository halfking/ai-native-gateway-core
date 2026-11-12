package sqlguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestStartupMigrationsStayInEveryDeliveryChannel —— 近邻窗口内的 startup
// 迁移必须在**三条投递通道**里都在场。
//
// # 背景（2026-11-12 48h 审计第五轮，本轮真实撞到）
//
//	迁移 962 在 838d2594f 落地时，只登记了 **installer 通道**
//	（`installer/internal/dbinit/runner.go` 的 `StartupFiles`）。
//	⇒ 但 211 的 seamless 部署通道走的是**另一份显式清单**：
//	   `scripts/apply-db-revision-sequence.sh` 的 `files=( ... )`。
//	⇒ 211 实测：`public.schema_migrations` 停在 **961**、
//	   `public.gateway_db_revision_sequences` 也**没有 962 行**
//	   ⇒ **211 的记账链在 962 处断层**。
//
//	★ 旁证（这条断层**不是**取舍，是疏漏）：仓内「编号 ≥900 的 startup 迁移
//	  全部在该清单里」是既有惯例 —— 实测 900~961 全部收录，**唯独 962 不在**。
//
//	★ ★ 而「记账断层」与「功能缺失」是两件事（别混）：
//	   211 的函数体仍被 boot 自愈（`db.recentSuccessRateEnsureSQL`）收敛到正确
//	   形态（实测 `prosrc` 带索引同形谓词）⇒ **功能面是好的**；
//	   但「账本说没做」⇒ 下次有人**按账本**判断「要不要重跑 / 要不要补登记」时
//	   会踩空 ⇒ ⇒ 记账链连续性本身就是本仓的一等公民
//	   （迁移 960 的全名就叫 `960_migration_ledger_continuity_repair`）。
//
// # 三条通道与各自的门
//
//	① 台账面            docs/db-changelog.md
//	  → 已有门：TestMigrationRegistryStaysContinuous（本文件同包）
//	② installer 通道    installer/internal/dbinit/runner.go 的 StartupFiles
//	  → 已有门：installer/internal/dbinit 的 TestStartupManifest
//	③ ★ 211 部署通道    scripts/apply-db-revision-sequence.sh 的 files=(...)
//	  → ★ 本门
//
//	⇒ ★ **一条通道漏登记＝那条通道上的机器永远看不到这条迁移**
//	  （211 的机器：迁移没进清单 ⇒ 不应用；installer 的机器：不在 StartupFiles
//	  ⇒ 不应用）⇒ ⇒ 新增 startup 迁移必须**三条通道同时登记**。
//
// # 判据设计（刻意避开本仓反复踩到的两个坑）
//
//	① **锚点取自磁盘侧**（磁盘上的最大迁移编号），**不取自清单侧**
//	  —— 否则「删掉清单里的那一行」会让锚点下移、把那个缺口自己移出窗口
//	  （同文件 TestMigrationRegistryStaysContinuous 的 NC-D 负控）。
//	② **只对近邻窗口断言**（与同包那道门同宽）—— 对全部历史文件断言会误报
//	  数百条（老文件未登记是设计内）⇒ **一道会误报的门比没有门更坏**。
const (
	// 211 seamless 部署通道的显式清单。
	revisionSequenceScriptPath = "../../scripts/apply-db-revision-sequence.sh"

	// startup 迁移目录（本门只看 startup/：manual/ 是**人工**在生产上跑的，
	// 刻意不进自动序列 —— 同 830 的处置口径）。
	startupMigrationsDir = "../../sql/migrations/startup"
)

// revisionSequenceEntryRe ＝ 211 通道清单里**数组条目**的精确形态。
//
// ⚠ 必须匹配**数组条目的精确形态**（带 `$ROOT_DIR` 前缀与双引号），
//
//	不能用「文件名出现在脚本里就算」—— 脚本里还有 `ledger_artifact_probe`
//	与 legacy replay 的按名引用（实测数组外仍有 1 处 startup 文件引用）
//	⇒ 宽口径会把「只有探针、没有清单条目」误判成已登记。
var revisionSequenceEntryRe = regexp.MustCompile(`"\$ROOT_DIR/sql/migrations/startup/([0-9][0-9a-z_]*\.sql)"`)

// revisionSequenceExemptions ＝「有意不进 211 自动序列」的 startup 迁移。
//
// ★ 判据：这是**例外表**，不是「缺口名单」—— 进表必须写明理由。
//
//	任何「新迁移忘了登记」都会先撞这道门。
var revisionSequenceExemptions = map[string]string{}

func TestStartupMigrationsStayInEveryDeliveryChannel(t *testing.T) {
	// ── ① 解析 211 通道的显式清单（只取 files=( ) 数组内的条目）────────────
	seqRaw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 211 部署通道清单 %s：%v", revisionSequenceScriptPath, err)
	}
	listed := map[string]bool{}
	for _, m := range revisionSequenceEntryRe.FindAllStringSubmatch(string(seqRaw), -1) {
		listed[m[1]] = true
	}
	if len(listed) == 0 {
		t.Fatalf("解析不到 211 通道清单的任何条目 —— 判据的形态写坏了（不是迁移漏登记）")
	}

	// ── ② 磁盘侧的编号迁移（锚点取磁盘侧，见文件头「判据设计」①）─────────
	entries, err := os.ReadDir(startupMigrationsDir)
	if err != nil {
		t.Fatalf("读迁移目录 %s：%v", startupMigrationsDir, err)
	}
	type mig struct {
		name string
		num  int
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		if !migrationNumPrefix.MatchString(e.Name()) {
			continue
		}
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			continue
		}
		migs = append(migs, mig{e.Name(), n})
	}
	if len(migs) == 0 {
		t.Fatalf("迁移目录里没有编号迁移：%s", startupMigrationsDir)
	}
	maxNum := -1
	for _, m := range migs {
		if m.num > maxNum {
			maxNum = m.num
		}
	}

	// ── ③ 近邻窗口内逐个表态 ───────────────────────────────────────────────
	found := 0
	for _, m := range migs {
		if m.num <= maxNum-windowSize || m.num > maxNum {
			continue
		}
		found++
		if listed[m.name] {
			continue
		}
		if note, ok := revisionSequenceExemptions[m.name]; ok {
			t.Logf("【已登记例外】%s 不在 211 自动序列清单里：%s", m.name, note)
			continue
		}
		t.Errorf("startup 迁移 %s（编号 %d，在近邻窗口 [%d, %d] 内）**未登记进 211 部署通道清单**"+
			"（%s 的 files=(...) 数组）。\n"+
			"后果：该迁移**不会被 211 的 seamless 部署通道应用** ⇒ 211 的 "+
			"public.schema_migrations 与 public.gateway_db_revision_sequences 都会在它这里**留断层**。\n"+
			"★ 实证（2026-11-12）：迁移 962 就漏了这一条通道 ⇒ 211 的记账链停在 961；\n"+
			"★ 注意「记账断层 ≠ 功能缺失」：函数体仍可能被 boot 自愈（Go 运行时 SSOT）收敛，\n"+
			"   但「账本说没做」会让后续按账本判断的人踩空。\n"+
			"修法：在 %s 的 files=(...) 数组里补一行（同条形制），并在旁边加一条\n"+
			"      ledger_artifact_probe \"<文件名>\" \"<产物断言>\"；\n"+
			"若这是有意不登记，请登记进 revisionSequenceExemptions 并写明理由。",
			m.name, m.num, maxNum-windowSize+1, maxNum, revisionSequenceScriptPath, revisionSequenceScriptPath)
	}
	if found == 0 {
		t.Fatalf("近邻窗口内没有编号迁移（maxNum=%d）—— 判据可能失效", maxNum)
	}
}

// TestRevisionSequenceEntryShapeIsPrecise —— 解析精度门（真实数据等值 + 合成对照）。
//
// ★ 为什么要有这道：**上一段那个判据的「匹配形态」写宽了就会恒绿**。
//
//	脚本里除了 files=( ) 数组，还有 `ledger_artifact_probe "<文件名>"` 与
//	legacy replay 的按名引用 ⇒ 若判据写成「文件名出现在脚本里就算」，
//	则「只有探针、没有清单条目」的迁移会被误判成已登记 ⇒ 门失效。
//
// ⇒ 这里钉两件事：
//
//	① ★ **真实数据等值**：把正则施加于**整个脚本**的命中集，必须与
//	   「只解析 files=( ) 数组块」的命中集**完全相等**
//	   ⇒ 正则既不漏数组里的条目、也不命中数组外的东西（artifact probe /
//	     legacy replay 的按名引用）⇒ **门的解析范围被证明是数组**。
//	② ★ **合成对照**：数组条目形态**必须**命中；artifact probe 的按名形态
//	   **必须不**命中 —— 这两条即使在真实数据上恰好同集，也各自独立成立
//	   （真实数据上 860 同时出现在数组内外 ⇒ 「宽口径会多命中」这个前提
//	   在真实数据上不成立 ⇒ 只能靠合成对照把判据本身钉住）。
func TestRevisionSequenceEntryShapeIsPrecise(t *testing.T) {
	raw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	text := string(raw)

	// ── ① 真实数据等值：整脚本命中集 == 数组块命中集 ─────────────────────
	arrBlock := revisionSequenceArrayBlock(t, text)
	strictWhole := map[string]bool{}
	for _, m := range revisionSequenceEntryRe.FindAllStringSubmatch(text, -1) {
		strictWhole[m[1]] = true
	}
	strictBlock := map[string]bool{}
	for _, m := range revisionSequenceEntryRe.FindAllStringSubmatch(arrBlock, -1) {
		strictBlock[m[1]] = true
	}
	if len(strictWhole) != len(strictBlock) {
		t.Errorf("解析精度门红：整脚本命中 %d 个、数组块命中 %d 个 ⇒ "+
			"判据的匹配范围**越出了数组**（或漏了数组里的条目）⇒ "+
			"「清单条目」这个概念被算错了 ⇒ 上面那道门会误判",
			len(strictWhole), len(strictBlock))
	}
	for name := range strictWhole {
		if !strictBlock[name] {
			t.Errorf("判据命中了数组之外的条目：%s（多半是 artifact probe 或 legacy replay 的按名引用）", name)
		}
	}
	for name := range strictBlock {
		if !strictWhole[name] {
			t.Errorf("数组里的条目被判据漏掉：%s", name)
		}
	}

	// ── ② 合成对照：数组条目形态命中 / artifact probe 按名形态不命中 ──────
	const arrayForm = `  "$ROOT_DIR/sql/migrations/startup/962_recent_success_rate_index_aligned.sql"`
	if !revisionSequenceEntryRe.MatchString(arrayForm) {
		t.Error("合成对照失效：判据匹配不到真实的数组条目形态")
	}
	const probeForm = `ledger_artifact_probe "962_recent_success_rate_index_aligned.sql" "to_regclass('public.x') IS NOT NULL"`
	if revisionSequenceEntryRe.MatchString(probeForm) {
		t.Error("合成对照失效：判据把 artifact probe 的按名形态也当成了清单条目 ⇒ " +
			"「只有探针、没有清单条目」的迁移会被误判成已登记 ⇒ 门失效")
	}
}

// revisionSequenceArrayBlock 抽出脚本里 files=( ... ) 数组块的文本。
//
// ⚠ 收尾判据：数组以**独占一行**的 `)` 结束 —— 用「第一个含 `)` 的行」会
//
//	被数组元素里的括号（如 `grep -E '...(...)'`）提前截断。
func revisionSequenceArrayBlock(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "files=(" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s 里找不到 files=( 数组 —— 判据的形态写坏了（不是迁移漏登记）", revisionSequenceScriptPath)
	}
	for j := start + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == ")" {
			return strings.Join(lines[start:j+1], "\n")
		}
	}
	t.Fatalf("%s 里 files=( 数组没有收尾 —— 判据的形态写坏了", revisionSequenceScriptPath)
	return ""
}

// TestRevisionSequenceExemptionsHaveReasons —— 例外表必须逐条写明理由。
//
// ★ 「例外表」最常见的腐化形态：先加一行占位、理由以后再补 ⇒
//
//	而没有理由的例外＝缺口改名 ⇒ 门失效。
func TestRevisionSequenceExemptionsHaveReasons(t *testing.T) {
	for name, note := range revisionSequenceExemptions {
		if strings.TrimSpace(note) == "" {
			t.Errorf("例外 %s 没写理由 —— 没有理由的例外＝缺口改名，门会失效", name)
		}
		if !strings.HasSuffix(name, ".sql") {
			t.Errorf("例外键 %s 不是迁移文件名", name)
		}
		if _, err := os.Stat(filepath.Join(startupMigrationsDir, name)); err != nil {
			t.Errorf("例外 %s 在磁盘上不存在 —— 例外表里留了已退役文件的条目", name)
		}
	}
}
