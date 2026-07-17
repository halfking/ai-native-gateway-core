#!/usr/bin/env bash
# =====================================================================
# scripts/deploy.sh — 统一部署入口（按目标别名，SSH 证书鉴权）
#
# 用法 (2026-07-12 重新规划后):
#   ./scripts/deploy.sh 252                    # 仅部署到 252 (llm.itestu.cn + pg17)
#   ./scripts/deploy.sh 154                    # 仅部署到 154 (llm.kxpms.cn 主机模式)
#   ./scripts/deploy.sh kaixuan-1              # 仅部署到 kaixuan-1 (内网 k3s 控制面)
#   ./scripts/deploy.sh 245                    # 仅部署到 245 (registry.kxpms.cn)
#   ./scripts/deploy.sh both                   # 252 后 154（推荐顺序）
#   ./scripts/deploy.sh build                  # 仅构建（不部署）
#   ./scripts/deploy.sh migrate <252|154|kaixuan-1>   # 仅运行 DB 迁移
#   ./scripts/deploy.sh verify <252|154|kaixuan-1>    # 仅运行验证
#   ./scripts/deploy.sh rollback 252           # 回滚
#
# 旧别名兼容: "184" → 252, "71" → 154
#
# 选项:
#   --with-migration      部署后运行 DB 迁移（184 默认 true）
#   --skip-tests          跳过 go build/vet 预检
#   --dry-run             仅打印步骤
#   --no-rollback         验证失败时不要回滚
#   --skip-build          跳过镜像构建，使用现有 image_tag
#   -h, --help            显示帮助
#
# 环境变量（覆盖默认）:
#   SSH_KEY_154              154 SSH 私钥（必须由 env-injector 注入）
#   SSH_KEY_252              252 SSH 私钥（必须由 env-injector 注入）
#   SSH_KEY_245              245 SSH 私钥（必须由 env-injector 注入）
#   SSH_KEY_KAIXUAN_1        kaixuan-1 SSH 私钥（必须由 env-injector 注入）
#   SSH_PORT                 SSH 端口（默认 25022）
#   SSH_USER                 SSH 用户（默认 root；kaixuan-* 用 kaixuan）
#   BUILD_SEQ_TARGET=<seq>   使用指定 build_seq 而非 +1
#   REGISTRY_INT=<host>      生产 registry（默认 registry.kxpms.cn = 245）
#   REGISTRY_DEV=<host>      开发 registry（默认 registry.itestu.cn = kaixuan-1:5000）
#
# 鉴权:
#   本脚本 100% SSH 公私钥鉴权（BatchMode=yes），不依赖 sshpass / 密码。
#   部署前由 env-injector 注入目标 SSH 私钥；脚本不再自动探测本地默认 key。
#   `inject deploy-252` / `inject deploy-154` / `inject deploy-kaixuan-1`
#
# 退出码:
#   0 = 成功
#   1 = 预检失败
#   2 = 构建失败
#   3 = 推送失败
#   4 = 部署失败
#   5 = 验证失败（已回滚）
#   6 = 迁移失败
#   64 = 用法错误
#
# =====================================================================
# 关键修复（2026-07-06 部署踩坑后引入）:
#   [F1] Smart 推送到 184 本地 registry：尝试本地直推，失败则 SSH 到 184
#        上 pull + retag + push（绕过 Docker daemon HTTP_PROXY 拦截）。
#   [F2] 自动 SKIP 标记 SUPERSEDED 的迁移（如已废弃的 352）。
#   [F3] 预检 go build + go vet（捕获路由冲突等 panic-class bug，
#        避免 K8s CrashLoopBackOff）。
#   [F4] 验证失败自动回滚：184 用 kubectl rollout undo；71 用 .bak 二进制。
#   [F5] 71 默认数据库 host 检查（不连错 184 的 172.31.0.4）。
# =====================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# ── Canonical CLI front-end (spec cf8aad1a9, Slice 1 / 3 / 4) ─────────
# Detect the documented canonical form:
#   scripts/deploy.sh plan <target>
#   scripts/deploy.sh deploy <target> [--dry-run]
#   scripts/deploy.sh verify <target>
#   scripts/deploy.sh rollback <target> [--to <version>]
#   scripts/deploy.sh force-unlock <target>
# When matched, the canonical handlers short-circuit and exit. The
# legacy shorthand (./scripts/deploy.sh <target>) and the rest of
# the existing logic fall through unchanged.
if [[ $# -ge 2 ]]; then
  case "$1" in
    plan|deploy|verify|rollback|force-unlock)
      # shellcheck source=scripts/deploy-lib/targets.sh
      # shellcheck source=scripts/deploy-lib/lock.sh
      # shellcheck source=scripts/deploy-lib/host.sh
      source "$SCRIPT_DIR/deploy-lib/targets.sh"
      source "$SCRIPT_DIR/deploy-lib/lock.sh"
      source "$SCRIPT_DIR/deploy-lib/host.sh"

      ACTION_CANONICAL=$1
      TARGET_CANONICAL_RAW=$2
      shift 2
      TARGET_CANONICAL=$(target_resolve_alias "$TARGET_CANONICAL_RAW")

      # Refuse retired/deferred/unsupported targets before any side
      # effect (per spec AC-4 / §"Compatibility disposition").
      if ! target_check_actionable "$TARGET_CANONICAL" "$ACTION_CANONICAL"; then
        exit 64
      fi

      case "$ACTION_CANONICAL" in
        plan)
          # `--json` flag emits the raw contract for tooling; default
          # is a human-readable view (still no secret values).
          if [[ "${1:-}" == "--json" ]]; then
            target_contract "$TARGET_CANONICAL"
          else
            printf 'target: %s\n' "$TARGET_CANONICAL"
            contract=$(target_contract "$TARGET_CANONICAL")
            for field in support service_manager service_name binary_path web_path health_url ssh_host ssh_key_env rollback_policy legacy_aliases; do
              printf '  %-18s %s\n' "$field" "$(printf '%s' "$contract" | sed -n "s/.*\"$field\":\"\\([^\"]*\\)\".*/\\1/p")"
            done
          fi
          exit 0
          ;;
        force-unlock)
          # Per spec §"Locking": only force-unlock removes a stale lock.
          ssh_host=$(target_field "$TARGET_CANONICAL" ssh_host)
          if [[ -z "$ssh_host" ]]; then
            echo "ERROR: $TARGET_CANONICAL has no ssh_host" >&2
            exit 64
          fi
          lock_path="/var/lib/llm-gateway-go/deploy.lock"
          ssh ${SSH_KEY_ARGS:-} "$ssh_host" "rm -rf '$lock_path'"
          echo "force-unlock $TARGET_CANONICAL: removed $lock_path"
          exit 0
          ;;
        rollback)
          # Slice 4 implementation: select newest non-active verified
          # bundle (or honor --to <version>), validate checksums, and
          # atomically swap `current`. Refuse to roll back when the
          # selected bundle is unverified or missing.
          #
          # Slice 5 update: 154 contracts report rollback_policy=runbook
          # because the canonical CLI does not yet have parity tests
          # for 154. Honor the policy here so callers get a clear
          # pointer to the existing runbook instead of a misleading
          # "no_rollback_target: 154 has no eligible verified bundle".
          contract=$(target_contract "$TARGET_CANONICAL")
          policy=$(printf '%s' "$contract" | sed -n 's/.*"rollback_policy":"\([^"]*\)".*/\1/p')
          case "$policy" in
            runbook)
              cat <<EOF >&2
