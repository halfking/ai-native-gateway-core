#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/lifecycle/install-service.sh
# SYNC_POLICY: 修改本文件时同步改 maintain 对应位置。service identity 通过 --service-name / --service-user / --service-group 传参，
#              默认值在 llm-gateway-go (binary prefix / user / group 全部可覆盖)。
# ADAPTATIONS: 加 --service-name / --service-user / --service-group CLI 参数；默认值从 maintain 切到 llm-gateway-go。

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

release_dir=""
prefix=""
config=""
confirm_token=""
start_service=true
# service identity 默认值（与 deploy/systemd/llm-gateway-go.service / deploy/launchd/com.kaixuan.llm-gateway-go.plist 对齐）。
# 可通过 --service-name / --service-user / --service-group 覆盖。
service_name="${SERVICE_NAME:-llm-gateway-go}"
service_user="${SERVICE_USER:-llm-gateway}"
service_group="${SERVICE_GROUP:-llm-gateway}"
# BINARY_PREFIX 给 release_binary_name 用（默认 llm-gateway-go）。
: "${BINARY_PREFIX:=llm-gateway-go}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --release-dir) release_dir=${2:-}; shift 2 ;;
    --prefix) prefix=${2:-}; shift 2 ;;
    --config) config=${2:-}; shift 2 ;;
    --service-name) service_name=${2:-}; shift 2 ;;
    --service-user) service_user=${2:-}; shift 2 ;;
    --service-group) service_group=${2:-}; shift 2 ;;
    --no-start) start_service=false; shift ;;
    --confirm) confirm_token=${2:-}; shift 2 ;;
    -h|--help)
      cat <<EOF
Usage: install-service.sh --release-dir DIR --confirm INSTALL-SERVICE
                          [--prefix DIR] [--config FILE]
                          [--service-name NAME] [--service-user USER] [--service-group GROUP]
                          [--no-start]

Installs a packaged release and service definition. Linux defaults:
  prefix /opt/llm-gateway, config /etc/llm-gateway/gateway.env,
  logs in journald. macOS defaults: /usr/local/llm-gateway,
  /usr/local/etc/llm-gateway/gateway.env, logs under
  /usr/local/var/log/llm-gateway. Existing config is preserved.
EOF
      exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

require_value --release-dir "$release_dir"
release_dir=$(absolute_path "$release_dir")
[[ -d "$release_dir" ]] || die "release directory not found: $release_dir"
platform=$(service_platform)
case "$platform" in
  linux)
    prefix=${prefix:-/opt/llm-gateway}
    config=${config:-/etc/llm-gateway/gateway.env}
    need_cmd systemctl
    [[ $(id -u) -eq 0 ]] || die "systemd installation requires root"
    if ! getent group "$service_group" >/dev/null 2>&1; then groupadd --system "$service_group"; fi
    if ! id "$service_user" >/dev/null 2>&1; then useradd --system --gid "$service_group" --home-dir "/var/lib/$service_user" --shell /usr/sbin/nologin "$service_user"; fi
    ;;
  darwin)
    prefix=${prefix:-/usr/local/llm-gateway}
    config=${config:-/usr/local/etc/llm-gateway/gateway.env}
    need_cmd launchctl
    [[ $(id -u) -eq 0 ]] || die "launchd installation requires root"
    ;;
  windows)
    # Windows 走 scripts/deploy/windows/install-service.ps1，本脚本不实现。
    die "use scripts/deploy/windows/install-service.ps1 on Windows"
    ;;
esac
confirm "INSTALL-SERVICE" "Install $service_name from $release_dir into $prefix and register its service." "$confirm_token"

binary_name=$(release_binary_name "$platform")
binary=$(find "$release_dir/bin" -maxdepth 1 -type f -name "$binary_name" -print -quit 2>/dev/null || true)
[[ -n "$binary" ]] || die "release directory must contain bin/$binary_name"
mkdir -p "$prefix/releases" "$(dirname "$config")"
version=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$release_dir/version.json")
destination="$prefix/releases/$version"
[[ ! -e "$destination" ]] || die "release is already installed: $destination"
cp -R "$release_dir" "$destination"
# 把找到的 binary 改名为规范名（gateway）。
cp -p "$binary" "$destination/bin/gateway"
chmod 0755 "$destination/bin/gateway" "$destination/scripts/lifecycle/service-runner.sh"
ln -sfn "$destination" "$prefix/current"

if [[ ! -e "$config" ]]; then
  install -m 0600 "$destination/config/gateway.env.example" "$config"
  warn "created config template at $config; populate secrets before starting the service"
  start_service=false
else
  chmod 0600 "$config"
fi
if [[ "$platform" == linux ]]; then
  chown "root:$service_group" "$config"
  install -d -o "$service_user" -g "$service_group" -m 0750 "/var/lib/$service_user" "/var/log/$service_user"
fi

if [[ "$platform" == linux ]]; then
  unit="/etc/systemd/system/${service_name}.service"
  sed -e "s|@PREFIX@|$prefix|g" \
      -e "s|@CONFIG_FILE@|$config|g" \
      -e "s|@SERVICE_USER@|$service_user|g" \
      -e "s|@SERVICE_GROUP@|$service_group|g" \
      -e "s|@SERVICE_NAME@|$service_name|g" \
    "$destination/deploy/systemd/${service_name}.service" > "$unit"
  chmod 0644 "$unit"
  systemctl daemon-reload
  systemctl enable "${service_name}.service"
  if [[ "$start_service" == true ]]; then systemctl restart "${service_name}.service"; fi
else
  log_dir="/usr/local/var/log/$service_user"
  mkdir -p "$log_dir"
  plist="/Library/LaunchDaemons/${service_name}.plist"
  sed -e "s|@PREFIX@|$prefix|g" \
      -e "s|@CONFIG_FILE@|$config|g" \
      -e "s|@LOG_DIR@|$log_dir|g" \
      -e "s|@SERVICE_NAME@|$service_name|g" \
    "$destination/deploy/launchd/${service_name}.plist" > "$plist"
  chown root:wheel "$plist"
  chmod 0644 "$plist"
  launchctl bootout "system/${service_name}" >/dev/null 2>&1 || true
  if [[ "$start_service" == true ]]; then launchctl bootstrap system "$plist"; fi
fi

log "installed release $version; config=$config; current=$prefix/current"
