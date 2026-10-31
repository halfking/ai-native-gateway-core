package ursm_test

// live_write_shape_test.go —— r1042 轮新增：★ **「生产代码写入的形状」与「真库实际形状」对不对得上**。
//
// ★★ 起因（★ 本轮实测，★ 不是推演）：
//
//	本机 PG 日志里出现 **542 条**同一种错误：
//	  ERROR: column "tenant_id" of relation "stats_reconciliation_diffs" does not exist
//	⇒ ⇒ 生产代码 domains/stats/reconciliation.go 的 INSERT 带着 tenant_id，
//	⇒ ⇒ 而真库那张表**根本没有这一列**（也缺 545 的 CHECK 与 546/548 的唯一索引）
//	⇒ ⇒ 那一轮**根本写不进去** ⇒ ⇒ **对账差异全部丢失**（只剩 slog.Warn 留痕）。
//
// ★★★ 而仓内**所有门都是绿的** ⇒ ⇒ 这就是 §488 那条通式的又一次应验：
// ★★★ 「门全绿」与「系统健康」之间**没有推理链** ——
// ★★★ 本仓的门验的是【仓内的形状】（迁移文件 / 代码 / 登记册），
// ★★★ ★★ **没有一道门问过「真库现在长什么样」**。
//
// ★★ 本门补的正是这条链：
//
//	★ 静态层（恒跑，不需要 DSN）：★ **从生产代码里派生**「写入所需形状」
//	  （每张目标表的 INSERT 列清单 + ON CONFLICT 的仲裁目标）
//	  ⇒ ⇒ 再问「仓内的启动迁移链**能不能**造出那个形状」
//	  ⇒ ⇒ 静态层抓的是【仓内自相矛盾】（代码要的东西迁移没造）。
//	★ 运行时层（需要 TEST_DATABASE_URL）：★ 真库上是不是**已经是**那个形状
//	  ⇒ ⇒ 运行时层抓的是【环境漂移】（本轮那 542 条错误就是它抓的）。
//
// ★★ 判据必须【从代码派生】而不是手写清单：★ 手写清单会随代码漂移
// ★★ ⇒ 门会一直绿着，而生产已经在写一个库里没有的列（★ 本轮的原样）。
//
// ★★ 无 DSN 时运行时层 **SKIP 而非 PASS**（★ 仓内既有惯例，见
// ★★ pg_slow_query_observability_test.go）⇒ ⇒ 静态层必须**独立有效**。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// ★ 生产写入路径（★ 判据的事实源）。
	reconcileWriterFile = "domains/stats/reconciliation.go"
	// ★ 启动迁移目录（★ 仓内能造出那个形状的地方）。
	startupMigrationDir = "sql/migrations/startup"
)

// tableSpec 是「生产代码写入某张表时所需形状」的抽象。
type tableSpec struct {
	// columns：★ 该表所有 INSERT 的列清单并集。
	columns []string
	// conflictColumns：★ ON CONFLICT 的仲裁目标列（★ 可能为空 ⇒ 该写入不依赖仲裁索引）。
	conflictColumns []string
}

// liveShape 是「真库（或迁移链）实际具备的形状」。
type liveShape struct {
	// columns：★ "table.column" ⇒ true。
	columns map[string]bool
	// uniqueIndexes：★ 按表分组（★ table → 各唯一索引的列清单）。
	uniqueIndexes map[string][][]string
}

// ───────────────────────── 派生 ─────────────────────────

