// Package routeguard 的路由门控守卫。
//
// R87-f：把「路由资格判定**不得**依赖内存熔断器的观测镜像」变成永久守卫。
//
// ## 为什么需要这道门
//
// R83b/F1 已经证明 `v_routable_credential_models.sql:17` 的 `is_routable`
// 是**单一 AND 合取**——合取项越加越容易出现「一项出问题、全凭据连坐」。
// F1 就是这样造成的：`health_status='warning'` 不在 `{healthy,unknown}` 里，
// 于是整个凭据的 14 个绑定里 6 个可路由的**一起出局**。
//
// R87-f 核实到：`credentials.circuit_state` / `cooling_until` 是**内存熔断器
// 的单向观测镜像**（`state_sync.go` 只写不读，全仓无恢复路径），而且
// **它今天不在路由视图里**。这是对的——熔断是纯内存状态机，DB 里的陈旧值
// 不得决定摘流。
//
// 但这两列**长得就像路由输入**（在 `credentials` 表上、有时间戳、名字里带
// circuit/cooling）。任何人「顺手」把 `circuit_state = 'open'` 写进那个合取，
// 就会让**重启后陈旧的 DB 值直接决定摘流**——这正是 65 号 §3 那个 P3
// 从「只影响观测」升级成「影响路由」的路径。
//
// ## 覆盖面：规范对象文件 + 所有重新定义该视图的迁移
//
// 只扫 `sql/objects/views/` 是不够的：`sql/migrations/startup/326_*` 与
// `460_*` 都会 `CREATE OR REPLACE VIEW` 同一个视图，**迁移在部署时是真的会
// 执行的**。所以迁移目录按「内容含 is_routable」**动态发现**，未来新增的
// 迁移自动纳入，无需维护清单（也就没有清单腐化的问题）。
package routeguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalView 是路由资格视图的规范对象文件。
const CanonicalView = "sql/objects/views/v_routable_credential_models.sql"

// MigrationsDir 是会 (re)define 路由视图的迁移目录。
const MigrationsDir = "sql/migrations"

// RoutableMarker 是视图的身份标记：文件必须含它才算「路由资格视图」。
// 用来同时防「清单写错路径」与「文件被改名/删除」两种失效。
const RoutableMarker = "is_routable"

// ForbiddenColumns 是**不得**出现在路由资格判定里的列。
//
// 理由：它们是内存熔断器的单向观测镜像（只写不读、跨进程会陈旧）。
// 让它们进入 is_routable 的 AND 合取，等于让「陈旧的 DB 值」凌驾于
// 「进程内真实的熔断状态」之上 —— 而熔断本来就是纯内存的，重启即重置。
var ForbiddenColumns = []string{
	"circuit_state",
	"cooling_until",
}

// Violation 是一条门控违规。
type Violation struct {
	File   string
	Column string
	Line   int
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d 路由资格判定引用了 %q", v.File, v.Line, v.Column)
}

// ScanContent 返回 content 中命中的禁用列（行号从 1 起）。
func ScanContent(file, content string) []Violation {
	var out []Violation
	lines := strings.Split(content, "\n")
	for _, col := range ForbiddenColumns {
		for i, line := range lines {
			if strings.Contains(line, col) {
				out = append(out, Violation{File: file, Column: col, Line: i + 1})
			}
		}
	}
	return out
}

// RoutingViewFiles 返回 root 下所有「路由资格视图」文件：
// 规范对象文件 + 迁移目录里内容含 RoutableMarker 的 .sql。
func RoutingViewFiles(root string) ([]string, error) {
	files := []string{CanonicalView}
	absMig := filepath.Join(root, MigrationsDir)
	err := filepath.WalkDir(absMig, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(b), RoutableMarker) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			// ToSlash so a caller comparing against a forward-slash constant
			// (MigrationsDir) can match. Native separators made the coverage
			// test count zero migrations on Windows while the scan itself
			// worked fine — a loud guard reporting a false alarm.
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Scan 扫描 root 下所有路由资格视图，返回违规列表。
func Scan(root string) ([]Violation, error) {
	files, err := RoutingViewFiles(root)
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, rel := range files {
		b, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			return nil, readErr
		}
		out = append(out, ScanContent(rel, string(b))...)
	}
	return out, nil
}
