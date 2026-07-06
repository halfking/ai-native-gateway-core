package goal

// loop_detector.go — decides when a goal session is "stuck in a loop" and
// picks a fallback model to rotate to.
//
// Two loop signals are tracked:
//   1. Budget exhaustion: auto_continue_count reached goal.max_auto_continue_count
//      (the model has been nudged N times without finishing).
//   2. Response repetition: the model keeps producing the same reply
//      (RepeatCount reached goal.repeat_threshold), which is the clearest sign
//      the current model is wedged — no amount of "please continue" will help.
//
// When either signal fires AND model switching is enabled, the detector picks
// the next model from the configured rotation list and the hook resets the
// continue budget so the new model gets a fresh attempt. Rotation is bounded by
// goal.max_model_switch_count, so even a permanently-stuck task eventually
// terminates (the hook then gives up and the session stays active for the
// client/operator to inspect).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// loopDecision is the outcome of analysing a session's continue state.
type loopDecision struct {
	// canContinue is true when the session still has continue budget left on
	// the CURRENT model and no loop signal fired.
	canContinue bool
	// switchModel is the model to rotate to before retrying. Non-empty only
	// when a loop was detected and model switching produced a fresh target.
	switchModel string
	// giveUp is true when both the continue budget AND the model-switch budget
	// are exhausted — the hook should stop nudging.
	giveUp bool
	// reason is a short human-readable explanation for logging.
	reason string
}

// detectLoop inspects the session counters and decides what to do next. It does
// NOT mutate the store; the caller (ModeHook) performs the actual rotation via
// AtomicModelSwitch + budget reset.
//
// maxContinue / maxSwitch / repeatThreshold are read from settings by the
// caller so they stay hot-reloadable.
func (h *ModeHook) detectLoop(ctx context.Context, sess *Session, req *response.InterceptRequest, maxContinue, maxSwitch, repeatThreshold int) loopDecision {
	budgetExhausted := sess.AutoContinueCount >= maxContinue
	repeating := repeatThreshold > 0 && sess.RepeatCount >= repeatThreshold

	// If neither loop signal fired, the session is healthy — keep nudging the
	// current model.
	if !budgetExhausted && !repeating {
		return loopDecision{canContinue: true, reason: "within_budget"}
	}

	// A loop was detected. Decide whether we're allowed to rotate models.
	switchEnabled := h.loadBool(req.TenantID, "goal.model_switch_on_loop", h.config.ModelSwitchOnLoop)
	if !switchEnabled {
		// Switching disabled → just give up to avoid an infinite loop on a
		// stuck model.
		slog.Warn("goal_loop_detected_no_switch",
			"session_id", req.SessionID,
			"budget_exhausted", budgetExhausted,
			"repeating", repeating,
			"repeat_count", sess.RepeatCount)
		return loopDecision{giveUp: true, reason: "loop_no_switch_configured"}
	}

	// Switching enabled but we've already rotated as many times as allowed.
	if maxSwitch > 0 && sess.ModelSwitchCount >= maxSwitch {
		slog.Warn("goal_loop_detected_switch_budget_exhausted",
			"session_id", req.SessionID,
			"model_switch_count", sess.ModelSwitchCount,
			"max_switch", maxSwitch)
		return loopDecision{giveUp: true, reason: "switch_budget_exhausted"}
	}

	// Pick the next model. Fall back to "auto" (autoroute) when no explicit
	// list is configured, or when the list is exhausted.
	nextModel := h.pickNextModel(sess)
	if nextModel == "" {
		return loopDecision{giveUp: true, reason: "no_fallback_models"}
	}

	cause := "budget_exhausted"
	if repeating {
		cause = "repeated_response"
	}
	slog.Info("goal_loop_detected_switching_model",
		"session_id", req.SessionID,
		"from_model", sess.CurrentModel,
		"to_model", nextModel,
		"cause", cause,
		"repeat_count", sess.RepeatCount)
	return loopDecision{
		switchModel: nextModel,
		reason:      cause,
	}
}

