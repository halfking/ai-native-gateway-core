#!/usr/bin/env bash
# tests/48h-audit/scripts/run-all.sh
# 一键跑指定（或全部）域的 4 类测试。
# Usage:
#   bash tests/48h-audit/scripts/run-all.sh                       # 全部 17 域
#   bash tests/48h-audit/scripts/run-all.sh --domains=D01,D02     # 指定域
#   bash tests/48h-audit/scripts/run-all.sh --categories=business # 只跑某一类
#   bash tests/48h-audit/scripts/run-all.sh --no-race             # 关闭 -race（debug 性能用）

set -uo pipefail

# 找出脚本真实路径（兼容 bash 直接调用、source、相对路径、绝对路径）
SELF_PATH="${BASH_SOURCE[0]:-$0}"
if [[ "$SELF_PATH" != /* ]]; then
  SELF_PATH="$(cd "$(dirname "$SELF_PATH")" && pwd)/$(basename "$SELF_PATH")"
fi
SELF_DIR="$(cd "$(dirname "$SELF_PATH")" && pwd)"
ROOT="$(cd "$SELF_DIR/../../.." && pwd)"
DIR="$ROOT/tests/48h-audit"
# 兜底：直接从 cwd 探测（支持 source 引入）
if [[ ! -d "$DIR" ]]; then
  CWD="$(pwd)"
  case "$CWD" in
    */tests/48h-audit) DIR="$CWD" ;;
    */tests)            DIR="$CWD/48h-audit" ;;
    *)                  DIR="$CWD/tests/48h-audit" ;;
  esac
fi
DOMAINS=""
CATEGORIES="business data stress safety"
USE_RACE=1

for arg in "$@"; do
  case "$arg" in
    --domains=*)    DOMAINS="${arg#*=}" ;;
    --categories=*) CATEGORIES="${arg#*=}" ;;
    --no-race)      USE_RACE=0 ;;
    *) echo "unknown arg: $arg" >&2; exit 1 ;;
  esac
done

c_red=$'\033[0;31m'; c_grn=$'\033[0;32m'; c_blu=$'\033[0;34m'; c_off=$'\033[0m'
log()  { printf '%s[run-all]%s %s\n' "$c_blu" "$c_off" "$*"; }
ok()   { printf '%s[ok]%s %s\n' "$c_grn" "$c_off" "$*"; }
err()  { printf '%s[err]%s %s\n' "$c_red" "$c_off" "$*" >&2; }

if [[ -z "$DOMAINS" ]]; then
  DOMAINS=$(ls "$DIR" | grep -E '^D[0-9]+-' | tr '\n' ',' | sed 's/,$//')
fi
log "domains=$DOMAINS"
log "categories=$CATEGORIES"
log "race=$USE_RACE"

cd "$ROOT"
total_pass=0
total_fail=0
failed_domains=()
for d in $(echo "$DOMAINS" | tr ',' ' '); do
  # 支持短名（D01）和长名（D01-ir-lifecycle）
  if [[ -d "$DIR/$d" ]]; then
    dpath="$DIR/$d"
    short_name="$d"
  else
    match=$(ls -d "$DIR/${d}-"* 2>/dev/null | head -1)
    if [[ -z "$match" ]]; then
      err "missing domain dir: $d"
      continue
    fi
    dpath="$match"
    short_name="$(basename "$match")"
  fi
  log "── $short_name ──"
  dom_pass=1
  for cat in $CATEGORIES; do
    cpath="$dpath/$cat"
    if [[ ! -d "$cpath" ]]; then
      log "  $cat: (no test dir)"
      continue
    fi
    go_files=$(find "$cpath" -name '*.go' -type f 2>/dev/null)
    if [[ -z "$go_files" ]]; then
      log "  $cat: (no .go files)"
      continue
    fi
    if [[ $USE_RACE -eq 1 ]]; then
      if go test -race -timeout 120s "./tests/48h-audit/$short_name/$cat/..." 2>&1 | tail -3; then
        ok "  $cat: PASS"
      else
        err "  $cat: FAIL"
        dom_pass=0
      fi
    else
      if go test -timeout 120s "./tests/48h-audit/$short_name/$cat/..." 2>&1 | tail -3; then
        ok "  $cat: PASS"
      else
        err "  $cat: FAIL"
        dom_pass=0
      fi
    fi
  done
  if [[ $dom_pass -eq 1 ]]; then
    total_pass=$((total_pass + 1))
  else
    total_fail=$((total_fail + 1))
    failed_domains+=("$d")
  fi
done

log "───────────────────────────────────────────"
log "domains: $((total_pass + total_fail))  pass: $total_pass  fail: $total_fail"
if [[ $total_fail -gt 0 ]]; then
  err "failed: ${failed_domains[*]}"
  exit 1
fi
ok "ALL DOMAINS PASS"