//go:build !integration

package admin

import (
	"strings"
	"testing"
)

// S3 波 4（bodies 逐点评估第二片）：存储治理面（data_lifecycle /
// data_lifecycle_blobs）的 bodies 读源钉桩。
//
// # 这道门守的是什么
//
// 停写后这两处都在**静默低报**，而它们的消费方是运维决策面：
//
//   - `dataLifecycleTrendSQL` 的 `compressed` 计数（压缩率）：v1-only 口径
//     7 天窗只认 7,076 轮，跨族忠实口径是 21,431 ⇒ 低报 68%；
//   - `blobs/cleanup/preview` 的 `EstimatedFreedBytes`：v1-only 口径报
//     13.9 MB，跨族口径 100.9 MB ⇒ **少报 86 MB 的可释放字节**，而
//     停写后的新正文在这个面板上**一律 0 字节**。
//
// # 第一版判据自己踩过的坑（判据纪律第 9 条的又一次）
//
// 我第一版只钉「有没有会话腿」，没钉**会话腿的判据本身对不对**，于是
// `sb.outbound_body IS NOT NULL` 一直全绿——而它在会话族里是**恒真**
// （真库实测 20,643/20,643 行通过，因为镜像用 jsonb `null` 而不是 SQL NULL
// 表示「没有正文」）。症状是趋势表的 `compressed` 变成 993 = 当天全部 turn
// 数（真实值 250），**看起来完全正常**。
//
// ⇒ 本门对会话腿钉的是**性质**：`IS NOT NULL` 之后必须跟 jsonb-null 排除。
// 钉拼写（必须出现某个字面量）在这里不成立，因为那个性质在两个族的写法不同。

func TestDataLifecycleTrendServesBothFamiliesWithFaithfulPredicate(t *testing.T) {
	for _, tc := range []struct{ name, tenantFilter string }{
		{"unscoped", ""},
		{"scoped", " AND rl.tenant_id = 'default'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sql := stripSQLComments(dataLifecycleTrendSQL(tc.tenantFilter))
			bodies := sql

			// 两条腿都必须在场（count 而非 Contains —— 删一条腿是静默回退）。
			if got := countOccurrences(bodies, "request_logs_bodies_with_current_month"); got == 0 {
				t.Errorf("没有 v1 腿 —— 停写前的历史会从压缩率里消失：\n%s", sql)
			}
			if !strings.Contains(bodies, "public.session_bodies_hot") ||
				!strings.Contains(bodies, "public.session_bodies") {
				t.Errorf("没有会话族双腿（hot ∪ parent）—— 只查 hot 会漏掉已 promote 的行：\n%s", sql)
			}

			// ★ 关键性质：会话腿的「有出站正文」判据必须排除 jsonb null。
			// 会话族用它表示「没有正文」（真库实测 11,420 行 jsonb-null、
			// 0 行 SQL NULL），而 v1 用 SQL NULL。所以会话族上裸
			// `IS NOT NULL` 是恒真：实测会让压缩率 250 → 993（当天全部 turn）。
			//
			// ⚠ 必须 count==N 而不是 Contains（判据纪律第 7、9 条）：
			// 无租户那一支有**两条**会话腿（hot ∪ parent）。第一版写
			// `count == 0` 时，把 parent 那条改成恒真、只留 hot 那条忠实，
			// 判据照样全绿（变异验证 M6 实测假绿）——而"两条腿里一条在说谎"
			// 正是这里要拦的东西：已 promote 的行会全部被算成压缩过。
			wantExclusions := 1 // 带租户那一支只有一条 EXISTS 会话腿
			if tc.name == "unscoped" {
				wantExclusions = 2 // hot ∪ parent 两条
			}
			if got := countOccurrences(bodies, "outbound_body <> 'null'::jsonb"); got != wantExclusions {
				t.Errorf("jsonb-null 排除出现 %d 次，期望 %d（每条会话腿各一次）：\n%s\n"+
					"会话族用 jsonb null 表示「没有正文」，裸 IS NOT NULL 恒真 ⇒ "+
					"压缩率会等于当天全部 turn 数（真库实测 993 vs 真实 250）。",
					got, wantExclusions, sql)
			}
		})
	}
}

