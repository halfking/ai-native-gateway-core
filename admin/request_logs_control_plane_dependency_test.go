// request_logs_control_plane_dependency_test.go — 2026-10-02。
//
// §9.9：**本表与 stopWriteClassification 是两条正交的轴，不是同一张表的第六档。**
//
// 已有那张表（request_logs_stop_write_classification_test.go）的五档
// ——errors_out / silently_empty / silently_frozen / unaffected /
// validator_dual_read——描述的都是**「读点的输出长什么样」**：接口返回 200 还是
// 500、结果集空不空、冻不冻结。所以它们全都落在「响应面」。
//
// 但 S4 停写还有一类读点，它���输出既不是 200 也不是 500，而是**去改数据库里的
// 另一个状态**：会话 id、轮次序号、凭据可用性、探针节奏。这一类在旧表里**没有
// 对应格**——不是判错了，是那张表的坐标系里没有这个维度。
//
// 为什么这不是学术问题：停写的门控（storage.request_logs_write_enabled）只管
// **写侧**。一个**读** v1 去决定「要不要写/写什么」的读点，永远落在门控之外。
// 于是 S4 可以在「所有读端分级都合格」的情况下，仍然悄悄改变写入行为。
//
// 最重的一条是 bg/credential_recovery.go：lookbackCandidateSQL 的查询结果直接
// 授权一次 URSM v2 恢复写入（success=true），代码注释原文写着
// 「success=true is justified by the SQL predicate」。停写后这个前提被冻结
// 36 小时，然后永久消失——**失效方向是放行写入授权**，与 2026-10-02 修掉的
// s4_ready 真空为绿同一族，只是这次放行的是凭据恢复，不是切流。
package admin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// controlPlaneVerdict 是「这个 v1 读点的输出**决定**了什么」。
type controlPlaneVerdict struct {
	// Feeds：读点结果被喂给哪个决策（人话，必须能独立看懂）。
	Feeds string

	// Live：是否存在**活着的**消费方。
	// 这一维不可省：本表第一条登记（popularity_tracker）在核实后被判为
	// dormant——它默认关闭，且它唯一的输出 GetRecommendedProbeInterval
	// 全仓没有生产调用方。把它当成活的会造出一个不存在的风险。
	Live bool

	// Gated：该消费方是否被 S4 写门覆盖。false = 门管不到它。
	Gated bool

	// Evidence：必须在该文件里逐字存在的片段。
	Evidence string

	// BlastRadius：Live && !Gated 时必填——「它授权/改变的具体写入是什么」。
	// 逼着每条活的、未被门控的读点都写清楚它能动什么，而不是只贴个 SQL。
	BlastRadius string

	Note string
}

const (
	// cpUnevaluated：还没查。**这是当前真实状态，不是占位符。**
	cpUnevaluated = "unevaluated"

	// cpNotControlPlane：读过 v1，但输出只流向展示/导出/对账，不决定写。
	cpNotControlPlane = "not_control_plane"

	// cpControlPlaneLive：输出决定写入或身份，且消费方是活的。
	cpControlPlaneLive = "control_plane_live"

	// cpControlPlaneDormant：结构上是控制面，但消费方默认关闭或无调用方。
	// 登记它是为了防止日后有人把它接上——那时这条会立刻变成 live。
	cpControlPlaneDormant = "control_plane_dormant"
)

var controlPlaneVerdicts = map[string]struct{ note string }{
	cpUnevaluated:         {"尚未查：不能与「已确认不是控制面」区分"},
	cpNotControlPlane:     {"只流向展示/导出/对账，不决定任何写入或身份"},
	cpControlPlaneLive:    {"活的：输出决定写入或身份"},
	cpControlPlaneDormant: {"结构上是控制面，但消费方默认关闭或无调用方"},
}

