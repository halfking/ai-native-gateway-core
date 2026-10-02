package admin

// request_logs_stop_write_classification_test.go — 2026-10-02：S4 停写的
// 逐文件依赖分类与覆盖率门。
//
// 审计 §8.5 的结论是：读 request_logs 的生产文件有 104 个、调用点 237 处，
// 而逐点评估**一个都没做过**。`TestRequestLogsReadInventoryIsComplete`
// （request_logs_read_inventory_test.go）钉住了「有 104 个」，但那只数个数，
// 不下判定——所以它防的是「缺口扩大时有人知道」，防不了「缺口已被评估」。
//
// 本文件提供后一半：一个逐文件分类登记，以及一道**覆盖率门**。
//
// # 分级按「停写之后会发生什么」，不按「读的是哪张表」
//
// 早先的分类（审计 §5.5.5）按「会话内读 / 全量流量」分。那是**查询形态**的
// 分类，答的是「能不能迁到 session 原生源」。但运维在灰度前要回答的是另一个
// 问题：「这个功能关停之后会怎样？」——而同一形态的两次读可能给出完全不同的
// 答案：一次 COUNT 会静默变成常数，一次 JOIN 缺失会直接报错。**报错是好事**
// （灰度时会立刻发现），**静默才是灰度的真正风险**。所以这里按后果分。
//
// # 为什么「未分类」不能是一种合法状态
//
// 最容易出现的自欺是：把大部分文件随手归到某一类，让表看起来是满的。
// 所以覆盖率门的判据是**未分类集合必须为空**，并且每条登记都必须带一段
// **在该文件里逐字存在**的证据（见 evidence 字段）——证据不存在即红。
// 这样「我大概知道它是会话内读」和「我打开文件确认过它的 WHERE 长什么样」
// 成了两件不同的事，前者进不来。
//
// 门**现在就红**：截至登记时点仍有未分类项，这是事实，不是门坏了。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// stopWriteEffect 是「S4 停写之后这个读点会怎样」。
//
// 注意每一档都写明**是报错还是静默**——这是本分类存在的理由。
const (
	// effectErrorsOut：查询形态要求新数据，停写后必然查不到 → 端点 500/空。
	// 这是**可接受**的失败模式：灰度时立刻暴露，不会悄悄给出错误答案。
	effectErrorsOut = "errors_out"

	// effectSilentlyEmpty：结果集变空但不报错。危险等级最高的一档——
	// 接口返回 200、字段齐全、数值全是零或空列表。
	effectSilentlyEmpty = "silently_empty"

	// effectSilentlyFrozen：结果是「存量不再增长」的常数。看不到任何错误信号，
	// 但每个基于它的判断（计数、比率、推荐、健康度）从此只反映停写之前。
	effectSilentlyFrozen = "silently_frozen"

	// effectUnaffected：读的是表的**结构/体量/生命周期**，不是流量。
	// 停写不改变这些语义（保留期、占用空间、待清理行数照常统计）。
	effectUnaffected = "unaffected_by_stop_write"

	// effectValidator：刻意比较 v1 与 session 族的对账面。
	// 停写后它的语义由 §8.8 处理（报告 void 而不是给出许可）。
	effectValidator = "validator_dual_read"

	// effectSilentlyDegradedContent：2026-10-02 新增。**行还在，某一列的内容
	// 静默变空或降级**，接口 200、字段齐全、结果集非空。
	//
	// 为什么必须单列一档（batch1 与 batch4 各自独立撞上同一堵墙）：前五档描述
	// 的都是「结果集」——空不空、冻不冻。但带 bodies 腿的读点停写后是另一种
	// 形状：主腿走 710 视图，**行照常出**（session 臂供数），而
	// `LEFT JOIN request_logs_bodies_* … COALESCE(rb.request_body,'')` 的正文腿
	// 没有 session 兜底 ⇒ 拿到的是**有行、没正文**。
	// 填 silently_empty 不准（结果集没空），填 silently_frozen 也不准（不是冻结
	// 在旧值，是这一列变成空串/NULL/0）。硬塞进任何一档，都会让「bodies 腿到底
	// 算不算硬失败」这个问题被分类表的沉默吞掉。
	effectSilentlyDegradedContent = "silently_degraded_content"
)

// 合法分级集合。新增档位必须同时更新本集合与本文件顶部的说明。
var stopWriteEffects = map[string]string{
	effectErrorsOut:               "停写后必然查不到，端点报错或返回空——可接受的失败模式",
	effectSilentlyEmpty:           "结果集静默变空但接口仍 200——灰度的真正风险",
	effectSilentlyFrozen:          "结果静默冻结为停写前的常数，无任何错误信号",
	effectUnaffected:              "读表结构/体量/生命周期，不读流量——停写不改变其语义",
	effectValidator:               "刻意做 v1↔session 对账，语义由 dual-read 门处理",
	effectSilentlyDegradedContent: "行还在但某一列内容静默变空/降级（典型：bodies 腿无 session 兜底）",
}

// stopWriteClassification 是逐文件登记。
//
//	Evidence 必须逐字出现在该文件里（门会核）。它不是描述，是**锚**：
//
// 用来把「我读过这个文件」和「我以为我读过这个文件」分开。
type stopWriteClassification struct {
	Effect   string
	Evidence string
	Note     string
}

// 尚未逐点评估的文件必须显式登记为 unclassified，而不是从表里消失——
// 「表里没有」和「已确认无关」是两件事，只有前者能被门看见。
const effectUnclassified = "unclassified"

// bodiesUnaffectedJustification 登记「读 bodies 族但判 unaffected」的**具名豁免**。
//
// 与其他豁免表不同，本表刻意**极小且每条都写机制**。判据的默认是拒绝：
// 放进这张表等于公开声明「我知道 bodies 没有 session 兜底，我论证了为什么
// 这个文件仍然不受影响」。空字符串或缺项都会被门判红。
//
// 每条豁免都必须回答同一个问题：**停写之后，这个读点返回的东西会变吗？**
var bodiesUnaffectedJustification = map[string]string{
	"admin/telemetry.go": "读点在写门内：upsertRequestLogBodies 的 ts 回填与 bodies 镜像" +
		"全部包在 `if requestLogsWriteEnabled() {}` 内（:391、:454）。停写后这段不执行，" +
		"读点根本不发生 ⇒ 无「返回内容变空」可言。这与「读 bodies 拿到空正文」是两种形状。",
	"db/db.go": "两处 bodies 引用都不是读内容：:7197 与 :7231 里的 " +
		"`'request_logs_bodies'` 是 **pg_class.relname 的字符串名单**（ALTER TABLE SET storage " +
		"参数 / ANALYZE 分区巡检），表名出现在一个正则/数组里，不是 FROM/JOIN 任何一张表。" +
		"bodies 族按正则识别，把「名字出现在巡检名单里」也算成「读 bodies」。" +
		"本文件全部 request_logs 命中都是结构面（见分级表的 Note）。",
	"admin/data_lifecycle_blobs.go": "读的是体量不是内容：唯一的 bodies 引用是 " +
		"`COALESCE(pg_column_size(rb.request_body), 0)`，即「存量正文占多少字节、" +
		"清理能省多少」。停写不改变存量字节数，也不改变待清理行数。" +
		"注意 bodies 族是按正则识别的，会把 pg_column_size 也算成「读 bodies」，" +
		"所以这条豁免不是可有可无的形式主义。",
}

