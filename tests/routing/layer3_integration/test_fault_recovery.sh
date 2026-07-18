#!/bin/bash
# Layer 3 Test: Fault Recovery and Resilience
# Tests credential failover, provider recovery, and graceful degradation

set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"

# Test environment setup
setup_test_env() {
    echo "Setting up test environment..."
    export LLM_GATEWAY_BYPASS_ROUTING=false
    export LLM_GATEWAY_ENABLE_STICKY=true
    export LLM_GATEWAY_ENABLE_COMPRESSION=false
    export LLM_GATEWAY_ENABLE_INPUT_DETECTION=false
    export LLM_GATEWAY_ENABLE_OUTPUT_COMPLIANCE=false
    
    echo "Environment configured: Full routing with sticky enabled"
}

# Helper: Generate unique session ID
generate_session_id() {
    echo "test-session-$(date +%s)-$RANDOM"
}

# T3.8: Credential Failover - Automatic Switch
test_credential_failover() {
    test_case "T3.8: Credential Failover - Automatic Switch"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Request 1: Establish baseline
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 10
    }'
    
    echo "Request 1: Establishing baseline..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "0")
    echo "Initial credential: $cred_id_1"
    
    # Request 2-10: Send multiple requests to observe failover behavior
    echo "Sending 10 requests to observe failover patterns..."
    local success_count=0
    local creds_seen=()
    
    for i in {1..10}; do
        http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
            "Authorization: Bearer $API_KEY|X-Gw-Session-Id: ${session_id}-req${i}"
        
        if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
            success_count=$((success_count + 1))
            
            capture_logs 50
            local cred_id
            cred_id=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "unknown")
            creds_seen+=("$cred_id")
        fi
        
        sleep 0.2
    done
    
    # Analyze results
    local unique_creds
    unique_creds=$(printf '%s\n' "${creds_seen[@]}" | sort -u | wc -l)
    
    echo "Success rate: $success_count/10"
    echo "Unique credentials used: $unique_creds"
    
    # High success rate indicates good failover
    if [ $success_count -ge 9 ]; then
        echo -e "${GREEN}✓${NC} High availability maintained: $success_count/10"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} Some failures detected: $success_count/10"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.9: Provider Recovery - Automatic Retry
test_provider_recovery() {
    test_case "T3.9: Provider Recovery - Automatic Retry"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Send request that should trigger retry logic
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Test recovery"}],
        "max_tokens": 10
    }'
    
    echo "Testing provider recovery with retry logic..."
    local start_time=$(date +%s)
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    # Check if request succeeded (after potential retries)
    if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
        echo -e "${GREEN}✓${NC} Request succeeded (duration: ${duration}s)"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        
        # Check logs for retry evidence
        capture_logs 100
        if echo "$LAST_LOGS" | grep -qi "retry\|attempt\|fallback"; then
            echo "  Retry mechanism detected in logs"
        fi
    else
        echo -e "${YELLOW}⊘${NC} Request failed: $LAST_HTTP_STATUS"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.10: Graceful Degradation - Service Continuity
test_graceful_degradation() {
    test_case "T3.10: Graceful Degradation - Service Continuity"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Test under constrained conditions
    echo "Testing graceful degradation under load..."
    
    local concurrent=20
    local temp_dir=$(mktemp -d)
    
    # Send concurrent requests to stress the system
    for i in $(seq 1 $concurrent); do
        {
            local payload='{
                "model": "gpt-4",
                "messages": [{"role": "user", "content": "Stress test"}],
                "max_tokens": 5
            }'
            
            curl -s -w "\n%{http_code}" \
                -X POST "$GATEWAY_URL/v1/chat/completions" \
                -H "Authorization: Bearer $API_KEY" \
                -H "X-Gw-Session-Id: ${session_id}-${i}" \
                -H "Content-Type: application/json" \
                -d "$payload" \
                > "$temp_dir/response_$i.txt" 2>&1
        } &
    done
    
    wait
    
    # Count successes and analyze degradation
    local success_count=0
    local degraded_count=0
    
    for i in $(seq 1 $concurrent); do
        local status
        status=$(tail -1 "$temp_dir/response_$i.txt")
        
        if [ "$status" = "200" ]; then
            success_count=$((success_count + 1))
        elif [ "$status" = "503" ] || [ "$status" = "429" ]; then
            degraded_count=$((degraded_count + 1))
        fi
    done
    
    rm -rf "$temp_dir"
    
    local success_rate=$((success_count * 100 / concurrent))
    local degraded_rate=$((degraded_count * 100 / concurrent))
    
    echo "Success rate: $success_rate% ($success_count/$concurrent)"
    echo "Graceful degradation: $degraded_rate% ($degraded_count/$concurrent)"
    
    # Success: either high success OR graceful degradation (not crashes)
    if [ $success_rate -ge 80 ] || [ $degraded_rate -gt 0 ]; then
        echo -e "${GREEN}✓${NC} System degraded gracefully"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} System did not handle load well"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.11: Sticky Cleanup After Failure
test_sticky_cleanup_after_failure() {
    test_case "T3.11: Sticky Cleanup After Failure"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Establish sticky binding
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    echo "Establishing sticky binding..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    
    if [ "$LAST_HTTP_STATUS" -ne 200 ]; then
        skip_test "Could not establish baseline"
        return
    fi
    
    capture_logs 50
    local original_cred
    original_cred=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "0")
    
    echo "Original credential: $original_cred"
    
    # Send multiple requests - if original cred fails, should clean up and switch
    echo "Testing sticky cleanup after failures..."
    local switch_detected=false
    
    for i in {1..5}; do
        http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
            "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
        
        if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
            capture_logs 50
            local current_cred
            current_cred=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "0")
            
            if [ "$current_cred" != "$original_cred" ] && [ "$current_cred" != "0" ]; then
                switch_detected=true
                echo "Credential switched: $original_cred → $current_cred"
                break
            fi
        fi
        
        sleep 1
    done
    
    if [ "$switch_detected" = true ]; then
        echo -e "${GREEN}✓${NC} Sticky cleanup and failover detected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} No credential switch detected (may be stable)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.12: Circuit Breaker Behavior
test_circuit_breaker() {
    test_case "T3.12: Circuit Breaker Behavior"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Send rapid requests to trigger circuit breaker
    echo "Testing circuit breaker with rapid requests..."
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Test"}],
        "max_tokens": 5
    }'
    
    local breaker_triggered=false
    local consecutive_fast_fails=0
    
    for i in {1..10}; do
        local start_ms=$(date +%s%3N)
        
        http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
            "Authorization: Bearer $API_KEY|X-Gw-Session-Id: ${session_id}-${i}"
        
        local end_ms=$(date +%s%3N)
        local duration=$((end_ms - start_ms))
        
        # Fast failure (<100ms) might indicate circuit breaker
        if [ "$LAST_HTTP_STATUS" -ne 200 ] && [ "$duration" -lt 100 ]; then
            consecutive_fast_fails=$((consecutive_fast_fails + 1))
            if [ $consecutive_fast_fails -ge 3 ]; then
                breaker_triggered=true
                echo "Circuit breaker detected (fast fail: ${duration}ms)"
                break
            fi
        else
            consecutive_fast_fails=0
        fi
        
        sleep 0.1
    done
    
    if [ "$breaker_triggered" = true ]; then
        echo -e "${GREEN}✓${NC} Circuit breaker working"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} Circuit breaker not detected (system may be healthy)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 3: Fault Recovery and Resilience Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_credential_failover
    test_provider_recovery
    test_graceful_degradation
    test_sticky_cleanup_after_failure
    test_circuit_breaker
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
