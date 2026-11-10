#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────
# node_probe_state 连败计数存量卫生（节点状态链断链⑧，2026-11-11）
#
# 背景：MNF/transient 塌缩根修（f7369de4f + df2720495，build_seq 3000 起）
# 只保证「排程不再塌回 5 分钟」，不回溯已膨胀的 consecutive_failures。
# 生产实测（211，2026-11-11 00:56+08）：cf≥1000 仍 12 行 / cf≥100 共 32 行
# （cred 29 与 99023 为主，http_429/503 瞬态族在旧 5 分钟窗口下累积）。
#
# 本脚本把 cf 超过阈值的行**封顶到持续档阈值（21）**：
#   - 语义无损：全仓 cf 消费面只有 `> 0` 与 `>= 持续阈值`（21）两档
#     （bg/probe_policy.go:96、bg/credential_recovery.go:779、
#      executor.go:2977 的 sustained CASE）⇒ 封顶后仍留在持续档。
#   - next_retry_at 一律不动：新代码已在给这些行 30min~1h 窗，
#     缩短排程只会制造无谓探测。
#   - paused / manual 状态不动。
#
# 用法：
#   bash scripts/audit/node-probe-state-cf-hygiene.sh [--apply]
#     [--threshold N]   # 触发阈值，默认 100
#     [--cap N]         # 封顶值，默认 21（= probe.sustained_failure_attempt_threshold）
#   默认 dry-run：只打印受影响行与计划 UPDATE。--apply 才执行。
#
# 退出码：0=正常（dry-run 或 apply 成功）；3=连不上 DB。
# ─────────────────────────────────────────────────────────────────────
set -euo pipefail

THRESHOLD=100
CAP=21
APPLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --threshold) THRESHOLD="$2"; shift ;;
    --cap) CAP="$2"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 64 ;;
  esac
  shift
done

# DB 通道：与 negcontrol/判定器同款 —— 211 宿主上 docker exec acc-pg17。
# 本机直连容器网络不可达（§653 实证）。
run_sql() {
  if [ -n "${GW_HOST:-}" ]; then
    ssh -o BatchMode=yes -o ConnectTimeout=8 "$GW_HOST" \
      "docker exec -i ${GW_PG_CONTAINER:-acc-pg17} psql -U ${GW_PG_USER:-acc_super} -d ${GW_PG_DB:-llm_gateway} -Atc \"$1\""
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${GW_PG_CONTAINER:-acc-pg17}$"; then
    docker exec -i "${GW_PG_CONTAINER:-acc-pg17}" psql -U "${GW_PG_USER:-acc_super}" -d "${GW_PG_DB:-llm_gateway}" -Atc "$1"
  else
    echo "no acc-pg17 reachable (set GW_HOST=211 or run on 211)" >&2
    exit 3
  fi
}

echo "== node_probe_state cf hygiene: threshold=${THRESHOLD} cap=${CAP} apply=${APPLY} =="
echo "-- 受影响行（前 30）--"
run_sql "SELECT credential_id, left(raw_model_name,32), consecutive_failures, paused, left(coalesce(last_err_code,''),20), next_retry_at FROM node_probe_state WHERE consecutive_failures >= ${THRESHOLD} ORDER BY consecutive_failures DESC LIMIT 30"
echo "-- 计数 --"
run_sql "SELECT count(*) FROM node_probe_state WHERE consecutive_failures >= ${THRESHOLD}"

if [ "$APPLY" -ne 1 ]; then
  echo
  echo "DRY-RUN. 计划执行的 UPDATE："
  echo "  UPDATE node_probe_state SET consecutive_failures = ${CAP}, updated_at = now() WHERE consecutive_failures >= ${THRESHOLD};"
  echo "加 --apply 执行。"
  exit 0
fi

echo "-- APPLY --"
run_sql "UPDATE node_probe_state SET consecutive_failures = ${CAP}, updated_at = now() WHERE consecutive_failures >= ${THRESHOLD}"
echo "-- 复核（触发阈值以上的行数，应为 0）--"
run_sql "SELECT count(*) FROM node_probe_state WHERE consecutive_failures >= ${THRESHOLD}"
echo "done."
