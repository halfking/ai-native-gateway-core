#!/bin/bash
# Layer 3 Test: Performance and Stability
# Tests long-running stability, burst traffic, and resource management

set -euo pipefail

# Load assertion library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../lib/assert.sh"

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
LONG_RUN_DURATION="${LONG_RUN_DURATION:-3600}"  # 1 hour default
BURST_CONCURRENT="${BURST_CONCURRENT:-100}"

# Test environment setup
setup_test_env() {
    echo "Setting up test environment..."
    export LLM_GATEWAY_BYPASS_ROUTING=false
    export LLM_GATEWAY_ENABLE_STICKY=true
    export LLM_GATEWAY_ENABLE_COMPRESSION=true
    export LLM_GATEWAY_ENABLE_INPUT_DETECTION=true
    export LLM_GATEWAY_ENABLE_OUTPUT_COMPLIANCE=true
    
    echo "Environment configured: Full pipeline enabled"
}

# Helper: Generate unique session ID
generate_session_id() {
    echo "test-session-$(date +%s)-$RANDOM"
}

# Helper: Get memory usage (RSS in MB)
get_memory_usage() {
    if command -v pgrep &> /dev/null; then
        local pids=$(pgrep -f "llm-gateway\|gateway" || echo "")
        if [ -n "$pids" ]; then
            ps -o rss= -p $pids | awk '{sum+=$1} END {print int(sum/1024)}'
        else
            echo "0"
        fi
    else
        echo "0"
    fi
}

# T3.13: Long-Running Stability Test
test_long_running_stability() {
    test_case "T3.13: Long-Running Stability Test"
    
    local duration=$LONG_RUN_DURATION
    echo "Running stability test for $duration seconds (~$((duration / 60)) minutes)"
    echo "Note: This is a TIME-CONSUMING test. Set LONG_RUN_DURATION to reduce duration."
    
    # Check if user wants to skip
    if [ "$duration" -gt 600 ] && [ "${SKIP_LONG_TESTS:-false}" = "true" ]; then
        skip_test "Long test skipped (set SKIP_LONG_TESTS=false to run)"
        return
    fi
    
    local start_time=$(date +%s)
    local end_time=$((start_time + duration))
    local request_count=0
    local success_count=0
    local failure_count=0
    local start_memory=$(get_memory_usage)
    
    echo "Starting at $(date)"
    echo "Initial memory: ${start_memory}MB"
    
    while [ $(date +%s) -lt $end_time ]; do
        local session_id
        session_id=$(generate_session_id)
        
        local payload='{
            "model": "gpt-4",
            "messages": [{"role": "user", "content": "Stability test"}],
            "max_tokens": 10
        }'
        
        http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
            "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id" &> /dev/null
        
        request_count=$((request_count + 1))
        
        if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
            success_count=$((success_count + 1))
        else
            failure_count=$((failure_count + 1))
        fi
        
        # Report progress every 100 requests
        if [ $((request_count % 100)) -eq 0 ]; then
            local current_time=$(date +%s)
            local elapsed=$((current_time - start_time))
            local current_memory=$(get_memory_usage)
            local memory_delta=$((current_memory - start_memory))
            
            echo "Progress: ${elapsed}s / ${duration}s | Requests: $request_count | Success: $success_count | Failed: $failure_count | Memory: ${current_memory}MB (+${memory_delta}MB)"
        fi
        
        sleep 1
    done
    
    local end_memory=$(get_memory_usage)
    local memory_growth=$((end_memory - start_memory))
    local success_rate=$((success_count * 100 / request_count))
    
    echo ""
    echo "=== Stability Test Results ==="
    echo "Duration: $duration seconds"
    echo "Total requests: $request_count"
    echo "Successful: $success_count"
    echo "Failed: $failure_count"
    echo "Success rate: ${success_rate}%"
    echo "Memory start: ${start_memory}MB"
    echo "Memory end: ${end_memory}MB"
    echo "Memory growth: ${memory_growth}MB"
    
    # Success criteria
    local test_passed=true
    
    if [ $success_rate -lt 95 ]; then
        echo -e "${RED}✗${NC} Success rate too low: ${success_rate}%"
        test_passed=false
    fi
    
    if [ $memory_growth -gt 500 ]; then
        echo -e "${RED}✗${NC} Memory growth too high: ${memory_growth}MB"
        test_passed=false
    fi
    
    if [ "$test_passed" = true ]; then
        echo -e "${GREEN}✓${NC} Stability test passed"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.14: Burst Traffic Handling
