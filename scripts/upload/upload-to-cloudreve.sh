#!/usr/bin/env bash
# scripts/upload/upload-to-cloudreve.sh - 上传文件到 Cloudreve
# 用法：bash upload-to-cloudreve.sh <archive_file> <version>

set -euo pipefail

# ============================================================================
# 配置
# ============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Cloudreve 配置
CLOUDREVE_URL="https://files.kxpms.cn"
CLOUDREVE_EMAIL="56551681@qq.com"
CLOUDREVE_PASSWORD="${CLOUDREVE_PASSWORD:-Veritrans&9527}"

# 上传目标目录
UPLOAD_BASE_PATH="/llm-gateway-go/releases"

# 参数
ARCHIVE_FILE="${1:?Usage: $0 <archive_file> <version>}"
VERSION="${2:?Usage: $0 <archive_file> <version>}"

if [[ ! -f "$ARCHIVE_FILE" ]]; then
    echo "❌ 文件不存在: $ARCHIVE_FILE"
    exit 1
fi

# 日志
UPLOAD_LOG="${PROJECT_ROOT}/build/logs/upload-$(date +%Y%m%d-%H%M%S).log"
mkdir -p "$(dirname "$UPLOAD_LOG")"

# ============================================================================
# 日志函数
# ============================================================================

log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" | tee -a "$UPLOAD_LOG"
}

log_success() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ✅ $*" | tee -a "$UPLOAD_LOG"
}

log_error() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ❌ $*" | tee -a "$UPLOAD_LOG"
}

log_step() {
    echo ""
    echo "=========================================" | tee -a "$UPLOAD_LOG"
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] 📤 $*" | tee -a "$UPLOAD_LOG"
    echo "=========================================" | tee -a "$UPLOAD_LOG"
}

# ============================================================================
# 会话管理
# ============================================================================

SESSION_FILE="/tmp/cloudreve_session.txt"

# 登录 Cloudreve
login_cloudreve() {
    log_step "步骤 1: 登录 Cloudreve"
    
    local login_response=$(curl -s -X POST "${CLOUDREVE_URL}/api/v3/user/session" \
        -H "Content-Type: application/json" \
        -d "{\"userName\":\"${CLOUDREVE_EMAIL}\",\"Password\":\"${CLOUDREVE_PASSWORD}\"}")
    
    # 提取 token
    local token=$(echo "$login_response" | jq -r '.data.token // empty')
    
    if [[ -z "$token" ]]; then
        log_error "登录失败"
        echo "$login_response" | jq '.' | tee -a "$UPLOAD_LOG"
        exit 1
    fi
    
    echo "$token" > "$SESSION_FILE"
    log_success "登录成功"
}

# 获取 token
get_token() {
    if [[ ! -f "$SESSION_FILE" ]]; then
        login_cloudreve
    fi
    cat "$SESSION_FILE"
}

# ============================================================================
# 目录管理
# ============================================================================

# 创建目录（如果不存在）
create_directory() {
    local dir_path="$1"
    local token=$(get_token)
    
    log "创建目录: $dir_path"
    
    # 获取父目录 ID
    local parent_path=$(dirname "$dir_path")
    local dir_name=$(basename "$dir_path")
    
    # 创建目录请求
    local create_response=$(curl -s -X PUT "${CLOUDREVE_URL}/api/v3/directory" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"path\":\"${parent_path}\",\"name\":\"${dir_name}\"}")
    
    local code=$(echo "$create_response" | jq -r '.code // 0')
    
    if [[ "$code" -eq 0 ]] || [[ "$code" -eq 40004 ]]; then
        # 40004 = 目录已存在
        log_success "目录准备完成: $dir_path"
        return 0
    else
        log_error "创建目录失败: $dir_path"
        echo "$create_response" | jq '.' | tee -a "$UPLOAD_LOG"
        return 1
    fi
}

# ============================================================================
# 文件上传
# ============================================================================

# 获取上传策略
get_upload_policy() {
    local file_path="$1"
    local upload_path="$2"
    local token=$(get_token)
    
    log "获取上传策略..."
    
    local filename=$(basename "$file_path")
    local filesize=$(stat -f%z "$file_path" 2>/dev/null || stat -c%s "$file_path" 2>/dev/null)
    
    local policy_response=$(curl -s -X POST "${CLOUDREVE_URL}/api/v3/file/upload" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"path\":\"${upload_path}\",\"size\":${filesize},\"name\":\"${filename}\"}")
    
    local code=$(echo "$policy_response" | jq -r '.code // -1')
    
    if [[ "$code" -ne 0 ]]; then
        log_error "获取上传策略失败"
        echo "$policy_response" | jq '.' | tee -a "$UPLOAD_LOG"
        return 1
    fi
    
    echo "$policy_response" | jq -r '.data'
}