// requestLogsStopWriteClassification 覆盖 requestLogsReadInventory 的 104 个文件。
//
// 键必须与 requestLogsReadInventory 完全一致（由门双向核）。
// 值在完成逐点评估前大量为 unclassified——**这是当前真实状态，不是占位符**。
var requestLogsStopWriteClassification = map[string]stopWriteClassification{
	// ── 本轮亲自打开确认的（证据为文件中逐字存在的片段）──────────────────
	// ── batch1：2026-10-02 逐点评估（29 条）────────────────────────────────
	//
	// 判据见 classificationHowTo。本批**没有**照抄子代理的结论：
	// 它自报「6 个文件的机械族有误」，我逐个核了 sourceFamilyOf 的实际返回值，
	// 结论是**分类器是对的**（那 5 个文件确实只读 710 视图，族就是 reads_710_view_only），
	// 子代理把「机械统计里的某个数字」当成了族标签。照它去「修」会修坏一个没坏的分类器。
	"admin/analytics.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_hot\n\t\t    WHERE request_id = ANY($1::text[])",
		Note:     "决策回放按 request_id 查 hot∪母表双腿；`errors.Is(err, pgx.ErrNoRows)` 已显式转 404（analytics.go:806），停写后新请求查不到立刻暴露。属可接受失败模式。",
	},
	"admin/data_lifecycle_attachments.go": {
		Effect:   effectErrorsOut,
		Evidence: "SELECT request_id, ts, tenant_id,\n\t\t       COALESCE(client_model, ''), success, attachments::text\n\t\tFROM request_logs",
		Note:     "三个读点全走裸母表。停写只冻结不删历史 ⇒ 历史附件仍可列出，只有停写后的新请求查不到：单条 404、列表尾部缺失。errors_out 而非静默。",
	},
	"admin/provider_diagnose.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT COALESCE(error_kind,'other'), COUNT(*) FROM request_logs_hot WHERE provider_id = $1 AND ts >= now() - interval '24 hours' GROUP BY error_kind",
		Note:     "24h 错误分类窗口（:529，`ecRows, _ :=` 显式丢弃 error）读 hot 冻结面。停写后错误计数恒为旧值，接口 200、字段齐全、无错误信号——健康度评分看起来「稳定良好」。",
	},
	"admin/session_detail_v2.go": {
		Effect:   effectErrorsOut,
		Evidence: "SELECT request_id FROM request_logs\n\t\t      WHERE tenant_id = $1 AND gw_session_id = $2",
		Note:     "主腿已迁 session_turns_with_current_month；残留 v1 读点只在 resolveSessionID 反向映射的 UNION 末两条腿，session 腿先行兜住活跃会话，0 候选走 errSessionNotFound → 404。代码注释 556-558 已自认此风险。",
	},
	"autoroute/recommend_v2.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT canonical_id, count(*) as usage_count\n\t\tFROM request_logs\n\t\tWHERE ts > NOW() - INTERVAL '48 hours'",
		Note:     "48h Top-3 canonical 榜与纠正分读点（:393-407，err 直接 `return map[string]float64{}, nil` 全吞）都读冻结母表。停写后 Top-3 返空 → fallback 挑不到模型且被写进 2 分钟 TTL 缓存持续生效，全程无错误。",
	},
	"bg/credential_selfcheck.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot rl\n\t\t\tWHERE rl.credential_id = c.id\n\t\t\t  AND rl.ts >= now() - interval '24 hours'",
		Note:     "pickDueCredential 的 MAX(rl.ts) 24h 窗口（:259-262）驱动自检选点；停写后窗口恒为旧行，ErrNoRows 被当「本轮无到期凭据」静默跳过。控制面轴另判为 live（写 self_check_runs / 探针提交）。",
	},
	"bg/model_probe.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT COALESCE(rl.outbound_model, rl.client_model) AS raw_model,\n\t\t\t               count(*) AS calls\n\t\t\t        FROM request_logs_hot rl\n\t\t\t        WHERE rl.success",
		Note:     "探测 usage 扫描全读 hot 冻结面。停写后 usage 集恒为旧值 ⇒ 非精选模型被无限延长 next_retry_at、精选模型过不了 traffic EXISTS 门，探测定时策略静默失真且无报错。",
	},
	"cmd/gateway/dual_read_validator.go": {
		Effect:   effectValidator,
		Evidence: "FROM request_logs_hot\n    WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''\n    UNION ALL",
		Note:     "刻意对账面。§8.2 已修：s4GateVerdictOf 在 v1WritesOn=false 时把 S4Ready 置为 void 并回 S4GateVoid/S4GateVoidReason，停写后报 void 而非给出「可开 S4」许可。",
	},
	"cmd/gateway/waterfall_db.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot\n\tWHERE t0_arrived_at IS NOT NULL\n\t  AND ($1 = '' OR tenant_id = $1)",
		Note:     "admin waterfall 的 DB 兜底腿（ORDER BY t0_arrived_at DESC）读 hot 冻结面。停写后只显示停写前那批 t0~t9 时间戳，接口 200、无错误，只表现为「最近没有瀑布数据」。",
	},
	"domains/analysis/request_summary.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs\n\t\tWHERE gw_session_id = $1",
		Note:     "loadRequestLogs 返回空切片 + nil error（:130-131）。停写后新会话一行取不到，摘要生成器对活跃会话静默产出空摘要，saveSummary 因 len 判定而不落库，无任何错误。",
	},
	"internal/quality/minute_aggregator.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot\nWHERE ts >= date_trunc('minute', NOW() - INTERVAL '1 minute')\n  AND ts < date_trunc('minute', NOW())",
		Note:     "分钟聚合 UPSERT 读上一分钟窗口；停写后窗口恒空，该分钟不产生行，但 ExecContext 返回 nil（无行可写不算错）⇒ request_stats_minute 静默停止增长。",
	},
	"tests/session_audit/cmd/audit-test/main.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs\n\t\tWHERE ts >= NOW() - INTERVAL '7 days'",
		Note:     "会话输出审计测试工具按 7 天窗口抽 body；停写后活跃会话一条不中，len(results) 变小但工具照常跑完并打印「252 数据库 request_logs 表 (最近 7 天)」。是测试工具，业务影响有限。",
	},
	"admin/attachments_routes.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT 1 FROM request_logs_with_current_month\n\t\t\t\t\tWHERE request_id = $1 AND tenant_id = $2",
		Note:     "附件归属校验（attachmentOwnedByTenant）只读 710 视图 EXISTS/JOIN，视图 = session 臂 ∪ v1 臂，session 臂继续增长 ⇒ 新请求仍能命中判归属，fail-closed 语义不变。",
	},
	"admin/model_status.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_with_current_month\n\t\t WHERE ts >= $1\n\t\t   AND client_model IS NOT NULL",
		// 2026-10-02 **自我更正**：batch1 判 unaffected（理由「读 710 视图，session 臂继续供数」），
		// 被新增的 null-padded 族门判红——判对了。逐行核实：:268-269 与 :296-297 是
		//   AND client_model IS NOT NULL
		//   AND TRIM(client_model) <> ''
		// 而 client_model 正是 migration 710 在 session 臂上补位成 NULL 的 30 列之一。
		// ⇒ 视图**照样返回行**，但这两条过滤把全部新流量滤掉 ⇒ 结果集真的为空。
		// 「视图有 session 臂所以不会空」只在不用补位列做谓词时成立。
		Note: "模型健康度看板在停写后**永久空白**：行级有 session 臂，但 client_model 恒 NULL 使过滤恒不命中。" +
			"接口 200、字段齐全、无任何错误信号。",
	},
	"admin/session_extract.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT api_key_id FROM request_logs_with_current_month",
		Note:     "三个读点（api_key_id / tenant_id / 轮次列表）均走 710 视图，代码注释 282-284、305-306 显式说明这是为 S4 停写做的加固（「直读物理表对新会话恒空，这里会静默返回 0 且无告警」）⇒ 停写后反而是正确形态。",
	},
	"admin/session_turns_tree.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month\n\t\t\t\t\tWHERE parent_request_id = ANY($1)",
		Note:     "主轮次/子请求两腿都已迁 710 视图（:324 是唯一真实调用点，其余 request_logs 命中全是注释）；视图 session 臂继续增长，树照常构建。",
	},
	"bg/candidate_failure_monitor.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "(SELECT max(ts) FROM request_logs_with_current_month WHERE ts >= now() - interval '5 minutes')",
		// 2026-10-02 **自我更正**：batch3/batch1 判 unaffected，被 null-padded 族门判红。
		// 核实 :267 `GROUP BY credential_id, provider_id, raw_model_name, error_kind`
		// —— provider_id 是 session 臂补位成 NULL 的列之一（raw_model_name 亦然，
		// 它是 v1-only 列）。行照样出、5 分钟 max(ts) 活性探针照样工作，
		// 但**分组键变了**：新流量全部归到 provider_id=NULL 那一组，
		// 失败率按 provider 的分母静默失真 ⇒ 5 档里没有这一形，取第 6 档。
		Note: "行级与时间窗都不受影响，坏的是分组归属。5 分钟活性探针（max(ts)）照常工作，" +
			"但按 provider 聚合的失败率把新流量全算到 NULL 组。（控制面轴另判 live：它 UPDATE credentials。）",
	},
	"bg/stats_minute_rollup_retire.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month AS r\n    WHERE r.request_status IN ('success', 'failure', 'rate_limited')",
		// 2026-10-02 **自我更正**：batch1 判 unaffected，被 null-padded 族门判红。
		// 核实 :27 / :106-108 的 NOT EXISTS 保护用的是补位列：
		//   AND COALESCE(r.provider_id, 0) = m.provider_id
		//   AND COALESCE(NULLIF(r.outbound_model,''), NULLIF(r.client_model,''), '') = m
		//   AND COALESCE(NULLIF(r.client_profile,''), '') = m.client_profile
		// provider_id / client_model / client_profile 三列在 session 臂恒 NULL
		// ⇒ COALESCE 兜成 0 / '' ⇒ 与已有 rollup 键**匹配不上** ⇒
		// 「这个键近期还有流量」的保护失效 ⇒ 本该保留的分钟行会被当成陈旧退役。
		// 失效方向是**多删派生数据**（不是少读），故取第 6 档并在此写明方向。
		Note: "退役判定依赖补位列做键匹配；停写后保护失效，派生分钟聚合被过度清理。" +
			"（控制面轴判 live：它 DELETE 三张 rollup 表。）",
	},
	"admin/auto_title_generator.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id",
		Note:     "主腿读 710 视图（行照常出），bodies 腿是可选增强且为 LEFT JOIN + 回落 response_preview ⇒ 标题生成主流程可用，但语料从全文降级为预览片段，接口 200 无错误。属 2026-10-02 新增的第 6 档。",
	},
	"admin/data_lifecycle_blobs.go": {
		Effect:   effectUnaffected,
		Evidence: "COALESCE(pg_column_size(rb.request_body), 0)",
		Note:     "读的是 pg_column_size 体量/待清理行数与 DDL 式 VACUUM，属体量与生命周期面；bodies 腿 LEFT JOIN + COALESCE 兜 0，读的是「存量多大、清理能省多少」而非流量本身。",
	},
	"admin/no_topic_session.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb\n\t\t  ON rb.request_id = rl.request_id",
		Note:     "主体读 710 视图（轮次行照常出、preview 字段可用），bodies 腿无 session 兜底 ⇒ 停写后新会话 response_body 全为 NULL/''。基于 body 的无话题判定与摘要内容静默为空，行仍在 ⇒ 第 6 档而非 silently_empty。",
	},
	"admin/telemetry.go": {
		Effect:   effectUnaffected,
		Evidence: "if requestLogsWriteEnabled() {",
		Note:     "本文件是写路径：读 request_logs_hot（upsertRequestLogBodies 的 ts 回填）与 bodies 镜像全部包在 if requestLogsWriteEnabled() 门内（:391、:454），停写后这段根本不执行 ⇒ 读点不发生。",
	},
	"cmd/tools/backfill_session_bodies/main.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs\n\t\tWHERE gw_session_id = $1 AND ($2 = '' OR tenant_id = $2)",
		Note:     "一次性 backfill 工具：Step1 按 gw_session_id 查母表取 turn 元数据，Step2 查 bodies 派生 session_bodies。停写后新会话在 v1 侧无行 ⇒ turnRows 空、派生不出 delta，log.Fatalf 不触发，工具静默「跑完但零产出」。",
	},
	"domains/sessionforensics/export.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id",
		Note:     "forensicsExportMessagesSQL 主腿读 710 视图（元数据仍出 session 行），bodies 腿 COALESCE(rb.request_body,'{}') 无 session 兜底 ⇒ 停写后新会话导出包 body 全为 {}。另 ListRecentSessions（:416 裸母表）完全冻结——一处两态，取降级档并在此说明。",
	},
	"admin/credential_success_rate.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "(SELECT MIN(ts) FROM request_logs_with_current_month rl\n\t\t\t WHERE rl.credential_id = c.id",
		Note:     "MIN(ts) 与 rsr 子查询走 710 视图（能持续更新），但同文件 resetCredentialSuccessRateRows 对 request_logs_hot 执行 DELETE 清 10 分钟前失败行（:117）；停写后清理无新行可删、MIN(ts) 反映冻结面 ⇒ 凭据「近期成功率」长期显示旧样本。",
	},
	"admin/provider_models.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT COUNT(*) FROM request_logs rl WHERE %s",
		Note:     "providerLogs 列表：count 腿裸读母表（:443，err 时 total=0 静默），data 腿读 710 视图。停写后 count 冻结在停写前总数、data 列表只剩历史，页数/总数与实际返回行自相矛盾且无错误提示。",
	},
	"admin/tenants.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs) -- sqlreadguard:allow R36-A1 漏热尾根修的双腿之母表腿",
		Note:     "7 天 credits/cost 聚合（:764-770）显式内联 hot UNION ALL 母表裸双腿绕开视图（注释：视图带租户过滤 COUNT 30s 超时）。停写后该并集完全冻结 ⇒ 租户账单恒为停写前 7 天旧值，接口 200、数值齐全、无告警。",
	},
	"admin/usage_trend_series.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_with_current_month_without_customer_id r",
		Note:     "detail 档读 ..._without_customer_id —— 这是**纯 v1 中间层视图**（hot UNION ALL 母表，无 session 臂），停写后完全冻结；provider 档读 request_stats_minute 同样不再增长 ⇒ 两条腿都恒为旧值。",
	},
	"domains/streaming/model_alternatives.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT canonical_model, COUNT(*) AS cnt\n    FROM request_logs_hot\n    WHERE ts > now() - interval '7 days'",
		Note:     "失败兜底推荐的 usage_7d CTE 读 hot 7 天窗（注释 229-242 明说为绕开视图改读 hot）。停写后该集合冻结为停写前 7 天的 popular 分层，COALESCE(u.cnt,0)>0 不再反映真实近期热度，无错误。",
	},
	// ── batch3：2026-10-02 逐点评估（23 条）────────────────────────────────
	//
	// 本批的核心价值不是 23 条判定，而是它逼出了 §9.14 的族分类器新维度：
	// migration 710 在 session 臂补了 30 列 NULL，视图「行级可用」≠「谓词级可用」。
	// 本批有 5 个文件因此被分到 familyViewNullPadded，其中 3 个的 unaffected
	// 判定在 §9.14.4 被改成 silently_empty / silently_degraded_content。
	"admin/auto_route.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_with_current_month_without_customer_id",
		Note:     "handleDecisions(:140) 刻意读 v1-only 中间层视图（7 天窗口），停写后继续吐停写前的旧决策。handleAudit(:534) 走 routing_analytics_source，而 bg/materialized_view_refresher.go:290,306 仍在每轮 REFRESH 这两个 MV ⇒ 不是「MV 停用导致冻结减轻」，而是**持续重算同一份冻结数据**，数值会随窗口排空而衰减（不是恒定常数）。",
	},
	"admin/diagnostics_credential.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND ts > now() - ($2 || ' minutes')::interval",
		Note:     "凭据诊断「最近失败」段查 request_logs_hot 的分钟级窗口，零行时 for 循环不执行，d.RecentFailuresCount = len(...) 变 0（:171），analyzeDiag 照常产出分析与恢复建议，接口 200、字段齐全。:166 注释已自认「RecentFailuresCount 会被低估成 0」。",
	},
	"admin/request_trace.go": {
		Effect:   effectErrorsOut,
		Evidence: "SELECT request_status, trace_events, ts FROM request_logs_hot WHERE request_id = $1",
		Note:     "按 request_id 点查，新请求在两张 v1 表都无行 ⇒ pgx.ErrNoRows ⇒ traceStateMissing ⇒ handleTrace(:133) 返 404 not_found，失败可见。次要腿 fetchRequestSummary(:311) 零行返回半填充结构不报错，但上游已先 404。",
	},
	"admin/swim_lane_init.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot rl",
		Note:     "泳道初始化读最近 N 小时，零行时循环不执行，返回 nil 切片 + 全零 stats + rows.Err()==nil，HandleSwimLaneInit(:74) 照样 json.Encode 200 ⇒ 前端拿到「一条请求都没有」的泳道。",
	},
	"bg/auto_route_settle_worker.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "LEFT JOIN request_logs_hot rl",
		Note:     "结算 outcome 腿(:392) 与 cohort 基线(:298)全读 request_logs_hot。停写后 p.success == nil ⇒ 每条待结算选择超过 settleAbandonAfter 就被计为 abandoned 而非 settled，而 loadTaskBaselines 返回空 map 且 err==nil，奖励函数继续用空/陈旧基线打分——整条链路无任何错误信号。",
	},
	"bg/ledger_reconciliation.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot",
		// 2026-10-02 口径保留：真实行为比「冻结」更坏，方向写在 Note 里。
		Note: "usageCreditSQL(:261) 是 request_logs_hot 与 credit_ledger_hot 的 FULL OUTER JOIN，" +
			"而 S4 开关**只门控 request_logs 族、不门控 credit_ledger**。停写后 usage 腿归零、ledger 腿继续增长 " +
			"⇒ 每笔新 consume 都变成 charged=0 vs debited>0 的**假 mismatch**，每轮最多 200 条灌进 " +
			"maas_reconciliation_findings，不报错。归档为 silently_frozen 是按「无错误信号的持续判定」；" +
			"语义上它不是冻结而是**误报洪水**，方向已写明以免后来者误读。",
	},
	"bg/today_success_probe.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot rl",
		Note:     "used CTE(:147) 取 24h 内成功流量，窗口排空后返回 0 行 ⇒ 不再产出任何 (credential, model) 对 ⇒ 自愈探针永久停摆，而调用侧(:133)照打 slog.Info(\"today success probe queued\", \"pairs\", 0)，日志看起来一切正常。",
	},
	"cmd/gateway/output_compliance_control.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs\n\t\t\tWHERE gw_session_id = sd.gw_session_id",
		Note:     "lookupOwners(:74) 用 LATERAL 从 request_logs 取 api_key_owner_user 作 callerOwner（该列是 session 臂补位 NULL 的 30 列之一）。停写后该腿恒无行 ⇒ callerOwner 恒为 \"\"，dataOwner 仍来自 session_dim 有值 ⇒ owner 规则判定「caller≠data」⇒ **所有合规输出被静默全量脱敏**。方向保守不泄漏，但功能整体失效且无错误。",
	},
	"discovery/discovery.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT 1 FROM request_logs rl",
		Note: "读端后果是那条 NOT EXISTS 守卫「查不到就判过期」。**该写入缺陷已于 §9.12 修复**" +
			"（staleExpiryMayRun：证据源停写时不下架），本条记录的是修复前/未开 ENABLE_CMB_EXPIRE 时的形状。" +
			"停写后守卫恒不命中 ⇒ 仍在被成功调用的 model 也被 UPDATE model_offers SET available = FALSE。" +
			"注意它是**破坏性写**且 RowsAffected>0 反而打 Info「expired stale models」。",
	},
	"domains/providerprofile/adapters.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot",
		Note:     "供应商画像 4 个查询全部 clamp 到 8h hot 保留期(:122)。停写后 COUNT(*) 归零而 err==nil ⇒ total_requests=0、error_count=0、AVG 为 NULL，BucketSuccessRates(:241) 返回空切片——「0 请求 0 错误」会被健康度/推荐逻辑读成「无异常」。（控制面轴另判 live：驱动凭据自动禁用。）",
	},
	"internal/trace/trace.go": {
		Effect:   effectErrorsOut,
		Evidence: "SELECT trace_events FROM (",
		Note:     "写路径整段已在门控内：FlushToPG(:473) 在 !settings.RequestLogsWriteEnabled() 时丢弃 trace 并 DEL Redis key 后 return nil，其下的 UPDATE request_logs_hot(:496) 停写期间根本不执行。唯一未门控的是读腿 LoadFromPG(:601)，停写后新 request_id 返 nil,nil ⇒ 上游 404，失败可见。",
	},
	"admin/live_stream_sse.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "行级完全可用：replay(:2423) 的 WHERE 只用 ts/tenant，session 臂照常供行，request_status 的 CASE 与 credential_id 都有真值，终态 overlay(:2546) 照常纠正。但 client_model 是补位 NULL ⇒ 泳道模型名静默变空并掉进 InferVendorFromModel 兜底。属第 6 档。",
	},
	"admin/session_analytics_breakdown.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "queryModelBreakdown(:266) 照常工作（outbound_model/cost/latency 在 session 臂有真值）；但 provider_id 是补位 NULL ⇒ queryProviderBreakdown(:308) 的 COALESCE(rl.provider_id::text,'unknown') 把全部新流量塌进单个 unknown 桶，且 buildWhereClause(:710) 的 AND rl.provider_id = $n 过滤器静默返 0 行、端点仍 200 + []。",
	},
	"admin/session_summary_v2.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "正文腿 queryTurnsForSummary 读 session 族不受影响；只有主路径 0 轮时才走的 fallback(:208) 读 v1 视图且带 MirrorDriftClassSQL = 'genuine_loss'，停写后返 0 行 ⇒ :212 return nil, fmt.Errorf(\"no turns found\") ⇒ HTTP 500。影响面是 V2 shadow-write 关闭的那部分会话（注释记近 3 天触发比例 10.87%），500 是可见失败，可接受。",
	},
	"admin/top_problems.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND rl.client_model IS NOT NULL AND rl.client_model != ''",
		Note:     "credential 榜(:194) 照常工作（credential_id 在 session 臂有真值）；但模型榜(:238) 靠 client_model 过滤，而该列是 session 臂补位 NULL 之一 ⇒ 新流量一条都进不来 ⇒ items := make([]topProblemsItem, 0, limit)(:209) 返回 200 + 空数组，「问题模型 Top N」永久空白且无任何提示。",
	},
	"bg/shared_pick.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "Priority 1「最常用 client_model」(:84) 带 AND client_model IS NOT NULL，session 臂该列恒 NULL ⇒ Scan 报 no rows ⇒ 代码 if err == nil && topModel != \"\" 不成立，**静默**降级到 Priority 2 featured、再降 random_fallback/empty。探针目标模型的选择依据从「真实最常用」悄悄变成「随便挑」，日志与返回值都不含任何降级标记。",
	},
	"domains/attachments/handler.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT attachments::text FROM request_logs_with_current_month WHERE request_id = $1",
		Note:     "710 视图对 session 臂投影 NULL::jsonb AS attachments（session_turns 只有 request_attachments、无 attachments）⇒ 新请求视图**照样返回行**，但 Scan(&raw []byte) 遇 NULL 报错 ⇒ :171 把 err 当成「无附件」⇒ 返 200 + attachments: []。附件数据其实还在 request_attachments 表里（domains/attachments/repository.go:183 已有读法），丢的只是这条 JSONB 读腿——属可修的读迁移，不需要数据抢救。",
	},
	"admin/compression_sessions.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "AND rb2.outbound_body IS NOT NULL",
		Note:     "列表行仍返回（compression_strategy 在 session 臂有真值），但 LATERAL 的 rb2.outbound_body IS NOT NULL(:147) 停写后恒假 ⇒ estimated_original_msgs 对每行都是 COALESCE(NULL,0)=0，且 outbound_msg_count / outbound_token_est 是补位 NULL ⇒ 200 OK、字段齐全、全零，**压缩效果的度量整体失去意义**。",
	},
	"admin/logs_summary.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COALESCE(rb.request_body::text, '') AS request_body",
		Note:     "bodies LEFT JOIN 停写后恒空，request_body/response_body 恒为 ''，而 request_preview/response_preview 在 session 臂**有**真值 ⇒ 送给 LLM 的会话摘要是「有预览、没正文」的截断输入。本文件 :231-233 注释自己写明「截断的输入会生成『看起来正常』的错摘要，比失败更难发现」，而这里既不报错也不留痕。",
	},
	"admin/session_sanitize_matches.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "SELECT COALESCE(rb.outbound_body, rb.request_body)",
		Note:     "loadOutboundBodySnippet(:224) 用 `_ =` 丢弃 err、body 为 nil 就返 \"\"，脱敏匹配视图里对应值经 maskSensitiveValue(:246) 显示为「—」⇒ 运维看到的是「该字段无敏感内容」而非「正文取不到了」。次要影响：lookupSessionTenant 的 v1 回落腿(:210)退化到 \"\" 时非超管拿到 403。",
	},
	"bg/passive_probe_listener.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "主腿照常工作——session 臂有 success/error_kind/outbound_model/request_status 真值，且它排除的探针流量本来就不在 session 臂里。硬失败的是 bodies 腿：MAX(COALESCE(rb.response_body::text,''))(:180) 停写后恒为 ''，passive_probe_state.last_response_body_preview 被静默清空，**故障诊断失去最后一段现场证据**。",
	},
	"db/db.go": {
		Effect:   effectUnaffected,
		Evidence: "WHERE to_regclass('public.request_logs') IS NOT NULL",
		Note:     "已核实无流量读：全部命中是结构面——ensureRequestLogSchema 的 information_schema/pg_indexes catalog 短路(:2008+)、ensureRequestLogsCurrentMonthView 的视图幂等重建(:227)、:7197/:7231 的 pg_class relname 名单（ALTER TABLE SET storage / ANALYZE 分区）。停写改变行数但不改变表结构与生命周期语义。（控制面轴同样判 unaffected：产出的是 DDL，不是决策。）",
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "JOIN request_logs_bodies_with_current_month rb",
		Note:     "pgRequestLogsSource 是**默认** MessageSource（summarizer.go:127 NewSummarizer 直接 &pgRequestLogsSource{}），bodies 无 session 兜底 ⇒ 停写后 JOIN 恒 0 行 ⇒ err 被 systemPromptPrefix(:52) 吞掉返回 \"\" ⇒ 会话总结照常生成、200、summary 字段齐全，只是永远缺系统提示词前缀。仅当 SetMessageSource 换成 v2SessionBodiesSource（读 session_bodies_unified）时才免疫。",
	},
	// ── batch4：2026-10-02 逐点评估（22 条）────────────────────────────────
	"admin/auto_route_correlations.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_with_current_month_without_request_class_due_at",
		Note:     "5 张相关表（by_model/by_strategy/by_task_type/by_model_task/verdict）全部只读 v1-only 中间层视图（无 session 臂）。错误路径全走 writeAutoRouteInternalErr 500，但空结果不报错 ⇒ 停写后这 7 天窗口持续吐停写前的陈旧成功率/延迟/成本，7 天窗口滚过停写点后 5 张表变空数组 + 200，自动路由相关性与黑名单决策会静默基于陈旧样本。",
	},
	"admin/probe_history.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs",
		Note:     "request_log_failures CTE（6h 窗口、request_status='failure'）是 /routing/recent-model-failures 三个 UNION 臂之一，err 路径 500 但 0 行不报错 ⇒ 停写 6h 后该臂归零，接口仍 200 且 sources.request_logs 字段齐全为 0，看板只剩 active/passive 探针数，真实流量失败从「模型发现」失败徽标里静默消失（单臂空，不是整体空）。",
	},
	"admin/routing.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot rl",
		Note:     "popularModelsHotSQL（rl.ts >= $1 = now-7d）是「凭据路由模型」picker 的 usage 源，计数喂 addHot(..., Source:\"usage\", Count)。SQL 错误只 slog.Warn、rows 迭代中止也只 Warn，无错误通道 ⇒ 停写后 7 天内计数冻结为停写前常数，7 天后该源静默退榜。",
	},
	"admin/work_types.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT COALESCE(work_type, 'unknown'), COUNT(*)",
		Note:     "4 个 24h 窗口读点（:289/:336/:376/:439，全部 FROM request_logs_hot）喂 by_work_type / by_l1_task / total_auto / total_specified。三个聚合读用 if err == nil 包住（查不到不算错），总数读是 `_ = h.db.QueryRow(...).Scan(...)` 吞错 ⇒ 停写 24h 后 by_work_type/by_l1_task 静默空 map、两个 total 静默 0，接口仍 200 且字段齐全。",
	},
	"bg/credential_recovery.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT 1 FROM request_logs_hot rl",
		Note:     "lookbackCandidateSQL 的双 EXISTS（request_logs_hot ∪ request_logs，36h 成功窗口）是「36h 成功回看恢复」的唯一证据源，scanLookbackRecoveries 里 `if len(candidates) == 0 { return }` 无日志无指标 ⇒ 停写 36h 后候选集静默为空，降级凭据不再被「有成功流量」证据自动拉回 available，只能等探针自身恢复，监控上看不出扫描在空跑。**该文件同时是 §9.10 的写授权缺陷**（陈旧证据持续放行恢复写入），控制面轴判 live 且更严重。",
	},
	"bg/lite_retention_worker.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT rowid FROM request_logs WHERE ts < ? LIMIT ?",
		Note:     "全文件没有对 v1 的流量读点——唯一触到 request_logs 的语句是 SQLite 行级保留期的 DELETE FROM request_logs WHERE rowid IN (...)，且是 lite 模式本地库、与 PG 侧 S4 门控无关；它读的是生命周期（待清理行），停写既不让它报错也不改变它的行为。",
	},
	"cmd/compression-bench/main.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs",
		Note:     "离线 bench CLI 从 FROM request_logs（ts >= NOW() - INTERVAL '1 day' * $1，默认 7 天）取历史行跑 SessionCompressor 基准，样本为空也不 Fatal 只 log.Printf ⇒ 停写后样本集静默冻结为停写前那批流量、基准结论悄悄变成「S4 之前」的口径，窗口完全滚过后打成 0 行仍照常输出空聚合。",
	},
	"cmd/gateway/waterfall_by_request.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_hot",
		Note:     "单请求 waterfall 落库回查按 request_id = $1 命中 request_logs_hot，pgx.ErrNoRows 被翻译成 (_, false, nil)，调用方 handleDispatchWaterfallByRequest 直接 http.Error(404, ...) ⇒ 内存环之外的请求立刻 404，灰度即可见（可接受失败模式）。",
	},
	"domains/analysis/optimizer.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SUM(COALESCE(cache_read_tokens,0)) FROM request_logs WHERE gw_session_id = ss.session_key",
		Note:     "三个相关子查询（cache_read_tokens 求和、compression_strategy 非空计数、outbound_token_est 求和）全部直读裸 request_logs 按 session_key 聚合，外面套 COALESCE(..., 0)，会话统计其余字段来自仍在长的 session_summaries ⇒ 新会话的优化建议里这三项静默为 0，而 request_count/成本照常有值，detect() 规则（如「存在压缩空间」）静默失活、save() 照常写库。",
	},
	"domains/streaming/anomaly_harvester.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot r",
		Note:     "backfillActualTokensSweep 用 UPDATE response_format_anomalies ... FROM request_logs_hot r 回填 actual_tokens，`n := ct.RowsAffected(); if n > 0` 才打日志 ⇒ 停写后 join 恒 0 行、无错无日志，新异常行的 actual_tokens 永远停在 NULL，response_format 异常的真实 token 口径静默退化为 estimated。",
	},
	"storage/sqlite/request_log_store.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs`",
		Note:     "这是 lite 模式自己的 SQLite request_logs（9 列轻量形态，body 只落 has_body 标记），NewRequestLogStore 在 StorageModeLite 下才被 factory 选中（full 模式走 newPgRequestLogStore），与 PG 侧 S4 门控不是同一条链；读点是 GetRequest/ListRequests，空结果走 storage.ErrNotFound 或非 nil 空切片且错误正常上抛，形态不因停写而变。",
	},
	"admin/model_routing_diagnostic.go": {
		// 2026-10-02 **自我更正**：batch4 判 unaffected，被 null-padded 族门判红。核实 :95-99
		// WHERE 确实作用在 request_logs_with_current_month 上，且是三分支 OR：
		//   lower(COALESCE(NULLIF(outbound_model,''),''))   = lower($1)   ← session 臂有真值
		//   lower(COALESCE(NULLIF(client_model,''),''))     = lower($1)   ← 补位 NULL，此臂恒不命中
		//   lower(COALESCE(NULLIF(canonical_model,''),''))  = lower($1)
		// ⇒ 不是全空（另两臂仍在），但**按客户端名查模型**这条路在停写后对未映射模型失效。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month",
		Note:     "request_stats CTE 按 24h 窗口统计 requests_24h/success_rate/p95/timeout/quota，外层 LEFT JOIN request_stats 且 COALESCE(...,0)。⚠️ 但 provider_id 是 session 臂补位 NULL 之一（§9.14），该 CTE 若按 provider_id 分组会塌进 unknown 桶——本条判 unaffected 的前提是它不按补位列分组，读端族门会复核这一点。",
	},
	"admin/session_analytics_timeseries.go": {
		// 2026-10-02 **自我更正**：batch4 判 unaffected，被 null-padded 族门判红。核实 :62
		// `AND %s.provider_id::text = ANY($%d)` 的 alias 就是 710 视图别名。
		// provider_id 在 session 臂恒 NULL ⇒ `NULL::text = ANY(...)` 求值为 NULL（非 true）
		// ⇒ 停写后**按 provider 过滤的时间线恒返回空**，而不过滤的路径照常有数据，
		// 同一个面板里两种过滤给出矛盾的空/非空，无任何错误。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "activity/cost/latency 三个读点（:193/:269/:340）全部读 710 视图，聚合列 request_status、cost_usd、prompt/completion tokens、cache_read/write_tokens、latency_ms、stream_first_chunk_ms 在 session 臂都由 t.* 真实投影（request_status 由 success/status_code 表达式算出）⇒ 停写后时间线/成本/延迟序列照常增长，错误路径仅在真 SQL 错误时 500。",
	},
	"admin/session_timeline_query.go": {
		Effect:   effectUnaffected,
		Evidence: "WHERE gw_session_id = $1`",
		Note:     "已按注释(:23-30)刻意改读 710 视图而非物理表，视图体 = session_turns_hot ∪ session_turns ∪（v1 冻结分支反连接）⇒ 停写后新会话轮次由 session 分支继续供数，镜像链启用之前的历史窗口仍由 v1 分支兜住；无行时返回 nil 由调用方各自还原成 null/[]，两种形态都不是新增的静默空。",
	},
	"admin/usage.go": {
		// 2026-10-02 **自我更正**：batch4 判 unaffected，被 null-padded 族门判红。判对了，但
		// **只对一半**：本文件是「真触发 + 假触发混在一起」的样本，正好说明文件级
		// 机械判定为何只能保守近似。
		//   真：:806-810 `OR (NOT success AND COALESCE(failure_stage,'') = '' AND
		//       provider_id IS NOT NULL …)` 位于 `FROM request_logs_with_current_month rl2`
		//       的子查询内 ⇒ 停写后新失败的 provider_id 恒 NULL，该分支对新增流量永不命中，
		//       失败分桶计数静默漏计。
		//   假：:334-366 的 ak.owner_user / app.code AS application_code、:456-583 的
		//       providers.provider_id、:767/:865/:1001 的 api_keys.id —— 这些列**同名但属于
		//       别的表**（api_keys / applications / providers / usage_ledger），那些表没有补位。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COUNT(*) FILTER (WHERE success) AS success_count,",
		Note:     "三处 710 读点（:810/:817 的 key 详情、:1107 的 usageKeyTraffic 5 分钟分桶）靠 session 臂继续变化。但吞错形态确实存在：`_ = h.db.QueryRow(...).Scan(&gatewayRejected, ...)` 把 810 那条整体吞掉，失败时三个字段静默 0，而同一响应里 total_requests/cost/success_rate 走的是另一个仍在长的 usage_ledger_with_current_month ⇒ 这是「静默矛盾」的高危形状（分账本不同源），只是 S4 本身不触发它。",
	},
	"bg/stats_minute_rollup.go": {
		// 2026-10-02 **自我更正**：batch4 判 unaffected，被 null-padded 族门判红——判对了。
		// 核实 :205/:281 `ON CONFLICT (bucket, tenant_id, provider_id, canonical_id)`：
		// provider_id / client_profile / client_model 都在**冲突键或分组维度**里，且取值来自
		// `COALESCE(r.provider_id, 0)`、`COALESCE(NULLIF(r.client_profile,''), '__unknown__')`。
		// 这三列在 session 臂恒 NULL ⇒ 停写后新流量全部落进 provider_id=0 / __unknown__ 桶，
		// 维度表与事实表**双双**归到假桶。行照常写入、Exec 正常返回，所以没有任何错误信号。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COUNT(*) FILTER (WHERE r.request_status = 'success')::bigint,",
		Note:     "rollupMain/rollupDims 三处读 710，窗口由 request_stats_rollup_cursor.last_ts 游标推进，session 臂持续产新 ts 行 ⇒ 分钟汇总照常滚动；Exec 错误会 return err 上抛。⚠️ 但 session 臂有两列恒 NULL（credits_rate_multiplier、client_ip）⇒ client_ip 维度会塌成 __unknown__、credits 估算按倍率 1.0 计——这是**值劣化**，见 Note 与 §9.15 的五种方向。",
	},
	"internal/collector/gateway_adapters.go": {
		Effect:   effectUnaffected,
		Evidence: "COALESCE(COUNT(*), 0)::float8 / 300.0 AS tps,",
		Note:     "PgTrafficReader.Snapshot 三处读 710（5 分钟 TPS/p50/p99/success、Top-20 模型、30 秒 in-flight），session 臂继续供数；吞错形态是 `if err != nil { return TrafficSnapshot{}, nil }` 与 `_ = r.Pool.QueryRow(...).Scan(&inFlight)`（失败即整份空快照 / 并发数 0，无错误），停写本身不触发这条路径。",
	},
	"admin/compression_stats.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id",
		Note:     "4 个读点里 3 个把 bodies 腿挂在 LEFT JOIN request_logs_bodies_with_current_month 上（bodies 无 session 兜底）⇒ 停写后 710 的 rl 行照常增长，但 with_outbound / compressed / estimated_original_tokens / summary_mode_rows 全部静默归 0，压缩率与省 token 数变成 0%、而 total 与 strategy 分布仍有数，接口 200。只有纯 token_band 那个读点（无 bodies）不退化。",
	},
	"admin/memora_handlers.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs `+where+`",
		Note:     "两条后果不同的腿：handleSessionMessages(:795-796) 读 710 + LEFT JOIN request_logs_bodies_with_current_month，bodies 腿硬失败 ⇒ 新会话 messages 仍返回（session 臂供行）但 request_body/response_body 是 COALESCE(...::text,'') 的空串、message_count 与 token/cost 汇总照常有值，200；handleMemoraContext(:615-616) 直读裸 request_logs + sessionLogsWhere，新 task 的 requestCount == 0 ⇒ 404 task not found（那条是 errors_out）。按「同一文件取更危险档」取 degraded_content，两条方向都写明。",
	},
	"admin/session_title.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COALESCE(rb.request_body::text, '') AS request_body,",
		Note:     "已核实 sessionLogsWhere(admin/session_scope.go:39) 主谓词是 gw_task_id = $1 AND ts > …，不是会话头；gw_task_id 由 session 臂的 details 层(d.gw_task_id)提供，故 loadTaskLogsForTitle 的行不会消失——但 bodies 腿硬失败使 request_body/response_body 变空串，buildSummaryCorpus 只能靠 request_preview/response_preview 兜底 ⇒ 标题照常 200 生成但语料从全文降级为预览片段；只有语料短到 40 rune 以下才显式 400。",
	},
	"cmd/scenario_driver/main.go": {
		Effect:   effectErrorsOut,
		Evidence: "SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'",
		Note:     "它自己造流量（POST {gateway}/v1/chat/completions 跑 60/20/80 轮）再量 v1 增量，所以停写后必然测不到：bodies 增量与 request_logs_hot success=false 增量双双为 0，Passed=false + Message: \"delta_bodies=0, expected >= 50\"，属立刻暴露的硬失败（离线工具，不影响在线面）。",
	},
	"domains/hooks/observability/telemetry/client.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND gw_session_id LIKE 'gw\\_%'",
		// 2026-10-02：读端与控制面分开记。读端后果是会话身份退化为「每请求新建」；
		// 控制面后果（写授权/身份）更严重，见 request_logs_control_plane_dependency_test.go。
		Note: "FindRecentGatewaySession(:593-625) 读 request_logs_hot **无门控**，no rows 被吞成 (\"\", nil)。" +
			"⚠️ 量级更正：主路径是 Redis 的 LastSystemSessionIndex（TTL 5 分钟，不受门控），DB finder 只是兜底——" +
			"子代理初判「每个请求静默新开 gw_session_id」过重。准确失效条件两条：" +
			"① 无 Redis 部署时 main.go:968 的装配不执行，lastSystemSession 为 nil，DB finder 成唯一路径；" +
			"② Redis 索引 miss（TTL 到期/重启/device seed 不匹配）。" +
			"turn_no（lookupTurnNumber :3767）经 outbox request-completed 事件在门控外提交，同样冻结。",
	},
	"admin/data_lifecycle.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT DATE(ts) AS day, COUNT(DISTINCT request_id) AS compressed",
		// 2026-10-02 自我更正：本条初判 effectUnaffected（理由「生命周期/保留期/
		// 体量面只读存量」），被族门判红——判对了。逐行核实后：该文件除体量与
		// 保留期外，:226-241 的 7 天**增长趋势**里有一条 bodies 腿
		// （COUNT(DISTINCT request_id) ... outbound_body IS NOT NULL），读
		// request_logs_bodies_with_current_month，而 bodies 没有 session 臂。
		// ⇒ 停写后 requests 趋势（走 710 视图，session 臂继续供数）继续增长，
		// 而 compressed 趋势冻结，**两条线分叉**，且无任何错误信号。
		// 比「整个端点冻结」更隐蔽：页面照常 200，只有一条线停住。
		Note: "体量/保留期部分不受影响，但 7 天增长趋势的 bodies 腿会冻结，" +
			"与仍在增长的 requests 线分叉且无错误信号。",
	},
	"admin/credential_monitor.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "COALESCE((SELECT COUNT(*) FROM request_logs_with_current_month rl WHERE rl.credential_id = c.id), 0)",
		Note:     "按 credential_id 聚合，不是会话键。停写后 total_requests 冻结为停写前的常数。",
	},
	"bg/auto_index_refresher.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot rl",
		Note:     "从近期流量推导推荐索引。停写后仍会持续产出「推荐」，但依据的是冻结窗口——静默陈旧。",
	},
	"bg/integrity_fingerprint_drift.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "AND COALESCE(is_auto_request, false) = false",
		// 2026-10-02 **自我更正**：本条初判为 silently_empty，被族门判红——判对了。
		// 该文件读的是 710 视图，而视图含 session 臂（真库 24h 内 36.55% 的行
		// 来自 session_turns），停写后 business 流量（is_auto_request=false，
		// 正是本文件的口径）**仍由 session 臂供给**，所以它不会「查空」。
		// 真正会消失的是它本来就排除掉的探针流量（task_type 探针族 + auto=true），
		// 而那些行本来就不在它的口径内。
		// ⇒ 停写后它**继续工作**，只是输入构成变了（业务流量改由 session 臂来）。
		// 在本地开发库上实测该口径 24h 内命中 0 行（system_fingerprint 全空），
		// 所以「继续工作」这一点无法在本地证实，见下方 measurementCaveat。
		Note: "基线窗口 vs 当前窗口的指纹比对。读 710 视图 ⇒ 停写后 session 臂仍供业务流量，" +
			"不会查空；它排除的探针流量本来就不在口径内。安全相关检测器，" +
			"但按当前证据不构成「停写后静默失效」——需在生产库复核一次。",
	},
	"domains/hooks/goal/history_store.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "LEFT JOIN request_logs_bodies rb",
		// 作者逐行核实（2026-10-02）：该查询按 gw_session_id 取最近 N 轮的
		// (request_body, response_body) 对，重建对话全文供 goal 审计钩子使用。
		// 停写后新会话在这条路径上取不到任何行；:120 起
		// `out := make([]HistoryMessage, 0, len(pairs)*2)` 在零行时返回
		// **非 nil 的空切片且 err == nil**，调用方无从区分「没有历史」与
		// 「历史为空」。⇒ 审计钩子会把空对话当作事实记下来。
		Note: "按会话重建对话全文；零行返回空切片而非报错，" +
			"goal 审计钩子会把空对话当成真实历史记入。",
	},
	"domains/routeincident/store.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "AND rl.ts >= NOW() - INTERVAL '24 hours'",
		// 作者核实：本文件自带说明——「Insufficient data is explicit: the
		// store returns an empty slice and the API layer renders a banner」。
		// 即失败是**可见**的（前端横幅提示数据不足），不是静默给出一张
		// 看起来正常的空图。判 errors_out 而非 silently_empty。
		// 残余风险：横幅文案说「数据不足」，运维可能误读为「本来就没什么流量」，
		// 而真实原因是停写 —— 灰度时应同步改文案或加来源标注。
		Note: "24h 全量流量时间线，读 710 视图 ⇒ session 臂仍供数、不会落空；" +
			"但它要的是「全量流量」，而 session 臂不含探针流量 ⇒ 停写后少计探针部分。" +
			"API 层有「数据不足」横幅，但那只在真为空时出现，此处不出现。" +
			"残余风险：横幅文案无法提示「数字已停止更新」。",
	},
}

