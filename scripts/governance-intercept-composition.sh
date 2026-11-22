#!/bin/bash
# governance-intercept-composition.sh —— 治理链**拦截构成**取数口径
# （2026-11-23 N2「治理链拦截率观测」的落地件）
#
# ── 为什么需要这个脚本 ──────────────────────────────────────────────────
#
#	归因日志已上线（★ dispatch gate 的 short-circuit 行带 `decision_reason` /
#	`decision_deny_plugins` / `decision_verdicts` 四个字段），★ 但**日志不是指标**：
#	★ 「拦截了多少、拦的是谁、拦的什么」目前只能靠人肉翻 docker logs。
#	⇒ ★ N2（拦截率观测）与 N7 选项③（维持现状 + 把拦截构成纳入监控）
#	  ★ 都缺一个**可复跑的取数口径** ⇒ ★ 本脚本就是那个口径。
#
# ── 它回答什么 ──────────────────────────────────────────────────────────
#
#	① 窗口内**拦截了多少**次、**占同期请求的多少**（★ 拦截率）；
#	② 拦截**由谁**造成（★ `decision_deny_plugins` 构成）；
#	③ 拦截**理由**是什么（★ `decision_reason` 构成）；
#	④ ★ **泄露边界体检**：★ 归因日志里**有没有** `matched_pattern` /
#	   `input_sample` / evidence 片段（★ 有 ⇒ 立即红、★ 因为那说明日志在泄题）。
#
# ── ★ 泄露边界（本脚本自身的纪律）────────────────────────────────────────
#
#	★ 本脚本**只输出聚合计数**（★ 插件名 / 理由文本 / 条数 / 比率），
#	★ **绝不输出任何用户原文、命中片段、request_preview**。
#	★ 归因日志里的 `decision_reason` 本身就是**形态化文本**
#	（★ 如「[prompt_injection] critical threat detected」），
#	★ 不含用户输入 ⇒ ★ 聚合它不会泄露。
#	★ ⇒ 本脚本只把**计数与构成**打印出来，★ 并对泄露字段做体检。
#
# ── 用法 ────────────────────────────────────────────────────────────────
#
#	bash scripts/governance-intercept-composition.sh --host 211 --window 24h
#	bash scripts/governance-intercept-composition.sh --host 211 --window 7d --json
#
# ★ 参数：
#   --host <ssh 目标>   默认 211
#   --window <时长>     docker logs --since 口径（默认 24h）
#   --container <名字>   网关容器名；★ 缺省自动探测（llm-gateway-* 的在跑容器）
#   --json              输出 JSON（★ 便于被别处采集）
#   --no-rate           不查请求总数（★ 不查就不出拦截率）
#
# ── ★ 退出码 ────────────────────────────────────────────────────────────
#
#	0 = 口径跑通（★ 无论拦截数是否为 0）
#	1 = 口径本身跑不通（★ 连不上 / 容器找不到 / 日志读不出来）
#	★ 泄露体检发现证据片段 ⇒ ★ 也走 1（★ 那是**真问题**、★ 不是口径问题）
#
# ── ★ 为什么分母用「请求总数」而不是「进入治理链的请求数」────────────
#
#	★ 治理链挂在**每个**进网关的请求上（★ v2 dispatch gate）⇒ ★ 分母取
#	★ 同窗口的**总请求数**是最保守（★ 偏小）⇒ ★ 报出来的拦截率是**上界**。
#	★ 宁可报得偏高（★ 让人警觉）★ 也不要报得偏低（★ 让人放心）。

set -uo pipefail

HOST="211"
WINDOW="24h"
CONTAINER=""
JSON_OUT=0
WITH_RATE=1