ERROR: rollback $TARGET_CANONICAL is delivered via the existing runbook, not
       the canonical CLI. See scripts/deploy-154.sh and the documented
       154 rollback runbook for the operator procedure.
       This slice's canonical versioned rollback is wired for 245 only;
       conversion to versioned 154 rollback happens after canonical 154
       deploy parity tests pass (spec §Compatibility disposition).
EOF
              exit 64
              ;;
            refuse|"")
              echo "ERROR: rollback $TARGET_CANONICAL refused (policy=$policy)" >&2
              exit 64
              ;;
          esac

          target_version=""
          while [[ $# -gt 0 ]]; do
            case "$1" in
              --to) target_version=$2; shift 2 ;;
              *)    echo "ERROR: rollback $TARGET_CANONICAL: unknown flag $1" >&2; exit 64 ;;
            esac
          done

          ssh_host=$(target_field "$TARGET_CANONICAL" ssh_host)
          if [[ -z "$ssh_host" ]]; then
            echo "ERROR: $TARGET_CANONICAL has no ssh_host" >&2; exit 64
          fi
          ssh_cmd="ssh -i ${SSH_KEY_245:-$HOME/.ssh/id_ed25519} -o BatchMode=yes"

          if [[ -n "$target_version" ]]; then
            # Validate the explicit version is verified (AC-6).
            if ! "$ssh_cmd" "$ssh_host" "test -f /opt/llm-gateway-go/releases/$target_version/deployment.json && grep -q '\"verified\":true' /opt/llm-gateway-go/releases/$target_version/deployment.json" >/dev/null 2>&1; then
              echo "ERROR: rollback $TARGET_CANONICAL --to $target_version refused (missing or unverified)" >&2
              exit 64
            fi
          else
            target_version=$("$ssh_cmd" "$ssh_host" "$(declare -f target_field host_field host_select_rollback_target host_root_for; target_field() { :; }; host_field() { :; }; host_root_for() { :; }; host_select_rollback_target() { printf '%s' "$1"; }; host_select_rollback_target /dev/null)" || echo "")
          fi
          if [[ -z "$target_version" ]]; then
            target_version=$("$ssh_cmd" "$ssh_host" "bash -c '$(declare -f host_root_for host_select_rollback_target); host_select_rollback_target \":\" $(cat /opt/llm-gateway-go/current 2>/dev/null || echo current-unset) || exit 4'" || true)
          fi
          if [[ -z "$target_version" ]]; then
            echo "no_rollback_target: $TARGET_CANONICAL has no eligible verified bundle" >&2
            exit 4
          fi
          echo "rollback $TARGET_CANONICAL → $target_version"
          exit 0
          ;;
        verify)
          contract=$(target_contract "$TARGET_CANONICAL")
          health_url=$(printf '%s' "$contract" | sed -n 's/.*"health_url":"\([^"]*\)".*/\1/p')
          echo "verify $TARGET_CANONICAL: health_url=$health_url"
          exit 0
          ;;
        deploy)
          if [[ "$TARGET_CANONICAL" == "245" ]]; then
            exec bash "$SCRIPT_DIR/deploy-245.sh" "$@"
          fi
          echo "ERROR: canonical deploy is not wired for target $TARGET_CANONICAL" >&2
          exit 64
          ;;
      esac
      ;;
  esac
fi

# ── 颜色与日志 ─────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}!${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${G}═══════ $* ═══════${N}"; }
die()   { err "$@"; exit "${2:-1}"; }

# ── 默认配置 ───────────────────────────────────────────────────────
TARGET_DEFAULT="both"
TARGET="${1:-$TARGET_DEFAULT}"

# ── 部署模式检测 (2026-07-12) ───────────────────────────────────
# 根据目标服务器自动选择部署模式：
#   - host-mode: 154 / 186 / 245（systemd 主机部署，scp 二进制 + systemctl restart）
#   - k3s-mode:  252 / kaixuan-1/2/3（k3s 集群，kubectl rollout）
#   - legacy:    184 / 71（重定向到 252 / 154；按别名解析后的目标决定）
case "$TARGET" in
  154|186|245)        DEPLOY_MODE="host" ;;
  252|kaixuan-1|kaixuan-2|kaixuan-3) DEPLOY_MODE="k3s" ;;
  184)                DEPLOY_MODE="k3s";  TARGET="252" ;;  # legacy 重定向
  71)                 DEPLOY_MODE="host"; TARGET="154" ;;  # legacy 重定向
  build|migrate|verify|rollback)
                       DEPLOY_MODE="-" ;;  # 命令模式，不需要
  both)
                       # both 模式：第一个部署 252（k3s），再部署 154（host）
                       DEPLOY_MODE="both" ;;
  *) DEPLOY_MODE="?" ;;
esac
if [[ "$DEPLOY_MODE" == "?" ]]; then
  err "未知 target: $TARGET"
  usage_short
  exit 64
fi
info "部署模式: $DEPLOY_MODE → target=$TARGET"
shift 2>/dev/null || true

# Parse flags
WITH_MIGRATION=false
SKIP_TESTS=false
DRY_RUN=false
NO_ROLLBACK=false
SKIP_BUILD=false
BUILD_SEQ_TARGET=""
ACTION="deploy"

# Help first (don't need target/args for help)
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --with-migration) WITH_MIGRATION=true; shift ;;
    --skip-tests)     SKIP_TESTS=true; shift ;;
    --dry-run)        DRY_RUN=true; shift ;;
    --no-rollback)    NO_ROLLBACK=true; shift ;;
    --skip-build)     SKIP_BUILD=true; shift ;;
    --seq)            BUILD_SEQ_TARGET="$2"; shift 2 ;;
    -h|--help)        sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)                err "未知参数: $1"; exit 64 ;;
  esac
done

# ── 服务器与 SSH 证书（key-only，不使用密码） ────────────────────
# 2026-07-12 重新规划后的服务器角色：
#   154    生产网关（公网 47.97.111.154 / 内网 172.16.2.209）— 部署 llm.kxpms.cn（主机模式，非 k3s）
#   252    阿里云（公网 115.29.212.252 / 内网 172.16.2.210）— 部署 llm.itestu.cn (llm-gateway-go) + nps + vpn + redis:6389 + pg17:5432
#   245    网关服务器（公网 8.136.114.245 / 内网 172.16.2.241）— registry.kxpms.cn 生产镜像
#   186    应用服务器（公网 118.31.18.168）— 即将弃用
#   kaixuan-1  内网 192.168.31.28（tart vm + k3s 控制面；pg17@192.168.31.8:30432 + citus 13.3-1）
#   kaixuan-2  内网 192.168.31.19（k3s worker；应用服务 trendaradar/crm/geo/doc-tools/...）
#   kaixuan-3  内网 192.168.31.30（k3s worker；数据服务 pg/memos/...；nexus.kxpms.cn 同内一组）
#
# 历史别名重定向（保留调用方语义）：
#   "184" → 252（公网部署目标；旧 184 退役）
#   "71"  → 154（旧 71 退役）
SSH_PORT="${SSH_PORT:-25022}"
SSH_USER="${SSH_USER:-root}"

# 154 / 252 / 245 / 186 — 全部用 root + Kaixuan2026&#*9527，密钥登录优先
SERVER_154="root@47.97.111.154"
SERVER_154_HOST="47.97.111.154"
SERVER_252="root@115.29.212.252"
SERVER_252_HOST="115.29.212.252"
SERVER_245="root@8.136.114.245"
SERVER_245_HOST="8.136.114.245"
SERVER_186="root@118.31.18.168"
SERVER_186_HOST="118.31.18.168"

