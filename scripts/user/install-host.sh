#!/usr/bin/env bash
# install-host.sh — 在主机上安装最新稳定版 llm-gateway-go 离线包。
# 用法:
#   curl -fsSL "$MAINTAIN_BASE/distribution/install-scripts/host" | bash
#   或: bash install-host.sh
#
# 配置解析层级（高→低，后写覆盖先写）：
#   1) 进程环境变量（MAINTAIN_BASE=... CHANNEL=... 等）
#   2) ~/.kxmaint/config（首次自动写回，模式 600）
#   3) 交互式回退（仅 stdin 是 tty 且 NO_INTERACTIVE!=1 且 DRY_RUN!=1 时）
#   4) 内置 default（MAINTAIN_BASE=https://llmgo.kxpms.cn/maintain-api 等）
#
# 关闭交互：NO_INTERACTIVE=1；跳过网络：DRY_RUN=1。
set -euo pipefail

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"
# 三个脚本（install-host/install-docker/upgrade）共用同一份 config。
# 这里允许 KXMAINT_CONFIG 指向文件覆盖，给测试或系统级部署留口子。
# load_config_file 会在文件不存在时直接返回，不自动创建；交互提示后
# 才调 save_config_file 写回。
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

# 交互式问一个值。stdin 不是 tty 或 NO_INTERACTIVE=1 时直接用 default。
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

# root 或 NO_SUDO=1（容器/测试）时直接执行；否则走 sudo。
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
require_tools() {
  local missing=() t
  for t in "$@"; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    echo "[install-host] 缺少必需命令: ${missing[*]}" >&2
    echo "[install-host] 需要 curl(下载) / tar(解包) / awk(解析 API 响应) / sha256sum 或 shasum(校验)" >&2
    exit 1
  fi
}

load_config_file

# load_config_file 在文件顶部已调用；这里把"环境未设"的项用交互或 default 填齐。
# 写回发生在脚本末尾（save_config_file），不在此处——让前面的 normalize / validate
# 能直接基于最终生效值运行。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgo.kxpms.cn/maintain-api"
fi
MAINTAIN_BASE="${MAINTAIN_BASE:-https://llmgo.kxpms.cn/maintain-api}"
CHANNEL="${CHANNEL:-stable}"
VERSION="${VERSION:-}"
PLATFORM="${PLATFORM:-linux}"
case "$PLATFORM" in
  linux|darwin) ;;
  windows)
    echo "[install-host] Windows host installation requires the PowerShell client-deploy.ps1 script" >&2
    exit 2
    ;;
  *) echo "[install-host] unsupported PLATFORM: $PLATFORM" >&2; exit 2 ;;
esac
# ARCH may be supplied explicitly by the generated command. Normalize catalog
# aliases while retaining uname detection when it is omitted.
if [ -n "${ARCH:-}" ]; then
  case "$ARCH" in
    amd64|x86_64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    loong64|loongarch64) ARCH=loong64 ;;
    *) echo "[install-host] unsupported ARCH: $ARCH" >&2; exit 2 ;;
  esac
else
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    loongarch64|loong64) ARCH=loong64 ;;
    *) echo "unsupported arch: $arch" >&2; exit 2 ;;
  esac