usage() {
  cat <<'EOF'
用法: governance-intercept-composition.sh [选项]

  --host <ssh 目标>    默认 211
  --window <时长>      docker logs --since 口径，默认 24h
  --container <名字>    网关容器名，缺省自动探测 llm-gateway-*
  --json               输出 JSON
  --no-rate            不查请求总数（不出拦截率）
  -h, --help           本帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --window) WINDOW="$2"; shift 2 ;;
    --container) CONTAINER="$2"; shift 2 ;;
    --json) JSON_OUT=1; shift ;;
    --no-rate) WITH_RATE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage >&2; exit 1 ;;
  esac
done

log() { printf '[intercept] %s\n' "$*" >&2; }

# ── ① 定位容器 ───────────────────────────────────────────────────────────
if [[ -z "$CONTAINER" ]]; then
  CONTAINER="$(ssh "$HOST" 'docker ps --format "{{.Names}}\t{{.Status}}" | grep -E "^llm-gateway-" | grep -v "Exited" | head -1 | cut -f1' 2>/dev/null)"
fi
if [[ -z "$CONTAINER" ]]; then
  log "★ 在 $HOST 上找不到在跑的 llm-gateway-* 容器（★ 口径跑不通）"
  exit 1
fi
log "目标: $HOST / $CONTAINER / 窗口 $WINDOW"

# ── ①b ★ 容器启动时刻 ⇒ 决定**日志的真实覆盖期** ───────────────────────────
#
# ★ ★ 为什么必须算（★ 本轮实测踩到、★ 而且它会让读数**静默失真**）：
#   ★ `docker logs` 是**按容器**的 ⇒ ★ 容器一重启（★ 每次蓝绿部署都重启）
#   ★ **旧容器的日志随旧容器一起消失** ⇒ ★ `--since 7d` 在一个 10 分钟前
#   ★ 刚起的容器上只会返回 10 分钟的日志。
#   ★ ⇒ ★ 「7 天零拦截」的真实含义是「**10 分钟内零拦截**」
#   ★ ⇒ ★ 而若分母仍按 7 天算（★ 80977 条）⇒ ★ **比率毫无意义**
#   ★ ⇒ ★ 所以：★ 覆盖期与分母**必须取同一个起点**。
CONTAINER_STARTED="$(ssh "$HOST" "docker inspect -f '{{.State.StartedAt}}' '$CONTAINER'" 2>/dev/null | tr -d '[:space:]')"
if [[ -z "$CONTAINER_STARTED" ]]; then
  log "★ 取不到容器启动时刻 ⇒ 覆盖期未知 ⇒ ★ 拦截率不可信 ⇒ 本次只报绝对数"
  COVERAGE_START=""
else
  COVERAGE_START="$CONTAINER_STARTED"
  log "容器启动时刻: $CONTAINER_STARTED"
fi

# ── ② 取归因日志行 ───────────────────────────────────────────────────────
RAW="$(ssh "$HOST" "docker logs --since '$WINDOW' '$CONTAINER' 2>&1 | grep 'v2 dispatch gate: short-circuit'" 2>/dev/null)"
# ★ 分歧观测行（★ N8：★ PI 家族两套判定口径相反时才会打、★ 罕见事件）
RAW_DIV="$(ssh "$HOST" "docker logs --since '$WINDOW' '$CONTAINER' 2>&1 | grep 'PI verdict divergence observed'" 2>/dev/null)"
if [[ -z "$RAW" ]]; then
  log "★ 窗口内没有任何 short-circuit 记录（★ 可能真的零拦截、★ 也可能该版本没有归因日志）"
  log "★ ★ 区分办法：★ 查该容器版本是否含归因字段（★ grep 启动日志的 decision_ 字段）"
  RAW=""
fi

