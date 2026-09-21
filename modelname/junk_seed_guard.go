// Package modelname — junk_seed_guard.go
//
// R50（2026-09-21）：junk-seed 守卫谓词的单一实现（自 provider/client.go 与
// admin/provider_vendor.go 的两份字面拷贝收敛而来；admin/models.go 的
// createModel 查重谓词形态不同，仍待 R51 一并收敛）。
//
// R50 修正背景（真库实测）：原谓词两臂全死——stdName 是 NormalizeRouteKey
// 的 dash 形输出（如 "opus-5"），左侧却把 canonical_name 折叠为下划线，
// `= $1` 恒 false；截断臂 `'%-' || $1` 对下划线折叠左侧同样恒不匹配。
// 现对 $1 也做 '-'→'_' 折叠并以 '%_' 做后缀匹配。
package modelname

// JunkSeedGuardSQL 是 auto_discovered / provider_refresh 回种 models_canonical
// 前的守卫查询：$1 = stdName（NormalizeRouteKey 输出）。命中（返回行）表示
// 已有 active canonical 与之归一化相等或是其更长母名（截断形），应抑制回种。
// 查询失败时调用方必须 fail-closed（跳过 INSERT），不得放行盲插。
const JunkSeedGuardSQL = `
			SELECT canonical_name FROM models_canonical
			WHERE status = 'active'
			  AND (
			    replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_') = replace($1, '-', '_')
			    OR ('_' || replace(replace(replace(replace(lower(canonical_name), '.', '_'), '-', '_'), ' ', '_'), '/', '_')) LIKE '%_' || replace($1, '-', '_')
			  )
			ORDER BY length(canonical_name), canonical_name
			LIMIT 1
		`
