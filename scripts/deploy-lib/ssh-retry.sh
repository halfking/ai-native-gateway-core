#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/ssh-retry.sh — SSH 命令重试 + 连接复用
#
# 解决两个部署痛点:
#   1. SSH 失败率高 (154 公网 IP 偶发抖动) — 加指数退避重试
#   2. 每次 ssh 调用都新建连接 (TCP 握手 + 认证 ~150-300ms) —
#      用 SSH ControlMaster 复用, 全程只握手一次
#
# 用法 (源 scripts/deploy-seamless.sh):
#   source scripts/deploy-lib/ssh-retry.sh
#   ssh_retry_init 245                       # 初始化 (建 master socket)
#   ssh_run 245 "systemctl restart foo"      # 走 master, 自动重试
#   ssh_run_pipe 245 "tar xzf - -C /opt"     # 管道版本
#   ssh_retry_close 245                      # 关闭 master
#
# 向后兼容:
#   remote_ssh / remote_ssh_pipe 仍然可作为函数名使用,
#   内部直接走 ssh_run / ssh_run_pipe. host.sh 的 "$ssh_cmd" "cmd"
#   调用风格不变.
#
# 设计要点:
#   - Master socket 路径: $TMPDIR/ssh-retry-${TARGET}-${PID}.sock
#     跟随脚本 PID 自动清理, 不会跨进程污染
#   - 重试策略: 最多 SSH_RETRY_MAX 次 (默认 3), 间隔 2s/4s/8s
#     仅重试「传输层」失败 (timeout / connection reset / EOF),
#     命令非 0 退出立即返回 (命令级错误不应重试)
#   - SSH options 集中维护, 与原 remote_ssh 保持一致
#   - stdout/stderr 全程透传, 不被装饰; 日志通过 _ssh_log 输出
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "ssh-retry.sh: requires bash" >&2
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317  # 由调用者 set -euo pipefail

: "${SSH_RETRY_MAX:=3}"        # 总尝试次数 (含首次)
: "${SSH_RETRY_BACKOFF:=2}"    # 首次退避秒数, 后续 ×2
: "${SSH_RETRY_TIMEOUT:=20}"   # 单次 SSH ConnectTimeout (秒)
: "${SSH_RETRY_KEEPALIVE:=5}"  # ServerAliveInterval
: "${SSH_RETRY_KEEPALIVE_MAX:=3}"
: "${SSH_RETRY_FALLBACK_AFTER:=0}"  # 154 默认立即使用跳板机（0=不先尝试直连）
                                     # 设为 2 可恢复"先直连 2 次再跳板"行为

# 调试: export SSH_RETRY_VERBOSE=1 可看到每次重试
: "${SSH_RETRY_VERBOSE:=0}"

# ── 状态: master sockets map (target -> socket_path) ─────────────
_SSH_RETRY_SOCKETS=()

# ── 内部: 取 SSH 选项 (-o ... 以数组形式传递) ───────────────────────
_ssh_retry_ssh_opts_for() {
  local target=$1
  local port=${SSH_PORT:-25022}
  local host
  host=$(target_field "$target" ssh_host 2>/dev/null || echo "")
  if [[ -z "$host" ]]; then
    err "ssh-retry: unknown target $target"
    return 1
  fi
  SSH_RETRY_OPTS=(
    -p "$port"
    -o StrictHostKeyChecking=accept-new
    -o BatchMode=yes
    -o "ConnectTimeout=${SSH_RETRY_TIMEOUT}"
    -o "ServerAliveInterval=${SSH_RETRY_KEEPALIVE}"
    -o "ServerAliveCountMax=${SSH_RETRY_KEEPALIVE_MAX}"
  )
}

# ── 内部: 取 SSH key flag (-i KEY) ────────────────────────────────
_ssh_retry_key_args() {
  local target=$1
  local key=${SSH_KEY_FILE:-}
  if [[ -z "$key" ]]; then
    case "$target" in
      245) key=${SSH_KEY_245:-} ;;
      154) key=${SSH_KEY_154:-} ;;
      252) key=${SSH_KEY_252:-} ;;
    esac
  fi
  if [[ -n "$key" && -f "$key" ]]; then
    SSH_RETRY_KEY_ARGS=(-i "$key")
    return 0
  fi
  echo "ssh-retry: injected SSH key missing for target $target" >&2
  return 1
}

# ── 内部: 分类 SSH 错误 — 是否可重试 ─────────────────────────────
# 退出码 + 输出关键字 -> "retry" or "fail"
_ssh_retry_classify() {
  local exit_code=$1
  local output=$2
  # 0: 成功
  if [[ "$exit_code" -eq 0 ]]; then
    printf 'ok'
    return
  fi
  # 255: ssh 自身失败 (传输层). 检查关键字.
  if [[ "$exit_code" -eq 255 ]]; then
    if [[ "$output" =~ (timed out|Connection reset|Connection closed|Operation timed out|No route to host|Connection refused|connection unexpectedly closed|ssh_exchange_identification) ]]; then
      printf 'retry'
      return
    fi
    # auth / host key 等不应该重试
    printf 'fail'
    return
  fi
  # 命令退出码 1-254: 远端命令失败, 不重试
  printf 'fail'
}

