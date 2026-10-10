#!/usr/bin/env bash
# Caddy HTTPS termination for llm-gateway-go local deployments.
#
# ## 目标
#
# 把 NetBird 解析到本机 peer 的 `llm.itestu.cn`（100.106.169.63）终结为 HTTPS，
# 反向代理到本地网关监听端口（默认 :8782）。整条链与既有 deploy-local.sh
# 完全解耦——deploy-local.sh 只管业务网关与 PG/Redis，Caddy 是 HTTP 边缘。
#
# ## 为什么自签名 CA + 服务端证书（而不是 Caddy ACME / Let's Encrypt）
#
# 1. 本机 peer（100.106.169.63）在公网上没有 80 端口，ACME http-01 走不通；
# 2. 域名 `llm.itestu.cn` 是 NetBird MagicDNS，CA 不会对它发证书；
# 3. 客户端是同一枚举的受控端 + 桌面浏览器，**把自签名 CA 推到 peer 的系统
#    trust store** 即可等价于公网证书的体验。
#
# ## 依赖
#
# - `curl`（下载 Caddy 二进制；不可用时直接 fail）
# - `openssl`（生成 CA + 服务端证书）
# - macOS / Linux 通用；其他 OS 留扩展点但不主动支持
#
# ## 输出目录（macOS 默认 / Linux 默认）
#
# | 路径 | 用途 |
# |---|---|
# | `~/sdk/caddy/caddy` | 二进制（不存在则下载） |
# | `~/kaixuan/caddy/Caddyfile` | 主配置（HTTP→HTTPS + reverse_proxy） |
# | `~/kaixuan/caddy/pki/ca.crt` + `ca.key` | 自签名 CA（3650 天） |
# | `~/kaixuan/caddy/pki/<domain>.{crt,key}` | 服务端证书（825 天，SAN=domain） |
# | `~/kaixuan/caddy/run/caddy.pid` | Caddy 自写 pidfile（caddy run --pidfile-file） |
# | `~/kaixuan/caddy/logs/caddy.log` | Caddy stdout/stderr |
#
# ## 用法
#
#     bash scripts/setup-caddy.sh                # install + configure + start
#     bash scripts/setup-caddy.sh --stop         # stop running instance
#     bash scripts/setup-caddy.sh --status       # pid + healthz + listening ports
#     bash scripts/setup-caddy.sh --dry-run      # print plan only (used by contract tests)
#     bash scripts/setup-caddy.sh --gen-cert-only <dir>   # generate certs to <dir> and exit
#     DL_DOMAIN=foo.example bash scripts/setup-caddy.sh   # override served hostname
#     DL_UPSTREAM=127.0.0.1:9000 bash scripts/setup-caddy.sh   # override backend
#     DL_HTTPS_PORT=8443 bash scripts/setup-caddy.sh     # non-privileged port (default 8443)
#
set -euo pipefail

# ───────────────────────────── paths ─────────────────────────────
DL_DOMAIN="${DL_DOMAIN:-llm.itestu.cn}"
DL_UPSTREAM="${DL_UPSTREAM:-127.0.0.1:8782}"
DL_HTTPS_PORT="${DL_HTTPS_PORT:-8443}"
DL_HTTP_PORT="${DL_HTTP_PORT:-8080}"
DL_CADDY_VERSION="${DL_CADDY_VERSION:-2.11.7}"
DL_CADDY_HOME="${DL_CADDY_HOME:-$HOME/sdk/caddy}"
DL_DEPLOY_ROOT="${DL_DEPLOY_ROOT:-$HOME/kaixuan}"
DL_CADDY_ROOT="${DL_CADDY_ROOT:-$DL_DEPLOY_ROOT/caddy}"
DL_PKI_DIR="$DL_CADDY_ROOT/pki"
DL_RUN_DIR="$DL_CADDY_ROOT/run"
DL_LOG_DIR="$DL_CADDY_ROOT/logs"
DL_CADDYFILE="$DL_CADDY_ROOT/Caddyfile"
DL_CA_CRT="$DL_PKI_DIR/ca.crt"
DL_CA_KEY="$DL_PKI_DIR/ca.key"
DL_SRV_CRT="$DL_PKI_DIR/${DL_DOMAIN}.crt"
DL_SRV_KEY="$DL_PKI_DIR/${DL_DOMAIN}.key"

