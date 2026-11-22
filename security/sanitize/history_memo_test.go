package sanitize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/internal/verdictkv"
)

// 历史守卫判决缓存的语义测试（2026-11-23 大请求体归约提速轮）。
//
// 钉住的契约：
//  1. 缓存守卫 ≡ 被包装的守卫（命中与未命中结论相同）；命中时**不再调用**原守卫；
//  2. 缓存键含策略：规则一变（mask→block / 换租户），旧判决自然失效；
//  3. 出错不缓存（读失败 / ctx 取消 / 判定错误），并且命中也尊重 ctx 取消；
//  4. 超大消息不进缓存（照旧流式判定）；
//  5. 本地层有界（FIFO 淘汰）；共享层带 TTL，且自带硬边界（写入预算 /
//     内存水位线 / 后端故障 ⇒ 停写或按未命中处理，绝不改变结论）；
//  6. 可并发调用。

// countingGuard 记录「原守卫被真正调用了几次」。
type countingGuard struct {
	calls atomic.Int64
	inner func(context.Context, func() io.Reader) (bool, error)
}

func (c *countingGuard) guard(ctx context.Context, open func() io.Reader) (bool, error) {
	c.calls.Add(1)
	if c.inner != nil {
		return c.inner(ctx, open)
	}
	data, err := io.ReadAll(open())
	if err != nil {
		return false, err
	}
	return !strings.Contains(string(data), "SECRET"), nil
}

func openText(s string) func() io.Reader { return func() io.Reader { return strings.NewReader(s) } }

// sameErr 把「两边都是 nil」和「同一条错误」视为相等。
func sameErr(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return errors.Is(a, b) || errors.Is(b, a)
}

func TestMemoizedHistoryGuardMatchesInnerAndSkipsRecompute(t *testing.T) {
	c := &countingGuard{}
	g := memoizeHistoryGuard(c.guard, [32]byte{1}, NewHistoryVerdictMemo(HistoryMemoConfig{LocalEntries: 64}), true)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		for _, tc := range []struct {
			text string
			safe bool
		}{
			{"billing port 6432 in /srv/billing/db.yaml", true},
			{"the token is SECRET-abc", false},
			{"中文内容：数据库端口 6432", true},
			{"", true},
		} {
			safe, err := g(ctx, openText(tc.text))
			if err != nil || safe != tc.safe {
				t.Fatalf("%q: safe=%v want=%v err=%v", tc.text, safe, tc.safe, err)
			}
		}
	}
	// 4 条不同消息 ⇒ 原守卫只应被调用 4 次（其余全部命中缓存）。
	if got := c.calls.Load(); got != 4 {
		t.Fatalf("inner calls = %d, want 4 (the whole point of the memo)", got)
	}
}

// TestVerdictKeyIncludesPolicyAndMessage —— 缓存键必须同时含策略与消息。
func TestVerdictKeyIncludesPolicyAndMessage(t *testing.T) {
	msg := []byte(`{"role":"user","content":"x"}`)
	a := verdictKey([32]byte{1}, msg)
	if a == verdictKey([32]byte{2}, msg) {
		t.Fatal("不同策略的相同消息必须落到不同的键")
	}
	if a == verdictKey([32]byte{1}, []byte(`{"role":"user","content":"y"}`)) {
		t.Fatal("相同策略的不同消息必须落到不同的键")
	}
	if a != verdictKey([32]byte{1}, msg) {
		t.Fatal("键必须可复现")
	}
}

