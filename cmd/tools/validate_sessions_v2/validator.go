package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// ValidationCheckV2 represents one validation check result (extended from main.go)
type ValidationCheckV2 struct {
	Name        string      `json:"name"`
	Severity    string      `json:"severity"` // "error" | "warning"
	Passed      bool        `json:"passed"`
	V1Value     interface{} `json:"v1_value,omitempty"`
	V2Value     interface{} `json:"v2_value,omitempty"`
	Description string      `json:"description"`
	Details     []string    `json:"details,omitempty"`
}

// SessionValidator validates V1 and V2 data for a session
type SessionValidator struct {
	tenantID  string
	sessionID string
}

// NewSessionValidator creates a new validator for a specific session
func NewSessionValidator(tenantID, sessionID string) *SessionValidator {
	return &SessionValidator{
		tenantID:  tenantID,
		sessionID: sessionID,
	}
}

// ValidateSession runs all validation checks for a session
func (v *SessionValidator) ValidateSession(v1Turns []V1Turn, v2Turns []V2Turn, v2Bodies []V2Body, v2Session *V2Session) []ValidationCheckV2 {
	var checks []ValidationCheckV2
	
	// Check 1: Request ID Parity
	checks = append(checks, v.checkRequestIDParity(v1Turns, v2Turns))
	
	// Check 2: Timestamp Consistency
	checks = append(checks, v.checkTimestampConsistency(v1Turns, v2Turns))
	
	// Check 3: Token Sum
	checks = append(checks, v.checkTokenSum(v1Turns, v2Turns))
	
	// Check 4: Cost Sum
	checks = append(checks, v.checkCostSum(v1Turns, v2Turns))
	
	// Check 5: Metadata Consistency
	checks = append(checks, v.checkMetadataConsistency(v1Turns, v2Turns))
	
	// Check 6: Session Snapshot Accuracy (only if V2 session exists)
	if v2Session != nil {
		checks = append(checks, v.checkSnapshotAccuracy(v2Turns, v2Session))
	}
	
	// Check 7: Bodies Integrity
	checks = append(checks, v.checkBodiesIntegrity(v2Turns, v2Bodies))
	
	return checks
}

// checkRequestIDParity verifies V1 and V2 have the same set of request IDs
func (v *SessionValidator) checkRequestIDParity(v1Turns []V1Turn, v2Turns []V2Turn) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Request ID Parity",
		Severity: "error",
		Passed:   true,
	}
	
	// Build sets of request IDs
	v1IDs := make(map[string]bool)
	for _, turn := range v1Turns {
		v1IDs[turn.RequestID] = true
	}
	
	v2IDs := make(map[string]bool)
	for _, turn := range v2Turns {
		v2IDs[turn.RequestID] = true
	}
	
	check.V1Value = len(v1IDs)
	check.V2Value = len(v2IDs)
	
	var missingInV2, missingInV1 []string
	
	// Check V1 -> V2
	for reqID := range v1IDs {
		if !v2IDs[reqID] {
			missingInV2 = append(missingInV2, reqID)
		}
	}
	
	// Check V2 -> V1
	for reqID := range v2IDs {
		if !v1IDs[reqID] {
			missingInV1 = append(missingInV1, reqID)
		}
	}
	
	if len(missingInV2) > 0 || len(missingInV1) > 0 {
		check.Passed = false
		if len(missingInV2) > 0 {
			check.Description = fmt.Sprintf("Missing in V2: %d request(s)", len(missingInV2))
			check.Details = append(check.Details, fmt.Sprintf("Missing in V2: %v", missingInV2))
		}
		if len(missingInV1) > 0 {
			check.Description += fmt.Sprintf(" Missing in V1: %d request(s)", len(missingInV1))
			check.Details = append(check.Details, fmt.Sprintf("Missing in V1: %v", missingInV1))
		}
	} else {
		check.Description = fmt.Sprintf("All %d requests present in both V1 and V2", len(v1IDs))
	}
	
	return check
}

