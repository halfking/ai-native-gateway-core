#!/usr/bin/env bash
# check-node-state-parity.sh —— 节点冷却语义「多状态面对拍」检查器（★ 只读）
#
# # 它查什么（P1-D-②，audit 2026-10-28 §4「六面写回的对拍门」）
#
# 同一个「这个节点现在不能路由」的判定，本仓有三个**各自独立**的存储面：
#
#   ① Redis NodeState   llmgw:cred_fp_node:<cred>:<model>
#      （JSON：disabled / disabled_until / disable_count —— 路由热路径权威）
#   ② DB node_probe_state
#      （next_retry_at > now() ⇒ 探针退避阶梯在生效；paused ⇒ 永久停泊）
#   ③ DB credentials.circuit_state = 'open'
#      （内存 breaker 的 DB 镜像 —— DBStateSync 异步 last-writer-wins）
#
# ★★★ 三面**没有共享的事务**，写入方也各不相同（探针 worker / 路由热路径 Lua /
#   DBStateSync 异步队列）⇒ ★ 一面禁了、另一面没禁，就是「退避被架空」或
#   「静默下线」的形态 —— R149（mirror 写失败致 45s holdoff 架空 5min/6h 阶梯）
#   就是其中一面的静默失败实证。
#
# # 对拍什么（★ 粒度对齐是本检查器的全部难点）
#
# 三面粒度不同：① (credential, model)；② (credential, raw_model_name)；
#   ③ (provider, credential) 凭据级。⇒ 只对拍「能归一到同一粒度」的断言：
#
#   类别 P（probe 面 vs NodeState 面，(cred, model) 粒度）：
#     DB next_retry_at 在未来 ≥60s（退避真实在生效）**且** paused = f
#     ⇒ Redis NodeState 应当知道这个节点在退避（disabled=t 或 disabled_until
#     在未来）。★ 60s 容差：两面写入方不同步，短退避（5s~60s）在采样瞬间
#     一面刚到期另一面还没翻转是正常形态，不算矛盾。
#
#   类别 Q（breaker 面 vs NodeState 面，凭据级 ⇒ 投影到该凭据全部模型）：
#     credentials.circuit_state = 'open' **且** availability 各族仍在
#     服务（quota 未耗尽、未 suspended）⇒ 该凭据在 Redis 里应当存在
#     ≥1 条 disabled/disabled_until 在未来的 NodeState。
#     ★ 反向不成立（NodeState 有禁用 ⇏ breaker open）：(cred, model) 粒度的
#     禁用本来就不该翻到凭据级 breaker —— 那是设计，不是漂移。
#
# # 用法
#
#   bash scripts/audit/check-node-state-parity.sh probe [DSN] [REDIS_ADDR]
#   bash scripts/audit/check-node-state-parity.sh classify < 清单.tsv
#
# ★ DSN 缺省取 LLM_GATEWAY_DATABASE_URL；REDIS_ADDR 缺省 127.0.0.1:6379。
# ★ **绝不打印 DSN、口令或 Redis 密码**。
#
# # 退出码
#
#   0 = 三面对拍一致（没有跨面矛盾）
#   2 = 有矛盾（P 或 Q 类非空）
#   3 = 探测失败（DB / Redis 不可达）
#   64 = 用法/环境错误
#
# # ★ 诚实边界
#
#   ★ 本检查器只报**矛盾**，★ **不改任何状态**（★ 不重置退避、★ 不动 breaker）。
#   ★ 采样是**瞬间快照**：两面在同一秒内各自翻转产生的竞态窗口会以容差吸收，
#     吸收不掉的持续矛盾才是本门要抓的。
set -uo pipefail

MODE="${1:-}"
DSN="${2:-${LLM_GATEWAY_DATABASE_URL:-}}"
REDIS_ADDR="${3:-${LLM_GATEWAY_REDIS_ADDR:-127.0.0.1:6379}}"

usage() {
  cat >&2 <<'USAGE'
用法:
  check-node-state-parity.sh probe [DSN] [REDIS_ADDR]   # 只读探库+探Redis，输出 TSV 清单
  check-node-state-parity.sh classify < 清单.tsv        # 对清单分类（门用；不需要任何连接）
退出码: 0 一致 / 2 有矛盾 / 3 探测失败 / 64 用法错误
★ 只读：不重置任何状态、不动 breaker、不写 Redis。
USAGE
}

