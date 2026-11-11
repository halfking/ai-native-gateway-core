package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration833EnsureWiredAndMirrorsFile 钉住 2026-11-11（48h 审计）补的
// 启动自愈：migration 833 与 832 同被文件链版本水位线跳过（211 实测
// pg_constraint 里四个 cmb_price_nonneg_* 0 条）⇒ 供应商价格列的负价新写入
// 不被拒。门：① ensure 存在且接线在 applyMigrationsOnce；② 四个约束镜像
// 文件的 NOT VALID 形态；③ 幂等（DROP IF EXISTS + ADD + 表存在短路）；④
// stamp schema_migrations 833；⑤ 关键片段与迁移文件同形（防漂移）。
func TestMigration833EnsureWiredAndMirrorsFile(t *testing.T) {
	text, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}

	const wiring = "db.ensureSupplierPriceNonnegCheckConstraints(migCtx)"
	if !strings.Contains(string(text), wiring) {
		t.Fatal("applyMigrationsOnce missing ensureSupplierPriceNonnegCheckConstraints wiring")
	}

	const sqlConst = "func (d *DB) ensureSupplierPriceNonnegCheckConstraints("
	fnStart := strings.Index(string(text), sqlConst)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureSupplierPriceNonnegCheckConstraints wrapper")
	}
	nextFn := strings.Index(string(text)[fnStart+1:], "func (d *DB) ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureSupplierPriceNonnegCheckConstraints")
	}
	body := string(text)[fnStart : fnStart+1+nextFn]

	if !strings.Contains(body, `d.tableExists(ctx, "credential_model_bindings")`) {
		t.Fatal("ensure must short-circuit when the bindings table is absent (catalog probe)")
	}
	for _, name := range []string{
		"cmb_price_nonneg_in",
		"cmb_price_nonneg_out",
		"cmb_price_nonneg_cache_read",
		"cmb_price_nonneg_cache_write",
	} {
		drop := "DROP CONSTRAINT IF EXISTS " + name
		add := "ADD CONSTRAINT " + name
		notValid := ">= 0) NOT VALID"
		if !strings.Contains(body, drop) {
			t.Fatalf("ensure SQL must %s (幂等重放)", drop)
		}
		if !strings.Contains(body, add) {
			t.Fatalf("ensure SQL must %s", add)
		}
		if !strings.Contains(body, notValid) {
			t.Fatal("ensure SQL must carry NOT VALID (不扫存量 ⇒ 不可能因存量数据中止 boot)")
		}
	}
	if !strings.Contains(body, `stampSchemaMigration(ctx, "833"`) {
		t.Fatal("ensure must stamp schema_migrations version 833 (含约束已齐的短路分支)")
	}

	// 与迁移文件同形：关键 DDL 片段必须能在迁移文件里找到（防漂移）。
	migBytes, err := os.ReadFile("../sql/migrations/startup/833_supplier_price_nonneg_check.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	for _, frag := range []string{
		"ADD CONSTRAINT cmb_price_nonneg_in",
		"ADD CONSTRAINT cmb_price_nonneg_out",
		"ADD CONSTRAINT cmb_price_nonneg_cache_read",
		"ADD CONSTRAINT cmb_price_nonneg_cache_write",
		"NOT VALID",
	} {
		if !strings.Contains(mig, frag) {
			t.Fatalf("migration file lacks fragment that ensure carries: %s", frag)
		}
	}
}
