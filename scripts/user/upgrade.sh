#!/usr/bin/env bash
# upgrade.sh — 用户侧升级工具。
#
# 子命令（默认 run = 全流程）：
#   list      查看可用版本列表（/distribution/versions 或 catalog）
#   download  仅下载最新包到旁路目录（校验 sha256）
#   install   解压到 staging（不切换；复核 sha256）
#   switch    停止服务 → 备份（失败即中止）→ 切换 → 启动
#   test      健康检查（三段：/healthz + /readyz + /api/distribution/versions）
#   rollback  从最近备份回滚，并在回滚后健康检查
#   record    上报 upgrade-report（status=completed|failed|rolled_back）
#   run       完整：检查→下载→安装→切换→测试→成功上报；失败自动回滚并上报
#
# 用法:
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/upgrade" | bash -s -- list
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/upgrade" | DRY_RUN=1 bash -s -- run
#   bash upgrade.sh run
#   TARGET_VERSION=1.2.0 bash upgrade.sh download
#
# Env:
#   ALLOW_UNVERIFIED_PACKAGE  1=允许安装没有已发布 sha256 的包（默认 0，拒绝）
#   HEALTH_RETRIES            健康检查重试秒数（默认 30）
#   NO_SUDO                   1=不用 sudo（已是 root / 容器 / 测试）
#   REPORT_DUMP_ONLY          1=只打印上报 JSON 不发送（供测试断言 payload 合法性）
#   INSTALL_MODE              auto|host|compose（默认 auto）
#   COMPOSE_DIR               Compose 工作目录（默认 INSTALL_ROOT）
#   COMPOSE_FILE              Compose 文件（默认 COMPOSE_DIR/docker-compose.yml）
#   LOG_PATH                  生命周期日志（默认 INSTALL_ROOT/.upgrade.log）
#   STATE_PATH                状态文件（默认 INSTALL_ROOT/.upgrade-state.json）
#   NO_INTERACTIVE            1=禁用交互提示（CI/管道）
#   BACKUP_KEEP               保留的快照份数（默认 3；旧的自动 prune）
#   HEALTH_URL / READYZ_URL   健康/就绪端点（默认 /healthz、/readyz）
#
# 配置解析层级（与 install-host/install-docker 一致）：
#   1) 环境变量  2) ~/.kxmaint/config  3) 交互回退（仅 tty+NO_INTERACTIVE!=1+DRY_RUN!=1）  4) default
set -euo pipefail

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"

# 与 install-host.sh / install-docker.sh 共用同一份解析逻辑；不 source config 文件
# 以防注入，仅按 KEY=VALUE 行读取；已显式 export 的环境变量不被 config 覆盖。
load_config_file() {
  [[ -f "$KXMAINT_CONFIG" ]] || return 0
  while IFS= read -r line; do
    case "$line" in
      '#'*|'') continue ;;
    esac
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    local k="${BASH_REMATCH[1]}" v="${BASH_REMATCH[2]}"
    if [[ -z "${!k:-}" ]]; then
      printf -v "$k" '%s' "$v"
    fi
  done < "$KXMAINT_CONFIG"
}

save_config_file() {
  local k
  umask 077
  run_priv mkdir -p "$(dirname "$KXMAINT_CONFIG")"
  {
    printf '# kxmaint user config — written by %s on %s\n' "${0##*/}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '# DO NOT edit by hand unless you know the format.\n'
    for k in "$@"; do
      printf '%s=%s\n' "$k" "${!k}"
    done
  } > "$KXMAINT_CONFIG.tmp"
  run_priv mv "$KXMAINT_CONFIG.tmp" "$KXMAINT_CONFIG"
  run_priv chmod 600 "$KXMAINT_CONFIG"
}

prompt_value() {
  local var="$1" prompt="$2" default="$3" reply=""
  if [[ "${NO_INTERACTIVE:-0}" == "1" || "${DRY_RUN:-0}" == "1" || ! -t 0 ]]; then
    printf -v "$var" '%s' "$default"
    return 0
  fi
  if [[ -n "$default" ]]; then
    read -r -p "$prompt [$default]: " reply || true
    [[ -n "$reply" ]] || reply="$default"
  else
    read -r -p "$prompt: " reply || true
    [[ -n "$reply" ]] || { echo "[kxmaint] $prompt cannot be empty" >&2; return 1; }
  fi
  printf -v "$var" '%s' "$reply"
}

run_priv() {
  if [[ "${NO_SUDO:-0}" == "1" || "$(id -u)" == "0" ]]; then
    "$@"
  else
    sudo "$@"
  fi
}

# ─── JSON 读取（不依赖 python3 / jq） ───
# 客户机不保证有 python3。此前版本用 `python3 -c` 解析每个 API 响应，缺 python3
# 时它静默输出空串，脚本把"解析失败"当成"没有发布版本"，用户只看到
# `no published release` —— 与真实原因毫无关系。这里用 awk 实现唯一一份 JSON
# 文法；install-host.sh 与 install-docker.sh 内嵌的这段必须逐字一致，由
# scripts/tests/static-check.sh 守护。
#
# 兼容性：只使用 POSIX awk（无 gawk 专有的三参数 match / gensub），并在 awk
# 调用前设置 LC_ALL=C，保证 \u00XX 还原为字节。
json_flatten() {
  LC_ALL=C awk '
    function skipws(   c) {
      while (i <= n) {
        c = substr(s, i, 1)
        if (c != " " && c != "\t" && c != "\n" && c != "\r") return
        i++
      }
    }
    function hexval(c) {
      if (c >= "0" && c <= "9") return c + 0
      if (c >= "a" && c <= "f") return index("abcdef", c) + 9
      if (c >= "A" && c <= "F") return index("ABCDEF", c) + 9
      return -1
    }
    # 调用时 s[i] 是起始引号。只还原 \uXXXX —— Go 的 encoding/json 会把 & < >
    # 转义，直链（storage_uri）里很常见。
    # 其余转义一律保持原样（反斜杠 + 字母）：扁平化协议是"每条记录一行"，
    # 还原成真实换行/制表符会把一条记录劈成两半，json_get 于是只取到半截值且
    # 不报错。JSON 字符串本就不允许裸控制字符，所以这样输出永远不会断行。
    function read_string(   c, esc, j, v, d) {
      i++
      out = ""
      while (i <= n) {
        c = substr(s, i, 1)
        if (c == "\\") {
          esc = substr(s, i + 1, 1)
          if (esc == "u") {
            v = 0
            for (j = 1; j <= 4; j++) {
              d = hexval(substr(s, i + 1 + j, 1))
              if (d < 0) { v = -1; break }
              v = v * 16 + d
            }
            if (v >= 32 && v < 127) out = out sprintf("%c", v)
            else out = out substr(s, i, 6)
            i += 6
            continue
          }
          out = out "\\" esc
          i += 2
          continue
        }
        if (c == "\"") { i++; return out }
        out = out c
        i++
      }
      return out
    }
    function parse_value(path,   c, j) {
      skipws()
      if (i > n) return
      c = substr(s, i, 1)
      if (c == "{") { parse_obj(path); return }
      if (c == "[") { parse_arr(path); return }
      if (c == "\"") { print path "\t" read_string(); return }
      j = i
      while (j <= n && index(",} \t\r\n]", substr(s, j, 1)) == 0) j++
      if (j > i) print path "\t" substr(s, i, j - i)
      i = j
    }
    function parse_obj(path,   c) {
      i++
      skipws()
      if (substr(s, i, 1) == "}") { i++; return }
      while (i <= n) {
        skipws()
        if (substr(s, i, 1) != "\"") { i++; continue }
        key = read_string()
        skipws()
        if (substr(s, i, 1) == ":") i++
        parse_value((path == "$") ? "$." key : path "." key)
        skipws()
        c = substr(s, i, 1)
        if (c == ",") { i++; continue }
        if (c == "}") { i++; return }
        i++
      }
    }
    function parse_arr(path,   c, idx) {
      i++
      skipws()
      if (substr(s, i, 1) == "]") { i++; return }
      idx = 0
      while (i <= n) {
        parse_value(path "." idx)
        idx++
        skipws()
        c = substr(s, i, 1)
        if (c == ",") { i++; continue }
        if (c == "]") { i++; return }
        i++
      }
    }
    { doc = doc $0 "\n" }
    END {
      s = doc
      n = length(s)
      i = 1
      skipws()
      parse_value("$")
    }
  '
}

