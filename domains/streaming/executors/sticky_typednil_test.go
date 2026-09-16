package executors

import (
	"testing"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

// TestStickyCacheTypedNilRedisStoreNoPanic: 252 无 Redis 部署实抓（2026-09-17）
// —— cmd/gateway 持 `var stickyStore *ursmcache.StickyStore` 在 Redis 不可用
// 时保持 nil，SetRedisStore 把 typed-nil 塞进 StickyRedisStore 接口，
// GetMultiLevel 的 `store != nil` 守卫失效，GetLevel 在 nil receiver 上
// panic（每请求 500，chat handler recover 兜底）。修复后 SetRedisStore 把
// typed-nil 归一化为非 typed nil，GetMultiLevel 必须走纯内存路径不 panic。
func TestStickyCacheTypedNilRedisStoreNoPanic(t *testing.T) {
	s := NewStickyCache()

	var typedNil *ursmcache.StickyStore
	s.SetRedisStore(typedNil)

	if got := s.redisStoreSnapshot(); got != nil {
		t.Fatalf("typed-nil store must be normalised to untyped nil, got %T", got)
	}

	appID, keyID := 7, 8
	// 无 Redis 时 GetMultiLevel 必须安全返回 not-found（修复前在此 panic）。
	cred := s.GetMultiLevel("t9", &appID, &keyID, "default", "sess9", "m")
	if cred.Found {
		t.Fatalf("typed-nil store must not report a sticky hit: %+v", cred)
	}

	// RecordSuccessMultiLevel 双写路径同样不得 panic。
	s.RecordSuccessMultiLevel("t9", &appID, &keyID, "default", "sess9", "m", 42)
}
