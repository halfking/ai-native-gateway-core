package errorsx

// restclasses.go — 失败归责词表（magpie v0.1.1367 的 19 类 rest 词表 → 本仓形态）。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-3）指出：magpie 的 `internal/gateway/routing.go` 把「一个候选为什么失败」
// 收敛成 **19 个类**，而且**每一类的注释都写明了"罚不罚"**：
//
//	rest 词表（internal/gateway/routing.go:270-325 的 fail* 常量）——
//	★ **实测是 20 个常量**（failCredit … failRestricted）：
//	credit / quota / rate / other / auth / canceled / foreign / floor / update /
//	verify / refused / shape / prompt / effort / proxy / overflow / room / slow /
//	loop / restricted
//	〔审计订正〕计划书写的是「19 类」，但它自己列出的名字是 20 个，
//	magpie 源码里也是 20 个 fail* 常量 ⇒ 「19」是笔误，本仓按 **20** 落地。
//
// 本仓 `PenaltyOf` 此前只对 **ErrorKind** 闭集做静态归责，**没有一张
// 「magpie 类 ↔ 本仓形态 ↔ 罚不罚」的对照表** ⇒ 三件事判不出来：
//
//	① 「内容/形状类失败是否会误罚账号」——这正是 magpie 用注释钉死的判据；
//	② 「本仓某个 kind 对应 magpie 的哪一类」——没有映射就没法复核；
//	③ 「本仓与 magpie 的差异」——magpie 的 `proxy`（代理没连上，什么都没送到）
//	   **一律不罚账号**，而本仓的网络类失败是**罚绑定**的 ⇒ 这是一条**真实语义差**，
//	   必须写出来而不是混过去。
//
// # 本文件的形态：表即真值源
//
// ★ **「罚/不罚」的判定写在代码里（`AllRestClasses`），不是写在注释里**
//   ⇒ 门禁（`errorsx/restclasses_test.go`）可以直接遍历这张表断言
//   「表的判定 == `PenaltyOf` 的判定」，**表与实现不同源就会红**。
//
// ★ 表里每一行都带 **Deviation** 字段：本仓与 magpie 不一致的地方**必须写明原因**，
//   没写就是漏项（门禁断言 Deviation 非空的行必须是「已知差异白名单」里的）。
//
// # magpie 侧的权威出处
//
//	`internal/gateway/routing.go:270-325`  —— 19 个 fail* 常量 + 各自的"罚不罚"注释
//	`internal/gateway/routing.go:880-1000` —— 休息决策（rest 的粒度与时长）
//	`internal/gateway/routing.go:1135-1173` —— restSlow（慢启动的模型粒度休息）
//
// # 引用（代码里别处自陈出处）
//
//	domains/credential/slowrest.go —— 慢启动（slow）的模型粒度休息实现
//	errorsx/penalty.go              —— 归责目标与 PenaltyOf

// ── 新增 ErrorKind（每个都有**真实生产者**，不是"声明了没人用"的死词表）──
//
// ★ 这三个 kind 是为了让 magpie 的三个类**能被真正判出来**而新增的：
//   没有它们，"首字节超时"与"流中超时"、"回复循环"与"空回复"、
//   "账号被厂商限制"与"鉴权失败"在本仓是**同一类**，判据无法表达。

const (
	// KindFirstByteTimeout 是**首字节超时**：上游连第一个字节都没吐。
	//
	// ★ 它必须与 KindStreamTimeout 分开：magpie 把首字节超时归为 `slow`，
	//   契约是「**第一次放过**（长 prompt 哪儿都慢）→ 反复发生才罚 →
	//   **罚在模型粒度**而不是账号粒度 → 退避递增」（routing.go:314-318、
	//   :1135-1173）。而**流中超时**（已经吐了半个响应）在本仓仍归
	//   KindStreamTimeout，继续走原有的 5 分钟绑定冷却。
	//   ⇒ 两者共用一个 kind 时，"首两次超时不进 breaker OPEN" 这条判据
	//   **根本无法表达**。
	//
	// 归责：静态判定为 **PenaltyNone**（第一次绝不罚）。反复发生的部分由
	// domains/credential/slowrest.go 的模型粒度休息器承担（**有状态**，
	// 不能写进静态表）。
	KindFirstByteTimeout ErrorKind = "first_byte_timeout"

	// KindReplyLoop 是**回复正文陷入循环**（重复块检测命中）。
	//
	// ★ 它与 KindEmptyResponse 分开：本仓的重复内容检测器
	//   （domains/streaming/integrity）只发事件、不改失败类别，
	//   于是「模型转圈」此前按 KindEmptyResponse 记账。
	//
	// 归责：**罚 (凭据, 模型) 绑定，不罚账号** —— magpie 的 failLoop 理由是
	// 「**是模型转圈，不是账号的锅**」（routing.go:319-321）。本仓把循环当作
	// **可恢复中断**并 failover 到下一个候选（与 magpie 的终局答复语义不同），
	// 所以休息粒度取「模型」——这与 magpie 的理由一致，且**不扩大**到账号。
	KindReplyLoop ErrorKind = "reply_loop"

	// KindAccountRestricted 是**账号被厂商限制**（不是鉴权失败）。
	//
	// ★ 它与 KindAuth 分开：magpie 把两者分开（`auth` = 凭据不对，
	//   `restricted` = 账号被厂商限制、**等申诉而不是等时间**，routing.go:322-325、
	//   :982）。本仓此前把 403 一律归到 KindAuth ⇒ 账号被限制会被记成
	//   「永久失效」（KindAuth 的 Permanent=true）⇒ **不可逆地摘掉一个还能申诉
	//   回来的账号**。
	//
	// 归责：**罚账号级（Credential）+ 熔断，但**不是**永久**（Permanent=false）
	//   —— 厂商解除限制后账号可以回来，永久摘除是错的。
	KindAccountRestricted ErrorKind = "account_restricted"
)

