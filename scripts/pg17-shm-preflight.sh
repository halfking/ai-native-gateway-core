#!/usr/bin/env bash
# =====================================================================
# scripts/pg17-shm-preflight.sh
#
# ★★★ N11（211 的 PG 容器 `/dev/shm` 只有 64MB）的【只读现况核查器】
#
# # 它解决什么
#
# N11 的处置方案本身早已具体化（docs/operations/2026-11-23-211配置优化执行报告.md
# §9.19·附4）：在 `/opt/kaixuan/stacks/platform/docker-compose.platform.yml`
# 的 `acc_pg17` 服务段加一行 `shm_size: "1gb"`，然后
# `docker compose … up -d --force-recreate acc_pg17`。
#
# ★ 但那份读数是**某一时刻**的 ⇒ ★ 而 owner 要拍板的前提是「**现在**是什么状况」
# ⇒ ★ 所以本脚本把「现况 + 爆炸半径」变成**一条可复跑的只读命令**。
#
# ★★★ 本脚本【严格只读】：
#   · 只做 docker inspect / ps / df / ipcs / grep / awk（读 compose 文本）；
#   · ★ 绝不执行 `--force-recreate`、绝不 `docker compose up`、绝不改任何文件；
#   · ★ 不回显 compose 里的密钥（★ 只提取与 shm 相关的行 ⇒ 不含密钥值）。
#
# # 用法
#
#   bash scripts/pg17-shm-preflight.sh              # 默认走 ssh alias `211`
#   bash scripts/pg17-shm-preflight.sh --host 211   # 显式指定 ssh 目标
#   bash scripts/pg17-shm-preflight.sh --container acc-pg17
#   bash scripts/pg17-shm-preflight.sh --compose /opt/kaixuan/stacks/platform/docker-compose.platform.yml
#
# # 退出码
#
#   0 = 核查完成（★ **不代表**「可以安全 recreate」⇒ 那仍需 owner 拍板）
#   2 = 远端不可达 / 容器不在场（★ ⇒ 无法核查）
# =====================================================================
set -uo pipefail

SSH_HOST="${SSH_HOST:-211}"
CONTAINER="${CONTAINER:-acc-pg17}"
COMPOSE_PATH="${COMPOSE_PATH:-/opt/kaixuan/stacks/platform/docker-compose.platform.yml}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)      SSH_HOST="$2"; shift 2 ;;
    --container) CONTAINER="$2"; shift 2 ;;
    --compose)   COMPOSE_PATH="$2"; shift 2 ;;
    -h|--help)   sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
done

log()  { printf '[pg17-preflight] %s\n' "$*"; }
bad()  { printf '[pg17-preflight] ✗ %s\n' "$*" >&2; }

# ── 远端可达性 ─────────────────────────────────────────────────────
if ! ssh -o ConnectTimeout=8 -o BatchMode=yes "$SSH_HOST" true 2>/dev/null; then
  bad "ssh ${SSH_HOST} 不可达（★ 内网不可用时可试 --via-mesh）"
  exit 2
fi
log "远端可达：${SSH_HOST}"

