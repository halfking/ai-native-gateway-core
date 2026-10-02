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