// checkTimestampConsistency verifies timestamps match between V1 and V2
func (v *SessionValidator) checkTimestampConsistency(v1Turns []V1Turn, v2Turns []V2Turn) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Timestamp Consistency",
		Severity: "error",
		Passed:   true,
	}
	
	// Build V1 map by request_id
	v1ByReqID := make(map[string]time.Time)
	for _, turn := range v1Turns {
		v1ByReqID[turn.RequestID] = turn.Ts
	}
	
	var mismatches []string
	for _, v2Turn := range v2Turns {
		v1Ts, exists := v1ByReqID[v2Turn.RequestID]
		if !exists {
			continue // Skip if not in V1 (will be caught by parity check)
		}
		
		// Compare timestamps (allow 1 second tolerance for precision)
		if v1Ts.Sub(v2Turn.Ts).Abs() > time.Second {
			check.Passed = false
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: V1=%s, V2=%s",
				v2Turn.RequestID,
				v1Ts.Format(time.RFC3339),
				v2Turn.Ts.Format(time.RFC3339),
			))
		}
	}
	
	if !check.Passed {
		check.Description = fmt.Sprintf("%d timestamp mismatch(es)", len(mismatches))
		check.Details = mismatches
	} else {
		check.Description = "All timestamps match"
	}
	
	return check
}

// checkTokenSum verifies token counts are consistent (with tolerance)
func (v *SessionValidator) checkTokenSum(v1Turns []V1Turn, v2Turns []V2Turn) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Token Sum",
		Severity: "warning", // Token differences are warnings, not errors
		Passed:   true,
	}
	
	// Sum V1 tokens (from usage JSONB)
	var v1Tokens int64
	for _, turn := range v1Turns {
		var usage map[string]interface{}
		if err := json.Unmarshal(turn.Usage, &usage); err == nil {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				v1Tokens += int64(prompt)
			}
			if completion, ok := usage["completion_tokens"].(float64); ok {
				v1Tokens += int64(completion)
			}
		}
	}
	
	// Sum V2 tokens
	var v2Tokens int64
	for _, turn := range v2Turns {
		v2Tokens += int64(turn.PromptTokens + turn.CompletionTokens)
	}
	
	check.V1Value = v1Tokens
	check.V2Value = v2Tokens
	
	// Calculate tolerance: 1% or 10 tokens, whichever is larger
	diff := abs(v1Tokens - v2Tokens)
	tolerance := int64(10)
	if pct := int64(float64(v1Tokens) * 0.01); pct > tolerance {
		tolerance = pct
	}
	
	if diff > tolerance {
		check.Passed = false
		check.Description = fmt.Sprintf(
			"Token mismatch: V1=%d, V2=%d (diff=%d, tolerance=%d)",
			v1Tokens, v2Tokens, diff, tolerance,
		)
	} else {
		check.Description = fmt.Sprintf("Token sums match (V1=%d, V2=%d)", v1Tokens, v2Tokens)
	}
	
	return check
}

// checkCostSum verifies cost calculations are consistent (with tolerance)
func (v *SessionValidator) checkCostSum(v1Turns []V1Turn, v2Turns []V2Turn) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Cost Sum",
		Severity: "warning", // Cost differences are warnings
		Passed:   true,
	}
	
	// Sum V1 costs
	var v1Cost float64
	for _, turn := range v1Turns {
		v1Cost += turn.CostUSD
	}
	
	// Sum V2 costs
	var v2Cost float64
	for _, turn := range v2Turns {
		v2Cost += turn.CostUSD
	}
	
	check.V1Value = v1Cost
	check.V2Value = v2Cost
	
	// Calculate tolerance: $0.01 or 1%, whichever is larger
	diff := abs64(v1Cost - v2Cost)
	tolerance := max64(0.01, v1Cost*0.01)
	
	if diff > tolerance {
		check.Passed = false
		check.Description = fmt.Sprintf(
			"Cost mismatch: V1=$%.6f, V2=$%.6f (diff=$%.6f, tolerance=$%.6f)",
			v1Cost, v2Cost, diff, tolerance,
		)
	} else {
		check.Description = fmt.Sprintf("Cost sums match (V1=$%.6f, V2=$%.6f)", v1Cost, v2Cost)
	}
	
	return check
}