# 执行上传
upload_file() {
    local file_path="$1"
    local upload_path="$2"
    
    log_step "步骤 2: 上传文件"
    
    # 创建目标目录
    create_directory "$upload_path"
    
    # 获取上传策略
    local policy=$(get_upload_policy "$file_path" "$upload_path")
    
    if [[ -z "$policy" ]]; then
        log_error "无法获取上传策略"
        return 1
    fi
    
    # 解析策略
    local upload_url=$(echo "$policy" | jq -r '.uploadURL')
    local session_id=$(echo "$policy" | jq -r '.sessionID')
    
    log "上传URL: $upload_url"
    log "Session ID: $session_id"
    
    # 上传文件
    log "开始上传: $(basename "$file_path")"
    
    local upload_response=$(curl -s -X POST "$upload_url" \
        -F "file=@${file_path}" \
        -F "policy=${session_id}")
    
    local code=$(echo "$upload_response" | jq -r '.code // -1')
    
    if [[ "$code" -eq 0 ]]; then
        log_success "上传成功"
        echo "$upload_response" | jq -r '.data'
        return 0
    else
        log_error "上传失败"
        echo "$upload_response" | jq '.' | tee -a "$UPLOAD_LOG"
        return 1
    fi
}

# ============================================================================
# 分享链接生成
# ============================================================================

# 创建分享链接
create_share_link() {
    local file_path="$1"
    local token=$(get_token)
    
    log_step "步骤 3: 生成分享链接"
    
    local share_response=$(curl -s -X POST "${CLOUDREVE_URL}/api/v3/share" \
        -H "Authorization: Bearer ${token}" \
        -H "Content-Type: application/json" \
        -d "{\"path\":\"${file_path}\",\"is_dir\":false,\"password\":\"\",\"expire\":0}")
    
    local code=$(echo "$share_response" | jq -r '.code // -1')
    
    if [[ "$code" -eq 0 ]]; then
        local share_url=$(echo "$share_response" | jq -r '.data.url')
        log_success "分享链接: $share_url"
        echo "$share_url"
        return 0
    else
        log_error "创建分享链接失败"
        echo "$share_response" | jq '.' | tee -a "$UPLOAD_LOG"
        return 1
    fi
}

# ============================================================================
# 主流程
# ============================================================================

main() {
    log_step "开始上传到 Cloudreve"
    
    log "文件: $(basename "$ARCHIVE_FILE")"
    log "版本: $VERSION"
    log "大小: $(du -h "$ARCHIVE_FILE" | cut -f1)"
    
    # 登录
    login_cloudreve
    
    # 构建上传路径
    local upload_path="${UPLOAD_BASE_PATH}/${VERSION}"
    local filename=$(basename "$ARCHIVE_FILE")
    
    # 上传文件
    local upload_result=$(upload_file "$ARCHIVE_FILE" "$upload_path")
    
    if [[ $? -ne 0 ]]; then
        log_error "上传失败"
        exit 1
    fi
    
    # 生成分享链接
    local full_path="${upload_path}/${filename}"
    local share_url=$(create_share_link "$full_path")
    
    # 生成上传记录
    cat > "${PROJECT_ROOT}/build/logs/upload-record-${VERSION}.json" << JSON
{
  "version": "$VERSION",
  "file": "$filename",
  "upload_path": "$full_path",
  "share_url": "$share_url",
  "uploaded_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "uploaded_by": "$(whoami)@$(hostname)",
  "file_size": $(stat -f%z "$ARCHIVE_FILE" 2>/dev/null || stat -c%s "$ARCHIVE_FILE" 2>/dev/null)
}
JSON
    
    log_step "上传完成"
    log_success "文件: $filename"
    log_success "路径: $full_path"
    log_success "分享链接: $share_url"
    log_success "日志: $UPLOAD_LOG"
    
    echo ""
    echo "🎉 上传成功!"
    echo "   文件: $filename"
    echo "   下载: $share_url"
    echo ""
}

# 错误处理
trap 'log_error "上传失败，查看日志: $UPLOAD_LOG"; exit 1' ERR

# 执行主流程
main

