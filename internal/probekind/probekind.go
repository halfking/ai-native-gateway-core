// Package probekind 是「什么算探测」的唯一真值源。
//
// # 为什么有这个包
//
// 2026-10-23 审计（会话 mvs_64ddb526ee11405aae54d67e7c4d6161）定位到：
// 仓内有 **4 处**独立代码决定一个请求算不算探测，历史上**没有同源**：
//
//	1. DB CHECK 闸门       middleware/origin_mw.go       isValidOriginStage
//	2. 侧表 is_probe 列    domains/streaming/context_attrs.go isProbeOriginStage
//	3. 实时拓扑 dashboard  admin/probe_request_info.go    extractProbeInfo
//	4. stats/board 排除    domains/stats/minute_entry.go  IsProbeTraffic
//
// 4 处各自维护一份清单 ⇒ 漂移。已实证的一次漂移：
// `systemOwnerFallbackStage`（middleware）把 self-check-worker /
// model-quality-worker 映射成探测 stage，但 stats 端的 actor 白名单
// 没有这两个 actor ⇒ 这两类 worker 写出的行 dashboard 认 probe、stats 不认
// ⇒ 板上「总请求数」持续虚高。
//
// 本包把三样东西收敛成一份规范数据，四处都从这里读：
//
//   - 探测 origin_stage 集合（ProbeStages）
//   - 探测 origin_actor 集合（ProbeActors，由 StageWriters 派生）
//   - 另外两条与 stage 无关的探测标记（task_type / quality_flags）
//
// # 两条硬约束（本包是 stdlib-only 的原因）
//
// ① **不得依赖 domains/hooks/observability/telemetry**。telemetry 在
//    middleware / domains / admin 的下层，被它依赖会把分类逻辑拖进依赖图
//    的底层，让"哪一层定义探测"这个问题再次散开。本包只吃基本类型，
//    调用方自己拆字段。
//
// ② **不得依赖 bg**。bg import domains/stats，domains/stats 要用本包，
//    本包再 import bg 就是循环。
//
// # 一致性由测试保证，不由注释保证
//
// 本包的规范数据是**字面量**（不是从代码推导出来的），因此必须由
// probekind_test.go 的自检门持续校验：
//
//   - StageWriters 的每个 stage ∈ ProbeStages
//   - StageWriters 的每个 actor ∈ ProbeActors（由构造保证）
//   - ProbeActors 覆盖**全部真实写入点**（门会扫全仓 grep 出
//     `X-LLM-Origin-Actor` 的取值，逐一比对）
//   - ProbeStages ⊆ DB CHECK 的合法集合（门会读 migration 341 的 SQL）
//   - ProbeActors == stats 端 SQL 谓词里的 actor 列表（门会读 bg 源码）
//
// 任何一处新增探测器而忘了改本包 ⇒ 上述门红。
package probekind

import (
	"sort"
	"strings"
)

