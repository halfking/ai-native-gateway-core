package startup

// migration_903_sessions_updated_at_idx_test.go —— 903 的契约门（R84 慢端点归因轮）。
//
// # 这道门守的是什么
//
// 903 给分区父表 public.sessions 补 (updated_at DESC, session_id DESC) 索引，
// 修的是 GET /api/admin/turns/sessions 生产恒 500@30s（2833 基线 REPEAT=5：
// 500,500,500,500,200，min 30.003s / med 30.013s / max 54.510s）。
//
// 索引类迁移最容易出的错有四种，本门各钉一条：
//
//	① **列序钉死**：本索引存在的**唯一**理由是提供
//	   `ORDER BY s.updated_at DESC, s.session_id DESC` 这个序。
//	   ★ 列序写成 (session_id, updated_at DESC) ⇒ 规划器仍然要多一次排序
//	   ⇒ 索引「在位但不起作用」⇒ 端点仍然是 32s，而**没有任何报错**。
//	   ⇒ 本门把「索引列序」与「Go 侧排序键」**绑在一起**断言：
//	   改了一边而没改另一边 ⇒ 门红。
//	② **幂等**：重跑必须跳过而不是报错（IF NOT EXISTS）。
//	③ **锁守卫**：编号 >870 的启动迁移必须自带 lock_timeout
//	   （startup_ddl_lock_guard_gate 的规矩）；本表是热写表
//	   （session_aggregator.go:216 每请求一次 upsert）⇒ 没守卫会在等锁期间堵住写。
//	④ **投递面**：只登记在 canonical 目录而没登记在 installer / 升级序列里
//	   ⇒ 生产库会静默跳过 ⇒ 「迁移写了但没生效」。
//
// 另外钉：两份副本逐字节一致、down 会把索引删回去、903 不含 CONCURRENTLY
// （⇒ 它跑在 installer's --single-transaction 通道里，不需要豁免表登记）。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真文件被判绿。
// 合成样本只在本门内部构造，不碰仓库里的真实迁移文件。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig903Name     = "903_sessions_updated_at_session_id_index.sql"
	mig903DownName = "903_sessions_updated_at_session_id_index.down.sql"

	mig903IndexName = "idx_sessions_updated_at_session_id"
	mig903Table     = "public.sessions"

	mig903UpSHA256   = "404503d920252f171d313c6235c28bbe93d5d7241104000371d41f9555349d48"
	mig903DownSHA256 = "fef2844f9b2ea0b835239cfd6da67198b9855df90eacb4e40f5b7caf108d673d"
)

func mig903Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig903Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig903StripComments 去行注释与块注释。
//
// ★ 必须剥：903 的文件头里有整段论证文字，其中就写着索引形态
// `(updated_at DESC, session_id DESC)` ⇒ 不剥注释的话，「文件里出现过
// 这个形态」这条断言会被注释里的字面量满足 ⇒ 恒绿门。
func mig903StripComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	return line.ReplaceAllString(s, " ")
}

