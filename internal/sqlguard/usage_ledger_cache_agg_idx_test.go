package sqlguard

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件是 873 迁移的**源码不变量层**：钉住「覆盖索引必须携带缓存 token 列」
// 这条结构性约束。它不碰数据库，因此每次 build 都会跑。
//
// 真库层（EXPLAIN 判计划是否真的转成 Index Only Scan）在
// admin/cache_economics_covering_index_test.go，由 TEST_DATABASE_URL 门控。
// 两层缺一不可：源码层能证明迁移文件**写了**什么，真库层才能证明
// 生产上那条聚合**真的**不再回堆。
//
// ★ 判据纪律（AGENTS.md）：本文件下面每条断言都在**剥掉 SQL 注释之后**才做。
//
//	873 的注释里反复出现 cache_read_tokens / usage_ledger_ts_tenant_cover_agg_idx
//	这些字面量（正是在解释为什么要有它们）—— 不剥离的话
//	`strings.Contains(file, "cache_read_tokens")` **恒真**，删掉索引里的
//	那一列判据照样全绿。
//
//	而且剥掉 `--` 行注释**还不够**（变异验证实测）：那两个旧索引名在
//	`DO $comment_873_index$` 块里又以**字符串字面量**的形式出现了一次
//	（COMMENT ON INDEX 的正文）。所以凡是要断言「某个名字只出现在某类
//	语句里」，都必须走**语句级**匹配，不能用整文件的 Contains。

const migration873 = "873_usage_ledger_ts_cover_cache_agg_idx.sql"

