#!/usr/bin/env bash
#
# pg-error-classifier.sh — PG 错误日志归属分类器（只读，可被 cron / 采集器消费）
#
# 用途：
#   拉取 docker 容器 llm-gateway-pg 最近 WINDOW_MIN 分钟的日志，抽取 PostgreSQL
#   ERROR 条目，按 docs/2026-09-05-pg-error-audit-and-environment.md §1 的错误
#   模式清单打标为三类：
#     project  — 本项目（llm-gateway-go）相关错误，出现即代表回归，需告警
#     external — 同实例上其他应用（crm 等）的已知噪音，见文档 §1 末段
#     unknown  — 未匹配任何已知模式（新错误，值得人工看一眼）
#
# 用法：
#   pg-error-classifier.sh [WINDOW_MIN]        # 参数优先；否则读环境变量 WINDOW_MIN
#   WINDOW_MIN=1440 pg-error-classifier.sh     # 默认 30 分钟
#
# 环境变量：
#   WINDOW_MIN        回看窗口分钟数（默认 30，参数可覆盖）
#   PG_LOG_CONTAINER  容器名（默认 llm-gateway-pg）
#   OUTPUT            all|human|tsv|jsonl（默认 all，三种输出都打印）
#
# 输出（stdout）：
#   1) 人类可读表
#   2) TSV：明细每类一行 `class<TAB>pattern<TAB>count`，末尾三行汇总
#      `summary<TAB>project_total|external_total|unknown_total<TAB>count`
#   3) JSON Lines：明细 {"class","pattern","count"}，末行汇总对象
#
# 退出码（供 cron / 采集器告警判断）：
#   0  正常（project_total == 0）
#   2  发现本项目相关错误（project_total > 0）→ 应触发告警
#   3  前置条件不满足：无 docker 命令 / docker logs 失败（容器不存在等）/
#      日志输入为空 / WINDOW_MIN 非法。stderr 会给出明确错误信息。
#
# crontab 示例（每 10 分钟）：
#   */10 * * * * /path/to/scripts/monitoring/pg-error-classifier.sh 30 \
#     >> /var/log/pg-error-classifier.log 2>&1   # 退出码 2 时可接告警通道
#
# 维护方式：
#   新增/调整 pattern 时在下方 classify_error() 的规则表中加一行，并注明出处
#   （环境文档 §1 的行号或新的审计文档）。规则自上而下首条命中即生效；project
#   规则应排在 external 之前，避免歧义消息（如 invalid input syntax for type
#   json 同时出现在多个应用的语句里）被抢标。
#
set -euo pipefail

WINDOW_MIN="${1:-${WINDOW_MIN:-30}}"
CONTAINER="${PG_LOG_CONTAINER:-llm-gateway-pg}"
OUTPUT="${OUTPUT:-all}"

die3() {
  echo "pg-error-classifier: $*" >&2
  exit 3
}

# ---------------------------------------------------------------- 前置检查
command -v docker >/dev/null 2>&1 \
  || die3 "docker command not found; PG log classification requires docker"

if ! [[ "$WINDOW_MIN" =~ ^[0-9]+$ ]] || [ "$WINDOW_MIN" -eq 0 ]; then
  die3 "WINDOW_MIN must be a positive integer (got: $WINDOW_MIN)"
fi

# 拉日志（stdout+stderr 合流；PG 容器日志走 stderr）。
log="$(docker logs "$CONTAINER" --since "${WINDOW_MIN}m" 2>&1)" \
  || die3 "docker logs $CONTAINER failed (container missing or docker daemon down)"

[[ -n "$log" ]] || die3 "no log input: container '$CONTAINER' produced no output in the last ${WINDOW_MIN}m"

