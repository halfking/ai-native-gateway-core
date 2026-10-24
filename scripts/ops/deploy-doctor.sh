#!/usr/bin/env bash
# =============================================================================
# scripts/ops/deploy-doctor.sh — 部署形态**只读**体检（不部署、不改配置）
#
# 回答一个问题：**这台机器上的「流量 → 槽位 → 端口」绑定还是真的吗？**
#
# 为什么需要它（2026-10-23，211 的 502 事故）
# ------------------------------------------------
# 211 是 docker 形态：nginx 的 upstream 块里**直接写死**了活跃槽端口
#   upstream ns_llm_gateway_upstream { server 127.0.0.1:<端口>; }
# 而 154/245 是 systemd 形态：nginx 里是
#   include /opt/llm-gateway-go/run/active-upstream.conf;
# ⇒ 端口在**部署时生成的文件**里 ⇒ 154/245 天然没有「端口烤死在 nginx」这个形态。
#
# 211 的真实漂移（2026-10-21 17:0x 实测）：同机另一个产品
# `ai-session-manager-asm-1` 占了 127.0.0.1:8782（网关旧 b 槽的端口），
# 而 nginx 的 upstream **仍指向 8782** ⇒ 网关的流量被送到**别的产品**的端口
# ⇒ llm.itestu.cn 502。
#
# ★ 本脚本**只读**：ssh + `cat` + `docker ps` + 读 nginx conf。
#   不写任何文件、不 reload、不重启、不动容器。
# ★ 端口判定**复用引擎的同一份逻辑**（scripts/ops/nginx-upstream-port.py，
#   掩码后定位 ⇒ 注释里的旧端口不算数）⇒ 体检结论与部署时的判定**同口径**。
#
# 用法
# ----
#   # 对活机体检（读 ssh 别名）
#   bash scripts/ops/deploy-doctor.sh --ssh 211 --slot-a 8781 --slot-b 8783
#
#   # 离线分析（门用；facts 文件形如 KEY=VALUE 每行一条）
#   bash scripts/ops/deploy-doctor.sh --facts-file facts.txt --slot-a 8781 --slot-b 8783
#
# 退出码
# ------
#   0  绑定正常（upstream 端口属于某个配置槽位）
#   2  ★ 绑定脱节（upstream 端口不属于任何槽位）—— 需要人工处置
#   3  取不到现场事实（连不上 / 缺工具）—— 体检没做成，**不是「正常」**
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPER="${NGINX_UPSTREAM_PORT_HELPER:-$SCRIPT_DIR/nginx-upstream-port.py}"

SSH_TARGET=""
NGINX_CONF="/etc/nginx/conf.d/ns-gateway-211.conf"
UPSTREAM_NAME="ns_llm_gateway_upstream"
SLOT_A=""
SLOT_B=""
DATA_ROOT="/opt/kaixuan/data/llm-gateway"
FACTS_FILE=""
# ★ 网关自己容器名的前缀（引擎侧 DD_CONTAINER_PREFIX 默认 llm-gateway-<target>）。
# ★ 空 = 不知道 ⇒ 「占用者是不是网关自己」这一问**判不了** ⇒ 只能报「疑似」，不报定论。
CONTAINER_PREFIX="${DD_CONTAINER_PREFIX:-llm-gateway-}"

usage() { sed -n '2,42p' "$0"; exit 0; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --ssh) SSH_TARGET="${2:-}"; shift 2 ;;
    --nginx-conf) NGINX_CONF="${2:-}"; shift 2 ;;
    --upstream) UPSTREAM_NAME="${2:-}"; shift 2 ;;
    --slot-a) SLOT_A="${2:-}"; shift 2 ;;
    --slot-b) SLOT_B="${2:-}"; shift 2 ;;
    --data-root) DATA_ROOT="${2:-}"; shift 2 ;;
    --facts-file) FACTS_FILE="${2:-}"; shift 2 ;;
    # ★ 2026-10-24 12h 审计轮（问题④）：网关**自己**的容器名前缀。
    #   引擎侧的容器名是 `llm-gateway-$DD_TARGET`（DD_CONTAINER_PREFIX）⇒
    #   体检必须知道这个前缀，才能判「占用这个端口的是不是**网关自己**」。
    --container-prefix) CONTAINER_PREFIX="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "未知参数：$1" >&2; exit 3 ;;
  esac
done

