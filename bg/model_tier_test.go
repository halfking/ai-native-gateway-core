package bg

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeModelKey(t *testing.T) {
	cases := map[string]string{
		"GPT-5.4":       "gpt-5.4",
		" glm-5.2 ":     "glm-5.2",
		"Claude-Sonnet": "claude-sonnet",
		"":              "",
		"   ":           "",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeModelKey(in), "input=%q", in)
	}
}

// newTierWithSet builds a ModelTier and publishes an immutable featuredSet
// directly (no DB), so IsFeaturedModel can be tested without Postgres.
func newTierWithSet(static, usage []string) *ModelTier {
	m := NewModelTier(nil, ModelTierConfig{RefreshInterval: time.Minute})
	fs := &featuredSet{static: map[string]struct{}{}, usage: map[string]struct{}{}}
	for _, s := range static {
		if k := normalizeModelKey(s); k != "" {
			fs.static[k] = struct{}{}
		}
	}
	for _, s := range usage {
		if k := normalizeModelKey(s); k != "" {
			fs.usage[k] = struct{}{}
		}
	}
	m.cur.Store(fs)
	return m
}

func TestIsFeaturedModel_StaticAndUsage(t *testing.T) {
	m := newTierWithSet([]string{"gpt-5.4", "Claude-Sonnet"}, []string{"glm-5.2"})

	assert.True(t, m.IsFeaturedModel("gpt-5.4", ""))        // static hit (raw)
	assert.True(t, m.IsFeaturedModel("GPT-5.4", ""))        // case-insensitive
	assert.True(t, m.IsFeaturedModel("claude-sonnet", ""))  // static normalized
	assert.True(t, m.IsFeaturedModel("glm-5.2", ""))        // usage hit
	assert.False(t, m.IsFeaturedModel("deepseek-chat", "")) // neither
	assert.False(t, m.IsFeaturedModel("", ""))              // empty

	// Canonical fallback: raw not in set but canonical is static.
	m2 := newTierWithSet([]string{"claude-sonnet-5"}, nil)
	assert.True(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", "claude-sonnet-5"))
	assert.False(t, m2.IsFeaturedModel("anthropic/claude-sonnet-5", ""))
}

func TestIsFeaturedModel_NilSafe(t *testing.T) {
	var m *ModelTier
	assert.False(t, m.IsFeaturedModel("gpt-5.4", "")) // nil receiver

	m2 := NewModelTier(nil, ModelTierConfig{})         // no snapshot stored
	assert.False(t, m2.IsFeaturedModel("gpt-5.4", "")) // empty snapshot
}

// TestGlobalIsFeaturedModel_AndPriority verifies the package-level accessor +
// FeaturedQueuePriority honor the global tier, and restore state afterward.
func TestGlobalIsFeaturedModel_AndPriority(t *testing.T) {
	prev := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prev) })

	SetGlobalModelTier(newTierWithSet([]string{"gpt-5.4"}, nil))
	assert.True(t, globalIsFeaturedModel("gpt-5.4", ""))
	assert.False(t, globalIsFeaturedModel("other-model", ""))

	// Featured ⇒ setting-driven priority (default 80 when settings Global nil).
	assert.Equal(t, int16(80), FeaturedQueuePriority("gpt-5.4", 60))
	// Non-featured ⇒ fallback.
	assert.Equal(t, int16(60), FeaturedQueuePriority("other-model", 60))
}

// TestModelTier_StopWithoutStart is a regression guard: Stop must be safe to
// call on a tier that was never started (done channel still open).
func TestModelTier_StopWithoutStart(t *testing.T) {
	m := NewModelTier(nil, ModelTierConfig{})
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked on a never-started tier")
	}
}

func TestModelTier_StopConcurrent(t *testing.T) {
	m := NewModelTier(nil, ModelTierConfig{})
	// Mark the tier as started without requiring a live database. This keeps the
	// test focused on Stop's close-once lifecycle guarantee.
	m.once.Store(true)

	const callers = 100
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			m.Stop()
		}()
	}
	wg.Wait()

	select {
	case <-m.done:
	default:
		t.Fatal("concurrent Stop calls did not close done")
	}
}

