#!/bin/bash
# Layer 1 Test: Anthropic Direct Connection
# Tests basic connectivity to Anthropic provider without routing

set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
FORCE_CREDENTIAL_ID="${FORCE_CREDENTIAL_ID:-456}"

# Test environment setup
setup_test_env() {
    echo "Setting up test environment..."
    export LLM_GATEWAY_BYPASS_ROUTING=true
    export LLM_GATEWAY_FORCE_CREDENTIAL_ID="$FORCE_CREDENTIAL_ID"
    export LLM_GATEWAY_DISABLE_STICKY=true
    export LLM_GATEWAY_DISABLE_COMPRESSION=true
    export LLM_GATEWAY_DISABLE_DETECTION=true
    export LLM_GATEWAY_DISABLE_CACHE=true
    
    echo "Environment configured:"
    echo "  GATEWAY_URL: $GATEWAY_URL"
    echo "  FORCE_CREDENTIAL_ID: $FORCE_CREDENTIAL_ID"
}

# T1.7: Anthropic Direct - Messages API (Non-streaming)
test_anthropic_messages() {
    test_case "T1.7: Anthropic Direct - Messages API (Non-streaming)"
    
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "Hello, respond with just OK"}
        ],
        "max_tokens": 10
    }'
    
    local headers="Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01"
    
    local response_time
    response_time=$(http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "$headers")
    
    # Assertions
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".id"
    assert_json_field_exists "$LAST_RESPONSE" ".content"
    assert_json_field_exists "$LAST_RESPONSE" ".usage"
    assert_json_field_equals "$LAST_RESPONSE" ".type" "message"
    assert_response_time_under 3000 "$response_time"
    
    # Verify credential was used
    capture_logs 50
    assert_log_contains "credential_id=$FORCE_CREDENTIAL_ID"
    
    echo "Response time: ${response_time}ms"
}

# T1.8: Anthropic Direct - Streaming Response
test_anthropic_streaming() {
    test_case "T1.8: Anthropic Direct - Streaming Response"
    
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "Count from 1 to 3"}
        ],
        "stream": true,
        "max_tokens": 50
    }'
    
    local temp_output=$(mktemp)
    
    # Make streaming request
    curl -s -N \
        -X POST "$GATEWAY_URL/v1/messages" \
        -H "Authorization: Bearer $API_KEY" \
        -H "X-Api-Version: 2023-06-01" \
        -H "Content-Type: application/json" \
        -d "$payload" \
        > "$temp_output" 2>&1
    
    local output=$(cat "$temp_output")
    rm -f "$temp_output"
    
    # Assertions
    assert_contains "$output" "event: "
    assert_contains "$output" "event: message_stop"
    
    # Count event chunks
    local chunk_count
    chunk_count=$(echo "$output" | grep -c "^event: " || echo "0")
    assert_gt 0 "$chunk_count" "event count"
    
    echo "Received $chunk_count events"
}

# T1.9: Anthropic Direct - Tool Use
test_anthropic_tool_use() {
    test_case "T1.9: Anthropic Direct - Tool Use"
    
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "What is the weather in San Francisco?"}
        ],
        "tools": [
            {
                "name": "get_weather",
                "description": "Get current weather for a location",
                "input_schema": {
                    "type": "object",
                    "properties": {
                        "location": {"type": "string", "description": "City name"}
                    },
                    "required": ["location"]
                }
            }
        ],
        "max_tokens": 200
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01"
    
    # Check if response is valid
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".content"
    
    # If tool use was triggered, verify structure
    local stop_reason
    stop_reason=$(echo "$LAST_RESPONSE" | jq -r '.stop_reason')
    
    if [ "$stop_reason" = "tool_use" ]; then
        echo "Tool use triggered"
        assert_json_field_exists "$LAST_RESPONSE" ".content[0].type"
        local content_type
        content_type=$(echo "$LAST_RESPONSE" | jq -r '.content[0].type')
        if [ "$content_type" = "tool_use" ]; then
            echo "✓ Tool use content detected"
        fi
    else
        echo "Tool use not triggered (stop_reason: $stop_reason)"
    fi
}

# T1.10: Anthropic Direct - System Prompt
test_anthropic_system_prompt() {
    test_case "T1.10: Anthropic Direct - System Prompt"
    
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "system": "You are a helpful assistant. Always respond in uppercase.",
        "messages": [
            {"role": "user", "content": "Say hello"}
        ],
        "max_tokens": 20
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01"
    
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".content"
    
    # Check if response is in uppercase (system prompt followed)
    local response_text
    response_text=$(echo "$LAST_RESPONSE" | jq -r '.content[0].text // ""')
    
    if [ -n "$response_text" ]; then
        echo "Response: $response_text"
        # Note: We can't strictly assert uppercase as model may not always follow
    fi
}

# T1.11: Anthropic Direct - Multi-turn Conversation
test_anthropic_multiturn() {
    test_case "T1.11: Anthropic Direct - Multi-turn Conversation"
    
    local payload='{
        "model": "claude-3-5-sonnet-20241022",
        "messages": [
            {"role": "user", "content": "My name is Alice"},
            {"role": "assistant", "content": "Nice to meet you, Alice!"},
            {"role": "user", "content": "What is my name?"}
        ],
        "max_tokens": 20
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01"
    
    assert_http_status 200
    assert_json_field_exists "$LAST_RESPONSE" ".content"
    
    # Check if response mentions "Alice"
    local response_text
    response_text=$(echo "$LAST_RESPONSE" | jq -r '.content[0].text // ""')
    
    if [[ "$response_text" == *"Alice"* ]] || [[ "$response_text" == *"alice"* ]]; then
        echo "✓ Model remembered name from context"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo "⊘ Model did not mention name: $response_text"
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T1.12: Anthropic Direct - Error Handling
test_anthropic_error_handling() {
    test_case "T1.12: Anthropic Direct - Error Handling"
    
    # Test with invalid model
    local payload='{
        "model": "nonexistent-claude-model",
        "messages": [
            {"role": "user", "content": "Hello"}
        ],
        "max_tokens": 10
    }'
    
    http_request "POST" "$GATEWAY_URL/v1/messages" "$payload" "Authorization: Bearer $API_KEY|X-Api-Version: 2023-06-01"
    
    # Should return 4xx error
    if [ "$LAST_HTTP_STATUS" -ge 400 ] && [ "$LAST_HTTP_STATUS" -lt 500 ]; then
        echo -e "${GREEN}✓${NC} Correctly returned 4xx error: $LAST_HTTP_STATUS"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Expected 4xx, got $LAST_HTTP_STATUS"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
    
    # Error response should have error field
    assert_json_field_exists "$LAST_RESPONSE" ".error"
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 1: Anthropic Direct Connection Tests"
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_anthropic_messages
    test_anthropic_streaming
    test_anthropic_tool_use
    test_anthropic_system_prompt
    test_anthropic_multiturn
    test_anthropic_error_handling
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