# json_get_flat <已扁平化的文本> <路径>，如 $.latest_version
json_get_flat() {
  printf '%s\n' "$1" | awk -F'\t' -v p="$2" '$1 == p { print $2; exit }'
}

# json_get <JSON 原文> <路径>
json_get() {
  json_get_flat "$(printf '%s' "$1" | json_flatten)" "$2"
}

# json_artifact_field <JSON 原文> <字段>：version-check 响应里第一个目标制品。
# 服务端按 (platform, arch) 过滤 target_artifacts，取 [0] 与既有实现一致。
json_artifact_field() {
  json_get "$1" "\$.target_artifacts.0.$2"
}

# catalog_sha_from_json <JSON 原文> <version> <platform> <arch>
# 在 versions[].items[] 两层结构里定位制品并打印 sha256（找不到则无输出）。
# 服务端 CatalogItem.SHA256 带 omitempty，未登记校验和的制品本来就可能缺字段，
# 因此这里"取不到"必须能安静地表达成空值。
catalog_sha_from_json() {
  local flat outer out
  flat="$(printf '%s' "$1" | json_flatten)"
  for outer in '$.versions.' '$.items.'; do
    out="$(printf '%s\n' "$flat" | awk -F'\t' -v outer="$outer" \
      -v want_v="$2" -v want_p="$3" -v want_a="$4" '
      { nrec++; path[nrec] = $1; val[nrec] = $2 }
      END {
        # 两遍扫描，**不依赖键的先后顺序**。单遍流式匹配要求 version 先于 items
        # 出现：真实服务端按结构体字段顺序编码恰好满足，但换成 map 序列化
        # （字母序，version 排在 items 之后）就会静默取不到校验和。
        # 请求侧也要剥 v：下载页允许 VERSION=v1.2.3，目录里存的是 1.2.3。
        sub(/^v/, "", want_v)
        target = ""
        for (r = 1; r <= nrec; r++) {
          if (index(path[r], outer) != 1) continue
          rest = substr(path[r], length(outer) + 1)
          d = index(rest, ".")
          if (d == 0 || substr(rest, d + 1) != "version") continue
          v = val[r]
          sub(/^v/, "", v)
          if (v == want_v) { target = substr(rest, 1, d - 1); break }
        }
        if (target == "") exit 0
        for (r = 1; r <= nrec; r++) {
          if (index(path[r], outer) != 1) continue
          rest = substr(path[r], length(outer) + 1)
          d = index(rest, ".")
          if (d == 0 || substr(rest, 1, d - 1) != target) continue
          tail_ = substr(rest, d + 1)
          if (index(tail_, "items.") != 1) continue
          t2 = substr(tail_, 7)
          d2 = index(t2, ".")
          if (d2 == 0) continue
          iidx = substr(t2, 1, d2 - 1)
          key = substr(t2, d2 + 1)
          if (key == "platform") plat[iidx] = val[r]
          else if (key == "arch") arch[iidx] = val[r]
          else if (key == "sha256") sum[iidx] = val[r]
        }
        for (k = 0; k < 100000; k++) {
          if ((k in plat) && plat[k] == want_p && arch[k] == want_a) { print sum[k]; exit }
        }
      }')"
    if [[ -n "$out" ]]; then
      printf '%s' "$out"
      return 0
    fi
  done
  return 0
}

# ─── JSON 写入 ───
# upgrade.sh 需要把任意命令输出（可能带引号/换行/反斜杠）拼成合法 JSON。
# 以前靠 python3 的 json.dumps；这里用 awk 做等价转义。注意不能用 gsub 做替换：
# gsub 的替换串里 \ 与 & 都有特殊含义，转义一层就够再转一层才能得到想要的字面量。
#
# 写出侧与读入侧**故意不对称**：json_string 产出标准 JSON（服务端 Go 的
# json.Unmarshal 能正确还原），而 json_flatten 保留 \n \t \\ \" 这些转义的原样
# 文本。原因见 read_string 的注释 —— 扁平化协议是"每条记录一行"，还原成真实
# 换行会把记录劈开。实际链路里两者不往返：写出去的只被服务端读，读回来的字段
# （url / file_name / sha256 / version）都是 ASCII 裸值，不含这些转义。
json_escape() {
  printf '%s' "$1" | LC_ALL=C awk '
    { if (NR > 1) doc = doc "\n"; doc = doc $0 }
    function esc(s,   i, c, out) {
      out = ""
      for (i = 1; i <= length(s); i++) {
        c = substr(s, i, 1)
        if (c == "\\") out = out "\\\\"
        else if (c == "\"") out = out "\\\""
        else if (c == "\t") out = out "\\t"
        else if (c == "\r") out = out "\\r"
        else if (c == "\n") out = out "\\n"
        else out = out c
      }
      return out
    }
    END { printf "%s", esc(doc) }
  '
}

# json_string <值> → 带引号的 JSON 字符串字面量
json_string() { printf '"%s"' "$(json_escape "$1")"; }

