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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
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
	"cmd/tools/validate_sessions_v2/loader.go": "离线校验工具，**读 v1 就是它的工作本身**：" +
		"LoadV1Turns(:119/:122) 两步查 v1 母表 + bodies（hot ∪ parent），目的是与 LoadV2Turns " +
		"逐轮对拍以判定镜像是否漂移。停写后它不报错、仍能对**存量**会话跑校验，只是覆盖不到新会话" +
		"（LoadV1Turns 缺 body 时 pgx.ErrNoRows 被 continue 吞掉、保留空默认，校验会报" +
		"「v2 多出轮次」而不是崩）。它由人工触发、不在在线读写路径上，" +
		"所以「不读正文内容」的退化不构成服务面影响。",
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
		// 2026-10-02（§9.28.2）**第二次**自我更正，且这次是降级：batch1 判
		// unaffected，被 null-padded 族门判红（判对了）→ 改判 silently_empty
		// （理由：client_model 是 710 在 session 臂补位成 NULL 的 30 列之一，
		//  两条过滤恒不命中 ⇒ 看板永久空白）。**那条理由本身依赖 710 形态**：
		// 734 已把 client_model 换成 d.client_model（733 特征层），真库覆盖
		// 99.9996%（parent 1,683,104/1,683,098 缺 6，hot 1,344/1,344 缺 0）
		// ⇒ 过滤正常命中，看板**不再空白**。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month\n\t\t WHERE ts >= $1\n\t\t   AND client_model IS NOT NULL\n\t\t   AND TRIM(client_model) <> ''",
		Note: "残余风险从「永久空白」降为「静默少计」：缺 details 行的 session 行（本机真库 " +
			"parent 6 行 / hot 0 行，占 0.0004%）client_model 为 NULL，被这两条过滤整行滤掉 ⇒ " +
			"看板计数略低于真实值，接口 200、字段齐全、无任何错误信号。\n" +
			"**这一格曾经被登记成最高危档（silently_empty），依据是 710 形态的补位事实；" +
			"该依据在 734 之后就不成立了。** 教训与 §9.28.2 同源：任何以「哪一列在 session 臂恒 NULL」" +
			"为前提的判定，都必须声明它钉在哪个投影形态上——否则过期时没有任何信号。",
	},
	"domains/attachments/handler.go": {
		// 同上：原判 silently_empty 的依据是「710 视图对 session 臂投影
		// NULL::jsonb AS attachments」。734 已改成 d.attachments（733 特征层），
		// 覆盖 99.9996% ⇒ 绝大多数 session 行能取到真值。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "SELECT attachments::text FROM request_logs_with_current_month WHERE request_id = $1",
		Note: "残余风险：缺 details 行的 session 行（本机真库 parent 6 行 / hot 0 行）attachments 为 NULL " +
			"⇒ :171 的 Scan(&raw []byte) 报错 ⇒ 被当成「无附件」⇒ 返 200 + attachments: []。" +
			"行还在、接口 200、字段齐全，只是**这 6 个请求的附件列表被静默清空**。\n" +
			"附件数据本身在 request_attachments 表里未丢（domains/attachments/repository.go:183 已有读法），" +
			"所以这是读腿的退化，不是数据丢失。",
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
	// ── batch5：2026-10-02 §9.35 新增（1 条）────────────────────────────────
	"admin/auto_route_outcome_freshness.go": {
		Effect:   effectValidator,
		Evidence: "SELECT MAX(ts) FROM request_logs_hot",
		Note:     "**这个文件不消费 v1 的数据，它观察 v1 的新鲜度**——所以判 validator 而非 silently_frozen。queryOutcomeFreshness 只取 MAX(ts) 一个标量，算出 age_seconds 并与 outcomeSourceStaleAfter(镜像 bg.settleAbandonAfter=4h) 比较，把结果作为 `outcome_source` 块挂在 /auto-route/audit、/affinity/ranking、/affinity/selections 三个响应上。停写后 MAX(ts) 冻结 ⇒ age 越过 4h ⇒ stale=true、reason=stale ⇒ **冻结本身就是信号**，没有任何业务判断建立在这个值上，所以不是 silently_frozen 那一档。v1 被退役后 MAX(ts) 报 42P01，映射成 reason=absent 而非 5xx（退役是预期终态）。门槛语义由 TestOutcomeSourceStaleAfterMirrorsSettleAbandonAfter 守。⚠ 注意：本条让 effectValidator 这一档第一次**装了非「v1↔session 对账」的用例**（它是新鲜度探针，不是对账）；下一个读这张表的人若发现该档语义已不足以覆盖观察类读点，应扩档而不是把它塞回 silently_frozen。",
	},
	// ── batch6：2026-10-02 §9.36 收口剩余未评估文件 ─────────────────────────
	//
	// 判据见 classificationHowTo。本批的产出**没有照抄子代理**：四批并行评估中，
	// 有三处两个子代理给出了**互相矛盾**的档位，全部由真库实测 + 读码定案，
	// 详见审计 §9.36.2。子代理在**两个方向上**都会判错，所以逐条手验不是形式。
	//
	// ⚠ 本批确立一条**读端判级的前置纪律**：判断「710 视图的读点在停写后是否还供数」
	// 时**必须量近期填充率，不能用全历史均值**。见下面 session_analytics_breakdown.go
	// 与 session_extract.go 两条——同一个量（gw_task_id / provider_id 的 NULL 比例），
	// 全历史口径与近期口径给出**相反**的档位。
	"discovery/discovery.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT 1 FROM request_logs rl",
		Note:     "机制是「**读点在门内**」，不是「结果不变」。:1160 的 `SELECT 1 FROM request_logs rl` 在 `UPDATE model_offers SET available=FALSE` 的 NOT EXISTS 子查询里（谓词含 `rl.success=TRUE AND rl.ts > now() - interval '<graceHours> hours'`，graceHours 默认 24），停写后该谓词确实会恒真——这正是 :1076-1085 注释自陈的失效形态。**但代码已经不再走到那里**：`expireStaleModels` 在 :1110-1115 有显式 S4 护栏 `if mayExpire, blockedReason := staleExpiryMayRun(settings.RequestLogsWriteEnabled()); !mayExpire { slog.Info(...); return }`，`staleExpiryMayRun`(:1091-1096) 在 !requestLogsWritable 时返回 false ⇒ 整段 stale 下架（含伴生的 credential_model_bindings 写）一起短路，护栏由 discovery/stale_expiry_gate_test.go 钉住，灰度时 slog 留有 reason + would_expire 可见。与 admin/telemetry.go 在 bodiesUnaffectedJustification 登记的「读点在写门内 ⇒ 无『返回内容变空』可言」同一种形状。**遗留缺口（不在本档范围）**：admin/request_logs_control_plane_dependency_test.go:297-309 仍登记 `Gated: false` 且 Note 描述的是「未加护栏时」的形态，而护栏是 e52687954（§9.12）后加的 ⇒ 登记表已过期；那道门只核 Evidence 逐字存在与 `Live && !Gated` 时 BlastRadius 非空，**没有任何一条把 Gated 与代码里的实际护栏对照** ⇒ 这条过期不会被自动发现。",
	},
	"domains/providerprofile/adapters.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "COUNT(*) FILTER (WHERE NOT success) AS error_count,",
		Note:     "**「空」在下游被读成「完美」，这是本批最实的一处。** 4 个读点（:147 AnalyzeRequests / :178 ErrorTypes / :209 429 命中率 / :260 BucketSuccessRates）全部 `FROM request_logs_hot`，窗口 `ts >= NOW() - INTERVAL '1 hour' * $2`，$2 clamp 到 providerProfileHotHours=8；而消费端 LightweightCollector.collectForCredential 实际传 **hours=2**（domains/providerprofile/collector.go:165）⇒ 停写 **2 小时**后四腿同时零行。零行不报错（COUNT(*) 返回一行 0）⇒ MetricSnapshot 照常 SaveSnapshot 落库，字段齐全：TotalRequests=0 / SuccessRequests=0 / ErrorCount=0 / ErrorTypes={} / RateLimitMetrics{0,0,0} / AvailabilityWindow{TotalBuckets:0}。**而评分端把零读成满分**：domains/providerprofile/scorer.go:384-385 `if totalRequests == 0 { return 100 }`，同形还有 :92（TotalRequests>0 才计可用性维度）、:98（RateLimitMetrics 存在即算「已测量」），以及本文件 :294-295 自己的注释「空桶不能算不可用，否则冷启动的供应商都会被扣成 0 分」。⇒ 停写后每个凭据的稳定性分趋向 100，且与「真的零流量」在数据上不可区分。四处 err 全部上抛（未被吞），但 0 行不是错误——这正是本档与 errors_out 的分界。窗口 2~8h ⇒ **empty 不是 frozen**。",
	},
	"internal/trace/trace.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_hot WHERE request_id = $1",
		Note:     "唯一读点：:611/:613 同一条 `LoadFromPG` 的 `UNION ALL ... WHERE trace_events IS NOT NULL ORDER BY ts DESC LIMIT 1`（hot ∪ 母表）。`trace_events` 是 **v1 独有**——视图契约刻意不投影它（db/request_logs_view_schema.go:606：「镜像从不写，近窗非空率 0，投影即净数据损失」），session 侧无任何等价物。**本档的判级关键是：被吞掉的错误没有消失，它被转译成了 HTTP 404。** `errors.Is(err, pgx.ErrNoRows)` 被显式吞成 `(nil, nil)`（:619-621，注释 :592-595 写明理由：原实现会让前端展示「no rows in result set」红色错误），非 ErrNoRows 仍上抛。停写后新请求在这条 PG 腿必然 0 行 ⇒ LoadFromPG 返 (nil,nil) ⇒ 上层 loadTrace 转去问 requestTraceState（admin/request_trace.go:187-196，同族另两腿同样 0 行）⇒ traceStateMissing ⇒ loadTrace 返 (nil,\"\",nil) ⇒ handleTrace 显式 **404 not_found**（:136-139）。灰度时立刻可见，故判可接受档。探针请求另有 node_probe_runs 合成路径（admin/request_trace.go:173-177），不受停写影响。",
	},
	"bg/stats_minute_rollup_retire.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month AS r",
		Note:     "三个读点（:21 retireClosedMainMinuteSQL / :77 retireClosedDimStatement / :99 retireClosedErrorDrillMinuteSQL）**全是 NOT EXISTS 存在性守卫，不投影任何值**，只回答「这个已闭分钟的键，视图还产不产出」，产出则保留、不产出则 DELETE。净效应是「**退化成 no-op**」而不是「误删」：① **删除侧与写入侧同源同谓词**——rollup 的整键替换（bg/stats_minute_rollup.go:226/:289）与这三条读的是同一张视图、同一套 dimKey 表达式（:66-68 注释明确 dimKey 与 error 过滤都来自 rollupDimQueries 同一拼接口径），停写后两侧看到同一批 session 臂行 ⇒ 不会把 rollup 刚写的键误判成悬空键删掉；② 这三条唯一要清理的是 v1 侧累加器（internal/quality/minute_aggregator.go 读 request_logs_hot，已单列）的键，而累加器在停写后停产 ⇒ 扫描窗内根本没有键可删，恒删 0 行；③ 视图继续供行，守卫继续保护这些行。用到的列里 request_status/ts/tenant_id/canonical_id/error_kind/outbound_model 六项是 815 视图 session 臂的 `t.*` 直映；`gw_task_id` 与 `api_key_prefix` 本文件**零命中**；仅 provider_id/client_profile/client_model 骑 details 层且只参与等值匹配。错误通道未被吞（fmt.Errorf 包装上抛 ⇒ 外层 slog.Warn）。",
	},
	"admin/data_lifecycle_blobs.go": {
		Effect:   effectUnaffected,
		Evidence: "COALESCE(pg_column_size(rb.request_body), 0),",
		Note:     "两个读点（:99/:220）都是 710 视图 LEFT JOIN bodies，但消费的两列是 `pg_column_size(rb.request_body)` 与 `rb.request_body IS NOT NULL` —— **体量不是内容**。端点语义是「**存量**正文占多少字节、清理能省多少」，停写后新正文本就不再产生，所以「数字停在存量值」是如实反映而非应变化却没变化的指标冻结。查询错误未被吞（:113-117 与 :228-232 都 writeInternalErr → 500）。bodies 族判 unaffected 需具名豁免，已在 bodiesUnaffectedJustification 登记；本轮独立复核该豁免成立（剥注释后两处 bodies 引用确实只有 pg_column_size / IS NOT NULL，:249-255 的 UPDATE 与 :263 的 VACUUM 都不是读点）。",
	},
	"admin/telemetry.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT $1, rl.ts, $2::jsonb, $3::jsonb",
		Note:     "机制是「读点在写门内 ⇒ 读点根本不发生」。剥注释后本文件的 request_logs 引用只剩 6 处，其中唯一的**内容读**是 :571-579 `SELECT $1, rl.ts, $2::jsonb, $3::jsonb FROM request_logs_hot rl WHERE rl.request_id = $1`（回填 bodies 的 ts），它只被 upsertRequestLogBodies(:560) 调用，而唯一调用点是 :534。**本轮用括号配平独立验证了门的作用域**：`if requestLogsWriteEnabled() {` 起于 :454，到 :540 仍未闭合 ⇒ :463 的 INSERT INTO request_logs_hot、:534 的 bodies 写入、:571-579 的读全部在门内；:391 的 t.mirrorRequestBodies(e) 同理。requestLogsWriteEnabled()(:371) 读 settings.RequestLogsWriteEnabled()，与 S4 键同源。错误路径 :536-540 只 slog.Warn + return，但停写时这条路不执行。bodies 族豁免已在 bodiesUnaffectedJustification 登记。",
	},
	"admin/no_topic_session.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb",
		Note:     "四个读点：:145-147 与 :320-322（710 + LEFT JOIN bodies）、:535（710 only，api_key_id/tenant_id）、:558（710 only，preview/work_type/request_mode）。**降级的是正文两列，不是行数**：:140-141 的 `COALESCE(rb.request_body::text,'')` / response_body 在 bodies 无 session 臂时对**新会话**恒为空串，而 message_count/request_count 仍非零、接口 200、消息列表结构齐全 ⇒ 消费方（前端消息列表 / 标题生成 / LLM）拿到「**有轮次、无正文**」的会话。:209-228 对 messages==nil 只降级为 `[]` 不报错；:339-341 Scan 失败 continue 也吞掉。判 degraded 而非 empty 的依据：710 的 session 臂继续供行，本文件的主谓词 `gw_task_id IS NULL AND api_key_prefix = $1` 在 session 臂上**今天仍能匹配**（实测近期 gw_task_id 填充 98.51%、api_key_prefix 100%，见 §9.36.2 的按天口径），所以不是恒 0 行。",
	},
	"cmd/tools/backfill_session_bodies/main.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT request_id, tenant_id, ts",
		Note:     "两个读点：:61-65 Step1 `SELECT request_id, tenant_id, ts ... FROM request_logs`（**直读 v1 物理母表**，非视图，无 session 臂）、:96 Step2 bodies 点查（request_logs_bodies 或 _hot，:88-91 动态拼名）。停写后 Step1 对**新会话恒 0 行** ⇒ turnRows 空 ⇒ 逐轮 Step2 不执行 ⇒ :145-146 打印 `turns=0 bodies=0` 并 **exit 0**，无任何错误信号 ⇒ 运维看到的是「跑完了」。而 Step2 对存量行仍命中、缺行则 log.Fatalf 硬失败——那是 Step2 的 errors_out 形态，但决定整条工具表现的是 Step1 的静默空。判 empty 而非 unaffected：它虽由人工触发、目标人群本就是 V2 之前的历史会话（那些行停写后仍在），但**对新会话它会静默欠回填**（得到 0 轮而不是从 session 族取数），且 exit 0 让这一点不可见。",
	},
	"domains/sessionforensics/export.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COALESCE(rb.request_body, '{}'::jsonb) AS request_body,",
		Note:     "三个读点：:51-52（forensicsExportMessagesSQL，710 + LEFT JOIN bodies）、:71-72（…SQLAlt，同形）、:416（`FROM request_logs` 直读物理母表，ListRecentSessions）。**产出形态正是本文件 :203-205 注释自己判定「比导出失败危险得多」的那种**：710 的 session 臂保证新会话仍有行、turn 编号连续、跳行会 :206 上抛（所以不触发），但正文因 bodies 无 session 臂被 COALESCE 成字面量 \"{}\"；而 `respBody != nil && *respBody != \"\"` 成立 ⇒ `msg.Content = \"{}\"` ⇒ 产出一份**结构自洽、逐轮齐全、正文全空**的证据包。第三个读点另属 silently_frozen（`GROUP BY gw_session_id` + `ORDER BY latest_at DESC LIMIT $2`，:418-420，无时间下界 ⇒ 最新会话集合永久停在停写前一刻），按「取最危险一档」记 degraded 并把 frozen 形态写在这里。错误不吞：:206/:244 上抛，:266-268 空消息且无摘要才回 ErrSessionNotFound。",
	},
	"admin/session_analytics_breakdown.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "两个读点：:266 queryModelBreakdown（按 outbound_model 聚合）、:315 queryProviderBreakdown（按 provider_id 聚合）。**两腿表现不同，判 degraded 是因为 provider 那条**。model 腿安全：实测 session 臂近期 outbound_model 缺失率 0.00%，815 视图里是 `t.model` 直映。provider 腿不安全：视图 :170 是 `d.provider_id`，d 是对 session_turn_details 的 LEFT JOIN；**真库按天实测该列在 session 臂的缺失率（NULL 或 0）是 09-27 的 22.15% → 09-30 的 51.87% → 10-01 的 68.03% → 10-02 的 49.19%，在恶化**。SQL 是 `GROUP BY rl.provider_id` + `COALESCE(rl.provider_id::text,'unknown')`，而 provider_id=0 不是 NULL ⇒ 这些行不会被 COALESCE 收进 'unknown'，而是聚成一个退化的 0/'unknown' 桶 ⇒ 停写后**约一半新流量不再计入其真实 provider**。行还在、接口 200、字段齐全、无错误。错误通道未被吞（:283-285/:331-334 err 上抛 ⇒ 500；:298/:347 rows.Err() 回传），0 行不是错误。",
	},
	"admin/session_extract.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT api_key_id FROM request_logs_with_current_month",
		Note:     "三个读点（:286 sessionAPIKeyID / :311 sessionTenantID / :331 loadSessionPreviewTurns）全部读 710 视图，WHERE 来自 sessionLogsWhere（admin/session_scope.go:39：`WHERE gw_task_id = $1 AND ts > NOW() - INTERVAL '1 hour' * $2`，$2 默认 24、clamp 1..168）。本条**是被真库实测从 wrongly-silently_empty 改判回来的**，判据值得单独记：gw_task_id 在 710 视图取自 `d.*`（:205，details 的 LEFT JOIN），全历史口径下 session 臂有 94.1% 为 NULL，看起来「停写后恒 0 行」；**但按天口径是 09-29 的 4.77% → 09-30 的 37.88% → 10-01 的 97.72% → 10-02 的 98.51%，api_key_prefix 自 09-26 起 100%** ⇒ details 写入链在 09-30 前后已修好、**近期行带着这些值**，读点照常命中。⇒ **判停写后果必须量近期填充率；全历史均值会给出相反的档位。** 文件 :282-284 与 :305-306 的注释也记录了它当初就是为 S4 专门从物理表切到 710 视图的。残余（与停写无关的既存缺陷）：sessionTenantID(:315-317) 任何 err 都 `return \"\"` 无日志，apiKeyID 缺失时静默回落 legacy 单租户 user_id（:300-303 注释自陈那正是要防的跨租户泄漏面）。",
	},
	"admin/session_summary_v2.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "唯一读点 :442（buildRequestLogsFallbackQuery → queryFallbackTurnKeys phase-1，投影只有 request_id/ts；正文走 admin/session_bodies_batch.go，已单列）。**它是 v1-only 会话的唯一供给源**，所以判 errors_out 而非 unaffected：generateSummary :202-216 的结构是「主路径 session 族返回 0 轮 ⇒ 走 v1 fallback ⇒ fallback 也 0 ⇒ `fmt.Errorf(\"no turns found for session %s\")`」，而 :203-207 注释写明这条腿的存在理由正是「很多会话 sessions_v2.enabled=false，session_turns 空而 v1 有完整对话」（注释自测触发率 10.87%）。停写后**这批会话的新行在 v1 里也没有** ⇒ 两条腿同时空 ⇒ **HTTP 500**（:415 注释确认该 error 映射到 500）。这是**响亮的失败**（灰度立刻可见，无被吞的错误通道），故落在可接受档；但它同时意味着「v1-only 会话的摘要生成整体失效」，不是「少了几行」。对 session 原生会话无影响：主路径命中，fallback 根本不调用。",
	},
	"admin/auto_route.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_with_current_month_without_customer_id",
		Note:     "读点 :140（handleDecisions）读的是 **v1-only 中间层视图** `_without_customer_id`（不在 requestLogsViewsWithSessionArm 白名单 ⇒ sourceFamilyOf 归 base 族）⇒ **无任何 session 臂供给**。窗口 `ts >= NOW() - INTERVAL '7 days'` + `ORDER BY ts DESC LIMIT`，长窗口 + 归档保留历史 ⇒ 停写只冻结不删，结果恒为停写前那批决策、max ts 停止前进，接口 200、列表有内容。危险不在报错（错误通道未被吞：:174-177 writeAutoRouteInternalErr，Scan 失败 warnRowSkip 续行，rows.Err() → writeAggRowsErr），而在于「**最近没有新决策**」与「最近没有自动路由流量」不可区分。⚠ 本文件在 §9.35 另加过一个 `outcome_source` 读点，但**那个读点在 admin/auto_route_outcome_freshness.go**（已单独登记），不在本条 Evidence 覆盖范围内。",
	},
	"admin/diagnostics_credential.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND ts > now() - ($2 || ' minutes')::interval",
		Note:     "读点 :148 `FROM request_logs_hot`（另 JOIN credentials/providers，非本族）。窗口 `ts > now() - ($2 || ' minutes')::interval`，默认 **15 分钟**、上限 1440（:73-78）⇒ 短窗口，停写后一小时内结果集真变空：RecentFailures 保持 :99 初始化的空切片、RecentFailuresCount=0，响应 200、字段齐全。**错误通道被吞**：:153 与 :167 都是 slog.Warn 后 else 跳过 ⇒ 查询失败与查不到行**产生完全相同的响应**。⚠ 控制面边缘（灰度前必须知道）：RecentFailuresCount 是 analyzeDiag(:174) 的 switch 条件（:210/:213/:216 判 >=5 / >=3 才建议 force-recover），归零后落到 default「凭据状态正常，无显著异常」⇒ 停写会把**真的坏了的凭据**读成正常并**收回**恢复建议。force-recover(:227+) 本身不读 request_logs、由人独立点 POST，所以不是自动授权写入，本表控制面轴仍判 not_control_plane。",
	},
	"admin/request_trace.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot WHERE request_id = $1",
		Note:     "三条 SQL 共 6 处表引用（每条都是 `request_logs_hot UNION ALL request_logs` 双腿点查，无时间窗）：:190/:192 requestTraceState、:306/:311 fetchRequestSummary（12 列元信息）、:602/:604 origin 判定。**三腿里两腿是静默的，故整档取 silently_empty 而不是 errors_out**。① :306/:311 是决定性的一条：`row.Scan` 任何失败都 `return requestSummary{RequestID: requestID}`（:322-325，注释写「没找到就返回半填充」）⇒ 消费方 handleAIPrompt(:255/:257) 照常 **200**；而 buildAIPrompt(:390-407) 每段都是 `if s.X != \"\"` 卫语句 ⇒ 空值被**静默整行省略、无任何标注**（注释声称的「prompt 中标注」与实现不符）⇒ 产出一份看起来完整、实则缺模型/状态/耗时/provider/credential/错误信息的故障分析提示词。② :602/:604 显式 `_ = ...Scan(&chosen)` 丢错，静默回落 origin=\"direct\"。③ :190/:192 那腿反而是响亮的：trace_events 是 v1 独有（internal/trace/trace.go 的同族读点已单列 errors_out），停写后 PG 腿 0 行 ⇒ handleTrace 显式 **404**。探针请求另有 node_probe_runs 合成路径（:173-177）不受影响。",
	},
	"admin/swim_lane_init.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot rl",
		Note:     "读点 :103 `FROM request_logs_hot rl`（LEFT JOIN models_canonical/model_families/credentials/providers，非本族）。窗口 `since = time.Now().Add(-hours)`，hours 默认 **1**、上限 24（:54-58）⇒ 最短窗口，停写后 requests 为 nil、stats 的三个计数器为 0、三个 map 为 `{}`，而响应是 `json.NewEncoder(w).Encode(resp)` **未设状态码 ⇒ 恒 200**，字段齐全。错误通道未被吞（Query err → return → writeInternalTextErr :113-116/:63-66）；Scan 失败 continue（:143-145）会静默丢行但不影响本档判定。窗口仅 1~24h ⇒ **empty 不是 frozen**。",
	},
	"bg/auto_route_settle_sql.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN ` + src.TurnsTable + ` rl",
		Note: "**§9.49 改判**：本条原先登记在 `bg/auto_route_settle_worker.go` 且判 " +
			"`silently_empty`，两处都已过期——(a) §9.43 把关系名改成 src.TurnsTable，" +
			"§9.44 把两条查询搬进本文件，Evidence 不再逐字存在于 worker 里；" +
			"(b) Effect 也不再是 empty。**这条登记拖了三轮才被发现**：§9.49 查明，" +
			"三道独立的门（读点清单 / 本表的族分类器 / 补位列扫描）全是绿的，" +
			"因为它们都靠「文件里能找到 v1 表名字面量」，而 §9.43 把它变成了 Go 表达式。" +
			"⇒ 已新增 `indirectRequestLogsReaders`（admin/request_logs_indirect_readers_test.go）" +
			"让这三道门认识间接读点；**不是**加注释让扫描器看见（那是伪造测量）。\n\n" +
			"**当前读法**：settleBaselinesSQL(:32，基线 cohort) 与 settlePendingSQL(:57，" +
			"outcome join :73 + LATERAL :80) 两条，关系名都是 src.TurnsTable；src 由 " +
			"`settleSourceFor(logsWriteEnabled)` 决定：写门开 ⇒ request_logs_hot，闸 ⇒ " +
			"session_turns_hot。调用点 loadTaskBaselines(:369) / settleBatch(:535) 仍在 " +
			"bg/auto_route_settle_worker.go。\n\n" +
			"**停写后（闸关）的实测后果，§9.44**：outcome join 仍命中（会话臂对 selection " +
			"request_id 覆盖 99.3%），**不再 silently_empty**；剩下的是三项降级：\n" +
			"  ① 基线 cohort 的总体换成了会话族的 `is_auto_request` 行。两族都健康的窗口" +
			"（2026-09-19..26）实测 v1 820332 行 vs 会话族 659252 行 = **80.4%**；" +
			"p95 偏移本地实测 +15.2%（⚠ 取自探针流量，不代表生产）。\n" +
			"  ② **本机近 24h 会话族 `is_auto_request=TRUE` 为 0 行**（v1 侧 2178 行）⇒ " +
			"cohort 变空 map ⇒ 每条 reward 的延迟项与成本项静默塌成中性 0.5。" +
			"空 map **不是 error**，原先的 `err != nil` 分支永不触发；已加 " +
			"`llmgw_autoroute_settle_baseline_cohort_rows` / `..._baseline_neutral_total` " +
			"与 `AutoRouteSettleBaselineCohortEmpty` / `...NeutralDominant` 告警让它变响。\n" +
			"  ③ 0.7% 的 selection 失去 outcome ⇒ 走 abandon 路径。\n\n" +
			"**⚠ 本条把 `silently_degraded_content` 装进了它原本没有的东西**：该档的既有语义" +
			"是「行还在，但某一**列**内容静默变空」（bodies 正文腿、provider 归属），" +
			"而这里是「行与 reward 都在，但 reward 的**分项**退化」。本文件自己的约定是" +
			"「应扩档而不是把它塞回去」——本轮**没有**扩档（扩档会改动 106 条登记的口径，" +
			"需要单独裁决），先如实记录。",
	},
	"bg/ledger_reconciliation.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_hot",
		Note:     "机制是「读点在门内 ⇒ 根本不发生」。读点 :345 在 usageCreditSQL() 的 usage CTE 里，`FROM request_logs_hot` FULL OUTER JOIN credit_ledger_hot。**但它在到达 SQL 之前就被短路**：checkUsageCredit(:377) 首行 `if ok, reason := usageCreditComparability(settings.RequestLogsWriteEnabled()); !ok { r.markSkipped(reason); slog.Info(...); return 0 }`，而 `usageCreditComparability(:327-332)` 在 !logsWriteEnabled 时返回 `(false, usageCreditSkipS4StopWrite)` ⇒ **查询一次都不发**。:317-324 的注释正是这个自我修复的记录（「继续比对会把停写本身记成账务差异」）。**观测性缺口已闭合（§9.38）**：本条此前记的「SkippedChecks() 只有测试消费者、没有 metric/告警」已修——RunOnce 末尾 `defer recordS4ScanSkipState(...)` 把 skip 列表发布为 `llm_gateway_bg_s4_scan_skipped_last_run{worker,reason}`（1=本轮未执行），由 `deploy/prometheus/rules/s4-scan-skip.yml` 的 `BgS4ScanSkipped`（for: 10m）消费；另有 `BgS4ScanStalled` 用 last_run_unix 区分「被门控跳过」与「worker 卡死」。停写期间只看 findings 计数仍会显示「账务无差异」，但**现在有一条会响的告警说这句话本身不成立**。",
	},
	"bg/today_success_probe.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot rl",
		Note:     "**控制面：读点输出直接决定是否发起探针提交。** 读点 :147 `FROM request_logs_hot rl`（JOIN credential_model_bindings/provider_models，LEFT JOIN node_probe_state），窗口 `rl.ts >= now() - interval '24 hours' AND rl.success = TRUE`，lookback = todaySuccessProbeLookback(:16) 24h、tick 15min(:15)。停写后 24h 内 used CTE 空 ⇒ 0 对 ⇒ `SubmitWithSource(credID, model, \"default\", \"today-success-probe\", \"selfcheck\")`(:126) **一次都不再被调用** ⇒ 被判 unhealthy 的 (credential, model) 永远拿不到复验探针 ⇒ **凭据自恢复闭环静默停止**。错误通道被吞：Query 失败是 slog.Warn + return(:110-112)，且与「0 对」在外部观察上同形（都只留一行 `slog.Info(\"today success probe queued\", \"pairs\", 0)`）⇒ 连「扫描在空跑」都看不出来。与 bg/credential_selfcheck.go 同族闭环。",
	},
	"cmd/gateway/output_compliance_control.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "WHERE gw_session_id = sd.gw_session_id",
		Note:     "**本批唯一带安全语义的一条。** 读点 :84 `FROM request_logs`，作为 LEFT JOIN LATERAL 挂在 `FROM session_dim sd` 上，取 api_key_owner_user（callerOwner），无任何时间窗、只有 `ORDER BY ts DESC LIMIT 1` ⇒ 对**任何停写前有流量的会话**，返回值永久钉在停写前那一行、caller 身份不再变化（不像 7 天窗会自己排空，这里不会）。消费链：lookupOwners → (callerOwner, dataOwner) → makeRedactOwnerLookup（写时脱敏）与输出合规 interceptor → outputcompliance.ShouldRedact(mode, callerOwner, dataOwner)。**反向方向是 fail-closed（安全）**：new-session ⇒ 0 行 ⇒ caller 为 NULL/空，OwnerAllowsSensitive 对空 caller 返 false（domains/outputcompliance/owner.go:36-43）⇒ owner_mismatch 模式下**过度脱敏**，与 :72-73 注释一致。**真正的风险是冻结**：会话的 api key 归属若变更，owner-mismatch 判定会永远沿用旧身份；且 `row.Scan` 错误与 NULL 行走同一条 `return \"\",\"\"`（:95-98）⇒ **错误通道被合并、无任何区分**。判 frozen 而非 empty（点查无窗、存量永久命中）。是否 live 受 getRedactionMode() 的线上取值影响：只有 owner_mismatch 模式受影响，off/always 不受影响。",
	},
	// ── batch7：2026-10-02 §9.36 续 batch6（11 条）──────────────────────────
	//
	// 本批的族全部是 reads_view_with_null_padded_predicate / reads_view_and_base，
	// 所以**判 unaffected 的三条都必须进 nullPaddedUnaffectedJustification**。
	// 判据要点见 batch6 段头：判「视图读点是否还供数」必须量**近期**填充率。
	"admin/auto_title_generator.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COALESCE(rb.request_body::text, '') AS request_body",
		Note:     "两个读点：:211 isFirstSuccessfulUserTurn（`SELECT EXISTS(... success=TRUE AND COALESCE(is_auto_request,FALSE)=FALSE ...)`）、:839 loadSessionLogsForTitle（710 视图 LEFT JOIN bodies，取该 session 前 5 轮）。**降级的是正文两列**：bodies 族只由 v1 写路径产出、**无 session 兜底** ⇒ 停写后新轮次的 request_body/response_body 为 NULL，被 `COALESCE(...,'')` 洗成合法空串，而行数、turn 数、顺序照旧 ⇒ LLM 标题语料里新轮次是空的、只有 preview。**且错误通道被吞**：:410 `if logs, err := g.loadSessionLogsForTitle(...); err == nil && len(logs) > 0` 在 err 非 nil 时整条 DB 腿被静默跳过、退回内存 corpus，连日志都不留；:211 的 err 走 slog.Warn 后 return false 同样吞掉。补位列核查：族标记命中的 client_model(:838) **只在 SELECT 投影**、不进任何 WHERE/GROUP BY/JOIN；gw_task_id 在本文件只出现在注释（:96/:100/:796/:799）不是读点 ⇒ 无恒 0 行路径，所以是 degraded 而非 empty。",
	},
	"admin/live_stream_sse.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "两个读点：:2423 live-stream 首帧回放（710 视图 LEFT JOIN credentials/providers/models_canonical，`rl.ts >= NOW() - INTERVAL '1 hour'`）、:2546 终态 overlay（`request_id = ANY($1) AND COALESCE(request_status,'') NOT IN ('','in_progress')`）。**判 unaffected 的理由是「流量由未挂 S4 写门的 session 臂持续供给 + 谓词不依赖补位列」，不是字面的「读结构/体量」**——见 nullPaddedUnaffectedJustification 的具名论证。补位列核查：client_model(:2409) 只在 SELECT 投影且有 `COALESCE(NULLIF(mc.canonical_name,''), NULLIF(rl.client_model,''), rl.outbound_model,'')` 双兜底；provider_id(:2425) 只在 LEFT JOIN 的 ON 条件且主值取自 credentials 侧（LEFT JOIN 不删行）；credential_id 是 710 直映非补位列。:2546 的 request_status 是 710 派生列（815:207-210 由 t.success/t.status_code 计算，session 臂真值）且 NOT IN 是排除语义，停写后历史行仍在、反而仍能返回终态用于纠正。错误通道：:2423 的 rows.Err() 上抛；:2546 查询失败只 slog.Debug 后 return nil、迭代中断只 slog.Warn（注释自称 best-effort），但那是 **DB 故障**通道、不是停写造成的结果集变化。**⚠ 族误触发**：本文件被分到 null_padded 族是因为补位集里含 `id`，而它的 `id` 命中全在 credentials/providers 上（:262/:273-274/:288-290），对 710 的两个读点一个补位列都没用。",
	},
	"admin/session_turns_tree.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month",
		Note:     "三个读点：:240 主轮次页（`FROM ` + `dbpkg.SessionFamilyTurnsForSessionSQL()` + ` WHERE (rl.parent_request_id IS NULL OR rl.parent_request_id='')`）、:324 子请求批量关联（`FROM request_logs_with_current_month WHERE parent_request_id = ANY($1)`）、:375 EXISTS 探测（404/403 判定）。**主腿与探测腿根本不经视图**——SessionFamilyTurnsForSessionSQL() 直读 session_turns_hot/session_turns 原生表（db/request_logs_view_schema.go:859-865），session 臂照写 ⇒ 恒有行。子请求腿读 710 视图但唯一谓词 parent_request_id 是直映列非补位列 ⇒ 不会恒 0 行。补位列核查：client_model(:228) 只在 `COALESCE(outbound_model, client_model, '')` 投影内、outbound_model 直映在前；request_type(:321) 只在 `COALESCE(request_type,'main')` 投影内、经 normalizeChildRequestType 归一 ⇒ 两者都不进 WHERE/GROUP BY/JOIN。**错误通道全程上抛**（:260/:272/:280/:381/:356-358，handler 按 IsStorageUnavailable 给 503 或 500），唯一例外 :858 的 scan 失败 continue 是坏行跳过、不是停写后果。",
	},
	"admin/top_problems.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND rl.client_model IS NOT NULL AND rl.client_model != ''",
		Note:     "两个读点、**两腿表现不同，model 腿主导**：:194 credential 榜（`LEFT JOIN credentials c ON c.id=rl.credential_id WHERE ... AND rl.credential_id IS NOT NULL GROUP BY rl.credential_id, c.label`）用的 credential_id 是 710 **直映**列（migration 710 第 204 行）⇒ session 臂正常供数、不会空；:238 model 榜（`WHERE ... AND rl.client_model IS NOT NULL AND rl.client_model != '' GROUP BY rl.client_model`）用的 client_model 是 710 第 202 行 `NULL::text AS client_model` 的**补位列**，且**同时用在 WHERE 与 GROUP BY ⇒ 真触发** ⇒ session 臂行被全部滤掉，该腿**恒 0 行**。窗口默认 24 小时（:97 `now.Add(-24*time.Hour)`）⇒ 短窗，判 empty 不判 frozen。⇒ 停写后 model 榜静默变空：接口仍 200、items 只剩 credential 条目、`resp[\"errors\"]` 不出现（这是「查不到行」不是「查询报错」，两条错误通道都不触发）⇒ 前端看到的就是「就是没有问题」。注意查询出错时 :131-145/:171-173 是 slog.WarnContext + 追加 errs 字段、**仍返回 200**，失败对前端不可见。",
	},
	"bg/candidate_failure_monitor.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month",
		Note:     "两个 request_logs 读点：:206 staleness 判据的 lastRequest（`SELECT max(ts) FROM request_logs_with_current_month WHERE ts >= now() - interval '5 minutes'`）、:333 auto-cool 的 win CTE（`WHERE ts >= now() - interval '5 minutes' AND credential_id IS NOT NULL GROUP BY credential_id`）。**判 unaffected 的理由是「流量由未挂 S4 写门的 session 臂持续供给 + 谓词不依赖补位列」**，见 nullPaddedUnaffectedJustification 的具名论证。5 分钟窗口本应倾向 empty，但**empty 的前提（没有新行）不成立**——session 臂照写 ⇒ 窗口内恒有新行，这正是这两个读点的用途。补位列核查：request_logs 读点的行级谓词只有 `ts` 与 `credential_id`（直映非补位）；族标记命中的 provider_id(:256/:267) **全部落在 candidate_failure_logs_with_current_month 腿**（不是 request_logs 族）且只在投影与 GROUP BY。⚠ 同 live_stream_sse：族把本文件分到 null_padded 是因为 `id`（:397 `WHERE id = $2`），那不是 710 视图的列 ⇒ **族误触发**。错误通道两处都是 `if err != nil { return err }`（:207/:353）上抛，不存在吞错转静默。下游表现：failureLogIsStale(:241-243) 在 lastRequest==nil 时 return false 不告警，但 lastRequest 不会为 nil；auto-cool 继续按真实失败率翻 cooling。",
	},
	"bg/shared_pick.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "AND client_model IS NOT NULL",
		Note:     "读点 :84，Priority 1「最常用 client_model」：`FROM request_logs_with_current_month rl WHERE rl.credential_id=$1 AND ts > now() - interval '7 days' AND success=TRUE AND <ProbeTrafficExclusionPredicateView> AND client_model IS NOT NULL GROUP BY client_model ORDER BY count(*) DESC LIMIT 1`。**该读点的唯一产出就是按 client_model 分组取最常用模型**，而 client_model 是 710 第 202 行的补位列、session 臂恒 NULL ⇒ :92 把它全部滤掉 ⇒ **恒 0 行**。**静默降级发生在消费方**：:97 `if err == nil && topModel != \"\"` 在查不到行时 err 是 pgx.ErrNoRows（非 nil）⇒ 条件不成立，**静默落到 Priority 2（featured）→ Priority 3（随机兜底）**，不返回错误、无日志 ⇒ 探针目标模型换了一套，输出仍有 Source 字段、看起来完全正常。判 empty 不判 frozen：结果集是**恒 0 行**而不是「停在一个非空常数」。补位列核查：quality_flags 经 bg/probe_policy.go:186 的 `NOT COALESCE('probe'=ANY(quality_flags),FALSE)` 包裹 ⇒ session 臂 NULL 求值为 FALSE、NOT 后为 TRUE、**不删行**（未实测，但即使删行档位仍是 empty，client_model 那条已足够）；task_type/origin_actor 同样被 COALESCE 包住且非补位列。",
	},
	"admin/credential_success_rate.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50, 3)",
		Note:     "**失效源是 recent_success_rate() 这个 SQL 函数**——本轮已在真库核过线上生效版本的定义：它直读 `FROM request_logs_hot`，窗口 `ts > NOW() - (p_window_hours || ' hours')::interval`，调用处传 3（:59/:183 的 `CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50, 3)`），另排除 probe/self-check 行。停写后 3 小时窗口滑过停写时刻 ⇒ 恒 0 行 ⇒ 函数返回 `AVG(...)::float8 = NULL` 与 `COUNT(*)::int = 0`。**消费方把零读成「正常」**：RecentRate=nil、RecentSamples=0、**BelowThreshold = (0 >= 20 AND …) = false** ⇒ 每个凭据都显示「低于阈值=false / 正常」，接口 200、字段齐全、数值为零/null ⇒ 典型静默变空（数值全零的 healthy 信号）。3 小时极短 ⇒ empty 不是 frozen。:47 的 oldest_request_time 子查询（`SELECT MIN(ts) ... WHERE rl.credential_id=c.id AND lower(COALESCE(rl.outbound_model, rl.client_model))=mo.canonical_raw_name AND rl.ts > NOW() - INTERVAL '3 hours'`）会变 NULL（omitempty ⇒ 字段消失）但外层行集由 `FROM model_offers mo JOIN credentials c` 决定、与 request_logs 无关，且错误上抛（:63/:83/:95/:186 → 500），不构成静默 ⇒ 由 recent_success_rate 主导。⚠ :116 的 `DELETE FROM request_logs_hot` 是写操作不是读点。补位列核查：client_model 只作 COALESCE 兜底且首位是直映的 outbound_model。",
	},
	"admin/provider_models.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT COUNT(*) FROM request_logs rl WHERE %s",
		Note:     "两个读点共用同一个 where 构造（:402 `conditions := []string{\"rl.provider_id = $1\"}` 恒在），**provider_id 是 710 的补位列** ⇒ 视图腿（:456 `FROM request_logs_with_current_month rl WHERE %s ORDER BY rl.ts DESC`）的 session 臂行被恒在谓词全部滤掉，只剩 v1 存量行；基表腿（:443 `SELECT COUNT(*) FROM request_logs rl WHERE %s`，**直读物理母表**）同样只剩存量。停写后 v1 不再增长 ⇒ **结果冻结为停写前的存量**，不是变空（provider_id 在 v1 行上有真实值、存量行持续可查）。默认无时间下界（filter.FromTS/ToTS 可选，不传则全量）⇒ 长窗口 ⇒ **frozen 而非 empty**。错误通道进一步放大不可见性：:444-447 `if err != nil { slog.Warn(\"providerLogs count failed\"); total = 0 }` ⇒ **计数失败直接降级为 0、仅 Warn、仍返回 200**；:456 明细腿的 rows.Err() 走 writeAggRowsErr 会 500。补位列核查：client_model(:412 `rl.client_model ILIKE $N OR rl.outbound_model ILIKE $N`) 仅当用户传了 filter.Model 才追加，且 OR 右侧 outbound_model 是直映列、session 臂有值 ⇒ 不恒 0 行。",
	},
	"admin/tenants.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_hot",
		Note:     "**两类读点并存，基表腿主导。** 视图腿：:297 attachTenantUsage7d（`FROM request_logs_with_current_month WHERE tenant_id = ANY($1) AND ts >= now() - INTERVAL '7 days' GROUP BY tenant_id`）与 :484 单租户同形态 ⇒ 710 session 臂持续供数、谓词 tenant_id 是直映列 ⇒ 继续反映真实流量，不空不冻。基表腿：:764 的 logsTable 内联 `request_logs_hot UNION ALL request_logs`，被 :798/:814/:861/:912 四组统计（credits/byModel/byApp/daily）复用 ⇒ 直读 v1、停写后不再增长，统计窗口由 days 决定（:722 默认 7、上限 365）⇒ **7 天后归零** ⇒ 稳态是 empty、停写后最初 7 天内是 frozen 的过渡态；按「取最危险一档」记 empty。**两条静默通道**：:302-304 `if err != nil { return }`（无日志直接返回，7 天用量字段保持零值）、:479 `_ = h.db.QueryRow(...).Scan(&t.Requests7d, ...)` **返回值直接丢弃**、失败时字段保持零值仍 writeJSON(200) ⇒ 租户用量显示为 0 而不是「无数据」。四组租户统计的错误则显式上抛（→ writeTenantStatsError）。补位列核查：:297/:484 谓词只有 tenant_id（非补位）与 ts；logsTable 投影的 client_model 只在 byModel 的 `COALESCE(NULLIF(outbound_model,''), NULLIF(client_model,''), '<unknown>')` 投影内、**不进 WHERE** ⇒ 不触发恒 0 行。",
	},
	"admin/usage_trend_series.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs_with_current_month_without_customer_id r",
		Note:     "**本条的关键是「该视图没有 session 臂」，本轮用 pg_get_viewdef 逐字确认过**：`request_logs_with_current_month_without_customer_id` 的真库定义是纯 `request_logs_hot UNION ALL request_logs`（118 列全量），**不含任何 session 分支**，与顶层 `request_logs_with_current_month`（session ∪ v1 反连接）不同。⚠ 记录一次**我自己犯的测法错误**：我一度按 request_id 把该视图的行与 session 族 LEFT JOIN，得出「2,167,015 行里 1,517,608 行来自 session（70%）」并据此差点改判 unaffected——那是**跨存储面按 id 匹配**的假象（该包装视图没有顶层那层反连接，v1 行的 request_id 本来就与 session 行重合）。**这里的正确量具是 pg_get_viewdef，不是按 id 猜来源。** ⇒ 停写后 v1 侧不再增长，两个读点（:354 queryUsageTrendDetail、:538 queryUsageTrendModelsDetail）的趋势序列最新桶不再有新数据、已落盘桶保持原值 ⇒ 无错误信号的停摆。窗口由 boardTimeRange 决定（boardDays 默认 1 天、上限 90，admin/board_time_range.go + dashboard_board.go:180）⇒ **默认 1 天窗很快归零（empty，稳态），7/30/90 天窗则冻结（frozen）**；按取最危险记 empty 并在此记录 frozen 过渡。错误通道上抛（:364/:377/:544/:561），scan 失败 warnRowSkip+continue 跳坏行。补位列：provider_id(:337/:522) 与 client_model(经 usageTrendModelExpr :39) 都在**可选**过滤分支内，恒在谓词只有 `r.request_status IN (...)`（710 派生列、session 臂真值）与 ts 区间 ⇒ 真正的失效源是「没有 session 臂」而非补位谓词。",
	},
	"domains/streaming/model_alternatives.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot",
		Note:     "读点 :244 usage_7d CTE：`FROM request_logs_hot WHERE ts > now() - interval '7 days' AND success = TRUE AND canonical_model IS NOT NULL GROUP BY canonical_model`，**直读 v1 基表**（:230 提到 request_logs_with_current_month 的那行是**注释**，说明为何改读 hot，不是读点）。消费链：usage_7d → `LEFT JOIN usage_7d u ON u.canonical_model = r.canonical_name` → `COALESCE(u.cnt,0) > 0` 进 WHERE（:265）→ 决定 'popular' 档是否入选并参与 ORDER BY。停写后 7 天窗内计数**冻结为停写前的常数**（模型仍在 routable/featured 里、cnt 停住）⇒ 'popular' 档的入选与排序静默停滞，接口正常返回、字段齐全、无错误。7 天 = 长窗口 ⇒ **frozen 而非 empty**；且 LEFT JOIN + featured/task_match 分支保证结果集非空（:262 + 注释 :178-179）。7 天滑过后 cnt→0、popular 档消失（那时才转 empty），但**稳态与最初表现都是 frozen**。补位列核查：canonical_model / success / ts 三者均非 710 补位列（canonical_model 是直映列），**无补位列进入谓词**。本文件内无吞错分支，整条大查询由调用方（chat 失败路径）处理。",
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
		// 历史：batch4 判 unaffected → null-padded 族门判红 → 改判 degraded，
		// 理由是「provider_id 在 session 臂恒 NULL ⇒ 按 provider 过滤恒空」。
		// 2026-10-02（§9.28.2）**该理由作废**：provider_id 现由 734 的
		// d.provider_id（733 特征层）供值，真库覆盖 99.9996%（parent 缺 6 行 /
		// hot 缺 0 行）⇒ `provider_id::text = ANY(...)` 正常命中，过滤路径不再恒空。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note: "activity/cost/latency 三个读点（:193/:269/:340）读 710 视图，聚合列 request_status、" +
			"cost_usd、tokens、cache_read/write_tokens、latency_ms、stream_first_chunk_ms 在 session 臂由 " +
			"t.* 真实投影 ⇒ 停写后时间线/成本/延迟序列照常增长。\n" +
			"残余：缺 details 行的那少数 session 行（本机 6/1.68M）按 provider 过滤时整行丢失 ⇒ " +
			"该 provider 的序列略短，接口 200、无错误信号。**原判据「恒 NULL ⇒ 恒空」已随 734 失效。**",
	},
	"admin/session_timeline_query.go": {
		Effect:   effectUnaffected,
		Evidence: "WHERE gw_session_id = $1`",
		Note:     "已按注释(:23-30)刻意改读 710 视图而非物理表，视图体 = session_turns_hot ∪ session_turns ∪（v1 冻结分支反连接）⇒ 停写后新会话轮次由 session 分支继续供数，镜像链启用之前的历史窗口仍由 v1 分支兜住；无行时返回 nil 由调用方各自还原成 null/[]，两种形态都不是新增的静默空。",
	},
	"admin/usage.go": {
		// 历史：batch4 判 unaffected → null-padded 族门判红 → 改判 degraded，理由是
		// 「:806-810 的 provider_id IS NOT NULL 位于 710 子查询内 ⇒ 停写后永不命中」。
		// 2026-10-02（§9.28.2）**该理由作废**：provider_id 现由 d.provider_id 供值，
		// 覆盖 99.9996%。另 :334-366/:456-583/:767/:865/:1001 的同名列本就属于别的表
		// （api_keys / applications / providers / usage_ledger），那部分「假触发」判断不变。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COUNT(*) FILTER (WHERE success) AS success_count,",
		Note: "三处 710 读点（:810/:817 的 key 详情、:1107 的 usageKeyTraffic 5 分钟分桶）靠 session 臂继续变化。\n" +
			"残余：缺 details 行的那少数 session 行在本文件被 provider_id 谓词漏掉，失败分桶略少计；" +
			"且 :810 那条查询被 `_ = h.db.QueryRow(...).Scan(...)` 整体吞错 ⇒ 该退化连日志都没有。\n" +
			"**注意**：吞错形态与补位无关，它独立存在；变的是「漏计多少」（原判「新增流量全部漏计」不成立）。",
	},
	"bg/stats_minute_rollup.go": {
		// 历史：batch4 判 unaffected → null-padded 族门判红 → 改判 degraded，理由是
		// 「provider_id / client_profile / client_model 在冲突键与分组维度里且 session 臂恒 NULL
		//  ⇒ 新流量全落进 provider_id=0 / __unknown__ 桶」。
		// 2026-10-02（§9.28.2）**该理由的三列全部作废**（734 details 层供值，99.9996%）。
		// 但同一文件里**另一条**理由仍然成立：credits_rate_multiplier 与 client_ip 确实
		// 还在恒 NULL 补位集里，逐列裁决见 db/request_logs_view_padded_columns.go。
		// 其中 client_ip 的「是错名副本」这个理由已于同日（§9.60.6）在 252 生产库证伪，
		// 但**「session 臂恒 NULL ⇒ 维度塌成 __unknown__」这个后果仍然成立**——
		// 它依赖的是「没投影」，不依赖「为什么没投影」。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "COUNT(*) FILTER (WHERE r.request_status = 'success')::bigint,",
		Note: "rollupMain/rollupDims 三处读 710，窗口由 request_stats_rollup_cursor.last_ts 推进，" +
			"session 臂持续产新 ts 行 ⇒ 分钟汇总照常滚动；Exec 错误 return err 上抛。\n" +
			"**仍然成立**的降级：credits_rate_multiplier 恒 NULL ⇒ credits 估算按倍率 1.0 计。值劣化，见 §9.15 五种方向。\n" +
			"**已作废（816）**：client_ip 在 session 臂恒 NULL ⇒ client_ip 维度塌成 __unknown__ —— " +
			"816 已把它从 NULL 补位改为 session 侧有源投影（带 CASE 守卫的 text→inet），" +
			"真库往返配对 1,735 行逐值精确匹配 1,294、失配 0。\n" +
			"（订正：本条原写「session_turns.client_ip 是 client_forwarded_for 的错名副本，" +
			"202,014/202,014 逐行相同」——那个数字取自本机库且**无分辨力**（全库 cff 仅 6 个" +
			"distinct 取值、多跳链路 0 条）。252 生产库复测：多跳 3,350 行两列**不等**，" +
			"且两族配对 826/826 相同 ⇒ 它存的是真源对端 IP，裁决已改判。" +
			"但本条的**降级结论不受影响**，因为它锚在「未投影」上。）\n" +
			"**已作废**：provider_id=0 / __unknown__ 桶那一条——那三列现在有真值。",
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
	// ── batch2：2026-10-02 逐点评估（25 条，读端轴收口）──────────────────────
	"admin/attempt_quality_api.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_with_current_month_without_request_class_due_at",
		Note:     "loadFinalRequestMetrics 刻意读 v1-only 中间层视图（注释明示这是为规避 710 turns 段 provider_id 投影 NULL 的设计取舍），窗口 ts >= $2 AND ts < $3（默认 24h、上限 168h），停写后不再有新行 ⇒ TotalRequests/SuccessRate 恒为旧值；**同一响应的 Aggregates 走 requestjourney（request_state_transitions，未受 S4 门控）继续增长**，两半口径从此永久背离而接口仍 200。",
	},
	"admin/data_lifecycle_metrics.go": {
		Effect:   effectUnaffected,
		Evidence: "pg_total_relation_size('request_logs') AS total_size",
		Note:     "唯一读点是 pg_total_relation_size + COUNT 分龄（7/30/90 天桶），读的是表体量与生命周期分布而非流量；停写后存量仍在、行数只因保留期清理下降，不产生查空或报错。已核实无流量类读点混入。",
	},
	"admin/providers.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "COALESCE(SUM(CASE WHEN lower(COALESCE(request_status, '')) IN ('failure', '') AND NOT success THEN 1 ELSE 0 END)::float8",
		Note:     "供应商详情页 error_rate_24h 走 LEFT JOIN (... FROM request_logs_hot WHERE ts >= now() - interval '24 hours' ...)，停写 24h 后该子查询恒零行 ⇒ LEFT JOIN 补 NULL ⇒ COALESCE(er.rate, 0) 把 error_rate_24h **静默改成 0**，接口仍 200、凭据数与健康数全部照常，运营会读成「该供应商零故障」。本次最隐蔽的一处。",
	},
	"admin/session_tenant.go": {
		Effect:   effectUnaffected,
		Evidence: "SELECT 1 FROM request_logs_hot",
		Note:     "assertTaskInTenant 是 OR-of-EXISTS 权限门，五条腿逐支核实：session_summaries / session_turn_details_hot / session_turn_details 三条 session 腿不受停写影响且先短路，v1 两条腿只是镜像链启用前的历史窗口。⚠️ 前提未实测：三条 session 腿对新 task 是否都及时落行，只能从代码确认「设计上供数」。若灰度发现某类 task 只在 v1 留痕，该门仍可能翻转成 404。",
	},
	"bg/auto_route_affinity_worker.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "SELECT 1 FROM request_logs_hot rl",
		Note:     "两条 NOT EXISTS 腿只作 synthetic 轮次排除闸、不供数；主聚合源 auto_route_selections_all（selection_writer 未受 S4 门控）继续增长。停写后 14 天滚动窗口内旧选择仍命中 v1 行做排除，新选择因 v1 无行而一律通过闸门 ⇒ **样本集逐渐失去 synthetic 排除，聚合值继续变化但口径悄然漂移**。既非冻结为常数也非对账，取最接近档并写明该语义。",
	},
	"bg/integrity_fingerprint_probe.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "AND system_fingerprint IS NOT NULL",
		Note:     "一次性存在性探针（ts > NOW() - days/2，hot 臂先查 miss 才查母表）。停写后当前半窗口内不再有带 system_fingerprint 的 v1 新行 ⇒ hasFP=false ⇒ probeEmpty=true 被 memoize，drift worker 走 fingerprintScanSkip **永久跳过全量扫描**（除非 in-process telemetry 观测到指纹重新 re-arm）。而真正的 drift 扫描读 710 视图、session 臂仍在增长 ⇒ 扫描被静默关闭。",
	},
	"bg/model_tier.go": {
		Effect:   effectSilentlyFrozen,
		Evidence: "FROM request_logs_hot rl",
		Note:     "Usage Top-N 段读 request_logs_hot 近 probe.featured_usage_window_hours（默认 72h）窗口。停写 72h 后查询返零行但不报错（err==nil 分支照常执行、rows 空迭代），fs.usage 被整表替换为空集并 m.cur.Store(fs) 落盘。**与 static 段刻意「失败保旧集」的防御相反**：这里空结果会静默清空 usage 精选模型集，深探范围无声收缩。",
	},
	"cmd/gateway/main_v3_wiring.go": {
		Effect:   effectErrorsOut,
		Evidence: "AND gw_session_id = $2 AND outbound_body IS NOT NULL",
		Note:     "LastOutboundForSession 是 L3 冷启动兜底（ORDER BY ts DESC LIMIT 1）。停写后新会话在 v1 里一行都没有 ⇒ pgx.ErrNoRows ⇒ loadFromLegacyDB 返回 err（compression/session_cache.go:670-673），调用方按缓存未命中降级；好在前面有 turnReader（session 原生源）与 Redis 两级先兜，属可接受的显式失败。",
	},
	"cmd/traffic-replay/main.go": {
		Effect:   effectUnaffected,
		Evidence: "WHERE is_auto_request = TRUE",
		Note:     "离线工具（人工触发、不在在线写路径上）；读的是回看窗口内的存量行，停写后仍能回放历史。仅默认 -days=7 窗口在停写 7 天后才空，届时 log.Fatal(\"no historical rows match the filter\") 显式退出。",
	},
	"domains/credentialstate/popularity_tracker.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "WHERE created_at > NOW() - INTERVAL '1 hour'",
		Note:     "后台 tracker 每 tick 用 1 小时窗口聚 client_model 热度。停写 1 小时后查询零行且 rows.Err() 为 nil ⇒ newPopularity 空 map 在写锁下**整体替换**旧 map（t.popularModels = newPopularity，无空集保护）⇒ GetPopularModels 返回空列表、所有模型 GetProbeInterval 一律回落 5 分钟默认值，全程无错误信号。（注：该消费方无生产调用方，见控制面轴的 dormant 判定；此处记的是数据面形态。）",
	},
	"internal/summarystore/store.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "SELECT COUNT(*)::int FROM request_logs_hot",
		Note:     "CountNewTurns / CountTotalTurns 是自动摘要的滚动闸与 5 轮下限闸（调用方 admin/auto_summary_generator.go:389,407）。COUNT 聚合恒返一行 0 而非 ErrNoRows ⇒ 停写后新会话 totalTurns 恒 0 ⇒ shouldTriggerSummary 静默返回 session_too_short_0_turns，**摘要永不触发**（有 reason 字符串但无告警）。",
	},
	"tests/test_popularity_tracker.go": {
		Effect:   effectUnaffected,
		Evidence: "WHERE table_name = 'request_logs'",
		Note:     "离线手工测试脚本（人工触发）；前半是 information_schema/pg_indexes 结构检查（与流量无关），后半 1 小时窗口统计停写后只会打印 0，不报错不改变退出语义。",
	},
	"admin/credential_monitor_heatmap.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "只读 710 视图的 CTE 聚合，session 臂（session_turns hot∪parent）继续增长，热力图不会查空。⚠️ 顺带发现（**与停写无关的既存疑点**）：该文件 295 行的 ExcludeSelfTest 分支硬引用 rl.origin_stage，而 710/734 的 canonical 列契约（db/request_logs_view_schema.go:575-615）名单里没有这一列——若真库视图确无此列，exclude_self_test=1 的查询会直接 SQL 报错。需真库确认，见审计 §9.17。",
	},
	"admin/route_incidents.go": {
		Effect:   effectUnaffected,
		Evidence: "AND rl.request_status = 'failure'",
		Note:     "findingsFor 的证据抽样读 710 视图（ts >= $2 从 inc.FirstFailureAt 起算），session 臂继续供数；total == 0 时返回空 findings 而非报错，是设计好的「证据不足」语义（InsufficientData），停写不改变这一行为。",
	},
	"admin/session_management_api.go": {
		// 历史：batch2 判 unaffected → null-padded 族门判红 → 改判 degraded，理由是
		// 「:298 裸投影 client_model，session 臂恒 NULL ⇒ 模型名一列对新请求恒为空」。
		// 2026-10-02（§9.28.2）**该理由作废**：client_model 现由 d.client_model 供值，
		// 覆盖 99.9996% ⇒ 新请求的模型名有真值。:230/:309 的 owner_user 命中在
		// session_dim 上（另一张表，未补位），那条「假触发」判断不变。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "WHERE rl.gw_session_id = $1",
		Note: "会话详情 Requests 列表读 710 视图（本文件 292-296 行注释明示这就是为「S4 停写后新会话查不到」" +
			"而做的切换），停写后 session 臂继续供数，模型名/预览/时间戳都有真值。\n" +
			"残余：缺 details 行的那少数 session 行模型名为空 ⇒ 列表里那几行少一列；另有既存降级设计——" +
			"err != nil 分支只把 Requests 置空而接口照返 200（与补位无关）。",
	},
	"admin/session_turns_unified.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month",
		Note:     "子请求补齐腿（parent_request_id = ANY($2)）读 710 视图的 request_status/latency_ms/origin_actor，session 臂三项均直映，停写后继续供数；主 turns 腿走 session 原生源，另有 tree_fallback 兜底。",
	},
	"bg/daily_probe_audit.go": {
		// 2026-10-02 **自我更正**：batch2 判 unaffected，被 null-padded 族门判红。核实 :128-130
		// 是**三分支 OR**：
		//   pm.raw_model_name    = rl.client_model    ← 补位 NULL，此臂对新增流量恒不命中
		//   pm.raw_model_name    = rl.outbound_model  ← session 臂有真值，仍有效
		//   pm.outbound_model_name = rl.outbound_model ← 同上，仍有效
		// ⇒ 不是全空（另两臂仍在），但 used CTE 的匹配面少了一条 ⇒ 取 degraded_content。
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "探测范围裁剪的 used CTE 读 710 视图 3 天窗口（probeUsageWindowInterval），session 臂继续增长故仍有真实流量可裁。⚠️ 已核实该 CTE 的 pm.raw_model_name = rl.client_model 一支在 session 臂上 client_model 为 NULL（710 投影 NULL 补位，734 才由 details 补真值）——属既存口径问题，与停写判级无关，但与 §9.14 的补位事实同源。",
	},
	"db/probe_views_unified.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_with_current_month rl",
		Note:     "本文件是视图 DDL（probeHealthDashboardViewsSQL，DROP+CREATE 那五个 dashboard 视图），real24 CTE 的 24h 真实请求反馈读 710 视图、session 臂继续供数；DDL 本身不随停写变化。",
	},
	"admin/body_resolver.go": {
		Effect:   effectErrorsOut,
		Evidence: "LEFT JOIN request_logs_bodies_hot rb",
		Note:     "lookupControlledBody 两段查（hot → 710 视图 LEFT JOIN bodies）：bodies 无 session 兜底但元数据腿有 ⇒ 停写后新 request_id 的 710 视图腿能查到元数据、bodies 腿返 NULL ⇒ COALESCE(...,' ') 给出空串正文，受控正文端点返回 200 但 prompt/response 为空串；只有元数据与 bodies 双双查不到时才走 err ⇒ 调用方统一 404（ErrNotFound），失败可见。",
	},
	"admin/logs.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_bodies_hot",
		Note:     "getLog 元数据走 logsSourceFromSQL()（默认 710 视图，session 臂继续供数），但 fetchRequestBodies / fetchRequestOutboundBody 的 bodies 两段停写后对新请求恒 sql.ErrNoRows，而调用方显式吞掉：detail.RequestBody = nil; detail.ResponseBody = nil 后照常 writeJSON(w, 200, detail) ⇒ **请求详情页对停写后的新请求永远 200 且正文字段静默为 null**，无任何错误信号。",
	},
	"admin/quality_correlations.go": {
		Effect:   effectSilentlyEmpty,
		Evidence: "FROM request_logs rl",
		Note:     "五个相关性分桶全部直读 v1 母表 request_logs LEFT JOIN request_logs_bodies、窗口 ts >= NOW() - INTERVAL '1 day' * $1；停写后每桶零行 ⇒ len(xs) < 2 静默 continue ⇒ results 空 ⇒ 洞察列表返回空数组、接口 200，无错误信号（session 族不参与这五个分桶）。",
	},
	"admin/unified_detail.go": {
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_bodies_hot",
		Note:     "元数据腿是 hot → 710 视图（session 臂兜底，停写后仍能定位新请求），正文腿 loadOutboundBody 只读 bodies_hot → bodies 视图；bodies 查不到时返回 requestdetail.ErrNotFound（:290-292）而上抛，转成 404/错误而非静默空 body ⇒ 失败可见，属可接受。",
	},
	"cmd/tools/validate_sessions_v2/loader.go": {
		Effect:   effectUnaffected,
		Evidence: "FROM request_logs_bodies_hot",
		Note:     "离线校验工具（人工触发、不在在线写路径上）；LoadV1Turns 两步查 v1 母表 + bodies（hot ∪ parent，缺 body 时 pgx.ErrNoRows 被 continue 吞掉、保留空默认），停写后仍能对存量会话跑校验，只是覆盖不到新会话。",
	},
	"domains/sessionsummary/summarizer.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "LEFT JOIN request_logs_bodies_with_current_month rb",
		Note:     "pgRequestLogsSource.getSessionMessagesQuery / GetMessagesSince 的轮次腿读 710 视图（session 臂继续供数、停写后仍返回行），但 bodies 腿无 session 兜底 ⇒ 停写后新会话每行 COALESCE(rb.request_body->>'role','user') 退化成 role='user'、content='' ，**消息数非零故不触发 no messages found 报错** ⇒ GenerateSummary 拿着 20 条空正文去调 LLM 生成空摘要并落库。（控制面轴判 live：它 UPDATE session_summaries。）",
	},
	"admin/session_bodies_batch.go": {
		Effect:   effectSilentlyDegradedContent,
		Evidence: "FROM request_logs_bodies_with_current_month rb",
		Note:     "纯点查助手，后果取决于上游 id 列表：上游 admin/session_summary_v2.go:336（ids 读 710 视图 + MirrorDriftClassSQL='genuine_loss'）与 admin/session_compare.go:309（ids 走 session 原生源）都继续产生新 id，但这些 id 在 bodies 里恒无行 ⇒ map 查不到即 body.requestBody == nil，mergeFallbackTurns 静默产出结构空正文轮次。**首现场在上游**：bodies 写入随 S4 门控停写，本文件是连带结果。另注 querySessionBodiesByRequestIDAndTS 这条 (request_id, ts) 形态本就一直几乎返回空（文件头实测 99.85% ts 不等），与停写无关。",
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
	// §9.49：判据的口径是「已知读点全集」= 直接表 ∪ 间接表。只用直接表时，
	// 间接读点（本条自己所在的 bg/auto_route_settle_sql.go）会被判成
	// 「已不在读点清单中」——而它明明在读。
	known := map[string]bool{}
	for _, f := range allKnownRequestLogsReaderFiles() {
		known[f] = true
	}
	for file := range requestLogsStopWriteClassification {
		if !known[file] {
			t.Errorf("分级表里的 %s 不在已知读点全集（直接表 ∪ 间接表）里 —— "+
				"该文件要么不再读 request_logs，要么清单陈旧；两种情况都让「N 个已评估」这句话失真", file)
		}
	}

	var todo []string
	for _, file := range allKnownRequestLogsReaderFiles() {
		c, ok := requestLogsStopWriteClassification[file]
		switch {
		case !ok, c.Effect == effectUnclassified:
			todo = append(todo, file)
		}
	}
	sort.Strings(todo)
	total := len(allKnownRequestLogsReaderFiles())
	t.Logf("S4 停写逐点评估进度：%d/%d 已评估，未评估 %d 个 —— %s",
		total-len(todo), total, len(todo), progressVerdict(len(todo)))
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

// sessionArmNullPaddedColumns 是 canonical 视图 session 臂上**仍为 NULL 补位**的列。
//
// 2026-10-02（§9.26）把它从硬编码的 30 列改成**从生效投影派生**，因为原来那份
// 钉在 migration 710 的 $proj$ 块上——那是 734 之前的形态，而现网早已是
// 734 的 details-joined 体：
//
//   - 710：30 列 `NULL::` 占位；
//   - 734：其中 **24 列**换成了 session_turn_details 特征层的真实值
//     （client_model / provider_id / attachments / quality_* / request_class …）；
//   - 815：再补 3 列（origin_stage / token_band / client_forwarded_for）。
//
// 真库 details 覆盖率 99.9996%（hot 1,344/1,344 无缺失；parent
// 1,683,104/1,683,098）⇒ 那 24 列在 session 分臂**不是空的**。
//
// 后果不是「多报几个」，而是方向反了：族分类器把「行级有值」判成「谓词级空」，
// 于是会**拒绝正确的判定**（本文件自己的注释就写过这条后果），并让 105 条读端
// 分类里的一批被按错误前提登记。§9.21 的「39 个读方读了恒 NULL 的补位列」同源
// —— 同一个被 710 形态污染的基线。
//
// 派生口径 = db.RequestLogsViewPaddedSessionColumns()（生效投影 withDetails=true
// 里形如 `NULL::` 的表达式对应列名）。权威在 db 包，这里只做转换，理由与
// 逐列裁决一起登记在 db/request_logs_view_padded_columns.go。
//
// **仍然成立的那一半**：这 6 列对读方依旧是「行级有、谓词级空」——视图照常返回
// 行，但按这些列过滤的结果集恒为空。且这 6 列里最要紧的 `id` 是**证明不可投影**
// 的（见同文件裁决表），所以它们只能靠改读法消解，不能靠补投影消解。
var sessionArmNullPaddedColumns = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, c := range dbpkg.RequestLogsViewPaddedSessionColumns() {
		m[c] = struct{}{}
	}
	return m
}()

