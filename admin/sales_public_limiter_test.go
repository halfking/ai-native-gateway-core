package admin

// sales_public_limiter_test.go —— 公开面真限速的纯单元判据（2026-11-08
// 审计 P2-2 修复：此前注释声称限速但实现为零）。

import "testing"

func TestIPWindowLimiterBoundsPerWindow(t *testing.T) {
	l := newIPWindowLimiter()
	const max = 3
	for i := 0; i < max; i++ {
		if !l.allow("1.2.3.4", max) {
			t.Fatalf("request %d within limit must pass", i+1)
		}
	}
	if l.allow("1.2.3.4", max) {
		t.Errorf("request %d must be rejected (fixed window)", max+1)
	}
	// 其他 IP 不受影响（per-key 隔离）。
	if !l.allow("5.6.7.8", max) {
		t.Errorf("different IP must have its own bucket")
	}
	// max<=0 = 关闭限速（阀门语义：0 = 关，不是全拒）。
	for i := 0; i < 100; i++ {
		if !l.allow("9.9.9.9", 0) {
			t.Fatalf("max=0 must disable the limiter (rejected at %d)", i+1)
		}
	}
}

func TestIPWindowLimiterWindowRollover(t *testing.T) {
	l := newIPWindowLimiter()
	l.window = 0 // 立即翻窗
	const max = 1
	if !l.allow("2.2.2.2", max) {
		t.Fatalf("first request must pass")
	}
	if !l.allow("2.2.2.2", max) {
		t.Errorf("after window rollover the bucket must reset")
	}
}

func TestIPWindowLimiterBucketResetBound(t *testing.T) {
	l := newIPWindowLimiter()
	l.maxKeys = 10
	for i := 0; i < 100; i++ {
		_ = l.allow(string(rune('a'+i%26))+string(rune('0'+i%10))+"."+string(rune(i)), 5)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 10 {
		t.Errorf("bucket map must be reset at maxKeys, got %d keys", len(l.buckets))
	}
}
