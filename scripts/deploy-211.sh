#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-211.sh — 211 Docker 形态蓝绿部署的**薄外壳**
# =====================================================================
# 与 deploy-154.sh / deploy-245.sh 同一族入口标准：本文件**只**做「目标机
# 语境」的包装——把 211 这台主机的信息用环境变量注入，然后 exec 通用引擎
# scripts/deploy-seamless-docker.sh。部署逻辑（蓝绿状态机、切流、验收、
# 回退）全部在引擎里，引擎本身**不含任何具体主机名**。
#
# ⇒ 新增一台 Docker 形态的主机 = 复制本文件改主机信息 + 改文件名，
#   **不需要复制引擎**（复制引擎 = 两份蓝绿语义各自演化，必漂）。
#
# ── 211 的主机信息（本文件存在的唯一理由）────────────────────────────────
#   目标机        192.168.31.211（ssh 别名 211）
#   状态目录      /opt/llm-gateway-go-211   ← 含 env/gateway.secret，
#                   **路径不可改**：SECRET_KEY 与库内 api_keys.key_hash 的
#                   HMAC 绑定，换路径 ⇒ 换密钥 ⇒ 全部 key 失效。
#   远端仓库根    /root/llm-gateway-go-211
#   蓝绿槽位      a=8781（当前活跃） / b=8783（候选）
#                 ⚠ **b 不是 8782**：同机另一个产品 `ai-session-manager-asm-1`
#                 （docker compose，2026-10-21 17:0x 起）已占用 127.0.0.1:8782
#                 ⇒ ⇒ 候选槽 b 在 8782 上 `docker run` 必失败：
#                 `Bind for 127.0.0.1:8782 failed: port is already allocated`
#                 ⇒ ⇒ 端口被**别的产品**占住 ⇒ **不是僵尸 proxy、不是本仓的问题**
#                 ⇒ ⇒ 处置 = 换一个空闲端口（8783 实测空闲：211 上 8783-8787 无监听）
#                 ⇒ ⇒ ★ **槽位端口必须是静态的**：引擎用 nginx upstream 的端口
#                 反推「谁是活跃槽」（`dd_active_slot` 把端口与 a/b 槽位配对），
#                 ⇒ ⇒ 端口一动态，活跃槽判定就失效 ⇒ ⇒ 不能做「自动挑空闲端口」。
#   容器名        llm-gateway-211-<slot>
#   nginx         /etc/nginx/conf.d/ns-gateway-211.conf
#                 upstream ns_llm_gateway_upstream（主 server 行 = 活跃槽端口）
#
# 用法:
#   bash scripts/deploy-211.sh                 # 部署最新代码到 211（蓝绿）
#   bash scripts/deploy-211.sh --status        # 只看探测与槽位现状，不改动
#   bash scripts/deploy-211.sh --stop          # 停掉两个槽（容器保留可 start）
#   bash scripts/deploy-211.sh --redis-db 9    # 引擎参数原样转发
#   bash scripts/deploy-211.sh --dry-run       # 打印目标合同 JSON
#   bash scripts/deploy-211.sh --force         # 211 部署无部署锁：接受但无操作
#
# 前端: **不需要手工预构建**。引擎在**部署机**侧（--via 分支）先跑
#   scripts/lib/node-pm.sh 的包管理器决策 → 必要时按 lockfile 装依赖 →
#   pnpm build web / pnpm build web-mobile → 再判产物新鲜度 → 随 tar 上树
#   → 种子进**候选槽专属**目录 → 绑定挂载到容器 /app/web/dist 与
#   /app/web-mobile/dist。历史口径是「运维先手工 build，脚本只校验」，
#   漏了就整次部署被「web/dist 陈旧」拦死（2026-10-22 实测），更坏的是
#   带逃生舱硬闯 ⇒ 线上是旧 SPA 而部署显示成功。
#   逃生舱: DD_SKIP_WEB_BUILD=1 跳过构建（新鲜度门仍照常判定）；
#           DD_ALLOW_STALE_WEB=1 放行陈旧/缺失（纯 API 形态，站点根路径
#           会退化成一段版本 JSON —— 不建议）。
#
# 基础镜像默认（可覆盖）:
#   DD_BASE_REGISTRY=registry-211.itestu.cn
#   DD_GO_BASE_IMAGE=$DD_BASE_REGISTRY/lang-base/kx-base-golang:1.27-alpine-amd64
#   DD_RUNTIME_BASE_IMAGE=$DD_BASE_REGISTRY/lang-base/alpine:3.22-amd64
#
# 等价于:
#   bash scripts/deploy-seamless-docker.sh --via 192.168.31.211 [args...]
# =====================================================================
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 蓝绿两槽的固定发布端口：**必须在这里定义**（而不是等到下面的「主机信息」块）——
# 因为 `--dry-run` 的合同 JSON 在参数分派段就要打印这两个端口
# ⇒ ⇒ 定义晚了，dry-run 会报旧端口 ⇒ ⇒ 合同与实际不一致。
# ⚠ b 槽 = 8783 而**不是 8782**：8782 已被同机另一个产品
#   （ai-session-manager-asm-1，docker compose）占用 ⇒ 候选槽在 8782 上起不来
#   （`Bind for 127.0.0.1:8782 failed: port is already allocated`）。
#   ★ 端口必须静态：引擎靠 nginx upstream 的端口反推活跃槽（dd_active_slot），
#   动态端口会让「谁是活跃槽」判定失效 ⇒ 切流方向可能反了。
export DD_SLOT_A_PORT="${DD_SLOT_A_PORT:-8781}"
export DD_SLOT_B_PORT="${DD_SLOT_B_PORT:-8783}"

