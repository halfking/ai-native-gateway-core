#!/usr/bin/env bash
# Verify the metric contract for a URSM v2 rollout stage.
# This command only reads /metrics or an archived exposition file.

set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: verify-ursm-v2-rollout.sh --stage shadow|canary|authoritative [--url URL --token TOKEN | --metrics-file FILE]
USAGE
  exit 2
}

stage=''
url=''
token=''
metrics_file=''

while (($#)); do
  case "$1" in
    --stage) [[ $# -ge 2 ]] || usage; stage=$2; shift 2 ;;
    --url) [[ $# -ge 2 ]] || usage; url=$2; shift 2 ;;
    --token) [[ $# -ge 2 ]] || usage; token=$2; shift 2 ;;
    --metrics-file) [[ $# -ge 2 ]] || usage; metrics_file=$2; shift 2 ;;
    -h|--help) usage ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage ;;
  esac
done

case "$stage" in
  shadow|canary|authoritative) ;;
  *) printf '%s\n' '--stage must be shadow, canary, or authoritative' >&2; usage ;;
esac

if [[ -n "$metrics_file" && -n "$url" ]]; then
  printf '%s\n' '--metrics-file and --url are mutually exclusive' >&2
  exit 2
fi
if [[ -z "$metrics_file" && -z "$url" ]]; then
  printf '%s\n' 'one of --metrics-file or --url is required' >&2
  exit 2
fi

if [[ -n "$metrics_file" ]]; then
  [[ -r "$metrics_file" ]] || { printf 'metrics file is not readable: %s\n' "$metrics_file" >&2; exit 1; }
  metrics=$(<"$metrics_file")
else
  curl_args=(-fsS --max-time 15)
  if [[ -n "$token" ]]; then
    curl_args+=(-H "Authorization: Bearer $token")
  fi
  metrics=$(curl "${curl_args[@]}" "${url%/}/metrics") || {
    printf 'failed to fetch metrics from %s/metrics\n' "${url%/}" >&2
    exit 1
  }
fi

metric_line() {
  local name=$1 label_name=$2 label_value=$3
  printf '%s\n' "$metrics" | awk -v name="$name" -v label_name="$label_name" -v label_value="$label_value" '
    $0 ~ "^" name "\\{" {
      pattern = label_name "=\\\"" label_value "\\\""
      if ($0 ~ pattern) { print; found = 1 }
    }
    END { exit(found ? 0 : 1) }
  '
}

metric_value() {
  local name=$1 label_name=$2 label_value=$3 line
  line=$(metric_line "$name" "$label_name" "$label_value") || return 1
  awk '{print $NF}' <<<"$line"
}

require_metric() {
  local name=$1 label_name=$2 label_value=$3 line
  if ! line=$(metric_line "$name" "$label_name" "$label_value"); then
    printf 'FAIL missing metric label: %s{%s="%s"}\n' "$name" "$label_name" "$label_value" >&2
    return 1
  fi
  printf '%s\n' "$line"
}

failures=0
current_failures=0
require_zero() {
  local name=$1 label_name=$2 label_value=$3 value
  if ! value=$(metric_value "$name" "$label_name" "$label_value"); then
    return 0
  fi
  if awk "BEGIN { exit !($value > 0) }"; then
    printf 'FAIL current metric is nonzero: %s{%s="%s"}=%s\n' "$name" "$label_name" "$label_value" "$value" >&2
    current_failures=$((current_failures + 1))
  fi
}


case "$stage" in
  shadow)
    for result in recorded skipped failed; do
      require_metric llm_gateway_ursm_v2_shadow_records_total result "$result" || failures=$((failures + 1))
    done
    for diff in identical availability order top1 error not_ready; do
      require_metric ursm_shadow_diff_total type "$diff" || failures=$((failures + 1))
    done
    require_zero llm_gateway_ursm_v2_shadow_records_total result failed
    for diff in availability order top1 error not_ready; do
      require_zero ursm_shadow_diff_total type "$diff"
    done
    # These historical labels must never be accepted as evidence. The code emits
    # availability/order; accepting aliases would make a missing series look zero.
    if printf '%s\n' "$metrics" | grep -Eq '^ursm_shadow_diff_total\{[^}]*type="(availability_mismatch|order_mismatch)"'; then
      printf 'WARN legacy mismatch labels found; use availability/order instead\n' >&2
    fi
    ;;
  canary)
    require_metric routing_state_source_total source canary || failures=$((failures + 1))
    require_metric routing_state_source_total source fallback || failures=$((failures + 1))
    for result in recorded failed; do
      require_metric llm_gateway_ursm_v2_shadow_records_total result "$result" || failures=$((failures + 1))
    done
    require_zero routing_state_source_total source fallback
    require_zero llm_gateway_ursm_v2_shadow_records_total result failed
    ;;
  authoritative)
    require_metric routing_state_source_total source authoritative || failures=$((failures + 1))
    require_metric routing_state_source_total source fallback || failures=$((failures + 1))
    require_zero routing_state_source_total source fallback
    ;;
esac

if ((failures > 0 || current_failures > 0)); then
  printf 'FAIL rollout evidence is unsafe or incomplete (%d missing series, %d nonzero failure counters)\n' "$failures" "$current_failures" >&2
  exit 1
fi
printf 'PASS metric contract is complete and current scrape has no rollout failures; use Prometheus increase() queries for final GO/NO-GO.\n'
