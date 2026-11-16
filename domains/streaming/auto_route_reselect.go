package streaming

// auto_route_reselect.go —— auto 请求的**零候选重选**（2026-11-16 auto 模式可用性轮）。
//
// ★ 为什么要这道门：
//
// auto 决策选出模型 A 之后，handler 会**先**按 A 解析候选（resolveCandidatesForRequest），
// 拿到候选才交给 dispatch / executor。这一层解析如果返回**零候选**（模型此刻没有
// 可路由节点：索引刷新滞后、凭据刚被摘、额度刚耗尽、模态登记不符……），
// 修前的形态是**当场 503 no_candidate** ⇒
//
//   · dispatch 的换模梯子（domains/dispatch/dispatcher.go tryModelChangeOutcome）
//     **根本没机会跑**——它在 executor 里面，而请求死在 executor 之前；
//   · 客户端只看到「原来的模型不可用」，没有任何退路。
//
// 211 部署后首轮 live 验收实测（2026-11-17 00:24）：auto 选到
// `qwen2.5-7b-instruct`，该模型此刻零候选 ⇒ **HTTP 503 no_candidates**。
//
// ★ magpie 的同款规则：
//
//   · 「auto 组消失时的兜底」——magpie internal/provider/group.go:829-837
//     AutoStandIn：auto 组不可用时落到一个还有节点的成员，**会话不中断**；
//   · 「同请求内失败即换下一个候选，客户端不感知」——internal/gateway/gateway.go:4968-4984。
//
// ⇒ 本文件把「零候选 ⇒ 网关自己换模型」补上：按 auto 决策给出的候选链逐个试，
// 找到一个此刻有节点的模型就改写并继续（后面的 dispatch / executor 流程完全不变）。
//
// ★ 三条边界：
//
//  1. **只对 auto 请求生效**（logCtx.IsAutoRequest）——显式点名模型的请求，
//     零候选的语义是「这个模型确实不可用」，应当如实报 503（客户端可以自己换）。
//  2. **只换模型，不换凭据**：凭据级重选是 dispatch 层的事，本层不越界。
//  3. **链走完仍无节点 ⇒ 照旧 503**：不把「真的没有可用模型」伪装成成功。

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// autoReselectResult 是零候选重选的结果。
type autoReselectResult struct {
	// Switched 为 true 表示找到了可路由的替代模型。
	Switched bool
	// Model 是新选中的模型（Switched 时有效）。
	Model string
	// Body 是改写到新模型的请求体（Switched 时有效）。
	Body []byte
	// Candidates / Policy / Modality 是按新模型重新解析的结果。
	Candidates []provider.Candidate
	Policy     *provider.Policy
	Modality   string
	// Tried 是本轮试过的模型（按链序），用于日志与排障。
	Tried []string
}

// autoRequestReselect 在 auto 选中的模型零候选时，沿 auto 候选链换一个还有节点的模型。
//
// fromModel：当前（零候选的）模型；bodyBytes：当前请求体（其 model 字段即 fromModel）。
//
// 失败语义（零候选、无候选、解析出错）都只是「这个模型不行，试下一个」——
// 只有整条链走完仍无节点才返回 Switched=false，由调用方照旧 503。
//
// ★ 链的选取（根修 5）：
//
//	宽退路池 AutoReselectModels（48h 热度榜 LIMIT 32）**先走**；
//	宽池全灭时回退窄链 AutoFallbackModels（dispatch 换模梯子：tier 链或 top-3）。
//
//	★ 消费规则（两条链的边界）：
//
//	· 宽池命中 ⇒ **窄链保留**——dispatch 的换模梯子还要用（零候选重选发生在
//	  dispatch **之前**，之后上游仍可能失败，那时还需要换模的备选）；
//	· 窄链被真的走过（宽池全灭之后）⇒ 窄链清空（此时再留着只会让 dispatch
//	  绕回刚试过的死模型）。
func (h *ChatHandler) autoRequestReselect(
	ctx context.Context,
	logCtx *RequestLogContext,
	fromModel string,
	profile string,
	tenantID string,
	bodyBytes []byte,
) autoReselectResult {
	result := autoReselectResult{}
	if logCtx == nil || !logCtx.IsAutoRequest {
		return result
	}

	wide := logCtx.AutoReselectModels
	narrow := logCtx.AutoFallbackModels
	logCtx.AutoReselectModels = nil

	// Wide ladder first.
	if h.tryAutoReselectLadder(ctx, logCtx, fromModel, profile, tenantID, bodyBytes, wide, &result) {
		return result
	}
	// Wide ladder exhausted → fall back to the narrow one, and consume it:
	// it is now in play, and keeping it would let dispatch walk back into
	// models we have just proven dead.
	if len(narrow) > 0 {
		logCtx.AutoFallbackModels = nil
		if h.tryAutoReselectLadder(ctx, logCtx, fromModel, profile, tenantID, bodyBytes, narrow, &result) {
			return result
		}
	}
	return result
}

