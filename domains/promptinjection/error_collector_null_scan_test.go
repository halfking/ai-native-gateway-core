package promptinjection

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// ══════════════════════════════════════════════════════════════════════
// 2026-11-18 根修钉测：队列领取对 NULL jsonb 列必须健壮（真库门）。
//
// 生产实证（245）：QueuedError 的五个 jsonb 字段是 json.RawMessage，
// database/sql 对「NULL → *json.RawMessage」报 Scan error。生产 Collect
// 会写满这些列，但手工入队/历史行/其它写入方可以留 NULL ⇒ 一行 NULL
// 就把整批领取炸掉 ⇒ 错误卡死在 processing。修法＝SELECT 侧
// COALESCE(col, 'null'::jsonb)。本门在真库上插一行**五列全 NULL** 的行，
// 领取必须成功且 RawMessage 读回 "null"。
// ══════════════════════════════════════════════════════════════════════

func TestClaimPendingErrorsToleratesNullJSONColumns(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that NULL jsonb rows claim cleanly")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.error_analysis_queue') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Skip("error_analysis_queue missing in TEST_PG_URL — nothing to verify against")
	}

	// 夹具纪律：事务内插行 + 领取，结尾回滚 ⇒ 不留数据。
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO error_analysis_queue
			(tenant_id, error_type, error_hash, error_message, status, priority, retry_count)
		VALUES ('null-scan-test', 'llm_detection_failed', 'nullscan-' || md5(random()::text),
		        'null jsonb columns scan fixture', 'pending', 9, 0)
		RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// 用与生产同一条 COALESCE 化的领取 SQL（经 *sql.DB 走 stdlib 驱动，
	// 复现 database/sql 的 Scan 行为——pgx 原生路径对 NULL 的处理不同，
	// 测不到这个 bug）。
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	sqlDB := stdlib.OpenDB(*cfg)
	defer sqlDB.Close()

	ec := &ErrorCollector{db: sqlDB}
	claimed, err := ec.ClaimPendingErrors(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPendingErrors failed on NULL jsonb row: %v — batch would be stuck in processing", err)
	}
	var hit *QueuedError
	for _, qe := range claimed {
		if qe.ID == id {
			hit = qe
			break
		}
	}
	if hit == nil {
		t.Fatalf("seeded row id=%d not claimed (claimed=%d rows)", id, len(claimed))
	}
	if string(hit.ErrorDetails) != "null" || string(hit.Metadata) != "null" {
		t.Fatalf("COALESCE shape wrong: error_details=%q metadata=%q (want \"null\")",
			string(hit.ErrorDetails), string(hit.Metadata))
	}
	_ = time.Now // keep time import if assertions change
}