// checkMetadataConsistency verifies critical metadata fields match
func (v *SessionValidator) checkMetadataConsistency(v1Turns []V1Turn, v2Turns []V2Turn) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Metadata Consistency",
		Severity: "error",
		Passed:   true,
	}
	
	// Build V1 map by request_id
	v1ByReqID := make(map[string]V1Turn)
	for _, turn := range v1Turns {
		v1ByReqID[turn.RequestID] = turn
	}
	
	var mismatches []string
	for _, v2Turn := range v2Turns {
		v1Turn, exists := v1ByReqID[v2Turn.RequestID]
		if !exists {
			continue // Skip if not in V1
		}
		
		// Check model
		if v1Turn.ClientModel != v2Turn.Model {
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: model V1=%s, V2=%s",
				v2Turn.RequestID, v1Turn.ClientModel, v2Turn.Model,
			))
		}
		
		// Check provider
		if v1Turn.ProviderID != v2Turn.Provider {
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: provider V1=%s, V2=%s",
				v2Turn.RequestID, v1Turn.ProviderID, v2Turn.Provider,
			))
		}
		
		// Check credential
		if v1Turn.CredentialID != v2Turn.CredentialID {
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: credential V1=%s, V2=%s",
				v2Turn.RequestID, v1Turn.CredentialID, v2Turn.CredentialID,
			))
		}
		
		// Check verdicts (extract from V1 compression_meta)
		var v1Meta map[string]interface{}
		v1InjectionVerdict := "skip"
		v1OutputVerdict := "skip"
		if err := json.Unmarshal(v1Turn.CompressionMeta, &v1Meta); err == nil {
			if verdict, ok := v1Meta["injection_verdict"].(string); ok {
				v1InjectionVerdict = verdict
			}
			if verdict, ok := v1Meta["output_verdict"].(string); ok {
				v1OutputVerdict = verdict
			}
		}
		
		if v1InjectionVerdict != v2Turn.InjectionVerdict {
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: injection_verdict V1=%s, V2=%s",
				v2Turn.RequestID, v1InjectionVerdict, v2Turn.InjectionVerdict,
			))
		}
		
		if v1OutputVerdict != v2Turn.OutputVerdict {
			mismatches = append(mismatches, fmt.Sprintf(
				"req=%s: output_verdict V1=%s, V2=%s",
				v2Turn.RequestID, v1OutputVerdict, v2Turn.OutputVerdict,
			))
		}
	}
	
	if len(mismatches) > 0 {
		check.Passed = false
		check.Description = fmt.Sprintf("%d metadata mismatch(es)", len(mismatches))
		check.Details = mismatches
	} else {
		check.Description = "All metadata fields match"
	}
	
	return check
}