// pickNextModel selects the next model from the rotation list, skipping the one
// currently in use. Returns "" when the list is empty.
func (h *ModeHook) pickNextModel(sess *Session) string {
	candidates := h.fallbackModels(sess.TenantID)
	if len(candidates) == 0 {
		// No explicit list — defer to autoroute.
		return "auto"
	}
	for _, c := range candidates {
		if c != sess.CurrentModel {
			return c
		}
	}
	// Everything in the list equals the current model. Re-use the first so we
	// at least rotate via autoroute resolution rather than giving up.
	return candidates[0]
}

// fallbackModels returns the configured rotation list, honouring settings
// overrides. The list may contain "auto" entries (routed via autoroute) inter-
// mixed with concrete model names.
func (h *ModeHook) fallbackModels(tenantID string) []string {
	// Allow a comma-separated runtime override via settings.
	cfgList := h.loadString(tenantID, "goal.fallback_models", "")
	if cfgList != "" {
		out := splitAndTrim(cfgList, ",")
		if len(out) > 0 {
			return out
		}
	}
	return h.config.FallbackModels
}

// hashResponse produces a stable hex digest of the assistant reply for repeat
// detection. We hash the *content* (not the raw body) so无关 metadata like
// timestamps/logprobs don't break the comparison. Truncated responses share a
// hash with their full form is NOT desired, so we hash the exact content.
func hashResponse(body []byte) string {
	content := extractAssistantContent(body)
	if content == "" {
		// Empty/unparseable — fall back to the raw body so two distinct
		// unparseable bodies don't collide.
		content = string(body)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// recordAndDetect is the bridge between response observation and loop detection.
// It hashes the response, persists the repeat counter, then runs detectLoop.
// Called once per intercepted response (stream & non-stream).
func (h *ModeHook) recordAndDetect(ctx context.Context, sess *Session, req *response.InterceptRequest) loopDecision {
	resetOnProgress := h.loadBool(req.TenantID, "goal.repeat_reset_on_progress", h.config.RepeatResetOnProgress)
	hash := hashResponse(req.ResponseBody)

	repeatCount, err := h.db.RecordResponse(ctx, req.SessionID, hash, resetOnProgress)
	if err != nil {
		slog.Warn("goal_record_response_failed", "error", err, "session_id", req.SessionID)
		// Keep going with the in-memory value; a stale repeat count is a soft
		// failure that should not block the continue decision.
		repeatCount = sess.RepeatCount
	}
	sess.RepeatCount = repeatCount
	sess.LastResponseHash = hash

	maxContinue := h.loadInt(req.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)
	maxSwitch := h.loadInt(req.TenantID, "goal.max_model_switch_count", h.config.MaxModelSwitchCount)
	repeatThreshold := h.loadInt(req.TenantID, "goal.repeat_threshold", h.config.RepeatThreshold)

	return h.detectLoop(ctx, sess, req, maxContinue, maxSwitch, repeatThreshold)
}

// applyModelSwitch performs the atomic rotation: bumps model_switch_count (CAS
// under maxSwitch), resets the continue budget, and records the new model.
// Returns true if this caller won the rotation.
func (h *ModeHook) applyModelSwitch(ctx context.Context, req *response.InterceptRequest, sess *Session, newModel string) bool {
	maxSwitch := h.loadInt(req.TenantID, "goal.max_model_switch_count", h.config.MaxModelSwitchCount)
	won, err := h.db.AtomicModelSwitch(ctx, req.SessionID, newModel, maxSwitch)
	if err != nil {
		slog.Warn("goal_model_switch_failed", "error", err, "session_id", req.SessionID)
		return false
	}
	if !won {
		return false
	}
	sess.ModelSwitchCount++
	sess.CurrentModel = newModel
	sess.AutoContinueCount = 0 // fresh budget for the new model
	return true
}

// splitAndTrim parses a comma-separated settings string into a clean list.
func splitAndTrim(s, sep string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
