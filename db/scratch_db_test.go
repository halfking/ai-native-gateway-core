package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

// scratch_db_test.go —— 真库门的**一次性空库**分配器。
//
// ## 为什么需要它（2026-10-21 R78 实测）
//
// db 包里有一批真库门是**破坏性**的：它们先 `DROP TABLE IF EXISTS public.<产品表>`
// 再用**玩具形态**重建（夹具只需要几列）。这些门原本直连 `TEST_DATABASE_URL`
// 指向的库，于是「跑一次 db 包」就等于「把形态库洗成玩具形态」。
//
// R78 轮实测（干净库 vs 跑过 db 包的库，同一套量具）：
//
//	关系数 2436 → 2296（−140）
//	被毁对象 7 表 + 25 视图 + 5 物化视图 + 103 索引 + 5 序列
//	掉列的表 12 张（credential_model_bindings 37 列、credentials 91、
//	           models_canonical 44、session_summaries 57、session_turns_hot 108 …）
//	被换成玩具形态的表 4 张（goal_sessions / models_canonical /
//	           session_summaries / _request_logs）
//
// ⇒ ⇒ **后果不是「库脏了」，是「后面的门全假红」**：被毁的表让别的门报
// 「relation does not exist」，被换形的表让别的门读到少得离谱的列。
// ⇒ ⇒ 所以真库门必须**自己带一个库**（库要假、表名要真）。
//
// ★ R78 轮订正：破坏源**不止一个**。实测四个文件共 7 个门直连 DSN 库并做破坏性 DDL：
//
//	db/r1039_request_logs_provider_id_column_test.go（3 个门）
//	db/startup_ddl_guards_realdb_test.go（1 个门，毁 6 张产品表 ⇒ CASCADE 25 视图）
//	db/startup_ddl_guards_earlier_realdb_test.go（1 个门，毁 maas_*/models_canonical）
//	db/db_745_ensure_realdb_test.go（2 个门，毁 report_snapshots）
//
// ## 三条硬约束
//
//  1. **绝不 fallback 到共享库**：拿不到一次性库 ⇒ 显式 SKIP（说明本次没跑），
//     绝不改成「那就用共享库跑吧」—— 那正是本文件要消灭的行为。
//  2. **空库自证**：分配后读一次 `pg_tables`，非空就失败。夹具的出发点
//     必须是「什么都没有」，否则「DROP 掉重建」就不是它想测的那件事。
//  3. **回收**：一次性库由 internal/testdb 在 t.Cleanup 里
//     `DROP DATABASE … WITH (FORCE)` 回收，不会攒出一堆孤儿库。
//
// ## 为什么用 internal/testdb 而不是自己拼
//
// 它已经带了三样本轮不想重写的东西：名字唯一化 + 63 字节截断、
// `mustLandInScratch`（DSN 真的落在新库上）、`WITH (FORCE)` 回收。
// 本文件只加「本包特有的两件事」：CREATEDB 预检（预检不过就明说没跑
// 而不是让 testdb.Fatalf）与空库自证。

// scratchPolicy：拿不到一次性库时怎么办。
//
//	scratchSkip —— 明说「本次没有跑真库行为验证」（真库门缺环境时的常态）。
//	scratchFail —— 硬失败（门的契约要求「坏 DSN 要失败而不是跳过」时用）。
type scratchPolicy int

const (
	scratchSkip scratchPolicy = iota
	scratchFail
)

// scratchBaseDSN 解析真库门的 base DSN。
//
// ⚠ base DSN **只用来派生一次性库**，本身不是被测连接。
// 没有它 ⇒ 显式 SKIP，并说明本次没有跑真库行为验证（不是安静地跳过）。
func scratchBaseDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_DB_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本次**没有**跑真库行为验证（只跑了源码级判据）")
	return ""
}

// checkCreateDatabase 预检当前角色有没有 CREATEDB。
//
// 为什么预检：internal/testdb.Create 在建库失败时是 t.Fatalf。
// 一个**权限受限**的 CI 角色会把「跑不了」变成「红门」，那是假红。
// 但也不能因此 fallback 到共享库（见文件头约束 1）⇒ 只能明说没跑。
func checkCreateDatabase(baseDSN string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return errors.New("连接 base DSN 失败: " + err.Error())
	}
	defer pool.Close()
	var can bool
	if err := pool.QueryRow(ctx,
		`SELECT rolcreatedb FROM pg_roles WHERE rolname = current_user`).Scan(&can); err != nil {
		return errors.New("查询 current_user 的 CREATEDB 失败: " + err.Error())
	}
	if !can {
		return errors.New("当前角色没有 CREATEDB —— 一次性库建不出来")
	}
	return nil
}

// scratchDSNFor 从 baseDSN 派生一个**一次性空库**，返回指向它的 DSN。
//
// 分配成功后做一次空库自证（public 下必须一张表都没有）。
func scratchDSNFor(t *testing.T, baseDSN string, policy scratchPolicy) string {
	t.Helper()
	if err := checkCreateDatabase(baseDSN); err != nil {
		msg := err.Error() + " —— 本次**没有**跑真库行为验证（这是刻意的：宁可明说没跑，" +
			"也不退回共享库去破坏它）"
		if policy == scratchFail {
			t.Fatal(msg)
		}
		t.Skip(msg)
		return ""
	}
	dsn := testdb.Create(t, baseDSN)

	// 空库自证：夹具的出发点必须是「什么都没有」。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接一次性库失败: %v", err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&n); err != nil {
		t.Fatalf("空库自证失败（读 pg_tables）: %v", err)
	}
	if n != 0 {
		t.Fatalf("一次性库不是空的（public 下有 %d 张表）⇒ 夹具的出发点不对，"+
			"「DROP 掉重建」就不再是它想测的那件事", n)
	}
	return dsn
}

// scratchPool 给调用方一条**一次性空库**上的连接池。
//
// 返回的库满足：名字唯一、内容为空、与 base DSN 指向的库**不是同一个**。
// 调用方可以在里面随便 DROP/CREATE —— 那些破坏只发生在一次性库上。
func scratchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := scratchDSNFor(t, scratchBaseDSN(t), scratchSkip)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接一次性库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// scratchDSN 从连接池反推它连的那个库的 DSN。
//
// 用途：有的门需要**第二条连接**（并发持锁、设 lock_timeout）。
// 它必须指向同一个一次性库 —— 从 pool 反推而不是另存一份，
// 免得两处真相不一致（那正是 internal/testdb 注释里记过的翻车形态）。
func scratchDSN(pool *pgxpool.Pool) string {
	return pool.Config().ConnString()
}