func TestModelTier_LoopRefreshUsesParentContext(t *testing.T) {
	src := sourceFromFile(t, "model_tier.go")
	loopStart := strings.Index(src, "func (m *ModelTier) loop(ctx context.Context)")
	if loopStart < 0 {
		t.Fatal("ModelTier.loop not found")
	}
	loopSrc := src[loopStart:]
	if end := strings.Index(loopSrc, "\nfunc "); end >= 0 {
		loopSrc = loopSrc[:end]
	}
	assert.Contains(t, loopSrc, "context.WithTimeout(ctx, 10*time.Second)")
	assert.NotContains(t, loopSrc, "context.WithTimeout(context.Background(), 10*time.Second)")
}

// guard: the SQL inside ModelTier.refresh must reference the real schema column
// names. It does not execute the query (that would need a DB) but it asserts
// the literal strings so a regression like "model" (a nonexistent column) is
// caught at unit-test time instead of at the 10min refresh.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：读源从 v1 热表换成**会话族热腿**
// （v1 族已 DROP，提及即整条语句 42P01）；模型名口径从
// COALESCE(outbound_model, client_model) 换成会话族的 raw_model_name —— 它
// 就是那个 COALESCE 的同义列（取值口径登记见 internal/sessionv2mirror/
// s1a_fields.go）。
func TestModelTier_SQLReferencesActualColumns(t *testing.T) {
	// We can't import the function body directly; instead we assert the source
	// text via string-grep on the SQL fragments we know are inside refresh().
	// The test fails if the developer accidentally references a missing column.
	mustContain := []string{
		"rl.raw_model_name", // real model-name column (session family)
		"WHERE rl.success",  // column exists
		"WHERE tenant_id = $1",
		"GROUP BY rl.raw_model_name", // dedup (audit #11)
	}
	src := sourceFromFile(t, "model_tier.go")
	// ★ 2026-11-11（断链⑦）：feat_names CTE 移到 featured_sources.go（与
	// selfcheck/model_probe watchdog 共用同一出处）⇒ 模式守卫改为读合并源，
	// 牙齿不减：featured 装载 SQL 无论落在哪个文件，列名守卫都覆盖。
	src += sourceFromFile(t, "featured_sources.go")
	for _, fragment := range mustContain {
		assert.True(t, strings.Contains(src, fragment),
			"model_tier.go+featured_sources.go must contain %q (audit guard)", fragment)
	}
	// 源必须是会话族热腿。
	// ⚠ 用正则而不是子串：`+` 两侧的空格是排版细节（`FROM `+db.X()+` rl` 与
	// `FROM ` + db.X() + ` rl` 都合法），子串匹配会随排版变化假红。
	sessionSourceRE := regexp.MustCompile("FROM\\s+`\\s*\\+\\s*db\\.SessionFamilyTurnsHotSourceSQL\\(\\)\\s*\\+\\s*`\\s+rl")
	assert.True(t, sessionSourceRE.MatchString(src),
		"model_tier.go must read the session-family hot leg (audit guard)")
	// 负向断言：不得再引用退役的 v1 族（迁移 890 已 DROP ⇒ 整条语句 42P01）。
	// ⚠ 正向对照：上面那条会话族源断言 —— 只删 v1 不加会话族会被抓住。
	// ⚠ 边界：文件里的历史注释提到过 "_request_logs_hot"（无 FROM），
	// 所以 needle 必须带 "FROM " 才不会踩到注释。
	// ⚠ 范围：负向守卫只看 model_tier.go —— featured_sources.go 的
	// `SELECT model FROM static` 是 CTE 别名列（非会话族物理列），不在本守卫语义内。
	assert.False(t, strings.Contains(src, "FROM _request_logs_hot"),
		"model_tier.go must not read the retired v1 family (migration 890 dropped it)")
	// Guard: must NOT reference the bare `model` column from request_logs_hot.
	// Use a tight pattern: a SELECT/GROUP BY/ORDER BY column reference, not just
	// any substring (avoid false-positive matches like `raw_model`, `client_model`,
	// or `outbound_model`). `\bmodel\b` matches a bare token `model` only when
	// it's not preceded by `raw_` or any column prefix.
	notAllowed := regexp.MustCompile(`(?im)(?:select|where|group\s+by|order\s+by)\s+(?:\w+\.)?model\b`)
	assert.False(t, notAllowed.MatchString(sourceFromFile(t, "model_tier.go")),
		"the session family has no bare `model` column; use rl.raw_model_name")

	// Tier-aware priority must return the featured default when the global
	// tier recognises the model. We restore the previous global state on exit.
	prevTier := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prevTier) })
	SetGlobalModelTier(newTierWithSet([]string{"gpt-5.4"}, nil))
	assert.Equal(t, int16(80), FeaturedQueuePriority("gpt-5.4", 60),
		"featured priority must default to 80 (>= integrity 70)")
}