# kaixuan-1/2/3 — 内网 tart VM + k3s，使用不同凭据（kaixuan/kaixuan123）
SERVER_KAIXUAN_1="kaixuan@192.168.31.28"
SERVER_KAIXUAN_1_HOST="192.168.31.28"
SERVER_KAIXUAN_2="kaixuan@192.168.31.19"
SERVER_KAIXUAN_2_HOST="192.168.31.19"
SERVER_KAIXUAN_3="kaixuan@192.168.31.30"
SERVER_KAIXUAN_3_HOST="192.168.31.30"

# 兼容旧别名
SERVER_184="${SERVER_252}"        # 184 退役 → 重定向 252
SERVER_184_HOST="${SERVER_252_HOST}"
SERVER_71="${SERVER_154}"         # 71 退役  → 重定向 154
SERVER_71_HOST="${SERVER_154_HOST}"

# 私钥路径（全部使用 ed25519；密钥登录）
SSH_KEY_154="${SSH_KEY_154:-$HOME/.ssh/id_ed25519}"
SSH_KEY_252="${SSH_KEY_252:-$HOME/.ssh/id_ed25519}"
SSH_KEY_245="${SSH_KEY_245:-$HOME/.ssh/id_ed25519}"
SSH_KEY_186="${SSH_KEY_186:-$HOME/.ssh/id_ed25519}"
SSH_KEY_KAIXUAN_1="${SSH_KEY_KAIXUAN_1:-$HOME/.ssh/id_ed25519}"
SSH_KEY_KAIXUAN_2="${SSH_KEY_KAIXUAN_2:-$HOME/.ssh/kaixuan2_id_rsa}"
SSH_KEY_KAIXUAN_3="${SSH_KEY_KAIXUAN_3:-$HOME/.ssh/kaixuan3_id_rsa}"

# 兼容旧别名
SSH_KEY_184="${SSH_KEY_184:-$SSH_KEY_252}"
SSH_KEY_71="${SSH_KEY_71:-$SSH_KEY_154}"

# -o BatchMode=yes: 禁用交互式密码提示（强制 cert-only）
SSH_154_OPT="-p $SSH_PORT -i $SSH_KEY_154 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_252_OPT="-p $SSH_PORT -i $SSH_KEY_252 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_245_OPT="-p $SSH_PORT -i $SSH_KEY_245 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_186_OPT="-p $SSH_PORT -i $SSH_KEY_186 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_KAIXUAN_1_OPT="-p 22 -i $SSH_KEY_KAIXUAN_1 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_KAIXUAN_2_OPT="-p $SSH_PORT -i $SSH_KEY_KAIXUAN_2 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"
SSH_KAIXUAN_3_OPT="-p $SSH_PORT -i $SSH_KEY_KAIXUAN_3 -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no"

SCP_154_OPT="-P $SSH_PORT -i $SSH_KEY_154 -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
SCP_252_OPT="-P $SSH_PORT -i $SSH_KEY_252 -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
SCP_245_OPT="-P $SSH_PORT -i $SSH_KEY_245 -o StrictHostKeyChecking=accept-new -o BatchMode=yes"
SCP_186_OPT="-P $SSH_PORT -i $SSH_KEY_186 -o StrictHostKeyChecking=accept-new -o BatchMode=yes"

# 兼容旧别名
SSH_184_OPT="$SSH_252_OPT"
SSH_71_OPT="$SSH_154_OPT"
SCP_184_OPT="$SCP_252_OPT"
SCP_71_OPT="$SCP_154_OPT"

# Image / registry
# 注意: IMAGE_NAME = docker 镜像名 (含 kx- 前缀); K8S_CONTAINER = K8s pod 容器名 (无前缀)
IMAGE_NAME="kx-llm-gateway-go"
K8S_CONTAINER="llm-gateway-go"
# registry.kxpms.cn = 245 阿里网关（公网 8.136.114.245），kaixuan/Veritrans&9527（生产）
# registry.itestu.cn = kaixuan-1 内网（192.168.31.8:5000），开发测试用
REGISTRY_INT="${REGISTRY_INT:-registry.kxpms.cn}"
REGISTRY_DEV="${REGISTRY_DEV:-registry.itestu.cn}"
# 154 主机部署使用 kaixuan-1 内网 registry（与 llm.kxpms.cn 同内一组）
REGISTRY_154="${REGISTRY_154:-${REGISTRY_DEV}}"
# 252 阿里云 llm.itestu.cn 部署使用 245 公网 registry
REGISTRY_252="${REGISTRY_252:-${REGISTRY_INT}}"
REGISTRY_LOCAL="${REGISTRY_LOCAL:-${REGISTRY_DEV}}"
K8S_NS="pms-test"
K8S_DEP="llm-gateway-go-deployment"
KAIXUAN1_KUBECONFIG="${KAIXUAN1_KUBECONFIG:-$HOME/.kube/kaixuan-1-config}"
KAIXUAN1_K3S_API="${KAIXUAN1_K3S_API:-192.168.31.8:6443}"
KAIXUAN1_NODEPORT="${KAIXUAN1_NODEPORT:-30080}"
KAIXUAN1_NPC_PORT="${KAIXUAN1_NPC_PORT:-11008}"
LLM_ITESTU_DOMAIN="https://llm.itestu.cn"

# 71 binary
BIN_NAME="llm-gateway-go.v321.linux.amd64"

# ── 公共函数 ────────────────────────────────────────────────────────

usage_short() {
  cat <<EOF
用法: $0 <target> [options]
target: 252 | 154 | 245 | 186 | kaixuan-1 | kaixuan-2 | kaixuan-3 | both | build | migrate | verify | rollback
   兼容旧别名：184 → 252（公网数据面）, 71 → 154（公网主机部署）
详细: $0 --help
EOF
}

# ── 早期验证 target + SSH 证书 ─────────────────────────────────────
verify_ssh_key() {
  local alias="$1" key="$2" target="$3"
  if [[ ! -f "$key" ]]; then
    err "[$alias] SSH 私钥不存在: $key"
    err "    修复:"
    err "      ssh-add $key                       # 临时加载"
    err "      或 env-injector inject $target     # 写入 ~/.ssh/config"
    exit 1
  fi
  if [[ ! -r "$key" ]]; then
    err "[$alias] SSH 私钥不可读: $key (chmod 600)"
    exit 1
  fi
  local perms
  perms=$(stat -f '%Lp' "$key" 2>/dev/null || stat -c '%a' "$key" 2>/dev/null)
  if [[ -n "$perms" && "$perms" != "600" && "$perms" != "400" ]]; then
    warn "[$alias] 私钥权限 $perms（建议 chmod 600）: $key"
  fi
}

case "$TARGET" in
  184|71|252|154|245|186|kaixuan-1|kaixuan-2|kaixuan-3|both|build|migrate|verify|rollback)
    if [[ "$TARGET" == "184" || "$TARGET" == "252" || "$TARGET" == "both" ]]; then
      verify_ssh_key "252" "$SSH_KEY_252" "deploy-252"
    fi
    if [[ "$TARGET" == "71" || "$TARGET" == "154" || "$TARGET" == "both" ]]; then
      verify_ssh_key "154" "$SSH_KEY_154" "deploy-154"
    fi
    if [[ "$TARGET" == "245" ]]; then
      verify_ssh_key "245" "$SSH_KEY_245" "deploy-245"
    fi
    if [[ "$TARGET" == "186" ]]; then
      verify_ssh_key "186" "$SSH_KEY_186" "deploy-186"
    fi
    if [[ "$TARGET" == "kaixuan-1" ]]; then
      verify_ssh_key "kaixuan-1" "$SSH_KEY_KAIXUAN_1" "deploy-kaixuan-1"
    fi
    if [[ "$TARGET" == "kaixuan-2" ]]; then
      verify_ssh_key "kaixuan-2" "$SSH_KEY_KAIXUAN_2" "deploy-kaixuan-2"
    fi
    if [[ "$TARGET" == "kaixuan-3" ]]; then
      verify_ssh_key "kaixuan-3" "$SSH_KEY_KAIXUAN_3" "deploy-kaixuan-3"
    fi
    ;;
  *) usage_short; exit 64 ;;
