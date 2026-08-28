#!/bin/bash
# Deploy and verify model IQ fix on server

set -e

echo "=== Deploying Model IQ Fix to Server ==="

# Navigate to project directory
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# Get latest changes
git stash pop || true
git pull origin main

# Build the gateway
echo "=== Building gateway ==="
go build -o llm-gateway ./cmd/gateway

# Check if binary was created
if [ ! -f "llm-gateway" ]; then
    echo "Error: Binary not created"
    exit 1
fi

echo "=== Build successful ==="
echo "Binary size: $(ls -lh llm-gateway | awk '{print $5}')"
echo ""
echo "Next steps:"
echo "1. Stop the gateway service on server"
echo "2. Upload the binary to server"
echo "3. Restart the gateway service"
echo "4. Check logs for: 'CHECKPOINT: model_quality_worker started'"
echo "5. Test via admin UI or API"
