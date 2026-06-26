#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════
# P0 HOTFIX Verification Script
# ═══════════════════════════════════════════════════════════════════════════

set -euo pipefail

LOG_PREFIX="[VERIFY $(date '+%H:%M:%S')]"

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} P0 HOTFIX Verification - request_logs Write Functionality"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo ""

***REMOVED***
# 1. Check container status
***REMOVED***

echo "${LOG_PREFIX} [1/4] Container Status"
echo "${LOG_PREFIX} ────────────────────────────────────────────────────────"

CONTAINER_NAME="${CONTAINER_NAME:-llm-gateway-go}"

if docker ps | grep -q "${CONTAINER_NAME}"; then
    UPTIME=$(docker ps --filter "name=${CONTAINER_NAME}" --format "{{.Status}}")
    echo "${LOG_PREFIX} ✓ Container running: ${UPTIME}"
    
    # Show recent logs
    echo "${LOG_PREFIX} Recent logs (last 10 lines):"
    docker logs "${CONTAINER_NAME}" --tail 10 2>&1 | sed "s/^/${LOG_PREFIX}   /"
else
    echo "${LOG_PREFIX} ✗ Container NOT running"
    exit 1
fi
echo ""

***REMOVED***
# 2. Check for errors in logs
***REMOVED***

echo "${LOG_PREFIX} [2/4] Error Check"
echo "${LOG_PREFIX} ────────────────────────────────────────────────────────"

ERROR_COUNT=$(docker logs "${CONTAINER_NAME}" --since 5m 2>&1 | grep -i "error\|fatal\|panic" | wc -l)

if [ "${ERROR_COUNT}" -eq 0 ]; then
    echo "${LOG_PREFIX} ✓ No errors in last 5 minutes"
else
    echo "${LOG_PREFIX} ⚠ Found ${ERROR_COUNT} error(s) in last 5 minutes:"
    docker logs "${CONTAINER_NAME}" --since 5m 2>&1 | grep -i "error\|fatal\|panic" | head -5 | sed "s/^/${LOG_PREFIX}   /"
fi
echo ""

***REMOVED***
# 3. Check database connection
***REMOVED***

echo "${LOG_PREFIX} [3/4] Database Verification"
echo "${LOG_PREFIX} ────────────────────────────────────────────────────────"

if [ -z "${LLM_GATEWAY_DATABASE_URL:-}" ]; then
    echo "${LOG_PREFIX} ⚠ LLM_GATEWAY_DATABASE_URL not set, skipping DB checks"
    echo "${LOG_PREFIX}   Set this variable to enable DB verification"
else
    echo "${LOG_PREFIX} Checking request_logs writes..."
    
    # Check recent writes (last 5 minutes)
    RESULT=$(psql "${LLM_GATEWAY_DATABASE_URL}" -t -c "
        SELECT 
            COUNT(*) as recent_count,
            TO_CHAR(MAX(ts), 'YYYY-MM-DD HH24:MI:SS') as latest_ts
        FROM request_logs
        WHERE ts > now() - interval '5 minutes'
    " 2>&1)
    
    if [ $? -eq 0 ]; then
        echo "${LOG_PREFIX} Database query result:"
        echo "${RESULT}" | sed "s/^/${LOG_PREFIX}   /"
        
        RECENT_COUNT=$(echo "${RESULT}" | awk '{print $1}')
        if [ "${RECENT_COUNT:-0}" -gt 0 ]; then
            echo "${LOG_PREFIX} ✓ request_logs is receiving writes"
        else
            echo "${LOG_PREFIX} ⚠ No writes in last 5 minutes (may be normal if no traffic)"
        fi
    else
        echo "${LOG_PREFIX} ✗ Database query failed"
        echo "${LOG_PREFIX} Error: ${RESULT}"
    fi
    
    # Check for duplicate rows
    echo ""
    echo "${LOG_PREFIX} Checking for duplicate rows (last 1 hour)..."
    
    DUP_RESULT=$(psql "${LLM_GATEWAY_DATABASE_URL}" -t -c "
        SELECT COUNT(DISTINCT request_id) as unique_ids,
               COUNT(*) as total_rows
        FROM request_logs
        WHERE ts > now() - interval '1 hour'
    " 2>&1)
    
    if [ $? -eq 0 ]; then
        echo "${DUP_RESULT}" | sed "s/^/${LOG_PREFIX}   /"
        
        UNIQUE=$(echo "${DUP_RESULT}" | awk '{print $1}')
        TOTAL=$(echo "${DUP_RESULT}" | awk '{print $2}')
        
        if [ "${UNIQUE}" -eq "${TOTAL}" ]; then
            echo "${LOG_PREFIX} ✓ No duplicates detected"
        else
            DUPS=$((TOTAL - UNIQUE))
            echo "${LOG_PREFIX} ⚠ Found ${DUPS} duplicate row(s) (acceptable, will be cleaned)"
        fi
    fi
fi
echo ""

***REMOVED***
# 4. Health check endpoint
***REMOVED***

echo "${LOG_PREFIX} [4/4] Health Check"
echo "${LOG_PREFIX} ────────────────────────────────────────────────────────"

HEALTH_URL="${HEALTH_URL:-http://localhost:8080/health}"

if command -v curl &> /dev/null; then
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${HEALTH_URL}" 2>/dev/null || echo "000")
    
    if [ "${HTTP_CODE}" = "200" ]; then
        echo "${LOG_PREFIX} ✓ Health check passed (${HEALTH_URL})"
    else
        echo "${LOG_PREFIX} ⚠ Health check returned: ${HTTP_CODE}"
    fi
else
    echo "${LOG_PREFIX} ⚠ curl not available, skipping health check"
fi

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Verification Complete"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo ""
echo "${LOG_PREFIX} Summary:"
echo "${LOG_PREFIX} - Container: $(docker ps --filter "name=${CONTAINER_NAME}" --format "{{.Status}}" 2>/dev/null || echo "NOT FOUND")"
echo "${LOG_PREFIX} - Image: $(docker inspect "${CONTAINER_NAME}" --format='{{.Config.Image}}' 2>/dev/null || echo "UNKNOWN")"
echo "${LOG_PREFIX} - Commit: $(git rev-parse --short HEAD 2>/dev/null || echo "UNKNOWN")"
echo ""
echo "${LOG_PREFIX} Next steps:"
echo "${LOG_PREFIX} 1. Monitor for 15-30 minutes"
echo "${LOG_PREFIX} 2. Run this script periodically: ./verify_p0_hotfix.sh"
echo "${LOG_PREFIX} 3. Check detailed logs: docker logs -f ${CONTAINER_NAME}"
echo ""