// TestMemoizedGuardPolicyChangeInvalidates —— 真实检测器 + 托管规则：规则从
// mask 改成 block 之后，同一条消息的判决必须跟着变（策略 key 进了缓存键）。
func TestMemoizedGuardPolicyChangeInvalidates(t *testing.T) {
	d, m := managedFixture(t)
	d.SetHistoryVerdictMemo(NewHistoryVerdictMemo(HistoryMemoConfig{}))
	cfg := sanitizerules.Config{Rules: []sanitizerules.Rule{{ID: "history", Name: "History", Type: "secret", Kind: "pattern", Pattern: `SYNTHETICVALUE[0-9]+`, Scopes: []string{"input"}, Action: "mask", Enabled: true, TenantID: "a", Fields: []string{"arguments"}}}}
	if _, err := m.Publish(context.Background(), 1, cfg, "test", "mask"); err != nil {
		t.Fatal(err)
	}
	open := openText(`{"arguments":"SYNTHETICVALUE42"}`)
	for i := 0; i < 2; i++ { // 第二次必然命中缓存
		ctx := WithAuthenticatedTenant(context.Background(), "a")
		if safe, err := d.HistoryGuard(ctx)(ctx, open); safe || err != nil {
			t.Fatalf("mask policy: safe=%v err=%v", safe, err)
		}
	}
	cfg.Rules[0].Action = "block"
	if _, err := m.Publish(context.Background(), 2, cfg, "test", "block"); err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "a")
	if safe, err := d.HistoryGuard(ctx)(ctx, open); safe || !errors.Is(err, ErrRuleBlocked) {
		t.Fatalf("规则改成 block 后判决必须跟着变（不得命中旧策略的缓存）: safe=%v err=%v", safe, err)
	}
	// 其它租户没有这条规则 ⇒ 判决为安全（且不串到租户 a 的结论）。
	other := WithAuthenticatedTenant(context.Background(), "b")
	if safe, err := d.HistoryGuard(other)(other, open); !safe || err != nil {
		t.Fatalf("tenant crossed: safe=%v err=%v", safe, err)
	}
}

// TestMemoizedGuardRealPolicyMatchesUnmemoized —— 用真实规则集逐条对拍：
// 开缓存与不开缓存的检测器，对同一批输入的 (safe, err) 必须完全一致。
func TestMemoizedGuardRealPolicyMatchesUnmemoized(t *testing.T) {
	plain, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cached.SetHistoryVerdictMemo(NewHistoryVerdictMemo(HistoryMemoConfig{}))
	inputs := []string{
		"billing port 6432 in /srv/billing/db.yaml",
		`{"password":"synthetic-test-password"}`,
		`"数据库密码改为 synthetic-test-password"`,
		"prefix\n-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("A", 5000) + "\n-----END PRIVATE KEY-----",
		"plain text without anything interesting\n第二行：普通内容",
		`{"nested":"escaped \"quote\" and \u4e2d\u6587 and surrogate \ud83d\ude00"}`,
	}
	ctx := context.Background()
	for round := 0; round < 2; round++ {
		for _, in := range inputs {
			wantSafe, wantErr := plain.HistoryGuard(ctx)(ctx, openText(in))
			gotSafe, gotErr := cached.HistoryGuard(ctx)(ctx, openText(in))
			if gotSafe != wantSafe || !sameErr(gotErr, wantErr) {
				t.Fatalf("round %d input %.40q: cached=(%v,%v) plain=(%v,%v)", round, in, gotSafe, gotErr, wantSafe, wantErr)
			}
		}
	}
	st := cached.historyMemo.Load().Stats()
	if st.LocalHits == 0 || st.Misses != uint64(len(inputs)) {
		t.Fatalf("缓存应恰好未命中 %d 次（首轮），实际 stats=%+v", len(inputs), st)
	}
}