// TestModelTier_CanonicalFeaturedExpandsRawVariants 钉住 2026-11-10 恢复检测
// 及时性轮的核心不变量：models_canonical.featured 存的是**裸 canonical 名**，
// 而 provider_models.raw_model_name 多数带供应商命名空间前缀
// （z-ai/glm-5.3-flash、codex/gpt-6-sol）。
//
// 探针侧的 scope 门 / backoff 乘数 / 队列优先级都用
// globalIsFeaturedModel(raw, "") —— **只比 raw 名**。⇒ 若装载时不展开 raw
// 变体，命名空间形态的免费模型永远匹配不上 featured ⇒ 恢复检测仍迟到 24h。
//
// 本测试断言：装载 SQL 必须同时取 canonical_name **与** raw_model_name，
// 且 raw 变体必须与裸名一起进 static 集合（否则上条不变量回归）。
func TestModelTier_CanonicalFeaturedExpandsRawVariants(t *testing.T) {
	src := sourceFromFile(t, "model_tier.go")

	// 1) 装载 SQL 必须同时取裸名与 raw 变体（两个 featured 来源共用一套展开）。
	assert.True(t, strings.Contains(src, "SELECT fn.name, COALESCE(pm.raw_model_name, '')"),
		"model_tier.go must select both the bare name and the raw variant")
	assert.True(t, strings.Contains(src, "LEFT JOIN provider_models pm ON pm.canonical_id = mc.id"),
		"model_tier.go must join provider_models to expand raw variants")

	// 2) raw 变体必须进 static 集合（不只是裸名进集合）。
	assert.True(t, strings.Contains(src, "fs.static[raw] = struct{}{}"),
		"model_tier.go must insert the raw variant into the static featured set")

	// 3) 负向断言：不得退化成只比裸名而漏掉 raw 变体 —— 装载处
	//    必须同时出现两个入集合动作。
	assert.True(t, strings.Contains(src, "fs.static[name] = struct{}{}"),
		"model_tier.go must insert the bare canonical name into the static featured set")
}

// TestFeaturedQueuePriority_NamespacedRawGetsFeaturedPriority 钉住配套行为：
// 命名空间形态的 raw 名一旦进了 featured 集合，队列优先级必须走 featured 档
// （80），而不是 fallback —— 否则「展开」在队列侧零效果。
func TestFeaturedQueuePriority_NamespacedRawGetsFeaturedPriority(t *testing.T) {
	prevTier := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prevTier) })

	// 模拟装载后的集合：裸 canonical 名 + 其 raw 变体
	SetGlobalModelTier(newTierWithSet(
		[]string{"glm-5.3-flash", "z-ai/glm-5.3-flash"}, nil))

	assert.Equal(t, int16(80), FeaturedQueuePriority("z-ai/glm-5.3-flash", 60),
		"namespaced raw variant of a featured canonical must get featured queue priority")
	assert.Equal(t, int16(80), FeaturedQueuePriority("glm-5.3-flash", 60),
		"bare canonical name must get featured queue priority")
	assert.Equal(t, int16(60), FeaturedQueuePriority("some-unrelated/model", 60),
		"non-featured model must keep the fallback priority")
}

// sourceFromFile reads the file (relative to this package's source root) and
// returns its content. Kept tiny to avoid pulling in go/ast for a single
// string guard.
func sourceFromFile(t *testing.T, relPath string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	data, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		// Fallback for callers that run from a different cwd.
		data, err = os.ReadFile(relPath)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
	}
	// Trim large trailing binary noise (none expected, but cheap insurance).
	if len(data) > 2<<20 {
		data = data[:2<<20]
	}
	return string(bytes.TrimSpace(data))
}
