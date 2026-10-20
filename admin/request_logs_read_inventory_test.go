package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// requestLogsReadInventory 是「仍读**已退役 v1 族**的生产文件」清单。
//
// # 这张表现在是空的（迁移 890，2026-10-20）
//
// 迁移 890 把 v1 族**物理删除**了（10 张底表 + 4 张 v1-only 视图 + 序列/索引/RLS）。
// v1 读方随之归零 ⇒ 这张表随之清空。
//
// 但「清空」不是「删掉这张表」：它现在守的是**反向**的不变式 ——
//
//	**不许出现新的已退役 v1 族读方。**
//
// 这正是本表从建表起就在守的东西（§8.5 的原话：「新增一个读 _request_logs 的文件
// ⇒ 有人开始读一张 S4 将停写的表；某个文件的读点数变了 ⇒ 这张表已陈旧，
// 而它的下游每一个数都跟着陈旧」），只是 890 之后那张表**不存在了**，
// 于是不变式从「不许有新的」升级成「一个都不许有」。
//
// # 口径（换正则就不是这个数，改这里之前先读审计 §8.5）
//
//	grep -rniE "(from|join)[[:space:]]+_request_logs(_[a-z_]+)?\b" --include=*.go . \
//	  | grep -v "_test\.go" | grep -v "^\./docs"
//
// ⚠ **2026-10-20（迁移 890）收窄**：原来 `(_[a-z_]+)?` 把 **canonical 视图**
// `request_logs_with_current_month` 也算成 v1 读点 —— 而 890 之后那张视图**仍然存在**
// （内容换成会话族两臂体，118 列同名同序）⇒ 读它**不是**读 v1。
// ⇒ 扫描口径改成「**已退役**的 v1 族关系」闭合集（retiredV1RelationNames：
// 5 张底表 × 两种拼写 + 4 张 v1-only 视图）。
//
// ⚠ 这个收窄**不是**「把扫描器改窄让它变绿」：
//   - 收窄后的集合是**更严格**的定义（只认真 v1），不是更宽；
//   - 少算的是「canonical 视图读方」——它们本来就不属于 v1 退役范围；
//   - 判据的区分力由 TestRetiredV1ReferencesAreAllAccountedFor 与
//     TestSessionFamilySourceShapeMatchersHaveTeeth 守着（阳性对照：真 v1 必须命中；
//     canonical 视图必须不命中）。
//
// # 扫描器扫不到的两类（都不是「v1 读方」，登记在别处）
//
//	① **切换层读方**（§9.49）：把表名收进 Go 标识符再拼进 SQL ——
//	   `maas.requestLogsSource(days)`、`db.SessionBodiesSourceSQL()`、
//	   `admin.logsSourceFromSQL()`。⇒ 登记在 indirectRequestLogsReaders。
//	② **已退役关系的残留引用**：890 之后仍引用被删关系的位置 ——
//	   登记在 retiredV1ReferenceExceptions（生产缺陷台账，本轮只报告不修）。
//
// # 历史（保留口径变更的理由，不删）
//
// 审计 §5.5.5「会话域视图依赖的最终分类」覆盖 14 个文件，而实际读
// `_request_logs*` 的生产文件有 104 个、237 个调用点（2026-10-01 实测）。
// §5.5.8 的标题写的是「第一版表不完整，逐条补齐」——**但那张表从未覆盖到
// 其余 90 个文件，且它自称补全过**。没有任何机制会在这个缺口扩大时报红。
//
//	2026-10-01 实测：**277 命中 − 40 条注释行 = 237 真实调用点 / 104 个文件**。
//	2026-10-05 加 `join` ⇒ **109 文件 / 271 调用点**。
//	2026-10-20（890 之后）收窄到「已退役关系」⇒ **0 文件 / 0 调用点**。
//
// ★★ **§9.259：这张表**不是**读方全集，别拿它当退役工作量。**
//
// 它只按 `(from|join)\s+…` **行扫描**，所以**看不见走切换层的读方**。
// 两张表各有各的口径，不是一张表的两半 ⇒ 退役排期要用**并集**
// （allKnownRequestLogsReaderFiles）。
//
// # 刻意不自动分类
//
// 我试过按「语句窗口内有无 gw_session_id / request_id =」自动分 A/B/C/D，
// 拿 §5.5.5 已人工核定的 14 个点交叉验证，**判错 5 个**。**所以这道门只数个数，不下判定。**
var requestLogsReadInventory = map[string]int{
	// 890 之后：已退役 v1 族的读方为零。
	// ⚠ **不要在这里手填「已知的例外」** —— 已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（那是生产缺陷台账，不是 v1 读方清单）。
}

