package startup

// migration_926_sanitize_block_review_degraded_test.go —— 迁移 926 的静态契约门。
//
// # 这道门钉什么
//
// ★ 迁移 926 把拦截审核单区分成「硬阻断」与「降级放行」两种语义：
//   921 把守卫缓冲越界的降级放行也登记成 pending 拦截单 ⇒ 211 生产实测
//   24h 内 226/226 条全是 builtin.guard.capacity_exceeded —— 审核面被
//   降级遥测灌爆，且降级单上的批准写出的放行行没有任何消费方（死操作）。
//
// ★ 三类断言：
//   ① 列面：degraded 追加在**末尾**且幂等（ADD COLUMN IF NOT EXISTS）；
//     回填以 builtin.guard.% 前缀为判据并带 NOT degraded 守卫（可重入）。
//   ② 登记面：installer embeddata 逐字节一致 + dbinit runner + 部署序列 +
//     db-changelog（含正确 sha256）——916/921 立的「全登记面」规矩照搬。
//   ③ 消费面（防回退）：计数与读数的降级排除口径必须存在于代码里
//     （CountPendingBlockDecisionsDB / insights 的 NOT degraded）——
//     只加列不接消费面，等于没修。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig926File     = "926_sanitize_block_review_degraded.sql"
	mig926DownFile = "926_sanitize_block_review_degraded.down.sql"
)

func readRepo926(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// TestMigration926AddsDegradedColumn 钉住列面：幂等追加 + 前缀回填。
func TestMigration926AddsDegradedColumn(t *testing.T) {
	body := readRepo926(t, mig926File)
	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS degraded BOOLEAN NOT NULL DEFAULT false") {
		t.Error("缺少幂等列追加：degraded BOOLEAN NOT NULL DEFAULT false")
	}
	if !strings.Contains(body, "ALTER TABLE sanitize_block_reviews") {
		t.Error("缺少 sanitize_block_reviews 的 ALTER")
	}
	// 回填判据 = 守卫合成哨兵前缀 + NOT degraded 守卫（重复执行不重复写）。
	if !strings.Contains(body, "rule_id LIKE 'builtin.guard.%'") {
		t.Error("回填必须以 builtin.guard.% 前缀为判据（policyBlockEvidenceKind 对整个前缀一视同仁）")
	}
	if !strings.Contains(body, "NOT degraded") {
		t.Error("回填必须带 NOT degraded 守卫（幂等可重入）")
	}
}

// TestMigration926DownDropsColumn 钉住 down 撤销面。
func TestMigration926DownDropsColumn(t *testing.T) {
	down := readRepo926(t, mig926DownFile)
	if !strings.Contains(down, "DROP COLUMN IF EXISTS degraded") {
		t.Error("down 缺少列撤销：degraded")
	}
}

// TestMigration926RegisteredEverywhere 钉住「全登记面」。
func TestMigration926RegisteredEverywhere(t *testing.T) {
	canonical := readRepo926(t, mig926File)
	installerCopy := readRepo926(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig926File)
	if installerCopy != canonical {
		t.Error("installer embeddata 副本必须与正本逐字节一致")
	}
	downCopy, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig926DownFile)
	if err != nil {
		t.Fatalf("read installer down: %v", err)
	}
	if string(downCopy) != readRepo926(t, mig926DownFile) {
		t.Error("installer down 副本必须与正本逐字节一致")
	}

	runner := readRepo926(t, "../../../installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, mig926File) {
		t.Error("926 必须登记进 installer 的 StartupFiles（否则全新安装缺列）")
	}
	seq := readRepo926(t, "../../../scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(seq, mig926File) {
		t.Error("926 必须登记进 apply-db-revision-sequence.sh（否则 211/245/154 存量库部署不到）")
	}

	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	changelog := readRepo926(t, "../../../docs/db-changelog.md")
	if !strings.Contains(changelog, want) {
		t.Errorf("db-changelog.md 里没有 926 当前的 sha256（%s）", want)
	}
}

// TestMigration926ConsumersExcludeDegraded 防回退：只加列不接消费面等于没修。
//
// ★ 三个消费点都在 Go 侧（DB 读面 + 管理面口径）：
//   ① CountPendingBlockDecisionsDB 的 SQL 必须排除 degraded；
//   ② BlockReviewInsightsDB 的 SQL 必须排除 degraded（顺带钉住 WHERE 存在
//     —— 租户过滤拼接曾缺 WHERE 前缀，非法 SQL）；
//   ③ 列序真值源 blockReviewColumns 必须含 degraded（scan 对应关系）。
func TestMigration926ConsumersExcludeDegraded(t *testing.T) {
	pg := readRepo926(t, "../../../domains/sanitizerules/postgres_block_review.go")
	if !strings.Contains(pg, "status='pending' AND NOT degraded") {
		t.Error("CountPendingBlockDecisionsDB 必须排除 degraded（待审数口径）")
	}
	if !strings.Contains(pg, "FROM sanitize_block_reviews WHERE NOT degraded") {
		t.Error("BlockReviewInsightsDB 必须排除 degraded 且带 WHERE（租户过滤曾因缺 WHERE 生成非法 SQL）")
	}
	if !strings.Contains(pg, "labeled_by, labeled_at, degraded") {
		t.Error("blockReviewColumns 列序真值源必须含 degraded（scan 对应关系）")
	}
	queue := readRepo926(t, "../../../domains/sanitizerules/review_queue.go")
	if !strings.Contains(queue, "CountPendingBlockDecisions") {
		t.Error("审核队列必须导出 CountPendingBlockDecisions（管理面徽标口径）")
	}
}