// ProbeStages 是规范探测 origin_stage 集合。
//
// 它同时是：DB CHECK 闸门的探测子集、侧表 is_probe 的判定集、
// 实时拓扑 dashboard pill 的判定集。
//
// ⚠ 2026-10-23 审计订正：本集**不等于**"当前有写入者的 stage 集合"。
// `system_health` / `probe_direct` / `probe_v2` / `model_probe` /
// `passive_probe` / `manual` 在本仓**没有写入点**（system_health 的
// bg/system_health.go 是只读扫描器，不发网关请求），但它们是 **domain 链的
// migration 341**（sql/migrations/domain/341_probe_origin_and_node_probe.sql）
// 已落库的 CHECK 合法值、侧表已承认的值 ⇒ 必须保留，否则历史行的
// is_probe 语义会被改写。
//
// ★ 2026-10-28（R175 轮）订正：上面那句「migration 341」**指错了文件** ——
// 今天的 startup 链里 `341_` 这个号被
// `sql/migrations/startup/341_hot_table_independence.sql` 占着 ⇒
// 「migration 341」会让人以为说的是它。⇒ 改为写全「domain 链的 341」+ 路径。
//
// ★★ 另一处更硬的订正：v1 族的 CHECK 随 890 的 v1 族退役**真删除**，而
// **会话族当时并未重建** ⇒ 从 890 到迁移 914 之间，origin_stage 在 DB 层
// **完全不被拒**，本集只是 Go 侧闸门的唯一依据。
// 迁移 914（sql/migrations/startup/914_session_turns_origin_stage_check.sql）
// 把同一份集合补回到 session_turns / session_turns_hot 的 DB CHECK 上
// ⇒ 两道闸门重新一致，且由
// sql/migrations/startup/migration_914_session_turns_origin_stage_check_test.go
// 逐项对拍（改本集必须同步改那条迁移，否则门会红）。
//
// 反过来，dashboard 旧实现里多出的 `direct` / `gateway` / `scheduled` /
// `probe_gateway` / `probe_scheduled` 五个**裸值/别名**不在本集里 ——
// 它们从来写不进 DB（middleware/origin_mw.go 的 isValidOriginStage 闸门
// 会挡掉），是死防御代码。2026-10-23 已从 dashboard 侧删除。
var ProbeStages = []string{
	"self_check",   // 自检 / 凭据自检 / 模型质量打分 / mock 探针
	"node_probe",   // 节点探针 / 主动探测服务
	"system_health", // 系统健康监控（本仓无写入点，见上方订正）
	"probe_direct", // legacy 主动探测
	"probe_v2",     // v2 主动探测
	"model_probe",  // 模型探针
	"passive_probe", // 被动探针监听
	"manual",       // 手动触发
}

// Writer 描述一个探测写入者。
type Writer struct {
	// Stage 是它声明（或应当落库）的 origin_stage。
	Stage string
	// ViaHTTP 为真表示它通过 HTTP 头的 X-LLM-Origin-Stage /
	// X-LLM-Origin-Actor 声明来源 ⇒ 必须经过 middleware 的 OriginMiddleware
	// 信任判定；为假表示它直接构造 _request_logs 行，绕过中间件。
	ViaHTTP bool
}

// Writers 是「哪个 origin_actor 写哪种探测 stage」的规范表。
//
// 它是 ProbeActors 的**唯一来源** ⇒ 不可能与 actor 集漂移。
//
// 两类写入者都在表里：
//   - HTTP 头写入者：经 middleware 的 OriginMiddleware 信任判定
//     （X-LLM-Origin-Stage / X-LLM-Origin-Actor）。
//   - telemetry 直写者：绕过 OriginMiddleware，直接构造 _request_logs 行。
//
// probekind_test.go 的 TestProbeActorsCoverEveryRealWriter 会扫全仓
// `X-LLM-Origin-Actor` 的取值，验证没有写入者漏在这个表外面。
var Writers = map[string]Writer{
	// ── HTTP 头写入者 ──
	"self-check-worker":           {Stage: "self_check", ViaHTTP: true}, // bg/self_check_worker.go
	"credential-selfcheck-worker": {Stage: "self_check", ViaHTTP: true}, // bg/credential_selfcheck.go
	"model-quality-worker":        {Stage: "self_check", ViaHTTP: true}, // domains/modelquality/invoker.go
	"model-credibility-worker":    {Stage: "self_check", ViaHTTP: true}, // domains/modelcredibility/probe.go
	"mock-probe-client":           {Stage: "self_check", ViaHTTP: true}, // internal/mockprobe/client.go
	"node-probe-worker":           {Stage: "node_probe", ViaHTTP: true}, // bg/node_probe.go
	"probe-service":               {Stage: "node_probe", ViaHTTP: true}, // bg/active_probe_executor.go
	// ── telemetry 直写者 ──
	"active-probe-worker": {Stage: "node_probe"}, // bg/active_probe_emitter.go
}