// sessionArmDetailsSuppliedColumns 是 734 用 session_turn_details 特征层顶掉的
// 那批列：**行级有值、缺 details 行时 NULL**。它们与上面那 6 列不是同一类风险，
// 所以单独登记而不是混进补位表——
//
//   - 补位列：恒 NULL（details 缺行与否都一样），过滤它们恒空；
//   - 本组：99.9996% 有值，过滤它们基本可用，只有 details 缺行的那 6 行会丢。
//
// 登记它是为了让「详情层缺行」这件事有一条独立的、可被单独盯的账，而不是
// 藏在「补位」这个过宽的词里。
var sessionArmDetailsSuppliedColumns = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, c := range dbpkg.SessionFamilyDetailsProjectionColumns() {
		m[c] = struct{}{}
	}
	return m
}()

// legacySessionArmNullPaddedColumns710 是 710 形态下的 30 列清单，**仅**供
// TestSessionArmNullPaddedColumnsMatchMigration 做差集报告用。
//
// 保留它的理由：那张表是「我们曾经相信过什么」的记录。删掉它，下一个人就只能
// 靠 git log 去考古「为什么 §9.21 说是 39 个」；留着它，差集报告能直接打印
// 「哪些列从恒 NULL 变成了有值」，这正是本轮最重要的那条结论。
var legacySessionArmNullPaddedColumns710 = map[string]struct{}{
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

// currentCanonicalViewMigrationPath 返回**当前权威迁移**——即 startup 目录里
// 编号最大的、以 CREATE OR REPLACE VIEW 重建 canonical 视图的 **up** 迁移。
//
// 为什么必须自动发现，不能写死编号（2026-10-02，审计 §9.64）：
//
//	权威源：710 → 734 → 740 → 815 → 816 → 817
//
//	写死 815 时门是绿的；816 把 client_ip 从 NULL 补位改成有源投影后，
//	这张门**当轮就红了**，而且红得像代码坏了。它红了整整一轮没人处理，
//	因为「816 那轮所有 db 包的测试都是绿的」，只有跨包这道 admin 门看得到。
//
//	写死编号的失败模式特别难查：**它要求「每次新增一条重建视图的迁移」都记得
//	回来改这里**，而改漏了的表现不是「门忘了新迁移」，是「门拿旧迁移当权威」
//	——一个看起来完全合理的红，却指向一个不存在的问题。
//
//	. down.sql 必须排除：down 恢复的是**旧形态**（817 down 回到 816 的形态，
//	816 down 回到 NULL 补位）。把 down 算进「当前」会让门在回滚方向上完全失准。
func currentCanonicalViewMigrationPath(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 startup 迁移目录失败：%v", err)
	}
	const needle = "CREATE OR REPLACE VIEW public.request_logs_with_current_month"
	bestNum, bestName := -1, ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读 %s 失败：%v", name, err)
		}
		if !strings.Contains(string(b), needle) || n <= bestNum {
			continue
		}
		bestNum, bestName = n, name
	}
	if bestName == "" {
		t.Fatalf("startup 目录里找不到重建 canonical 视图的 up 迁移 —— " +
			"要么迁移被搬走了，要么 CREATE 语句的写法变了。本门会因此拿不到权威源，" +
			"而那正是它唯一能发现的事。")
	}
	return filepath.Join(dir, bestName)
}

