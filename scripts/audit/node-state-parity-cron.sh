#!/usr/bin/env bash
# scripts/audit/node-state-parity-cron.sh
#
# P1-D-② 定期调度（audit 2026-10-28 §4「六面写回的对拍门……定期抽样比对」）：
# 每 10 分钟在部署机上跑一次 node-state-parity 检查器（只读），有矛盾时
# 响亮失败（exit 2 透传）并落日志 —— 把「静默失败」变成「响亮失败」。
#
# 检查器本体：scripts/audit/check-node-state-parity.sh（★ 只读：SELECT +
# redis GET，不动任何状态）。行为门 scripts/ursmcheck/node_state_parity_gate_test.go
# 在 CI 钉它的判定逻辑；本 cron 只负责【在生产上周期性真跑 probe 模式】——
# 那是门明确不覆盖的另一半（门不连任何库）。
#
# Cron entry（host-level，参照 columnar-daily-cron.sh 惯例）：
#   *\/10 * * * *  LLM_GATEWAY_DATABASE_URL=<dsn> /opt/scripts/node-state-parity-cron.sh >> /var/log/node-state-parity.log 2>&1
#
# 环境变量：
#   LLM_GATEWAY_DATABASE_URL   必填（DSN；检查器绝不打印它）
#   LLM_GATEWAY_REDIS_ADDR     选填，缺省 127.0.0.1:6379
#   NSP_LOG_FILE               选填，缺省 /var/log/node-state-parity.log
#
# 退出码 = 检查器退出码（0 一致 / 2 有矛盾 / 3 探测失败 / 64 环境错误）。
# 探测失败（3）不告警只记日志 —— DB/Redis 抖动不该制造 paging 噪音；
# 真矛盾（2）才是本 cron 的产出。

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKER="$SCRIPT_DIR/check-node-state-parity.sh"
LOG_FILE="${NSP_LOG_FILE:-/var/log/node-state-parity.log}"

if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
  echo "$(date '+%F %T') [nsp-cron] FATAL: LLM_GATEWAY_DATABASE_URL 未设置" >> "$LOG_FILE"
  exit 64
fi

if [[ ! -x "$CHECKER" ]]; then
  echo "$(date '+%F %T') [nsp-cron] FATAL: 检查器不在位：$CHECKER" >> "$LOG_FILE"
  exit 64
fi

# 检查器把 #meta TSV 打到 stdout、人读的判定打到 stderr；都进日志。
out="$(mktemp "${TMPDIR:-/tmp}/nsp-cron.XXXXXX")"
err="$(mktemp "${TMPDIR:-/tmp}/nsp-cron-err.XXXXXX")"

bash "$CHECKER" probe "${LLM_GATEWAY_DATABASE_URL}" "${LLM_GATEWAY_REDIS_ADDR:-127.0.0.1:6379}" \
  >"$out" 2>"$err"
rc=$?

ts="$(date '+%F %T')"
{
  echo "$ts [nsp-cron] exit=$rc"
  if [[ $rc -eq 0 ]]; then
    grep '^#meta' "$out" | sed "s/^/$ts [nsp-cron] /"
  else
    cat "$err" | sed "s/^/$ts [nsp-cron] /"
    cat "$out" | sed "s/^/$ts [nsp-cron] /"
  fi
} >> "$LOG_FILE"

rm -f "$out" "$err"
exit "$rc"
