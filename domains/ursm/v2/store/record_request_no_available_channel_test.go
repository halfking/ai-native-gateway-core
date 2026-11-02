package store

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// ── 第十一轮 · F-C 的 URSM 侧闸门（2026-11-02）────────────────────────────
//
// 「无可用后端」（no_available_channel）是**免费档容忍闭集**的一等公民。
//
// ★ 为什么它属于这个闭集：它的**凭据级**语义是「中转分组暂时没有通道」
//   （"No available channel for model X under group Y"）⇒ 属于 245
//   free-capacity plan 说的那类「flaky 但偶尔可用」的免费供给 ⇒
//   连败不该把它硬禁用（免费档是「有总比没强」）。
//
// ★ 第九轮（F-B）把执行器写进 URSM 的 err_kind 从 "transient" 换成了
//   "no_available_channel"，而本闭集**没有登记**它 ⇒ 免费档节点在连败 3 次
//   后被**硬禁用**（disabled=1, available=0, cool 300s）⇒ 免费额度被白扔
//   —— 正是这个闭集存在的理由被绕过。
//
// ★ 本门是那道回归的**行为级**复现：把 err_kind 换成
//   "no_available_channel" 跑免费档连败 ⇒ 必须仍然被容忍。

// TestRecordRequestFreeBillingNoAvailableChannelDoesNotDisable 钉门 U：
// 免费档 + no_available_channel 连败 ⇒ **不得**硬禁用节点。
func TestRecordRequestFreeBillingNoAvailableChannelDoesNotDisable(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	for i := 0; i < 5; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:7:nac",
			"ursm:v2:win:1m:7:nac", "ursm:v2:win:5m:7:nac", "ursm:v2:win:30m:7:nac",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "no_available_channel",
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "nac-fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
				BillingMode:  "free",
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	disabled, err := rdb.HGet(ctx, "ursm:v2:node:7:nac", "disabled").Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled == "1" {
		t.Fatal("免费档的 no_available_channel 连败把节点硬禁用了（disabled=1）⇒ " +
			"免费额度被白扔，正是 free_transient 容忍闭集要防的事")
	}
	available, err := rdb.HGet(ctx, "ursm:v2:node:7:nac", "available").Result()
	if err != nil {
		t.Fatalf("HGet available: %v", err)
	}
	if available != "1" {
		t.Fatalf("available=%s, want 1（免费档容忍 ⇒ 只软降级，不硬禁用）", available)
	}
	// 容忍分支必须留下**可观测的理由**，否则「没硬禁用」和「什么都没记」分不清。
	reason, err := rdb.HGet(ctx, "ursm:v2:node:7:nac", "disabled_reason").Result()
	if err != nil {
		t.Fatalf("HGet disabled_reason: %v", err)
	}
	if reason != "free_transient_tolerated" {
		t.Fatalf("disabled_reason=%q, want free_transient_tolerated", reason)
	}
	// fail_streak 仍要累计（可观测性不受容忍影响）。
	failStreak, err := rdb.HGet(ctx, "ursm:v2:node:7:nac", "fail_streak").Result()
	if err != nil || failStreak != "5" {
		t.Fatalf("fail_streak=%q err=%v, want 5", failStreak, err)
	}
}