// tryAutoReselectLadder walks the given models (in order) and stops at the
// first one that resolves to a routable candidate, filling result in place.
// Returns true when a switch happened.
func (h *ChatHandler) tryAutoReselectLadder(
	ctx context.Context,
	logCtx *RequestLogContext,
	fromModel string,
	profile string,
	tenantID string,
	bodyBytes []byte,
	models []string,
	result *autoReselectResult,
) bool {
	for _, nextModel := range models {
		if nextModel == "" || nextModel == fromModel {
			continue
		}
		result.Tried = append(result.Tried, nextModel)

		// 先改写请求体，再按新体解析候选：模态判定读的是请求体里的内容
		// （detectRequestModality），带旧模型名去解析会算错模态。
		newBody := rewriteBodyWithModel(bodyBytes, nextModel)
		if len(newBody) == 0 {
			continue
		}
		candidates, policy, modality, err := resolveCandidatesForRequest(
			ctx, h.provider, nextModel, profile, tenantID, newBody,
		)
		if err != nil {
			slog.WarnContext(ctx, "auto: candidate re-resolve failed for fallback model",
				"model", nextModel, "from", fromModel, "error", err)
			continue
		}
		if len(candidates) == 0 {
			slog.InfoContext(ctx, "auto: fallback model has no candidates either; trying the next one",
				"model", nextModel, "from", fromModel)
			continue
		}
		result.Switched = true
		result.Model = nextModel
		result.Body = newBody
		result.Candidates = candidates
		result.Policy = policy
		result.Modality = modality
		return true
	}
	return false
}

// applyAutoReselect writes a successful reselect back into the request
// context: the model name, the rewritten body, the freshly resolved
// candidates, the routing policy and the request modality.
//
// It is a separate, testable function on purpose: the wiring is where a
// silent half-rewrite would show up as "the log says model B but the
// upstream was asked for model A" — a defect no candidate-resolution test
// can see. (Root cause 4, 2026-11-17.)
//
// Returns true when the request was rewritten.
func applyAutoReselect(
	res *autoReselectResult,
	clientModel *string,
	bodyBytes *[]byte,
	candidates *[]provider.Candidate,
	policy **provider.Policy,
	requestModality *string,
) bool {
	if res == nil || !res.Switched {
		return false
	}
	if clientModel != nil {
		*clientModel = res.Model
	}
	if bodyBytes != nil {
		*bodyBytes = res.Body
	}
	if candidates != nil {
		*candidates = res.Candidates
	}
	if policy != nil {
		*policy = res.Policy
	}
	if requestModality != nil {
		*requestModality = res.Modality
	}
	return true
}

// recordAutoReselect 为一次零候选重选留下可观测痕迹：
// live action（前端实时阶段树看得到「换模了」）+ 结构化日志。
func (h *ChatHandler) recordAutoReselect(
	r *http.Request,
	requestID string,
	fromModel string,
	res autoReselectResult,
	candidateCount int,
) {
	if !res.Switched {
		return
	}
	slog.Info("auto: switched model after zero candidates",
		"request_id", requestID,
		"from_model", fromModel,
		"to_model", res.Model,
		"candidates_count", candidateCount,
		"tried", res.Tried,
	)
	h.liveActions.Emit(r.Context(), liveactions.ActionEvent{
		RequestID: requestID,
		Action:    liveactions.ActionModelSwitch,
		Model:     res.Model,
		Detail: map[string]string{
			"from_model":       fromModel,
			"to_model":         res.Model,
			"reason":           "zero_candidates",
			"candidates_count": strconv.Itoa(candidateCount),
		},
	})
}
