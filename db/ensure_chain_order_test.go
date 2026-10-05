package db

// ensure_chain_order_test.go —— db-open ensure 链的**调用顺序**是承重的（静态钉子）。
//
// # 背景：自锁不是推演出来的，是复现出来的
//
// 迁移 825 独占创建 `models_canonical.modality_source`。而 db-open 的 ensure 链
// （applyMigrationsOnce）跑在 migrate **之前**——账本 < 825 的库在 ensure 期就会
// 撞上引用该列的语句：
//
//	ensureAudioModalityBackfill  →  COALESCE(modality_source, '') NOT IN (...)
//
// 真库实测（2026-10-04，本地 pg17，新鲜 baseline+seed 库、无该列）：
//
//	ERROR:  column "modality_source" does not exist   (SQLSTATE 42703)
//
// 而 ensureAudioModalityBackfill 一旦失败，Open 就失败，migrate 永远到不了应用
// 825 那一步 ⇒ **启动自锁**：账本 < 825 的库永远起不来，而 825 又永远应用不上。
//
// 修法是把 825 的幂等 DDL 镜像（ensureModalityGradedVerification）放在
// ensureAudioModalityBackfill **之前**。
//
// # 为什么这个顺序需要一道门
//
// 顺序只由两行相邻的调用表达，旁边的注释解释了一百字——但注释不拦人。
// 一旦有人「按字母序整理 ensure 链」「把 backfill 挪到 backfill 那一组」，
// 没有任何东西会红，而红的时候是**整个环境起不来**，不是某个测试失败。
//
// 与 modality_source_writers_guard_test.go 同一取舍：这是文本钉子，红不能
// 证明语义正确；它抓的是「有人把顺序当无关紧要的排版重排」这唯一一种回退。
// 真的「顺序错了会 42703」这一点由那个真库复现背书，不是本门推断的。

import (
	"strings"
	"testing"
)

// ensureChainCalls 返回 applyMigrationsOnce 的函数体。
func ensureChainCalls(t *testing.T) string {
	t.Helper()
	src := readGuardedFile(t, "db.go")
	i := strings.Index(src, "func (db *DB) applyMigrationsOnce")
	if i < 0 {
		t.Fatal("applyMigrationsOnce 不在 db/db.go —— 门守的代码被移动/改名，需同步本门")
	}
	body := src[i:]
	// 函数体到下一个顶格声明为止。
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	return body
}

func TestEnsureChainCreatesModalitySourceColumnsBeforeAnyWriterReadsThem(t *testing.T) {
	body := ensureChainCalls(t)

	producer := strings.Index(body, "ensureModalityGradedVerification(migCtx)")
	consumer := strings.Index(body, "ensureAudioModalityBackfill(migCtx)")

	if producer < 0 {
		t.Fatal("ensure 链里找不到 ensureModalityGradedVerification(migCtx) —— 825 的幂等镜像被移除或改名。" +
			"它是 modality_source 的唯一 db-open 期生产者，移除即意味着下面的消费方会 42703")
	}
	if consumer < 0 {
		t.Fatal("ensure 链里找不到 ensureAudioModalityBackfill(migCtx) —— 被移动/改名，需同步本门")
	}

	if producer > consumer {
		t.Errorf("ensure 链顺序反了：ensureAudioModalityBackfill(第 %d 处) 跑在 "+
			"ensureModalityGradedVerification(第 %d 处) 之前。\n"+
			"backfill 的 WHERE 读 COALESCE(modality_source, '')，而该列只由 825 的镜像创建；"+
			"账本 < 825 的库在这里报 42703 column does not exist，Open 失败 ⇒ migrate 永远到不了应用 825 "+
			"⇒ 启动自锁。真库复现过，别把这个顺序当排版重排。",
			consumer, producer)
	}
}

// TestEnsureModalityGradedVerificationCreatesTheColumnItsConsumersRead 是上一条的
// 内容侧补充：钉住「镜像确实建了那一列」。顺序对了但镜像里少建一列，同样是 42703，
// 而那属于 SQL 内容漂移，与调用顺序是两回事，所以分开钉。
func TestEnsureModalityGradedVerificationCreatesTheColumnItsConsumersRead(t *testing.T) {
	src := readGuardedFile(t, "db.go")
	i := strings.Index(src, "func (db *DB) ensureModalityGradedVerification")
	if i < 0 {
		t.Fatal("ensureModalityGradedVerification 不在 db/db.go —— 门守的代码被移动/改名，需同步本门")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}

	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS modality_source text") {
		t.Error("825 镜像里没有 ADD COLUMN IF NOT EXISTS modality_source text —— " +
			"db-open 期的唯一生产点。缺它则 ensureAudioModalityBackfill 在账本 < 825 的库上 42703（启动自锁）")
	}
	// 主键前置：证据表的 FK 指向 models_canonical(id)，而 SSOT 从未声明主键。
	if !strings.Contains(body, "models_canonical_pkey") {
		t.Error("825 镜像里没有 models_canonical_pkey 的前置自愈 —— " +
			"model_modality_verification.canonical_id 是 REFERENCES models_canonical(id)，" +
			"而 SSOT 只给了 canonical_name 唯一键，全新安装在 FK 处报 42830（真库复现过）")
	}
}
