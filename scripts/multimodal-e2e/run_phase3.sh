#!/usr/bin/env bash
# scripts/multimodal-e2e/run_phase3.sh
#
# Phase 3 — Real model E2E test runner.
#
# Iterates scripts/multimodal-e2e/cases/T-*.json and POSTs the matching
# payload to $GATEWAY_URL. Reads API keys from environment only (never
# inlines secrets into logs or repo).
#
# Auth: Authorization: Bearer $LLM_GATEWAY_API_KEY (client credential sent
# to gateway; gateway forwards to upstream with its stored credentials).
#
# Usage:
#   export LLM_GATEWAY_API_KEY=...      # any valid client key
#   export GATEWAY_URL=http://localhost:8781
#   export ONLY_IDS=T-02,T-04        # optional case selection
#   export SKIP_IDS=T-05             # optional ad-hoc skips
#   export MODEL_OVERRIDE_T_02=gpt-4o # optional per-case model override
#   bash scripts/multimodal-e2e/run_phase3.sh           # execute
#   bash scripts/multimodal-e2e/run_phase3.sh --dry-run # print curl, no send
#
# Pass criteria per case (driven by case.expected field):
#   "accept" → HTTP 200 + non-empty content (default)
#   "reject" → HTTP 400/422 (upstream or gateway rejected modality mismatch)
#   "skip"   → leave for later session
#
# Cost guardrails (from 00-test-plan.md §9):
#   - ≤2 calls per case, ≤15 total
#   - single-call timeout 30s
#   - no retries on 5xx (treat as fail)
#
# CLI:
#   --dry-run          print curl, no send
#   --id T-02,T-04     restrict to listed case IDs (alias for ONLY_IDS)
#   --id-file PATH     read case IDs from PATH (whitespace/comma separated,
#                      comments via '#'); alias for ONLY_IDS
#   --skip T-05        skip listed case IDs (alias for SKIP_IDS)
#   --skip-file PATH   read case IDs from PATH; alias for SKIP_IDS

set -uo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
API_KEY="${LLM_GATEWAY_API_KEY:-}"
SAMPLES_DIR="$(cd "$(dirname "$0")/../../docs/multimodal-testing/samples" && pwd)"
CASES_DIR="$(cd "$(dirname "$0")" && pwd)/cases"
LOG_FILE="${LOG_FILE:-/tmp/multimodal-e2e-phase3.log}"
ONLY_IDS="${ONLY_IDS:-}"
SKIP_IDS="${SKIP_IDS:-}"

DRY_RUN=0

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
yel()   { printf '\033[33m%s\033[0m\n' "$*"; }

read_id_file() {
  local file="$1"
  [[ -f "$file" ]] || { red "id file not found: $file"; return 1; }
  python3 - "$file" <<'PY'
import re,sys
ids=[]
for line in open(sys.argv[1]):
  line=line.split('#',1)[0]
  for m in re.findall(r'T-\d{2,4}', line):
    if m not in ids: ids.append(m)
print(','.join(ids))
PY
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --) shift; while [[ $# -gt 0 ]]; do shift; done ;;  # swallow separators
    --dry-run) DRY_RUN=1; shift ;;
    --id) [[ $# -ge 2 ]] || { red "--id needs a value"; exit 2; }; ONLY_IDS="${ONLY_IDS:+${ONLY_IDS},}$2"; shift 2 ;;
    --id=*) ONLY_IDS="${ONLY_IDS:+${ONLY_IDS},}${1#--id=}"; shift ;;
    --id-file) [[ $# -ge 2 ]] || { red "--id-file needs a path"; exit 2; }; ONLY_IDS="${ONLY_IDS:+${ONLY_IDS},}$(read_id_file "$2")"; shift 2 ;;
    --id-file=*) ONLY_IDS="${ONLY_IDS:+${ONLY_IDS},}$(read_id_file "${1#--id-file=}")"; shift ;;
    --skip) [[ $# -ge 2 ]] || { red "--skip needs a value"; exit 2; }; SKIP_IDS="${SKIP_IDS:+${SKIP_IDS},}$2"; shift 2 ;;
    --skip=*) SKIP_IDS="${SKIP_IDS:+${SKIP_IDS},}${1#--skip=}"; shift ;;
    --skip-file) [[ $# -ge 2 ]] || { red "--skip-file needs a path"; exit 2; }; SKIP_IDS="${SKIP_IDS:+${SKIP_IDS},}$(read_id_file "$2")"; shift 2 ;;
    --skip-file=*) SKIP_IDS="${SKIP_IDS:+${SKIP_IDS},}$(read_id_file "${1#--skip-file=}")"; shift ;;
    -h|--help) sed -n '1,30p' "$0"; exit 0 ;;
    --*) red "unknown arg: $1"; exit 2 ;;
    *) red "unknown arg: $1"; exit 2 ;;
  esac
