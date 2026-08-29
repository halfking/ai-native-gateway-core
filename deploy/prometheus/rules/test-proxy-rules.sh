#!/bin/bash
# Test script for proxy alerting rules validation

set -e

RULES_FILE="proxy-rules.yml"
PROMETHEUS_URL="${PROMETHEUS_URL:-http://localhost:9090}"

echo "=== Proxy Alerting Rules Test ==="
echo ""

# 1. Validate YAML syntax
echo "1. Validating YAML syntax..."
if command -v python3 &> /dev/null; then
    python3 -c "import yaml; yaml.safe_load(open('$RULES_FILE')); print('   ✓ YAML syntax valid')"
else
    echo "   ⚠ Python3 not found, skipping YAML validation"
fi

# 2. Validate with promtool (if available)
echo ""
echo "2. Validating Prometheus rule syntax..."
if command -v promtool &> /dev/null; then
    promtool check rules "$RULES_FILE"
    echo "   ✓ Prometheus rule syntax valid"
elif command -v docker &> /dev/null; then
    echo "   Using Docker to validate..."
    docker run --rm -v "$(pwd):/rules:ro" prom/prometheus:v2.47.0 promtool check rules "/rules/$RULES_FILE"
    echo "   ✓ Prometheus rule syntax valid (via Docker)"
else
    echo "   ⚠ Neither promtool nor docker found, skipping validation"
fi

# 3. Check if rules are loaded in Prometheus
echo ""
echo "3. Checking if rules are loaded in Prometheus..."
if curl -sf "$PROMETHEUS_URL/api/v1/rules" > /dev/null 2>&1; then
    LOADED=$(curl -sf "$PROMETHEUS_URL/api/v1/rules" | grep -c "proxy_alerts" || echo "0")
    if [ "$LOADED" -gt 0 ]; then
        echo "   ✓ proxy_alerts rule group is loaded in Prometheus"
        
        # Count rules
        RULE_COUNT=$(curl -sf "$PROMETHEUS_URL/api/v1/rules" | grep -o '"alert"' | wc -l)
        echo "   ℹ Total alert rules loaded: $RULE_COUNT"
    else
        echo "   ✗ proxy_alerts rule group NOT found in Prometheus"
        echo "   → Run: curl -X POST $PROMETHEUS_URL/-/reload"
    fi
else
    echo "   ⚠ Cannot connect to Prometheus at $PROMETHEUS_URL"
fi

# 4. Check metrics availability
echo ""
echo "4. Checking if proxy metrics are available..."
METRICS=(
    "llm_gateway_proxy_nodes_total"
    "llm_gateway_proxy_nodes_dialable"
    "llm_gateway_proxy_nodes_unhealthy"
    "llm_gateway_proxy_subscription_refresh_total"
    "llm_gateway_proxy_node_health_check_total"
    "llm_gateway_proxy_node_selection_total"
    "llm_gateway_proxy_password_decrypt_failed_total"
)

AVAILABLE=0
for metric in "${METRICS[@]}"; do
    if curl -sf "$PROMETHEUS_URL/api/v1/query?query=$metric" | grep -q '"result":\['; then
        ((AVAILABLE++))
    fi
done

echo "   ℹ $AVAILABLE/${#METRICS[@]} metrics are available"
if [ $AVAILABLE -eq 0 ]; then
    echo "   ⚠ No proxy metrics found. Is the LLM Gateway running and exposing metrics?"
fi

echo ""
echo "=== Test Complete ==="