# ── ③ 泄露体检（★ 先做：★ 有泄露就不该往下走）────────────────────────────
LEAK_HITS="$(( $(printf '%s\n' "$RAW" | grep -c -E 'matched_pattern|input_sample|"evidence"' || true) + $(printf '%s\n' "$RAW_DIV" | grep -c -E 'matched_pattern|input_sample|"evidence"' || true) ))"
if [[ "${LEAK_HITS:-0}" -gt 0 ]]; then
  log "★★ 泄露体检失败：★ 归因日志里出现 matched_pattern / input_sample / evidence 片段"
  log "★★ 共 $LEAK_HITS 行命中 ⇒ ★ 这说明归因日志在泄题（★ 违反 internal_error_guard 纪律）"
  log "★★ ★ 本脚本不打印这些行（★ 只报计数）⇒ ★ 请人工核对该容器的日志采集侧"
  exit 1
fi

# ── ④ 分母：★ 与**覆盖期同起点**的请求总数（★ 只查不打印原文）─────────────
#
# ★ ★ 分母必须与分子**同一覆盖期**（★ 见 ①b）：★ 否则「10 分钟的分子 ÷ 7 天的分母」
# ★ = 一个**没有意义**的比率、★ 而且它长得像个正常数字 ⇒ ★ 比不给还坏。
REQ_TOTAL=""
REQ_WINDOW_START=""
if [[ "$WITH_RATE" -eq 1 ]]; then
  if [[ -n "$COVERAGE_START" ]]; then
    # ★ 起点取「窗口起点」与「容器启动时刻」的**较晚者**。
    REQ_TOTAL="$(ssh "$HOST" "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -At -F'|' -c \"select count(*), coalesce(to_char(min(ts) at time zone 'UTC','YYYY-MM-DD HH24:MI:SS'),'') from request_logs_with_current_month where ts > greatest(now() - interval '$WINDOW', '$COVERAGE_START'::timestamptz)\"" 2>/dev/null </dev/null | tr -d '[:space:]')"
    REQ_TOTAL="${REQ_TOTAL%%|*}"
  else
    REQ_TOTAL="$(ssh "$HOST" "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -At -c \"select count(*) from request_logs_with_current_month where ts > now() - interval '$WINDOW'\"" 2>/dev/null </dev/null | tr -d '[:space:]')"
  fi
  [[ -z "$REQ_TOTAL" ]] && log "★ 取请求总数失败 ⇒ 本次只报绝对数、★ 不报拦截率"
fi

# ── ④b 治理链期望状态（★ 「零拦截」的**前置条件**）─────────────────────────
#
# ★ ★ 为什么必须读（★ 本轮实测踩到、★ 而且它会让读数**静默失真**）：
#   ★ 「窗口内零拦截」有**两种**截然不同的含义：
#   ① 真的没有攻击；② ★ **治理链那一段根本没在跑**
#   （★ `security.enabled=false` ⇒ ★ `domains/security.SecurityHook` 的
#   ★ `Enabled()` 恒 false ⇒ ★ `prompt_injection` / 敏感词 / 策略合规 /
#   ★ 工具风险 / 数据外泄 **五个插件一个都不裁决** ⇒ ★ 请求一路放行）。
#   ⇒ ★ 不读这个开关就报「零拦截」⇒ ★ **读者会把「链被关了」读成「没有攻击」**。
# ★ ★ 而且这是**当次实查**、★ 不是常量：★ 生产配置会变（★ 本轮实测
#   `security.enabled` 与我早前记录的快照**相反**）⇒ ★ 报告里的配置快照会过期。
GOV_SW_RAW="$(ssh "$HOST" "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -At -F'|' -c \"select key, value::text from settings_kv where key in ('security.enabled','security.mode') order by key\"" 2>/dev/null </dev/null | tr -d '\r')"
# ★ ★ 只去 CR（★ **不能** `tr -d '[:space:]'`：★ 那会把**换行**也删掉
#   ⇒ ★ 两行粘成一行 ⇒ ★ key 解析成「security.enabled=truesecurity.mode」⇒ ★ 读数失真）。
if [[ -z "$GOV_SW_RAW" ]]; then
  log "★ 取不到治理链开关（★ security.enabled / security.mode）⇒ ★ 本次读数不能据此下「无攻击」结论"
