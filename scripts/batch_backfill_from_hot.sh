#!/usr/bin/env bash
# Batch backfill script for Session V2 validation
# Backfills multiple sessions from request_logs_bodies_hot table

set -euo pipefail

DSN="${DSN:-postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable}"
BACKFILL_TOOL="${BACKFILL_TOOL:-/tmp/backfill_session_bodies}"
DRY_RUN="${DRY_RUN:-false}"

# Test sessions with complete bodies (from hot table query)
SESSIONS=(
  "gt_gw_e40ad078-2f86-4cbf-a8f6-005a84764b19"
  "gt_gw_485c2f31-6df7-464e-885f-7021567efcd5"
  "gt_gw_9ab1107f-ad06-4336-a033-997b8d9ea7e8"
  "gt_gw_5590a519-32f7-4701-8c86-de5678f4a3f6"
  "gt_gw_951adaab-ee04-4909-84fe-c59fbedffe7e"
  "gt_gw_178fd758-8c37-424e-a0df-9dcd59ad4ff4"
  "gt_gw_8017f1f4-d52c-46eb-aff2-fd30b236165d"
  "gt_gw_91e25118-c248-49dc-b4e5-1772afc1774b"
  "gt_gw_df5fd271-df4c-4a5d-b5ee-7438b7855a2e"
  "gt_gw_dc273e2b-0cf5-4c41-b4ed-1f185f71d7d3"
)

echo "=========================================="
echo "Batch Backfill Session Bodies"
echo "=========================================="
echo "Total sessions: ${#SESSIONS[@]}"
echo "Dry-run: $DRY_RUN"
echo "Tool: $BACKFILL_TOOL"
echo ""

success_count=0
fail_count=0
total_time=0

for session in "${SESSIONS[@]}"; do
  echo "----------------------------------------"
  echo "Session: $session"
  
  start=$(date +%s%N)
  
  if "$BACKFILL_TOOL" --dsn="$DSN" --tenant="default" --session="$session" --dry-run="$DRY_RUN" --use-hot=true; then
    end=$(date +%s%N)
    elapsed=$((($end - $start) / 1000000))  # Convert to milliseconds
    total_time=$(($total_time + $elapsed))
    success_count=$(($success_count + 1))
    echo "✓ SUCCESS (${elapsed}ms)"
  else
    end=$(date +%s%N)
    elapsed=$((($end - $start) / 1000000))
    total_time=$(($total_time + $elapsed))
    fail_count=$(($fail_count + 1))
    echo "✗ FAILED (${elapsed}ms)"
  fi
  echo ""
done

echo "=========================================="
echo "Batch Backfill Summary"
echo "=========================================="
echo "Total sessions: ${#SESSIONS[@]}"
echo "Success: $success_count"
echo "Failed: $fail_count"
echo "Total time: ${total_time}ms"
if [ $success_count -gt 0 ]; then
  avg_time=$(($total_time / $success_count))
  echo "Average time per session: ${avg_time}ms"
fi
echo ""

exit $fail_count
