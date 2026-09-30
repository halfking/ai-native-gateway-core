// Round 43: sql/objects/ 的定位与依赖守卫。
//
// 调查问题（连续两轮挂在待办里）：sql/objects/ 到底是 SSOT、还是历史归档？
// 结论是**两者都是，且这个双重身份正是风险来源**：
//
//  1. 对**静态分析门**它是 SSOT（承重）。
//     internal/dbx/jsonb_param_static_test.go 用正则解析
//     sql/objects/tables/*.sql 构建「每表已知 jsonb 列」映射，
//     驱动整个包的静态 lint；admin/providers_schema_contract_test.go 直接
//     读 sql/objects/tables/providers.sql 当契约。
//     若该目录消失，jsonbReCreateTable 匹配不到任何表，列映射退化为空，
//     **静态 lint 会静默退化成「什么都检查不到」而依然全绿** —— 典型假绿。
//
//  2. 对**部署**它是惰性的。
//     全仓唯一引用是 deploy/sql/sync-objects.sh，它只做单向文件拷贝
//     （sql/objects → deploy/sql/objects）并写 README 让人手动 psql -f。
//     没有任何流水线会 apply 它；而且 deploy/sql/objects/ 目录根本不存在，
//     说明该脚本从未被运行过。
//
//  3. 它还是**部分基线对象的唯一出处**：本轮对账里那 5 个
//     「不在源库、无迁移可重建」的对象，在本目录里都有逐对象 DDL。
//
// 于是 schema 在仓里有**四种表示**（迁移目录 / 01-schema 三份副本 /
// sql/objects / deploy/sql/objects 镜像），而只有前两种有功能路径。
// 本守卫至少守住第 1 条（承重那条），不让它被当成可清理的冗余目录删掉。
package schema

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const objectsRoot = "../../sql/objects"

// TestSqlObjectsIsPresentForStaticAnalysisGates pins the load-bearing role.
// The failure mode this prevents is subtle and silent: with the directory gone,
// the jsonb column map is empty, the static lint checks nothing, and the suite
// still reports ok.
func TestSqlObjectsIsPresentForStaticAnalysisGates(t *testing.T) {
	for _, sub := range []string{"tables", "indexes", "constraints", "views", "functions"} {
		dir := filepath.Join(objectsRoot, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("sql/objects/%s 不可读：%v\n"+
				"  该目录是 internal/dbx 静态 jsonb lint 的列映射来源；"+
				"删除它会让该 lint 静默退化成「零检查」而依然全绿", sub, err)
			continue
		}
		if len(entries) == 0 {
			t.Errorf("sql/objects/%s 为空", sub)
		}
	}
}

// TestJsonbLintParsesObjectTables binds the dependency explicitly: the lint
// must keep reading sql/objects/tables/*.sql. If someone "cleans up" the
// static analysis to read the baseline or the live database instead, this
// fires so the change is deliberate — a different column source changes what
// the lint can see.
func TestJsonbLintParsesObjectTables(t *testing.T) {
	b, err := os.ReadFile("../../internal/dbx/jsonb_param_static_test.go")
	if err != nil {
		t.Fatalf("read jsonb static test: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "sql/objects/tables/") {
		t.Error("internal/dbx 的静态 jsonb lint 不再从 sql/objects/tables/*.sql 取列映射；" +
			"若这是有意改用别的来源，请同步更新 TestSqlObjectsIsPresentForStaticAnalysisGates " +
			"并复核该 lint 的检查面是否变窄")
	}
	// The map must be built by actually parsing those files, not hardcoded.
	if !regexp.MustCompile(`(?s)sql/objects/tables/.*ReadDir|filepath\.Glob.*sql/objects`).MatchString(src) {
		t.Log("注意：未见显式的目录遍历/Glob，请确认 jsonb 列映射确实由文件内容解析而来")
	}
}

// TestObjectsDirIsNotAppliedAnywhere records the negative fact that matters for
// baseline work: sql/objects/ is NOT part of any deployment path. The only
// reference is a one-way file copy whose target directory does not exist.
//
// This matters because it is easy to assume "the object files must be applied
// somewhere" and to treat them as a migration source. They are not. A baseline
// rebuilt from them would carry objects that no pipeline has ever applied.
func TestObjectsDirIsNotAppliedAnywhere(t *testing.T) {
	b, err := os.ReadFile("../../deploy/sql/sync-objects.sh")
	if err != nil {
		t.Skipf("sync-objects.sh 不可读，跳过；这不构成「sql/objects 未被应用」的证据: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "cp -f") && !strings.Contains(src, "rsync") {
		t.Error("sync-objects.sh 不再是纯拷贝；若它开始 apply 对象，" +
			"本守卫的结论（objects 对部署惰性）需重估")
	}
	// And the copy target is absent, i.e. the mirror was never materialised.
	if _, err := os.Stat("../../deploy/sql/objects"); err == nil {
		t.Log("deploy/sql/objects 现已存在；该镜像与 sql/objects 的漂移需要单独守卫")
	} else {
		t.Log("deploy/sql/objects 不存在 —— 同步脚本从未产出过镜像（与「objects 对部署惰性」一致）")
	}
}
