// featured_sources_failure_semantics_test.go —— featured 装载的**失败语义**
// （★ 2026-11-11 审计 B-6 的钉测）
//
// ★ B-6 的原形态：★ `bg/featured_sources.go` 的注释写「Query errors fail EMPTY
// （★ 同样的 fail-open 姿态）」★ ★ 而代码是 `return nil, err` ⇒ ★★ 注释是**假**的。
//
// ★ 本用例把**真实行为**钉成事实：★ 注释已被改写为描述真实行为，
// ★ ★⇒ ★★ 若将来有人把代码改成「出错 ⇒ 退化成空集合」（★ 即让那句旧注释
// ★★ 重新变成真的），★ ★本用例必须红** ★ ⇒ ★★ 于是「注释」不再是散文，
// ★★ 而是**被行为门守着**的陈述。
//
// ★ 同时钉住另外两件事：
//
//	① db == nil ⇒ (nil, nil) —— ★ 这才是**真的**「空」（★ 无 DB 可问）。
//	② 查询成功 ⇒ 逐行返回模型名（★ 空字符串行被丢弃）。

package bg

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestQueryStaticFeaturedModels_QueryErrorIsNotEmpty断言查询出错时
// **返回错误**，而不是退化成「空 featured 集合」。
//
// ★ 变异臂：★ 把 `return nil, err` 改成 `return nil, nil` ⇒ ★ 本用例红。
func TestQueryStaticFeaturedModels_QueryErrorIsNotEmpty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	boom := errors.New("featured source unavailable")
	mock.ExpectQuery("SELECT model FROM static").WithArgs("default").WillReturnError(boom)

	out, err := QueryStaticFeaturedModels(context.Background(), mock, "default")
	if err == nil {
		t.Fatalf("查询出错时应当返回错误（★ fail-closed：★ featured 读不到 ⇒不得退化成『无 featured 偏好』）★实际返回了集合 %v 与 nil 错误", out)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("返回的错误不是底层那个（★ 不得吞掉底层原因）：%v", err)
	}
	if out != nil {
		t.Fatalf("出错时集合应当是 nil，实际是 %v", out)
	}
}

// TestQueryStaticFeaturedModels_NilDBIsTrulyEmpty 断言 db == nil 时返回
// (nil, nil) —— ★ 这是**真的**「空」，与上面的「失败」是**两件事**。
//
// ★ 为什么要分开钉：★ 注释里「空」这个词原本把这两件事混为一谈
// ★ ★⇒ ★★ 「读不到」与「没有」必须可区分。
func TestQueryStaticFeaturedModels_NilDBIsTrulyEmpty(t *testing.T) {
	out, err := QueryStaticFeaturedModels(context.Background(), nil, "default")
	if err != nil {
		t.Fatalf("db == nil 时不应返回错误，实际：%v", err)
	}
	if len(out) != 0 {
		t.Fatalf("db == nil 时集合应当为空，实际 %v", out)
	}
}

// TestQueryStaticFeaturedModels_SuccessReturnsRows 断言查询成功时逐行返回
// 模型名，且**空字符串行被丢弃**（★ 那不是模型名）。
func TestQueryStaticFeaturedModels_SuccessReturnsRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT model FROM static").WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{"model"}).
			AddRow("model-a").
			AddRow("").
			AddRow("model-b"))

	out, err := QueryStaticFeaturedModels(context.Background(), mock, "default")
	if err != nil {
		t.Fatalf("查询成功却返回错误：%v", err)
	}
	want := []string{"model-a", "model-b"}
	if len(out) != len(want) {
		t.Fatalf("集合=%v，期望 %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("集合[%d]=%q，期望 %q（★ 顺序与空行丢弃都要对）", i, out[i], want[i])
		}
	}
}
