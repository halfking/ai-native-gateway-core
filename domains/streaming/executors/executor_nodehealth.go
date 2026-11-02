package executors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

func (e *Executor) nodeOutcomeAuthority() (*nodehealth.OutcomeReducer, nodehealth.Adapter) {
	if e == nil {
		return nil, nil
	}
	e.nodeHealthMu.Lock()
	defer e.nodeHealthMu.Unlock()
	if e.NodeOutcomeReducer == nil {
		e.NodeOutcomeReducer = nodehealth.NewOutcomeReducer()
	}
	adapter := e.NodeHealthAdapter
	if adapter == nil {
		adapter = executorNodeHealthAdapter{executor: e}
	}
	return e.NodeOutcomeReducer, adapter
}

func (e *Executor) reduceDispatchOutcome(ctx context.Context, observation nodehealth.Observation) (nodehealth.Decision, bool) {
	reducer, adapter := e.nodeOutcomeAuthority()
	if reducer == nil {
		return nodehealth.Decision{}, false
	}
	decision, err := reducer.ReduceAndApply(ctx, observation, adapter)
	if err != nil {
		slog.Warn("dispatch node-health outcome failed",
			"attempt_id", observation.AttemptID,
			"credential_id", observation.Node.CredentialID,
			"model", observation.Node.Model,
			"error", err)
		return decision, false
	}
	return decision, decision.Accepted
}

func (e *Executor) reduceDispatchForwardOutcome(
	ctx context.Context,
	params *ExecParams,
	cand provider.Candidate,
	attemptID string,
	out dispatch.ForwardOutcome,
	startedAt time.Time,
	healthEvidence bool,
) (nodehealth.Decision, bool) {
	// Node health and URSM state must use the upstream raw model. A standardized
	// client-facing name can map to multiple provider model bindings, so using it
	// here would merge their empty-response windows and misroute sibling models.
	model := cand.BindingRawModel()
	if model == "" {
		model = strings.TrimSpace(cand.StandardizedName)
	}
	if model == "" && params != nil {
		model = strings.TrimSpace(params.Model)
	}
	requestID := ""
	tenantID := ""
	if params != nil {
		requestID = params.RequestID
		tenantID = params.TenantID
		if requestID == "" && params.R != nil {
			requestID = params.R.Header.Get("X-Request-Id")
		}
	}

	latencyMs := int(time.Since(startedAt).Milliseconds())
	if result, ok := out.Result.(*ExecuteResult); ok && result != nil && result.LatencyMs > 0 {
		latencyMs = result.LatencyMs
	}
	outcome := requestjourney.OutcomeSuccess
	kind := nodehealth.ErrorKind("")
	if out.Err != nil {
		execKind := classifyExecError(out.Err)
		if errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded) || execKind == errorsx.KindCanceled {
			outcome = requestjourney.OutcomeCanceled
		} else {
			outcome = requestjourney.OutcomeFailure
			kind = nodeHealthErrorKind(execKind)
			if !healthEvidence {
				kind = nodehealth.ErrorKindRequest
			}
		}
	}
	detail := ""
	if out.Err != nil {
		detail = truncateForStore(out.Err.Error())
	}
	return e.reduceDispatchOutcome(ctx, nodehealth.Observation{
		Node: nodehealth.NodeKey{
			TenantID:     tenantID,
			ProviderID:   int64(cand.ProviderID),
			CredentialID: int64(cand.CredentialID),
			Model:        model,
		},
		AttemptID:   attemptID,
		Phase:       nodehealth.PhaseRequest,
		Outcome:     outcome,
		ErrorKind:   kind,
		HTTPStatus:  out.HTTPStatus,
		RequestID:   requestID,
		BillingMode: cand.BillingMode,
		LatencyMs:   latencyMs,
		ErrorDetail: detail,
	})
}

func dispatchOutcomeConsumesProbe(decision nodehealth.Decision, applied bool) bool {
	if !applied {
		return false
	}
	for _, effect := range decision.Effects {
		if effect.Kind == nodehealth.EffectRecordCircuitFailure || effect.Kind == nodehealth.EffectRecoverCircuit {
			return true
		}
	}
	return false
}

func dispatchHTTPStatus(err error) int {
	var upstreamErr *upstreampkg.Error
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return upstreamErr.StatusCode
	}
	return 0
}

