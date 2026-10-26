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
//  1. `v1WriteLivenessSQL` **真的能跑**（语法有效、两个面都返回数）。
//     静态门能钉住「读了两个面」，但钉不住「SQL 能不能执行」——
//     而这正是它唯一会在运行时才暴露的失败形态。
//  2. ★ **退役形态下的判决必须落在 {dead, quiet}，且 `alive` 不可达**。
//
// # R69 轮改瞄（2026-10-21）：产品侧已经退役感知，测试侧还停在旧形状
//
// ★ **产品侧已改**（`cmd/gateway/v1_write_liveness.go`，迁移 890 之后）：
// 探针**只读会话族两面**（`session_turns_hot` / `session_turns`），
// v1 侧按定义恒 0 ⇒ 分类器只会给 `dead`（会话侧在写）或 `quiet`（都没写）。
// 文件头还写明：**「别把这两个 0 读成『v1 表空了』」** —— 那是**退役**，不是停写。
//
// ★ **本测试侧没跟上**：它还在 `Scan(&hot, &parent, &turnsHot, &turnsParent)`
// （4 个落点）⇒ 而 SQL 现在只返回 **2 列**
// ⇒ ⇒ 报错 `number of field descriptions must equal number of destinations, got 2`
// ⇒ ⇒ **这不是 v1 族的问题，是「产品改了、夹具没改」**。
//
// ⇒ 改瞄后的三条判据：
//
//	① 探针在真库上**可执行**（这是这道门唯一不可替代的价值 —— 静态门钉不了「能不能跑」）；
//	② 判决与**退役形态**一致：会话侧有行 ⇒ `dead`；都没写 ⇒ `quiet`；
//	   ★ 且 **`alive` 不可达**（探针给不出非 0 的 v1 侧）；
//	③ **负控制**（防止分类器被写死成恒 `dead`）：人为给 v1 侧 1 行 ⇒ 必须判出 `alive`。
//
// # ★ 为什么原来的「单面实现」负控制被退役
//
// 原负控制用**真数据**构造「两面都只读父表」「v1 读父表而 turns 读 hot」
// 两个变体，证它们会误报。★ **在退役形态下这个证法失效了**：
// 分类器把两面**相加**（`turns := in.TurnsHotRows + in.TurnsParent`）
// ⇒ ⇒ 单面读到的和与双面读到的和**在阈值判定上不可区分**
// ⇒ ⇒ 「只读了一个面」这个性质**搬到了探针层**（SQL 常量），
// 而探针层的性质**静态门已经钉住了**（「读了两个面」）⇒ ⇒ 不必在这里重复。
// ⇒ ★ 新的负控制（③）守的是**判决层**当前真正能坏的那件事：**分类器被写死**。
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

	// ★ 探针现在只返回**两个**会话族的计数（v1 侧恒 0，见文件头）。
	var turnsHot, turnsParent int64
	err = pool.QueryRow(ctx, v1WriteLivenessSQL, since).
		Scan(&turnsHot, &turnsParent)
	if err != nil {
		t.Fatalf("v1WriteLivenessSQL 执行失败：%v", err)
	}
	t.Logf("最近 %s：turns_hot=%d turns_parent=%d（v1 侧**恒 0**：族已退役，不是「表空了」）",
		window, turnsHot, turnsParent)

	in := v1WriteLivenessInput{
		V1HotRows: 0, V1ParentRows: 0, // ★ 退役态：探针给不出 v1 侧的行数
		TurnsHotRows: turnsHot, TurnsParent: turnsParent, ThresholdRows: 1,
	}

	// ② 判决与退役形态一致。
	want := livenessQuiet
	if turnsHot+turnsParent >= 1 {
		want = livenessDead
	}
	if got := ClassifyV1WriteLiveness(in); got != want {
		t.Errorf("判决 = %q，期望 %q —— 退役形态下：会话侧有行 ⇒ dead；都没写 ⇒ quiet。"+
			"（信号层会把 dead 在停写开关关着时降级成 retired，这不是本门量的事。）", got, want)
	}
	if got := ClassifyV1WriteLiveness(in); got == livenessAlive {
		t.Errorf("判决 = %q —— ★ **alive 在退役形态下不可达**"+
			"（探针只读会话族 ⇒ v1 侧恒 0 ⇒ 分类器只可能给 dead/quiet）。", got)
	}

	// ③ ★ 负控制：分类器**本身**仍能判出 alive ——
	// 否则「永远 dead」与「判对了」在退出码上完全一样。
	if got := ClassifyV1WriteLiveness(v1WriteLivenessInput{
		V1HotRows:    1, // 人为给 v1 侧 1 行
		TurnsHotRows: turnsHot, TurnsParent: turnsParent, ThresholdRows: 1,
	}); got != livenessAlive {
		t.Errorf("负控制失效：人为给 v1 侧 1 行仍判成 %q（期望 %q）—— "+
			"说明分类器可能已被写死成「恒 dead/quiet」，那样的话本门的 ② 是恒真的。",
			got, livenessAlive)
	}
}