# ─────────────── probe：DB 面（一条只读 SQL 产出对拍底表） ───────────────
#
# 底表每行 = 一个 (credential_id, raw_model_name) 的退避现状，附上凭据级
# breaker 镜像与 availability 族，供 classify 与 Redis 面对拍。
#
# ★ 类别判定需要的 Redis 值在 probe 模式里逐行抓（redis-cli GET），SQL 只负责
#   DB 侧；**判定逻辑全部在 classify**（与查库分开 ⇒ 门不需要真库）。
PROBE_SQL="WITH probe_face AS (
  SELECT n.credential_id,
         n.raw_model_name,
         n.paused,
         n.next_retry_at,
         EXTRACT(EPOCH FROM (n.next_retry_at - now()))::bigint AS next_retry_in_sec,
         c.provider_id,
         COALESCE(c.circuit_state, '') AS circuit_state,
         COALESCE(c.quota_state, '')   AS quota_state,
         COALESCE(c.availability_state, '') AS availability_state
  FROM node_probe_state n
  JOIN credentials c ON c.id = n.credential_id
  WHERE n.paused = false
    AND n.next_retry_at > now() + interval '60 seconds'
)
SELECT credential_id, raw_model_name, next_retry_in_sec, provider_id,
       circuit_state, quota_state, availability_state
FROM probe_face
ORDER BY credential_id, raw_model_name;"

