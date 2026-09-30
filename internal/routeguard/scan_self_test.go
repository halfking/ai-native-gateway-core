package routeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 磁盘读取型守卫不受 `go test -overlay` 影响（overlay 只影响编译），
// 所以本包的判别力由下面这组合成夹具承担，而不是靠变异真实仓内文件。
// 见 docs/audit/playbook/conventions.md §9.3 / §9.4。

// TestScanContentFlagsForbiddenColumns 证明「禁用列出现即红」这条判据
// 真的能红 —— 属于 §9.3 说的「无参数字面量不存在」型判据必须自证。
func TestScanContentFlagsForbiddenColumns(t *testing.T) {
	clean := `CREATE OR REPLACE VIEW v_routable_credential_models AS
SELECT c.id, TRUE AS is_routable
FROM credentials c
WHERE c.status = 'active' AND c.availability_state = 'ready';`
	if vs := ScanContent("clean.sql", clean); len(vs) != 0 {
		t.Fatalf("干净视图不应产生违规，实际：%v", vs)
	}

	for _, col := range ForbiddenColumns {
		dirty := "SELECT 1 FROM credentials c\nWHERE c." + col + " <> 'open'\n-- is_routable"
		vs := ScanContent("dirty.sql", dirty)
		if len(vs) != 1 {
			t.Fatalf("注入 %q 后应恰好报 1 条违规，实际 %d 条：%v", col, len(vs), vs)
		}
		if vs[0].Column != col {
			t.Fatalf("违规列应为 %q，实际 %q", col, vs[0].Column)
		}
		if vs[0].Line != 2 {
			t.Fatalf("违规行号应为 2，实际 %d", vs[0].Line)
		}
	}
}

// TestRoutingViewFilesDiscoversRedefiningMigrations 在 t.TempDir() 里造一棵
// 合成仓，验证两件事：
//  1. 内容含 is_routable 的**迁移**会被动态发现（不需要维护清单）；
//  2. 不含 is_routable 的迁移不会被误纳入（证明 marker 过滤不是恒真）。
func TestRoutingViewFilesDiscoversRedefiningMigrations(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(CanonicalView, "SELECT TRUE AS is_routable FROM credentials;")
	write(filepath.Join(MigrationsDir, "startup", "999_redefines.sql"),
		"CREATE OR REPLACE VIEW v_routable_credential_models AS SELECT TRUE AS is_routable FROM credentials;")
	write(filepath.Join(MigrationsDir, "startup", "998_unrelated.sql"),
		"-- 无关迁移：建了个不含路由资格视图的表\nCREATE TABLE t_demo(id int);")

	files, err := RoutingViewFiles(root)
	if err != nil {
		t.Fatalf("RoutingViewFiles: %v", err)
	}
	var foundMigration bool
	for _, f := range files {
		if strings.Contains(f, "999_redefines") {
			foundMigration = true
		}
		if strings.Contains(f, "998_unrelated") {
			t.Fatalf("不含 %s 的迁移被误纳入：%v", RoutableMarker, files)
		}
	}
	if !foundMigration {
		t.Fatalf("含 %s 的 redefine 迁移未被动态发现：%v", RoutableMarker, files)
	}

	// 再往该迁移里注入 circuit_state，Scan 必须抓到。
	write(filepath.Join(MigrationsDir, "startup", "999_redefines.sql"),
		"CREATE OR REPLACE VIEW v_routable_credential_models AS\n"+
			"SELECT TRUE AS is_routable FROM credentials c WHERE c.circuit_state <> 'open';")
	vs, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(vs) != 1 || vs[0].Column != "circuit_state" {
		t.Fatalf("迁移里注入 circuit_state 后应报 1 条违规，实际：%v", vs)
	}
	if !strings.Contains(vs[0].File, "999_redefines") {
		t.Fatalf("违规应归因到该迁移文件，实际归因 %q", vs[0].File)
	}
}