fi

# ── ⑤ 聚合（★ 交本地 python3 解析 JSON 行、★ 只出计数）─────────────────
export INTERCEPT_RAW="$RAW"
export INTERCEPT_RAW_DIV="$RAW_DIV"
export INTERCEPT_REQ_TOTAL="$REQ_TOTAL"
export INTERCEPT_JSON="$JSON_OUT"
export INTERCEPT_HOST="$HOST"
export INTERCEPT_CONTAINER="$CONTAINER"
export INTERCEPT_WINDOW="$WINDOW"
export INTERCEPT_STARTED="${COVERAGE_START:-}"
export INTERCEPT_GOV_SW="$GOV_SW_RAW"

python3 - <<'PY'
import json
import os
from collections import Counter

raw = os.environ.get("INTERCEPT_RAW", "")
raw_div = os.environ.get("INTERCEPT_RAW_DIV", "")
req_total = os.environ.get("INTERCEPT_REQ_TOTAL", "").strip()
as_json = os.environ.get("INTERCEPT_JSON", "0") == "1"
host = os.environ.get("INTERCEPT_HOST", "")
container = os.environ.get("INTERCEPT_CONTAINER", "")
window = os.environ.get("INTERCEPT_WINDOW", "")
started = os.environ.get("INTERCEPT_STARTED", "")
gov_raw = os.environ.get("INTERCEPT_GOV_SW", "")

# ── 治理链期望状态（★ 「零拦截」的前置条件）───────────────────────────────
gov = {}
for line in gov_raw.splitlines():
    line = line.strip()
    if not line or "|" not in line:
        continue
    k, v = line.split("|", 1)
    k = k.strip()
    if k:
        gov[k] = v.strip()
gov_enabled = None
if "security.enabled" in gov:
    lv = gov["security.enabled"].strip().strip('"').lower()
    if lv in ("true", "t", "1"):
        gov_enabled = True
    elif lv in ("false", "f", "0"):
        gov_enabled = False

plugins = Counter()
reasons = Counter()
kinds = Counter()
paths = Counter()
stamps = []
total = 0
bad_lines = 0

for line in raw.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except Exception:
        bad_lines += 1
        continue
    # ★ ★ 非 JSON 对象行（★ 例如 `42`、★ `"x"`、★ `[1]`）⇒ ★ **不是 dict**
    #   ⇒ ★ `d.get` 会抛 AttributeError ⇒ ★ 整个脚本崩掉。
    #   ⇒ ★ `docker logs` 里混进非 JSON 行是**常态**（★ 多行日志、★ 裸行）
    #   ⇒ ★ 这里必须挡一层（★ 计入「不可解析行」⇒ ★ 读数会偏低、★ 但脚本活着）。
    if not isinstance(d, dict):
        bad_lines += 1
        continue
    total += 1
    ts = d.get("timestamp", "")
    if ts:
        stamps.append(ts)
    plugins[d.get("decision_deny_plugins", "") or "(空)"] += 1
    reasons[d.get("decision_reason", "") or "(空)"] += 1
    kinds[d.get("decision_kind", "") or "(空)"] += 1
    paths[d.get("path", "") or "(空)"] += 1

# ── 分歧观测（N8）：★ 两种形态 ────────────────────────────────────────────
#   ★ 形态 a（拦截侧）：★ 同一份 decision_verdicts 里 PI 家族内部方向不一致
#     ⇒ ★ **既有日志里就有** ⇒ ★ 本脚本当场解析（★ 不需要新代码上线）。
#   ★ 形态 b（放行侧）：★ 请求正常放行 ⇒ ★ 只由新观测点产出
#     （★ 「v2 dispatch: PI verdict divergence observed」那行）。
import re as _re

PI_FAMILY = ("prompt_injection", "prompt_injection_enhanced", "security.legacy")
_vd_re = _re.compile(r"([A-Za-z0-9_.]+)\((allow|deny),sev(\d+)")