// TestMemoizedGuardDoesNotCacheErrors —— 出错不落缓存：读失败 / 判定错误
// 每次都原样返回，且下一次仍然重新判定。
func TestMemoizedGuardDoesNotCacheErrors(t *testing.T) {
	want := errors.New("archive read failed")
	c := &countingGuard{}
	g := memoizeHistoryGuard(c.guard, [32]byte{3}, NewHistoryVerdictMemo(HistoryMemoConfig{}), true)
	for i := 0; i < 3; i++ {
		if safe, err := g(context.Background(), func() io.Reader { return historyFailReader{want} }); safe || !errors.Is(err, want) {
			t.Fatalf("read failure must surface: safe=%v err=%v", safe, err)
		}
	}
	// 判定阶段的错误（如 block 规则）同样不缓存。
	blockErr := errors.New("blocked by rule")
	failing := &countingGuard{inner: func(context.Context, func() io.Reader) (bool, error) { return false, blockErr }}
	g2 := memoizeHistoryGuard(failing.guard, [32]byte{4}, NewHistoryVerdictMemo(HistoryMemoConfig{}), true)
	for i := 0; i < 3; i++ {
		if _, err := g2(context.Background(), openText("x")); !errors.Is(err, blockErr) {
			t.Fatalf("inner error must surface: %v", err)
		}
	}
	if failing.calls.Load() != 3 {
		t.Fatalf("出错的结果不得被缓存：inner 被调用 %d 次，期望 3", failing.calls.Load())
	}
	// 命中也尊重 ctx 取消。
	ok := &countingGuard{}
	g3 := memoizeHistoryGuard(ok.guard, [32]byte{5}, NewHistoryVerdictMemo(HistoryMemoConfig{}), true)
	if _, err := g3(context.Background(), openText("hello")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if safe, err := g3(ctx, openText("hello")); safe || !errors.Is(err, context.Canceled) {
		t.Fatalf("命中时也必须尊重取消: safe=%v err=%v", safe, err)
	}
}

// TestMemoizedGuardBypassesLargeMessages —— 超过上限的消息不读进内存算哈希，
// 每次都照旧交给原守卫（流式）。
func TestMemoizedGuardBypassesLargeMessages(t *testing.T) {
	c := &countingGuard{}
	g := memoizeHistoryGuard(c.guard, [32]byte{6}, NewHistoryVerdictMemo(HistoryMemoConfig{}), true)
	big := strings.Repeat("a", historyMemoMaxBytes+16)
	for i := 0; i < 3; i++ {
		if safe, err := g(context.Background(), openText(big)); !safe || err != nil {
			t.Fatalf("large message: safe=%v err=%v", safe, err)
		}
	}
	if c.calls.Load() != 3 {
		t.Fatalf("超大消息不应进缓存：inner 被调用 %d 次，期望 3", c.calls.Load())
	}
}

// TestMemoizeHistoryGuardNoopWithoutMemoOrMatches —— 没有缓存 / 策略里没有
// 规则组 ⇒ 原样返回（连源都不读）。
func TestMemoizeHistoryGuardNoopWithoutMemoOrMatches(t *testing.T) {
	// 没有 ⇒ 原样返回被包装的守卫。
	c := &countingGuard{}
	if memoizeHistoryGuard(c.guard, [32]byte{7}, nil, true) == nil {
		t.Fatal("nil memo must return the inner guard")
	}
	// 策略里没有规则组 ⇒ 包装层连源都不读（被包装的守卫自己决定读不读）。
	reads := 0
	inner := func(context.Context, func() io.Reader) (bool, error) { return true, nil }
	g := memoizeHistoryGuard(inner, [32]byte{7}, NewHistoryVerdictMemo(HistoryMemoConfig{}), false)
	safe, err := g(context.Background(), func() io.Reader { reads++; return strings.NewReader("x") })
	if !safe || err != nil || reads != 0 {
		t.Fatalf("无规则组时不得读源: safe=%v err=%v reads=%d", safe, err, reads)
	}
}

// TestLocalVerdictMemoIsBoundedAndFIFO —— 本地层有界；淘汰的是最早进来的。
func TestLocalVerdictMemoIsBoundedAndFIFO(t *testing.T) {
	const capacity = 4096
	m := newLocalVerdictMemo(capacity)
	total := 0
	for i := range m.shards {
		total += m.shards[i].cap
	}
	if total != capacity {
		t.Fatalf("capacity = %d, want %d", total, capacity)
	}
	key := func(i byte) [32]byte { return [32]byte{i, i, i} }
	for i := 0; i < 5000; i++ {
		m.put(key(byte(i%200)), byte(i%7) == 0)
	}
	size := 0
	for i := range m.shards {
		size += len(m.shards[i].vals)
	}
	if size > total {
		t.Fatalf("cache grew past its bound: %d > %d", size, total)
	}
	// FIFO：固定片里塞满 cap+1 个键 ⇒ 最早的那个被淘汰，最新的还在。
	s := m.shard(key(0))
	for i := 0; i < s.cap+1; i++ {
		var k [32]byte
		k[0] = 0
		k[1] = byte(i)
		m.put(k, true)
	}
	var first [32]byte
	if _, ok := s.vals[first]; ok {
		t.Fatal("FIFO 淘汰失效：最早的键仍在缓存里")
	}
	var last [32]byte
	last[0], last[1] = 0, byte(s.cap)
	if _, ok := s.vals[last]; !ok {
		t.Fatal("最新的键必须留在缓存里")
	}
}

// ---- 共享层 ----

type fakeKV struct {
	mu       sync.Mutex
	vals     map[string]string
	ttls     map[string]time.Duration
	used     int64
	failNext error
	sets     int
}

func (f *fakeKV) Get(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", false, err
	}
	v, ok := f.vals[key]
	return v, ok, nil
}
func (f *fakeKV) Set(_ context.Context, key, val string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	if f.vals == nil {
		f.vals = map[string]string{}
		f.ttls = map[string]time.Duration{}
	}
	f.vals[key] = val
	f.ttls[key] = ttl
	f.sets++
	return nil
}
func (f *fakeKV) UsedMemory(context.Context) (int64, error) { return f.used, nil }

