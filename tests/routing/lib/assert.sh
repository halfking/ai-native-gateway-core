#!/bin/bash
# assert.sh - Test assertion library for routing tests
# Usage: source tests/routing/lib/assert.sh

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Global test state
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
LAST_HTTP_STATUS=0
LAST_RESPONSE=""
LAST_LOGS=""

# Initialize test run
init_test_run() {
    TESTS_RUN=0
    TESTS_PASSED=0
    TESTS_FAILED=0
    echo "=================================================="
    echo "Starting test run: $(date)"
    echo "=================================================="
}

# Finalize test run and print summary
finalize_test_run() {
    echo ""
    echo "=================================================="
    echo "Test Summary"
    echo "=================================================="
    echo "Total:  $TESTS_RUN"
    echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
    if [ $TESTS_FAILED -gt 0 ]; then
        echo -e "${RED}Failed: $TESTS_FAILED${NC}"
        exit 1
    else
        echo -e "${GREEN}All tests passed!${NC}"
        exit 0
    fi
}

# Assert HTTP status code
# Usage: assert_http_status 200
assert_http_status() {
    local expected=$1
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if [ "$LAST_HTTP_STATUS" -eq "$expected" ]; then
        echo -e "${GREEN}✓${NC} HTTP status is $expected"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} HTTP status: expected $expected, got $LAST_HTTP_STATUS"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert JSON field exists
# Usage: assert_json_field_exists "$response" ".id"
assert_json_field_exists() {
    local json="$1"
    local field="$2"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    local value
    value=$(echo "$json" | jq -r "$field" 2>/dev/null || echo "null")
    
    if [ "$value" != "null" ] && [ -n "$value" ]; then
        echo -e "${GREEN}✓${NC} JSON field $field exists: $value"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} JSON field $field does not exist or is null"
        echo "Response: $json"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert JSON field equals value
# Usage: assert_json_field_equals "$response" ".model" "gpt-4"
assert_json_field_equals() {
    local json="$1"
    local field="$2"
    local expected="$3"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    local actual
    actual=$(echo "$json" | jq -r "$field" 2>/dev/null || echo "null")
    
    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}✓${NC} JSON field $field equals '$expected'"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} JSON field $field: expected '$expected', got '$actual'"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert log contains string
# Usage: assert_log_contains "credential_id=123"
assert_log_contains() {
    local pattern="$1"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if grep -q "$pattern" <<< "$LAST_LOGS"; then
        echo -e "${GREEN}✓${NC} Log contains: $pattern"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} Log does not contain: $pattern"
        echo "Recent logs:"
        echo "$LAST_LOGS" | tail -20
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert response time within threshold
# Usage: assert_response_time_under 2000 (ms)
assert_response_time_under() {
    local threshold_ms=$1
    local actual_ms=$2
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if [ "$actual_ms" -lt "$threshold_ms" ]; then
        echo -e "${GREEN}✓${NC} Response time ${actual_ms}ms < ${threshold_ms}ms"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} Response time ${actual_ms}ms >= ${threshold_ms}ms"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert string contains substring
# Usage: assert_contains "$string" "substring"
assert_contains() {
    local haystack="$1"
    local needle="$2"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if [[ "$haystack" == *"$needle"* ]]; then
        echo -e "${GREEN}✓${NC} String contains: $needle"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} String does not contain: $needle"
        echo "String: $haystack"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert equals
# Usage: assert_equals "expected" "actual" "description"
assert_equals() {
    local expected="$1"
    local actual="$2"
    local desc="${3:-value}"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}✓${NC} $desc equals '$expected'"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} $desc: expected '$expected', got '$actual'"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Assert greater than
# Usage: assert_gt 10 5 "count"
assert_gt() {
    local threshold=$1
    local actual=$2
    local desc="${3:-value}"
    TESTS_RUN=$((TESTS_RUN + 1))
    
    if [ "$actual" -gt "$threshold" ]; then
        echo -e "${GREEN}✓${NC} $desc ($actual) > $threshold"
        TESTS_PASSED=$((TESTS_PASSED + 1))
        return 0
    else
        echo -e "${RED}✗${NC} $desc ($actual) <= $threshold"
        TESTS_FAILED=$((TESTS_FAILED + 1))
        return 1
    fi
}

# Make HTTP request and capture response
# Usage: http_request POST "http://localhost:8080/v1/chat/completions" "$payload" "$headers"
http_request() {
    local method="$1"
    local url="$2"
    local data="${3:-}"
    local headers="${4:-}"
    
    local temp_response=$(mktemp)
    local temp_headers=$(mktemp)
    
    local start_time=$(date +%s%3N)
    
    if [ -n "$data" ]; then
        LAST_HTTP_STATUS=$(curl -s -w "%{http_code}" -o "$temp_response" \
            -X "$method" \
            -H "Content-Type: application/json" \
            ${headers:+-H "$headers"} \
            -d "$data" \
            "$url")
    else
        LAST_HTTP_STATUS=$(curl -s -w "%{http_code}" -o "$temp_response" \
            -X "$method" \
            ${headers:+-H "$headers"} \
            "$url")
    fi
    
    local end_time=$(date +%s%3N)
    local response_time=$((end_time - start_time))
    
    LAST_RESPONSE=$(cat "$temp_response")
    
    rm -f "$temp_response" "$temp_headers"
    
    echo "$response_time" # Return response time in ms
}

# Capture recent logs from gateway
# Usage: capture_logs 100 (last N lines)
capture_logs() {
    local lines="${1:-100}"
    
    # Try multiple log sources
    if [ -f "/var/log/llm-gateway/gateway.log" ]; then
        LAST_LOGS=$(tail -n "$lines" /var/log/llm-gateway/gateway.log)
    elif command -v kubectl &> /dev/null; then
        LAST_LOGS=$(kubectl logs -l app=llm-gateway --tail="$lines" 2>/dev/null || echo "")
    else
        LAST_LOGS=""
    fi
}

# Wait for service to be ready
# Usage: wait_for_service "http://localhost:8080/healthz" 30
wait_for_service() {
    local url="$1"
    local timeout="${2:-30}"
    local elapsed=0
    
    echo "Waiting for service at $url..."
    
    while [ $elapsed -lt $timeout ]; do
        if curl -s -f "$url" > /dev/null 2>&1; then
            echo -e "${GREEN}✓${NC} Service is ready"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    
    echo -e "${RED}✗${NC} Service not ready after ${timeout}s"
    return 1
}

# Print test section header
test_section() {
    local title="$1"
    echo ""
    echo "=================================================="
    echo "$title"
    echo "=================================================="
}

# Print test case header
test_case() {
    local name="$1"
    echo ""
    echo "--- Test: $name ---"
}

# Skip test with reason
skip_test() {
    local reason="$1"
    echo -e "${YELLOW}⊘${NC} SKIPPED: $reason"
    TESTS_RUN=$((TESTS_RUN + 1))
}

# Export functions for use in test scripts
export -f init_test_run
export -f finalize_test_run
export -f assert_http_status
export -f assert_json_field_exists
export -f assert_json_field_equals
export -f assert_log_contains
export -f assert_response_time_under
export -f assert_contains
export -f assert_equals
export -f assert_gt
export -f http_request
export -f capture_logs
export -f wait_for_service
export -f test_section
export -f test_case
export -f skip_test