if [[ -z "$FACTS_FILE" && -z "$SSH_TARGET" ]]; then
  echo "用法：--ssh <别名> 或 --facts-file <文件>（见脚本头）" >&2
  exit 3
fi

# ── 分析（离线可测：--facts-file 走这里）────────────────────────────
analyze() {
  local facts="$1"
  local upstream_port="" state_slot="" no_conf="0" port_owner=""
  local -a containers=()
  local line name ports st rest

  while IFS= read -r line; do
    case "$line" in
      UPSTREAM_PORT=*) upstream_port="${line#UPSTREAM_PORT=}" ;;
      STATE_SLOT=*)   state_slot="${line#STATE_SLOT=}" ;;
      PORT_OWNER=*)   port_owner="${line#PORT_OWNER=}" ;;
      NO_NGINX_CONF=*) no_conf="${line#NO_NGINX_CONF=}" ;;
      CONTAINER=*)
        rest="${line#CONTAINER=}"
        name="${rest%%|*}"
        rest="${rest#*|}"
        ports="${rest%%|*}"
        st="${rest#*|}"
        containers+=("$name|$ports|$st")
        ;;
    esac
  done <<<"$facts"

  echo "== 槽位 ↔ 端口 绑定体检 =="
  echo "upstream 主端口 : ${upstream_port:-（读不到）}"
  echo "配置槽位        : a=${SLOT_A:-（未配）} / b=${SLOT_B:-（未配）}"
  echo "引擎账本        : ${state_slot:-（无）}"
  if [[ -n "${no_conf}" && "$no_conf" != "0" ]]; then
    echo "nginx conf      : 读不到（${NGINX_CONF}）"
  fi

  # 端口被谁占着：优先用 docker 的 publish 过滤结果（精确）；
  # 没有该事实时退回字符串匹配（★ 变量必须**分开**声明：
  # 同一句 `local a=… b=$a` 里后一个读到的是**旧值** —— bash 在执行赋值前
  # 先展开全部词，实测 `local cn=… rest=… cp=$rest` 会让 cp 变成空串）。
  local owner="${port_owner}"
  if [[ -z "$owner" ]]; then
    owner="（该端口没有容器发布）"
    local c cn crext cp
    # ★ bash 3.2 兼容：空数组在 `set -u` 下用 "${arr[@]}" 展开会报
    #   `unbound variable`（实测 exit 127）⇒ 而**「没有容器发布这个端口」正是
    #   本脚本要诊断的场景之一** ⇒ 一报错就等于「体检没做成」。
    #   ⇒ 用仓库既有写法 "${arr[@]+"${arr[@]}"}"（scripts/deploy-211.sh:171 同款）。
    for c in ${containers[@]+"${containers[@]}"}; do
      cn="${c%%|*}"
      crext="${c#*|}"
      cp="${crext%%|*}"
      if [[ "$cp" == *":${upstream_port}->"* || "$cp" == *":${upstream_port}/tcp"* ]]; then
        owner="${cn}"
        break
      fi
    done
  fi
  echo "该端口的占用者  : ${owner}"

  # 判定
  if [[ -z "$upstream_port" ]]; then
    echo "结论            : ★ 取不到 upstream 主端口（全新主机？或 conf 路径不对）"
    return 3
  fi
  # ★★ 2026-10-24 12h 审计轮（问题④）：**占用者这个事实必须进入判据**。
  #
  #   原判据只有一句「upstream 端口 ∈ {配置槽位}」⇒ 那是**配置自洽性**检查，
  #   不是「网关真的在服务这个端口」检查。
  #   ⇒ ⇒ 211 事故形态（upstream=8782，而 8782 是**另一个产品**的容器占着，
  #      且 8782 恰好被配成了 b 槽端口）**照样判 OK**。
  #   ⇒ 而本脚本**已经算出**了占用者，却从不把它放进判据 ⇒ 事实到手了不用。
  #
  #   现在：端口虽然落在某个槽位上，但**占用者不是网关自己的容器** ⇒
  #   ⇒ 判据不成立 ⇒ 明确报「被别的容器占用」并以 **2**（需人工处置）退出。
  #   ★ 占用者**未知**（没有容器发布该端口）时不判死：证据不足 ⇒ 只提示。
  #
  # ★★ **判据顺序**（本轮实测踩到并修正）：先判「端口不属于任何槽位」
  #   （更根本的违规），**再**判「端口落在槽位上但被别的容器占用」。
  #   ⇒ 反过来的话，「端口根本不属于任何槽位」这一形态会被「被别的容器占用」
  #   抢先命中 ⇒ 结论文案会丢掉「不属于任何配置槽位」这个更准确的说法。
  _owner_is_foreign=""
  if [[ -n "$owner" && "$owner" != "（该端口没有容器发布）" ]]; then
    if [[ -z "$CONTAINER_PREFIX" ]]; then
      _owner_is_foreign="unknown"
    elif [[ "$owner" == "$CONTAINER_PREFIX"* ]]; then
      _owner_is_foreign="no"
    else
      _owner_is_foreign="yes"
    fi
  fi

  # ① 端口压根不属于任何配置槽位（最根本的违规）⇒ 先判它。
  _port_in_slot=""
  if [[ -n "$SLOT_A" && "$upstream_port" == "$SLOT_A" ]]; then _port_in_slot="a"; fi
  if [[ -z "$_port_in_slot" && -n "$SLOT_B" && "$upstream_port" == "$SLOT_B" ]]; then _port_in_slot="b"; fi
  if [[ -z "$_port_in_slot" ]]; then
    cat <<EOF
