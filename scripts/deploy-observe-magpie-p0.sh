#!/usr/bin/env bash
# Magpie P0 deploy + observe — HARD GATE: requires explicit owner auth.
#
#   MAGPIE_P0_DEPLOY_AUTH=1 TARGET=local|154 \
#     bash scripts/deploy-observe-magpie-p0.sh
#
# Without MAGPIE_P0_DEPLOY_AUTH=1 this script refuses (exit 2).
# Does not claim 全绿; observation script collects fingerprints/counters only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${TARGET:-local}"

if [[ "${MAGPIE_P0_DEPLOY_AUTH:-}" != "1" ]]; then
  cat >&2 <<'EOF'
REFUSE: MAGPIE_P0_DEPLOY_AUTH=1 not set.
Owner must authorize deploy in one explicit sentence, then re-run:
  MAGPIE_P0_DEPLOY_AUTH=1 TARGET=local bash scripts/deploy-observe-magpie-p0.sh
  MAGPIE_P0_DEPLOY_AUTH=1 TARGET=154  bash scripts/deploy-observe-magpie-p0.sh
EOF
  exit 2
fi

echo "=== preflight: no live deploy lock ==="
if pgrep -af 'deploy-seamless|deploy-154|deploy-245|deploy-local' 2>/dev/null \
  | grep -vE 'pgrep|deploy-observe-magpie|Cursor Helper|extension-host' | grep -q .; then
  echo "REFUSE: another deploy process is live — wait, do not --force" >&2
  pgrep -af 'deploy-seamless|deploy-154|deploy-245|deploy-local' | head -10 >&2
  exit 3
fi

export PATH="${HOME}/sdk/bash5/bin:${HOME}/sdk/go/bin:${PATH}"
# deploy-lib 软链常断（指向不存在的 ai-native-workspace）；钉在场的 SSOT。
if [[ -z "${AIAN_DEPLOY_LIB:-}" ]]; then
  for cand in \
    "${HOME}/workspace/ai-native-tools/deploy-lib" \
    "${HOME}/workspace/ai-native-workspace/deploy-lib"; do
    if [[ -d "$cand" ]]; then
      export AIAN_DEPLOY_LIB="$cand"
      break
    fi
  done
fi
if [[ -n "${AIAN_DEPLOY_LIB:-}" ]]; then
  echo "AIAN_DEPLOY_LIB=$AIAN_DEPLOY_LIB"
else
  echo "WARN: AIAN_DEPLOY_LIB unset and no deploy-lib SSOT found under ~/workspace" >&2
fi

# Frontend dist refuse 门：缺则先提示（不在此脚本内 pnpm build，避免越权装依赖）。
if [[ ! -f "$ROOT/web/dist/index.html" || ! -f "$ROOT/web-mobile/dist/index.html" ]]; then
  echo "WARN: web/dist or web-mobile/dist missing — deploy-seamless may refuse. Build both before auth deploy:" >&2
  echo "  (cd web && corepack pnpm install --frozen-lockfile && corepack pnpm build)" >&2
  echo "  (cd web-mobile && corepack pnpm install --frozen-lockfile && corepack pnpm build)" >&2
fi

case "$TARGET" in
  local)
    echo "=== deploy-local (host-first) ==="
    # Caller must have LLM_GATEWAY_* / DL_* as needed; we only invoke.
    if [[ ! -x "$ROOT/scripts/deploy-local.sh" ]]; then
      echo "missing scripts/deploy-local.sh" >&2
      exit 1
    fi
    bash "$ROOT/scripts/deploy-local.sh" deploy
    # Resolve binary for observe
    BIN=""
    for cand in \
      "${LLM_GATEWAY_ROOT:-$HOME/kaixuan/llm-gateway-go-v2}/run/gateway" \
      "$HOME/kaixuan/llm-gateway-go/bin/gateway" \
      "$HOME/kaixuan/llm-gateway-go/bin/current/gateway" \
      "$HOME/kaixuan/llm-gateway-go/run/gateway" \
      "$HOME/kaixuan/llm-gateway-go-v2/run/gateway"; do
      [[ -x "$cand" || -r "$cand" ]] && BIN="$cand" && break
    done
    TARGET=local BIN="$BIN" LOG_GLOB="${LOG_GLOB:-}" \
      bash "$ROOT/scripts/observe-magpie-p0-postdeploy.sh"
    ;;
  154|245)
    echo "=== deploy-$TARGET (seamless) ==="
    if [[ ! -x "$ROOT/scripts/deploy-${TARGET}.sh" ]]; then
      echo "missing scripts/deploy-${TARGET}.sh" >&2
      exit 1
    fi
    # ENVS_ROOT may need override on this machine — leave to caller env.
    bash "$ROOT/scripts/deploy-${TARGET}.sh"
    echo "=== observe on $TARGET (ssh) ==="
    BASELINE_REMOTE=""
    BASELINE_LOCAL="$ROOT/docs/audit/magpie-p0-154-baseline-20261014.json"
    if [[ "$TARGET" == "154" && -r "$BASELINE_LOCAL" ]]; then
      BASELINE_REMOTE="/tmp/magpie-p0-154-baseline.json"
      scp -q "$BASELINE_LOCAL" "$TARGET:$BASELINE_REMOTE"
    fi
    # 154/245 二进制名 llm-gateway-go；observe 脚本会自解析。JOURNAL=1 走 canary@port。
    ssh "$TARGET" "TARGET=$TARGET JOURNAL=1 WINDOW_MIN=${WINDOW_MIN:-30} BASELINE_JSON=${BASELINE_REMOTE} bash -s" \
      < "$ROOT/scripts/observe-magpie-p0-postdeploy.sh"
    ;;
  *)
    echo "unknown TARGET=$TARGET" >&2
    exit 1
    ;;
esac

echo "=== deploy+observe finished (manual W2/E2/survival still required; 勿宣称全绿) ==="
