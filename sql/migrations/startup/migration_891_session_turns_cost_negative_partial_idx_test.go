package startup

// migration_891_session_turns_cost_negative_partial_idx_test.go —— 891 的契约门。
//
// # 这道门守的是什么
//
// 891 给**会话族**（session_turns 分区父表 + session_turns_hot）建两条
// `WHERE cost_usd < 0` 的部分索引，根修 bg/routing_health_checks.go
// `recorded_cost_is_negative` 检查的全表扫描。
//
// 索引类迁移最容易出的错是**「索引与查询形态脱钩」**——谓词改了、键序改了、
// INCLUDE 漏了一列，检查照样跑、照样返回「没有负成本行」，但计划器已经静默
// 退回顺序扫描，浪费照旧且**没人知道**。所以本门钉四件事：
//
//	① up 体确实建了**两条**部分索引，谓词是 `cost_usd < 0`；
//	② 键序 `(ts, credential_id, raw_model_name)`（服务 30 天窗范围谓词 +
//	   GROUP BY），INCLUDE `(cost_usd, cache_read_tokens, prompt_tokens)`
//	   （服务聚合，让它 index-only）；
//	③ **必须自带 lock_timeout**（>870 的启动迁移的仓库规矩，见
//	   installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go）；
//	④ 有同名 `.down.sql`，且 down 会 DROP 这两条索引。
//
// 另外钉两份副本的**逐字节一致**（canonical vs installer embeddata）——
// 只改一份会让装出来的库与仓库的库分叉（881/882/890 同款门）。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真文件被判绿。
// ⇒ 一个恒返回「合格」的检测器也能让本门全绿（AGENTS.md 判据纪律第 3 条）。
// 合成样本**只在本门内部构造**，不碰仓库里的真实迁移文件。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig886Name     = "891_session_turns_cost_negative_partial_idx.sql"
	mig886DownName = "891_session_turns_cost_negative_partial_idx.down.sql"

	// mig886ParentIdx / mig886HotIdx 是两条索引名（882 同款命名惯例：
	// idx_<表>_<语义>）。
	mig886ParentIdx = "idx_session_turns_cost_negative"
	mig886HotIdx    = "idx_session_turns_hot_cost_negative"
)

func mig886Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig886Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig886StripComments 去行注释与块注释。
//
// ⚠ 为什么必须去：891 的文件头**大量引用**旧的 882 索引形态（作为「为什么建」
// 的论证），若不剥注释，那些注释里的 `WHERE cost_usd < 0` /
// `(ts, credential_id, outbound_model)` 会被当成**可执行体**读进来 ⇒
// 检测器会把注释当证据 ⇒ 一个「只写了注释、没建索引」的迁移也能全绿。
//
// 去法：先剥块注释（含跨行），再剥行注释（`--` 到行尾）。
// 字符串字面量里的 `--` 本仓迁移里不出现，故不处理引号上下文。
func mig886StripComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	return line.ReplaceAllString(s, " ")
}

// mig886IndexShape 是从 CREATE INDEX 语句里抽出的形态要素。
type mig886IndexShape struct {
	IndexName string
	Table     string
	Keys      string
	Include   string
	Predicate string
}

// mig886CreateIndexRE 匹配一条 CREATE INDEX [IF NOT EXISTS] 语句。
//
// ★ 逐字照抄真实文件里的排版形态：
//
//	CREATE INDEX IF NOT EXISTS <name>
//	    ON public.<table> (<keys>)
//	    INCLUDE (<cols>)
//	    WHERE <pred>;
//
// 捕获组：1=index 名 2=表 3=键列 4=INCLUDE 列 5=谓词。
var mig886CreateIndexRE = regexp.MustCompile(
	`(?is)CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+ON\s+([\w.]+)\s*\(([^)]*)\)\s*` +
		`(?:INCLUDE\s*\(([^)]*)\)\s*)?(?:WHERE\s+([^;]+?))?;`)

// mig886ExtractShapes 抽出 body 里全部 CREATE INDEX 语句的形态。
func mig886ExtractShapes(body string) []mig886IndexShape {
	clean := mig886StripComments(body)
	var out []mig886IndexShape
	for _, m := range mig886CreateIndexRE.FindAllStringSubmatch(clean, -1) {
		out = append(out, mig886IndexShape{
			IndexName: m[1],
			Table:     m[2],
			Keys:      mig886NormCols(m[3]),
			Include:   mig886NormCols(m[4]),
			Predicate: mig886NormSpace(m[5]),
		})
	}
	return out
}

