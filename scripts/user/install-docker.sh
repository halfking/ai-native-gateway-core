#!/usr/bin/env bash
# install-docker.sh — 用 Docker Compose 部署最新网关（amd64/arm64 + 可选 pg17+circus/citus）。
#
# Env:
#   MAINTAIN_BASE   API 根
#   CHANNEL         stable
#   COMPOSE_DIR     写出目录（默认 ./llm-gateway-docker）
#   IMAGE_DIR       镜像 tar 下载目录（默认 $COMPOSE_DIR/images）
#   INCLUDE_DB      1=写入 db 服务
#   DB_IMAGE        覆盖数据库镜像；设置后不再尝试下载/回退
#   GATEWAY_IMAGE   覆盖网关镜像；设置后不再要求本地存在 tar
#   FETCH_IMAGES    1(默认)=从 API 发现并下载 docker 镜像 tar；0=纯离线
#   LOAD_IMAGE_TAR  若设置路径，则 docker load -i 该 tar（网关镜像），优先于下载
#   LOAD_DB_TAR     若设置路径，则 docker load -i 该 tar（pg17-circus），优先于下载
#   ALLOW_DB_IMAGE_FALLBACK  1(默认)=pg17-circus 不可得时回退 postgres:17-alpine；0=直接失败
#   DRY_RUN         1=只写文件（不下载、不 docker load、不 compose up）
#   NO_INTERACTIVE  1=禁用交互提示（CI/管道）
#
# 配置解析层级（与 install-host.sh / upgrade.sh 一致）：
#   1) 环境变量  2) ~/.kxmaint/config  3) 交互回退（仅 tty+NO_INTERACTIVE!=1+DRY_RUN!=1）  4) default
set -euo pipefail

KXMAINT_CONFIG="${KXMAINT_CONFIG:-$HOME/.kxmaint/config}"

# 与 install-host.sh / upgrade.sh 共用同一份解析逻辑；不 source config 文件
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
require_tools() {
  local missing=() t
  for t in "$@"; do
    command -v "$t" >/dev/null 2>&1 || missing+=("$t")
  done
  if [[ "${#missing[@]}" -gt 0 ]]; then
    echo "[install-docker] 缺少必需命令: ${missing[*]}" >&2
    echo "[install-docker] 需要 curl(下载) / awk(解析 API 响应) / sha256sum 或 shasum(校验)" >&2
    exit 1
  fi
}

load_config_file

# 在 default 之前用交互补齐——同时供后续 validate 复用。
if [[ -z "${MAINTAIN_BASE:-}" ]]; then
  prompt_value MAINTAIN_BASE "Maintain API base URL" "https://llmgateway.internal.example.com/maintain-api"
fi
CHANNEL="${CHANNEL:-stable}"
PLATFORM="${PLATFORM:-linux}"
[[ "$PLATFORM" == "linux" ]] || { echo "[install-docker] Docker installer requires PLATFORM=linux (got ${PLATFORM})" >&2; exit 2; }
VERSION="${VERSION:-}"
# ARCH may be supplied explicitly by the generated command. Normalize catalog
# aliases while retaining uname detection when it is omitted.
if [ -n "${ARCH:-}" ]; then
  case "$ARCH" in
    amd64|x86_64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    loong64|loongarch64) ARCH=loong64 ;;
    *) echo "[install-docker] unsupported ARCH: $ARCH" >&2; exit 2 ;;
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
    echo "[install-docker] invalid VERSION=${VERSION} (must match ^[0-9A-Za-z._+-]{1,64}$)" >&2
    exit 2
  }