# json_object <key> <json-literal> [<key> <json-literal> ...]
# 值必须已经是 JSON 字面量：字符串用 json_string 包一层，布尔/数字直接写。
json_object() {
  local out="{" first=1 k v
  while [[ $# -ge 2 ]]; do
    k="$1"; v="$2"; shift 2
    [[ "$first" -eq 1 ]] || out="$out,"
    first=0
    out="$out\"$k\":$v"
  done
  printf '%s}' "$out"
}

# mapfile 的可移植替代：mapfile 是 bash 4.0+，macOS 自带 /bin/bash 是 3.2。
# 逐行读入调用方的 fields 数组并保留空行，保持与 mapfile -t 一致的下标语义。
read_into_fields() {
  local line
  fields=()
  while IFS= read -r line; do
    fields+=("$line")
  done < <("$@" || true)
}

# 辅助函数（load_config_file / save_config_file / prompt_value / run_priv /
# JSON 工具）都在上面定义完了，这里先把 ~/.kxmaint/config 读进来，让下面的
# normalize / validate 基于最终生效值运行。
load_config_file

# 在 default 之前用交互补齐——同时供后续 validate 复用。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgo.kxpms.cn/maintain-api"
fi

MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
# kaixuan-layout.sh 只在"脚本来自文件"时加载。`curl … | bash -s -- run`（下载页
# 给的就是这条命令）下 BASH_SOURCE 为空，旧写法退化成当前工作目录：既加载不到
# 真正的库，又会在 $PWD 下执行一个来路不明的同名文件。
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  _KX_LAYOUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)/lib/kaixuan-layout.sh"
  if [[ -f "$_KX_LAYOUT" ]]; then
    # shellcheck source=lib/kaixuan-layout.sh
    source "$_KX_LAYOUT"
  fi
fi
if ! declare -F kx_resolve_install_root >/dev/null 2>&1; then
  kx_resolve_install_root() {
    [[ -n "${INSTALL_ROOT:-}" ]] && { printf '%s\n' "$INSTALL_ROOT"; return 0; }
    [[ -d /opt/llm-gateway ]] && { printf '%s\n' /opt/llm-gateway; return 0; }
    [[ -d "${HOME}/Downloads/llm-gateway-files" ]] && { printf '%s\n' "${HOME}/Downloads/llm-gateway-files"; return 0; }
    case "$(uname -s 2>/dev/null || echo unknown)" in
      Darwin) printf '%s\n' "${HOME}/kaixuan/llm-gateway-go" ;;
      Linux) printf '%s\n' "/opt/kaixuan/llm-gateway-go" ;;
      MINGW*|MSYS*|CYGWIN*)
        if [[ -d /d && -w /d ]]; then printf '%s\n' /d/kaixuan/llm-gateway-go
        elif [[ -d /c && -w /c ]]; then printf '%s\n' /c/kaixuan/llm-gateway-go
        else printf '%s\n' "${HOME}/kaixuan/llm-gateway-go"; fi ;;
      *) printf '%s\n' "${HOME}/kaixuan/llm-gateway-go" ;;
    esac
  }
fi
INSTALL_ROOT="$(kx_resolve_install_root)"
INSTANCE_ID="${INSTANCE_ID:-$(cat "${INSTALL_ROOT}/instance_id" 2>/dev/null || true)}"
CURRENT_VERSION="${CURRENT_VERSION:-$(cat "${INSTALL_ROOT}/VERSION" 2>/dev/null || echo v0.0.0)}"
# Customer gateway probes — never default to maintain control-plane ports or /maintain/version.
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:8781/healthz}"
READYZ_URL="${READYZ_URL:-${HEALTH_URL%/healthz}/readyz}"
# Empty VERSION_PROBE_URL => compare INSTALL_ROOT/VERSION to LATEST (FR-PROBE).
VERSION_PROBE_URL="${VERSION_PROBE_URL:-}"
HEALTH_RETRIES="${HEALTH_RETRIES:-30}"
DRY_RUN="${DRY_RUN:-0}"
TARGET_VERSION="${TARGET_VERSION:-}"
# Accept either VERSION or TARGET_VERSION so the variable name matches
# install-host.sh / install-docker.sh (TARGET_VERSION is the older spelling).
# Either form may be set; we keep them in sync so downstream code that reads
# TARGET_VERSION keeps working.
VERSION="${VERSION:-${TARGET_VERSION:-}}"
: "${TARGET_VERSION:=${VERSION}}"
# Validate VERSION (semver + build_seq; same character set as the UI's
# commandBuilder.ts allowlist and the other install scripts).
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[upgrade] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi
# 没有已发布 sha256 时是否允许安装。默认 0：拒绝装未校验的二进制。
ALLOW_UNVERIFIED_PACKAGE="${ALLOW_UNVERIFIED_PACKAGE:-0}"
STAGING="${INSTALL_ROOT}/.upgrade-staging"
INSTALL_MODE="${INSTALL_MODE:-auto}"
COMPOSE_DIR="${COMPOSE_DIR:-${INSTALL_ROOT}}"
COMPOSE_FILE="${COMPOSE_FILE:-${COMPOSE_DIR}/docker-compose.yml}"
LOG_PATH="${LOG_PATH:-${INSTALL_ROOT}/.upgrade.log}"
STATE_PATH="${STATE_PATH:-${INSTALL_ROOT}/.upgrade-state.json}"
ROLLBACK_SCRIPT="${ROLLBACK_SCRIPT:-sudo bash ${INSTALL_ROOT}/scripts/upgrade.sh rollback}"
CMD="${1:-run}"
# 升级前快照保留份数；超过则按 mtime 升序剪枝。默认 3。
BACKUP_KEEP="${BACKUP_KEEP:-3}"

case "$INSTALL_MODE" in
  auto|host|compose) ;;
  *) echo "[upgrade] invalid INSTALL_MODE=${INSTALL_MODE} (auto|host|compose)" >&2; exit 2 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  loongarch64) ARCH=loong64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 2 ;;
esac

LATEST=""
URL=""
SHA=""
FILE=""
UPDATE_AVAILABLE=""

# run_priv / load_config_file 等辅助函数在脚本顶部已经定义。

LOG_AVAILABLE=0
INSTALL_MODE_EFFECTIVE="host"

log_event() {
  [[ "$LOG_AVAILABLE" == "1" ]] || return 0
  printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | run_priv tee -a "$LOG_PATH" >/dev/null || true
}

