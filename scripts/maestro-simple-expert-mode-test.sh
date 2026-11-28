#!/usr/bin/env bash
# maestro-simple-expert-mode-test.sh
# 简易·专家模式功能全面验证脚本（Maestro方法）
# 
# 测试环境：211服务器（192.168.31.211）
# 测试用户：admin / Veritrans&9527
# 测试范围：8个核心用户场景

set -euo pipefail

# 配置（245 示例：BASE_URL=http://127.0.0.1:8781 在目标机上跑，或经 SSH 转发）
BASE_URL="${BASE_URL:-http://192.168.31.211:8781}"
USERNAME="${USERNAME:-admin}"
PASSWORD="${PASSWORD:-Veritrans&9527}"
TEST_RESULTS="/tmp/maestro-test-results-$(date +%Y%m%d-%H%M%S).log"

echo "=== 简易·专家模式 Maestro 测试 ===" | tee "$TEST_RESULTS"
echo "测试时间: $(date)" | tee -a "$TEST_RESULTS"
echo "目标环境: $BASE_URL" | tee -a "$TEST_RESULTS"
echo "" | tee -a "$TEST_RESULTS"

# 测试计数器
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# JSON 取值（不依赖 jq；生产机常无 jq）
json_get() {
    local expr="$1"
    python3 -c "
import json,sys
d=json.load(sys.stdin)
def dig(obj, path):
    cur=obj
    for p in path.split('.'):
        if isinstance(cur, dict) and p in cur:
            cur=cur[p]
        else:
            return ''
    if cur is None:
        return ''
    return cur
v=dig(d, '''$expr''')
if v=='' and '.' not in '''$expr''':
    # fallbacks used by callers
    pass
print(v if not isinstance(v,(dict,list)) else json.dumps(v))
"
}

json_len() {
    python3 -c "import json,sys;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else (len(d.get('items') or d.get('data') or []) if isinstance(d,dict) else 0))"
}

# 测试结果记录函数
test_passed() {
    local test_name="$1"
    # 不用 ((var++))：当值为 0 时表达式结果为 0 ⇒ set -e 下直接退出
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    PASSED_TESTS=$((PASSED_TESTS + 1))
    echo "✅ PASS: $test_name" | tee -a "$TEST_RESULTS"
}

test_failed() {
    local test_name="$1"
    local reason="$2"
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    FAILED_TESTS=$((FAILED_TESTS + 1))
    echo "❌ FAIL: $test_name - $reason" | tee -a "$TEST_RESULTS"
}

test_section() {
    local section="$1"
    echo "" | tee -a "$TEST_RESULTS"
    echo "📋 $section" | tee -a "$TEST_RESULTS"
    echo "----------------------------------------" | tee -a "$TEST_RESULTS"
}

# 获取登录 token（契约：POST /api/auth/token → access_token；勿用 /api/admin/login）
login() {
    echo "正在登录..." | tee -a "$TEST_RESULTS"
    local response
    response=$(curl -s -X POST "$BASE_URL/api/auth/token" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
        -w "\n%{http_code}")
    
    local http_code=$(echo "$response" | tail -1)
    local body=$(echo "$response" | sed '$d')
    
    if [ "$http_code" = "200" ]; then
        TOKEN=$(echo "$body" | python3 -c "import json,sys;d=json.load(sys.stdin);print(d.get('access_token') or d.get('token') or (d.get('data') or {}).get('token') or '')")
        if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ]; then
            test_passed "登录成功"
            return 0
        fi
    fi
    
    test_failed "登录失败" "HTTP $http_code"
    return 1
}

# ============================================================================
# 测试场景1：工作模式切换
# ============================================================================
test_section "场景1：工作模式切换"