fi
COMPOSE_DIR="${COMPOSE_DIR:-$PWD/llm-gateway-docker}"
IMAGE_DIR="${IMAGE_DIR:-${COMPOSE_DIR}/images}"
INCLUDE_DB="${INCLUDE_DB:-1}"
FETCH_IMAGES="${FETCH_IMAGES:-1}"
ALLOW_DB_IMAGE_FALLBACK="${ALLOW_DB_IMAGE_FALLBACK:-1}"
DRY_RUN="${DRY_RUN:-0}"
POSTGRES_USER="${POSTGRES_USER:-gateway}"
POSTGRES_DB="${POSTGRES_DB:-gateway}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"

require_tools curl awk
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo "[install-docker] 缺少 sha256sum 或 shasum —— 无法校验镜像包，拒绝安装" >&2
  exit 1
fi

load_existing_password() {
  local env_file="${COMPOSE_DIR}/.env" line value
  [[ -f "$env_file" ]] || return 0
  line="$(grep -E '^POSTGRES_PASSWORD=' "$env_file" | tail -1 || true)"
  if [[ -n "$line" ]]; then
    value="${line#POSTGRES_PASSWORD=}"
    [[ -n "$value" ]] && POSTGRES_PASSWORD="$value"
  fi
}

generate_password() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  elif [[ -r /dev/urandom ]] && command -v od >/dev/null 2>&1; then
    # /dev/urandom + od 是 POSIX 组合，Linux 与 macOS 都有。刻意不退回 python3：
    # 这条安装路径的其余部分已经不依赖它了。
    head -c 256 /dev/urandom | LC_ALL=C od -An -tx1 | tr -d ' \n' | head -c 48
    echo
  else
    echo "[install-docker] 需要 openssl 或 /dev/urandom 之一来生成数据库口令（或手工设置 POSTGRES_PASSWORD）" >&2
    return 1
  fi
}

ensure_database_password() {
  [[ "$INCLUDE_DB" == "1" ]] || return 0
  load_existing_password
  if [[ -z "$POSTGRES_PASSWORD" ]]; then
    POSTGRES_PASSWORD="$(generate_password)" || exit 1
    echo "[install-docker] generated a random database password; see ${COMPOSE_DIR}/.env (mode 600)"
  fi
  if [[ "${#POSTGRES_PASSWORD}" -lt 24 ]]; then
    echo "[install-docker] POSTGRES_PASSWORD must be at least 24 characters" >&2
    exit 2
  fi
  umask 077
  if [[ -f "${COMPOSE_DIR}/.env" ]]; then
    if ! grep -q '^POSTGRES_PASSWORD=' "${COMPOSE_DIR}/.env"; then
      printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD" >>"${COMPOSE_DIR}/.env"
    fi
  else
    printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD" >"${COMPOSE_DIR}/.env"
  fi
  chmod 600 "${COMPOSE_DIR}/.env"
}

# 校验 sha256：Linux 用 sha256sum，缺失时退回 shasum；两者都没有就必须失败，
# 不能"没有工具就当校验通过"。
verify_sha256() {
  local expected="$1" file="$2" actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    echo "[install-docker] no sha256 tool (sha256sum/shasum) — cannot verify $file" >&2
    return 1
  fi
  # 用 tr 折叠大小写而不是 ${var,,}：后者是 bash 4.0+ 语法，而 macOS 自带的
  # /bin/bash 是 3.2。install-docker.sh 虽只装 linux 容器，脚本仍会被同一个
  # 入口分发执行，不应依赖调用方的 bash 版本。
  if [[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" != \
        "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ]]; then
    echo "[install-docker] checksum MISMATCH $file" >&2
    echo "[install-docker]   expected=$expected" >&2
    echo "[install-docker]   actual  =$actual" >&2
    return 1
  fi
  echo "[install-docker] sha256 OK $(basename "$file")"
}

# version-check 返回 target_artifacts[]，字段是 artifact_name / sha256 / storage_uri
# （storage_uri 在服务端被换成签名直链；签名失败时仍是 cloudreve:// 原值）。
api_artifact() {
  local platform="$1" want_arch="$2" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=${platform}&arch=${want_arch}" 2>/dev/null)" || return 1
  json_artifact_field "$json" artifact_name
  json_artifact_field "$json" sha256
  json_artifact_field "$json" storage_uri
}

