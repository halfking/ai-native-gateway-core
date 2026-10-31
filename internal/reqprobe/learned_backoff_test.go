package reqprobe

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 2026-11-01 参数兼容静态化轮 TTL 复核的判据（任务：「TTL 再探测成本是否
// 需要按 (provider,model) 分级」）。裁决：LearnedKey 本就是
// (providerID, outboundModel, actualWire) 粒度，不引入静态分级表；改指数
// 退避——同一参数族每次「再探测后仍被拒且剥参成功」（RecoveredCount 递增）
// 把下一窗口翻倍（默认上限 4 步 ⇒ 16×；LLM_GATEWAY_REQPROBE_LEARNED_TTL_
// BACKOFF_MAX=0 关闭）。
//
// 变异臂 M7 靶：learnedWindowWithSteps 退化为恒 base（删指数移位）⇒ 两臂红；
// 变异臂 M8 靶：memory/redis 两 cutoff 改回固定 learnedTTL() ⇒ 窗口臂红。

func TestLearnedWindowBackoffSteps(t *testing.T) {
	mk := func() *Record { return &Record{} }
	cases := []struct {
		recovered int
		steps     int
		want      time.Duration
	}{
		{1, 4, 1 * time.Hour},  // 首次成功仍是 base
		{2, 4, 2 * time.Hour},  // 第 2 次翻倍
		{3, 4, 4 * time.Hour},  // 第 3 次
		{4, 4, 8 * time.Hour},  // 第 4 次
		{5, 4, 16 * time.Hour}, // 第 5 次
		{9, 4, 16 * time.Hour}, // 超出步数上限 → 封顶 2^4
		{6, 0, 1 * time.Hour},  // steps=0 关闭退避
		{1, 0, 1 * time.Hour},
		{0, 4, 1 * time.Hour}, // 无成功（不在学习面，窗口无意义但不越界）
	}
	for _, tc := range cases {
		rec := mk()
		rec.RecoveredCount = tc.recovered
		if got := learnedWindowWithSteps(rec, tc.steps); got != tc.want {
			t.Fatalf("RecoveredCount=%d steps=%d: window = %v, want %v", tc.recovered, tc.steps, got, tc.want)
		}
	}
}

// newBackoffStore 建固定时钟的内存 store。
func newBackoffStore(t *testing.T, now time.Time) *MemoryStore {
	t.Helper()
	s := NewMemoryStore()
	s.nowFn = func() time.Time { return now }
	return s
}

// upsertLearned 写入一条「nvidia glm reasoning 被拒且剥参恢复」记录并
// 校准 RecoveredCount（Upsert 对同指纹取 max，直写保证测试确定性）。
func upsertLearned(t *testing.T, s *MemoryStore, recovered int, lastSeen time.Time) {
	t.Helper()
	_, err := s.Upsert(context.Background(), Record{
		Day:            "2026-11-01",
		ProviderID:     18,
		ProviderCode:   "nvidia",
		OutboundModel:  "z-ai/glm-5.3-flash",
		Protocol:       "openai-completions",
		Trigger:        TriggerParamRejected,
		Param:          "reasoning",
		HTTPStatus:     400,
		RecoveredCount: recovered,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s.mu.Lock()
	for el := s.order.Front(); el != nil; el = el.Next() {
		e := el.Value.(*memEntry)
		if e.rec.OutboundModel == "z-ai/glm-5.3-flash" && e.rec.Param == "reasoning" {
			e.rec.LastSeen = lastSeen
			e.rec.RecoveredCount = recovered
		}
	}
	s.mu.Unlock()
}

func learnedHas(s *MemoryStore, param string) bool {
	got := s.LearnedParams(context.Background(), LearnedKey{
		ProviderID: 18, OutboundModel: "z-ai/glm-5.3-flash", ActualWire: "openai-completions",
	})
	for _, p := range got {
		if strings.EqualFold(p, param) {
			return true
		}
	}
	return false
}

// TestLearnedParamsBackoffWindows 钉 LearnedParams 的窗口语义：
// 退避只奖励「被反复证实的拒绝」，固定窗口下必过期的读数在退避下命中。
func TestLearnedParamsBackoffWindows(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)

	// 90 分钟前、成功 2 次 ⇒ 窗口 2h ⇒ 仍前置剥参（固定 1h 窗口下此处为空）。
	s := newBackoffStore(t, now)
	upsertLearned(t, s, 2, now.Add(-90*time.Minute))
	if !learnedHas(s, "reasoning") {
		t.Fatal("RC=2、LastSeen-90m 应命中 2h 退避窗口")
	}

	// 3 小时前、成功 3 次 ⇒ 窗口 4h ⇒ 仍命中。
	s2 := newBackoffStore(t, now)
	upsertLearned(t, s2, 3, now.Add(-3*time.Hour))
	if !learnedHas(s2, "reasoning") {
		t.Fatal("RC=3、LastSeen-3h 应命中 4h 退避窗口")
	}

	// 同样 3 小时前但只成功 1 次 ⇒ 窗口 1h ⇒ 已过期（需要再探测）。
	s3 := newBackoffStore(t, now)
	upsertLearned(t, s3, 1, now.Add(-3*time.Hour))
	if learnedHas(s3, "reasoning") {
		t.Fatal("RC=1、LastSeen-3h 必须过期（退避不奖励首次成功）")
	}

	// 20 小时前、成功 5 次 ⇒ 窗口 16h ⇒ 已过期（退避上限封顶，探测永续）。
	s4 := newBackoffStore(t, now)
	upsertLearned(t, s4, 5, now.Add(-20*time.Hour))
	if learnedHas(s4, "reasoning") {
		t.Fatal("RC=5、LastSeen-20h 必须过期（16h 封顶 ≠ 永久记忆，再确认语义保留）")
	}
}