div_a_total = 0
div_a_pairs = Counter()
for line in raw.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except Exception:
        continue
    if not isinstance(d, dict):
        continue
    vtxt = d.get("decision_verdicts", "") or ""
    if not vtxt:
        continue
    fam = []
    for m in _vd_re.finditer(vtxt):
        plugin, allow, sev = m.group(1), m.group(2), int(m.group(3))
        if plugin.startswith(PI_FAMILY):
            fam.append((plugin, allow, sev))
    if not fam:
        continue
    has_deny = any(a == "deny" for _, a, _ in fam)
    has_allow = any(a == "allow" for _, a, _ in fam)
    if has_deny and has_allow:
        div_a_total += 1
        denied = ",".join(p for p, a, _ in fam if a == "deny")
        allowed = ",".join(p for p, a, _ in fam if a == "allow")
        div_a_pairs["%s ⇄ %s" % (denied, allowed)] += 1

# ── 分歧观测（N8）─────────────────────────────────────────────────────────
div_total = 0
div_pairs = Counter()
div_stamps = []
for line in raw_div.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        d = json.loads(line)
    except Exception:
        continue
    if not isinstance(d, dict):
        continue
    div_total += 1
    denied = d.get("pi_divergence_denied", "") or "(空)"
    allowed = d.get("pi_divergence_allowed", "") or "(空)"
    div_pairs["%s ⇄ %s" % (denied, allowed)] += 1
    ts = d.get("timestamp", "")
    if ts:
        div_stamps.append(ts)

rate = None
if req_total and req_total.isdigit() and int(req_total) > 0:
    rate = total * 100.0 / int(req_total)

if as_json:
    print(json.dumps({
        "host": host,
        "container": container,
        "window": window,
        "log_coverage_start": started,
        "intercepts": total,
        "requests_total": int(req_total) if req_total.isdigit() else None,
        "intercept_rate_pct": round(rate, 6) if rate is not None else None,
        "by_deny_plugins": dict(plugins),
        "by_reason": dict(reasons),
        "by_decision_kind": dict(kinds),
        "by_path": dict(paths),
        "intercept_timestamps": stamps,
        "pi_divergences_pass_side": div_total,
        "pi_divergences_block_side": div_a_total,
        "pi_divergence_block_side_pairs": dict(div_a_pairs),
        "pi_divergence_pairs": dict(div_pairs),
        "pi_divergence_timestamps": div_stamps,
        "unparsable_lines": bad_lines,
        "evidence_leak_lines": 0,
        "governance_switches": gov,
        "governance_chain_enabled": gov_enabled,
    }, ensure_ascii=False, indent=2))