# catalog/versions 里的 items[] 是 CatalogItem：platform/arch/artifact_name/sha256。
# ticket 接口不返回 sha256，所以走 ticket 时必须回到目录里取校验值。
catalog_sha() {
  local version="$1" platform="$2" want_arch="$3" json=""
  json="$(curl -fsSL "${MAINTAIN_BASE}/distribution/versions?channel=${CHANNEL}" 2>/dev/null)" \
    || json="$(curl -fsSL "${MAINTAIN_BASE}/downloads/catalog" 2>/dev/null)" || return 0
  catalog_sha_from_json "$json" "$version" "$platform" "$want_arch"
}

ticket_download() {
  local version="$1" platform="$2" want_arch="$3" body="" resp=""
  # 请求体用 printf 拼：version 来自 VERSION/目录，platform 与 arch 都来自上面的
  # 白名单枚举，三者都不需要 JSON 转义。
  body="$(printf '{"version":"%s","platform":"%s","arch":"%s"}' "$version" "$platform" "$want_arch")" || return 1
  resp="$(curl -fsSL -X POST "${MAINTAIN_BASE}/downloads/ticket" \
    -H 'Content-Type: application/json' --data "$body" 2>/dev/null)" || return 1
  json_get "$resp" '$.url'
  json_get "$resp" '$.file_name'
}

# 发现 → 下载 → 校验 → docker load。返回非 0 表示这个镜像没拿到（调用方决定是否致命）。
fetch_and_load() {
  local platform="docker" want_arch="$1" what="$2"
  local name="" sha="" uri="" url=""
  local fields=()
  if [[ -n "${VERSION:-}" ]]; then
    read_into_fields ticket_download "$LATEST_BARE" "$platform" "$want_arch"
    url="${fields[0]:-}"
    name="${fields[1]:-}"
    sha="$(catalog_sha "$LATEST_BARE" "$platform" "$want_arch" || true)"
  else
    read_into_fields api_artifact "$platform" "$want_arch"
    name="${fields[0]:-}"
    sha="${fields[1]:-}"
    uri="${fields[2]:-}"
    if [[ -n "$uri" ]]; then
      case "$uri" in
        http://*|https://*) url="$uri" ;;
      esac
    fi
    if [[ -z "$url" ]]; then
      fields=()
      read_into_fields ticket_download "$LATEST_BARE" "$platform" "$want_arch"
      url="${fields[0]:-}"
      [[ -n "${fields[1]:-}" ]] && name="${fields[1]}"
      if [[ -z "$sha" ]]; then
        sha="$(catalog_sha "$LATEST_BARE" "$platform" "$want_arch" || true)"
      fi
    fi
  fi
  if [[ -z "$url" || -z "$name" ]]; then
    echo "[install-docker] ${what}: no published docker/${want_arch} artifact for ${LATEST}" >&2
    return 1
  fi
  case "$url" in
    http://*|https://*) ;;
    *) echo "[install-docker] ${what}: refusing unsupported download URI: $url" >&2; return 1 ;;
  esac
  if [[ -z "$sha" ]]; then
    echo "[install-docker] ${what}: artifact ${name} has no published sha256 — refusing to load unverified image" >&2
    return 1
  fi
  mkdir -p "$IMAGE_DIR"
  local tar="${IMAGE_DIR}/${name}"
  echo "[install-docker] ${what}: downloading ${name}"
  if ! curl -fsSL "$url" -o "$tar"; then
    echo "[install-docker] ${what}: download failed ${name}" >&2
    rm -f "$tar"
    return 1
  fi
  if ! verify_sha256 "$sha" "$tar"; then
    rm -f "$tar"
    return 1
  fi
  load_tar "$tar" "$what"
}

