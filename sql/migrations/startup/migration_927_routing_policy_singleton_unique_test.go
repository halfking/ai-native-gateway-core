package startup

// migration_927_routing_policy_singleton_unique_test.go —— 迁移 927 的静态契约门。
//
// # 这道门补的是哪块缺口
//
// ★ 927（routing_policy 单例化：去重 + UNIQUE (tenant_id)）由 12h 审计第 1 轮
// （fd3d243d5）落地时做了**五点同步**（embeddata 副本 / main.go go:embed +
// embeddedSQLFiles 映射 / dbinit StartupFiles / 部署通道 files=() / TSV），
// ★★ **唯独漏了第六面：docs/db-changelog.md 台账行** ⇒ ⇒ ⇒
// ★★ `internal/sqlguard` 的迁移连续性门（TestMigrationRegistryStaysContinuous）
// ★★ 判红 ⇒ ★★ 而那道门**只对近邻窗口断言**（窗口锚点 = 磁盘侧最大编号）
// ★★ ⇒ 927 恰在窗口内 ⇒ ⇒ ⇒ 这是**真红**，不是环境噪声。
//
// ★★★ **为什么这道门不是「把已有断言再抄一遍」**：★★★
// ★ 927 落地时**没有任何一道门钉住它的登记点** ⇒ ⇒ ⇒ ★ 924 号那类
// ★ 「五点同步漏一点 ⇒ 全新安装在926 处 file not found」（P0）的形态
// ★ 对 927 **仍然成立** ⇒ ⇒ ⇒ ★★ 只补台账行 = 只治症状；★ 把七处登记点
// ★★ 逐点钉住 = 治根（★ 892 号的教训：「只改文件」≠「生效」，登记点必须
// ★★ 逐处对齐，且每处都要有门钉住，否则缺口只能靠「恰好有人跑那个包的
// ★★ 测试」才暴露）。
//
// ★ 纪律来源：scripts/ursmcheck/section_14335_test.go 的
// TestSection14335RegistrationPoints（892 的七点登记门）——
// ★ 本门照它的形状写（**needle 恰好出现一次**的断言，而不是 Contains 型）。
//
// # 三类断言
//
//	① 登记面：七处登记点逐点在场，且**每处恰好一次**（canonical 与 embeddata
//	  两副本逐字节一致 / main.go 的 go:embed 指令 / main.go 的映射条目 /
//	  runner.go 的 StartupFiles / 部署通道 files=() / TSV 清单行 / db-changelog 台账行）。
//	② 台账面：台账里的 sha256必须是**文件实测值**（不是从别处抄）。
//	③ 语义面（防回退）：去重「不猜」+ UNIQUE (tenant_id) + **自带事务**
//	  三件事必须在文件里 —— 前两件是缺陷本体，第三件是 2026-10-31 同轮修的
//	  「SET LOCAL 曾落在事务块外 ⇒ 两个守卫实际都没生效」。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	mig927File     = "927_routing_policy_singleton_unique.sql"
	mig927DownFile = "927_routing_policy_singleton_unique.down.sql"

	// 927 在 installer 的启动迁移清单（sql/schema/installed_startup_migrations.tsv）
	// 里的序号位 —— ★ 台账行里写「第 327 位」是**读数**，本门把它钉成**断言**
	// （★ 读数会漂，断言不会）。
	mig927TSVSeq = "327"
)

// mig927Read 读仓内文件（相对本包目录）。
func mig927Read(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读 %s：%v", rel, err)
	}
	return string(raw)
}

