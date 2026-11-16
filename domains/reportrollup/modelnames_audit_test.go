package reportrollup

// modelnames_audit_test.go —— 模型过滤的「canonical ↔ raw」互认回归门
// （2026-11-16 审计轮）。
//
// 审计在 211 真库上量到的错位：路由页（/routing-v2?tab=resolve）的模型选择器
// 发的是 **canonical 名**（minimax-m2.5），而对账快照的维度是
// **raw_model_name**（MiniMax-M2.5 / minimax-m2.5 / auto/minimax-m2.5）。
// 两者字面不等 ⇒ 直接拿 canonical 名去等值过滤，结果是「选了模型却一行都
// 没有」。
//
// 真库覆盖面（211 实测）：区间内 1200 个有请求的 raw 模型名里，**1199 个**
// 能落到某个 canonical（经 canonical 名 / model_offers / model_aliases 三条
// 路径），只有 1 个（claude-3-7-sonnet-20250219）真的不在目录里。
// ⇒ 展开必须「查到就展开，查不到就原样精确匹配」——不能因为目录缺项把用户
// 筛空。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestResolveModelRawNames_ExpandsCanonical canonical 名 → 整个 raw 名家族。
func TestResolveModelRawNames_ExpandsCanonical(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	mock.ExpectQuery("WITH target AS").
		WithArgs("minimax-m2.5").
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("MiniMax-M2.5").
			AddRow("auto/minimax-m2.5").
			AddRow("minimax-m2.5").
			AddRow("minimax_m2.5"))

	names, err := ResolveModelRawNames(context.Background(), mock, "minimax-m2.5")
	if err != nil {
		t.Fatalf("ResolveModelRawNames: %v", err)
	}
	want := map[string]bool{
		"minimax-m2.5":      true, // 入参本身必须在集合里
		"MiniMax-M2.5":      true,
		"auto/minimax-m2.5": true,
		"minimax_m2.5":      true,
	}
	if len(names) != len(want) {
		t.Fatalf("展开结果 = %v，期望 %d 项", names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("展开结果里有多余项 %q", n)
		}
	}
	// 排序稳定（便于断言与调试）。
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("展开结果未排序：%v", names)
			break
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("未满足的期望：%v", err)
	}
}

// TestResolveModelRawNames_UnknownStaysExact 目录外的名字（`<unknown>`）
// 只能原样精确匹配——**不能**因为查不到 canonical 就把过滤整个丢掉。
func TestResolveModelRawNames_UnknownStaysExact(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	mock.ExpectQuery("WITH target AS").
		WithArgs("<unknown>").
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}))

	names, err := ResolveModelRawNames(context.Background(), mock, "<unknown>")
	if err != nil {
		t.Fatalf("ResolveModelRawNames: %v", err)
	}
	if len(names) != 1 || names[0] != "<unknown>" {
		t.Fatalf("展开结果 = %v，期望 [<unknown>]（原样精确匹配）", names)
	}
}

// TestResolveModelRawNames_Empty 空前缀不查库。
func TestResolveModelRawNames_Empty(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	names, err := ResolveModelRawNames(context.Background(), mock, "   ")
	if err != nil {
		t.Fatalf("ResolveModelRawNames: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("空名应返回空集合，实际 %v", names)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("不该发出任何查询：%v", err)
	}
}

// TestResolveModelRawNames_QueryError 查库失败必须**报错**——调用方据此
// 降级成「只用原值精确匹配」，而不是把过滤悄悄丢掉（丢掉会让页面显示全量
// 数据，用户以为筛过了）。
func TestResolveModelRawNames_QueryError(t *testing.T) {
	mock, closer := newMockPool(t)
	defer closer()

	mock.ExpectQuery("WITH target AS").WillReturnError(errors.New("boom"))

	names, err := ResolveModelRawNames(context.Background(), mock, "glm-5.2")
	if err == nil {
		t.Fatalf("查库失败必须返回 error，实际 names=%v", names)
	}
	if !strings.Contains(err.Error(), "resolve model names") {
		t.Errorf("错误信息缺少上下文：%v", err)
	}
}