结论            : ★★ 绑定脱节 —— upstream 指向 ${upstream_port}，它不属于任何配置槽位
                  （a=${SLOT_A:-未配} / b=${SLOT_B:-未配}）⇒ 流量现在打在
                  「${owner}」上 ⇒ 这就是 llm.itestu.cn 502 的成因形态。
处置            : ① 先确认 ${upstream_port} 上跑的是谁（上面「占用者」一行）；
                  ② 若是**别的产品**（211 实测过：ai-session-manager-asm-1 占了
                     127.0.0.1:8782）⇒ 把 upstream 主 server 行改到网关**实际在服务**
                     的端口（${SLOT_A:-a} 或 ${SLOT_B:-b}），再重跑部署；
                  ③ 若是**网关自己** ⇒ 端口配置与现场不一致 ⇒ 改 DD_SLOT_*_PORT
                     使其与现场一致（槽位端口必须静态：活跃槽判定靠端口↔槽位配对）。
★ 部署侧已加闸：绑定脱节时 deploy-seamless-docker.sh 会**在写任何东西之前**
  拒绝部署并打出同样这份说明（不会再拿猜出来的槽位去切流/停槽）。
EOF
    return 2
  fi

  # ② 端口落在某个槽位上，但**占用者不是网关自己的容器** ⇒ 事实检查不通过。
  if [[ "$_owner_is_foreign" == "yes" ]]; then
    cat <<EOF
结论            : ★★ 端口被**别的容器**占用 —— upstream 指向 127.0.0.1:${upstream_port}，
                  该端口**确实落在某个配置槽位**（a=${SLOT_A:-未配} / b=${SLOT_B:-未配}），
                  但监听它的是「${owner}」，**不是网关自己的容器**（网关容器名前缀：${CONTAINER_PREFIX}*）
                  ⇒ ⇒ 「端口 ∈ 槽位」这个配置自洽性检查**通不过事实检查**：
                  网关的流量会被送到**别的产品**的端口 ⇒ 502。
处置            : ① 把 upstream 主 server 行改到网关**实际在服务**的端口；
                  ② 或先把占用 ${upstream_port} 的别的产品迁走；
                  ③ 确认槽位端口配置与现场一致（槽位端口必须静态：活跃槽判定靠端口↔槽位配对）。
EOF
    return 2
  fi
  if [[ "$_owner_is_foreign" == "unknown" ]]; then
    echo "结论            : ★ upstream 在槽位端口上，但**不知道**占用者是不是网关自己的容器"
    echo "                  （未提供 --container-prefix）⇒ 无法判定「被别的产品占用」这一形态。"
  fi
  if [[ "$_port_in_slot" == "a" ]]; then
    echo "结论            : OK（upstream 在槽 a 上，绑定正常）"
  else
    echo "结论            : OK（upstream 在槽 b 上，绑定正常）"
  fi
  return 0
}

# ── ssh 出口（可被替身替换，门用来模拟「连不上」）──────────────────────
# ★ DOCTOR_SSH_CMD 指向一个可执行文件：它收到 <target> 与 <远端命令>，
#   行为与 `ssh <target> <cmd>` 一致。
dd_ssh() {
  local target="$1"; shift
  if [[ -n "${DOCTOR_SSH_CMD:-}" ]]; then
    "$DOCTOR_SSH_CMD" "$target" "$@"
    return $?
  fi
  ssh -o ConnectTimeout=10 -o BatchMode=yes "$target" "$@"
}