func TestTieredVerdictMemoSharedRoundTrip(t *testing.T) {
	// 两个互不相干的进程视角（各自的本地层是空的），共用一个 Redis。
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer client.Close()
	var kv VerdictKV = verdictkv.NewRedis(client, "llmgw:histmemo:")
	p1 := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour})
	p2 := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour})
	key := [32]byte{9}
	if _, ok := p1.Lookup(context.Background(), key); ok {
		t.Fatal("空缓存不应命中")
	}
	p1.Store(context.Background(), key, false)
	if safe, ok := p2.Lookup(context.Background(), key); !ok || safe {
		t.Fatalf("另一个进程视角应从共享层读到 false: ok=%v safe=%v", ok, safe)
	}
	// p2 视角：本地未命中不算「未命中」（共享层给了答案）⇒ 只记一次共享命中。
	if st := p2.Stats(); st.SharedHits != 1 || st.Misses != 0 {
		t.Fatalf("stats = %+v", st)
	}
	// 命中共享层后提升到本地层：第二次不再触共享层。
	if safe, ok := p2.Lookup(context.Background(), key); !ok || safe || p2.Stats().SharedHits != 1 {
		t.Fatalf("应已提升到本地层: ok=%v safe=%v stats=%+v", ok, safe, p2.Stats())
	}
	// TTL 确实设上了。
	keys := srv.Keys()
	if len(keys) != 1 {
		t.Fatalf("shared keys = %v, want exactly one", keys)
	}
	if ttl := srv.TTL(keys[0]); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("shared key TTL = %v, want (0, 1h]", ttl)
	}
}