// requestLogsControlPlaneReaders 覆盖 requestLogsReadInventory 中**所有非 admin/**
// 的文件——控制面依赖只可能出现在请求路径与后台 worker 侧；admin/ 是读端展示面，
// 那条轴由 requestLogsStopWriteClassification 负责。
//
// 键必须与 requestLogsReadInventory 的非 admin 子集完全一致（由门双向核）。
var requestLogsControlPlaneReaders = map[string]controlPlaneVerdict{
	// ── 已逐行核实 ───────────────────────────────────────────────────────
	"bg/credential_recovery.go": {
		Feeds: "凭据恢复授权：lookbackCandidateSQL 选出「36h 内有成功流量」的降级/离线绑定",
		Live:  true,
		Gated: false,
		Evidence: "SELECT 1 FROM request_logs_hot rl\n" +
			"\t\t          WHERE rl.credential_id = cmb.credential_id\n" +
			"\t\t            AND COALESCE(rl.outbound_model, rl.client_model) = pm.raw_model_name\n" +
			"\t\t            AND rl.success = TRUE",
		BlastRadius: "ursmRecoverSink(..., success=true, 0) —— URSM v2 的 Recover(30) 写入，" +
			"外加 dispatchProbe 与候选缓存失效。代码注释原文：「success=true is justified by " +
			"the SQL predicate」。",
		Note: "**本表最重的一条。** 两段后果，第二段比第一段危险：\n" +
			"  ① 停写 36h 后候选集恒空 → `if len(candidates) == 0 { return }` 静默返回，" +
			"无日志无告警，降级凭据只能等自身探针恢复；\n" +
			"  ② **停写后 36h 内仍在用停写前的陈旧成功记录授权恢复写入**——失效方向是" +
			"**继续放行**，不是停止。\n" +
			"与 2026-10-02 修掉的 s4_ready 真空为绿同族：门控的前提消失后仍给出许可，" +
			"而这次许可的对象是凭据可用性。",
	},
	"domains/hooks/observability/telemetry/client.go": {
		Feeds: "会话身份与轮次序号：FindRecentGatewaySession 决定无 id 请求复用哪个会话；lookupTurnNumber 决定 outbox 事件里的 turn_no",
		Live:  true,
		Gated: false,
		Evidence: "AND gw_session_id LIKE 'gw\\_%'\n" +
			"\t\t  AND ts >= NOW() - ($4 * INTERVAL '1 second')\n" +
			"\t\tORDER BY ts DESC\n" +
			"\t\tLIMIT 1",
		BlastRadius: "空结果 ⇒ session_assignment.go:194 createSession()，即**新分配一个 gw_ 会话 id**；" +
			"turn_no 则冻结为停写前的计数。",
		Note: "**2026-10-02 自我更正**：子代理初判「每个请求静默新开 gw_session_id」，" +
			"我复核后**判定过重**——主路径是 Redis 的 LastSystemSessionIndex（5 分钟 TTL，" +
			"不受 S4 门控），DB finder 只是兜底。准确的失效条件是两条：\n" +
			"  ① 无 Redis 部署：main.go:968 的装配在 redis 分支内，lastSystemSession 为 nil，" +
			"session_assignment.go:160 的判空直接跳过 ⇒ **DB finder 是唯一路径**，完全切断；\n" +
			"  ② Redis 索引 miss（TTL 到期 / Redis 重启 / device seed 不匹配）⇒ 原本由 DB " +
			"兜底的续对话变成新建会话。\n" +
			"turn_no 一侧确认为门控外：client.go:2113 注释原文「outbox request-completed " +
			"会话事件在门控外照常提交」。缓解：同处注释写明「the session/v2 aggregator owns " +
			"the authoritative turn_no anyway」，属派生计数器，非权威。",
	},
	"domains/providerprofile/adapters.go": {
		Feeds: "provider 画像打分 → AlertEngine → PGCredentialActor 决定凭据自动禁用/恢复",
		Live:  true,
		Gated: false,
		Evidence: "\t\tSELECT COUNT(*) FILTER (WHERE upstream_status_code = 429) AS rl_hits,\n" +
			"\t\t       COUNT(*) AS total\n" +
			"\t\tFROM request_logs_hot\n" +
			"\t\tWHERE credential_id = $1\n" +
			"\t\t  AND ts >= NOW() - INTERVAL '1 hour' * $2",
		BlastRadius: "bg/provider_profile_workers.go 装配的 AlertEngine 用该画像驱动 " +
			"PGCredentialActor 的**凭据自动禁用与恢复**。",
		Note: "活的：经 bg 的 ProfileCollector / ProfileAggregator / ProfileAlertWorker " +
			"三个 worker 装配到 main.go。停写后每 credential 的 1h 窗口指标归零，画像失去" +
			"依据。**未核实**：AlertEngine 在「无数据」时是「不告警」（安全）还是" +
			"「评分掉到阈值以下 → 禁用凭据」（危险）——这决定它是降级还是事故，" +
			"必须在生产库复核一次才能定级。",
	},
	// ── bg/ worker 簇：凭据健康与路由子系统（2026-10-02 逐个核实）──────────
	//
	// 这一簇的共同形状：**读 v1 流量 → 决定凭据/路由/探针的状态写入**。
	// 它比 §9.9.3 ① 的 credential_recovery 更广——后者是单一文件的一次写入
	// 授权，这一簇是整个「凭据可用性反馈环」的输入端。
	//
	// ⚠️ 方向不止一种，逐条不同，别把它们当同一类处理：
	//   · 继续放行（credential_recovery：36h 内用陈旧证据授权恢复）
	//   · 退回保守（model_probe：热度没了 → next_retry_at 不再推后 → 探针变频繁）
	//   · 静默失效（today_success_probe：候选集空 → 不再提交探测 → 只能等自身恢复）
	"bg/candidate_failure_monitor.go": {
		Feeds:    "5 分钟失败信号 → UPDATE credentials（凭据状态写入）",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_with_current_month",
		BlastRadius: "bg/candidate_failure_monitor.go:391 `UPDATE credentials` —— 凭据级状态写入，" +
			"由 :206/:333 两处 710 视图读点（5 分钟窗口）驱动。",
		Note: "读 710 视图（有 session 臂）⇒ 停写后业务流量仍供数，**不会查空**。" +
			"但探针流量消失会改变 5 分钟窗口的构成，失败信号口径随之变化。",
	},
	"bg/credential_selfcheck.go": {
		Feeds:    "request_logs_hot 近期表现 → 自检任务提交（self_check_runs / system_probe_runs / probe sink）",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_hot rl",
		BlastRadius: "SetProbeSink 注入的探测提交 + `INSERT INTO self_check_runs` / " +
			"`INSERT INTO system_probe_runs`（:822/:851）。",
		Note: "三处读点（:260/:544/:588）决定「要不要自检、探什么」。停写后输入冻结。",
	},
	"bg/model_probe.go": {
		Feeds:    "usage CTE（request_logs_hot 成功流量）→ model_probe_state.next_retry_at → 探针退避 → broken_confirmed → available=FALSE",
		Live:     true,
		Gated:    false,
		Evidence: "COALESCE(rl.outbound_model, rl.client_model) AS raw_model,",
		BlastRadius: "`UPDATE model_probe_state SET next_retry_at`（:436）与最终的 " +
			"`UPDATE credential_model_bindings SET available = FALSE`（:1074）。",
		Note: "**方向与 ① 相反，且不能只看终点。** reconcileBrokenConfirmedBindings（:1072）" +
			"本身不读 v1，它读 model_probe_state；v1 的影响在**上游**：:421-435 的 usage CTE " +
			"用 v1 成功流量算「热模型」并把它们的 next_retry_at 推后。" +
			"⇒ 停写后 usage 臂恒空，热门模型**不再获得退避加成**，探针反而变频繁。" +
			"别把它误判成「凭据被误禁用」——那是另一条链，且不由 v1 直接驱动。",
	},
	"bg/today_success_probe.go": {
		Feeds:       "request_logs_hot 成功记录 → 今日成功探测的提交（仅对 available=FALSE 的 pair）",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_hot rl",
		BlastRadius: "探测提交 → 探测结果写回 credential_model_bindings.available。",
		Note: "文件头注释：only pairs with a current unhealthy signal are submitted。" +
			"⇒ 停写 36h 后无成功证据，降级 pair 不再被主动探测，**只能等自身恢复**——" +
			"与 credential_recovery ① 同方向的静默失效。",
	},
	"bg/passive_probe_listener.go": {
		Feeds:    "710 视图流量计数 → passive_probe_state（INSERT/UPDATE，多处）",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_with_current_month rl",
		BlastRadius: "passive_probe_state 的 :112/:170/:218/:259/:474 五处写入——" +
			"该表驱动被动探测的开关与计数。",
		Note: "读 710 视图 ⇒ session 臂继续供数，不会查空；但 passive 计数与 probe 流量强相关，" +
			"停写后构成变化。",
	},
	"bg/daily_probe_audit.go": {
		Feeds:    "dailyProbeAuditSQL()（710 视图）→ 批量提交节点探测",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_with_current_month rl",
		BlastRadius: "a.run() 遍历结果后逐个提交探测（submitted 计数，:191+），" +
			"探测结果写回凭据状态。",
		Note: "核实依据：函数体 :181-197 查到 credID/model 后进入提交循环，不是只读报表。",
	},
	"bg/integrity_fingerprint_probe.go": {
		Feeds:    "system_fingerprint 探针流量是否存在（bool）→ 是否触发安全检测",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs -- sqlreadguard:allow D11 probe parent arm (hot arm checked first; existence superset of the view)",
		BlastRadius: "probeFingerprintTraffic() 返回的 bool 决定 IntegrityFingerprintDrift " +
			"是否判定「指纹漂移」——安全检测器的触发条件。",
		Note: "文件仅 57 行、单一函数，但它是安全检测器的**触发开关**。" +
			"与读端表里已登记的 bg/integrity_fingerprint_drift.go 是同一系统的两端。",
	},
	"bg/auto_route_affinity_worker.go": {
		Feeds:    "request_logs_hot/parent 亲和度证据 → task_model_affinity（INSERT/UPDATE ×3）",
		Live:     true,
		Gated:    false,
		Evidence: "SELECT 1 FROM request_logs rl",
		BlastRadius: "task_model_affinity 表的 :385/:484/:507 三处写入——" +
			"该表决定任务→模型的亲和路由。",
		Note: "两段读（hot + parent）合并判定亲和，语义上等价于「v1 侧有流量才有亲和」。",
	},
	"bg/auto_route_settle_worker.go": {
		Feeds:       "request_logs_hot 结算证据 → auto_route_selections_hot（UPDATE ×2）",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_hot rl",
		BlastRadius: "`UPDATE auto_route_selections_hot`（:557/:578）——自动路由选择的结算状态。",
		Note:        "注释（:252）说明这是 hot-only 结算。停写后结算证据断流。",
	},
	"bg/ledger_reconciliation.go": {
		Feeds:       "request_logs_hot 对账证据 → maas_reconciliation_findings（INSERT）",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "`INSERT INTO maas_reconciliation_findings`（:336）——对账发现，驱动告警/处置。",
		Note:        "写入的是**观察记录**而非直接改凭据，但 findings 会驱动下游处置，故判 live。",
	},
	"bg/model_tier.go": {
		Feeds:    "request_logs_hot 流量 → IsFeaturedModel() 的分层结果 → 路由",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_hot rl",
		BlastRadius: "IsFeaturedModel(rawModel, canonical) 的返回值被路由消费——" +
			"决定某模型是否按 featured 档处理。",
		Note: "写入面在内存（refresh :110），但**消费方是路由决策**，故判 live 而非 not_control_plane。",
	},
	"bg/shared_pick.go": {
		Feeds:       "710 视图 → PickProbeModelForCredential() → 探测哪个模型",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "探测目标模型的选择 → 探测 → 凭据可用性回写。",
		Note:        "与 today_success_probe / daily_probe_audit 同属「探测目标选择」族。",
	},
	"bg/auto_index_refresher.go": {
		Feeds:       "request_logs_hot 近期流量 → 推荐索引 → INSERT INTO credential_model_index_hot",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_hot rl",
		BlastRadius: "`INSERT INTO credential_model_index_hot`（:215）——按推荐建/改索引。",
		Note: "读端表已登记为 silently_frozen；控制面轴上它是 live（写的是索引配置）。" +
			"两轴并存不矛盾：**同一个读点可以既「输出冻结」又「决定写入」**——" +
			"这正是不能把两轴合并的实证。",
	},
	"bg/stats_minute_rollup.go": {
		Feeds:       "710 视图 → request_stats_minute / request_stats_dim_minute 物化",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month r",
		BlastRadius: "（dormant：写入的是派生统计物化，不是决策）",
		Note: "判 not_control_plane 的依据：写入目标是**派生数据**（分钟汇总 + 游标），" +
			"不决定任何凭据/路由/身份。读端轴已登记为 unaffected。" +
			"⚠️ 但 session 臂有两列恒 NULL（credits_rate_multiplier、client_ip，见读端表备注），" +
			"会让 credits 按倍率 1.0 计、client_ip 维度塌成 __unknown__——" +
			"这是**值劣化**，不属于任何一档，属遗留项。",
	},
	"bg/lite_retention_worker.go": {
		Feeds:       "SQLite 保留期清理（与 PG 侧 S4 门控不同链）",
		Live:        false,
		Gated:       false,
		Evidence:    "DELETE FROM request_logs WHERE rowid IN (",
		BlastRadius: "（dormant：lite 模式本地库的行级保留期）",
		Note:        "读的是**待清理行**（生命周期），不是流量。与 PG 侧停写门控无关。",
	},
	// ── 第二批：2026-10-02 逐个核实 ───────────────────────────────────────
	"discovery/discovery.go": {
		Feeds:    "request_logs 的「近期无成功」→ UPDATE model_offers SET available=FALSE（禁用写入）",
		Live:     true,
		Gated:    false,
		Evidence: "SELECT 1 FROM request_logs rl",
		BlastRadius: "`UPDATE model_offers SET available = FALSE, " +
			"unavailable_reason = 'auto_discovery_expired'`（:1112）——把凭据上的模型下架。",
		Note: "**本表方向最危险的一条。** 它与 credential_recovery 同形但方向相反，" +
			"而且是**否定式守卫**：`NOT EXISTS (… rl.success = TRUE AND rl.ts > now() - interval 'N hours')`。" +
			"停写后 NOT EXISTS **恒真** ⇒ 无论模型是否真的在用，都会被判为 auto_discovery_expired " +
			"而下架。这不是「恢复变慢」，是**主动禁用仍在工作的凭据模型**。" +
			"失效方向与 s4_ready 真空为绿同族，但后果更重：写的是可用性，且无异常、无告警。",
	},
	"cmd/gateway/output_compliance_control.go": {
		Feeds:       "request_logs 的 api_key_owner_user → 输出脱敏的 owner 判定",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs\n\t\t\tWHERE gw_session_id = sd.gw_session_id",
		BlastRadius: "lookupOwners() 返回的 callerOwner 为空 → owner 规则走保守分支 → **输出被脱敏**。",
		Note: "失效方向是**过度脱敏**而非泄漏（代码注释明确：失败应永不泄漏），属 fail-safe。" +
			"但影响面不小：caller owner 取不到会让合规控制对所有会话保守处理。" +
			"注意它优先用 session_dim.owner_user，v1 只提供 caller 侧 ⇒ 影响是部分的。",
	},
	"domains/sessionsummary/summarizer.go": {
		Feeds:       "710 视图语料 → UPDATE session_summaries（会话摘要内容）",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "`UPDATE session_summaries`（:812）——会话摘要正文。",
		Note:        "读 710（有 session 臂）⇒ 不查空；但正文质量依赖 bodies 腿（见读端表 bodies 族）。",
	},
	"internal/summarystore/store.go": {
		Feeds:       "request_logs_hot 计数 → session_summaries 的 INSERT/UPDATE 决策",
		Live:        true,
		Gated:       false,
		Evidence:    "SELECT COUNT(*)::int FROM request_logs_hot",
		BlastRadius: "`INSERT INTO session_summaries`（:181）——是否/如何写会话摘要。",
		Note:        "计数用于「有没有轮次可摘要」的判断；停写后计数冻结。",
	},
	"domains/routeincident/store.go": {
		Feeds:       "710 视图 → 路由事件状态机（pending/active/recovering 迁移）",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "事件状态迁移写入（SELECT … FOR UPDATE 保护的 UPDATE 路径）。",
		Note:        "读端表已登记并经族门复核过；控制面轴上它是 live（驱动状态机迁移）。",
	},
	"internal/quality/minute_aggregator.go": {
		Feeds:    "request_logs_hot → provider_metrics_minute（供给质量评分）",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_hot",
		BlastRadius: "`INSERT INTO provider_metrics_minute`（:21）——分钟级质量指标，" +
			"下游据此评分并影响路由/凭据决策。",
		Note: "判 live 而非 not_control_plane 的依据：写的是**被决策消费的指标**，不是展示用物化。",
	},
	"autoroute/recommend_v2.go": {
		Feeds:       "request_logs → 自动路由推荐 → 下游 worker 写 task_model_affinity / selections",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "本文件不直接写；其推荐结果被 bg/auto_route_* worker 消费并落库。",
		Note:        "与 bg/model_tier.go 同理：写入面在别处，但**消费方是路由决策**，故判 live。",
	},
	"domains/attachments/handler.go": {
		Feeds:       "按 request_id 取 attachments → 响应内容",
		Live:        false,
		Gated:       false,
		Evidence:    "SELECT attachments::text FROM request_logs_with_current_month WHERE request_id = $1",
		BlastRadius: "（dormant：只流向响应体，不决定任何写入或身份）",
		Note:        "文件内无 UPDATE/INSERT，读点唯一且直接服务响应。",
	},
	// ── 第三批：2026-10-02 逐个核实 ───────────────────────────────────────
	"bg/integrity_fingerprint_drift.go": {
		Feeds:    "710 视图指纹基线 → integrity_fingerprint_baseline / model_integrity_events",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_with_current_month",
		BlastRadius: "`INSERT INTO integrity_fingerprint_baseline`（:363）与 " +
			"`INSERT INTO model_integrity_events`（:396）——安全基线与安全事件。",
		Note: "读端表已登记为 silently_frozen（经族门复核）。控制面轴上是 live：" +
			"它写的是**安全事件**，停写后指纹比对的两侧都冻结 ⇒ 漂移检测静默失效。",
	},
	"bg/stats_minute_rollup_retire.go": {
		Feeds:    "710 视图 → 游标推进 → DELETE 三张 rollup 表",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_with_current_month AS r",
		BlastRadius: "`DELETE FROM request_stats_minute`（:16）、" +
			"`request_stats_dim_minute`（:71）、`request_stats_error_drill_minute`（:94）。",
		Note: "方向安全：游标冻结 ⇒ retire 停止 ⇒ 聚合数据累积而非丢失。" +
			"判 live 只因为它确实由 v1 读驱动 DELETE，失效方向不危险。",
	},
	"domains/streaming/anomaly_harvester.go": {
		Feeds:    "request_logs_hot 回填 actual_tokens → response_format_anomalies / fault_events",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs_hot r",
		BlastRadius: "`UPDATE response_format_anomalies SET actual_tokens`（:219）与 " +
			"`INSERT INTO fault_events`（:358）。",
		Note: "停写后 join 恒 0 行且 `if n > 0` 才打日志 ⇒ 无错无日志，actual_tokens 永远 NULL，" +
			"异常记录的真实 token 口径退化为 estimated。fault_events 同样不再新增。",
	},
	"cmd/gateway/main_v3_wiring.go": {
		Feeds:       "request_logs 的上一轮 outbound → compression.LastOutboundRow → 压缩决策",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "压缩决策的输入——决定发给上游的内容形态。",
		Note:        "无本地写入，但**消费方是压缩决策**，会改变实际发往 provider 的请求体，故判 live。",
	},
	"domains/hooks/goal/history_store.go": {
		Feeds:       "request_logs + bodies → FormatHistoryForPrompt → 拼进 prompt 的历史",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs rl",
		BlastRadius: "拼装进下一次请求 prompt 的历史消息——直接改变发往上游的内容。",
		Note:        "与 main_v3_wiring 同形：无本地写，但输出进入请求体。",
	},
	"domains/streaming/model_alternatives.go": {
		Feeds:       "request_logs_hot 7 天热度 → 返回给客户端的备选模型列表",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "（dormant：无写入；输出是 API 响应内容）",
		Note: "判 not_control_plane 的依据：无任何写入，输出流向响应。" +
			"但它**改变 API 答案**（备选模型列表变空），读端轴上已由 batch4 登记为 silently_empty 语义。",
	},
	"internal/trace/trace.go": {
		Feeds:       "request_logs 的 trace_events → LoadFromPG → 链路详情响应",
		Live:        false,
		Gated:       false,
		Evidence:    "SELECT trace_events, ts FROM request_logs_hot WHERE request_id = $1",
		BlastRadius: "（dormant：读点服务响应；:496 的 UPDATE 是 v1 自身写入，已在 S4 门控内）",
		Note: "读端无下游决策；同文件的 :496 `UPDATE request_logs_hot` 是 v1 内部写，" +
			"已随 S4 门控一并关停，不属本轴。",
	},
	"cmd/gateway/waterfall_db.go": {
		Feeds:       "request_logs_hot → waterfall 响应",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "（dormant：只服务响应）",
		Note:        "无写入。停写后查不到 → errors_out（可接受失败模式），已在读端轴登记。",
	},
	// ── 第四批：2026-10-02 收口，52/52 ────────────────────────────────────
	//
	// 这一批的证据**逐条从源码实取**（脚本打印真实匹配行），不用词宽的
	// `request_logs` 子串——否则「已评估」与「凭印象」就分不开了。
	"domains/analysis/optimizer.go": {
		Feeds:    "request_logs 会话级 token/压缩统计 → INSERT INTO session_optimization_suggestions",
		Live:     true,
		Gated:    false,
		Evidence: "COALESCE((SELECT SUM(COALESCE(cache_read_tokens,0)) FROM request_logs WHERE gw_session_id = ss.session_key), 0)",
		BlastRadius: "`INSERT INTO session_optimization_suggestions`（:232）——优化建议落库，" +
			"其 detect() 规则（如「存在压缩空间」）决定是否产出建议。",
		Note: "会话其余统计来自仍在长的 session_summaries，只有这三个子查询直读 v1。" +
			"⇒ 停写后 cache_read_tokens / compression_strategy / outbound_token_est 恒 0，" +
			"而 request_count 与成本照常有值 ⇒ **建议照常写入、内容静默失真**。",
	},
	"domains/analysis/request_summary.go": {
		Feeds:    "request_logs 会话内请求 → INSERT INTO session_request_summaries（LLM 阶段）",
		Live:     true,
		Gated:    false,
		Evidence: "FROM request_logs",
		BlastRadius: "`INSERT INTO session_request_summaries … ON CONFLICT DO UPDATE`（:174）" +
			"——单请求摘要落库。",
		Note: "与 optimizer 同形：直读 v1 裸表，session 侧无兜底。",
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Feeds:       "bodies 腿取会话首条请求体的 system prompt → 作为总结 LLM 的额外输入 → 写 session_summaries",
		Live:        true,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "与 summarizer.go 共用同一次 `UPDATE session_summaries` 写入（:812）。",
		Note: "**bodies 族的具体实例**：JOIN request_logs_bodies_with_current_month，" +
			"bodies 没有 session 臂 ⇒ 停写后取不到 system prompt，会话摘要**质量**静默下降" +
			"（摘要照常生成，只是少了原始系统提示词这一路输入）。",
	},
	"cmd/gateway/dual_read_validator.go": {
		Feeds:       "v1 ↔ session 对账 → 发布 S4 门控判定（不写服务状态）",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "（dormant：产出的是判定报告，本身不决定凭据/路由/身份）",
		Note: "**它就是 S4 那道门本身**。§8.2 已修掉它的真空为绿（s4_ready ⇔ 写入中 ∧ 扫到过 ∧ 无真漏写）。" +
			"在本轴上它不是「读 v1 决定写」，而是「读两侧做对账」，故判 not_control_plane；" +
			"它的问题归 validator 轴，不归控制面轴。",
	},
	"cmd/gateway/waterfall_by_request.go": {
		Feeds:       "request_id → waterfall 响应（查不到即 404）",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "（dormant：无写入）",
		Note:        "失败模式是 errors_out（404），属可接受：灰度立刻暴露。",
	},
	"cmd/compression-bench/main.go": {
		Feeds:       "历史行 → 离线压缩基准样本",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "（dormant：离线 CLI，无写入，不在服务路径）",
		Note: "样本为空不 Fatal，只打日志 ⇒ 停写后基准结论静默变成「S4 之前」口径。" +
			"不影响在线面，但会让离线基准失去可比性。",
	},
	"cmd/scenario_driver/main.go": {
		Feeds:       "自造流量后测量 v1/bodies 增量",
		Live:        false,
		Gated:       false,
		Evidence:    "SELECT count(*) FROM request_logs_bodies_hot",
		BlastRadius: "（dormant：离线工具，无写入）",
		Note:        "停写后必然测不到（delta=0）→ 硬失败 `Passed=false`，属「工具失效」而非服务退化。",
	},
	"cmd/traffic-replay/main.go": {
		Feeds:       "历史行 → 离线回放样本",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "（dormant：离线 CLI）",
		Note: "注释里的 request_logs 已被扫描器剔除（§8.5 的 40 条注释规则），" +
			"此处证据取自 :133 的真实代码行。",
	},
	"cmd/tools/backfill_session_bodies/main.go": {
		Feeds:       "v1 → 回填 session_bodies（离线迁移工具）",
		Live:        false,
		Gated:       false,
		Evidence:    "query from request_logs_bodies_hot",
		BlastRadius: "（dormant：离线迁移工具，不在服务路径）",
		Note: "⚠️ 但它本身是 v1→session 的迁移工具：S4 之后**它的输入会随时间消失**，" +
			"回填窗口是关闭的。这是迁移排期的约束，不是停写期的退化。",
	},
	"cmd/tools/validate_sessions_v2/loader.go": {
		Feeds:       "v1 与 v2 双读 → 校验报告",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_bodies_hot",
		BlastRadius: "（dormant：离线校验工具）",
		Note:        "LoadV1Turns / LoadV2Turns 成对存在，语义上就是 validator 轴。",
	},
	"db/db.go": {
		Feeds:       "启动期 DDL：ensureRoutingAnalyticsColumns 建/改分析视图",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_hot",
		BlastRadius: "（dormant：改的是 schema/视图定义，不是服务状态）",
		Note: "读点位于迁移 SQL 字符串里（:3169/:3187/:5871）。停写不删表，DDL 行为不变。" +
			"**核实过程的一处自我更正**：我一度以为扫描器把注释当读点，证据是 :77/:181/:224 " +
			"四处 request_logs 全在注释里；逐行核对后确认那四处确实被剔除，真实读点在 3169 等行，" +
			"扫描器无缺陷。",
	},
	"db/probe_views_unified.go": {
		Feeds:       "返回 CREATE OR REPLACE VIEW 的 SQL 文本",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "（dormant：产出视图定义）",
		Note:        "视图的 session 臂覆盖情况由读端表的 pg_get_viewdef 门负责，不在本轴。",
	},
	"domains/sessionforensics/export.go": {
		Feeds:       "v1 → 会话取证导出包",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month rl",
		BlastRadius: "（dormant：导出，不决定服务状态）",
		Note:        "停写后导出内容只剩停写前的行——这是数据保全问题，已在 §8.6 的 641,452 行议题内。",
	},
	"internal/collector/gateway_adapters.go": {
		Feeds:       "PgTrafficReader.Snapshot → TPS/p50/p99/Top20/in-flight 采样",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs_with_current_month",
		BlastRadius: "（dormant：只读快照）",
		Note: "吞错形态确实存在（`if err != nil { return TrafficSnapshot{}, nil }`），" +
			"但停写本身不触发该路径。读 710 ⇒ session 臂继续供数。",
	},
	"storage/sqlite/request_log_store.go": {
		Feeds:       "lite 模式自己的 SQLite request_logs（9 列轻量形态）",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "（dormant：与 PG 侧 S4 门控不同链）",
		Note:        "StorageModeLite 下才被 factory 选中，full 模式走 newPgRequestLogStore。",
	},
	"tests/session_audit/cmd/audit-test/main.go": {
		Feeds:       "抽取测试语料",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "（dormant：测试工具）",
		Note:        "本包在 tests/ 下且文件名非 _test.go，扫描器不排除，故进了清单。",
	},
	"tests/test_popularity_tracker.go": {
		Feeds:       "手工验证 popularity tracker 的脚本",
		Live:        false,
		Gated:       false,
		Evidence:    "FROM request_logs",
		BlastRadius: "（dormant：手工脚本）",
		Note: "它手工查 v1 造热度数据来验证 tracker，与 domains/credentialstate 的 " +
			"dormant 判定是同一件事的两端。",
	},
	"domains/credentialstate/popularity_tracker.go": {
		Feeds: "模型热度 → GetProbeInterval → 探针节奏（10s/2m/10m）",
		Live:  false,
		Gated: false,
		Evidence: "SELECT client_model, COUNT(*) AS request_count\n" +
			"\t\tFROM request_logs_hot\n" +
			"\t\tWHERE created_at > NOW() - INTERVAL '1 hour'",
		BlastRadius: "（dormant，暂无实际写入影响）",
		Note: "**2026-10-02 自我更正**：初判为 live 且列为高危（停写后 refresh 把 " +
			"popularModels 换成空 map，GetProbeInterval 对所有模型回落到 5 分钟默认，" +
			"相对热门模型的 10 秒是 30 倍探测衰减）。逐行核实后**判定为 dormant**，两条独立理由：\n" +
			"  ① main.go:1535 由 LLM_GATEWAY_ENABLE_POPULARITY_TRACKING=true 把关，**默认 false**；\n" +
			"  ② 它唯一的输出 Manager.GetRecommendedProbeInterval **全仓无生产调用方**（仅定义）。\n" +
			"登记为 dormant 而不是删掉，是为了防日后有人把消费方接上——那时本条会立刻变 live。",
	},
}