// stripMigration873Comments 去掉 `--` 行注释。
func stripMigration873Comments(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// loadMigration873 读回 873 的 up 文件（剥注释后的形态）。
func loadMigration873(t *testing.T) (raw string, code string) {
	t.Helper()
	var lastErr error
	for _, dir := range migrationsDirs {
		b, err := os.ReadFile(filepath.Join(dir, migration873))
		if err == nil {
			raw = string(b)
			return raw, stripMigration873Comments(raw)
		}
		lastErr = err
	}
	t.Fatalf("cannot read %s: %v", migration873, lastErr)
	return "", ""
}

// sqlStatements 把剥过注释的 SQL 切成「以分号结尾」的语句片段。
func sqlStatements(code string) []string {
	var stmts []string
	for _, s := range strings.Split(code, ";") {
		if s = strings.TrimSpace(s); s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}

// migration873IndexDef 返回某个表上由 873 建立的 CREATE INDEX 语句。
// 用「表名 + INCLUDE 列表首列」定位，而不是按索引名 grep ——
// 分区上的索引名由 PG 按列名自动改写，按名字找会得出「没建」的假结论
// （870 已经踩过一次这个坑，本文件头注也引用了它）。
func migration873IndexDef(t *testing.T, code, table string) string {
	t.Helper()
	for _, stmt := range sqlStatements(code) {
		if !strings.HasPrefix(stmt, "CREATE INDEX") {
			continue
		}
		if !strings.Contains(stmt, "ON public."+table+" (") &&
			!strings.Contains(stmt, "ON public."+table+"\n") &&
			!strings.Contains(stmt, "ON ONLY (") && !strings.Contains(stmt, "public."+table) {
			continue
		}
		// 必须是这张表的，不能是另一张。
		if !strings.Contains(stmt, "public."+table) {
			continue
		}
		if strings.Contains(stmt, "INCLUDE (prompt_tokens") {
			return stmt
		}
	}
	return ""
}

// TestMigration873CoverIncludeCarriesCacheTokens —— 本轮缺陷的正判据。
//
// 856 的覆盖索引 INCLUDE 里没有 cache_read_tokens，于是
// `SELECT COUNT(*), SUM(prompt_tokens), SUM(cost_usd), SUM(cache_read_tokens)`
// 会退回 Parallel Index Scan 逐行回堆（30 天 1,478k 行：buffers 209,361）。
// 去掉这一列即转 Index Only Scan（buffers 19,245）。
// ⇒ 迁移必须把缓存 token 列放进 INCLUDE，否则该缺陷原样保留。
func TestMigration873CoverIncludeCarriesCacheTokens(t *testing.T) {
	_, code := loadMigration873(t)

	def := migration873IndexDef(t, code, "usage_ledger")
	if def == "" {
		t.Fatal("873 未能在分区父表 usage_ledger 上找到带 INCLUDE 的 CREATE INDEX，判据前提不成立")
	}

	for _, col := range []string{"cache_read_tokens", "cache_write_tokens"} {
		if !strings.Contains(def, col) {
			t.Errorf("873 的 INCLUDE 列表缺 %s：带 SUM(%s) 的窗口聚合会退回逐行回堆"+
				"（实测 buffers 19,245 → 209,361）", col, col)
		}
	}
}

// TestMigration873HotArmGetsSameShape —— 视图是 UNION ALL，两臂形状不同会让
// planner 在其中一臂退回回堆，收益减半（沿用 856 的这条理由）。
func TestMigration873HotArmGetsSameShape(t *testing.T) {
	_, code := loadMigration873(t)

	hot := migration873IndexDef(t, code, "usage_ledger_hot")
	if hot == "" {
		t.Fatal("873 未能在 usage_ledger_hot 上找到带 INCLUDE 的 CREATE INDEX（UNION ALL 视图的另一臂会退回回堆）")
	}
	for _, col := range []string{"cache_read_tokens", "cache_write_tokens"} {
		if !strings.Contains(hot, col) {
			t.Errorf("873 的 hot 表 INCLUDE 列表缺 %s", col)
		}
	}
}

// TestMigration873KeyColumnsAreTsTenant —— 列序沿用 856 的取舍：键 (ts, tenant_id)。
//   超管（无 tenant 过滤）：ts 范围即可定位 ⇒ index-only
//   租户档（AND tenant_id = $2）：tenant_id 是第二列 ⇒ 同样 index-only
// 若把 tenant_id 挪进 INCLUDE，索引会宽一截而收益相同。
func TestMigration873KeyColumnsAreTsTenant(t *testing.T) {
	_, code := loadMigration873(t)

	for _, table := range []string{"usage_ledger", "usage_ledger_hot"} {
		def := migration873IndexDef(t, code, table)
		if def == "" {
			t.Fatalf("未找到 %s 上的 873 索引", table)
		}
		keyPart := def
		if i := strings.Index(def, "INCLUDE"); i >= 0 {
			keyPart = def[:i]
		}
		if !strings.Contains(keyPart, "ts, tenant_id") {
			t.Errorf("%s 的 873 索引键列不是 (ts, tenant_id)：%q", table, strings.TrimSpace(keyPart))
		}
	}
}

// TestMigration873CascadesToPartitions —— 870 的实测坑：`ON ONLY` 作为**创建选项**
// 会抑制向分区的级联，父表只留 0 字节空壳。凡在这张分区父表上建索引的迁移，
// 都不得带 ON ONLY。
func TestMigration873CascadesToPartitions(t *testing.T) {
	_, code := loadMigration873(t)

	if strings.Contains(code, "ON ONLY") {
		t.Error("873 建索引时带 ON ONLY，会抑制向分区的级联，只留 0 字节空壳（870 实测）")
	}
	if strings.Contains(code, "CONCURRENTLY") {
		t.Error("873 带 CONCURRENTLY：PG 不支持在分区父表上 CREATE INDEX CONCURRENTLY（849/870 实测）")
	}
}

// droppedIndexNames 返回代码里每条 `DROP INDEX` 语句所指向的索引名。
func droppedIndexNames(code string) []string {
	var names []string
	for _, stmt := range sqlStatements(code) {
		if !strings.HasPrefix(stmt, "DROP INDEX") {
			continue
		}
		fields := strings.Fields(stmt)
		names = append(names, fields[len(fields)-1])
	}
	return names
}

// TestMigration873DropsSuperseded856Index —— 873 是 856 的**超集**。
// usage_ledger 是全站最热的写表，让 (ts, tenant_id) 前缀、高度冗余的
// 索引并存，等于给写路径加一份长期写放大。
// ⇒ 必须在本迁移内删掉 856 的两条。
//
// 同时钉住**顺序**：每条 DROP 都必须排在所有 CREATE 之后。反过来写的话，
// 构建失败时旧索引已经被删掉，这条聚合查询就彻底没有覆盖索引了。
func TestMigration873DropsSuperseded856Index(t *testing.T) {
	_, code := loadMigration873(t)

	dropped := droppedIndexNames(code)
	want := map[string]bool{
		"public.usage_ledger_ts_tenant_cover_agg_idx":     false,
		"public.usage_ledger_hot_ts_tenant_cover_agg_idx": false,
	}
	for _, n := range dropped {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, hit := range want {
		if !hit {
			t.Errorf("873 的 DROP INDEX 语句里没有 %s：两条同前缀索引并存会给最热的写表加冗余写放大"+
				"（当前 DROP 到的索引：%v）", n, dropped)
		}
	}

	lastCreate := strings.LastIndex(code, "CREATE INDEX")
	firstDrop := strings.Index(code, "DROP INDEX")
	if lastCreate < 0 || firstDrop < 0 {
		t.Fatal("873 缺少 CREATE INDEX 或 DROP INDEX，判据前提不成立")
	}
	if firstDrop < lastCreate {
		t.Error("873 有 DROP INDEX 排在 CREATE INDEX 之前：构建失败时会把旧覆盖索引先带走")
	}
}

// TestMigration873GuardRefusesToDropUnbuiltIndex —— 删旧索引前必须先确认新索引
// **建出来且有效**。没有这道闸，构建失败会把 cache-economics 的故障扩散到
// usage summary / by-model 整族。
//
// 这里断言的是**出现次数**而不是「出现过就算」：守卫块里有 3 条失败路径
// （两个新索引各一条 + 一条「未建成」），任何一条被删掉都必须变红。
// 只写 `strings.Contains(guard, "RAISE EXCEPTION")` 的话，删掉三条里的两条
// 判据照样绿 —— 本轮变异验证实测过这个形态。
func TestMigration873GuardRefusesToDropUnbuiltIndex(t *testing.T) {
	_, code := loadMigration873(t)

	// ★ 守卫块必须**两头都界定**。`code[start:]` 会一路取到文件末尾，
	//   把后面 $comment_873_index$ 块里的同名 to_regclass / 索引名一起吞进来，
	//   于是「守卫块里有没有判这个索引」恒为真 —— 本轮变异验证实测过这个形态。
	//   这是同一个陷阱连踩第三次：剥 `--` 注释不够，语句级匹配也不够，
	//   **取子串的边界本身**要能界定住。
	const guardTag = "$drop_856_superseded$"
	guardStart := strings.Index(code, guardTag)
	if guardStart < 0 {
		t.Fatal("873 缺少 $drop_856_superseded$ 守卫块")
	}
	guardEnd := strings.Index(code[guardStart:], guardTag+";")
	if guardEnd < 0 {
		t.Fatal("873 的守卫块没有闭合的 $drop_856_superseded$;")
	}
	guard := code[guardStart : guardStart+guardEnd]

	for _, idx := range []string{"usage_ledger_ts_tenant_cache_agg_idx", "usage_ledger_hot_ts_tenant_cache_agg_idx"} {
		if !strings.Contains(guard, "to_regclass('public."+idx+"')") {
			t.Errorf("守卫块没有确认 %s 是否建成：未确认就删旧索引，失败时整族聚合查询失去覆盖索引", idx)
		}
	}
	if n := strings.Count(guard, "indisvalid"); n < 2 {
		t.Errorf("守卫块只检查了 %d 处 indisvalid（应 ≥2，每个新索引一处）："+
			"索引建到一半无效时也会被当成可用", n)
	}
	if n := strings.Count(guard, "RAISE EXCEPTION"); n < 3 {
		t.Errorf("守卫块只有 %d 条 RAISE EXCEPTION（应 ≥3）：校验失败会变成静默继续执行", n)
	}
}

// TestMigration873HasDownFile —— 回滚脚本必须存在，且**只删索引不动数据**。
// 回滚 873 的代价是功能退化而不是故障恢复，所以更要保证它随手可用。
func TestMigration873HasDownFile(t *testing.T) {
	const down = "873_usage_ledger_ts_cover_cache_agg_idx.down.sql"

	var found string
	for _, dir := range migrationsDirs {
		if _, err := os.Stat(filepath.Join(dir, down)); err == nil {
			found = dir
			break
		}
	}
	if found == "" {
		t.Fatalf("%s 不存在：873 无法回滚", down)
	}

	b, err := os.ReadFile(filepath.Join(found, down))
	if err != nil {
		t.Fatalf("read %s: %v", down, err)
	}
	downCode := stripMigration873Comments(string(b))

	for _, name := range []string{
		"public.usage_ledger_ts_tenant_cache_agg_idx",
		"public.usage_ledger_hot_ts_tenant_cache_agg_idx",
	} {
		hit := false
		for _, n := range droppedIndexNames(downCode) {
			if n == name {
				hit = true
			}
		}
		if !hit {
			t.Errorf("回滚脚本的 DROP INDEX 语句里没有 %s", name)
		}
	}
	for _, forbidden := range []string{"DELETE", "TRUNCATE", "DROP TABLE", "UPDATE "} {
		if strings.Contains(downCode, forbidden) {
			t.Errorf("回滚脚本出现了 %q：索引迁移的回滚不得触碰数据", forbidden)
		}
	}
}

// TestMigration873MirroredToInstaller —— 迁移必须与 installer 内嵌副本逐字节相同，
// 否则「本地跑了、部署链上没跑」这类缺口无从发现（856 的两个副本就是逐字节相同）。
func TestMigration873MirroredToInstaller(t *testing.T) {
	const embedDir = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	const srcDir = "../../sql/migrations/startup"

	for _, name := range []string{
		migration873,
		"873_usage_ledger_ts_cover_cache_agg_idx.down.sql",
	} {
		src, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		embed, err := os.ReadFile(filepath.Join(embedDir, name))
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if string(src) != string(embed) {
			t.Errorf("%s 与 installer 内嵌副本不是逐字节相同（%d vs %d bytes）："+
				"部署链跑的那份会与本地这份不一致", name, len(src), len(embed))
		}
	}
}

// TestMigration873RegisteredInChangelog —— 新迁移必须在台账里按**真实 SHA** 登记。
// 见 TestRegistryShaIsActuallyTheFile 与 TestMigrationRegistryStaysContinuous；
// 本条只补一层「873 自己有没有登记」的定向断言，失败信息能直接指名文件。
func TestMigration873RegisteredInChangelog(t *testing.T) {
	b, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	src, err := os.ReadFile(filepath.Join(migrationsDirs[0], migration873))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	want := sha256Hex(src)

	if !strings.Contains(string(b), want) {
		t.Errorf("台账里找不到 873 的真实 SHA %s：未登记的迁移会被静默部署（224 号背景）", want)
	}
	if !strings.Contains(string(b), "| 873 |") {
		t.Error("台账里没有 873 的编号行")
	}
}

// sha256Hex 与台账里记录的 SHA 口径一致（无分隔符的小写十六进制）。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