// TestMigration927RegisteredEverywhere 钉住「七处登记点」。
//
// ★ 每处断言 **needle 恰好出现一次**：Contains 型门只保护「至少有一处」，
// ★ 而台账这类文件天然会多处提到同一迁移号（正文散文、让号记录……）⇒
// ★ needle 必须挑**只可能出现在登记位**的那一段。
func TestMigration927RegisteredEverywhere(t *testing.T) {
	canonical := mig927Read(t, mig927File)
	downCanonical := mig927Read(t, mig927DownFile)

	// ① embeddata 副本与 canonical **逐字节一致**。
	// ★ 迁移正文改一个字而副本没跟上 ⇒ 两条通道执行的**不是同一份字节**，
	// ★ 而台账行里的 sha 承诺的是「同一份」。
	embedUp := mig927Read(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig927File)
	if embedUp != canonical {
		t.Error("installer embeddata 副本（up）必须与 canonical 逐字节一致")
	}
	embedDown := mig927Read(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig927DownFile)
	if embedDown != downCanonical {
		t.Error("installer embeddata 副本（down）必须与 canonical 逐字节一致")
	}

	// ②～⑤ 四处注册点（main.go 两处 + runner.go 一处 + 部署通道一处）。
	installerGo := mig927Read(t, "../../../installer/cmd/llm-gw-installer/main.go")
	runnerGo := mig927Read(t, "../../../installer/internal/dbinit/runner.go")
	seqSh := mig927Read(t, "../../../scripts/apply-db-revision-sequence.sh")

	points := []struct {
		label  string
		body   string
		needle string
		why    string
	}{
		{"main.go 的 go:embed 指令", installerGo,
			"//go:embed embeddata/startup/" + mig927File,
			"全新装机编不出二进制（installer 不会内嵌这个文件）"},
		{"main.go 的 embeddedSQLFiles 映射", installerGo,
			`"startup/` + mig927File + `":`,
			"setupSQLDir 只遍历内存里的 embeddedSQLFiles map ⇒ 缺它 ⇒ 全新安装在 927 处 file not found"},
		{"runner.go 的 StartupFiles", runnerGo,
			`"` + mig927File + `"`,
			"新装库不会应用它（安装器的启动迁移清单缺它）"},
		{"部署通道 files=()", seqSh,
			`"$ROOT_DIR/sql/migrations/startup/` + mig927File + `"`,
			"升级库上会被静默跳过（脚本 warn-only + 部署侧只查文件存在）"},
		{"启动迁移清单 TSV", mig927Read(t, "../../../sql/schema/installed_startup_migrations.tsv"),
			mig927TSVSeq + "\t" + mig927File,
			"位置记录断档（installer 的 manifest 门会红，但那个包不在 ursmcheck 回归范围内）"},
		{"db-changelog 台账行", mig927Read(t, "../../../docs/db-changelog.md"),
			"| 927 | `" + mig927File + "` |",
			"sqlguard 的迁移连续性门判红且运维看不见"},
	}
	for _, p := range points {
		if n := strings.Count(p.body, p.needle); n != 1 {
			t.Errorf("%s：%s 里 %s 出现 %d 次（要求恰好 1 次）—— %s",
				p.label, p.label, p.needle, n, p.why)
		}
	}
}

// TestMigration927ChangelogShaIsMeasured 钉住「台账里的 sha 是文件实测值」。
//
// ★ 台账是**账本**：登记行可能与磁盘脱节 ⇒ 而部署通道的
// verify-migration-checksums.sh 会判 MISMATCH ⇒ CI Unified Verification 失败。
func TestMigration927ChangelogShaIsMeasured(t *testing.T) {
	canonical := mig927Read(t, mig927File)
	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])

	changelog := mig927Read(t, "../../../docs/db-changelog.md")
	if !strings.Contains(changelog, want) {
		t.Errorf("docs/db-changelog.md 里没有 927 当前的 sha256（%s）⇒ 登记册与磁盘不一致", want)
	}

	// down 的 sha 也登记在台账正文里（★ 与 903/904/905/906 等既有迁移同款）。
	down := mig927Read(t, mig927DownFile)
	downSum := sha256.Sum256([]byte(down))
	downWant := hex.EncodeToString(downSum[:])
	if !strings.Contains(changelog, downWant) {
		t.Errorf("docs/db-changelog.md 里没有 927 down 当前的 sha256（%s）⇒ 登记册与磁盘不一致", downWant)
	}
}