else:
    print("=== 治理链拦截构成（%s / %s / 窗口 %s）===" % (host, container, window))
    if started:
        print("日志覆盖起点: %s（★ 容器此刻起算）" % started)
        print("★ ★ docker logs 按容器存：★ 容器一重启，★ 更早的日志随旧容器一起消失")
        print("★ ★ ⇒ 窗口长于容器寿命时，★ 实际覆盖期 = 容器启动至今（★ 下方的分子分母同用它）")
    else:
        print("日志覆盖起点: 未知（★ 取不到容器启动时刻 ⇒ 本次不出拦截率）")
    print("拦截次数: %d" % total)
    # ★ 治理链期望状态（★ 零拦截的**前置条件**：★ 链被关 ⇒ 零拦截 ≠ 无攻击）
    print("")
    print("-- 治理链期望状态（★ 「零拦截」的前置条件）--")
    if gov:
        for k in sorted(gov):
            print("  %s=%s" % (k, gov[k]))
    else:
        print("  （取不到 ⇒ ★ 本次读数**不能**据此下「无攻击」结论）")
    if gov_enabled is False:
        print("  ★★★ 警示：★ security.enabled=false ⇒ ★ 治理链 security.plugins 阶段")
        print("       （★ prompt_injection / 敏感词 / 策略合规 / 工具风险 / 数据外泄）")
        print("       **整段被跳过** ⇒ ★ 本次「零拦截」**不可**解读为「没有攻击」")
    elif gov_enabled is True:
        print("  ★ 链是开着的（★ security.enabled=true）⇒ ★ 零拦截可按「无命中」读")
    else:
        print("  ★ 开关状态未知 ⇒ ★ 零拦截**不可**解读为「没有攻击」")
    if rate is not None:
        print("同期请求总数: %s" % req_total)
        print("拦截率（上界口径）: %.6f%%" % rate)
    else:
        print("同期请求总数: 未知（★ 本次不出拦截率）")
    if bad_lines:
        print("★ 不可解析的日志行: %d（★ 口径读数可能偏低）" % bad_lines)
    print("")
    print("-- 拦截构成（decision_deny_plugins）--")
    if plugins:
        for k, v in plugins.most_common():
            print("  %5d  %s" % (v, k))
    else:
        print("  （窗口内零拦截）")
    print("")
    print("-- 拦截理由（decision_reason）--")
    if reasons:
        for k, v in reasons.most_common():
            print("  %5d  %s" % (v, k))
    else:
        print("  （窗口内零拦截）")
    print("")
    print("-- 判定类型（decision_kind）--")
    for k, v in kinds.most_common():
        print("  %5d  %s" % (v, k))
    print("")
    print("-- 路径（path）--")
    for k, v in paths.most_common():
        print("  %5d  %s" % (v, k))
    print("")
    if stamps:
        print("-- 拦截时刻（★ 用途：★ 判断是【自然拦截】还是【人工探针】★ 集中在一两个时间点）--")
        for t in stamps[:50]:
            print("  %s" % t)
        if len(stamps) > 50:
            print("  …… 另有 %d 条未列" % (len(stamps) - 50))
    print("")
    print("-- PI 家族判决分歧（N8：★ 两套注入判定口径相反的观测）--")
    print("  [形态 a｜拦截侧：★ 从既有 decision_verdicts 解析]")
    if div_a_total:
        print("    分歧次数: %d" % div_a_total)
        for k, v in div_a_pairs.most_common():
            print("    %5d  %s" % (v, k))
    else:
        print("    （窗口内零分歧）")
    print("  [形态 b｜放行侧：★ 来自新观测点]")
    if div_total:
        print("    分歧次数: %d" % div_total)
        for k, v in div_pairs.most_common():
            print("    %5d  %s" % (v, k))
        for t in div_stamps[:20]:
            print("         %s" % t)
    else:
        print("    （窗口内零分歧）")
    print("")
    print("★ ★ 读数须知（四条，★ 别把这张表当结论）：")
    print("   ① 拦截率是**上界**：★ 分子里可能混着**人工探针**（★ 探针也是真实请求）")
    print("      ⇒ ★ 真拦截率**只能**用【无探针窗口】或剔除探针后重算；")
    print("   ② 分子覆盖期 = 容器启动至今（★ 容器一重启，★ 更早的日志就没了）")
    print("      ⇒ ★ 窗口写着 7d、实际可能只覆盖了 10 分钟（★ 上面的『覆盖起点』说了算）；")
    print("   ③ 理由文本是**形态化**的（★ 不含用户原文）★ 但仍可能含**插件自己的措辞**")
    print("      ⇒ ★ 报表外发前请自行过一遍。")
    print("   ④ ★ **零拦截 ≠ 没有攻击**：★ 先看上面的『治理链期望状态』")
    print("      ⇒ ★ `security.enabled=false` ⇒ ★ 治理链那一段**整段不跑**")
    print("      ⇒ ★ 零拦截只说明『链开着但没命中』；★ 链被关时它**什么都不能说明**。")
    print("")
    print("★ 泄露体检: 通过（★ 0 处 matched_pattern / input_sample / evidence）")
PY
