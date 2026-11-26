#!/usr/bin/env bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# =============================================================================
# deploy-pre-gate.sh — D3 部署前置测试门禁
# =============================================================================
# 定位：**手工只读预检 / 可选的前置门禁**。
#
# ★ 2026-11-23（12h 修订审计轮）**更正声明**：
#   本文件此前自述「蓝绿部署的**前置检查**，在任何破坏性动作（停容器/切流）
#   之前运行。失败时阻止部署继续」——**与事实不符**：全仓**无任何调用方**
#   （`grep -rln "deploy-pre-gate" scripts/ deploy/` 只命中它自己与
#   `scripts/ursmcheck/cjk_var_sites.tsv` 的棘轮登记册）⇒ 那句「阻止部署」
#   **从来不会发生**。
#   现改为**两件事都做实**：
#   ① `scripts/deploy-211.sh` 已在破坏性动作前接入本脚本，但**默认不跑**
#      （需 `DD_RUN_PRE_GATE=1` **显式**启用）——理由见下；
#   ② 文件头如实写明「默认不参与部署流程」。
#
# ★ 为什么默认不跑（而不是接成必跑）：本门禁的三道前置条件**在部署机上未必成立**
#   —— G2 需要 Maestro + ADMIN_PASSWORD、G3 需要 web/dist 且 mtime < N 分钟
#   （★ 而实际流程是「产物随 tar 上树、部署机不构建前端」，见
#   `scripts/deploy-seamless-docker.sh` 注释）⇒ 接成必跑会让**不具备条件的
#   部署机恒红**，把门禁变成噪声。
#   ⇒ **是否把 D3 设为默认必跑，属 owner 决策**（见审计文档 §4 待决清单）。
#
# 检查项（W1 设计）：
#   G1. sqlguard 迁移校验（internal/sqlguard 的迁移通道/注册表连续性测试）
#   G2. Maestro 核心 4 个 flow 快速回归（01/03/06/09）
#   G3. 前端产物新鲜度检查（web/dist 存在且 mtime<10min；GATE_DIST_MAX_AGE_MIN=0 ⇒ 跳过）
#
# 用法：
#   bash scripts/deploy-pre-gate.sh                      # 本地检查
#   bash scripts/deploy-pre-gate.sh --maestro-only       # 仅Maestro回归
#   bash scripts/deploy-pre-gate.sh --skip-maestro       # 跳过Maestro（sqlguard+产物）
#   bash scripts/deploy-pre-gate.sh --via <host>         # SSH到目标机检查
#
# 环境变量：
#   GATE_MAESTRO_FLOWS   Maestro flow列表（空格分隔），默认"01 03 06 09"
#   GATE_MAESTRO_TIMEOUT Maestro超时（秒），默认120
#   GATE_SKIP_MAESTRO    跳过Maestro（1=跳过），默认0
#   GATE_DIST_MAX_AGE_MIN 前端产物最大年龄（分钟）；**0 = 跳过该检查**（默认10）
#   BASE_URL             Maestro测试目标URL，默认http://localhost:8780
#   ADMIN_USER           Maestro登录用户
#   ADMIN_PASSWORD       Maestro登录密码
#
# 返回值：
#   0 = 所有检查通过，可继续部署
#   1 = 检查失败，阻止部署
#   2 = 参数错误
#
# =============================================================================
set -euo pipefail

# ★ 原始参数留档：参数解析循环会 shift 掉它们，远程执行（--via）需要原样
#   传给目标机 ⇒ 必须在解析**之前**留档（否则远端收到的是空参数 ⇒
#   --maestro-only / --skip-maestro 之类的模式标志会被静默丢失）。
GATE_ORIG_ARGS=("$@")

usage() {
  cat <<'EOF'
用法：bash scripts/deploy-pre-gate.sh [选项]

选项：
  --maestro-only       仅跑 Maestro 回归
  --skip-maestro       跳过 Maestro（仍跑 sqlguard + 前端产物检查）
  --via HOST           通过 SSH 在目标机上执行本门禁
  --help, -h           显示本帮助

环境变量：GATE_MAESTRO_FLOWS / GATE_MAESTRO_TIMEOUT / GATE_SKIP_MAESTRO /
          GATE_DIST_MAX_AGE_MIN（0=跳过产物新鲜度）/ BASE_URL /
          ADMIN_USER / ADMIN_PASSWORD

返回值：0=通过  1=检查失败  2=参数错误
EOF
}