func TestDataLifecycleScopedBranchUsesExistenceNotJoin(t *testing.T) {
	scoped := stripSQLComments(dataLifecycleTrendSQL(" AND rl.tenant_id = 't'"))

	// 带租户那一支要数的是「有多少轮带出站正文」——存在性问题，EXISTS 就够，
	// 且天然免疫镜像双生（本地实测 11,711 个 request_id 同时存在于两族）。
	// 换回 JOIN 形态必须先撞这里：那会让 COUNT 随双生膨胀。
	if got := countOccurrences(scoped, "EXISTS (SELECT 1 FROM"); got != 2 {
		t.Errorf("带租户那一支有 %d 个 EXISTS 分支，期望 2（v1 腿 + 会话族腿）：\n%s", got, scoped)
	}
	if !strings.Contains(scoped, dataLifecycleCompressedExists) {
		t.Errorf("带租户那一支没有内嵌 dataLifecycleCompressedExists：\n%s", scoped)
	}
	// v1 腿的裸 IS NOT NULL 是**对的**（v1 用 SQL NULL 表示无正文，实测
	// jsonb-null 0 行）。这条反向断言是防止有人"顺手统一"成 jsonb 写法，
	// 那会让 v1 侧恒真、多算 1,201 行（真库实测 10-12：v1 5,405 vs
	// 会话族忠实 4,204，并集恰好 5,405 —— 靠的正是 v1 侧不加排除）。
	if !strings.Contains(scoped, "v.request_id = rl.request_id AND v.outbound_body IS NOT NULL") {
		t.Errorf("带租户那一支的 v1 腿判据被改动了 —— v1 侧不该加 jsonb-null 排除：\n%s", scoped)
	}
}

func TestDataLifecycleBodiesBytesSourceDedupesTwins(t *testing.T) {
	src := dataLifecycleBodiesBytesSource

	// 镜像双生：同一 request_id 在两族都存在（本地实测 11,711 个）。
	// 不去重就会把字节数算两遍。
	if !strings.Contains(src, "DISTINCT ON (request_id)") {
		t.Errorf("字节源没有按 request_id 去重 —— 双生会被算两遍，清理预览的"+
			"可释放字节虚高（本地实测双生 11,711 个）：\n%s", src)
	}
	// 三条腿都要在场。
	for _, rel := range []string{
		"request_logs_bodies_with_current_month",
		"public.session_bodies_hot",
		"public.session_bodies",
	} {
		if !strings.Contains(src, rel) {
			t.Errorf("字节源缺少 %s：\n%s", rel, src)
		}
	}
	// 排序键决定「挑哪一份副本」：先挑有内容的，再挑会话副本。
	// 三键缺一不可——只按族排序，会让「会话副本是 jsonb-null」压掉
	// 「v1 副本有正文」的行（真库实测 v1 多出的 1,201 行正是这种）。
	order := strings.Index(src, "ORDER BY request_id,")
	if order < 0 {
		t.Fatalf("字节源没有 ORDER BY（去重挑副本的规则缺失）：\n%s", src)
	}
	tail := src[order:]
	for _, key := range []string{"outbound_body IS NOT NULL", "request_body IS NOT NULL", "fam DESC"} {
		if !strings.Contains(tail, key) {
			t.Errorf("去重排序键缺少 %q —— 副本选择规则不完整：\n%s", key, tail)
		}
	}
}

// TestDataLifecycleBlobsPreviewCountsAreFaithful 钉住清理预览的两个计数列。
//
// 预览把 `COUNT(*) FILTER (WHERE ...)` 当成「有多少行的请求体会被置 NULL」。
// 跨族源投影的是 `pg_column_size(...)`，而 jsonb `null` 的 size 是 4（**不是
// NULL**）⇒ 直接沿用 `IS NOT NULL` 会把 1,882 行 jsonb-null 的请求体算成
// "可清理"，与 v1 口径不等价。
//
// ⚠ 第二版把判据从「字节源投影了什么」改成「**预览这句 SQL 用了什么**」。
// 第一版钉源投影时，把 FILTER 改回 `request_body_bytes IS NOT NULL` 判据
// 依然全绿（变异验证 M5 实测假绿）——投影还在，只是没人用它。
// ⇒ 凡是要拦「消费方改回错判据」，判据必须落在消费方那一句上。
func TestDataLifecycleBlobsPreviewCountsAreFaithful(t *testing.T) {
	sql := stripSQLComments(dataLifecycleBlobCleanupPreviewSQL(""))

	for _, col := range []string{"has_request_body", "has_outbound_body"} {
		if !strings.Contains(sql, "COUNT(*) FILTER (WHERE rb."+col+")") {
			t.Errorf("预览的 COUNT FILTER 没有用 rb.%s —— 退回按字节列判会把 jsonb-null "+
				"算成可清理（jsonb null 的 size=4，不是 NULL）：\n%s", col, sql)
		}
	}
	// 反向钉桩：按字节列判的两个写法都不得回来。
	for _, bad := range []string{
		"FILTER (WHERE rb.request_body_bytes IS NOT NULL)",
		"FILTER (WHERE rb.outbound_body_bytes IS NOT NULL)",
	} {
		if strings.Contains(sql, bad) {
			t.Errorf("预览又用字节列判「有正文」了（%q）——jsonb null 的 size 是 4：\n%s", bad, sql)
		}
	}
	// 字节数那列仍用字节（空间口径）：jsonb null 占 4 字节，置 NULL 确实省。
	if !strings.Contains(sql, "SUM(COALESCE(rb.request_body_bytes,0) + COALESCE(rb.outbound_body_bytes,0))") {
		t.Errorf("字节合计不再用字节列 —— 空间口径与行数口径不同，不要一并改掉：\n%s", sql)
	}
}
