// modelnames.go —— 模型过滤值的「canonical 名 ↔ raw 名」互认（2026-11-16 审计轮）。
//
// 背景：同一个「模型」在两套口径里是**两个字符串**。
//
//	路由页（/routing-v2?tab=resolve）的模型选择器发的是 **canonical 名**
//	（minimax-m2.5，来自 models_canonical.canonical_name）；
//	对账快照的维度是 **raw_model_name**
//	（MiniMax-M2.5 / minimax-m2.5 / auto/minimax-m2.5，来自 model_offers）。
//
// 两者字面不等 ⇒ 直接把 canonical 名塞进 raw_model_name 的等值过滤，
// 结果是「选了模型却一行都没有」。真库实测（2026-11-16，211，daily_grain
// 2026-11-10~11-15 的 356 个有请求的 raw 名）：
//
//	字面等于某个 canonical 名（lower() 折叠，与本文件的比较口径一致）= 296 个
//	字面不等于任何 canonical 名                              = 60 个
//	  ⇒ 这 60 个承载 17,559 笔 = 全量 123,367 的 14.23% 会被「按 canonical 过滤」漏掉
//	能解析到 canonical id（三条路径任一命中）                = 355 个
//	完全无 canonical 归属                                     = 1 个
//	  = claude-3-7-sonnet-20250219（220 笔）
//
// ⇒ 只按 canonical 过滤会把 14.23% 的流量藏起来。
//
// 所以过滤前必须**展开**成 raw 名集合：给定名字 → 找到它所属的 canonical
// → 取该 canonical 的全部 raw 名（model_offers 的字面形态 +
// model_aliases 的小写别名）。集合里始终包含原值本身，
// ⇒ 目录外的 raw 名（`<unknown>`）依然能被精确筛中，一行都不丢。
package reportrollup

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// resolveModelCanonicalSQL 把一个模型名解析成它所属的 canonical id。
//
// 三条解析路径各覆盖一类名字：
//  1. canonical 名本身（minimax-m2.5）；
//  2. raw 名（MiniMax-M2.5 / auto/minimax-m2.5）——model_offers 存着
//     每个 raw 名归属的 canonical；
//  3. 别名（minimax_m2_5 / minimax-m2-5）——model_aliases 是小写键。
//
// 比较一律用 lower()：canonical_name 与 alias raw_name 在库里的字面形态
// 不统一（目录里 canonical 多为小写，别名键恒小写，而 raw 名大小写混用），
// 不折叠大小写会把同一个模型拆成两半。
const resolveModelCanonicalSQL = `
WITH target AS (
    SELECT mc.id AS canonical_id
      FROM models_canonical mc
     WHERE lower(mc.canonical_name) = lower($1)
    UNION ALL
    SELECT mo.canonical_id
      FROM model_offers mo
     WHERE lower(mo.raw_model_name) = lower($1)
       AND mo.canonical_id IS NOT NULL
    UNION ALL
    SELECT ma.canonical_id
      FROM model_aliases ma
     WHERE lower(ma.raw_name) = lower($1)
       AND ma.status = 'active'
    LIMIT 1
)
SELECT DISTINCT mo.raw_model_name
  FROM model_offers mo
 WHERE mo.canonical_id IN (SELECT canonical_id FROM target)
UNION
SELECT ma.raw_name
  FROM model_aliases ma
 WHERE ma.canonical_id IN (SELECT canonical_id FROM target)
   AND ma.status = 'active'
`

// ResolveModelRawNames 把用户给的模型名展开成一组 raw_model_name。
//
// 返回值**始终包含入参本身**（原样），所以：
//   - 目录外的 raw 名（`<unknown>`、provider 私有的 raw 名）照常精确命中；
//   - canonical 名则命中整个家族。
//
// 查不到 canonical（名字不属于任何目录条目）时只返回原值 ⇒ 过滤退化成
// 精确匹配，行为与展开前一致（不会因为目录缺项把用户筛空）。
//
// 失败时返回 error，调用方**必须**降级成「只用原值精确匹配」而不是把过滤
// 悄悄丢掉——丢掉过滤会让页面显示全量数据，用户以为筛过了。
func ResolveModelRawNames(ctx context.Context, q Querier, name string) ([]string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, nil
	}
	out := map[string]bool{trimmed: true}
	rows, err := q.Query(ctx, resolveModelCanonicalSQL, trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve model names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan model name: %w", err)
		}
		if raw != "" {
			out[raw] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for raw := range out {
		names = append(names, raw)
	}
	sort.Strings(names)
	return names, nil
}