// TestTieredVerdictMemoSelfProtection —— 共享层的硬边界：写入预算、内存
// 水位线、后端故障。任何一条触线都只影响「写不写共享层」，不影响结论。
func TestTieredVerdictMemoSelfProtection(t *testing.T) {
	ctx := context.Background()

	t.Run("写入预算", func(t *testing.T) {
		kv := &fakeKV{}
		now := time.Unix(1_700_000_000, 0)
		m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour, SharedMaxWritesPerHour: 3, now: func() time.Time { return now }})
		for i := 0; i < 4; i++ {
			m.Store(ctx, [32]byte{byte(i)}, true)
		}
		if kv.sets != 3 || m.Stats().SharedSkipped != 1 {
			t.Fatalf("sets=%d skipped=%d, want 3 / 1（第 4 条被预算挡下）", kv.sets, m.Stats().SharedSkipped)
		}
		// 过一个小时窗口 ⇒ 恢复可写。
		now = now.Add(time.Hour + time.Second)
		m.Store(ctx, [32]byte{9}, true)
		if kv.sets != 4 {
			t.Fatalf("新窗口应恢复写入: sets=%d", kv.sets)
		}
	})

	t.Run("内存水位线", func(t *testing.T) {
		kv := &fakeKV{used: 500}
		now := time.Unix(1_700_000_000, 0)
		m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour, SharedMaxUsedBytes: 1000, now: func() time.Time { return now }})
		m.Store(ctx, [32]byte{1}, true)
		kv.used = 5000 // 超线
		now = now.Add(31 * time.Second)
		m.Store(ctx, [32]byte{2}, true)
		m.Store(ctx, [32]byte{3}, true) // 水位结果在 30s 内复用 ⇒ 仍停写
		kv.used = 100                   // 回落
		now = now.Add(31 * time.Second)
		m.Store(ctx, [32]byte{4}, true)
		if kv.sets != 2 || m.Stats().SharedSkipped != 2 {
			t.Fatalf("sets=%d skipped=%d, want 2 / 2（超线期间的两条被挡）", kv.sets, m.Stats().SharedSkipped)
		}
	})

	t.Run("后端故障不改变结论", func(t *testing.T) {
		kv := &fakeKV{failNext: errors.New("redis down")}
		m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour})
		if _, ok := m.Lookup(ctx, [32]byte{1}); ok {
			t.Fatal("后端故障必须按未命中处理")
		}
		kv.failNext = errors.New("redis down")
		m.Store(ctx, [32]byte{1}, true) // 写失败只计数
		if st := m.Stats(); st.SharedErrors != 2 {
			t.Fatalf("shared errors = %d, want 2", st.SharedErrors)
		}
		// 本地层照常工作。
		if safe, ok := m.Lookup(ctx, [32]byte{1}); !ok || !safe {
			t.Fatalf("共享层故障时本地层必须照常: ok=%v safe=%v", ok, safe)
		}
	})

	t.Run("未配置共享层", func(t *testing.T) {
		m := NewHistoryVerdictMemo(HistoryMemoConfig{})
		m.Store(ctx, [32]byte{1}, true)
		if safe, ok := m.Lookup(ctx, [32]byte{1}); !ok || !safe {
			t.Fatalf("local only: ok=%v safe=%v", ok, safe)
		}
		if st := m.Stats(); st.SharedSkipped != 0 {
			t.Fatalf("没有共享层时不应计入 skipped: %+v", st)
		}
	})
}

// breakerKV 是一个可以「一直失败 / 一直很慢」的共享层假后端，并统计调用次数。
type breakerKV struct {
	mu      sync.Mutex
	calls   int
	failAll bool
	slowFor time.Duration
}

func (b *breakerKV) op() error {
	b.mu.Lock()
	b.calls++
	fail, slow := b.failAll, b.slowFor
	b.mu.Unlock()
	if slow > 0 {
		time.Sleep(slow)
	}
	if fail {
		return errors.New("redis stalled")
	}
	return nil
}
func (b *breakerKV) Get(context.Context, string) (string, bool, error) {
	if err := b.op(); err != nil {
		return "", false, err
	}
	return "", false, nil
}
func (b *breakerKV) Set(context.Context, string, string, time.Duration) error { return b.op() }
func (b *breakerKV) UsedMemory(context.Context) (int64, error)                { return 0, nil }
func (b *breakerKV) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}
func (b *breakerKV) setFail(fail bool) {
	b.mu.Lock()
	b.failAll = fail
	b.mu.Unlock()
}

