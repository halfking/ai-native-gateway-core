package startup

// migration_922_model_credibility_test.go — 迁移 922 的静态契约门。
//
// # 这道门钉什么
//
// ★ 迁移 922 建模型可信度（防掺水）两张表，并幂等补齐
// provider_quality_profiles 的 reliability_score / calculated_at
// （migration 435 遗漏 ⇒ 只跑迁移的全新安装会缺列）。
//
// 四类断言（照搬 921 立的「全登记面」规矩）：
//   ① 列面：可信度分数列**可为 NULL**（缺失维度 ≠ 0 分）+ verdict 闭集 CHECK
//     + UNIQUE (tenant_id, credential_id, raw_model_name)。
//   ② 缺陷修复面：provider_quality_profiles 的 reliability_score /
//     calculated_at 必须是 ADD COLUMN IF NOT EXISTS（幂等）。
//   ③ 幂等面：两张表全部 IF NOT EXISTS ⇒ 走事务通道（不得出现 CONCURRENTLY，
//     否则 installer 的 no-transaction 标记没配，全新安装会失败）。
//   ④ 登记面：installer embeddata 逐字节一致 + dbinit runner + 部署序列 +
//     db-changelog（含正确 sha256）。
//
// ★ 额外钉一条**防回退**断言：provider_quality_profiles 不得新增
//   模型可信度到 reliability_score 列（那是一列两义；语义归属在
//   internal/quality/scorer_reliability.go —— 服务可靠性）。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig922File     = "922_model_credibility.sql"
	mig922DownFile = "922_model_credibility.down.sql"
)

func readRepo922(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// TestMigration922TablesAreIdempotentAndNullable 钉住列面 + 幂等面。
func TestMigration922TablesAreIdempotentAndNullable(t *testing.T) {
	body := readRepo922(t, mig922File)

	for _, table := range []string{"provider_model_credibility", "provider_model_credibility_runs"} {
		if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS public."+table) {
			t.Errorf("缺少幂等建表：%s", table)
		}
	}

	// 分数列必须可空（无 NOT NULL / DEFAULT 0）——缺失维度写 NULL。
	for _, col := range []string{
		"credibility_score", "identity_score", "capability_score",
		"consistency_score", "reference_score",
	} {
		if strings.Contains(body, col+" numeric(5,2) NOT NULL") {
			t.Errorf("%s 不得是 NOT NULL：没测到 ≠ 0 分", col)
		}
		if strings.Contains(body, col+" numeric(5,2) DEFAULT") {
			t.Errorf("%s 不得带 DEFAULT：默认值会把「没测」伪装成有结论", col)
		}
	}

	// verdict 闭集。
	for _, v := range []string{"'verified'", "'suspected'", "'diluted'", "'unknown'"} {
		if !strings.Contains(body, v) {
			t.Errorf("verdict CHECK 缺少取值 %s", v)
		}
	}

	// upsert 冲突目标。
	if !strings.Contains(body, "UNIQUE (tenant_id, credential_id, raw_model_name)") {
		t.Error("缺少 (tenant_id, credential_id, raw_model_name) 唯一约束 —— upsert 的冲突目标")
	}

	// 435 缺陷修复必须幂等。
	for _, col := range []string{"reliability_score", "calculated_at"} {
		if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS "+col) {
			t.Errorf("provider_quality_profiles 缺少幂等补列：%s", col)
		}
	}

	// 不得含 CONCURRENTLY（无 dbinit:no-transaction 标记 ⇒ 走事务通道）。
	if strings.Contains(strings.ToUpper(body), "CONCURRENTLY") {
		t.Error("迁移不得使用 CONCURRENTLY：文件头没有 dbinit:no-transaction 标记")
	}
}

// TestMigration922KeepsReliabilityColumnSemantics 钉住「一列两义」防线。
func TestMigration922KeepsReliabilityColumnSemantics(t *testing.T) {
	body := readRepo922(t, mig922File)
	// reliability_score 只允许幂等补列，不允许被写成模型可信度的落点。
	// （语义归属：internal/quality/scorer_reliability.go —— 服务可靠性。）
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "--") || strings.HasPrefix(l, "COMMENT") {
			continue
		}
		if strings.Contains(l, "reliability_score") && !strings.Contains(l, "ADD COLUMN IF NOT EXISTS") {
			t.Errorf("reliability_score 出现在非补列语句里（一列两义防线）：%s", l)
		}
	}
}

// TestMigration922RegisteredEverywhere 钉住全登记面。
func TestMigration922RegisteredEverywhere(t *testing.T) {
	body := readRepo922(t, mig922File)

	// installer embeddata 逐字节一致（路径从本目录出发：../../installer/...）。
	embed, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig922File)
	if err != nil {
		t.Fatalf("read embeddata: %v", err)
	}
	if string(embed) != body {
		t.Error("installer embeddata 与 sql/migrations/startup 逐字节不一致")
	}

	// dbinit runner。
	runner, err := os.ReadFile("../../../installer/internal/dbinit/runner.go")
	if err != nil {
		t.Fatalf("read runner: %v", err)
	}
	if !strings.Contains(string(runner), `"`+mig922File+`"`) {
		t.Error("dbinit runner 未登记 922")
	}

	// 部署序列脚本。
	seq, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatalf("read sequence script: %v", err)
	}
	if !strings.Contains(string(seq), "startup/"+mig922File) {
		t.Error("apply-db-revision-sequence.sh 未登记 922")
	}
	if !strings.Contains(string(seq), `ledger_artifact_probe "`+mig922File+`"`) {
		t.Error("apply-db-revision-sequence.sh 缺少 922 的产物探针")
	}

	// db-changelog sha256。
	changelog, err := os.ReadFile("../../../docs/db-changelog.md")
	if err != nil {
		t.Fatalf("read db-changelog: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	want := hex.EncodeToString(sum[:])
	if !strings.Contains(string(changelog), "| 922 | `"+mig922File+"` | `"+want+"`") {
		t.Errorf("db-changelog 的 922 行 sha 不符（want %s）", want)
	}

	// installed_startup_migrations.tsv。
	tsv, err := os.ReadFile("../../../sql/schema/installed_startup_migrations.tsv")
	if err != nil {
		t.Fatalf("read tsv: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(tsv), "\n") {
		if strings.HasSuffix(line, "\t"+mig922File) {
			found = true
			break
		}
	}
	if !found {
		t.Error("installed_startup_migrations.tsv 未登记 922")
	}
}

// TestMigration922DownDoesNotRollbackFixColumns 钉住 down 的边界。
//
// ★ 判据是**语句级**的：down 允许在注释里提到这两列（说明为什么不回滚），
// 但不得出现 ALTER TABLE ... DROP COLUMN 语句。
func TestMigration922DownDoesNotRollbackFixColumns(t *testing.T) {
	down := readRepo922(t, mig922DownFile)
	for _, line := range strings.Split(down, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "--") {
			continue
		}
		if strings.Contains(l, "DROP COLUMN") {
			t.Errorf("down 含 DROP COLUMN 语句（不得回滚修复列）：%s", l)
		}
		if strings.Contains(l, "ALTER TABLE") && strings.Contains(l, "provider_quality_profiles") {
			t.Errorf("down 不得触碰 provider_quality_profiles：%s", l)
		}
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS public.provider_model_credibility_runs") ||
		!strings.Contains(down, "DROP TABLE IF EXISTS public.provider_model_credibility") {
		t.Error("down 必须删除两张新表")
	}
}
