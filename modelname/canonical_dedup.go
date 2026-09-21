// Package modelname — canonical_dedup.go
//
// R50（2026-09-21 审计轮，F19+F13 收敛专项收尾）：models_canonical 归一化
// 谓词的单一实现。三个调用点——admin/models.go createModel 查重、
// provider/client.go auto_discovered 回种守卫、admin/provider_vendor.go
// enrollCredentialModels 回种守卫——的 SQL 一律从本文件取，调用方不许再
// 手写折叠链（历史教训：手写拷贝在 R48/R49 两臂全死却零测试暴露，真库
// 实测才抓到）。
//
// 折叠语义（与 NormalizeRouteKey 的 dash→underscore 口径一致）：
// lower + ['.' , '-' , ' ' , '/'] → '_'。createModel 查重在此基础上追加
// 连续分隔符折叠（regexp_replace '[-_]{2,}' → '_'），对齐 run-collapse 语义
// （R50 修正：无折叠时 "claude--opus-5" 与 "claude-opus-5" 不判重）。
package modelname

import "fmt"

// foldedNameExpr 返回把 SQL 目标（列名或参数占位符）折叠为下划线形的
// 表达式。折叠链的唯一手写点；所有谓词经它合成。
func foldedNameExpr(target string) string {
	return fmt.Sprintf(
		"replace(replace(replace(replace(lower(%s), '.', '_'), '-', '_'), ' ', '_'), '/', '_')",
		target)
}

// JunkSeedGuardSQL 是 auto_discovered / provider_refresh 回种 models_canonical
// 前的守卫查询：$1 = stdName（NormalizeRouteKey 输出）。命中（返回行）表示
// 已有 active canonical 与之归一化相等或是其更长母名（截断形），应抑制回种。
// 查询失败时调用方必须 fail-closed（跳过 INSERT），不得放行盲插。
// 字节级等价由 TestJunkSeedGuardSQL_FoldSingleSource 钉死（R50 收敛前的
// 字面量形态，谓词语义经真库实证修正，勿手改）。
var JunkSeedGuardSQL = fmt.Sprintf(`
			SELECT canonical_name FROM models_canonical
			WHERE status = 'active'
			  AND (
			    %[1]s = replace($1, '-', '_')
			    OR ('_' || %[1]s) LIKE '%%_' || replace($1, '-', '_')
			  )
			ORDER BY length(canonical_name), canonical_name
			LIMIT 1
		`, foldedNameExpr("canonical_name"))

// DedupCanonicalNameSQL 是 admin createModel 的归一化查重查询：
// $1 = 待建 canonical_name。命中表示已有 canonical 与之精确同名（lower 相等）
// 或折叠+连续分隔符归一后相等，应 409 拒绝。与 JunkSeedGuardSQL 刻意不同：
// 查重不带 status 过滤（disabled 拼写同样占用名字空间）、多 run-collapse 臂；
// 守卫只对 active 行抑制且含截断母名臂。并发窗口的 DB 兜底是
// uq_models_canonical_active_folded_name 表达式唯一索引——因真库存在 6 组
// 双侧均有活跃引用的折叠重复对（2026-09-21 实测），须待数据对账后建
// （R51）；索引就位前本查询仍是唯一闸门，调用方另需捕获 23505。
var DedupCanonicalNameSQL = fmt.Sprintf(`
		SELECT canonical_name FROM models_canonical
		WHERE lower(canonical_name) = lower($1)
		   OR regexp_replace(%[1]s, '[-_]{2,}', '_', 'g')
		    = regexp_replace(%[2]s, '[-_]{2,}', '_', 'g')
		ORDER BY length(canonical_name), canonical_name
		LIMIT 1
	`, foldedNameExpr("canonical_name"), foldedNameExpr("$1"))
