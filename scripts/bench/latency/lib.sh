#!/usr/bin/env bash
# scripts/bench/latency/lib.sh — 公共校验（生产主机硬拒绝）
# shellcheck shell=bash
#
# Callers: run_smoke.sh (source); operators exporting GW_BASE before k6/python.
# No data-file I/O. User instruction: only add scripts/bench/latency/; no prod loadtest.

LATENCY_BENCH_BLOCKED_HOSTS=(
  14.103.112.184
  14.103.174.71
  120.78.136.252
  47.97.111.154
  120.79.47.154
  8.136.114.245
  llm.itestu.cn
  llm.kxpms.cn
)

latency_bench_url_host() {
  local url="${1:-}"
  url="${url#http://}"
  url="${url#https://}"
  url="${url%%/*}"
  url="${url##*@}"
  url="${url%%:*}"
  printf '%s' "$url"
}

latency_bench_assert_safe_url() {
  local label="$1"
  local url="$2"
  if [[ -z "$url" ]]; then
    return 0
  fi
  local host
  host="$(latency_bench_url_host "$url")"
  if [[ -z "$host" ]]; then
    echo "[latency-bench] FAIL: ${label} 无法解析主机: ${url}" >&2
    return 1
  fi
  local blocked
  for blocked in "${LATENCY_BENCH_BLOCKED_HOSTS[@]}"; do
    if [[ "$host" == "$blocked" ]] || [[ "$host" == *".$blocked" ]]; then
      echo "[latency-bench] REFUSED: ${label}=${url} 命中生产/禁压主机 ${blocked}" >&2
      echo "[latency-bench] 只允许 127.0.0.1 / localhost / 私有网段本地试验环境。" >&2
      return 1
    fi
  done
  if [[ "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    case "$host" in
      127.*|10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[0-1].*) ;;
      *)
        echo "[latency-bench] REFUSED: ${label}=${url} 看起来是公网 IP，禁止压测。" >&2
        return 1
        ;;
    esac
  fi
  return 0
}

latency_bench_assert_safe_env() {
  latency_bench_assert_safe_url GW_BASE "${GW_BASE:-}" || return 1
  latency_bench_assert_safe_url UPSTREAM_BASE "${UPSTREAM_BASE:-}" || return 1
  latency_bench_assert_safe_url DIRECT_BASE "${DIRECT_BASE:-}" || return 1
  return 0
}