type executorNodeHealthAdapter struct {
	executor *Executor
}

// nodeFlashBlipConfirmTotal observes the Wave 3 B2② double-confirm verdicts
// (confirmed = degrade applied after both pings failed; transient = a ping
// succeeded and the node keeps its healthy state; busy = a confirm was
// already running for the node).
var nodeFlashBlipConfirmTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_node_flash_blip_confirm_total",
		Help: "Flash-blip double-confirm verdicts after the first consecutive network/timeout failure of a node.",
	},
	[]string{"result"},
)

// flashBlipEligible reports whether this decision is the FIRST consecutive
// failure of the node with a flash-blip capable error kind. Only then is the
// degrade deferred behind the double confirmation; later consecutive
// failures (the node is already suspect) degrade immediately as before.
func flashBlipEligible(decision nodehealth.Decision, confirm NodeProbeConfirmFunc) bool {
	if confirm == nil || decision.Outcome != requestjourney.OutcomeFailure {
		return false
	}
	if decision.ConsecutiveFailures != 1 {
		return false
	}
	switch decision.ErrorKind {
	case nodehealth.ErrorKindNetwork, nodehealth.ErrorKindTimeout:
		return true
	default:
		return false
	}
}

func (a executorNodeHealthAdapter) ApplyNodeHealthDecision(ctx context.Context, decision nodehealth.Decision) error {
	e := a.executor
	if e == nil {
		return nil
	}
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	// A named model rejection is binding evidence even when upstream
	// classification fell through to ErrorKindUpstream. The reducer intentionally
	// remains classification-only and may emit the full third-failure effect set;
	// the adapter owns this detail-aware scope correction so every effect consumer
	// sees the same narrowed decision.
	_, modelScopedOverride := errorsx.ModelScopedRejection([]byte(decision.ErrorDetail))
	deferDegrade := flashBlipEligible(decision, e.NodeProbeConfirm)
	var errs []error
	var deferred []nodehealth.Effect
	for _, effect := range decision.Effects {
		if modelScopedOverride && isCredentialWideNodeHealthEffect(effect.Kind) {
			continue
		}
		switch effect.Kind {
		case nodehealth.EffectRecordCircuitFailure:
			// Circuit semantics are unchanged by the flash-blip gate: the
			// original request failure keeps recording; the confirm pings
			// themselves never touch the breaker ("不叠加熔断").
			if e.Circuit != nil {
				providerID, credentialID := int(decision.Node.ProviderID), int(decision.Node.CredentialID)
				// P0-4 rest 分级：quota 族失败且正文携带重置信号（绝对时刻
				// /相对时长/欠费词）时，冷却对齐到该时刻而非指数梯——上游
				// 显式重置时间是硬事实，不随 billing mode 缩短，但 free 标记
				// 仍要预置，让后续非对齐失败沿用 freeTierPolicies 档。
				if errorsx.IsQuotaExhaustion(kind) && strings.TrimSpace(decision.ErrorDetail) != "" {
					if hint := errorsx.GradeQuotaRest([]byte(decision.ErrorDetail), time.Now()); hint.HasReset {
						if strings.EqualFold(strings.TrimSpace(decision.BillingMode), "free") {
							if brk := e.Circuit.Get(providerID, credentialID); brk != nil {
								brk.MarkFreeTier()
							}
						}
						e.Circuit.RecordFailureWithReset(providerID, credentialID, kind, hint.ResetAt, true)
						continue
					}
				}
				// 2026-09-15 (245 free-capacity plan): billing-mode-aware
				// recording lets free credentials adopt the shortened
				// freeTierPolicies cooling profile inside the breaker.
				e.Circuit.RecordFailureWithBillingMode(providerID, credentialID, kind, decision.BillingMode)
			}
		case nodehealth.EffectRecoverCircuit:
			if e.Circuit != nil {
				e.Circuit.RecordSuccess(int(decision.Node.ProviderID), int(decision.Node.CredentialID))
			}
		case nodehealth.EffectRestoreBinding:
			if state := e.nodeHealthStateWriter(); state != nil && state.Enabled() {
				if err := state.RestoreOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
				}
			}
		case nodehealth.EffectPersistNodeStatus:
			if decision.Outcome == requestjourney.OutcomeSuccess {
				e.applyNodeHealthPersist(ctx, decision, &errs)
			} else if deferDegrade {
				deferred = append(deferred, effect)
			} else {
				e.applyNodeHealthPersist(ctx, decision, &errs)
			}
		case nodehealth.EffectSetBindingUnavailable, nodehealth.EffectSetCredentialUnavailable,
			nodehealth.EffectUpdateURSM, nodehealth.EffectInvalidateCandidateCache:
			if deferDegrade {
				deferred = append(deferred, effect)
			} else {
				e.applyNodeHealthDegradeEffect(ctx, decision, effect, &errs)
			}
		case nodehealth.EffectScheduleProbe:
			// UnifiedProbeScheduler 已于 2026-09-01 作为死代码移除；
			// 探测调度由 ProbeQueue/StateObserver 路径接管。
		case nodehealth.EffectCancelProbeBackoff:
			if e.NodeProbeHealthy != nil {
				if err := e.NodeProbeHealthy(ctx, int(decision.Node.CredentialID), decision.Node.Model); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if len(deferred) > 0 {
		e.scheduleFlashBlipConfirm(ctx, decision, deferred)
	}
	return errors.Join(errs...)
}

// applyNodeHealthPersist applies the EffectPersistNodeStatus arm (legacy
// state observer writers).
func (e *Executor) applyNodeHealthPersist(ctx context.Context, decision nodehealth.Decision, errs *[]error) {
	if e.StateObserver == nil || !e.legacyWritersEnabled() {
		return
	}
	if decision.Outcome == requestjourney.OutcomeSuccess {
		e.StateObserver.UpdateOnSuccess(ctx, int(decision.Node.CredentialID), decision.Node.Model, decision.LatencyMs, decision.RequestID)
		return
	}
	if decision.Outcome != requestjourney.OutcomeFailure {
		return
	}
	// The legacy observer can independently reach its credential-wide cooling
	// threshold. Keep it out of a named-model rejection, otherwise filtering
	// only SetCredentialUnavailable above still lets siblings be collateral.
	if _, modelScoped := errorsx.ModelScopedRejection([]byte(decision.ErrorDetail)); modelScoped {
		return
	}
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	e.StateObserver.UpdateOnFailure(ctx, int(decision.Node.CredentialID), decision.Node.Model, kind, decision.RequestID, decision.Node.TenantID, decision.BillingMode)
}

// isCredentialWideNodeHealthEffect marks effects that can contaminate sibling
// models on a credential. Model-scoped overrides retain node-specific effects
// (binding state, URSM, cache invalidation, scheduling and probe bookkeeping).
func isCredentialWideNodeHealthEffect(kind nodehealth.EffectKind) bool {
	switch kind {
	case nodehealth.EffectRecordCircuitFailure, nodehealth.EffectSetCredentialUnavailable:
		return true
	default:
		return false
	}
}

func (e *Executor) nodeHealthStateWriter() interface {
	Enabled() bool
	RestoreOnSuccess(context.Context, int, string) error
	WriteOnError(context.Context, int, string, credential.Failure) error
	SetCredentialUnavailable(context.Context, int, credential.Failure) error
} {
	if e == nil {
		return nil
	}
	if e.NodeHealthState != nil {
		return e.NodeHealthState
	}
	return e.State
}

// applyNodeHealthDegradeEffect applies one degrade-family effect (binding /
// credential unavailable, URSM failure record, candidate cache
// invalidation).
// ursmOutcomeFromDecision 把 nodehealth 的归一化决策翻译成 URSM v2 的写入意图。
//
// 抽成纯函数而不是内联在 applyNodeHealthDegradeEffect 里，是为了**让这段可测**：
// e.URSMv2 是具体类型 *ursmv2.Manager 而非接口，EffectUpdateURSM 分支此前
// 无法在不构造真实 Manager 的前提下写单测 —— 这正是 HealthStatus 被漏掉
// 两年都没人发现的结构性原因。
//
// 2026-10-03 补 HealthStatus：此前本映射**没有**这一项，于是
// ursmv2api.RequestOutcome.HealthStatus 恒空 → store 侧恒空 →
// record_request.lua 的 `valid_health[""]` 恒假 → `HSET node_key "health"`
// 永不执行 → health_status typed 列在生产上 100% 为空。
// 而 api/types.go 的字段契约写着 "Supplied by the executor"。
//
// 词表对齐：decision.Status 是 requestjourney.NodeHealthStatus（11 值），
// api 侧只认 5 个（healthy/suspect/degraded/recovering/quarantined）。
// 其余（unknown/cooling/probing/disabled/unhealthy）不是「健康信号」，
// 交给 lua 的 valid_health 表拒掉并保持原值 —— 语义正确，此处不过滤。
func ursmOutcomeFromDecision(decision nodehealth.Decision, kind credential.ErrorKind) ursmv2api.RequestOutcome {
	return ursmv2api.RequestOutcome{
		CredentialID: int(decision.Node.CredentialID),
		RawModel:     decision.Node.Model,
		TenantID:     decision.Node.TenantID,
		BillingMode:  decision.BillingMode,
		Success:      decision.Outcome == requestjourney.OutcomeSuccess,
		LatencyMs:    decision.LatencyMs,
		ErrorKind:    string(kind),
		RequestID:    decision.RequestID,
		DedupKey:     decision.AttemptID,
		HealthStatus: string(decision.Status),
	}
}

func (e *Executor) applyNodeHealthDegradeEffect(ctx context.Context, decision nodehealth.Decision, effect nodehealth.Effect, errs *[]error) {
	kind := errorKindFromNodeHealth(decision.ErrorKind)
	switch effect.Kind {
	case nodehealth.EffectSetBindingUnavailable:
		if state := e.nodeHealthStateWriter(); state != nil && state.Enabled() {
			failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
			if err := state.WriteOnError(ctx, int(decision.Node.CredentialID), decision.Node.Model, failure); err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectSetCredentialUnavailable:
		if state := e.nodeHealthStateWriter(); state != nil && state.Enabled() {
			failure := credential.Failure{Kind: kind, Detail: decision.ErrorDetail}
			if err := state.SetCredentialUnavailable(ctx, int(decision.Node.CredentialID), failure); err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectUpdateURSM:
		if e.URSMv2 != nil {
			err := e.URSMv2.RecordRequest(ctx, ursmOutcomeFromDecision(decision, kind))
			if err != nil {
				*errs = append(*errs, err)
			}
		}
	case nodehealth.EffectInvalidateCandidateCache:
		provider.InvalidateCandidateCacheForCredential(int(decision.Node.CredentialID))
	}
}

// scheduleFlashBlipConfirm runs the deferred degrade behind the double
// confirmation (Wave 3 B2②). One confirm per node at a time; the goroutine
// outlives the request (WithoutCancel) and applies the deferred effects only
// when both pings failed. A concurrent confirm or a spawn race leaves the
// state untouched — the running confirm's verdict already converges the
// node's state either way.
func (e *Executor) scheduleFlashBlipConfirm(requestCtx context.Context, decision nodehealth.Decision, deferred []nodehealth.Effect) {
	key := fmt.Sprintf("%d|%s", decision.Node.CredentialID, decision.Node.Model)
	e.flashBlipMu.Lock()
	if e.flashBlipInFlight == nil {
		e.flashBlipInFlight = make(map[string]struct{})
	}
	if _, busy := e.flashBlipInFlight[key]; busy {
		e.flashBlipMu.Unlock()
		nodeFlashBlipConfirmTotal.WithLabelValues("busy").Inc()
		return
	}
	e.flashBlipInFlight[key] = struct{}{}
	e.flashBlipMu.Unlock()

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("flash-blip confirm panic", "recover", rec, "credential_id", decision.Node.CredentialID)
			}
			e.flashBlipMu.Lock()
			delete(e.flashBlipInFlight, key)
			e.flashBlipMu.Unlock()
		}()
		// Wait out the blip window before the first ping; the confirm
		// itself owns its pacing and budget.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), 45*time.Second)
		defer cancel()
		confirmedBroken := e.NodeProbeConfirm(ctx, int(decision.Node.CredentialID), decision.Node.Model)
		if !confirmedBroken {
			nodeFlashBlipConfirmTotal.WithLabelValues("transient").Inc()
			slog.Info("node flash-blip: transient error confirmed, degrade suppressed",
				"credential_id", decision.Node.CredentialID,
				"model", decision.Node.Model,
			)
			return
		}
		nodeFlashBlipConfirmTotal.WithLabelValues("confirmed").Inc()
		slog.Warn("node flash-blip: both confirm pings failed, applying deferred degrade",
			"credential_id", decision.Node.CredentialID,
			"model", decision.Node.Model,
		)
		var errs []error
		for _, effect := range deferred {
			if effect.Kind == nodehealth.EffectPersistNodeStatus {
				e.applyNodeHealthPersist(ctx, decision, &errs)
				continue
			}
			e.applyNodeHealthDegradeEffect(ctx, decision, effect, &errs)
		}
		if err := errors.Join(errs...); err != nil {
			slog.Warn("flash-blip deferred degrade partially failed",
				"credential_id", decision.Node.CredentialID,
				"model", decision.Node.Model,
				"error", err,
			)
		}
	}()
}

func (e *Executor) recordProtocolCircuitSuccess(params *ExecParams, providerID, credentialID int) {
	if params != nil && params.DispatchAttempt {
		return
	}
	if e.Circuit != nil {
		e.Circuit.RecordSuccess(providerID, credentialID)
	}
}

func (e *Executor) recordProtocolCircuitFailure(params *ExecParams, providerID, credentialID int, kind errorsx.ErrorKind, billingMode string) {
	if kind == errorsx.KindEmptyResponse {
		// Empty responses are recorded on the tenant/credential/raw-model URSM
		// node for soft routing penalties. The legacy circuit is only keyed by
		// provider/credential and would incorrectly suppress sibling models.
		return
	}
	// P0-C：与 Breaker.RecordFailure 同矩阵——PenaltyNone（json-word /
	// client_bug / context_length…）永不进协议腿熔断。
	if errorsx.PenaltyOf(kind).Target == errorsx.PenaltyNone {
		return
	}
	// 2026-11-02（4xx 归因族收口轮）：**绑定级**证据不进**账号级**断路器。
	//
	// ★ 断路器按 provider+credential 记账 ⇒ 一次绑定级失败连坐该凭据上的
	//   **全部**模型 ⇒ 3 次即整凭据熔断 ⇒ 全租户 503（2026-11-02 事故本体）。
	//   model_unavailable 的语义是「上游不提供这一对」⇒ 账号本身健康
	//   ⇒ 只写这一对的冷却（credential.Writer 的绑定级分级桶），
	//   断路器不得计数。
	if kind == errorsx.KindModelUnavailable {
		return
	}
	if params != nil && params.DispatchAttempt {
		return
	}
	if e.Circuit != nil {
		// R31 (audit 2026-09-16 §四#5): the legacy protocol paths (chat /
		// anthropic stream interruption) previously recorded through plain
		// RecordFailure, so a free credential failing here never adopted the
		// freeTierPolicies cooling profile and opened at the paid cadence —
		// the exact flap the 245 free-capacity plan eliminated on the V2
		// dispatch path only.
		e.Circuit.RecordFailureWithBillingMode(providerID, credentialID, kind, billingMode)
	}
}

func (e *Executor) writeProtocolCredentialStateOnError(params *ExecParams, ctx context.Context, credentialID int, model string, kind errorsx.ErrorKind, err error) {
	if params != nil && params.DispatchAttempt {
		return
	}
	e.writeCredentialStateOnError(ctx, credentialID, model, kind, err)
}

func nodeHealthErrorKind(kind errorsx.ErrorKind) nodehealth.ErrorKind {
	// 2026-11-02：请求侧错误族一律映射成 ErrorKindRequest ⇒ reducer 的
	// effectsFor 直接返回 nil ⇒ 不写绑定、不写凭据不可用、不记熔断、
	// 不更新 URSM、不排探针、**且不累计 consecutive_failures**。
	// 这一条是本轮事故里「3 连败 ⇒ 整凭据 cooling」那条链的断点。
	// （IsRequestSideError ⊇ IsClientBug，故既有行为不变。）
	if errorsx.IsRequestSideError(kind) || errorsx.IsContentFilter(kind) || kind == errorsx.KindContextLength || kind == errorsx.KindCanceled {
		return nodehealth.ErrorKindRequest
	}
	switch kind {
	case errorsx.KindNetwork:
		return nodehealth.ErrorKindNetwork
	case errorsx.KindTimeout, errorsx.KindStreamTimeout:
		return nodehealth.ErrorKindTimeout
	case errorsx.KindRateLimit, errorsx.KindConcurrent:
		return nodehealth.ErrorKindRateLimit
	case errorsx.KindAuth, errorsx.KindAuthRevoked:
		return nodehealth.ErrorKindAuth
	case errorsx.KindQuota, errorsx.KindQuotaBalance, errorsx.KindQuotaPeriodic, errorsx.KindQuotaPermanent:
		return nodehealth.ErrorKindQuota
	case errorsx.KindModelNotFound, errorsx.KindModelDeprecated:
		return nodehealth.ErrorKindModelBinding
	case errorsx.KindNoAvailableChannel:
		// 2026-11-02（第九轮 · F-B）：「无可用后端」**不是**单一档位——
		// 归责层级取决于上游状态码：
		//   4xx（中转的 "No enterprise provider is available for this request
		//   path: POST /v1/messages … model series: claude."）⇒ 证据指向
		//   **这一对 (凭据,模型)** ⇒ 绑定级（只冷却这一对、保留跨凭据转移）；
		//   5xx（中转分组的 "No available channel for model X under group Y"）
		//   ⇒ 证据指向**中转整体** ⇒ 凭据级（连败到阈值就熔断该凭据）。
		// ★ 判据在 nodehealth.reducer 的 isBindingScoped 里（它能拿到
		//   Observation.HTTPStatus）；这里只负责**把 kind 传到那一档**，
		//   不要在这里塌成 ErrorKindUpstream —— 塌了就再也分不出 4xx/5xx。
		//   理由见 docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md
		//   §十.6（F-B 登记）与 §十二（本轮实施记录）。
		return nodehealth.ErrorKindNoAvailableChannel
	case errorsx.KindModelUnavailable:
		// 2026-11-02（4xx 归因族收口轮）：裸 404 = 「上游不提供这一对」。
		// 归**绑定级**（与 model_binding 同族）⇒ reducer 只写这一对，
		// **不**记熔断、**不**置账号 cooling、**不**累计账号连败；
		// 同时**不**落请求侧（ErrorKindRequest）——落请求侧会一个字节的
		// 绑定级状态都不写，缺口留给探针阶梯（§六.7 登记的那个缺口）。
		return nodehealth.ErrorKindModelUnavailable
	case errorsx.KindEmptyResponse:
		return nodehealth.ErrorKindEmptyResponse
	default:
		return nodehealth.ErrorKindUpstream
	}
}

func errorKindFromNodeHealth(kind nodehealth.ErrorKind) errorsx.ErrorKind {
	switch kind {
	case nodehealth.ErrorKindNetwork:
		return errorsx.KindNetwork
	case nodehealth.ErrorKindTimeout:
		return errorsx.KindTimeout
	case nodehealth.ErrorKindRateLimit:
		return errorsx.KindRateLimit
	case nodehealth.ErrorKindAuth:
		return errorsx.KindAuth
	case nodehealth.ErrorKindQuota:
		return errorsx.KindQuotaPermanent
	case nodehealth.ErrorKindModelBinding:
		return errorsx.KindModelNotFound
	case nodehealth.ErrorKindNoAvailableChannel:
		// 2026-11-02（第九轮 · F-B）：与 nodeHealthErrorKind 对称回写。
		// ★ 回写的是 **no_available_channel**，不是 model_not_found ——
		//   写进 credential.Writer 的是 reason='auto_no_available_channel'
		//   （绑定级冷却），不是「模型不存在」那套终态语义。
		return errorsx.KindNoAvailableChannel
	case nodehealth.ErrorKindModelUnavailable:
		return errorsx.KindModelUnavailable
	case nodehealth.ErrorKindEmptyResponse:
		return errorsx.KindEmptyResponse
	case nodehealth.ErrorKindRequest:
		// Round-trip of nodeHealthErrorKind(client_bug…) — must not collapse
		// to KindTransient or a forced EffectRecordCircuitFailure would cool
		// the node (P0-C / r1027 cascade).
		return errorsx.KindClientBug
	default:
		return errorsx.KindTransient
	}
}