# ───────────────────────────── io ─────────────────────────────
log() { printf '[setup-caddy] %s\n' "$*" >&2; }
warn() { printf '[setup-caddy] warning: %s\n' "$*" >&2; }
die() { printf '[setup-caddy] error: %s\n' "$*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

ensure_layout() {
  mkdir -p "$DL_CADDY_HOME" "$DL_CADDY_ROOT" "$DL_PKI_DIR" "$DL_RUN_DIR" "$DL_LOG_DIR"
}

# ───────────────────────────── install binary ─────────────────────────────
install_caddy_binary() {
  local arch os
  arch=$(uname -m)
  os=$(uname -s)
  case "$os" in
    Darwin) os="mac" ;;
    Linux)  os="linux" ;;
    *) die "unsupported OS: $os" ;;
  esac
  case "$arch" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64)  arch="amd64" ;;
    *) die "unsupported arch: $arch" ;;
  esac
  local url="https://github.com/caddyserver/caddy/releases/download/v${DL_CADDY_VERSION}/caddy_${DL_CADDY_VERSION}_${os}_${arch}.tar.gz"
  local tmp
  tmp=$(mktemp -d -t caddy-dl.XXXXXX)
  log "downloading Caddy ${DL_CADDY_VERSION} for ${os}/${arch}"
  curl -fsSL --retry 3 -o "$tmp/caddy.tgz" "$url" || die "download failed: $url"
  tar -xzf "$tmp/caddy.tgz" -C "$tmp" caddy || die "extract failed"
  install -m 0755 "$tmp/caddy" "$DL_CADDY_HOME/caddy"
  rm -rf "$tmp"
  "$DL_CADDY_HOME/caddy" version || die "binary sanity check failed"
}

ensure_binary() {
  if [[ -x "$DL_CADDY_HOME/caddy" ]]; then
    log "caddy already at $DL_CADDY_HOME/caddy ($("$DL_CADDY_HOME/caddy" version | head -1))"
    return 0
  fi
  need_cmd curl
  install_caddy_binary
}

# ───────────────────────────── cert generation ─────────────────────────────
# gen_pki <pki_dir> <domain>
#   <pki_dir>/ca.crt + ca.key       (self-signed CA, 3650d)
#   <pki_dir>/<domain>.crt + .key   (server cert signed by CA, with SAN, 825d)
# Pure openssl, no mkcert / cfssl / Caddy dependency.
gen_pki() {
  local pki="$1" domain="$2"
  local ca_crt="$pki/ca.crt" ca_key="$pki/ca.key"
  local srv_crt="$pki/${domain}.crt" srv_key="$pki/${domain}.key"
  local csr="$pki/${domain}.csr" cfg="$pki/${domain}.cnf" ext="$pki/${domain}.v3.ext"
  mkdir -p "$pki"

  if [[ -f "$ca_crt" && -f "$ca_key" && -f "$srv_crt" && -f "$srv_key" ]]; then
    log "PKI already present at $pki"
    return 0
  fi

  log "generating CA at $ca_crt"
  openssl genrsa -out "$ca_key" 4096 2>/dev/null
  openssl req -x509 -new -nodes -key "$ca_key" -sha256 -days 3650 \
    -subj "/CN=llm-gateway-local-ca" \
    -out "$ca_crt"

  log "generating server cert for $domain (SAN=$domain)"
  openssl genrsa -out "$srv_key" 2048 2>/dev/null
  cat > "$cfg" <<EOF
[ req ]
distinguished_name = dn
prompt = no
[ dn ]
CN = ${domain}
[ v3_req ]
subjectAltName = @alt_names
[ alt_names ]
DNS.1 = ${domain}
EOF
  cat > "$ext" <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
subjectAltName = @alt_names
[ alt_names ]
DNS.1 = ${domain}
EOF
  openssl req -new -key "$srv_key" -out "$csr" -config "$cfg"
  openssl x509 -req -in "$csr" -CA "$ca_crt" -CAkey "$ca_key" -CAcreateserial \
    -out "$srv_crt" -days 825 -sha256 -extfile "$ext"
  rm -f "$csr" "$cfg" "$ext" "$pki/ca.srl"
  chmod 0600 "$ca_key" "$srv_key"
  chmod 0644 "$ca_crt" "$srv_crt"
  log "cert chain written:"
  log "    CA:    $ca_crt"
  log "    CERT:  $srv_crt"
  log "    KEY:   $srv_key"
}