// controlPlaneUnreviewed 是分母里**还没查**的文件。
//
// 与 stopWriteClassification 用同一套纪律：未查必须显式登记为 cpUnevaluated，
// 「表里没有」与「已确认无关」是两件事，只有前者能被门看见。
func controlPlaneUnreviewed() []string {
	var todo []string
	for file := range requestLogsReadInventory {
		if strings.HasPrefix(file, "admin/") {
			continue // 展示面，归 stopWriteClassification 那条轴
		}
		v, ok := requestLogsControlPlaneReaders[file]
		if !ok || v.Feeds == "" {
			todo = append(todo, file)
		}
	}
	sort.Strings(todo)
	return todo
}

// TestRequestLogsControlPlaneKnownEntriesAreReal 是常跑守卫：已登记的每一条都必须
// 锚在真实代码上，且「活的 + 门控外的」必须写清楚它能改动什么。
//
// 这道门针对的是本次发现的那个坐标系漏洞：如果允许把一条活的、未被门控的读点
// 登记成「读端 silently_empty」就算交差，下一批就会照着那张表继续填，把控制面
// 依赖重新藏起来。
func TestRequestLogsControlPlaneKnownEntriesAreReal(t *testing.T) {
	root := repoRootFromCaller(t)
	for file, v := range requestLogsControlPlaneReaders {
		if _, ok := requestLogsReadInventory[file]; !ok {
			t.Errorf("%s: 不在 requestLogsReadInventory 里——控制面表不得凭空多出文件", file)
			continue
		}
		if strings.TrimSpace(v.Feeds) == "" {
			t.Errorf("%s: Feeds 为空——控制面读点必须写明它的输出决定什么", file)
			continue
		}
		verdict := cpNotControlPlane
		switch {
		case !v.Live:
			verdict = cpControlPlaneDormant
		case !v.Gated:
			verdict = cpControlPlaneLive
		}
		if _, ok := controlPlaneVerdicts[verdict]; !ok {
			t.Errorf("%s: 未知判定 %q", file, verdict)
		}
		if strings.TrimSpace(v.Evidence) == "" {
			t.Errorf("%s: Evidence 为空", file)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		if !strings.Contains(string(raw), v.Evidence) {
			t.Errorf("%s: Evidence 在该文件中不存在：\n  %q", file, v.Evidence)
		}
		// 关键约束：活的且门控外的读点，必须点名它授权/改变的写入。
		// 少了这一条，「活」就只是一个形容词。
		if v.Live && !v.Gated && strings.TrimSpace(v.BlastRadius) == "" {
			t.Errorf("%s: 判定为 %s（活 + 门控外）却没有 BlastRadius——\n"+
				"必须写清它具体授权或改变了哪个写入，否则「活」不可核。",
				file, verdict)
		}
	}
}