// TestMigration927SemanticShape 钉住「缺陷本体三件事」。
//
// ★ ① 去重**不猜**：内容不一致 ⇒ 报错、一行都不删 —— 「迁移替人选 = 悄悄改路由行为」。
// ★ ② UNIQUE (tenant_id)：让 02-seed.sql 的 ON CONFLICT DO NOTHING 第一次真正生效
// ★    （★ 约束建在 tenant_id 而非 id：id 被 CHECK (id = 1) 锁死）。
// ★ ③ **自带事务**：SET LOCAL 只在事务块内生效 ⇒ 文件必须 BEGIN; … COMMIT;
// ★    （★ 部署通道的 psql_stream 不加 --single-transaction ⇒ 不自带事务 ⇒
// ★    顶部的 lock_timeout / statement_timeout 两个守卫都是空文）。
func TestMigration927SemanticShape(t *testing.T) {
	body := mig927Read(t, mig927File)

	// ① 不猜：内容不一致 ⇒ 抛错。
	if !strings.Contains(body, "count(DISTINCT (to_jsonb(rp) - 'updated_at'))") {
		t.Error("去重必须用「去掉 updated_at 的整行内容」比较（★ updated_at 是易变时间戳，不是配置内容）")
	}
	if !strings.Contains(body, "RAISE EXCEPTION") {
		t.Error("内容不一致时必须 RAISE EXCEPTION（★ 报错让维护者裁决，一行都不删）")
	}
	if !strings.Contains(body, "k.ctid < rp.ctid") {
		t.Error("去重必须保留 ctid 最小（最早插入 ⇒ 调优历史最长）的那一行")
	}

	// ② 约束建在 tenant_id 上（★ 不是 id）。
	if !strings.Contains(body, "ADD CONSTRAINT routing_policy_tenant_id_key UNIQUE (tenant_id)") {
		t.Error("必须加 UNIQUE (tenant_id) 约束（★ 有了它，02-seed 的 ON CONFLICT DO NOTHING 才第一次真正生效）")
	}
	if strings.Contains(body, "UNIQUE (id)") {
		t.Error("约束不得建在 id 上（★ id 被 CHECK (id = 1) 锁死，tenant_id 才是真正的区分列）")
	}

	// ③ 自带事务（★ BEGIN; 独立成行 —— DO $$ BEGIN … END $$; 不是事务起点）。
	//
	// ★★★ **判据必须量「写在哪」而不是「有没有」**：★★★ ★ 本文件的修复说明
	// ★★★ （头部注释）里就写着「SET LOCAL」「BEGIN; … COMMIT;」这些字样 ⇒ ⇒ ⇒
	// ★★★ ⇒ **不剥注释就会把注释当成声明** ⇒ ⇒ ⇒ ★★ 那又是一道假绿
	// ★★★ ⇒ ★★ （★ 与 installer侧「SET LOCAL 有效性」门同款教训：
	// ★★★ ★ 既有门只判「有没有这个字符串」，★ ★ 本门判「写在事务块内没有」）。
	// ★★★ ⇒ 复用同包既有的 stripSQLComments（migration_602_test.go）——
	// ★★★ ★ 同一口径只能有一份实现。
	code := stripSQLComments(body)

	beginRe := regexp.MustCompile(`(?im)^[ \t]*BEGIN\s*;`)
	commitRe := regexp.MustCompile(`(?im)^[ \t]*COMMIT\s*;`)
	beginLoc := beginRe.FindStringIndex(code)
	commitLoc := commitRe.FindStringIndex(code)
	if beginLoc == nil {
		t.Error("文件必须自带独立的 `BEGIN;` 行（★ 否则 SET LOCAL 落在事务块外 ⇒ 两个守卫实际都没生效）")
	}
	if commitLoc == nil {
		t.Error("自带事务必须有独立的 `COMMIT;` 行（★ 否则三步各自独立提交 ⇒ 「行已删、约束未建」的半应用状态可能发生）")
	}
	if beginLoc != nil && commitLoc != nil {
		if beginLoc[0] > commitLoc[0] {
			t.Error("`COMMIT;` 出现在 `BEGIN;` 之前 ⇒ 事务块顺序反了")
		}
		// ★ 守卫必须在**事务块内**：两个 SET LOCAL 都落在 BEGIN 与 COMMIT 之间。
		for _, needle := range []string{
			"SET LOCAL lock_timeout = '2s'",
			"SET LOCAL statement_timeout = '30s'",
		} {
			idx := strings.Index(code, needle)
			if idx < 0 {
				t.Errorf("缺锁守卫 %s（★ ADD CONSTRAINT UNIQUE 要拿 ACCESS EXCLUSIVE 锁 ⇒ 可无限期等锁）", needle)
				continue
			}
			if idx < beginLoc[1] || idx > commitLoc[0] {
				t.Errorf("%s 落在事务块**外**（★ SET LOCAL 只在事务块内生效 ⇒ 事务块外只发一条 WARNING、值不生效 ⇒ 守卫是空文）", needle)
			}
		}
	}
}
