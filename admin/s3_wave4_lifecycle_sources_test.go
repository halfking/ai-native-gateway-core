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

func countOccurrences(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	return strings.Count(haystack, needle)
}

func TestDataLifecycleTrendServesBothFamiliesWithFaithfulPredicate(t *testing.T) {
	for _, tc := range []struct{ name, tenantFilter string }{
		{"unscoped", ""},
		{"scoped", " AND rl.tenant_id = 'default'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sql := stripSQLComments(dataLifecycleTrendSQL(tc.tenantFilter))
			bodies := sql

			// ★ 2026-10-20（迁移 890，v1 族真删）：**跨族**这件事本身退役了。
			// 原来的两腿是「v1 bodies 视图腿（冻结点之前的停写前历史）」+
			// 「会话族腿」；v1 族被 DROP ⇒ v1 腿必然 42P01 ⇒ 它必须消失。
			// 冻结点 db.V1FreezeCutoffSQL 此刻是常数 '-infinity'（db 侧
			// v1_freeze_cutoff.go 的 890 注释），所以会话腿覆盖全部时间，
			// 与删掉 v1 腿之前的实际取值**逐字相同**（v1 腿在冻结点之下恒空）。
			//
			// ⇒ 本门从「两条腿都必须在场」**反转**为「v1 族关系名必须不在场」。
			if hits := retiredV1RelationsInSQL(bodies); len(hits) > 0 {
				t.Errorf("压缩率的 v1 腿仍出现已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP，读它 = 42P01：\n%s", hits, sql)
			}
			// 就地阳性对照：同一把尺子必须能认出 v1 形态（否则上面那条恒绿）。
			if hits := retiredV1RelationsInSQL("FROM request_logs_bodies_with_current_month rb"); len(hits) == 0 {
				t.Error("retiredV1RelationsInSQL 认不出 v1 正文视图 —— 负向断言已恒绿，判据坏了")
			}
			// 会话腿走 session_bodies_unified 合并视图（hot ∪ 分区父表，迁移 625）。
			// 只查 hot 会漏掉已 promote 的行、只查父表会漏掉最近数小时——
			// 裸父表读法另有两面门（session_family_two_surface_test）钉着。
			// scoped 那支不用 unified：它的会话腿是 dataLifecycleCompressedExists
			// 里的 EXISTS 子查询（SessionFamilyBodiesSourceSQL = hot∪父表），
			// 由 TestDataLifecycleScopedBranchUsesExistenceNotJoin 的 EXISTS
			// 计数断言覆盖。
			if tc.name == "unscoped" && !strings.Contains(bodies, "public.session_bodies_unified") {
				t.Errorf("没有会话族腿（session_bodies_unified）—— 停写后的新流量会从压缩率里消失：\n%s", sql)
			}

			// ★ 关键性质：会话腿的「有出站正文」判据必须排除 jsonb null。
			// 会话族用它表示「没有正文」（真库实测 11,420 行 jsonb-null、
			// 0 行 SQL NULL），而 v1 用 SQL NULL。所以会话族上裸
			// `IS NOT NULL` 是恒真：实测会让压缩率 250 → 993（当天全部 turn）。
			//
			// ⚠ 必须 count==N 而不是 Contains（判据纪律第 7、9 条）：
			// 趋势的无租户支有**一条**会话腿（unified 单腿）。
			wantExclusions := 1
			if got := countOccurrences(bodies, "outbound_body <> 'null'::jsonb"); got != wantExclusions {
				t.Errorf("jsonb-null 排除出现 %d 次，期望 %d（会话腿各一次）：\n%s\n"+
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
	// ★ 2026-10-20（迁移 890）：跨族的 v1 腿退役 ⇒ EXISTS 分支从 2 个
	// （v1 腿 + 会话族腿）收敛为 **1 个**（只剩会话族腿）。
	if got := countOccurrences(scoped, "EXISTS (SELECT 1 FROM"); got != 1 {
		t.Errorf("带租户那一支有 %d 个 EXISTS 分支，期望 1（v1 腿已随 890 退役，只剩会话族腿）：\n%s", got, scoped)
	}
	if !strings.Contains(scoped, dataLifecycleCompressedExists) {
		t.Errorf("带租户那一支没有内嵌 dataLifecycleCompressedExists：\n%s", scoped)
	}
	// ★ 反转：原来这条反向断言守的是「v1 腿的**裸** IS NOT NULL 是对的」
	//（v1 用 SQL NULL 表示无正文）。v1 腿退役后，会话族是**唯一**的一族，
	// 而会话族用 jsonb null 表示「没有正文」（真库实测 11,420 行 jsonb-null、
	// 0 行 SQL NULL）⇒ 裸 IS NOT NULL 在会话族上是**恒真**（实测会把
	// 压缩率 250 → 993 = 当天全部 turn 数）。
	// ⇒ 判据反转成「会话族腿必须带 jsonb-null 排除」。
	if !strings.Contains(scoped, "sb.outbound_body IS NOT NULL AND sb.outbound_body <> 'null'::jsonb") {
		t.Errorf("带租户那一支的会话族腿判据被改动了 —— 会话族用 jsonb null 表示「没有正文」，"+
			"裸 IS NOT NULL 恒真 ⇒ 压缩率会等于当天全部 turn 数（真库实测 993 vs 真实 250）：\n%s", scoped)
	}
	// v1 族关系名必须不在场（v1 腿退役；反向对照：尺子本身能认出 v1）。
	if hits := retiredV1RelationsInSQL(scoped); len(hits) > 0 {
		t.Errorf("带租户那一支仍出现已退役 v1 族关系名 %v：\n%s", hits, scoped)
	}
	if hits := retiredV1RelationsInSQL("FROM request_logs_bodies_with_current_month v"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 正文视图 —— 负向断言已恒绿，判据坏了")
	}
}

// 注：本文件原另有两条钉 `dataLifecycleBodiesBytesSource` /
// `dataLifecycleBlobCleanupPreviewSQL` 的判据（S3 波 4 第二片），合并
// origin/main 的 S4 波 4 下半场时 data_lifecycle_blobs.go 采用了并行会话的
// 会话臂实现（sessionBodiesGovernanceSource），那两条随符号一并退役；
// blobs 侧的钉桩由 s3_wave4_bodies_sources_test.go 接管。本文件只保留
// data_lifecycle.go（趋势端点）的判据——该文件保留了本侧的跨族 + 冻结点
// 切分实现（远端版本 trend 仍 v1-only，生产 data-lifecycle/stats 500@30s
// 的既存超时正是这一族查询的 v1 存储语义）。
