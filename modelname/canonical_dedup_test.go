package modelname

import (
	"strings"
	"testing"
)

// TestJunkSeedGuardSQL_FoldSingleSource —— R50 F19 收敛钉桩：守卫 SQL 从
// foldedNameExpr 单一来源合成后，必须与 R50 真库实证修正时的字面量字节
// 级一致（并行会话 df9b041e6 的两臂全死修复语义不许漂移）。若本测试红，
// 说明折叠链或谓词被改动——先过真库三件套再动。
func TestJunkSeedGuardSQL_FoldSingleSource(t *testing.T) {
	const literalForm = `
			SELECT canonical_name FROM models_canonical
			WHERE status = 'active'
			  AND (
			    replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_') = replace($1, '-', '_')
			    OR ('_' || replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_')) LIKE '%_' || replace($1, '-', '_')
			  )
			ORDER BY length(canonical_name), canonical_name
			LIMIT 1
		`
	if JunkSeedGuardSQL != literalForm {
		t.Fatalf("JunkSeedGuardSQL drifted from the byte-exact R50 fixed form.\n got: %q\nwant: %q", JunkSeedGuardSQL, literalForm)
	}
}

// TestDedupCanonicalNameSQL_Shape —— admin createModel 查重谓词（收敛后）
// 的形状锁：精确 lower 臂 + 折叠 run-collapse 臂双侧同构、无 status 过滤
// （disabled 拼写同样占用名字空间）。
func TestDedupCanonicalNameSQL_Shape(t *testing.T) {
	for _, fragment := range []string{
		"lower(canonical_name) = lower($1)",
		"regexp_replace(",
		"'[-_]{2,}', '_', 'g'",
		// 折叠链出现两次（列侧 + 参数侧），且不由本表之外的来源手写
		"replace(replace(replace(replace(lower(canonical_name)",
		"replace(replace(replace(replace(lower($1)",
		"ORDER BY length(canonical_name), canonical_name",
	} {
		if !strings.Contains(DedupCanonicalNameSQL, fragment) {
			t.Errorf("DedupCanonicalNameSQL missing fragment %q\nSQL: %s", fragment, DedupCanonicalNameSQL)
		}
	}
	if strings.Contains(DedupCanonicalNameSQL, "status = 'active'") {
		t.Errorf("dedup gate must not filter by status (disabled spellings still occupy the name space)")
	}
}

// TestFoldedNameExpr_SingleSource —— 折叠链唯一手写点：两个谓词里的折叠
// 链必须逐字相同（除目标名），防止未来一份修 '.' 顺序另一份漏改。
func TestFoldedNameExpr_SingleSource(t *testing.T) {
	col := foldedNameExpr("canonical_name")
	param := foldedNameExpr("$1")
	if strings.Replace(col, "canonical_name", "$1", 1) != param {
		t.Fatalf("fold expressions diverge:\n col : %s\n param: %s", col, param)
	}
	// 折叠次序契约：lower 最内层，四类分隔符逐一替换（与 NormalizeRouteKey
	// 的 dash→underscore 口径同源）。
	if !strings.HasPrefix(col, "replace(replace(replace(replace(lower(") {
		t.Fatalf("fold must keep lower as the innermost transform: %s", col)
	}
}
