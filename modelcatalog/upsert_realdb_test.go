package modelcatalog

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// upsert_realdb_test.go —— 手工建模语句（insertManualCredentialModelSQL）
// 的**真库 parse 期**门。
//
// ## 为什么需要它（2026-11-17 实测）
//
// 实测复现：`POST /api/providers/{pid}/credentials/{cid}/models`
// ⇒ HTTP 500，日志里是
// `ERROR: could not determine data type of parameter $8 (SQLSTATE 42P08)`。
//
// ★ **42P08 是 parse 期（PREPARE）缺陷** ⇒ 任何"跑一下 handler 就知道"的
//
//	门都抓不到它，除非那条门真的让 PostgreSQL 去 parse 这条语句。
//	而包里的既有单测是**结构性字符串断言 + pgxmock**（它自己的注释就写着
//	"pgxmock cannot execute SQL"）⇒ **必然漏**。
//
// ⇒ 这条门走**真库 PREPARE**：PG 在 Parse 阶段做类型推断 ⇒ 类型推不出来
//
//	就直接报错 ⇒ 抓的就是这个缺陷本身。
//
// ## 安全边界
//
//   - PREPARE 只做 parse + plan，**不执行** ⇒ **不写任何行**、不建任何对象
//     ⇒ 可以安全地指向现有库（不需要一次性隔离库）。
//   - 缺表（42P01）时**显式 SKIP 并说明**：那是 schema 太旧，不是本门的判据；
//     拿它当失败会造出一个「假红」，拿它当通过会造出一个「假绿」。
//   - 没设 DSN 时**显式 SKIP 并说明**：本腿没跑（跳过不构成证据）。
func TestManualInsertSQL_PreparesOnRealDB(t *testing.T) {
	dsn := manualInsertRealDBDSN()
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL / TEST_PG_DSN 未设置 —— " +
			"本次**没有**跑真库 PREPARE 验证（42P08 这类 parse 期缺陷只有真库抓得到）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("连不上真库（%v）⇒ 本次没跑真库 PREPARE 验证", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// ★ 用 pgx 的 Prepare（而不是 Exec）：它发的就是 Parse 消息且**不带参数
	//   类型 OID** ⇒ 由 PostgreSQL 自己推断 ⇒ 推不出来就在 Parse 阶段报
	//   42P08 ⇒ 与生产路径（QueryRow → Parse 时类型未指定）**同一条路**。
	stmt, err := conn.Prepare(ctx, "manual_insert_probe", insertManualCredentialModelSQL)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			t.Skipf("真库缺少相关表（%s）⇒ schema 太旧，本腿不执行（跳过不构成证据）", pgErr.Message)
		}
		t.Fatalf("PREPARE insertManualCredentialModelSQL 失败（这是 42P08 那类 parse 期缺陷的落点）：%v", err)
	}
	_ = stmt
	// 回收这条会话内的 prepared statement（它只活在本次会话里，不落盘）。
	_, _ = conn.Exec(ctx, "DEALLOCATE manual_insert_probe")
}

// manualInsertRealDBDSN 取真库 DSN（与仓内其它真库腿同一组变量名）。
func manualInsertRealDBDSN() string {
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_DB_URL", "TEST_PG_DSN"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}
