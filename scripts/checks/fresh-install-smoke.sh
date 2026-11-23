#!/usr/bin/env bash
# =====================================================================
# scripts/checks/fresh-install-smoke.sh —— 「空库新装」烟测门
#
# ★★ 2026-11-23 L29 轮新增（★ ★ ★ ★ 首版出过自伤事故，★ 教训写在本文件最上面，
# ★ ★ ★ ★ **先读这段再改这个文件** ★ ★ ★ ★）。
#
# ★★★ 首版自伤事故（★ 本机实测，★ 零持久损害、★ 但必须记着）★★★：
#   ★ 首版**没有**给 e2e-bootstrap 传容器名 ⇒ ★★ 它用了工具的**默认容器名**
#      `llm-gateway-pg` ⇒ ★★ 而本机恰好有一个**在役的、多项目共用的开发库容器**
#      叫这个名字 ⇒ ★★ **⇒ 首版这道门把自己的安装跑到了那台在役开发库上。**
#   ★ 后果评估（★ 逐条查过）：★ 触发的迁移是 `388`（★ **幂等无操作**：★ 它的
#      `ADD COLUMN IF NOT EXISTS` 被守卫跳过 ⇒ ★ **没有加列**）与 `392`
#      （★ **单事务**执行 ⇒ ★ 失败即**整体回滚**）⇒ ★★ **⇒ 开发库零持久变更**
#      （★ 实测：★ 当天记账行 0 条、★ 表结构与列序逐列比对一致）★★
#   ★★ **⇒ 教训（★ 写死在这道门的第一条判据里）**：
#      ★★ **「门脚本要真跑」的第一个代价就是它会真的写库 ⇒ ⇒ 门脚本必须自带隔离：
#      ★★ ① 只打自己起的容器（★ 显式传容器名，★ 绝不吃工具的默认值）；
#      ★★ ② 装之前断言目标库**确实是空的**（★ 非空 ⇒ 拒绝执行）；
#      ★★ ③ 断言目标容器确实是**自己刚起的那个**（★ 而不是碰巧同名的在役容器）。**
#
# ★★ 这道门为什么存在（一句话）：
#   ★★ **「迁移在生产 apply 成功」不等于「迁移集能装进空库」。**
#   ★ 2026-11-23 实测（真库空库、★ 安装器真实路径）：
#     · 第 1 轮止步于 `956`（★ 数据依赖守卫）
#     · 旁路 956 后止步于 `981`（★ 迁移集内部顺序倒置）
#     · ★★ 而**生产好好的**（★ 211 上 990 记账 11-19 23:21、★ 981 落地时列已在）
#     · ⇒ ★★ 这两个阻断点**在生产上永远不会暴露** ⇒ ⇒ 仓里必须有一道
#       **自己会跑空库安装**的门。
#
# ★★ 「空库」与「老库」是两个世界（★ 本轮撞出的第二个通用式）：
#   ★ `392` 的视图是 `SELECT * FROM hot UNION ALL SELECT * FROM 父表` ⇒ ★ **逐位置比类型**
#   ⇒ ★ 本机开发库上两张表**列数相同但列序不同**（★ 第 19–21 列错位）⇒ ★ 该迁移在
#   ★ **老库上恒失败**；★ 而空库上两表同序 ⇒ ★★ **⇒ 「空库门」抓不到「老库列漂移」
#   ★ 这一类** ⇒ ⇒ ⇒ **⇒ 一道门只能覆盖一个世界。**
#
# ★ 三态判据（★ 「跑不了」与「跑红了」必须分得开）：
#   · 环境跑不了（★ 无 docker 守护进程、★ 镜像拉不到）⇒ **明确 SKIP 并打印原因**
#     （★ 仓内先例：`guards-deploy`「缺件时明确 SKIP，★ 不伪装成通过」）
#   · 装失败 / 断言不满足 ⇒ **FAIL（退出码 1）**
#   · 全过 ⇒ PASS（退出码 0）
#
# ★ 调用方（★ 纪律：「门没有调用方 = 声明不是门」）：
#   `make guards-fresh-install`（★ 进 `make guards` 慢门族，★ 不进快门族
#   ⇒ ★ codeup 载体的 4 核预算不受影响）
#
# ★ 运行时依赖：★ docker 守护进程 ＋ 一个可用的 PG 镜像 ＋ bash 3.2（★ macOS 自带）
# =====================================================================
set -euo pipefail

