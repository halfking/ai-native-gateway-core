#!/usr/bin/env bash
# =============================================================================
# deploy-seamless-docker.sh — Docker 形态的蓝绿无缝部署引擎（主机无关）
# =============================================================================
# 定位：与 deploy-seamless.sh（宿主机 systemd 形态）**同款蓝绿语义**的 Docker
#      版本：把本网关以两个槽位（slot a / slot b）跑在同一台机器上，
#      候选槽先起、验收过才切 nginx upstream，失败则原路退回旧槽。
#
# ★ 为什么是这个形态（2026-10-20 211 502 事故的直接教训）：
#   旧实现是单容器形态（`docker rm -f` 旧容器 → 才做输入校验 → 才起新容器），
#   于是**任何**拆后失败都把「一次部署失败」放大成「服务下线」（502）。
#   单容器形态没有候选实例可并行 ⇒ 失败无处可退。
#   蓝绿形态天然免疫：旧槽全程在服务，候选失败只要丢掉候选即可。
#   deploy-seamless.sh（154/245）早就是这个形态，本次把它移植到 Docker 侧，
#   并把「守卫（输入检查）必须跑在拆服务之前」一并带过来。
#
# ── 蓝绿状态机 ────────────────────────────────────────────────────────────
#   槽位：a ↔ b，各绑一个固定发布端口（DD_SLOT_A_PORT / DD_SLOT_B_PORT）。
#   活跃槽记在 <data root>/run/active-slot；nginx upstream 的主 server 行
#   指向活跃槽的端口。
#   一次部署：
#     ① 前置门（前端产物新鲜度）—— 任何破坏性动作之前，失败即「本次不部署」
#     ② 构建/推送新镜像（不动任何在跑的容器）
#     ③ 种子化**候选槽专属**目录（web/dist、version.json）
#     ④ 起候选容器（候选端口），旧槽继续服务 ⇒ 零停机
#     ⑤ 验收候选：readyz(DB+Redis) / version / 网页面(HTML+入口 JS MIME)
#     ⑥ 切流：改 nginx upstream 主 server 行 → nginx -t → reload
#        → 复验公网版本身份（防「文件已切、流量未切」的半切换态）
#     ⑦ 切流成功 ⇒ 停旧槽容器（保留镜像 tag，人工可回滚）
#     任一步失败 ⇒ abort：删候选槽、切回 upstream、旧槽继续服务
#
# ── 主机无关：所有主机信息由**薄外壳**用环境变量注入 ──────────────────────
#   引擎自身不含任何具体主机名。新增一台 Docker 形态的主机 = 写一个薄外壳
#   （如 scripts/deploy-211.sh）设置下列变量后 exec 本引擎，无需复制引擎。
#     DD_TARGET            目标标识（日志/容器/镜像命名用）
#     DD_HOST              目标机地址（ssh 目标）
#     DD_STATE_DIR         目标机上的状态目录（锁/密钥/claim）
#     DD_REMOTE_ROOT       目标机上的仓库同步根
#     DD_DATA_ROOT         网关数据根（bind mount 落点）
#     DD_SLOT_A_PORT       槽 a 发布端口
#     DD_SLOT_B_PORT       槽 b 发布端口
#     DD_CONTAINER_PREFIX  容器名前缀（实际容器名 = <prefix>-<slot>）
#     DD_IMAGE             应用镜像名
#     DD_NETWORK           docker 网络名
#     DD_NGINX_CONF        nginx 配置文件（切流改这里）
#     DD_NGINX_UPSTREAM    nginx upstream 块名
#     DD_DOMAIN            公网域名（切流后验版本身份用）
#     DD_PG_* / DD_REDIS_* 数据库与缓存的复用口径
#     DD_BASE_REGISTRY / DD_GO_BASE_IMAGE / DD_RUNTIME_BASE_IMAGE 基础镜像
#   全部有默认值；外壳显式注入的优先（env > 默认）。
#
# 用法（通常由薄外壳调用）：
#   bash scripts/deploy-seamless-docker.sh                 # 部署
#   bash scripts/deploy-seamless-docker.sh --status        # 只看探测结果，不改动
#   bash scripts/deploy-seamless-docker.sh --stop          # 停掉两个槽的容器
#   bash scripts/deploy-seamless-docker.sh --via <host>    # 从部署机远程执行
#
# 密钥落点（不进仓库）：$DD_STATE_DIR/env/gateway.env（chmod 600）
# =============================================================================
set -euo pipefail

# ── 主机信息（由薄外壳注入；此处只给默认值）─────────────────────────────
DD_TARGET="${DD_TARGET:-docker}"
DD_HOST="${DD_HOST:-}"
DD_STATE_DIR="${DD_STATE_DIR:-/opt/llm-gateway-go-docker/$DD_TARGET}"
DD_REMOTE_ROOT="${DD_REMOTE_ROOT:-$DD_STATE_DIR/src}"
DD_NETWORK="${DD_NETWORK:-llm-gateway-net}"
# 蓝绿两槽的固定发布端口：槽位与端口是**绑定**的，活跃槽由状态文件记录。
DD_SLOT_A_PORT="${DD_SLOT_A_PORT:-8781}"
DD_SLOT_B_PORT="${DD_SLOT_B_PORT:-8782}"
DD_CONTAINER_PREFIX="${DD_CONTAINER_PREFIX:-llm-gateway-$DD_TARGET}"
DD_IMAGE="${DD_IMAGE:-llm-gateway-$DD_TARGET}"
DD_GW_IMAGE="${DD_GW_IMAGE:-$DD_IMAGE}"
DD_GW_DATA_ROOT="${DD_GW_DATA_ROOT:-/opt/kaixuan/data/llm-gateway}"
# 兼容旧变量名（DD_GW_PORT）：单机形态的历史写法。
DD_GW_PORT="${DD_GW_PORT:-$DD_SLOT_A_PORT}"
DD_PG_DB="${DD_PG_DB:-llm_gateway}"
DD_PG_USER="${DD_PG_USER:-llm_gateway}"
# 「调用方显式钉死」与「默认值」必须分开记：选谁 PG 的第一优先级是显式指定，
# 若把默认名 kx-pg17-columnar 也当成显式值，自管实例会永远压过任何真候选。
if [[ -n "${DD_PG_CONTAINER:-}" ]]; then DD_PG_CONTAINER_PINNED=1; else DD_PG_CONTAINER_PINNED=0; fi
DD_PG_CONTAINER="${DD_PG_CONTAINER:-kx-pg17-columnar}"
DD_PG_IMAGE="${DD_PG_IMAGE:-kx-pg17-columnar:17}"
DD_REDIS_CONTAINER="${DD_REDIS_CONTAINER:-llm-gateway-redis}"
DD_REDIS_IMAGE="${DD_REDIS_IMAGE:-redis:7-alpine}"
DD_PGCOLUMNAR_VER="${DD_PGCOLUMNAR_VER:-v1.0-alpha4}"
# 库号倒排：上界默认 15（Redis 默认 16 库的最大编号），下界 0
DD_REDIS_DB_START="${DD_REDIS_DB_START:-15}"
DD_REDIS_DB_END="${DD_REDIS_DB_END:-0}"
# nginx 切流：改 upstream 块里主 server 行的 127.0.0.1:<port>
DD_NGINX_CONF="${DD_NGINX_CONF:-/etc/nginx/conf.d/ns-gateway-${DD_TARGET}.conf}"
DD_NGINX_UPSTREAM="${DD_NGINX_UPSTREAM:-ns_llm_gateway_upstream}"
DD_DOMAIN="${DD_DOMAIN:-}"

DD_REPO_ROOT="${LLM_GATEWAY_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
# ★ upstream 端口读写判定脚本的路径：**按引擎自身所在目录解析**（scripts/ops/…），
#   而不是 DD_REPO_ROOT —— 后者可被 LLM_GATEWAY_ROOT 覆盖成别处 ⇒ 脚本就找不到了。
#   远端形态：引擎跑在 $DD_REMOTE_ROOT/scripts/ ⇒ 脚本在 $DD_REMOTE_ROOT/scripts/ops/。
DD_UPSTREAM_PORT_HELPER="${DD_UPSTREAM_PORT_HELPER:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ops/nginx-upstream-port.py}"
# 前端产物来源（前端由部署机预构建，随 tar 上树，引擎不构建前端）。
DD_WEB_DIST_SRC="${DD_WEB_DIST_SRC:-$DD_REPO_ROOT/web/dist}"
DD_MOBILE_DIST_SRC="${DD_MOBILE_DIST_SRC:-$DD_REPO_ROOT/web-mobile/dist}"
# 纯 API 形态逃生舱：前端缺失/陈旧时是否继续部署（默认拒绝）。
DD_ALLOW_STALE_WEB="${DD_ALLOW_STALE_WEB:-0}"

log()  { printf '[%s] %s\n' "$DD_TARGET" "$*" >&2; }
warn() { printf '[%s] WARN: %s\n' "$DD_TARGET" "$*" >&2; }
die()  { printf '[%s] FATAL: %s\n' "$DD_TARGET" "$*" >&2; exit 1; }

# =============================================================================
# 失败必须以失败告终（2026-10-22）—— 「完成标记」守卫
# =============================================================================
# ★ 为什么不能只看 $?：
#   EXIT trap 的**最后一条命令的退出码**会成为整个脚本的退出码。原写法
#   `trap 'lock_release_local' EXIT` 里 lock_release_local 末尾是 `rm -rf`
#   （成功 ⇒ 0）⇒ 任何**隐式**失败（set -u 的未绑定变量、set -e 的命令失败）
#   都会把退出码洗成 0 ⇒ 部署其实没发生，调用方却拿到「成功」。
#   ⇒ 在这个 2000+ 行、满是函数的引擎里，一次「其实啥都没干」的失败会被
#     报成**成功**。这正是「部署结果是错的」那一类故障的形态：人看到绿字，
#     线上没变（比如 llm.itestu.cn 仍是一段 JSON）。
#   ⇒ 判定成败不依赖 $?，改用**完成标记**：只有真正走到收尾才置 1；
#     没走到 ⇒ 退出码强制非 0（fail-closed）。
#
# ★★★★ 2026-10-23（12h 审计轮）**订正本块原先写的「实测矩阵」**：
#   原矩阵写的是「set -eu + 函数内未绑定 ⇒ 退出码 **0**」⇒ ⇒ **本轮实测证伪**。
#   实测矩阵（★ 每格都是真跑出来的，不是推出来的）：
#     死法              没有 EXIT trap    有 EXIT trap(bash 3.2.57)   有 EXIT trap(bash 5.2.0)
#     未绑定变量          1                 **0**（★ 察觉不到）        1
#     命令失败(false)     1                 1                          1
#     函数 return 1      1                 1                          1
#     exit 7             7                 7                          7
#   ⇒ ⇒ ★ **真正的因果变量是「有没有装 EXIT trap」与「哪个 bash」，不是 `set -eu`**：
#     · **没有 trap 时**，`set -eu` + 未绑定变量退出 **1**（可察觉）；
#     · **装了 trap 之后**，trap 入口处的 `$?` 才可能是 0，**且与 bash 版本相关**
#       （macOS 自带 bash 3.2.57 ⇒ 0；bash 5.2.0 ⇒ 1）。
#   ⇒ ⇒ ★ **光「保住 $?」是不够的**（在 bash 3.2 上它保住的恰恰是 0）⇒ ⇒
#   ⇒ ⇒ ★ **真正让结果在两个解释器上都确定的是【完成标记守卫】（fail-closed）**
#   ⇒ ⇒ ⇒ 这才是本守卫存在的理由（**不是**「set -eu 有 bug」）。
#   ⇒ 复现脚本与完整读数：docs/12小时内修订审计-2026-10-23-0331.md §2.4。
#   ⇒ 通用式（与 LESSONS「写『某形态失败 ⇒ exit N』必须带前提条件」同源）：
#     ★ **无条件的事实句会被读成普适规律** ⇒ ⇒ 事实句必须带前提条件。
#   ⇒ 回归门：scripts/ursmcheck/deploy_shared_build_lock_guard_test.go
#     （★ 行为式：把本守卫的真实实现抽出来在多个 bash 上跑四种死法）。
DD_DEPLOY_COMPLETED=0

# dd_exit_guard_finalize <退出码> —— EXIT trap 的收尾（把退出码判成「成功/失败」）。
dd_exit_guard_finalize() {
  local rc="${1:-0}"
  if [[ "$DD_DEPLOY_COMPLETED" != "1" && "$rc" == "0" ]]; then
    rc=1
    printf '[%s] FATAL: 部署未走到完成标记却将以 0 退出（bash set -eu 的隐式失败会丢退出码）—— 按失败处理\n' "$DD_TARGET" >&2
  fi
  exit "$rc"
}

# dd_install_exit_guard —— 装上完成标记守卫（只对 deploy 模式装）。
dd_install_exit_guard() {
  trap 'rc=$?; dd_exit_guard_finalize "$rc"' EXIT
}

# ── 共享构建锁（2026-10-23 12h 审计轮新增）────────────────────────────
# ★ 为什么 docker 路径也必须拿这把锁：
#   deploy-seamless.sh（245/154 systemd 路径）**早就知道**共享 checkout 的写操作
#   必须单独串行 ⇒ 它在改 version / web/dist / 本地暂存前额外拿
#   【共享构建锁】（kx-llm-gateway-build.lock；scripts/deploy-lib/lock.sh 的注释
#   原文就是「A separate shared build lock protects checkout mutations」）。
#   ★ 而 2026-10-22 引入的 dd_build_web_dist 让**本脚本也开始写同一个共享 checkout**
#   （web/dist、web-mobile/dist、web/public/menu-config.json）
#   ⇒ 若本脚本只拿【每目标部署锁】（kx-llm-gateway-deploy-<target>.lock）
#   ⇒ ⇒ 两把锁**互不排斥** ⇒ ⇒ 一次 211 部署与一次 245 部署并发时，两个进程会在
#   ⇒ ⇒ **同一个 web/dist 里同时写** ⇒ ⇒ 产出可能是【半截的 dist】
#   ⇒ ⇒ 半截 dist 随 tar 上树 ⇒ ⇒ 线上是坏掉的 SPA。
#   ⇒ ⇒ 所以：凡是会写这个 checkout 的路径，都必须拿**同一把**共享构建锁。
#
# 释放时机：tar 同步完成之后（远端部署阶段是长耗时，且远端用的是**另一份拷贝**，
#   不再碰共享 checkout）⇒ ⇒ 与 systemd 路径的语义一致（它也在远端阶段前放锁）。
# ⚠ 释放失败不能盖掉原始退出码 ⇒ 一律 `|| true`。
DD_BUILD_LOCK_HELD=0
dd_release_build_lock() {
  [[ "${DD_BUILD_LOCK_HELD:-0}" == "1" ]] || return 0
  DD_BUILD_LOCK_HELD=0
  lock_release_build >/dev/null 2>&1 || true
}

# =============================================================================
# 前端产物新鲜度门（2026-10-20 211 502 事故收口）
# =============================================================================
# 211 的前端是「部署机预构建 → 随 tar 上树 → 种子进容器」，引擎**不构建**前端，
# 所以「产物是否比源码新」只能在这里判。两处调用：
#   ① --via 侧（部署机）：同步之前就判，失败则连目标机都不碰（最早可判点）；
#   ② 211 本机：远端树才是「真正要部署的那份」，是权威判据。
#
# ★ 为什么这道门必须单独抽出来、且必须在**拆掉旧容器之前**跑：
#   2026-10-20 实测事故——旧顺序是 `docker rm -f 旧容器` 之后才做新鲜度判定，
#   判定一失败（die）就变成「旧容器没了 + 新容器还没起」= llm.itestu.cn 502。
#   守卫是**部署前的输入检查**，它的失败形态必须是「这次部署不发生」，
#   而不是「服务下线」。⇒ 判定与拆服务必须是两段，且判定在前。
#
# 口径：dist/index.html 必须不早于源码树里最新的一个**会进 bundle** 的文件。
# *.test.ts / *.spec.ts 不进 bundle，mtime 漂移不该拦部署（2026-10-15 实测
# LiveRequestStreamV2.queueStreamNotice.test.ts 误杀整次 211 部署）。
dd_web_dist_newer_source() {
  local _dist="$1" _src="$2"
  [[ -d "$_src" ]] || return 0
  find "$_src" -type f \
    ! -name '*.test.ts' ! -name '*.test.tsx' ! -name '*.test.js' ! -name '*.test.jsx' \
    ! -name '*.spec.ts' ! -name '*.spec.tsx' ! -name '*.spec.js' ! -name '*.spec.jsx' \
    -newer "$_dist/index.html" -print -quit 2>/dev/null || true
}

# dd_check_web_dist <dist 目录> <源码目录> <标签>
#   0 = 通过
#   1 = 陈旧（产物比源码旧）—— 逃生舱 DD_ALLOW_STALE_WEB=1 可放行
#   2 = 缺失（压根没有前端）—— 逃生舱可放行（纯 API 形态）
#   3 = 缺失但上一轮种子还在 —— **无逃生舱**：沿用等于无声前端回滚
# 原因文本放在 DD_WEB_CHECK_ERR。
DD_WEB_CHECK_ERR=""
dd_check_web_dist() {
  local _dist="$1" _src="$2" _label="$3" _newer
  DD_WEB_CHECK_ERR=""
  if [[ ! -f "$_dist/index.html" ]]; then
    # 「上一轮种子还在」有两种布局，**都要认**：
    #   ① 旧平铺布局：<root>/<label>/dist/index.html
    #   ② 蓝绿按槽布局：<root>/<label>/<slot>/dist/index.html（当前形态）
    # ⚠ 2026-10-20 修：槽位隔离后只认① ⇒ ②下这条探测恒为「不在」⇒
    #   结论从「缺失但上一轮种子还在（**硬拒绝**，逃生舱也不放行）」掉成
    #   「缺失且无种子（逃生舱可放行走纯 API）」⇒ 逃生舱会把**没有前端**的
    #   版本发上线（用户打开是白页/404），而门禁本该拦住。
    if [[ -n "${DD_GW_DATA_ROOT:-}" ]]; then
      if [[ -f "${DD_GW_DATA_ROOT}/${_label}/dist/index.html" ]] \
         || compgen -G "${DD_GW_DATA_ROOT}/${_label}/*/dist/index.html" >/dev/null 2>&1; then
        DD_WEB_CHECK_ERR="缺少 ${_dist}，但上一轮种子仍在 ${DD_GW_DATA_ROOT}/${_label}/（平铺或按槽）—— 直接沿用等于无声前端回滚。请构建前端（corepack pnpm install --frozen-lockfile && pnpm build）后再部署"
        return 3
      fi
    fi
    DD_WEB_CHECK_ERR="缺少 $_dist/index.html，拒绝部署空前端（2026-10-11 2538 白页事故口径）。构建前端，或显式 DD_ALLOW_STALE_WEB=1 走纯 API 形态"
    return 2
  fi
  _newer="$(dd_web_dist_newer_source "$_dist" "$_src")"
  if [[ -n "$_newer" ]]; then
    DD_WEB_CHECK_ERR="$_dist 陈旧：$_newer 晚于 $_dist/index.html，拒绝部署陈旧前端（否则线上是旧 SPA 而部署显示成功）。重新构建前端，或显式 DD_ALLOW_STALE_WEB=1 走纯 API 形态"
    return 1
  fi
  return 0
}

# dd_preflight_web_dist <repo 根> [side]
#   side=repo   —— 部署机侧：判定**即将被同步上去**的那份产物。
#                   引擎在 211 上是从同步树的 web/dist 种子化的，而
#                   DD_WEB_DIST_SRC 覆盖项不会转发到远端，所以这里必须
#                   判仓库默认路径，不能判覆盖路径（否则会出现「本地判的
#                   是一份、线上发的是另一份」）。
#   side=target —— 211 本机侧：判**远端真正要用的**那份（尊重
#                   DD_WEB_DIST_SRC 覆盖）。
# 任何一项不通过 ⇒ **die**（拒绝部署）。
# ⚠ 2026-10-20 修（逃生舱在本层是**死的**）：本函数原先在「逃生舱放行」时
#   返回 **1**，而调用方写的是 `dd_preflight_web_dist ... || exit 1`
#   ⇒ 返回 1 = 「失败」⇒ **带了 DD_ALLOW_STALE_WEB=1 也照样退出**，
#   逃生舱形同虚设。实测：2026-10-20 真机部署带着逃生舱仍被
#   「web/dist 陈旧」拦死在远端侧。
#   ⇒ 语义改为：**放行 ⇒ 返回 0**（告警已经 warn 过了），与 deploy-local.sh
#     的 `check_web_dist_freshness`（逃生舱 → warn + return 0）同一族口径。
#     本函数只在**必须拒绝**时 die（code=3「上一轮种子还在」或没开逃生舱）。
dd_preflight_web_dist() {
  local _root="$1" _side="${2:-target}" _rc=0 _code
  local _web_dist _web_src _mob_dist _mob_src
  if [[ "$_side" == "repo" ]]; then
    _web_dist="$_root/web/dist";      _web_src="$_root/web/src"
    _mob_dist="$_root/web-mobile/dist"; _mob_src="$_root/web-mobile/src"
  else
    _web_dist="${DD_WEB_DIST_SRC:-$_root/web/dist}";      _web_src="$_root/web/src"
    _mob_dist="${DD_MOBILE_DIST_SRC:-$_root/web-mobile/dist}"; _mob_src="$_root/web-mobile/src"
  fi
  # ⚠ 必须写成 `|| _code=$?`：set -e 下裸调用返回非零会让整个脚本直接退出，
  # 根本走不到下面的分诊。
  _code=0; dd_check_web_dist "$_web_dist" "$_web_src" "web" || _code=$?
  if (( _code != 0 )); then
    if [[ "$_code" == "3" || "$DD_ALLOW_STALE_WEB" != "1" ]]; then
      die "$DD_WEB_CHECK_ERR"
    fi
    warn "${DD_WEB_CHECK_ERR}（DD_ALLOW_STALE_WEB=1：放行，继续部署）"
  fi
  _code=0; dd_check_web_dist "$_mob_dist" "$_mob_src" "web-mobile" || _code=$?
  if (( _code != 0 )); then
    if [[ "$_code" == "3" || "$DD_ALLOW_STALE_WEB" != "1" ]]; then
      die "$DD_WEB_CHECK_ERR"
    fi
    warn "${DD_WEB_CHECK_ERR}（DD_ALLOW_STALE_WEB=1：放行，继续部署）"
  fi
  return $_rc
}

