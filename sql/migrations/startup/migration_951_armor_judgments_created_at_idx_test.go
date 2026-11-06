package startup

// migration_951_armor_judgments_created_at_idx_test.go —— 迁移 951 的契约门。
//
// 951 给 armor_judgments 补 (created_at) 单列前导索引，根修
// bg/audit_trimmer.go 90d retention DELETE 批的「整条复合索引顺扫」
// （R189 252 SQL 审计轮：quiet EXPLAIN ANALYZE 3,689ms/批 —— 内层
// SELECT 只按 created_at 过滤，而表上唯一的 created_at 索引
// idx_armor_judgments_tenant_time 以 tenant_id 为前导，无边界可落）。
//
// 四件（882 契约门同款）：
//
//	① 形态门：CREATE INDEX IF NOT EXISTS 的表/索引名/键序 —— 键序必须
//	  是裸 (created_at)；改成 (tenant_id, created_at) 或
//	  (created_at DESC) 都会让 trim 谓词重新失去边界 ⇒ 门红。
//	② down 门：down 删同名索引。
//	③ cross-pin 门：trim 侧 SQL（bg/audit_trimmer.go）仍读
//	  armor_judgments 且谓词仍是 created_at < NOW() - —— 若 trim 形状
//	  漂移（比如换了列名），索引就不再服务它 ⇒ 门红。
//	④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致
//	  （951.sql 与 951.down.sql 各一份）。
//
// 判据纪律：Go 侧读 SQL 用字符串字面量提取（同 882 门），判「提到关系」
// 按标识符精确匹配不做子串猜。

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig951Name     = "951_armor_judgments_created_at_idx.sql"
	mig951DownName = "951_armor_judgments_created_at_idx.down.sql"
)

func mig951Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig951Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// ① 形态门：裸 (created_at) 键序 + IF NOT EXISTS 幂等 + lock guard（878 款）。
func TestMigration951ArmorJudgmentsCreatedAtIdxShape(t *testing.T) {
	root := mig951Tree(t)
	sql := mig951Read(t, root, filepath.Join("sql", "migrations", "startup", mig951Name))

	// 去注释后比对（-- 行注释），避免头注里的示例文本顶成断言命中。
	var codeLines []string
	for _, ln := range strings.Split(sql, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		codeLines = append(codeLines, ln)
	}
	code := strings.Join(codeLines, "\n")
	// 压平空白，做形态级（非字节级）比对。
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(code, " ")

	if !strings.Contains(flat,
		"CREATE INDEX IF NOT EXISTS idx_armor_judgments_created_at ON public.armor_judgments (created_at);") {
		t.Fatalf("951 形态漂移：必须恰好是\n"+
			"  CREATE INDEX IF NOT EXISTS idx_armor_judgments_created_at\n"+
			"      ON public.armor_judgments (created_at);\n"+
			"（裸单列键序是本迁移的全部意义；改键序/改索引名/加谓词都会让 "+
			"bg/audit_trimmer.go 的 created_at 范围谓词重新失去边界）\n实际：%s", flat)
	}
	if !strings.Contains(flat, "SET LOCAL lock_timeout = '60s';") {
		t.Fatalf("951 缺 lock_timeout（878 款：>870 强锁 DDL 必须带 lock_timeout）")
	}
}

// ② down 门：同名 DROP INDEX。
func TestMigration951DownDropsSameIndex(t *testing.T) {
	root := mig951Tree(t)
	down := mig951Read(t, root, filepath.Join("sql", "migrations", "startup", mig951DownName))
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_armor_judgments_created_at") {
		t.Fatalf("951.down 必须删同名索引 idx_armor_judgments_created_at，实际：%s", down)
	}
}

// ③ cross-pin 门：trim 侧 SQL 形状未漂移（索引服务的谓词还在）。
func TestMigration951TrimmerPredicateCrossPin(t *testing.T) {
	root := mig951Tree(t)
	goSrc := mig951Read(t, root, filepath.Join("bg", "audit_trimmer.go"))

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "bg", "audit_trimmer.go"), goSrc, 0)
	if err != nil {
		t.Fatalf("parse audit_trimmer.go: %v", err)
	}
	var literals []string
	ast.Inspect(f, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			literals = append(literals, bl.Value)
		}
		return true
	})
	joined := strings.Join(literals, "\n")

	if !strings.Contains(joined, "DELETE FROM armor_judgments") {
		t.Fatalf("trim 侧已不再读 armor_judgments？951 索引的谓词主体消失了 —— " +
			"请按「测试处置三分法」重新裁决这道门（882 门 2026-10-20 改题先例）")
	}
	if !regexp.MustCompile(`created_at\s*<\s*NOW\(\)`).MatchString(joined) {
		t.Fatalf("trim 侧谓词不再按 created_at 过滤：951 索引不再服务它，门红")
	}
}

// ④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致。
func TestMigration951EmbeddataByteMatch(t *testing.T) {
	root := mig951Tree(t)
	for _, name := range []string{mig951Name, mig951DownName} {
		canon := mig951Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embed := mig951Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer",
			"embeddata", "startup", name))
		hc := sha256.Sum256([]byte(canon))
		he := sha256.Sum256([]byte(embed))
		if hex.EncodeToString(hc[:]) != hex.EncodeToString(he[:]) {
			t.Fatalf("%s canonical 与 embeddata 两份字节不一致（sha %s vs %s）——"+
				"embed 通道会静默投递另一份迁移", name,
				hex.EncodeToString(hc[:])[:12], hex.EncodeToString(he[:])[:12])
		}
	}
}