var (
	// ★ 一条 INSERT 的头（★ 表名 + 列清单）。
	reInsertHeadRe = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(\w+)\s*\(([^)]*)\)`)
	// ★ 单条 INSERT 内的 ON CONFLICT。
	reConflictRe     = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\(([^)]*)\)`)
	reCreateTableRe  = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s*\(([^;]*?)\);`)
	reAddColumnRe    = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(\w+)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)
	reCreateUniqueRe = regexp.MustCompile(`(?is)CREATE\s+UNIQUE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?\w+\s+ON\s+(\w+)\s*\(([^)]*)\)`)
	reColIdentRe     = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// splitCols 把 "a, b,\n c" 拆成规范化列名（★ 去空白 / 去引号 / 小写）。
func splitCols(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		c := strings.TrimSpace(raw)
		c = strings.Trim(c, `"`)
		c = strings.ToLower(c)
		if c == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// parseWriteSpec 从生产代码里解析「每张目标表的写入所需形状」。
//
// ★★★ 必须**按表**解析：★ 本仓这个文件里有**两张**目标表
// ★★★ （stats_reconciliation_runs 写运行头、stats_reconciliation_diffs 写差异行）
// ★★★ 且**只有后者带 ON CONFLICT** ⇒ ⇒ ★ 第一版只取「第一条 INSERT + 全局第一条
// ★★★ ON CONFLICT」⇒ ★★ 把 runs 表的列配上了 diffs 表的仲裁目标 ⇒ ⇒ 假缺口。
func parseWriteSpec(src string) (map[string]*tableSpec, error) {
	out := map[string]*tableSpec{}
	ensure := func(tbl string) *tableSpec {
		if s, ok := out[tbl]; ok {
			return s
		}
		s := &tableSpec{}
		out[tbl] = s
		return s
	}
	addCols := func(s *tableSpec, cols []string) {
		seen := map[string]bool{}
		for _, c := range s.columns {
			seen[c] = true
		}
		for _, c := range cols {
			if !seen[c] {
				seen[c] = true
				s.columns = append(s.columns, c)
			}
		}
		sort.Strings(s.columns)
	}

	// ★★★ 按【INSERT 切段】再在段内找 ON CONFLICT ——
	// ★★★ 第一版用「一条正则 + 惰性匹配」⇒ ★★ 从 runs 表的 INSERT 一路
	// ★★★ 惰性跨到 diffs 表的 ON CONFLICT ⇒ ⇒ 把 diffs 的仲裁目标安到了 runs 头上
	// ★★★ ⇒ ⇒ 报出「runs 缺唯一索引」这个**假缺口** ⇒ ⇒ 门差点被自己的
	// ★★★ 第一版实现判红 ⇒ ★★ 这就是「判据自己也要被量」。
	heads := reInsertHeadRe.FindAllStringSubmatchIndex(src, -1)
	for i, h := range heads {
		tbl := strings.ToLower(src[h[2]:h[3]])
		cols := splitCols(src[h[4]:h[5]])
		s := ensure(tbl)
		addCols(s, cols)
		// ★ 段尾 = 下一条 INSERT 的起点（★ 或文件末尾）。
		end := len(src)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		segment := src[h[1]:end]
		if len(s.conflictColumns) == 0 {
			if cm := reConflictRe.FindStringSubmatch(segment); cm != nil {
				s.conflictColumns = splitCols(cm[1])
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("★ 生产代码里没有解析到任何 INSERT")
	}
	for tbl, s := range out {
		if len(s.columns) == 0 {
			return nil, fmt.Errorf("★ 表 %s 没有解析到列清单", tbl)
		}
	}
	return out, nil
}

// parseMigrationsShape 从启动迁移目录里解析「仓内能造出的形状」。
func parseMigrationsShape(dir string) (liveShape, error) {
	shape := liveShape{
		columns:       map[string]bool{},
		uniqueIndexes: map[string][][]string{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return shape, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return shape, err
		}
		src := string(b)
		for _, m := range reCreateTableRe.FindAllStringSubmatch(src, -1) {
			tbl := strings.ToLower(m[1])
			for _, line := range strings.Split(m[2], "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "--") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) == 0 {
					continue
				}
				name := strings.ToLower(strings.TrimSuffix(fields[0], ","))
				if !reColIdentRe.MatchString(name) {
					continue
				}
				// ★ 只收「列定义」行 ⇒ 排除 PRIMARY KEY / UNIQUE / CHECK / FOREIGN 段。
				upper := strings.ToUpper(line)
				if strings.HasPrefix(upper, "PRIMARY") || strings.HasPrefix(upper, "UNIQUE") ||
					strings.HasPrefix(upper, "CHECK") || strings.HasPrefix(upper, "FOREIGN") ||
					strings.HasPrefix(upper, "CONSTRAINT") {
					continue
				}
				shape.columns[tbl+"."+name] = true
			}
		}
		for _, m := range reAddColumnRe.FindAllStringSubmatch(src, -1) {
			shape.columns[strings.ToLower(m[1])+"."+strings.ToLower(m[2])] = true
		}
		for _, m := range reCreateUniqueRe.FindAllStringSubmatch(src, -1) {
			tbl := strings.ToLower(m[1])
			cols := splitCols(m[2])
			if len(cols) == 0 {
				continue
			}
			shape.uniqueIndexes[tbl] = append(shape.uniqueIndexes[tbl], cols)
		}
	}
	return shape, nil
}

// ───────────────────────── 判据（★ 纯函数 ⇒ 可自证有牙） ─────────────────────────

// shapeGaps 返回「所需形状 vs 现有形状」的缺口清单（★ 空 = 满足）。
func shapeGaps(specs map[string]*tableSpec, have liveShape) []string {
	var gaps []string
	for tbl, spec := range specs {
		for _, c := range spec.columns {
			if !have.columns[tbl+"."+c] {
				gaps = append(gaps, fmt.Sprintf("缺列 %s.%s", tbl, c))
			}
		}
		if len(spec.conflictColumns) == 0 {
			continue
		}
		want := append([]string(nil), spec.conflictColumns...)
		sort.Strings(want)
		found := false
		for _, idx := range have.uniqueIndexes[tbl] {
			got := append([]string(nil), idx...)
			sort.Strings(got)
			if strings.Join(got, ",") == strings.Join(want, ",") {
				found = true
				break
			}
		}
		if !found {
			gaps = append(gaps, fmt.Sprintf(
				"缺唯一索引（%s 的仲裁目标 %s）", tbl, strings.Join(spec.conflictColumns, ", ")))
		}
	}
	sort.Strings(gaps)
	return gaps
}

// ───────────────────────── 门 ─────────────────────────

// TestReconciliationWriteShapeIsCoveredByRepoMigrations：★ 静态层 ——
// ★ 仓内的启动迁移链**能不能**造出生产写入所需的形状。
//
// ★ 这一层抓的是【仓内自相矛盾】：★ 代码要 tenant_id，迁移链却没加。
func TestReconciliationWriteShapeIsCoveredByRepoMigrations(t *testing.T) {
	root := hollowRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, reconcileWriterFile))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", reconcileWriterFile, err)
	}
	specs, err := parseWriteSpec(string(src))
	if err != nil {
		t.Fatalf("解析生产写入形状失败：%v", err)
	}
	shape, err := parseMigrationsShape(filepath.Join(root, startupMigrationDir))
	if err != nil {
		t.Fatalf("解析启动迁移形状失败：%v", err)
	}
	gaps := shapeGaps(specs, shape)
	if len(gaps) > 0 {
		var detail []string
		for tbl, s := range specs {
			detail = append(detail, fmt.Sprintf("  · %s：列 %v，仲裁目标 %v",
				tbl, s.columns, s.conflictColumns))
		}
		sort.Strings(detail)
		t.Fatalf("★ 生产写入形状与仓内迁移链不一致（%d 处）：\n  %s\n%s\n"+
			"  ⇒ 仓内没有任何迁移能造出这个形状 ⇒ ⇒ 新装库上这条写入会直接失败",
			len(gaps), strings.Join(gaps, "\n  "), strings.Join(detail, "\n"))
	}
	t.Logf("★ 生产写入形状：%d 张表 ⇒ 迁移链可满足 ✓", len(specs))
}