# ── 参数与环境变量 ────────────────────────────────────────────────────────
GATE_MODE="full"                    # full / maestro-only / no-maestro
GATE_VIA_HOST=""                    # SSH目标机
GATE_MAESTRO_FLOWS="${GATE_MAESTRO_FLOWS:-01 03 06 09}"
GATE_MAESTRO_TIMEOUT="${GATE_MAESTRO_TIMEOUT:-120}"
GATE_SKIP_MAESTRO="${GATE_SKIP_MAESTRO:-0}"
GATE_BASE_URL="${BASE_URL:-http://localhost:8780}"
GATE_ADMIN_USER="${ADMIN_USER:-admin}"
GATE_ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
GATE_DIST_MAX_AGE_MIN="${GATE_DIST_MAX_AGE_MIN:-10}"
GATE_MAESTRO_REGISTRY_TIMEOUT="${GATE_MAESTRO_REGISTRY_TIMEOUT:-120}"

# ── 解析参数 ──────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --maestro-only)
      GATE_MODE="maestro-only"
      shift
      ;;
    --skip-maestro)
      GATE_SKIP_MAESTRO=1
      shift
      ;;
    --via)
      GATE_VIA_HOST="$2"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    --)
      # ★ 选项终止符：远程执行时 ssh 会把 `--` 之后的内容作为脚本参数传入
      #   （`bash -s -- arg1` ⇒ 远端脚本的 $1 是 arg1、**不是** `--`）⇒
      #   这里显式支持 `--`，避免落到 *) 分支报「未知参数 --」并 exit 2。
      shift
      break
      ;;
    *)
      echo "错误：未知参数 $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

# ── 工具函数 ──────────────────────────────────────────────────────────────
log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] $*" >&2
}

err() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] ❌ $*" >&2
}

ok() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [GATE] ✅ $*" >&2
}

# ── G1: sqlguard 迁移校验 ─────────────────────────────────────────────────
#
# assert_test_exists <包路径> <测试名>
#   断言该包内**确实存在**至少 1 个匹配该名字的用例。
#
# ★ 为什么必须有这道闸（2026-11-23 12h 修订审计轮）：`go test -run <不存在的
#   名字>` 返回 **exit 0**（输出 "ok … [no tests to run]"）⇒ **空跑恒绿**
#   ⇒ 一条「看起来有守卫」的判据可能**什么都没跑**。
#   实测：本门禁原先点名的 TestStartupManifestEmbeddata **全仓不存在**
#   （真实名字是 TestStartupManifestMatchesStartupFiles）⇒ 那条判据一直是空跑。
assert_test_exists() {
  local pkg="$1" name="$2"
  local listed
  listed="$(go test "${pkg}" -list '.*' 2>/dev/null || true)"
  grep -qx "${name}" <<<"$listed"
}

