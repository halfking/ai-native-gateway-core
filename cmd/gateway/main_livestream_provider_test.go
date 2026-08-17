package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestLiveQueueSnapshotProvider(t *testing.T) {
	p := dispatch.NewPipeline(dispatch.Deps{})
	got := liveQueueSnapshotProvider(p)
	if !got.Enabled {
		t.Fatalf("Enabled = false, want dispatch gate state true in test")
	}
	if !got.Wired {
		t.Fatal("Wired = false, want true")
	}
	if got.Models == nil || got.Credentials == nil {
		t.Fatalf("empty lanes must be non-nil: models=%v credentials=%v", got.Models, got.Credentials)
	}

	got = liveQueueSnapshotProvider(nil)
	if got.Wired {
		t.Fatal("nil pipeline must report Wired=false")
	}
	if got.Models == nil || got.Credentials == nil {
		t.Fatal("nil pipeline must still expose empty lane arrays")
	}
}

func TestLiveQueueSnapshotFromLanes(t *testing.T) {
	got := liveQueueSnapshotFromLanes(
		[]dispatch.QueueSnapshot{{Model: "m", Depth: 2}},
		[]dispatch.QueueSnapshot{{Credential: 7, Mode: "concurrency", Depth: 3}},
		true,
		true,
	)
	if len(got.Models) != 1 || got.Models[0].Model != "m" || got.Models[0].Depth != 2 {
		t.Fatalf("model lane = %+v", got.Models)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].Credential != 7 || got.Credentials[0].Mode != "concurrency" || got.Credentials[0].Depth != 3 {
		t.Fatalf("credential lane = %+v", got.Credentials)
	}
}

func TestLiveNodeStatusCacheRetainsLastGoodSnapshot(t *testing.T) {
	cache := &liveNodeStatusCache{snapshot: []admin.LiveNodeStatus{}}
	want := []admin.LiveNodeStatus{{CredentialID: 1, ProviderID: 2, ProviderCode: "anthropic", HealthStatus: "healthy"}}
	if err := cache.refreshWith(func(context.Context) ([]admin.LiveNodeStatus, error) {
		return want, nil
	}, context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if got := cache.get(); len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("snapshot after success = %+v", got)
	}

	errBoom := errors.New("database unavailable")
	if err := cache.refreshWith(func(context.Context) ([]admin.LiveNodeStatus, error) {
		return nil, errBoom
	}, context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("refresh error = %v, want %v", err, errBoom)
	}
	got := cache.get()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("last good snapshot was not retained: %+v", got)
	}

	got[0].ProviderCode = "mutated"
	if cache.get()[0].ProviderCode != "anthropic" {
		t.Fatal("cache returned internal slice instead of a copy")
	}
}

// --- OBS-BE4 (V3.3-OBS, 2026-08-15): node projection disable/cooldown fields ---

// TestNodeDisableKindSnapshot 锚定 disable_kind 的口径：
// manual_disabled 优先于一切 → "manual"；availability/quota/circuit 任一
// 非 ok（availability≠ready / quota≠ok / circuit≠closed）→ "system"；
// 全部健康或空值（COALESCE 前的缺省）→ ""（wire 上省略）。
func TestNodeDisableKindSnapshot(t *testing.T) {
	cases := []struct {
		name              string
		manual            bool
		avail, quota, ckt string
		want              string
	}{
		{"all healthy", false, "ready", "ok", "closed", ""},
		{"all empty", false, "", "", "", ""},
		{"manual wins over system", true, "rate_limited", "ok", "open", "manual"},
		{"manual alone", true, "ready", "ok", "closed", "manual"},
		{"availability cooling", false, "cooling", "ok", "closed", "system"},
		{"availability suspended", false, "suspended", "ok", "closed", "system"},
		{"quota exhausted", false, "ready", "permanently_exhausted", "closed", "system"},
		{"circuit open", false, "ready", "ok", "open", "system"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nodeDisableKind(c.manual, c.avail, c.quota, c.ckt); got != c.want {
				t.Fatalf("nodeDisableKind(%v, %q, %q, %q) = %q, want %q",
					c.manual, c.avail, c.quota, c.ckt, got, c.want)
			}
		})
	}
}

// TestEarliestTimeSnapshot：system_recover_at = 三列中最早的非空值；
// 全空 → nil（wire 上省略）。注意返回值是 "最早"，不是任意非空。
func TestEarliestTimeSnapshot(t *testing.T) {
	if got := earliestTime(nil, nil, nil); got != nil {
		t.Fatalf("all nil must yield nil, got %v", got)
	}
	t1 := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	t3 := t1.Add(-1 * time.Hour)
	if got := earliestTime(&t1, &t2, &t3); got == nil || !got.Equal(t3) {
		t.Fatalf("earliestTime = %v, want %v", got, t3)
	}
	if got := earliestTime(nil, &t2, nil); got == nil || !got.Equal(t2) {
		t.Fatalf("earliestTime single non-nil = %v, want %v", got, t2)
	}
}