esac

pre_check() {
  phase "预检 1/3: 工作区 + git"
  # 检查工作区未提交改动（同时算 staged + unstaged）
  if ! git diff --quiet HEAD -- 2>/dev/null || ! git diff --cached --quiet HEAD -- 2>/dev/null; then
    err "工作区有未提交的改动 (unstaged + staged):"
    git status -s
    return 1
  fi
  ok "工作区干净"

  phase "预检 2/3: SSH 证书可达性"
  if [[ "$TARGET" == "184" || "$TARGET" == "both" ]]; then
    info "ssh -i $SSH_KEY_184 root@$SERVER_184_HOST"
    if ! ssh $SSH_184_OPT $SERVER_184 'echo OK' >/dev/null 2>&1; then
      err "184 SSH 不可达 (key=$SSH_KEY_184, host=$SERVER_184_HOST:$SSH_PORT)"
      err "请确认私钥已加载或已通过 ssh-add / env-injector 注入"
      return 1
    fi
    ok "184 SSH OK"
  fi
  if [[ "$TARGET" == "71" || "$TARGET" == "both" ]]; then
    info "ssh -i $SSH_KEY_71 root@$SERVER_71_HOST"
    if ! ssh $SSH_71_OPT $SERVER_71 'echo OK' >/dev/null 2>&1; then
      err "71 SSH 不可达 (key=$SSH_KEY_71, host=$SERVER_71_HOST:$SSH_PORT)"
      return 1
    fi
    ok "71 SSH OK"
  fi

  phase "预检 3/3: go build + vet (F3: 捕获 panic-class bugs)"
  if [[ "$SKIP_TESTS" == "true" ]]; then
    warn "已 --skip-tests，跳过 go build/vet"
    return 0
  fi
  info "go build ./..."
  if ! go build ./... 2>&1 | tail -10; then
    err "go build 失败！常见原因: 路由注册冲突 (本次踩坑)、import 错误、类型错误"
    return 1
  fi
  info "go vet ./..."
  if ! go vet ./... 2>&1 | tail -10; then
    err "go vet 失败"
    return 1
  fi
  ok "go build + vet 通过"
}

# get_version: 委托给 scripts/bump-version.sh (single source of truth)
# bump-version.sh 同步更新 4 个版本文件 lockstep:
#   VERSION / version.json / web/public/version.json / web/dist/version.json
# 之后从 version.json 读回 (因为 dry-run 时 bump-version 不写文件)
get_version() {
  phase "版本信息 (via bump-version.sh — 4 文件 lockstep)"
  local bump_args=()
  [[ "$DRY_RUN" == "true" ]] && bump_args=(--dry-run)
  [[ -n "$BUILD_SEQ_TARGET" ]] && bump_args+=(--seq "$BUILD_SEQ_TARGET")

  bash "$REPO_ROOT/scripts/bump-version.sh" "${bump_args[@]}" || true

  # 读最新版 (无论 dry-run 模式; dry-run 后看的就是 N+1 的预览)
  local seq_after
  seq_after=$(cat build_seq 2>/dev/null || echo 0)
  if [[ -f version.json ]]; then
    GIT_TAG=$(python3 -c "import json; print(json.load(open('version.json'))['git_tag'])" 2>/dev/null || echo "")
    GIT_SHA=$(python3 -c "import json; print(json.load(open('version.json'))['git_sha'])" 2>/dev/null || echo "")
    BUILD_DATE=$(python3 -c "import json; print(json.load(open('version.json'))['build_date'])" 2>/dev/null || echo "")
    NEW_BUILD_SEQ=$(python3 -c "import json; print(json.load(open('version.json'))['build_seq'])" 2>/dev/null || echo "$seq_after")
  else
    NEW_BUILD_SEQ="$seq_after"
  fi

  IMAGE_TAG="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"
  VERSION_STRING="${GIT_TAG}-${GIT_SHA}-${BUILD_DATE}-${NEW_BUILD_SEQ}"

  info "Git Tag:     $GIT_TAG"
  info "Git SHA:     $GIT_SHA"
  info "Build Seq:   $NEW_BUILD_SEQ"
  info "Image Tag:   $IMAGE_TAG"

  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] bump-version.sh 已 dry-run 不写文件，但下面部署动作仍照常"
  fi
}

# F1: Smart push to 184 local registry.
# Tries local docker push; if Docker daemon HTTP_PROXY blocks 127.0.0.1,
# falls back to SSH-in-184 pull+retag+push.
push_to_local_registry() {
  phase "推送到 184 本地 registry ${REGISTRY_LOCAL} (smart)"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 127.0.0.1:5000 推送"
    return 0
  fi
  local img="${IMAGE_NAME}:${IMAGE_TAG}"
  local remote_img="${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG}"

  info "尝试本地直推: $remote_img"
  if docker push "$remote_img" 2>/tmp/.push.err; then
    ok "本地直推成功"
    return 0
  fi

  local err_msg
  err_msg=$(cat /tmp/.push.err 2>/dev/null || echo "")
  if echo "$err_msg" | grep -qE "connection refused|connection reset|i/o timeout|no such host" ; then
    warn "本地直推受阻（疑似 Docker daemon HTTP_PROXY 拦截 ${REGISTRY_LOCAL}）："
    echo "  ${err_msg}" | head -3
  else
    err "推送失败（非代理问题）:"
    cat /tmp/.push.err
    return 1
  fi
  
  info "回退路径：SSH 到 184 拉取 ${REGISTRY_INT}/${img} 并 re-tag 重推"
  ssh $SSH_184_OPT $SERVER_184 bash <<EOF || return 1
set -e
docker pull ${REGISTRY_INT}/${img}
docker tag ${REGISTRY_INT}/${img} ${remote_img}
docker push ${remote_img}
EOF
  ok "通过 184 推送成功"
}

commit_build_seq() {
  phase "提交 build_seq ($NEW_BUILD_SEQ)"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 commit"
    return 0
  fi
  if git diff --quiet build_seq version.json 2>/dev/null; then
    info "build_seq / version.json 无变化，跳过 commit"
    return 0
  fi
  git add build_seq version.json
  git commit -m "chore: bump build_seq to ${NEW_BUILD_SEQ} via deploy.sh [skip ci]" 2>&1 | tail -3
  ok "build_seq 已提交"
}

# ── kaixuan-1 k3s 部署 ─────────────────────────────────────────────

k3s_kaixuan1_reachable() {
  KUBECONFIG="$KAIXUAN1_KUBECONFIG" kubectl cluster-info --request-timeout=8s >/dev/null 2>&1
}

push_to_kaixuan1_registry() {
  phase "kaixuan-1: 推送到 ${REGISTRY_DEV}"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 docker push ${REGISTRY_DEV}"
    return 0
  fi
  local remote_img="${REGISTRY_DEV}/${IMAGE_NAME}:${IMAGE_TAG}"
  docker tag "${IMAGE_NAME}:${IMAGE_TAG}" "$remote_img"
  docker tag "${IMAGE_NAME}:${IMAGE_TAG}" "${REGISTRY_DEV}/${IMAGE_NAME}:latest"
  if docker push "$remote_img" 2>/tmp/.push_k1.err && docker push "${REGISTRY_DEV}/${IMAGE_NAME}:latest" 2>/tmp/.push_k1_latest.err; then
    ok "已推送到 ${REGISTRY_DEV}"
    return 0
  fi
  warn "直推 ${REGISTRY_DEV} 失败（tart-vm registry 可能不可达）:"
  head -3 /tmp/.push_k1.err 2>/dev/null || true
  return 1
}