// measurementCaveat 适用于本文件里所有**幅度**数字，必须与结构性事实分开读。
//
// 结构性事实（处处成立，来自 pg_get_viewdef 的真库输出）：
//   request_logs_with_current_month = session_turns ∪ session_turns_hot
//                                     ∪ request_logs ∪ request_logs_hot
//   ⇒ 停写只冻结 v1 两条臂，session 两条臂继续增长；视图读者不会「查空」。
//
// 幅度数字（**仅本地开发库 llm_gateway，24h 窗口实测**，2026-10-02）：
//   视图 7,221 行 = session 臂 2,639（36.55%）+ v1 臂 4,582（63.45%）；
//   v1 独有的 4,580 行中，4,570 行是 task_type='probe_triggered'（探针流量），
//   10 行是 request_status='in_progress' 的非终态占位（按设计不镜像），
//   有会话头却真漏写的 = 0。
//
// 生产库的业务/探针流量配比可能不同，所以**「停写会少记 63.45%」不可直接当生产结论**；
// 可移植的是「少记的那部分里，约 99.8% 是探针流量与占位行」这个结构性事实，
// 以及它给出的那个决策问题：analytics 到底要不要统计探针流量（§8 第 3 项）。
//
// 另记一次自摆的乌龙：临时查询先报「有会话头却未镜像 = 10 行」，看着像新 P0；
// 逐行看才发现 10 行全是 in_progress + error_kind 为空，即 non_terminal 按设计排除桶，
// 真值仍为 0。那条查询没用 db.MirrorDriftClassSQL，是自造口径——**与审计 §5.5.6
// 「复用诊断口径的分类表达式前先确认标签方向」是同一个坑，当场又踩了一次。**
//
// fingerprintDriftVerdict 记录 bg/integrity_fingerprint_drift.go 的核实过程，
// 放在登记之外是因为 Go 的结构体字面量里放不下这种长注记。
//
// 它的 SQL 把 request_logs_with_current_month 切成 baseline 窗口（> currentDays
// 天前）与 current 窗口（最近 currentDays 天），joined CTE 带 `c.total >= $2`
// （minSamples）门槛。停写后 current 窗口逐日清空 ⇒ joined 恒空 ⇒ 返回 0 行。
// 调用方（:321 起）逐行累积 batch，**零行不告警、不报错、不写任何状态**。
//
// 而这个任务的功能是「发现上游静默更换 system_fingerprint」——静默在这里读作
// 「无漂移」，即**安全监测器在输入停止后报告健康**。这是本分类里后果最重的一档：
// 它不会让灰度失败，只会让灰度通过之后，一个安全信号永久消失且无人察觉。
//
// 另注其过滤 `is_auto_request = false` 且排除探针 task_type，所以它依赖的
// 正是**业务流量**，停写后必然归零，而不是「只剩探针行」。

