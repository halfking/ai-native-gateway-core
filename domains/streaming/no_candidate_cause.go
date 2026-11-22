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
	"strings"

	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/provider"
)

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
	return noCandidateCause{
		Causes: causes,
		Message: i18n.T(ctx, i18n.MsgNoCandidateBlocked, map[string]any{
			"Model":  clientModel,
			"Causes": strings.Join(parts, causeListJoiner(i18n.LocaleFromContext(ctx))),
		}),
	}
}