load_tar() {
  local tar="$1" what="$2"
  echo "[install-docker] ${what}: docker load -i $tar"
  if ! docker load -i "$tar"; then
    echo "[install-docker] ${what}: docker load failed ($tar)" >&2
    return 1
  fi
}

docker_image_present() {
  docker image inspect "$1" >/dev/null 2>&1
}

# If VERSION is set explicitly, skip version-check (which always returns "latest")
# and resolve the requested version directly via the ticket endpoint. This makes
# historical versions installable on demand. VERSION="" (default) preserves the
# old "latest on channel" path.
if [ -n "${VERSION:-}" ]; then
  LATEST="${VERSION}"
  LATEST_BARE="${LATEST#v}"
else
  echo "[install-docker] fetching version-check for linux/${ARCH}"
  CHECK_JSON="$(curl -fsSL "${MAINTAIN_BASE}/distribution/version-check?channel=${CHANNEL}&current=v0.0.0&platform=linux&arch=${ARCH}")"
  LATEST="$(json_get "$CHECK_JSON" '$.latest_version')"
  [[ -n "$LATEST" ]] || { echo "no published release" >&2; exit 1; }
  LATEST_BARE="${LATEST#v}"
fi

GATEWAY_IMAGE_DEFAULT="llm-gateway-go:${LATEST_BARE}-${ARCH}"
PG17_IMAGE="pg17-circus:${LATEST_BARE}"
DB_IMAGE_FALLBACK="postgres:17-alpine"

mkdir -p "$COMPOSE_DIR"
ensure_database_password

HAVE_DOCKER=0
if command -v docker >/dev/null 2>&1; then HAVE_DOCKER=1; fi

GATEWAY_IMAGE_EFFECTIVE="${GATEWAY_IMAGE:-$GATEWAY_IMAGE_DEFAULT}"
DB_IMAGE_EFFECTIVE="${DB_IMAGE:-$PG17_IMAGE}"