# ───────────────────────────── Caddyfile ─────────────────────────────
# We do NOT use Caddy's internal `tls` directive (which would require ACME or
# trust the Caddy-trusted CA pool). Instead, point tls at the local PEM files
# we just generated. Caddy will refuse to start if it cannot load both files.
write_caddyfile() {
  cat > "$DL_CADDYFILE" <<EOF
# Auto-generated by scripts/setup-caddy.sh — do not edit by hand; rerun the
# script to regenerate. Domain=${DL_DOMAIN} upstream=${DL_UPSTREAM}

# ── HTTP → HTTPS redirect (sane default) ─────────────────────────────
:${DL_HTTP_PORT} {
    redir https://{host}{uri} permanent
}

# ── HTTPS reverse proxy to local gateway ─────────────────────────────
:${DL_HTTPS_PORT} {
    tls $DL_SRV_CRT $DL_SRV_KEY
    encode zstd gzip
    reverse_proxy $DL_UPSTREAM {
        header_up X-Forwarded-Proto https
        header_up X-Forwarded-Host {host}
        header_up X-Real-IP {remote_host}
    }
}
EOF
  log "wrote $DL_CADDYFILE"
}

# ───────────────────────────── lifecycle ─────────────────────────────
caddy_pid() {
  [[ -f "$DL_RUN_DIR/caddy.pid" ]] && cat "$DL_RUN_DIR/caddy.pid" 2>/dev/null || true
}

is_running() {
  local pid; pid=$(caddy_pid)
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

start_caddy() {
  if is_running; then
    log "caddy already running (pid=$(caddy_pid))"
    return 0
  fi
  log "starting caddy (https=${DL_HTTPS_PORT} http=${DL_HTTP_PORT} upstream=${DL_UPSTREAM})"
  # caddy run accepts --pidfile (writes its pid); we redirect output for ops triage
  nohup "$DL_CADDY_HOME/caddy" run \
      --config "$DL_CADDYFILE" \
      --pidfile "$DL_RUN_DIR/caddy.pid" \
      >"$DL_LOG_DIR/caddy.log" 2>&1 &
  # Wait briefly for startup so caller can probe immediately
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.2
    if is_running; then
      log "caddy started (pid=$(caddy_pid))"
      return 0
    fi
  done
  warn "caddy did not become ready in 2s; tail of log:"
  tail -20 "$DL_LOG_DIR/caddy.log" >&2 || true
  die "caddy failed to start"
}

stop_caddy() {
  if ! is_running; then
    log "caddy not running"
    return 0
  fi
  local pid; pid=$(caddy_pid)
  log "stopping pid=$pid"
  kill -TERM "$pid" 2>/dev/null || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.2
    kill -0 "$pid" 2>/dev/null || break
  done
  if kill -0 "$pid" 2>/dev/null; then
    warn "graceful stop timed out; SIGKILL"
    kill -KILL "$pid" 2>/dev/null || true
  fi
  rm -f "$DL_RUN_DIR/caddy.pid"
}

print_status() {
  local pid; pid=$(caddy_pid)
  if is_running; then
    echo "caddy: running (pid=$pid)"
  else
    echo "caddy: stopped"
    return 0
  fi
  "$DL_CADDY_HOME/caddy" version 2>/dev/null || true
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR==1 || $1=="caddy"' || true
  fi
  echo "upstream: $DL_UPSTREAM"
  echo "domain:   $DL_DOMAIN"
  echo "config:   $DL_CADDYFILE"
}

print_plan() {
  cat <<EOF
[setup-caddy] dry-run plan
  binary:    $DL_CADDY_HOME/caddy  (version=${DL_CADDY_VERSION})
  config:    $DL_CADDYFILE
  pki:       $DL_PKI_DIR
  run:       $DL_RUN_DIR
  logs:      $DL_LOG_DIR
  domain:    $DL_DOMAIN
  upstream:  $DL_UPSTREAM
  http:      :${DL_HTTP_PORT}  (redirect → https)
  https:     :${DL_HTTPS_PORT}
EOF
}

# ───────────────────────────── main flow ─────────────────────────────
do_all() {
  ensure_layout
  ensure_binary
  gen_pki "$DL_PKI_DIR" "$DL_DOMAIN"
  write_caddyfile
  start_caddy
  print_status
}

# dispatch
case "${1:-all}" in
  all|--all)             shift || true; do_all "$@" ;;
  --stop)                ensure_layout; stop_caddy ;;
  --status)              ensure_layout; print_status ;;
  --dry-run)             print_plan ;;
  --gen-cert-only)
    dir="${2:?usage: --gen-cert-only <dir>}"
    mkdir -p "$dir"
    gen_pki "$dir" "$DL_DOMAIN"
    ;;
  -h|--help|help)        sed -n '2,55p' "$0"; exit 0 ;;
  *) die "unknown command: $1 (--help for usage)" ;;
esac