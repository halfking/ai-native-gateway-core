#!/usr/bin/env bash
# 代码安全审查脚本
# 扫描 Go 代码中的安全问题

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 结果统计
TOTAL_ISSUES=0
HIGH_ISSUES=0
MEDIUM_ISSUES=0
LOW_ISSUES=0

REPORT_FILE="$SCRIPT_DIR/code_review_report.md"
: > "$REPORT_FILE"

log_info() {
    echo -e "${GREEN}[INFO]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

log_section() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE} $*${NC}"
    echo -e "${BLUE}========================================${NC}"
}

report_issue() {
    local severity=$1
    local title=$2
    local file=$3
    local line=$4
    local description=$5
    
    TOTAL_ISSUES=$((TOTAL_ISSUES + 1))
    
    case $severity in
        HIGH)
            HIGH_ISSUES=$((HIGH_ISSUES + 1))
            echo -e "${RED}[HIGH]${NC} $title"
            ;;
        MEDIUM)
            MEDIUM_ISSUES=$((MEDIUM_ISSUES + 1))
            echo -e "${YELLOW}[MEDIUM]${NC} $title"
            ;;
        LOW)
            LOW_ISSUES=$((LOW_ISSUES + 1))
            echo -e "${GREEN}[LOW]${NC} $title"
            ;;
    esac
    
    echo "  📁 $file:$line"
    echo "  💬 $description"
    echo ""
    
    # 写入报告
    cat >> "$REPORT_FILE" <<EOF

## [$severity] $title

