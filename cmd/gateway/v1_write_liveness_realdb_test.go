//go:build !integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v1_write_liveness_realdb_test.go —— 在真库上证明两件事：
//
//  1. `v1WriteLivenessSQL` **真的能跑**（语法有效、四个面都返回数）。
//     静态门能钉住「读了两个面」，但钉不住「SQL 能不能执行」——
//     而这正是它唯一会在运行时才暴露的失败形态。
//  2. ★ **单面实现会当场误报 `dead`**，用真数据证明，不是用构造的输入。
//
// # 为什么第 2 条值得单独一条门
//
// 负控制如果只用构造输入，它证明的是「我的分类函数按我写的表工作」。
// 用真数据，它证明的是「**此刻**这个错误实现会给出错误判决」——
// 而这正是防回归需要的形状。
func TestV1WriteLiveness_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ctx := context.Background()
	const window = 30 * time.Minute
	since := time.Now().Add(-window)

	var hot, parent, turnsHot, turnsParent int64
	err = pool.QueryRow(ctx, v1WriteLivenessSQL, since).
		Scan(&hot, &parent, &turnsHot, &turnsParent)
	if err != nil {
		t.Fatalf("v1WriteLivenessSQL 执行失败：%v", err)
	}
	t.Logf("最近 %s：v1_hot=%d v1_parent=%d turns_hot=%d turns_parent=%d",
		window, hot, parent, turnsHot, turnsParent)

	in := v1WriteLivenessInput{
		V1HotRows: hot, V1ParentRows: parent,
		TurnsHotRows: turnsHot, TurnsParent: turnsParent, ThresholdRows: 1,
	}
	if got := ClassifyV1WriteLiveness(in); got != livenessAlive {
		t.Errorf("判决 = %q，期望 %q —— v1 写入腿此刻是否真的在写？"+
			"若 v1 确实没在写，本门应当改期望值而不是继续报 alive", got, livenessAlive)
	}

	// ★ 负控制：**两种**常见的单面实现，在真数据上都是错的。
	// 记下来是因为「错成哪个」取决于实现怎么写，而两种都错这件事本身
	// 才是要拦的东西 —— 只断言一种会让人以为另一种是安全的。
	//
	//	变体 A（两面都只读父表）：v1 父 0 行、turns 父 0 行 ⇒ quiet
	//	变体 B（v1 读父表、turns 读 hot）：v1 0 行、turns hot 有行 ⇒ dead
	//
	// 变体 A 的形状值得单独说：它给出的不是假死警，而是
	// **「什么都没证明」** —— 那正是 §9.238 记的原始失效形态
	// （「开关读数是 true、v1 实际零行、全程无任何信号」），
	// 只不过这里的 v1 不是零行，而是读数落在了一个本来就不该有行的面上。
	parentOnlyBoth := ClassifyV1WriteLiveness(v1WriteLivenessInput{
		V1HotRows: 0, V1ParentRows: parent,
		TurnsHotRows: 0, TurnsParent: turnsParent, ThresholdRows: 1,
	})
	parentOnlyV1HotTurns := ClassifyV1WriteLiveness(v1WriteLivenessInput{
		V1HotRows: 0, V1ParentRows: parent,
		TurnsHotRows: turnsHot, TurnsParent: 0, ThresholdRows: 1,
	})

	if parent == 0 && hot > 0 {
		if parentOnlyBoth == livenessAlive {
			t.Errorf("负控制失效：单面实现（两面都只读父表）判成 alive —— " +
				"真数据上这一条不成立，双面判据就没有被证伪过")
		}
		if parentOnlyV1HotTurns == livenessAlive {
			t.Errorf("负控制失效：单面实现（v1 读父表、turns 读 hot）判成 alive —— 同上")
		}
		t.Logf("★ 活假阳性已证实（父表 0 行 / hot %d 行 / turns_hot %d 行）："+
			"只读父表 ⇒ %q；v1 读父表而 turns 读 hot ⇒ %q；双面 ⇒ %q。"+
			"前者是 §9.238 那个「什么都没证明」的形状。",
			hot, turnsHot, parentOnlyBoth, parentOnlyV1HotTurns, livenessAlive)
	} else {
		t.Logf("本次父表有 %d 行，两种单面负控制本轮都不成立（换窗口可能成立）", parent)
	}
}
