// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES
package routeorder

// routeorder_test.go — P1-2 的门：**策略只排序、失败开放**。
//
// # 这道门守住哪块缺口
//
// magpie 的路由策略点契约（internal/routes/routes.go + docs/subsystems/
// gateway-routing.md）：`Order` **只返一个排列**；**panic / 超时 / 非排列 ⇒ 回到
// 原顺序**；**每条路线仍会被试**。
//
// ★ 本仓此前**没有**这一层：策略散在多层（executors 的复合评分排序、权重排序、
//   autocombo 的 sortCandidates、routingopt 的 sortByScore、admin 的稳定排序），
//   没有统一契约，也**没有一处**写明"排序失败时怎么办"。
//
// # 判据清单（对应计划书 §6-P1-2 的验收）
//
//	① 非排列返回 ⇒ 原顺序
//	② panic ⇒ 原顺序
//	③ 超时 ⇒ 原顺序
//	④ 现有策略迁移前后**候选集合与顺序一致**的对拍
//	   ⇒ 在 domains/streaming/executors 的适配器测试里（routeorder_adapter_test.go）
//
// ★ 外加两条**硬约束**的断言：
//	⑤ `Apply` **永不丢座位** ⇒ 每条路线仍会被试（排序不是筛选）
//	⑥ 座位是**字段级 allow-list** ⇒ 策略看不到凭据明文/请求体

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func magpieP0Seats(n int) []Seat {
	out := make([]Seat, n)
	for i := range out {
		out[i] = Seat{
			Index:        i,
			CredentialID: 100 + i,
			ProviderID:   200 + i,
			Model:        "model-" + string(rune('a'+i)),
			Tier:         i,
			Weight:       10 - i,
			Known:        i%2 == 0,
			SuccessRate:  float64(i) / float64(n+1),
			Resting:      i == n-1,
			Tokens:       1000 - i*10,
			Used:         float64(i) / float64(n+1),
		}
	}
	return out
}

func magpieP0SameOrder(a, b []Seat) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Index != b[i].Index {
			return false
		}
	}
	return true
}

// TestOrderNonPermutationFallsBackToOriginal 判据①：**非排列 ⇒ 原顺序**。
func TestOrderNonPermutationFallsBackToOriginal(t *testing.T) {
	cases := []struct {
		name string
		idx  []int
	}{
		{"nil", nil},
		{"少给（少一个座位）", []int{0, 1}},
		{"多给（多一个座位）", []int{0, 1, 2, 3, 0}},
		{"重复", []int{0, 1, 1}},
		{"越界", []int{0, 1, 9}},
		{"负数", []int{0, 1, -1}},
		{"空（非零长度）", []int{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seats := magpieP0Seats(3)
			s := OrderFunc{StrategyName: "bad", Fn: func(string, []Seat) []int { return c.idx }}
			got := Order(context.Background(), s, "pool", seats, time.Second)
			if !magpieP0SameOrder(seats, magpieP0Seats(3)) {
				t.Fatal("前提失效：夹具不是原顺序")
			}
			want := []int{0, 1, 2}
			if len(got) != len(want) {
				t.Fatalf("非排列时长度 = %d，want %d（原顺序）", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("非排列时排列 = %v，want %v（原顺序）", got, want)
				}
			}
		})
	}
}

// TestOrderPanicFallsBackToOriginal 判据②：**panic ⇒ 原顺序**。
//
// ★ 排序失败**不得**让请求失败 —— 这是失败开放的核心含义。
func TestOrderPanicFallsBackToOriginal(t *testing.T) {
	seats := magpieP0Seats(4)
	s := OrderFunc{StrategyName: "panicky", Fn: func(string, []Seat) []int {
		panic("strategy exploded")
	}}
	got := Order(context.Background(), s, "pool", seats, time.Second)
	if len(got) != 4 {
		t.Fatalf("panic 后长度 = %d，want 4（原顺序）", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("panic 后排列 = %v，want [0 1 2 3]（原顺序）", got)
		}
	}
}

// TestOrderTimeoutFallsBackToOriginal 判据③：**超时 ⇒ 原顺序**。
func TestOrderTimeoutFallsBackToOriginal(t *testing.T) {
	seats := magpieP0Seats(3)
	release := make(chan struct{})
	s := OrderFunc{StrategyName: "slow", Fn: func(string, []Seat) []int {
		<-release // 永远不返回，直到测试收尾
		return []int{2, 1, 0}
	}}
	got := Order(context.Background(), s, "pool", seats, 20*time.Millisecond)
	close(release)
	if len(got) != 3 {
		t.Fatalf("超时后长度 = %d，want 3（原顺序）", len(got))
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("超时后排列 = %v，want [0 1 2]（原顺序）", got)
		}
	}
}

// TestOrderContextCancelFallsBackToOriginal 断言上下文取消 ⇒ 原顺序。
func TestOrderContextCancelFallsBackToOriginal(t *testing.T) {
	seats := magpieP0Seats(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := OrderFunc{StrategyName: "cancel", Fn: func(string, []Seat) []int {
		return []int{2, 1, 0}
	}}
	got := Order(ctx, s, "pool", seats, time.Second)
	for i, v := range got {
		if v != i {
			t.Fatalf("取消后排列 = %v，want [0 1 2]（原顺序）", got)
		}
	}
}

// TestOrderAppliesValidPermutation 断言合法排列被**如实**采用。
func TestOrderAppliesValidPermutation(t *testing.T) {
	seats := magpieP0Seats(3)
	s := OrderFunc{StrategyName: "reverser", Fn: func(string, []Seat) []int {
		return []int{2, 1, 0}
	}}
	got := Order(context.Background(), s, "pool", seats, time.Second)
	want := []int{2, 1, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排列 = %v，want %v", got, want)
		}
	}
}