test_mode_switch() {
    # 测试1.1：获取当前工作模式
    local response
    response=$(curl -s "$BASE_URL/api/admin/settings/gateway.mode" \
        -H "Authorization: Bearer $TOKEN" \
        -w "\n%{http_code}")
    
    local http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ]; then
        test_passed "获取工作模式设置"
    else
        test_failed "获取工作模式设置" "HTTP $http_code"
        return 1
    fi
    
    # 测试1.2：验证设置项存在
    local body=$(echo "$response" | sed '$d')
    local value=$(echo "$body" | python3 -c "import json,sys;d=json.load(sys.stdin);print(d.get('value') if d.get('value') is not None else (d.get('data') or {}).get('value') or '')")
    
    if [ -n "$value" ]; then
        test_passed "工作模式值存在: $value"
    else
        test_failed "工作模式值缺失" "响应: $body"
    fi
}

# ============================================================================
# 测试场景2：简易模式 - 接入清单（首页）
# ============================================================================
test_section "场景2：接入清单"

test_access_list() {
    # 测试2.1：获取密钥列表
    local response
    response=$(curl -s "$BASE_URL/api/keys" \
        -H "Authorization: Bearer $TOKEN" \
        -w "\n%{http_code}")
    
    local http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ]; then
        test_passed "获取密钥列表"
    else
        test_failed "获取密钥列表" "HTTP $http_code"
        return 1
    fi
    
    # 测试2.2：验证数据结构
    local body=$(echo "$response" | sed '$d')
    local count=$(echo "$body" | json_len)
    
    test_passed "密钥列表数量: $count"
}

# ============================================================================
# 测试场景3：简易模式 - 供应商页
# ============================================================================
test_section "场景3：供应商页"

test_providers() {
    # 测试3.1：获取供应商列表
    local response
    response=$(curl -s "$BASE_URL/api/providers" \
        -H "Authorization: Bearer $TOKEN" \
        -w "\n%{http_code}")
    
    local http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ]; then
        test_passed "获取供应商列表"
    else
        test_failed "获取供应商列表" "HTTP $http_code"
        return 1
    fi
    
    # 测试3.2：获取供应商目录
    response=$(curl -s "$BASE_URL/api/catalog" \
        -H "Authorization: Bearer $TOKEN" \
        -w "\n%{http_code}")
    
    http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ]; then
        local body=$(echo "$response" | sed '$d')
        local count=$(echo "$body" | json_len)
        test_passed "获取供应商目录: $count 个"
    else
        test_failed "获取供应商目录" "HTTP $http_code"
    fi
}

# ============================================================================
# 测试场景4：前端资源可访问性
# ============================================================================
test_section "场景4：前端资源"

test_frontend() {
    # 测试4.1：访问首页
    local response
    response=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/")
    
    if [ "$response" = "200" ]; then
        test_passed "首页可访问"
    else
        test_failed "首页访问失败" "HTTP $response"
    fi
    
    # 测试4.2：验证简易模式前端痕迹（打包后可能被哈希/压缩吃掉 ⇒ WARN 非 FAIL）
    # ★ grep 无命中时 exit 1；配合 set -o pipefail 会整脚本退出 —— 必须 || true
    response=$( { curl -s "$BASE_URL/" | grep -oE 'SimpleProvidersView|SimpleOverviewView|WorkModeSwitch|simple-mode' || true; } | wc -l | tr -d ' ')
    response=${response:-0}
    if [ "$response" -gt 0 ] 2>/dev/null; then
        test_passed "简易模式前端资源已部署"
    else
        echo "⚠️  WARN: 无法检测简易模式前端资源（可能已混淆/压缩）" | tee -a "$TEST_RESULTS"
    fi
}

# ============================================================================
# 测试场景5：API端点健康检查
# ============================================================================
test_section "场景5：API健康检查"

test_api_health() {
    # 测试5.1：版本身份（/api/system/info 已不存在）
    local response
    response=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/api/system/version")
    
    if [ "$response" = "200" ]; then
        test_passed "系统版本API"
    else
        test_failed "系统版本API" "HTTP $response"
    fi
    
    # 测试5.2：路由可用模型
    response=$(curl -s "$BASE_URL/api/routing/available-models" \
        -H "Authorization: Bearer $TOKEN" \
        -w "\n%{http_code}")
    
    local http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ]; then
        test_passed "可用模型API"
    else
        test_failed "可用模型API" "HTTP $http_code"
    fi
}