// TestSessionArmNullPaddedColumnsMatchMigration 钉住派生集合与**当前权威迁移**
// （= 当前重建该视图的最高号 up 迁移的 $proj$）声明的一致。
//
// 权威源为什么从 710 换到 815 再换到 816：710 的 $proj$ 是 734 **之前**的形态，
// 它的 30 条 `NULL::` 占位里有 24 条在 734 之后已经被 `d.<col>` 取代。原实现拿
// 710 当权威，于是那张表过期了整整两轮而门一直绿——因为它比的是自己（派生自
// 710 的同一份形态）与自己。这正是「门测的不是它声称测的那个东西」的教科书
// 形态：它确实在校验一致性，只是两边的错误抵消了。
//
// 816（2026-10-02）把 session 臂的 client_ip 从 NULL 补位改为有源投影，同时
// b28ad0c98 把 client_ip 从 db.RequestLogsViewPaddedSessionColumns 里移除——
// 权威源停在 815 时门红（迁移里有、表里没有），这是对的：权威必须跟着**最新
// 视图迁移**走，否则又回到「自己比自己」。
//
// 权威源又为什么从写死 815/816 换成**自动发现**（2026-10-02）：816 落地后这张门
// 红了一轮（写死 815），改成写死 816 又会在 817 落地时**再红一次**——
// 写死编号这件事本身就是失败模式。并行会话已把权威源切到 816（a0da9066d），
// 本轮把它升级为自动发现：详见 currentCanonicalViewMigrationPath 的注释。
//
// 权威源又为什么从写死 815 换成**自动发现**（2026-10-02）：816 落地后这张门红了，
// 而写死编号这件事本身就是失败模式——详见 currentCanonicalViewMigrationPath
// 的注释。
//
// 现在两侧的独立性来自：左侧派生自 **db 包的生效投影**（withDetails=true），
// 右侧解析自**当前权威迁移文件**的 $proj$ 块。两者由 db 包的 viewdef 等价契约
// （TestRequestLogsViewV2EnsureMatchesMigration）保证同源但不同路径。
func TestSessionArmNullPaddedColumnsMatchMigration(t *testing.T) {
	root := repoRootFromCaller(t)
	migPath := currentCanonicalViewMigrationPath(t, root)
	raw, err := os.ReadFile(migPath)
	if err != nil {
		t.Fatalf("读权威迁移 %s 失败 %v", migPath, err)
	}
	t.Logf("当前权威迁移 = %s", filepath.Base(migPath))
	declared := map[string]struct{}{}
	re := regexp.MustCompile(`NULL::[A-Za-z\[\] ]+ AS ([a-z_]+)`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		declared[m[1]] = struct{}{}
	}
	if len(declared) == 0 {
		t.Fatalf("%s 里没解析出任何 NULL 补位列——正则失效或迁移被重写，"+
			"族分类器会静默退化成「视图族一律行级可用」", filepath.Base(migPath))
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
		t.Errorf("session 臂 NULL 补位表与当前权威迁移 %s 不一致：\n"+
			"  迁移里有、表里没有：%v\n  表里有、迁移里没有：%v\n"+
			"后果：族分类器会把「谓词级空」误判成「行级有」，"+
			"从而拒绝正确的 silently_empty 判定。", filepath.Base(migPath), missing, extra)
	}
}

