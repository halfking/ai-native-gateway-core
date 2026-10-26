package persist

// 2026-10-17：关停态判定的**量具自证**。
//
// # 为什么这道门必须存在
//
// 「关停时连接池已关闭」这个修法整个建立在一条假设上：
// **pgxpool.Close() 之后，Begin/Acquire 返回的错误可以被
// errors.Is 认成 puddle.ErrClosedPool。**
//
// ★ 这条假设**不是显然的**，而且如果它不成立，症状会极难归因：
//
//	假设成立 → 修法有效
//	假设不成立 → 修法静默无效，代码照常编译、照常跑，
//	              关停 WARN 一条不少，而**没有任何报错指向"修法没生效"**
//
// pgxpool v5 **不导出** ErrClosedPool（它只存在于底层 puddle），
// 所以这层「透出」必须被证明，而不是被相信。

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"
)

// TestClosedPoolErrorIsIdentifiableAsPuddleSentinel 证明哨兵可判定。
//
// 刻意**不连任何数据库**：pgxpool.New 对不可达地址也能成功返回，
// Close() 之后 Acquire/Begin 必然走 puddle 的关闭分支。
// ⇒ 不需要真库就能验到同一层，不需要 opt-in 门，也就不受
// 「realdb 门被静默跳过」那个流程缺口影响（见 2026-10-17 审计 §七.6）。
func TestClosedPoolErrorIsIdentifiableAsPuddleSentinel(t *testing.T) {
	p, err := pgxpool.New(context.Background(),
		"postgres://127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	p.Close() // 从未建立连接，直接关闭

	_, berr := p.Begin(context.Background())
	if berr == nil {
		t.Fatal("a closed pool must not hand out a transaction")
	}
	if !errors.Is(berr, puddle.ErrClosedPool) {
		t.Fatalf("errors.Is(beginErr, puddle.ErrClosedPool) = false\n"+
			"  err  = %v\n  type = %T\n"+
			"⇒ 关停态修法的前提不成立：pool-closed 会被当成普通失败继续 Warn，"+
			" 而这条门正是为了在该前提被破坏时立刻变红。",
			berr, berr)
	}

	// 负向对照：关停态判定**不能**误伤一个真错误。
	// 若 isPoolClosed 写成「任何 error 都算关停」，这里就是唯一的防线。
	sentinel := errors.New("canceling statement due to lock timeout")
	if isPoolClosed(sentinel) {
		t.Fatal("isPoolClosed matched an unrelated error — 关停态会变成吞错的万能借口")
	}
	if isPoolClosed(nil) {
		t.Fatal("isPoolClosed(nil) = true, want false")
	}
}