done

if [[ -z "$API_KEY" && $DRY_RUN -eq 0 ]]; then
  red "LLM_GATEWAY_API_KEY not set. Refusing to run real model tests."
  red "Use --dry-run to print curl without sending, or export the key."
  exit 2
fi

PASS=0; FAIL=0; SKIP=0; TOTAL=0
: > "$LOG_FILE"

run_case() {
  local case_json="$1"
  local id model endpoint expected
  id=$(jq -r '.id' "$case_json")
  model=$(jq -r '.model' "$case_json")
  endpoint=$(jq -r '.endpoint' "$case_json")
  expected=$(jq -r '.expected // "accept"' "$case_json")
  local env_id="${id//-/_}" override_var="MODEL_OVERRIDE_${id//-/_}"
  local override_model="${!override_var:-}"
  [[ -n "$override_model" ]] && model="$override_model"
  TOTAL=$((TOTAL+1))

  if [[ -n "$ONLY_IDS" && ",$ONLY_IDS," != *",$id,"* ]]; then
    yel "  [$id] SKIP (not selected by ONLY_IDS)"
    SKIP=$((SKIP+1)); return
  fi
  if [[ -n "$SKIP_IDS" && ",$SKIP_IDS," == *",$id,"* ]]; then
    yel "  [$id] SKIP (selected by SKIP_IDS)"
    SKIP=$((SKIP+1)); return
  fi
  if [[ "$expected" == "skip" ]]; then
    local skip_reason
    skip_reason=$(jq -r '.skip_reason // "no reason given"' "$case_json")
    yel "  [$id] SKIP ($skip_reason)"
    SKIP=$((SKIP+1)); return
  fi

  # Build payload by format convention; v1 implementation handles OpenAI
  # /v1/chat/completions natively. Anthropic /v1/messages is shimmed to
  # chat completions internally — for Phase 3 we hit /v1/chat/completions
  # uniformly to keep the script simple.
  local payload
  case "$(jq -r '.format' "$case_json")" in
    openai-image-url)
      payload=$(jq -n --arg m "$model" --arg img "https://upload.wikimedia.org/wikipedia/commons/thumb/3/3a/Cat03.jpg/320px-Cat03.jpg" '{
        model: $m, max_tokens: 50,
        messages: [{role:"user",content:[
          {type:"text",text:"describe in one short sentence"},
          {type:"image_url",image_url:{url:$img}}
        ]}]
      }') ;;
    openai-image-base64)
      local b64
      b64=$(base64 -i "$SAMPLES_DIR/test-image-small.png" | tr -d '\n')
      payload=$(jq -n --arg m "$model" --arg d "$b64" '{
        model: $m, max_tokens: 50,
        messages: [{role:"user",content:[
          {type:"text",text:"describe in one short sentence"},
          {type:"image_url",image_url:{url:("data:image/png;base64,"+$d)}}
        ]}]
      }') ;;
    anthropic-source-base64)
      local b64
      b64=$(base64 -i "$SAMPLES_DIR/test-image-small.png" | tr -d '\n')
      payload=$(jq -n --arg m "$model" --arg d "$b64" '{
        model: $m, max_tokens: 50,
        messages: [{role:"user",content:[
          {type:"text",text:"describe in one short sentence"},
          {type:"image",source:{type:"base64",media_type:"image/png",data:$d}}
        ]}]
      }') ;;
    multipart-audio)
      # Whisper transcription is multipart, not chat completions — handled
      # separately by T-05 branch below to keep shell plumbing simple.
      payload="__WHISPER__" ;;
    *)
      red "  [$id] FAIL: unknown format in case json"
      FAIL=$((FAIL+1)); return ;;
  esac

  if [[ "$payload" == "__WHISPER__" ]]; then
    if [[ $DRY_RUN -eq 1 ]]; then
      yel "  [$id] DRY-RUN: curl POST $GATEWAY_URL/v1/audio/transcriptions -F file=@$SAMPLES_DIR/test-audio-short.mp3 -F model=$model"
    else
      local code body
      body=$(curl -sS --max-time 30 -o /tmp/whisper.out -w '%{http_code}' \
        -H "Authorization: Bearer $API_KEY" \
        -F "file=@$SAMPLES_DIR/test-audio-short.mp3" \
        -F "model=$model" \
        "$GATEWAY_URL/v1/audio/transcriptions" 2>>"$LOG_FILE")
      code=$body
      if [[ "$code" == "200" ]] && [[ -s /tmp/whisper.out ]]; then
        green "  [$id] PASS ($model, http=$code)"
        PASS=$((PASS+1))
      else
        red "  [$id] FAIL ($model, http=$code): $(head -c 200 /tmp/whisper.out)"
        FAIL=$((FAIL+1))
      fi
    fi
    return
  fi

  local cmd
  cmd=(curl -sS --max-time 30 -o /tmp/case.out -w '%{http_code}' \
        -H "Authorization: Bearer $API_KEY" \
        -H 'Content-Type: application/json' \
        --data-binary "$payload" \
        "$GATEWAY_URL$endpoint")

  if [[ $DRY_RUN -eq 1 ]]; then
    yel "  [$id] DRY-RUN: curl POST $GATEWAY_URL$endpoint model=$model (expected=$expected)"
    SKIP=$((SKIP+1)); return
  fi

  local code
  : > /tmp/case.out
  code=$( "${cmd[@]}" 2>>"$LOG_FILE" )
  case "$expected" in
    accept)
      if [[ "$code" == "200" ]] && grep -q '"content"' /tmp/case.out; then
        green "  [$id] PASS ($model, http=$code)"
        PASS=$((PASS+1))
      else
        red "  [$id] FAIL ($model, http=$code): $(head -c 200 /tmp/case.out)"
        FAIL=$((FAIL+1))
      fi ;;
    reject)
      local error_code
      error_code=$(jq -r '.error.code // empty' /tmp/case.out 2>/dev/null || true)
      if [[ "$code" =~ ^(400|422|404)$ ]]; then
        green "  [$id] PASS (expected reject, http=$code)"
        PASS=$((PASS+1))
      elif [[ "$code" == "503" && "$error_code" == "no_candidate" ]]; then
        green "  [$id] PASS (expected reject, http=$code, code=$error_code)"
        PASS=$((PASS+1))
      else
        red "  [$id] FAIL (expected reject, got http=$code): $(head -c 200 /tmp/case.out)"
        FAIL=$((FAIL+1))
      fi ;;
  esac
}

echo "== Phase 3 E2E =="
echo "  gateway: $GATEWAY_URL"
echo "  cases:   $CASES_DIR"
echo "  samples: $SAMPLES_DIR"
echo "  mode:    $([[ $DRY_RUN -eq 1 ]] && echo 'dry-run' || echo 'live')"
echo "  git:     $(git -C "$CASES_DIR/../.." rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "  only:    ${ONLY_IDS:-all}"
echo "  skip:    ${SKIP_IDS:-none}"
for override_var in ${!MODEL_OVERRIDE_T_@}; do
  echo "  override: $override_var=${!override_var}"
done
echo

for case_json in "$CASES_DIR"/T-*.json; do
  [[ -f "$case_json" ]] || continue
  run_case "$case_json"
done

echo
echo "== Summary =="
echo "  pass: $PASS  fail: $FAIL  skip: $SKIP  total: $TOTAL"

[[ $FAIL -eq 0 ]] && exit 0 || exit 1