// TestFPNodeProjectionSnapshot 锚定 fpslot NodeState → OBS-BE4 字段聚合：
//
//   - 冷却中（Disabled && DisabledUntil>now）才计 fp_disabled；冷却已过期
//     的残留 Disabled=true 不算（与 NodeState.IsUsable 的到期语义一致）；
//   - fp_disabled_until 取最近一次禁用（max LastDisabledAt）对应模型的
//     DisabledUntil；LastDisabledAt 缺失时回退比较 DisabledUntil；
//   - last_error_at 取全部绑定模型的最大 LastFailureAt；
//   - 全部字段缺省时为 nil（wire 上省略）。
func TestFPNodeProjectionSnapshot(t *testing.T) {
	now := int64(1786468800) // 2026-08-15 12:00:00 UTC

	// 健康节点：无任何投影字段。
	proj := fpNodeProjection(now, []*credentialfpslot.NodeState{
		{CredentialID: 1, Model: "m1"},
	})
	if proj.fpDisabled != nil || proj.fpDisabledUntil != nil || proj.lastErrorAt != nil {
		t.Fatalf("healthy node must project nothing, got %+v", proj)
	}

	// 冷却中 + 历史失败：fp_disabled=true，until 取最近一次禁用。
	stOld := &credentialfpslot.NodeState{
		CredentialID: 1, Model: "m1",
		Disabled: true, DisabledUntil: now + 100, LastDisabledAt: now - 600,
		LastFailureAt: now - 700,
	}
	stNew := &credentialfpslot.NodeState{
		CredentialID: 1, Model: "m2",
		Disabled: true, DisabledUntil: now + 300, LastDisabledAt: now - 60,
		LastFailureAt: now - 50,
	}
	proj = fpNodeProjection(now, []*credentialfpslot.NodeState{stOld, stNew})
	if proj.fpDisabled == nil || !*proj.fpDisabled {
		t.Fatalf("fp_disabled must be true, got %+v", proj)
	}
	if proj.fpDisabledUntil == nil || proj.fpDisabledUntil.Unix() != now+300 {
		t.Fatalf("fp_disabled_until must follow the most recent disable (m2), got %v", proj.fpDisabledUntil)
	}
	if proj.lastErrorAt == nil || proj.lastErrorAt.Unix() != now-50 {
		t.Fatalf("last_error_at must be max LastFailureAt, got %v", proj.lastErrorAt)
	}

	// 冷却已过期的残留 Disabled：视为已恢复。
	stExpired := &credentialfpslot.NodeState{
		CredentialID: 1, Model: "m1",
		Disabled: true, DisabledUntil: now - 1, LastDisabledAt: now - 400,
		LastFailureAt: now - 400,
	}
	proj = fpNodeProjection(now, []*credentialfpslot.NodeState{stExpired})
	if proj.fpDisabled != nil || proj.fpDisabledUntil != nil {
		t.Fatalf("expired cooldown must not project disabled, got %+v", proj)
	}
	if proj.lastErrorAt == nil || proj.lastErrorAt.Unix() != now-400 {
		t.Fatalf("expired cooldown still has last_error_at, got %+v", proj)
	}

	// LastDisabledAt 缺失（P0 字段前的旧数据）：回退用 DisabledUntil 排序。
	stNoStamp := &credentialfpslot.NodeState{
		CredentialID: 1, Model: "m1",
		Disabled: true, DisabledUntil: now + 240,
	}
	proj = fpNodeProjection(now, []*credentialfpslot.NodeState{stNoStamp})
	if proj.fpDisabled == nil || !*proj.fpDisabled {
		t.Fatalf("fallback disable flag failed, got %+v", proj)
	}
	if proj.fpDisabledUntil == nil || proj.fpDisabledUntil.Unix() != now+240 {
		t.Fatalf("fallback until failed, got %v", proj.fpDisabledUntil)
	}

	// 空切片 / nil 成员安全。
	proj = fpNodeProjection(now, nil)
	if proj.fpDisabled != nil || proj.fpDisabledUntil != nil || proj.lastErrorAt != nil {
		t.Fatalf("empty states must project nothing, got %+v", proj)
	}
	proj = fpNodeProjection(now, []*credentialfpslot.NodeState{nil})
	if proj.fpDisabled != nil {
		t.Fatalf("nil state entry must be skipped, got %+v", proj)
	}
}