// TestRequestLogsStopWriteClassificationProgress 每次都跑，负责两件事：
// ① 反向：分级表里不该有已退出读点清单的文件（否则「104 个已评估」本身陈旧）；
// ② 正向：把「已评估 N/104」打进日志，让缺口在常规测试输出里**看得见**。
//
// 它**不**在未评估时失败。理由：这是「把一件事做完」的进度条，不是「防止事情
// 变坏」的守卫；让它常红只会挡住所有人，却不会让那 98 个文件被评估。
// 硬条件（未评估必须为零）放在 build tag `s4audit` 下，见
// TestRequestLogsStopWriteNothingLeftUnclassified。
//
// 进度门把「正向检查」和「完成条件」拆开，是被自己的第一版实现逼出来的：
// 原写法的 HasNoUnclassified 只遍历分级表里已有的条目，于是「只登记 3 个」时
// 未分类集合恒为空、门恒绿——**它守的正是「没登记」这件事，却因为分母取错而
// 看不见没登记**。凡是「未完成项」类判据，分母必须取自**应当被完成的那份清单**。
func TestRequestLogsStopWriteClassificationProgress(t *testing.T) {
	for file := range requestLogsStopWriteClassification {
		if _, ok := requestLogsReadInventory[file]; !ok {
			t.Errorf("分级表里的 %s 已不在读点清单中 —— 该文件要么不再读 request_logs，"+
				"要么清单陈旧；两种情况都让「104 个已评估」这句话失真", file)
		}
	}

	var todo []string
	for file := range requestLogsReadInventory {
		c, ok := requestLogsStopWriteClassification[file]
		switch {
		case !ok, c.Effect == effectUnclassified:
			todo = append(todo, file)
		}
	}
	sort.Strings(todo)
	t.Logf("S4 停写逐点评估进度：%d/%d 已评估，未评估 %d 个 —— %s",
		len(requestLogsReadInventory)-len(todo), len(requestLogsReadInventory),
		len(todo), progressVerdict(len(todo)))
	if len(todo) > 0 {
		t.Logf("未评估清单：\n  %s", strings.Join(todo, "\n  "))
	}
}

