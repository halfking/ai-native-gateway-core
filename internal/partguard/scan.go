// Package partguard 的仓库级扫描器。
//
// R78：把「hot + columnar 分区表，更新/删除只能在 hot 表进行」这条
// objective 红线从文档变成永久守卫。
//
// 为什么需要门：这条红线**当前已经被 3 条生产路径违反**，只是因为这些
// 父表恰好还是 heap 才没有炸。db/db.go 的 promote 函数内直接
// UPDATE public.session_bodies；bg/partition_manager.go 对分区父表
// stats_event_inbox 直接 DELETE；cmd/gateway 对 public.sessions 直接
// UPDATE。一旦按 objective 把它们列存化，citus-columnar 引擎直接拒绝
// UPDATE/DELETE，三条链路同时失败。门的作用不是修这 3 条（那是架构
// 决策，须产品裁决），而是**挡住第 4 条**。
//
// 判据形态：只扫**非测试** .go 文件里的**字符串字面量**（SQL 就在其中），
// 不扫注释、不扫 SQL 迁移文件（一次性回填脚本在父表为 heap 时合法）。
// 走 go/ast 取字面量而非全文正则，是因为注释里提到表名会造成假警报，而
// 正则无法可靠区分"注释里的 UPDATE"与"语句里的 UPDATE"。
package partguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Violation 是对分区父表的一条写操作。
type Violation struct {
	File  string // 仓库相对路径，斜杠分隔
	Line  int    // 1-based
	Stmt  string // 命中的语句形态（UPDATE/DELETE/TRUNCATE）
	Table string // 被写的父表
}

// 标识符边界：靠捕获组的**贪婪**语义实现，不加额外断言。
//
// 两次踩坑都记在这里，因为它们是同一个错误的两种形态：
//   - R78 子代理用 `\b` 锚右边界 → `request_logs_hot` 的下划线被判成
//     边界，把合法的 hot 表写入误报成父表违规。
//   - 本守卫第一版用 `(?:[A-Za-z0-9_])` 当"边界" → 这是**消耗式**分组
//     不是零宽断言，等于要求表名后面还得再有一个标识符字符，于是几乎
//     永不匹配（首跑 TOTAL=0，差点把 3 条真实违规当成"仓库干净"）。
//     Go 的 RE2 不支持 lookahead (?![...])，想写零宽断言也写不了。
//
// 正确做法就是什么都不加：`([a-z_][a-z0-9_]*)` 贪婪吃到标识符自然结束
// （后跟空格/换行/别名/标点），因此 `request_logs_hot` 被整体捕获成
// `request_logs_hot`、查不在父表集合里而正确跳过。
var stmtPatterns = []struct {
	stmt  string
	regex *regexp.Regexp
}{
	{"UPDATE", regexp.MustCompile(`(?i)UPDATE\s+(?:ONLY\s+)?(?:public\.)?([a-z_][a-z0-9_]*)`)},
	{"DELETE", regexp.MustCompile(`(?i)DELETE\s+FROM\s+(?:ONLY\s+)?(?:public\.)?([a-z_][a-z0-9_]*)`)},
	{"TRUNCATE", regexp.MustCompile(`(?i)TRUNCATE\s+(?:TABLE\s+)?(?:ONLY\s+)?(?:public\.)?([a-z_][a-z0-9_]*)`)},
}

// sqliteBacked 是连 SQLite 而非 PostgreSQL 的路径。它们的表名恰好与
// PG 分区父表重名（sessions / session_turns / request_logs /
// session_turn_details），但那不是"对分区父表写"——是另一个数据库里的
// 同名单文件表。红线约束的是 PostgreSQL 侧，故整路径排除。
//
// 排除按**路径**而非按表名：SQLite 侧没有任何 PG 父表 DML，把它混进
// allowlist 会让"PG 上的违规"与"另一个库里的同名表"混在一张清单里，
// 复核时无法分辨。bg/lite_retention_worker.go 也在此列——它的文件头
// 明确声明"只持有 *sql.DB，不 import storage 包"。
var sqliteBacked = []string{
	"storage/sqlite/",
	"bg/lite_retention_worker.go",
}

func isSQLitePath(rel string) bool {
	for _, p := range sqliteBacked {
		if rel == p || strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// CollectViolations 扫描 root 下所有非测试 .go 文件，返回对分区父表的写操作。
func CollectViolations(root string) ([]Violation, error) {
	return collectViolations(root, false)
}

// collectViolations 的 includeSQLite 只服务于「排除项是否还在承重」的自测：
// 门本身永远传 false。
func collectViolations(root string, includeSQLite bool) ([]Violation, error) {
	parentSet := make(map[string]bool, len(partitionParents))
	for _, p := range partitionParents {
		parentSet[p] = true
	}

	var out []Violation
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", ".build-local", "bin", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil // 解析不了的源文件不是本门的判据范围
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !includeSQLite && isSQLitePath(rel) {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			raw, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.Contains(strings.ToUpper(raw), " ") {
				return true // SQL 一定含空格；剪掉绝大多数非 SQL 字面量
			}
			base := fset.Position(lit.Pos()).Line
			// 一个字面量可能含多行 SQL（Go 源码里的行号与 SQL 行号无关），
			// 因此按行展开后逐行匹配，保留可定位的行号。
			for i, line := range strings.Split(raw, "\n") {
				for _, sp := range stmtPatterns {
					m := sp.regex.FindStringSubmatch(line)
					if m == nil {
						continue
					}
					tbl := strings.ToLower(m[1])
					if !parentSet[tbl] {
						continue
					}
					out = append(out, Violation{
						File: rel, Line: base + i, Stmt: sp.stmt, Table: tbl,
					})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