// RestClass 是 magpie 一个 rest 词表类在本仓的落法。
//
// ★ 判定写在**结构体字段**里而不是注释里 ⇒ 门禁可以逐行断言
//
//	「表里的 Target == PenaltyOf(Kind).Target」，表与实现漂移就红。
type RestClass struct {
	// Class 是 magpie 的 fail* 类名（internal/gateway/routing.go:270-325）。
	Class string
	// Kind 是本仓对应的 ErrorKind；**空字符串表示本仓当前没有对应形态**
	// （此时 Target/Verdict 只描述 magpie 侧语义，不约束本仓实现）。
	Kind ErrorKind
	// Target 是本仓的归责目标；Kind 非空时必须等于 PenaltyOf(Kind).Target。
	Target PenaltyTarget
	// Verdict 是一句话的「罚 / 不罚」判定（中文，面向审计阅读）。
	Verdict string
	// Deviation 写明本仓与 magpie 的**已知差异及原因**；无差异时为空字符串。
	Deviation string
}

// magpie 侧 19 个类的固定名（门禁用它断言「表覆盖了全部 19 类」）。
const (
	MagpieClassCredit     = "credit"
	MagpieClassQuota      = "quota"
	MagpieClassRate       = "rate"
	MagpieClassOther      = "other"
	MagpieClassAuth       = "auth"
	MagpieClassCanceled   = "canceled"
	MagpieClassForeign    = "foreign"
	MagpieClassFloor      = "floor"
	MagpieClassUpdate     = "update"
	MagpieClassVerify     = "verify"
	MagpieClassRefused    = "refused"
	MagpieClassShape      = "shape"
	MagpieClassPrompt     = "prompt"
	MagpieClassEffort     = "effort"
	MagpieClassProxy      = "proxy"
	MagpieClassOverflow   = "overflow"
	MagpieClassRoom       = "room"
	MagpieClassSlow       = "slow"
	MagpieClassLoop       = "loop"
	MagpieClassRestricted = "restricted"
)

// AllMagpieRestClasses 是 magpie 侧 20 个 rest 词表类的全集（按 magpie 源码顺序）。
//
// ★ 计数以 magpie 源码为准（internal/gateway/routing.go 的 20 个 fail* 常量），
//
//	不以计划书的「19」为准 —— 详见文件头「审计订正」。
func AllMagpieRestClasses() []string {
	return []string{
		MagpieClassCredit, MagpieClassQuota, MagpieClassRate, MagpieClassOther,
		MagpieClassAuth, MagpieClassCanceled, MagpieClassForeign, MagpieClassFloor,
		MagpieClassUpdate, MagpieClassVerify, MagpieClassRefused, MagpieClassShape,
		MagpieClassPrompt, MagpieClassEffort, MagpieClassProxy, MagpieClassOverflow,
		MagpieClassRoom, MagpieClassSlow, MagpieClassLoop, MagpieClassRestricted,
	}
}

