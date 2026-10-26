// Package pipelinev2 —— **v1 / v2 两套路径并存期的判据与差异清单**（P2-1）。
//
// # 这道改造补的是哪块缺口
//
// 计划书 §6-P2-1 的原话：
//
//	「**v2 pipeline 的默认关** —— 把『默认关』当**未完成**而不是『关闭』：
//	给出切换判据（哪些能力必须在 v2 里、哪些可留 v1），并明确『两套路径并存』
//	期间的**行为差异清单**」
//
// ★ 也就是说：P2-1 要的不是「把开关打开」，而是**把「为什么还没打开」写清楚**，
// 并且让这个「还没打开」**可被机器校验**（不是文档里的一段话）。
//
// # 为什么判据与差异要写在【代码】里
//
// 本仓反复吃过同一类亏：**「声明与实现不同源」**。
// 若把判据写在 Markdown 里、差异写在表格里，那么：
//
//	① 判据会与 `cmd/gateway/main_pipeline.go` 的实际声明**悄悄分家**；
//	② 差异清单里的「每条差异有测试覆盖」会退化成**一句承诺**。
//
// ⇒ 所以本包把两样东西都做成**代码即真值源**：
//
//   - `Criterion`：切换判据（哪些能力**必须**在 v2 / **必须**留 v1 / **可以**留 v1），
//     每条带 `Evidence`（★ 必须在生产文件里**逐字出现**的句子 ⇒ 判据不能凭空写）；
//   - `Difference`：并存期的**行为差异**，每条带 `TestName`
//     （★ 那条**真实存在**的测试 ⇒ 「每条差异有测试覆盖」变成机器可校验）。
//
// # 门
//
// `scripts/ursmcheck/magpie_p2_v2_criteria_test.go`：
//
//	① 判据清单非空、字段齐全、每条的 `Evidence` 真的出现在生产文件里；
//	② 差异清单非空、字段齐全、每条的 `TestName` 在仓内**真实存在**；
//	③ 两份清单的 ID 各自唯一；
//	④ 开关的**默认关**与「阶段错误不阻断」这两条代码事实仍然成立。
package pipelinev2

// Requirement 是「某项能力在切换到 v2 时的归属要求」。
type Requirement string

const (
	// MustStayInV1：★ **必须**留在 v1（v2 不得接管）。
	MustStayInV1 Requirement = "must_stay_in_v1"
	// MustBeInV2：★ **必须**在 v2 里（v1 缺失 ⇒ 切换前必须补齐）。
	MustBeInV2 Requirement = "must_be_in_v2"
	// MayStayInV1：**可以**留在 v1（不是「不该」，是「不必」）⇒
	// ★ 这类是「可优化」而不是「阻塞项」—— 措辞上刻意区分开。
	MayStayInV1 Requirement = "may_stay_in_v1"
)

// EvidenceFile 是判据依据默认所在的生产文件。
const EvidenceFile = "cmd/gateway/main_pipeline.go"

// Criterion 是一条**切换判据**。
type Criterion struct {
	// ID 唯一编号（如 C01）。
	ID string
	// Capability 是这项能力的名字（与 Evidence 里的措辞对应）。
	Capability string
	// Requirement 是归属要求。
	Requirement Requirement
	// Reason 写明「为什么是这个要求」。
	Reason string
	// Evidence 是**必须在 EvidenceFile 里逐字出现**的句子。
	//
	// ★ 这条约束是本判据表的**承重墙**：它保证判据不是「拍脑袋写的分类」，
	// 而是**代码自己已经声明过的事实** ⇒ 判据与实现不会悄悄分家。
	Evidence string
	// EvidenceFile 覆盖默认文件（空 ⇒ EvidenceFile）。
	EvidenceFile string
}

