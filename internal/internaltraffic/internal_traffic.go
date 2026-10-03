// Package internaltraffic — 「什么是网关内部/合成流量」的**唯一事实源**。
//
// # 这个包存在的理由：一个问题、三份手抄的答案
//
// 判定「这一行是不是网关自己发起的内部流量」这件事，在本仓曾有**三份**各自
// 硬编码的答案，而且它们分处三个互不依赖的包：
//
//	域名/包                          形态            实际内容
//	telemetry/internal_loopback.go   Go，4 臂        is_auto_request ∧ (request_type ∨ actor ∨ taskless)
//	autoroute/shadow_actors.go       Go + SQL，2 臂   actor ∈ 3 生成器 ∨ actor LIKE 'goal-%'
//	db/request_logs_view_schema.go   SQL，4 臂        同 telemetry 的 4 臂（手抄进 CASE 表达式）
//
// 三者之间**没有任何依赖边**，所以「改了一处」与「忘了另两处」在编译期完全等价。
// 审计 §9.58.3 已经记下这个家族（两份名单互不相认）；本包把「同一件事有多份真相源」
// 从**注释里的提醒**变成**结构上不可能**：三处都从这里取名字。
//
// # 刻意不合并成「一个判据」
//
// ⚠ **三份判据的臂集本来就不同，这不是 bug，是三种不同的用途**：
//
//	actor ∪ goal-%          ——「这个 actor 是合成 actor」，用于聚合面（cohort / LATERAL /
//	                            work-type 统计）排除合成轮次。**不含** taskless 臂，
//	                            因为业务 auto 轮次也常常没有 task_type，按 taskless 排除
//	                            会把真业务一起排除掉。
//	4 臂（is_auto ∧ …）      ——「这是不是标题/摘要回环」，用于 session_turns 镜像的排除与
//	                            双读对账的分类。它**必须**含 taskless 兜底臂。
//
// 把它们强行合并成「一个谓词」会让两处用途同时变错（聚合面开始吃掉业务轮次，
// 或镜像开始放行生成器行）。⇒ 本包统一的是**事实（名字与前缀）**，
// 而**臂集由调用方显式选择**，并且 `ClassifyInternalLoopback` 把 4 臂的判定
// 做成带名字的返回值，让「哪条臂命中的」可被检查而不只是拿到一个 bool。
//
// # 不包含什么（以及为什么）
//
// **不**包含 `middleware/origin_mw.go` 的 `trustedOriginOwners` /
// `globalAuthStageActorPairs` / `systemOwnerFallbackStage` 三份名单。
// 它们的判定是**安全**问题（「这个调用者申报的 X-LLM-Origin-Stage 值可不可信」），
// 与「这一行是不是内部流量」不是同一个问题；把它们并进来会让一个查询侧的
// 分类器获得改写 header 信任判定的影响力。
// 那三份名单对两个生成器命中 0（审计 §9.58.3）这个事实，由
// `middleware` 侧的差异门负责暴露，不在本包的职责内。
package internaltraffic

import "strings"

// GoalShadowActorPrefix 是 goal 影子轮次 actor 的前缀
// （response_interceptor_helpers.followUpSourceActor 写入的
// goal-continue / goal-model-switch / goal-audit）。
//
// 来源：原 autoroute/shadow_actors.go 的 `goalShadowActorPrefix`。
const GoalShadowActorPrefix = "goal-"

// generatorActors 是 X-Gw-Is-Auto 回环的写入方（标题/摘要自调用）。
//
// 来源：原 telemetry/internal_loopback.go:35 与 autoroute/shadow_actors.go:26
// 的 `internalLoopbackActors` —— **两份一模一样的字面量**。
// 顺序即 SQL `IN (...)` 里的顺序，保持原样以免让渲染出的 SQL 文本发生
// 无意义的字节变化（那种变化会让所有以文本为键的门同时变红，掩盖真正的变化）。
var generatorActors = []string{
	"auto-title-generator",
	"auto-summary-generator",
	"session-summary",
}

// generatorRequestTypes 是标题/摘要生成请求的 request_type 取值。
//
// 来源：原 telemetry/internal_loopback.go:29。
var generatorRequestTypes = []string{
	"title_gen",
	"summary",
}

// GeneratorActors 返回生成器 actor 名的副本。
//
// 返回副本而不是切片本身：调用方若能改动底层切片，就等于又开了一份可写的真相源。
func GeneratorActors() []string {
	out := make([]string, len(generatorActors))
	copy(out, generatorActors)
	return out
}

// GeneratorRequestTypes 返回生成器 request_type 取值的副本。
func GeneratorRequestTypes() []string {
	out := make([]string, len(generatorRequestTypes))
	copy(out, generatorRequestTypes)
	return out
}