// TestPaddedAndDetailsSuppliedSetsAreDisjointAndComplete 把两张表的关系钉死：
// 它们必须是**互斥且完备**的两半，而不是两份各自为政的清单。
//
// 上一轮的失效形态是「补位表里塞着 24 个其实有值的列」——它既不互斥（那些列
// 同时是 details 供值列）也不完备（漏了 815 之后的 client_ip 等）。这条门让
// 「两张表重叠」直接变成一次可读的报告，而不是等人从族分类的异常里反推。
func TestPaddedAndDetailsSuppliedSetsAreDisjointAndComplete(t *testing.T) {
	var overlap []string
	for c := range sessionArmNullPaddedColumns {
		if _, ok := sessionArmDetailsSuppliedColumns[c]; ok {
			overlap = append(overlap, c)
		}
	}
	sort.Strings(overlap)
	if len(overlap) > 0 {
		t.Errorf("同一列同时被登记为「恒 NULL 补位」与「details 供值」：%v\n"+
			"两者是相反的风险判断（恒空 vs 99.9996%% 有值），同时成立说明有人没想清楚。", overlap)
	}
	// 完备性：710 的 30 条要么在补位表、要么在 details 表，两边都找不到的
	// 说明「从恒 NULL 变成了有值」这条结论没有登记来源。
	var unaccounted []string
	for c := range legacySessionArmNullPaddedColumns710 {
		_, inPadded := sessionArmNullPaddedColumns[c]
		_, inDetails := sessionArmDetailsSuppliedColumns[c]
		if !inPadded && !inDetails {
			unaccounted = append(unaccounted, c)
		}
	}
	sort.Strings(unaccounted)
	if len(unaccounted) > 0 {
		t.Errorf("710 的补位列里，这些既不在当前补位表也不在 details 供值表：%v\n"+
			"请在 db/request_logs_view_padded_columns.go 给它一条裁决（补投影 / 不同东西 / "+
			"无源 / 随 v1 退役），否则「从恒 NULL 变成了有值」这个结论没有落点。", unaccounted)
	}
	// 报告口径：把「不再恒 NULL」的列数按真实集合算出来，让这条差集在 CI 日志
	// 里可见。不能用 len(legacy)-overlap-unaccounted —— 那样算出来的是
	// 「legacy 与 padded 的差」，而 815 之后新增的列（test_col / client_ip /
	// credits_rate_multiplier）不在 legacy 里，会被这个式子悄悄算进"迁移过来的"。
	stillPadded := 0
	for c := range legacySessionArmNullPaddedColumns710 {
		if _, ok := sessionArmNullPaddedColumns[c]; ok {
			stillPadded++
		}
	}
	t.Logf("session 臂列账：恒 NULL 补位 %d 列（其中 %d 列沿自 710），"+
		"details 供值（缺行时 NULL）%d 列，710 时期记为恒 NULL、现已不是的 %d 列",
		len(sessionArmNullPaddedColumns), stillPadded,
		len(sessionArmDetailsSuppliedColumns),
		len(legacySessionArmNullPaddedColumns710)-stillPadded)
}

