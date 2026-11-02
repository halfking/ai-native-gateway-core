package errorsx

// penalty.go — P0-C（2026-10-14 magpie 对标）：失败归责矩阵。
//
// 原则（magpie「只有 rest 决定谁被排除」）：先判「谁该受罚」，再动 breaker /
// 绑定冷却。请求内容/形状/客户端契约错误不罚节点；模型级拒绝只罚
// (credential, model)；账号级证据才罚凭据。
//
// DecideFailover 的 Scope/Fuse/Permanent 已接线 PenaltyOf；credential.Writer
// 的 WriteOnError / SetCredentialUnavailable / coolingDuration 对
// PenaltyNone 短路（不写冷却）；Breaker.RecordFailure 同样以 PenaltyNone
// 为前置短路（json-word / context_length 等不冷却）。P0-4 rest 分级
// （RecordFailureWithReset）仍按 per-kind 冷却时长策略。
// 本文件闭集登记全部 ErrorKind（见 AllErrorKinds），未登记即测试红。

// PenaltyTarget 是归责对象。
type PenaltyTarget string

const (
	// PenaltyNone：不罚任何人（换候选可不计冷却；典型：client_bug / shape）。
	PenaltyNone PenaltyTarget = "none"
	// PenaltyBinding：只罚 (credential, model) 绑定。
	PenaltyBinding PenaltyTarget = "binding"
	// PenaltyCredential：罚整凭据（含其下全部模型绑定）。
	PenaltyCredential PenaltyTarget = "credential"
	// PenaltyProvider：罚 provider 级（罕见；上游整站宕）。
	PenaltyProvider PenaltyTarget = "provider"
)

// Penalty 是一次失败的归责结论。
type Penalty struct {
	Target PenaltyTarget
	// Fuse 是否进入熔断/隔离语义（与 FailoverDecision.Fuse 对齐）。
	Fuse bool
	// Permanent 是否永久隔离（配额永久耗尽 / 鉴权吊销）。
	Permanent bool
}

// AllErrorKinds 是 ErrorKind 闭集（登记表）。新增 Kind 常量必须同步追加，
// 否则 TestPenaltyOfClosedSet 红——与 r1019 动词表封闭性钉子同构。
func AllErrorKinds() []ErrorKind {
	return []ErrorKind{
		KindTransient,
		KindTimeout,
		KindNetwork,
		KindRateLimit,
		KindAuth,
		KindQuota,
		KindUpstreamDown,
		KindCanceled,
		KindClientBug,
		// 2026-11-02：上游拒收但无法再归因的 4xx（请求侧错误族）。
		// 与 KindClientBug 同判——见 kind 声明处的事故链说明。
		KindRequestRejected,
		// 2026-11-02（4xx 归因族收口轮）：绑定级证据——「上游不提供这一对」。
		// 归责落在 (凭据,模型)，**不**落账号（见 KindModelUnavailable 的
		// kind 声明与 PenaltyOf 的分支）。
		KindModelUnavailable,
		KindConcurrent,
		KindAuthRevoked,
		KindQuotaPeriodic,
		KindQuotaBalance,
		KindQuotaPermanent,
		KindModelNotFound,
		KindStreamTimeout,
		KindToolCallIdMismatch,
		KindContextLength,
		KindUnsupportedFeature,
		KindModelDeprecated,
		KindContentFilter,
		KindEmptyResponse,
		KindConversion,
		KindUpstreamContextLoss,
		KindUpstreamOverloaded,
		KindNoAvailableChannel,
		KindCircuitOpen,
		KindFpSlotSaturated,
		// 2026-10-24 magpie 19 类 rest 词表轮（restclasses.go）：这三个 kind
		// 是为了让 magpie 的 slow / loop / restricted 三类**能被真正判出来**
		// 而新增的 —— 此前它们分别与 KindStreamTimeout / KindEmptyResponse /
		// KindAuth 同形，判据无法表达。各自的生产者见 restclasses.go 的注释。
		KindFirstByteTimeout,
		KindReplyLoop,
		KindAccountRestricted,
	}
}