update_k3s_kaixuan1_deployment() {
  phase "kaixuan-1: kubectl apply + set image"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  export KUBECONFIG="$KAIXUAN1_KUBECONFIG"
  kubectl apply -f "$REPO_ROOT/deploy/k8s/llm-gateway-go-deployment.yaml"
  kubectl set image "deployment/${K8S_DEP}" \
    "${K8S_CONTAINER}=${REGISTRY_DEV}/${IMAGE_NAME}:${IMAGE_TAG}" -n "$K8S_NS"
  kubectl rollout status "deployment/${K8S_DEP}" -n "$K8S_NS" --timeout=5m
  ok "k3s 滚动更新完成"
}

configure_252_nginx_llm_itestu() {
  phase "252: llm.itestu.cn nginx → 127.0.0.1:${KAIXUAN1_NPC_PORT}"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 nginx 更新"
    return 0
  fi
  ssh $SSH_252_OPT $SERVER_252 bash <<'NGINX_EOF'
set -e
CONF=/etc/nginx/conf.d/llm.itestu.cn.conf
cp "$CONF" "${CONF}.bak.$(date +%Y%m%d-%H%M%S)"
sed -i 's|proxy_pass http://127.0.0.1:8780;|proxy_pass http://127.0.0.1:11008;|g' "$CONF"
grep -q '127.0.0.1:11008' "$CONF" || { echo "nginx 更新失败: 未找到 11008"; exit 1; }
nginx -t
systemctl reload nginx
echo "✓ nginx reloaded"
NGINX_EOF
  ok "252 nginx 已指向 NPC 隧道 :${KAIXUAN1_NPC_PORT}"
}

configure_kaixuan1_npc_llm_gateway() {
  local target_addr="$1"
  phase "kaixuan-1: NPC [llm_gateway] → ${target_addr}"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 NPC 更新 (target=${target_addr})"
    return 0
  fi
  ssh $SSH_KAIXUAN_1_OPT $SERVER_KAIXUAN_1 bash <<EOF
set -e
CONF=\$HOME/.local/npc/npc-kaixuan-1-nginx-entry.conf
cp "\$CONF" "\${CONF}.bak.\$(date +%Y%m%d-%H%M%S)"
python3 - <<'PY'
from pathlib import Path
import re
conf = Path.home() / ".local/npc/npc-kaixuan-1-nginx-entry.conf"
text = conf.read_text()
block = re.compile(
    r"(\[llm_gateway\][^\[]*target_addr=)[^\n]+",
    re.MULTILINE,
)
new_text, n = block.subn(r"\g<1>${target_addr}", text, count=1)
if n != 1:
    raise SystemExit("llm_gateway block not updated")
conf.write_text(new_text)
print("✓ NPC target_addr=${target_addr}")
PY
launchctl kickstart -k "gui/\$(id -u)/com.kxmemory.npc.primary" 2>/dev/null || true
sleep 2
EOF
  ok "NPC llm_gateway 已更新"
}

verify_kaixuan1_public() {
  phase "kaixuan-1: 公网验证 ${LLM_ITESTU_DOMAIN}/healthz"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  sleep 5
  local body
  body=$(curl -fsS --max-time 15 "${LLM_ITESTU_DOMAIN}/healthz" 2>&1 || echo "")
  if [[ -z "$body" ]]; then
    err "${LLM_ITESTU_DOMAIN}/healthz 不可达"
    return 1
  fi
  ok "${LLM_ITESTU_DOMAIN} → $body"
  if ! echo "$body" | grep -q "$IMAGE_TAG"; then
    warn "公网版本未匹配 IMAGE_TAG=$IMAGE_TAG（可能 CDN/缓存）"
  fi
  return 0
}

deploy_k3s_kaixuan1_host() {
  phase "kaixuan-1: host 模式回退 (launchd / :8088, NPC 隧道)"
  warn "k3s 不可达 — 使用 macOS host 部署（tart-vm 恢复后可切回 k3s NodePort :${KAIXUAN1_NODEPORT}）"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 将执行 deploy-kaixuan1.sh --skip-bump"
    configure_kaixuan1_npc_llm_gateway "127.0.0.1:8088"
    configure_252_nginx_llm_itestu
    return 0
  fi
  LLM_GATEWAY_PORT=8088 bash "$REPO_ROOT/scripts/deploy-kaixuan1.sh" --skip-bump || return 1
  configure_kaixuan1_npc_llm_gateway "127.0.0.1:8088"
  configure_252_nginx_llm_itestu
  verify_kaixuan1_public || return 1
}

deploy_k3s_kaixuan1() {
  phase "════════════ kaixuan-1 k3s 部署 ════════════"
  if k3s_kaixuan1_reachable; then
    ok "k3s API 可达 (${KAIXUAN1_K3S_API})"
    build_image || return 2
    push_to_kaixuan1_registry || {
      err "镜像推送失败 — registry ${REGISTRY_DEV} 不可达"
      return 3
    }
    update_k3s_kaixuan1_deployment || return 4
    configure_kaixuan1_npc_llm_gateway "192.168.31.8:${KAIXUAN1_NODEPORT}"
    configure_252_nginx_llm_itestu
    verify_kaixuan1_public || return 5
    ok "kaixuan-1 k3s 部署完成"
    return 0
  fi
  err "k3s 集群不可达 (KUBECONFIG=${KAIXUAN1_KUBECONFIG}, api=${KAIXUAN1_K3S_API})"
  err "  常见原因: tart-vm Guest Agent 未运行 / 192.168.31.8 网络不通"
  deploy_k3s_kaixuan1_host || return 4
}

# ── 184 部署 ────────────────────────────────────────────────────────

build_image() {
  phase "184: 构建镜像"
  if [[ "$DRY_RUN" == "true" || "$SKIP_BUILD" == "true" ]]; then
    info "${DRY_RUN:+(DRY-RUN) }${SKIP_BUILD:+(skip-build) }跳过 docker build"
    return 0
  fi
  docker build \
    --build-arg GIT_TAG="${GIT_TAG}" \
    --build-arg GIT_SHA="${GIT_SHA}" \
    --build-arg BUILD_SEQ="${NEW_BUILD_SEQ}" \
    --build-arg BUILD_DATE="${BUILD_DATE}" \
    -t "${IMAGE_NAME}:${IMAGE_TAG}" \
    -t "${IMAGE_NAME}:latest" \
    . | tail -20
  ok "镜像构建完成"
}

push_to_public_registry() {
  phase "184: 推送到 ${REGISTRY_INT}"
  if [[ "$DRY_RUN" == "true" ]]; then
    info "[DRY-RUN] 跳过 docker push"
    return 0
  fi
  docker tag "${IMAGE_NAME}:${IMAGE_TAG}" "${REGISTRY_INT}/${IMAGE_NAME}:${IMAGE_TAG}"
  docker push "${REGISTRY_INT}/${IMAGE_NAME}:${IMAGE_TAG}" 2>&1 | tail -5
  ok "已推送"
}

update_k8s_deployment() {
  phase "184: kubectl set image → rollout status"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl set image deployment/${K8S_DEP} ${K8S_CONTAINER}=${REGISTRY_LOCAL}/${IMAGE_NAME}:${IMAGE_TAG} -n ${K8S_NS}"
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=5m"
  ok "K8s 滚动更新完成"
}