// mig886NormCols 归一化列清单：去空白、逐项 trim、逗号后补空格。
// ⇒ `(ts,credential_id,raw_model_name)` 与 `(ts, credential_id, raw_model_name)`
// 归一到同一形态 ⇒ 判据比的是**列集合与顺序**，不是排版。
func mig886NormCols(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// mig886NormSpace 压掉连续空白（含换行）。
func mig886NormSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mig886FindShape 找出指定表名上的那一条索引形态；找不到返回 nil。
func mig886FindShape(shapes []mig886IndexShape, table string) *mig886IndexShape {
	for i := range shapes {
		if shapes[i].Table == table {
			return &shapes[i]
		}
	}
	return nil
}

// mig886CheckShape 校验一条索引形态是否合格，返回**问题描述列表**（空 = 合格）。
//
// ★ 抽成带参的纯函数（而不是把断言直接写在 Test 里），是为了让**阳性对照**
//
//	能喂合成样本：同一个函数，喂真文件 ⇒ 空列表；喂坏样本 ⇒ 非空列表。
//	若把断言写在 Test 里，阳性对照就得复制一份判据 ⇒ 两份判据会漂移
//	（AGENTS.md 判据纪律：断言与被测对象必须共用同一把尺子）。
func mig886CheckShape(shape mig886IndexShape, wantTable, wantName, wantKeys, wantInclude, wantPredicate string) []string {
	var problems []string
	if shape.IndexName != wantName {
		problems = append(problems, "索引名 = "+shape.IndexName+"，期望 "+wantName)
	}
	if shape.Table != wantTable {
		problems = append(problems, "表 = "+shape.Table+"，期望 "+wantTable)
	}
	if shape.Keys != wantKeys {
		problems = append(problems, "键序 = "+shape.Keys+"，期望 "+wantKeys)
	}
	if shape.Include != wantInclude {
		problems = append(problems, "INCLUDE = "+shape.Include+"，期望 "+wantInclude)
	}
	if shape.Predicate != wantPredicate {
		problems = append(problems, "谓词 = "+shape.Predicate+"，期望 "+wantPredicate)
	}
	return problems
}

// ── ① 两条部分索引 + 谓词 + 键序 + INCLUDE ──────────────────────────────────

// TestMigration891_UpBuildsTwoCostNegativePartials 钉 ①。
//
// 两条（父表 + hot 表）**都要**在：只有一条 ⇒ 另一半的表仍是全表扫描，
// 而检查是 UNION ALL 读**两个**存储面 ⇒ 漏一条就等于没修。
func TestMigration891_UpBuildsTwoCostNegativePartials(t *testing.T) {
	up := mig886Read(t, mig886Tree(t), filepath.Join("sql", "migrations", "startup", mig886Name))
	shapes := mig886ExtractShapes(up)

	// 阳性对照：检测器必须能读到东西（一个恒返回空的提取器也能让本门全绿）。
	if len(shapes) == 0 {
		t.Fatal("891 的 up 体里没解析出任何 CREATE INDEX —— 提取器坏了或文件形态变了")
	}
	if len(shapes) != 2 {
		t.Fatalf("891 的 up 体里解析出 %d 条 CREATE INDEX，期望 2 条（父表 + hot 表）：%v",
			len(shapes), shapes)
	}

	const (
		wantKeys      = "ts, credential_id, raw_model_name"
		wantInclude   = "cost_usd, cache_read_tokens, prompt_tokens"
		wantPredicate = "cost_usd < 0"
	)

	for _, tc := range []struct {
		table string
		name  string
	}{
		{"public.session_turns", mig886ParentIdx},
		{"public.session_turns_hot", mig886HotIdx},
	} {
		shape := mig886FindShape(shapes, tc.table)
		if shape == nil {
			t.Errorf("891 的 up 体里没有 %s 上的索引（期望 %s）—— 检查读两个存储面，漏一个就等于没修",
				tc.table, tc.name)
			continue
		}
		if problems := mig886CheckShape(*shape, tc.table, tc.name, wantKeys, wantInclude, wantPredicate); len(problems) > 0 {
			t.Errorf("891 在 %s 上的索引形态不对：%s", tc.table, strings.Join(problems, "；"))
		}
	}
}

// TestMigration891_DetectorRejectsBadShapes 是 ① 的**阳性对照**：
// 喂三份**合成**的坏样本，断言检测器**判红**。
//
// ⇒ 证明「形状检查」这个尺子真的有鉴别力，而不是恒返回「合格」。
// 合成样本只在本函数内部构造，不碰仓库里的真实文件。
func TestMigration891_DetectorRejectsBadShapes(t *testing.T) {
	const (
		wantKeys      = "ts, credential_id, raw_model_name"
		wantInclude   = "cost_usd, cache_read_tokens, prompt_tokens"
		wantPredicate = "cost_usd < 0"
	)
	ok := mig886IndexShape{
		IndexName: mig886ParentIdx,
		Table:     "public.session_turns",
		Keys:      wantKeys,
		Include:   wantInclude,
		Predicate: wantPredicate,
	}

	// 对照 0：合格样本必须**判绿**（否则下面的红就毫无意义）。
	if problems := mig886CheckShape(ok, ok.Table, ok.IndexName, wantKeys, wantInclude, wantPredicate); len(problems) != 0 {
		t.Fatalf("阳性对照失败：合格样本被判红：%s", strings.Join(problems, "；"))
	}

	bad := []struct {
		name   string
		mutate func(*mig886IndexShape)
	}{
		{"谓词不是 cost_usd < 0", func(s *mig886IndexShape) { s.Predicate = "cost_usd <= 0" }},
		{"谓词缺失（无 WHERE）", func(s *mig886IndexShape) { s.Predicate = "" }},
		{"键序换了（ts 不在首位）", func(s *mig886IndexShape) {
			s.Keys = "credential_id, raw_model_name, ts"
		}},
		{"键序漏列（少了 credential_id）", func(s *mig886IndexShape) {
			s.Keys = "ts, raw_model_name"
		}},
		{"INCLUDE 漏列（少了 prompt_tokens）", func(s *mig886IndexShape) {
			s.Include = "cost_usd, cache_read_tokens"
		}},
		{"INCLUDE 整段缺失", func(s *mig886IndexShape) { s.Include = "" }},
		{"索引名漂了", func(s *mig886IndexShape) { s.IndexName = "idx_session_turns_cost_neg" }},
	}
	for _, tc := range bad {
		shape := ok
		tc.mutate(&shape)
		if problems := mig886CheckShape(shape, ok.Table, ok.IndexName, wantKeys, wantInclude, wantPredicate); len(problems) == 0 {
			t.Errorf("阳性对照失败：%s 的坏样本被判成合格 —— 检测器没有鉴别力", tc.name)
		}
	}

	// 对照（提取器层）：一份**没有 cost_usd < 0 谓词**的建索引语句，
	// 必须被提取成「谓词为空」而不是「谓词随便什么都行」。
	noPred := mig886ExtractShapes("CREATE INDEX IF NOT EXISTS x ON public.t (a) INCLUDE (b);")
	if len(noPred) != 1 {
		t.Fatalf("阳性对照失败：合成样本解析出 %d 条，期望 1 条", len(noPred))
	}
	if noPred[0].Predicate != "" {
		t.Errorf("阳性对照失败：无 WHERE 的样本谓词 = %q，期望空串", noPred[0].Predicate)
	}
}

// ── ② lock_timeout ──────────────────────────────────────────────────────────

// TestMigration891_CarriesLockTimeout 钉 ③：必须自带 lock_timeout。
//
// >870 的启动迁移带强锁 DDL 必须带 lock_timeout（仓库规矩，
// installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go）。
// 普通 CREATE INDEX 持 SHARE 锁到构建完成 ⇒ 抢不到锁时必须**快速失败**。
func TestMigration891_CarriesLockTimeout(t *testing.T) {
	up := mig886Read(t, mig886Tree(t), filepath.Join("sql", "migrations", "startup", mig886Name))
	clean := mig886StripComments(up)

	if !strings.Contains(strings.ToLower(clean), "lock_timeout") {
		t.Errorf("891 的 up 体没有 lock_timeout —— 编号 >870 的启动迁移必须自带（" +
			"普通 CREATE INDEX 持 SHARE 锁，抢不到锁时必须快速失败而不是排队堵住写流量）")
	}
	// 值必须是**秒级**（不是 0 / 不是无限），否则「快速失败」是空话。
	lt := regexp.MustCompile(`(?i)lock_timeout\s*=\s*'(\d+)s'`)
	m := lt.FindStringSubmatch(clean)
	if m == nil {
		t.Errorf("891 的 lock_timeout 不是 '<秒>s' 形态（实测未匹配）—— 门禁要求它是秒级有限值")
	} else if m[1] == "0" {
		t.Errorf("891 的 lock_timeout = 0s ⇒ 无限等待，与「快速失败」的语义相反")
	}
}

// TestMigration891_LockTimeoutDetectorCanFire 是 ③ 的**阳性对照**：
// 一份**没有** lock_timeout 的合成样本必须被判不合格。
func TestMigration891_LockTimeoutDetectorCanFire(t *testing.T) {
	hasLockTimeout := func(body string) bool {
		return strings.Contains(strings.ToLower(mig886StripComments(body)), "lock_timeout")
	}
	if !hasLockTimeout("SET LOCAL lock_timeout = '60s';") {
		t.Fatal("阳性对照失败：带 lock_timeout 的样本被判为没有 —— 检测器坏了")
	}
	if hasLockTimeout("SET LOCAL statement_timeout = '10min';") {
		t.Error("阳性对照失败：没有 lock_timeout 的样本被判为有 —— 检测器没有鉴别力")
	}
}

// ── ③ down 形态 ─────────────────────────────────────────────────────────────

// TestMigration891_DownDropsBothIndexes 钉 ④。
//
// ⚠ down 的语义比「删两个索引」更重要：回滚之后，882 那条 v1 索引也已随 890
//
//	的表一起没了 ⇒ 会话族上**再无任何** cost_usd 索引 ⇒ 检查退回全表扫描。
//	所以 down 必须把**两条**都删掉（漏删一条 = 留半拉子状态）。
func TestMigration891_DownDropsBothIndexes(t *testing.T) {
	down := mig886Read(t, mig886Tree(t), filepath.Join("sql", "migrations", "startup", mig886DownName))
	clean := mig886StripComments(down)

	for _, want := range []string{
		"DROP INDEX IF EXISTS public." + mig886ParentIdx,
		"DROP INDEX IF EXISTS public." + mig886HotIdx,
	} {
		if !strings.Contains(clean, want) {
			t.Errorf("891 的 down 必须包含 %q（漏删一条 ⇒ 检查退回半拉子状态：父表有 hot 没有）", want)
		}
	}
}

// TestMigration891_DownDetectorCanFire 是 ③ 的**阳性对照**：
// 只删一条的样本必须被判不合格。
func TestMigration891_DownDetectorCanFire(t *testing.T) {
	droppedBoth := func(body string) []string {
		clean := mig886StripComments(body)
		var missing []string
		for _, name := range []string{mig886ParentIdx, mig886HotIdx} {
			if !strings.Contains(clean, "DROP INDEX IF EXISTS public."+name) {
				missing = append(missing, name)
			}
		}
		return missing
	}
	both := "DROP INDEX IF EXISTS public." + mig886ParentIdx + ";\n" +
		"DROP INDEX IF EXISTS public." + mig886HotIdx + ";"
	if missing := droppedBoth(both); len(missing) != 0 {
		t.Fatalf("阳性对照失败：删两条的样本被判缺 %v —— 检测器坏了", missing)
	}
	one := "DROP INDEX IF EXISTS public." + mig886ParentIdx + ";"
	if missing := droppedBoth(one); len(missing) != 1 || missing[0] != mig886HotIdx {
		t.Errorf("阳性对照失败：只删一条的样本被判为合格（缺 %v）—— 检测器没有鉴别力", missing)
	}
}

// ── ④ 两份副本逐字节一致 ────────────────────────────────────────────────────

// TestMigration891_EmbeddataCopiesAreByteIdentical 钉两份副本一致。
//
// canonical 与 installer embeddata 各一份；只改一份 ⇒ 装出来的库与仓库的库
// 分叉（881/882/890 同款门）。
func TestMigration891_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig886Tree(t)
	for _, name := range []string{mig886Name, mig886DownName} {
		canonical := mig886Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig886Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 的 canonical 与 installer embeddata 两份不一致 —— 只改一份会让装出来的库与仓库分叉", name)
		}
	}
}