- **文件**: \`$file:$line\`
- **描述**: $description

\`\`\`
$(sed -n "${line}p" "$file" 2>/dev/null || echo "无法读取行")
\`\`\`

EOF
}

# ============================================================
# 检查 1: SQL 注入风险
# ============================================================
check_sql_injection() {
    log_section "检查 SQL 注入风险"
    
    log_info "扫描 SQL 字符串拼接..."
    
    # 检查 fmt.Sprintf 构造 SQL
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            report_issue "HIGH" "SQL 字符串拼接" "$file" "$line" \
                "使用 fmt.Sprintf 构造 SQL 可能导致注入，应使用参数化查询"
        fi
    done < <(grep -rn 'fmt\.Sprintf.*SELECT\|fmt\.Sprintf.*INSERT\|fmt\.Sprintf.*UPDATE\|fmt\.Sprintf.*DELETE' \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" | grep -v "// " || true)
    
    # 检查字符串连接构造 SQL
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && [[ "$content" =~ \"SELECT.*\"+|\"INSERT.*\"+|\"UPDATE.*\"+|\"DELETE.*\"+ ]]; then
            report_issue "HIGH" "SQL 字符串连接" "$file" "$line" \
                "使用 + 连接 SQL 字符串可能导致注入"
        fi
    done < <(grep -rn '"SELECT\|"INSERT\|"UPDATE\|"DELETE' \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" | grep "+" || true)
    
    log_info "✅ SQL 注入检查完成"
}

# ============================================================
# 检查 2: 路径穿越风险
# ============================================================
check_path_traversal() {
    log_section "检查路径穿越风险"
    
    log_info "扫描文件路径操作..."
    
    # 检查 os.Open 使用
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            # 检查该文件中是否有 filepath.Clean 或路径验证
            if ! grep -q "filepath.Clean\|strings.Contains.*\\.\\.\|filepath.Abs" "$file" 2>/dev/null; then
                report_issue "MEDIUM" "缺少路径验证" "$file" "$line" \
                    "os.Open/OpenFile 调用缺少路径清理和 .. 检测"
            fi
        fi
    done < <(grep -rn "os\.Open\|os\.OpenFile\|os\.Create" \
        --include="*.go" "$PROJECT_ROOT/autoupdate" "$PROJECT_ROOT/installer" 2>/dev/null || true)
    
    # 检查 filepath.Join 后是否验证
    log_info "检查 filepath.Join 使用..."
    local join_count=0
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            join_count=$((join_count + 1))
        fi
    done < <(grep -rn "filepath.Join" --include="*.go" "$PROJECT_ROOT" 2>/dev/null || true)
    
    log_info "找到 $join_count 处 filepath.Join 调用（需人工审查）"
    
    log_info "✅ 路径穿越检查完成"
}

# ============================================================
# 检查 3: 敏感信息泄露
# ============================================================
check_sensitive_info() {
    log_section "检查敏感信息泄露"
    
    log_info "扫描日志中的敏感数据..."
    
    # 检查 fmt.Printf 打印敏感字段
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && [[ "$content" =~ token|password|secret|key|credential ]]; then
            # 排除安全的日志（只打印 key 名称，不打印值）
            if ! [[ "$content" =~ \".*key.*\"|key_name|key_id ]]; then
                report_issue "MEDIUM" "可能泄露敏感信息" "$file" "$line" \
                    "日志可能包含敏感数据（token/password/secret）"
            fi
        fi
    done < <(grep -rn 'fmt\.Printf\|log\.Printf\|slog\.Info\|slog\.Debug' \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" || true)
    
    # 检查环境变量打印
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            report_issue "LOW" "打印环境变量" "$file" "$line" \
                "打印 os.Environ 可能泄露敏感配置"
        fi
    done < <(grep -rn "os.Environ\|os.Getenv.*Print" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" || true)
    
    log_info "✅ 敏感信息泄露检查完成"
}

# ============================================================
# 检查 4: 不安全的加密
# ============================================================
check_crypto() {
    log_section "检查加密实现"
    
    log_info "扫描不安全的加密算法..."
    
    # 检查弱加密算法
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            report_issue "HIGH" "使用弱加密算法 MD5" "$file" "$line" \
                "MD5 已不安全，应使用 SHA256 或更强算法"
        fi
    done < <(grep -rn "crypto/md5\|md5.New\|md5.Sum" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" | grep -v "// " || true)
    
    # 检查 SHA1（除非用于 HMAC）
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && ! [[ "$content" =~ hmac ]]; then
            report_issue "MEDIUM" "使用 SHA1" "$file" "$line" \
                "SHA1 已被攻破，建议升级到 SHA256"
        fi
    done < <(grep -rn "crypto/sha1\|sha1.New\|sha1.Sum" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" | grep -v "// " || true)
    
    # 检查硬编码密钥
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && [[ "$content" =~ =\"[a-zA-Z0-9]{16,}\" ]]; then
            report_issue "HIGH" "可能存在硬编码密钥" "$file" "$line" \
                "检测到硬编码字符串，确认是否为加密密钥"
        fi
    done < <(grep -rn "AES\|DES\|key\s*:=\s*\[\]byte" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" || true)
    
    log_info "✅ 加密检查完成"
}

# ============================================================
# 检查 5: 命令注入
# ============================================================
check_command_injection() {
    log_section "检查命令注入风险"
    
    log_info "扫描 exec.Command 使用..."
    
    # 检查 exec.Command 使用用户输入
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]]; then
            # 检查是否使用了 shell
            if [[ "$content" =~ bash|sh|cmd.exe ]]; then
                report_issue "HIGH" "通过 shell 执行命令" "$file" "$line" \
                    "通过 shell 执行可能导致命令注入，建议直接调用命令"
            else
                report_issue "LOW" "使用 exec.Command" "$file" "$line" \
                    "确保命令参数来自可信源（需人工审查）"
            fi
        fi
    done < <(grep -rn "exec\.Command\|exec\.CommandContext" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" || true)
    
    log_info "✅ 命令注入检查完成"
}

# ============================================================
# 检查 6: 不安全的随机数
# ============================================================
check_random() {
    log_section "检查随机数生成"
    
    log_info "扫描不安全的随机数..."
    
    # 检查使用 math/rand 生成安全相关随机数
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && [[ "$content" =~ token|secret|nonce|password|key ]]; then
            report_issue "HIGH" "使用不安全的随机数" "$file" "$line" \
                "安全相关随机数应使用 crypto/rand 而非 math/rand"
        fi
    done < <(grep -rn "math/rand\|rand.Intn\|rand.Int" \
        --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" || true)
    
    log_info "✅ 随机数检查完成"
}

# ============================================================
# 检查 7: 并发安全
# ============================================================
check_concurrency() {
    log_section "检查并发安全"
    
    log_info "扫描可能的竞态条件..."
    
    # 检查没有 mutex 保护的 map 写入
    local map_writes=0
    while IFS=: read -r file line content; do
        if [[ -n "$file" ]] && [[ "$content" =~ \[.*\]\s*= ]]; then
            # 检查文件中是否有 sync.Mutex 或 sync.RWMutex
            if ! grep -q "sync\.Mutex\|sync\.RWMutex\|sync\.Map" "$file" 2>/dev/null; then
                map_writes=$((map_writes + 1))
            fi
        fi
    done < <(grep -rn "map\[" --include="*.go" "$PROJECT_ROOT" 2>/dev/null | grep -v "_test.go" | head -50 || true)
    
    if [[ $map_writes -gt 0 ]]; then
        log_warn "发现 $map_writes 个可能的无锁 map 操作（需人工审查）"
    fi
    
    log_info "✅ 并发安全检查完成"
}

# ============================================================
# 运行 gosec（如果可用）
# ============================================================
run_gosec() {
    log_section "运行 gosec 静态分析"
    
    if ! command -v gosec &> /dev/null; then
        log_warn "gosec 未安装，跳过静态分析"
        log_info "安装命令: go install github.com/securego/gosec/v2/cmd/gosec@latest"
        return
    fi
    
    log_info "运行 gosec..."
    
    local gosec_report="$SCRIPT_DIR/gosec-report.json"
    
    if gosec -fmt json -out "$gosec_report" ./... 2>/dev/null; then
        log_info "✅ gosec 扫描完成"
        
        # 解析结果
        if command -v jq &> /dev/null && [[ -f "$gosec_report" ]]; then
            local issues=$(jq '.Issues | length' "$gosec_report" 2>/dev/null || echo "0")
            log_info "发现 $issues 个问题"
            
            # 提取 HIGH 级别问题
            local high=$(jq '[.Issues[] | select(.severity == "HIGH")] | length' "$gosec_report" 2>/dev/null || echo "0")
            if [[ $high -gt 0 ]]; then
                log_error "发现 $high 个 HIGH 级别问题"
                HIGH_ISSUES=$((HIGH_ISSUES + high))
            fi
        fi
    else
        log_warn "gosec 扫描失败或有问题"
    fi
}

# ============================================================
# 生成报告
# ============================================================
generate_report() {
    log_section "生成安全报告"
    
    cat > "$REPORT_FILE" <<EOF
# 代码安全审查报告

**生成时间**: $(date '+%Y-%m-%d %H:%M:%S')
**项目**: glowing-tiger

## 📊 问题统计

| 级别 | 数量 |
|------|------|
| 🔴 HIGH | $HIGH_ISSUES |
| 🟡 MEDIUM | $MEDIUM_ISSUES |
| 🟢 LOW | $LOW_ISSUES |
| **总计** | **$TOTAL_ISSUES** |

## 📋 检查项

- ✅ SQL 注入防护
- ✅ 路径穿越防护
- ✅ 敏感信息泄露
- ✅ 加密实现
- ✅ 命令注入
- ✅ 随机数生成
- ✅ 并发安全

---

## 🔍 详细问题

EOF
    
    log_info "报告已生成: $REPORT_FILE"
}

# ============================================================
# 主函数
# ============================================================
main() {
    log_info "================================================"
    log_info "  代码安全审查"
    log_info "================================================"
    log_info "项目根目录: $PROJECT_ROOT"
    echo ""
    
    # 运行所有检查
    check_sql_injection
    check_path_traversal
    check_sensitive_info
    check_crypto
    check_command_injection
    check_random
    check_concurrency
    run_gosec
    
    # 生成报告
    generate_report
    
    # 汇总结果
    echo ""
    log_info "================================================"
    log_info "  审查结果汇总"
    log_info "================================================"
    log_info "总问题数: $TOTAL_ISSUES"
    log_info "  🔴 HIGH: $HIGH_ISSUES"
    log_info "  🟡 MEDIUM: $MEDIUM_ISSUES"
    log_info "  🟢 LOW: $LOW_ISSUES"
    echo ""
    log_info "详细报告: $REPORT_FILE"
    
    if [[ $HIGH_ISSUES -gt 0 ]]; then
        echo ""
        log_error "❌ 发现 $HIGH_ISSUES 个 HIGH 级别安全问题，必须修复！"
        exit 1
    elif [[ $MEDIUM_ISSUES -gt 0 ]]; then
        echo ""
        log_warn "⚠️  发现 $MEDIUM_ISSUES 个 MEDIUM 级别问题，建议修复"
        exit 0
    else
        echo ""
        log_info "✅ 未发现严重安全问题"
        exit 0
    fi
}

main "$@"