func progressVerdict(todo int) string {
	if todo == 0 {
		return "已全部评估"
	}
	return "缺口仍在，S4 灰度前置条件未达成；填法见 " + classificationHowTo
}

// classificationHowTo 是填分级登记的操作说明。它被三处引用（进度日志、覆盖率门、
// 以及门失败时的提示），所以写成常量而不是复制三份——措辞一旦分叉，三个地方
// 会给出互相矛盾的做法。
const classificationHowTo = `打开该文件、找到读 request_logs 的位置，按「停写之后会发生什么」归入：

  errors_out              停写后必然查不到 → 报错/空。可接受的失败模式，灰度时立刻暴露
  silently_empty          结果集静默变空，接口仍 200、字段齐全、全零/空列表
  silently_frozen         结果静默冻结为停写前的常数，无任何错误信号
  unaffected_by_stop_write  读表结构/体量/生命周期，不读流量
  validator_dual_read     刻意做 v1↔session 对账
  silently_degraded_content  行还在，但某一列内容静默变空/降级（第 6 档，2026-10-02）

并把该处**逐字**的一段 SQL 片段填进 Evidence（门会核它是否真的在文件里）。

先看族再看后果：只读 710 视图的读点停写后**不会查空**（视图含 session 臂），
判 silently_empty 会被 TestStopWriteEffectAgreesWithSourceFamily 判红。

⚠️ 但「读 710 视图」本身**不**再禁止 silently_empty：migration 710 在 session 臂上
把 30 列补成了 NULL（client_model / provider_id / attachments / outbound_msg_count /
api_key_owner_user / gw_task_id / model_chosen …）。视图**照样返回行**，但只要
WHERE / GROUP BY / JOIN 用到这些列，就是「行级有、谓词级空」——按该列过滤恒 0 行。
族分类器会把这类文件分到 reads_view_with_null_padded_predicate 族。
在该族里判 unaffected 需要在 nullPaddedUnaffectedJustification 里具名论证。
不要按「读哪张表」或「函数名像不像会话查询」来分——审计 §5.5.6 记过：第一版把
gw_task_id 过滤的三处误判成会话内读，差了两档。`

