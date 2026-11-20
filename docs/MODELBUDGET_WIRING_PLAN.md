# Gateway Modelbudget Request Path Wiring - Implementation Plan

**Status:** Ready for implementation  
**Priority:** P1 (Phase 2 收口周)  
**Prerequisite:** Modelbudget domain already implemented (commit b4c50014c, 11 tests passing)  
**Depends on:** acc-go MaxCostPerTask (0684b1b7), orchestrator budget injection (2cc1eb4f)

## Context

The modelbudget domain is fully implemented in `domains/modelbudget/` with:
- ✅ PricingSource interface for cost estimation
- ✅ Estimator with pricing table for major LLM models
- ✅ Degrader with family-based model degradation
- ✅ 11 tests passing

**What's missing:** Integration into the streaming handler's request path.

## Integration Points

### 1. Import modelbudget domain

**File:** `domains/streaming/handler.go`

Add to imports:
```go
"github.com/kaixuan/llm-gateway-go/domains/modelbudget"
```

### 2. Extract max_cost_per_task from payload

**Location:** Early in request processing, after authentication

The acc-go orchestrator injects `max_cost_per_task` (USD) into the task payload.

```go
// Extract budget from task payload (if present)
var budget float64 // USD
if taskPayload, ok := req["task_payload"].(map[string]interface{}); ok {
    if maxCost, ok := taskPayload["max_cost_per_task"].(float64); ok {
        budget = maxCost
    }
}
```

### 3. Add ownership check ("归属探测后认领")

**Required:** Verify request ownership/authorization before applying budget logic

```go
// Ownership check: verify caller has access to this task/project
if !hasOwnership(ctx, tenantID, projectID, taskID) {
    return errUnauthorized
}
```

Integration point: Use existing authentication/authorization middleware.

### 4. Initialize Degrader with pricing source

**Location:** Handler initialization or per-request

```go
// Initialize once at handler startup
pricing := modelbudget.NewEstimator()
degrader := modelbudget.NewDegrader(
    pricing,
    [][]string{
        // GPT family ladder (expensive → cheap)
        {"gpt-4", "gpt-4-turbo", "gpt-3.5-turbo"},
        // Claude family ladder
        {"claude-3-opus", "claude-3-sonnet", "claude-3-haiku"},
        // Add other families
    },
)
```

### 5. Call Degrader.Select() before routing

**Location:** Before model routing in request handler

```go
// Extract requested model from request
requestedModel := req.Model // or wherever model is specified

// Estimate input/output tokens (use existing logic or approximation)
inputTokens := estimateInputTokens(req.Messages)
outputTokens := estimateOutputTokens(req.MaxTokens) // or use default

// Apply budget-aware model selection
decision := degrader.Select(requestedModel, budget, inputTokens, outputTokens)

// Log decision for observability
log.Info().
    Str("requested_model", requestedModel).
    Str("selected_model", decision.Model).
    Bool("degraded", decision.Degraded).
    Bool("fits_budget", decision.FitsBudget).
    Float64("estimated_cost", decision.EstimatedCost).
    Str("reason", decision.Reason).
    Msg("modelbudget decision")

// Use decision.Model for routing
actualModel := decision.Model
```

### 6. Handle budget violations

**When FitsBudget = false:**

Option A: Reject request with informative error
```go
if !decision.FitsBudget {
    return &BudgetExceededError{
        RequestedModel: requestedModel,
        Budget:         budget,
        EstimatedCost:  decision.EstimatedCost,
        CheapestOption: decision.Model,
    }
}
```

Option B: Proceed with cheapest model (caller decision)
```go
// Decision.Model already contains the cheapest option
// Proceed with warning in metadata
```

### 7. Update routing logic

**File:** Wherever model routing happens (autoroute integration)

```go
// OLD: route to req.Model
// NEW: route to decision.Model

routeConfig := autoroute.Config{
    Model:    decision.Model,  // Use budget-selected model
    Provider: selectProvider(decision.Model),
    // ... other config
}
```

### 8. Add metadata to response

Include budget decision in response metadata for transparency:

```go
metadata := map[string]interface{}{
    "model_selection": map[string]interface{}{
        "requested":       requestedModel,
        "actual":          decision.Model,
        "degraded":        decision.Degraded,
        "estimated_cost":  decision.EstimatedCost,
        "budget":          budget,
        "fits_budget":     decision.FitsBudget,
    },
}
```

## Testing Strategy

### Unit Tests

1. **Test budget extraction from payload**
   - Valid budget present
   - No budget present (skip modelbudget logic)
   - Invalid budget format

2. **Test ownership check integration**
   - Authorized request proceeds
   - Unauthorized request rejected
   - Missing credentials

3. **Test model degradation**
   - Within budget: use requested model
   - Over budget: degrade to cheaper family member
   - Way over budget: use cheapest, return error/warning

4. **Test routing with degraded model**
   - Verify correct provider selected for degraded model
   - Verify request forwarded with correct model name

### Integration Tests

1. **End-to-end budget enforcement**
   - Send request with max_cost_per_task
   - Verify expensive model degraded to cheaper
   - Verify response metadata includes decision

2. **Contract tests with acc-go**
   - Verify budget format matches acc-go injection
   - Test with real budget values from orchestrator

## Implementation Checklist

- [ ] Import modelbudget domain in streaming/handler.go
- [ ] Extract max_cost_per_task from task payload
- [ ] Add ownership/authorization check
- [ ] Initialize Degrader with pricing + family ladders
- [ ] Call Degrader.Select() before routing
- [ ] Handle FitsBudget = false cases
- [ ] Update routing logic to use decision.Model
- [ ] Add budget decision to response metadata
- [ ] Write unit tests for budget extraction
- [ ] Write unit tests for degradation logic integration
- [ ] Write integration test for e2e budget enforcement
- [ ] Update documentation
- [ ] Test with real traffic (canary/staging)

## File Changes Summary

**Modified:**
- `domains/streaming/handler.go` - Main integration (import, budget extraction, degrader call, routing)
- `domains/streaming/session_routing.go` - If routing logic needs updates
- Possibly `autoroute/*` - If routing config needs budget awareness

**New:**
- `domains/streaming/modelbudget_integration_test.go` - Integration tests

**Tests:**
- Add ~5-8 unit tests for handler integration
- Add 2-3 integration tests for e2e flow

## Estimated Effort

- **Implementation:** 2-3 hours
- **Testing:** 1-2 hours
- **Total:** 3-5 hours

## Dependencies

**Upstream (already done):**
- ✅ acc-go MaxCostPerTask gate (0684b1b7)
- ✅ Orchestrator budget injection (2cc1eb4f)
- ✅ Gateway modelbudget domain (b4c50014c)

**Downstream (blocked by this):**
- ❌ 两侧真 e2e 契约钉测 (e2e contract tests)

## Notes

1. **"留 resident"**: The note says to leave for resident session handling. Ensure the wiring doesn't interfere with resident session routing.

2. **"归属探测后认领"**: Ownership check is critical - only apply budget logic after verifying request authorization.

3. **Unknown prices**: Per contract, unknown prices estimate to 0 and pass through (don't reject).

4. **Observability**: Log all budget decisions for debugging and monitoring.

5. **Backward compatibility**: Requests without max_cost_per_task should work normally (skip budget logic).

## Next Steps

1. Read streaming/handler.go to understand full request flow
2. Locate exact point for budget extraction
3. Implement budget extraction + ownership check
4. Wire degrader call before routing
5. Write tests
6. Deploy to staging and verify with test traffic
7. Document the integration for team