// TestLiveDbHasReconciliationWriteShape：★ 运行时层 ——
// ★ **真库**上是不是已经是生产写入所需的形状。
//
// ★★ 这一层是本轮那 542 条错误本来该被抓住的地方：
// ★★ 本机库缺 tenant_id ⇒ ⇒ 生产写入整条失败 ⇒ ⇒ 对账差异全部丢失。
func TestLiveDbHasReconciliationWriteShape(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证写入形状" +
			"（★ 这道腿在 CI 里恒 SKIP ⇒ 静态层必须独立有效）")
	}
	root := hollowRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, reconcileWriterFile))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", reconcileWriterFile, err)
	}
	specs, err := parseWriteSpec(string(src))
	if err != nil {
		t.Fatalf("解析生产写入形状失败：%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连真库失败：%v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	live := liveShape{
		columns:       map[string]bool{},
		uniqueIndexes: map[string][][]string{},
	}
	rows, err := conn.Query(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = ANY($1)`, specTableNames(specs))
	if err != nil {
		t.Fatalf("读真库列清单失败：%v", err)
	}
	for rows.Next() {
		var tbl, c string
		if err := rows.Scan(&tbl, &c); err != nil {
			rows.Close()
			t.Fatalf("扫描列名失败：%v", err)
		}
		live.columns[strings.ToLower(tbl)+"."+strings.ToLower(c)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列清单失败：%v", err)
	}

	idxRows, err := conn.Query(ctx, `
		SELECT i.indrelid::regclass::text, c.relname, a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indisunique AND n.nspname = 'public'
		  AND i.indrelid = ANY($1::regclass[])`, specTableRegclasses(specs))
	if err != nil {
		t.Fatalf("读真库唯一索引失败：%v", err)
	}
	cur := ""
	for idxRows.Next() {
		var tbl, name, att string
		if err := idxRows.Scan(&tbl, &name, &att); err != nil {
			idxRows.Close()
			t.Fatalf("扫描索引列失败：%v", err)
		}
		tbl = strings.ToLower(tbl)
		if name != cur {
			live.uniqueIndexes[tbl] = append(live.uniqueIndexes[tbl], []string{})
			cur = name
		}
		live.uniqueIndexes[tbl][len(live.uniqueIndexes[tbl])-1] = append(
			live.uniqueIndexes[tbl][len(live.uniqueIndexes[tbl])-1], strings.ToLower(att))
	}
	idxRows.Close()
	if err := idxRows.Err(); err != nil {
		t.Fatalf("遍历索引列失败：%v", err)
	}

	// ★ 目标表本身必须存在（★ 不存在 ⇒ 写入直接报 relation does not exist）。
	for tbl := range specs {
		if len(live.columns) > 0 && !tableExists(live, tbl) {
			t.Fatalf("★ 真库里根本没有表 %s ⇒ ⇒ 生产写入会直接失败（relation does not exist）", tbl)
		}
	}

	gaps := shapeGaps(specs, live)
	if len(gaps) > 0 {
		t.Fatalf("★ 真库形状与生产写入要求不一致（%d 处）：\n  %s\n"+
			"  ⇒ 这一族写入在真库上**整条失败**（不是写小了，是写不进去）\n"+
			"  ⇒ ⇒ 修法：补跑对应的 startup 迁移（★ 仓内 %s 目录）",
			len(gaps), strings.Join(gaps, "\n  "), startupMigrationDir)
	}
	t.Logf("★ 真库形状满足生产写入要求：%d 张表 ✓", len(specs))
}

// tableExists 判断真库形状里是否有这张表的任何列（★ 有列 ⇒ 表存在）。
func tableExists(live liveShape, tbl string) bool {
	for k := range live.columns {
		if strings.HasPrefix(k, tbl+".") {
			return true
		}
	}
	return false
}

// specTableNames 返回目标表名（★ 供 SQL 的 ANY($1) 用）。
func specTableNames(specs map[string]*tableSpec) []string {
	out := make([]string, 0, len(specs))
	for t := range specs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// specTableRegclasses 返回目标表的 regclass 文本（★ 供 SQL 的 ANY($1::regclass[]) 用）。
func specTableRegclasses(specs map[string]*tableSpec) []string {
	out := make([]string, 0, len(specs))
	for t := range specs {
		out = append(out, "public."+t)
	}
	sort.Strings(out)
	return out
}

// TestLiveWriteShapeDetectorHasTeeth：★ 自证有牙 ——
// ★ 「缺列 / 缺仲裁索引 / 多表串味」三种漂移，判据都必须报出来。
//
// ★★★ 仓内纪律：★ 门归零之后要立刻做变异臂 ⇒ ★ 这里就是内建的变异臂：
// ★★★ 把形状**故意弄坏** ⇒ ⇒ 判据必须报出缺口 ⇒ ⇒ 否则这道门恒绿且无意义。
func TestLiveWriteShapeDetectorHasTeeth(t *testing.T) {
	specs := map[string]*tableSpec{
		"t": {columns: []string{"a", "b"}, conflictColumns: []string{"a", "b"}},
		"u": {columns: []string{"x"}},
	}
	complete := liveShape{
		columns:       map[string]bool{"t.a": true, "t.b": true, "u.x": true},
		uniqueIndexes: map[string][][]string{"t": {{"a", "b"}}},
	}
	if gaps := shapeGaps(specs, complete); len(gaps) != 0 {
		t.Fatalf("★ 完整形状被判出缺口（%v）⇒ ⇒ 判据过宽 ⇒ ⇒ 会误报", gaps)
	}

	// ★ 变异臂 1：缺一列。
	noCol := liveShape{
		columns:       map[string]bool{"t.a": true, "u.x": true},
		uniqueIndexes: map[string][][]string{"t": {{"a", "b"}}},
	}
	gaps := shapeGaps(specs, noCol)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "缺列 t.b") {
		t.Fatalf("★ 缺列时判据没报出「缺列 t.b」（%v）⇒ ⇒ 门没牙", gaps)
	}

	// ★ 变异臂 2：仲裁索引的列集不对。
	wrongIdx := liveShape{
		columns:       map[string]bool{"t.a": true, "t.b": true, "u.x": true},
		uniqueIndexes: map[string][][]string{"t": {{"a"}}},
	}
	gaps = shapeGaps(specs, wrongIdx)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "缺唯一索引") {
		t.Fatalf("★ 缺仲裁索引时判据没报出「缺唯一索引」（%v）⇒ ⇒ 门没牙", gaps)
	}

	// ★ 变异臂 3：列集相同但顺序不同 ⇒ **不是**缺口（★ 别把 PG 的匹配语义判错）。
	reordered := liveShape{
		columns:       map[string]bool{"t.a": true, "t.b": true, "u.x": true},
		uniqueIndexes: map[string][][]string{"t": {{"b", "a"}}},
	}
	if gaps := shapeGaps(specs, reordered); len(gaps) != 0 {
		t.Fatalf("★ 列集相同、顺序不同的唯一索引被判成缺口（%v）⇒ ⇒ 判据把 PG 的列集匹配判成了有序匹配", gaps)
	}

	// ★ 变异臂 4：★ 多表串味 —— ★ u 表的列不能拿 t 表的仲裁索引顶账。
	crossed := liveShape{
		columns:       map[string]bool{"t.a": true, "t.b": true, "u.x": true},
		uniqueIndexes: map[string][][]string{"u": {{"a", "b"}}},
	}
	gaps = shapeGaps(specs, crossed)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "缺唯一索引（t ") {
		t.Fatalf("★ 别的表的唯一索引被当成了本表的仲裁索引（%v）⇒ ⇒ 门没牙", gaps)
	}
}
