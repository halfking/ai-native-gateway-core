// 「隔离 schema 残留判据」的**自证**（阳性对照 + 阴性对照）。
//
// ★ 为什么必须有自证：残留判据被**收窄**了（从【全库 dlperf_*】收到
//
//	【本次运行自己创建的 schema 集合】）⇒ 最大的风险是**收窄过头 ⇒ 判据失效
//	⇒ 残留再也抓不到**。「不再误红」与「还能抓到自己」是两件事，各要有证据：
//
// ① 阳性对照（TestDataLifecycleStorageResidueScopePositive）
//
//	人为造一个**不属于本次运行**的 dlperf_* schema ⇒ 判据必须**仍然绿**
//	（证明判据不再被环境脏影响）。
//
// ② 阴性对照（TestDataLifecycleStorageResidueScopeNegative）
//
//	人为留一个**属于本次运行**的 schema 残留 ⇒ 判据必须**报红**
//	（证明判据没有失效）。
//
// ★ 两条缺一不可：只有 ① ⇒ 可能是「判据被关掉了」；只有 ② ⇒ 可能是「判据只剩
//
//	查全库」照样会红，但分不出是谁的。
//
// 运行（真库）：
//
//	TEST_DATABASE_URL="postgres://…@127.0.0.1:54318/llm_gateway?sslmode=disable" \
//	go test ./admin -run 'DataLifecycleStorageResidueScope' -count=1 -v
//
// 未设 DSN 时**显式 skip**（skip 不等于通过：真库腿没跑就说没跑）。
package admin

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dlPerfFakeReporter 收集断言消息用。
//
// ★ 自证测试要「验证判据会红」，但不能真的让用例红 ⇒ 把断言的**落点**
//
//	（*testing.T）换成收集器；**判据本身（dlPerfAssertNoResidue）是同一条代码路径**。
type dlPerfFakeReporter struct {
	msgs []string
}

func (f *dlPerfFakeReporter) Errorf(format string, args ...any) {
	f.msgs = append(f.msgs, fmt.Sprintf(format, args...))
}

// dlPerfSelfTestPool 连真库；没配 DSN 就显式 skip（说明这一腿没验到）。
func dlPerfSelfTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := dlPerfTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// TestDataLifecycleStorageResidueScopePositive 阳性对照：
// 库里存在**不属于本次运行**的 dlperf_* schema 时，残留判据必须仍然绿。
//
// 这正是原病灶的现场：并行会话 / 上一轮 panic 的残骸 / 别人手工建的 schema，
// 会让旧判据（查全库 dlperf_*）误红，而那个红与被测代码无关。
func TestDataLifecycleStorageResidueScopePositive(t *testing.T) {
	ctx := context.Background()
	pool := dlPerfSelfTestPool(t)
	// ★ 必须用 t.Cleanup 而不是 defer：t.Cleanup 在**测试函数返回之后**才跑，
	//   defer pool.Close() 会在 cleanup 之前把池关掉 ⇒ cleanup 里报 "closed pool"。
	//   且注册在最前 ⇒ LIFO 下它**最后**跑 ⇒ 本测试自己的删库动作仍有池可用。
	t.Cleanup(pool.Close)

	// ① 造一个「不属于本次运行」的 dlperf_* schema（名字里没有本次运行令牌）。
	foreign := "dlperf_foreign_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+foreign); err != nil {
		t.Fatalf("create foreign schema %s: %v", foreign, err)
	}
	// ★ 注册顺序 = LIFO 执行顺序：本条 cleanup 必须**晚于** helper 的 cleanup 执行，
	//   否则 helper 检查时外来 schema 已被删掉 ⇒ 阳性对照证明不了任何东西。
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(dctx, "DROP SCHEMA IF EXISTS "+foreign+" CASCADE"); err != nil {
			t.Errorf("drop foreign schema %s: %v", foreign, err)
		}
	})

	// ② 证明「旧判据（查全库 dlperf_*）在这个库上确实是红的」——
	//    这就是原判据误红的真实成因，别让阳性对照变成空转。
	var legacy int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'dlperf\\_%'").Scan(&legacy); err != nil {
		t.Fatalf("legacy residue probe: %v", err)
	}
	if legacy == 0 {
		t.Fatalf("前提不在场：库里一个 dlperf_* 都没有 ⇒ 阳性对照证明不了什么")
	}

	// ③ 走真 helper：它的 cleanup 里跑的就是新判据 ⇒ 这里仍绿
	//    ⇒ 判据不再被环境脏影响（旧判据在这里必红）。
	_, _, schema := newStoragePerfHandler(t)

	// ④ 显式复核：新判据的集合里**没有**外来的 schema，
	//    而**有**本次运行自己建的（此刻还没到 cleanup，它当然还在）。
	residue, err := dlPerfRunResidueNames(ctx, pool)
	if err != nil {
		t.Fatalf("run residue probe: %v", err)
	}
	if slices.Contains(residue, foreign) {
		t.Errorf("阳性对照失败：新判据把不属于本次运行的 schema %s 算成了残留（residue=%v）", foreign, residue)
	}
	if !slices.Contains(residue, schema) {
		t.Errorf("前提不在场：本次运行自己建的 %s 竟不在残留集合里（residue=%v）⇒ 判据可能已经瞎了", schema, residue)
	}
	t.Logf("阳性对照：外来的 %s 在库里（旧判据读到 %d 个 dlperf_* ⇒ 必红）；新判据的残留集合 = %v（不含外来 schema）",
		foreign, legacy, residue)
}