// TestRequestLogsReadInventoryIsComplete 守「已退役 v1 族的读方为零」这条不变式。
//
// # 890 之后这张门的语义变了（2026-10-20）
//
// 建表时它守的是**双向漂移**：「新增读方」与「清单陈旧」都要红。
// 890 把 v1 族删掉之后，读方总体归零 ⇒ 清单随之清空 ⇒
//
//	**清单必须为空，且实测读方必须为空。**
//
// 实测读方 = 扫描结果 − 已退役关系的**具名例外**
// （retiredV1ReferenceExceptions：那是 890 已删、但生产侧还没改的残留引用，
//
//	属生产缺陷台账，登记在别处；它们运行时会 42P01，已在审计文档里报告）。
//
// # 为什么要留一道「清单为空」的门
//
// 清空一张表，最省事的做法是**把门一起删掉**——那样「不许有新的 v1 读方」
// 这条不变式就没人守了。⇒ 门保留，判据从「清单 ⊆ 实测」翻成「**实测必须为空**」。
//
// # 阳性对照
//
// 「实测为空」这条断言最典型的失效形态是**扫描器坏了**（模式写错 / 路径写错
// ⇒ 恒空 ⇒ 恒绿）。⇒ 扫描器的区分力由 TestRetiredV1ReferencesAreAllAccountedFor
// 的模式级对照钉住（真 v1 必须命中、canonical 视图必须不命中），
// 本门另加一条：**例外台账里登记的文件必须真的被扫描命中**（见下），
// 保证扫描器不是恒空的。
func TestRequestLogsReadInventoryIsComplete(t *testing.T) {
	root := repoRootFromCaller(t)
	actual := scanRequestLogsReaders(t, root)

	total := 0
	for _, n := range actual {
		total += n
	}
	t.Logf("已退役 v1 族读方：实测 %d 文件 / %d 调用点（清单 %d 文件 / %d 调用点；"+
		"另有 %d 个已具名的残留引用见 retiredV1ReferenceExceptions）",
		len(actual), total, len(requestLogsReadInventory), sumInventory(),
		len(retiredV1ReferenceExceptions))

	// ① 清单必须为空：v1 族已删，任何登记项都是陈旧的退役台账。
	if len(requestLogsReadInventory) > 0 {
		var files []string
		for f := range requestLogsReadInventory {
			files = append(files, f)
		}
		sort.Strings(files)
		t.Errorf("requestLogsReadInventory 仍有 %d 条登记 —— 890 之后 v1 族已物理删除，"+
			"这张表必须为空（残留引用登记在 retiredV1ReferenceExceptions）：\n  %s",
			len(files), strings.Join(files, "\n  "))
	}

	// ② 实测读方（扣除已具名例外）必须为空。
	var unregistered []string
	for file := range actual {
		if _, ok := retiredV1ReferenceExceptions[file]; ok {
			continue
		}
		unregistered = append(unregistered, file)
	}
	sort.Strings(unregistered)
	if len(unregistered) > 0 {
		t.Errorf("%d 个生产文件读**已退役**的 v1 族关系（未登记为已知例外）：\n  %s\n"+
			"890 已经把它们从库里删了 ⇒ 这些代码运行时会 42P01。"+
			"处置：生产侧改指（会话族 / canonical 视图），或具名登记进 retiredV1ReferenceExceptions。",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}

	// ③ 阳性对照（扫描器不是恒空的）：例外台账登记的文件必须真的被扫到。
	//
	// ⚠ 这一条只在例外台账非空时有内容；台账清空后本条自动空转 ——
	// 那时扫描器的区分力由 TestRetiredV1ReferencesAreAllAccountedFor 的
	// 模式级对照继续守着。
	for file := range retiredV1ReferenceExceptions {
		if _, ok := actual[file]; !ok {
			t.Errorf("retiredV1ReferenceExceptions 登记了 %s，但扫描器在它身上测到 0 处 —— "+
				"要么生产侧已修（请把它从例外台账删掉），要么扫描器看不见它（扫描器坏了）。", file)
		}
	}
}

func sumInventory() int {
	n := 0
	for _, v := range requestLogsReadInventory {
		n += v
	}
	return n
}

// requestLogsReadPattern is the Go-side twin of the grep in the file comment.
// Go's regexp has no lookaround, so comment filtering happens in scanRequestLogsReaders.
//
// # 2026-10-05：`from` 独占 ⇒ 三个活着的 API 读方对本门**完全不可见**
//
// 原模式是 `from\s+_request_logs(_[a-z_]+)?\b`，即**只有 FROM 算读点**。
// 一段 SQL 只要不写 FROM、只写 JOIN，它在本门眼里就是**零读点** ——
// 不是「少算了一个」，是「整个文件不进总体」。实测抓到 3 个：
//
//	admin/session_online.go    :112  JOIN request_logs_with_current_month rl
//	admin/session_compare.go   :903  LEFT JOIN request_logs_bodies_with_current_month rb
//	admin/session_export.go    :227  LEFT JOIN request_logs_bodies_with_current_month rb
//
// 三条都是**活的查询**（不是注释、不是死代码），而 `admin/session_compare.go`
// 与 `admin/session_export.go` 是**会话导出/对比 API 仍然挂在 v1 上的唯一原因**：
// 它们的 turn 腿早已走会话族（`SessionFamilyTurnsForSessionSQL()`），
// 剩下的 bodies 腿还是 `request_logs_bodies_with_current_month`。
//
// 方向：这是**假零**。它让「_request_logs 有 106 个读方」这句话在退役清单上
// 少了三个，而少的正好是退役时才会暴露的那类（§9.199「已 repoint 的读方」
// 与「从未被分析的文件」混在一个 clean 桶里的同族，但成因在**扫描器**而不是分类器）。
//
// ⇒ 模式扩成 `from|join`。**不是**加一条「已知例外」清单——那只是把同一个洞
// 从扫描器搬到登记表，而登记表需要人记得更新；JOIN 到 v1 关系**按定义**就是
// 一个 v1 读点，没有误报空间。
//
// # 2026-10-05：同一批里的两个文件在本表**退场**（审计 §9.230）
//
// 上面那张名单里的 `admin/session_compare.go` / `admin/session_export.go`
// 在本轮被改成经 `db.SessionBodiesSourceSQL()` 取 bodies 源，于是它们源码里
// **不再有** v1 关系名的字面量 ⇒ 本表（按行正则扫字面量）测到 0 处。
//
// ⚠ **「本表测到 0」不等于「它不读 v1」**——开关默认那一臂就是
// `return "request_logs_bodies_with_current_month rb"`。把这两条从本表删掉
// 之前必须先确认它们在别处有家，否则就是 §9.45「把不知道报成安全」的同一次复发。
// ⇒ 处置按本仓**已有**的两张表分工，不新造第三套：
//
//	字面量读方 → requestLogsReadInventory（本表）
//	间接读方   → indirectRequestLogsReaders（§9.49 建的那张）
//	两者并集   → allKnownRequestLogsReaderFiles(t) 〔§9.232 起并入切换层消费点〕
//
// 与 `bg/auto_route_settle_sql.go` 完全同形：那个文件同样因为关系名在 Go
// 标识符里而不在本表，登记在间接表。⇒ 本表从 109 降到 107，**不是读方变少了**。
// `_?` 前缀（迁移 860）：v1 底表物理名已带待删除标志前缀，源码字面量同步；
// 后缀通配的既有语义不变（canonical 视图读方本来就经 `(_[a-z_]+)?` 计入）。
// ★ 2026-10-20（迁移 890，v1 族真删）：扫描口径从「任何 request_logs* 关系」
// 收窄为「**已退役的 v1 族关系**」。
//
// 原来 `(_[a-z_]+)?` 把 **canonical 视图** `request_logs_with_current_month`
// 也算成 v1 读点 —— 而 890 之后那张视图**仍然存在**，内容是会话族两臂体
// （118 列同名同序）⇒ 读它**不是**读 v1。
//
// ⇒ 「v1 读方总体」必须只由**已退役**的关系构成（retiredV1RelationNames：
// 5 张 v1 底表 × 两种拼写 + 4 张 v1-only 视图）。
//
// ⚠ 这个收窄**不是**「把扫描器改窄让它变绿」：
//   - 收窄后的集合是**更严格**的定义（只认真 v1），不是更宽；
//   - 少算的是「canonical 视图读方」——它们本来就不属于 v1 退役范围；
//   - 判据的区分力由 TestRetiredV1ReferencesAreAllAccountedFor 守着
//     （阳性对照：真 v1 必须命中；canonical 视图必须不命中）。
var requestLogsReadPattern = retiredV1ReadPattern

// sqliteStorageFace 是**与 PG 的 v1 族同名不同物**的存储面目录。
//
// `storage/sqlite/request_log_store.go` 里的 `request_logs` 是它自己那张
// **SQLite** 表（列是 request_id / tenant_id / session_id / ts / method /
// path / status_code / duration_ms / has_body，参数占位符是 `?`），
// 与 PostgreSQL 侧被 890 删除的 118 列 v1 族毫无关系。
//
// ⇒ 它命中 retiredV1RelationNames 里的 "request_logs" 是**同名假阳性**：
// 把它算成 v1 读方，会让「v1 读方总体」永远清不干净。
//
// ⚠ 为什么按**路径**排除而不是按「有没有 `?` 占位符」：
// 后者是启发式（一个 PG SQL 里出现 `?` 也可能是 JSON 操作符），
// 而前者是**结构性**事实（另一个存储引擎、另一个 schema）。
var sqliteStorageFace = "storage/sqlite/"

func scanRequestLogsReaders(t *testing.T, root string) map[string]int {
	t.Helper()
	out := map[string]int{}
	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true,
		".audit": true, ".audit-validation": true, ".audit-workspace": true}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// SQLite 存储面有自己的 request_logs 表 ⇒ 同名假阳性，排除。
		if strings.HasPrefix(rel, sqliteStorageFace) {
			return nil
		}
		n := 0
		for _, line := range strings.Split(string(src), "\n") {
			if !requestLogsReadPattern.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			// 注释不是调用点。40 条注释里 36 条是小写 `from`，
			// 不剔会得 277 而非 237（审计 §8.5）。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			n++
		}
		if n > 0 {
			out[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	return out
}

// repoRootFromCaller walks up from this file to the directory holding go.mod.
func repoRootFromCaller(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