write_state() {
  local status="$1" backup="${2:-}" target="${3:-${LATEST:-}}" tmp
  local state_dir
  state_dir="$(dirname "$STATE_PATH")"
  if ! run_priv mkdir -p "$state_dir" 2>/dev/null || ! run_priv touch "$LOG_PATH" 2>/dev/null; then
    echo "[upgrade] WARN state/log path unavailable: state=$STATE_PATH log=$LOG_PATH (stdout/stderr remain the log source)" >&2
    return 0
  fi
  tmp="${TMPDIR:-/tmp}/upgrade-state.$$"
  # log_available 必须是 JSON 布尔而不是字符串，否则 Go 侧反序列化会整条失败。
  local log_avail="false"
  [[ "$LOG_AVAILABLE" == "1" ]] && log_avail="true"
  if ! json_object \
      "install_mode"    "$(json_string "$INSTALL_MODE_EFFECTIVE")" \
      "backup_dir"      "$(json_string "$backup")" \
      "rollback_script" "$(json_string "$ROLLBACK_SCRIPT")" \
      "log_path"        "$(json_string "$LOG_PATH")" \
      "log_available"   "$log_avail" \
      "current_version" "$(json_string "$CURRENT_VERSION")" \
      "target_version"  "$(json_string "$target")" \
      "status"          "$(json_string "$status")" >"$tmp"; then
    echo "[upgrade] WARN cannot write state $STATE_PATH (stdout/stderr remain the log source)" >&2
    rm -f "$tmp"
    return 0
  fi
  if ! run_priv mv "$tmp" "$STATE_PATH"; then
    echo "[upgrade] WARN cannot install state $STATE_PATH (stdout/stderr remain the log source)" >&2
    rm -f "$tmp"
    return 0
  fi
  log_event "state status=$status mode=$INSTALL_MODE_EFFECTIVE backup=${backup:-none}"
}

compose_available() {
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1
}

compose_uses_install_root() {
  [[ -f "$COMPOSE_FILE" ]] || return 1
  [[ "${COMPOSE_MANAGED_ROOT:-0}" == "1" ]] || grep -F -- "$INSTALL_ROOT" "$COMPOSE_FILE" >/dev/null 2>&1
}

detect_install_mode() {
  case "$INSTALL_MODE" in
    host) INSTALL_MODE_EFFECTIVE="host" ;;
    compose)
      [[ -f "$COMPOSE_FILE" ]] || { echo "[upgrade] compose mode requires $COMPOSE_FILE" >&2; return 1; }
      compose_available || { echo "[upgrade] compose mode requires docker compose" >&2; return 1; }
      INSTALL_MODE_EFFECTIVE="compose"
      ;;
    auto)
      if [[ -f "$COMPOSE_FILE" ]]; then
        compose_available || { echo "[upgrade] found $COMPOSE_FILE but docker compose is unavailable" >&2; return 1; }
        INSTALL_MODE_EFFECTIVE="compose"
      else
        INSTALL_MODE_EFFECTIVE="host"
      fi
      ;;
  esac
}

validate_compose_upgrade() {
  [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]] || return 0
  if ! compose_uses_install_root; then
    echo "[upgrade] refusing host-package switch for image-only Compose project: $COMPOSE_FILE" >&2
    echo "[upgrade] use install-docker.sh to load the published image tar, then run docker compose up -d" >&2
    return 1
  fi
}

compose_exec() {
  (cd "$COMPOSE_DIR" && run_priv docker compose -f "$COMPOSE_FILE" "$@")
}

init_logging() {
  if run_priv mkdir -p "$(dirname "$LOG_PATH")" 2>/dev/null && run_priv touch "$LOG_PATH" 2>/dev/null; then
    LOG_AVAILABLE=1
    log_event "upgrade command=$CMD mode=${INSTALL_MODE_EFFECTIVE}"
  else
    echo "[upgrade] WARN cannot create $LOG_PATH; stdout/stderr are the log source" >&2
  fi
}

sha256_of() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    return 1
  fi
}

# 校验失败必须返回非 0：以前 `echo "$SHA  $pkg" | sha256sum -c -` 只在 SHA 非空时跑，
# 而定向升级路径把 SHA 清空了，等于整条路径无校验。
verify_sha256() {
  local expected="$1" file="$2" actual=""
  if ! actual="$(sha256_of "$file")"; then
    echo "[upgrade] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  # 用 tr 折叠大小写而不是 ${var,,}：后者是 bash 4.0+ 语法，而下载页把 darwin
  # 列为正式安装平台，macOS 自带的 /bin/bash 是 3.2。
  if [[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" != \
        "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
    echo "[upgrade] checksum MISMATCH $file" >&2
    echo "[upgrade]   expected=$expected" >&2
    echo "[upgrade]   actual  =$actual" >&2
    return 1
  fi
  echo "[upgrade] sha256 OK $(basename "$file")"
}

report() {
  local status="$1" err="${2:-}"
  [[ -n "$INSTANCE_ID" ]] || { echo "[upgrade] skip report (no INSTANCE_ID) status=$status"; return 0; }
  # error 来自任意命令输出，可能带引号/换行/反斜杠；直接字符串拼接会产出非法
  # JSON 或注入额外字段。json_string 负责逐字符转义（等价于 python 的
  # json.dumps，但不需要 python3）。
  local body
  if ! body="$(json_object \
      "instance_id"  "$(json_string "$INSTANCE_ID")" \
      "from_version" "$(json_string "$CURRENT_VERSION")" \
      "to_version"   "$(json_string "${LATEST:-}")" \
      "status"       "$(json_string "$status")" \
      "error"        "$(json_string "$err")")"; then
    echo "[upgrade] report payload build failed (status=$status)" >&2
    return 0
  fi
  [[ "${REPORT_DUMP_ONLY:-0}" != "1" ]] || { printf '%s\n' "$body"; return 0; }
  # Public distribution endpoint (no customer JWT required); agent auth route also exists.
  # 上报是观测信息，不能顶掉真正的升级结果 —— 失败只告警。
  if ! curl -fsSL -X POST "${MAINTAIN_BASE}/distribution/upgrade-report" \
    -H 'Content-Type: application/json' \
    -H "X-License-Key: ${LICENSE_KEY:-}" \
    -H "X-Hardware-Hash: ${HARDWARE_HASH:-}" \
    -d "$body" >/dev/null 2>&1; then
    echo "[upgrade] WARN report failed (status=$status) — 结果不受影响" >&2
    return 0
  fi
  echo "[upgrade] reported status=$status"
}

# ticket 接口只返回 url/file_name/expires_at/request_id，没有 sha256。
# 定向升级要校验，就必须回到目录接口取已发布的 sha256（CatalogItem.sha256）。
published_sha() {
  local version="$1" json=""
  if ! json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)"; then
    json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  fi
  catalog_sha_from_json "$json" "$version" linux "$ARCH"
}

fetch_check() {
  local current="${CURRENT_VERSION}"
  # Asking for a specific TARGET_VERSION still uses version-check against current
  # to obtain signed storage_uri for the latest matching platform; if target is
  # set and differs from latest we fall back to ticket.
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=${current}&platform=linux&arch=${ARCH}")"
  local flat latest cur_ver avail target
  flat="$(printf '%s' "$CHECK_JSON" | json_flatten)"
  latest="$(json_get_flat "$flat" '$.latest_version')"
  latest="${latest#v}"
  cur_ver="$(json_get_flat "$flat" '$.current_version')"
  cur_ver="${cur_ver#v}"
  avail="false"
  [[ "$(json_get_flat "$flat" '$.update_available')" == "true" ]] && avail="true"
  # TARGET_VERSION 可能带空白或 v 前缀（用户直接输入）。这里用 tr 去掉全部空白，
  # 比逐侧 strip 更容易读，对版本号这种取值也没有实际差别。
  target="$(printf '%s' "${TARGET_VERSION:-}" | tr -d '[:space:]')"
  target="${target#v}"
  if [[ -n "$target" ]]; then
    # Pin target; treat as available when target != current.
    if [[ -n "$target" && "$target" != "$cur_ver" ]]; then avail="true"; else avail="false"; fi
    latest="$target"
  fi
  UPDATE_AVAILABLE="$avail"
  LATEST="$latest"
  URL="$(json_get_flat "$flat" '$.target_artifacts.0.storage_uri')"
  SHA="$(json_get_flat "$flat" '$.target_artifacts.0.sha256')"
  FILE="$(json_get_flat "$flat" '$.target_artifacts.0.artifact_name')"

  if [[ -n "$TARGET_VERSION" && ( -z "$URL" || "${LATEST#v}" != "${TARGET_VERSION#v}" ) ]]; then
    local ver="${TARGET_VERSION#v}"
    local ticket ticket_body
    ticket_body="$(printf '{"version":"%s","platform":"linux","arch":"%s"}' "$ver" "$ARCH")"
    ticket="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" -H 'Content-Type: application/json' \
      --data "$ticket_body")"
    URL="$(json_get "$ticket" '$.url')"
    FILE="$(json_get "$ticket" '$.file_name')"
    LATEST="$ver"
    UPDATE_AVAILABLE=true
    # ticket 没有 sha256 —— 从目录接口补齐，而不是把校验值清空。
    SHA="$(published_sha "$ver" || true)"
  fi
}

