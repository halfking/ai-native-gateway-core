// Guard for the free-token-pool architecture: the scan side and the
// credential side are two chains that do not meet.
//
// Why this guard exists (2026-10-04, audit round 231):
//
// objective asks for "免费的 token 资源自动扫描、注册、和聚合使用，做为一个标准的
// 供应商池来处理" — three links: scan → register → aggregate-use.
//
// What the code actually does (audit 231):
//
//	(a) SCAN  domains/freediscovery → INSERT INTO free_resource_catalog
//	    (import_service.go:261-272). That table has NO api_key / secret /
//	    credential_id column at all (28 columns, db/db_omnifree.go:47-82 and
//	    sql/migrations/084-freediscovery-schema.sql:148-152). It cannot create
//	    a credential, therefore it can never produce a routing candidate.
//	    Its only consumer is the auto/* virtual route, where catalog rows act
//	    as a FILTER over credentials that already exist
//	    (handler_autocombo.go:183 → :208-224 → virtual_factory.go:300).
//
//	(b) REGISTER  admin/free_pool_extra.go:1690-1695 → INSERT INTO credentials
//	    (:1628) + model_offers with billing_mode='free'. This one does produce
//	    candidates, and candidateQuerySQL actively PRIORITISES it
//	    (provider/client.go:1796-1797 puts billing_mode='free' in tier 1).
//
// The two chains share no code. So the scan result can inform a human, but
// nothing auto-registers a scanned resource. The audit records this as the
// concrete shape of the gap; whether to wire it is a product decision
// (the blocking decision is named in the code comment at
// discovery_engine.go:107-109: 「免费 token 池『暂不投入』的产品裁决未拍板」).
//
// This guard therefore does NOT assert a defect. It pins the structural
// premise the whole finding rests on: `free_resource_catalog` carries no
// credential column. If somebody adds one, the architecture has changed and
// every conclusion in report 231 has to be re-derived — so the guard turns red
// and says exactly that.
//
// Absence-style claims elsewhere in this domain (no end-to-end test that a
// free credential is actually selected; no product-decision item in the ledger
// for the P1) are printed, not enforced (playbook §157).
package sqlguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// catalogCredentialColumns are column names that would make
// free_resource_catalog a credential source. A credential table must carry at
// least one of them.
var catalogCredentialColumns = []string{
	"api_key", "apikey", "secret", "credential_id", "credentialid",
	"key_ciphertext", "secret_ciphertext", "encrypted_key", "access_token",
}

// TestOmniFreeCatalogHasNoCredentialColumns pins the premise behind report 231.
//
// It reads the DDL, not the Go structs, because the column set is what the
// database actually enforces; a struct field that is never written would
// otherwise make the table look like a credential source.
func TestOmniFreeCatalogHasNoCredentialColumns(t *testing.T) {
	root := repoRootForTest(t)

	ddl := []string{
		filepath.Join("sql", "migrations", "075-omnifree-schema.sql"),
		filepath.Join("sql", "migrations", "084-freediscovery-schema.sql"),
	}

	found := 0
	for _, rel := range ddl {
		p := filepath.Join(root, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Logf("读不到 %s（%v），跳过该来源", rel, err)
			continue
		}
		body := string(b)
		for _, block := range freeResourceCatalogDDLBlocks(body) {
			found++
			cols := parseColumnNames(block)
			t.Logf("%s：free_resource_catalog DDL 块含 %d 列", rel, len(cols))
			for _, c := range cols {
				lc := strings.ToLower(c)
				for _, bad := range catalogCredentialColumns {
					if strings.Contains(lc, bad) {
						t.Errorf("%s：free_resource_catalog 出现了凭据列 %q。\n"+
							"审计 231 的结论建立在「扫描链的产物表没有凭据列、因此永远无法成为路由候选」之上。\n"+
							"一旦加了凭据列，扫描→注册→聚合三环就可能真的接通，\n"+
							"报告 231 的每一条结论都必须重新推导（这是结构变化，不是缺陷修复）。",
							rel, c)
					}
				}
			}
		}
	}
	if found == 0 {
		t.Fatalf("在 %v 里没有解析出任何 free_resource_catalog DDL 块 —— "+
			"判据扫不到目标就是它报「通过」的同一种形态（playbook §181）", ddl)
	}
	t.Logf("free_resource_catalog 在 %d 处 DDL 块中被确认不含任何凭据列。", found)
}

// TestOmniFreeGapIsRecorded prints the two non-enforceable parts of the
// finding: the missing end-to-end assertion, and the missing decision item.
func TestOmniFreeGapIsRecorded(t *testing.T) {
	root := repoRootForTest(t)

	// (1) The candidate path must have no free/source exclusion clause. We
	// only report; asserting absence by substring is exactly the pattern
	// §157 warns about.
	q := filepath.Join(root, "provider", "client.go")
	if b, err := os.ReadFile(q); err == nil {
		body := string(b)
		if strings.Contains(body, "WHEN 'free' THEN 1") {
			t.Logf("候选排序把 billing_mode='free' 放在第 1 档（%s，provider/client.go:1796-1797）"+
				"⇒ 免费池一旦注册进去是【优先】而不是【排除】。", q)
		}
	}

	// (2) The blocking product decision, quoted from the code itself.
	eng := filepath.Join(root, "domains", "freediscovery", "discovery_engine.go")
	b, err := os.ReadFile(eng)
	if err != nil {
		t.Fatalf("read %s: %v", eng, err)
	}
	if m := regexp.MustCompile(`(?m)^.*产品裁决未拍板.*$`).FindString(string(b)); m != "" {
		t.Logf("阻塞点由代码自陈：discovery_engine.go 「%s」", strings.TrimSpace(m))
		t.Logf("⚠️ 台账里没有与它对应的【待裁决】条目 ⇒ 一条 P1 若没有裁决项就永远不会被解决。")
	}
}

// freeResourceCatalogDDLBlocks returns the CREATE TABLE / ALTER TABLE ADD
// COLUMN bodies that define free_resource_catalog.
func freeResourceCatalogDDLBlocks(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if !regexp.MustCompile(`(?i)free_resource_catalog`).MatchString(l) {
			continue
		}
		// Collect from this line until parentheses balance (CREATE TABLE (…)
		// or ALTER TABLE … ADD COLUMN lines).
		depth := strings.Count(l, "(") - strings.Count(l, ")")
		if depth <= 0 {
			if strings.Contains(l, "ADD COLUMN") {
				out = append(out, l)
			}
			continue
		}
		buf := []string{l}
		for j := i + 1; j < len(lines) && depth > 0; j++ {
			buf = append(buf, lines[j])
			depth += strings.Count(lines[j], "(") - strings.Count(lines[j], ")")
		}
		out = append(out, strings.Join(buf, "\n"))
	}
	return out
}

var ddlIdent = regexp.MustCompile(`(?i)^\s*"?([a-z_][a-z0-9_]*)"?\s+(text|bigint|integer|int|boolean|bool|timestamptz|timestamp|jsonb|json|numeric|real|double\s+precision|uuid|serial|bigserial)`)

// parseColumnNames extracts column names from a DDL body.
func parseColumnNames(block string) []string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "--") ||
			strings.Contains(strings.ToUpper(t), "CONSTRAINT") ||
			strings.Contains(strings.ToUpper(t), "PRIMARY KEY") ||
			strings.Contains(strings.ToUpper(t), "REFERENCES") {
			continue
		}
		if m := ddlIdent.FindStringSubmatch(t); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}