# dd_build_web_dist <repo 根>
#   在**部署机**侧把 web/ 与 web-mobile/ 的前端产物构建出来。
#
#   为什么必须有这一步（2026-10-22「llm.itestu.cn 打开是一段 JSON」事故）：
#   211 的前端是「部署机预构建 → 随 tar 上树 → 种子进容器」，引擎本身**不构建**
#   前端。历史口径把「构建」当成运维手册里的一条前置条件 ⇒ 漏了就出三种故障，
#   而且是**同一个根因链**：
#     ① dist 比源码旧 ⇒ 新鲜度门拦死整次部署（2026-10-22 用户实测）；
#     ② 带 DD_ALLOW_STALE_WEB=1 硬闯 ⇒ 线上是**旧 SPA**，但部署**显示成功**
#        （这比 ① 更坏：门被绕过了，缺陷被藏起来了）；
#     ③ dist 压根不存在（纯 API 形态）⇒ 容器里没有 /app/web/dist
#        ⇒ Go 的 streaming.NewStaticHandler 因目录不存在返回 nil
#        ⇒ GET / 落到「版本 JSON」分支 ⇒ **站点根路径是一段 JSON**。
#   ⇒ 把构建收进脚本：新鲜度门就从「运维手册里的前置条件」变成
#     「脚本自己保证的不变量」，②③ 两种失败形态也一起消失。
#
#   口径与 245/154 的 systemd 路径（scripts/deploy-seamless.sh）**完全一致**：
#     · 包管理器由 scripts/lib/node-pm.sh 单点决策（pnpm 官方 / npm 并行）；
#     · 依赖「缺失 **或** manifest 比 node_modules 新」才按 lockfile 重装
#       （pm_deps_stale，SSOT，两条部署路径共用同一个判据）；
#     · 构建失败 ⇒ die（绝不「构建失败就拿旧产物继续部署」）。
#
#   只在**部署机**侧跑（--via 分支）：211 上没有 node/pnpm，且源码在部署机上
#   才是最新的（远端树只是同步过去的快照）。远端那侧仍然只做新鲜度**判定**
#   （权威判据 = 远端树里真正要部署的那份）。
#
#   逃生舱：DD_SKIP_WEB_BUILD=1 跳过构建（离线/CI 场景）。注意它**不等于**
#   「部署旧前端」：跳过后新鲜度门照常判定，陈旧仍会 die；纯 API 形态要另外
#   显式 DD_ALLOW_STALE_WEB=1（引擎里已有这个逃生舱，语义不变）。
dd_build_web_dist() {
  # ⚠ bash 的 `local a=$1 b=$a` **不行**：local 会先把所有名字声明成未绑定
  #   的局部变量，再逐个赋值 ⇒ `b=$a` 展开时 a 还是未绑定 ⇒ set -u 直接
  #   「_root: unbound variable」把脚本打死（2026-10-22 实测，第一次跑就撞上）。
  #   ⇒ 分成两条 local。
  local _root="$1"
  local _pm_lib="$_root/scripts/lib/node-pm.sh"
  local _dir
  if [[ "${DD_SKIP_WEB_BUILD:-0}" == "1" ]]; then
    warn "DD_SKIP_WEB_BUILD=1：跳过前端自动构建（新鲜度门仍照常判定；纯 API 形态请另设 DD_ALLOW_STALE_WEB=1）"
    return 0
  fi
  [[ -f "$_pm_lib" ]] || die "缺少 ${_pm_lib}（前端包管理器 SSOT 不在场，无法构建前端）"
  # shellcheck source=/dev/null
  source "$_pm_lib"
  # 先把「这台机器能不能构建前端」这件事说清楚，别让 pnpm 缺失伪装成
  # 「安装失败」（node-pm.sh 的报错是「pnpm 与 npm 都不在 PATH 上」）。
  pm_resolve >/dev/null 2>&1 || die "未找到 node/pnpm（前端构建需要 Node.js 20+ 与 pnpm 或 npm）"
  for _dir in web web-mobile; do
    [[ -f "$_root/$_dir/package.json" ]] || die "缺少 $_root/$_dir/package.json（前端源码不在仓内，无法构建）"
    if pm_deps_stale "$_root/$_dir"; then
      log "前端依赖（${_dir}）缺失或过期，按 lockfile 安装"
      pm_install "$_root/$_dir" || die "前端依赖安装失败（${_dir}）—— 拒绝部署"
    fi
    log "构建前端（${_dir}）..."
    pm_run build "$_root/$_dir" || die "前端构建失败（${_dir}）—— 拒绝部署旧产物"
    [[ -f "$_root/$_dir/dist/index.html" ]] || die "前端构建结束但 $_root/$_dir/dist/index.html 不存在（构建产物不完整）—— 拒绝部署"
    log "前端产物就绪：$_root/$_dir/dist"
  done
  return 0
}

# redis-cli 认证参数（由 redis_auth_args 填充；见 Redis 探测段）
REDIS_AUTH=()

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; }

MODE=deploy
VIA=""
FORCE_REDIS_DB=""
# 转发给远端引擎的参数（--via 本身不转发：远端是本机模式执行）。
# ★ 2026-10-20 修：原实现在参数循环里 shift 完再用 `$*` 转发 ⇒ `$*` 恒为空
#   ⇒ `--redis-db 9` 从未到达远端引擎（外壳文档里还写着能传）。
#   现在显式收集，别再靠 `$*`。
DD_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --status)     MODE=status; DD_ARGS+=(--status); shift ;;
    --probe-only) MODE=probe-only; DD_ARGS+=(--probe-only); shift ;;
    --stop)       MODE=stop; DD_ARGS+=(--stop); shift ;;
    --redis-db)   FORCE_REDIS_DB="${2:?--redis-db N}"; DD_ARGS+=(--redis-db "$FORCE_REDIS_DB"); shift 2 ;;
    --via)        VIA="${2:?--via HOST}"; shift 2 ;;
    -h|--help)    usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

CLAIM_FILE="$DD_STATE_DIR/redis-db.claim"
ENV_FILE="$DD_STATE_DIR/env/gateway.env"

# ── AUTO-GENERATOR-ENV:BEGIN ──
# Keep explicit false across blue/green releases. Never source the saved file:
# values must match Gateway's boolean grammar before persistence or SSH forwarding.
dd_auto_generator_env() {
  local env_file="$1" key value line
  AUTO_GENERATOR_ENV=()
  for key in LLM_GATEWAY_AUTO_TITLE_ENABLED LLM_GATEWAY_AUTO_SUMMARY_ENABLED; do
    if [[ -n "${!key+x}" ]]; then
      value="${!key}"
    elif [[ -n "$env_file" && -f "$env_file" ]]; then
      line="$(sed -nE "/^(export )?${key}=/p" "$env_file" | tail -1)"
      [[ -n "$line" ]] || continue
      value="${line#*=}"
      # The engine writes unquoted bools; accept matching operator-added quotes.
      case "$value" in
        \"*\") value="${value#\"}"; value="${value%\"}";;
        \'*\') value="${value#\'}"; value="${value%\'}";;
      esac
    else
      continue
    fi
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    case "$value" in
      1|t|T|true|TRUE|True) value=true;;
      0|f|F|false|FALSE|False) value=false;;
      *) die "invalid auto generator boolean: ${key}";;
    esac
    printf -v "$key" '%s' "$value"
    AUTO_GENERATOR_ENV+=("${key}=${value}")
  done
}
# ── AUTO-GENERATOR-ENV:END ──
# Validate before writes/SSH; a --via caller must not forward its local saved file.
if [[ -n "$VIA" ]]; then
  dd_auto_generator_env ''
else
  dd_auto_generator_env "$ENV_FILE"
fi

# ── 执行位置守卫（2026-10-15）────────────────────────────────────────
# 无参模式 = 「在 211 本机以 root 执行」：状态目录 /opt/llm-gateway-go-211、
# 本机 docker 容器探测、docker build、127.0.0.1:8781 就绪探测全是目标机
# 语境。在部署机（macOS）上裸跑会在 mkdir /opt/llm-gateway-go-211 处报
# Permission denied——那不是权限配置问题，是执行位置不对；而且就算 sudo
# 建出目录，后面操作的也是 Mac 自己的 docker 而不是 211 的。远程部署一律
# 走 --via（tar 同步仓库 → ssh 到目标机以 root 重跑本脚本）。
if [[ -z "$VIA" && "$(uname -s)" == "Darwin" ]]; then
  die "检测到在 macOS 上以本机模式运行。本脚本无参模式必须在目标机（${DD_HOST:-本地}）本机以 root 执行（状态目录 ${DD_STATE_DIR} 是 Linux 路径）；从部署机远程部署请用对应的薄外壳（如 scripts/deploy-211.sh）或 $0 --via ${DD_HOST}（基础镜像可用 DD_GO_BASE_IMAGE/DD_RUNTIME_BASE_IMAGE 覆盖）"
fi

# =============================================================================
# --via：把当前仓库同步到目标机并在目标机上执行本脚本
# =============================================================================
if [[ -n "$VIA" ]]; then
  # ── 主机信息随行清单（两种 --via 路径共用）────────────────────────────
  # 用显式清单而不是「导出全部 DD_*」——内部量（DD_SWITCHED / DD_CANDIDATE_SLOT 等）
  # 转发过去只会制造歧义。
  # ⚠⚠ 2026-10-20 修（会让部署直接失败的一类）：**repo 相对派生的量不能转发**。
  #   DD_WEB_DIST_SRC / DD_MOBILE_DIST_SRC / DD_CONFIGS_SRC 在引擎顶部按
  #   `$DD_REPO_ROOT/<x>` 算出来，而 DD_REPO_ROOT **两侧不同**（部署机是
  #   /Users/…/llm-gateway-go，远端是 /root/llm-gateway-go-211）。
  #   转发过去 = 远端拿着 macOS 路径去种子化 ⇒ 前端产物「缺失」⇒ 撞
  #   「上一轮种子还在」判定 ⇒ **部署直接 die**；configs 则是静默回落内置
  #   脱敏集（sensitive_patterns.yaml 没种进去）。⇒ 一律**不转发**，
  #   让远端按自己的 DD_REPO_ROOT 重算。
  _dd_fwd=""
  for _v in DD_TARGET DD_HOST DD_STATE_DIR DD_REMOTE_ROOT DD_NETWORK \
            DD_SLOT_A_PORT DD_SLOT_B_PORT DD_CONTAINER_PREFIX DD_IMAGE \
            DD_GW_DATA_ROOT DD_NGINX_CONF DD_NGINX_UPSTREAM DD_DOMAIN \
            DD_PG_DB DD_PG_USER DD_PG_CONTAINER DD_PG_IMAGE DD_PGCOLUMNAR_VER \
            DD_REDIS_CONTAINER DD_REDIS_IMAGE DD_REDIS_DB_START DD_REDIS_DB_END \
            DD_BASE_REGISTRY DD_GO_BASE_IMAGE DD_RUNTIME_BASE_IMAGE \
            DD_ALLOW_STALE_WEB DD_PUBLIC_PROBE_IP DD_ADMIN_USER DD_ADMIN_PASSWORD \
            DD_SKIP_LOGIN_SMOKE LLM_GATEWAY_HARD_BLOCK_RULES \
            LLM_GATEWAY_AUTO_TITLE_ENABLED LLM_GATEWAY_AUTO_SUMMARY_ENABLED; do
    if [[ -n "${!_v+x}" ]]; then _dd_fwd+=" ${_v}='${!_v}'"; fi
  done
  _dd_args="${DD_ARGS[@]+"${DD_ARGS[@]}"}"

  # ── 非 deploy 模式（--status / --probe-only / --stop）──────────────────
  # 这三个模式只依赖 docker 与环境变量，**不需要仓库内容** ⇒ 不同步工作树
  # （同步会 rm -rf 目标树，对「只想看一眼状态」是过大的副作用）。
  # 做法：只把引擎单文件送到目标机临时路径跑，跑完即删。
  if [[ "$MODE" != "deploy" ]]; then
    _dd_tmp="/tmp/deploy-seamless-docker.$$.sh"
    log "远程执行 ${MODE}（只送引擎单文件，不同步仓库）..."
    scp "$DD_REPO_ROOT/scripts/deploy-seamless-docker.sh" "$VIA:$_dd_tmp" \
      || die "引擎单文件投递到 ${VIA} 失败"
    ssh "$VIA" "chmod +x '$_dd_tmp' && DD_BUILD_SHA=''${_dd_fwd} bash '$_dd_tmp' ${_dd_args}; rc=\$?; rm -f '$_dd_tmp'; exit \$rc"
    exit $?
  fi

  # ── 部署锁（deploy-lib lock.sh，与 deploy-seamless 同一族 SSOT）────────
  # --via 全程是部署机侧的连续 ssh 步骤（rm → tar 同步 → 清理 → 远端执行），
  # 锁必须在部署机侧包住全程：两个并发 --via 会在 rm -rf vs tar 解包处互相
  # 炸（2026-10-14/15 三次实测 "Directory not empty"/"Cannot mkdir"）。
  # 锁不在场（如 211 本机模式无 deploy-lib）时 warn 跳过，行为同旧版。
  _dd_lib_resolve="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deploy-lib-resolve.sh"
  if [[ -f "$_dd_lib_resolve" ]]; then
    # shellcheck source=deploy-lib-resolve.sh
    source "$_dd_lib_resolve"
  fi
  if [[ -n "${AIAN_DEPLOY_LIB:-}" && -f "$AIAN_DEPLOY_LIB/lock.sh" ]]; then
    # shellcheck source=/dev/null
    source "$AIAN_DEPLOY_LIB/lock.sh"
    LOCK_LOCAL_TARGET="$DD_TARGET"
    if ! lock_acquire_local; then
      die "另一个 ${DD_TARGET} 部署正在本机进行（锁：${LOCK_LOCAL_DIR}）。并发会话请等待其完成；确认无存活部署进程后可 rm -rf 该锁目录重试（勿在对方运行时硬抢）"
    fi
    # ★ 退出码必须被保住（2026-10-22 修）：EXIT trap 的**最后一条命令的退出码**
    #   会成为整个脚本的退出码。原写法 `trap 'lock_release_local' EXIT` 里
    #   lock_release_local 末尾是 `rm -rf`（成功 ⇒ 0）⇒ 任何**隐式**失败
    #   （set -u 的未绑定变量、set -e 的命令失败）都会把退出码洗成 **0**
    #   ⇒ 部署其实没发生，调用方却拿到「成功」。
    #   实测（2026-10-22）：第一次跑自动构建就被 `_root: unbound variable`
    #   打死，而 deploy-211.sh 的退出码是 **0** —— 正是「部署结果是错的」
    #   那种形态（人看到绿字，线上没变）。
    #   ⇒ 先把入口退出码存下来，释放锁之后再用它退出；
    #     并交给 dd_exit_guard_finalize 判「有没有走到完成标记」（fail-closed）。
    #   ⚠ `lock_release_local || true`：锁清理失败不能反过来盖掉原始失败码。
    #   ⚠ dd_release_build_lock || true：同上（共享构建锁的清理也不能盖掉原始码）。
    trap 'rc=$?; lock_release_local || true; dd_release_build_lock || true; dd_exit_guard_finalize "$rc"' EXIT
    log "已取得 ${DD_TARGET} 部署锁: ${LOCK_LOCAL_DIR}"
    # ★ 共享构建锁（2026-10-23 12h 审计轮修）：与 deploy-seamless.sh（245/154
    #   systemd 路径）**同一把**（kx-llm-gateway-build.lock）。
    #   ⇒ 理由见上面 dd_release_build_lock 的注释：每目标部署锁只挡「同目标的两个
    #   --via」，**挡不住「211 docker × 245 systemd」** ⇒ ⇒ 两者会同时写同一份 dist。
    #   ⇒ 拿不到就**拒绝部署**（fail-closed）：宁可不部署，也不要产出一份半截 dist。
    LOCK_LOCAL_BUILD_DIR="${TMPDIR:-/tmp}/kx-llm-gateway-build.lock"
    LOCK_BUILD_TARGET="$DD_TARGET"
    if ! lock_acquire_build; then
      die "共享构建锁被占用（${LOCK_LOCAL_BUILD_DIR}）—— 另一个部署正在改这个共享 checkout（version / web/dist / 本地暂存）⇒ 请等它结束后再重试（勿在对方运行时硬抢）"
    fi
    DD_BUILD_LOCK_HELD=1
    log "已取得共享构建锁: ${LOCK_LOCAL_BUILD_DIR}"
  else
    warn "deploy-lib lock.sh 不在场，本次无部署锁（并发部署可能互相踩）—— ★ 这也意味着【共享构建锁】同样不可用 ⇒ ⇒ 跨部署路径（211 docker × 245/154 systemd）并发写同一份 web/dist 时【没有串行保护】"
  fi
  # ★ 构建提交号随行：目标树不带 .git，远端只能从这里拿到真实 HEAD，用来改写
  #   种子 version.json（否则 /version 显示的是工作区里别的部署留下的戳）。
  #
  # ★★ 必须**在构建前端之前**取（2026-10-22 修）：前端构建会**重新生成**
  #   web/public/menu-config.json（exported_at 每次都是当前时间戳）⇒ 构建一
  #   跑，工作树就「脏」了 ⇒ 随后的 dirty 判定恒为真 ⇒ **每次部署都带
  #   -dirty 后缀**，哪怕源码一个字没改 ⇒ 这个标记就永久失去了告警价值。
  #   ⇒ 取值时机挪到构建之前：
  #     · 源码真的漂了（有人改了 web/src 却没提交）⇒ 仍然是 -dirty（告警有效）；
  #     · 只是构建产物的时间戳变了 ⇒ 不再误报。
  #   （构建生成的那份 menu-config.json 仍会随 tar 上树 —— 那本来就是
  #     要进 SPA 的构建产物。）
  DD_BUILD_SHA="$(git -C "$DD_REPO_ROOT" rev-parse --short=8 HEAD 2>/dev/null || true)"
  # ★ 2026-10-23（12h 审计轮修）：dirty 判定**排除三个版本产物**。
  #   理由：bump-version.sh（systemd 路径 245/154 的 1/9 步）会改
  #   VERSION / version.json / web/public/version.json 三个**被 git 跟踪**的文件
  #   **且从不提交** ⇒ ⇒ 任意一次 245/154 部署之后，这个共享 checkout 就长期脏
  #   ⇒ ⇒ 若把它们算进 dirty 判定 ⇒ ⇒ 2026-10-22 那次「版本戳不再恒带 -dirty」
  #   ⇒ ⇒ 的修复会**从另一扇门被原样顶回来**（告警永久失效）。
  #   ⇒ 版本产物的漂移 ≠ 源码漂移 ⇒ ⇒ 故排除。
  #   ⇒ 门：scripts/ursmcheck/deploy_shared_build_lock_guard_test.go 判据④。
  if [[ -n "$DD_BUILD_SHA" && -n "$(git -C "$DD_REPO_ROOT" status --porcelain --untracked-files=no -- . ':(exclude)VERSION' ':(exclude)version.json' ':(exclude)web/public/version.json' 2>/dev/null)" ]]; then
    DD_BUILD_SHA="${DD_BUILD_SHA}-dirty"
  fi
  # ★ 前端自动构建（2026-10-22）：**先构建，再判新鲜度**。
  # 顺序不能反：门判的是「产物是否比源码新」，不构建就判 ⇒ 门只会把
  # 「运维忘了 build」翻译成一句 FATAL，而正确形态是「脚本自己把产物构建出来」。
  # 构建发生在**部署机**侧（211 上没有 node/pnpm），产物随后随 tar 上树，
  # 远端那道门（权威判据 = 远端树里真正要部署的那份）就自然通过。
  #
  # ★★★ 2026-10-23（12h 审计轮）**订正本行原注释**：
  #   原注释写「放在**拿锁之后**：两个并发部署不能同时构建同一份 dist」。
  #   ⇒ ⇒ **这句只对「同目标的两个 --via」成立**，对「211 docker × 245 systemd」
  #   ⇒ ⇒ **不成立**（两把锁互不排斥）⇒ ⇒ 它是一条**假不变量**。
  #   ⇒ ⇒ 现在真正保证这件事的是**上面那道共享构建锁**（与 deploy-seamless.sh 同一把）：
  #   ⇒ ⇒ 凡是写这个 checkout 的路径都必须拿它 ⇒ ⇒ 跨路径也串行。
  #   ⇒ 门：scripts/ursmcheck/deploy_shared_build_lock_guard_test.go 判据①（顺序）。
  dd_build_web_dist "$DD_REPO_ROOT"
  # ★ 前端产物新鲜度门（2026-10-20 211 502 事故收口）：**在同步之前**判。
  # 事故形态：这道门原先跑在「旧容器已 rm 之后」，一失败就等于「一次部署失败
  # = 服务下线」（llm.itestu.cn 502，根因见 dd_preflight_web_dist 注释）。
  # 放在部署机侧 = 最早可判点：不 sync、不 build、不 push，线上毫发无损。
  dd_preflight_web_dist "$DD_REPO_ROOT" repo || exit 1
  log "同步仓库到 $VIA:${DD_HOST} ..."
  # 先在目标端把工作树整体清空——tar 解包不会删目标已有文件，旧部署残留的
  # macOS `._*` 元数据 / .env* 会一直挂在那让 docker COPY 重新带进镜像。
  # 2026-10-13 真机第三轮翻车根因：第一次同步带进去了 `._*`，加 --exclude 重跑也没用。
  ssh "$VIA" "rm -rf '$DD_REMOTE_ROOT' && mkdir -p '$DD_REMOTE_ROOT'"
  # 用 tar over ssh 排除体积大/无用目录，避免 node_modules、.git 拖慢或撑爆磁盘。
  # .env* 必须排干净：Dockerfile 里是 `COPY . .`，而 .dockerignore 并不排除
  # .env*，所以一旦把 .env.local 同步过去，它的 admin key / secret / 口令就会
  # 进 docker build 上下文、留在镜像层里（docker history 就能翻出来）。
  # ._* / .DS_Store 排除：macOS 同步到 Linux 时 AppleDouble 元数据会进 go:embed，
  # i18n 加载 `._ar.json` 直接 panic invalid character '\x00'。见真机 2026-10-13 翻车。
  # ⚠ 不要排除任何形态的 dist：macOS bsdtar 的 exclude 对无通配模式也按
  # 「任意尾部路径段」语义匹配（实测 --exclude=dist 与 --exclude=./dist 都会
  # 命中 web/dist），root 的 admin SPA 与 /m 移动端就没了——llm.itestu.cn
  # 打开只剩版本 JSON（2026-10-15 两轮实测）。目标树每次同步前整体 rm -rf
  # 重建，陈旧 dist 残留不成立；前端 dist 由部署机预构建后随树上树。
  # ⚠ 同样不要 --exclude=build：会误伤 vendor/.../api/types/build/*.go
  # （2026-10-15 12h 审计 P2）。仓库根无 build/ 目录；大缓存用 .build-local。
  # macOS bsdtar 默认把 quarantine/provenance xattr 打进 archive，GNU tar 刷屏
  # Ignoring unknown extended header；偶发半截同步 → 远端缺 scripts/（RC=127）。
  # COPYFILE_DISABLE + --no-xattrs/--no-mac-metadata（有则用）关掉资源叉/xattr。
  _tar_create=(tar -C "$DD_REPO_ROOT")
  if [[ "$(uname -s)" == "Darwin" ]]; then
    export COPYFILE_DISABLE=1
    _tar_create+=(--no-xattrs)
    tar --help 2>&1 | grep -q -- '--no-mac-metadata' && _tar_create+=(--no-mac-metadata)
  fi
  _tar_create+=(
    --exclude=.git --exclude=node_modules
    --exclude=.build-local
    --exclude='*.log' --exclude='.env' --exclude='.env.*'
    --exclude='._*' --exclude='.DS_Store'
    -cf - .
  )
  "${_tar_create[@]}" | ssh "$VIA" "tar --no-xattrs -C '$DD_REMOTE_ROOT' -xf - && \
        find '$DD_REMOTE_ROOT' -name '._*' -type f -delete && \
        find '$DD_REMOTE_ROOT' -name '.DS_Store' -type f -delete && \
        echo '[${DD_TARGET}] 同步后 ._* 残留: '\$(find '$DD_REMOTE_ROOT' -name '._*' -type f | wc -l)"
  # 双保险：目标机上再扫一遍，真有残留就当场删掉，别留给下一次构建
  ssh "$VIA" "find '$DD_REMOTE_ROOT' -maxdepth 2 -name '.env*' -type f -delete; \
             echo '[${DD_TARGET}] 目标树残留 .env*: '\$(find '$DD_REMOTE_ROOT' -name '.env*' -type f | wc -l) \
             ' ._*: '\$(find '$DD_REMOTE_ROOT' -name '._*' -type f | wc -l)"
  # 半截同步硬门：缺入口脚本就别往下跑（否则 bash 报 No such file → RC=127）
  ssh "$VIA" "test -f '$DD_REMOTE_ROOT/scripts/deploy-seamless-docker.sh'" \
    || die "同步后远端缺少 scripts/deploy-seamless-docker.sh（半截 tar？）；拒跑部署"
  # ★ 共享构建锁到此可以放（2026-10-23 12h 审计轮）：
  #   共享 checkout 的写操作（取 DD_BUILD_SHA → 构建前端 → 打包上树）已全部完成
  #   ⇒ ⇒ 下一个部署可以安全地改这个 checkout ⇒ ⇒ 不必占着锁跑完整个远端阶段。
  #   ⇒ 与 deploy-seamless.sh 的语义一致（它也在远端阶段前放锁）。
  #   ⚠ EXIT trap 里还有一次兜底释放 ⇒ 正常路径与异常路径都只放一次（幂等）。
  dd_release_build_lock
  # 基础镜像 build-arg 变量一并转发：权威仓 registry-<target>.itestu.cn（方案 42）；
  # 拉不到时引擎回落 /opt/kaixuan/base-images 离线包。空值 = 远端走脚本默认。
  # 入口未设时在 --via 侧按 target 拼 registry 默认，避免远端空值回落 docker.io。
  DD_BASE_REGISTRY="${DD_BASE_REGISTRY:-registry-${DD_TARGET}.itestu.cn}"
  DD_GO_BASE_IMAGE="${DD_GO_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/kx-base-golang:1.27-alpine-amd64}"
  DD_RUNTIME_BASE_IMAGE="${DD_RUNTIME_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/alpine:3.22-amd64}"
  log "在 $VIA 上执行 deploy（builder=${DD_GO_BASE_IMAGE} runtime=${DD_RUNTIME_BASE_IMAGE}）..."
  ssh "$VIA" "cd '$DD_REMOTE_ROOT' && DD_BUILD_SHA='${DD_BUILD_SHA}'${_dd_fwd} LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY='${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}' PROBE_TIMEOUT_SECS='${PROBE_TIMEOUT_SECS:-}' bash scripts/deploy-seamless-docker.sh ${_dd_args}"
  _dd_rc=$?
  # 只有远端真的跑完了，才置完成标记（fail-closed 的另一半）。
  if [[ $_dd_rc -eq 0 ]]; then
    DD_DEPLOY_COMPLETED=1
  else
    warn "远端部署返回 ${_dd_rc}（未置完成标记 ⇒ 本地按失败退出）"
  fi
  exit $_dd_rc