# ---------------------------------------------------------------- 解析 PG 日志条目
# PG textlog 的同一条目每一行都带独立时间戳前缀，形态：
#   2026-09-04 00:51:58.511 CST [7087] ERROR:  invalid input syntax ...
#   2026-09-04 00:51:58.511 CST [7087] DETAIL:  ...
#   2026-09-04 00:51:58.511 CST [7087] CONTEXT: ...
#   2026-09-04 00:51:58.511 CST [7087] STATEMENT:  INSERT INTO ...（可多行，
#   续行不带时间戳，以制表符缩进）
# 归属歧义消息（invalid input syntax for type json 等）必须看后续 STATEMENT，
# 因此把 ERROR 行之后、下一行非 DETAIL/CONTEXT/STATEMENT 类日志之前的所有内容
# 压平成该条目的 statement 上下文。
# 输出：每条记录一行 `message \x1f statement_flat`（\x1f = ASCII 31 单元分隔符，
# 用 sprintf("%c",31) 生成以保证 awk 可移植性）。
records="$(printf '%s\n' "$log" | awk '
function flush() {
  if (inerr && msg != "") {
    gsub(/\t/, " ", stmt)
    gsub(/[[:space:]]+/, " ", stmt)
    printf "%s%s%s\n", msg, sep, stmt
  }
  inerr = 0; msg = ""; stmt = ""
}
BEGIN { sep = sprintf("%c", 31) }
{
  line = $0
  sub(/\r$/, "", line)
  if (line ~ /^[0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}:[0-9]{2}/) {
    if (inerr && line ~ /\] (DETAIL|CONTEXT|STATEMENT|QUERY|HINT|ERROR CONTEXT):/) {
      # 同一条 ERROR 的附属行：剥掉 "<ts> <pid> KEYWORD:  " 前缀后并入 statement
      sub(/^[0-9-]+ [0-9:.]+ [A-Za-z]+ \[[0-9]+\] [A-Z ]+: */, "", line)
      stmt = stmt " " line
    } else {
      flush()
      if (line ~ /\] ERROR:  /) {
        inerr = 1
        idx = index(line, "ERROR:  ")
        msg = substr(line, idx + 8)   # 跳过 "ERROR:  "（8 字符），避免前导空格
      }
    }
  } else if (inerr) {
    # 多行 STATEMENT 的无时间戳续行
    stmt = stmt " " line
  }
}
END { flush() }
')"

# ---------------------------------------------------------------- 分类规则
# classify_error <message> <statement>
# 输出 `class<TAB>pattern`。规则出处见各行注释（环境文档行号）。
# 维护：新 pattern 加一个 elif 分支 + 出处注释 + unique pattern 名。
classify_error() {
  local msg="$1" stmt="$2"

  # --- project（本项目，出现即回归） -------------------------------------
  # §1 表行1（L12）/ §3.1（L67）：citus columnar 分区 × 统一视图合成列 source，
  # 消息形如 "cache lookup failed for attribute source of relation 1148425"。
  if printf '%s' "$msg" | grep -qE 'cache lookup failed for attribute [^ ]+ of relation'; then
    echo -e 'project\tcitus_columnar_source_column'
    return
  fi
  # §1 表行3（L14）：model_integrity_events 写 jsonb 列收到非 JSON 文本
  # （历史根因：pgx SimpleProtocol 下 []byte 被内联为 bytea hex）。
  # 该消息文本在 external 噪音里也出现，必须用 STATEMENT 归属到本表。
  if printf '%s' "$msg" | grep -qF 'invalid input syntax for type json' \
    && printf '%s' "$stmt" | grep -qE 'INSERT INTO[[:space:]]+(public\.)?model_integrity_events'; then
    echo -e 'project\tmodel_integrity_events_json_syntax'
    return
  fi
  # §1 表行2（L13）：auto_route_selections 分区函数/关系缺失
  # （历史根因：修复序列单标记静默吞掉后追加的 650/656）。
  if printf '%s' "$msg" | grep -qE 'function (ensure|promote)_auto_route_selections_partition.*does not exist'; then
    echo -e 'project\tauto_route_partition_function_missing'
    return
  fi
  # §1 表行2（L13）：relation "auto_route_selections_hot|all|月分区" does not exist
  if printf '%s' "$msg" | grep -qE 'relation "auto_route_selections_(hot|all|[0-9]{4}_[0-9]{2})" does not exist'; then
    echo -e 'project\tauto_route_relation_missing'
    return
  fi
  # 兜底（§1 "等"）：语句命中本项目核心对象但消息未被上面规则覆盖时仍归 project，
  # 避免新型回归被静默归入 unknown。对象清单 = 文档 §2.4 关键 schema。
  if printf '%s' "$stmt" | grep -qE 'INSERT INTO[[:space:]]+(public\.)?(model_integrity_events|candidate_failure_logs|provider_error_details|provider_error_aggregator_state|auto_route_selections|tuning_proposals|fault_events)([[:space:](]|$)' \
    || printf '%s' "$stmt" | grep -qE '(UPDATE|FROM)[[:space:]]+(public\.)?(provider_error_details|provider_error_aggregator_state|candidate_failure_logs_unified)([[:space:](]|$)'; then
    echo -e 'project\tproject_object_sql_error'
    return
  fi

  # --- external（其他应用的已知噪音，§1 末段 L17） -------------------------
  # employees 表噪音一：列不存在（crm）
  if printf '%s' "$msg" | grep -qF 'column e.employee_id does not exist'; then
    echo -e 'external\tcrm_employees_column_missing'
    return
  fi
  # employees 表噪音二：同列多次赋值（crm agent 心跳 upsert）
  if printf '%s' "$msg" | grep -qF 'multiple assignments to same column "last_heartbeat_at"'; then
    echo -e 'external\tcrm_employees_multiple_assignment'
    return
  fi
  # daily_kline：列 "days" 不存在
  if printf '%s' "$msg" | grep -qF 'column "days" does not exist'; then
    echo -e 'external\tdaily_kline_days_column'
    return
  fi
  # llm_usage_records 关系不存在
  if printf '%s' "$msg" | grep -qF 'relation "llm_usage_records" does not exist'; then
    echo -e 'external\tllm_usage_records_missing'
    return
  fi
  # agent_groups 关系不存在
  if printf '%s' "$msg" | grep -qF 'relation "agent_groups" does not exist'; then
    echo -e 'external\tagent_groups_missing'
    return
  fi
  # audit_logs RLS 拒绝
  if printf '%s' "$msg" | grep -qE 'row-level security policy for table "audit_logs"'; then
    echo -e 'external\taudit_logs_rls_denied'
    return
  fi
  # search_path 未设置即建对象
  if printf '%s' "$msg" | grep -qF 'no schema has been selected to create in'; then
    echo -e 'external\tno_schema_selected'
    return
  fi
  # 语句超时被取消（多应用共享实例，归属不明，按 §1 归 external）
  if printf '%s' "$msg" | grep -qF 'canceling statement due to user request'; then
    echo -e 'external\tcanceling_statement'
    return
  fi

  # 未匹配任何已知模式
  echo -e 'unknown\tunclassified'
}