cmd_list() {
  echo "[upgrade] available versions (channel=${CHANNEL})"
  LIST_JSON=""
  if LIST_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)"; then
    :
  else
    LIST_JSON="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog")"
  fi
  # 在扁平化流上分组打印。优先 $.items（服务端把同一份列表同时挂在 items 和
  # versions 两个键下），再退回 $.versions。
  #
  # 写法约束：**"}" 与 "else" 必须同一行**。这些脚本在 Windows 检出时是 CRLF，
  # 而 awk 程序文本里 "}" 和 "else" 之间夹一个 CR 会让 gawk 直接报语法错误。
  printf '%s' "$LIST_JSON" | json_flatten | awk -F'\t' '
    { n++; p[n] = $1; v[n] = $2 }
    END {
      root = ""
      for (r = 1; r <= n; r++) {
        s = substr(p[r], 3); d = index(s, "."); if (d == 0) continue
        seg = substr(s, 1, d - 1)
        if (seg == "items") { root = seg; break }
        if (seg == "versions" && root == "") root = seg
      }
      if (root == "") { print "  (no versions)"; exit 0 }
      for (r = 1; r <= n; r++) {
        s = substr(p[r], 3); d = index(s, "."); if (d == 0) continue
        if (substr(s, 1, d - 1) != root) continue
        rest = substr(s, d + 1)
        d2 = index(rest, "."); if (d2 == 0) continue
        vi = substr(rest, 1, d2 - 1)
        tail = substr(rest, d2 + 1)
        if (tail == "version") { ver[vi] = v[r]; if (!(vi in seen)) { seen[vi] = 1; order[++no] = vi } }
        else if (tail == "build_seq") { seq[vi] = v[r] }
        else if (tail == "channel") { ch[vi] = v[r] }
        else {
          d3 = index(tail, "."); if (d3 == 0) continue
          grp = substr(tail, 1, d3 - 1)
          if (grp != "items" && grp != "artifacts") continue
          t2 = substr(tail, d3 + 1)
          d4 = index(t2, "."); if (d4 == 0) continue
          id = vi "." substr(t2, 1, d4 - 1)      # 版本.制品，全局唯一
          key = substr(t2, d4 + 1)
          if (key == "platform") { plat[id] = v[r] }
          else if (key == "arch") { arch[id] = v[r] }
          else if (key == "size_label") { size[id] = v[r] }
          else if (key == "size_bytes") { if (!(id in size)) size[id] = v[r] }
          if ((id in plat) && !(id in jseen)) { jseen[id] = 1; jorder[++jno] = id }
        }
      }
      for (o = 1; o <= no; o++) {
        vi = order[o]; parts = ""; np = 0
        for (q = 1; q <= jno; q++) {
          id = jorder[q]
          oi = id; sub(/\.[0-9]+$/, "", oi)     # 只取版本部分，用于分组
          if (oi != vi) continue
          p_ = ((id in plat) && plat[id] != "") ? plat[id] : "?"
          a_ = ((id in arch) && arch[id] != "") ? arch[id] : "?"
          s_ = ((id in size) && size[id] != "") ? size[id] : "?"
          parts = (np++ ? parts ", " : "") p_ "/" a_ ":" s_
        }
        if (parts == "") parts = "-"
        printf "  %s  build=%s  channel=%s  [%s]\n", ver[vi], (vi in seq ? seq[vi] : ""), (vi in ch ? ch[vi] : ""), parts
      }
    }'
  fetch_check
  echo "[upgrade] current=${CURRENT_VERSION} latest=${LATEST:-?} update_available=${UPDATE_AVAILABLE:-false}"
}

# 无已发布 sha256 时的显式决定：默认拒绝，ALLOW_UNVERIFIED_PACKAGE=1 才放行且大声告警。
require_sha_decision() {
  local ver="$1"
  if [[ -n "$SHA" ]]; then
    return 0
  fi
  if [[ "$ALLOW_UNVERIFIED_PACKAGE" == "1" ]]; then
    echo "[upgrade] ############################################################" >&2
    echo "[upgrade] WARNING: ${ver} 没有已发布 sha256，按 ALLOW_UNVERIFIED_PACKAGE=1 继续" >&2
    echo "[upgrade] WARNING: 本次升级的安装包完整性未经校验" >&2
    echo "[upgrade] ############################################################" >&2
    return 0
  fi
  echo "[upgrade] refusing unverified package: ${ver} 无已发布 sha256" >&2
  echo "[upgrade] 请让发布方补登记 sha256，或显式设置 ALLOW_UNVERIFIED_PACKAGE=1 承担风险" >&2
  return 1
}