// TestRecordRequestFreeBillingNoAvailableChannelTolerated 说明「被容忍」
// 这一事实**必须**由闭集登记支撑 —— 门 U 的阴性对照：
// 同一个免费档 + 同样连败次数，把 err_kind 换成一个**不在**闭集里的值
// ⇒ 必须**照常硬禁用**。
//
// ★ 没有这道对照的话，「no_available_channel 恰好没被硬禁用」可能只是
//
//	因为脚本压根没走到硬禁用分支（而不是因为闭集登记了它）
//	⇒ 门 U 会变成恒绿。
func TestRecordRequestFreeBillingNoAvailableChannelTolerated(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()

	for i := 0; i < 5; i++ {
		res, err := s.RecordRequest(ctx, "ursm:v2:node:8:ctrl",
			"ursm:v2:win:1m:8:ctrl", "ursm:v2:win:5m:8:ctrl", "ursm:v2:win:30m:8:ctrl",
			RecordOutcome{
				Success:      false,
				ErrorKind:    "auth", // 不在 transient_kinds 里 ⇒ 不容忍
				NowMs:        now + int64(i)*1000,
				LatencyMs:    100,
				RequestID:    "ctrl-fail-" + string(rune('0'+i)),
				NodeTTL:      time.Minute,
				Window5mTTL:  6 * time.Minute,
				Window30mTTL: 35 * time.Minute,
				CoolSeconds:  300,
				BillingMode:  "free",
			})
		if err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if res.Status != "applied" {
			t.Fatalf("failure %d: status=%s", i, res.Status)
		}
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	disabled, err := rdb.HGet(ctx, "ursm:v2:node:8:ctrl", "disabled").Result()
	if err != nil && err != redis.Nil {
		t.Fatalf("HGet disabled: %v", err)
	}
	if disabled != "1" {
		t.Fatalf("disabled=%q, want 1 —— 不在闭集里的 kind 在免费档下必须照常硬禁用；"+
			"若这里也变成「不硬禁用」，说明门 U 的对照失效（脚本没走到硬禁用分支）", disabled)
	}
}

// luaTransientKinds 解析 record_request*.lua 里的 transient_kinds 闭集。
var luaTransientKinds = regexp.MustCompile(`(?s)local transient_kinds = \{(.*?)\}`)

func readLuaTransientKinds(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	m := luaTransientKinds.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s 里找不到 transient_kinds 闭集", path)
	}
	out := map[string]bool{}
	for _, entry := range regexp.MustCompile(`(\w+)\s*=\s*true`).FindAllStringSubmatch(m[1], -1) {
		out[entry[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s 的 transient_kinds 解析出 0 项", path)
	}
	return out
}

// TestLuaTransientKindsSetsAreIdentical 钉门 U 的**另一半**（结构级）：
// 两个 Lua 脚本（record_request.lua / record_request_dual.lua）各有**一份**
// transient_kinds 闭集 ⇒ 它们必须**逐项一致**。
//
// ★ 为什么要有这道门：双写脚本（dual）只在带 key set 的路径上生效
//
//	（Store.RecordRequestKeySet），行为级门 U 走的是 RecordRequest
//	⇒ **只覆盖了其中一份** ⇒ 只改 dual 那份不会被任何行为门抓到。
//	⇒ 这道结构门把两份锁在一起。
func TestLuaTransientKindsSetsAreIdentical(t *testing.T) {
	legacy := readLuaTransientKinds(t, "record_request.lua")
	dual := readLuaTransientKinds(t, "record_request_dual.lua")

	for kind := range legacy {
		if !dual[kind] {
			t.Errorf("record_request_dual.lua 的 transient_kinds 缺少 %q —— "+
				"两份闭集必须逐项一致（dual 走的是 RecordRequestKeySet 路径，"+
				"行为级门覆盖不到它）", kind)
		}
	}
	for kind := range dual {
		if !legacy[kind] {
			t.Errorf("record_request.lua 的 transient_kinds 缺少 %q —— "+
				"两份闭集必须逐项一致", kind)
		}
	}
}

// TestLuaTransientKindsContainNoAvailableChannel 把「这一档必须在容忍闭集里」
// 钉在**闭集本体**上（不依赖行为门走哪条路径）。
func TestLuaTransientKindsContainNoAvailableChannel(t *testing.T) {
	for _, path := range []string{"record_request.lua", "record_request_dual.lua"} {
		kinds := readLuaTransientKinds(t, path)
		if !kinds["no_available_channel"] {
			t.Errorf("%s 的 transient_kinds 没有 no_available_channel ⇒ "+
				"免费档在连败 3 次后会被硬禁用 ⇒ 免费额度被白扔", path)
		}
	}
}