# ---------------------------------------------------------------- 计数
tmp="$(mktemp)"
trap 'rm -f "$tmp" "$tmp.rec" "$tmp.cls" "$tmp.cnt"' EXIT
printf '%s\n' "$records" > "$tmp.rec"

: > "$tmp.cls"
while IFS= read -r rec; do
  [ -n "$rec" ] || continue
  msg="${rec%%$'\x1f'*}"
  stmt="${rec#*$'\x1f'}"
  classify_error "$msg" "$stmt" >> "$tmp.cls"
done < "$tmp.rec"

# `count class pattern`
sort "$tmp.cls" | uniq -c | awk '{c=$1; sub(/^[[:space:]]*[0-9]+[[:space:]]*/, ""); print c "\t" $0}' > "$tmp.cnt"

proj_total="$(awk -F'\t' '$2=="project"{s+=$1} END{print s+0}' "$tmp.cnt")"
ext_total="$(awk -F'\t' '$2=="external"{s+=$1} END{print s+0}' "$tmp.cnt")"
unk_total="$(awk -F'\t' '$2=="unknown"{s+=$1} END{print s+0}' "$tmp.cnt")"
err_count=$((proj_total + ext_total + unk_total))

# ---------------------------------------------------------------- 输出
show_human() {
  echo "== PG ERROR classification (container=$CONTAINER window=${WINDOW_MIN}m errors=$err_count) =="
  if [ "$err_count" -eq 0 ]; then
    echo "  (no ERROR entries in window — healthy per doc §2.5 baseline)"
  else
    printf '  %6s  %-8s  %s\n' COUNT CLASS PATTERN
    sort -t$'\t' -k1,1rn "$tmp.cnt" | while IFS=$'\t' read -r cnt cls pat; do
      printf '  %6s  %-8s  %s\n' "$cnt" "$cls" "$pat"
    done
  fi
  printf '  %-8s  project=%s external=%s unknown=%s\n' TOTAL "$proj_total" "$ext_total" "$unk_total"
}

show_tsv() {
  echo "== TSV =="
  awk -F'\t' '{print $2 "\t" $3 "\t" $1}' "$tmp.cnt"
  printf 'summary\tproject_total\t%s\n' "$proj_total"
  printf 'summary\texternal_total\t%s\n' "$ext_total"
  printf 'summary\tunknown_total\t%s\n' "$unk_total"
}

show_jsonl() {
  echo "== JSONL =="
  awk -F'\t' '{printf "{\"class\":\"%s\",\"pattern\":\"%s\",\"count\":%s}\n", $2, $3, $1}' "$tmp.cnt"
  printf '{"project_total":%s,"external_total":%s,"unknown_total":%s}\n' \
    "$proj_total" "$ext_total" "$unk_total"
}

case "$OUTPUT" in
  human) show_human ;;
  tsv) show_tsv ;;
  jsonl) show_jsonl ;;
  all) show_human; echo; show_tsv; echo; show_jsonl ;;
  *) die3 "OUTPUT must be one of: all|human|tsv|jsonl (got: $OUTPUT)" ;;
esac

# 退出码：project 相关错误存在 → 2（告警）；否则 0。
if [ "$proj_total" -gt 0 ]; then
  exit 2
fi
exit 0