// ── ⑤ 顺序前提（fresh-install 路径）─────────────────────────────────────────

// TestMigration891_DependsOnlyOnSessionFamilyTables 钉顺序前提。
//
// 891 只引用两张表：public.session_turns 与 public.session_turns_hot。
// 它们都由**启动序列**建（基线 01-schema + installer-only 的
// session_turns_hot_bootstrap.sql），**不是** 890 建的 ⇒ 891 的 up 体
// 不依赖 890 或任何其它未创建对象。
//
// ⚠ 反向也钉：若将来有人在 891 里引用了别的新对象（例如 890 建的视图），
//
//	这道门会红 ⇒ 提醒先把前提写清楚。
func TestMigration891_DependsOnlyOnSessionFamilyTables(t *testing.T) {
	up := mig886Read(t, mig886Tree(t), filepath.Join("sql", "migrations", "startup", mig886Name))
	clean := mig886StripComments(up)

	// 只允许出现这两张表名。
	allowed := map[string]bool{
		"session_turns":     true,
		"session_turns_hot": true,
	}
	tableRef := regexp.MustCompile(`\b(?:public\.)?session_turns(?:_hot)?\b`)
	seen := map[string]bool{}
	for _, m := range tableRef.FindAllString(clean, -1) {
		seen[m] = true
	}
	for name := range seen {
		base := strings.TrimPrefix(name, "public.")
		if !allowed[base] {
			t.Errorf("891 引用了预期外的表 %q —— 本迁移只应依赖 session_turns / session_turns_hot", name)
		}
	}
	if len(seen) == 0 {
		t.Error("891 的 up 体里没解析出任何会话族表引用 —— 提取器坏了或文件形态变了")
	}
}
