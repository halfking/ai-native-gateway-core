package executors

// reqprobe_integration.go — 请求侧异常探测的 executor 接入（2026-09-21）。
//
// executeOpenAI 在 4xx 终端分支里做两类一次性"免费重试"：
//   1. 参数剔除：reqprobe.Diagnose 判定 param_rejected 后，剔除错误点名的
//      参数（未点名则剔除全部"不常见参数"）重试一次；
//   2. 模式回退：native /v1/responses 传输被端点级拒绝（404/405 或文案
//      明示）时，回退到 chat/completions 传输重试一次（仅流式或非
//      responses 客户端可安全回退；非流式 responses 客户端只记录）。
//
// 每类探测每请求最多一次（reqProbeRuntime 的 tried 标志），重试不消耗
// 重试预算（probeRetry 抵消 attempt++）且跳过退避延迟。探测结果（含
// "剔除后成功/失败"）经 Coordinator 异步落 Store，供 /format-anomalies
// 的"请求错误"页查看与批量解决；被成功验证过的参数规则会在后续请求中
// 前置剔除（学习，LLM_GATEWAY_REQPROBE_LEARN=off 可关）。

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// reqProbeRuntime 是单次 executeOpenAI 调用的探测状态（跨重试迭代保持）。
type reqProbeRuntime struct {
	paramTried bool
	modeTried  bool
	// active 表示至少做过一次剔除/回退尝试（结果待定：成功在
	// recordAttemptSuccess 记 recovered，再次 4xx 在终端分支记未恢复）。
	active   bool
	recorded bool
	diag     reqprobe.Diagnosis
	input    reqprobe.Input
	meta     reqprobe.TerminalMeta
}

// reqprobeApplyLearned 在首次出站前应用已学习的参数剔除规则。
// 返回（可能被修改的）body。
func (e *Executor) reqprobeApplyLearned(body []byte, cand provider.Candidate, requestID string) []byte {
	if e.RequestProbe == nil {
		return body
	}
	learned := e.RequestProbe.LearnedParams(context.Background(), cand.CatalogCode, cand.RawModel)
	if len(learned) == 0 {
		return body
	}
	out, stripped := reqprobe.StripParams(body, strings.Join(learned, ","))
	if len(stripped) == 0 {
		return body
	}
	slog.Info("reqprobe: applying learned param strips",
		"request_id", requestID,
		"provider_id", cand.ProviderID,
		"raw_model", cand.RawModel,
		"stripped", strings.Join(stripped, ","),
	)
	return out
}

// reqprobeRecordSuccess 在探测重试后的成功路径记录 recovered。
func (e *Executor) reqprobeRecordSuccess(probe *reqProbeRuntime) {
	if e.RequestProbe == nil || probe == nil || !probe.active || probe.recorded {
		return
	}
	probe.recorded = true
	e.RequestProbe.RecordTerminal(probe.input, probe.diag, probe.meta, true)
	slog.Info("reqprobe: probe retry recovered the request",
		"request_id", probe.meta.RequestID,
		"trigger", string(probe.diag.Trigger),
		"param", probe.diag.Param,
		"suggest_mode", probe.diag.SuggestMode,
	)
}