// TestTieredVerdictMemoBreaker —— 共享层连续失败 ⇒ 熔断：之后既不读也不写
// （否则一次 Redis 卡顿会被逐条消息放大成几千次超时）；冷却期满半开一个探测，
// 探测失败立即重开、成功则恢复。全程只影响「问不问共享层」，不影响结论。
func TestTieredVerdictMemoBreaker(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	kv := &breakerKV{failAll: true}
	m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour, now: func() time.Time { return now }})
	inner := &countingGuard{}
	g := memoizeHistoryGuard(inner.guard, [32]byte{8}, m, true)
	// 30 条互不相同的消息：每条都读一次共享层 + 算完写一次。
	// 没有熔断是 60 次后端调用；熔断后应停在阈值处。
	for i := 0; i < 30; i++ {
		if safe, err := g(ctx, openText(fmt.Sprintf("message %d", i))); !safe || err != nil {
			t.Fatalf("后端故障时结论必须不变: safe=%v err=%v", safe, err)
		}
	}
	if got := kv.count(); got != sharedMemoFailThreshold {
		t.Fatalf("熔断后不得再触共享层: calls=%d, want %d", got, sharedMemoFailThreshold)
	}
	if st := m.Stats(); st.BreakerOpens != 1 || st.Misses != 30 || inner.calls.Load() != 30 {
		t.Fatalf("stats=%+v inner=%d: 应打开一次熔断、30 条全部照常判定", st, inner.calls.Load())
	}
	// 熔断期间：本地层照常命中（第一次的那条已经在本地层）。
	if _, ok := m.Lookup(ctx, verdictKey([32]byte{8}, []byte("message 0"))); !ok {
		t.Fatal("熔断不影响本地层")
	}
	if kv.count() != sharedMemoFailThreshold {
		t.Fatal("本地命中不该触共享层")
	}
	// 冷却期满 ⇒ 半开：放一个探测出去；后端还没好 ⇒ 立即重开。
	now = now.Add(sharedMemoCooldown + time.Second)
	m.Lookup(ctx, [32]byte{77})
	if kv.count() != sharedMemoFailThreshold+1 || m.Stats().BreakerOpens != 2 {
		t.Fatalf("探测失败应立即重开: calls=%d opens=%d", kv.count(), m.Stats().BreakerOpens)
	}
	m.Lookup(ctx, [32]byte{78})
	m.Store(ctx, [32]byte{78}, true)
	if kv.count() != sharedMemoFailThreshold+1 {
		t.Fatalf("重开后应继续绕过: calls=%d", kv.count())
	}
	// 后端恢复 ⇒ 下一个探测成功 ⇒ 闭合，读写恢复。
	kv.setFail(false)
	now = now.Add(sharedMemoCooldown + time.Second)
	m.Lookup(ctx, [32]byte{79})
	if kv.count() != sharedMemoFailThreshold+2 {
		t.Fatalf("冷却期满应放探测: calls=%d", kv.count())
	}
	m.Store(ctx, [32]byte{79}, true)
	m.Lookup(ctx, [32]byte{80})
	if kv.count() != sharedMemoFailThreshold+4 || m.Stats().BreakerOpens != 2 || m.Stats().SharedWrites != 1 {
		t.Fatalf("探测成功后应恢复读写: calls=%d stats=%+v", kv.count(), m.Stats())
	}
	// 零星失败（没到阈值）不触发熔断。
	now = now.Add(sharedMemoCooldown + time.Second)
	for i := 0; i < 4; i++ {
		kv.setFail(true)
		m.Lookup(ctx, [32]byte{byte(90 + i)})
		kv.setFail(false)
		m.Lookup(ctx, [32]byte{byte(95 + i)})
	}
	if m.Stats().BreakerOpens != 2 {
		t.Fatalf("未连续达到阈值不得熔断: %+v", m.Stats())
	}
}

// TestTieredVerdictMemoBreakerSlowOps —— 后端不报错但很慢同样要熔断（每次都
// 接近超时的后端比直接报错的后端更伤：逐条消息各赔一次等待）。
func TestTieredVerdictMemoBreakerSlowOps(t *testing.T) {
	ctx := context.Background()
	slow := &breakerKV{slowFor: sharedMemoSlowOp + 3*time.Millisecond}
	m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: slow, SharedTTL: time.Hour})
	for i := 0; i < 40; i++ {
		if _, ok := m.Lookup(ctx, [32]byte{byte(i)}); ok {
			t.Fatal("空后端不应命中")
		}
	}
	if got := slow.count(); got != sharedMemoFailThreshold || m.Stats().BreakerOpens != 1 {
		t.Fatalf("连续变慢应熔断: calls=%d stats=%+v", got, m.Stats())
	}
	// 正常偏慢（远低于阈值）不触发。
	ok := &breakerKV{slowFor: time.Millisecond}
	m2 := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: ok, SharedTTL: time.Hour})
	for i := 0; i < 40; i++ {
		m2.Lookup(ctx, [32]byte{byte(i)})
	}
	if ok.count() != 40 || m2.Stats().BreakerOpens != 0 {
		t.Fatalf("正常延迟不得熔断: calls=%d stats=%+v", ok.count(), m2.Stats())
	}
}

