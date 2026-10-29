package startup

// migration_921_sanitize_block_workbench_test.go —— 迁移 921 的静态契约门。
//
// # 这道门钉什么
//
// ★ 迁移 921 把拦截审核从「只有摘要与说明」升级成「**工作台**」：
//   人工标注（label）+ 原始会话内容的有界摘录（证据表）+ 自动优化
//   （误报计数到阈值 ⇒ 租户级 7 天放行）。
//
// ★ 四类断言：
//   ① 列面：审核单追加 session_id / label / label_comment / labeled_by /
//     labeled_at，且**幂等**（ADD COLUMN IF NOT EXISTS）。
//   ② 证据面：证据表存在、幂等、**带 expires_at（TTL）**；★ 并且
//     **sanitize_block_reviews 仍然没有载荷原文列** ⇒ 917 的契约不破
//     （证据是**另一张表**、另一套生命周期）。
//   ③ 约束面：label 有闭集 CHECK；放行来源 CHECK 含 'auto_label'
//     （自动优化与人工批准同键同语义）。
//   ④ 登记面：installer embeddata 逐字节一致 + dbinit runner + 部署序列 +
//     db-changelog（含正确 sha256）——916 立的「全登记面」规矩照搬。
//
// ★ 额外钉一条**防回退**断言：917 的迁移文件里**不得**出现载荷原文列名
//   （payload_value / sample_）——那是对 917 契约的持续看护。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig921File     = "921_sanitize_block_workbench.sql"
	mig921DownFile = "921_sanitize_block_workbench.down.sql"
)

func readRepo921(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// TestMigration921AddsWorkbenchColumns 钉住审核单的五个新列 + 幂等写法。
func TestMigration921AddsWorkbenchColumns(t *testing.T) {
	body := readRepo921(t, mig921File)
	for _, column := range []string{
		"session_id", "label", "label_comment", "labeled_by", "labeled_at",
	} {
		if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS "+column) {
			t.Errorf("缺少幂等列追加：%s", column)
		}
	}
	// 追加必须落在**末尾**：既有列序不变（列序变更会静默改错 scan 的对应关系）。
	if !strings.Contains(body, "ALTER TABLE sanitize_block_reviews") {
		t.Error("缺少 sanitize_block_reviews 的 ALTER")
	}
	// 标注闭集在 DDL 层兜底（状态机不只靠 Go 校验）。
	if !strings.Contains(body, "sanitize_block_reviews_label_check") {
		t.Error("缺少标注闭集约束")
	}
	for _, label := range []string{"false_positive", "true_positive", "rule_too_broad", "needs_rule"} {
		if !strings.Contains(body, label) {
			t.Errorf("标注闭集缺少 %s", label)
		}
	}
}

// TestMigration921EvidenceTableIsSeparateAndExpires 钉住「证据单独一张表 +
// 有 TTL」，且审核记录表**仍然没有载荷原文列**。
func TestMigration921EvidenceTableIsSeparateAndExpires(t *testing.T) {
	body := readRepo921(t, mig921File)
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS sanitize_block_evidence (") {
		t.Fatal("缺少幂等建表：sanitize_block_evidence")
	}
	if !strings.Contains(body, "expires_at TIMESTAMPTZ") {
		t.Error("证据表缺少 expires_at（TTL 是敏感摘录的生命周期边界）")
	}
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS sanitize_block_evidence_expires_idx") {
		t.Error("缺少幂等索引（expires_idx）")
	}
	// ★ 防回退：917 的审核记录表不得被追加载荷原文列。
	review917 := readRepo921(t, "917_sanitize_block_reviews.sql")
	if strings.Contains(review917, "payload_value") || strings.Contains(review917, " sample_") {
		t.Error("917 的审核记录表不得携带载荷原文列（证据必须走单独的证据表）")
	}
	// 本迁移**不得**往审核记录表加原文列。
	if strings.Contains(body, "ADD COLUMN IF NOT EXISTS payload_value") ||
		strings.Contains(body, "ADD COLUMN IF NOT EXISTS evidence_content") {
		t.Error("审核记录表不得追加载荷原文列（证据走 sanitize_block_evidence）")
	}
}

// TestMigration921ReleaseSourceAllowsAutoLabel 钉住自动优化的来源取值。
func TestMigration921ReleaseSourceAllowsAutoLabel(t *testing.T) {
	body := readRepo921(t, mig921File)
	if !strings.Contains(body, "CHECK(source IN ('manual','review','auto_label'))") {
		t.Error("放行来源约束必须含 auto_label（自动优化与人工批准同键同语义）")
	}
}

// TestMigration921DownReversesEverything 钉住 down 撤销面。
//
// ★ down 必须**先删 auto_label 行再收 CHECK** —— 否则现存非法值顶住约束，
// 撤销会失败（这是本迁移 down 里唯一有顺序要求的地方）。
func TestMigration921DownReversesEverything(t *testing.T) {
	down := readRepo921(t, mig921DownFile)
	if !strings.Contains(down, "DROP TABLE IF EXISTS sanitize_block_evidence CASCADE") {
		t.Error("down 缺少证据表的撤销")
	}
	for _, column := range []string{"labeled_at", "labeled_by", "label_comment", "label", "session_id"} {
		if !strings.Contains(down, "DROP COLUMN IF EXISTS "+column) {
			t.Errorf("down 缺少列撤销：%s", column)
		}
	}
	deleteIdx := strings.Index(down, "DELETE FROM sanitize_egress_releases WHERE source = 'auto_label'")
	dropIdx := strings.Index(down, "DROP CONSTRAINT sanitize_egress_releases_source_check")
	if deleteIdx < 0 {
		t.Fatal("down 缺少 auto_label 放行行的清理")
	}
	if dropIdx < 0 {
		t.Fatal("down 缺少来源约束的撤销")
	}
	if deleteIdx > dropIdx {
		t.Error("down 必须先删 auto_label 行再收 CHECK（现存非法值会顶住约束）")
	}
}

// TestMigration921RegisteredEverywhere 钉住「全登记面」。
func TestMigration921RegisteredEverywhere(t *testing.T) {
	installerCopy := readRepo921(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig921File)
	canonical := readRepo921(t, mig921File)
	if installerCopy != canonical {
		t.Error("installer embeddata 副本必须与正本逐字节一致")
	}
	downCopy, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig921DownFile)
	if err != nil {
		t.Fatalf("read installer down: %v", err)
	}
	if string(downCopy) != readRepo921(t, mig921DownFile) {
		t.Error("installer down 副本必须与正本逐字节一致")
	}

	runner := readRepo921(t, "../../../installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, mig921File) {
		t.Error("921 必须登记进 installer 的 StartupFiles（否则全新安装缺表）")
	}
	seq := readRepo921(t, "../../../scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(seq, mig921File) {
		t.Error("921 必须登记进 apply-db-revision-sequence.sh（否则 211/252 存量库部署不到）")
	}

	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	changelog := readRepo921(t, "../../../docs/db-changelog.md")
	if !strings.Contains(changelog, want) {
		t.Errorf("db-changelog.md 里没有 921 当前的 sha256（%s）", want)
	}
}