// AllRestClasses 返回 magpie 19 类 → 本仓形态的对照表。
//
// ★ **顺序与 magpie 源码一致**（credit → restricted），便于逐条对读。
// ★ 表是**真值源**：判定写在 Target 字段，门禁逐行对拍 `PenaltyOf`。
func AllRestClasses() []RestClass {
	return []RestClass{
		{
			Class:  MagpieClassCredit,
			Kind:   KindQuotaBalance,
			Target: PenaltyCredential,
			Verdict: "罚账号级：厂商说「额度/余额耗尽」⇒ 该账号这一轮用完了，" +
				"但额度会回来 ⇒ 罚账号（凭据），不永久。",
			Deviation: "magpie 的 creditRest 与 quotaRest 时长不同（credit 短、quota 长）；" +
				"本仓 KindQuotaBalance 与 KindQuota 同族，时长由 coolingDuration 统一给出（30s 默认）。",
		},
		{
			Class:   MagpieClassQuota,
			Kind:    KindQuota,
			Target:  PenaltyCredential,
			Verdict: "罚账号级：配额耗尽 ⇒ 该账号被摘，直到配额窗口回来。",
		},
		{
			Class:   MagpieClassRate,
			Kind:    KindRateLimit,
			Target:  PenaltyBinding,
			Verdict: "罚 (凭据, 模型) 绑定：限流是短窗口信号。",
			Deviation: "★ magpie 罚**账号**级（rateRest 休息账号，routing.go:976-980）；" +
				"本仓罚**绑定**级。差异原因：本仓的限流大量是**按模型**下发的" +
				"（同一把 key 上某个模型被限、另一个模型照常），罚账号会把" +
				"同 key 的其它模型一起摘掉 ⇒ 候选池骤降。**这条是本仓与 magpie" +
				"最实质的一处语义差，不是笔误。**",
		},
		{
			Class:   MagpieClassOther,
			Kind:    KindTransient,
			Target:  PenaltyBinding,
			Verdict: "罚 (凭据, 模型) 绑定：说不清原因的失败仍要降权，但只降到模型粒度。",
			Deviation: "magpie 的 `other` 休息**账号**（routing.go:990-996 的 default 分支，" +
				"除非 provider.RestCap==Off）。本仓的兜底类罚**绑定**。" +
				"原因同上：账号级休息会连带摘掉同账号的其它模型。",
		},
		{
			Class:   MagpieClassAuth,
			Kind:    KindAuth,
			Target:  PenaltyCredential,
			Verdict: "罚账号级：凭据不对 ⇒ 该账号这一轮用不了（**永久**，因为凭据本身要换）。",
		},
		{
			Class:   MagpieClassCanceled,
			Kind:    KindCanceled,
			Target:  PenaltyNone,
			Verdict: "不罚：客户端自己走了 ⇒ 换候选即可，别动账号。",
		},
		{
			Class: MagpieClassForeign,
			Kind:  "",
			Verdict: "不罚账号：magpie 侧是「推理被别的账号封印，去掉封印重发」（routing.go:282-284）。" +
				"本仓无对应形态（无跨账号推理封印续传机制）⇒ **N/A**，不构成缺口。",
		},
		{
			Class: MagpieClassFloor,
			Kind:  "",
			Verdict: "不罚账号：magpie 侧是「回复比厂商给的下限还短 ⇒ 按下限重发」" +
				"（routing.go:285-287）。本仓无对应形态 ⇒ **N/A**。",
		},
		{
			Class: MagpieClassUpdate,
			Kind:  "",
			Verdict: "不罚账号：magpie 侧是「厂商不接 configuration_update 项 ⇒ 去掉重发」" +
				"（routing.go:288-290）。本仓无对应形态 ⇒ **N/A**。",
		},
		{
			Class: MagpieClassVerify,
			Kind:  "",
			Verdict: "罚账号级（magpie 侧）：账号需厂商验证才能继续，验证前按住不放" +
				"（routing.go:986-995）。本仓无对应形态（无「验证待办」状态机）⇒ **N/A**。",
		},
		{
			Class:  MagpieClassRefused,
			Kind:   KindContentFilter,
			Target: PenaltyNone,
			Verdict: "★ **不罚账号**：安全过滤拒的是**内容**，任何一个账号都会被拒 ⇒ " +
				"只换候选（routing.go:291-293）。",
		},
		{
			Class:   MagpieClassShape,
			Kind:    KindClientBug,
			Target:  PenaltyNone,
			Verdict: "★ **不罚账号**：厂商读不懂请求形状（routing.go:294-295）⇒ 只换候选。",
		},
		{
			Class:   MagpieClassPrompt,
			Kind:    KindUnsupportedFeature,
			Target:  PenaltyNone,
			Verdict: "★ **不罚账号**：厂商不接 agent 的系统提示（routing.go:296-298）⇒ 只换候选。",
			Deviation: "magpie 把 prompt 与 effort 分成两个类；本仓两者都落在" +
				"「厂商不接这个特性」的同一归责上（KindUnsupportedFeature ⇒ PenaltyNone），" +
				"**归责结论相同**（都不罚账号），故不另立 kind。",
		},
		{
			Class:     MagpieClassEffort,
			Kind:      KindUnsupportedFeature,
			Target:    PenaltyNone,
			Verdict:   "★ **不罚账号**：套餐不接这个推理档（routing.go:299-301）⇒ 只换候选。",
			Deviation: "同 prompt 类：与本仓 KindUnsupportedFeature 合并，结论同为 PenaltyNone。",
		},
		{
			Class:  MagpieClassProxy,
			Kind:   KindNetwork,
			Target: PenaltyBinding,
			Verdict: "★ magpie 侧**不罚账号**：「代理没连上，**什么都没送到**」⇒ 只换候选" +
				"（routing.go:302-305、:913-915）。",
			Deviation: "★ **本仓罚绑定**（KindNetwork → PenaltyBinding）。原因：本仓出站走" +
				"`upstream.ProxyResolver`，它只**改写 URL**、不产生可区分的失败信号 ⇒ " +
				"「代理没连上」在本仓与「直连上游失败」**同一条错误路径**，" +
				"无法拆成两个 kind。⇒ 这是一条**已知且未消除**的语义差；" +
				"若将来代理失败能被独立识别，应新增 KindProxyUnreachable ⇒ PenaltyNone。",
		},
		{
			Class:   MagpieClassOverflow,
			Kind:    KindContextLength,
			Target:  PenaltyNone,
			Verdict: "★ **不罚账号**：对话太长，换个装得下的模型/账号（routing.go:306-308）。",
		},
		{
			Class: MagpieClassRoom,
			Kind:  "",
			Verdict: "★ magpie 侧**不罚账号**：「窗口装得下 prompt、装不下这次要的回复」" +
				"（routing.go:309-311）⇒ 本仓无对应形态 ⇒ **N/A**。",
			Deviation: "本仓的上下文长度判定只覆盖「prompt 装不下」（KindContextLength），" +
				"没有「回复装不下」这一侧 ⇒ 无 kind 可映射。",
		},
		{
			Class:  MagpieClassSlow,
			Kind:   KindFirstByteTimeout,
			Target: PenaltyNone,
			Verdict: "★ **第一次放过**，反复发生才罚，且**罚在模型粒度**（不是账号）、" +
				"退避递增（routing.go:314-318、:1135-1173）。",
			Deviation: "★ 本仓用**两个机制**拼出 magpie 的一个契约：静态表判" +
				"**PenaltyNone**（第一次绝不罚）+ `domains/credential/slowrest.go` 的" +
				"**有状态**模型粒度休息器（第 2 次起才休息，退避 1min→2min→4min…，" +
				"30min 遗忘窗）。⇒ 静态表表达不了「第二次才罚」，必须由状态机承担。",
		},
		{
			Class:   MagpieClassLoop,
			Kind:    KindReplyLoop,
			Target:  PenaltyBinding,
			Verdict: "★ **不罚账号**：「是模型转圈，不是账号的锅」（routing.go:319-321）。",
			Deviation: "magpie 的 failLoop 是**谁都不休息**（它把循环当作终局答复告诉 agent）；" +
				"本仓把循环当**可恢复中断**并 failover 到下一个候选 ⇒ 休息粒度取" +
				"**(凭据, 模型)**，即模型粒度，**不扩大**到账号 ⇒ magpie 的理由（不罚账号）被保住。",
		},
		{
			Class:  MagpieClassRestricted,
			Kind:   KindAccountRestricted,
			Target: PenaltyCredential,
			Verdict: "★ magpie 侧罚账号级，但**不是永久**：厂商限制账号 ⇒ 等申诉，" +
				"不是等时间（routing.go:322-325、:982-985）。",
			Deviation: "magpie 把它与 `auth` 分开是**关键**：本仓此前 403 一律归 KindAuth，" +
				"而 KindAuth 是 **Permanent=true** ⇒ 账号被限制会被**永久摘除**，" +
				"而厂商解除限制后账号是能回来的 ⇒ 本轮新增 KindAccountRestricted" +
				"（Credential + Fuse，**非永久**）承接这一形态。",
		},
	}
}

// RestClassOf 返回某个 ErrorKind 在词表里的行（找不到返回 false）。
func RestClassOf(kind ErrorKind) (RestClass, bool) {
	for _, rc := range AllRestClasses() {
		if rc.Kind != "" && rc.Kind == kind {
			return rc, true
		}
	}
	return RestClass{}, false
}

// RestClassesByTarget 按归责目标分组统计（供台账/观测用）。
func RestClassesByTarget() map[PenaltyTarget]int {
	out := map[PenaltyTarget]int{}
	for _, rc := range AllRestClasses() {
		if rc.Kind == "" {
			continue
		}
		out[rc.Target]++
	}
	return out
}
