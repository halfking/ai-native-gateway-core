#!/bin/bash
# Layer 2 Test: Sticky Session Behavior
# Tests multi-level sticky routing (L1/L2/L3)

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
    export LLM_GATEWAY_DISABLE_COMPRESSION=true
    export LLM_GATEWAY_DISABLE_DETECTION=true
    
    echo "Environment configured:"
    echo "  GATEWAY_URL: $GATEWAY_URL"
    echo "  Sticky: ENABLED"
}

# Helper: Extract credential_id from logs
extract_credential_id() {
    local logs="$1"
    echo "$logs" | grep -oP 'credential_id=\K\d+' | head -1
}

# Helper: Generate unique session ID
generate_session_id() {
    echo "test-session-$(date +%s)-$RANDOM"
}

# T2.1.1: L1 Sticky - Session + Model Binding
test_l1_sticky_session_model() {
    test_case "T2.1.1: L1 Sticky - Session + Model Binding"
    
    local session_id
    session_id=$(generate_session_id)
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    # Request 1: Establish sticky
    echo "Request 1: Establishing sticky..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(extract_credential_id "$LAST_LOGS")
    echo "Used credential: $cred_id_1"
    
    # Request 2: Should use same credential (L1 hit)
    sleep 1
    echo "Request 2: Testing L1 sticky..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_2
    cred_id_2=$(extract_credential_id "$LAST_LOGS")
    echo "Used credential: $cred_id_2"
    
    # Should use same credential
    assert_equals "$cred_id_1" "$cred_id_2" "credential_id (L1 sticky)"
    assert_log_contains "sticky L1 hit"
}

# T2.1.2: L2 Sticky - Client + Model Binding (Cross-Session)
test_l2_sticky_client_model() {
    test_case "T2.1.2: L2 Sticky - Client + Model Binding (Cross-Session)"
    
    local session_id_1
    session_id_1=$(generate_session_id)
    local session_id_2="${session_id_1}-different"
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    # Request 1: First session
    echo "Request 1: First session..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id_1"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(extract_credential_id "$LAST_LOGS")
    echo "Session 1 used credential: $cred_id_1"
    
    # Request 2: Different session, same API key, same model
    sleep 1
    echo "Request 2: Different session, same model..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id_2"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_2
    cred_id_2=$(extract_credential_id "$LAST_LOGS")
    echo "Session 2 used credential: $cred_id_2"
    
    # Should use same credential (L2 hit)
    assert_equals "$cred_id_1" "$cred_id_2" "credential_id (L2 sticky)"
    assert_log_contains "sticky L2 hit"
}

# T2.1.3: L3 Sticky - Client Baseline (Cross-Model)
test_l3_sticky_client_baseline() {
    test_case "T2.1.3: L3 Sticky - Client Baseline (Cross-Model)"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Request 1: Model A
    local payload_1='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    echo "Request 1: Model gpt-4..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload_1" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(extract_credential_id "$LAST_LOGS")
    echo "GPT-4 used credential: $cred_id_1"
    
    # Request 2: Different model (should hit L3 if L1/L2 miss)
    local payload_2='{
        "model": "gpt-3.5-turbo",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    sleep 1
    echo "Request 2: Model gpt-3.5-turbo..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload_2" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    
    # Check if L3 was hit (may or may not, depending on available credentials)
    if echo "$LAST_LOGS" | grep -q "sticky L3 hit"; then
        echo -e "${GREEN}✓${NC} L3 sticky hit detected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${YELLOW}⊘${NC} L3 sticky not hit (may have different credentials for different models)"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T2.1.4: Sticky Failure Cleanup - Consecutive Failures
test_sticky_failure_cleanup() {
    test_case "T2.1.4: Sticky Failure Cleanup - Consecutive Failures"
    
    local session_id
    session_id=$(generate_session_id)
    
    # Force use of a credential that will fail
    local force_cred_id=9999  # Non-existent credential
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    echo "This test requires manual credential manipulation"
    echo "Skipping for automated run..."
    skip_test "Requires manual credential disable/enable"
}

# T2.1.5: Sticky TTL Expiration
test_sticky_ttl_expiration() {
    test_case "T2.1.5: Sticky TTL Expiration"
    
    local session_id
    session_id=$(generate_session_id)
    
    local payload='{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}],
        "max_tokens": 5
    }'
    
    # Request 1: Establish sticky
    echo "Request 1: Establishing sticky..."
    http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id"
    assert_http_status 200
    
    capture_logs 100
    local cred_id_1
    cred_id_1=$(extract_credential_id "$LAST_LOGS")
    echo "Used credential: $cred_id_1"
    
    # Wait for TTL to expire (L1 default: 5-10min)
    echo "Waiting for L1 TTL to expire (this would take 5-10 minutes)..."
    echo "Skipping actual wait in automated test..."
    skip_test "TTL expiration test requires 5-10 minute wait"
}

# T2.1.6: Sticky Persistence Across Restarts
test_sticky_persistence() {
    test_case "T2.1.6: Sticky Persistence Across Restarts"
    
    echo "This test requires gateway restart"
    skip_test "Requires gateway restart capability"
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 2: Sticky Session Behavior Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_l1_sticky_session_model
    test_l2_sticky_client_model
    test_l3_sticky_client_baseline
    test_sticky_failure_cleanup
    test_sticky_ttl_expiration
    test_sticky_persistence
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
