package routeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRootFrom 从工作目录向上找到含 go.mod 的目录。
// 不用 filepath.Abs("../..") —— 深度猜错会越过仓库根扫到同 workspace 的兄弟项目，
// 那样只会安静地给出假证据（判据锚错层级的教训）。
//
// 必须先取绝对路径：filepath.Dir(".") 恒等于 "."，从 "." 起步会立刻判定
// 「到顶了」而返回包目录（本门第一版就栽在这里，表现为三条用例全部
// lstat sql/migrations: no such file or directory）。
func repoRootFrom(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return abs
		}
		abs = parent
	}
}

// TestRoutingViewMustNotDependOnCircuitMirror 是本包的主门。
func TestRoutingViewMustNotDependOnCircuitMirror(t *testing.T) {
	root := repoRootFrom(".")
	vs, err := Scan(root)
	if err != nil {
		t.Fatalf("scan routing views: %v", err)
	}
	for _, v := range vs {
		t.Errorf("%s\n"+
			"  路由资格判定引用了内存熔断器的**观测镜像**。\n"+
			"  这两列是单向写入的（state_sync 只写不读，全仓无恢复路径），跨进程会陈旧；\n"+
			"  而熔断本身是纯内存的（重启即 Closed）。让陈旧 DB 值进入 is_routable 的\n"+
			"  单一 AND 合取，等于让它凌驾于真实熔断状态之上 —— F1 的连坐就是这么来的。",
			v)
	}
}

// TestCanonicalViewExistsAndCarriesMarker 防清单失效：
// 路径写错、文件被改名、或视图被删掉时，本包会退化成「什么都没扫」。
func TestCanonicalViewExistsAndCarriesMarker(t *testing.T) {
	root := repoRootFrom(".")
	b, err := os.ReadFile(filepath.Join(root, CanonicalView))
	if err != nil {
		t.Fatalf("读规范视图失败 %s：%v\n"+
			"  该文件缺失会让本门扫描不到任何东西 ⇒ 恒绿。", CanonicalView, err)
	}
	if !strings.Contains(string(b), RoutableMarker) {
		t.Fatalf("规范视图 %s 里找不到 %q —— 视图形态已变，\n"+
			"  请确认它是否仍是路由资格视图，并同步更新本包常量。", CanonicalView, RoutableMarker)
	}
}

// TestMigrationCoverageIsNotZero 防「只扫规范文件」的退化。
//
// sql/migrations/startup/326_* 与 460_* 都会 CREATE OR REPLACE VIEW 同一个视图，
// **迁移在部署时是真的会执行的** —— 只扫 sql/objects/ 等于给后门留了一道门。
func TestMigrationCoverageIsNotZero(t *testing.T) {
	root := repoRootFrom(".")
	files, err := RoutingViewFiles(root)
	if err != nil {
		t.Fatalf("enumerate routing view files: %v", err)
	}
	migrations := 0
	// RoutingViewFiles normalises to forward slashes, so build the prefix the
	// same way. Appending filepath.Separator to a forward-slash constant
	// produced a string ("sql/migrations\") that can never match, which is why
	// this test read zero on Windows even though the scan found 19 files.
	migPrefix := filepath.ToSlash(MigrationsDir) + "/"
	for _, f := range files {
		if strings.HasPrefix(f, migPrefix) {
			migrations++
		}
	}
	// 至少要覆盖到 326/460 这两个已知会 redefine 视图的迁移。
	if migrations < 2 {
		t.Fatalf("只发现 %d 个会 redefine 路由视图的迁移（期望 ≥2，即 326/460）。\n"+
			"  实际发现：%v\n"+
			"  迁移在部署时是会执行的，只扫 sql/objects/ 等于留了后门。",
			migrations, files)
	}
}