// TestTieredVerdictMemoIgnoresUnknownSharedValue —— 共享层里的值不认识（既不是
// "0" 也不是 "1"）⇒ 当未命中重算，绝不拿它当判决。
func TestTieredVerdictMemoIgnoresUnknownSharedValue(t *testing.T) {
	ctx := context.Background()
	kv := &fakeKV{vals: map[string]string{}, ttls: map[string]time.Duration{}}
	m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour})
	bad, good := [32]byte{1}, [32]byte{2}
	kv.vals[encodeVerdictKey(bad)] = "true"
	kv.vals[encodeVerdictKey(good)] = "1"
	if safe, ok := m.Lookup(ctx, bad); ok {
		t.Fatalf("坏值不得当命中: safe=%v", safe)
	}
	if safe, ok := m.Lookup(ctx, good); !ok || !safe {
		t.Fatalf("正常值必须照常命中: ok=%v safe=%v", ok, safe)
	}
	if st := m.Stats(); st.SharedHits != 1 || st.Misses != 1 {
		t.Fatalf("stats=%+v: 坏值记未命中、正常值记共享命中", st)
	}
	// 走包装后的守卫：共享层里有一条「说是安全」的坏记录，而原守卫的结论是
	// 不安全 ⇒ 必须以原守卫为准。
	inner := &countingGuard{}
	policyKey := [32]byte{3}
	g := memoizeHistoryGuard(inner.guard, policyKey, m, true)
	msg := []byte("the token is SECRET-abc")
	kv.vals[encodeVerdictKey(verdictKey(policyKey, msg))] = "yes"
	if safe, err := g(ctx, openText(string(msg))); safe || err != nil || inner.calls.Load() != 1 {
		t.Fatalf("坏值不得当判决: safe=%v err=%v inner=%d", safe, err, inner.calls.Load())
	}
}

// TestTieredVerdictMemoBreakerConcurrent —— 多个工作协程同时撞上故障后端：
// 熔断只打开一次、无数据竞争（-race）、结论不变。
func TestTieredVerdictMemoBreakerConcurrent(t *testing.T) {
	kv := &breakerKV{failAll: true}
	m := NewHistoryVerdictMemo(HistoryMemoConfig{Shared: kv, SharedTTL: time.Hour})
	inner := &countingGuard{}
	g := memoizeHistoryGuard(inner.guard, [32]byte{8}, m, true)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if safe, err := g(context.Background(), openText(fmt.Sprintf("c-%d-%d", w, i))); !safe || err != nil {
					t.Errorf("w=%d i=%d: safe=%v err=%v", w, i, safe, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if st := m.Stats(); st.BreakerOpens == 0 || inner.calls.Load() != 800 {
		t.Fatalf("stats=%+v inner=%d", st, inner.calls.Load())
	}
	// 熔断打开之后的调用都不该再触后端：16 个协程各至多再赔几次在途调用。
	if kv.count() > sharedMemoFailThreshold+4*16 {
		t.Fatalf("熔断后仍有大量后端调用: calls=%d", kv.count())
	}
}

// TestMemoizedGuardConcurrent —— 并发调用下结论一致、无数据竞争（-race）。
func TestMemoizedGuardConcurrent(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d.SetHistoryVerdictMemo(NewHistoryVerdictMemo(HistoryMemoConfig{LocalEntries: 4096}))
	g := d.HistoryGuard(context.Background())
	texts := []string{
		"billing port 6432 in /srv/billing/db.yaml",
		`{"password":"synthetic-test-password"}`,
		"nothing to see here",
	}
	want := make([]bool, len(texts))
	for i, tx := range texts {
		safe, err := g(context.Background(), openText(tx))
		if err != nil {
			t.Fatal(err)
		}
		want[i] = safe
	}
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				idx := (w + i) % len(texts)
				safe, err := g(context.Background(), openText(texts[idx]))
				if err != nil || safe != want[idx] {
					t.Errorf("concurrent verdict drift: idx=%d safe=%v want=%v err=%v", idx, safe, want[idx], err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