// IsGeneratorActor 报告 actor 是否是标题/摘要生成器。
//
// 两侧（Go 判据与 SQL 谓词）都**先 TrimSpace** 再比对。这不是可有可无的：
// SQL 侧的旧写法用 `COALESCE(col,”) NOT IN (...)`，若入口处带了空白，
// 两边会给出不同答案，而「不匹配」的方向是把它当成业务流量放进去。
// 真正的 TrimSpace 由写入方保证（见 autoroute/shadow_actors.go 的 R39 注记）。
func IsGeneratorActor(actor string) bool {
	a := strings.TrimSpace(actor)
	for _, g := range generatorActors {
		if a == g {
			return true
		}
	}
	return false
}

// IsGeneratorRequestType 报告 request_type 是否是标题/摘要生成请求。
func IsGeneratorRequestType(requestType string) bool {
	r := strings.TrimSpace(requestType)
	for _, g := range generatorRequestTypes {
		if r == g {
			return true
		}
	}
	return false
}

// IsSyntheticActor 报告 origin_actor 是否是网关合成轮次。
// 空 actor（普通用户流量）永远不是合成流量。
//
// 逐字保留原 autoroute.IsSyntheticActor 的语义（含「空串先返回 false」这个
// 显式分支：它与「空串不以 goal- 开头」结果相同，但保留分支是为了让
// 「空 actor 不是合成 actor」这条决定在代码里是可见的，而不是靠推理得出）。
func IsSyntheticActor(originActor string) bool {
	a := strings.TrimSpace(originActor)
	if a == "" {
		return false
	}
	if IsGeneratorActor(a) {
		return true
	}
	return strings.HasPrefix(a, GoalShadowActorPrefix)
}

// InternalLoopbackArm 是 4 臂判据里**命中的是哪一条**。
//
// 为什么要有它而不只是 bool：审计 §9.73.3 的 252 实测发现「actor 臂」与
// 「taskless 兜底臂」在生产上命中**完全相同的 3,200 行**（对称差 0/0/3200），
// 即兜底臂当前零独立贡献。只返回 bool 的话，这个事实没有任何地方能记；
// 返回命中的臂，「哪条臂是活的」就变成可断言的。
type InternalLoopbackArm string

const (
	// ArmNone：不是内部回环。
	ArmNone InternalLoopbackArm = ""
	// ArmRequestType：request_type ∈ {title_gen, summary}。
	ArmRequestType InternalLoopbackArm = "request_type"
	// ArmActor：origin_actor ∈ 三个生成器。
	ArmActor InternalLoopbackArm = "origin_actor"
	// ArmTaskless：task_type 缺失/空（taskless auto entry 兜底）。
	ArmTaskless InternalLoopbackArm = "task_type"
)

// ClassifyInternalLoopback 判定一行 auto 记录是不是标题/摘要内部回环，并返回
// 命中的臂。
//
// 逐字保留原 telemetry.IsInternalAutoEntry 的判定顺序与语义：
//
//	is_auto_request 为假或为 NULL ⇒ 不是内部回环（NULL 不算内部）
//	非 nil 的 request_type 去空白后 ∈ {title_gen, summary} ⇒ 是
//	非 nil 的 origin_actor  去空白后 ∈ 三个生成器        ⇒ 是
//	否则：task_type 为 nil 或去空白后为空 ⇒ 是
//
// ⚠ **「非 nil」这个条件不能省**。request_type 为非 nil 但内容是空串时，
// 去空白后是 ""，不在集合里，于是继续往下走——与 nil 的结果**不同**
// （nil 会落到 taskless 臂，空串会落到下一条臂）。把这个条件写成
// `strings.TrimSpace(COALESCE(x,”)) ∈ set` 会把两者合并，于是 SQL 侧与
// Go 侧在「非 nil 空串」这一格上分歧。
//
// 三个指针参数为 nil 与「指向空串」是**不同的输入**，本函数刻意让调用方
// 能区分它们。
func ClassifyInternalLoopback(isAuto bool, requestType, originActor, taskType *string) InternalLoopbackArm {
	if !isAuto {
		return ArmNone
	}
	if requestType != nil && IsGeneratorRequestType(*requestType) {
		return ArmRequestType
	}
	if originActor != nil && IsGeneratorActor(*originActor) {
		return ArmActor
	}
	if taskType == nil || strings.TrimSpace(*taskType) == "" {
		return ArmTaskless
	}
	return ArmNone
}

// IsInternalLoopback 是 ClassifyInternalLoopback 的 bool 形态。
func IsInternalLoopback(isAuto bool, requestType, originActor, taskType *string) bool {
	return ClassifyInternalLoopback(isAuto, requestType, originActor, taskType) != ArmNone
}

