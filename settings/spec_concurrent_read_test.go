package settings

import (
	"sync"
	"testing"
)

// ★ 2026-10-22 12h 审计轮（B5 补漏）· 并发读门。
//
// ★ 背景：Go runtime 对「并发读写同一个 map」直接抛
//   `fatal error: concurrent map read and map write`
//   —— **不可 recover** ⇒ 整个进程死。
//
// ★ B5 那一轮给 `EffectiveValue` 里**第一处** `r.backends` 读加了 RLock，
//   但**同一张 map 在同一函数里还有第二处读**（env 分支的
//   `r.backends[EnvBackendScope]`）仍然无锁 ⇒ 修法自身是半截的。
//
// ★ 本门守的判据：**Registry 的每一张 map，每一次读都在锁内**。
//   跑法必须带 `-race`（race detector 才能把「无锁读 + 有锁写」报出来）。
//
// ★ 负控设计：写入侧**故意持有写锁**（RegisterBackend 本来就持锁），
//   读侧则并发地调 EffectiveValue ⇒ 只要有一处读没在 RLock 内，
//   race detector 必然报出 `r.backends` 上的 data race。
//
// ★ 注意：写入侧必须登记**非 nil** 的 Backend —— RegisterBackend 允许 nil，
//   而 EffectiveValue 会直接 `b.Get(...)` ⇒ nil Backend 是另一条
//   「编程错误 ⇒ 空指针」的路径，不是本门要守的东西（见审计档风险项）。

// probeBackend 是一个最简的 Backend 实现：读恒返回空（走默认链），
// 写实现只是让接口齐全。并发读的判据不依赖它的行为。
type probeBackend struct{}

func (probeBackend) Get(_ Scope, _ string) (jsonRawMessage, error) { return nil, nil }
func (probeBackend) GetTenant(_ string, _ string) (jsonRawMessage, error) {
	return nil, nil
}
func (probeBackend) Set(_ Scope, _ string, _ any) (jsonRawMessage, error) { return nil, nil }
func (probeBackend) SetTenant(_ string, _ string, _ any) (jsonRawMessage, error) {
	return nil, nil
}

func TestRegistryConcurrentReadDuringBackendRegistration(t *testing.T) {
	r := NewRegistry()
	r.MustRegisterSpec(&Spec{
		Key:      "concurrent.probe",
		Type:     TypeString,
		EnvName:  "LLM_GATEWAY_CONCURRENT_PROBE",
		Scope:    ScopePlatform,
		Default:  "fallback",
		Category: CategoryGeneral,
	})

	// 先登记一个 env backend，让 EffectiveValue 的 env 分支真的会走到
	// `r.backends[EnvBackendScope]` 那一行 —— 否则这条读路径根本不被执行。
	r.RegisterBackend(EnvBackendScope, probeBackend{})

	const readers = 8
	const iterations = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读侧：并发地反复走 EffectiveValue（含 DB 分支与 env 分支）。
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, _, err := r.EffectiveValue(ScopePlatform, "concurrent.probe", ""); err != nil {
					t.Errorf("EffectiveValue 失败：%v", err)
					return
				}
			}
		}()
	}

	// 写侧：反复重新登记 backend（持写锁）⇒ 与读侧并发。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			r.RegisterBackend(ScopePlatform, probeBackend{})
			r.RegisterBackend(EnvBackendScope, probeBackend{})
			r.RegisterBackend(ScopeTenant, probeBackend{})
		}
		close(stop)
	}()

	wg.Wait()
}