fi
# Validate VERSION (semver + build_seq; same character set as the UI's
# commandBuilder.ts allowlist). Reject anything that could break the
# downstream JSON payload to /downloads/ticket.
if [ -n "${VERSION:-}" ]; then
  [[ "${VERSION}" =~ ^[0-9A-Za-z._+-]{1,64}$ ]] || {
    echo "[install-host] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi

# New-install default is kaixuan (FR-LAYOUT). Explicit INSTALL_ROOT and
# existing /opt/llm-gateway or ~/Downloads/llm-gateway-files win.
#
# kaixuan-layout.sh 只在"脚本来自文件"时加载。`curl … | bash`（下载页给的就是
# 这条命令）下 BASH_SOURCE 为空，原来的 `cd "$(dirname "${BASH_SOURCE[0]:-}")"`
# 会退化成当前工作目录，于是：既加载不到真正的库（管道路径下 kx_prepare_layout /
# kx_switch_current 全部静默缺失），又会在 $PWD 下执行一个来路不明的同名文件。
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
EDITION="${EDITION:-customer}"
DRY_RUN="${DRY_RUN:-0}"

# 管道路径（curl | bash）拿不到 lib/kaixuan-layout.sh 时，用等价的内联实现，
# 保证"从文件跑"和"从管道跑"产出同一套目录结构与 bin/current 指针。
if ! declare -F kx_prepare_layout >/dev/null 2>&1; then
  kx_prepare_layout() {
    local root="$1"
    mkdir -p "$root/attachments" "$root/bin" "$root/backups" "$root/logs" "$root/raw-logs" "$root/run"
  }
fi
if ! declare -F kx_switch_current >/dev/null 2>&1; then
  kx_switch_current() {
    local root="$1" version="$2" build="${3:-}" slot dest tmp
    version="${version#v}"
    if [[ -n "$build" ]]; then slot="$version.$build"; else slot="$version"; fi
    dest="$root/bin/$slot"
    mkdir -p "$dest"
    tmp="$root/bin/.current.new.$$"
    ln -s "$slot" "$tmp"
    rm -f "$root/bin/current"
    mv "$tmp" "$root/bin/current"
  }
fi

require_tools curl tar awk
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo "[install-host] 缺少 sha256sum 或 shasum —— 无法校验下载包，拒绝安装" >&2
  exit 1
fi

# 第一次成功生成默认值后，把可复用项写回 config —— 不写一次性值（VERSION/TARGET_VERSION）。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "$DRY_RUN" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL EDITION INSTALL_ROOT PLATFORM || true
}

verify_sha256() {
  local expected="$1" file="$2" actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    echo "[install-host] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  # 用 tr 折叠大小写而不是 ${var,,}：后者是 bash 4.0+ 语法，而 macOS 自带的
  # /bin/bash 是 3.2，下载页却把 darwin 列为正式安装平台。
  if [[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" != \
        "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
    echo "[install-host] checksum MISMATCH $file" >&2
    echo "[install-host]   expected=$expected" >&2
    echo "[install-host]   actual  =$actual" >&2
    return 1
  fi
}

catalog_sha() {
  local version="$1" platform="$2" want_arch="$3" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)" \
    || json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  catalog_sha_from_json "$json" "$version" "$platform" "$want_arch"
}

echo "[install-host] checking latest ${CHANNEL} for ${PLATFORM}/${ARCH}"
# If VERSION is set explicitly, skip version-check (which always returns "latest")
# and resolve the requested version directly via the ticket endpoint. This makes
# historical versions installable on demand. VERSION="" (default) preserves the
# old "latest on channel" path.
if [ -n "${VERSION:-}" ]; then
  # 请求体直接用 printf 拼：VERSION 已按 ^[0-9A-Za-z._+-]{1,64}$ 校验，
  # PLATFORM/ARCH 来自上面的白名单枚举，三个值都不需要 JSON 转义。
  REQ_BODY="$(printf '{"version":"%s","platform":"%s","arch":"%s"}' "$VERSION" "$PLATFORM" "$ARCH")"
  TICKET_JSON="$(curl -fsSL \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "$REQ_BODY" \
    "${MAINTAIN_BASE}/downloads/ticket")"
  URL="$(json_get "$TICKET_JSON" '$.url')"
  FILE="$(json_get "$TICKET_JSON" '$.file_name')"
  LATEST="${VERSION}"
  SHA="$(catalog_sha "${VERSION}" "${PLATFORM}" "${ARCH}" || true)"
  [[ -n "$SHA" ]] || {
    echo "[install-host] explicit VERSION=${VERSION} has no published sha256 for ${PLATFORM}/${ARCH} — refusing unverified download" >&2
    exit 1
  }
else
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=${PLATFORM}&arch=${ARCH}&edition=${EDITION}")"

  # 一次扁平化，四次取值。直接对四个变量赋值，不再用 mapfile（bash 4.0+）。
  _flat="$(printf '%s' "$CHECK_JSON" | json_flatten)"
  LATEST="$(json_get_flat "$_flat" '$.latest_version')"
  URL="$(json_get_flat "$_flat" '$.target_artifacts.0.storage_uri')"
  SHA="$(json_get_flat "$_flat" '$.target_artifacts.0.sha256')"
  FILE="$(json_get_flat "$_flat" '$.target_artifacts.0.artifact_name')"
fi

if [[ -z "$LATEST" || -z "$URL" ]]; then
  # If the user explicitly requested a VERSION, do NOT silently fall back to
  # the catalog top — they want what they asked for, or a clear error.
  if [ -n "${VERSION:-}" ]; then
    echo "[install-host] explicit VERSION=${VERSION} not found via ticket" >&2
    exit 1
  fi
  # fallback: ticket API after catalog
  CATALOG_JSON="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog")"
  VERSION="$(json_get "$CATALOG_JSON" '$.versions.0.version')"
  [[ -n "$VERSION" ]] || { echo "no published release" >&2; exit 1; }
  REQ_BODY="$(printf '{"version":"%s","platform":"%s","arch":"%s"}' "$VERSION" "$PLATFORM" "$ARCH")"
  TICKET_JSON="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" -H 'Content-Type: application/json' \
    --data "$REQ_BODY")"
  URL="$(json_get "$TICKET_JSON" '$.url')"
  FILE="$(json_get "$TICKET_JSON" '$.file_name')"
  LATEST="$VERSION"
  # fallback 路径同样必须拿到 sha256，否则下面的统一校验会因为空值而失败。
  SHA="$(catalog_sha "$VERSION" "$PLATFORM" "$ARCH" || true)"