if [[ "$DRY_RUN" != "1" && "$HAVE_DOCKER" == "1" ]]; then
  # 网关镜像：手工 tar > 在线发现下载 > 本地已有镜像 > GATEWAY_IMAGE 覆盖。
  GATEWAY_READY=0
  if [[ -n "${GATEWAY_IMAGE:-}" ]]; then
    echo "[install-docker] gateway: using GATEWAY_IMAGE=${GATEWAY_IMAGE} (operator override)"
    GATEWAY_READY=1
  elif [[ -n "${LOAD_IMAGE_TAR:-}" ]]; then
    [[ -f "$LOAD_IMAGE_TAR" ]] || { echo "[install-docker] LOAD_IMAGE_TAR not found: $LOAD_IMAGE_TAR" >&2; exit 1; }
    load_tar "$LOAD_IMAGE_TAR" "gateway" || exit 1
    GATEWAY_READY=1
  elif docker_image_present "$GATEWAY_IMAGE_DEFAULT"; then
    echo "[install-docker] gateway: ${GATEWAY_IMAGE_DEFAULT} already present locally"
    GATEWAY_READY=1
  elif [[ "$FETCH_IMAGES" == "1" ]] && fetch_and_load "$ARCH" "gateway"; then
    GATEWAY_READY=1
  fi
  if [[ "$GATEWAY_READY" != "1" ]]; then
    echo "[install-docker] gateway image ${GATEWAY_IMAGE_DEFAULT} unavailable:" >&2
    echo "[install-docker]   - 没有本地镜像，且未能从 ${MAINTAIN_BASE} 下载 docker/${ARCH} 镜像" >&2
    echo "[install-docker]   - 离线安装请设 LOAD_IMAGE_TAR=/path/llm-gateway-go-${LATEST_BARE}-${ARCH}.tar" >&2
    echo "[install-docker]   - 或设 GATEWAY_IMAGE=<registry 镜像> 使用自有仓库" >&2
    exit 1
  fi
  if [[ -z "${GATEWAY_IMAGE:-}" ]] && ! docker_image_present "$GATEWAY_IMAGE_DEFAULT"; then
    echo "[install-docker] loaded tar did not provide expected tag ${GATEWAY_IMAGE_DEFAULT}" >&2
    echo "[install-docker] 本地镜像列表：" >&2
    docker image ls --format '  {{.Repository}}:{{.Tag}}' >&2 || true
    exit 1
  fi

  # 数据库镜像：回退到 postgres:17-alpine 是安装前的显式决定，
  # 不是 compose 失败之后再重试 —— 那样会把真实错误当成"镜像缺失"掩盖掉。
  if [[ "$INCLUDE_DB" == "1" ]]; then
    if [[ -n "${DB_IMAGE:-}" ]]; then
      echo "[install-docker] db: using DB_IMAGE=${DB_IMAGE} (operator override, no fallback)"
    else
      DB_READY=0
      if [[ -n "${LOAD_DB_TAR:-}" ]]; then
        [[ -f "$LOAD_DB_TAR" ]] || { echo "[install-docker] LOAD_DB_TAR not found: $LOAD_DB_TAR" >&2; exit 1; }
        load_tar "$LOAD_DB_TAR" "db" || exit 1
        DB_READY=1
      elif docker_image_present "$PG17_IMAGE"; then
        echo "[install-docker] db: ${PG17_IMAGE} already present locally"
        DB_READY=1
      elif [[ "$FETCH_IMAGES" == "1" ]] && fetch_and_load multi "db"; then
        DB_READY=1
      fi
      if [[ "$DB_READY" == "1" ]] && docker_image_present "$PG17_IMAGE"; then
        DB_IMAGE_EFFECTIVE="$PG17_IMAGE"
      elif [[ "$ALLOW_DB_IMAGE_FALLBACK" == "1" ]]; then
        DB_IMAGE_EFFECTIVE="$DB_IMAGE_FALLBACK"
        echo "[install-docker] db: ${PG17_IMAGE} 不可得 — 按 ALLOW_DB_IMAGE_FALLBACK=1 回退 ${DB_IMAGE_FALLBACK}" >&2
        echo "[install-docker] db: 回退镜像不含 circus/citus 运维侧车，仅用于引导" >&2
      else
        echo "[install-docker] db: ${PG17_IMAGE} 不可得，且 ALLOW_DB_IMAGE_FALLBACK=0" >&2
        echo "[install-docker] db: 请提供 LOAD_DB_TAR=/path/pg17-circus-${LATEST_BARE}.tar 或设 DB_IMAGE=" >&2
        exit 1
      fi
    fi
  fi
elif [[ "$DRY_RUN" != "1" ]]; then
  echo "[install-docker] docker not found — skip image fetch/load"
fi

cat >"${COMPOSE_DIR}/docker-compose.yml" <<YAML
# Generated by install-docker.sh for ${LATEST} (${ARCH})
# Offline image tars (when published) live under files.internal.example.com:
	#   cloudreve://my/release/llm-gateway-go/${LATEST_BARE}/docker/linux-${ARCH}/
services:
  gateway:
    image: \${GATEWAY_IMAGE:-${GATEWAY_IMAGE_EFFECTIVE}}
    ports:
      - "\${GATEWAY_PORT:-8080}:8080"
    environment:
      - MAINTAIN_BASE=${MAINTAIN_BASE}
    restart: unless-stopped
YAML

if [[ "$INCLUDE_DB" == "1" ]]; then
  cat >>"${COMPOSE_DIR}/docker-compose.yml" <<YAML
  db:
    # 安装前已确定的镜像（pg17-circus 可得时优先，否则 ${DB_IMAGE_FALLBACK}）。
    image: \${DB_IMAGE:-${DB_IMAGE_EFFECTIVE}}
    environment:
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
      POSTGRES_DB: \${POSTGRES_DB:-${POSTGRES_DB}}
    volumes:
      - pgdata:/var/lib/postgresql/data
    restart: unless-stopped