// PenaltyOf 返回 ErrorKind 的归责目标。未知 kind → PenaltyBinding + Fuse
// false（保守：可换候选但不永久隔离）。
func PenaltyOf(kind ErrorKind) Penalty {
	switch kind {
	// —— 不罚：请求侧 / 形状 / 内容策略 ——
	case KindClientBug, KindToolCallIdMismatch, KindContextLength,
		KindContentFilter, KindUnsupportedFeature, KindConversion,
		KindCanceled, KindCircuitOpen, KindFpSlotSaturated:
		// circuit_open / fp_slot_saturated 是网关侧状态，再记一次冷却无意义。
		return Penalty{Target: PenaltyNone}

	// —— 不罚：上游拒收但无法进一步归因的 4xx（2026-11-02）——
	//
	// ★ 这条是本轮事故的**放大器**判据：上游 400 此前落 KindTransient ⇒
	//   PenaltyBinding ⇒ 3 连败后 EffectSetCredentialUnavailable ⇒
	//   整个凭据（其下全部 binding、全部租户）被置 cooling
	//   （生产实测：一条客户端错单让 /v1/embeddings 对所有租户 503）。
	//
	//   归因口径：上游回了 4xx 且错误体里没有任何模型级/凭据级/配额级/
	//   限流级裁决 ⇒ 证据指向**这次请求**，不指向节点。节点健康不该因此
	//   冷却、绑定不该因此翻红、探针不该因此扇出。
	//   注意：这只改**归责**，不改「什么算失败」——请求仍然失败，
	//   且仍允许换一个兄弟候选（action_policy 的 request_rejected 分支）。
	case KindRequestRejected:
		return Penalty{Target: PenaltyNone}

	// —— 绑定级：上游确定性地不提供这一对 (凭据,模型) ——
	//
	// ★ 2026-11-02（4xx 归因族收口轮）：裸 404 的两种读法（账号里没有这个
	//   模型 / 这个凭据的端点路径配错）**都**指向这一对，都不指向账号 ⇒
	//   归责落在**绑定级**，与 KindModelNotFound / KindModelDeprecated 同桶。
	//
	// ★ 与请求侧族（KindRequestRejected → PenaltyNone）的分界：
	//   请求侧 = 「证据指向这次请求」⇒ 一个字节的共享状态都不写；
	//   本 kind = 「证据指向这一对」⇒ 写这一对的冷却，**不**写账号。
	//
	// ★ Fuse 保持 false：本 kind **不**进熔断语义（熔断是账号级），
	//   且必须保留跨凭据故障转移（兄弟凭据可能能服务同一个模型）。
	case KindModelUnavailable:
		return Penalty{Target: PenaltyBinding}

	// —— 不罚：首字节超时（magpie 的 slow）——
	//
	// ★ magpie 的契约是「**第一次放过**（长 prompt 哪儿都慢）→ 反复发生才罚
	//   → **罚在模型粒度**而不是账号 → 退避递增」（routing.go:314-318、
	//   :1135-1173）。静态表只能表达「不罚」这一半；「第二次才罚」由
	//   domains/credential/slowrest.go 的**有状态**模型粒度休息器承担。
	//   ⇒ 这里判 none，才不会在第一次就摘掉绑定。
	case KindFirstByteTimeout:
		return Penalty{Target: PenaltyNone}

	// —— 绑定级：回复陷入循环 ——
	//
	// ★ magpie 的 failLoop 理由是「**是模型转圈，不是账号的锅**」
	//   （routing.go:319-321）⇒ 休息粒度取 (凭据, 模型)，**不罚账号**。
	case KindReplyLoop:
		return Penalty{Target: PenaltyBinding}

	// —— 绑定级：模型不存在 / 弃用 / 限流 / 瞬时 / 空响应 ——
	case KindModelNotFound, KindModelDeprecated:
		return Penalty{Target: PenaltyBinding}
	case KindRateLimit, KindConcurrent, KindUpstreamOverloaded,
		KindNoAvailableChannel, KindTransient, KindTimeout, KindNetwork,
		KindStreamTimeout, KindEmptyResponse, KindUpstreamContextLoss:
		return Penalty{Target: PenaltyBinding}

	// —— 凭据级：鉴权 / 配额 ——
	case KindAuth:
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: true}
	case KindAuthRevoked:
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: true}
	case KindQuotaPermanent:
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: true}
	case KindQuota, KindQuotaBalance:
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: true}
	case KindQuotaPeriodic:
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: false}
	case KindAccountRestricted:
		// ★ 账号被**厂商限制**（magpie 的 restricted）：罚账号级，但**不是永久** ——
		//   magpie 的理由是「等申诉，不是等时间」（routing.go:322-325、:982-985）。
		//   Permanent=true 会把一个**厂商解除限制后还能回来**的账号永久摘掉 ⇒ 不能给永久。
		//   与 KindAuth 的差别正在这里：凭据不对（auth）是永久的，账号被限制不是。
		return Penalty{Target: PenaltyCredential, Fuse: true, Permanent: false}

	// —— provider 级：整站不可达 ——
	case KindUpstreamDown:
		return Penalty{Target: PenaltyProvider, Fuse: true}

	default:
		return Penalty{Target: PenaltyBinding}
	}
}

// FailoverScopeOf 把 PenaltyTarget 映射到既有 FailoverScope（兼容 DecideFailover）。
func FailoverScopeOf(p Penalty) FailoverScope {
	switch p.Target {
	case PenaltyNone:
		return ScopeNone
	case PenaltyBinding:
		return ScopeModel
	case PenaltyCredential, PenaltyProvider:
		return ScopeCredential
	default:
		return ScopeModel
	}
}