// sqlStringList 渲染 SQL 字符串列表，逐字保持既有拼写（',' 分隔、**无空格**）。
//
// 「无空格」不是风格问题：既有 SQL 文本是以它为键被几道门检查的
// （如 bg/auto_route_settle_source_gate_test.go 钉住 `autoroute.SQLExcludeSyntheticActors`
// 出现在调用点），渲染成 'a', 'b' 会让那些门在没有语义变化的情况下变红，
// 从而把「真的变了」淹没在噪声里。
func sqlStringList(items []string) string {
	return "'" + strings.Join(items, "','") + "'"
}

// 下面两个常量是给**const 表达式**用的 SQL 列表字面量。
//
// 为什么需要它们：`db.MirrorDriftClassSQL` 与 `cmd/gateway` 的
// `const mirrorDriftClassSQL = db.MirrorDriftClassSQL` 都是 **const**
// （const 表达式里不能调函数），所以那条 SQL 无法通过 SQLInternalLoopbackPredicate
// 生成，只能拼接。⇒ 名字在 Go 切片与 SQL 字面量里各出现一次。
//
// 这个重复由 init + TestSQLListConstantsMatchTheGoSets 双向钉住：
// 两者不一致就在**进程启动时**panic，而不是等某道以 SQL 文本为键的门变红。
const (
	// GeneratorActorsSQLList 必须等于 sqlStringList(generatorActors)。
	GeneratorActorsSQLList = "'auto-title-generator','auto-summary-generator','session-summary'"
	// GeneratorRequestTypesSQLList 必须等于 sqlStringList(generatorRequestTypes)。
	GeneratorRequestTypesSQLList = "'title_gen','summary'"
)

func init() {
	// 启动即校验：const 字面量与 Go 集合是同一份事实的两个投影，
	// 任何一边被单独改掉都会在这里立刻炸。
	if got := sqlStringList(generatorActors); got != GeneratorActorsSQLList {
		panic("internaltraffic: GeneratorActorsSQLList 与 Go 集合不一致：" +
			"字面量=" + GeneratorActorsSQLList + " 生成=" + got +
			"（改了其中一边必须同时改另一边）")
	}
	if got := sqlStringList(generatorRequestTypes); got != GeneratorRequestTypesSQLList {
		panic("internaltraffic: GeneratorRequestTypesSQLList 与 Go 集合不一致：" +
			"字面量=" + GeneratorRequestTypesSQLList + " 生成=" + got +
			"（改了其中一边必须同时改另一边）")
	}
}

// SQLExcludeSyntheticActors 返回排除合成轮次的 SQL 谓词
// （可安全追加在 WHERE 之后 —— 每个条件都以 AND 开头）。
// alias 可为 "" 表示不带表别名的列引用。
//
// ★ **渲染结果与原 autoroute.SQLExcludeSyntheticActors 逐字相同**（包括
// `LIKE 'goal-%'` 与 `NOT IN (...)` 的先后顺序与分隔符），
// 由 TestSQLRenderingsAreByteIdenticalToThePreexistingText 钉住。
func SQLExcludeSyntheticActors(alias string) string {
	col := "origin_actor"
	if alias != "" {
		col = alias + ".origin_actor"
	}
	return " AND COALESCE(" + col + ", '') NOT LIKE '" + GoalShadowActorPrefix + "%'" +
		" AND COALESCE(" + col + ", '') NOT IN (" + sqlStringList(generatorActors) + ")"
}

// SQLInternalLoopbackPredicate 返回 4 臂「内部回环」判定表达式（**不带**外层
// WHEN，是可嵌进 CASE 的布尔表达式）。
//
// 与 db.MirrorDriftClassSQL 里那只手抄的 internal_loopback 臂逐字同义：
// 同样的三列、同样的 TRIM/COALESCE、同样的「非 nil 才比对」语义
// （SQL 侧 `TRIM(COALESCE(col,”))` 对应 Go 侧的「nil 不参与该臂」——
// 差别只在一格：Go 侧 task_type 为 nil 时命中 taskless 臂，SQL 侧
// `TRIM(COALESCE(task_type,”)) = ”` 对 nil 与空串**都**命中，这是既有行为，
// 本包不改动它；db 侧那处与 Go 侧的分歧由既有的 parity 测试负责，
// 不由本包悄悄"修好"）。
func SQLInternalLoopbackPredicate(alias string) string {
	p := func(col string) string {
		if alias != "" {
			return alias + "." + col
		}
		return col
	}
	return "(   TRIM(COALESCE(" + p("request_type") + ", '')) IN (" + sqlStringList(generatorRequestTypes) + ")" +
		" OR TRIM(COALESCE(" + p("origin_actor") + ", '')) IN (" + sqlStringList(generatorActors) + ")" +
		" OR TRIM(COALESCE(" + p("task_type") + ", '')) = '')"
}
