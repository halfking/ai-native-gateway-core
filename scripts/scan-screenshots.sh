#!/bin/bash
# Screenshot Security Scanner
# Scans PNG files for potential sensitive information

set -e

RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
NC='\033[0m'

SCREENSHOTS_DIRS=(
    "gui-test-screenshots"
    "docs/screenshots"
)

SENSITIVE_PATTERNS=(
    # Internal IPs
    "172\.16\.[0-9]+\.[0-9]+"
    "192\.168\.[0-9]+\.[0-9]+"
    "10\.[0-9]+\.[0-9]+\.[0-9]+"
    # Internal domains
    "kxpms\.cn"
    "\.internal"
    "\.local"
    # API key patterns
    "sk-[a-zA-Z0-9]{32,}"
    "Bearer [a-zA-Z0-9]{20,}"
    # Email patterns that look real
    "@[a-zA-Z0-9-]+\.(com|cn|net)"
    # Session/Request IDs that look real
    "gw_[a-f0-9]{24,}"
    "req_[a-f0-9]{24,}"
)

echo "=== Screenshot Security Scanner ==="
echo ""

TOTAL_SCANNED=0
SUSPICIOUS_COUNT=0
CLEAN_COUNT=0

for DIR in "${SCREENSHOTS_DIRS[@]}"; do
    if [ ! -d "$DIR" ]; then
        echo "Directory not found: $DIR"
        continue
    fi
    
    echo "Scanning directory: $DIR"
    
    for PNG in "$DIR"/*.png; do
        if [ ! -f "$PNG" ]; then
            continue
        fi
        
        TOTAL_SCANNED=$((TOTAL_SCANNED + 1))
        BASENAME=$(basename "$PNG")
        
        # Use tesseract OCR if available, otherwise use strings
        TEXT=""
        if command -v tesseract &> /dev/null; then
            TEXT=$(tesseract "$PNG" - 2>/dev/null || true)
        else
            # Fallback: extract any readable text from PNG metadata
            TEXT=$(strings "$PNG" 2>/dev/null || true)
        fi
        
        # Check for sensitive patterns
        FOUND_ISSUES=0
        ISSUES=""
        
        for PATTERN in "${SENSITIVE_PATTERNS[@]}"; do
            if echo "$TEXT" | grep -qE "$PATTERN" 2>/dev/null; then
                FOUND_ISSUES=1
                MATCH=$(echo "$TEXT" | grep -oE "$PATTERN" | head -1)
                ISSUES="${ISSUES}    - Pattern: ${PATTERN} (matched: ${MATCH})\n"
            fi
        done
        
        if [ $FOUND_ISSUES -eq 1 ]; then
            echo -e "${RED}✗ SUSPICIOUS${NC}: $BASENAME"
            echo -e "$ISSUES"
            SUSPICIOUS_COUNT=$((SUSPICIOUS_COUNT + 1))
        else
            echo -e "${GREEN}✓ CLEAN${NC}: $BASENAME"
            CLEAN_COUNT=$((CLEAN_COUNT + 1))
        fi
    done
    
    echo ""
done

echo "=== Summary ==="
echo "Total scanned: $TOTAL_SCANNED"
echo -e "${GREEN}Clean: $CLEAN_COUNT${NC}"
echo -e "${RED}Suspicious: $SUSPICIOUS_COUNT${NC}"
echo ""

if [ $SUSPICIOUS_COUNT -gt 0 ]; then
    echo -e "${YELLOW}WARNING: Some screenshots may contain sensitive information${NC}"
    echo "Recommendation: Manual review required or regenerate with demo data"
    exit 1
else
    echo -e "${GREEN}All screenshots appear safe for public release${NC}"
    exit 0
fi