// criteria 是判据表（单一真值源）。
var criteria = []Criterion{
	{
		ID:          "C01",
		Capability:  "auth",
		Requirement: MustStayInV1,
		Reason: "v1 的 chatHandler 是认证的 source of truth；v2 只**包一层**、" +
			"不接管认证 ⇒ 切换到 v2 不改变认证语义。",
		Evidence: "source of truth for v1 auth",
	},
	{
		ID:          "C02",
		Capability:  "key verify",
		Requirement: MustStayInV1,
		Reason:      "同上（凭据校验属 v1 的 source of truth）。",
		Evidence:    "key verify, model policy",
	},
	{
		ID:          "C03",
		Capability:  "model policy",
		Requirement: MustStayInV1,
		Reason:      "同上（模型策略属 v1 的 source of truth）。",
		Evidence:    "model policy",
	},
	{
		ID:          "C04",
		Capability:  "request WAL",
		Requirement: MustStayInV1,
		Reason: "请求 WAL 的写入方在 v1 ⇒ v2 的 pipeline 不重复写，" +
			"否则**同一个请求两条审计链**（源码里有过一次 request_id 分叉的修复记录）。",
		Evidence: "request WAL, telemetry",
	},
	{
		ID:          "C05",
		Capability:  "telemetry",
		Requirement: MustStayInV1,
		Reason:      "同 C04（遥测由 v1 负责，v2 不重复计数）。",
		Evidence:    "telemetry",
	},
	{
		ID:          "C06",
		Capability:  "sticky cache",
		Requirement: MustStayInV1,
		Reason:      "同 C04（粘性缓存由 v1 负责）。",
		Evidence:    "sticky cache",
	},
	{
		ID:          "C07",
		Capability:  "the LLM call",
		Requirement: MustStayInV1,
		Reason: "★ **上游调用本身必须留在 v1** —— 这是 v2 包装器的设计前提：" +
			"v2 只做 preflight/postflight，真正的调用由 v1 执行。",
		Evidence: "DELEGATES the LLM call",
	},
	{
		ID:          "C08",
		Capability:  "transformation stage",
		Requirement: MayStayInV1,
		Reason: "★ 骨架的 `transform` 阶段在 v2 包装器里**被刻意省略**" +
			"（prometheus 重复注册的规避）⇒ ⇒ 它**不是**切换的前置条件，" +
			"但它也意味着「v2 与骨架的 16 个阶段并非一一对应」⇒ 切换前需要一份阶段对照表。",
		Evidence: "transformation stage is omitted",
	},
}

// Criteria 返回判据表（只读副本）。
func Criteria() []Criterion {
	out := make([]Criterion, len(criteria))
	copy(out, criteria)
	return out
}

// Difference 是并存期的一条**行为差异**。
type Difference struct {
	// ID 唯一编号（如 D01）。
	ID string
	// Dimension 是差异的维度（开关 / 时序 / 错误策略 / …）。
	Dimension string
	// V1 描述 v1（未开 v2）时的行为。
	V1 string
	// V2 描述 v2（开了 v2）时的行为。
	V2 string
	// Impact 写明这条差异对调用方/运维的影响。
	Impact string
	// TestName 是**覆盖这条差异的那条测试**的名字。
	//
	// ★ 它必须**真实存在**于仓内某条 `*_test.go` 里 ⇒
	// 「每条差异有测试覆盖」由此成为**机器可校验**的事实，而不是一句承诺。
	//
	// ★ 与 Uncovered **恰有一个**非空：一条差异要么有测试，要么**显式**承认没有。
	TestName string
	// Topic 是这条差异的主题词。
	//
	// ★ 它必须**出现在 TestName 里** ⇒ 「测试存在」之外还要「**名实相符**」。
	// ★ 这条约束抓的是一种**门抓不到的缺陷**：把 A 差异挂到 B 差异的测试上
	// （测试**存在**，但它根本不测 A）⇒ 单看「存在」会放行。
	// ⇒ 若这条差异确实没有测试 ⇒ TestName 留空、填 Uncovered。
	Topic string
	// ExtraTestNames 是**同一条差异**的**补充**覆盖测试（可选）。
	//
	// ★ 为什么需要它：一条差异常由**多个**语义侧面组成
	// （例：D09 = 「沿用已有 id」+「缺失时合成」+「写回 header」三面）。
	// ⇒ 单值 TestName 只能挂其中一面 ⇒ 另一面会**悄悄失去**机器校验。
	//
	// ★ 与 TestName **都**必须真实存在于仓内 ⇒ 删掉其中任一个 ⇒ 门红。
	ExtraTestNames []string
	// RequiredAssertions 把这条差异的**语义断言**逐测试归位（可选）。
	//
	// ★ map 的 key = 覆盖该断言的**测试函数名**；value = 该断言。
	// ★ 每条断言必须**逐字出现在【那个测试所在文件】的【代码面】**里 ——
	// ★ 「代码面」= 该文件经 go/parser 解析后的**表达式 + 字符串字面量**语料，
	// ★ **注释不计入** ⇒ ⇒ 「在注释里写一句『我断言了 X』」蒙混不过去。
	//
	// ★★ 为什么**按测试归位**而不是「一条差异一个断言列表」：
	// ★★ 若断言作用于「所有覆盖测试的并集」，那么 A 测试文件里的一个 `ParseInt`
	// ★★ 就能满足「B 语义需要断言纳秒整数」⇒ **子串偶然命中**被放行
	// ★★ （★ 本仓吃过这个亏：`"t.model = $2"` 被 `"ft.model = $2"` 偶然包含）。
	// ⇒ ⇒ 归位之后，**谁断言的，就在谁的文件里**。
	//
	// ★ key 必须是 TestName 或 ExtraTestNames 里的名字（门会核对这个集合关系）。
	//
	// ★ 留空 = 该差异只享受「存在性 + 主题词」的**弱保证**（★ 已知局限，
	// ★ 登记在审计档 §3.19；补强方法同本字段）。
	RequiredAssertions map[string][]string
	// Uncovered 写明「这条差异**目前没有**测试覆盖」的理由（TestName 为空时必填）。
	//
	// ★ **显式登记**比「悄悄不写」好：差异清单因此是**完整**的，
	// 而缺口是**可见**的（而不是靠「清单里没这条」来隐藏）。
	Uncovered string
}