// TestRequestLogsStopWriteClassificationEvidenceIsReal 钉住「证据必须存在」。
//
// 这是整个登记的承重点：没有它，104 条登记可以在一个下午凭印象写完，而门全绿。
// 变异验证：把任一 Evidence 改成文件里没有的片段即红。
func TestRequestLogsStopWriteClassificationEvidenceIsReal(t *testing.T) {
	root := repoRootFromCaller(t)
	for file, c := range requestLogsStopWriteClassification {
		if c.Effect == effectUnclassified {
			continue
		}
		if _, ok := stopWriteEffects[c.Effect]; !ok {
			t.Errorf("%s: 未知分级 %q（合法值：%s）", file, c.Effect, effectsKeyList())
			continue
		}
		if strings.TrimSpace(c.Evidence) == "" {
			t.Errorf("%s: 分级为 %s 但 Evidence 为空——分级必须带一段在该文件里逐字存在的证据", file, c.Effect)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v（登记里的文件必须存在）", file, err)
			continue
		}
		if !strings.Contains(string(raw), c.Evidence) {
			t.Errorf("%s: Evidence 在该文件中不存在：\n  %q\n"+
				"分级必须锚在真实存在的代码上，否则「已评估」无法与「凭印象」区分",
				file, c.Evidence)
		}
	}
}

