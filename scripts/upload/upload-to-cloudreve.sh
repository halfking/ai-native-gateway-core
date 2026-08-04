#!/usr/bin/env bash
# scripts/upload/upload-to-cloudreve.sh - 上传文件到 Cloudreve（v4 API + WebDAV）
# 用法：bash upload-to-cloudreve.sh <archive_file> <version>
#
# 说明：
#   生产 Cloudreve 为 v4.15.0，旧 v3 API（/api/v3/*）已全部 404，本脚本按 v4 重写。
#   上传走 WebDAV PUT（用 dav_account 明文密码 Basic auth，不依赖 admin 主密码）；
#   分享链接走 v4 API（PUT /api/v4/share，需 JWT，即 admin 主密码登录）。
#   若未提供 CLOUDREVE_PASSWORD，则只上传不生成分享链接。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Cloudreve 配置
CLOUDREVE_URL="${CLOUDREVE_URL:-https://files.kxpms.cn}"
CLOUDREVE_EMAIL="${CLOUDREVE_EMAIL:-admin@itestu.cn}"
# dav_account 明文密码（WebDAV 上传用，非 admin 主密码）
CLOUDREVE_DAV_PASSWORD="${CLOUDREVE_DAV_PASSWORD:-LLMOfflineDav@2026}"
# admin 主密码（分享链接用，可选）
CLOUDREVE_PASSWORD="${CLOUDREVE_PASSWORD:-}"

# 上传目标 WebDAV 路径（对应 dav_account uri=cloudreve://my/）
UPLOAD_WEBDAV_BASE="/dav/llm-gateway-go/releases"

ARCHIVE_FILE="${1:?Usage: $0 <archive_file> <version>}"
VERSION="${2:?Usage: $0 <archive_file> <version>}"

if [[ ! -f "$ARCHIVE_FILE" ]]; then
    echo "❌ 文件不存在: $ARCHIVE_FILE"
    exit 1
fi

UPLOAD_LOG="${PROJECT_ROOT}/build/logs/upload-$(date +%Y%m%d-%H%M%S).log"
mkdir -p "$(dirname "$UPLOAD_LOG")"

log() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" | tee -a "$UPLOAD_LOG"; }
log_ok() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ✅ $*" | tee -a "$UPLOAD_LOG"; }
log_err() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ❌ $*" | tee -a "$UPLOAD_LOG"; }

# ============================================================================
# WebDAV 上传
# ============================================================================

dav_auth() { echo "$CLOUDREVE_EMAIL:$CLOUDREVE_DAV_PASSWORD"; }

upload_via_webdav() {
    local src="$1" dst_path="$2"
    local filename; filename=$(basename "$src")
    local dir_path; dir_path=$(dirname "$dst_path")

    log "确保目录存在: $dir_path"
    mkcol_paths "$dir_path"

    log "WebDAV PUT: $dst_path (size=$(stat -f%z "$src" 2>/dev/null || stat -c%s "$src"))"
    local code
    code=$(curl -s -o /tmp/cr-upload-resp.txt -w "%{http_code}" -X PUT \
        "${CLOUDREVE_URL}${dst_path}" \
        -u "$(dav_auth)" \
        --data-binary @"$src" \
        --max-time 3600)
    if [[ "$code" == "201" ]] || [[ "$code" == "204" ]]; then
        log_ok "上传成功 HTTP=$code"
        return 0
    fi
    log_err "上传失败 HTTP=$code: $(cat /tmp/cr-upload-resp.txt 2>/dev/null)"
    return 1
}

mkcol_paths() {
    local dir="$1" rel=""
    [[ "$dir" == "/" ]] && return 0
    local base; base="/dav"
    local rest; rest="${dir#/dav}"
    [[ -n "$rest" ]] || return 0
    local IFS='/'
    for seg in $rest; do
        [[ -z "$seg" ]] && continue
        rel="$rel/$seg"
        local code
        code=$(curl -s -o /dev/null -w "%{http_code}" -X MKCOL "${CLOUDREVE_URL}${base}${rel}" -u "$(dav_auth)" --max-time 15)
        if [[ "$code" != "201" ]] && [[ "$code" != "405" ]] && [[ "$code" != "409" ]]; then
            log "MKCOL $rel → HTTP=$code"
        fi
    done
}

# ============================================================================
# v4 API 分享链接
# ============================================================================

login_v4() {
    local resp token
    resp=$(curl -s -X POST "${CLOUDREVE_URL}/api/v4/session/token" \
        -H "Content-Type: application/json" \
        -d "{\"email\":\"${CLOUDREVE_EMAIL}\",\"password\":\"${CLOUDREVE_PASSWORD}\"}" \
        --max-time 20)
    token=$(echo "$resp" | jq -r '.data.token.access_token // empty')
    if [[ -z "$token" ]]; then
        log_err "v4 登录失败: $(echo "$resp" | head -c 200)"
        return 1
    fi
    echo "$token"
}

create_share_v4() {
    local uri="$1" token
    token=$(login_v4) || return 1
    log "创建分享: $uri"
    local resp
    resp=$(curl -s -X PUT "${CLOUDREVE_URL}/api/v4/share" \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/json" \
        -d "{\"uri\":\"${uri}\",\"is_private\":false,\"expire\":0,\"downloads\":0}" \
        --max-time 20)
    local url
    url=$(echo "$resp" | jq -r '.data // empty')
    if [[ "$url" == "https://"* ]]; then
        log_ok "分享链接: $url"
        echo "$url"
        return 0
    fi
    log_err "创建分享失败: $resp"
    return 1
}

# ============================================================================
# 主流程
# ============================================================================

main() {
    local filename version
    filename=$(basename "$ARCHIVE_FILE")
    version="$VERSION"

    log "上传文件: $filename (version=$version)"
    log "文件大小: $(du -h "$ARCHIVE_FILE" | cut -f1)"

    local webdav_dir="${UPLOAD_WEBDAV_BASE}/${version}"
    local webdav_full="${webdav_dir}/${filename}"

    if ! upload_via_webdav "$ARCHIVE_FILE" "$webdav_full"; then
        log_err "上传失败"
        exit 1
    fi

    local share_url=""
    if [[ -n "$CLOUDREVE_PASSWORD" ]]; then
        # cloudreve URI: dav_account uri=cloudreve://my/ 前缀 + webdav 相对路径
        local fs_uri="cloudreve://my${webdav_dir}/${filename}"
        share_url=$(create_share_v4 "$fs_uri") || true
    else
        log "未提供 CLOUDREVE_PASSWORD，跳过分享链接生成"
    fi

    cat > "${PROJECT_ROOT}/build/logs/upload-record-${version}.json" << JSON
{
  "version": "$version",
  "file": "$filename",
  "webdav_path": "$webdav_full",
  "share_url": "$share_url",
  "uploaded_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "uploaded_by": "$(whoami)@$(hostname)",
  "file_size": $(stat -f%z "$ARCHIVE_FILE" 2>/dev/null || stat -c%s "$ARCHIVE_FILE")
}
JSON

    log_ok "上传完成"
    log_ok "WebDAV: ${CLOUDREVE_URL}${webdav_full}"
    [[ -n "$share_url" ]] && log_ok "下载: $share_url"
    echo ""
    echo "🎉 上传成功!"
    echo "   WebDAV: ${CLOUDREVE_URL}${webdav_full}"
    [[ -n "$share_url" ]] && echo "   下载: $share_url"
    echo ""
}

trap 'log_err "上传失败，查看日志: $UPLOAD_LOG"; exit 1' ERR

main
