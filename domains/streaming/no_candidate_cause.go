package streaming

// no_candidate_cause.go —— 「候选为空」的**客户端可见归因**（2026-11-22）。
//
// # 为什么要有这个文件
//
// ★ 211 事故：claude-sonnet-5-5 的 7 条候选全部「上游余额耗尽」，owner 连续
// ★ 23 笔拿到的都是
//
//	503 overloaded_error  "No available provider for model 'claude-sonnet-5-5'"
//
// ★ 这句话描述的是**网关此刻的状态**（没东西可路由）⇒ 客户端的理性反应是
// ★ 重试（overloaded_error 的字面意思就是「过载了，稍后再试」）。而真相是
// ★ 上游账户没余额——重试一百次也一样，要等有人去充值。owner 的感受是
// ★ 「这个模型总是失败」，但无从知道原因。owner 原话：这类的错误要如实地
// ★ 返回给客户端，不要让人迷惑。
//
// ★ 仓内对「永久性失败必须说清楚」已有先例——模态不匹配（2026-10-28：
// ★ 「等一会儿」永远没用 ⇒ 必须说清楚）。这里把同一原则接到「候选被状态闸
// ★ 滤空」上：归因数据来自 provider.DiagnoseEmptyCandidates（只读旁路查询，
// ★ 成功路径零开销），文案走 i18n（MsgNoCandidateBlocked + 原因短语）。
//
// # 纪律（与 diagnoseModalityMismatch 相同）
//
//   - **具名函数**：接线才是会退化的那一环——诊断函数体好测，「handler 在
//     放弃成 503 之前有没有真的调它」只有具名函数才能被测试点到；
//   - **诊断是改进，不是新的失败方式**：查询出错 / resolver 不支持 / 归因
//     解释不了这次空 ⇒ 一律退回通用文案（MsgNoCandidate），绝不编原因；
//   - **给客户端的是「哪一类原因 + 该怎么办」**，不给 offer 计数（那是有
//     多少条上游凭据——运营侧信息，只进日志与管理面）。

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// noCandidateDiagnoseBudget 是「候选为空」归因诊断的**独立**时间预算。
//
// ★ 为什么需要独立预算（2026-11-23 12h 修订审计轮）：
//
// 归因诊断是**失败路径**上的只读旁路查询（provider.DiagnoseEmptyCandidates）。
// 它此前直接用请求 ctx（r.Context()），**没有自己的时间上限** ⇒
//
//	① 失败路径恰恰是数据库最可能吃紧的时刻（候选判空往往伴随上游异常、
//	   凭据大面积被闸滤掉），诊断查询一慢，**503 的应答就被拖住**；
//	② 诊断在三个协议面各调一次（chat/completions + messages + responses），
//	   每面都付一次这个延迟；
//	③ 既有纪律只挡得住「诊断**出错**」⇒ 退回通用文案；**挡不住「诊断很慢」**
//	   ⇒ 诊断失败不变成请求失败，但诊断慢会把请求拖成超时。
//
// ⇒ 修法：给诊断一份**自己的**短预算，从请求 ctx 派生（客户端取消仍能透传）。
//
//	超预算 ⇒ ctx 到期 ⇒ 查询返回错 ⇒ 走既有的「诊断出错」分支 ⇒ 退回通用文案，
//	语义与「诊断失败」完全一致，不新增任何失败形态。
//
// 取值 2s 的理由：这是一次只读、按 key 归因的小查询（正常在毫秒级），
// 2s 足以覆盖冷缓存/锁等待的常态抖动；再长就会开始与客户端超时抢时间。
const noCandidateDiagnoseBudget = 2 * time.Second

// noCandidateCause 是一次「候选为空」归因的客户端可见形态。
type noCandidateCause struct {
	// Causes 是有界的原因令牌（provider.ClientCause*），按主因优先、去重。
	Causes []string
	// Message 是本地化的人读句子；Causes 为空时必为空串。
	Message string
}

// empty 返回 true 表示「归因解释不了这次空」⇒ 调用方维持通用文案。
func (d noCandidateCause) empty() bool { return len(d.Causes) == 0 }