// stopWriteSourceFamily 是**机械可判定**的那一维：直接看文件读的是哪一族表。
//
// 为什么要单独一维、且放在「后果」之前：后果**依赖**于读的是哪一族，而这个依赖
// 在本项目里被集体判错过一次。2026-10-02 的批量评估中，「只读 710 视图」的一批
// 文件被逐个判成 silently_empty（停写后查不到）——**系统性错误**。真库实测：
//
//	request_logs_with_current_month 的定义含四张基表：
//	  session_turns ∪ session_turns_hot ∪ request_logs ∪ request_logs_hot
//	24h 实测：视图 7,221 行 = session 臂 2,639（36.55%）+ v1 臂 4,582（63.45%）
//
// 停写只冻结 v1 那条臂，**session 那条臂继续增长**（镜像链不归 S4 管）。所以视图
// 读者停写后仍拿得到数、只是少计。判「空」会把一批实际还能用的读点误报成最危险档，
// 进而把真正该处置的基表读者淹没——这正是把子代理结论照单全收会发生的事。
//
// 四族的差别本质是**有无 session 侧兜底**：
//
//	familyBodies —— bodies 族只有 v1 一份，session 侧无等价物
//	               （session_bodies 只有增量、无 final_full 全量，见审计 §6）
//	               ⇒ 停写后硬失败，无退路。
//	familyView   —— 710 视图有 session 臂 ⇒ 静默少计。
//	familyBase   —— request_logs / request_logs_hot 为 v1 专有 ⇒ 完全停止增长。
//	familyBodiesMixed —— 一并读，但**其中 bodies 腿没有任何 session 兜底**。
//	               「一并读」不等于「部分退化」：主轮次腿可能照常工作，
//	               而正文腿彻底失效，表现为「拿得到轮次、拿不到正文」。
//	familyViewBase —— 视图腿（session 臂仍供数）+ 基表腿（停写即冻结）⇒ 部分退化。
const (
	familyBodies      = "reads_bodies_family"
	familyView        = "reads_710_view_only"
	familyBase        = "reads_base_tables_only"
	familyBodiesMixed = "reads_bodies_plus_other"
	familyViewBase    = "reads_view_and_base"
	// familyViewNullPadded：读 710 视图（行级有 session 臂），但**谓词/分组/连接**
	// 用到了 session 臂恒 NULL 的 30 列之一 ⇒ 行级有、谓词级空。
	familyViewNullPadded = "reads_view_with_null_padded_predicate"
)

var (
	gateStopWriteLineCommentRE  = regexp.MustCompile(`(?m)//[^\n]*`)
	gateStopWriteBlockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// 注释先剥是硬要求：本仓 40 条 grep 命中里 36 条是注释，不剥的话
	// 「读 710 视图」会被历史注释里的提及带偏。
	familyBodiesRE = regexp.MustCompile(`(?i)request_logs_bodies`)
	familyBaseRE   = regexp.MustCompile(`(?i)\bfrom\s+request_logs(_hot)?\b`)
	// 视图族**不能**靠名字模式判定：名字带 request_logs_with_ 的中间层视图
	// 可能仍是纯 v1。下面的 requestLogsViewsWithSessionArm 才是判据，
	// 且它由真库门 TestRequestLogsViewSessionArmPinIsCurrent 钉住。
	familyAnyViewRE = regexp.MustCompile(`(?i)\brequest_logs_(with_[a-z_]+|prev)\b`)
)

// requestLogsViewsWithSessionArm 是「停写后仍由 session 臂供数」的视图白名单。
//
// ⛔ 这份名单是**真库实测**得出的，不是从名字或迁移文件推的（2026-10-02）。
// 我最初用 `request_logs_with_[a-z_]+` 判定视图族，结果把两个中间层视图
// 一起算进了「停写后仍供数」——真库 pg_get_viewdef 显示它们是**纯 v1**：
//
//	request_logs_with_current_month                        HAS_SESSION_ARM
//	request_logs_with_current_month_without_customer_id    V1_ONLY   ← 我判错了
//	request_logs_with_current_month_without_request_class_due_at  V1_ONLY  ← 我判错了
//	request_logs_bodies_with_current_month                 V1_ONLY（bodies 族，本就无 session 臂）
//
// 名字带 `request_logs_with_` 的两个 `_without_*` 视图是 577/734 迁移为了
// 规避投影缺失而**故意**建成 v1-only 的中间层（见 audit §9.7）。把它们当视图族
// 会让 7 个生产文件的停写后果被系统性低估。
//
// 白名单必须是**白**名单而不是「凡 view 皆算」：新增一个 v1-only 视图时，
// 它默认落进 base 族（保守、正确方向），要改成视图族得显式加进来并说明理由。
var requestLogsViewsWithSessionArm = map[string]struct{}{
	"request_logs_with_current_month": {},
}

// sessionArmNullPaddedColumns 是 710 视图 session 臂上**恒为 NULL** 的列。
//
// 为什么必须单独记一张表（2026-10-02）：「710 视图含 session 臂 ⇒ 停写后不会
// 查空」这句话只在**行级**成立。migration 710 的 session 臂对 30 列做 NULL 补位
// （`710_request_logs_view_session_family_v2.sql`），其中包括 client_model、
// provider_id、attachments、outbound_msg_count、outbound_token_est、
// api_key_owner_user、gw_task_id、model_chosen、strategy_used……
//
// ⇒ 任何在 WHERE / GROUP BY / JOIN / COALESCE 判定里用到这几列的读点，会
// **行级有、谓词级空**：视图照常返回行，但按该列过滤的结果集恒为空。
// 这不是「查不到」，是「查得到但没有一行匹配」——旧族约束（视图族不得判
// silently_empty）在这里是**错的**，而且会拒绝正确的判定。
//
// 实证：domains/attachments/handler.go 读 `attachments::text`，session 臂该列
// 恒 NULL ⇒ Scan 报错 ⇒ 被当成「无附件」⇒ 200 + attachments: []。
// 附件数据其实还在 request_attachments 表里，丢的只是这条 JSONB 读腿。
var sessionArmNullPaddedColumns = map[string]struct{}{
	"affinity_hit": {}, "api_key_owner_user": {}, "api_key_prefix": {},
	"application_code": {}, "attachments": {}, "auto_profile": {},
	"client_model": {}, "client_profile": {}, "compression_reason": {},
	"due_at": {}, "gw_task_id": {}, "id": {}, "key_alias": {},
	"model_chosen": {}, "outbound_msg_count": {}, "outbound_msg_hashes": {},
	"outbound_token_est": {}, "owner_user": {}, "provider_id": {},
	"provider_model": {}, "quality_fix_actions": {}, "request_class": {},
	"request_type": {}, "strategy_used": {}, "stream_chunk_errors": {},
	"stream_chunks_sent": {}, "test_tab_indent": {}, "transform_rule_id": {},
	"virtual_ip": {}, "virtual_mac": {},
}

