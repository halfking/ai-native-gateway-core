//go:build !integration

package admin

// request_logs_view_arm_cutover_test.go — 2026-10-05（审计 §9.206 / 决策 D29-d）。
//
// # 这道门补的洞
//
// D29-a 把「直读 v1 底表的读方」和「经 canonical 视图读 v1 臂的读方」分成两个总体
// （审计 §9.199），并确认后者**不是 breaker**——它们今天工作正常。
//
// 但那 10 个读方当时只有门内一句 `t.Logf`。这不是守卫：
// **新增一个经视图读 v1 臂的读方，不会让任何东西变红**，
// 而「没人提」和「没问题」长得一模一样。
// ⇒ 本文件把那 10 个读方变成**登记表 + 双向一致性门**。
//
// # 这不是 blocker，是**切换时的迁移清单**
//
// 语义与 retirementBreakers 完全不同，措辞必须跟着变：
//
//	直读底表（DROP 之前必须改） vs 经视图读（DROP **那一刻**才出问题）
//
// 触发条件是 `request_logs_with_current_month` 的 v1 臂随 `DROP request_logs` 消失
// ⇒ 这些文件**不是报错，而是静默返回空结果**。典型的 work_type 在 session 臂上是 0%，
// 页面变空、接口 200、错误日志什么都不留。
//
// 静默降级比缺表更危险：缺表至少会喊。
//
// # 双向一致性
//
//   - 实测到了但没登记 → 有人新增了依赖，没人被告知；
//   - 登记了但实测不到 → 要么它被修好了（那就销账），要么提取器看不见它了
//     （**先修提取器**，见下面的 stale 提示）。
//
// ⚠ 「不再出现在实测里」**不等于**「落了它无害」：也可能只是这个文件的依赖
// 被重构成了另一种形状。所以 stale 分支要求人工判断，不自动销账。

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	db "github.com/kaixuan/llm-gateway-go/db"
)

// viewArmCutoverReaders 是 D29-d 的**切换迁移清单**。
//
// ⚠ 措辞纪律：这里**不能**写 "reads request_logs.X directly"——这些文件
// **不直读底表**，它们读的是视图的 v1 臂。写成直读就是不实陈述，
// 而**不实陈述比缺一条登记更糟**，因为它会误导下一个复核的人（§9.199）。
//
// ⚠ 这里**只放真正的读方**。`db/db.go`（canonical 投影的定义者）与
// `domains/hooks/observability/telemetry/client.go`（v1 **写方**）同样出现在
// view-arm 扫描里——因为它们**提到了那些关系名**——但它们的角色不是「读方」：
// 前者退役 request_logs = 换掉整个投影体，后者 = 停止写入。
// 两者的**处置时点与读方不同**（读方是切换那一刻才降级，这两者是切换之前就要动），
// 所以它们留在 retirementBreakers，不在这里重复登记。
//
// 括号里的列与 verdict 由本文件的门**实测**得出（`go test -v` 即可看到），
// **不要手抄**：手抄的列清单从第一次提交起就开始腐烂。
var viewArmCutoverReaders = map[string]string{
	"admin/auto_route.go":         "**work_type 是主要风险**：verdict=repoint-empty，session 臂上 work_type 实测 0.00%。切换后按 work_type 过滤 ⇒ 返回空集，接口仍 200",
	"admin/credential_monitor.go": "verdict=repoint-gap-only：依赖 error_kind 这类「v1 有、session 臂无对应源」的列，切换后监控维度会缺",
	"admin/memora_handlers.go":    "**work_type + request_preview/response_preview**：verdict=repoint-empty。preview 在 session 臂约 39%，切换后摘要输入变「有轮次、无预览」",
	"admin/no_topic_session.go":   "**work_type + preview 列**：verdict=repoint-empty。preview 在 session 臂约 39%",
	"admin/session_extract.go":    "**依赖面最窄的一个**（6 列）但含 work_type：verdict=repoint-empty",
	"admin/session_online.go": "verdict=repoint-gap-only：依赖 `id` 与 `tenant_id`。" +
		"经 `JOIN request_logs_with_current_month rl ON rl.id = slr.last_request_id` " +
		"把 `session_last_requests.last_request_id` 解回 v1 那一行只为拿 tenant_id。" +
		"⚠ **这一条是 §9.226.2 扩 from|join 之后才进清单的**：它只 JOIN 不 FROM，" +
		"在旧扫描器下对 inventory 完全不可见。切换后若 v1 臂消失，这个 JOIN 取不到 id，" +
		"在线会话列表会**整页为空**（不是缺一列，是连不上行）——" +
		"比 repoint-empty 更靠前一步的失效。",
	"admin/session_timeline_query.go":      "**work_type + preview 列**：verdict=repoint-empty。会话抽取页按 work_type 过滤会空",
	"domains/sessionforensics/export.go":   "**混合读方**（既经视图读又直读底表），两个桶都收它；verdict=repoint-empty。切换后取证导出会缺 work_type/attachments",
	"domains/sessionsummary/summarizer.go": "verdict=repoint-gap-only ⚠ **后果最隐蔽**：摘要输入变空不会失败，会生成「看起来正常」的错摘要——比失败更难发现",
}

