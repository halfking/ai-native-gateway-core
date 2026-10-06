// Package db — db_838_asr_catalog_seed_realdb_test.go
//
// ensureAsrCatalogSeed 的真库回归（2026-10-06 ASR 多模型轮）：三层种子
// （canonical → provider_models → bindings）在真实 schema 上幂等——连跑
// 两遍行数不翻倍、modality 只升不降。SQL 形状错误（列名/约束）只有真库
// 能暴露，编译期与 mock 都看不出。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 750/751 真库回归同门控）。
package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureAsrCatalogSeed_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	db := &DB{pool: pool}
	// 预清理：种子行删干净再跑（幂等断言才有意义）。
	cleanup := func(t *testing.T) {
		t.Helper()
		for _, seed := range asrCatalogSeeds {
			_, err := pool.Exec(ctx, `
				DELETE FROM credential_model_bindings b
				USING provider_models x, providers p
				WHERE b.provider_model_id = x.id AND x.provider_id = p.id
				  AND p.code = $1 AND x.raw_model_name = $2`, seed.providerCode, seed.raw)
			if err != nil {
				t.Fatalf("cleanup bindings: %v", err)
			}
			_, err = pool.Exec(ctx, `
				DELETE FROM provider_models x
				USING providers p
				WHERE x.provider_id = p.id AND p.code = $1 AND x.raw_model_name = $2`,
				seed.providerCode, seed.raw)
			if err != nil {
				t.Fatalf("cleanup provider_models: %v", err)
			}
		}
	}
	cleanup(t)

	if err := db.ensureAsrCatalogSeed(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 第二遍：幂等核心断言。
	if err := db.ensureAsrCatalogSeed(ctx); err != nil {
		t.Fatalf("second run (idempotency): %v", err)
	}

	for _, seed := range asrCatalogSeeds {
		var modality, modalitySource string
		if err := pool.QueryRow(ctx, `
			SELECT modality, COALESCE(modality_source,'') FROM models_canonical WHERE canonical_name = $1`,
			seed.canonical).Scan(&modality, &modalitySource); err != nil {
			t.Fatalf("canonical %s missing: %v", seed.canonical, err)
		}
		if modality != "audio" || modalitySource != "manual" {
			t.Fatalf("canonical %s = %s/%s, want audio/manual", seed.canonical, modality, modalitySource)
		}
		var pmCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM provider_models x JOIN providers p ON p.id = x.provider_id
			WHERE p.code = $1 AND x.raw_model_name = $2 AND x.available`,
			seed.providerCode, seed.raw).Scan(&pmCount); err != nil {
			t.Fatalf("provider_models count: %v", err)
		}
		if pmCount == 0 {
			t.Fatalf("provider_models row missing for %s/%s", seed.providerCode, seed.raw)
		}
		var bCount, dupCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*), count(*) - count(DISTINCT b.credential_id)
			FROM credential_model_bindings b
			JOIN provider_models x ON x.id = b.provider_model_id
			JOIN providers p ON p.id = x.provider_id
			WHERE p.code = $1 AND x.raw_model_name = $2`,
			seed.providerCode, seed.raw).Scan(&bCount, &dupCount); err != nil {
			t.Fatalf("bindings count: %v", err)
		}
		if bCount == 0 {
			t.Fatalf("no bindings created for %s", seed.raw)
		}
		if dupCount != 0 {
			t.Fatalf("duplicate bindings for %s: %d", seed.raw, dupCount)
		}
		// 只升不降：把 canonical 手工改成 text 后重跑必须被抬回 audio。
		if _, err := pool.Exec(ctx, `
			UPDATE models_canonical SET modality='text', modality_source=NULL WHERE canonical_name=$1`,
			seed.canonical); err != nil {
			t.Fatalf("downgrade setup: %v", err)
		}
		if err := db.ensureAsrCatalogSeed(ctx); err != nil {
			t.Fatalf("escalate run: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT modality FROM models_canonical WHERE canonical_name = $1`,
			seed.canonical).Scan(&modality); err != nil {
			t.Fatalf("recheck canonical: %v", err)
		}
		if modality != "audio" {
			t.Fatalf("canonical %s not escalated back to audio (got %s)", seed.canonical, modality)
		}
	}
	// 收尾清理（不污染本地环境目录；canonical 行保留——它是有效目录数据）。
	cleanup(t)
}