# ── 内部: 该 target 是否有 fallback 路径 ────────────────────────
# 154 公网 IP 抖动, 默认通过 252 (115.29.212.252) 跳板机中转最稳.
# 返回: 空字符串 (无 fallback) 或 "user@hop_host"
#
# 环境变量控制:
#   SSH_RETRY_DIRECT_MODE=1  强制直连（跳过跳板机，用于应急场景）
#   SSH_RETRY_FALLBACK_AFTER=0  立即使用跳板机（不先尝试直连）
_ssh_retry_fallback_hop() {
  local target=$1
  # 直连模式：跳过跳板机
  if [[ "${SSH_RETRY_DIRECT_MODE:-0}" == "1" ]]; then
    printf ''
    return
  fi
  case "$target" in
    154) printf 'root@115.29.212.252' ;;
    *)   printf '' ;;
  esac
}

# ── ssh_retry_init <target> ─────────────────────────────────────
# 建 master socket. 后续 ssh_run 复用此 socket.
# 必须先 source targets.sh 才能解析 target -> host.
ssh_retry_init() {
  local target=$1
  local sock_dir sock_path
  sock_dir="${TMPDIR:-/tmp}/ssh-retry-$$"
  mkdir -p "$sock_dir"
  sock_path="$sock_dir/cm-${target}.sock"
  _SSH_RETRY_SOCKETS[$target]=$sock_path
  if [[ "$SSH_RETRY_VERBOSE" == "1" ]]; then
    echo "[ssh-retry] init target=$target socket=$sock_path" >&2
  fi
  return 0
}

# ── ssh_retry_close <target> ────────────────────────────────────
# 显式关闭 master socket. 通常脚本结尾会自动清理 (trap).
ssh_retry_close() {
  local target=$1
  local sock_path=${_SSH_RETRY_SOCKETS[$target]:-}
  [[ -z "$sock_path" ]] && return 0
  if [[ -S "$sock_path" ]]; then
    ssh -o ControlPath="$sock_path" -O exit "${SSH_HOST_CACHE[$target]:-dummy}" >/dev/null 2>&1 || true
  fi
  unset '_SSH_RETRY_SOCKETS[$target]'
}

# ── ssh_retry_close_all ─────────────────────────────────────────
# 关闭所有 master, 通常 trap 调用.
ssh_retry_close_all() {
  local t
  for t in "${!_SSH_RETRY_SOCKETS[@]}"; do
    ssh_retry_close "$t"
  done
  local sock_dir="${TMPDIR:-/tmp}/ssh-retry-$$"
  rm -rf "$sock_dir" 2>/dev/null || true
}

# ── ssh_run <target> <command...> ───────────────────────────────
# 等价于原 remote_ssh: 在 target 上执行 shell 命令.
# 自动重试, 走 ControlMaster.
# 连续失败 SSH_RETRY_FALLBACK_AFTER 次后, 切到 ProxyCommand via 中转.
ssh_run() {
  local target=$1
  shift
  local script="$*"

  # 缓存 host 以避免重复查询
  local host=${SSH_HOST_CACHE[$target]:-}
  if [[ -z "$host" ]]; then
    host=$(target_field "$target" ssh_host)
    SSH_HOST_CACHE[$target]=$host
  fi
  local sock=${_SSH_RETRY_SOCKETS[$target]:-}
  if [[ -z "$sock" ]]; then
    ssh_retry_init "$target"
    sock=${_SSH_RETRY_SOCKETS[$target]}
  fi

  _ssh_retry_key_args "$target"
  _ssh_retry_ssh_opts_for "$target"

  local hop=$(_ssh_retry_fallback_hop "$target")
  local proxy_flag=""
  if [[ -n "$hop" ]]; then
    # 中转通过 hop: 用 ssh -W host:port 把 154 流量转发进 252 隧道
    local port=${SSH_PORT:-25022}
    proxy_flag="-o ProxyCommand=ssh -W ${host#*@}:${port} $hop"
    # ProxyCommand 会复用 hop 的 SSH 配置, 用同一个 key
  fi

  local attempt=0 max=$SSH_RETRY_MAX backoff=$SSH_RETRY_BACKOFF
  local exit_code="" output="" classify="" used_proxy=false
  while (( attempt < max )); do
    attempt=$((attempt+1))
    local -a extra_flags=()
    if [[ "$used_proxy" == "true" ]]; then
      # ssh expects -o and its ProxyCommand value as separate argv entries.
      extra_flags=(-o "${proxy_flag#-o }")
    fi
    output=$(ssh -o ControlMaster=auto -o ControlPath="$sock" -o ControlPersist=600 \
              "${SSH_RETRY_KEY_ARGS[@]}" "${SSH_RETRY_OPTS[@]}" "${extra_flags[@]}" "$host" "$script" 2>&1)
    exit_code=$?
    classify=$(_ssh_retry_classify "$exit_code" "$output")
    case "$classify" in
      ok)
        printf '%s' "$output"
        return 0
        ;;
      retry)
        # 失败累计到阈值且有 fallback: 切到 ProxyCommand 路径
        if [[ -n "$hop" ]] && [[ "$used_proxy" != "true" ]] \
            && (( attempt >= SSH_RETRY_FALLBACK_AFTER )); then
          used_proxy=true
          if [[ "$SSH_RETRY_VERBOSE" == "1" ]]; then
            echo "[ssh-retry] target=$target attempt=$attempt 直连失败, 切到 ProxyCommand via $hop" >&2
          fi
          # 不退避, 立即重试
          continue
        fi
        if (( attempt < max )); then
          if [[ "$SSH_RETRY_VERBOSE" == "1" ]]; then
            echo "[ssh-retry] target=$target attempt=$attempt/$max rc=$exit_code retry in ${backoff}s (proxy=$used_proxy)" >&2
          fi
          sleep "$backoff"
          backoff=$((backoff * 2))
          continue
        fi
        ;;
      fail)
        printf '%s' "$output"
        return "$exit_code"
        ;;
    esac
  done

  if [[ "$SSH_RETRY_VERBOSE" == "1" ]]; then
    echo "[ssh-retry] target=$target exhausted $max attempts (proxy=$used_proxy)" >&2
  fi
  printf '%s' "$output"
  return "$exit_code"
}

