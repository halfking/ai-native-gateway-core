package startup

// migration_917_sanitize_block_reviews_test.go —— 迁移 917 的静态契约门。
//
// # 这道门钉什么
//
// ★ 迁移 917 把 r1039 / 第 33 增量的「拦截审核队列 + 放行表 + 硬阻断清单
//   管理面」从**进程内**落库（owner 口径：审批通过 ⇒ 同一租户一周内同规则
//   放行 ⇒ 放行必须跨重启存活 ⇒ 触发记录必须可回溯）。
//
// ★ 三类断言：
//   ① 内容：三张表都在、幂等（IF NOT EXISTS）、审核记录**不带载荷原文列**
//      （match_detail/reason 是说明文字，payload 只允许 digest 形态——
//      这是 r1022「payload preview 不进日志」同一条纪律的落库面）。
//   ② down：三张表都要撤（★ 撤销顺序与外键无关，但 DROP 必须齐全）。
//   ③ 登记面：installer embeddata 逐字节一致 + dbinit runner + 部署序列 +
//      db-changelog（含正确 sha256）——916 立的「全登记面」规矩照搬。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig917File     = "917_sanitize_block_reviews.sql"
	mig917DownFile = "917_sanitize_block_reviews.down.sql"
)

func readRepo917(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

func TestMigration917TablesAreIdempotentAndPayloadFree(t *testing.T) {
	body := readRepo917(t, mig917File)
	for _, table := range []string{"sanitize_block_reviews", "sanitize_egress_releases", "sanitize_hardblock_state"} {
		if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Errorf("缺少幂等建表：%s", table)
		}
	}
	// 索引也必须幂等：重复部署（211/252 存量 + fresh install）不能炸。
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS sanitize_block_reviews_status_created_idx") ||
		!strings.Contains(body, "CREATE UNIQUE INDEX IF NOT EXISTS sanitize_egress_releases_key_idx") {
		t.Error("缺少幂等索引（status_created / releases 唯一键）")
	}
	// ★ 载荷原文不许落库：被拦的值可能是凭据。允许的列只有 digest（不可逆）
	// 与说明文字；出现 payload_value/content/sample 之类原文列即违规。
	if strings.Contains(body, "payload_value") || strings.Contains(body, " sample_") {
		t.Error("审核记录不得携带载荷原文列（只有 payload_digest）")
	}
	// 审核状态机与放行来源约束在列定义里（迁移层兜底，不只靠 Go 校验）。
	if !strings.Contains(body, "CHECK(status IN ('pending','approved','rejected'))") {
		t.Error("sanitize_block_reviews.status 缺少状态机约束")
	}
	if !strings.Contains(body, "CHECK(source IN ('manual','review'))") {
		t.Error("sanitize_egress_releases.source 缺少来源约束")
	}
	// 7 天放行依赖 expires_at；hardblock 单例行依赖 singleton 主键。
	if !strings.Contains(body, "expires_at TIMESTAMPTZ") {
		t.Error("放行表缺少 expires_at（7 天放行的载体）")
	}
	if !strings.Contains(body, "singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(singleton)") {
		t.Error("hardblock_state 缺少 singleton 主键")
	}
}

func TestMigration917DownDropsAllTables(t *testing.T) {
	down := readRepo917(t, mig917DownFile)
	for _, table := range []string{"sanitize_hardblock_state", "sanitize_egress_releases", "sanitize_block_reviews"} {
		if !strings.Contains(down, "DROP TABLE IF EXISTS "+table+" CASCADE") {
			t.Errorf("down 缺少 %s 的撤销", table)
		}
	}
}

func TestMigration917RegisteredEverywhere(t *testing.T) {
	installerCopy := readRepo917(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig917File)
	canonical := readRepo917(t, mig917File)
	if installerCopy != canonical {
		t.Error("installer embeddata 副本必须与正本逐字节一致")
	}
	downCopy, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig917DownFile)
	if err != nil {
		t.Fatalf("read installer down: %v", err)
	}
	if string(downCopy) != readRepo917(t, mig917DownFile) {
		t.Error("installer down 副本必须与正本逐字节一致")
	}

	runner := readRepo917(t, "../../../installer/internal/dbinit/runner.go")
	if !strings.Contains(runner, mig917File) {
		t.Error("917 必须登记进 installer 的 StartupFiles（否则全新安装缺表）")
	}
	seq := readRepo917(t, "../../../scripts/apply-db-revision-sequence.sh")
	if !strings.Contains(seq, mig917File) {
		t.Error("917 必须登记进 apply-db-revision-sequence.sh（否则 211/252 存量库部署不到）")
	}

	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	changelog := readRepo917(t, "../../../docs/db-changelog.md")
	if !strings.Contains(changelog, want) {
		t.Errorf("db-changelog.md 里没有 917 当前的 sha256（%s）", want)
	}
}