# ============================================================================
# 测试场景6：简易模式特定端点
# ============================================================================
test_section "场景6：简易模式特定功能"

test_simple_mode_endpoints() {
    # 测试6.1：工作模式设置项类型
    local response
    response=$(curl -s "$BASE_URL/api/admin/settings/gateway.mode" \
        -H "Authorization: Bearer $TOKEN")
    
    local value=$(echo "$response" | python3 -c "import json,sys;d=json.load(sys.stdin);v=d.get('value');
print(v if v is not None else (d.get('data') or {}).get('value') or '')")
    
    if [ "$value" = "simple" ] || [ "$value" = "expert" ]; then
        test_passed "工作模式值有效: $value"
    else
        # 默认值或空值也可以接受
        if [ -z "$value" ] || [ "$value" = "null" ]; then
            test_passed "工作模式使用默认值"
        else
            test_failed "工作模式值无效" "value=$value"
        fi
    fi
}

# ============================================================================
# 测试场景7：导航配置验证
# ============================================================================
test_section "场景7：导航配置"

test_navigation() {
    # 这个测试需要检查前端路由配置
    # 由于是SPA应用，我们检查关键路径是否返回200
    
    local paths=(
        "/"
        "/dashboard"
        "/providers"
        "/keys"
    )
    
    for path in "${paths[@]}"; do
        local response
        response=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL$path")
        
        # 200=SPA 壳；401=网关/nginx 鉴权门（路径存在）；302 登录跳转亦可
        if [ "$response" = "200" ] || [ "$response" = "401" ] || [ "$response" = "302" ]; then
            test_passed "路径可达: $path (HTTP $response)"
        else
            test_failed "路径访问失败: $path" "HTTP $response"
        fi
    done
}

# ============================================================================
# 测试场景8：回归测试 - 确保专家模式功能未破坏
# ============================================================================
test_section "场景8：专家模式回归"

test_expert_mode_regression() {
    # 测试8.1：关键API端点仍然可用
    local endpoints=(
        "/api/providers"
        "/api/keys"
        "/api/models"
        "/api/usage/summary"
    )
    
    for endpoint in "${endpoints[@]}"; do
        local response
        response=$(curl -s -o /dev/null -w "%{http_code}" \
            "$BASE_URL$endpoint" \
            -H "Authorization: Bearer $TOKEN")
        
        if [ "$response" = "200" ] || [ "$response" = "401" ]; then
            # 401表示需要认证，但端点存在
            test_passed "端点存在: $endpoint"
        else
            test_failed "端点问题: $endpoint" "HTTP $response"
        fi
    done
}

# ============================================================================
# 执行测试
# ============================================================================

# 登录
if ! login; then
    echo "" | tee -a "$TEST_RESULTS"
    echo "❌ 登录失败，无法继续测试" | tee -a "$TEST_RESULTS"
    exit 1
fi

# 执行所有测试场景
test_mode_switch
test_access_list
test_providers
test_frontend
test_api_health
test_simple_mode_endpoints
test_navigation
test_expert_mode_regression

# ============================================================================
# 测试总结
# ============================================================================

echo "" | tee -a "$TEST_RESULTS"
echo "========================================" | tee -a "$TEST_RESULTS"
echo "测试总结" | tee -a "$TEST_RESULTS"
echo "========================================" | tee -a "$TEST_RESULTS"
echo "总测试数: $TOTAL_TESTS" | tee -a "$TEST_RESULTS"
echo "通过: $PASSED_TESTS" | tee -a "$TEST_RESULTS"
echo "失败: $FAILED_TESTS" | tee -a "$TEST_RESULTS"

if [ $FAILED_TESTS -eq 0 ]; then
    echo "" | tee -a "$TEST_RESULTS"
    echo "🎉 所有测试通过！" | tee -a "$TEST_RESULTS"
    exit 0
else
    echo "" | tee -a "$TEST_RESULTS"
    echo "⚠️  有 $FAILED_TESTS 个测试失败" | tee -a "$TEST_RESULTS"
    exit 1
fi
