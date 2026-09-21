package executors

// paramledger_integration.go — 参数协商账本的 executor 接入（2026-09-22）。
//
// 出站方向：paramguard 的语义级调整（clamp/strip/normalize）与 reqprobe 的
// 参数剔除/模式回退都会记入 ParamLedger（按 request_id 去重合并）。
// 回程方向：native responses 写回客户端前，把回显的 reasoning.effort
// 还原为客户端原始值（非流式在本包；流式在 streaming 包的
// StreamNativeResponsesSSE，共享同一账本实例）。
//
// 账本写入是非阻塞快速路径；nil 账本时一切行为关闭（测试与最小部署）。

import (
	"github.com/kaixuan/llm-gateway-go/internal/paramguard"
	"github.com/kaixuan/llm-gateway-go/internal/paramledger"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// applyParamguardLedger 跑 paramguard 并把语义级调整记账。
func (e *Executor) applyParamguardLedger(params *ExecParams, body []byte, dialect paramreg.Dialect) []byte {
	if e == nil {
		return paramguard.Apply(body, dialect)
	}
	return applyGuardToLedger(e.ParamLedger, requestIDOf(params), body, dialect)
}

// applyGuardToLedger 是无 receiver 版本（prepareRequestBody 等普通函数用）。
func applyGuardToLedger(ledger *paramledger.Ledger, requestID string, body []byte, dialect paramreg.Dialect) []byte {
	out, reports := paramguard.ApplyReported(body, dialect)
	if ledger == nil || requestID == "" || len(reports) == 0 {
		return out
	}
	adjs := make([]paramledger.Adjustment, 0, len(reports))
	for _, r := range reports {
		adjs = append(adjs, paramledger.Adjustment{
			Field:    r.Field,
			Original: r.Original,
			Sent:     r.Sent,
			Action:   paramledger.Action(r.Action),
			Reason:   r.Reason,
		})
	}
	ledger.Record(requestID, adjs...)
	return out
}

func requestIDOf(params *ExecParams) string {
	if params == nil {
		return ""
	}
	return params.RequestID
}

// ledgerRecordProbe 记录 reqprobe 的参数剔除 / 模式回退。
// stripParams 是被剔除的参数清单；modeFrom/modeTo 非空时记录模式回退。
func (e *Executor) ledgerRecordProbe(params *ExecParams, cand provider.Candidate, stripParams []string, modeFrom, modeTo string) {
	if e.ParamLedger == nil || params == nil {
		return
	}
	adjs := make([]paramledger.Adjustment, 0, 2)
	for _, p := range stripParams {
		adjs = append(adjs, paramledger.Adjustment{
			Field:    p,
			Original: p,
			Sent:     "",
			Action:   paramledger.ActionStrip,
			Reason:   "reqprobe: upstream rejected param",
		})
	}
	if modeFrom != "" && modeTo != "" {
		adjs = append(adjs, paramledger.Adjustment{
			Field:    "transport",
			Original: modeFrom,
			Sent:     modeTo,
			Action:   paramledger.ActionModeSwitch,
			Reason:   "reqprobe: native responses transport rejected",
		})
	}
	if len(adjs) > 0 {
		e.ParamLedger.Record(params.RequestID, adjs...)
	}
}

// restoreClientEcho 把（可能是被调整后的）回显参数还原为客户端原始值。
// 当前覆盖 Responses 形态的 reasoning.effort；无账目或无回显时原样返回。
func (e *Executor) restoreClientEcho(params *ExecParams, body []byte) []byte {
	if e.ParamLedger == nil || params == nil || len(body) == 0 {
		return body
	}
	return e.ParamLedger.RestoreResponsesEffort(body, params.RequestID)
}