// TestApplyNeverDropsSeats 判据⑤（**硬约束**）：**每条路线仍会被试**。
//
// ★ 排序**不是筛选**：施加排列后座位数必须不变，且必须是一个排列
//
//	⇒ 一个策略**无法**悄悄让某条路线"根本不试"。
func TestApplyNeverDropsSeats(t *testing.T) {
	seats := magpieP0Seats(5)
	got := Apply(seats, []int{4, 3, 2, 1, 0})
	if len(got) != len(seats) {
		t.Fatalf("座位数 = %d，want %d（排序不得丢座位）", len(got), len(seats))
	}
	seen := map[int]bool{}
	for _, s := range got {
		if seen[s.Index] {
			t.Fatalf("座位 %d 重复出现 —— 排列必须是一一对应", s.Index)
		}
		seen[s.Index] = true
	}
	for i := range seats {
		if !seen[i] {
			t.Fatalf("座位 %d 消失了 —— **每条路线仍会被试**", i)
		}
	}
	// 原切片不得被改动。
	if seats[0].Index != 0 || seats[4].Index != 4 {
		t.Fatal("Apply 改动了原切片 —— 必须返回新切片")
	}
}

// TestApplyNonPermutationKeepsAllSeats 断言非排列 ⇒ 原样返回，且**座位一个不少**。
func TestApplyNonPermutationKeepsAllSeats(t *testing.T) {
	seats := magpieP0Seats(4)
	got := Apply(seats, []int{0, 1}) // 少给
	if len(got) != len(seats) {
		t.Fatalf("座位数 = %d，want %d（非排列也必须原样返回全部座位）", len(got), len(seats))
	}
	for i := range got {
		if got[i].Index != seats[i].Index {
			t.Fatalf("非排列时座位 %d 的 Index = %d，want %d（原样）", i, got[i].Index, seats[i].Index)
		}
	}
}

// TestIsPermutation 判定表。
func TestIsPermutation(t *testing.T) {
	cases := []struct {
		idx  []int
		n    int
		want bool
	}{
		{[]int{0, 1, 2}, 3, true},
		{[]int{2, 0, 1}, 3, true},
		{[]int{}, 0, true},
		{[]int{0, 1}, 3, false},
		{[]int{0, 1, 2, 0}, 3, false},
		{[]int{0, 1, 3}, 3, false},
		{[]int{0, 1, -1}, 3, false},
		{nil, 0, true},
		{nil, 3, false},
	}
	for _, c := range cases {
		if got := IsPermutation(c.idx, c.n); got != c.want {
			t.Fatalf("IsPermutation(%v,%d) = %v，want %v", c.idx, c.n, got, c.want)
		}
	}
}

// TestNilStrategyReturnsOriginal 断言 nil 策略 ⇒ 原顺序（不是 panic）。
func TestNilStrategyReturnsOriginal(t *testing.T) {
	seats := magpieP0Seats(3)
	got := Order(context.Background(), nil, "pool", seats, time.Second)
	for i, v := range got {
		if v != i {
			t.Fatalf("nil 策略排列 = %v，want 原顺序", got)
		}
	}
}

// TestSeatExposesNoSensitiveFields 判据⑥（**字段级 allow-list**）：
// Seat **不得**携带凭据明文、API Key、请求体。
//
// ★ 这条是**反射**约束：有人日后往 Seat 里加一个 `APIKey string` 字段 ⇒ 这里红。
func TestSeatExposesNoSensitiveFields(t *testing.T) {
	// ★ 禁的是**凭据/密文/请求体**这类字段名，不是笼统的 "token" ——
	//   magpie 的 Seat 里本来就有 `Tokens`（**剩余令牌额度**，是容量不是秘密），
	//   把它禁掉会误伤正确的字段。
	forbidden := []string{
		"apikey", "api_key", "secret", "password", "authorization",
		"privatekey", "private_key", "accesskey", "access_key",
		"body", "payload",
	}
	typ := reflect.TypeOf(Seat{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		lower := strings.ToLower(name)
		for _, bad := range forbidden {
			if strings.Contains(lower, bad) {
				t.Fatalf("Seat 出现了敏感字段 %q —— 策略能看到的只有座位信息，不得含凭据/请求体", name)
			}
		}
	}
	// ★ 但"标识"与"额度"类字段是允许的：CredentialID 是**标识**不是密文
	//   （magpie 的 Seat 里也有 Key / Account 这类标识），Tokens 是**额度**。
	//   ⇒ 禁的是"能直接拿去当凭据用的东西"，不是"含 credential 字样的东西"。
	//
	// ★ 但"额度"类字段是允许的（magpie 的 Seat 就有 Tokens）。
	if _, ok := typ.FieldByName("Tokens"); !ok {
		t.Fatal("Seat 应当保留 Tokens（剩余令牌额度）—— 它是容量，不是秘密")
	}
}