// nullPaddedColWordRE 为某个 NULL 补位列构造词边界匹配。
//
// 用词边界而不是裸子串：`id` 是补位列，但 `provider_id` / `session_id` 里都含
// "id"，裸子串会把几乎每个文件都判成谓词级空，那条族约束就废了。
func nullPaddedColWordRE(col string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
}

// nullPaddedPredicateHit 报告：这个文件里是否存在某个**函数**（顶层函数或闭包），
// 它既引用了会话臂视图、又提到了某个 NULL 补位列。
//
// 为什么要「函数」而不是「字符串字面量」——这是本轮实测逼出来的：
// 逐字面量归属会被**拼接出来的 SQL** 击穿。bg/shared_pick.go 的 Priority 1 读点里
//
//	`SELECT client_model ... FROM request_logs_with_current_month rl
//	 WHERE ... AND client_model IS NOT NULL GROUP BY client_model`
//
// 夹着一个 `<ProbeTrafficExclusionPredicateView>` 占位，整条 SQL 由多个字面量拼成：
// 含 `client_model` 的那个字面量不出现视图名，含视图名的那个字面量不含
// `client_model`。逐字面量归属会把这条**真谓词**（该读点的唯一产出就是按
// client_model 分组取最常用模型，而它是补位列 ⇒ 恒 0 行）误判成误触发。
//
// 为什么不是「整文件」——那是本轮修掉的旧形状：`id` 命中常常来自**别的表**
// （`ak.id` on api_keys、`WHERE id = 1` on rollup cursor、`RETURNING id` on
// incidents 自身）或者根本不是 SQL（`r.PathValue("id")`、`parts[0]`、
// 一条 Go 正则字面量里的 `correlation_id`）。整文件口径下 19 个文件里有 8 个
// 是这样被带进本族的。
//
// 函数作用域是**两者之间**的粒度，也是两者的超集保护：函数体 ⊇ 单个字面量，
// 所以凡是被逐字面量口径判定为触发的，本口径一定也判定为触发（实测
// strictOnly = 0，无新增漏判），同时把「同文件但不同表」的命中挡在外面。
//
// 解析失败时**退回整文件口径**（保守方向：宁可留在本族并要求具名论证，
// 也不要把一个真触发悄悄放出族外）。
func nullPaddedPredicateHit(raw string) (hit bool, via string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", raw, 0)
	if err != nil {
		for col := range sessionArmNullPaddedColumns {
			if nullPaddedColWordRE(col).MatchString(raw) {
				return true, col + "（解析失败，退回整文件口径）"
			}
		}
		return false, ""
	}
	// 先看这个文件到底有没有出现补位列——绝大多数文件在这一步就出局。
	present := map[string]bool{}
	for col := range sessionArmNullPaddedColumns {
		if nullPaddedColWordRE(col).MatchString(raw) {
			present[col] = true
		}
	}
	if len(present) == 0 {
		return false, ""
	}

	// 收集作用域：所有函数体（顶层 FuncDecl 与闭包 FuncLit）**加上**那些
	// 不在任何函数内的字符串字面量。
	//
	// 只取函数体是不够的，而且是实测抓到的：包级 `const xxxSQL = \`...\`` 形式的
	// 读点整段都在函数之外——
	//   domains/sessionforensics/export.go:37/:57  const forensicsExportMessagesSQL(Alt)
	//   domains/streaming/model_alternatives.go:180 const alternativesSQL
	// 它们是**真的**读视图 + 真的用补位列，漏掉它们等于放走真触发。
	// 这条不是推演出来的假设，是 TestNullPaddedAttributionNeverLosesALiteralLevelHit
	// 报红后逐个打开文件确认的。
	type span struct{ lo, hi int }
	base := fset.File(file.Pos()).Base()
	off := func(p token.Pos) int { return int(p) - base }
	var spans []span
	ast.Inspect(file, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			spans = append(spans, span{off(n.Pos()), off(n.End())})
		}
		return true
	})
	inAnySpan := func(lo, hi int) bool {
		for _, sp := range spans {
			if lo >= sp.lo && hi <= sp.hi {
				return true
			}
		}
		return false
	}
	// 包级字面量（函数之外的）各自构成一个作用域。
	ast.Inspect(file, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		lo, hi := off(bl.Pos()), off(bl.End())
		if inAnySpan(lo, hi) {
			return true
		}
		spans = append(spans, span{lo, hi})
		return true
	})
	for _, sp := range spans {
		if sp.lo < 0 || sp.hi > len(raw) || sp.lo >= sp.hi {
			continue
		}
		body := raw[sp.lo:sp.hi]
		low := strings.ToLower(body)
		readsView := false
		for name := range requestLogsViewsWithSessionArm {
			if strings.Contains(low, name) {
				readsView = true
				break
			}
		}
		if !readsView {
			continue
		}
		cols := make([]string, 0, len(present))
		for col := range present {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			if nullPaddedColWordRE(col).MatchString(body) {
				return true, col
			}
		}
	}
	return false, ""
}