run_sqlguard() {
  log "G1: sqlguard 迁移校验..."

  if ! command -v go &>/dev/null; then
    err "G1: go 命令不存在，跳过 sqlguard"
    return 1
  fi

  # ★ 2026-11-23（12h 修订审计轮）修正三处真缺陷：
  #   ① 包路径原写 `./startup/sqlguard` —— **该目录不存在**（仓内只有
  #      `internal/sqlguard`）⇒ `go test ./startup/sqlguard` 报
  #      "directory not found / setup failed" ⇒ **G1 恒红**。
  #   ② 测试名之一 `TestStartupManifestEmbeddata` —— **全仓不存在**；真实名字是
  #      `installer/internal/dbinit/startup_manifest_test.go` 里的
  #      `TestStartupManifestMatchesStartupFiles`，且 `installer/` 是**嵌套模块**
  #      ⇒ 必须在 installer 模块根跑（主模块根跑 ./installer/... 会 setup failed）。
  #   ③ 空跑恒绿 ⇒ 每条判据先 `go test -list` 断言「至少匹配 1 个用例」。
  local failed=0

  # ── 判据 1/2：internal/sqlguard 的迁移通道与注册表连续性（主模块根跑）──
  local guard_tests=(
    "TestStartupMigrationsStayInEveryDeliveryChannel"
    "TestMigrationRegistryStaysContinuous"
  )
  for test in "${guard_tests[@]}"; do
    if ! assert_test_exists ./internal/sqlguard "$test"; then
      err "  $test 在 ./internal/sqlguard 中不存在（防空跑判据触发）"
      failed=1
      continue
    fi
    log "  运行 $test..."
    if ! go test -timeout 60s ./internal/sqlguard -run "^${test}$" 2>&1 | tee "/tmp/gate-sqlguard-${test}.log"; then
      err "  $test 失败"
      failed=1
    else
      ok "  $test 通过"
    fi
  done

  # ── 判据 3：installer 模块的启动清单一致性（**嵌套模块 ⇒ 在 installer 根跑**）──
  local manifest_test="TestStartupManifestMatchesStartupFiles"
  if [[ ! -d installer ]]; then
    err "  installer 目录不存在，跳过 $manifest_test"
    failed=1
  elif ! ( cd installer && assert_test_exists ./internal/dbinit "$manifest_test" ); then
    err "  $manifest_test 在 installer/internal/dbinit 中不存在（防空跑判据触发）"
    failed=1
  else
    log "  运行 ${manifest_test}（installer 模块根）..."
    if ! ( cd installer && go test -timeout 60s ./internal/dbinit -run "^${manifest_test}$" 2>&1 | tee "/tmp/gate-sqlguard-${manifest_test}.log" ); then
      err "  $manifest_test 失败"
      failed=1
    else
      ok "  $manifest_test 通过"
    fi
  fi

  if [[ $failed -eq 1 ]]; then
    err "G1: sqlguard 检查失败"
    return 1
  fi

  ok "G1: sqlguard 检查通过"
  return 0
}

# ── 可移植超时（2026-11-23 12h 修订审计轮）───────────────────────────────
#
# ★ 形态：GNU `timeout` 属 coreutils，**macOS 上不存在**（只有 `gtimeout`）⇒
#   直接调用 `timeout` 会得到 "command not found" ⇒ **G2 恒红**。
#   而本仓脚本大量在 macOS 上运行（`scripts/lib/go-toolchain.sh` 就是为 macOS
#   提供 bash5 的）⇒ 这是一条「在 Linux 上正常、在 macOS 上必红」的隐形地雷。
#
# ★ 三级降级：timeout → gtimeout → 纯 bash（后台进程 + 轮询 + kill）。
#   纯 bash 版的超时退出码用 124，与 GNU timeout 一致（调用方按「非 0 即失败」
#   处理即可）。
GATE_TIMEOUT_BIN=""
if command -v timeout &>/dev/null; then
  GATE_TIMEOUT_BIN="$(command -v timeout)"
elif command -v gtimeout &>/dev/null; then
  GATE_TIMEOUT_BIN="$(command -v gtimeout)"
fi

