package bg

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 空闲退避（2026-10-13，runbook §10.110.15）的判据分两层，缺一不可：
//
//	L1 决策表：nextIdlePoll 的行为——空转翻倍、封顶、**取到活立刻复位**。
//	L2 结构接线：run() 真的调用它、真的按返回值 Reset 定时器，
//	   且没有退回改前的固定 ticker。
//
// 只写 L1 是假绿的一半：决策函数可以完全正确，而 run() 压根不调它
// （或者调了却忽略返回值），生产行为一字不变。
// 只写 L2 也是假绿的一半：run() 调了它，但决策函数写反了。

const (
	backoffBase = 250 * time.Millisecond
	backoffCap  = 30 * time.Second
)

// L1：决策表。逐条对应「空转翻倍 / 封顶 / 取到活复位」三条承诺。
func TestNextIdlePoll_DecisionTable(t *testing.T) {
	cases := []struct {
		name    string
		current time.Duration
		claimed int
		want    time.Duration
		why     string
	}{
		{
			name: "空转一轮翻倍", current: backoffBase, claimed: 0,
			want: 500 * time.Millisecond,
			why:  "第一轮空转把 250ms 推到 500ms",
		},
		{
			name: "空转逐轮翻倍", current: 16 * time.Second, claimed: 0,
			want: 30 * time.Second,
			why:  "32s 超过封顶 30s ⇒ 截到封顶而不是溢出",
		},
		{
			name: "已在封顶且继续空转", current: backoffCap, claimed: 0,
			want: backoffCap,
			why:  "封顶是稳定态，不能因为再翻倍而抖动",
		},
		{
			name: "取到活立刻复位到 base", current: backoffCap, claimed: 1,
			want: backoffBase,
			why:  "★ 这是「队列有活时延迟不变」这条承诺的全部实现",
		},
		{
			name: "取到多个活同样复位到 base", current: backoffCap, claimed: 7,
			want: backoffBase,
			why:  "复位只看「有没有活」，不看活有多少",
		},
		{
			name: "复位后再次空转只翻一倍", current: backoffBase, claimed: 3,
			want: backoffBase, // 本轮有活 ⇒ 等 base
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextIdlePoll(tc.current, backoffBase, backoffCap, tc.claimed)
			if got != tc.want {
				t.Fatalf("nextIdlePoll(current=%v, claimed=%d) = %v, want %v —— %s",
					tc.current, tc.claimed, got, tc.want, tc.why)
			}
		})
	}
}

// L1 的边界：cap 未配置（0）时 run() 会把 maxIdle 归一到 base，
// 决策函数本身也必须不会返回「比 base 更慢」的值。
func TestNextIdlePoll_NeverSlowerThanBase(t *testing.T) {
	for _, cur := range []time.Duration{backoffBase, time.Second, backoffCap} {
		got := nextIdlePoll(cur, backoffBase, backoffBase /* cap == base */, 0)
		if got != backoffBase {
			t.Fatalf("cap==base 时 nextIdlePoll(%v, claimed=0) = %v，want %v",
				cur, got, backoffBase)
		}
	}
}

// L1 的可观测承诺：模拟「退到封顶 → 有活 → 再空转」的完整序列，
// 断言总等待里**空转侧**被压缩、有活侧没有被拖慢。
func TestNextIdlePoll_SequenceNeverExceedsCapAndAlwaysResetsOnWork(t *testing.T) {
	wait := backoffBase
	idleRounds := 0
	workRounds := 0

	// 前 40 轮空转 ⇒ 应该爬到封顶并停在那里。
	for i := 0; i < 40; i++ {
		wait = nextIdlePoll(wait, backoffBase, backoffCap, 0)
		idleRounds++
		if wait > backoffCap {
			t.Fatalf("第 %d 轮空转后等待 %v 超过封顶 %v", i, wait, backoffCap)
		}
	}
	if wait != backoffCap {
		t.Fatalf("40 轮空转后应停在封顶，实际 %v", wait)
	}

	// 中间插入一轮取到活 ⇒ 必须复位。
	wait = nextIdlePoll(wait, backoffBase, backoffCap, 1)
	if wait != backoffBase {
		t.Fatalf("取到活后未复位：%v", wait)
	}
	workRounds++

	// 再空转 ⇒ 只翻一倍（不是从封顶继续）。
	wait = nextIdlePoll(wait, backoffBase, backoffCap, 0)
	if wait != 2*backoffBase {
		t.Fatalf("复位后首轮空转应只翻一倍，实际 %v", wait)
	}

	t.Logf("空转 %d 轮 / 有活 %d 轮，封顶 %v", idleRounds, workRounds, backoffCap)
}

// L2：结构接线。判据读的是 run() 的函数体本身（与被测函数同一层）。
func TestProbeQueueIdlePollBackoffIsWiredIntoRun(t *testing.T) {
	src, err := os.ReadFile("probe_queue_worker.go")
	if err != nil {
		t.Fatalf("读不到 probe_queue_worker.go: %v", err)
	}
	body := funcBody(t, string(src), "func (w *ProbeQueueWorker) run(ctx context.Context) {")

	must := []struct{ needle, why string }{
		{"nextIdlePoll(", "run() 必须调用退避决策函数，否则决策表再对也是死代码"},
		{"timer.Reset(", "必须按决策结果重置定时器"},
		{"w.processBatch(ctx)", "必须取本轮 claim 数，退避才有依据"},
	}
	for _, m := range must {
		if !strings.Contains(body, m.needle) {
			t.Errorf("run() 里没有 %q —— %s", m.needle, m.why)
		}
	}

	// 改前是固定 ticker。留着它就意味着退避形同虚设：
	// nextIdlePoll 会被调用，但等待时长根本不由它决定。
	if strings.Contains(body, "time.NewTicker") {
		t.Error("run() 还在用 time.NewTicker —— 固定间隔与空闲退避不能共存，" +
			"否则 nextIdlePoll 的返回值不决定实际等待时长")
	}

	// processBatch 的返回值必须真的带 claim 数出来，否则 run() 恒拿 0。
	pb := funcBody(t, string(src), "func (w *ProbeQueueWorker) processBatch(ctx context.Context) (int, error) {")
	for _, needle := range []string{"return 0, nil", "return 0, err", "return len(tasks), nil"} {
		if !strings.Contains(pb, needle) {
			t.Errorf("processBatch 里没有 %q —— claim 数没有从这一层传出去", needle)
		}
	}
}

// funcBody 复用 bg/auto_index_refresher_mutex_test.go 里的同名helper
//（同一 package，本文件不再重复定义）。