// TestDataLifecycleStorageResidueScopeNegative 阴性对照：
// 人为留下**属于本次运行**的 schema 残留时，残留判据必须报红。
//
// ★ 这一条是「收窄没有过头」的证据：判据变窄后仍然能抓住**自己**的残留。
func TestDataLifecycleStorageResidueScopeNegative(t *testing.T) {
	ctx := context.Background()
	pool := dlPerfSelfTestPool(t)
	// ★ 同上：t.Cleanup 而非 defer ⇒ 删库动作跑的时候池还活着。
	t.Cleanup(pool.Close)

	// ① 造一个「属于本次运行」的 schema（用 helper 同一个生成器 ⇒ 名字形态一致），
	//    并且**先不删**它 —— 模拟「helper 建了，但没删掉」。
	leaked := dlPerfNewSchemaName(t, t.Name())
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+leaked); err != nil {
		t.Fatalf("create leaked schema %s: %v", leaked, err)
	}
	dlPerfRegisterSchema(leaked)
	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(dctx, "DROP SCHEMA IF EXISTS "+leaked+" CASCADE"); err != nil {
			t.Errorf("drop leaked schema %s: %v", leaked, err)
		}
		dlPerfClaimSchemaDropped(leaked)
	})
	// 声称删掉了（cleanup 的语义：我删了，判据去复查「真的没了吗」）。
	dlPerfClaimSchemaDropped(leaked)

	// ② 阴性对照：cleanup 里那条**一模一样的**断言必须报红。
	//    （只把落点从 *testing.T 换成收集器 —— 判据本身是同一条代码路径。）
	rep := &dlPerfFakeReporter{}
	dlPerfAssertNoResidue(ctx, pool, dlPerfRunClaimedDroppedNames(), rep)
	if len(rep.msgs) == 0 {
		t.Fatalf("阴性对照失败：判据对「本次运行自己的残留 %s」没有报红 ⇒ 判据已失效（收窄过头）", leaked)
	}
	t.Logf("阴性对照：残留 %s ⇒ 判据报红：%v", leaked, rep.msgs)

	// ③ 复核：run 级集合确实把它算成了残留。
	residue, err := dlPerfRunResidueNames(ctx, pool)
	if err != nil {
		t.Fatalf("run residue probe: %v", err)
	}
	if !slices.Contains(residue, leaked) {
		t.Errorf("阴性对照失败：run 级残留集合 %v 里没有 %s", residue, leaked)
	}
}