# ★★ 2026-11-23：★ 首版用 `set -uo pipefail`（★ 不含 -e）⇒ ★★ bash 的「未绑定变量」
# ★★ 只在启用 -u 时**报错并继续**，★ 若错误发生在管道右侧 ⇒ ★★ **退出码会被管道吃掉**
# ★★ ⇒ ★★ 一道门最坏的情形就是「失败了却看起来通过」⇒ ⇒ 这里改成 -e ＋ ERR trap：
# ★★ **任何意外错误 ⇒ 立刻带定位信息判红（exit 1）**，★ 绝不静默 0。
trap 'printf "FAIL: 脚本在第 %s 行异常退出（★ 这是门自身的缺陷，★ 不是判据红）\n" "$LINENO" >&2; exit 1' ERR

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1

# ── ★★ Go 构建缓存隔离（★ 仓内硬约定：scripts/ursmcheck/gocache_script_family_guard_test.go
#   ★★ 「在作用域内的编译型脚本必须把 GOCACHE 指向【专用目录】」★ ★ ★ 本脚本跑 `go run`
#   ★★ ⇒ ⇒ 它**就是**编译型脚本 ⇒ ⇒ 不隔离就会让那道门当场点名 ★ ★
#   ★★ 走仓内共享入口 `scripts/lib/gocache.sh` 的 `gocache_use`（★ 与仓内同族脚本同款写法）。
__gocache_dir="$REPO_ROOT"
if [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  while [ -n "$__gocache_dir" ] && [ "$__gocache_dir" != "/" ]; do
    __gocache_dir="$(dirname "$__gocache_dir")"
    if [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
      break
    fi
  done
fi
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  # shellcheck source=/dev/null
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
else
  printf 'FAIL: 找不到 scripts/lib/gocache.sh ⇒ ★ 无法隔离 Go 构建缓存 ⇒ ★ 拒绝执行\n' >&2
  exit 1
fi

# ── 可调参数（★ 环境变量优先）─────────────────────────────────────────
CONTAINER_NAME="${FRESH_INSTALL_CONTAINER:-l30-fresh-install-smoke-$$}"
PG_USER="${FRESH_INSTALL_PG_USER:-llm_gateway}"
PG_DB="${FRESH_INSTALL_PG_DB:-llm_gateway}"
PG_IMAGE="${FRESH_INSTALL_PG_IMAGE:-}"
LOCAL_IMAGE="kx-citus-pg17:offline-arm64"
REMOTE_IMAGE="registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-amd64"

SKIP=0
FAIL=0
note() { printf '%s\n' "$*"; }
skip() { printf 'SKIP: %s\n' "$*"; SKIP=1; }
bad()  { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL+1)); }

