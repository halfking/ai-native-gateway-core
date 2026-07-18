#!/bin/bash
# Test Runner - Execute all routing tests
# Usage: ./run_all_tests.sh [layer1|layer2|layer3|all]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_LAYER="${1:-all}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"

# Test results tracking
TOTAL_SUITES=0
PASSED_SUITES=0
FAILED_SUITES=0

# Print banner
print_banner() {
    echo ""
    echo "=================================================="
    echo "  LLM Gateway Routing Test Suite"
    echo "=================================================="
    echo "Date: $(date)"
    echo "Target: $GATEWAY_URL"
    echo "Layer: $TEST_LAYER"
    echo "=================================================="
    echo ""
}

# Run a test suite
run_test_suite() {
    local test_script="$1"
    local test_name
    test_name=$(basename "$test_script" .sh)
    
    echo ""
    echo -e "${BLUE}▶ Running: $test_name${NC}"
    echo "---"
    
    TOTAL_SUITES=$((TOTAL_SUITES + 1))
    
    if bash "$test_script"; then
        echo -e "${GREEN}✓ $test_name PASSED${NC}"
        PASSED_SUITES=$((PASSED_SUITES + 1))
        return 0
    else
        echo -e "${RED}✗ $test_name FAILED${NC}"
        FAILED_SUITES=$((FAILED_SUITES + 1))
        return 1
    fi
}

# Run Layer 1 tests
run_layer1_tests() {
    echo ""
    echo "=================================================="
    echo "  Layer 1: Direct Provider Tests"
    echo "=================================================="
    
    local layer1_dir="$SCRIPT_DIR/layer1_direct"
    
    if [ -d "$layer1_dir" ]; then
        for test_script in "$layer1_dir"/test_*.sh; do
            if [ -f "$test_script" ]; then
                chmod +x "$test_script"
                run_test_suite "$test_script" || true
            fi
        done
    else
        echo -e "${YELLOW}No Layer 1 tests found${NC}"
    fi
}

# Run Layer 2 tests
run_layer2_tests() {
    echo ""
    echo "=================================================="
    echo "  Layer 2: Component Tests"
    echo "=================================================="
    
    local layer2_dir="$SCRIPT_DIR/layer2_components"
    
    if [ -d "$layer2_dir" ]; then
        for test_script in "$layer2_dir"/test_*.sh; do
            if [ -f "$test_script" ]; then
                chmod +x "$test_script"
                run_test_suite "$test_script" || true
            fi
        done
    else
        echo -e "${YELLOW}No Layer 2 tests found${NC}"
    fi
}

# Run Layer 3 tests
run_layer3_tests() {
    echo ""
    echo "=================================================="
    echo "  Layer 3: Integration Tests"
    echo "=================================================="
    
    local layer3_dir="$SCRIPT_DIR/layer3_integration"
    
    if [ -d "$layer3_dir" ]; then
        for test_script in "$layer3_dir"/test_*.sh; do
            if [ -f "$test_script" ]; then
                chmod +x "$test_script"
                run_test_suite "$test_script" || true
            fi
        done
    else
        echo -e "${YELLOW}No Layer 3 tests found${NC}"
    fi
}

# Print summary
print_summary() {
    echo ""
    echo "=================================================="
    echo "  Test Summary"
    echo "=================================================="
    echo "Total Suites: $TOTAL_SUITES"
    echo -e "${GREEN}Passed:       $PASSED_SUITES${NC}"
    
    if [ $FAILED_SUITES -gt 0 ]; then
        echo -e "${RED}Failed:       $FAILED_SUITES${NC}"
    else
        echo "Failed:       0"
    fi
    
    local success_rate=0
    if [ $TOTAL_SUITES -gt 0 ]; then
        success_rate=$((PASSED_SUITES * 100 / TOTAL_SUITES))
    fi
    
    echo "Success Rate: ${success_rate}%"
    echo "=================================================="
    
    if [ $FAILED_SUITES -eq 0 ]; then
        echo -e "${GREEN}All test suites passed!${NC}"
        exit 0
    else
        echo -e "${RED}Some test suites failed${NC}"
        exit 1
    fi
}

# Check prerequisites
check_prerequisites() {
    echo "Checking prerequisites..."
    
    # Check if jq is installed
    if ! command -v jq &> /dev/null; then
        echo -e "${RED}Error: jq is not installed${NC}"
        echo "Install with: brew install jq (macOS) or apt-get install jq (Linux)"
        exit 1
    fi
    
    # Check if curl is installed
    if ! command -v curl &> /dev/null; then
        echo -e "${RED}Error: curl is not installed${NC}"
        exit 1
    fi
    
    # Check if gateway is accessible
    echo "Checking gateway availability at $GATEWAY_URL..."
    if ! curl -s -f "$GATEWAY_URL/healthz" > /dev/null 2>&1; then
        echo -e "${YELLOW}Warning: Gateway not responding at $GATEWAY_URL${NC}"
        echo "Make sure the gateway is running before running tests"
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    else
        echo -e "${GREEN}Gateway is accessible${NC}"
    fi
}

# Main execution
main() {
    print_banner
    check_prerequisites
    
    case "$TEST_LAYER" in
        layer1|l1)
            run_layer1_tests
            ;;
        layer2|l2)
            run_layer2_tests
            ;;
        layer3|l3)
            run_layer3_tests
            ;;
        all)
            run_layer1_tests
            run_layer2_tests
            run_layer3_tests
            ;;
        *)
            echo -e "${RED}Invalid layer: $TEST_LAYER${NC}"
            echo "Usage: $0 [layer1|layer2|layer3|all]"
            exit 1
            ;;
    esac
    
    print_summary
}

# Run main
main
