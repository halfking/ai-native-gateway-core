package admin

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ★ 这道门存在的理由（2026-10-20 复核轮，
//
//	docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.6）：
//
//	GET /api/admin/session-analytics/users/{owner} 在**该 owner 一行会话都
//	没有**时恒 500：聚合用裸 SUM/MIN/MAX ⇒ 返回全 NULL ⇒ Scan 到非空的 Go
//	标量报 "cannot scan NULL into *int64"。
//	「没有会话」是最正常不过的状态，不该是服务端故障。
//
//	这道门逐字跑生产用的那条 SQL（包级常量）：让 PG 自己把 NULL 递给
//	Scan，看会不会炸。旧的裸 SUM 写法在这里必然炸 ⇒ 门是有效的。
func TestUserProfileStatsSQL_NullAggregatesAreCoalesced(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the endpoint works")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT to_regclass('public.session_owners') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !exists {
		t.Skip("session_owners missing in TEST_PG_URL — nothing to verify against")
	}

	// 一个**确定没有行**的 owner。
	const owner = "audit-no-such-owner-20261020"
	rows, err := conn.Query(ctx,
		fmt.Sprintf(userProfileBaseStatsSQL, "so.owner_user = $1"), owner)
	if err != nil {
		t.Fatalf("stats query failed: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("聚合查询必须返回一行（无 GROUP BY ⇒ 恒一行）")
	}
	var (
		sessionCount, totalRequests, totalSuccess, totalErrors, endUserCount int
		totalCost, avgCost                                                   float64
		firstSeenAt, lastSeenAt                                              *time.Time
	)
	if err := rows.Scan(&sessionCount, &totalRequests, &totalCost, &avgCost,
		&totalSuccess, &totalErrors, &firstSeenAt, &lastSeenAt, &endUserCount); err != nil {
		t.Fatalf("scan stats row: %v", err)
	}
	if totalRequests != 0 || totalSuccess != 0 || totalErrors != 0 {
		t.Errorf("空 owner 的聚合应全为 0：requests=%d success=%d errors=%d",
			totalRequests, totalSuccess, totalErrors)
	}
	if firstSeenAt != nil || lastSeenAt != nil {
		t.Errorf("空 owner 的时间戳应为 NULL（由调用方落成零值），got first=%v last=%v",
			firstSeenAt, lastSeenAt)
	}
}