run_with_timeout() {
  local seconds="$1"; shift
  if [[ -n "$GATE_TIMEOUT_BIN" ]]; then
    "$GATE_TIMEOUT_BIN" "${seconds}s" "$@"
    return $?
  fi
  # 纯 bash 超时（macOS 无 GNU timeout 时的兜底）
  "$@" &
  local pid=$!
  local waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [[ $waited -ge $seconds ]]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

# ── G1.5: Maestro 路由登记口径（★ 新增路由必须同时登记 README）────────────
#
# ★ 为什么这道检查要进【部署前置门】：
#   2026-11-23 实测踩到的那一类 —— ★ L14 落地轮**新增了路由**
#   （`/admin/session-analytics/users/overview`）★ **只改了 `web/src/router.ts`**
#   ⇒ ★ `maestro/README.md` 的路由口径清单**一个字没改**
#   ⇒ ★ `audit-route-coverage.mjs --check` 的 **5 条判据当场红**（★ 每条都正好差 1）
#   ⇒ ★★ **而它【没进任何一道门】**（★ `deploy-test-gate.sh` 只选 Go 包、
#     ★ `deploy-pre-gate.sh` 跑的是 sqlguard ＋ Maestro 回归 ＋ 产物新鲜度）
#   ⇒ ★★★ **⇒ 一个【加了功能却没登记】的缺陷、★ 能一路飘到发布**。
#
# ★ 为什么用 python3 版而不是 mjs 版：★ `audit-readme-route-stats.py` **零第三方依赖**
#   （★ 仓内 CI 精简镜像里 node 不可靠 ⇒ ★ node 版门会「有就跑、没有就 skip」
#   ⇒ ★ 那样的门**等于没接**；★ 仓内先例见 `maestro-readme-route-stats-gate.sh` 头注）。
run_maestro_registry() {
  local gate="scripts/checks/maestro-readme-route-stats-gate.sh"
  if [[ ! -f "$gate" ]]; then
    err "找不到路由登记口径门：$gate"
    return 1
  fi
  # ★ 源码树不在此处（★ 例如只在目标机上跑、只有 dist 产物）⇒ ★ **显式 SKIP**：
  #   ★ 不伪装成通过、★ 也不把「输入缺失」当成漂移（★ 仓内先例：fresh-install 门的无 docker SKIP）。
  if [[ ! -f "web/src/router.ts" || ! -f "maestro/README.md" ]]; then
    ok "跳过 Maestro 路由登记口径（★ 本机没有 web/src/router.ts 或 maestro/README.md ⇒ 源码树不在此处）"
    return 0
  fi
  log "检查 Maestro 路由登记口径（README §1.1/§1.2/§1.3/§3.13 ↔ web/src/router.ts）"
  if run_with_timeout "${GATE_MAESTRO_REGISTRY_TIMEOUT}" bash "$gate"; then
    ok "Maestro 路由登记口径与现役树一致"
    return 0
  fi
  err "Maestro 路由登记口径漂移（★ 多半是【新增了路由却没登记 README】⇒ ★ 新增路由必须同时登记）"
  return 1
}

# ── G2: Maestro 快速回归 ──────────────────────────────────────────────────
run_maestro() {
  log "G2: Maestro 快速回归（flows: ${GATE_MAESTRO_FLOWS}）..."
  
  if ! command -v maestro &>/dev/null; then
    err "G2: maestro 命令不存在，跳过"
    return 1
  fi
  
  # 检查必需环境变量
  if [[ -z "$GATE_ADMIN_PASSWORD" ]]; then
    err "G2: ADMIN_PASSWORD 未设置，无法运行 Maestro"
    return 1
  fi
  
  local failed=0 log_dir
  log_dir="$(mktemp -d "${TMPDIR:-/tmp}/gate-maestro.XXXXXX")" || return 1
  for flow in $GATE_MAESTRO_FLOWS; do
    local flow_glob="maestro/flows/${flow}-*.yaml"
    # ★ 用 compgen -G（bash ≥4 内建）判存在并取回匹配列表 ⇒ 不再依赖 `ls`，
    #   也避免未加引号的通配在 `set -u` 下踩空变量。
    #   ★ macOS 自带的 bash 3.2 **没有** compgen -G ⇒ 退回 shell 通配展开
    #   （此时用 ls 是刻意的：只在旧 bash 上兜底，且只读不写）。
    local flow_files=""
    if flow_files="$(compgen -G "$flow_glob" 2>/dev/null)" && [[ -n "$flow_files" ]]; then
      :
    else
      flow_files="$(ls $flow_glob 2>/dev/null || true)"
    fi
    if [[ -z "$flow_files" ]]; then
      err "  flow $flow 文件不存在：$flow_glob"
      failed=1
      continue
    fi

    log "  运行 flow $flow..."
    local log_file="${log_dir}/flow-${flow}.log"

    # 使用超时防止挂起（★ 可移植实现，见 run_with_timeout）
    # Maestro 的 --env 接收 KEY=VALUE。复用 Web runner 解析动态路由并配置视口。
    if BASE_URL="$GATE_BASE_URL" LLM_GATEWAY_ADMIN_USER="$GATE_ADMIN_USER" \
      LLM_GATEWAY_ADMIN_PASSWORD="$GATE_ADMIN_PASSWORD" \
      MAESTRO_REPORT_ROOT="${log_dir}/reports" \
      run_with_timeout "${GATE_MAESTRO_TIMEOUT}" bash maestro/run-local.sh \
      $flow_files \
      > "$log_file" 2>&1; then
      ok "  flow $flow 通过"
    else
      err "  flow $flow 失败（日志：${log_file}）"
      failed=1
    fi
  done
  
  if [[ $failed -eq 1 ]]; then
    err "G2: Maestro 回归失败"
    return 1
  fi
  
  ok "G2: Maestro 回归通过"
  return 0
}

# ── G3: 前端产物新鲜度检查 ───────────────────────────────────────────────
check_dist_freshness() {
  log "G3: 前端产物新鲜度检查..."

  # ★ 2026-11-23（12h 修订审计轮）：GATE_DIST_MAX_AGE_MIN=0 ⇒ **显式跳过**。
  #   理由：实际部署流程是「产物随 tar 上树、部署机**不构建前端**」
  #   （见 scripts/deploy-seamless-docker.sh 的注释）⇒ 在部署机上用
  #   「mtime < 10 分钟」判新鲜度**必然误红** ⇒ 必须能显式关掉。
  if [[ "$GATE_DIST_MAX_AGE_MIN" == "0" ]]; then
    log "  GATE_DIST_MAX_AGE_MIN=0 ⇒ 跳过前端产物新鲜度检查（显式跳过）"
    return 0
  fi

  local dist_dir="web/dist"
  if [[ ! -d "$dist_dir" ]]; then
    err "G3: $dist_dir 不存在"
    return 1
  fi
  
  # 检查 index.html mtime
  local index_html="$dist_dir/index.html"
  if [[ ! -f "$index_html" ]]; then
    err "G3: $index_html 不存在"
    return 1
  fi
  
  local mtime
  mtime=$(stat -f %m "$index_html" 2>/dev/null || stat -c %Y "$index_html" 2>/dev/null)
  local now
  now=$(date +%s)
  local age_sec=$((now - mtime))
  local age_min=$((age_sec / 60))
  
  if [[ $age_min -gt $GATE_DIST_MAX_AGE_MIN ]]; then
    err "G3: 前端产物过期（age=${age_min}min > ${GATE_DIST_MAX_AGE_MIN}min）"
    err "    提示：运行 npm run build 重新构建"
    return 1
  fi
  
  ok "G3: 前端产物新鲜（age=${age_min}min）"
  return 0
}

# ── 主流程 ────────────────────────────────────────────────────────────────
main() {
  log "开始部署前置测试门禁"
  log "模式：$GATE_MODE"
  
  local failed=0
  
  # ★ 这道【每个模式都跑】：★ 它只要 python3、★ 亚秒级、★ 且与部署产物无关。
  run_maestro_registry || failed=1
  
  case "$GATE_MODE" in
    maestro-only)
      run_maestro || failed=1
      ;;
    *)
      # full 模式
      if [[ "$GATE_MODE" != "no-maestro" ]] && [[ "$GATE_SKIP_MAESTRO" -eq 0 ]]; then
        check_dist_freshness || failed=1
        run_sqlguard || failed=1
        run_maestro || failed=1
      else
        check_dist_freshness || failed=1
        run_sqlguard || failed=1
      fi
      ;;
  esac
  
  if [[ $failed -eq 1 ]]; then
    err "==============================================="
    err "部署前置测试门禁失败，阻止部署"
    err "==============================================="
    return 1
  fi
  
  ok "==============================================="
  ok "部署前置测试门禁全部通过"
  ok "==============================================="
  return 0
}

