package startup

// migration_952_model_price_versions_test.go —— 迁移 952 的契约门。
//
// 952 = model_price_versions：(canonical_id, billing_mode) 维度的价格期
// 版本账（运营审计轮项2）。判据四件（951 门同款）：
//
//	① 形态门：CREATE TABLE 表名 + PK(canonical_id, billing_mode,
//	  effective_from) + effective_to 期间 CHECK + billing_mode 闭集
//	  CHECK（pay_as_you_go|token_plan|booster_pack）+ 部分唯一现行索引
//	  uq_model_price_versions_current（WHERE effective_to IS NULL）——
//	  「同一模型同一模式至多一个现行价」的库级强制，索引谓词漂移即红。
//	② down 门：down 删表 + 删 schema_migrations 行。
//	③ cross-pin 门：写入侧（maas/model_price_versions.go）仍然写
//	  model_price_versions 且闭期谓词仍是 effective_to IS NULL；读侧
//	  （uq 索引）与服务端 close 谓词同源 —— 服务改用别的列名即红。
//	④ 字节一致门：canonical 与 installer embeddata 两份 SHA 一致。
//
// 判据纪律：读 SQL 用字符串提取，按标识符精确匹配。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig952Name     = "952_model_price_versions.sql"
	mig952DownName = "952_model_price_versions.down.sql"
)

func TestMigration952Shape(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig952Name)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS public.model_price_versions (",
		"PRIMARY KEY (canonical_id, billing_mode, effective_from)",
		"CHECK (effective_to IS NULL OR effective_to > effective_from)",
		"billing_mode IN ('pay_as_you_go', 'token_plan', 'booster_pack')",
		"source IN ('manual', 'batch', 'global_fill', 'reset', 'seed')",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("952 up missing %q", want)
		}
	}
	// 现行期部分唯一索引：索引名 + WHERE 谓词必须精确——这是「同一模型
	// 同一模式至多一个现行价」的库级强制。
	const uqIdx = "CREATE UNIQUE INDEX IF NOT EXISTS uq_model_price_versions_current"
	idxStart := strings.Index(body, uqIdx)
	if idxStart < 0 {
		t.Fatalf("952 up missing current-period unique index %q", uqIdx)
	}
	idxEnd := strings.Index(body[idxStart:], ";")
	stmt := body[idxStart : idxStart+idxEnd]
	if !strings.Contains(stmt, "(canonical_id, billing_mode)") ||
		!strings.Contains(stmt, "WHERE effective_to IS NULL") {
		t.Fatalf("current-period unique index drifted: %s", stmt)
	}
	if !strings.Contains(body, "INSERT INTO public.schema_migrations (version, description)") ||
		!strings.Contains(body, "'952'") {
		t.Fatalf("952 up must register itself in schema_migrations")
	}
}

func TestMigration952Down(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig952DownName)
	if !strings.Contains(body, "DROP TABLE IF EXISTS public.model_price_versions") {
		t.Fatalf("952 down must drop the table")
	}
	if !strings.Contains(body, "DELETE FROM public.schema_migrations WHERE version = '952'") {
		t.Fatalf("952 down must deregister from schema_migrations")
	}
}

func TestMigration952CrossPin(t *testing.T) {
	root := mig951Tree(t)
	svc, err := os.ReadFile(filepath.Join(root, "maas", "model_price_versions.go"))
	if err != nil {
		t.Fatalf("read maas/model_price_versions.go: %v", err)
	}
	body := string(svc)
	// 写侧：闭期谓词与插入目标表必须仍指向 952 的表与现行期语义。
	for _, want := range []string{
		"FROM model_price_versions",
		"WHERE canonical_id = $1 AND billing_mode = $2 AND effective_to IS NULL",
		"INSERT INTO model_price_versions",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("maas/model_price_versions.go missing %q (952 cross-pin drift)", want)
		}
	}
	// 写侧 hook 仍然接在三个 model_credit_rates 写路径上。
	rates, err := os.ReadFile(filepath.Join(root, "maas", "model_rates.go"))
	if err != nil {
		t.Fatalf("read maas/model_rates.go: %v", err)
	}
	rb := string(rates)
	if strings.Count(rb, "SyncPayAsYouGoPriceVersion(") < 3 {
		t.Fatalf("upsert/reset/delete must each snapshot into the version ledger (found %d hooks)", strings.Count(rb, "SyncPayAsYouGoPriceVersion("))
	}
}

func TestMigration952ByteIdenticalEmbeddata(t *testing.T) {
	root := mig951Tree(t)
	for _, name := range []string{mig952Name, mig952DownName} {
		a, err := os.ReadFile(filepath.Join(root, "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read canonical %s: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(root, "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if err != nil {
			t.Fatalf("read embeddata %s: %v", name, err)
		}
		sa, sb := sha256.Sum256(a), sha256.Sum256(b)
		if hex.EncodeToString(sa[:]) != hex.EncodeToString(sb[:]) {
			t.Fatalf("%s canonical and embeddata copies differ (sha %x vs %x)", name, sa, sb)
		}
	}
}
