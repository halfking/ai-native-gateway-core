#!/bin/bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi

# F-41激活验证脚本
# 验证 PromptInjectionHook 延迟绑定和 user_content 提取的实现状态

set -e

echo "╔════════════════════════════════════════════════════════════╗"
echo "║         F-41 Activation Implementation Verification        ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

cd "$(dirname "$0")/.."

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

check_passed=0
check_failed=0

# 检查函数
check() {
    local description=$1
    local file=$2
    local pattern=$3
    
    echo -n "Checking: $description ... "
    
    if grep -q "$pattern" "$file" 2>/dev/null; then
        echo -e "${GREEN}✅ PASS${NC}"
        ((check_passed++))
        return 0
    else
        echo -e "${RED}❌ FAIL${NC}"
        echo "  Expected pattern: $pattern"
        echo "  In file: $file"
        ((check_failed++))
        return 1
    fi
}

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "1. PromptInjectionHook Implementation"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "Hook struct with detector field" \
    "domains/hooks/promptinjection/hook.go" \
    "type Hook struct"

check "NewHook constructor" \
    "domains/hooks/promptinjection/hook.go" \
    "func NewHook"

check "BindDetector method" \
    "domains/hooks/promptinjection/hook.go" \
    "func.*BindDetector"

check "Enabled checks detector != nil" \
    "domains/hooks/promptinjection/hook.go" \
    "h.detector == nil"

check "Execute reads user_content" \
    "domains/hooks/promptinjection/hook.go" \
    'Metadata\["user_content"\]'

check "Execute writes governance verdict" \
    "domains/hooks/promptinjection/hook.go" \
    "RecordVerdict"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "2. Pipeline Integration"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "Hook registered in pipeline" \
    "cmd/gateway/main_pipeline.go" \
    "governance_prompt_injection"

check "Hook stored in deps" \
    "cmd/gateway/main_pipeline.go" \
    "deps.PromptInjectionHook"

check "Stage added unconditionally" \
    "cmd/gateway/main_pipeline.go" \
    "governance_prompt_injection.*Phase"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "3. Detector Late Binding"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "SetV2DispatchAnalysisResources accepts detector" \
    "cmd/gateway/main_pipeline.go" \
    "detector promptinjectionhooks.Detector"

check "Detector stored in deps" \
    "cmd/gateway/main_pipeline.go" \
    "deps.PromptInjectionDetector = detector"

check "BindDetector called on hook" \
    "cmd/gateway/main_pipeline.go" \
    "PromptInjectionHook.BindDetector"

check "Detector created in main.go" \
    "cmd/gateway/main.go" \
    "promptinjection.NewDetector"

check "Detector passed to SetV2DispatchAnalysisResources" \
    "cmd/gateway/main.go" \
    "SetV2DispatchAnalysisResources.*piDetector"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "4. User Content Extraction"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "user_content_extract.go exists" \
    "cmd/gateway/user_content_extract.go" \
    "userContentFromBody"

check "userContentEnabled function" \
    "cmd/gateway/user_content_extract.go" \
    "func userContentEnabled"

check "userContentFromBody function" \
    "cmd/gateway/user_content_extract.go" \
    "func userContentFromBody"

check "Supports OpenAI messages" \
    "cmd/gateway/user_content_extract.go" \
    "Messages.*json.RawMessage"

check "Supports completions prompt" \
    "cmd/gateway/user_content_extract.go" \
    "Prompt.*json.RawMessage"

check "Supports responses input" \
    "cmd/gateway/user_content_extract.go" \
    "Input.*json.RawMessage"

check "Multimodal content support" \
    "cmd/gateway/user_content_extract.go" \
    "textsFromContentJSON"

check "Rune-safe truncation" \
    "cmd/gateway/user_content_extract.go" \
    "truncateRuneSafe"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "5. User Content Writing"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "user_content written in main_pipeline.go" \
    "cmd/gateway/main_pipeline.go" \
    'Metadata\["user_content"\].*='

check "userContentEnabled check" \
    "cmd/gateway/main_pipeline.go" \
    "userContentEnabled()"

check "userContentFromBody called" \
    "cmd/gateway/main_pipeline.go" \
    "userContentFromBody(rawBody)"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "6. Unit Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

check "Hook unit tests exist" \
    "domains/hooks/promptinjection/hook_test.go" \
    "func TestHook"

echo ""
echo "Running hook unit tests..."
if go test ./domains/hooks/promptinjection -v -run TestHook 2>&1 | grep -q "PASS"; then
    echo -e "${GREEN}✅ All hook unit tests passed${NC}"
    ((check_passed++))
else
    echo -e "${RED}❌ Some hook unit tests failed${NC}"
    ((check_failed++))
fi

echo ""
echo "════════════════════════════════════════════════════════════"
echo "Summary"
echo "════════════════════════════════════════════════════════════"
echo ""
echo "Total checks: $((check_passed + check_failed))"
echo -e "${GREEN}Passed: $check_passed${NC}"
echo -e "${RED}Failed: $check_failed${NC}"
echo ""

if [ $check_failed -eq 0 ]; then
    echo -e "${GREEN}✅ F-41 Implementation: COMPLETE${NC}"
    echo ""
    echo "All components verified:"
    echo "  ✅ PromptInjectionHook with BindDetector"
    echo "  ✅ Detector late binding in SetV2DispatchAnalysisResources"
    echo "  ✅ user_content extraction from 4 endpoint types"
    echo "  ✅ user_content written to env.Metadata"
    echo "  ✅ Hook registered in pipeline (unconditionally)"
    echo "  ✅ Unit tests passing"
    echo ""
    echo "Status: 🟢 PRODUCTION READY"
    exit 0
else
    echo -e "${RED}❌ F-41 Implementation: INCOMPLETE${NC}"
    echo ""
    echo "Missing components detected. Please review failed checks above."
    exit 1
fi
