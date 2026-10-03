package persist

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// 818 回归门。
//
// 本文件钉住三件事，其中第三件是**唯一会静默丢数据**的失败模式：
//
//  1. payload 剔除 7 个与 typed 列重复的键（省 70.8 B/行 = payload 的 24%）；
//  2. 24 个已知键正确落到 typed 列，且**键缺失时落 NULL 而非零值**；
//  3. **payload 的排除方向是「白名单剔除」** —— hash 里新出现的、
//     818 不知道的键必须仍然留在 payload 中。
//
// 第 3 条为什么是最要紧的：hash 来自 Redis HGETALL，schema 会演进
// （writer.go 原注释明写「以防未来需要恢复其他字段（pricing, concurrency,
// etc.）」）。若把排除表写成「只保留已知键」的白名单方向，
// hash 每加一个字段，该字段就会**静默**从审计仓里消失 —— 编译通过、
// 测试通过、DB 不报错，只是数据没了。
func collectOne(t *testing.T, fields map[string]string) Row {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-a", 42, "model-a")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := rdb.HSet(ctx, key, fields).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	return rows[0]
}

func payloadKeys(t *testing.T, r Row) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Payload, &m); err != nil {
		t.Fatalf("payload unmarshal: %v (%s)", err, r.Payload)
	}
	return m
}

func Test818PayloadStripsKeysThatAlreadyHaveTypedColumns(t *testing.T) {
	r := collectOne(t, map[string]string{
		// 7 个重复键：必须从 payload 消失（它们已在 typed 列里）
		"available": "1", "generation": "43", "source_priority": "20",
		"fail_streak": "2", "sr_1m": "0.5", "sr_5m": "0.6", "sr_30m": "0.7",
		// 2026-10-03 修：这里原用 last_err 当「非重复键」样本，但那条断言的前提是错的 ——
		// writer.go 早就有 assignStr(&row.LastErr, hash["last_err"])，表里也有 last_err 列。
		// 「last_err 没有 typed 列对应」是**事实错误**，而 payloadDuplicateKeys 当初正是
		// 照着同一个错误假设只排了 7 个键，导致另外 24 个重复键双双落盘
		// （生产实测 payload 35 键里 27 键重复，全表多占约 3436 MB）。
		// 改用真正没有 typed 列的键 —— 2026-10-03 生产 payload 实测存在的 8 个之一。
		"successes_1m": "7",
	})
	got := payloadKeys(t, r)

	for _, k := range payloadDuplicateKeys {
		if _, dup := got[k]; dup {
			t.Errorf("payload still carries %q, which already has a typed column", k)
		}
	}
	if _, ok := got["successes_1m"]; !ok {
		t.Error("payload dropped successes_1m, which has no typed column duplicate")
	}
}

func Test818PayloadKeepsUnknownHashKeysForForwardCompat(t *testing.T) {
	// 这三个键模拟 818 之后 hash 新增的字段。它们的值在生产上可能不存在，
	// 但测试必须钉住：任何未被列为重复键的键都要留在 payload 里。
	future := map[string]string{
		"pricing_tier_v2":  "gold",
		"concurrency_slot": "17",
		"brand_new_field":  "whatever",
	}
	fields := map[string]string{"available": "1"}
	for k, v := range future {
		fields[k] = v
	}

	got := payloadKeys(t, collectOne(t, fields))
	for k, v := range future {
		gv, ok := got[k]
		if !ok {
			t.Errorf("payload dropped unknown hash key %q — 818 的排除表方向反了，"+
				"hash 演进时该字段会静默丢失", k)
			continue
		}
		if gv != v {
			t.Errorf("payload[%q] = %v, want %v", k, gv, v)
		}
	}
}

