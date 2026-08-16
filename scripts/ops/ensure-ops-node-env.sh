#!/usr/bin/env bash
# Ensure OPS_NODE_REGION (and center-agent enablement) on local / 245 / 154.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

TARGET="${1:-}"
REGION="${2:-$TARGET}"

usage() {
  echo "用法: $0 <local|245|154> [region]" >&2
  exit 1
}

[[ -n "$TARGET" ]] || usage
case "$TARGET" in
  local|245|154) ;;
  *) usage ;;
esac

case "$REGION" in
  local|245|154|unknown) ;;
  *) REGION="$TARGET" ;;
esac

SSH_PORT="${LLM_GATEWAY_SSH_PORT:-${SSH_PORT:-25022}}"
case "$TARGET" in
  245) SSH_KEY_FILE="${SSH_KEY_245:-${SSH_KEY_FILE:-}}" ;;
  154) SSH_KEY_FILE="${SSH_KEY_154:-${SSH_KEY_FILE:-}}" ;;
esac
[[ -n "$SSH_KEY_FILE" && -f "$SSH_KEY_FILE" ]] || { echo "ERROR: missing SSH key for $TARGET" >&2; exit 1; }
SSH_OPTS=(-i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)

env_file_for_target() {
  case "$1" in
    154) printf '%s\n' "/etc/llm-gateway-go/env" ;;
    245) printf '%s\n' "/opt/llm-gateway-go/.env" ;;
    *) printf '%s\n' "" ;;
  esac
}

upsert_env_keys() {
  local file=$1
  python3 - "$file" "$REGION" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
region = sys.argv[2]
wanted = {
    "LLM_GATEWAY_CENTER_URL": "https://llm.kxpms.cn",
    "OPS_NODE_REGION": region,
    "OPS_CENTER_AGENT_DISABLED": "0",
    "OPS_COLLECT_URL": "https://llm.kxpms.cn",
}
# 2026-08-17: 显式指定 encoding='utf-8'。Python 3.6 的 pathlib.Path.read_text()
# 默认走 locale.getpreferredencoding()，在 245 (LANG=C) 上是 ASCII；env 文件若含
# 中文注释/破折号等 UTF-8 字节，read_text() 会抛 UnicodeDecodeError。
lines = path.read_text(encoding='utf-8').splitlines() if path.exists() else []
out, touched = [], set()
for ln in lines:
    if (ln.startswith("LLM_GATEWAY_CENTER_URL=") or
            ln.startswith("OPS_NODE_REGION=") or
            ln.startswith("OPS_CENTER_AGENT_DISABLED=") or
            ln.startswith("OPS_COLLECT_URL=")):
        key = ln.split("=", 1)[0]
        if key in wanted:
            out.append(f"{key}={wanted[key]}")
            touched.add(key)
            continue
    out.append(ln)
if out and out[-1].strip():
    out.append("")
missing = [k for k in wanted if k not in touched]
if missing:
    out.append("# ops node telemetry (centeragent → 252 gateway_instances)")
    for k in sorted(missing):
        out.append(f"{k}={wanted[k]}")
path.parent.mkdir(parents=True, exist_ok=True)
path.write_text("\n".join(out).rstrip() + "\n", encoding='utf-8')
print("updated", path, "region=", region)
PY
}

if [[ "$TARGET" == "local" ]]; then
  local_env="$ROOT/.env"
  if [[ ! -f "$local_env" ]]; then
    cp "$ROOT/.env.example" "$local_env"
  fi
  upsert_env_keys "$local_env"
  mkdir -p "$HOME/.local/share/kx-gateway" /var/lib/kx-gateway 2>/dev/null || true
  echo "[ensure-ops-node-env] ✓ local .env OPS_NODE_REGION=$REGION"
  exit 0
fi

ENV_FILE="$(env_file_for_target "$TARGET")"
case "$TARGET" in
  154) SSH_HOST="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}" ;;
  245) SSH_HOST="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}" ;;
esac

echo "[ensure-ops-node-env] 更新 $TARGET ($ENV_FILE) region=$REGION"
ssh "${SSH_OPTS[@]}" "$SSH_HOST" "python3 - <<'PY'
import shutil, time
from pathlib import Path

env_path = Path('$ENV_FILE')
region = '$REGION'
bak = env_path.with_suffix(env_path.suffix + f'.bak.ops.{time.strftime(\"%Y%m%d-%H%M%S\")}')
if env_path.exists():
    shutil.copy2(env_path, bak)
wanted = {
    'LLM_GATEWAY_CENTER_URL': 'https://llm.kxpms.cn',
    'OPS_NODE_REGION': region,
    'OPS_CENTER_AGENT_DISABLED': '0',
    'OPS_COLLECT_URL': 'https://llm.kxpms.cn',
}
# 2026-08-17: 显式 encoding='utf-8'，避免 Python 3.6 + LANG=C 默认 ASCII 抛
# UnicodeDecodeError（见上文同源 fix 注释）。
lines = env_path.read_text(encoding='utf-8').splitlines() if env_path.exists() else []
out, touched = [], set()
for ln in lines:
    if (ln.startswith('LLM_GATEWAY_CENTER_URL=') or
            ln.startswith('OPS_NODE_REGION=') or
            ln.startswith('OPS_CENTER_AGENT_DISABLED=') or
            ln.startswith('OPS_COLLECT_URL=')):
        key = ln.split('=', 1)[0]
        if key in wanted:
            out.append(f'{key}={wanted[key]}')
            touched.add(key)
            continue
    out.append(ln)
if out and out[-1].strip():
    out.append('')
missing = [k for k in wanted if k not in touched]
if missing:
    out.append('# ops node telemetry (centeragent → 252 gateway_instances)')
    for k in sorted(missing):
        out.append(f'{k}={wanted[k]}')
env_path.parent.mkdir(parents=True, exist_ok=True)
env_path.write_text('\\n'.join(out).rstrip() + '\\n', encoding='utf-8')
print('backup:', bak)
print('region:', region)
PY"
ssh "${SSH_OPTS[@]}" "$SSH_HOST" "mkdir -p /var/lib/kx-gateway && chmod 700 /var/lib/kx-gateway"
echo "[ensure-ops-node-env] ✓ $TARGET 已设置 OPS_NODE_REGION=$REGION"