# ── ssh_run_pipe <target> <command> ─────────────────────────────
# 用于 tar 管道 (透传 stdin). 控制流同上, 但保留 stdin 转发.
ssh_run_pipe() {
  local target=$1
  shift
  local script="$*"

  local host=${SSH_HOST_CACHE[$target]:-}
  if [[ -z "$host" ]]; then
    host=$(target_field "$target" ssh_host)
    SSH_HOST_CACHE[$target]=$host
  fi
  local sock=${_SSH_RETRY_SOCKETS[$target]:-}
  if [[ -z "$sock" ]]; then
    ssh_retry_init "$target"
    sock=${_SSH_RETRY_SOCKETS[$target]}
  fi

  _ssh_retry_key_args "$target"
  _ssh_retry_ssh_opts_for "$target"

  local attempt=0 max=$SSH_RETRY_MAX backoff=$SSH_RETRY_BACKOFF
  local exit_code="" classify=""
  while (( attempt < max )); do
    attempt=$((attempt+1))
    # 注意: 这里用 | ssh 让 stdin 直通; ssh 自身的 stderr 我们丢弃
    ssh -o ControlMaster=auto -o ControlPath="$sock" -o ControlPersist=600 \
        "${SSH_RETRY_KEY_ARGS[@]}" "${SSH_RETRY_OPTS[@]}" "$host" "$script" >/dev/null 2>&1
    exit_code=$?
    classify=$(_ssh_retry_classify "$exit_code" "")
    case "$classify" in
      ok)
        return 0
        ;;
      retry)
        if (( attempt < max )); then
          if [[ "$SSH_RETRY_VERBOSE" == "1" ]]; then
            echo "[ssh-retry] target=$target pipe attempt=$attempt/$max retry in ${backoff}s" >&2
          fi
          sleep "$backoff"
          backoff=$((backoff * 2))
          continue
        fi
        ;;
      fail)
        return "$exit_code"
        ;;
    esac
  done
  return "$exit_code"
}

# ── 向后兼容: 原 remote_ssh / remote_ssh_pipe 别名 ─────────────
# host.sh 通过 "$ssh_cmd" "cmd" 调用. 我们定义一个 wrapper 函数名,
# 部署脚本会赋值 SSH_CMD=ssh_run_alias 让 host.sh 仍能工作.
ssh_run_alias() {
  # 调用者: SSH_CMD=ssh_run_alias, 然后 "$SSH_CMD" "245" "cmd"
  # 但原 remote_ssh 签名是 remote_ssh <cmd>, 不是 <target> <cmd>.
  # 为了兼容 "$ssh_cmd" "cmd" 调用风格, 我们要求调用方传 target.
  # 部署脚本统一用 ssh_run <target> <cmd>, 所以这里只是空定义避免崩.
  err "ssh_run_alias called — use ssh_run <target> <cmd> instead"
  return 64
}

# ── 自动 trap (脚本退出时清理 master sockets) ─────────────────
__ssh_retry_install_trap() {
  trap 'ssh_retry_close_all' EXIT INT TERM
}

# 当本文件被 source 时, 默认装 trap (幂等).
__ssh_retry_install_trap