// sourceFamilyOf 从文件源码机械判定它读哪一族。
func sourceFamilyOf(code string) string {
	// raw 保留未剥离的原文，供 nullPaddedPredicateHit 做 AST 解析。顺序很重要：
	// 剥注释会吃掉字符串字面量里的 "//"（URL、SQL `--` 注释），把 Go 源码弄成
	// 无法解析，于是 nullPaddedPredicateHit 会静默退回整文件口径。
	// 第一版把原始值丢了（只留被覆盖后的 code），结果 admin/logs_summary.go
	// 靠这条 fallback 留在本族——一个**注释在说用原文、代码在用剥过的**的错。
	raw := code
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
	//
	// 判据是**函数作用域归属**（见 nullPaddedPredicateHit）：补位列必须出现在
	// 一个既读会话臂视图、又提到该列的函数体里。整文件口径会把 `ak.id`、
	// `WHERE id = 1`、`r.PathValue("id")` 这些**别的表 / 非 SQL** 的命中带进来
	// —— 实测 19 个本族文件里 8 个是这样来的。
	//
	// 注意 np 用的是**原始 raw**（未剥注释）：AST 解析需要能读的源码，而函数体
	// 取的是原文，注释在不在都不影响「这个函数读没读那个视图」。
	np, _ := nullPaddedPredicateHit(raw)
	np = np && vi
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
	// §9.49：遍历「直接表 ∪ 间接表」。只遍历直接表时，间接读点（表名是
	// Go 表达式，见 indirectRequestLogsReaders）**从未进入过这个分类器**，
	// 于是它报「未归入任何一族」——而它其实一直在读 v1。
	for _, file := range allKnownRequestLogsReaderFiles() {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fam := sourceFamilyOf(string(raw))
		if ind, isIndirect := indirectRequestLogsReaders[file]; isIndirect {
			// 器眼看不见它，族由登记给定；登记里没有合法族时才算 undetermined。
			if ind.Family == "" {
				undetermined = append(undetermined, file+"(间接登记未给族)")
				continue
			}
			fam = ind.Family
		}
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
	// §9.49：分母用「直接表 ∪ 间接表」。用 len(requestLogsReadInventory) 会让
	// 每一个间接读点都算成一次「漏计」——而它既没漏也没重，是分母本身少算了一个。
	if total != len(allKnownRequestLogsReaderFiles()) {
		t.Errorf("六族合计 %d ≠ 读点清单 %d（直接表 %d + 间接表 %d）—— 有文件被重复计数或漏计",
			total, len(allKnownRequestLogsReaderFiles()),
			len(requestLogsReadInventory), len(indirectRequestLogsReaders))
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
	"admin/live_stream_sse.go": "命中列都不在**行级谓词**上：client_model(:2409) 只在 SELECT 投影内" +
		"且已被同表达式的非补位列双兜底（`COALESCE(NULLIF(mc.canonical_name,''), NULLIF(rl.client_model,''), " +
		"rl.outbound_model,'')`，canonical_name 来自 models_canonical 表、outbound_model 是 710 直映列）；" +
		"provider_id(:2425) 只在 LEFT JOIN 的 ON 条件里、且主值取自 `COALESCE(c.provider_id, rl.provider_id)`" +
		"的 credentials 侧，而 LEFT JOIN 不删行。两个读点的谓词只有 `ts >= NOW() - INTERVAL '1 hour'`、" +
		"`request_id = ANY($1)` 与一个排除语义的 `request_status NOT IN ('','in_progress')`（request_status " +
		"是 815:207-210 由 t.success/t.status_code 计算的派生列，session 臂真值）⇒ 无恒 0 行路径。流量由未挂 " +
		"S4 写门的 turn_writer.go 持续供给，1 小时窗口恒有新行 ⇒ 判 unaffected 成立。⚠ 本条同时记录一次" +
		"**族误触发**：本文件被分到本族是因为补位集里含 `id`，而它的 id 命中（:262/:273-274/:288-290）" +
		"全在 credentials/providers 上，对 710 的两个读点一个补位列都没用到。",
	"admin/credential_monitor_heatmap.go": "两处命中都在**以 outbound_model 打头的 COALESCE 里**：" +
		":311 `lower(COALESCE(rl.outbound_model, rl.client_model))`（whereClause）、" +
		":322 同一表达式做投影别名。outbound_model 在 session 臂是真值且排第一位，" +
		"client_model 只在 outbound_model 为空时才被读到 ⇒ 对有真实 outbound_model 的行不改变结果。",
	"db/probe_views_unified.go": "同形：:96 与 :104 都是 " +
		"`lower(COALESCE(rl.outbound_model, rl.client_model))`，outbound_model 排第一且为真值列；" +
		"其余命中（:148/:149 的 id / provider_id）属于 credentials 与 providers 两张表，列同名但表不同。",
	"admin/route_incidents.go": "31 处命中全部是 `id`，且逐行核实**没有一处是 SQL 列**：:132 parts[0]、:135/:147/:153 " +
		"是 handleDetail/handleEvents/handleTimeline 的入参解析等 Go 代码。" +
		"本文件对 710 视图的读点用 ts / request_status / credential_id，都不是补位列。",
}