// retryable：被状态闸挡住不是「稍后再试」能解决的——要等人充值 / 启用。
// 状态闸全开（原因不明）的空候选不走本归因，仍是既有的可重试 503。
func (d noCandidateCause) retryable() bool { return false }

// causeToken 是进错误信封旁键的机器可读值：多因按主因优先用 "+" 连接。
func (d noCandidateCause) causeToken() string { return strings.Join(d.Causes, "+") }

// extras 是并入错误对象的旁键。**只在这笔确实有归因时才非空**——没有归因
// 的空候选保持既有信封形状逐字节不变。
func (d noCandidateCause) extras() map[string]any {
	if d.empty() {
		return nil
	}
	return map[string]any{
		"cause":     d.causeToken(),
		"retryable": d.retryable(),
		// /v1/messages 的写出函数（writeAnthropicError）没有 code 形参——
		// Anthropic 协议的客户端此前拿不到机器可读的错误码。chat / responses
		// 面本来就有同一个值，并入后不变。
		"code": "no_candidate",
	}
}

// causeListJoiner 是原因短语之间的分隔符。中日文用全角分号，其余用「; 」——
// 211 活体验证时中文句子里出现半角分号，读起来割裂。
func causeListJoiner(loc i18n.Locale) string {
	switch loc {
	case i18n.ZhCN, i18n.ZhTW, i18n.Ja:
		return "；"
	}
	return "; "
}

// logMessage 是进请求日志的稳定英文句子：既有前缀逐字保留（日志检索、
// 看板按前缀聚合不受影响），归因以方括号附在后面。
func (d noCandidateCause) logMessage(clientModel string) string {
	msg := "No available provider for model '" + clientModel + "'"
	if d.empty() {
		return msg
	}
	return msg + " [cause=" + d.causeToken() + "]"
}

// emptyCandidateDiagnoser 是 providerResolver 的**可选能力**：真实现
// （*provider.Client）具备；测试替身不必实现——缺席即退回通用文案。
type emptyCandidateDiagnoser interface {
	DiagnoseEmptyCandidates(ctx context.Context, model, tenantID string) (provider.EmptyCandidateBreakdown, error)
}

// noCandidateCauseMessageKey 把原因令牌映射到 i18n 原因短语键。未知令牌
// 返回空串（调用方跳过）——令牌集合是 provider 侧的有界枚举，这里不兜底
// 编词。
func noCandidateCauseMessageKey(cause string) string {
	switch cause {
	case provider.ClientCauseQuota:
		return i18n.MsgNoCandidateCauseQuota
	case provider.ClientCauseDisabled:
		return i18n.MsgNoCandidateCauseDisabled
	case provider.ClientCauseCredentialMissing:
		return i18n.MsgNoCandidateCauseCredentialMissing
	case provider.ClientCauseOtherTenant:
		return i18n.MsgNoCandidateCauseOtherTenant
	case provider.ClientCauseNotRegistered:
		return i18n.MsgNoCandidateCauseNotRegistered
	}
	return ""
}