// checkSnapshotAccuracy verifies sessions table aggregates match session_turns
func (v *SessionValidator) checkSnapshotAccuracy(v2Turns []V2Turn, v2Session *V2Session) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Session Snapshot Accuracy",
		Severity: "error",
		Passed:   true,
	}
	
	// Calculate expected values from turns
	expectedTurns := len(v2Turns)
	expectedTokens := 0
	expectedCost := 0.0
	expectedLastTurnNo := 0
	
	for _, turn := range v2Turns {
		expectedTokens += turn.PromptTokens + turn.CompletionTokens
		expectedCost += turn.CostUSD
		if turn.TurnNo > expectedLastTurnNo {
			expectedLastTurnNo = turn.TurnNo
		}
	}
	
	var issues []string
	
	// Check turn count
	if v2Session.TotalTurns != expectedTurns {
		check.Passed = false
		issues = append(issues, fmt.Sprintf(
			"total_turns: snapshot=%d, actual=%d",
			v2Session.TotalTurns, expectedTurns,
		))
	}
	
	// Check token count (with tolerance)
	tokenDiff := abs(int64(v2Session.TotalTokens - expectedTokens))
	tokenTolerance := int64(max64(10, float64(expectedTokens)*0.01))
	if tokenDiff > tokenTolerance {
		check.Passed = false
		check.Severity = "warning" // Token mismatch is warning
		issues = append(issues, fmt.Sprintf(
			"total_tokens: snapshot=%d, actual=%d (diff=%d)",
			v2Session.TotalTokens, expectedTokens, tokenDiff,
		))
	}
	
	// Check cost (with tolerance)
	costDiff := abs64(v2Session.TotalCostUSD - expectedCost)
	costTolerance := max64(0.01, expectedCost*0.01)
	if costDiff > costTolerance {
		check.Passed = false
		check.Severity = "warning" // Cost mismatch is warning
		issues = append(issues, fmt.Sprintf(
			"total_cost: snapshot=$%.6f, actual=$%.6f (diff=$%.6f)",
			v2Session.TotalCostUSD, expectedCost, costDiff,
		))
	}
	
	// Check last turn number
	if v2Session.LastTurnNo != expectedLastTurnNo {
		check.Passed = false
		issues = append(issues, fmt.Sprintf(
			"last_turn_no: snapshot=%d, actual=%d",
			v2Session.LastTurnNo, expectedLastTurnNo,
		))
	}
	
	if !check.Passed {
		check.Description = fmt.Sprintf("Snapshot has %d discrepanc(ies)", len(issues))
		check.Details = issues
	} else {
		check.Description = "Session snapshot matches aggregated turns"
	}
	
	return check
}

// checkBodiesIntegrity verifies session_bodies deltas are valid JSON
func (v *SessionValidator) checkBodiesIntegrity(v2Turns []V2Turn, v2Bodies []V2Body) ValidationCheckV2 {
	check := ValidationCheckV2{
		Name:     "Bodies Integrity",
		Severity: "error",
		Passed:   true,
	}
	
	// Build submit_mode map
	submitModes := make(map[int]string)
	for _, turn := range v2Turns {
		submitModes[turn.TurnNo] = turn.SubmitMode
	}
	
	var jsonErrors []string
	var compressedModes []string
	
	for _, body := range v2Bodies {
		// Check request_delta JSON validity
		var reqDelta []interface{}
		if len(body.RequestDelta) > 0 {
			if err := json.Unmarshal(body.RequestDelta, &reqDelta); err != nil {
				check.Passed = false
				jsonErrors = append(jsonErrors, fmt.Sprintf(
					"turn %d: invalid request_delta JSON: %v",
					body.TurnNo, err,
				))
			}
		}
		
		// Check response_delta JSON validity
		var respDelta []interface{}
		if len(body.ResponseDelta) > 0 {
			if err := json.Unmarshal(body.ResponseDelta, &respDelta); err != nil {
				check.Passed = false
				jsonErrors = append(jsonErrors, fmt.Sprintf(
					"turn %d: invalid response_delta JSON: %v",
					body.TurnNo, err,
				))
			}
		}
		
		// Check if compressed mode (can't do strict comparison)
		if mode, ok := submitModes[body.TurnNo]; ok {
			if mode != "full" {
				compressedModes = append(compressedModes, fmt.Sprintf(
					"turn %d: submit_mode=%s",
					body.TurnNo, mode,
				))
			}
		}
	}
	
	// Report results
	if len(jsonErrors) > 0 {
		check.Description = fmt.Sprintf("%d JSON parsing error(s)", len(jsonErrors))
		check.Details = jsonErrors
	} else if len(compressedModes) > 0 {
		check.Passed = true // Still passes, but note compressed modes
		check.Severity = "warning"
		check.Description = fmt.Sprintf(
			"All JSON valid; %d turn(s) use compressed mode (strict body comparison not possible)",
			len(compressedModes),
		)
		check.Details = compressedModes
	} else {
		check.Description = "All bodies have valid JSON"
	}
	
	return check
}
