#!/bin/bash
set -e

echo "=== Local Deployment Script for Credential Heatmap Feature ==="
echo ""

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Step 1: Database Migration
echo -e "${YELLOW}Step 1: Applying database migration...${NC}"
if [ -f "migrations/035_add_heatmap_indexes.sql" ]; then
    echo "Found migration file. Please run:"
    echo "  psql -f migrations/035_add_heatmap_indexes.sql"
    echo ""
    read -p "Have you applied the database migration? (y/n) " -n 1 -r
    echo ""
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${RED}Please apply the database migration first.${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ Database migration confirmed${NC}"
else
    echo -e "${RED}Migration file not found!${NC}"
    exit 1
fi

# Step 2: Build Backend
echo ""
echo -e "${YELLOW}Step 2: Building backend...${NC}"
go build -o ./bin/llm-gateway-go
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Backend built successfully${NC}"
else
    echo -e "${RED}✗ Backend build failed${NC}"
    exit 1
fi

# Step 3: Start Backend (in background)
echo ""
echo -e "${YELLOW}Step 3: Starting backend service...${NC}"
# Check if backend is already running
if lsof -Pi :8782 -sTCP:LISTEN -t >/dev/null 2>&1; then
    echo "Backend already running on port 8782"
    read -p "Kill existing process and restart? (y/n) " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        lsof -ti:8782 | xargs kill -9 2>/dev/null || true
        sleep 2
    else
        echo "Using existing backend process"
    fi
fi

if ! lsof -Pi :8782 -sTCP:LISTEN -t >/dev/null 2>&1; then
    nohup ./bin/llm-gateway-go > backend.log 2>&1 &
    BACKEND_PID=$!
    echo "Backend started with PID: $BACKEND_PID"
    sleep 3
    
    # Verify backend is running
    if lsof -Pi :8782 -sTCP:LISTEN -t >/dev/null 2>&1; then
        echo -e "${GREEN}✓ Backend service started on port 8782${NC}"
    else
        echo -e "${RED}✗ Backend failed to start. Check backend.log${NC}"
        exit 1
    fi
else
    echo -e "${GREEN}✓ Backend service running on port 8782${NC}"
fi

# Step 4: Install Frontend Dependencies (if needed)
echo ""
echo -e "${YELLOW}Step 4: Checking frontend dependencies...${NC}"
cd web
if [ ! -d "node_modules" ]; then
    echo "Installing frontend dependencies..."
    pnpm install
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✓ Frontend dependencies installed${NC}"
    else
        echo -e "${RED}✗ Frontend dependency installation failed${NC}"
        cd ..
        exit 1
    fi
else
    echo -e "${GREEN}✓ Frontend dependencies already installed${NC}"
fi

# Step 5: Start Frontend Dev Server
echo ""
echo -e "${YELLOW}Step 5: Starting frontend dev server...${NC}"

# Check if frontend is already running
if lsof -Pi :5173 -sTCP:LISTEN -t >/dev/null 2>&1; then
    echo "Frontend already running on port 5173"
    read -p "Kill existing process and restart? (y/n) " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        lsof -ti:5173 | xargs kill -9 2>/dev/null || true
        sleep 2
    else
        echo "Using existing frontend process"
        cd ..
        echo ""
        echo -e "${GREEN}=== Deployment Complete ===${NC}"
        echo ""
        echo "Access the application at:"
        echo -e "${GREEN}  http://localhost:5173/routing-v2/credentials${NC}"
        echo ""
        echo "Backend log: backend.log"
        echo "Frontend log: web/frontend.log"
        exit 0
    fi
fi

if ! lsof -Pi :5173 -sTCP:LISTEN -t >/dev/null 2>&1; then
    nohup pnpm dev > frontend.log 2>&1 &
    FRONTEND_PID=$!
    echo "Frontend started with PID: $FRONTEND_PID"
    sleep 5
    
    # Verify frontend is running
    if lsof -Pi :5173 -sTCP:LISTEN -t >/dev/null 2>&1; then
        echo -e "${GREEN}✓ Frontend dev server started on port 5173${NC}"
    else
        echo -e "${RED}✗ Frontend failed to start. Check web/frontend.log${NC}"
        cd ..
        exit 1
    fi
else
    echo -e "${GREEN}✓ Frontend dev server running on port 5173${NC}"
fi

cd ..

# Step 6: Run API Tests (optional)
echo ""
read -p "Run API tests? (y/n) " -n 1 -r
echo ""
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${YELLOW}Running API tests...${NC}"
    if [ -f "test-heatmap-api.sh" ]; then
        bash test-heatmap-api.sh
    else
        echo -e "${YELLOW}test-heatmap-api.sh not found, skipping tests${NC}"
    fi
fi

echo ""
echo -e "${GREEN}=== Deployment Complete ===${NC}"
echo ""
echo "Services are running:"
echo -e "  Backend:  ${GREEN}http://localhost:8782${NC}"
echo -e "  Frontend: ${GREEN}http://localhost:5173${NC}"
echo ""
echo "Access the Credential Heatmap at:"
echo -e "  ${GREEN}http://localhost:5173/routing-v2/credentials${NC}"
echo ""
echo "Logs:"
echo "  Backend:  backend.log"
echo "  Frontend: web/frontend.log"
echo ""
echo "To stop services:"
echo "  kill \$(lsof -ti:8782,5173)"
