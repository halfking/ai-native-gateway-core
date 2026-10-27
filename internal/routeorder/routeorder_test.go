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
//
// ★ **且已取消的请求根本不启动排序**（2026-10-28 补）：
//
//	旧实现下这条门是**概率性**的 —— 「ctx 已取消」与「策略结果已就绪」同时成立时，
//	Go 的 `select` 在就绪分支间**随机**选 ⇒ 同一个输入时而原顺序、时而策略顺序。
//
//	⇒ 门要能**确定性地**抓住回归 ⇒ 额外断言**策略根本没被启动**
//	（旧实现 100% 破，不靠概率）。
func TestOrderContextCancelFallsBackToOriginal(t *testing.T) {
	seats := magpieP0Seats(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := make(chan struct{}, 1)
	s := OrderFunc{StrategyName: "cancel", Fn: func(string, []Seat) []int {
		started <- struct{}{}
		return []int{2, 1, 0}
	}}
	got := Order(ctx, s, "pool", seats, time.Second)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("取消后排列 = %v，want %v（原顺序）", got, want)
	}
	// ★ 已取消 ⇒ 不启动排序（否则「取消 ⇒ 原顺序」就得靠 select 抛硬币）。
	select {
	case <-started:
		t.Fatal("已取消的请求仍启动了策略排序 —— 「取消 ⇒ 原顺序」不得依赖抛硬币")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestOrderPickCancelBeatsValidPermutation 断言「**取消优先于结果**」这条契约的
// **唯一判据**（orderPick）。
//
// ★ 为什么单独单测它：这条契约在**集成层**只在一个 **ns 级窗口**里可观测
//
//	（「ctx 已取消」与「结果已就绪」同时成立的那一瞬间）⇒ 靠集成测试去撞要跑
//	上万轮才有一次 ⇒ 门必须**确定性**。
//	⇒ 把判据抽成 orderPick，直接单测它：判据一改，这里立刻红。
func TestOrderPickCancelBeatsValidPermutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// ① 已取消 + 合法排列 ⇒ 原顺序（取消赢）。
	got := orderPick(ctx, []int{2, 1, 0}, 3)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("已取消时排列 = %v，want %v（取消必须赢）", got, want)
	}
	// ② 未取消 + 合法排列 ⇒ 如实采用。
	got = orderPick(context.Background(), []int{2, 1, 0}, 3)
	if !reflect.DeepEqual(got, []int{2, 1, 0}) {
		t.Fatalf("未取消时排列 = %v，want [2 1 0]（合法排列应当被采用）", got)
	}
	// ③ 未取消 + 非排列 ⇒ 原顺序。
	got = orderPick(context.Background(), []int{0, 1}, 3)
	if !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Fatalf("非排列时排列 = %v，want [0 1 2]（原顺序）", got)
	}
}

// TestOrderCancelDuringWaitBeatsReadyResult 断言：**取消与结果同时就绪时，取消赢**。
//
// ★ 这是旧实现里**抛硬币**的那一处：策略在返回前把 ctx 取消掉 ⇒
//
//	「ctx 已取消」与「结果是合法排列」**同时**成立 ⇒ 旧 `select` 随机选支
//	⇒ 输出时而 [0 1 2]、时而 [2 1 0]。
//
// ★ 门要能抓住它 ⇒ 跑多轮（每轮都是一次独立抛硬币）⇒ 旧实现必红。
//
// ★ 轮数怎么定的：这条契约在**集成层**只在「ctx 已取消」与「结果已就绪」
//
//	**同时成立**的那一瞬间可观测 ⇒ 实测该窗口命中率只有 **0.145%**
//	（策略 goroutine 必须抢在主 goroutine 走到 `select` 之前算完）
//	⇒ 200 轮只能撞到 ~25% ⇒ 这里跑 **20000 轮**（约 18ms）
//	⇒ 命中概率 ≈ 1-(1-0.00145)^20000 ≈ 99.999%。
func TestOrderCancelDuringWaitBeatsReadyResult(t *testing.T) {
	const rounds = 20000
	for i := 0; i < rounds; i++ {
		seats := magpieP0Seats(3)
		ctx, cancel := context.WithCancel(context.Background())
		s := OrderFunc{StrategyName: "cancel-inside", Fn: func(string, []Seat) []int {
			cancel() // ★ 取消与返回**贴脸** ⇒ 两个分支同时就绪
			return []int{2, 1, 0}
		}}
		got := Order(ctx, s, "pool", seats, time.Second)
		want := []int{0, 1, 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 轮：取消后排列 = %v，want %v（取消必须赢，不得抛硬币）", i+1, got, want)
		}
		cancel() // 兜底（cancel 可重复调用）
	}
}

// TestOrderCancelDuringWaitReturnsPromptly 断言：**等待途中**被取消 ⇒ 立即返回原顺序。
//
// ★ 门不能只覆盖"自己写的那一半"：只看最终排列的话，**取消分支与超时分支同归**
//
//	（都返回原顺序）⇒ 把取消分支删掉，这条门照样绿。
//	⇒ 必须额外断言**时限**：取消必须让 Order **当场**返回，而不是等超时兜底。
func TestOrderCancelDuringWaitReturnsPromptly(t *testing.T) {
	seats := magpieP0Seats(3)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release) // 收尾放行策略 goroutine（否则它会一直挂着）
	s := OrderFunc{StrategyName: "blocked", Fn: func(string, []Seat) []int {
		close(started)
		<-release
		return []int{2, 1, 0}
	}}
	done := make(chan []int, 1)
	go func() { done <- Order(ctx, s, "pool", seats, 30*time.Second) }()
	<-started // 策略确实在跑
	cancel()
	var got []int
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Order 未在 2s 内返回 —— 取消分支失效（只剩超时兜底）")
	}
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("取消后排列 = %v，want %v（原顺序）", got, want)
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
