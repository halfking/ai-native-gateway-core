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
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month\n\t\t WHERE ts >= $1\n\t\t   AND client_model IS NOT NULL",
		Note:     "queryModelStatusAggs / queryModelStatusHourBuckets 两个读点都只读 710 视图（族=reads_710_view_only，机械判定与真实读点一致）。视图含持续增长的 session 臂，模型健康度看板不受停写影响。",
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
		Effect:   effectUnaffected,
		Evidence: "(SELECT max(ts) FROM request_logs_with_current_month WHERE ts >= now() - interval '5 minutes')",
		Note:     "checkStaleness 与 checkAutoCool 均只读 710 视图（族=reads_710_view_only）。session 臂继续写入 ⇒ 5 分钟活性探针与 auto-cool 失败率窗口照常有新行，读端不受停写影响。（控制面轴另判为 live：它 UPDATE credentials。）",
	},
	"bg/stats_minute_rollup_retire.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month AS r\n    WHERE r.request_status IN ('success', 'failure', 'rate_limited')",
		Note:     "三条 retire SQL 都以 710 视图作 NOT EXISTS 保护，视图持续产出键 ⇒ 重扫/退役逻辑读端照常工作。（控制面轴判 live：它 DELETE 派生聚合。）",
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

并把该处**逐字**的一段 SQL 片段填进 Evidence（门会核它是否真的在文件里）。

先看族再看后果：只读 710 视图的读点停写后**不会查空**（视图含 session 臂），
判 silently_empty 会被 TestStopWriteEffectAgreesWithSourceFamily 判红。
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
	switch {
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
	for _, k := range []string{familyBodies, familyBodiesMixed, familyView, familyBase, familyViewBase} {
		total += counts[k]
	}
	if total != len(requestLogsReadInventory) {
		t.Errorf("四族合计 %d ≠ 读点清单 %d —— 有文件被重复计数或漏计", total, len(requestLogsReadInventory))
	}
	t.Logf("S4 停写影响面（按读表族，机械判定）：\n"+
		"  bodies_only=%d        正文无兜底 ⇒ 硬失败\n"+
		"  bodies_plus_other=%d  正文腿硬失败（轮次腿可能照常）\n"+
		"  view_only=%d          session 臂仍供数 ⇒ 静默少计\n"+
		"  base_only=%d          完全停止增长\n"+
		"  view_and_base=%d      部分退化",
		counts[familyBodies], counts[familyBodiesMixed], counts[familyView],
		counts[familyBase], counts[familyViewBase])
}

// TestStopWriteEffectAgreesWithSourceFamily 拦住本项目已经犯过一次的错：
// **710 视图读者不可能「静默变空」**——停写后它们仍由 session 臂供数。
//
// 判据打成关系（族 × 档位的合法组合）而不是硬编码清单：新增档位时必须同时
// 说明它与四族的关系，规则跟着走；否则新档位会默认放行所有组合。
func TestStopWriteEffectAgreesWithSourceFamily(t *testing.T) {
	// 「静默变空」要求读点**完全**没有 session 侧供给。只读 710 视图不满足
	// 这个前提——真库实测该视图 24h 内 36.55% 的行来自 session_turns。
	forbidden := map[string][]string{
		familyView: {effectSilentlyEmpty},
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
		for _, bad := range forbidden[fam] {
			if c.Effect == bad {
				t.Errorf("%s（族=%s）被判 %q，但它读的是 710 视图，停写后 session 臂仍供数"+
					"（真库实测 24h 内 36.55%% 的视图行来自 session_turns），不可能静默变空。"+
					"应为 %q（静默少计）或 %q（报错）。",
					file, fam, bad, effectSilentlyFrozen, effectErrorsOut)
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