// TestSessionArmNullPaddedColumnsMatchMigration 钉住上面那张表与 migration 710
// 声明的一致，防止「视图加了列 / 迁移改了投影」之后表悄悄过期。
//
// 这张表过期 = 族分类器开始把「谓词级空」判成「行级有」= 门会拒绝正确判定。
// 所以它必须由**权威来源**（迁移文件）反向校验，而不是靠人记得更新。
func TestSessionArmNullPaddedColumnsMatchMigration(t *testing.T) {
	root := repoRootFromCaller(t)
	raw, err := os.ReadFile(filepath.Join(root,
		"sql/migrations/startup/710_request_logs_view_session_family_v2.sql"))
	if err != nil {
		t.Fatalf("读 migration 710 失败 %v", err)
	}
	declared := map[string]struct{}{}
	re := regexp.MustCompile(`NULL::[A-Za-z ]+ AS ([a-z_]+)`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		declared[m[1]] = struct{}{}
	}
	// test_tab_indent 是迁移里的探针列，保留在表内以便 mismatch 报告完整。
	if len(declared) == 0 {
		t.Fatal("migration 710 里没解析出任何 NULL 补位列——正则失效或迁移被重写，" +
			"族分类器会静默退化成「视图族一律行级可用」")
	}
	var missing, extra []string
	for c := range declared {
		if _, ok := sessionArmNullPaddedColumns[c]; !ok {
			missing = append(missing, c)
		}
	}
	for c := range sessionArmNullPaddedColumns {
		if _, ok := declared[c]; !ok {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("session 臂 NULL 补位表与 migration 710 不一致：\n"+
			"  迁移里有、表里没有：%v\n  表里有、迁移里没有：%v\n"+
			"后果：族分类器会把「谓词级空」误判成「行级有」，"+
			"从而拒绝正确的 silently_empty 判定。", missing, extra)
	}
}

// nullPaddedColWordRE 为某个 NULL 补位列构造词边界匹配。
//
// 用词边界而不是裸子串：`id` 是补位列，但 `provider_id` / `session_id` 里都含
// "id"，裸子串会把几乎每个文件都判成谓词级空，那条族约束就废了。
func nullPaddedColWordRE(col string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
}

// sourceFamilyOf 从文件源码机械判定它读哪一族。
func sourceFamilyOf(code string) string {
	code = gateStopWriteLineCommentRE.ReplaceAllString(code, " ")
	code = gateStopWriteBlockCommentRE.ReplaceAllString(code, " ")
	bo := familyBodiesRE.MatchString(code)
	vi, v1OnlyView := false, false
	for _, m := range familyAnyViewRE.FindAllString(code, -1) {
		if _, ok := requestLogsViewsWithSessionArm[strings.ToLower(m)]; ok {
			vi = true
		} else {
			// 引用了一个 v1-only 视图（如 ..._without_customer_id）：
			// 它没有 session 臂，停写后与读基表同命运 ⇒ 归 base 族。
			v1OnlyView = true
		}
	}
	// 读了 v1-only 视图的，即使 SQL 文本里没有裸 FROM request_logs，也是
	// 实质上的基表读者——这是「只看 from request_logs 就会漏掉」的一类。
	ba := familyBaseRE.MatchString(code) || v1OnlyView
	// 视图族的第二维：谓词级 NULL 补位（2026-10-02）。
	// 只在「确实读会话臂视图」且「代码里出现 NULL 补位列名」时成立——
	// 后者是个偏保守的近似（SELECT 出来与 WHERE 过滤同形），但方向安全：
	// 它只会**放开** silently_empty、**禁止** unaffected，不会把正确判定判错。
	np := false
	if vi {
		for col := range sessionArmNullPaddedColumns {
			if nullPaddedColWordRE(col).MatchString(code) {
				np = true
				break
			}
		}
	}
	switch {
	case vi && np && !ba:
		return familyViewNullPadded
	case bo && (vi || ba):
		return familyBodiesMixed
	case bo:
		return familyBodies
	case vi && ba:
		return familyViewBase
	case vi:
		return familyView
	case ba:
		return familyBase
	default:
		return "undetermined"
	}
}

// TestRequestLogsStopWriteSourceFamilyCoversInventory 产出并钉住那张四分表。
//
// 它不做判断，只做**机械分类**——正因如此它可以自己给自己当判据：它测的是
// 「按代码测出的族」与「按代码测出的族」相等，没有解释空间，所以不会恒绿
// （除非分类逻辑本身坏了）。价值在于把「104 个文件」变成 4 个可行动的队列。
func TestRequestLogsStopWriteSourceFamilyCoversInventory(t *testing.T) {
	root := repoRootFromCaller(t)
	counts := map[string]int{}
	var undetermined []string
	for file := range requestLogsReadInventory {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fam := sourceFamilyOf(string(raw))
		if fam == "undetermined" {
			undetermined = append(undetermined, file)
			continue
		}
		counts[fam]++
	}
	if len(undetermined) > 0 {
		sort.Strings(undetermined)
		t.Fatalf("以下文件的读法未归入任何一族（需人看一眼，通常是 SQL 在共享构造函数里）：%v",
			undetermined)
	}
	total := 0
	for _, k := range []string{familyBodies, familyBodiesMixed, familyView, familyBase,
		familyViewBase, familyViewNullPadded} {
		total += counts[k]
	}
	if total != len(requestLogsReadInventory) {
		t.Errorf("六族合计 %d ≠ 读点清单 %d —— 有文件被重复计数或漏计", total, len(requestLogsReadInventory))
	}
	t.Logf("S4 停写影响面（按读表族，机械判定）：\n"+
		"  bodies_only=%d                正文无兜底 ⇒ 硬失败\n"+
		"  bodies_plus_other=%d          正文腿硬失败（轮次腿可能照常）\n"+
		"  view_only=%d                  session 臂仍供数 ⇒ 静默少计\n"+
		"  view_with_null_padded=%d      **行级有、谓词级空**（migration 710 补位 30 列）\n"+
		"  base_only=%d                  完全停止增长\n"+
		"  view_and_base=%d              部分退化",
		counts[familyBodies], counts[familyBodiesMixed], counts[familyView],
		counts[familyViewNullPadded], counts[familyBase], counts[familyViewBase])
}

// TestStopWriteEffectAgreesWithSourceFamily 拦住本项目已经犯过一次的错：
// **710 视图读者不可能「静默变空」**——停写后它们仍由 session 臂供数。
//
// 判据打成关系（族 × 档位的合法组合）而不是硬编码清单：新增档位时必须同时
// 说明它与四族的关系，规则跟着走；否则新档位会默认放行所有组合。
func TestStopWriteEffectAgreesWithSourceFamily(t *testing.T) {
	// 「静默变空」要求读点**完全**没有 session 侧供给。只读 710 视图不满足
	// 这个前提——真库实测该视图 24h 内 36.55% 的行来自 session_turns。
	// 「静默变空」要求读点**完全**没有 session 侧供给。
	//
	// ⚠️ 2026-10-02 收窄：这条原本一刀切套在 familyView 上，**是错的**。
	// migration 710 对 session 臂补了 30 列 NULL（client_model / provider_id /
	// attachments / outbound_msg_count / api_key_owner_user …），凡在
	// WHERE / GROUP BY / JOIN 用到这些列的读点，会「行级有、谓词级空」——
	// 视图照常返回行，但按该列过滤恒 0 行。这类文件判 silently_empty 是**正确**的，
	// 旧规则会拒绝它。现在它们被分到 familyViewNullPadded，只约束 unaffected。
	forbidden := map[string][]string{
		familyView: {effectSilentlyEmpty},
		// 谓词级空族：结果集可以真的为空（合法）。
		//
		// 「不受影响」**不直接判红、而是要求具名论证**——因为 sourceFamilyOf 的
		// 这一维是**保守近似**：它只检查代码里是否出现补位列名，无法区分
		// 「用在 WHERE/GROUP BY」（真触发）与「只出现在投影里 / 路径字符串 /
		// Go 结构体字段里」（过度触发）。实测 5 个被标记文件里 3 真 2 假。
		// 机械地一刀切会再次误伤正确代码（同 §9.13.2 的教训），
		// 所以这里要求 nullPaddedUnaffectedJustification 具名登记。
	}
	root := repoRootFromCaller(t)
	for file, c := range requestLogsStopWriteClassification {
		if c.Effect == effectUnclassified {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		fam := sourceFamilyOf(string(raw))
		if fam == familyViewNullPadded && c.Effect == effectUnaffected {
			if reason, ok := nullPaddedUnaffectedJustification[file]; !ok || strings.TrimSpace(reason) == "" {
				t.Errorf("%s（族=%s）判为「停写不受影响」，但它在 session 臂 NULL 补位的列上出现。\n"+
					"该族的自动判定是保守近似（只看列名出现，不区分谓词与投影），所以这里不直接判红，\n"+
					"但必须在 nullPaddedUnaffectedJustification 里具名论证：若该列只出现在投影/路径/结构体\n"+
					"字段里，或已被同表达式的非补位列 COALESCE 兜住，写清机制即可放行。", file, fam)
			}
		}
		for _, bad := range forbidden[fam] {
			if c.Effect == bad {
				t.Errorf("%s（族=%s）被判 %q，但它只读 710 视图且**未**触及任何 session 臂\n"+
					"NULL 补位列 ⇒ 停写后 session 臂仍按行供数（真库实测 24h 内 36.55%% 的视图行\n"+
					"来自 session_turns），不可能静默变空。应为 %q（静默少计）或 %q（报错）。\n"+
					"若本文件其实用到了补位列做谓词，它应被分到 %s 族——请核 sourceFamilyOf。",
					file, fam, bad, effectSilentlyFrozen, effectErrorsOut, familyViewNullPadded)
			}
		}
		// bodies 族无 session 侧等价物，判「不受影响」几乎一定是错的。
		// 带 bodies 腿的 mixed 族同理——它们的正文腿没有任何退路。
		//
		// **默认拒绝 + 具名豁免**（2026-10-02）：这道门最初一刀切禁止 bodies 族
		// 判 unaffected，实测误伤了两种**确实不受影响**的形态：
		//   ① 读点本身在写门内（停写后根本不执行 ⇒ 读点不发生）；
		//   ② 读的是 bodies 的**体量/生命周期**（pg_column_size、保留期清理），
		//      不是正文内容。
		// 这不是把门放宽，而是把「一刀切禁止」换成「默认拒绝 + 必须具名登记」：
		// 放行条件从「门写宽了」变成「有人写下了为什么，而这段话会被 diff 审到」。
		if (fam == familyBodies || fam == familyBodiesMixed) && c.Effect == effectUnaffected {
			if reason, ok := bodiesUnaffectedJustification[file]; !ok || strings.TrimSpace(reason) == "" {
				t.Errorf("%s 读 bodies 族却判为「停写不受影响」：bodies 没有 session 侧等价物"+
					"（session_bodies 只有增量、无 final_full 全量，见审计 §6）。\n"+
					"若确属「读点在写门内」或「只读体量/生命周期」，必须在 "+
					"bodiesUnaffectedJustification 里具名登记并写明机制——默认拒绝，具名放行。", file)
			}
		}
		// 反向约束（2026-10-02）：纯基表族停写后读点**整体停止**，行都不剩，
		// 「内容降级」描述的不是它——那是 silently_frozen（冻结在停写前常数）。
		// 只有「行还在、某一列变空」才是 degraded_content 的形状。
		if fam == familyBase && c.Effect == effectSilentlyDegradedContent {
			t.Errorf("%s（族=%s）被判 %q：纯基表族停写后读点整体停止，结果集直接空掉或冻结，"+
				"不存在「行还在但某列变空」的形状。该判据应改为 %q 或 %q。",
				file, fam, effectSilentlyDegradedContent, effectSilentlyFrozen, effectSilentlyEmpty)
		}
	}
}

func effectsKeyList() string {
	keys := make([]string, 0, len(stopWriteEffects))
	for k := range stopWriteEffects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "/")
}

// nullPaddedUnaffectedJustification 登记「触及 session 臂 NULL 补位列、但仍判
// unaffected」的**具名论证**。
//
// 与 bodiesUnaffectedJustification 同一模式：**默认要求论证，具名放行**。
// 存在的理由是 sourceFamilyOf 的这一维只能看「列名是否出现」，分不清
// 「用在 WHERE/GROUP BY」（真触发：结果集谓词级恒空）与
// 「只出现在投影 / URL 路径 / Go 结构体字段 / 已被同表达式非补位列 COALESCE
// 兜住」（过度触发）。
//
// 每条必须回答：**停写之后，这个读点过滤/分组用的列会不会变？**
var nullPaddedUnaffectedJustification = map[string]string{
	"admin/attachments_routes.go": "两处命中都不是 SQL：`/api/attachments/` 是 HTTP 路径字面量" +
		"（第 38 行 strings.TrimPrefix），`\"attachments\": []any{}` 是响应 JSON 的键（第 103 行）。" +
		"本文件对 710 视图的读点是 attachmentOwnedByTenant 的 EXISTS/JOIN，只用 request_id 与 " +
		"tenant_id 过滤，两列在 session 臂都有真值 ⇒ 判 unaffected 成立。",
	"admin/session_turns_tree.go": "命中列只出现在**投影**里，且已被同表达式的非补位列兜住：" +
		"`COALESCE(outbound_model, client_model, '')`（:228）里 outbound_model 在 session 臂是真值，" +
		"所以 client_model 为 NULL 不改变结果；`COALESCE(request_type, 'main')`（:321/:333）同理有默认。" +
		"本文件不用任何补位列做 WHERE / GROUP BY / JOIN ⇒ 判 unaffected 成立。",
	"admin/session_timeline_query.go": "唯一的补位列命中是 :32 的投影 " +
		"`SELECT request_id, ts, success, client_model, outbound_model, …`。client_model " +
		"在 session 臂为 NULL，但**同一投影里并列了 outbound_model**（session 臂有真值），" +
		"消费方取模型名时有非补位列可选；本文件不用任何补位列做 WHERE / GROUP BY / JOIN " +
		"⇒ 判 unaffected 成立。",
	"internal/collector/gateway_adapters.go": "两处命中都在**以 outbound_model 打头的 COALESCE 里**：\n" +
		"  :61 SELECT COALESCE(NULLIF(outbound_model,''), client_model) AS model_name\n" +
		"  :64 AND   COALESCE(NULLIF(outbound_model,''), client_model, '') <> ''\n" +
		"outbound_model 在 session 臂是真值（不在补位表里），且排在**第一位**；" +
		"client_model 只在 outbound_model 为空时才被读到。⇒ :64 的谓词对有真实 outbound_model " +
		"的行照样通过，Top-20 模型榜与 p50/p99 统计不因 client_model 为 NULL 而变化。",
}