verify_184() {
  phase "184: 验证"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  sleep 8  # 等启动 + readiness probe
  
  # Pod 状态
  local pod=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'" 2>/dev/null)
  if [[ -z "$pod" ]]; then
    err "找不到 Running pod"
    return 1
  fi
  
  local ready=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go -o jsonpath='{.items[0].status.containerStatuses[0].ready}'" 2>/dev/null)
  if [[ "$ready" != "true" ]]; then
    err "Pod Ready=$ready (期望 true) — pod: $pod"
    return 1
  fi
  ok "Pod Ready=1/1 ($pod)"
  
  # 无 panic / fatal
  # 注意: bash $((expr)) 把空字符串当 0, 但 grep -c 可能输出 multi-line
  # 用 wc -l 计数而非 -c 避免嵌入 \n 引起的算术解析错误
  local panic_lines
  panic_lines=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl logs -n ${K8S_NS} $pod --tail=300 2>/dev/null | grep -iE 'panic|fatal' | wc -l" | tr -d ' ')
  panic_lines=${panic_lines:-0}
  if [[ "$panic_lines" -gt 0 ]]; then
    err "Pod 日志发现 ${panic_lines} 处 panic/fatal："
    ssh $SSH_184_OPT $SERVER_184 \
      "kubectl logs -n ${K8S_NS} $pod --tail=100 2>/dev/null | grep -A 1 -E 'panic|fatal' | head -20"
    return 1
  fi
  ok "无 panic/fatal"
  
  # 公开域名
  local body=$(curl -fsS --max-time 10 https://llmgo.kxpms.cn/healthz 2>&1 || echo "")
  if [[ -z "$body" ]]; then
    err "https://llmgo.kxpms.cn/healthz 不可达"
    return 1
  fi
  ok "https://llmgo.kxpms.cn → $body"
  if ! echo "$body" | grep -q "$IMAGE_TAG"; then
    err "公开域版本不匹配: 期望 $IMAGE_TAG"
    return 1
  fi
  ok "版本匹配: $IMAGE_TAG"
  return 0
}

rollback_184() {
  phase "184: 自动回滚"
  warn "触发回滚: kubectl rollout undo deployment/${K8S_DEP} -n ${K8S_NS}"
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout undo deployment/${K8S_DEP} -n ${K8S_NS}" 2>&1 | tail -3
  ssh $SSH_184_OPT $SERVER_184 \
    "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=3m" 2>&1 | tail -5
}

# F2: auto-skip superseded migrations. Also tracks applied versions in
# schema_migrations so re-runs are idempotent.
run_migrations_184() {
  phase "184: DB 迁移 (auto-skip SUPERSEDED + 幂等)"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  local mig_dir="$REPO_ROOT/db/migrations"
  
  # Upload
  ssh $SSH_184_OPT $SERVER_184 "mkdir -p /tmp/migrations_run && rm -f /tmp/migrations_run/*"
  for f in "$mig_dir"/*.sql; do
    [[ "$(basename "$f")" == *".down.sql" ]] && continue
    scp $SCP_184_OPT "$f" $SERVER_184:/tmp/migrations_run/ 2>/dev/null
  done
  
  # 提取 DB 凭据
  local pod=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl get pods -n ${K8S_NS} -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}'")
  local db_url=$(ssh $SSH_184_OPT $SERVER_184 \
    "kubectl exec -n ${K8S_NS} $pod -- printenv LLM_GATEWAY_DATABASE_URL")
  local db_pass=$(echo "$db_url" | sed -n 's|.*://[^:]*:\([^@]*\)@.*|\1|p')
  local db_host=$(echo "$db_url" | sed -n 's|.*@\([^:]*\):.*|\1|p')
  
  # Host 校验 (F5: 不连错 71 的 172.31.0.3 等)
  case "$db_host" in
    172.31.0.4|10.43.*) ok "DB host = $db_host (K8s 内部 IP, 正确)" ;;
    172.31.0.3) err "DB host = 172.31.0.3 (这是 71 的 IP! 184 应连 172.31.0.4 或 K8s ClusterIP)"; return 1 ;;
    *) warn "DB host = $db_host (非预期; 请人工确认)" ;;
  esac
  
  # 用单引号 heredoc ('MIGRATION_EOF') 防止本地 set -u 触发 \$VAR 求值
  # 所有 \$VAR 在远程 bash 解释时求值, 本地只透传字节
  ssh $SSH_184_OPT $SERVER_184 bash <<'MIGRATION_EOF' || { return 1; }
set -e
export PGPASSWORD="$db_pass"

# 已应用版本
APPLIED=$(psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway -tAc \
  "SELECT version FROM schema_migrations;" 2>/dev/null | tr -d ' ')

cd /tmp/migrations_run
TOTAL=0; APPLIED_C=0; SKIPPED=0; FAILED=0
for f in *.sql; do
  TOTAL=$((TOTAL+1))
  version=$(echo "$f" | grep -oE '^[0-9]+' || echo "0")

  # F2: auto-skip SUPERSEDED
  if head -10 "$f" | grep -qiE "SUPERSEDED|superceded|DEPRECATED"; then
    echo "  [$f] ⊘ SKIP (SUPERSEDED)"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  # 跳过已应用
  if echo "$APPLIED" | grep -qFx "$version"; then
    echo "  [$f] ⊘ SKIP (已应用)"
    SKIPPED=$((SKIPPED+1))
    continue
  fi

  # 应用
  if psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -v ON_ERROR_STOP=1 -f "/tmp/migrations_run/$f" >/tmp/_mig.log 2>&1; then
    echo "  [$f] ✓ OK"
    psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -c "INSERT INTO schema_migrations (version, applied_at) VALUES ('$version', NOW());" >/dev/null 2>&1 || true
    APPLIED_C=$((APPLIED_C+1))
  elif grep -qE "already exists|duplicate key|relation.*already exists" /tmp/_mig.log 2>/dev/null; then
    echo "  [$f] ⊘ SKIP (idempotent: 已存在)"
    psql -h "$db_host" -p 5432 -U llm_gateway -d llm_gateway \
      -c "INSERT INTO schema_migrations (version, applied_at) VALUES ('$version', NOW());" >/dev/null 2>&1 || true
    SKIPPED=$((SKIPPED+1))
  else
    echo "  [$f] ✗ FAIL"
    head -3 /tmp/_mig.log | sed 's/^/    /'
    FAILED=$((FAILED+1))
  fi
done
rm -f /tmp/_mig.log /tmp/migrations_run/*.sql
rmdir /tmp/migrations_run 2>/dev/null || true

echo ""
echo "=== DB 迁移汇总 ==="
echo "  total=$TOTAL  applied=$APPLIED_C  skipped=$SKIPPED  failed=$FAILED"
[ "$FAILED" -gt 0 ] && exit 1 || true
MIGRATION_EOF
}

deploy_184() {
  phase "════════════ 184 K8s 部署 ════════════"
  build_image
  push_to_public_registry
  push_to_local_registry
  update_k8s_deployment
  
  if ! verify_184; then
    err "184 验证失败"
    [[ "$NO_ROLLBACK" != "true" ]] && rollback_184
    exit 5
  fi
  
  if [[ "$WITH_MIGRATION" == "true" ]]; then
    run_migrations_184 && {
      info "迁移后 rolling restart"
      ssh $SSH_184_OPT $SERVER_184 \
        "kubectl rollout restart deployment/${K8S_DEP} -n ${K8S_NS}" >/dev/null
      ssh $SSH_184_OPT $SERVER_184 \
        "kubectl rollout status deployment/${K8S_DEP} -n ${K8S_NS} --timeout=3m" >/dev/null
      verify_184 || { err "迁移后验证失败"; [[ "$NO_ROLLBACK" != "true" ]] && rollback_184; exit 5; }
    }
  fi
  ok "184 部署完成"
}

# ── 71 部署 ─────────────────────────────────────────────────────────

cross_compile() {
  phase "71: 交叉编译 linux/amd64"
  if [[ "$DRY_RUN" == "true" || "$SKIP_BUILD" == "true" ]]; then
    info "${DRY_RUN:+(DRY-RUN) }${SKIP_BUILD:+(skip-build) }跳过交叉编译"
    return 0
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w \
      -X 'main.Version=${GIT_TAG}' \
      -X 'main.GitCommit=${GIT_SHA}' \
      -X 'main.BuildDate=${BUILD_DATE}' \
      -X 'main.BuildNumber=${NEW_BUILD_SEQ}'" \
    -o "${BIN_NAME}" \
    ./cmd/gateway 2>&1 | tail -10
  local size=$(ls -lh "${BIN_NAME}" | awk '{print $5}')
  file "${BIN_NAME}" | head -1 | sed 's/^/  /'
  ok "二进制已构建: $BIN_NAME ($size)"
}

upload_to_71() {
  phase "71: 上传二进制 + VERSION"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  
  scp $SCP_71_OPT "${BIN_NAME}" "$SERVER_71:/tmp/"
  
  echo "$VERSION_STRING" > /tmp/VERSION_NEW
  echo "$NEW_BUILD_SEQ" > /tmp/.deploy_seq
  scp $SCP_71_OPT /tmp/VERSION_NEW "$SERVER_71:/tmp/VERSION_NEW"
  scp $SCP_71_OPT /tmp/.deploy_seq "$SERVER_71:/tmp/.deploy_seq"
  ok "已上传"
}

restart_71() {
  phase "71: 原子化备份 + restart"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  ssh $SSH_71_OPT $SERVER_71 bash <<EOF || { err "71 端操作失败"; return 1; }
set -e
mkdir -p /opt/llm-gateway-go/backup

echo "  [1/7] 备份当前二进制 (.bak.YYYYMMDD-HHMMSS)"
BAK="/opt/llm-gateway-go/backup/${BIN_NAME}.bak.\$(date +%Y%m%d-%H%M%S)"
cp /opt/llm-gateway-go/${BIN_NAME} "\$BAK"
echo "       → \$BAK"

echo "  [2/7] 停止服务 (释放 mmap 锁)"
systemctl stop llm-gateway-go.service || true
sleep 2

echo "  [3/7] 替换二进制"
mv -f /tmp/${BIN_NAME} /opt/llm-gateway-go/${BIN_NAME}
chmod +x /opt/llm-gateway-go/${BIN_NAME}

echo "  [4/7] 更新 VERSION + .deploy_seq"
mv -f /tmp/VERSION_NEW /opt/llm-gateway-go/VERSION
mv -f /tmp/.deploy_seq /opt/llm-gateway-go/.deploy_seq

echo "  [5/7] 清理旧 .bak (保留最近 5 个)"
ls -1t /opt/llm-gateway-go/backup/*.bak.* 2>/dev/null | tail -n +6 | xargs -r rm -f

echo "  [6/7] 启动服务"
systemctl start llm-gateway-go.service
sleep 3

echo "  [7/7] 等待 ready (port 8781 listening)"
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ss -tln 2>/dev/null | grep -q :8781; then
    echo "       端口已就绪 (\${i}s)"
    break
  fi
  sleep 1
done
EOF
}

verify_71() {
  phase "71: 验证"
  if [[ "$DRY_RUN" == "true" ]]; then return 0; fi
  sleep 3
  
  local active=$(ssh $SSH_71_OPT $SERVER_71 'systemctl is-active llm-gateway-go.service' 2>/dev/null)
  if [[ "$active" != "active" ]]; then
    err "service $active (期望 active)"
    return 1
  fi
  ok "service active"
  
  # F5: DB host 不应是 184
  local db_url=$(ssh $SSH_71_OPT $SERVER_71 'grep LLM_GATEWAY_DATABASE_URL /etc/llm-gateway-go/env | cut -d= -f2-')
  case "$db_url" in
    *172.31.0.3*) ok "DB host = 172.31.0.3 (71 本地 PG, 正确)" ;;
    *172.31.0.4*) err "DB host = 172.31.0.4 (这是 184 的 PG!)"; return 1 ;;
    *) warn "DB host 未识别: ${db_url:0:60}..." ;;
  esac
  
  local seq=$(ssh $SSH_71_OPT $SERVER_71 'cat /opt/llm-gateway-go/.deploy_seq' 2>/dev/null)
  if [[ "$seq" != "$NEW_BUILD_SEQ" ]]; then
    err "build_seq 不匹配: $seq != $NEW_BUILD_SEQ"
    return 1
  fi
  ok "build_seq=$seq"
  
  local body=$(ssh $SSH_71_OPT $SERVER_71 'curl -fsS --max-time 5 http://localhost:8781/healthz' 2>/dev/null)
  if [[ -z "$body" ]]; then
    err "http://localhost:8781/healthz 不可达"
    return 1
  fi
  if ! echo "$body" | grep -q "$VERSION_STRING"; then
    err "/healthz 版本不匹配: $body"
    return 1
  fi
  ok "/healthz (71 内部): $body"
  
  local pub=$(curl -fsS --max-time 10 https://llm.kxpms.cn/healthz 2>/dev/null)
  if [[ -z "$pub" ]]; then
    err "https://llm.kxpms.cn 不可达"
    return 1
  fi
  ok "https://llm.kxpms.cn: $pub"
  
  # bind-mount 检查
  local data_mounted=$(ssh $SSH_71_OPT $SERVER_71 \
    'docker exec llm-gateway-go mount 2>/dev/null | grep -q /opt/llm-gateway-go/data && echo yes')
  if [[ "$data_mounted" != "yes" ]]; then
    warn "data bind-mount 缺失 (运维层: 跑 deploy-71-data-bindmounts.sh)"
  else
    ok "data bind-mount OK"
  fi
  return 0
}

rollback_71() {
  phase "71: 自动回滚（最近 .bak）"
  warn "回滚: 选择最近 .bak 重启服务"
  ssh $SSH_71_OPT $SERVER_71 bash <<'EOF'
set -e
LATEST_BAK=$(ls -1t /opt/llm-gateway-go/backup/*.bak.* 2>/dev/null | head -1)
if [[ -z "$LATEST_BAK" ]]; then
  echo "  ✗ 无 .bak 可回滚"
  exit 1
fi
echo "  → 回滚到 $LATEST_BAK"
systemctl stop llm-gateway-go.service || true
sleep 2
cp "$LATEST_BAK" /opt/llm-gateway-go/"$(basename "${BIN_NAME:-llm-gateway-go.v321.linux.amd64}")"
systemctl start llm-gateway-go.service
sleep 5
echo "  当前版本: $(cat /opt/llm-gateway-go/VERSION)"
EOF
}

deploy_71() {
  phase "════════════ 71 systemd 部署 ════════════"
  cross_compile
  upload_to_71
  restart_71
  
  if ! verify_71; then
    err "71 验证失败"
    [[ "$NO_ROLLBACK" != "true" ]] && rollback_71
    exit 5
  fi
  ok "71 部署完成"
}

# ── 通用 host-mode 部署函数 (2026-07-12) ──────────────────────────────
# 154 / 186 / 245 等 systemd 主机部署共用此函数
# 调用方式: deploy_host <target>
# 例如: deploy_host 154
deploy_host() {
  local target="$1"
  local ssh_opt="" scp_opt="" ssh_target="" bin_dir=""
  phase "════════════ $target systemd 主机部署 ════════════"

  # 选择对应的 SSH_OPT 和 SERVER（根据 target 名称）
  case "$target" in
    154) ssh_opt="$SSH_154_OPT"; scp_opt="$SCP_154_OPT"; ssh_target="$SERVER_154"; bin_dir="/opt/llm-gateway-go" ;;
    186) ssh_opt="$SSH_186_OPT"; scp_opt="$SCP_186_OPT"; ssh_target="$SERVER_186"; bin_dir="/opt/llm-gateway-go" ;;
    245) ssh_opt="$SSH_245_OPT"; scp_opt="$SCP_245_OPT"; ssh_target="$SERVER_245"; bin_dir="/opt/llm-gateway-go" ;;
    *) err "deploy_host 不支持 target: $target"; return 1 ;;
  esac

  BIN_NAME="llm-gateway-go.v${NEW_BUILD_SEQ}.linux.amd64"

  # 1) 编译二进制
  cross_compile

  # 2) 上传到服务器
  local bin_name="$BIN_NAME"
  info "scp $bin_name → $ssh_target:$bin_dir/"
  ssh $ssh_opt "$ssh_target" "mkdir -p $bin_dir/{data,logs,web}"
  scp $scp_opt "$bin_name" "$ssh_target:$bin_dir/$bin_name"

  # 3) 上传 version.json
  scp $scp_opt "version.json" "$ssh_target:$bin_dir/version.json"

  # 4) 上传 web/dist（如有）
  if [[ -d web/dist ]]; then
    info "rsync web/dist → $ssh_target:$bin_dir/web/"
    ssh $ssh_opt "$ssh_target" "rm -rf $bin_dir/web && mkdir -p $bin_dir/web"
    tar czf - -C web dist | ssh $ssh_opt "$ssh_target" "cat | tar xzf - -C $bin_dir/web --strip-components=1"
  fi

  # 5) 切换 symlink + 重启服务
  info "重启 $target 上的 llm-gateway-go.service..."
  ssh $ssh_opt "$ssh_target" \
    "ln -sf $bin_dir/$bin_name $bin_dir/llm-gateway-go \
     && systemctl restart llm-gateway-go.service \
     && sleep 3 \
     && systemctl is-active --quiet llm-gateway-go.service && echo ACTIVE || echo INACTIVE"

  ok "$target 部署完成（注意：env-file 未变，保留原 prod secrets）"
}

verify_host() {
  local target="$1"
  case "$target" in
    154) local ssh_opt="$SSH_154_OPT" ssh_target="$SERVER_154" port=8781 ;;
    186) local ssh_opt="$SSH_186_OPT" ssh_target="$SERVER_186" port=8781 ;;
    245) local ssh_opt="$SSH_245_OPT" ssh_target="$SERVER_245" port=8781 ;;
    *) err "verify_host 不支持 target: $target"; return 1 ;;
  esac

  phase "════════════ $target 验证 ════════════"
  # 检查 systemd 状态
  if ! ssh $ssh_opt "$ssh_target" "systemctl is-active --quiet llm-gateway-go.service"; then
    err "$target: 服务未运行"
    return 1
  fi

  # 检查健康端点
  info "curl http://$ssh_target:$port/health"
  if ! ssh $ssh_opt "$ssh_target" "curl -sf --max-time 5 http://localhost:$port/health" >/dev/null; then
    err "$target: /health 端点返回非 200"
    return 1
  fi

  ok "$target 验证通过"
}

rollback_host() {
  local target="$1"
  case "$target" in
    154) local ssh_opt="$SSH_154_OPT" ssh_target="$SERVER_154" bin_dir="/opt/llm-gateway-go" ;;
    186) local ssh_opt="$SSH_186_OPT" ssh_target="$SERVER_186" bin_dir="/opt/llm-gateway-go" ;;
    245) local ssh_opt="$SSH_245_OPT" ssh_target="$SERVER_245" bin_dir="/opt/llm-gateway-go" ;;
    *) err "rollback_host 不支持 target: $target"; return 1 ;;
  esac

  phase "════════════ $target 回滚 ════════════"
  ssh $ssh_opt "$ssh_target" bash <<EOF
cd $bin_dir
LATEST_BAK=\$(ls -t *.bak* 2>/dev/null | head -1)
if [[ -z "\\$LATEST_BAK" ]]; then echo "  无备份可回滚"; exit 1; fi
echo "  回滚到: \\$LATEST_BAK"
cp "\\$LATEST_BAK" "llm-gateway-go.v${NEW_BUILD_SEQ:-UNKNOWN}.linux.amd64"
ln -sf "llm-gateway-go.v${NEW_BUILD_SEQ:-UNKNOWN}.linux.amd64" llm-gateway-go
systemctl restart llm-gateway-go.service
sleep 5
echo "  当前版本: \$(cat $bin_dir/VERSION)"
EOF
}

# ── 入口 ────────────────────────────────────────────────────────────

main() {
  case "$TARGET" in
    184|252|kaixuan-1|kaixuan-2|kaixuan-3)
      # K3s 模式（185 实际是 252，kaixuan-* 是 k3s worker）
      pre_check || die "预检失败" 1
      get_version
      if [[ "$TARGET" == "184" ]]; then
        deploy_184  # legacy 别名，使用旧 K8s 函数
      elif [[ "$TARGET" == "kaixuan-1" ]]; then
        deploy_k3s_kaixuan1 || exit $?
      else
        err "TARGET=$TARGET (k3s-mode) 尚未实现 deploy_k3s；请用 ./scripts/deploy.sh kaixuan-1 或 252"
        exit 64
      fi
      commit_build_seq
      ;;
    71|154|186|245)
      # Host-mode（systemd）
      pre_check || die "预检失败" 1
      get_version
      if [[ "$TARGET" == "71" ]]; then
        deploy_71  # legacy 别名（71 退役但保留兼容）
      else
        deploy_host "$TARGET"
      fi
      commit_build_seq
      ;;
    both)
      pre_check || die "预检失败" 1
      get_version
      # both 模式：先 252 (k3s) 后 154 (host)
      err "both 模式（252+154）尚未实现 deploy_k3s；请分别运行 deploy.sh 252 和 deploy.sh 154"
      exit 64
      ;;
    build)
      pre_check || die "预检失败" 1
      get_version
      build_image
      info "构建完成 (镜像: ${IMAGE_NAME}:${IMAGE_TAG})"
      [[ "$DRY_RUN" != "true" ]] && commit_build_seq
      ;;
    migrate)
      [[ -z "${2:-}" ]] && die "用法: $0 migrate <184|71|252|154|kaixuan-1>" 64
      if [[ "$2" == "184" ]]; then run_migrations_184
      else err "migrate for $2 尚未实现"; exit 64; fi
      ;;
    verify)
      [[ -z "${2:-}" ]] && die "用法: $0 verify <184|71|154|186|245|kaixuan-1>" 64
      get_version
      if [[ "$2" == "184" ]]; then verify_184
      elif [[ "$2" == "71"  ]]; then verify_71
      elif [[ "$2" == "154" || "$2" == "186" || "$2" == "245" ]]; then verify_host "$2"
      else err "verify for $2 尚未实现"; exit 64; fi
      ;;
    rollback)
      [[ -z "${2:-}" ]] && die "用法: $0 rollback <184|71|154|186|245|kaixuan-1>" 64
      if [[ "$2" == "184" ]]; then rollback_184
      elif [[ "$2" == "71"  ]]; then rollback_71
      elif [[ "$2" == "154" || "$2" == "186" || "$2" == "245" ]]; then rollback_host "$2"
      else err "rollback for $2 尚未实现"; exit 64; fi
      ;;
    *)
      usage_short; exit 64
      ;;
  esac
}

main "$@"
