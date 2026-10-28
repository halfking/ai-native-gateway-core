package executors

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sticky_p1b_test.go — P1-B fixes (audit 2026-10-28 §4).
//
// ① TTL 配置接线：settings.StickyTTLConfig 此前 0 消费者，sticky.go 硬编码
//    同值 —— LiteLLM #28427 的「配置说一套、代码做另一套」形态。接线后
//    改配置必须真实生效（本文件钉住）。
// ② 删除落 DB：DeleteMultiLevel / ClearForCredential 此前只删内存+Redis，
//    重启 RestoreFromDB 把已删绑定复活（L3 24h 窗口内 fatal 凭据可复活）。
//    DB 面删除用 sqlmock 级别的假池钉不住（dbPool 是 *pgxpool.Pool 具体类
//    型）⇒ 本文件钉【行为契约】：nil 池时删除路径绝不 panic 且内存面照常
//    清空（生产接线由 admin 5 个调用方传真池，错误路径 best-effort 已由
//    slog.Debug 兜底）。真正的 DB 断言属于 TEST_PG_URL 契约测试面。

func TestStickyTTLConfigWiring_Production(t *testing.T) {
	// 接线不改行为：默认配置下的 TTL 与历史硬编码逐字节同值。
	cfg := settings.DefaultStickyTTLConfig()
	setStickyTTLConfigForTest(cfg)
	defer setStickyTTLConfigForTest(settings.DefaultStickyTTLConfig())

	assert.Equal(t, 30*time.Second, calculateSessionStickyTTL("text-embedding-3-large"))
	assert.Equal(t, 10*time.Minute, calculateSessionStickyTTL("gpt-4-turbo"))
	assert.Equal(t, 30*time.Minute, calculateSessionStickyTTL("gpt-3.5-turbo-instruct"))
	assert.Equal(t, 15*time.Minute, calculateSessionStickyTTL("unknown-model-xyz"))
	assert.Equal(t, 24*time.Hour, stickyL3TTL())
	assert.Equal(t, 15*time.Minute, stickyTTLConfig.DefaultL1TTL)
}

func TestStickyTTLConfigWiring_ConfigActuallyDrivesTTL(t *testing.T) {
	// 防假接线：把配置改成非历史值 ⇒ calculateSessionStickyTTL 必须跟着变
	//（旧硬编码实现下这条红 —— 那正是 P1-B-① 要治的「配置漂移」盲区）。
	defer setStickyTTLConfigForTest(settings.DefaultStickyTTLConfig())
	setStickyTTLConfigForTest(settings.StickyTTLConfig{
		EmbeddingTTL:      7 * time.Second,
		ChatTTL:           3 * time.Minute,
		CompletionTTL:     11 * time.Minute,
		DefaultL1TTL:      4 * time.Minute,
		ClientModelTTL:    2 * time.Hour,
		ClientBaselineTTL: 6 * time.Hour,
	})

	assert.Equal(t, 7*time.Second, calculateSessionStickyTTL("text-embedding-3-large"), "embedding TTL must come from config")
	assert.Equal(t, 3*time.Minute, calculateSessionStickyTTL("gpt-4-turbo"), "chat TTL must come from config")
	assert.Equal(t, 11*time.Minute, calculateSessionStickyTTL("text-davinci-003"), "completion TTL must come from config")
	assert.Equal(t, 4*time.Minute, calculateSessionStickyTTL("unknown-model-xyz"), "default TTL must come from config")
	assert.Equal(t, 6*time.Hour, stickyL3TTL(), "L3 TTL must come from config")
}

// TestClearForCredential_NilDBDoesNotPanic pins the P1-B-② error path: the
// new DB delete is best-effort — a cache with no DB pool (every existing
// unit-test shape, and deployments without sticky persistence) must clear
// the in-memory face exactly as before, never panic.
func TestClearForCredential_NilDBDoesNotPanic(t *testing.T) {
	cache := NewStickyCache()
	cache.Set("k1", 42, time.Minute)
	cache.Set("k2", 42, time.Minute)
	cache.Set("k3", 99, time.Minute)

	cleared, err := cache.ClearForCredential(42)
	require.NoError(t, err)
	assert.Equal(t, 2, cleared)

	_, found := cache.Get("k1")
	assert.False(t, found)
	_, found = cache.Get("k3")
	assert.True(t, found, "other credentials' bindings must survive")
}

// TestDeleteMultiLevel_NilDBDoesNotPanic pins the same contract on the
// multi-level path (currently zero production callers, but it is an exported
// API and the audit names it explicitly).
func TestDeleteMultiLevel_NilDBDoesNotPanic(t *testing.T) {
	cache := NewStickyCache()
	appID, apiKeyID := 1, 2
	cache.RecordSuccessMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123)

	assert.NotPanics(t, func() {
		cache.DeleteMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4", 123)
	})

	lookup := cache.GetMultiLevel("tenant", &appID, &apiKeyID, "profile", "session", "gpt-4")
	assert.False(t, lookup.Found, "in-memory face must be cleared even without a DB pool")
}
