package admin

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

// scratch_db_test.go —— admin 包**破坏性真库门**的一次性空库分配器。
//
// ## 为什么需要它（R80 轮实测）
//
// admin 包里有两道门是**破坏性**的：它们要 `DROP TABLE IF EXISTS public.<产品表>`
// 再用夹具形态重建 ⇒ ⇒ 它们**只能在「目标表不存在」的库上跑**。
//
// ★★ R80 轮实测：这两道门**在任何产品形态库上永远 SKIP** ——
// `admin/models_modality_source_realdb_test.go` 的闸是「**8 张夹具表存在一张就 SKIP**」
// 而产品形态库里那 8 张表**永远存在**
// ⇒ ⇒ **这两道门从来没有真正跑过**（`go test` 把 SKIP 记成 PASS ⇒ 人看到的是「通过」）。
// ★★ 作者自己的注释就写着这件事：
// 「注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到『表已存在』直接 SKIP，
// **人看到的是『通过』，实际一次都没跑**」
// ⇒ ⇒ **他防住了残桩，却制造了永久 SKIP** ——
// 安全闸必须同时回答「上次跑崩留下的残桩怎么办」**和**「上次根本没跑过我看得出来吗」。
//
// ⇒ 本文件给它们一个**空的一次性库**：**库要假、表名要真**。
//
// ## 三条硬约束（与 db/scratch_db_test.go 同款）
//
//  1. **绝不 fallback 到共享库**：拿不到一次性库 ⇒ 显式 SKIP（说明本次没跑），
//     绝不改成「那就用共享库跑吧」—— 那正是本文件要消灭的行为。
//  2. **空库自证**：分配后读一次 `pg_tables`，非空就失败。夹具的出发点必须是
//     「什么都没有」，否则「DROP 掉重建」就不是它想测的那件事。
//  3. **回收**：由 internal/testdb 在 t.Cleanup 里 `DROP DATABASE … WITH (FORCE)` 回收，
//     不会攒出一堆孤儿库。
//
// ## 为什么用 internal/testdb 而不是自己拼
//
// 它已经带了三样本轮不想重写的东西：名字唯一化 + 63 字节截断、
// `mustLandInScratch`（DSN 真的落在新库上）、`WITH (FORCE)` 回收。
// 本文件只加「本包特有的两件事」：CREATEDB 预检（预检不过就**明说没跑**
// 而不是让 testdb.Fatalf）与空库自证。
//
// ★ 为什么**不**抽到 internal/testdb 去共用：db 包那份带了自己的 scratchPolicy
// （scratchFail / scratchSkip），语义不同；共用它会牵动 db 包 7 道已验收的门。
// ⇒ 宁可在本包复制一份 60 行，也不要在已验收的隔离线上做重构。

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

// scratchEmptyPool 给调用方一条**一次性空库**上的连接池。
//
// 返回的库满足：名字唯一、内容为空、与 base DSN 指向的库**不是同一个**。
// 调用方可以在里面随便 DROP/CREATE —— 那些破坏只发生在一次性库上。
//
// ★ 与 db 包那份的差别只有一个：本包的两道门**只读**、不写回产品表
// （它们 UPDATE / SELECT 自己的夹具表）⇒ ⇒ 破坏面天然更小；
// 但「目标表已存在就 SKIP」这个闸仍然会让它们在产品形态库上永远不跑
// ⇒ ⇒ 闸必须换成本文件，不能换别的。
func scratchEmptyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	baseDSN := scratchBaseDSN(t)

	if err := checkCreateDatabase(baseDSN); err != nil {
		t.Skip(err.Error() + " —— 本次**没有**跑真库行为验证（这是刻意的：宁可明说没跑，" +
			"也不退回共享库去破坏它）")
	}
	dsn := testdb.Create(t, baseDSN)

	// 空库自证：夹具的出发点必须是「什么都没有」。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接一次性库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&n); err != nil {
		t.Fatalf("空库自证失败（读 pg_tables）: %v", err)
	}
	if n != 0 {
		t.Fatalf("一次性库不是空的（public 下有 %d 张表）⇒ 夹具的出发点不对，"+
			"「DROP 掉重建」就不再是它想测的那件事", n)
	}
	return pool
}