test_burst_traffic() {
    test_case "T3.14: Burst Traffic Handling"
    
    local concurrent=$BURST_CONCURRENT
    echo "Testing burst traffic with $concurrent concurrent requests"
    
    local temp_dir=$(mktemp -d)
    local start_time=$(date +%s%3N)
    
    # Send burst
    for i in $(seq 1 $concurrent); do
        {
            local session_id="burst-session-$i"
            local payload='{
                "model": "gpt-4",
                "messages": [{"role": "user", "content": "Burst test"}],
                "max_tokens": 5
            }'
            
            curl -s -w "\n%{http_code}\n%{time_total}" \
                -X POST "$GATEWAY_URL/v1/chat/completions" \
                -H "Authorization: Bearer $API_KEY" \
                -H "X-Gw-Session-Id: $session_id" \
                -H "Content-Type: application/json" \
                -d "$payload" \
                > "$temp_dir/response_$i.txt" 2>&1
        } &
    done
    
    echo "Waiting for all requests to complete..."
    wait
    
    local end_time=$(date +%s%3N)
    local total_duration=$((end_time - start_time))
    
    # Analyze results
    local success_count=0
    local error_count=0
    local timeout_count=0
    local total_time=0
    local min_time=999999
    local max_time=0
    
    for i in $(seq 1 $concurrent); do
        if [ -f "$temp_dir/response_$i.txt" ]; then
            local status=$(sed -n '2p' "$temp_dir/response_$i.txt")
            local time_ms=$(sed -n '3p' "$temp_dir/response_$i.txt" | awk '{print int($1 * 1000)}')
            
            if [ "$status" = "200" ]; then
                success_count=$((success_count + 1))
                
                if [ "$time_ms" != "" ]; then
                    total_time=$((total_time + time_ms))
                    [ "$time_ms" -lt "$min_time" ] && min_time=$time_ms
                    [ "$time_ms" -gt "$max_time" ] && max_time=$time_ms
                fi
            elif [ "$status" = "503" ] || [ "$status" = "502" ]; then
                error_count=$((error_count + 1))
            else
                timeout_count=$((timeout_count + 1))
            fi
        fi
    done
    
    rm -rf "$temp_dir"
    
    local success_rate=$((success_count * 100 / concurrent))
    local avg_time=$((total_time / success_count))
    
    echo ""
    echo "=== Burst Traffic Results ==="
    echo "Concurrent requests: $concurrent"
    echo "Total duration: ${total_duration}ms"
    echo "Successful: $success_count ($success_rate%)"
    echo "Errors (503/502): $error_count"
    echo "Timeouts: $timeout_count"
    echo "Response time (avg): ${avg_time}ms"
    echo "Response time (min): ${min_time}ms"
    echo "Response time (max): ${max_time}ms"
    
    # Success criteria
    if [ $success_rate -ge 90 ]; then
        echo -e "${GREEN}✓${NC} Burst traffic handled well: ${success_rate}%"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Burst traffic handling poor: ${success_rate}%"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# T3.15: Sustained Load Test
test_sustained_load() {
    test_case "T3.15: Sustained Load Test (5 minutes)"
    
    local duration=300  # 5 minutes
    local rps=10  # Requests per second
    
    echo "Running sustained load test: ${rps} RPS for ${duration}s"
    
    local start_time=$(date +%s)
    local end_time=$((start_time + duration))
    local request_count=0
    local success_count=0
    local total_latency=0
    
    while [ $(date +%s) -lt $end_time ]; do
        local batch_start=$(date +%s%3N)
        
        # Send batch of requests
        for i in $(seq 1 $rps); do
            {
                local session_id="sustained-$RANDOM"
                local payload='{
                    "model": "gpt-4",
                    "messages": [{"role": "user", "content": "Load test"}],
                    "max_tokens": 5
                }'
                
                local req_start=$(date +%s%3N)
                http_request "POST" "$GATEWAY_URL/v1/chat/completions" "$payload" \
                    "Authorization: Bearer $API_KEY|X-Gw-Session-Id: $session_id" &> /dev/null
                local req_end=$(date +%s%3N)
                
                request_count=$((request_count + 1))
                
                if [ "$LAST_HTTP_STATUS" -eq 200 ]; then
                    success_count=$((success_count + 1))
                    total_latency=$((total_latency + req_end - req_start))
                fi
            } &
        done
        
        # Wait for batch to complete
        wait
        
        # Report every 30 seconds
        if [ $((request_count % (rps * 30))) -eq 0 ]; then
            local elapsed=$(($(date +%s) - start_time))
            local success_rate=$((success_count * 100 / request_count))
            echo "Progress: ${elapsed}s | Requests: $request_count | Success rate: ${success_rate}%"
        fi
        
        # Sleep remainder of second
        local batch_end=$(date +%s%3N)
        local batch_duration=$((batch_end - batch_start))
        local sleep_ms=$((1000 - batch_duration))
        [ $sleep_ms -gt 0 ] && sleep 0.$((sleep_ms / 100))
    done
    
    local success_rate=$((success_count * 100 / request_count))
    local avg_latency=$((total_latency / success_count))
    
    echo ""
    echo "=== Sustained Load Results ==="
    echo "Duration: ${duration}s"
    echo "Target RPS: $rps"
    echo "Total requests: $request_count"
    echo "Successful: $success_count"
    echo "Success rate: ${success_rate}%"
    echo "Average latency: ${avg_latency}ms"
    
    if [ $success_rate -ge 95 ]; then
        echo -e "${GREEN}✓${NC} Sustained load handled well: ${success_rate}%"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗${NC} Sustained load handling poor: ${success_rate}%"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
    TESTS_RUN=$((TESTS_RUN + 1))
}

# Main execution
main() {
    init_test_run
    
    test_section "Layer 3: Performance and Stability Tests"
    
    echo ""
    echo "⚠️  PERFORMANCE TESTS NOTICE ⚠️"
    echo "These tests are time-consuming and resource-intensive:"
    echo "- T3.13: Long-running stability (default: 1 hour)"
    echo "- T3.14: Burst traffic (default: 100 concurrent)"
    echo "- T3.15: Sustained load (5 minutes)"
    echo ""
    echo "Configuration:"
    echo "  LONG_RUN_DURATION=$LONG_RUN_DURATION seconds"
    echo "  BURST_CONCURRENT=$BURST_CONCURRENT requests"
    echo "  SKIP_LONG_TESTS=${SKIP_LONG_TESTS:-false}"
    echo ""
    
    # Check if service is ready
    if ! wait_for_service "$GATEWAY_URL/healthz" 30; then
        echo -e "${RED}Gateway not ready, skipping tests${NC}"
        exit 1
    fi
    
    setup_test_env
    
    # Run tests
    test_long_running_stability
    test_burst_traffic
    test_sustained_load
    
    finalize_test_run
}

# Run main if executed directly
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
