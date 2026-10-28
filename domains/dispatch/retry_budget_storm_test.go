package dispatch

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestP0CSamePhaseRetryStormBudgetOnOff — P0-C 验收③（audit 2026-10-28 §4
// 原文）：“仿真同相位失败（100 并发同时 5xx）下总上游尝试次数对比
// （预算 on/off）”。同相位风暴的堆积发生在退避等待窗内，所以时间 harness
// 必须用 steppedSleeper：wave1 冻结时间全量 park（parked 群体真实堆积、
// 预算真正咬合），再逐步脉冲放行后续波次。advancingSleeper 在这里会把
// parked 项瞬间到期 —— 堆积永远不出现，预算永远不咬合（量具伪影：首版
// 验收探针就是这么得出 600=600 假读数的）。
//
// 读数锚点（2026-10-28 验收轮，-count=3 稳定）：
//
//	off     = 600（storm × 2 同 provider 凭据 × 3 次尝试，全员走满梯子）
//	shadow  = 600（与 off 逐行为一致 —— shadow 只计数不拒绝的契约）
//	enforce ≈ 241-251（超预算重试落换凭据梯子 ⇒ 总尝试 −58%；parked 峰值
//	          被压到 floor(10)+ε，等待人群有界 —— 这才是「治同相位重试风暴」）
//
// ★ 修复前读数（同一 harness、仅 failover.go 预算拒绝分支一行之差）：
// enforce 也是 600 —— 首版拒绝后 return p.tryEnqueueCred(cred, qr) 同凭据
// 立即入队，梯子结构与放行完全相同，预算 enforce 对总尝试次数零效果
// （方案原文与 observeRetryBudget 注释说的都是“落到换凭据梯子”）。
func TestP0CSamePhaseRetryStormBudgetOnOff(t *testing.T) {
	const storm = 100

	run := func(t *testing.T, name string, mutate func(*Config)) (total, parkedPeak int) {
		t.Helper()
		clock := &fakeClock{now: time.Now()}
		step := make(chan struct{})
		f := &fakeDeps{
			// 两个同 provider 凭据：跨 provider 换凭据默认禁止
			// （AllowProviderChange=false），异 provider 兄弟会被
			// PlanSwitchCred 标 tried 跳过，梯子走不满。
			refsByModel: map[string][]CredentialRef{"m": {
				credWithProvider(1, 10, 8),
				credWithProvider(2, 10, 8),
			}},
			forwardFn: func(_ context.Context, _ *QueuedRequest, _ CredentialRef) ForwardOutcome {
				return ForwardOutcome{Err: fmt.Errorf("upstream 503"), ErrorKind: "upstream_503", HTTPStatus: 503}
			},
			forwardCalls: map[int]int{},
		}
		p := f.pipeline()
		cfg := DefaultConfig()
		mutate(&cfg)
		p.Reload(cfg)
		s := NewHeapRetryScheduler(p.onRetryDue, clock, steppedSleeper(clock, step), 0)
		defer s.Close()
		p.SetRetryScheduler(s)
		p.Start()
		defer p.Stop()

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < storm; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				qr := NewQueuedRequest(fmt.Sprintf("storm-%s-%d", name, i), "t", "m", context.Background(), nil)
				qr.GatewayInstanceID = "gw-test"
				<-start // 同相位：全部请求同一时刻放行
				_, _ = p.Submit(context.Background(), qr)
			}(i)
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()

		// Phase 1：冻结时间，等 wave1 全量 park（活跃转发归零 + parked 稳定）。
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if n := s.Len(); n > parkedPeak {
				parkedPeak = n
			}
			if p.inFlight.Load() == 0 && s.Len() > 0 {
				time.Sleep(20 * time.Millisecond) // failover goroutine 异步落 park
				if p.inFlight.Load() == 0 {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		if n := s.Len(); n > parkedPeak {
			parkedPeak = n
		}

		// Phase 2：脉冲放行时间，直到风暴排空。
		drained := false
		for i := 0; i < 4000 && !drained; i++ {
			select {
			case step <- struct{}{}:
			case <-done:
				drained = true
				continue
			}
			if n := s.Len(); n > parkedPeak {
				parkedPeak = n
			}
			select {
			case <-done:
				drained = true
			case <-time.After(time.Millisecond):
			}
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: storm did not drain", name)
		}

		f.mu.Lock()
		for _, n := range f.forwardCalls {
			total += n
		}
		f.mu.Unlock()
		t.Logf("[storm:%s] total_attempts=%d parked_peak=%d", name, total, parkedPeak)
		return total, parkedPeak
	}

	offTotal, offParked := run(t, "off", func(c *Config) { c.RetryBudgetPercent = 0 })
	shadowTotal, _ := run(t, "shadow", func(c *Config) {})
	enforceTotal, enforceParked := run(t, "enforce", func(c *Config) { c.RetryBudgetEnforce = true })

	// off 的结构化读数：storm × 2 凭据 ×（1 次首试 + 2 次 same-cred 重试）。
	if want := storm * 6; offTotal != want {
		t.Errorf("off 臂总尝试次数 = %d, want %d（结构性读数漂移 ⇒ 量具前提变了）", offTotal, want)
	}
	if offParked != storm {
		t.Errorf("off 臂 parked 峰值 = %d, want %d（无预算时同相位风暴应全员堆 park）", offParked, storm)
	}
	// shadow 契约：只计数不拒绝 —— 行为与 off 逐一致。
	if shadowTotal != offTotal {
		t.Errorf("shadow 臂总尝试次数 = %d, want %d（shadow 不得改变行为）", shadowTotal, offTotal)
	}
	// 方案原文的验收主张：预算 on 时总上游尝试次数显著低于 off。
	if enforceTotal >= offTotal/2 {
		t.Errorf("enforce 臂总尝试次数 = %d, want < %d（预算 enforce 必须压低同相位风暴的总尝试）", enforceTotal, offTotal/2)
	}
	// 风暴形态主张：等待人群有界（≈floor），而不是全员堆 park。
	if enforceParked >= offParked/5 {
		t.Errorf("enforce 臂 parked 峰值 = %d, want < %d（预算必须封顶等待人群）", enforceParked, offParked/5)
	}
}