volumes:
  pgdata:
YAML
fi

cat >"${COMPOSE_DIR}/.env.example" <<EOF
GATEWAY_IMAGE=${GATEWAY_IMAGE_EFFECTIVE}
GATEWAY_PORT=8080
DB_IMAGE=${DB_IMAGE_EFFECTIVE}
POSTGRES_USER=${POSTGRES_USER}
POSTGRES_DB=${POSTGRES_DB}
# Copy the generated ${COMPOSE_DIR}/.env or set a strong POSTGRES_PASSWORD before starting.
EOF

cat >"${COMPOSE_DIR}/INSTALL-DOCKER.md" <<EOF
# Docker 安装说明（${LATEST}）

1. 镜像获取：脚本默认从 ${MAINTAIN_BASE} 发现并下载 docker 产物（校验 sha256 后 docker load）：
   - \`llm-gateway-go-${LATEST_BARE}-${ARCH}.tar\`（platform=docker, arch=${ARCH}）
   - \`pg17-circus-${LATEST_BARE}.tar\`（platform=docker, arch=multi）
   离线场景：\`LOAD_IMAGE_TAR=… LOAD_DB_TAR=… FETCH_IMAGES=0 bash install-docker.sh\`
2. \`cp .env.example .env\` 并按需修改
3. \`docker compose up -d\`
4. 健康检查后访问 Maintain 激活页完成在线/离线激活

当前编排使用：gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}
离线包与 Docker 通道并行；主机安装请用 install-scripts/host。
EOF

echo "[install-docker] wrote ${COMPOSE_DIR}/docker-compose.yml for ${LATEST}"
if [[ "$DRY_RUN" == "1" ]]; then
  echo "[install-docker] DRY_RUN=1 — no download / no docker load / no compose up"
  exit 0
fi
if [[ "$HAVE_DOCKER" != "1" ]]; then
  # 编排文件已写好，但一台容器都没起来。安装模式的成功含义是"网关在跑"，
  # 这里返回 0 会让自动化把一次什么都没做的安装当成成功；而且版本存在性
  # 校验只发生在"有 docker"分支里，所以指定一个不存在的版本也照样返回 0。
  # 只想生成文件请显式用 DRY_RUN=1。
  echo "[install-docker] docker not found — ${COMPOSE_DIR} 已就绪，但没有启动任何容器" >&2
  echo "[install-docker] 请先安装 Docker Engine；或用 DRY_RUN=1 只生成编排文件" >&2
  exit 1
fi
# compose 失败必须让安装失败：以前这里吞掉了错误并打印"attempted"。
if ! (cd "$COMPOSE_DIR" && docker compose up -d); then
  echo "[install-docker] docker compose up FAILED in ${COMPOSE_DIR}" >&2
  echo "[install-docker] gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}" >&2
  echo "[install-docker] 排查：docker compose -f ${COMPOSE_DIR}/docker-compose.yml logs" >&2
  exit 1
fi
echo "[install-docker] compose up OK (gateway=${GATEWAY_IMAGE_EFFECTIVE} db=${DB_IMAGE_EFFECTIVE}). Activate via maintain UI."
# 仅持久化可复用项：不含 IMAGE_* / LOAD_*_TAR / POSTGRES_PASSWORD / 单次 VERSION。
KXMAINT_PERSISTED=0
persist_config() {
  [[ "$KXMAINT_PERSISTED" == "1" ]] && return 0
  [[ -n "${KXMAINT_SKIP_PERSIST:-}" ]] && return 0
  [[ "${DRY_RUN:-0}" == "1" ]] && return 0
  KXMAINT_PERSISTED=1
  save_config_file MAINTAIN_BASE CHANNEL COMPOSE_DIR IMAGE_DIR INCLUDE_DB ALLOW_DB_IMAGE_FALLBACK || true
}
persist_config