cmd_download() {
  fetch_check
  if [[ "$UPDATE_AVAILABLE" != "true" ]]; then
    echo "[upgrade] already up to date (${LATEST:-$CURRENT_VERSION})"
    return 0
  fi
  [[ -n "$URL" ]] || { echo "missing download uri" >&2; exit 1; }
  echo "[upgrade] download ${CURRENT_VERSION} -> ${LATEST} file=${FILE}"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 url=$URL sha256=${SHA:-<none>}"
    return 0
  fi
  # 校验决定放在下载之前：没有 sha 又没开开关，就不该把包落到盘上。
  require_sha_decision "${LATEST}" || exit 1
  run_priv mkdir -p "$STAGING"
  local pkg="${STAGING}/.package.tar.gz"
  curl -fsSL "$URL" -o "$pkg"
  if [[ -n "$SHA" ]]; then
    if ! verify_sha256 "$SHA" "$pkg"; then
      run_priv rm -f "$pkg"
      echo "[upgrade] 已删除校验失败的包，未改动任何已安装文件" >&2
      exit 1
    fi
    printf '%s\n' "$SHA" | run_priv tee "${STAGING}/.package_sha256" >/dev/null
  else
    printf '%s\n' "UNVERIFIED" | run_priv tee "${STAGING}/.package_sha256" >/dev/null
  fi
  printf '%s\n' "$LATEST" | run_priv tee "${STAGING}/.target_version" >/dev/null
  printf '%s\n' "${FILE:-pkg.tar.gz}" | run_priv tee "${STAGING}/.package_name" >/dev/null
  echo "[upgrade] package saved to $pkg"
}

cmd_install() {
  local pkg="${STAGING}/.package.tar.gz"
  [[ -f "$pkg" ]] || { echo "run download first (missing $pkg)" >&2; exit 1; }
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would extract $pkg -> $STAGING"
    return 0
  fi
  # 复核：download 与 install 可能是两次独立调用，包在中间被换掉也要拦住。
  local recorded
  recorded="$(cat "${STAGING}/.package_sha256" 2>/dev/null || true)"
  if [[ -z "$recorded" ]]; then
    echo "[upgrade] missing ${STAGING}/.package_sha256 — 请重新执行 download" >&2
    exit 1
  fi
  if [[ "$recorded" == "UNVERIFIED" ]]; then
    if [[ "$ALLOW_UNVERIFIED_PACKAGE" != "1" ]]; then
      echo "[upgrade] staged package is unverified — 拒绝安装（设 ALLOW_UNVERIFIED_PACKAGE=1 才放行）" >&2
      exit 1
    fi
    echo "[upgrade] WARNING installing unverified package (ALLOW_UNVERIFIED_PACKAGE=1)" >&2
  else
    verify_sha256 "$recorded" "$pkg" || exit 1
  fi
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || true)"
  run_priv mkdir -p "${STAGING}/content"
  run_priv rm -rf "${STAGING}/content/"*
  run_priv tar -xzf "$pkg" -C "${STAGING}/content" --strip-components=1 2>/dev/null \
    || run_priv tar -xzf "$pkg" -C "${STAGING}/content"
  echo "[upgrade] staged content in ${STAGING}/content"
}

latest_backup() {
  ls -1d "${INSTALL_ROOT}"/.upgrade-backup-* 2>/dev/null | sort | tail -1 || true
}

