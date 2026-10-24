package startup

// migration_908_route_incident_type_created_drop_test.go —— 908 的契约门
// （★ 取号沿革：原号 903 → 与并行会话 903_sessions_updated_at_session_id_index 撞号让号 905
//   → 与上游 905_turns_session_model_index 再次撞号，2026-10-25 再让号为 908；判决内容零变化）
// （R147 存储效率续轮，㉘ 代码面核对后的裁决落地）。
//
// # 这道门守的是什么
//
// 908 删除 route_incident_events 的 idx_route_incident_events_type_created
// (event_type, created_at DESC)——三低扫描索引里唯一可删的一个（另两个各有
// 不可替代消费方：incident_created 服务 incident 维度事件枚举；created_at
// 是 retention worker hasTimeIndex 探针的硬前置）。判决链见
// docs/audit/2026-10-24-R147-*.md §2。
//
// 删索引类迁移最容易出的错是**「删了被需要的东西」或「删了又被复活」**：
//	① 不带 CONCURRENTLY ⇒ 活写表上普通 DROP 的锁形态阻断 failure_observed 写入；
//	② 不带 IF EXISTS ⇒ 幂等性缺失，重跑必红；
//	③ db/db.go 的 ensure 链若仍含同名 CREATE ⇒ 每次启动把刚删的索引复活
//	  （718 的三项核实里「无 Go ensure 创建者」在本表不成立，故必须配套拆除）；
//	④ baseline 被顺手清掉 ⇒ fresh-install 链的「建出→908 删掉」次序被破坏
//	  （718 先例：baseline 是时点快照，不做回收站）。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配**阳性对照**（合成坏样本喂同一把尺子，必须判红）；
// 台账断言用**存在性**口径（≥1 行，LESSONS §302：台账多行是修订/再投递的
// 正式语义，不做行数形状断言）。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	mig908Name     = "908_route_incident_events_type_created_drop.sql"
	mig908DownName = "908_route_incident_events_type_created_drop.down.sql"

	mig908Idx      = "idx_route_incident_events_type_created"
	mig908Table    = "route_incident_events"
	mig908UpSHA256 = "bcd401ec78150ecb544e85966b95c75edbe81943669beb45c2f2fe8440490b15"
	// 与 db-changelog.md 台账行一致；down 体同步钉。
	mig908DownSHA256 = "4533a81fbeab4b58a56b53aa1dbddbe441d75eae7c38a6c6059c73d256fe8fdf"
)

func mig908Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig908Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func mig908StripComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	return line.ReplaceAllString(s, " ")
}

// mig908CheckUpBody 校验 up 体形态，返回问题描述列表（空 = 合格）。
// ★ 纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
func mig908CheckUpBody(clean string) []string {
	var problems []string

	// ① 删除语句本体：CONCURRENTLY + IF EXISTS + 带模式限定。
	dropRE := regexp.MustCompile(`(?i)DROP\s+INDEX\s+CONCURRENTLY\s+IF\s+EXISTS\s+public\.` + regexp.QuoteMeta(mig908Idx))
	if !dropRE.MatchString(clean) {
		problems = append(problems, "缺 DROP INDEX CONCURRENTLY IF EXISTS public."+mig908Idx+"（不带 CONCURRENTLY 会在活写表上阻断 failure_observed 写入；不带 IF EXISTS 则幂等性缺失）")
	}

	// ② 删除是本文件唯一的目录变更 —— 不能夹带其它 DROP/CREATE。
	if got := len(regexp.MustCompile(`(?i)\bDROP\b`).FindAllString(clean, -1)); got != 1 {
		problems = append(problems, "up 体的 DROP 语句数 = "+strconv.Itoa(got)+"，应为恰 1 条（夹带其它变更会让判决链失真）")
	}

	// ③ 非事务通道纪律：会话级 SET + RESET（SET LOCAL 在非事务通道不生效）。
	for _, kw := range []string{"SET lock_timeout", "SET statement_timeout", "RESET statement_timeout", "RESET lock_timeout"} {
		if !strings.Contains(clean, kw) {
			problems = append(problems, "缺 "+kw+"（非事务通道必须会话级 SET 并 RESET，871 头注同款）")
		}
	}
	return problems
}

// ── ① up 体形态 + 阳性对照 ─────────────────────────────────────────────────

func TestMigration908_UpBodyShape(t *testing.T) {
	up := mig908Read(t, mig908Tree(t), filepath.Join("sql", "migrations", "startup", mig908Name))
	if problems := mig908CheckUpBody(mig908StripComments(up)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("908 up 体：%s", p)
		}
	}
	if !strings.Contains(up, "dbinit:no-transaction") {
		t.Error("908 up 体缺 dbinit:no-transaction 标记 —— 全新安装会死在 DROP INDEX CONCURRENTLY inside transaction block")
	}
}

