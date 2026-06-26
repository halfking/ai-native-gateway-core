#!/bin/bash
set -euo pipefail

# ═══════════════════════════════════════════════════════════════════════════
# P0 HOTFIX Deployment Script: request_logs Write Restoration
# ═══════════════════════════════════════════════════════════════════════════
# 
# Purpose: Deploy commit fdb9a9bd to restore request_logs write functionality
# 
# What this script does:
# 1. Verify the fix commit is present
# 2. Build new Docker image
# 3. Tag with hotfix version
# 4. Stop existing container
# 5. Start new container
# 6. Verify request_logs is writing
# 
# ═══════════════════════════════════════════════════════════════════════════

LOG_PREFIX="[P0-HOTFIX $(date '+%Y-%m-%d %H:%M:%S')]"

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} P0 HOTFIX: request_logs Write Restoration"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo ""

***REMOVED***
# Step 1: Verify fix commit
***REMOVED***

echo "${LOG_PREFIX} Step 1/6: Verifying fix commit (fdb9a9bd)..."

if ! git log --oneline -1 | grep -q "revert ON CONFLICT to (request_id, ts)"; then
    echo "${LOG_PREFIX} ERROR: Fix commit not found in current HEAD" >&2
    echo "${LOG_PREFIX} Expected: fix(telemetry): revert ON CONFLICT to (request_id, ts)" >&2
    exit 1
fi

CURRENT_COMMIT=$(git rev-parse --short HEAD)
echo "${LOG_PREFIX} ✓ Current commit: ${CURRENT_COMMIT}"
echo ""

***REMOVED***
# Step 2: Build Docker image
***REMOVED***

echo "${LOG_PREFIX} Step 2/6: Building Docker image..."

IMAGE_NAME="llm-gateway-go"
HOTFIX_TAG="p0-hotfix-$(date +%Y%m%d-%H%M%S)-${CURRENT_COMMIT}"

echo "${LOG_PREFIX} Building: ${IMAGE_NAME}:${HOTFIX_TAG}"

if docker build -t "${IMAGE_NAME}:${HOTFIX_TAG}" -t "${IMAGE_NAME}:latest" .; then
    echo "${LOG_PREFIX} ✓ Docker image built successfully"
else
    echo "${LOG_PREFIX} ERROR: Docker build failed" >&2
    exit 2
fi
echo ""

***REMOVED***
# Step 3: Backup current container (optional)
***REMOVED***

echo "${LOG_PREFIX} Step 3/6: Checking existing container..."

CONTAINER_NAME="llm-gateway-go"

if docker ps -a | grep -q "${CONTAINER_NAME}"; then
    echo "${LOG_PREFIX} Found existing container: ${CONTAINER_NAME}"
    
    # Get current image
    CURRENT_IMAGE=$(docker inspect "${CONTAINER_NAME}" --format='{{.Config.Image}}' 2>/dev/null || echo "unknown")
    echo "${LOG_PREFIX} Current image: ${CURRENT_IMAGE}"
    
    # Stop and rename old container (for rollback if needed)
    BACKUP_NAME="${CONTAINER_NAME}-backup-$(date +%Y%m%d-%H%M%S)"
    echo "${LOG_PREFIX} Backing up to: ${BACKUP_NAME}"
    
    docker stop "${CONTAINER_NAME}" 2>/dev/null || true
    docker rename "${CONTAINER_NAME}" "${BACKUP_NAME}" 2>/dev/null || true
    
    echo "${LOG_PREFIX} ✓ Old container backed up"
else
    echo "${LOG_PREFIX} No existing container found (fresh deployment)"
fi
echo ""

***REMOVED***
# Step 4: Deploy new container
***REMOVED***

echo "${LOG_PREFIX} Step 4/6: Deploying new container..."

# Note: You need to customize these environment variables for your setup
# Using a generic command - adjust as needed

if [ -f ".env" ]; then
    echo "${LOG_PREFIX} Loading environment from .env file"
    source .env
fi

# Example deployment (adjust ports, volumes, env vars as needed)
docker run -d \
    --name "${CONTAINER_NAME}" \
    --restart unless-stopped \
    -p 8080:8080 \
    -e "LLM_GATEWAY_DATABASE_URL=${LLM_GATEWAY_DATABASE_URL:-}" \
    -e "LLM_GATEWAY_REDIS_URL=${LLM_GATEWAY_REDIS_URL:-}" \
    "${IMAGE_NAME}:${HOTFIX_TAG}"

if [ $? -eq 0 ]; then
    echo "${LOG_PREFIX} ✓ Container started successfully"
else
    echo "${LOG_PREFIX} ERROR: Failed to start container" >&2
    exit 3
fi
echo ""

***REMOVED***
# Step 5: Wait for startup
***REMOVED***

echo "${LOG_PREFIX} Step 5/6: Waiting for gateway to start..."

for i in {1..30}; do
    if docker logs "${CONTAINER_NAME}" 2>&1 | grep -q "HTTP server listening"; then
        echo "${LOG_PREFIX} ✓ Gateway started (after ${i}s)"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "${LOG_PREFIX} WARNING: Timeout waiting for startup" >&2
        echo "${LOG_PREFIX} Check logs: docker logs ${CONTAINER_NAME}" >&2
    fi
    sleep 1
done
echo ""

***REMOVED***
# Step 6: Verify request_logs writes (requires DB access)
***REMOVED***

echo "${LOG_PREFIX} Step 6/6: Verification..."

if [ -n "${LLM_GATEWAY_DATABASE_URL:-}" ]; then
    echo "${LOG_PREFIX} Checking request_logs writes (last 5 minutes)..."
    
    psql "${LLM_GATEWAY_DATABASE_URL}" -t -c "
        SELECT 
            COUNT(*) as recent_count,
            MAX(ts) as latest_ts
        FROM request_logs
        WHERE ts > now() - interval '5 minutes'
    " 2>/dev/null || {
        echo "${LOG_PREFIX} WARNING: Could not verify DB writes (psql not available or DB unreachable)"
        echo "${LOG_PREFIX} Manual verification recommended"
    }
else
    echo "${LOG_PREFIX} WARNING: LLM_GATEWAY_DATABASE_URL not set, skipping DB verification"
fi

echo ""
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo "${LOG_PREFIX} Deployment completed!"
echo "${LOG_PREFIX} ════════════════════════════════════════════════════════"
echo ""
echo "${LOG_PREFIX} Image:     ${IMAGE_NAME}:${HOTFIX_TAG}"
echo "${LOG_PREFIX} Container: ${CONTAINER_NAME}"
echo "${LOG_PREFIX} Commit:    ${CURRENT_COMMIT}"
echo ""
echo "${LOG_PREFIX} Next steps:"
echo "${LOG_PREFIX} 1. Send test request to verify request_logs writes"
echo "${LOG_PREFIX} 2. Monitor for 15 minutes"
echo "${LOG_PREFIX} 3. Check logs: docker logs -f ${CONTAINER_NAME}"
echo "${LOG_PREFIX} 4. If issues, rollback: docker stop ${CONTAINER_NAME} && docker start ${CONTAINER_NAME}-backup-*"
echo ""
echo "${LOG_PREFIX} Documentation:"
echo "${LOG_PREFIX} - P0_INCIDENT_SUMMARY.md"
echo "${LOG_PREFIX} - CRITICAL_BUG_ANALYSIS.md"
echo "${LOG_PREFIX} - HOTFIX_REVERT_ON_CONFLICT.md"
echo ""