// mig903CheckUpBody 校验 up 体形态，返回问题描述列表（空 = 合格）。
// ★ 抽成纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
func mig903CheckUpBody(clean string) []string {
	var problems []string

	// ① 索引形态：名字 + 表 + 列序 + 方向，逐字钉死。
	//
	// ★ 列序是**语义**而不是风格：(updated_at DESC, session_id DESC) 才能
	//   直接供 ORDER BY 用；写成 (session_id, updated_at DESC) 时索引仍然
	//   「在位」，但规划器必须额外排序 ⇒ 端点回到 32s 且**不报任何错**。
	idxRE := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
		regexp.QuoteMeta(mig903IndexName) + `\s+ON\s+` + regexp.QuoteMeta(mig903Table) +
		`\s*\(\s*updated_at\s+DESC\s*,\s*session_id\s+DESC\s*\)`)
	if !idxRE.MatchString(clean) {
		problems = append(problems, "缺索引定义（CREATE INDEX IF NOT EXISTS "+mig903IndexName+
			" ON "+mig903Table+" (updated_at DESC, session_id DESC)）—— 列序必须是 updated_at DESC 在前，否则索引在位但供不上 ORDER BY 的序")
	}

	// ② 幂等：IF NOT EXISTS 已在上面的正则里钉住；这里再钉一次「不得
	//    出现不带 IF NOT EXISTS 的同名创建」（否则重跑直接报错）。
	nonIdemRE := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+` + regexp.QuoteMeta(mig903IndexName))
	if nonIdemRE.MatchString(clean) {
		problems = append(problems, "存在不带 IF NOT EXISTS 的同名 CREATE INDEX —— 重跑会直接报错")
	}

	// ③ 不得出现 CONCURRENTLY：分区父表不支持 CREATE INDEX CONCURRENTLY
	//    ⇒ 带了就永远建不上（installer's --single-transaction 通道也会判死）。
	if regexp.MustCompile(`(?is)CREATE\s+INDEX\s+CONCURRENTLY`).MatchString(clean) {
		problems = append(problems, "含 CREATE INDEX CONCURRENTLY —— 分区父表不支持它（PG 会直接报错），且 installer's 单事务通道也判死")
	}

	// ④ 事务边界：必须有 BEGIN/COMMIT（与 891/901 同款形态）。
	if !regexp.MustCompile(`(?is)\bBEGIN\s*;`).MatchString(clean) {
		problems = append(problems, "缺 BEGIN —— 索引创建与锁超时的设置应在一个显式事务里")
	}
	if !regexp.MustCompile(`(?is)\bCOMMIT\s*;`).MatchString(clean) {
		problems = append(problems, "缺 COMMIT")
	}

	return problems
}

// ── ① up 体形态 ────────────────────────────────────────────────────────────

func TestMigration903_UpBodyShape(t *testing.T) {
	up := mig903Read(t, mig903Tree(t), filepath.Join("sql", "migrations", "startup", mig903Name))
	if problems := mig903CheckUpBody(mig903StripComments(up)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("903 up 体：%s", p)
		}
	}
}

// TestMigration903_DetectorRejectsBadShapes 阳性对照：四份坏样本都必须被判红。
//
// ★ 四份坏样本对应四种「索引在位但不起作用」的形态 —— 这正是本门要防的。
func TestMigration903_DetectorRejectsBadShapes(t *testing.T) {
	good := mig903StripComments(mig903Read(t, mig903Tree(t), filepath.Join("sql", "migrations", "startup", mig903Name)))
	if len(mig903CheckUpBody(good)) != 0 {
		t.Fatal("阳性对照失败：真文件被判为不合格 —— 检测器坏了")
	}

	// 坏样本①：列序颠倒（updated_at 与 session_id 换位）⇒ 索引供不上 ORDER BY 的序。
	bad1 := strings.Replace(good,
		"(updated_at DESC, session_id DESC)", "(session_id, updated_at DESC)", 1)
	if len(mig903CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（列序颠倒）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本②：方向丢失（updated_at DESC 写成 updated_at）⇒ 排序列方向不对。
	bad2 := strings.Replace(good,
		"(updated_at DESC, session_id DESC)", "(updated_at, session_id DESC)", 1)
	if len(mig903CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（updated_at 的 DESC 丢失）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本③：幂等守卫被摘（IF NOT EXISTS 没了）⇒ 重跑直接报错。
	bad3 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS "+mig903IndexName,
		"CREATE INDEX "+mig903IndexName, 1)
	if len(mig903CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（幂等守卫缺失）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本④：换成分区父表不支持的 CONCURRENTLY。
	bad4 := strings.Replace(good, "CREATE INDEX IF NOT EXISTS "+mig903IndexName,
		"CREATE INDEX CONCURRENTLY "+mig903IndexName, 1)
	if len(mig903CheckUpBody(bad4)) == 0 {
		t.Error("坏样本④（CONCURRENTLY）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② lock_timeout / statement_timeout（仓库规矩） ─────────────────────────

func TestMigration903_CarriesLockAndStatementTimeout(t *testing.T) {
	up := mig903Read(t, mig903Tree(t), filepath.Join("sql", "migrations", "startup", mig903Name))
	clean := strings.ToLower(mig903StripComments(up))

	if !strings.Contains(clean, "lock_timeout") {
		t.Error("903 的 up 体没有 lock_timeout —— 编号 >870 的启动迁移必须自带（startup_ddl_lock_guard_gate 规矩）")
	}
	lt := regexp.MustCompile(`(?i)lock_timeout\s*=\s*'(\d+)s'`)
	m := lt.FindStringSubmatch(up)
	if m == nil {
		t.Error("903 的 lock_timeout 不是 '<秒>s' 形态")
	} else if m[1] == "0" {
		t.Error("903 的 lock_timeout = 0s ⇒ 无限等待，与「快速失败」的语义相反")
	}

	// statement_timeout：共享 PG 服务端 statement_timeout 30s 会掐断长索引
	// 构建 ⇒ 迁移永不记账 ⇒ 永久卡点（891/901 同款理由）。
	if !strings.Contains(clean, "statement_timeout") {
		t.Error("903 的 up 体没有 statement_timeout —— 长索引构建会被共享 PG 的服务端超时掐断后永不记账")
	}
	st := regexp.MustCompile(`(?i)statement_timeout\s*=\s*'(\d+)min'`)
	sm := st.FindStringSubmatch(up)
	if sm == nil {
		t.Error("903 的 statement_timeout 不是 '<分钟>min' 形态")
	} else if sm[1] == "0" {
		t.Error("903 的 statement_timeout = 0min ⇒ 无限等待")
	}
}

// ── ③ down 把索引删回去 ───────────────────────────────────────────────────

func TestMigration903_DownDropsIndex(t *testing.T) {
	down := mig903StripComments(mig903Read(t, mig903Tree(t), filepath.Join("sql", "migrations", "startup", mig903DownName)))
	// ★ 形态要同时接受带/不带 schema 前缀的两种写法（public.idx_… / idx_…）
	//   —— 第一版只认不带前缀的那种 ⇒ 把**正确**的 down 判成不合格
	//   ⇒ 这就是「量具坏了」而不是「被测坏了」。
	dropRE := regexp.MustCompile(`(?is)DROP\s+INDEX\s+IF\s+EXISTS\s+(?:public\.)?` +
		regexp.QuoteMeta(mig903IndexName))
	if !dropRE.MatchString(down) {
		t.Error("903 down 未删 " + mig903IndexName + " —— 回滚后索引还在，慢查询形态不会回来")
	}
	if !strings.Contains(down, mig903IndexName) {
		t.Error("903 down 未提到 " + mig903IndexName)
	}
}

// ── ④ 两份副本逐字节一致 ──────────────────────────────────────────────────

func TestMigration903_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig903Tree(t)
	for _, name := range []string{mig903Name, mig903DownName} {
		canonical := mig903Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig903Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 与 installer embeddata 副本不一致 —— 只改一份会让装出来的库与仓库的库分叉", name)
		}
		sum := sha256.Sum256([]byte(canonical))
		got := hex.EncodeToString(sum[:])
		want := mig903UpSHA256
		if name == mig903DownName {
			want = mig903DownSHA256
		}
		if got != want {
			t.Errorf("%s 的 sha256 = %s 与台账登记的 %s 不符 —— 文件改动后必须同步 db-changelog", name, got, want)
		}
	}
}

// ── ⑤ 登记面（7 点） ──────────────────────────────────────────────────────

func TestMigration903_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig903Tree(t)

	runner := mig903Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig903Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 903 —— 新装链会跳过它")
	}

	main := mig903Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig903Name) {
		t.Error("main.go 缺 903 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, `"startup/`+mig903Name+`"`) {
		t.Error("main.go 的 embed map 未登记 903")
	}

	seq := mig903Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig903Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 903 —— 升级库会被静默跳过（投递通道缺口）")
	}

	tsv := mig903Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig903Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 903")
	}

	changelog := mig903Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 903 | `"+mig903Name+"`") {
		t.Error("docs/db-changelog.md 未登记 903")
	}
	// ★ 必须拿**文件实测**的 sha 去比对台账，而不是只查「台账里含某个 sha 串」。
	//   第一版只查 contains(常量) ⇒ 迁移文件被改（sha 变了）时台账照样绿
	//   ⇒ ⇒ 台账与文件分叉，而**没有任何门会红**。
	upBytes := mig903Read(t, root, filepath.Join("sql", "migrations", "startup", mig903Name))
	downBytes := mig903Read(t, root, filepath.Join("sql", "migrations", "startup", mig903DownName))
	upSum := sha256.Sum256([]byte(upBytes))
	downSum := sha256.Sum256([]byte(downBytes))
	if got := hex.EncodeToString(upSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("docs/db-changelog.md 里 903 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
	if got := hex.EncodeToString(downSum[:]); !strings.Contains(changelog, got) {
		t.Errorf("docs/db-changelog.md 里 903 down 的 sha256 与文件实测不符（实测 %s）—— 台账与文件已分叉", got)
	}
}

// ── ⑥ ★ 语义耦合门：索引列序必须与 Go 侧排序键一致 ───────────────────────
//
// ★ 这是本门**最有价值**的一条：它把「数据库里的索引形态」与「Go 侧查询的
//   排序键」绑在一起。
//
// 事故形态（正是它要防的）：有人为了别的理由改了 admin/turns_sessions.go
// 的 ORDER BY（比如改成 s.created_at DESC），**没有同步改索引** ⇒
// 索引仍在位、迁移仍在台账里、端点却悄悄回到 32s ⇒ 而**没有任何门会红**。
//
// 反向也要拦：有人改了索引列序却没改 ORDER BY ⇒ 同样是无声退化。

func TestMigration903_IndexColumnOrderMatchesQuerySortOrder(t *testing.T) {
	root := mig903Tree(t)

	// Go 侧排序键。
	goSrc := mig903Read(t, root, filepath.Join("admin", "turns_sessions.go"))
	sortRE := regexp.MustCompile(`ORDER\s+BY\s+s\.updated_at\s+DESC\s*,\s*s\.session_id\s+DESC`)
	if !sortRE.MatchString(goSrc) {
		t.Fatal("读不到 admin/turns_sessions.go 里的 ORDER BY s.updated_at DESC, s.session_id DESC —— " +
			"若排序键已改，本门的前提失效，请人工复核索引形态是否还要跟着改")
	}

	// 迁移侧索引列序。
	up := mig903StripComments(mig903Read(t, root, filepath.Join("sql", "migrations", "startup", mig903Name)))
	idxRE := regexp.MustCompile(`(?is)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` +
		regexp.QuoteMeta(mig903IndexName) + `\s+ON\s+` + regexp.QuoteMeta(mig903Table) +
		`\s*\(\s*updated_at\s+DESC\s*,\s*session_id\s+DESC\s*\)`)
	if !idxRE.MatchString(up) {
		t.Error("索引列序与 Go 侧 ORDER BY (updated_at DESC, session_id DESC) 不一致 —— " +
			"索引会「在位但供不上序」，端点会无声地退回 32s")
	}
}

// ── ⑦ 不得出现在单事务通道的豁免表里 ─────────────────────────────────────
//
// ★ 903 不含 CONCURRENTLY ⇒ 它跑在 installer's --single-transaction 通道里
//   ⇒ **不需要**在 startup_concurrency_gate_test.go 的豁免表登记。
//   若有人后来给它加了 CONCURRENTLY 却忘了改豁免表 ⇒ 全新安装会死在
//   「CREATE INDEX CONCURRENTLY cannot run inside a transaction block」。
//   ⇒ 本门钉住「903 不在豁免表里」，并在检测到 CONCURRENTLY 时立刻报红。

func TestMigration903_NotInConcurrencyExceptionTable(t *testing.T) {
	root := mig903Tree(t)

	// ★ 判定必须**剥掉注释**：903 的文件头散文里就写着
	//   「分区父表不支持 CREATE INDEX CONCURRENTLY」—— 第一版读原文，
	//   把这句**说明**当成了「文件里用了 CONCURRENTLY」⇒ 把正确的迁移判红
	//   ⇒ 同样是「量具坏了」。
	up := mig903StripComments(mig903Read(t, root, filepath.Join("sql", "migrations", "startup", mig903Name)))
	if strings.Contains(strings.ToUpper(up), "CONCURRENTLY") {
		t.Error("903 的可执行体出现了 CONCURRENTLY —— 分区父表不支持它（PG 会直接报错），" +
			"且必须同步改 startup_concurrency_gate_test.go 的豁免表")
	}

	conc := mig903Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "startup_concurrency_gate_test.go"))
	if strings.Contains(conc, `"`+mig903Name+`"`) {
		t.Error("903 不含 CONCURRENTLY，却登记在单事务通道的豁免表里 —— 豁免表只收真正需要非事务通道的文件")
	}
}