func TestMigration908_DetectorRejectsBadShapes(t *testing.T) {
	good := mig908StripComments(mig908Read(t, mig908Tree(t), filepath.Join("sql", "migrations", "startup", mig908Name)))
	if len(mig908CheckUpBody(good)) != 0 {
		t.Fatal("阳性对照失败：真文件被判为不合格 —— 检测器坏了")
	}

	// 坏样本 ①：去掉 CONCURRENTLY（普通 DROP 的锁形态阻断活写表）。
	bad1 := strings.Replace(good, "DROP INDEX CONCURRENTLY IF EXISTS", "DROP INDEX IF EXISTS", 1)
	if len(mig908CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（非 CONCURRENTLY 形态）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本 ②：去掉 IF EXISTS（幂等性缺失）。
	bad2 := strings.Replace(good, "DROP INDEX CONCURRENTLY IF EXISTS", "DROP INDEX CONCURRENTLY", 1)
	if len(mig908CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（无 IF EXISTS）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本 ③：抽掉 RESET（非事务通道的会话级 SET 泄漏给同连接后续语句）。
	// ★ 直接删除而不是注释化：检测器收到的已是剥过注释的文本，
	//   注释化的样本会被 Contains 子串匹配漏过（本门自踩，同 §302 族的
	//   「断言文本前先剥注释」纪律的反向应用）。
	bad3 := strings.Replace(good, "RESET statement_timeout;", "", 1)
	if len(mig908CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（RESET 被抽掉）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② down 恢复同形索引 ────────────────────────────────────────────────────

func TestMigration908_DownRestoresSameShape(t *testing.T) {
	down := mig908StripComments(mig908Read(t, mig908Tree(t), filepath.Join("sql", "migrations", "startup", mig908DownName)))
	re := regexp.MustCompile(`(?i)CREATE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+` + regexp.QuoteMeta(mig908Idx) +
		`\s+ON\s+public\.` + mig908Table + `\s*\(\s*event_type\s*,\s*created_at\s+DESC\s*\)`)
	if !re.MatchString(down) {
		t.Error("908 down 未按同形重建 " + mig908Idx + " (event_type, created_at DESC) —— 回退路径必须是逐字同形")
	}
	if !strings.Contains(down, "SET lock_timeout") || !strings.Contains(down, "RESET lock_timeout") {
		t.Error("908 down 缺会话级 lock_timeout 纪律（SET + RESET）")
	}
}

// ── ③ 两份副本逐字节一致 + sha 钉 ─────────────────────────────────────────

func TestMigration908_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig908Tree(t)
	for name, want := range map[string]string{
		mig908Name:     mig908UpSHA256,
		mig908DownName: mig908DownSHA256,
	} {
		canonical := mig908Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig908Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 与 installer embeddata 副本不一致 —— 只改一份会让装出来的库与仓库的库分叉", name)
		}
		sum := sha256.Sum256([]byte(canonical))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s 的 sha256 = %s 与台账不符 —— 文件被改动后必须同步 db-changelog/TSV/门常量", name, got)
		}
	}
}

// ── ④ 登记面（8 点） ──────────────────────────────────────────────────────

func TestMigration908_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig908Tree(t)

	runner := mig908Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig908Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 908 —— 新装链会跳过它")
	}

	main := mig908Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig908Name) {
		t.Error("main.go 缺 908 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, `"startup/`+mig908Name+`"`) {
		t.Error("main.go 的 embed map 未登记 908")
	}

	seq := mig908Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig908Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 908 —— 升级库会被静默跳过（投递通道缺口）")
	}

	tsv := mig908Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig908Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 908")
	}

	// ★ 台账用**存在性**口径（LESSONS §302：台账多行是修订/再投递的正式语义）。
	changelog := mig908Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 908 | `"+mig908Name+"`") {
		t.Error("docs/db-changelog.md 未登记 908")
	}
	if !strings.Contains(changelog, mig908UpSHA256) {
		t.Error("docs/db-changelog.md 里 908 的 sha256 与文件实测不符")
	}

	conc := mig908Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "startup_concurrency_gate_test.go"))
	if !strings.Contains(conc, `"`+mig908Name+`":`) {
		t.Error("startup_concurrency_gate_test.go 的豁免/计数表未登记 908 —— CONCURRENTLY 会被单事务通道判死")
	}
}

// ── ⑤ 配套不变式：ensure 链不复活；baseline 保留 ──────────────────────────

// TestMigration908_GoEnsureChainDoesNotResurrect：db/db.go 的
// ensureRouteIncidentSchema 若仍含同名 CREATE INDEX IF NOT EXISTS，产品每次
// 启动都会把 908 刚删的索引原地复活（718 三项核实的「无 Go ensure 创建者」
// 在本表不成立，靠本腿把「配套拆除」钉住）。
func TestMigration908_GoEnsureChainDoesNotResurrect(t *testing.T) {
	root := mig908Tree(t)
	for _, rel := range []string{
		filepath.Join("db", "db.go"),
		filepath.Join("db", "db_omnifree.go"),
	} {
		src := mig908Read(t, root, rel)
		createRE := regexp.MustCompile(`(?i)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+` + regexp.QuoteMeta(mig908Idx))
		if createRE.MatchString(src) {
			t.Errorf("%s 仍会创建 %s —— ensure 链会复活 908 刚删的索引", rel, mig908Idx)
		}
	}
}

// TestMigration908_BaselineKeepsIndexUntilChainDropsIt：baseline（两份快照）
// 刻意保留该索引 —— fresh-install 链的次序是「baseline 建出 → 启动迁移 908
// 删掉」（718 先例）。若有人只删 baseline 不动 908，或反之，本腿会提醒
// 两半必须一起看。
func TestMigration908_BaselineKeepsIndexUntilChainDropsIt(t *testing.T) {
	root := mig908Tree(t)
	for _, rel := range []string{
		filepath.Join("sql", "schema", "01-schema.sql"),
		filepath.Join("deploy", "sql", "schemas", "baseline", "01-schema.sql"),
	} {
		baseline := mig908Read(t, root, rel)
		if !strings.Contains(baseline, "CREATE INDEX "+mig908Idx+" ") &&
			!strings.Contains(baseline, "CREATE INDEX IF NOT EXISTS "+mig908Idx) {
			t.Errorf("%s 不再包含 %s —— baseline 与 908 的「建出→删掉」次序被破坏（若有意退役 baseline 行，请同步更新本门与 908 头注）", rel, mig908Idx)
		}
	}
}
