package sqlguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 本文件是「迁移 971（pi_error_analysis_system）投递六点齐备性」的门。
//
// # 背景（2026-11-17 同步并行车 + 部署验证轮，本轮真实撞到）
//
//	并行车推来的 PI 智能体错误自动分析系统 Phase 1（0e62d88da）带了一个迁移：
//	`sql/migrations/domain/641_pi_error_analysis_system.sql`。
//
//	★ 取证结论（逐点量出来的，不是推测）：
//
//	① **`sql/migrations/domain/` 是死文件区**：该目录 95 个文件，**无一件**
//	   登记进 installer 的 StartupFiles / deploy 通道的 files=(...) 数组
//	   ⇒ 没有任何执行器会应用它们（与本仓 db/migrations/ README 的「冻结通道，
//	   无执行器接线」同型）。
//	② 本仓的两道投递门**都看不见 domain/**：
//	   - internal/sqlguard 的三通道覆盖门只扫 `sql/migrations/startup/`；
//	   - installer 的 embed 强制门只对 **≥849** 的 canonical 件要求 embed 副本。
//	   ⇒ 「没登记」这件事在 CI 里**不会变红** ⇒ 迁移可以静默地永不落地。
//	③ 原件还**非幂等**（9 个裸 `CREATE INDEX` + 裸 `CREATE TRIGGER`）：
//	   deploy 通道按内容指纹台账（public.gateway_db_revision_sequences）跳过
//	   已应用文件，但**内容一旦被加固（sha 变化）就会重放**
//	   ⇒ 重放时 42P07 / 42710 ⇒ 通道失败 ⇒ 部署 die（切流前）。
//
// # 本门钉住什么
//
//	① 971 的**六点位**逐点在位（canonical / embeddata 副本 / go:embed +
//	   var / embeddedSQLFiles map / dbinit StartupFiles / deploy 通道 files 数组
//	   + ledger_artifact_probe / tsv 台账）；
//	② 971 的 SQL **幂等**（索引 IF NOT EXISTS / 触发器先 DROP IF EXISTS /
//	   台账自登记 ON CONFLICT）；
//	③ PI 错误分析代码引用的表**确实由 971 建出来**（代码↔迁移锚定）；
//	④ **`sql/migrations/domain/` 不再接收新迁移**（≥641 一律红）——
//	   这是把上面那个逃逸口**焊死**的那一条。
const (
	pi971UpFile   = "971_pi_error_analysis_system.sql"
	pi971DownFile = "971_pi_error_analysis_system.down.sql"

	pi971CanonicalDir = "../../sql/migrations/startup"
	pi971EmbedDir     = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	pi971InstallerGo  = "../../installer/cmd/llm-gw-installer/main.go"
	pi971DbinitRunner = "../../installer/internal/dbinit/runner.go"
	pi971Tsv          = "../../sql/schema/installed_startup_migrations.tsv"
	pi971DomainDir    = "../../sql/migrations/domain"

	// pi971PIErrorCode 是 PI 错误分析的实现文件（代码↔迁移锚定用）。
	pi971PIErrorCode = "../../domains/promptinjection"

	// pi971DomainEscapeFloor 是「domain/ 目录里允许的最大编号」。
	//
	// ★ 收编 641 时把 domain/ 的最大编号从 641 降回 640 ⇒ 这条界正是
	//   「逃逸口关闭点」的刻度：**今后任何 ≥641 的迁移落进 domain/ 都会红**。
	pi971DomainEscapeFloor = 641
)

// pi971TableRe 抽取 PI 错误分析代码里出现的表名（error_ 前缀的裸标识符）。
var pi971TableRe = regexp.MustCompile(`\b(error_analysis_[a-z_]+)\b`)