fi

echo "[install-host] target=${LATEST} file=${FILE}"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
curl -fsSL "$URL" -o "${WORKDIR}/${FILE:-package.tar.gz}"
# sha256 是硬门。CatalogItem.SHA256 带 omitempty，发布时漏登记校验和的制品在
# latest 路径上过去会因为 SHA 为空而**跳过校验**直接安装 —— 而显式 VERSION
# 路径是拒绝的，install-docker.sh 也是拒绝的。三处口径必须一致。
[[ -n "$SHA" ]] || {
  echo "[install-host] ${LATEST} has no published sha256 for ${PLATFORM}/${ARCH} — refusing unverified download" >&2
  exit 1
}
verify_sha256 "$SHA" "${WORKDIR}/${FILE:-package.tar.gz}"

if [[ "$DRY_RUN" == "1" ]]; then
  echo "[install-host] DRY_RUN=1 — downloaded only"
  exit 0
fi

# 走 run_priv 而不是裸 sudo：NO_SUDO=1（容器 / rootless / CI）和"当前已是 root"
# 都必须真的生效。裸 sudo 让这两种环境在下载全部成功之后、安装落地之前失败。
run_priv mkdir -p "$INSTALL_ROOT"
if declare -F kx_prepare_layout >/dev/null 2>&1; then
  run_priv kx_prepare_layout "$INSTALL_ROOT" 0 0
fi
run_priv tar -xzf "${WORKDIR}/${FILE:-package.tar.gz}" -C "$INSTALL_ROOT" --strip-components=1 2>/dev/null \
  || run_priv tar -xzf "${WORKDIR}/${FILE:-package.tar.gz}" -C "$INSTALL_ROOT"
if [[ -x "$INSTALL_ROOT/install.sh" ]]; then
  run_priv "$INSTALL_ROOT/install.sh"
elif [[ -x "$INSTALL_ROOT/scripts/install.sh" ]]; then
  run_priv "$INSTALL_ROOT/scripts/install.sh"
fi
printf '%s\n' "$LATEST" | run_priv tee "$INSTALL_ROOT/VERSION" >/dev/null
if declare -F kx_switch_current >/dev/null 2>&1 && [[ -n "$LATEST" ]]; then
  run_priv kx_switch_current "$INSTALL_ROOT" "$LATEST" ""
fi

persist_config
echo "[install-host] done for ${PLATFORM}/${ARCH}. Activate at: ${MAINTAIN_BASE%/maintain-api}/maintain/activate"
echo "[install-host] offline: ${MAINTAIN_BASE%/maintain-api}/maintain/offline-activation"