// ProbeActors 是规范探测 origin_actor 集合，**由 Writers 派生**
// （排序后冻结，避免 map 迭代序不定导致 SQL 字符串漂移）。
//
// 用途：当 origin_stage 不可读时（stats 的 113 列视图没有这一列），
// 用 actor 作为等价代理。代理的保真度依赖 Writers 覆盖全部写入者
// —— 漏一个 ⇒ 该写入者的行会被 stats 当作业务流量（这正是 2026-10-23
// 修掉的 B' 缺陷：self-check-worker / model-quality-worker 漏在白名单外）。
var ProbeActors = sortedWriterKeys()

// HTTPStageActors 是「HTTP 头写入者」按 stage 分组的反向索引
// （stage → 该 stage 的合法 actor 集合）。
//
// 它是 middleware 的 globalAuthStageActorPairs 配对表的**唯一来源** ——
// 2026-10-23 之前那张表与 systemOwnerFallbackStage 是同一份数据的两个
// 方向（stage→actor 与 actor→stage），各写一份 ⇒ 必然漂移（B' 即其后果）。
// 现在只保留这一份，两个方向都从它派生。
func HTTPStageActors() map[string][]string {
	out := map[string][]string{}
	for actor, w := range Writers {
		if !w.ViaHTTP {
			continue
		}
		out[w.Stage] = append(out[w.Stage], actor)
	}
	for stage := range out {
		sort.Strings(out[stage])
	}
	return out
}

// BusinessStage 是非探测 origin_stage 的规范取值。
const BusinessStage = "business"

// ProbeTaskType 是主动探测行的 task_type 规范取值。
const ProbeTaskType = "probe_triggered"

// ProbeQualityFlag 是探测行 quality_flags 里的规范标记。
const ProbeQualityFlag = "probe"

// ProbeAttemptPrefix 是 quality_flags 里携带轮次的标记前缀（attempt_N）。
const ProbeAttemptPrefix = "attempt_"

// IsProbeStage 判定一个 origin_stage 是否是探测 stage。
func IsProbeStage(stage string) bool {
	return contains(ProbeStages, stage)
}

// IsProbeActor 判定一个 origin_actor 是否是探测 actor。
func IsProbeActor(actor string) bool {
	return contains(ProbeActors, strings.TrimSpace(actor))
}

// IsProbeTaskType 判定一个 task_type 是否是探测标记。
func IsProbeTaskType(taskType string) bool {
	return strings.TrimSpace(taskType) == ProbeTaskType
}

// IsProbeQualityFlags 判定一组 quality_flags 是否带探测标记。
func IsProbeQualityFlags(flags []string) bool {
	for _, f := range flags {
		if f == ProbeQualityFlag {
			return true
		}
	}
	return false
}

// ProbeAttemptFromQualityFlags 从 quality_flags 里解析探测轮次（attempt_N）。
// 没有则返回 0。
func ProbeAttemptFromQualityFlags(flags []string) int {
	attempt := 0
	for _, f := range flags {
		if !strings.HasPrefix(f, ProbeAttemptPrefix) {
			continue
		}
		n := 0
		for _, r := range f[len(ProbeAttemptPrefix):] {
			if r < '0' || r > '9' {
				n = -1
				break
			}
			n = n*10 + int(r-'0')
		}
		if n > attempt {
			attempt = n
		}
	}
	return attempt
}

// IsProbeMarkers 判定一条行是否带「与 origin_stage 无关」的探测标记。
//
// 这三条（quality_flags / task_type / origin_actor）是 stats 端
// 113 列视图**能读到**的全部标记 ⇒ SQL 谓词与 Go 活体写入器都用它，
// 保证两个写入器对同一批行给出一致判定（它们喂同一个 ON CONFLICT 键）。
//
// ⚠ 刻意**不含** origin_stage：视图没有这一列，加上去会让 SQL 臂与 Go 臂
// 对同一行给出不同判定（Go 臂能读到 stage）⇒ 两臂写进同一个 rollup 键时
// 互相覆盖。origin_stage 的判定在 dashboard / 侧表两处进行，见
// IsProbeStage。
func IsProbeMarkers(taskType string, qualityFlags []string, originActor string) bool {
	if IsProbeQualityFlags(qualityFlags) {
		return true
	}
	if IsProbeTaskType(taskType) {
		return true
	}
	if IsProbeActor(originActor) {
		return true
	}
	return false
}