# ── 远端只读采集（★ 单次 ssh 批处理 ⇒ 一次往返）────────────────────
# ★ 这里刻意【不】回显 compose 全文（★ 可能含密钥）⇒ 只做定向提取。
REMOTE=$(ssh -o ConnectTimeout=8 -o BatchMode=yes "$SSH_HOST" bash -s -- \
  "$CONTAINER" "$COMPOSE_PATH" <<'REMOTE_EOF' 2>&1
set -uo pipefail
C="$1"; CP="$2"

echo "### SECTION container"
if ! docker ps --filter "name=^${C}$" --format '{{.Names}}|{{.Status}}|{{.Image}}' | grep -q .; then
  echo "ABSENT"
  exit 0
fi
docker inspect "$C" --format 'name={{.Name}}
image={{.Config.Image}}
started={{.State.StartedAt}}
compose_project={{index .Config.Labels "com.docker.compose.project"}}
compose_service={{index .Config.Labels "com.docker.compose.service"}}
compose_config_files={{index .Config.Labels "com.docker.compose.project.config_files"}}
compose_workdir={{index .Config.Labels "com.docker.compose.project.working_dir"}}
shm_size={{.HostConfig.ShmSize}}
restart_policy={{.HostConfig.RestartPolicy.Name}}
network_mode={{.HostConfig.NetworkMode}}'

echo "### SECTION shm"
docker exec "$C" df -h /dev/shm 2>/dev/null | tail -2
echo "dsm_segments=$(docker exec "$C" sh -c 'ipcs -m 2>/dev/null | tail -n +4 | wc -l' 2>/dev/null)"
echo "pg_processes=$(docker exec "$C" sh -c 'ps -e 2>/dev/null | grep -c postgres' 2>/dev/null)"

echo "### SECTION networks"
docker inspect "$C" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'

echo "### SECTION network_members"
# ★★ 爆炸半径的**真口径**：★ 不是 compose 里的 depends_on（★ 那是编排依赖），
#   ★ 而是「**哪些容器连在同一个网络上**」⇒ ★ recreate 掉 PG ⇒ 这些容器
#   ★ 在窗口内会连不上它（★ 而它们自己不会重建、★ 不会自愈）。
for n in $(docker inspect "$C" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'); do
  members=$(docker network inspect "$n" --format '{{range $id,$c := .Containers}}{{$c.Name}} {{end}}' 2>/dev/null)
  echo "  net=${n} members=${members}"
done

echo "### SECTION ports"
docker inspect "$C" --format '{{range $p, $conf := .NetworkSettings.Ports}}{{range $conf}}{{$p}} -> {{.HostIp}}:{{.HostPort}}{{"\n"}}{{end}}{{end}}'

echo "### SECTION mounts"
docker inspect "$C" --format '{{range .Mounts}}{{.Type}}|{{.Source}}|{{.Destination}}|rw={{.RW}}{{"\n"}}{{end}}'

echo "### SECTION compose_shm"
if [ -f "$CP" ]; then
  echo "compose_exists=yes"
  echo "compose_in_git=$(cd "$(dirname "$CP")" 2>/dev/null && git rev-parse --is-inside-work-tree 2>/dev/null || echo no)"
  # ★ 只提取 acc_pg17 服务段里与 shm 相关的行（★ 定向、不回显全文）。
  awk -v svc="acc_pg17" '
    /^[[:space:]]{2}[A-Za-z0-9_.-]+:[[:space:]]*$/ {
      cur=$1; sub(/:$/,"",cur); next
    }
    cur==svc && /shm_size|tmpfs|shm-/ { print "  " $0 }
  ' "$CP"
  echo "compose_shm_size_present=$(awk -v svc="acc_pg17" '
    /^[[:space:]]{2}[A-Za-z0-9_.-]+:[[:space:]]*$/ { cur=$1; sub(/:$/,"",cur); next }
    cur==svc && /shm_size/ { found=1 }
    END { print (found?"yes":"no") }' "$CP")"
else
  echo "compose_exists=no"
fi
REMOTE_EOF
)

if ! printf '%s' "$REMOTE" | grep -q '### SECTION container'; then
  bad "远端采集失败（★ 输出片段）："
  printf '%s\n' "$REMOTE" | head -20 >&2
  exit 2
fi

if printf '%s' "$REMOTE" | grep -q '^ABSENT$'; then
  bad "容器 ${CONTAINER} 不在 211 上（★ 已停止？已改名？）"
  exit 2
fi

echo "=== N11 只读现况核查（★ 严格只读）=============================="
printf '%s\n' "$REMOTE"
echo "==============================================================="
echo
echo "★ 结论口径（★ 本脚本不替 owner 做决定）："
echo "  ① 若上面 shm_size=67108864 ⇒ 仍是 Docker 默认 64MB ⇒ 补丁仍未生效；"
echo "  ② 若 compose_shm_size_present=no ⇒ compose 里确实没写该键 ⇒ 加一行即可；"
echo "  ③ networks/ports 段列出的是 recreate 的爆炸半径 ⇒ 窗口须与这些使用者协调；"
echo "  ④ mounts 段里 PGDATA 是 bind mount ⇒ 重建**不动数据**（只换容器壳）；"
echo "  ⑤ ★ 本脚本**不执行** recreate ⇒ 动手前仍需 owner 一句授权。"