# 备份失败必须中止：以前是 `cp -a ... || true`，等于在没有回滚材料的情况下
# 继续 rsync --delete 覆盖线上安装。
make_backup() {
  local backup="$1"
  if ! command -v rsync >/dev/null 2>&1; then
    echo "[upgrade] backup FAILED: rsync required" >&2
    return 1
  fi
  if ! run_priv mkdir -p "$backup"; then
    echo "[upgrade] backup FAILED: cannot create $backup" >&2
    return 1
  fi
  # 备份目录就在 INSTALL_ROOT 里面，所以必须排除 .upgrade-*，否则会把备份
  # 目录复制进它自己。原来用的是 `cp -a "$INSTALL_ROOT"/. "$backup"/ || true`：
  # GNU cp 会报 "cannot copy a directory into itself" 并返回非 0，BSD cp 直接
  # 递归到路径超长 —— 两种情况都被 `|| true` 吞掉，"备份成功"从未被验证过。
  if ! run_priv rsync -a --exclude '.upgrade-*' "$INSTALL_ROOT"/ "$backup"/; then
    echo "[upgrade] backup FAILED: rsync $INSTALL_ROOT -> $backup" >&2
    return 1
  fi
  # 空备份等于没有备份。
  if [[ -z "$(ls -A "$backup" 2>/dev/null || true)" ]]; then
    echo "[upgrade] backup FAILED: $backup is empty" >&2
    return 1
  fi
  # 逐项复核顶层条目（.upgrade-* 是工具自身目录，不参与）。
  local entry name missing=0
  for entry in "$INSTALL_ROOT"/*; do
    [[ -e "$entry" ]] || continue
    name="$(basename "$entry")"
    case "$name" in .upgrade-*) continue ;; esac
    if [[ ! -e "${backup}/${name}" ]]; then
      echo "[upgrade] backup FAILED: missing ${name} in $backup" >&2
      missing=1
    fi
  done
  [[ "$missing" == "0" ]] || return 1
  echo "[upgrade] backup -> $backup (verified)"
}

service_stop() {
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    echo "[upgrade] stopping compose project ${COMPOSE_FILE}"
    compose_exec stop
    return
  fi
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -E -q '^llm-gateway(\.service)?'; then
    run_priv systemctl stop llm-gateway
  fi
}

service_start() {
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    echo "[upgrade] recreating compose project ${COMPOSE_FILE}"
    compose_exec up -d --force-recreate
    return
  fi
  if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -E -q '^llm-gateway(\.service)?'; then
    run_priv systemctl start llm-gateway
  fi
}

cmd_switch() {
  [[ -d "${STAGING}/content" ]] || { echo "run install first" >&2; return 1; }
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || echo "$LATEST")"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would switch to ${LATEST}"
    return 0
  fi
  if ! command -v rsync >/dev/null 2>&1; then
    echo "rsync required for switch" >&2
    return 1
  fi
  local BACKUP="${INSTALL_ROOT}/.upgrade-backup-$(date +%Y%m%d%H%M%S)-$$"
  detect_install_mode || return 1
  validate_compose_upgrade || return 1
  init_logging
  write_state prepared "$BACKUP" "$LATEST"
  if ! run_priv mkdir -p "$INSTALL_ROOT"; then
    echo "[upgrade] cannot create $INSTALL_ROOT" >&2
    return 1
  fi
  # 备份在停服之前做，且失败就退出 —— 此时线上安装还未被触碰。
  if [[ -n "$(ls -A "$INSTALL_ROOT" 2>/dev/null | grep -v '^\.upgrade' || true)" ]]; then
    if ! make_backup "$BACKUP"; then
      echo "[upgrade] aborting switch: 没有可用备份，不会覆盖 ${INSTALL_ROOT}" >&2
      return 1
    fi
  else
    echo "[upgrade] ${INSTALL_ROOT} 为空 — 首次安装，无需备份"
  fi
  service_stop
  if ! run_priv rsync -a --delete --exclude '.upgrade-*' "${STAGING}/content"/ "$INSTALL_ROOT"/; then
    echo "[upgrade] rsync FAILED — 安装目录可能处于半更新状态" >&2
    return 1
  fi
  service_start
  if declare -F kx_prepare_layout >/dev/null 2>&1; then
    kx_prepare_layout "$INSTALL_ROOT" 0 0
  fi
  if declare -F kx_switch_current >/dev/null 2>&1 && [[ -n "$LATEST" ]]; then
    kx_switch_current "$INSTALL_ROOT" "$LATEST" "${BUILD_SEQ:-}"
  fi
  write_state switched "$BACKUP" "$LATEST"
  prune_old_backups
  echo "[upgrade] switched; service restarted if available (mode=${INSTALL_MODE_EFFECTIVE})"
}

cmd_test() {
  # 三段健康检查：/healthz（进程在跑）+ /readyz（自检完成，可服务）+
  # /version（端点解析出的版本号必须等于切完后的 LATEST）。
  # 任意一段失败都让 cmd_run 触发回滚路径 —— 这是 W8 第二轮审计的硬性增强。
  local failures=0
  local stage label url ok
  for stage in healthz readyz version; do
    case "$stage" in
      healthz)  label="/healthz"; url="$HEALTH_URL" ;;
      readyz)   label="/readyz";  url="$READYZ_URL" ;;
      version)
        # 版本探针：默认读客户机 VERSION 文件；仅当 VERSION_PROBE_URL 显式设置时才走 HTTP。
        # 禁止默认打 maintain /maintain/version（FR-PROBE）。
        label="version"
        if [[ -z "${LATEST:-}" ]]; then
          echo "[upgrade] $label: skipped (no LATEST recorded)" >&2
          failures=$((failures + 1))
          continue
        fi
        local got want_have
        want_have="${LATEST#v}"
        if [[ -z "${VERSION_PROBE_URL:-}" ]]; then
          if declare -F kx_read_installed_version >/dev/null 2>&1; then
            got="$(kx_read_installed_version "$INSTALL_ROOT" 2>/dev/null || true)"
          else
            if [[ -f "${INSTALL_ROOT}/VERSION" ]]; then
              got="$(tr -d '[:space:]' < "${INSTALL_ROOT}/VERSION")"
            elif [[ -f "${INSTALL_ROOT}/bin/current/VERSION" ]]; then
              got="$(tr -d '[:space:]' < "${INSTALL_ROOT}/bin/current/VERSION")"
            else
              got=""
            fi
          fi
          got="${got#v}"
          if [[ -z "$got" ]]; then
            echo "[upgrade] $label: missing VERSION under ${INSTALL_ROOT} or bin/current" >&2
            failures=$((failures + 1))
            continue
          fi
          # tr 折叠大小写而不是 ${got,,}（bash 4.0+，macOS 自带 bash 3.2 跑不了）。
          if [[ "$(printf '%s' "$got" | tr '[:upper:]' '[:lower:]')" != \
                "$(printf '%s' "$want_have" | tr '[:upper:]' '[:lower:]')" ]]; then
            echo "[upgrade] $label: file=$got expected=$want_have" >&2
            failures=$((failures + 1))
            continue
          fi
          echo "[upgrade] $label OK (file $got matches LATEST=$want_have)"
          continue
        fi
        url="$VERSION_PROBE_URL"
        local body
        body="$(curl -fsS "$url" 2>/dev/null || true)"
        if [[ -z "$body" ]]; then
          echo "[upgrade] $label: empty/non-2xx from $url" >&2
          failures=$((failures + 1))
          continue
        fi
        # 依次尝试 version / service_version / build_version，取第一个非空字符串。
        local flat k
        flat="$(printf '%s' "$body" | json_flatten)"
        got=""
        for k in version service_version build_version; do
          got="$(json_get_flat "$flat" "\$.$k")"
          [[ -n "$got" ]] && break
        done
        if [[ -z "$got" ]]; then
          echo "[upgrade] $label: no version field in response" >&2
          failures=$((failures + 1))
          continue
        fi
        if [[ "$(printf '%s' "$got" | tr '[:upper:]' '[:lower:]')" != \
              "$(printf '%s' "$want_have" | tr '[:upper:]' '[:lower:]')" ]]; then
          echo "[upgrade] $label: reported=$got expected=$want_have" >&2
          failures=$((failures + 1))
          continue
        fi
        echo "[upgrade] $label OK ($got matches LATEST=$want_have)"
        continue
        ;;
    esac
    ok=0
    for _ in $(seq 1 "$HEALTH_RETRIES"); do
      if curl -fsS "$url" >/dev/null 2>&1; then ok=1; break; fi
      sleep 1
    done
    if [[ "$ok" == "1" ]]; then
      echo "[upgrade] $label OK ($url)"
    else
      echo "[upgrade] $label FAILED ($url)" >&2
      failures=$((failures + 1))
    fi
  done
  if [[ "$failures" == "0" ]]; then
    return 0
  fi
  echo "[upgrade] health check FAILED ($failures stage(s)) — rollback will be triggered by cmd_run" >&2
  return 1
}

# W8 快照轮转：保留 BACKUP_KEEP 份最近成功的快照，按 mtime 升序剪枝。
# 失败/回滚过的快照也参与轮转（prefix .upgrade-backup-*），但若目录里有
# 任何写回的 state 标记（.rolled-back），就不动它 —— 让运维回溯更直观。
prune_old_backups() {
  local keep="${BACKUP_KEEP:-3}"
  local all=() old=() entry name
  # -1d 列出所有备份目录，按 mtime 升序。
  while IFS= read -r entry; do
    [[ -d "$entry" ]] || continue
    name="$(basename "$entry")"
    [[ "$name" == .upgrade-backup-* ]] || continue
    all+=("$entry")
  done < <(ls -1dt "${INSTALL_ROOT}"/.upgrade-backup-* 2>/dev/null | tac)
  # 超出 keep 的剪掉；不递归 —— 只动备份目录本身。
  if (( ${#all[@]} > keep )); then
    old=("${all[@]:keep}")
    for entry in "${old[@]}"; do
      echo "[upgrade] pruning old backup $entry (keep=$keep)"
      run_priv rm -rf "$entry" || true
    done
  fi
}

# 从备份恢复。返回 0=恢复命令成功，非 0=恢复本身失败（不代表健康）。
restore_from_backup() {
  local backup="$1"
  service_stop
  if [[ "$INSTALL_MODE_EFFECTIVE" == "compose" ]]; then
    # Compose projects managed by this script are bind-mounted to INSTALL_ROOT;
    # restoring files followed by recreate is the only safe rollback operation.
    if ! run_priv rsync -a --delete --exclude '.upgrade-*' "$backup"/ "$INSTALL_ROOT"/; then
      echo "[upgrade] rollback rsync FAILED from $backup" >&2
      return 1
    fi
  elif ! run_priv rsync -a --delete --exclude '.upgrade-*' "$backup"/ "$INSTALL_ROOT"/; then
    echo "[upgrade] rollback rsync FAILED from $backup" >&2
    return 1
  fi
  service_start
  write_state rolled_back "$backup" "$LATEST"
}

cmd_rollback() {
  local BACKUP
  detect_install_mode || exit 1
  validate_compose_upgrade || exit 1
  init_logging
  BACKUP="$(latest_backup)"
  [[ -n "$BACKUP" && -d "$BACKUP" ]] || { echo "no backup found under ${INSTALL_ROOT}/.upgrade-backup-*" >&2; exit 1; }
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 would rollback from $BACKUP"
    return 0
  fi
  LATEST="$(cat "${STAGING}/.target_version" 2>/dev/null || true)"
  local base
  base="$(basename "$BACKUP")"
  if ! restore_from_backup "$BACKUP"; then
    report failed "manual rollback from ${base} failed: rsync error"
    echo "[upgrade] rollback FAILED from $BACKUP — 安装目录未恢复" >&2
    return 1
  fi
  # 回滚也要证明自己成功了：以前回滚完直接报 rolled_back，
  # 服务起不来时用户看到的是"成功"。
  if cmd_test; then
    report rolled_back "manual rollback from ${base}"
    echo "[upgrade] rolled back from $BACKUP and healthy"
    return 0
  fi
  report failed "manual rollback from ${base} restored files but health check failed (${HEALTH_URL})"
  echo "[upgrade] rollback attempted from $BACKUP but service is STILL UNHEALTHY ($HEALTH_URL)" >&2
  return 1
}

cmd_record() {
  local status="${2:-completed}"
  local err="${3:-}"
  detect_install_mode || exit 1
  init_logging
  LATEST="${LATEST:-$(cat "${STAGING}/.target_version" 2>/dev/null || true)}"
  write_state "$status" "$(latest_backup)" "$LATEST"
  fetch_check || true
  LATEST="${LATEST:-$(cat "${STAGING}/.target_version" 2>/dev/null || true)}"
  report "$status" "$err"
}

cmd_run() {
  echo "[upgrade] current=${CURRENT_VERSION} channel=${CHANNEL}"
  fetch_check
  if [[ "$UPDATE_AVAILABLE" != "true" ]]; then
    echo "[upgrade] already up to date (${LATEST:-$CURRENT_VERSION})"
    exit 0
  fi
  [[ -n "$URL" ]] || { echo "missing download uri" >&2; exit 1; }
  echo "[upgrade] ${CURRENT_VERSION} -> ${LATEST}"
  if [[ "$DRY_RUN" == "1" ]]; then
    echo "[upgrade] DRY_RUN=1 url=$URL sha256=${SHA:-<none>}"
    exit 0
  fi
  cmd_download
  cmd_install
  # switch 在覆盖前已确认备份可用；它失败时要么什么都没动，
  # 要么已有备份可回滚 —— 两种情况都不能报成功。
  if ! cmd_switch; then
    local BACKUP
    BACKUP="$(latest_backup)"
    if [[ -n "$BACKUP" && -d "$BACKUP" ]]; then
      echo "[upgrade] switch failed — rolling back from $BACKUP" >&2
      if restore_from_backup "$BACKUP" && cmd_test; then
        report rolled_back "switch failed; rolled back and healthy"
      else
        report failed "switch failed and rollback did not restore a healthy service"
      fi
    else
      report failed "switch failed before backup existed; install untouched"
    fi
    exit 1
  fi
  if ! cmd_test; then
    echo "[upgrade] health check failed — rolling back" >&2
    local BACKUP
    BACKUP="$(latest_backup)"
    if [[ -n "$BACKUP" && -d "$BACKUP" ]]; then
      if restore_from_backup "$BACKUP" && cmd_test; then
        report rolled_back "health check failed after switch"
      else
        report failed "health check failed after switch; rollback did not restore a healthy service"
      fi
    else
      report failed "health check failed; no backup"
    fi
    exit 1
  fi
  printf '%s\n' "$LATEST" | run_priv tee "$INSTALL_ROOT/VERSION" >/dev/null
  report completed
  echo "[upgrade] success ${CURRENT_VERSION} -> ${LATEST}"
}

case "$CMD" in
  list) detect_install_mode || exit 1; init_logging; cmd_list ;;
  download) detect_install_mode || exit 1; init_logging; cmd_download ;;
  install) detect_install_mode || exit 1; init_logging; cmd_install ;;
  switch) cmd_switch ;;
  test) detect_install_mode || exit 1; init_logging; cmd_test ;;
  rollback) cmd_rollback ;;
  record) cmd_record "$@" ;;
  run|full|"") cmd_run ;;
  -h|--help|help)
    sed -n '2,26p' "$0"
    ;;
  *)
    echo "unknown command: $CMD (list|download|install|switch|test|rollback|record|run)" >&2
    exit 2
    ;;
esac

# 子命令成功跑完后，把可复用的解析结果写回 ~/.kxmaint/config（mode 600）。
# 包含 MAINTAIN_BASE / CHANNEL / INSTALL_ROOT / INSTALL_MODE / COMPOSE_DIR / BACKUP_KEEP。
# 不写一次性值（VERSION / TARGET_VERSION / HEALTH_RETRIES / LICENSE_KEY）。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "${DRY_RUN:-0}" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL INSTALL_ROOT INSTALL_MODE COMPOSE_DIR BACKUP_KEEP || true
}
persist_config
