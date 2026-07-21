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

set -uo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
API_KEY="${LLM_GATEWAY_API_KEY:-}"
SAMPLES_DIR="$(cd "$(dirname "$0")/../../docs/multimodal-testing/samples" && pwd)"
CASES_DIR="$(cd "$(dirname "$0")" && pwd)/cases"
LOG_FILE="${LOG_FILE:-/tmp/multimodal-e2e-phase3.log}"

DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
yel()   { printf '\033[33m%s\033[0m\n' "$*"; }

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
  TOTAL=$((TOTAL+1))

  if [[ "$expected" == "skip" ]]; then
    yel "  [$id] SKIP (declared in case.expected)"
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
      if [[ "$code" =~ ^(400|422|404)$ ]]; then
        green "  [$id] PASS (expected reject, http=$code)"
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
echo

for case_json in "$CASES_DIR"/T-*.json; do
  [[ -f "$case_json" ]] || continue
  run_case "$case_json"
done

echo
echo "== Summary =="
echo "  pass: $PASS  fail: $FAIL  skip: $SKIP  total: $TOTAL"

[[ $FAIL -eq 0 ]] && exit 0 || exit 1