# ── 远程执行支持 ──────────────────────────────────────────────────────────
if [[ -n "$GATE_VIA_HOST" ]]; then
  log "通过 SSH 在 $GATE_VIA_HOST 上执行门禁检查"
  # ★ 2026-11-23（12h 修订审计轮）修正：`--via` 远程模式此前**必然失败**，
  #   且失败原因与声明不符：
  #   ① 原写法 `ssh "$HOST" "bash -s" < "$0" -- "${@}"`：参数解析循环已把
  #      "${@}" 全部 shift 掉 ⇒ 远端收到的是**空参数** ⇒ --maestro-only /
  #      --skip-maestro 之类的模式标志被**静默丢弃**（远端按 full 模式跑）。
  #   ② `--` 被 ssh 拼进远端命令 `bash -s --` ⇒ 远端脚本的 $1 **不是** "--"
  #      （实测 $#=0），但若远端脚本的参数循环没有 `--` 分支，落进 *) 分支
  #      就会报「未知参数 --」并 exit 2。
  #   现改为：`bash -s -- <参数>`（`--` 是 bash 的**选项终止符** ⇒ 远端 $1
  #   就是第一个真参数），且参数取自**解析之前**留档的 GATE_ORIG_ARGS。
  ssh "$GATE_VIA_HOST" "bash -s -- $(printf '%q ' ${GATE_ORIG_ARGS[@]+"${GATE_ORIG_ARGS[@]}"})" < "$0"
  exit $?
fi

# ── 执行 ──────────────────────────────────────────────────────────────────
main
exit $?
