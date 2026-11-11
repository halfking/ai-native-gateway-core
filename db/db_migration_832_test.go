package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigration832EnsureWiredAndMirrorsFile 钉住 2026-11-11（48h 审计）补的
// 启动自愈：migration 832 在 211 被文件链版本水位线跳过（schema_migrations
// 已到 956 而 832 缺席）⇒ 漂移对账 worker 写健康面 42P01（8 次/24h）。
// 门：① ensure 函数存在且接线在 applyMigrationsOnce；② DDL 与迁移文件同形
//（表名/约束/部分索引）；③ 幂等（IF NOT EXISTS）；④ stamp schema_migrations 832。
func TestMigration832EnsureWiredAndMirrorsFile(t *testing.T) {
	text, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}

	const wiring = "db.ensureModelBaselineObservationHealth(migCtx)"
	if !strings.Contains(string(text), wiring) {
		t.Fatal("applyMigrationsOnce missing ensureModelBaselineObservationHealth wiring")
	}

	const sqlConst = "func (d *DB) ensureModelBaselineObservationHealth("
	fnStart := strings.Index(string(text), sqlConst)
	if fnStart < 0 {
		t.Fatal("db.go missing ensureModelBaselineObservationHealth wrapper")
	}
	nextFn := strings.Index(string(text)[fnStart+1:], "func (d *DB) ")
	if nextFn < 0 {
		t.Fatal("db.go malformed: no function after ensureModelBaselineObservationHealth")
	}
	body := string(text)[fnStart : fnStart+1+nextFn]

	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS public.model_baseline_price_observation_health") {
		t.Fatal("ensure SQL must CREATE TABLE IF NOT EXISTS (幂等)")
	}
	if !strings.Contains(body, "model_baseline_price_observation_health_nonneg") {
		t.Fatal("ensure SQL must carry the nonneg CHECK constraint")
	}
	if !strings.Contains(body, "idx_model_baseline_price_observation_health_stale") {
		t.Fatal("ensure SQL must carry the stale partial index")
	}
	if !strings.Contains(body, "VALUES ('832',") {
		t.Fatal("ensure SQL must stamp schema_migrations version 832")
	}
	if !strings.Contains(body, `d.tableExists(ctx, "model_baseline_price_observation_health")`) {
		t.Fatal("ensure must short-circuit on existing table (catalog probe)")
	}

	// 与迁移文件同形：关键 DDL 片段必须能在迁移文件里找到（防漂移）。
	migBytes, err := os.ReadFile("../sql/migrations/startup/832_model_baseline_observation_health.sql")
	if err != nil {
		t.Fatal(err)
	}
	mig := string(migBytes)
	for _, frag := range []string{
		"CREATE TABLE IF NOT EXISTS public.model_baseline_price_observation_health",
		"model_baseline_price_observation_health_nonneg",
		"idx_model_baseline_price_observation_health_stale",
	} {
		if !strings.Contains(mig, frag) {
			t.Fatalf("migration file lacks fragment that ensure carries: %s", frag)
		}
	}
}