// IsProbe 是「这条行算不算探测」的**完整**判定 —— 能读到 origin_stage 的
// 表面（实时拓扑 dashboard / 侧表 request_context_attrs）用它。
//
// 之所以是 stage 路径 ∪ markers 路径（而不是只看其中之一）：每一个真实
// 探测写入者都会**同时**留下 stage 与 actor 两个痕迹（见 Writers 表），
// 所以任一路径都能认出它；两条路径都认不出 ⇒ 它确实不是探测。
//
// markers 路径的存在还有一个具体收益：origin_stage 丢失的行（早期 row /
// 早退路径 / 中间件降级）仍能靠 actor 被认出 ⇒ 不再漏标。
//
// ⚠ stats 端**不用**这个函数（它只用 IsProbeMarkers）—— 原因见
// IsProbeMarkers 的注释：SQL 臂读不到 origin_stage，两个写入器必须用
// 同一套标记才能对同一行给出一致判定。
func IsProbe(originStage, taskType string, qualityFlags []string, originActor string) bool {
	if IsProbeStage(originStage) {
		return true
	}
	return IsProbeMarkers(taskType, qualityFlags, originActor)
}

// ValidStages 是 DB CHECK 允许的全部 origin_stage 取值（探测 ∪ business）。
func ValidStages() []string {
	out := make([]string, 0, len(ProbeStages)+1)
	out = append(out, ProbeStages...)
	out = append(out, BusinessStage)
	return out
}

// IsValidStage 判定一个 origin_stage 是否能落进 session_turns.origin_stage。
//
// DB CHECK 用的就是这份集合 —— 落库前的闸门与落库后的约束必须一致，
// 否则会出现"中间件认、DB 拒"的行。
//
// ★ 2026-10-28（R175 轮）订正：本注释此前写的是「能落进
// `_request_logs.origin_stage`」且「DB CHECK（migration 341）用的就是这份集合」
// ——**两处都指错了**：
//   ① `_request_logs` 已随 890 的 v1 族退役**真删除** ⇒ 该表不存在；
//   ② v1 族的 `request_logs_origin_stage_check` 随那次 DROP 一起消失，
//      会话族**从未重建** ⇒ 那段时间 DB 侧**根本没有 CHECK**。
// ⇒ 现在的 DB CHECK 是**迁移 914** 建的：
//   sql/migrations/startup/914_session_turns_origin_stage_check.sql
//   （父表 session_turns + hot 表 session_turns_hot 两侧，允许集 = ValidStages()）。
// ⇒ 两边由 sql/migrations/startup/migration_914_session_turns_origin_stage_check_test.go
//   逐项对拍：改 ValidStages() 必须同步改那条迁移，否则门会红。
func IsValidStage(stage string) bool {
	return contains(ValidStages(), stage)
}

// SQLActorList 把 ProbeActors 渲染成 SQL IN 列表：'a', 'b', 'c'。
//
// 给 stats 端的 SQL 谓词用 —— actor 列表从此只有一份定义，
// 不再由 Go 侧和 SQL 侧各写一遍。
func SQLActorList() string {
	quoted := make([]string, 0, len(ProbeActors))
	for _, a := range ProbeActors {
		quoted = append(quoted, "'"+a+"'")
	}
	return strings.Join(quoted, ", ")
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func sortedWriterKeys() []string {
	out := make([]string, 0, len(Writers))
	for k := range Writers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