func Test818TypedColumnsArePopulatedFromHash(t *testing.T) {
	r := collectOne(t, map[string]string{
		"available":              "1",
		"updated_at_ms":          "1790915622313",
		"last_probe_at_ms":       "1790915622314",
		"last_probe_latency_ms":  "401",
		"last_err":               "http_404",
		"disabled":               "0",
		"manual_hold":            "1",
		"success_count":          "12",
		"lat_ewma_ms":            "357",
		"empty_response_rate_1m": "0.25",
		"manual_reason":          "ops pinned",
	})

	if r.UpdatedAtMS == nil || *r.UpdatedAtMS != 1790915622313 {
		t.Errorf("UpdatedAtMS = %v, want 1790915622313", r.UpdatedAtMS)
	}
	if r.LastProbeLatencyMS == nil || *r.LastProbeLatencyMS != 401 {
		t.Errorf("LastProbeLatencyMS = %v, want 401", r.LastProbeLatencyMS)
	}
	if r.LastErr == nil || *r.LastErr != "http_404" {
		t.Errorf("LastErr = %v, want http_404", r.LastErr)
	}
	// "0" 必须落成非 nil 的 false，而不是 nil：
	// 「显式采集到 0」和「这一时刻没这个键」在排障时结论相反。
	if r.Disabled == nil || *r.Disabled != false {
		t.Errorf("Disabled = %v, want non-nil false", r.Disabled)
	}
	if r.ManualHold == nil || *r.ManualHold != true {
		t.Errorf("ManualHold = %v, want non-nil true", r.ManualHold)
	}
	if r.SuccessCount == nil || *r.SuccessCount != 12 {
		t.Errorf("SuccessCount = %v, want 12", r.SuccessCount)
	}
	if r.LatEWMAMS == nil || *r.LatEWMAMS != 357 {
		t.Errorf("LatEWMAMS = %v, want 357", r.LatEWMAMS)
	}
	if r.EmptyResponseRate1m == nil || *r.EmptyResponseRate1m != 0.25 {
		t.Errorf("EmptyResponseRate1m = %v, want 0.25", r.EmptyResponseRate1m)
	}
	if r.ManualReason == nil || *r.ManualReason != "ops pinned" {
		t.Errorf("ManualReason = %v, want 'ops pinned'", r.ManualReason)
	}
}

func Test818MissingHashKeysStayNilNotZero(t *testing.T) {
	// 只有一个极简 hash：绝大多数 818 列的键根本不存在。
	r := collectOne(t, map[string]string{"available": "1"})

	for name, got := range map[string]any{
		"UpdatedAtMS":          r.UpdatedAtMS,
		"LastProbeLatencyMS":   r.LastProbeLatencyMS,
		"LastErr":              r.LastErr,
		"Disabled":             r.Disabled,
		"SuccessCount":         r.SuccessCount,
		"LatEWMAMS":            r.LatEWMAMS,
		"ManualReason":         r.ManualReason,
		"EventSeq":             r.EventSeq,
		"EmptyResponseRate30m": r.EmptyResponseRate30m,
	} {
		if !isNilPtr(got) {
			t.Errorf("%s = %v, want nil for an absent hash key (0 would read as a real measurement)", name, got)
		}
	}
}

func Test818MalformedValuesAreDroppedNotZeroed(t *testing.T) {
	// 不可解析的值必须落 nil，而不是被静默当作 0 —— 否则库里会出现
	// 一个看起来是真的、实际是解析失败的读数。
	r := collectOne(t, map[string]string{
		"available":     "1",
		"last_err":      "http_404",
		"success_count": "not-a-number",
		"lat_ewma_ms":   "NaN-ish",
		"disabled":      "maybe",
	})
	if r.SuccessCount != nil {
		t.Errorf("SuccessCount = %v, want nil for unparseable value", r.SuccessCount)
	}
	if r.LatEWMAMS != nil {
		t.Errorf("LatEWMAMS = %v, want nil for unparseable value", r.LatEWMAMS)
	}
	if r.Disabled != nil {
		t.Errorf("Disabled = %v, want nil for a value that is neither 0 nor 1", r.Disabled)
	}
	// 同一次采集里可解析的键仍要正常落值。
	if r.LastErr == nil || *r.LastErr != "http_404" {
		t.Errorf("LastErr = %v, want http_404 (one bad key must not drop the others)", r.LastErr)
	}
}

// isNilPtr 用反射判断「任意类型的指针是否为 nil」，避免为每个类型各写一遍断言。
func isNilPtr(v any) bool {
	switch p := v.(type) {
	case *int64:
		return p == nil
	case *int:
		return p == nil
	case *float64:
		return p == nil
	case *bool:
		return p == nil
	case *string:
		return p == nil
	case nil:
		return true
	default:
		t := v
		_ = t
		return false
	}
}

var _ = context.Background