// TestViewArmCutoverReadersRegistryIsConsistent gates the cutover manifest in
// both directions, and prints the measured columns so whoever performs the
// cutover gets numbers rather than a file list.
func TestViewArmCutoverReadersRegistryIsConsistent(t *testing.T) {
	root := repoRootFromCaller(t)
	_, viewArm := measureV1ReadingExposure(t, root)

	// ⚠⚠ 第一版在这里写的是「两个登记表必须不相交」，**那是错的**，
	// 而且被真库实测当场否掉：`db/db.go`（投影定义者）与 `telemetry/client.go`
	// （v1 writer）本来就该留在 retirementBreakers，它们却也出现在 view-arm 扫描里
	// —— 因为它们**提到了那些关系名**。
	// 判据红了先怀疑判据：这条「不变式」把**角色不同**当成了**登记重复**。
	//
	// 真正的不变式有两条，方向相反：
	//
	//  1. viewArmCutoverReaders ∩ retirementBreakers = ∅
	//     「切换清单里的条目声称自己是读方」与「它是 blocker」不能同时成立 ——
	//     两者的处置时点不同，混写会让切换清单里混进一个早就该改的文件。
	//  2. viewArm ⊆ viewArmCutoverReaders ∪ retirementBreakers
	//     每个实测到的依赖**必须被解释**。落在并集之外的那个才是真正的洞。
	var contradictory []string
	for f := range viewArmCutoverReaders {
		if _, ok := retirementBreakers[f]; ok {
			contradictory = append(contradictory, f)
		}
	}
	if len(contradictory) > 0 {
		sort.Strings(contradictory)
		t.Errorf("%d 个文件**同时**登记在 viewArmCutoverReaders 与 retirementBreakers 里（%s）—— "+
			"它的登记声称自己是「切换时才降级的读方」，而 breaker 声称它是「DROP 之前必须改」。"+
			"处置时点不同，择一并删掉另一条：\n"+
			"  直读底表 ⇒ 只留 retirementBreakers；仅经视图读 ⇒ 只留 viewArmCutoverReaders",
			len(contradictory), strings.Join(contradictory, ", "))
	}

	// 不变式 2：每个实测依赖都要被解释。被 breaker 解释的那些**打出来**
	// （可见但不失败）—— 它们不是洞，只是角色不同。
	var unregistered, explained, stale []string
	for f := range viewArm {
		_, inCutover := viewArmCutoverReaders[f]
		_, inBreakers := retirementBreakers[f]
		switch {
		case inCutover:
		case inBreakers:
			explained = append(explained, f+" ("+viewArm[f]+")")
		default:
			unregistered = append(unregistered, f+" ("+viewArm[f]+")")
		}
	}
	sort.Strings(explained)
	if len(explained) > 0 {
		t.Logf("=== 实测到、但由 retirementBreakers 解释（角色不同，不是读方，不算洞）：%d 个 ===",
			len(explained))
		for _, e := range explained {
			t.Logf("  %s", e)
		}
	}
	for f := range viewArmCutoverReaders {
		if _, ok := viewArm[f]; !ok {
			stale = append(stale, f)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	// 把实测列打出来：清单的价值在于切换那天能带着**数字**走，
	// 而不是一个文件名。仅在有内容时打印，避免无内容时刷屏。
	for _, f := range sortedStringKeys(viewArm) {
		cols := map[string]bool{}
		for _, l := range extractV1ReadingLiterals(t, filepath.Join(root, f)) {
			if !(l.viaCanonicalView || !l.viaBaseTable) {
				continue
			}
			for c := range l.allColumns {
				cols[c] = true
			}
		}
		names := make([]string, 0, len(cols))
		for c := range cols {
			names = append(names, c)
		}
		sort.Strings(names)
		if len(names) == 0 {
			// ⚠ 零样本必须指名，不能当成「没有依赖」。
			t.Logf("  %-52s [%s]  ⚠ 未测到任何列（不是「无依赖」）", f, viewArm[f])
			continue
		}
		verdict := db.RetirementRepointVerdictFor(names)
		t.Logf("  %-52s [%s]  cols=[%s]  verdict=%s",
			f, viewArm[f], strings.Join(names, " "), verdict)
	}

	if len(unregistered) > 0 {
		t.Errorf("%d 个文件经 canonical 视图读 v1 臂、且读了会话族供不上的列，但**不在** "+
			"viewArmCutoverReaders 里 —— 没人被告知它们在切换时会静默降级：\n  %s\n"+
			"处置：登记进 viewArmCutoverReaders（措辞必须是「切换时的迁移问题」，"+
			"**不是**「reads request_logs.X directly」），或先把它 repoint 掉。",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}

	if len(stale) > 0 {
		t.Errorf("%d 个登记在 viewArmCutoverReaders 里的文件，实测已不再经视图读 v1 臂 —— "+
			"**不要自动销账**，先判断是哪一种：\n"+
			"  (a) 它被 repoint / 修好了 ⇒ 那就删掉条目，否则每个复核的人都要重查一遍已解决的问题；\n"+
			"  (b) 它的依赖被重构成了另一种形状，提取器看不见了 ⇒ **先修提取器**，\n"+
			"      否则这条门正在对真实依赖失明，而「门还绿着」会被读成「没问题」。\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