# ★ 删除动作**必须重定向 stdout**：本机的 `rm -rf` 被包装成可恢复删除工具，
#   而它**会往 stdout 打印一行**（形如「mavis-trash: moved to trash: '...'」）
#   ⇒ 若这一步发生在**命令替换**里，那行提示会被当成「现场事实」⇒
#   「连不上」会被误报成「conf 读不到」（2026-10-23 实测踩过）。
dd_cleanup() {
  rm -rf "$1" >/dev/null 2>&1 || true
}

# ── 取现场事实（只读）─────────────────────────────────────────────────
collect_facts() {
  local target="$1"
  local tmp
  tmp="$(mktemp -d)"
  # ① 把 nginx conf **取回本地**再判定：判定逻辑用仓里那份共享脚本（与引擎同口径）
  if ! dd_ssh "$target" "cat '$NGINX_CONF' 2>/dev/null" > "$tmp/conf" 2>/dev/null; then
    dd_cleanup "$tmp"; return 1
  fi
  if [[ ! -s "$tmp/conf" ]]; then
    echo "UPSTREAM_PORT="
    echo "NO_NGINX_CONF=1"
    echo "STATE_SLOT="
    dd_cleanup "$tmp"; return 0
  fi
  # ② upstream 块里若是 include（systemd 形态）⇒ 端口在那个生成文件里 ⇒ 取回来
  local inc="" inc_file=""
  inc="$(python3 "$HELPER" --conf "$tmp/conf" --upstream "$UPSTREAM_NAME" --print-include 2>/dev/null || true)"
  if [[ -n "$inc" ]]; then
    inc_file="$tmp/inc"
    dd_ssh "$target" "cat '$inc' 2>/dev/null" > "$inc_file" 2>/dev/null || true
    [[ -s "$inc_file" ]] || inc_file=""
  fi
  local port
  if [[ -n "$inc_file" ]]; then
    port="$(python3 "$HELPER" --conf "$tmp/conf" --upstream "$UPSTREAM_NAME" --include "$inc_file" 2>/dev/null || true)"
  else
    port="$(python3 "$HELPER" --conf "$tmp/conf" --upstream "$UPSTREAM_NAME" 2>/dev/null || true)"
  fi
  echo "UPSTREAM_PORT=${port:-}"
  # ③ 该端口由哪个容器发布（docker 的 publish 过滤是**精确**匹配，比字符串匹配可靠）
  if [[ -n "$port" ]]; then
    local owner
    owner="$(dd_ssh "$target" \
      "docker ps -a --filter publish=$port --format '{{.Names}}' 2>/dev/null | paste -sd, -" 2>/dev/null || true)"
    echo "PORT_OWNER=${owner:-}"
  fi
  # ④ 容器 → 端口映射（谁占了哪个端口）
  dd_ssh "$target" \
    "docker ps -a --format '{{.Names}}|{{.Ports}}|{{.State}}' 2>/dev/null" 2>/dev/null \
    | while IFS='|' read -r nm pr st; do
        [[ -n "$nm" ]] || continue
        echo "CONTAINER=$nm|$pr|$st"
      done
  # ⑤ 引擎的账本（仅供参考，不是事实）
  local slot
  slot="$(dd_ssh "$target" \
    "tr -d '[:space:]' < '$DATA_ROOT/run/active-slot' 2>/dev/null || true" 2>/dev/null || true)"
  echo "STATE_SLOT=${slot:-}"
  dd_cleanup "$tmp"
}

# ── 主流程 ───────────────────────────────────────────────────────────
if [[ -n "$FACTS_FILE" ]]; then
  if [[ ! -f "$FACTS_FILE" ]]; then
    echo "facts 文件不存在：$FACTS_FILE" >&2
    exit 3
  fi
  facts="$(cat "$FACTS_FILE")"
else
  # ★ 用**返回码**判「取不到事实」（不是「输出为空」）：采集过程里任何
  #   往 stdout 冒出来的辅助输出都会污染 facts（实测踩过：删除工具的提示行）。
  if ! facts="$(collect_facts "$SSH_TARGET")"; then
    echo "★ 取不到 ${SSH_TARGET} 的现场事实（连不上？ssh 认证？缺 docker？）" >&2
    echo "  ⇒ 体检**没做成**，这**不是**「正常」" >&2
    exit 3
  fi
fi

analyze "$facts"
exit $?