fi

# ★ 目标机侧（211 本机执行的那一份）的完成标记守卫。
#   远端这一份**没有** --via 分支里的锁 trap，所以要单独装守卫 ——
#   否则「函数内未绑定变量 ⇒ bash 退出码 0」这条坑在目标机侧照样会把
#   「啥都没干」报成成功（2026-10-22 实测矩阵）。
#   ⚠ 只对 deploy 模式装：--status / --probe-only / --stop 本来就是
#   「看一眼」的动作，不该被「没走到完成标记」判成失败。
if [[ "$MODE" == "deploy" ]]; then
  dd_install_exit_guard
fi

mkdir -p "$DD_STATE_DIR/env" "$DD_STATE_DIR/logs"
chmod 700 "$DD_STATE_DIR" 2>/dev/null || true

# =============================================================================
# 前置：Docker 必须可用
# =============================================================================
need_docker() {
  command -v docker >/dev/null 2>&1 || die "docker 未安装（${DD_HOST:-本机}）"
  docker info >/dev/null 2>&1 || die "docker 守护进程未运行或当前用户无权限（试 sudo docker info）"
}
need_docker
log "Docker: $(docker --version)"

# ★ 前端产物新鲜度门（211 本机侧，远端树 = 真正要部署的那份，是权威判据）。
# 位置：一切破坏性动作之前（PG/Redis 新建、镜像构建推送、拆旧容器都在其后）。
# 2026-10-20 事故根因：这道门原先在 `docker rm -f` **之后** ⇒ 守卫失败＝停机。
# 守卫的失败形态必须是「这次部署不发生」，不是「服务下线」。
# --status/--probe-only/--stop 不该被前端新鲜度挡住（那是查环境的动作）。
if [[ "$MODE" == "deploy" ]]; then
  # ANCHOR: d211-preflight-guard（部署机侧 --via 分支还有一处同名门，见下）
  dd_preflight_web_dist "$DD_REPO_ROOT" || exit 1
fi

# =============================================================================
# 探测一：PostgreSQL 17 + columnar
# =============================================================================
# 探活单个候选容器，输出 "<server_version_num> <columnar_flag> <vector_flag>"；失败返回非 0。
# 关键变更：判定 columnar 不能查 pg_am —— pg_am 是「当前已建扩展的某库里」的访问方法表，
# 复用别人起的容器时若目标库还没建 citus_columnar/pgcolumnar，pg_am 永远是空的。
# 正确口径是查 pg_available_extensions（在 template1 上看安装能力），这才是「装了就能用」。
# 三种实现均认：citus_columnar（Citus 系，acc-pg17 / kx-citus-pg17 就是这种）、
# columnar（Citus 早版本/直挂）、pgcolumnar（独立发行版）。vector 也同时校验。
pg_probe_container() {
  local c="$1" u pw out
  u="$(docker exec "$c" printenv POSTGRES_USER 2>/dev/null || true)"
  u="${u:-postgres}"
  pw="$(docker exec "$c" printenv POSTGRES_PASSWORD 2>/dev/null || true)"
  pw="${pw:-${LLM_GATEWAY_PG_PASSWORD:-}}"
  for cu in "$u" postgres; do
    # ⚠ 不能 `tr -d '[:space:]'`：三段以空格分隔，去掉空格后会被 read 当成一个字段。
    # 这正是 2026-10-13 第一轮误判「acc-pg17 列不满足」的真因：把 "170010 1 1" 压成
    # "17001011"，pg_is_17 判 17001011 ≥ 180000 直接 false → 走「新建」。
    out="$(docker exec -e PGPASSWORD="$pw" "$c" \
      psql -X -U "$cu" -d postgres -tAc \
      "SELECT current_setting('server_version_num')::int
            || ' ' || (CASE WHEN EXISTS (SELECT 1 FROM pg_available_extensions
                                          WHERE name IN ('citus_columnar','columnar','pgcolumnar'))
                            THEN 1 ELSE 0 END)
            || ' ' || (CASE WHEN EXISTS (SELECT 1 FROM pg_available_extensions
                                          WHERE name='vector')
                            THEN 1 ELSE 0 END);" \
      2>/dev/null | tr -d '\r' | head -1)" || continue
    # 兼容 psql 在某些容器里多吐一个 "WARNING:" 头的情形——只取第一段三字段
    out="$(printf '%s\n' "$out" | awk '{print $1, $2, $3}')"
    [[ -n "$out" ]] && { printf '%s\n' "$out"; return 0; }
  done
  return 1
}

pg_is_17()         { local v="$1"; (( v >= 170000 && v < 180000 )); }
pg_has_columnar() { (( "$1" > 0 )); }
pg_has_vector()    { (( "$1" > 0 )); }

# ── 网关库检测（2026-10-18）──────────────────────────────────────────────
# 「这台容器里有没有网关的库」是**独立于**「这台容器是不是 pg17+columnar」的一个维度。
# 211 上同时满足 PG 能力判据的容器有两只：acc-pg17（持有 llm_gateway）与
# redclaw-local-stack-redclaw-casdoor-db-1（redclaw 栈自己的 PG，没有网关库），
# 两者镜像同为 kx-citus-pg17:amd64。只看能力判据无法区分，必须再问库。
#
# 输出 "<has_db> <has_gateway_objects>"，两者都是 0/1：
#   has_db            —— pg_database 里有 $DD_PG_DB
#   has_gateway_objects —— 该库 public schema 下有网关自己的表（schema_migrations /
#                       session_summaries / gateway_db_revision_sequences / api_keys）
# 连接口沿用 pg_probe_container 的解析口径（POSTGRES_USER → postgres），
# 因为复用他人容器时超级用户几乎从不叫 postgres（acc-pg17=acc_super、
# casdoor=casdoor）。注意全程不连 $DD_PG_DB 本身——它可能根本不存在。
pg_gateway_db_state() {
  local c="$1" u pw has objs
  u="$(docker exec "$c" printenv POSTGRES_USER 2>/dev/null || true)"
  u="${u:-postgres}"
  pw="$(docker exec "$c" printenv POSTGRES_PASSWORD 2>/dev/null || true)"
  pw="${pw:-${LLM_GATEWAY_PG_PASSWORD:-}}"
  for cu in "$u" postgres; do
    has="$(docker exec -e PGPASSWORD="$pw" "$c" \
      psql -X -U "$cu" -d postgres -tAc \
      "SELECT count(*) FROM pg_database WHERE datname='${DD_PG_DB}'" \
      2>/dev/null | tr -d '\r' | head -1)" || continue
    has="$(printf '%s' "$has" | tr -cd '0-9')"
    [[ -z "$has" ]] && continue
    if (( has == 0 )); then printf '0 0\n'; return 0; fi
    objs="$(docker exec -e PGPASSWORD="$pw" "$c" \
      psql -X -U "$cu" -d "$DD_PG_DB" -tAc \
      "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'
         AND table_name IN ('schema_migrations','session_summaries',
                            'gateway_db_revision_sequences','api_keys')" \
      2>/dev/null | tr -d '\r' | head -1)" || continue
    objs="$(printf '%s' "$objs" | tr -cd '0-9')"
    (( objs > 0 )) || objs=0
    printf '1 %s\n' "$objs"
    return 0
  done
  printf '0 0\n'
  return 0
}

# 上一次部署绑定的 PG 主机名（写在 gateway.env 的 DSN 里）。这是「上次用的是什么」
# 的**唯一权威记录**，比任何探测都强——但仍要过下面的 has_db 复核才敢直接复用。
pg_persisted_host() {
  [[ -f "$ENV_FILE" ]] || return 0
  sed -nE 's#^export LLM_GATEWAY_DATABASE_URL=postgres://[^@]*@([^:/]+).*#\1#p' "$ENV_FILE" | tail -1
}

# 纯打分（无 IO，可离线驱动）：候选 → 分值，-1 = 不可复用。
# 六个入参：explicit persisted has_db has_objs managed n_qualified
# 判据纪律：唯一允许「什么都探测不到也敢复用」的情形是**全场只有一个**候选
# （那时不存在「选错谁」的可能）；一旦有歧义就走新建，而不是赌 docker ps 的顺序。
pg_candidate_score() {
  local is_explicit="${1:-0}" is_persisted="${2:-0}" has_db="${3:-0}" has_objs="${4:-0}"
  local is_managed="${5:-0}" n_qualified="${6:-1}"
  (( is_explicit )) && { printf '1000\n'; return 0; }
  (( is_persisted )) && { (( has_db )) && { printf '900\n'; return 0; }; printf '200\n'; return 0; }
  (( has_db )) && { (( has_objs )) && { printf '800\n'; return 0; }; printf '700\n'; return 0; }
  (( is_managed )) && { printf '600\n'; return 0; }
  (( n_qualified == 1 )) && { printf '500\n'; return 0; }
  printf -- '-1\n'
}

# 纯选择（无 IO）：入参是候选表，每行
#   "<name> <explicit> <persisted> <has_db> <has_objs> <managed>"
# 输出被选中的容器名；无可复用者返回非 0。
# 并列取**排序后第一个**（调用方按名字排序），保证同样的候选集永远选出同一个，
# 不受 docker ps 顺序影响。
pg_pick_from_candidates() {
  local table="$1" n
  n="$(printf '%s\n' "$table" | grep -c . || true)"
  (( n > 0 )) || return 1
  local name explicit persisted has_db has_objs managed score best="" best_score=-1
  while read -r name explicit persisted has_db has_objs managed; do
    [[ -n "$name" ]] || continue
    score="$(pg_candidate_score "${explicit:-0}" "${persisted:-0}" "${has_db:-0}" \
                                "${has_objs:-0}" "${managed:-0}" "$n")"
    if (( score > best_score )); then
      best_score="$score"; best="$name"
      [[ "$score" -ge 200 ]] && log "  候选 $name: 网关库=${has_db} 网关对象=${has_objs} → 分值 $score"
    fi
  done <<< "$table"
  [[ -n "$best" ]] || return 1
  printf '%s\n' "$best"
}

# 在所有容器里找一个 pg17+columnar 的实例；找到输出容器名，否则返回非 0。
#
# ⚠ 2026-10-13 真机翻车：docker ps --format "{{.Names}}|{{.Image}}" 用 `|` 分隔，
# 但默认 IFS=space+tab+newline，read -r name img 不会在 `|` 切分 → img 永远是空
# → case 关键字匹配只走 lower_name（永远没 *citus* 之类）→ 所有候选全 continue
# → 走「新建」路径 → codeload tarball 没 submodule → 构建炸。
# 修：单行 IFS='|' read 强制按 | 切。
#
# ⚠ 2026-10-18 真机翻车（本函数第二次重写）：旧实现「第一个合格者即返回」。
# 「合格」只覆盖了 pg17+columnar+vector 这三个**能力**维度，而 211 上同时满足
# 能力判据的容器有两只是常态（acc-pg17 与 redclaw 栈自己的
# redclaw-local-stack-redclaw-casdoor-db-1，同为 kx-citus-pg17 镜像）。docker ps
# 的输出顺序既不稳定也不带语义（实测选中了只活了 38 分钟的 casdoor PG），随后
# ensure_pg_database 在错误超级用户上静默失败，最终在几百行之后的迁移腿报
# 「database llm_gateway does not exist」——离根因三层远，且换个重启顺序就换一套
# 症状。⇒ **能力判据与「选谁」必须分离**：先把全部合格者收集齐，再按
#   谁持有网关库 / 谁是上次绑定 / 谁是脚本自建 / 唯一候选
# 的显式优先级打分（pg_candidate_score + pg_pick_from_candidates）。
find_pg_container() {
  local name img lower_name lower_img probe ver col vec
  local persisted has_db has_objs explicit managed persisted_hit list="" picked
  persisted="$(pg_persisted_host)"
  while IFS='|' read -r name img; do
    [[ -z "$name" ]] && continue
    # 容器状态必须是 running 才谈得上"能用"
    [[ "$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || true)" == "running" ]] || continue
    lower_name="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    lower_img="$(printf '%s' "$img" | tr '[:upper:]' '[:lower:]')"
    case "$lower_name$lower_img" in
      *postgres*|*citus*|*" pg"*|*-pg*|*pg17*) ;;
      *) continue ;;
    esac
    if probe="$(pg_probe_container "$name")"; then
      # probe 形如 "<ver> <col> <vec>"，三段用 read 拆
      read -r ver col vec < <(printf '%s\n' "$probe")
      if pg_is_17 "$ver" && pg_has_columnar "$col" && pg_has_vector "$vec"; then
        read -r has_db has_objs < <(pg_gateway_db_state "$name")
        explicit=0; managed=0
        [[ "$name" == "$DD_PG_CONTAINER" ]] && {
          [[ "$DD_PG_CONTAINER_PINNED" == "1" ]] && explicit=1 || managed=1
        }
        [[ -n "$persisted" && "$name" == "$persisted" ]] && persisted_hit=1 || persisted_hit=0
        list+="$name $explicit $persisted_hit ${has_db:-0} ${has_objs:-0} $managed"$'\n'
      else
        log "  候选 $name: 版本=${ver} columnar=${col} vector=${vec} → 不满足(需 PG17+columnar+vector)，继续找"
      fi
    else
      log "  候选 $name: psql 探活失败（可能不是 PG 或需密码），继续找"
    fi
  done < <(docker ps --format '{{.Names}}|{{.Image}}')
  [[ -n "$list" ]] || return 1
  # 按名字排序：把 docker ps 顺序这个隐含随机量从决策里彻底去掉，并列时结果稳定。
  picked="$(pg_pick_from_candidates "$(printf '%s' "$list" | sort)" || true)"
  if [[ -z "$picked" ]]; then
    warn "有合格 PG 实例但都无法判定归属（都没有网关库 ${DD_PG_DB}），不猜——将新建专用实例"
    printf '%s\n' "$list" | while read -r n _ _ _ _ _; do log "  落选 ${n}（无网关库，且存在多个候选）"; done
    return 1
  fi
  read -r _n _e _p _pdb _pobj _m < <(printf '%s' "$list" | awk -v p="$picked" '$1==p {print; exit}')
  local db_desc
  if (( ${_pdb:-0} )); then db_desc="存在（网关对象 ${_pobj:-0}）"; else db_desc="缺失（本次将创建）"; fi
  log "选定 PG 实例: ${picked}（网关库 ${DD_PG_DB} ${db_desc}）"
  printf '%s\n' "$picked"
}