// differences 是差异清单（单一真值源）。
//
// ★ 每条的 TestName 都指向 `cmd/gateway/v2_dispatch_test.go`、
// `cmd/gateway/main_v2_pipeline_test.go` 或 `cmd/gateway/main_pipeline_requestid_test.go`
// 里**已存在**的测试（★ D09 在 2026-10-24 §3.19 补齐 ⇒ 指向第三个文件）。
var differences = []Difference{
	{
		ID:        "D01",
		Dimension: "开关默认值",
		V1:        "未设环境变量时，4 个 v1 端点直接路由到各自的 handler。",
		V2:        "只有显式设为 1/true/yes 才会被 v2DispatchHandler 包一层。",
		Impact:    "★ 默认**零行为变化** ⇒ 开关打开的风险面只落在「显式打开」的那一次。",
		TestName:  "TestV2Dispatch_DefaultOff_NoPipeline",
		Topic:     "DefaultOff",
	},
	{
		ID:        "D02",
		Dimension: "开关默认值",
		V1:        "显式设为 false 时同样不包装。",
		V2:        "显式 false 与「不设」等价（不会因为「设了但为假」而进入别的分支）。",
		Impact:    "运维可以**显式**关掉它而不必删环境变量。",
		TestName:  "TestV2Dispatch_ExplicitFalse_NoPipeline",
		Topic:     "ExplicitFalse",
	},
	{
		ID:        "D03",
		Dimension: "开关默认值",
		V1:        "—",
		V2:        "显式设为 true 时才构造 pipeline 与包装器。",
		Impact:    "⇒ 构造开销只在开启时付出（关闭时是 cheap path）。",
		TestName:  "TestV2Dispatch_ExplicitTrue_BuildsPipeline",
		Topic:     "ExplicitTrue",
	},
	{
		ID:        "D04",
		Dimension: "开关的兼容面",
		V1:        "只认新变量 `LLM_GATEWAY_USE_V2_PIPELINE`。",
		V2: "★ legacy 变量 `LLM_GATEWAY_V2_ENABLED` **仍然生效**（OR 语义）⇒ " +
			"老运维的手势（肌肉记忆）不会失效。",
		Impact:   "★ 反面：两个变量**任一**为真就开 ⇒ 排查时必须**两个都看**。",
		TestName: "TestV2Dispatch_LegacyFlagStillWorks",
		Topic:    "Legacy",
	},
	{
		ID:        "D05",
		Dimension: "端点覆盖面",
		V1:        "4 个端点各自直连 handler。",
		V2: "★ **同样 4 个**端点都被包装（chat/completions 与 completions 共用 chatHandler；" +
			"messages 与 responses 各用自己的 handler）。",
		Impact:   "⇒ 不存在「只包了一半」的中间状态。",
		TestName: "TestV2Dispatch_AllFourEndpointsForwardToFallback",
		Topic:    "AllFourEndpoints",
	},
	{
		ID:        "D06",
		Dimension: "执行时序",
		V1:        "handler 直接开始处理请求。",
		V2:        "★ pipeline **在** v1 handler **之前**执行（preflight），之后才是真正的调用。",
		Impact:    "⇒ v2 的阶段能观察到请求的**原始形态**；但也意味着 pipeline 的耗时算在请求前面。",
		TestName:  "TestV2Dispatch_PipelineExecutesBeforeFallback",
		Topic:     "ExecutesBefore",
	},
	{
		ID:        "D07",
		Dimension: "错误策略",
		V1:        "没有 pipeline 阶段 ⇒ 不存在阶段错误。",
		V2: "★ 阶段错误**被记录但不阻断**：请求仍然落到 v1 handler 完成" +
			"（源码：a misbehaving Pipeline stage cannot lose data）。",
		Impact:   "★ 这是 v2 的**安全契约**：pipeline 出错 ⇒ 降级到 v1 行为，而不是 5xx。",
		TestName: "TestV2Dispatch_PipelineErrorDoesNotBlockFallback",
		Topic:    "DoesNotBlock",
	},
	{
		ID:        "D08",
		Dimension: "请求体",
		V1:        "handler 直接读 body。",
		V2: "v2 要**先解析** body 才能构造 PipelineRequest ⇒ ★ 必须保证**不消费**请求体，" +
			"否则 v1 handler 会读到空的 body。",
		Impact:   "⇒ 这是「两套路径并存」最容易出事的地方（body 被读两次）。",
		TestName: "TestV2Dispatch_BodyParsingDoesNotConsumeRequest",
		Topic:    "DoesNotConsume",
	},
	{
		ID:        "D09",
		Dimension: "请求标识",
		V1:        "由 v1 自行生成 request id。",
		V2: "★ v2 优先取 `X-Request-Id`；缺失时合成 `v2pipe-<纳秒>` 并**写回** header，" +
			"以免一个请求分裂成两条审计链。",
		Impact:   "⇒ 审计/追踪的 request id 在两套路径下**保持一致**（这是 2026-07-27 修过的点）。",
		TestName: "TestV2DispatchRequestIDSynthesizesAndWritesBack",
		// ★ 本条由**两个**测试覆盖（三面语义各一条）⇒ 另一面登记在 ExtraTestNames。
		ExtraTestNames: []string{"TestV2DispatchRequestIDPrefersServerID"},
		Topic:          "RequestID",
		// ★ RequiredAssertions 把「测试名里有主题词」升级为「**代码里真的断言了**」。
		// ★ 三面语义各自归位到断言它的那个测试：
		// ★   ① 「沿用已有 id」  → 断言 envelope 与服务端 id 相等
		// ★   ② 「缺失时合成」  → 断言 v2pipe- 前缀 + 纳秒部分是整数
		// ★   ③ 「写回 header」 → 断言写回值与 envelope 值相等（否则一个请求分裂成两条审计链）
		// ★ 判据面 = ★★ **逐函数**取出的**代码语料**（★ **不是**整文件 —— ★ 2026-10-24
		// ★ 第四轮 verifier 实测撞出来的假牙，详见审计档 §3.19.8）：
		// ★   = 该测试函数**自身**的函数体 + 它**直接调用**的同文件普通函数（**传递可达**）
		// ★ ⇒ ⇒ ① 同文件里**别的**测试的字串**进不来**
		// ★      （★ 否则「把断言从测试里搬走、留在同文件的 helper 里」就能蒙混过关）；
		// ★ ⇒ ⇒ ② 被调用 helper 里的断言**算数**
		// ★      （★ 否则「真正的断言写在 helper 里」这种正常写法会被误杀）
		// ★ 语料只由 AST 节点拼成 ⇒ **注释不计入**
		// ★ ⇒ 在注释里写一句「我断言了 X」蒙混不过去
		// ★（★ 这正是本条此前登记缺口的原因：门只查「测试存在」⇒ 查不出来）。
		RequiredAssertions: map[string][]string{
			"TestV2DispatchRequestIDPrefersServerID": {
				`got.envelopeID != serverID`,
				`got.headerID != serverID`,
			},
			"TestV2DispatchRequestIDSynthesizesAndWritesBack": {
				`"v2pipe-"`,
				`ParseInt`,
				`got.headerID != got.envelopeID`,
			},
		},
	},
	{
		ID:        "D10",
		Dimension: "nil 安全",
		V1:        "—",
		V2:        "deps / parent mux / chatHandler / shutdown 任一为 nil 时**安全降级**（不 panic）。",
		Impact:    "⇒ 装配顺序出错时是「不生效」而不是「崩溃」。",
		TestName:  "TestV2Dispatch_NilChatHandlerSafe",
		Topic:     "NilChatHandler",
	},
	{
		ID:        "D11",
		Dimension: "路径隔离",
		V1:        "v1 端点与 /v2/* 命名空间互不影响。",
		V2:        "★ v1 端点与 /v2/* **可共存**（v1 端点被包一层，/v2/* 仍是独立命名空间）。",
		Impact:    "⇒ 「开 v2」不等于「v1 消失」⇒ 回滚只需关开关。",
		TestName:  "TestV2Pipeline_MuxIsolation_V1AndV2Coexist",
		Topic:     "MuxIsolation",
	},
	{
		ID:        "D12",
		Dimension: "子功能开关",
		V1:        "—",
		V2: "各子能力（cache / security / audit / observ / streaming / auth / analysis）" +
			"**可单独关闭**，且有各自的默认值。",
		Impact:   "⇒ 切换时可以只开一部分 ⇒ **灰度粒度**在子能力这一级。",
		TestName: "TestLoadV2PipelineConfig_Defaults",
		Topic:    "Defaults",
	},
	{
		ID:        "D13",
		Dimension: "子功能开关",
		V1:        "—",
		V2:        "★ 子开关可以显式关掉（且默认值与「显式关」是两个不同的语义）。",
		Impact:    "⇒ 关闭某个子能力时不会「悄悄回到默认」。",
		TestName:  "TestLoadV2PipelineConfig_DisablesSubFlags",
		Topic:     "DisablesSubFlags",
	},
	{
		ID:        "D14",
		Dimension: "session v2",
		V1:        "session v2 hook 不注册。",
		V2:        "★ 即便开了 v2 pipeline，**session v2 hook 仍不注册**（两条链路独立）。",
		Impact:    "⇒ 「开 v2」不等于「把 session 存储也切过去」⇒ 少一个变量。",
		TestName:  "TestSessionV2HookNotRegistered",
		Topic:     "SessionV2HookNotRegistered",
	},
	{
		ID:        "D15",
		Dimension: "压缩",
		V1:        "强制压缩由 v1 自己的链路处理。",
		V2:        "★ 强制压缩的 header 会**透传到 pipeline** ⇒ 压缩能力在 v2 下仍生效。",
		Impact:    "⇒ 上下文压缩不会被「切到 v2」悄悄关掉。",
		TestName:  "TestV2Dispatch_ForceCompressionHeaderReachesPipeline",
		Topic:     "ForceCompressionHeader",
	},
	{
		ID:        "D16",
		Dimension: "压缩",
		V1:        "压缩失败 ⇒ 自行决定回落。",
		V2:        "★ 流式压缩失败 ⇒ **回落**而不是报错（fail-open）。",
		Impact:    "⇒ 压缩是增强不是闸门（与本轮 P1-4 的改写失败策略同款取向）。",
		TestName:  "TestForceCompressionMetadata_StreamCompressionFailureFallsBack",
		Topic:     "StreamCompressionFailure",
	},
	{
		ID:        "D17",
		Dimension: "关闭",
		V1:        "—",
		V2:        "shutdown 在 nil 情况下安全（不 panic）。",
		Impact:    "⇒ 关停路径不会因为 v2 的存在而出错。",
		TestName:  "TestV2Dispatch_ShutdownNilSafe",
		Topic:     "ShutdownNilSafe",
	},
}

// Differences 返回差异清单（只读副本）。
func Differences() []Difference {
	out := make([]Difference, len(differences))
	copy(out, differences)
	return out
}

// WrappedEndpoints 返回被 v2 包装的 4 个 v1 端点路径。
//
// ★ 与 `cmd/gateway/main_pipeline.go` 的 `registerV2DispatchRoutes` 同款；
// 门禁会拿它与生产文件里的注册点**对拍**。
func WrappedEndpoints() []string {
	return []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/messages",
		"/v1/responses",
	}
}
