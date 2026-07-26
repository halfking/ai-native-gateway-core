#!/usr/bin/env bash
# ===========================================================================
# check-domain-drift.sh — 扫描 Go 源码中硬编码域名，与 DOMAIN_REGISTRY 对比
#
# Purpose: 检测未登记的域名硬编码，防止"爆一个挂一片"（audit P2-1）
# Usage:   bash scripts/check-domain-drift.sh [--list-unregistered] [--ci]
# ===========================================================================

set -euo pipefail

REGISTRY="docs/domains/DOMAIN_REGISTRY.md"
GO_SRC_DIRS=("cmd" "internal" "domains" "installer" "maas" "admin")

# 从 registry 提取已知域名（忽略注释行和表头行）
extract_known_domains() {
    grep -oE '[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}' "$REGISTRY" \
        | grep -v '\.md\|\.go\|\.sh\|\.yaml\|\.cnf\|\.conf\|example\.com\|internal\.example' \
        | sort -u
}

# 扫描 Go 源码中的硬编码域名（字符串字面量中的 FQDN）
scan_go_domains() {
    local pattern='"[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.([a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}[^"]*"'
    for dir in "${GO_SRC_DIRS[@]}"; do
        rg -n --type go "$pattern" "$dir" 2>/dev/null || true
    done
}

echo "=== Domain Drift Check ==="

if [[ ! -f "$REGISTRY" ]]; then
    echo "❌ DOMAIN_REGISTRY not found at $REGISTRY"
    exit 1
fi

KNOWN=$(extract_known_domains)
echo "Known domains in registry: $(echo "$KNOWN" | wc -l)"

# Scan and extract unique domains from Go source
DRIFT=$(
    scan_go_domains \
        | grep -oE 'https?://[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}' \
        | sed 's|https\?://||' \
        | sort -u
)

UNREGISTERED=()
while IFS= read -r domain; do
    [[ -z "$domain" ]] && continue
    if ! echo "$KNOWN" | grep -qF "$domain"; then
        UNREGISTERED+=("$domain")
    fi
done <<< "$DRIFT"

if [[ ${#UNREGISTERED[@]} -eq 0 ]]; then
    echo "✅ All domains in Go source are registered"
    exit 0
else
    echo "⚠️  Unregistered domains found:"
    printf '   - %s\n' "${UNREGISTERED[@]}"
    echo ""
    echo "   Either:"
    echo "   1. Add them to $REGISTRY (for new production domains)"
    echo "   2. Replace with env-var injection (for configurable domains)"
    echo "   3. Mark as excluded if they're test/example domains"
    exit 1
fi