func TestPi971DeliverySixPointsAreInPlace(t *testing.T) {
	// ── ① canonical 正典件在位 ───────────────────────────────────────────
	if _, err := os.ReadFile(filepath.Join(pi971CanonicalDir, pi971UpFile)); err != nil {
		t.Fatalf("读 canonical %s：%v", pi971UpFile, err)
	}
	if _, err := os.Stat(filepath.Join(pi971CanonicalDir, pi971DownFile)); err != nil {
		t.Errorf("canonical down 件 %s 不存在：%v", pi971DownFile, err)
	}

	// ── ② embeddata 副本逐字节相同（embed 副本漂移 = 849/850/851 那一族）──
	for _, name := range []string{pi971UpFile, pi971DownFile} {
		embed, err := os.ReadFile(filepath.Join(pi971EmbedDir, name))
		if err != nil {
			t.Errorf("embeddata 副本 %s 不存在：%v", name, err)
			continue
		}
		src, err := os.ReadFile(filepath.Join(pi971CanonicalDir, name))
		if err != nil {
			t.Errorf("读 canonical %s：%v", name, err)
			continue
		}
		if string(embed) != string(src) {
			t.Errorf("embeddata 漂移 %s：embeddata 与 sql/migrations/startup 逐字节不一致"+
				"（installer 会带着非正典 SQL 上门；849/850/851 那一族）", name)
		}
	}

	// ── ③ installer main.go：go:embed ×2 + embeddedSQLFiles map ×2 ─────
	installerGo, err := os.ReadFile(pi971InstallerGo)
	if err != nil {
		t.Fatalf("读 %s：%v", pi971InstallerGo, err)
	}
	installerText := string(installerGo)
	for _, name := range []string{pi971UpFile, pi971DownFile} {
		if !strings.Contains(installerText, "//go:embed embeddata/startup/"+name) {
			t.Errorf("installer main.go 缺 go:embed 指令：embeddata/startup/%s", name)
		}
		if !strings.Contains(installerText, `"startup/`+name+`"`) {
			t.Errorf("installer main.go 的 embeddedSQLFiles map 缺条目：%q", "startup/"+name)
		}
	}

	// ── ④ dbinit runner 的 StartupFiles ──────────────────────────────────
	runnerGo, err := os.ReadFile(pi971DbinitRunner)
	if err != nil {
		t.Fatalf("读 %s：%v", pi971DbinitRunner, err)
	}
	if !strings.Contains(string(runnerGo), `"`+pi971UpFile+`"`) {
		t.Errorf("dbinit runner 的 StartupFiles 缺 %q ⇒ 全新安装/升级不会应用这条迁移", pi971UpFile)
	}

	// ── ⑤ deploy 通道 files=(...) 数组 + ledger_artifact_probe ───────────
	seqRaw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	seqText := string(seqRaw)
	arrBlock := revisionSequenceArrayBlock(t, seqText)
	if !revisionSequenceEntryRe.MatchString(arrBlock) ||
		!strings.Contains(arrBlock, pi971UpFile) {
		t.Errorf("deploy 通道 files=(...) 数组缺 %q ⇒ 211 的 seamless 部署通道不会应用这条迁移"+
			"（962 那次断层的同型）", pi971UpFile)
	}
	if !strings.Contains(seqText, `ledger_artifact_probe "`+pi971UpFile+`"`) {
		t.Errorf("deploy 通道缺 ledger_artifact_probe %q ⇒ 「台账说已应用但产物不在场」"+
			"这一形态无人对账（2026-10-29 211 实测：912/913 台账有行、产物缺席）", pi971UpFile)
	}

	// ── ⑥ tsv 台账行 ─────────────────────────────────────────────────────
	tsvRaw, err := os.ReadFile(pi971Tsv)
	if err != nil {
		t.Fatalf("读 %s：%v", pi971Tsv, err)
	}
	foundTSV := false
	for _, line := range strings.Split(string(tsvRaw), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "\t"+pi971UpFile) ||
			strings.HasSuffix(strings.TrimSpace(line), " "+pi971UpFile) {
			foundTSV = true
			break
		}
	}
	if !foundTSV {
		t.Errorf("%s 缺 %q 的行 ⇒ 启动链的机器可读镜像与实际投递不同步",
			pi971Tsv, pi971UpFile)
	}
}

// TestPi971MigrationIsIdempotent —— 通道会按内容指纹**重放**已应用文件，
// 所以投递通道上的迁移**必须幂等**；否则一次内容加固就会让重放炸掉部署。
func TestPi971MigrationIsIdempotent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi971CanonicalDir, pi971UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi971UpFile, err)
	}
	lines := strings.Split(string(raw), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "CREATE INDEX") && !strings.Contains(upper, "IF NOT EXISTS") {
			t.Errorf("第 %d 行的 CREATE INDEX 缺 IF NOT EXISTS：%s\n"+
				"后果：通道按内容指纹重放本件时 42P07（relation already exists）⇒ 部署 die",
				i+1, trimmed)
		}
		if strings.HasPrefix(upper, "CREATE TRIGGER") {
			// 触发器必须先有 DROP TRIGGER IF EXISTS 兜底
			dropped := false
			for j := 0; j < i; j++ {
				d := strings.ToUpper(strings.TrimSpace(lines[j]))
				if strings.HasPrefix(d, "DROP TRIGGER IF EXISTS") {
					dropped = true
					break
				}
			}
			if !dropped {
				t.Errorf("第 %d 行的 CREATE TRIGGER 之前没有 DROP TRIGGER IF EXISTS：%s\n"+
					"后果：通道重放本件时 42710（trigger already exists）⇒ 部署 die",
					i+1, trimmed)
			}
		}
	}

	// 台账自登记（≥952 纪律）：缺了 ⇒ 「账本说没做」⇒ 后续按账本判断的人踩空。
	text := string(raw)
	if !strings.Contains(text, "schema_migrations") {
		t.Errorf("%s 没有 schema_migrations 自登记行 ⇒ 211 的记账链在这里断层", pi971UpFile)
	}
	if !strings.Contains(text, "VALUES ('971'") {
		t.Errorf("%s 的自登记行里没有 '971' 这个版本号", pi971UpFile)
	}
	if !strings.Contains(strings.ToUpper(text), "ON CONFLICT") {
		t.Errorf("%s 的自登记行没有 ON CONFLICT ⇒ 重放时唯一键冲突", pi971UpFile)
	}
}