# ★★ ★★ 清理动作**同样**要带隔离守卫（★ ★ 首版自伤事故的**第二半**）：
# ★★ 首版的 trap 按**名字**删容器 ⇒ ★★ 于是「我们拒绝执行」的路径上，
# ★★ **trap 仍然把别人的容器删了**（★ 本机实测：★ ★ 在役的 llm-gateway-pg
# ★★ 被误删）⇒ ★★ ⇒ ★★ **⇒ 只堵启动路径不够，★ 每一个碰外部世界的动作
# ★★ （起容器 / 删容器 / 写库）都要带「只碰我自己的」守卫。**
# ★★ ⇒ 这里改成：**只按【我们自己的容器 ID】删**（★ 名字被别人抢走也不会误删）。
WE_CREATED_ID=""
cleanup() {
  if [ "${KEEP_CONTAINER:-0}" = "1" ]; then
    note "  （KEEP_CONTAINER=1 ⇒ 保留容器 ${CONTAINER_NAME} 供排查）"
    return
  fi
  if [ -n "$WE_CREATED_ID" ]; then
    docker rm -f "$WE_CREATED_ID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# ── 前置 1：docker 守护进程（★ 没有就 SKIP，★ 不伪装成通过）────────────
if ! command -v docker >/dev/null 2>&1; then
  skip "本机没有 docker 命令 ⇒ 这道门在本环境跑不了（★ 不是通过）"
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  skip "docker 守护进程不可用 ⇒ 这道门在本环境跑不了（★ 不是通过）"
  exit 0
fi

# ── 前置 2：选一个可用镜像（★ 本地有就用本地，★ 否则试远端）────────────
if [ -z "$PG_IMAGE" ]; then
  if docker image inspect "$LOCAL_IMAGE" >/dev/null 2>&1; then
    PG_IMAGE="$LOCAL_IMAGE"
  elif docker image inspect "$REMOTE_IMAGE" >/dev/null 2>&1; then
    PG_IMAGE="$REMOTE_IMAGE"
  else
    note "本地没有可用镜像 ⇒ 尝试拉取 $REMOTE_IMAGE …"
    if docker pull "$REMOTE_IMAGE" >/dev/null 2>&1; then
      PG_IMAGE="$REMOTE_IMAGE"
    else
      skip "既没有本地镜像、★ 也拉不到 ${REMOTE_IMAGE}（★ 需要 registry 凭据）⇒ 跑不了"
      exit 0
    fi
  fi
fi
note "镜像：$PG_IMAGE"

# ── 前置 3：起一次性容器（★ 口令随机生成、★ 不落盘、★ 不回显）────────
POSTGRES_PASSWORD="fresh$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
# ★★★ ★★ 隔离断言 ③（★ 首版自伤事故的**直接**补丁）：★★
# ★★ 首版在这里写了 `docker rm -f "$CONTAINER_NAME"` ⇒ ★★ **当容器名撞车时，
# ★★ 它会把别人的容器删掉**（★ 本机实测：★ 首版因此删掉了在役的
# ★★ `llm-gateway-pg` 开发库容器 ⇒ ★★ 数据在宿主机目录里没丢、★ 但容器没了）
# ★★ ⇒ ⇒ **这里改成：撞车就拒绝执行，★ 一个字都不删。** ★★
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  bad "容器名 ${CONTAINER_NAME} 已被占用 ⇒ ★★ 拒绝执行（★ ★ 绝不删除既有容器：★ ★ 首版自伤事故的直接补丁）"
  exit 1
fi
if ! docker run -d --name "$CONTAINER_NAME" --shm-size=1g \
      -e POSTGRES_USER="$PG_USER" \
      -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
      -e POSTGRES_DB="$PG_DB" \
      "$PG_IMAGE" >/dev/null 2>&1; then
  bad "起一次性 PG 容器失败（★ 镜像 ${PG_IMAGE}）"
  exit 1
fi
# ★ 记下**我们自己的**容器 ID（★ 清理只按这个 ID 删，★ 不按名字）
WE_CREATED_ID="$(docker inspect -f '{{.Id}}' "$CONTAINER_NAME" 2>/dev/null || true)"
note "一次性容器：${CONTAINER_NAME}（★ ★ 本门只打它，★ 绝不打任何别的容器）"

ready=0
for i in $(seq 1 90); do
  if docker exec "$CONTAINER_NAME" pg_isready -U "$PG_USER" >/dev/null 2>&1; then
    note "postgres ready after ${i}s"
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  bad "postgres 90s 内没就绪"
  docker logs "$CONTAINER_NAME" 2>&1 | tail -20 >&2 || true
  exit 1
fi

# ── ★★ 隔离断言 ②：★ 目标库必须**确实是空的**（★ 非空 ⇒ 拒绝执行）★★
# ★ 这条是首版自伤事故的直接补丁：★ 「门只打自己起的容器」还不够
# —— ★★ 万一容器名被环境变量指到了别处、★ 那个库又有数据，★ 就必须在这里挡住。
ntables=$(docker exec "$CONTAINER_NAME" psql -U "$PG_USER" -d "$PG_DB" -Atc \
  "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null)
if [ "${ntables:-0}" != "0" ]; then
  bad "目标库 $PG_DB 在容器 $CONTAINER_NAME 里**不是空的**（public 表数=${ntables}）⇒ ★ 拒绝执行"
  exit 1
fi
note "  ✔ 目标库确认为空（public 表数 0）"

# ── 主体：★ 用安装器真实路径装一个空库（★ 与生产安装器同一段代码）──────
# ★★★ ★★ 隔离断言 ①：★★ **必须显式传容器名** ⇒ ★★ 绝不吃 e2e-bootstrap 的
# ★★ 默认容器名 `llm-gateway-pg`（★ ★ ★ 本机有一个同名的在役开发库容器！★ ★ ★）
note "★ 跑空库新装（cmd/e2e-bootstrap ⇒ 与生产安装器同一段 InitSchema）…"
INSTALL_LOG="$(mktemp -t fresh-install)"
if ! ( cd installer && E2E_PG_CONTAINER="$CONTAINER_NAME" \
        go run ./cmd/e2e-bootstrap "$PG_DB" cmd/llm-gw-installer/embeddata ) >"$INSTALL_LOG" 2>&1; then
  note "---- 安装输出（尾部 25 行）----"
  tail -25 "$INSTALL_LOG"
  note "---- 安装输出（尾部 25 行）----"
  bad "空库新装失败（★ ★ 这就是「新装会炸」——★ 具体是哪一条迁移炸的，★ 看上面）"
  rm -f "$INSTALL_LOG"
  exit 1
fi
tail -3 "$INSTALL_LOG"
rm -f "$INSTALL_LOG"

# ── 断言：★ 不只看「没报错」，★ 要看「装出来的东西对不对」────────────
q() { docker exec "$CONTAINER_NAME" psql -U "$PG_USER" -d "$PG_DB" -Atc "$1" 2>/dev/null; }

views=$(q "select count(*) from pg_matviews where matviewname like '%aggregates%'")
if [ "$views" != "4" ]; then
  bad "会话分析物化视图数＝${views:-<空>}，★ 期望 4（★ ⇒ 981 的产物没到位）"
else
  note "  ✔ 会话分析物化视图 4 个"
fi

col=$(q "select count(*) from information_schema.columns where table_name='session_summaries' and column_name='total_credits_charged'")
if [ "$col" != "1" ]; then
  bad "session_summaries.total_credits_charged 列数＝${col:-<空>}，★ 期望 1（★ ⇒ 990 的产物没到位）"
else
  note "  ✔ session_summaries.total_credits_charged 在"
fi

seed=$(q "select count(*) from models_canonical where canonical_name='minimax-m3.1-flash' and status='active' and featured is true")
if [ "$seed" != "1" ]; then
  bad "models_canonical 里的 minimax-m3.1-flash（active + featured）行数＝${seed:-<空>}，★ 期望 1（★ ⇒ 956 的守卫/标记没到位）"
else
  note "  ✔ minimax-m3.1-flash 在位且 featured=TRUE"
fi

tables=$(q "select count(*) from information_schema.tables where table_schema='public'")
note "  ℹ public 表数：${tables:-<空>}"

if [ "$FAIL" -ne 0 ]; then
  printf 'fresh-install-smoke: FAIL（%d 条判据不满足）\n' "$FAIL" >&2
  exit 1
fi
if [ "$SKIP" -ne 0 ]; then
  printf 'fresh-install-smoke: SKIP\n'
  exit 0
fi
note "fresh-install-smoke: PASS"
exit 0