# Password authentication is never used by the canonical deploy path. Clear a
# stale inherited value so unrelated shell configuration cannot block a key-only
# deployment or leak into child processes.
unset SSHPASS

# scripts/deploy-lib is a repo-relative symlink that dangles in temporary
# worktrees (/private/tmp/lgw-merge-*); resolve the SSOT robustly instead of
# sourcing through it (env preset → link target → $HOME default, same order
# as _shared-lib.sh; 2026-10-07 audit round F2).
# shellcheck source=deploy-lib-resolve.sh
source "$SCRIPT_DIR/deploy-lib-resolve.sh"
# shellcheck source=deploy-lib/parse-wrapper-flags.sh
source "$AIAN_DEPLOY_LIB/parse-wrapper-flags.sh"

# Strip operator-level recovery flags before delegating. The seamless
# orchestrator owns recovery of all lock layers and then reacquires them.
FORCE_UNLOCK=0
ARGS=()
extract_force_unlock FORCE_UNLOCK ARGS "$@"

# bash 3.2 + set -u: empty-array "${ARGS[@]}" / "${ARGS[*]}" expansions die as
# unbound — guard by count (no-arg `bash scripts/deploy-211.sh` path).
if (( ${#ARGS[@]} > 0 )); then
  for arg in "${ARGS[@]}"; do
    [[ "$arg" == --help || "$arg" == -h ]] && { sed -n '2,40p' "$0"; exit 0; }
  done
  if [[ " ${ARGS[*]} " == *' --dry-run '* ]]; then
    # 211 是 Docker 形态的**蓝绿**部署：两个槽各绑一个固定端口，活跃槽由
    # nginx upstream 实际指向的端口决定（事实优先于状态文件）。
    # ⚠ 端口从下面的 export 读，**不写死字面量** ⇒ 改了 DD_SLOT_*_PORT 时
    #   dry-run 的合同 JSON 不会继续报旧端口（否则合同与实际不一致）。
    printf '{"target":"211","slot_a_port":"%s","slot_b_port":"%s","active_slot":null,"candidate_slot":null}\n' \
      "${DD_SLOT_A_PORT:-8781}" "${DD_SLOT_B_PORT:-8783}"
    exit 0
  fi
fi

if [[ $FORCE_UNLOCK -eq 1 ]]; then
  # 家族口径里 --force = 恢复并重建部署锁；211 引擎无部署锁（每次部署整体
  # rm -rf 重建目标树），卡死场景直接重跑即可，故接受但无操作，明说不静默。
  echo "[deploy-211] 211 docker 部署无部署锁，--force 无需也无效（忽略）" >&2
fi

# 透传健康探针超时：与 deploy-154/245 同一族标准（PROBE_TIMEOUT_SECS）。
# 211 的就绪等待是引擎内 curl 轮询（容器首启 ensure 链 + 前端种子挂载，
# 默认 120s），调用方可覆盖，引擎按 2s 步进取半作为轮次上限。
export PROBE_TIMEOUT_SECS="${PROBE_TIMEOUT_SECS:-120}"

# =====================================================================
# 211 主机信息（引擎的全部主机相关输入都在这里）
# =====================================================================
export DD_TARGET="211"
# ssh 目标与探测 IP 分开：ssh 走别名（~/.ssh/config 里带 User/Port/密钥），
# 探测 IP 用于切流后的公网复验（--resolve 到 211 的 LAN 443 监听）。
export DD_SSH_TARGET="${DD_SSH_TARGET:-211}"
export DD_HOST="${DD_HOST:-192.168.31.211}"
export DD_STATE_DIR="${DD_STATE_DIR:-/opt/llm-gateway-go-211}"
export DD_REMOTE_ROOT="${DD_REMOTE_ROOT:-/root/llm-gateway-go-211}"
export DD_NETWORK="${DD_NETWORK:-llm-gateway-net}"
# ★ 蓝绿两槽的固定发布端口（槽位与端口绑定，活跃槽由 nginx upstream 判定）
#   **定义在文件头**（set -euo pipefail 之后）—— `--dry-run` 的合同 JSON 在
#   参数分派段就要打印它们 ⇒ 定义在这里会报旧端口 ⇒ 合同与实际不一致。
export DD_CONTAINER_PREFIX="${DD_CONTAINER_PREFIX:-llm-gateway-211}"
export DD_IMAGE="${DD_IMAGE:-llm-gateway-211}"
export DD_GW_DATA_ROOT="${DD_GW_DATA_ROOT:-/opt/kaixuan/data/llm-gateway}"
export DD_NGINX_CONF="${DD_NGINX_CONF:-/etc/nginx/conf.d/ns-gateway-211.conf}"
export DD_NGINX_UPSTREAM="${DD_NGINX_UPSTREAM:-ns_llm_gateway_upstream}"
export DD_DOMAIN="${DD_DOMAIN:-llm.itestu.cn}"
export DD_PG_DB="${DD_PG_DB:-llm_gateway}"
export DD_PG_USER="${DD_PG_USER:-llm_gateway}"
export DD_REDIS_DB_START="${DD_REDIS_DB_START:-15}"
export DD_REDIS_DB_END="${DD_REDIS_DB_END:-0}"

# 211 直连 docker.io 不通；基础镜像权威仓是 registry-211.itestu.cn（方案 42）。
# 入口显式钉默认值，经 --via 转发到远端，避免引擎静默回落 docker.io。
export DD_BASE_REGISTRY="${DD_BASE_REGISTRY:-registry-211.itestu.cn}"
export DD_GO_BASE_IMAGE="${DD_GO_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/kx-base-golang:1.27-alpine-amd64}"
export DD_RUNTIME_BASE_IMAGE="${DD_RUNTIME_BASE_IMAGE:-${DD_BASE_REGISTRY}/lang-base/alpine:3.22-amd64}"

# ★ 2026-10-23：--doctor = **只读体检**（不部署、不改配置、不 reload）。
#   回答「这台机器上的「流量 → 槽位 → 端口」绑定还是真的吗？」
#   ⇒ 211 的 nginx 把活跃槽端口**烤死**在 upstream 块里（154/245 是 include
#     生成文件 ⇒ 无此形态）⇒ 端口与槽位可以漂移 ⇒ 漂了就是 llm.itestu.cn 502。
#   ⇒ 体检脚本：scripts/ops/deploy-doctor.sh（纯只读：cat + docker ps + 读 conf）。
#   ⇒ 退出码：0 正常 / 2 脱节（需人工处置）/ 3 体检没做成（**不是**正常）。
# ⚠ 2026-10-26：`ARGS[*]` 在 no-arg 路径上是 unbound（bash 3.2 + set -u 的
#   既有坑，下方末段注释点名过同一陷阱但只守卫了最后一处）⇒ 本行同款
#   `${ARGS[*]+...}` 守卫补齐，否则 `deploy-211.sh`（无参 = 标准部署）在
#   macOS bash 3.2 上根本起不来。
if [[ " ${ARGS[*]+${ARGS[*]}} " == *' --doctor '* ]]; then
  exec bash "$SCRIPT_DIR/ops/deploy-doctor.sh" \
    --ssh "$DD_SSH_TARGET" \
    --nginx-conf "$DD_NGINX_CONF" \
    --upstream "$DD_NGINX_UPSTREAM" \
    --slot-a "$DD_SLOT_A_PORT" \
    --slot-b "$DD_SLOT_B_PORT" \
    --data-root "$DD_GW_DATA_ROOT"
fi

# =====================================================================
# ★ 部署前的**测试闸门**（2026-10-29 审计轮补）★★
#
# ★ 为什么必须有：★ 部署**只编译、不跑测试** ⇒ ★★ **红灯也能上线** ★★
#   （★ 实证：★ 2026-10-29 那一轮的容量契约测试一直是红的，★ 而它守的正是
#   「越界不再杀请求」这条误报修复的另一半 ⇒ ★★ 闸门 = 上线前把「改动触及
#   的包」的测试跑一遍 ★★）★★
# ★ 逃生舱：★ `DD_SKIP_TEST_GATE=1`（★ 显式给，★ 不允许默默跳过）★★
# ★ 闸门在**本机**跑（★ docker + vendor ⇒ 离线可用；★ 不依赖目标机环境）★★
bash "$SCRIPT_DIR/deploy-test-gate.sh"

# bash 3.2 + set -u: the `${ARGS[@]+${ARGS[@]}}` guard keeps the no-arg path
# (empty ARGS) from dying as an unbound variable, same reason as the guard above.
exec bash "$SCRIPT_DIR/deploy-seamless-docker.sh" --via "$DD_SSH_TARGET" "${ARGS[@]+"${ARGS[@]}"}"