// TestPi971CreatesEveryTableThePICodeUses —— 代码↔迁移锚定。
//
// ★ 为什么要有这条：迁移建了表但代码没查，或代码查了表但迁移没建，
//   **两边各自单测都绿** ⇒ 只有交叉对拍能抓住。
func TestPi971CreatesEveryTableThePICodeUses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi971CanonicalDir, pi971UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi971UpFile, err)
	}
	sqlText := string(raw)

	files, err := os.ReadDir(pi971PIErrorCode)
	if err != nil {
		t.Fatalf("读 %s：%v", pi971PIErrorCode, err)
	}
	seen := map[string]bool{}
	for _, e := range files {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(pi971PIErrorCode, e.Name()))
		if err != nil {
			t.Fatalf("读 %s：%v", e.Name(), err)
		}
		for _, m := range pi971TableRe.FindAllString(string(body), -1) {
			seen[m] = true
		}
	}
	if len(seen) == 0 {
		t.Fatalf("在 %s 里一个 error_analysis_* 表名都没扫到 —— 锚点判据失效（代码形态变了？）",
			pi971PIErrorCode)
	}
	for table := range seen {
		if !strings.Contains(sqlText, "CREATE TABLE IF NOT EXISTS public."+table) {
			t.Errorf("PI 错误分析代码引用了表 %s，但 %s 没有建它 ⇒ 代码一跑就是 42P01",
				table, pi971UpFile)
		}
	}
}

// TestDomainMigrationDirRejectsNewMigrations —— 把逃逸口**焊死**。
//
// ★ 事故形态（2026-11-17 实测）：迁移 641 落在 sql/migrations/domain/ ⇒
//   ① 没有任何执行器会应用它（死文件区）；② 本仓两道投递门都扫不到它
//   （三通道覆盖门只扫 startup/；embed 强制门只管 ≥849）；
//   ⇒ 「没登记」在 CI 里**不会变红** ⇒ 迁移静默永不落地。
//
// ⇒ 这条门让「把新迁移放进 domain/」这个动作**当场变红**，而不是等到
//   某天有人在生产上发现表不存在。
func TestDomainMigrationDirRejectsNewMigrations(t *testing.T) {
	entries, err := os.ReadDir(pi971DomainDir)
	if err != nil {
		if os.IsNotExist(err) {
			return // 目录若已被整体删除，这条门无事可做（不是静默失效）
		}
		t.Fatalf("读 %s：%v", pi971DomainDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, ok := migrationNumberFromName(e.Name())
		if !ok {
			continue
		}
		if num >= pi971DomainEscapeFloor {
			t.Errorf("sql/migrations/domain/%s（编号 %d ≥ %d）落在**死文件区** ⇒ "+
				"没有任何执行器会应用它（installer StartupFiles / deploy 通道 files 数组都不含它），"+
				"且本仓两道投递门都扫不到该目录 ⇒ 「没登记」不会变红 ⇒ 迁移静默永不落地。\n"+
				"★ 正典通道是 sql/migrations/startup/；本仓的 db/migrations/README 同样以"+
				"「冻结通道，禁止新增」的口径处置同类目录。\n"+
				"修法：把该迁移搬进 sql/migrations/startup/，并完成六点同步"+
				"（canonical / embeddata 副本 / go:embed + var / embeddedSQLFiles map /"+
				" dbinit StartupFiles / deploy 通道 files 数组 + ledger_artifact_probe / tsv 台账）。",
				e.Name(), num, pi971DomainEscapeFloor)
		}
	}
}

// migrationNumberFromName 解析 "<num>_<name>.sql" 的编号前缀。
func migrationNumberFromName(name string) (int, bool) {
	idx := strings.Index(name, "_")
	if idx <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, false
	}
	return n, true
}