probe_db() {
  local dsn="$1" raddr="$2"
  command -v psql >/dev/null 2>&1 || { printf 'FATAL: PATH 里没有 psql\n' >&2; exit 64; }
  command -v redis-cli >/dev/null 2>&1 || { printf 'FATAL: PATH 里没有 redis-cli\n' >&2; exit 64; }
  [[ -n "$dsn" ]] || { printf 'FATAL: 未提供 DSN（参数 2 或 LLM_GATEWAY_DATABASE_URL）\n' >&2; exit 64; }

  local rows
  rows="$(psql "$dsn" -v ON_ERROR_STOP=1 -At -F $'\t' -c "$PROBE_SQL" 2>&1)" || {
    printf 'FATAL: DB 探测失败：%s\n' "$rows" >&2
    exit 3
  }
  [[ -z "$rows" ]] && { printf '#meta\tprobe_rows\t0\n'; exit 0; }

  printf '#meta\tprobe_rows\t%s\n' "$(printf '%s\n' "$rows" | wc -l | tr -d ' ')"
  printf 'credential_id\traw_model_name\tnext_retry_in_sec\tprovider_id\tcircuit_state\tquota_state\tavailability_state\tredis_disabled\tredis_disabled_until_in_sec\n'
  while IFS=$'\t' read -r cred model next_in provider ckt quota avail; do
    [[ -z "$cred" ]] && continue
    local raw dflag duntil duntil_in
    raw="$(redis-cli -h "${raddr%%:*}" -p "${raddr##*:}" --raw GET "llmgw:cred_fp_node:${cred}:${model}" 2>/dev/null || true)"
    dflag=0; duntil_in=-999999
    if [[ -n "$raw" ]]; then
      # NodeState 是 JSON；只抽对拍需要的两个键（python3 json，容错损坏值）
      if command -v python3 >/dev/null 2>&1; then
        read -r dflag duntil_in < <(printf '%s' "$raw" | python3 -c '
import json, sys, time
try:
    s = json.load(sys.stdin)
    d = 1 if s.get("disabled") else 0
    u = int(s.get("disabled_until") or 0) - int(time.time())
    print(d, u)
except Exception:
    print(0, -999999)
' 2>/dev/null || printf '0\t-999999')
      fi
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$cred" "$model" "$next_in" "$provider" "$ckt" "$quota" "$avail" "$dflag" "$duntil_in"
  done <<< "$rows"
  exit 0
}

# ─────────────── classify（★ 判定逻辑与查库分开 ⇒ 门不需要真库） ───────────────
classify_file() {
  local src="$1"
  local n p q verdict code
  # 输入列：1 cred 2 model 3 next_retry_in_sec 4 provider 5 circuit_state
  #         6 quota_state 7 availability_state 8 redis_disabled 9 redis_disabled_until_in_sec
  n="$(awk 'BEGIN{FS="\t"} !/^#/{c++} END{print c+0}' "$src" 2>/dev/null)"
  # P 类：DB 退避 ≥60s 在未来，NodeState 却完全不知道（没 disabled 且
  # disabled_until 不在未来）。
  p="$(awk 'BEGIN{FS="\t"} !/^#/ && $3+0>=60 && $8+0==0 && $9+0<=0 {c++} END{print c+0}' "$src" 2>/dev/null)"
  # Q 类：凭据级 breaker 开路（circuit_state=open）且凭据在服务（quota 非耗尽、
  # availability 非 suspended），但该凭据**没有任何一行** NodeState 禁用证据。
  # 用 awk 两遍：先收集 open 凭据，再查它们有没有任一行禁用。
  q="$(awk -v src="$src" 'BEGIN{FS="\t"}
    !/^#/ && $5=="open" && $6 !~ /exhausted/ && $7 != "suspended" { open_cred[$1]=1 }
    END {
      close(src)
      while ((getline line < src) > 0) {
        split(line, f, "\t")
        if (f[1] in open_cred && (f[8]+0==1 || f[9]+0>0)) delete open_cred[f[1]]
      }
      c=0; for (k in open_cred) c++
      print c+0
    }' "$src" 2>/dev/null)"

  if [[ $(( p + q )) -gt 0 ]]; then verdict="inconsistent"; code=2
  else verdict="consistent"; code=0; fi

  {
    printf '════════ 节点冷却语义「多状态面对拍」检查 ════════\n'
    printf '判定          : %s\n' "$verdict"
    printf '退出码        : %s\n' "$code"
    printf '对拍行数      : %s\n' "${n:-0}"
    printf '── 按断言分类（★ 单一真值源在 classify 的类别定义）──\n'
    printf '  P DB 退避在生效而 NodeState 不知道（退避面被架空形态）: %s\n' "${p:-0}"
    printf '      ⇒ ★ R149 同族：退避阶梯只在 DB 面推进，路由热路径照常打\n'
    printf '  Q breaker 开路而全凭据无 NodeState 禁用证据（静默下线形态）: %s\n' "${q:-0}"
    printf '      ⇒ ★ breaker 面 open 但热路径权威面零痕迹 ⇒ 检查 DBStateSync 队列丢包\n'
    if [[ "$code" -ne 0 ]]; then
      printf '判读：★ 两类矛盾指向不同的写入方（探针 mirror / DBStateSync），先看本检查器\n'
      printf '      输出的对应面最近是否有写失败（llmgw_node_probe_mirror_write_failed_total /\n'
      printf '      DBStateSync dropped 计数）。本检查器只读，不动手。\n'
    else
      printf '判读：★ 没有发现跨状态面矛盾。\n'
    fi
  } >&2
  printf '#meta\tverdict\t%s\n' "$verdict"
  printf '#meta\texit_code\t%s\n' "$code"
  printf '#meta\tparity_rows\t%s\n' "${n:-0}"
  printf '#meta\tclass_P_backoff_unseen\t%s\n' "${p:-0}"
  printf '#meta\tclass_Q_open_without_trace\t%s\n' "${q:-0}"
  cat "$src"
  # ★★ 退出码必须反映判定（★ cat 的 0 不得吃掉 classify 的判定码 ——
  # crc 检查器的同款收尾：★ 「不许静默降级」的落点是退出码）。
  return "$code"
}

case "$MODE" in
  probe)
    probe_db "$DSN" "$REDIS_ADDR"
    exit $?
    ;;
  classify)
    # ★★ 这里不能用 `local`（本分支在顶层 case 里、不在函数内）——
    # bash3.2 + set -u 下会直接 unbound 崩溃（crc 检查器同坑实录）。
    snap="$(mktemp "${TMPDIR:-/tmp}/nsp-snap.XXXXXX")" || { printf 'FATAL: 建临时文件失败\n' >&2; exit 64; }
    cat > "$snap"
    classify_file "$snap"
    crc=$?
    rm -f "$snap"
    exit "$crc"
    ;;
  -h|--help|help) usage; exit 0 ;;
  *) printf 'FATAL: 未知模式（可用：probe / classify / --help）\n' >&2; usage; exit 64 ;;
esac