// diagnoseNoCandidateCause 在「已经判空」的出口给出客户端可见归因。
//
// 只在零候选路径调用（成功路径零开销）。任何一步不成立都返回空归因：
// handler 没接 resolver / resolver 不具备诊断能力 / 查询出错 / 状态闸解释
// 不了（闸全开）。调用方据 empty() 决定用归因文案还是通用文案。
func (h *ChatHandler) diagnoseNoCandidateCause(ctx context.Context, clientModel, tenantID string) noCandidateCause {
	if h == nil || h.provider == nil {
		return noCandidateCause{}
	}
	diag, ok := h.provider.(emptyCandidateDiagnoser)
	if !ok {
		return noCandidateCause{}
	}
	// ★ 独立时间预算（2026-11-23 审计轮）：诊断是失败路径上的旁路查询，
	//   不能让它无限期拖住 503 的应答 ⇒ 自带 2s 上限。
	//   从请求 ctx 派生 ⇒ 客户端取消仍会透传（不被这层预算吞掉）。
	ctx, cancel := context.WithTimeout(ctx, noCandidateDiagnoseBudget)
	defer cancel()
	b, err := diag.DiagnoseEmptyCandidates(ctx, clientModel, tenantID)
	if err != nil {
		// 诊断是旁路：查不出来就按没有归因处理，绝不让一次诊断失败变成
		// 一次新的请求失败（也绝不猜一个原因）。
		return noCandidateCause{}
	}
	causes := b.ClientCauses()
	if len(causes) == 0 {
		return noCandidateCause{}
	}
	parts := make([]string, 0, len(causes))
	for _, c := range causes {
		if key := noCandidateCauseMessageKey(c); key != "" {
			parts = append(parts, i18n.T(ctx, key))
		}
	}
	if len(parts) == 0 {
		return noCandidateCause{}
	}
	// 2026-11-23：「模型未登记」时没有候选，不应说"全部候选节点都被挡住"。
	// 有候选被挡 ⇒ 用 MsgNoCandidateBlocked；无候选（只有 not_registered）⇒ 用专用模板。
	msgKey := i18n.MsgNoCandidateBlocked
	if len(causes) == 1 && causes[0] == provider.ClientCauseNotRegistered {
		msgKey = i18n.MsgNoCandidateNotRegistered
	}
	return noCandidateCause{
		Causes: causes,
		Message: i18n.T(ctx, msgKey, map[string]any{
			"Model":  clientModel,
			"Causes": strings.Join(parts, causeListJoiner(i18n.LocaleFromContext(ctx))),
		}),
	}
}
// statusCodeForNoCandidateCause 根据归因令牌返回 HTTP 状态码。
//
// 2026-11-23：owner 明确要求「候选被永久性原因挡空时，需要更换成4xx，并且要
// 返回正确的错误信息」——所有归因情况（retryable: false）都是"重试无效"，
// 应区别于"暂时过载、稍后重试"的 503。
//
// 映射规则：
//   - model_not_registered（模型未登记）→ 404
//   - provider_not_in_tenant（租户隔离）→ 403
//   - upstream_quota_exhausted（上游配额耗尽）→ 429
//   - provider_disabled / credential_missing → 503（但 error type 改为 api_error）
//   - 无归因 → 503（保持原行为）
func statusCodeForNoCandidateCause(causes []string) int {
	if len(causes) == 0 {
		return http.StatusServiceUnavailable
	}
	// 按优先级检查（从最具体到最通用）
	for _, c := range causes {
		switch c {
		case provider.ClientCauseNotRegistered:
			return http.StatusNotFound // 404
		case provider.ClientCauseOtherTenant:
			return http.StatusForbidden // 403
		case provider.ClientCauseQuota:
			return http.StatusTooManyRequests // 429
		}
	}
	// 其余（provider_disabled / credential_missing）仍 503，
	// 但 error type 会由下面的 errorCodeForNoCandidateCause 改变
	return http.StatusServiceUnavailable
}

// errorCodeForNoCandidateCause 根据归因令牌返回更精确的 error code，
// 以便 Anthropic 面能正确映射到对应的 error type。
//
// OpenAI 面的 type 由 ingressErrorType(status) 从状态码自动推导；
// Anthropic 面的 type 由 anthropicErrorType(type, code) 映射，优先看 code。
func errorCodeForNoCandidateCause(causes []string) string {
	if len(causes) == 0 {
		return "no_candidate"
	}
	for _, c := range causes {
		switch c {
		case provider.ClientCauseNotRegistered:
			// Anthropic: code "model_not_found" → type "not_found_error"
			return "model_not_found"
		case provider.ClientCauseOtherTenant:
			// Anthropic: code "permission_error" → type "permission_error"
			return "permission_error"
		case provider.ClientCauseQuota:
			// Anthropic: code "rate_limit_error" → type "rate_limit_error"
			return "rate_limit_error"
		}
	}
	// provider_disabled / credential_missing：保持 no_candidate，
	// 但状态码仍是 503 → type "server_error" (OpenAI) / "api_error" (Anthropic)
	return "no_candidate"
}
