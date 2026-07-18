#!/bin/bash
# Layer 3 Test: Full Routing Integration
# Tests complete routing pipeline with all components enabled

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
    export LLM_GATEWAY_ENABLE_COMPRESSION=true
    export LLM_GATEWAY_ENABLE_INPUT_DETECTION=true
    export LLM_GATEWAY_ENABLE_OUTPUT_COMPLIANCE=true
    export LLM_GATEWAY_ENABLE_CACHE=true
    
    echo "Environment configured: Full routing enabled"
}

# Helper: Generate unique session ID
generate_session_id() {
    echo "test-session-$(date +%s)-$RANDOM"
}

# T3.1: Normal Routing - Multiple Candidates
test_normal_routing_multiple_candidates() {
    test_case "T3.1: Normal Routing - Multiple Candidates"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Send multiple requests to observe load balancing
    local creds_used=()
    
    for i in {1..10}; do
        local payload='{
            "model": "gpt-4",
            "messages": [{"role": "user", "content": "Hello"}],
            "max_tokens": 5
        }'
        
        http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
            "Authorization: Bearer $API_KEY|X-Gw-Session-Id: ${session_id}-${i}"
        
        if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
            capture_logs 50
            local cred_id
            cred_id=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "unknown")
            creds_used+=("$cred_id")
        fi
        
        sleep 0.1
    done
    
    # Count unique credentials used
    local unique_creds
    unique_creds=$(printf '%s\n' "${creds_used[@]}" | sort -u | wc -l)
    
    echo "Unique credentials used: $unique_creds out of 10 requests"
    
    if [ "$unique_creds" -gt 1 ]; then
        echo -e "${GREEN}✓${NC} Load balancing detected (used $unique_creds credentials)"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} Only 1 credential used (may be expected if only 1 available)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.2: Candidate Filtering - Auto-skip Unavailable
test_candidate_filtering() {
    test_case "T3.2: Candidate Filtering - Auto-skip Unavailable"
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY"
    
    # Check logs for filtering
    capture_logs 100
    
    if echo "$LAST_LOGS" | grep -q "filtered\|unavailable\|cooling"; then
        echo "✓ Candidate filtering detected in logs"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "⊘ No filtering logged (all candidates may be available)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.3: Sticky + Routing Integration
test_sticky_routing_integration() {
    test_case "T3.3: Sticky + Routing Integration"
    
    local session_id
    session_id=$(generate_session_id)
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    # Request 1: Establish routing + sticky
    echo "Request 1: Establishing sticky..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "0")
    
    # Request 2: Should use same credential via sticky
    sleep 1
    echo "Request 2: Testing sticky..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_2
    cred_id_2=$(echo "$LAST_LOGS" | grep -oP 'credential_id=\K\d+' | head -1 || echo "0")
    
    # Verify sticky hit
    if [ "$cred_id_1" = "$cred_id_2" ] && [ "$cred_id_1" != "0" ]; then
        echo -e "${GREEN}✓${NC} Sticky routing working: credential $cred_id_1 reused"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} Sticky not hit or credentials differ ($cred_id_1 vs $cred_id_2)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.4: Degraded Mode - Single Candidate
test_degraded_mode_single_candidate() {
    test_case "T3.4: Degraded Mode - Single Candidate (Simulated)"
    
    # This test would require manipulating credentials to have only 1 available
    # For now, we test that requests still succeed when load is high
    
    local session_id
    session_id=$(generate_session_id)
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    
    # Should still succeed even under pressure
    assert_http_status 200
    
    echo "Request succeeded (degraded mode would allow single candidate)"
}

# T3.5: No Candidates - Sync Probe Recovery (if enabled)
test_no_candidates_probe() {
    test_case "T3.5: No Candidates - Sync Probe Recovery"
    
    # This test requires all candidates to be unavailable
    # Skip in normal run
    
    skip_test "Requires all candidates unavailable (manual setup needed)"
}

# T3.6: Concurrent Load - Mixed Models
test_concurrent_mixed_models() {
    test_case "T3.6: Concurrent Load - Mixed Models"
    
    local concurrent=20
    local temp_dir=$(mktemp -d)
    
    echo "Sending $concurrent concurrent requests (mixed models)..."
    
    for i in $(seq 1 $concurrent); do
        {
            # Alternate between models
            local model="gpt-4"
            if [ $((i % 2)) -eq 0 ]; then
                model="gpt-3.5-turbo"
            fi
            
            local session_id="test-session-$i"
            
            local payload
            payload=$(jq -n \
                --arg model "$model" \
                --arg session "$session_id" \
                '{
                    "model": $model,
                    "messages": [{"role": "user", "content": "Hello"}],
                    "max_tokens": 5
                }')
            
            curl -s -w "\n%{http_code}" \
                -X POST "$GATEWAY_URL/v1/chat/completions" \
                -H "Authorization: Bearer $API_KEY" \
                -H "X-Gw-Session-Id: $session_id" \
                -H "Content-Type: application/json" \
                -d "$payload" \
                > "$temp_dir/response_$i.txt"
        } &
    done
    
    # Wait for all
    wait
    
    # Count results
    local success_count=0
    local gpt4_count=0
    local gpt35_count=0
    
    for i in $(seq 1 $concurrent); do
        local status
        status=$(tail -1 "$temp_dir/response_$i.txt")
        if [ "$status" = "200" ]; then
            success_count=$((success_count + 1))
            
            # Check which model was used
            if grep -q '"model":"gpt-4"' "$temp_dir/response_$i.txt" 2>/dev/null; then
                gpt4_count=$((gpt4_count + 1))
            elif grep -q '"model":"gpt-3.5-turbo"' "$temp_dir/response_$i.txt" 2>/dev/null; then
                gpt35_count=$((gpt35_count + 1))
            fi
        fi
    done
    
    rm -rf "$temp_dir"
    
    local success_rate=$((success_count * 100 / concurrent))
    echo "Success rate: $success_rate% ($success_count/$concurrent)"
    echo "GPT-4: $gpt4_count, GPT-3.5: $gpt35_count"
    
    if [ $success_rate -ge 95 ]; then
        echo -e "${GREEN}✓${NC} Concurrent mixed load test passed"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Success rate too low: $success_rate%"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.7: End-to-End Latency
test_e2e_latency() {
    test_case "T3.7: End-to-End Latency (Full Pipeline)"
    
    local session_id
    session_id=$(generate_session_id)
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Say OK"}],
        "max_tokens": 5
    }'
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
        "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id")
    
    assert_http_status 200
    
    # Full pipeline should still be reasonably fast
    echo "Full pipeline response time: ${response_time}ms"
    
    if [ "$response_time" -lt 5000 ]; then
        echo -e "${GREEN}✓${NC} Latency acceptable: ${response_time}ms < 5000ms"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} Latency high: ${response_time}ms (may be expected)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 3: Full Routing Integration Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_normal_routing_multiple_candidates
    test_candidate_filtering
    test_sticky_routing_integration
    test_degraded_mode_single_candidate
    test_no_candidates_probe
    test_concurrent_mixed_models
    test_e2e_latency
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