# 新建 PG17+columnar 实例（源码编译 pgcolumnar 扩展打进镜像）
#
# 基础镜像选 pgvector/pgvector:pg17 而不是 postgres:17-*：网关库实际用到 8 个扩展，
#   btree_gist / pg_stat_statements / pg_trgm / pgcrypto / pgstattuple 属 contrib（自带），
#   pgcolumnar 需要源码编译，
#   **vector(pgvector) 在 vanilla postgres 镜像里根本没有** —— 少了它，
#   从本机灌数据时 CREATE EXTENSION vector 直接失败。
# 该镜像是 Debian 系（bookworm），所以构建链走 apt-get 而不是 apk。
create_pg_container() {
  log "未找到匹配的 pg17+columnar 实例，开始新建 ..."
  local build_dir="$DD_STATE_DIR/build"
  mkdir -p "$build_dir"
  cat > "$build_dir/Dockerfile.pgcolumnar" <<EOF
# 由 deploy-seamless-docker.sh 生成：PostgreSQL 17 + pgvector + pgcolumnar
# 关键：pgcolumnar 仓库的 objstore/ 是 git submodule，codeload 的 tarball 不含它，
# make 会在 objstore 步骤报 "No rule to make target 'all'"。改用 git clone
# --recurse-submodules 才是正确拉法。同时补 pkg-config（pgcolumnar 用它解析
# zlib/lz4/zstd 的 link flag，没有就 _inflateInit2_ undefined）。
FROM ${DD_PG_BASE_IMAGE:-pgvector/pgvector:pg17}
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential libzstd-dev liblz4-dev zlib1g-dev pkg-config git curl ca-certificates \
 && git clone --recurse-submodules --depth 1 --branch ${DD_PGCOLUMNAR_VER} \
      https://github.com/commandprompt/pgcolumnar /tmp/pgc \
 && cd /tmp/pgc && make PG_CONFIG=/usr/local/bin/pg_config LDFLAGS="-Wl,--as-needed" \
 && make PG_CONFIG=/usr/local/bin/pg_config LDFLAGS="-Wl,--as-needed" install \
 && rm -rf /tmp/pgc /var/lib/apt/lists/*
EOF
  log "构建 PG 镜像（含 pgcolumnar ${DD_PGCOLUMNAR_VER}），分钟级 ..."
  docker build -t "$DD_PG_IMAGE" -f "$build_dir/Dockerfile.pgcolumnar" "$build_dir" \
    || die "PG 镜像构建失败，见上方输出"

  local pw; pw="${LLM_GATEWAY_PG_PASSWORD:-}"
  if [[ -z "$pw" ]]; then
    pw="$(head -c 24 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
  fi
  printf 'LLM_GATEWAY_PG_PASSWORD=%s\n' "$pw" > "$DD_STATE_DIR/env/pg.secret"
  chmod 600 "$DD_STATE_DIR/env/pg.secret"

  docker rm -f "$DD_PG_CONTAINER" >/dev/null 2>&1 || true
  # pg_stat_statements 必须预加载，否则后续 CREATE EXTENSION 会报
  # "must be loaded via shared_preload_libraries"。镜像 entrypoint 见到首个参数
  # 以 "-" 开头会自动补 "postgres"，所以这里可以直接传 -c。
  docker run -d --name "$DD_PG_CONTAINER" --restart unless-stopped \
    -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD="$pw" \
    -e POSTGRES_DB=postgres \
    -v "${DD_PG_CONTAINER}-data:/var/lib/postgresql/data" \
    "$DD_PG_IMAGE" \
    -c shared_preload_libraries=pg_stat_statements,pgcolumnar \
    -c pg_stat_statements.track=all \
    >/dev/null || die "PG 容器启动失败"

  log "等待 PG 就绪 ..."
  local i
  for i in $(seq 1 60); do
    pg_probe_container "$DD_PG_CONTAINER" >/dev/null 2>&1 && break
    (( i == 60 )) && die "PG 60 次探测后仍未就绪：docker logs $DD_PG_CONTAINER"
    sleep 2
  done
  log "PG 实例就绪: $DD_PG_CONTAINER"
}

# 复用他人容器时的超级用户解析（2026-10-18）。
# ⚠ 旧代码这里硬编码 `-U postgres`。acc-pg17 的超级用户是 acc_super、casdoor 栈的
# 是 casdoor——`-U postgres` 在两者上都直接报「role does not exist」，而那一处
# 被 `2>/dev/null` + `|| warn` 双重吞掉：存在性检查判假不存在 → CREATE DATABASE
# 失败 → 只 warn → 脚本带着一个**根本没建出来的库**继续往下跑，最终在迁移腿报
# 「database llm_gateway does not exist」。根因离症状三百行，且换个容器就换症状。
# ⇒ 解析口径与 pg_probe_container / pg_gateway_db_state 统一（POSTGRES_USER 优先）。
pg_superuser_of() {
  local u
  u="$(docker exec "$1" printenv POSTGRES_USER 2>/dev/null || true)"
  printf '%s\n' "${u:-postgres}"
}

# 以超级用户身份在容器内跑 psql（不吞错误：调用方决定是 warn 还是 die）
pg_as_super() {
  local c="$1" su="$2"; shift 2
  docker exec -e PGPASSWORD="${LLM_GATEWAY_PG_PASSWORD:-}" "$c" \
    psql -X -v ON_ERROR_STOP=1 -U "$su" "$@"
}

# 在选定的 PG 实例里准备网关库/角色（幂等）
ensure_pg_database() {
  local c="$1" su exists
  su="$(pg_superuser_of "$c")"
  # 角色必须先于库创建：CREATE DATABASE ... OWNER 指向不存在的角色会 42704，
  # 而旧顺序（先建库后建角色）正好把这个失败也吞进 warn 里。
  pg_as_super "$c" "$su" -d postgres -c \
    "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='${DD_PG_USER}') THEN CREATE ROLE \"${DD_PG_USER}\" LOGIN PASSWORD '${LLM_GATEWAY_PG_PASSWORD:-}'; END IF; END \$\$;" \
    >/dev/null 2>&1 || die "无法在 PG 容器 $c 上以超级用户 $su 建角色 ${DD_PG_USER}（密码来源 LLM_GATEWAY_PG_PASSWORD）——不继续：带着建不出来的库往下走只会换一个更远的报错"

  # 若已被别的服务建过同名库，直接复用（不覆盖数据）
  exists="$(pg_as_super "$c" "$su" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname='${DD_PG_DB}'" 2>/dev/null | head -1 || true)"
  if [[ "$exists" == "1" ]]; then
    log "数据库 $DD_PG_DB 已存在（容器 ${c}，超级用户 ${su}），复用"
  else
    pg_as_super "$c" "$su" -d postgres -c \
      "CREATE DATABASE \"${DD_PG_DB}\" OWNER \"${DD_PG_USER}\";" \
      >/dev/null 2>&1 \
      || die "在 PG 容器 ${c}（超级用户 ${su}）上创建数据库 $DD_PG_DB 失败；请确认容器归属正确后重试（用 DD_PG_CONTAINER 显式指定）"
    log "已创建数据库 ${DD_PG_DB}（容器 ${c}）"
  fi
  # 兼容多种 columnar 发行：citus_columnar（Citus 系，acc-pg17 即此）/
  # columnar / pgcolumnar / vector + 其他 contrib 扩展一并装上。
  # 网关运行时 00-prereqs.sql 会再 idempotent 装一次，这里是兜底（即使 prereqs
  # 没跑到也能让 USING columnar 立即可用）。
  pg_as_super "$c" "$su" -d "$DD_PG_DB" -v ON_ERROR_STOP=0 -c "
DO \$col\$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='citus_columnar') THEN
    BEGIN EXECUTE 'CREATE EXTENSION IF NOT EXISTS citus WITH SCHEMA pg_catalog'; EXCEPTION WHEN OTHERS THEN NULL; END;
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS citus_columnar WITH SCHEMA pg_catalog';
  ELSIF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='columnar') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS columnar';
  ELSIF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='pgcolumnar') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS pgcolumnar';
  END IF;
END\$col\$;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pgstattuple;
" >/dev/null 2>&1 || warn "扩展预装告警（可能无权限/部分扩展不可用，由 00-prereqs.sql 接管）"
}

# =============================================================================
# 探测二：Redis
# =============================================================================
# 复用别人起的 redis 容器时，密码可能既不在 env 也不在 REDISCLI_AUTH，而是埋在
# `docker inspect ... Cmd` 的 --requirepass 参数里。所以探测期不能假定 LLM_GATEWAY_REDIS_PASSWORD
# 已经设好——redis_cli 第一次先无密码 PING，不通再尝试从启动命令抽密码，再不通才放弃。
# 全局缓存到 REDIS_PASSWORD 以供后续 redis_db_in_use / cluster 探测共用。

redis_try_ping() {
  # $1=container $2=password(可空)；输出 PONG/错误；返回 0/非 0
  # ⚠ 2026-10-13 真机翻车：redis-cli 7 加 -a 时会在 PONG 之前多打一行
  # "Warning: Using a password with '-a' ..."，[[ "$out" == "PONG" ]] 直接不等 → 探活
  # 判失败 → 走「新建 Redis」分支 → 抢了别人的端口。修：再走一次 tail -1 拿 PONG。
  local c="$1" pw="${2:-}"
  local args=()
  [[ -n "$pw" ]] && args=( -a "$pw" )
  docker exec "$c" redis-cli "${args[@]}" PING 2>/dev/null | tail -n 1
}

redis_extract_password_from_cmd() {
  # 从 `docker inspect ... Cmd` + Entrypoint 拼出的字符串里抓 --requirepass 下一个 token
  local c="$1" args="" pw="" prev=""
  args="$(docker inspect -f '{{range .Config.Cmd}}{{.}} {{end}}{{range .Config.Entrypoint}}{{.}} {{end}}' "$c" 2>/dev/null || true)"
  for tok in $args; do
    case "$prev" in
      --requirepass) pw="$tok"; break ;;
    esac
    prev="$tok"
  done
  [[ -n "$pw" ]] && printf '%s' "$pw"
}

# 探活某容器是否可用：返回 0 表示 PING 通，并通过 stdout 末尾写密码（用 \x1f 分隔）。
# 调用方拿到「name\x1fpw」，拆开就够。子 shell 改全局变量在 bash 里无效，
# 所以走 stdout 通道回传更稳。调用方用 `printf '%s\n%s\n' name pw` 也能两行；
# 但本函数只产密码片段即可，name 由调用方自己加。
redis_probe_usable() {
  local c="$1" pw="" out
  pw="$(docker exec "$c" printenv REDISCLI_AUTH 2>/dev/null || true)"
  pw="${pw:-${LLM_GATEWAY_REDIS_PASSWORD:-}}"
  if [[ -z "$pw" ]]; then
    pw="$(redis_extract_password_from_cmd "$c" || true)"
  fi
  out="$(redis_try_ping "$c" "$pw")"
  if [[ "$out" == "PONG" ]]; then
    # stdout 第一行写"PONG 密码"前缀分隔，调用方按行解析拿到 pw。
    # 比「函数内改全局变量」更稳——bash 命令替换里子 shell 的全局赋值
    # 对父 shell 不可见，这是 2026-10-13 真机第四轮翻车根因。
    printf 'PONG\x1f%s\n' "$pw"
    return 0
  fi
  return 1
}

redis_auth_args() {
  if [[ -n "${REDIS_PASSWORD:-}" ]]; then
    REDIS_AUTH=( -a "$REDIS_PASSWORD" )
  else
    REDIS_AUTH=()
  fi
}

redis_ping() {
  local c="$1"
  redis_auth_args
  docker exec "$c" redis-cli "${REDIS_AUTH[@]}" PING 2>/dev/null | grep -q PONG
}

redis_cluster_enabled() {
  local c="$1"
  redis_auth_args
  [[ "$(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" \
      INFO server 2>/dev/null | grep -c '^cluster_enabled:1')" == "1" ]]
}

find_redis_container() {
  local name img ln li
  while IFS='|' read -r name img; do
    [[ -z "$name" ]] && continue
    [[ "$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || true)" == "running" ]] || continue
    ln="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
    li="$(printf '%s' "$img" | tr '[:upper:]' '[:lower:]')"
    case "$ln$li" in
      *redis*) ;;
      *) continue ;;
    esac
    # 探活：redis_probe_usable stdout 写 "PONG\x1f<pw>"。
    #
    # ⚠️ 2026-10-13 12h 审计轮（P1）修正：此前本函数无条件 printf 两行
    # `name\n<pw>`，调用方用 tail -1 取密码。**无密码（trust 模式）时 pw 是空串**，
    # 输出 "name\n\n" 而命令替换的 $() **会剥掉尾部换行** ⇒ tail -1 取回的是
    # 容器名自己。实测 REDIS_PASSWORD=[my-redis]（应为空）。
    # 后果链：redis_auth_args 造出 `-a my-redis`；redis_db_in_use 的 DBSIZE 回包
    # 非数字，撞 `|| return 0`（读不出→保守当占用）⇒ 16 个库全被判占用 ⇒
    # allocate_redis_db die "db15..db0 全被占用"。侥幸存活也会把容器名当密码
    # 写进 env/gateway.env。
    #
    # 改法：**只在确有密码时才输出第二行**，调用方按行数判别。
    # 空密码 → 1 行；有密码 → 2 行。两种情形在 $() 剥尾换行后都无歧义。
    # （不能改成全局变量：REDIS_RAW="$(…)" 在子 shell 里跑，全局赋值传不回来。）
    if probe_out="$(redis_probe_usable "$name")"; then
      local rpw="${probe_out#*$'\x1f'}"
      if [[ -n "$rpw" ]]; then
        printf '%s\n%s\n' "$name" "$rpw"
      else
        printf '%s\n' "$name"          # trust 模式：只有容器名
      fi
      return 0
    fi
    log "  候选 $name: redis PING 失败（无密码+启动命令也抽不出），跳过"
  done < <(docker ps --format '{{.Names}}|{{.Image}}')
  return 1
}

# 某个库号是否已经被占用（有 key）。probe 可注入，便于离线测试。
redis_db_in_use() {
  local c="$1" idx="$2" n
  redis_auth_args
  n="$(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" -n "$idx" DBSIZE 2>/dev/null | tr -d '[:space:]')"
  [[ "$n" =~ ^[0-9]+$ ]] || return 0   # 读不出来 → 保守当作占用
  (( n > 0 ))
}

# ── 网关自占命名空间（封闭集，2026-10-14）────────────────────────────
# claim 复用判定的「自占」识别依据：网关的 Redis 键持久化在复用的实例里
# （nbjl-redis 开 AOF），容器重建后旧库的键还在。此前 allocate 只看 DBSIZE>0，
# 网关自己昨天留下的键让自己永远「占用」自己的库 → 每次 deploy 都下移一号
# （211 实测 15→14→…→5，即挂账的「claim 不粘」根因）。
# ⚠ 新增网关 Redis 键前缀时必须同步本集合，否则漂移复发。
GATEWAY_KEY_PREFIXES=("llmgw:" "session:" "requestjourney:" "ursm:")

# claim 库的采样里是否出现网关自己的键前缀（出现即视为自占，可安全复用）
redis_db_has_gateway_keys() {
  local c="$1" idx="$2" k p
  redis_auth_args
  while IFS= read -r k; do
    [[ -z "$k" ]] && continue
    for p in "${GATEWAY_KEY_PREFIXES[@]}"; do
      case "$k" in "$p"*) return 0 ;; esac
    done
  done < <(docker exec "$c" redis-cli "${REDIS_AUTH[@]}" -n "$idx" --scan 2>/dev/null | head -50)
  return 1
}

create_redis_container() {
  log "未找到可用的 Redis 实例，开始新建 ..."
  docker pull "$DD_REDIS_IMAGE" >/dev/null 2>&1 || warn "拉取 $DD_REDIS_IMAGE 失败，使用本地镜像"
  docker rm -f "$DD_REDIS_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$DD_REDIS_CONTAINER" --restart unless-stopped \
    -v "${DD_REDIS_CONTAINER}-data:/data" \
    "$DD_REDIS_IMAGE" redis-server --appendonly yes >/dev/null \
    || die "Redis 容器启动失败"
  local i
  for i in $(seq 1 30); do
    redis_ping "$DD_REDIS_CONTAINER" && break
    (( i == 30 )) && die "Redis 30 次探测后仍未就绪"
    sleep 1
  done
  log "Redis 实例就绪: $DD_REDIS_CONTAINER"
}

# ── 核心：Redis 库号（"tableid"）从 15 往下倒排分配 ────────────────────
# in_use_fn: 形参为库号，返回 0 表示该库已被占用
# 规则：
#   1. 有持久化 claim 且该库当前仍空闲 → 复用（重跑不漂移）
#   2. 否则从 START(15) 递减到 END(0) 找第一个空闲库
#   3. 全被占 → 报错，不抢别人已用的库
allocate_redis_db() {
  local c="$1" claimed
  if [[ -f "$CLAIM_FILE" ]]; then
    claimed="$(cat "$CLAIM_FILE" 2>/dev/null || true)"
    if [[ "$claimed" =~ ^[0-9]+$ ]]; then
      if ! redis_db_in_use "$c" "$claimed"; then
        log "复用已分配的 Redis 库号 db${claimed}（claim 文件持久化，避免重跑漂移）"
        printf '%s\n' "$claimed"; return 0
      fi
      if redis_db_has_gateway_keys "$c" "$claimed"; then
        # 库非空但采样全是网关自己的键（部署间隙的持久化残留）→ 自占，复用不漂移
        log "复用已分配的 Redis 库号 db${claimed}（库内为网关自身残留键，非他人占用）"
        printf '%s\n' "$claimed"; return 0
      fi
      warn "claim 里的 db${claimed} 被其它服务的键占用，重新分配"
    fi
  fi
  local idx
  for (( idx = DD_REDIS_DB_START; idx >= DD_REDIS_DB_END; idx-- )); do
    if ! redis_db_in_use "$c" "$idx"; then
      printf '%s\n' "$idx" > "$CLAIM_FILE"
      log "已分配 Redis 库号 db${idx}（从 db$DD_REDIS_DB_START 往下倒排，避开其它服务）"
      printf '%s\n' "$idx"; return 0
    fi
  done
  die "db${DD_REDIS_DB_START}..db${DD_REDIS_DB_END} 全被占用，无法为网关分配独立库号"
}

# =============================================================================
# 探测汇总 / status
# =============================================================================
PG_FOUND="$(find_pg_container || true)"
# find_redis_container：**1 行** = 只有容器名（trust 模式，密码为空）；
# **2 行** = 容器名 + 密码。命令替换的 $() 会剥掉尾部换行，故无密码时
# 绝不能多打一个空行——否则 tail -1 会取回容器名自己（见函数内 P1 注释）。
REDIS_RAW="$(find_redis_container || true)"
REDIS_FOUND=""
REDIS_PASSWORD=""
if [[ -n "$REDIS_RAW" ]]; then
  REDIS_FOUND="$(printf '%s\n' "$REDIS_RAW" | head -1)"
  if [[ "$(printf '%s\n' "$REDIS_RAW" | wc -l | tr -d ' ')" -ge 2 ]]; then
    REDIS_PASSWORD="$(printf '%s\n' "$REDIS_RAW" | tail -1)"
  fi
fi

show_status() {
  echo "=== ${DD_TARGET} 资源探测 ==="
  # 蓝绿槽位现状：一眼看出「现在谁在服务、候选槽该用哪个」。
  _st_active="$(dd_active_slot)"
  if [[ "$_st_active" == "a" ]]; then _st_cand="b"; else _st_cand="a"; fi
  _st_up="$(dd_upstream_port || true)"
  echo "槽位 : 活跃=${_st_active}(端口 $(dd_slot_port "$_st_active"))  候选=${_st_cand}(端口 $(dd_slot_port "$_st_cand"))  upstream=${_st_up:-读不到}"
  for _st in a b; do
    if docker inspect --type container "$(dd_container_name "$_st")" >/dev/null 2>&1; then
      echo "容器 : $(dd_container_name "$_st") 存在（端口 $(dd_slot_port "$_st")）"
    fi
  done
  if [[ -n "$PG_FOUND" ]]; then
    local p ver col vec
    p="$(pg_probe_container "$PG_FOUND" || echo '? 0 0')"
    read -r ver col vec < <(printf '%s\n' "$p")
    echo "PG   : 复用容器 $PG_FOUND  (版本=${ver} columnar=${col} vector=${vec})"
    # 谁在服务网关库这件事必须看得见：能力判据合格的容器不止一只，
    # 「选中的这只里到底有没有网关库」才是复用的理由。
    local _db _objs
    read -r _db _objs < <(pg_gateway_db_state "$PG_FOUND")
    echo "       网关库 $DD_PG_DB: $( ((_db)) && echo "存在（网关对象 ${_objs}）" || echo '不存在（将在本次部署中创建）' )"
    local _prev; _prev="$(pg_persisted_host)"
    [[ -n "$_prev" && "$_prev" != "$PG_FOUND" ]] && \
      echo "       ⚠ 上次绑定的是 ${_prev}，与本次选定的不一致"
  else
    echo "PG   : 无可复用的 pg17+columnar+vector 实例（将新建 ${DD_PG_CONTAINER}）"
  fi
  if [[ -n "$REDIS_FOUND" ]]; then
    echo "Redis: 复用容器 $REDIS_FOUND"
    if redis_cluster_enabled "$REDIS_FOUND"; then
      echo "       ⚠ 该实例开启了 cluster（只支持 db0），库号倒排不可用，将固定 db0"
    else
      local cur="(未分配)"
      [[ -f "$CLAIM_FILE" ]] && cur="db$(cat "$CLAIM_FILE")"
      echo "       当前分配库号: $cur  (倒排区间 ${DD_REDIS_DB_START}..${DD_REDIS_DB_END})"
    fi
  else
    echo "Redis: 无可用实例（将新建 ${DD_REDIS_CONTAINER}）"
  fi
}

# ⚠ 2026-10-20：本块必须**排在 --status / --probe-only / --stop 的分派之前**。
#   show_status 会调 dd_active_slot（→ dd_upstream_port / dd_slot_port /
#   dd_container_name）—— 函数在 bash 里是**运行时**解析，定义在调用点之后
#   就是 `command not found`。实测：--status 直接报
#   `dd_active_slot: command not found`（2026-10-20 真机验证抓到）。

# =============================================================================
# 蓝绿：槽位状态 / nginx 切流 / 失败回退
# =============================================================================
# 2026-10-20 211 502 事故的直接教训：单容器形态（`docker rm -f` 旧容器 → 才做
# 输入校验 → 才起新容器）把**任何**拆后失败都放大成「服务下线」。
# 蓝绿形态：旧槽全程在服务，候选槽失败只要丢掉候选即可 —— 与 154/245 的
# deploy-seamless.sh 同语义（候选先起 → 验收 → 切流 → 停旧实例；失败 abort）。
#
# 槽位与端口是**绑定**的：槽 a 永远占 DD_SLOT_A_PORT，槽 b 永远占
# DD_SLOT_B_PORT；「谁是活跃槽」记在 <data root>/run/active-slot。
# 容器名 = <DD_CONTAINER_PREFIX>-<slot>。
DD_ACTIVE_SLOT=""       # 本次开始前在服务的槽
DD_CANDIDATE_SLOT=""    # 本次要发布的槽
DD_SWITCHED=0           # 是否已切过 nginx upstream（决定 abort 时要不要切回）
DD_OLD_STOPPED=0        # 旧槽是否已被停（切流成功后才会停）

dd_slot_port() {
  case "$1" in
    a) printf '%s' "$DD_SLOT_A_PORT" ;;
    b) printf '%s' "$DD_SLOT_B_PORT" ;;
    *) return 1 ;;
  esac
}

dd_container_name() { printf '%s-%s' "$DD_CONTAINER_PREFIX" "$1"; }

# 活跃槽的判定：**以 nginx upstream 实际指向的端口为准**（流量在哪，活跃槽就在
# 哪），upstream 读不到时才回落到状态文件 <data root>/run/active-slot，
# 再兜底为 a（首次部署）。
# ⚠ 为什么不以状态文件为准：状态文件是本脚本的账本，upstream 是**事实**。
# 两者可能因手工改 nginx 而分叉；此时必须以事实为准，否则会把候选槽算成
# 活跃槽 → 切流方向反了。
dd_active_slot() {
  local _f="${DD_GW_DATA_ROOT}/run/active-slot" _s _port
  _port="$(dd_upstream_port || true)"
  case "$_port" in
    "$DD_SLOT_A_PORT") printf 'a'; return 0 ;;
    "$DD_SLOT_B_PORT") printf 'b'; return 0 ;;
  esac
  if [[ -f "$_f" ]]; then
    _s="$(tr -d '[:space:]' < "$_f" 2>/dev/null || true)"
    case "$_s" in a|b) printf '%s' "$_s"; return 0 ;; esac
  fi
  printf 'a'
}

dd_write_active_slot() {
  mkdir -p "${DD_GW_DATA_ROOT}/run"
  printf '%s\n' "$1" > "${DD_GW_DATA_ROOT}/run/active-slot"
  printf '%s\n' "$(dd_slot_port "$1")" > "${DD_GW_DATA_ROOT}/run/active-port"
}

# ── 槽位↔端口绑定体检（2026-10-23，211 的 502 事故收口）────────────────
# ★ 为什么这道闸必须存在（形态与 154/245 根本不同）：
#   · 154/245（systemd 形态）：nginx 里是
#       `include /opt/llm-gateway-go/run/active-upstream.conf;`
#     ⇒ 端口在**部署时生成的文件**里 ⇒ 不存在「端口烤死在 nginx 里」的形态。
#   · 211（docker 形态）：nginx 的 upstream 块里**直接写死**
#       `server 127.0.0.1:<活跃槽端口>;`
#     ⇒ 槽位端口在**两个地方**各存一份（脚本的 DD_SLOT_*_PORT 与 nginx）⇒
#       **可以漂移**。
#   ★ 漂移的真实形态（2026-10-21 17:0x 211 实测）：同机另一个产品
#     `ai-session-manager-asm-1` 占了 127.0.0.1:8782（网关旧 b 槽的端口），
#     而 nginx 的 upstream **仍指向 8782** ⇒ 网关的流量被送到**别的产品**的
#     端口上 ⇒ llm.itestu.cn 502。
#   ★ 而 `dd_active_slot` 在「upstream 端口不匹配任何槽位」时是**静默回退**
#     （状态文件 → 兜底 a）⇒ 引擎会拿着一个**猜出来的**活跃槽去决定
#     「候选槽」与「停哪个槽」⇒ 可能把流量切到没人监听的端口，或停掉
#     **正在服务**的槽 ⇒ 502。
#   ⇒ 这里改成：**先体检；对不上就拒绝部署**，并把现场事实与处置方法一并
#     打出来（属主需要的是「知道该怎么改」，不是「部署失败」四个字）。
#   ⇒ 体检只**读**（nginx conf + 状态文件），不写任何东西。
dd_check_slot_binding() {
  local _port _sa="${DD_SLOT_A_PORT:-}" _sb="${DD_SLOT_B_PORT:-}" _sf _claim
  # 没配槽位端口 ⇒ 这台机器不走蓝绿端口映射 ⇒ 本闸不适用
  [[ -n "$_sa" || -n "$_sb" ]] || return 0
  _port="$(dd_upstream_port || true)"
  # ★ 2026-10-24 12h 审计轮（问题⑨）：读不到 upstream 时**不阻断**（全新主机 /
  #   还没配 nginx ⇒ 本闸不适用，这个前提是成立的），但**必须出声**。
  #   ⇒ 原实现静默 `return 0` ⇒ 活跃槽判定**静默回退到状态文件**
  #   ⇒ 而状态文件记的是上一次部署的槽位 ⇒ 可能在没人看见的情况下
  #   切到/停掉一个**已经不对了**的槽位。
  #   ⇒ 保持 fail-open（不改阻断语义，避免打断全新主机首部署），
  #   但把「我这次没体检成」明确说出来 —— 静默的兜底比报错更危险。
  if [[ -z "$_port" ]]; then
    warn "★ 读不到 nginx upstream 主端口 ⇒ 本次**未做槽位绑定体检** ⇒ 活跃槽判定将回退到状态文件（${DD_GW_DATA_ROOT}/run/active-slot）"
    warn "  ⇒ 若这是全新主机（还没配 nginx）则属正常；若是**存量**机器，请先核对 nginx upstream 指向的端口。"
    return 0
  fi
  case "$_port" in
    "$_sa"|"$_sb") return 0 ;;
  esac
  _sf="${DD_GW_DATA_ROOT}/run/active-slot"
  _claim="(无状态文件)"
  if [[ -f "$_sf" ]]; then
    _claim="$(tr -d '[:space:]' < "$_sf" 2>/dev/null || true)"
    [[ -n "$_claim" ]] || _claim="(空)"
  fi
  die "nginx upstream ${DD_NGINX_UPSTREAM} 指向 127.0.0.1:${_port} —— 该端口**不属于任何配置槽位**（a=${_sa} / b=${_sb}）⇒ 槽位与端口的绑定已与现场脱节。
      拒绝继续部署：活跃槽判定会静默回退到状态文件（记的是 ${_claim}）⇒ 可能把流量切到没人监听的端口、或停掉正在服务的槽 ⇒ 502。
      现场处置（先看清端口是谁在听）：
        ssh ${DD_SSH_TARGET:-本机} 'ss -lntp | grep -E \":(${_sa}|${_sb}|${_port}) \"'
        · 若 ${_port} 上是**别的产品**（211 实测过：ai-session-manager-asm-1 占了 127.0.0.1:8782）⇒ 把 upstream 主 server 行改到网关**实际在服务**的端口（${_sa} 或 ${_sb}），再重跑部署；
        · 若 ${_port} 上**就是**网关容器 ⇒ 端口配置与现场不一致 ⇒ 改 DD_SLOT_*_PORT 使其与现场一致（槽位端口必须静态：活跃槽判定靠端口↔槽位配对）。
      只读体检：bash scripts/ops/deploy-doctor.sh --ssh ${DD_SSH_TARGET:-本机} --slot-a ${_sa} --slot-b ${_sb}"
}

# ---- nginx 切流：改 upstream 块里主 server 行的 127.0.0.1:<port> ----------
# 为什么切 upstream 而不是改容器端口：蓝绿两个槽各绑一个固定端口，切流的
# 动作只能是「把流量指向另一个端口」，容器端口本身不变（与 154/245 的
# active-upstream.conf 片段同一思路，这里是就地改 upstream 块的主 server 行，
# 少一个 include 迁移面）。
dd_upstream_port() {
  local _conf="$DD_NGINX_CONF" _helper="$DD_UPSTREAM_PORT_HELPER"
  [[ -f "$_conf" ]] || return 1
  # ★ 判定逻辑在共享脚本里（掩码后定位 ⇒ 注释里的旧端口不算数）。
  #   不剥注释 ⇒ 读到注释里的旧端口 ⇒ 活跃槽判错 ⇒ 可能切错端口/停掉活着的槽
  #   ⇒ 502（245 的 upstream 块里就留着 `# Original hardcoded server 127.0.0.1:8781;`）。
  [[ -f "$_helper" ]] || return 1
  python3 "$_helper" --conf "$_conf" --upstream "$DD_NGINX_UPSTREAM" 2>/dev/null
}

# dd_switch_upstream <端口> —— 把 upstream 主 server 行改到 <端口> 并 reload。
# 幂等：已经是该端口时不动文件（仍会 reload，nginx 平滑重载不中断连接）。
#
# ★ 2026-10-20 修（判据门 deploy_211_teardown_order_test.sh 场景⑤抓到）：
#   原实现是「先改文件 → 再 nginx -t → 失败就只 warn 不 reload」。
#   失败形态是**潜伏的半切换态**：磁盘上的配置已指向候选端口，nginx 跑的
#   还是旧配置（流量没变，暂不致命）—— 但此后**任何一次** nginx reload
#   （人工的、systemd 的、crontab 的）都会把这份已改未验证的配置加载进去。
#   ⇒ 改成：**先备份改前内容 → 改文件 → nginx -t → 失败则把文件还原**。
#   还原后磁盘与运行态一致，不留半切换态。
dd_switch_upstream() {
  local _port="$1" _conf="$DD_NGINX_CONF" _bak
  [[ -f "$_conf" ]] || { warn "nginx conf 不在场（${_conf}），无法切流"; return 1; }
  if ! grep -q "upstream ${DD_NGINX_UPSTREAM}" "$_conf"; then
    warn "nginx conf 无 upstream ${DD_NGINX_UPSTREAM} 块，无法切流"
    return 1
  fi
  _bak="${_conf}.bak-dd-upstream"
  if [[ ! -f "$DD_UPSTREAM_PORT_HELPER" ]]; then
    warn "upstream 端口判定脚本不在场（${DD_UPSTREAM_PORT_HELPER}）⇒ 拒绝切流（不做半吊子改写）"
    return 1
  fi
  local _sw_out
  # ★ 同一段判定逻辑（掩码后定位 ⇒ 只改**活的上游行**，不会改到注释里那个旧端口；
  #   旧实现会改注释并报 patched ⇒ nginx reload 后活的上游根本没变 ⇒
  #   引擎以为「已切流」⇒ 随后停掉「旧槽」⇒ 流量落空 ⇒ 502）。
  # ★ 退出码语义：0 = 已改/已是目标；2 = noop（块形态不认识）⇒ 走失败分支。
  if _sw_out="$(python3 "$DD_UPSTREAM_PORT_HELPER" \
                  --conf "$_conf" --upstream "$DD_NGINX_UPSTREAM" \
                  --switch "$_port" --backup "$_bak" 2>&1)"; then
    if nginx -t >/dev/null 2>&1; then
      if nginx -s reload >/dev/null 2>&1; then
        log "nginx upstream ${DD_NGINX_UPSTREAM} → 127.0.0.1:${_port}（已 reload）"
        return 0
      fi
      warn "nginx reload 失败（配置已改，需手工 reload）"
      return 1
    fi
    # nginx -t 失败 ⇒ 把配置还原成改前内容：磁盘不得停在「已改未生效」状态
    if cp -f "$_bak" "$_conf" 2>/dev/null; then
      warn "nginx -t 失败，已还原 ${_conf}（备份 ${_bak}）；未 reload"
    else
      warn "nginx -t 失败且还原失败（${_conf}）—— 需手工确认 upstream 指向"
    fi
    return 1
  else
    warn "切流补丁未应用（upstream 块形态变了？判定=${_sw_out:-empty}）"
    return 1
  fi
}

# ---- abort：丢掉候选槽 + 把 upstream 切回旧槽 + 旧槽若已停则拉回 ----------
# 这是蓝绿的核心安全网：**旧槽一直在服务**，任何一步失败都只是「没发上」，
# 而不是「服务下线」。
dd_abort_deploy() {
  local _reason="$1"
  warn "回退本次部署（蓝绿 abort）：$_reason"
  if [[ -n "$DD_CANDIDATE_SLOT" ]]; then
    docker rm -f "$(dd_container_name "$DD_CANDIDATE_SLOT")" >/dev/null 2>&1 || true
    log "候选槽 ${DD_CANDIDATE_SLOT} 已丢弃（容器 $(dd_container_name "$DD_CANDIDATE_SLOT")）"
  fi
  if [[ "$DD_SWITCHED" == "1" && -n "$DD_ACTIVE_SLOT" ]]; then
    if dd_switch_upstream "$(dd_slot_port "$DD_ACTIVE_SLOT")"; then
      log "upstream 已切回旧槽 ${DD_ACTIVE_SLOT}:$(dd_slot_port "$DD_ACTIVE_SLOT")"
    else
      warn "upstream 切回失败 —— 需手工改 ${DD_NGINX_CONF} 的 ${DD_NGINX_UPSTREAM} 块"
    fi
  fi
  if [[ "$DD_OLD_STOPPED" == "1" && -n "$DD_ACTIVE_SLOT" ]]; then
    if docker start "$(dd_container_name "$DD_ACTIVE_SLOT")" >/dev/null 2>&1; then
      log "旧槽 ${DD_ACTIVE_SLOT} 已重新拉起"
    else
      warn "旧槽拉起失败 —— 需手工 docker start $(dd_container_name "$DD_ACTIVE_SLOT")"
    fi
  fi
  if [[ -n "$DD_ACTIVE_SLOT" ]]; then
    dd_write_active_slot "$DD_ACTIVE_SLOT" || true
  fi
  return 0
}

# dd_fail <原因> —— 失败即 abort（旧槽继续服务），然后退出。
dd_fail() {
  local _reason="$1"
  if [[ -n "$DD_CANDIDATE_SLOT" || "$DD_SWITCHED" == "1" ]]; then
    dd_abort_deploy "$_reason" || warn "回退未完全成功，需人工确认槽位与 upstream"
  fi
  die "$_reason"
}

if [[ "$MODE" == "status" ]]; then
  show_status
  exit 0
fi

# --probe-only：探测 + show_status + 真伪密码验证，但不构建不启动
if [[ "$MODE" == "probe-only" ]]; then
  show_status
  if [[ -n "$PG_FOUND" ]]; then
    echo
    echo "PG 复用细节:"
    echo "  container     = $PG_FOUND"
    echo "  POSTGRES_USER = $(docker exec "$PG_FOUND" printenv POSTGRES_USER 2>/dev/null || echo '?')"
    echo "  POSTGRES_DB   = $(docker exec "$PG_FOUND" printenv POSTGRES_DB 2>/dev/null || echo '?')"
    echo "  网关库状态     = $(pg_gateway_db_state "$PG_FOUND")（有网关库, 网关对象数）"
  fi
  if [[ -n "$REDIS_FOUND" ]]; then
    echo
    echo "Redis 复用细节:"
    echo "  container  = $REDIS_FOUND"
    echo "  password 长度 = ${#REDIS_PASSWORD}（空=trust 模式）"
    docker exec "$REDIS_FOUND" redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} INFO server 2>/dev/null | grep -E "redis_version|cluster_enabled|tcp_port" | sed 's/^/    /'
    # 端到端 PING 一次，确认捕获的密码真能通过
    pong="$(docker exec "$REDIS_FOUND" redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} PING 2>/dev/null | tail -n 1)"
    echo "  PING(end-to-end) = $pong"
  fi
  exit 0
fi

if [[ "$MODE" == "stop" ]]; then
  # ★ 停而不删（与「停旧槽」同一口径）：容器与镜像都留着 ⇒ 人工 docker start
  #   即可回到停机前的版本。停掉的容器不占端口（端口由 docker proxy 释放），
  #   下一轮部署的候选槽起得来。
  log "停止网关容器（两个槽 + 历史单机容器；不动 PG/Redis，容器保留可 start 回来）"
  for _slot in a b; do
    _c="$(dd_container_name "$_slot")"
    if docker inspect --type container "$_c" >/dev/null 2>&1; then
      docker stop "$_c" >/dev/null 2>&1 || warn "停止 ${_c} 失败"
    fi
  done
  # 蓝绿之前的单机形态容器（<prefix>，无 -<slot> 后缀）：一并停掉，
  # 否则它还占着槽 a 的端口，下一次蓝绿部署的候选槽起不来。
  if docker inspect --type container "$DD_CONTAINER_PREFIX" >/dev/null 2>&1; then
    docker stop "$DD_CONTAINER_PREFIX" >/dev/null 2>&1 || warn "停止 ${DD_CONTAINER_PREFIX} 失败"
  fi
  exit 0
fi

# =============================================================================
# 复用 / 新建
# =============================================================================
if [[ -n "$PG_FOUND" ]]; then
  log "复用已有 pg17+columnar 实例: $PG_FOUND"
  PG_CONTAINER="$PG_FOUND"
  PG_REUSED=1
  PG_USER="${LLM_GATEWAY_PG_USER:-$(docker exec "$PG_CONTAINER" printenv POSTGRES_USER 2>/dev/null || true)}"
  PG_USER="${PG_USER:-postgres}"
  PG_PASS="${LLM_GATEWAY_PG_PASSWORD:-}"
  if [[ -z "$PG_PASS" ]]; then
    PG_PASS="$(docker exec "$PG_CONTAINER" printenv POSTGRES_PASSWORD 2>/dev/null || true)"
  fi
  # 复用 Citus 系容器时超级用户是 acc_super 而非 POSTGRES_USER；为避免误用，
  # PG_USER 由后续 ensure_pg_database 内部用 postgres 走 DO 块建 role，与复用容器
  # 的实际超级用户解耦——这里只用于 DSN。
  [[ -z "$PG_PASS" ]] && PG_PASS="unused"
else
  create_pg_container
  PG_CONTAINER="$DD_PG_CONTAINER"
  PG_REUSED=0
  PG_PASS="${LLM_GATEWAY_PG_PASSWORD:-}"
  [[ -f "$DD_STATE_DIR/env/pg.secret" ]] && PG_PASS="$(cut -d= -f2- "$DD_STATE_DIR/env/pg.secret")"
fi

if [[ -n "$REDIS_FOUND" ]]; then
  log "复用已有 Redis 实例: $REDIS_FOUND"
  REDIS_CONTAINER="$REDIS_FOUND"
  REDIS_REUSED=1
else
  create_redis_container
  REDIS_CONTAINER="$DD_REDIS_CONTAINER"
  REDIS_REUSED=0
fi

ensure_pg_database "$PG_CONTAINER"

# ── Redis 库号分配（用户要求的"从 15 往前倒排"）────────────────────
if redis_cluster_enabled "$REDIS_CONTAINER"; then
  REDIS_DB=0
  warn "Redis $REDIS_CONTAINER 处于 cluster 模式，仅支持 db0，无法倒排隔离；固定 db0（可能与其它服务共用）"
else
  if [[ -n "$FORCE_REDIS_DB" ]]; then
    REDIS_DB="$FORCE_REDIS_DB"
    printf '%s\n' "$REDIS_DB" > "$CLAIM_FILE"
    log "按 --redis-db 强制指定库号 db$REDIS_DB"
  else
    REDIS_DB="$(allocate_redis_db "$REDIS_CONTAINER")"
  fi
fi

# URSM 专用 Redis db（与网关会话库隔离，避免 persist SCAN 扫会话键）。
# 允许调用方用 URSM_V2_REDIS_DB 覆盖；否则先复用 $ENV_FILE 里上次分配的库号
# （库空或只含网关自身键即视为自占）；都不成立才默认优先 14，跳过 REDIS_DB，找空库。
# 不复用时 URSM 写下的 ursm:* 键会让上次的库判「占用」，库号逐次下移直到 0～15
# 全被占、部署 die（211 实测 14→…→0）。
URSM_REDIS_DB="${URSM_V2_REDIS_DB:-}"
if [[ -z "$URSM_REDIS_DB" && -f "$ENV_FILE" ]]; then
  _prev_ursm="$(sed -nE 's/^(export )?URSM_V2_REDIS_DB=([0-9]+)$/\2/p' "$ENV_FILE" | tail -1)"
  if [[ "$_prev_ursm" =~ ^[0-9]+$ && "$_prev_ursm" != "$REDIS_DB" ]]; then
    if ! redis_db_in_use "$REDIS_CONTAINER" "$_prev_ursm" \
      || redis_db_has_gateway_keys "$REDIS_CONTAINER" "$_prev_ursm"; then
      URSM_REDIS_DB="$_prev_ursm"
      log "复用已分配的 URSM Redis 库号 db${URSM_REDIS_DB}（$ENV_FILE 持久化，避免重跑漂移）"
    fi
  fi
fi
if [[ -z "$URSM_REDIS_DB" ]]; then
  for _cand in 14 13 12 11 10 9 8 7 6 5 4 3 2 1 0; do
    if [[ "$_cand" == "$REDIS_DB" ]]; then
      continue
    fi
    if ! redis_db_in_use "$REDIS_CONTAINER" "$_cand"; then
      URSM_REDIS_DB="$_cand"
      break
    fi
  done
fi
[[ -n "$URSM_REDIS_DB" ]] || die "无法为 URSM 分配独立 Redis db（与会话库 db${REDIS_DB} 冲突且无空库）"
log "URSM 专用 Redis 库号 db${URSM_REDIS_DB}（会话库 db${REDIS_DB}）"

# =============================================================================
# 网络：把网关与复用的 PG/Redis 接到同一个网络，按容器名互访
# =============================================================================
docker network inspect "$DD_NETWORK" >/dev/null 2>&1 || docker network create "$DD_NETWORK" >/dev/null
for c in "$PG_CONTAINER" "$REDIS_CONTAINER"; do
  docker inspect -f '{{json .NetworkSettings.Networks}}' "$c" 2>/dev/null | grep -q "\"$DD_NETWORK\"" \
    || docker network connect "$DD_NETWORK" "$c" >/dev/null 2>&1 \
    || warn "把 $c 接入网络 $DD_NETWORK 失败（若已接入可忽略）"
done

# =============================================================================
# 构建网关镜像（仓库 Dockerfile；基础镜像默认 registry-<target>.itestu.cn）
# =============================================================================
# 内网机直连 docker.io 不通。权威基础镜像仓 = registry-<target>.itestu.cn
# （方案 42；211 ⇒ registry-211.itestu.cn，由薄外壳注入）：
#   builder = lang-base/kx-base-golang:1.27-alpine-amd64
#   runtime = lang-base/alpine:3.22-amd64
# 拉不到时回落 /opt/kaixuan/base-images 离线包（load + retag 到期望名）。
# ★ 默认值必须是**按 target 拼**的，不是写死某个主机名 —— 引擎主机无关。
DD_BASE_REGISTRY="${DD_BASE_REGISTRY:-registry-${DD_TARGET}.itestu.cn}"
DD_GO_BASE_IMAGE="${DD_GO_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/kx-base-golang:1.27-alpine-amd64}"
DD_RUNTIME_BASE_IMAGE="${DD_RUNTIME_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/alpine:3.22-amd64}"
DD_GO_BASE_OFFLINE="${DD_GO_BASE_OFFLINE:-kx-base/golang:1.27-alpine-amd64}"
DD_RUNTIME_BASE_OFFLINE="${DD_RUNTIME_BASE_OFFLINE:-alpine:3.22}"
DD_BASE_IMAGES_DIR="${DD_BASE_IMAGES_DIR:-/opt/kaixuan/base-images}"

# 确保单个基础镜像在本地：已有 → pull 权威仓 → 离线 tag 复用/装载后 retag。
dd_ensure_base_image() {
  local want="$1" offline="$2" tar_glob="$3" img
  if docker image inspect "$want" >/dev/null 2>&1; then
    log "基础镜像已在本地: $want"
    return 0
  fi
  log "拉取基础镜像: $want"
  if docker pull "$want" >/dev/null 2>&1; then
    log "已从 registry 拉取: $want"
    return 0
  fi
  warn "拉取失败，尝试离线回落 -> ${offline} / ${tar_glob}"
  if ! docker image inspect "$offline" >/dev/null 2>&1; then
    if [[ -x "$DD_BASE_IMAGES_DIR/load-base-images.sh" ]]; then
      bash "$DD_BASE_IMAGES_DIR/load-base-images.sh" load \
        || die "离线基础镜像装载失败: ${DD_BASE_IMAGES_DIR}"
    else
      # 最小回落：按 glob 直接 gunzip|load（load 脚本不在场时）
      local hit=""
      # shellcheck disable=SC2086
      for hit in $DD_BASE_IMAGES_DIR/$tar_glob; do
        [[ -f "$hit" ]] || continue
        log "docker load < $hit"
        gunzip -c "$hit" | docker load >/dev/null
        break
      done
    fi
  fi
  if docker image inspect "$offline" >/dev/null 2>&1; then
    docker tag "$offline" "$want"
    log "离线镜像已 retag: $offline → $want"
    return 0
  fi
  # load 后 RepoTags 可能与 offline 名不一致——扫本地找最接近的 golang/alpine
  if img="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E "^(${offline}|${want})$" | head -1)"; then
    docker tag "$img" "$want"
    log "已对齐基础镜像标签: $img → $want"
    return 0
  fi
  die "基础镜像不可用: want=${want} offline=${offline} (${DD_BASE_REGISTRY} 与离线库都未命中; ${DD_TARGET} 不通 docker.io)"
}

dd_ensure_base_images() {
  dd_ensure_base_image "$DD_GO_BASE_IMAGE" "$DD_GO_BASE_OFFLINE" \
    'kx-base-golang-1.27-alpine-amd64.tar.gz'
  dd_ensure_base_image "$DD_RUNTIME_BASE_IMAGE" "$DD_RUNTIME_BASE_OFFLINE" \
    'alpine-3.22-amd64.tar.gz'
}

if [[ ! -f "$DD_REPO_ROOT/Dockerfile" ]]; then
  die "未找到 Dockerfile：$DD_REPO_ROOT/Dockerfile（请用 --via 同步仓库或手动放置）"
fi
dd_ensure_base_images
log "构建网关镜像 ${DD_GW_IMAGE} (builder=${DD_GO_BASE_IMAGE} runtime=${DD_RUNTIME_BASE_IMAGE}) ..."
build_gateway_image() {
  local net_flag="${1:-}"
  (
    cd "$DD_REPO_ROOT"
    # shellcheck disable=SC2086
    docker build $net_flag \
      --build-arg "BASE_REGISTRY=${DD_BASE_REGISTRY}" \
      --build-arg "GO_BASE_IMAGE=${DD_GO_BASE_IMAGE}" \
      --build-arg "RUNTIME_BASE_IMAGE=${DD_RUNTIME_BASE_IMAGE}" \
      -t "$DD_GW_IMAGE" -f Dockerfile .
  )
}
if ! build_gateway_image; then
  # 内网/NAT 下 docker build 默认桥接网络常常拉不动 go module；
  # 换宿主网络重试一次是这类环境的标准解法（基础镜像已在本地，host 网主要
  # 救 GOPROXY）。
  warn "首次构建失败，用 --network=host 重试一次（内网/NAT 常见原因）"
  build_gateway_image "--network=host" || die "网关镜像构建失败（两种网络模式都试过了）"
fi

# 构建成功后把应用镜像推入 registry-<target>（经 loopback 写路径），便于审计与
# 他机拉取。失败只 warn——部署继续用本机刚 build 的 tag。
dd_publish_app_image() {
  local sha="${DD_BUILD_SHA:-latest}" local_reg="127.0.0.1:5000"
  local repo="app-images/llm-gateway-go" tag="${DD_TARGET}-${sha}"
  local push_ref="${local_reg}/${repo}:${tag}"
  local public_ref="${DD_BASE_REGISTRY}/${repo}:${tag}"
  [[ "${DD_PUBLISH_IMAGE:-1}" == "1" ]] || { log "跳过镜像推送（DD_PUBLISH_IMAGE≠1）"; return 0; }
  command -v docker >/dev/null 2>&1 || return 0
  # 仅本机 registry 写路径；公网域名对写有闸门。凭据来自已 login 的 docker config。
  if ! docker image inspect "$DD_GW_IMAGE" >/dev/null 2>&1; then
    warn "应用镜像 $DD_GW_IMAGE 不在本地，跳过推送"
    return 0
  fi
  if ! curl -fsS --connect-timeout 2 "http://${local_reg}/v2/" >/dev/null 2>&1 \
     && ! curl -fsS --connect-timeout 2 -o /dev/null -w '' "http://${local_reg}/v2/" >/dev/null 2>&1; then
    # registry:2 未认证时 /v2/ 返 401 也算在线
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 2 "http://${local_reg}/v2/" || echo 000)"
    [[ "$code" == "401" || "$code" == "200" ]] || {
      warn "本机 registry ${local_reg} 不可达 (code=${code}), 跳过推送 ${public_ref}"
      return 0
    }
  fi
  docker tag "$DD_GW_IMAGE" "$push_ref" || { warn "tag $push_ref 失败"; return 0; }
  if docker push "$push_ref" >/dev/null 2>&1; then
    docker tag "$DD_GW_IMAGE" "$public_ref" >/dev/null 2>&1 || true
    log "应用镜像已推送: ${public_ref}（via ${push_ref}）"
  else
    warn "应用镜像推送失败（可能缺 itestu-rw 登录 127.0.0.1:5000）；本机 tag 仍可用于部署"
  fi
}
dd_publish_app_image

# =============================================================================
# 启动网关容器
# =============================================================================
SECRET_KEY="${LLM_GATEWAY_SECRET_KEY:-}"
if [[ -z "$SECRET_KEY" ]]; then
  # ⚠ SECRET_KEY 决定 api_keys.key_hash 的 HMAC（domains/authentication/
  # verifier.go）。**换了库就得换对SECRET**：2026-10-14 实测，211 库为
  # mac mini 血统（生产 SECRET_KEY）时，用本机旧值/随机值 → 全部 key
  # invalid_key。持久文件 env/gateway.secret 是「这个库对应的秘钥」，
  # 与 DB 血统绑定，不要在换库时沿用旧值。
  if [[ -f "$DD_STATE_DIR/env/gateway.secret" ]]; then
    SECRET_KEY="$(cut -d= -f2- "$DD_STATE_DIR/env/gateway.secret")"
  else
    SECRET_KEY="$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
    printf 'LLM_GATEWAY_SECRET_KEY=%s\n' "$SECRET_KEY" > "$DD_STATE_DIR/env/gateway.secret"
    chmod 600 "$DD_STATE_DIR/env/gateway.secret"
  fi
fi

CORS_ORIGINS="${LLM_GATEWAY_CORS_ORIGINS:-*}"

# 凭据密文解密密钥（r1024 部署轮补）：211 的库是生产数据副本，credentials
# 密文按生产 keyring（kid=legacy）加密；不注入 CREDENTIAL_ENCRYPTION_KEY 时
# 网关回落到 SECRET_KEY 派生钥，解不开信封（node_probe "cannot decrypt:
# unknown format" + 路由全灭 key_decrypt_failed）。与 SECRET_KEY 同款持久化：
# 环境变量优先，其次 gateway.credkey 文件；不自动生成（错钥只会静默全灭）。
CRED_ENC_KEY="${LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY:-}"
if [[ -z "$CRED_ENC_KEY" && -f "$DD_STATE_DIR/env/gateway.credkey" ]]; then
  CRED_ENC_KEY="$(cut -d= -f2- "$DD_STATE_DIR/env/gateway.credkey")"
fi
if [[ -n "$CRED_ENC_KEY" ]]; then
  printf 'LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=%s\n' "$CRED_ENC_KEY" > "$DD_STATE_DIR/env/gateway.credkey"
  chmod 600 "$DD_STATE_DIR/env/gateway.credkey"
fi

# 生产模式（LLM_GATEWAY_ENV=production）的启动门：config.ValidateAuthSecrets 缺任
# 一项即 panic。环境变量优先，其次 env/<文件> 持久值，都没有才首次生成——重跑
# 不轮换。静态 API key 不得用 sk- 前缀（auth_mw 把 sk-* 委托给库校验，静态比对
# 永远走不到）；admin token 按 rule 20 §3 带 ops_ 前缀。
dd_auth_secret() {
  local name="$1" file="$DD_STATE_DIR/env/$2" prefix="$3" val="${!1:-}"
  if [[ -z "$val" && -f "$file" ]]; then
    val="$(cut -d= -f2- "$file")"
  fi
  if [[ -z "$val" ]]; then
    val="${prefix}$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  fi
  printf '%s=%s\n' "$name" "$val" > "$file"
  chmod 600 "$file"
  printf '%s' "$val"
}
GW_STATIC_API_KEY="$(dd_auth_secret LLM_GATEWAY_API_KEY gateway.apikey "gw${DD_TARGET}_")"
GW_ADMIN_API_KEY="$(dd_auth_secret LLM_GATEWAY_ADMIN_API_KEY gateway.adminkey ops_)"
GW_CURSOR_HMAC="$(dd_auth_secret CURSOR_HMAC_SECRET gateway.cursorhmac '')"
[[ -n "$GW_STATIC_API_KEY" && -n "$GW_ADMIN_API_KEY" && ${#GW_CURSOR_HMAC} -ge 32 ]] \
  || die "鉴权密钥生成/读取失败（production 模式启动门会拒启）：检查 $DD_STATE_DIR/env/ 写权限"

# 组装网关要用的连接串（走 docker 网络内的容器名）
# 密码必须 URL-encode（`@` / `:` / `/` 会拆断 DSN；`unused` 表示 trust 模式可不带）
url_encode() { printf '%s' "$1" | python3 -c "import sys, urllib.parse; print(urllib.parse.quote(sys.stdin.read(), safe=''))" 2>/dev/null || printf '%s' "$1"; }
PG_PASS_ENC="$(url_encode "$PG_PASS")"
REDIS_PASS_ENC="$(url_encode "${REDIS_PASSWORD:-}")"
# PG_USER 也得编码（acc-pg17 实际是 acc_super，正常无特殊字符，但保持一致）
PG_USER_ENC="$(url_encode "$PG_USER")"
# ⚠ 2026-10-13 真机第六轮翻车：网关 config.Redis 只认 ADDR/DB/PASSWORD 三段，
# 不认 LLM_GATEWAY_REDIS_URL（v2 旧版协议已废弃）。按 config.RedisAddr / RedisDB /
# RedisPassword 三个独立变量注入。env 文件里仍保留 LLM_GATEWAY_REDIS_URL 仅作
# 运维查看用。
REDIS_ADDR="${REDIS_CONTAINER}:6379"
if [[ -n "${REDIS_PASSWORD:-}" ]]; then
  REDIS_URL="redis://:${REDIS_PASS_ENC}@${REDIS_ADDR}/${REDIS_DB}"
else
  REDIS_URL="redis://${REDIS_ADDR}/${REDIS_DB}"
fi
DB_URL="postgres://${PG_USER_ENC}:${PG_PASS_ENC}@${PG_CONTAINER}:5432/${DD_PG_DB}?sslmode=disable"

# 这个 env 文件是「shell 可 source」的**持久记录**（供运维查、同步脚本读），
# 不是给 docker --env-file 用的：
#   · docker 的 env-file 解析器不剥 `export` 前缀 —— 一行
#     `export FOO=bar` 会被当成 key 叫 "export FOO" 的变量，全部设不进去；
#   · 也不认 %q 的 shell 转义（\?、空格转义等），会把值改写坏。
# 所以容器一律走 -e argv（引号由 bash 负责，不经任何二次解析）。
# ── 硬阻断清单（r1037，2026-10-20）─────────────────────────────────────
# 「哪些规则允许终止回合」由 LLM_GATEWAY_HARD_BLOCK_RULES 决定（scope:rule
# 列表，如 egress:builtin.egress_sink）。出厂默认只有 input 允许硬阻断，
# egress/output 一律不终止回合。
#
# 取值优先级：部署机环境变量 > env 文件里上次落下的值（运维手工改过的那份，
# 跨部署保留） > 不设（用网关内置默认）。
#
# ⚠ env 文件每次部署都被重写 ⇒ 只在部署机 export 一次的话，下一次部署就没了。
#   运维要长期改，就改 env 文件里的这一行（引擎会保留它）。
HARD_BLOCK_RULES="${LLM_GATEWAY_HARD_BLOCK_RULES:-}"
if [[ -z "$HARD_BLOCK_RULES" && -f "$ENV_FILE" ]]; then
  HARD_BLOCK_RULES="$(sed -nE 's/^(export )?LLM_GATEWAY_HARD_BLOCK_RULES=(.*)$/\2/p' "$ENV_FILE" | tail -1)"
  # 去掉 shell 引号形态（bash %q 对空串会写成 ''，对含空格的值会加反斜杠）。
  HARD_BLOCK_RULES="${HARD_BLOCK_RULES%\'}"; HARD_BLOCK_RULES="${HARD_BLOCK_RULES#\'}"
  HARD_BLOCK_RULES="${HARD_BLOCK_RULES%\"}"; HARD_BLOCK_RULES="${HARD_BLOCK_RULES#\"}"
fi
# 格式体检：只接受 scope:rule 列表的字符集。非法值**不注入**——让网关用
# 它的内置默认（且网关那边会报 error 并保持默认），而不是把垃圾塞进容器。
if [[ -n "$HARD_BLOCK_RULES" && ! "$HARD_BLOCK_RULES" =~ ^[A-Za-z0-9:_.*,-]+$ ]]; then
  log "硬阻断清单值含非法字符，忽略并用网关内置默认: ${HARD_BLOCK_RULES}"
  HARD_BLOCK_RULES=""
fi

# ── 网关 → ASM 事件投递开关（2026-10-21）──────────────────────────────
# 网关把 outbox 事件投给 ai-session-manager，需要**两个开关同时非空**：
#   ASM_INTERNAL_ENDPOINT                    —— ASM 的 /internal/v1/events 地址
#   AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET  —— 双方共用的 HMAC 密钥
# 网关侧是双开关 opt-in（cmd/gateway/main.go：两个都齐才起 outbox dispatcher
# 与 outbox writer，否则**一条事件都不投**）；ASM 侧用同一个密钥校验签名。
# 只配一个 = 仍然不投 ⇒ 两个要么都配要么都不配。
#
# 取值优先级：部署机环境变量 > env 文件里上次落下的值（运维手工改过的那份，
# 跨部署保留） > 不设（不投事件）。
# ⚠ env 文件每次部署都被重写 ⇒ 只在部署机 export 一次的话，下一次部署就没了
#   ⇒ 运维要长期开，就改 env 文件里的这两行（引擎会保留它们）。
#
# ── ASM-ENV-BLOCK:BEGIN ──
# 单独成函数是为了能被判据门**真实执行**（scripts/deploy-seamless-asm_env_test.sh
# 按哨兵抽取本函数在沙箱里跑 ⇒ 测的是真代码，不是复制一份的假实现）。
dd_asm_event_env() {
  local env_file="${1:-$ENV_FILE}"
  ASM_INTERNAL_ENDPOINT="${ASM_INTERNAL_ENDPOINT:-}"
  ASM_EVENT_SECRET="${AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET:-}"
  # 2026-10-27 多模态标准接口轮：/v1/summarize 的 asm 在线优先委托。
  # 取值优先级与全文件同款：部署机环境变量 > env 文件里上次落下的值 > 不设。
  LLM_GATEWAY_ASM_BASE_URL="${LLM_GATEWAY_ASM_BASE_URL:-}"
  LLM_GATEWAY_ASM_PLUGIN_ID="${LLM_GATEWAY_ASM_PLUGIN_ID:-}"
  if [[ -f "$env_file" ]]; then
    if [[ -z "$ASM_INTERNAL_ENDPOINT" ]]; then
      ASM_INTERNAL_ENDPOINT="$(sed -nE 's/^(export )?ASM_INTERNAL_ENDPOINT=(.*)$/\2/p' "$env_file" | tail -1)"
    fi
    if [[ -z "$ASM_EVENT_SECRET" ]]; then
      ASM_EVENT_SECRET="$(sed -nE 's/^(export )?AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=(.*)$/\2/p' "$env_file" | tail -1)"
    fi
    if [[ -z "$LLM_GATEWAY_ASM_BASE_URL" ]]; then
      LLM_GATEWAY_ASM_BASE_URL="$(sed -nE 's/^(export )?LLM_GATEWAY_ASM_BASE_URL=(.*)$/\2/p' "$env_file" | tail -1)"
    fi
    if [[ -z "$LLM_GATEWAY_ASM_PLUGIN_ID" ]]; then
      LLM_GATEWAY_ASM_PLUGIN_ID="$(sed -nE 's/^(export )?LLM_GATEWAY_ASM_PLUGIN_ID=(.*)$/\2/p' "$env_file" | tail -1)"
    fi
    # 去掉 shell 引号形态（bash %q 对空串会写成 ''，对含空格的值会加反斜杠）。
    ASM_INTERNAL_ENDPOINT="${ASM_INTERNAL_ENDPOINT%\'}"; ASM_INTERNAL_ENDPOINT="${ASM_INTERNAL_ENDPOINT#\'}"
    ASM_INTERNAL_ENDPOINT="${ASM_INTERNAL_ENDPOINT%\"}"; ASM_INTERNAL_ENDPOINT="${ASM_INTERNAL_ENDPOINT#\"}"
    ASM_EVENT_SECRET="${ASM_EVENT_SECRET%\'}"; ASM_EVENT_SECRET="${ASM_EVENT_SECRET#\'}"
    ASM_EVENT_SECRET="${ASM_EVENT_SECRET%\"}"; ASM_EVENT_SECRET="${ASM_EVENT_SECRET#\"}"
    LLM_GATEWAY_ASM_BASE_URL="${LLM_GATEWAY_ASM_BASE_URL%\'}"; LLM_GATEWAY_ASM_BASE_URL="${LLM_GATEWAY_ASM_BASE_URL#\'}"
    LLM_GATEWAY_ASM_BASE_URL="${LLM_GATEWAY_ASM_BASE_URL%\"}"; LLM_GATEWAY_ASM_BASE_URL="${LLM_GATEWAY_ASM_BASE_URL#\"}"
    LLM_GATEWAY_ASM_PLUGIN_ID="${LLM_GATEWAY_ASM_PLUGIN_ID%\'}"; LLM_GATEWAY_ASM_PLUGIN_ID="${LLM_GATEWAY_ASM_PLUGIN_ID#\'}"
    LLM_GATEWAY_ASM_PLUGIN_ID="${LLM_GATEWAY_ASM_PLUGIN_ID%\"}"; LLM_GATEWAY_ASM_PLUGIN_ID="${LLM_GATEWAY_ASM_PLUGIN_ID#\"}"
  fi
}
# ── ASM-ENV-BLOCK:END ──
dd_asm_event_env "$ENV_FILE"
dd_auto_generator_env "$ENV_FILE"

# ── GATEWAY-ENV-WRITE:BEGIN ──
{
  printf '# llm-gateway-go @ %s（由 deploy-seamless-docker.sh 生成，勿提交）\n' "${DD_HOST:-目标机}"
  printf '# shell 可 source；容器用 -e 注入，不走 docker 的 env 文件参数\n'
  printf 'export LLM_GATEWAY_STORAGE_MODE=full\n'
  printf 'export LLM_GATEWAY_DATABASE_URL=%q\n' "$DB_URL"
  # 网关 config.Redis 解析三段独立变量：ADDR/DB/PASSWORD
  printf 'export LLM_GATEWAY_REDIS_ADDR=%q\n' "$REDIS_ADDR"
  printf 'export LLM_GATEWAY_REDIS_DB=%q\n' "$REDIS_DB"
  printf 'export LLM_GATEWAY_REDIS_PASSWORD=%q\n' "${REDIS_PASSWORD:-}"
  # 兼容老格式：运维人看 env 也能直接拿到完整 DSN
  printf 'export LLM_GATEWAY_REDIS_URL=%q\n' "$REDIS_URL"
  printf 'export LLM_GATEWAY_SECRET_KEY=%q\n' "$SECRET_KEY"
  printf 'export LLM_GATEWAY_CORS_ORIGINS=%q\n' "$CORS_ORIGINS"
  if [[ -n "$CRED_ENC_KEY" ]]; then
    printf 'export LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=%q\n' "$CRED_ENC_KEY"
  fi
  # 2026-10-14：解 URSM ModeOff（SYSTEM_MONITOR 未设）+ persist SCAN 与会话键隔离。
  # URSM 专用 db 优先 14，且不得与网关会话 REDIS_DB 撞车；已被占用则降序找空库。
  printf 'export LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true\n'
  printf 'export URSM_V2_REDIS_DB=%q\n' "$URSM_REDIS_DB"
  printf 'export LLM_GATEWAY_ENV=production\n'
  # 211 是机队内部部署（非客户 first-boot）：未激活时 SPA router 会把
  # / 与管理页软重定向到 /bootstrap 激活向导，表现为「llm.itestu.cn 打不开」。
  # licensing/bootstrap_api.go 认此开关；154/245 靠库内 license 行，211 独立
  # 库 + 容器硬件指纹无对应 license，必须显式钉激活。
  printf 'export LLM_GATEWAY_LICENSE_ACTIVATED=1\n'
  printf 'export LLM_GATEWAY_API_KEY=%q\n' "$GW_STATIC_API_KEY"
  printf 'export LLM_GATEWAY_ADMIN_API_KEY=%q\n' "$GW_ADMIN_API_KEY"
  printf 'export CURSOR_HMAC_SECRET=%q\n' "$GW_CURSOR_HMAC"
  # 硬阻断清单（空 = 用网关内置默认：只有 input 允许硬阻断）。
  if [[ -n "$HARD_BLOCK_RULES" ]]; then
    printf 'export LLM_GATEWAY_HARD_BLOCK_RULES=%q\n' "$HARD_BLOCK_RULES"
  fi
  # 网关 → ASM 事件投递开关（空 = 不投事件）。写进 env 文件是为了**跨部署保留**
  # （运维手工改过的值下次部署还在），不是给 docker --env-file 用的。
  if [[ -n "$ASM_INTERNAL_ENDPOINT" ]]; then
    printf 'export ASM_INTERNAL_ENDPOINT=%q\n' "$ASM_INTERNAL_ENDPOINT"
  fi
  if [[ -n "$ASM_EVENT_SECRET" ]]; then
    printf 'export AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=%q\n' "$ASM_EVENT_SECRET"
  fi
  for setting in "${AUTO_GENERATOR_ENV[@]+"${AUTO_GENERATOR_ENV[@]}"}"; do
    printf 'export %s\n' "$setting"
  done
  # 2026-10-27：长文本总结 asm 在线优先委托（空 = 恒走网关管线）。
  if [[ -n "$LLM_GATEWAY_ASM_BASE_URL" ]]; then
    printf 'export LLM_GATEWAY_ASM_BASE_URL=%q\n' "$LLM_GATEWAY_ASM_BASE_URL"
  fi
  if [[ -n "$LLM_GATEWAY_ASM_PLUGIN_ID" ]]; then
    printf 'export LLM_GATEWAY_ASM_PLUGIN_ID=%q\n' "$LLM_GATEWAY_ASM_PLUGIN_ID"
  fi
} > "$ENV_FILE"
chmod 600 "$ENV_FILE"
# ── GATEWAY-ENV-WRITE:END ──

GW_ENV=(
  -e "LLM_GATEWAY_STORAGE_MODE=full"
  -e "LLM_GATEWAY_DATABASE_URL=${DB_URL}"
  -e "LLM_GATEWAY_REDIS_ADDR=${REDIS_ADDR}"
  -e "LLM_GATEWAY_REDIS_DB=${REDIS_DB}"
  -e "LLM_GATEWAY_REDIS_PASSWORD=${REDIS_PASSWORD:-}"
  -e "LLM_GATEWAY_SECRET_KEY=${SECRET_KEY}"
  -e "LLM_GATEWAY_CORS_ORIGINS=${CORS_ORIGINS}"
  -e "LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true"
  -e "URSM_V2_REDIS_DB=${URSM_REDIS_DB}"
  -e "LLM_GATEWAY_ENV=production"
  -e "LLM_GATEWAY_LICENSE_ACTIVATED=1"
  -e "LLM_GATEWAY_API_KEY=${GW_STATIC_API_KEY}"
  -e "LLM_GATEWAY_ADMIN_API_KEY=${GW_ADMIN_API_KEY}"
  -e "CURSOR_HMAC_SECRET=${GW_CURSOR_HMAC}"
)
if [[ -n "$CRED_ENC_KEY" ]]; then
  GW_ENV+=( -e "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=${CRED_ENC_KEY}" )
fi
# 硬阻断清单（r1037）：空值不注入 ⇒ 网关用内置默认（只有 input 允许硬阻断）。
if [[ -n "$HARD_BLOCK_RULES" ]]; then
  GW_ENV+=( -e "LLM_GATEWAY_HARD_BLOCK_RULES=${HARD_BLOCK_RULES}" )
  log "硬阻断清单 LLM_GATEWAY_HARD_BLOCK_RULES=${HARD_BLOCK_RULES}"
else
  log "硬阻断清单未配置 → 用网关内置默认（只有 input 允许硬阻断，egress/output 不终止回合）"
fi

# ── ASM-ENV-INJECT:BEGIN ──
# 网关 → ASM 事件投递：两个开关都齐才注入（网关侧是双开关 opt-in，
# 只给一个 = 网关照样不投事件）。★ 密钥不进日志。
if [[ -n "$ASM_INTERNAL_ENDPOINT" && -n "$ASM_EVENT_SECRET" ]]; then
  GW_ENV+=( -e "ASM_INTERNAL_ENDPOINT=${ASM_INTERNAL_ENDPOINT}" )
  GW_ENV+=( -e "AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET=${ASM_EVENT_SECRET}" )
  log "ASM 事件投递已启用：${ASM_INTERNAL_ENDPOINT}（共享密钥已配，值不打印）"
else
  log "ASM 事件投递未启用（endpoint_configured=$([[ -n "$ASM_INTERNAL_ENDPOINT" ]] && echo true || echo false) secret_configured=$([[ -n "$ASM_EVENT_SECRET" ]] && echo true || echo false)）—— 网关不会投任何事件"
fi
# 2026-10-27：/v1/summarize 的 asm 在线优先委托（endpoint 空 = 恒走网关
# 管线；plugin_id 缺省 ai-session-manager，由网关 main.go 兜底）。
# ★ ${VAR:-} 形态：本块会被判据门抽进 set -u 沙箱 eval，未设变量必须容错。
if [[ -n "${LLM_GATEWAY_ASM_BASE_URL:-}" ]]; then
  GW_ENV+=( -e "LLM_GATEWAY_ASM_BASE_URL=${LLM_GATEWAY_ASM_BASE_URL}" )
  if [[ -n "${LLM_GATEWAY_ASM_PLUGIN_ID:-}" ]]; then
    GW_ENV+=( -e "LLM_GATEWAY_ASM_PLUGIN_ID=${LLM_GATEWAY_ASM_PLUGIN_ID}" )
  fi
  log "ASM 总结委托已启用：${LLM_GATEWAY_ASM_BASE_URL}"
else
  log "ASM 总结委托未配置（LLM_GATEWAY_ASM_BASE_URL 为空）—— 长文本总结恒走网关管线"
fi
# ── ASM-ENV-INJECT:END ──

# ── AUTO-GENERATOR-INJECT:BEGIN ──
for setting in "${AUTO_GENERATOR_ENV[@]+"${AUTO_GENERATOR_ENV[@]}"}"; do
  GW_ENV+=(-e "$setting")
done
# ── AUTO-GENERATOR-INJECT:END ──

# ── 状态目录出容器（2026-10-13，对齐 211 机 DEPLOY-STANDARDS 总纲）──────
# /app/data（keystore/hotzone/attachments/backups/model-quality）与 /app/logs
# 原先落在容器层——每次部署 docker rm -f 即静默清空（keystore 丢=api key
# 快照/凭据状态回落，hotzone 与模型质量观测归零）。按 211 机总纲「容器是
# 空壳，数据在宿主 /opt/kaixuan/data」改为 bind mount。宿主目录为空且旧
# 容器还在时，先 docker cp 导出一次（迁移；旧容器被 rm 后此步自然跳过）。
# DB（acc-pg17→/opt/kaixuan/data/pg/acc-pg17）与 Redis（→/opt/kaixuan/data/
# redis）按本脚本「优先复用」口径本就落在宿主 bind，不在本块范围。
# 属主对齐容器内运行用户（uid 65532=llmgw）。
DD_GW_DATA_ROOT="${DD_GW_DATA_ROOT:-/opt/kaixuan/data/llm-gateway}"
# ★ 槽位↔端口绑定体检：**先于一切写动作**（mkdir/chown/迁移/起容器/切流都在它后面）
#   ⇒ 绑定已脱节的机器上，本轮部署**一个字节都不写**，只报出处置方法。
dd_check_slot_binding
mkdir -p "$DD_GW_DATA_ROOT/data" "$DD_GW_DATA_ROOT/logs"
chown -R 65532:65532 "$DD_GW_DATA_ROOT" 2>/dev/null || true

# ── 迁移腿（2026-10-14 r1030）：网关容器启动前重放 DB 修订序列 ──────────
# deploy-seamless（245/154）自带 deploy_apply_pending_migrations；本脚本原先
# 缺这一腿——从别机 dump 恢复的库（如 acc-pg17 ← mac pg18 dump）停在旧迁移
# 位（实测 schema_migrations 止于 851），新网关 boot 探测 `_request_logs_hot`
# （迁移 860 改名后的家族名）失败 → 静默降级 lite（readyz 的 mode 字段是唯
# 一肉眼判据）。序列幂等（already applied 无操作、健康路径零 DDL），故每次
# 部署无脑重放是安全默认。
#
# ★ 位置（2026-10-20 211 502 事故收口）：本腿**前移到退役旧容器之前**。
# 迁移是幂等的前向 DDL，154/245 家族同样是「切流前迁移」；放在退役之前 ⇒
# 迁移失败时旧容器还在服务（旧二进制对着已迁移的库），不会把「迁移失败」
# 放大成「服务下线」。
REVISION_SEQ="$(cd "$(dirname "$0")" && pwd)/apply-db-revision-sequence.sh"
if [[ -f "$REVISION_SEQ" ]]; then
  log "重放 DB 修订序列（幂等）→ ${PG_CONTAINER}/${DD_PG_DB}"
  DATABASE_URL="$DB_URL" \
  LLM_GATEWAY_PG_CONTAINER="$PG_CONTAINER" \
  LLM_GATEWAY_PG_USER="${LLM_GATEWAY_PG_USER:-$PG_USER}" \
  LLM_GATEWAY_PG_PASSWORD="$PG_PASS" \
  LLM_GATEWAY_PG_DATABASE="$DD_PG_DB" \
    bash "$REVISION_SEQ" || die "DB 修订序列失败——直接启动会降级 lite，先修库再部署"
else
  echo "warning: apply-db-revision-sequence.sh 不在场，跳过迁移重放" >&2
fi

# ── 蓝绿：确定本次的活跃槽与候选槽 ─────────────────────────────────────
# 活跃槽 = 状态文件记录的槽（缺省 a）；候选槽 = 另一个。
# ⚠ 顺序是硬要求：**槽位判定与全部输入判定（前端新鲜度、迁移重放）都必须在
# 任何破坏性动作之前**；旧槽此时仍在服务，旧槽的容器与种子目录本轮全程不动。
DD_ACTIVE_SLOT="$(dd_active_slot)"
if [[ "$DD_ACTIVE_SLOT" == "a" ]]; then DD_CANDIDATE_SLOT="b"; else DD_CANDIDATE_SLOT="a"; fi
DD_CANDIDATE_CONTAINER="$(dd_container_name "$DD_CANDIDATE_SLOT")"
DD_CANDIDATE_PORT="$(dd_slot_port "$DD_CANDIDATE_SLOT")"
DD_ACTIVE_CONTAINER="$(dd_container_name "$DD_ACTIVE_SLOT")"
DD_ACTIVE_PORT_NOW="$(dd_slot_port "$DD_ACTIVE_SLOT")"
log "蓝绿槽位：活跃=${DD_ACTIVE_SLOT}(端口 ${DD_ACTIVE_PORT_NOW}) → 候选=${DD_CANDIDATE_SLOT}(端口 ${DD_CANDIDATE_PORT})"

# ── configs 随部署种子（2026-10-14）────────────────────────────────────
# 镜像不含 configs/（sensitive_patterns.yaml 缺失时网关回落内置脱敏集，
# 与宿主机部署行为不一致）；docker cp 进容器层则 docker rm -f 即丢。
# 改为宿主 bind（只读），每次部署以仓库文件为准覆盖种子。
# 用 DD_REPO_ROOT（脚本顶部已解析），禁止再 $(cd "$(dirname "$0")/..")——
# docker build 段可能已 cd 走 cwd，相对 scripts/.. 会 No such file（2026-10-15 实测）。
DD_CONFIGS_SRC="${DD_CONFIGS_SRC:-$DD_REPO_ROOT/configs}"
mkdir -p "$DD_GW_DATA_ROOT/configs"
for _cfg in sensitive_patterns.yaml sensitive_words.json; do
  if [[ -f "$DD_CONFIGS_SRC/$_cfg" ]]; then
    cp -f "$DD_CONFIGS_SRC/$_cfg" "$DD_GW_DATA_ROOT/configs/$_cfg"
  else
    echo "warning: $DD_CONFIGS_SRC/$_cfg 不存在，容器将回落内置默认集" >&2
  fi
done
chown -R 65532:65532 "$DD_GW_DATA_ROOT/configs" 2>/dev/null || true

# ── Web 前端随部署种子（2026-10-15）：root SPA + /m 移动端 + 版本元数据 ──
# 网关从 cfg.StaticDir（env LLM_GATEWAY_STATIC_DIR，默认探 cwd 下的
# web/dist/index.html）挂 admin SPA，从 MOBILE_WEB_DIST 挂 /m 移动端，
# 从 version.json 读版本元数据——三者镜像里都没有，root 只会回落版本 JSON。
# 与 configs 同款治理：宿主 bind 只读，每次部署以仓库（部署机预构建）为准
# 覆盖种子。
#
# 2026-10-15 审计 P1 收口：本段原先只 warn 不拒绝，搬运链路通了但「产物是新的 /
# 构建成功 / 缺失要失败」三件事一条没闭环，于是——
#   ① dist 缺失时 rm -rf 不执行，上一版 SPA 继续被挂载 = **无声前端回滚**；
#   ② dist 陈旧时照样种子化，部署照样绿；
#   ③ 就绪只探 /readyz 与 /healthz、不探 /，以上两类都不会让部署失败。
# deploy-local.sh 对同一形态是硬失败（2026-10-11 2538 白页事故收口）。此处对齐
# 同一口径，同时保留「纯 API 形态」逃生舱：DD_ALLOW_STALE_WEB=1。
DD_WEB_DIST_SRC="${DD_WEB_DIST_SRC:-$DD_REPO_ROOT/web/dist}"
DD_MOBILE_DIST_SRC="${DD_MOBILE_DIST_SRC:-$DD_REPO_ROOT/web-mobile/dist}"
DD_ALLOW_STALE_WEB="${DD_ALLOW_STALE_WEB:-0}"

# seed_web_dist <dist 目录> <源码目录> <标签>
# 判定（存在性 + 新鲜度）复用 dd_check_web_dist —— 与部署机侧前置门、
# 211 本机侧前置门是**同一份规则**，避免三处口径漂移。本函数只回答
# 「这份产物能不能用」；搬运在下面单独做。
#   0 陈旧且 DD_ALLOW_STALE_WEB≠1 → 拒绝
#   1 陈旧且 DD_ALLOW_STALE_WEB=1  → 放行（仍会种子化这份旧产物）
#   2 缺失且 DD_ALLOW_STALE_WEB=1  → 纯 API 形态（不挂载前端）
#   3 缺失但上一轮种子还在            → 无逃生舱，直接拒绝
# ⚠ 本函数在**退役旧容器之后**才被调用，所以它的拒绝路径必须走 dd_fail
# （回滚旧容器）而不是裸 die —— 裸 die 会把「守卫失败」变成「服务下线」，
# 这正是 2026-10-20 502 事故的成因。
seed_web_dist() {
  local _dist="$1" _src="$2" _label="$3" _code
  _code=0
  dd_check_web_dist "$_dist" "$_src" "$_label" || _code=$?
  case "$_code" in
    0)
      return 0
      ;;
    1)
      if [[ "$DD_ALLOW_STALE_WEB" != "1" ]]; then
        dd_fail "$DD_WEB_CHECK_ERR"
      fi
      warn "${DD_WEB_CHECK_ERR}（$DD_ALLOW_STALE_WEB=1：放行并照常种子化这份旧产物）"
      return 0
      ;;
    2)
      if [[ "$DD_ALLOW_STALE_WEB" != "1" ]]; then
        dd_fail "$DD_WEB_CHECK_ERR"
      fi
      warn "${DD_WEB_CHECK_ERR}（$DD_ALLOW_STALE_WEB=1：按纯 API 形态继续，本次不挂载前端）"
      return 1
      ;;
    *)
      dd_fail "$DD_WEB_CHECK_ERR"
      ;;
  esac
}

seed_web_dist "$DD_WEB_DIST_SRC" "$DD_REPO_ROOT/web/src" "web" && GW_WEB_DIST_OK=1 || GW_WEB_DIST_OK=0
seed_web_dist "$DD_MOBILE_DIST_SRC" "$DD_REPO_ROOT/web-mobile/src" "web-mobile" && GW_MOBILE_DIST_OK=1 || GW_MOBILE_DIST_OK=0

mkdir -p "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT" "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT"
GW_MOUNTS=(
  -v "${DD_GW_DATA_ROOT}/data:/app/data"
  -v "${DD_GW_DATA_ROOT}/logs:/app/logs"
  -v "${DD_GW_DATA_ROOT}/configs:/app/configs:ro"
)
GW_WEB_ENV=()
# ⚠ 种子目录是**候选槽专属**的（web/<slot>/dist）：旧槽的目录本轮完全不碰。
# 这正是「回退不需要还原种子」的原因 —— 旧槽的 SPA/版本戳原样还在它自己的
# 目录里，不会出现「旧二进制 + 新 SPA」的错配组合。
if [[ "$GW_WEB_DIST_OK" == "1" ]]; then
  rm -rf "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT/dist"
  cp -R "$DD_WEB_DIST_SRC" "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT/dist"
  chown -R 65532:65532 "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT" 2>/dev/null || true
  GW_MOUNTS+=( -v "${DD_GW_DATA_ROOT}/web/$DD_CANDIDATE_SLOT/dist:/app/web/dist:ro" )
  GW_WEB_ENV+=( -e "LLM_GATEWAY_STATIC_DIR=/app/web/dist" )
  log "admin SPA 种子: web/dist → ${DD_GW_DATA_ROOT}/web/$DD_CANDIDATE_SLOT/dist（槽 ${DD_CANDIDATE_SLOT}）"
fi
if [[ "$GW_MOBILE_DIST_OK" == "1" ]]; then
  rm -rf "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT/dist"
  cp -R "$DD_MOBILE_DIST_SRC" "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT/dist"
  chown -R 65532:65532 "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT" 2>/dev/null || true
  GW_MOUNTS+=( -v "${DD_GW_DATA_ROOT}/web-mobile/$DD_CANDIDATE_SLOT/dist:/app/web-mobile/dist:ro" )
  GW_WEB_ENV+=( -e "MOBILE_WEB_DIST=/app/web-mobile/dist" )
  log "移动端 SPA 种子: web-mobile/dist → ${DD_GW_DATA_ROOT}/web-mobile/$DD_CANDIDATE_SLOT/dist（槽 ${DD_CANDIDATE_SLOT}）"
fi
mkdir -p "$DD_GW_DATA_ROOT/version"
if [[ -f "$DD_REPO_ROOT/version.json" ]]; then
  cp -f "$DD_REPO_ROOT/version.json" "$DD_GW_DATA_ROOT/version/$DD_CANDIDATE_SLOT.json"
  # 仓库 version.json 的戳来自最近一次 154/245/本地部署（或提交时的旧值），
  # 不是本次 211 构建；有 DD_BUILD_SHA 时改写成 <tag>-<sha>-<date>-<target>。
  # ⚠ 改写的是**本槽的副本**（version/<slot>.json）：槽位隔离后旧槽的版本戳
  #   不能被本轮改掉（那会让仍在服务/可回滚的旧槽的 /version 说谎）。
  #   ⚠ 2026-10-20 修：原实现改写的是 $DD_GW_DATA_ROOT/version.json（**没被挂载
  #   的另一个路径**）且 python 只传了 2 个参数却读 sys.argv[3] ⇒ IndexError
  #   被 `|| warn` 吞掉 ⇒ 版本戳实际从未被改写。
  if [[ -n "${DD_BUILD_SHA:-}" ]]; then
    python3 - "$DD_GW_DATA_ROOT/version/$DD_CANDIDATE_SLOT.json" "$DD_BUILD_SHA" "$DD_TARGET" <<'PY' \
      || warn "version.json 改写失败，沿用仓库原戳"
import json, sys, time
path, sha, target = sys.argv[1], sys.argv[2], sys.argv[3]
v = json.load(open(path))
date = time.strftime("%Y%m%d")
v.update(git_sha=sha, build_date=date, target=target,
         version="%s-%s-%s-%s" % (v.get("git_tag", "dev"), sha, date, target))
json.dump(v, open(path, "w"), indent=2)
PY
  fi
  chown 65532:65532 "$DD_GW_DATA_ROOT/version/$DD_CANDIDATE_SLOT.json" 2>/dev/null || true
  GW_MOUNTS+=( -v "${DD_GW_DATA_ROOT}/version/$DD_CANDIDATE_SLOT.json:/app/version.json:ro" )
  log "版本元数据种子: version.json（$(python3 -c "import json;print(json.load(open('$DD_GW_DATA_ROOT/version/$DD_CANDIDATE_SLOT.json')).get('version','?'))" 2>/dev/null || echo '?')）"
else
  echo "warning: $DD_REPO_ROOT/version.json 缺失，/version 将显示 dev/unknown" >&2
fi
# ${arr[@]+…} 惯用法：GW_WEB_ENV 可能为空（dist 全缺），bash 3.2 在 set -u 下
# 直接 "${空数组[@]}" 会报 unbound variable
GW_ENV+=( ${GW_WEB_ENV[@]+"${GW_WEB_ENV[@]}"} )

# ── 起候选槽容器（旧槽继续服务 ⇒ 零停机）────────────────────────────────
# 候选槽占用的是**另一个端口**（槽位与端口绑定），所以两个容器可以并存；
# 流量仍由 nginx upstream 指向旧槽，直到切流那一步。
docker rm -f "$DD_CANDIDATE_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$DD_CANDIDATE_CONTAINER" --restart unless-stopped \
  --network "$DD_NETWORK" \
  -p "${DD_CANDIDATE_PORT}:8781" \
  "${GW_MOUNTS[@]}" \
  "${GW_ENV[@]}" \
  "$DD_GW_IMAGE" >/dev/null || dd_fail "候选槽容器启动失败（槽 ${DD_CANDIDATE_SLOT}，端口 ${DD_CANDIDATE_PORT}）"
log "候选槽已起：${DD_CANDIDATE_CONTAINER} @ ${DD_CANDIDATE_PORT}（旧槽 ${DD_ACTIVE_SLOT} @ ${DD_ACTIVE_PORT_NOW} 仍在服务）"

# =============================================================================
# 就绪验证
# =============================================================================
log "等待网关就绪 ..."
# PROBE_TIMEOUT_SECS 与 deploy-154/245 同一族标准（deploy-211.sh 入口默认
# export 120）。⚠ 必须带 :- 兜底：ssh 远端执行时该变量不一定穿过（bash 4.4+
# 的 set -u 会让未绑定变量进算术直接炸——2026-10-15 实测翻车点）；
# 2s 步进，轮次=超时/2，下限 1 防呆。
ready=0
ready_body=""
probe_rounds=$(( ${PROBE_TIMEOUT_SECS:-120} / 2 )); (( probe_rounds >= 1 )) || probe_rounds=1
for i in $(seq 1 "$probe_rounds"); do
  if ready_body="$(curl -fsS "http://127.0.0.1:${DD_CANDIDATE_PORT}/readyz" 2>/dev/null)"; then
    ready=1; break
  fi
  if curl -fsS "http://127.0.0.1:${DD_CANDIDATE_PORT}/healthz" >/dev/null 2>&1; then
    # healthz 通但 readyz 不通：继续等到 readyz，避免把半就绪当成功
    :
  fi
  sleep 2
done
if (( ready == 0 )); then
  warn "网关未在预期时间内就绪，最近日志："
  docker logs --tail 40 "$DD_CANDIDATE_CONTAINER" >&2 || true
  dd_fail "网关部署失败"
fi

# 就绪不等于可用：历史上 lite 回落也返 HTTP 200。当前 readyz 契约是
# status=ready + database.connected + redis.connected（无独立 mode 字段）；
# 若将来重新出现 mode=lite 字面，同样判失败。
ready_check="$(printf '%s' "$ready_body" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin)
except Exception as e:
  print("parse_error"); raise SystemExit(0)
mode=(d.get("mode") or d.get("storage_mode") or "")
if str(mode).lower()=="lite":
  print("lite"); raise SystemExit(0)
st=str(d.get("status") or "").lower()
db=d.get("database") if isinstance(d.get("database"), dict) else {}
rd=d.get("redis") if isinstance(d.get("redis"), dict) else {}
ok = (st in ("ready","ok","healthy")
      and bool(db.get("connected") is True or db.get("ok") is True or db=={})
      and bool(rd.get("connected") is True or rd.get("ok") is True or rd=={}))
# 有 database/redis 对象时必须 connected；都没有对象时只信 status（旧形态）
if "database" in d and db.get("connected") is not True:
  print("db_down"); raise SystemExit(0)
if "redis" in d and rd.get("connected") is not True:
  print("redis_down"); raise SystemExit(0)
print("ok" if ok or st=="ready" else "not_ready")
' 2>/dev/null || echo parse_error)"
case "$ready_check" in
  ok) log "readyz OK (database+redis connected, status=ready)" ;;
  lite)
    docker logs --tail 40 "$DD_CANDIDATE_CONTAINER" >&2 || true
    dd_fail "部署验证失败：readyz mode=lite（DB/迁移未接上）"
    ;;
  db_down|redis_down|not_ready|parse_error|*)
    warn "readyz 正文: $ready_body"
    docker logs --tail 40 "$DD_CANDIDATE_CONTAINER" >&2 || true
    dd_fail "部署验证失败：readyz 未证明 DB/Redis 就绪（check=${ready_check}）"
    ;;
esac

# 版本戳与前端：部署成功后必须能读到本次构建痕迹（有 DD_BUILD_SHA 时）
if ver_body="$(curl -fsS "http://127.0.0.1:${DD_CANDIDATE_PORT}/version" 2>/dev/null || true)"; then
  log "version: $(printf '%s' "$ver_body" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin); print(d.get("version") or d)
except Exception:
  print(sys.stdin.read()[:120])' 2>/dev/null || printf '%s' "$ver_body" | head -c 120)"
  if [[ -n "${DD_BUILD_SHA:-}" ]] && ! printf '%s' "$ver_body" | grep -qF "${DD_BUILD_SHA%-dirty}"; then
    warn "version 未包含构建提交 ${DD_BUILD_SHA}（可能种子失败）；继续但标黄"
  fi
fi
if [[ "${GW_WEB_DIST_OK:-0}" == "1" ]]; then
  root_ct="$(curl -fsSI "http://127.0.0.1:${DD_CANDIDATE_PORT}/" 2>/dev/null | tr -d '\r' | awk -F': ' 'tolower($1)=="content-type"{print tolower($2); exit}')"
  case "$root_ct" in
    text/html*) log "admin SPA 可达 (Content-Type: ${root_ct})" ;;
    *) warn "admin SPA 探测异常 Content-Type='${root_ct:-empty}' (期望 text/html)" ;;
  esac
fi

# ── nginx <域名> TLS 腿：X-Real-IP 必须是 $remote_addr ──────────────────
# 旧配置用 map 变量 ns_real_ip(= 入站 X-Real-IP 原样)，浏览器不带该头时为空，
# 限流/验证码只能落到 docker 桥接 RemoteAddr，多客户端共桶。幂等修补。
#
# ★★ 2026-10-23 重构（211 部署复验轮 F2）：判定逻辑**挪出**引擎，放到
#   scripts/ops/nginx-llm-realip-patch.py（可离线用夹具验证，见
#   tests/nginx_llm_realip_patch_test.sh）；引擎只做「调用 + 按分类处置」。
#   修掉的三个真缺陷（详见该脚本头注）：
#   ① ★ 只在**自己的 conf** 里找 443 块 ⇒ 211（443 腿在**通用 TLS 终结器**
#      里，server_name 不含该域）**永远找不到** ⇒ 每次部署打一条**假**告警
#      「块形态变了?」——既不是形态变了，也不是该修的没修成，而是**本机压根
#      不承担该域的 443**。⇒ 现在跨文件搜，且把「没找到」分成
#      「确实没有」（信息）与「形态不认识」（才告警）两种结论。
#   ② ★ 域名**写死** llm.itestu.cn（权威是 DD_DOMAIN）⇒ 换域名会静默改错对象。
#   ③ ★ `nginx -t` 失败时**不还原**文件 ⇒ 磁盘上留着没验证过的配置，
#      之后任何一次 reload 都会加载它（潜伏半切换态；同仓审计
#      docs/audit/2026-10-20-211-bluegreen-docker-engine.md §4.1 已定过形态）。
#
# ★ 处置一律按补丁器给出的**分类**（RESULT=），**不靠退出码做分支**
#   （原实现正是在这里踩过 `if ! cmd; then 成功 else 失败` 的反转坑）。
dd_fix_nginx_llm_real_ip() {
  local domain="${DD_DOMAIN:-}"
  local patcher="$DD_REPO_ROOT/scripts/ops/nginx-llm-realip-patch.py"
  if [[ -z "$domain" ]]; then
    log "nginx X-Real-IP：未配置 DD_DOMAIN ⇒ 跳过（不猜域名）"
    return 0
  fi
  if [[ ! -f "$patcher" ]]; then
    warn "nginx X-Real-IP 修补脚本不在场（${patcher}）⇒ 跳过"
    return 0
  fi
  # 候选：自己的 conf（切流用的那个）+ 标准 nginx 配置目录（★ 跨文件找 443 腿）
  # ★ 搜索根可覆盖（DD_NGINX_SEARCH_ROOTS，冒号分隔）：默认写死主机路径会让
  #   这段逻辑**无法离线验证**（测试机上 /etc/nginx 不存在 ⇒ 永远只扫一个文件）
  #   ⇒ 给了旋钮，门才能在临时目录里跑真引擎函数。
  local roots=()
  local d rest
  rest="${DD_NGINX_SEARCH_ROOTS:-/etc/nginx/conf.d:/etc/nginx/sites-enabled}"
  while [[ -n "$rest" ]]; do
    d="${rest%%:*}"
    if [[ "$rest" == *:* ]]; then rest="${rest#*:}"; else rest=""; fi
    [[ -n "$d" && -d "$d" ]] && roots+=(--search-root "$d")
  done
  # 遗留旋钮（历史上有主机用它指定「承载该域的另一个 conf」）：一并纳入候选
  local confs=(--conf "$DD_NGINX_CONF")
  [[ -n "${DD_NGINX_LLM_CONF:-}" ]] && confs+=(--conf "$DD_NGINX_LLM_CONF")
  local out result=""
  out="$(python3 "$patcher" --domain "$domain" "${confs[@]}" \
          ${roots[@]+"${roots[@]}"} --apply 2>&1)" || {
    warn "nginx X-Real-IP 修补脚本执行失败：${out}"
    return 0
  }
  result="$(printf '%s\n' "$out" | sed -n 's/^RESULT=//p' | head -1)"
  # 改了哪些文件 / 对应备份（还原要用）
  local files=() backs=()
  local line
  while IFS= read -r line; do
    case "$line" in
      FILE=*)   files+=("${line#FILE=}") ;;
      BACKUP=*) backs+=("${line#BACKUP=}") ;;
    esac
  done <<<"$out"
  local detail
  while IFS= read -r line; do
    detail="${line#DETAIL=}"
    [[ "$line" == DETAIL=* ]] || continue
    case "$result" in
      patched) ;;                       # patched 的明细由下面那行 log 概括
      *) log "  ${detail}" ;;
    esac
  done <<<"$out"
  case "$result" in
    patched)
      if nginx -t >/dev/null 2>&1; then
        if nginx -s reload >/dev/null 2>&1; then
          log "nginx ${domain} TLS: X-Real-IP -> \$remote_addr (reloaded)"
        else
          warn "nginx reload 失败（配置已改，需手工 reload）"
        fi
      else
        # ★ nginx -t 失败 ⇒ **还原**：磁盘上留着没验证过的配置 =
        #   潜伏半切换态（之后任何一次 reload 都会加载它）
        # ★ 用 ${#files[@]} 判空而不是直接展开空数组：bash 3.2 + set -u
        #   展开空数组会报未绑定变量（仓库通用纪律）。
        local i
        if [[ "${#files[@]}" -gt 0 ]]; then
          for i in "${!files[@]}"; do
            [[ -n "${backs[$i]:-}" ]] && cp "${backs[$i]}" "${files[$i]}" 2>/dev/null || true
          done
        fi
        warn "nginx -t 失败 ⇒ 已还原 X-Real-IP 改动（未 reload）"
      fi
      ;;
    already)
      log "nginx ${domain} TLS: X-Real-IP 已是 \$remote_addr (无需改动，未 reload)"
      ;;
    skip-domain-not-on-443-block)
      # ★ 这是**信息**不是告警：本机不承担该域的 443（211：443 由通用终结器承担）
      log "nginx ${domain}：本机没有该域的 443 TLS 块 ⇒ 无需 X-Real-IP 修补"
      ;;
    skip-shared-leg)
      log "nginx ${domain}：该 443 块是共享终结器（含其它域）⇒ 不自动改（不波及别的域）"
      ;;
    noop-unknown-shape)
      warn "nginx ${domain}：X-Real-IP 形态不认识 ⇒ 未改（需人工确认）"
      ;;
    *)
      warn "nginx X-Real-IP 修补未应用（结果=${result:-empty}）"
      ;;
  esac
}
dd_fix_nginx_llm_real_ip

# =============================================================================
# 网页面验收（2026-10-20，154/245 同款教训移植到 211）
# =============================================================================
# 154 的 500 事故证明：**API 全绿 ≠ 网页可用**——那次 readyz / healthz / 登录
# 全过，只有 nginx 读静态页失败，/dashboard 返 500。211 原来只探 /readyz 与 /
# （且 / 只 warn），网页面坏掉照样报「部署完成」。
# 这里按 154 的口径补齐两层：
#   ① 直连容器：取真实 HTML → 抽出入口 JS → 校验它以 JavaScript MIME 发出来；
#   ② 公网入口：llm.itestu.cn 经 nginx 终结后必须 200（不能 502）。
# ⚠ 211 的 nginx upstream 有 backup（100.106.169.63:8782 = mac mini），
# 所以公网 200 **可能**是 backup 顶上的；因此 ① 直连是「211 自己的责任」，
# ② 公网是「入口通不通」，两层都要过。
dd_verify_web_surface() {
  local base="http://127.0.0.1:${DD_CANDIDATE_PORT}"
  local html html_type asset asset_type
  html="$(curl -fsSL --max-time 10 -H 'Accept: text/html' -w '\n%{content_type}' "${base}/dashboard")" || {
    warn "网关直连 /dashboard 取不到 HTML（${base}）"; return 1; }
  html_type="${html##*$'\n'}"; html="${html%$'\n'*}"
  html_type="$(printf '%s' "${html_type%%;*}" | tr '[:upper:]' '[:lower:]' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [[ "$html_type" == "text/html" ]] || { warn "dashboard media type is not HTML: '${html_type:-empty}'"; return 1; }
  [[ "$html" == *"<html"* && "$html" == *"</html>"* ]] || { warn "dashboard did not return HTML"; return 1; }
  asset="$(printf '%s\n' "$html" | sed -n 's@.*src="\(/assets/[A-Za-z0-9._/-]*\.js\)".*@\1@p' | sed -n '1p')"
  [[ "$asset" == /assets/*.js ]] || { warn "dashboard has no built JavaScript entry"; return 1; }
  asset_type="$(curl -fsS --max-time 10 -o /dev/null -w '%{content_type}' "${base}${asset}")" || {
    warn "dashboard 入口资源取不到: ${asset}"; return 1; }
  asset_type="$(printf '%s' "${asset_type%%;*}" | tr '[:upper:]' '[:lower:]' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  case "$asset_type" in
    text/javascript|application/javascript)
      log "网页面 OK（HTML + 入口 JS ${asset} → ${asset_type}）"
      return 0
      ;;
    *)
      warn "dashboard 入口不是 JavaScript: '${asset_type:-empty}'"
      return 1
      ;;
  esac
}

# ── 公网入口（**切流之后**才验）──────────────────────────────────────
# ⚠ 顺序是硬要求：切流前 upstream 还指着旧槽，这时验到的不是本次发布。
# 探不通（连不上）时只告警不阻断（nginx 腿可能绑在别的 IP 上，不是本次部署
# 的责任）；探得通但不是 200（502/5xx）则是真故障 ⇒ 阻断并 abort。
dd_verify_public_entry() {
  local probe_ip="${DD_PUBLIC_PROBE_IP:-$DD_HOST}" code
  code="$(curl -k -sSL --max-time 10 --resolve "${DD_DOMAIN}:443:${probe_ip}" \
      -H 'Accept: text/html' -o /dev/null -w '%{http_code}' \
      "https://${DD_DOMAIN}/dashboard" 2>/dev/null)" || {
    warn "公网入口不可达（${probe_ip}:443）——跳过公网校验，不阻断部署"
    return 0
  }
  if [[ "$code" != "200" ]]; then
    warn "公网入口 ${DD_DOMAIN} 返回 ${code}（期望 200）"
    return 1
  fi
  log "公网入口 OK（${DD_DOMAIN} → ${probe_ip}:443 → ${code}）"
  return 0
}

# dd_public_version —— 从**公网**读 /version（切流后的身份复验用）。
# 探不通返回空串（不 die，调用方决定）。
# 独立成函数是为了能**单独驱动**（判据门可以替身化它，做「reload 异步」的
# 竞态回归）。
dd_public_version() {
  local _probe_ip="${DD_PUBLIC_PROBE_IP:-$DD_HOST}"
  [[ -n "$DD_DOMAIN" ]] || { printf ''; return 0; }
  curl -k -fsSL --max-time 10 --resolve "${DD_DOMAIN}:443:${_probe_ip}" \
    "https://${DD_DOMAIN}/version" 2>/dev/null || printf ''
}

# ── 切流 + 公网版本身份复验 ──────────────────────────────────────────
# 切流：把 nginx upstream 的主 server 行从旧槽端口改到候选槽端口并 reload。
# 复验：切流后从**公网**读 /version，必须含本次构建 sha —— 这是「半切换态」
# （文件已切、流量未切）的唯一判据；只看本机端口会误判（nginx 可能还在
# 服务旧 upstream 的连接）。
#
# ★★ 2026-10-20 真机实测（211）：复验必须**轮询等新配置生效**，不能探一次
#   就下结论。`nginx -s reload` 是**异步**的 —— 它只给 master 发信号，
#   新 worker 起来之前，**旧 worker 仍会用旧 upstream 应答新连接**。
#   实测：切流后立刻探一次公网 /version ⇒ 拿到的是**旧槽**的版本戳
#   （3eb2be91）⇒ 误判「半切换态」⇒ 触发 abort（abort 本身是对的，
#   但判据太急，把一次成功的部署判成失败）。
#   ⇒ 改成轮询（默认 10 次 × 1s）：拿到新 sha 才算过；始终拿不到才判失败。
dd_switch_and_verify() {
  local _before _after _i _ok=0
  _before="$(dd_upstream_port || true)"
  log "切流：nginx upstream ${DD_NGINX_UPSTREAM} 由 127.0.0.1:${_before:-?} 切到 127.0.0.1:${DD_CANDIDATE_PORT}"
  dd_switch_upstream "$DD_CANDIDATE_PORT" || return 1
  DD_SWITCHED=1
  if [[ -n "${DD_BUILD_SHA:-}" ]]; then
    for _i in $(seq 1 "${DD_SWITCH_VERIFY_TRIES:-10}"); do
      _after="$(dd_public_version)"
      # ⚠ 用 bash 原生模式匹配，**不要** `printf | grep -q`：
      #   `grep -q` 命中即退 ⇒ 上游 printf 收 SIGPIPE(141) ⇒ pipefail 把整条
      #   管道判成非零 ⇒ 「明明匹配却当没匹配」—— 本轮实测过一次（假阴性）。
      if [[ -n "$_after" && "$_after" == *"${DD_BUILD_SHA%-dirty}"* ]]; then
        _ok=1
        break
      fi
      (( _i == 1 )) || sleep 1
    done
    if [[ "$_ok" != "1" ]]; then
      if [[ -z "$_after" ]]; then
        warn "切流后公网 /version 读不到（域名 ${DD_DOMAIN}，已重试 ${DD_SWITCH_VERIFY_TRIES:-10} 次）"
      else
        warn "切流后公网版本身份不符：期望含 ${DD_BUILD_SHA%-dirty}，实得 ${_after:0:160}（已重试 ${DD_SWITCH_VERIFY_TRIES:-10} 次）"
      fi
      return 1
    fi
    log "公网版本身份 OK（${DD_BUILD_SHA%-dirty}，第 ${_i} 次探到）"
  fi
  return 0
}

if [[ "${GW_WEB_DIST_OK:-0}" == "1" ]]; then
  dd_verify_web_surface || dd_fail "网页面验收失败（候选槽直连 /dashboard 或入口 JS 不可用）"
fi

# ── 切流（此刻旧槽仍在服务；切流失败会 abort 切回）────────────────────
dd_switch_and_verify || dd_fail "切流失败（nginx upstream 未切到候选槽，或公网版本身份不符）"

# ── 公网入口（切流后验：这次验到的确实是本次发布）────────────────────
dd_verify_public_entry || dd_fail "公网入口验收失败（${DD_DOMAIN} 非 200）"

# ── 登录冒烟：/api/auth/token（不是 /api/admin/login）────────────────
# 凭据优先环境变量，其次 DD_ADMIN_*，默认 admin / Veritrans&9527（与
# 目标库 users.admin 对齐）。失败不阻断部署收尾，但标红方便运维看见。
dd_smoke_admin_login() {
  local user="${DD_ADMIN_USER:-${LLM_GATEWAY_ADMIN_USER:-admin}}"
  local pass="${DD_ADMIN_PASSWORD:-${LLM_GATEWAY_ADMIN_PASSWORD:-Veritrans&9527}}"
  local url="http://127.0.0.1:${DD_CANDIDATE_PORT}/api/auth/token"
  local body code
  body="$(python3 -c 'import json,sys; print(json.dumps({"username":sys.argv[1],"password":sys.argv[2]}))' "$user" "$pass")"
  code="$(curl -sS -o /tmp/dd-login.json -w '%{http_code}' -X POST "$url" \
    -H 'Content-Type: application/json' -d "$body" || echo 000)"
  if [[ "$code" != "200" ]]; then
    warn "登录冒烟失败 HTTP ${code} body=$(head -c 160 /tmp/dd-login.json 2>/dev/null)"
    return 1
  fi
  if ! python3 -c 'import json,sys; d=json.load(open("/tmp/dd-login.json")); assert d.get("access_token") and d.get("user",{}).get("username")==sys.argv[1]' "$user" 2>/dev/null; then
    warn "登录冒烟响应缺 access_token/user"
    return 1
  fi
  # 连登 6 次必须仍 200（回归 JWT 成功路径 Reset 限流桶）
  local i c
  for i in 1 2 3 4 5 6; do
    c="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$url" \
      -H 'Content-Type: application/json' -d "$body" || echo 000)"
    if [[ "$c" != "200" ]]; then
      warn "登录限流回归失败：第 ${i} 次 HTTP ${c}（期望成功登录 Reset 后可连登）"
      return 1
    fi
  done
  log "登录冒烟 OK（user=${user}，连登 6 次无 429）"
  return 0
}
if [[ "${DD_SKIP_LOGIN_SMOKE:-0}" != "1" ]]; then
  dd_smoke_admin_login || warn "登录冒烟未通过——页面可能仍无法登录，请查 users.admin 密码与限流"
fi

# ── 切流成功：停旧槽 + 写活跃槽 + 清历史单机容器 ─────────────────────
# 旧槽容器**停而不删**：容器对象与镜像都留着 ⇒ 人工回滚只要 docker start。
# 停掉它是为了释放它的端口给下一轮的候选槽（槽位与端口是绑定的）。
if docker inspect --type container "$DD_ACTIVE_CONTAINER" >/dev/null 2>&1; then
  if docker stop "$DD_ACTIVE_CONTAINER" >/dev/null 2>&1; then
    DD_OLD_STOPPED=1
    log "旧槽已停：${DD_ACTIVE_CONTAINER}（保留容器与镜像，可 docker start 回滚）"
  else
    warn "旧槽停止失败（${DD_ACTIVE_CONTAINER}）——它仍占着端口，下一轮候选槽可能起不来"
  fi
fi
# 蓝绿之前的历史单机容器（<prefix>，无 -<slot> 后缀）：它占着槽 a 的端口，
# 下一轮候选槽=a 时会绑不上 ⇒ 切流成功后必须让位。
# ★ 停而不删：改名成 <prefix>-legacy 留着（容器 + 镜像都在），人工可
#   `docker start` 回滚到蓝绿之前的那一版。与「停旧槽」同一口径。
#   只有改名失败才退化为 rm -f（端口必须释放，端口不释放是硬故障）。
if docker inspect --type container "$DD_CONTAINER_PREFIX" >/dev/null 2>&1; then
  _dd_legacy="${DD_CONTAINER_PREFIX}-legacy"
  if docker inspect --type container "$_dd_legacy" >/dev/null 2>&1; then
    docker rm -f "$_dd_legacy" >/dev/null 2>&1 || true
  fi
  if docker rename "$DD_CONTAINER_PREFIX" "$_dd_legacy" >/dev/null 2>&1; then
    if docker stop "$_dd_legacy" >/dev/null 2>&1; then
      log "历史单机容器已退役：${DD_CONTAINER_PREFIX} → ${_dd_legacy}（已停，保留可回滚）"
    else
      # ⚠ 改名成功但停失败 ⇒ 容器**已经**不叫原名了 ⇒ 再去 rm 原名是空操作，
      #   容器会继续占着 8781 ⇒ 下一轮候选槽=a 绑不上端口。
      docker rm -f "$_dd_legacy" >/dev/null 2>&1 || true
      warn "历史单机容器改名成功但停止失败（${_dd_legacy}）—— 已强制删除以释放端口"
    fi
  else
    docker rm -f "$DD_CONTAINER_PREFIX" >/dev/null 2>&1 || true
    warn "历史单机容器 ${DD_CONTAINER_PREFIX} 改名失败 —— 已强制删除以释放端口"
  fi
fi
dd_write_active_slot "$DD_CANDIDATE_SLOT"

log "=========================================================="
log "部署完成 @ ${DD_HOST:-本机}（槽 ${DD_CANDIDATE_SLOT} 接管）"
log "  活跃槽  : ${DD_CANDIDATE_SLOT} → 容器 $(dd_container_name "$DD_CANDIDATE_SLOT") @ 127.0.0.1:${DD_CANDIDATE_PORT}"
log "  旧槽    : ${DD_ACTIVE_SLOT} → 容器 $(dd_container_name "$DD_ACTIVE_SLOT")（已停，可 docker start 回滚）"
log "  网关    : http://${DD_HOST:-127.0.0.1}:${DD_CANDIDATE_PORT}"
log "  镜像    : $DD_GW_IMAGE  builder=$DD_GO_BASE_IMAGE"
log "  域名    : https://${DD_DOMAIN}/  (nginx ${DD_NGINX_UPSTREAM} → 127.0.0.1:${DD_CANDIDATE_PORT})"
log "  登录    : POST /api/auth/token  (不要用 /api/admin/login)"
log "  PG      : $PG_CONTAINER  ($( ((PG_REUSED)) && echo 复用 || echo 新建 ))  库=$DD_PG_DB"
log "  Redis   : $REDIS_CONTAINER ($( ((REDIS_REUSED)) && echo 复用 || echo 新建 ))  库号=db${REDIS_DB} (从 ${DD_REDIS_DB_START} 倒排)"
log "  密钥    : $ENV_FILE (chmod 600，勿提交)"
log "=========================================================="
# ★ 完成标记（fail-closed 守卫的另一半）：只有真正走到这里，退出码 0 才代表
#   「这次部署成功了」。任何提前退出的路径（die / abort / 隐式失败）都会被
#   dd_exit_guard_finalize 判成失败。
DD_DEPLOY_COMPLETED=1
