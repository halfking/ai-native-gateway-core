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
# ★★★ 2026-11-23 L33 三轮（L34 取证轮）扩门：★★ 这道门现在**四段**跑 ★★
#   （★ ① 空库新装 ② 装完再升 ③ **部署通道真跑** ④ 部署通道视角的跳过 ★★）
#
# ★★★ 2026-11-23 L33 轮扩门：★★ 这道门**第二阶段**跑「装完再升」
#   ★★★（★ 同一个库、★ 同一个容器、★ 第二次跑安装器 ⇒ 增量路径）：
#   ★ 实测：★ **同一个刚装成功的库，第二次升级必失败**（★ 旧代码）：
#     · 失败点①：★ 安装器专属资产 `session_turns_hot_bootstrap.sql` 恒执行
#       ⇒ 撞已被 890 下线的 `public.request_logs_hot`；
#     · 失败点②：★★ **台账盲区** ⇒ ★ 392 条 startup 迁移只有 112 条自登记
#       ⇒ ★★ 其余 280 条**从不记账** ⇒ ★★ **每次增量都被重放** ⇒ ★★ 而
#       ★★ 「重放安全」是**未经证明的假设**。
#   ★★ ⇒ ★★ 门必须同时覆盖「新装」与「再升」两段 ⇒ ★★ ⇒ ★★ 同一个门里两段
#   ★★ 判据（★ 第二段的四条判据写在下面那段注释里）。
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
# ★★ ★ 高负载下实测到的一个真弱点（★ 本轮补）：★★ 这一步**只跑一次** ⇒ ★ 机器
# ★★ 被压满时（★ 本机实测 load 600+）★ psql 可能**瞬时**失败（★ PG 尚在启动或
# ★ docker exec 打嗝）⇒ ★★ 而 `set -e` ＋ ERR trap 会把「探测失败」报成
# ★★ **「门自身的缺陷」**（★ 实测：门在第 195 行异常退出、★ 而门其实没坏）★★
# ★★ ⇒ ★★ **「探测失败」与「库非空」必须分开**：★ 前者重试几次、★ 后者直接拒。
ntables=""
attempt=1
while [ "$attempt" -le 6 ]; do
  if ntables=$(docker exec "$CONTAINER_NAME" psql -U "$PG_USER" -d "$PG_DB" -Atc \
        "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null); then
    break
  fi
  ntables=""
  attempt=$((attempt+1))
  sleep 2
done
if [ -z "$ntables" ]; then
  bad "探测目标库 $PG_DB 的表数失败（★ 连不上或 PG 未就绪、★ 重试 6 次仍失败）⇒ ★ 拒绝执行（★ 这不是判据红）"
  exit 1
fi
if [ "$ntables" != "0" ]; then
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

# ═══════════════════════════════════════════════════════════════════════
# ── 第二阶段：★ 「装完再升」（★ 增量路径）★ ────────────────────────────
# ★★★ 这道门现在覆盖**两个世界**：★★ 空库新装 ＋ ★★ **装完再升**。
#
# ★★★ 为什么必须有第二阶段（★ 2026-11-23 L33 轮实测）：★★
#   ★ **同一个刚装成功的库，第二次跑安装器必失败**（★ 本机真库复现 ×4）：
#     · 失败点①：★ `session_turns_hot_bootstrap.sql`（★ 安装器专属资产）
#       ⇒ ★ 旧代码对**无数字前缀**的文件一律「恒执行」⇒ ★ 它撞已被 890
#       下线的 `public.request_logs_hot` ⇒ ★★ 报错
#       `ERROR:  relation "public.request_logs_hot" does not exist`；
#     · 失败点②：★★ **台账盲区** ⇒ ★ 392 条 startup 迁移里**只有 112 条**
#       在文件尾部自登记 ⇒ ★★ 其余 280 条**从不记账** ⇒ ★★ 而增量通道按
#       「版本在台账里 ⇒ 跳过」判定 ⇒ ★★ **那 280 条每次增量都被重放**
#       ⇒ ★★ 而「重放安全」是**未经证明的假设**（★ 392 的 `SELECT * UNION ALL`
#       视图在列序漂移的库上炸 ⇒ 第三个发现）。
#   ★★ ⇒ ★★ 「空库装得出来」**不等于**「同一个库升得上去」⇒ ★★ 两段都要跑。
#
# ★ 判据（★ 全部对**真库**跑，★ 不是模拟）：
#   ① 第二次安装 **必须成功**（★ 退出码 0）；
#   ② 第二次安装 **不许重放任何带版本号的迁移**（★ 逐条解析安装日志）；
#   ③ 第二次安装**不许新增台账行**（★ ⇒ 印证②：★ 没有「应用了又补记」）；
#   ④ 第二次安装后 **产物仍在**（★ 4 视图 / 990 的列 / 956 的种子行）
#      ⇒ ★ 增量路径不许把已装好的库改坏。
# ═══════════════════════════════════════════════════════════════════════
note "★ 跑第二次安装（★ 增量路径：★ 同一个库、★ 同一个容器）…"
ledger_before=$(q "select count(*) from schema_migrations")
RUN2_LOG="$(mktemp -t fresh-install-run2)"
if ! ( cd installer && E2E_PG_CONTAINER="$CONTAINER_NAME" \
        go run ./cmd/e2e-bootstrap "$PG_DB" cmd/llm-gw-installer/embeddata ) >"$RUN2_LOG" 2>&1; then
  note "---- 第二次安装输出（尾部 25 行）----"
  tail -25 "$RUN2_LOG"
  note "---- 第二次安装输出（尾部 25 行）----"
  bad "★ 装完再升失败（★ ★ 「同一个刚装成功的库，第二次升级必失败」——★ 这正是老库升级通道）"
  rm -f "$RUN2_LOG"
  exit 1
fi
note "  ✔ 第二次安装成功（★ 增量路径）"

ledger_after=$(q "select count(*) from schema_migrations")
if [ "${ledger_before:-0}" != "${ledger_after:-0}" ]; then
  bad "第二次运行后台账行数从 ${ledger_before:-<空>} 变成 ${ledger_after:-<空>} ⇒ ★ 有迁移被重放（★ 增量通道应当**只补未登记的变更**）"
else
  note "  ✔ 台账行数未变（${ledger_after:-<空>}）⇒ 没有迁移被重放"
fi

# ★ 逐条解析第二次运行的日志：★ 「应用 startup migration <文件>」里
# ★ **形如 `数字_` 的**（★ 即有版本号、★ 参与台账判定的）⇒ 就是**被重放的
# ★ 带版本号迁移**（★ 应当为 0 条）。
# ★★ 口径说明（★ 写下来免得下一个人踩）：★ 这里判的是「**纯数字前缀 + 下划线**」
# ★★ ⇒ `361b_provider_models_source_prelude.sql`（★ 数字 + 字母）**不算**带版本号
# ★★ ⇒ ★ 它每次增量都照跑是**设计如此**（★ 无版本号 ⇒ 不进版本台账 ⇒ ★ 靠
# ★★ 幂等契约兜底，★ 且它的契约明确允许在已有部署上跑）。
# ★★★ 量具自身的坑（★ 本轮真踩了一次，★ 写在这里免得复发）：★★
# ★★★ `grep` **无匹配时退出码是 1** ⇒ ★ 在 `set -euo pipefail` 下 ⇒ ★
# ★★★ 「没有重放任何迁移」（★ 正是我们要的**好**结果）⇒ ★★ 反而让**整道门**
# ★★★ 在第 298 行异常退出（★ ERR trap 报「门自身的缺陷」）★★
# ★★★ ⇒ ★★ 所以这里的 `grep` 一律配 `|| true`（★ 让「无匹配」变成 0 行而不是失败）。
replayed=$( (grep -o '应用 startup migration [0-9][0-9]*_[^ ]*' "$RUN2_LOG" 2>/dev/null || true) | wc -l | tr -d ' ')
if [ "${replayed:-0}" != "0" ]; then
  bad "第二次运行重放了 ${replayed} 条带版本号的迁移（★ 增量通道的跳过判据失效）"
  (grep -o '应用 startup migration [0-9][0-9]*_[^ ]*' "$RUN2_LOG" || true) | head -5 | sed 's/^/      /'
else
  note "  ✔ 第二次运行没有重放任何带版本号的迁移"
fi
rm -f "$RUN2_LOG"

views2=$(q "select count(*) from pg_matviews where matviewname like '%aggregates%'")
if [ "$views2" != "4" ]; then
  bad "★ 第二次运行后会话分析物化视图数＝${views2:-<空>}，★ 期望 4（★ ⇒ 增量路径把已装好的库改坏了）"
else
  note "  ✔ 增量路径后 4 个会话分析物化视图仍在"
fi
col2=$(q "select count(*) from information_schema.columns where table_name='session_summaries' and column_name='total_credits_charged'")
if [ "$col2" != "1" ]; then
  bad "★ 第二次运行后 session_summaries.total_credits_charged 列数＝${col2:-<空>}，★ 期望 1（★ ⇒ 增量路径把已装好的库改坏了）"
else
  note "  ✔ 增量路径后 total_credits_charged 仍在"
fi

# ═══════════════════════════════════════════════════════════════════════
# ── 第三阶段：★ 部署通道**真跑**一次（★ L34 取证轮新增）────────────────
# ★★★ 这段把「部署通道不认安装器的账」从**一次性测量**变成**持续可观测的信号** ★★★
#
# ★ 背景（★ 报告 §7.31 的真库实测）：★ 「安装器刚装好的库 ⇒ 直接交给部署通道」这条路
#   ★★ 是**断的** ★★ ⇒ ★ 臂 D：★ 部署通道的账在新装库里**连表都不存在** ⇒ ★
#   ★ 它从零重放 ⇒ ★ **第 13 个文件就炸**（★ `563_session_summary_trigger_on_hot.sql`
#   ★ ⇒ 逐字 `ERROR:  relation "request_logs_hot" does not exist`）★★ ⇒ ★
#   ★ 臂 E（★ 喂上交集 330 条）⇒ ★ 跳过 53 条、★ 断点**前移**到「部署通道独有」的
#   ★ 第 6 条（★ `661_…` ⇒ `cannot change name of view column "id" to "source"`）★★
#   ★ ⇒ ★★ **⇒ 「让部署通道也认安装器的账」是必要条件、★ 不是充分条件** ★★
#
# ★ 本阶段断言三件事（★ **全部是可判定的事实**、★ 不是「跑过了就算过」 ★）：
#   ① 跑之前：★ 部署通道那本账 `gateway_db_revision_sequences` **不存在或为空**
#      ⇒ ★⇒ ★ **它对「安装器已经应用了什么」零知识**（★ 今天的实测口径 ★）；
#   ② 它**不得默默成功**（★ 成功＝「重放了却恰好没炸」⇒ ★ 危险、★ 而门会误判成通过 ★）；
#   ③ 失败模式必须是**已知的两种之一**（★ 台账盲区类 `request_logs_hot` ／
#      ★ 部署通道独有文件类 `view column`）⇒ ★ ★ **其它失败模式 ⇒ 报「未知失败模式」**
#      ⇒ ★ 因为那意味着**又冒出了第三道断点** ⇒ ★ 必须有人看 ★★。
#
# ★ ★★ 口径变化的处置（★ **刻意留红** ★★）：★ 若哪天方案 A′（★ 两本账互通 ＋
# ★ 那 26 条补齐 ★）落地、★ 部署通道在新装库上跑通了 ⇒ ★ ⇒ ★ **本段会报「口径变了」
# ★ 并红** ★★ ⇒ ★ **那不是本门坏了、★ 是缺口被修好了** ⇒ ★ **请连同本门与报告
# ★ §7.31 一起更新** ★★ ⇒ ★★ **⇒ 这道门的价值就是：★ 让「缺口还在」这件事
# ★ 每天都有人看见、★ 而不是一次测完就躺在报告里** ★★。
#
# ★ ★★ 判据写法纪律（★ 本轮刚踩过的坑、★ 写在这里免得复发 ★★）：★ **`|| true`
# ★ 绝不能放在 `if` 的条件位**（★ 那会让条件**恒为真** ⇒ ★ 假绿＋假红同时出现 ★★）
# ★ ⇒ ★ 下面的判据**一律用 grep 的真实退出码**（★ `if` 条件位上的非零退出不触发
# ★ `set -e`、★ 也不触发 ERR trap ★ ⇒ ★ 安全 ★★）。
# ═══════════════════════════════════════════════════════════════════════
note "★ 跑第三阶段（★ 部署通道真跑：★ 它在这台新装库上实际会做什么）…"
deploy_ledger_exists=$(q "select count(*) from pg_class where relname='gateway_db_revision_sequences'")
deploy_ledger_rows=0
if [ "${deploy_ledger_exists:-0}" != "0" ]; then
  deploy_ledger_rows=$(q "select count(*) from public.gateway_db_revision_sequences")
fi
if [ "${deploy_ledger_rows:-0}" = "0" ]; then
  note "  ✔ 跑之前部署通道那本账**不存在或为空** ⇒ ★ 它对「安装器已经应用了什么」零知识（★ 今天的实测口径）"
else
  # ★★ 判红（★ 刻意留红 ★★）：★ 「零知识」是本段其余两条断言的**前提** ⇒ ★
  # ★★ 前提不成立时、★ 本段就**测不到它想测的东西**了（★ ⇒ 门会静默退化成
  # ★★ 「跑过一次就算过」★★）⇒ ★★ 所以这里必须红、★ 而不是打条提示 ★★。
  # ★★ 而「账在跑之前就有行」只有两种来源：★ ① 有人给部署通道喂过账（★ 方案 B
  # ★★ ⇒ ★ L33 已论证它会制造新分叉 ★★）★ ② 口径变了 ⇒ ★★ 两种都**必须**有人复核 ★★。
  bad "★ 跑之前部署通道那本账已有 ${deploy_ledger_rows} 行 ⇒ ★ 「它对安装器零知识」这个前提**不成立** ⇒ ★ ★ 本段测不到它想测的东西了 ⇒ ★ ★ 口径变了（★ 请核对报告 §7.31 ＋ 刻意留红）★★"
fi
DEPLOY_RUN_LOG="$(mktemp -t fresh-install-deploy)"
DEPLOY_RC=0
( cd "$REPO_ROOT" && \
  LLM_GATEWAY_PSQL_TRANSPORT=container \
  LLM_GATEWAY_PG_CONTAINER="$CONTAINER_NAME" \
  DATABASE_URL="postgres://${PG_USER}:${POSTGRES_PASSWORD}@127.0.0.1:5432/${PG_DB}" \
  bash scripts/apply-db-revision-sequence.sh ) >"$DEPLOY_RUN_LOG" 2>&1 || DEPLOY_RC=$?
if [ "$DEPLOY_RC" -eq 0 ]; then
  bad "★ 部署通道在新装库上**成功了** ⇒ ★⇒ ★ 口径变了（★ 要么两本账已互通、★ 要么迁移已自足）⇒ ★ **请更新本门与报告 §7.31**（★ 刻意留红）★"
else
  note "  ℹ 部署通道退出码 = ${DEPLOY_RC}（★ 今天的实测：★ 它会中止）"
  if grep -q 'relation "request_logs_hot" does not exist' "$DEPLOY_RUN_LOG" || \
     grep -q 'cannot change name of view column' "$DEPLOY_RUN_LOG"; then
    note "  ✔ 失败模式是**已知的两种之一**（★ 台账盲区类 ／ 部署通道独有文件类）⇒ ★ 与报告 §7.31 的实测一致"
  else
    bad "★ 部署通道以**未知模式**失败（★ 退出码 ${DEPLOY_RC}、★ 但不是已知的两种失败模式）⇒ ★ ⇒ ★ **可能又冒出了第三道断点** ⇒ ★ 请人看："
    awk '/ERROR:/{print "      " $0; exit}' "$DEPLOY_RUN_LOG"
  fi
  awk '/ERROR:/{print "      " $0; exit}' "$DEPLOY_RUN_LOG"
fi
rm -f "$DEPLOY_RUN_LOG"
# ═══════════════════════════════════════════════════════════════════════
# ── 第四阶段：★ 「部署通道视角」的跳过（★ F3 的运行时覆盖）────────────
# ★★★ 本阶段补的是 L33 轮自己登记的缺口：★ 修法 F3（★ 安装器**只读**咨询部署
# ★★★ 通道的权威台账 `gateway_db_revision_sequences`）★★ 此前**只有单元测试**
# ★★★ 覆盖（★ 运行时只跑到「表不存在」那条分支）⇒ ★★ 本阶段给它真库运行时覆盖。
#
# ★ 做法（★ 三步，★ 让「只有 F3 能跳过它」成立）：
#   ① 造出权威台账表（★ DDL 与 scripts/apply-db-revision-sequence.sh **同款**）；
#   ② ★★ 先从 `schema_migrations` **删掉**目标文件的版本行 ⇒ ★★ 于是「版本规则」
#      不再跳过它 ⇒ ★★ **只剩 F3 能跳过它**；
#   ③ 把该文件的**内容 sha256** 写进权威台账（★ marker = 「<序列名>:<文件名>」）。
#   ⇒ ★ 再跑一次安装器（★ 增量）⇒ ★ 断言：★ **该文件没有被应用**（★ 日志里是
#      「跳过 …（部署通道已按同一内容指纹应用）」）。
#
# ★ 为什么这条文件选 `484_request_logs_hot_add_status_code.sql`：★ 它在**老库**上
# ★ 重放会炸（★ `relation "request_logs_hot" does not exist`——★ 那张表已被后续
# ★ 迁移下线）⇒ ★⇒ ★ **F3 一旦失效，第三次安装会当场失败**（★ 强判据）★★ ⇒ 而不是
# ★ 一个「看起来还行」的弱判据。
# ═══════════════════════════════════════════════════════════════════════
DEPLOY_FILE="484_request_logs_hot_add_status_code.sql"
DEPLOY_VERSION="484"
DEPLOY_SEQ="session-summary-and-integrity-2026-09"
if command -v sha256sum >/dev/null 2>&1; then
  sha_tool="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  sha_tool="shasum -a 256"
else
  sha_tool=""
fi
if [ -z "$sha_tool" ]; then
  note "  ℹ 没有 sha256sum/shasum ⇒ ★ 第四阶段（★ F3 运行时覆盖）在本机跑不了（★ 不是通过）"
else
  deploy_src="installer/cmd/llm-gw-installer/embeddata/startup/${DEPLOY_FILE}"
  if [ ! -f "$deploy_src" ]; then
    bad "找不到 ${deploy_src} ⇒ 第四阶段无法执行"
  else
    deploy_sha="$($sha_tool "$deploy_src" | cut -d' ' -f1)"
    note "★ 跑第四阶段（★ 部署通道视角：★ 权威台账同内容指纹 ⇒ 不重放）…"
    q "CREATE TABLE IF NOT EXISTS public.gateway_db_revision_sequences (sequence_name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())" >/dev/null
    q "ALTER TABLE public.gateway_db_revision_sequences ADD COLUMN IF NOT EXISTS content_sha256 text" >/dev/null
    q "ALTER TABLE public.gateway_db_revision_sequences ADD COLUMN IF NOT EXISTS execution_sha256 text, ADD COLUMN IF NOT EXISTS runtime_adaptation text" >/dev/null
    # ★★ 删掉目标文件的版本行 ⇒ ★★ 版本规则不再跳过它 ⇒ ★★ 只剩 F3 能跳
    q "DELETE FROM public.schema_migrations WHERE version = '${DEPLOY_VERSION}'" >/dev/null
    q "INSERT INTO public.gateway_db_revision_sequences (sequence_name, content_sha256) VALUES ('${DEPLOY_SEQ}:${DEPLOY_FILE}', '${deploy_sha}') ON CONFLICT (sequence_name) DO NOTHING" >/dev/null
    RUN3_LOG="$(mktemp -t fresh-install-run3)"
    if ! ( cd installer && E2E_PG_CONTAINER="$CONTAINER_NAME" \
            go run ./cmd/e2e-bootstrap "$PG_DB" cmd/llm-gw-installer/embeddata ) >"$RUN3_LOG" 2>&1; then
      note "---- 第三次安装输出（尾部 20 行）----"
      tail -20 "$RUN3_LOG"
      note "---- 第三次安装输出（尾部 20 行）----"
      bad "★ 第四阶段失败（★ ★ 权威台账已登记 ${DEPLOY_FILE} 的同一内容指纹、★ 版本行也已删 ⇒ ★ 只剩 F3 能跳过它 ⇒ ★ 它被重放了 ⇒ ★ 而它一重放就炸）"
      rm -f "$RUN3_LOG"
      exit 1
    fi
    # ★★ 判据：★ 该文件必须是「被跳过」⇒ ★ 日志里出现 F3 的理由
    #
    # ★★★★ ★★★ 量具坑（★ 2026-11-23 L33 二轮实测撞到、★ **本轮头号发现**）★★★
    # ★★★★ 第一版的写法是 `if (grep -q "…" "$LOG" || true); then` ⇒ ★★ **恒真**★★
    # ★★★★ ⇒ ★★ 「跳过」那条**恒判绿**（★ 假绿）、★ 「被重放」那条**恒判红**
    # ★★★★ （★ 假红）★★ ⇒ ★★ 两条判据**同时恒成立** ⇒ ★★ 门输出自相矛盾
    # ★★★★ （★ 「✔ 被跳过」与「✘ 被重放」同时出现）★★ ⇒ ★★ 实测日志里明明只有
    # ★★★★ 一行「跳过」行 ⇒ ★★ **矛盾只在判据写法里、★ 不在日志里**★★。
    # ★★★★ ⇒ ★★ 教训（★ 写死在这里）：★★ **`|| true` 只能用在「命令替换里取计数」
    # ★★★★ 或「诊断打印」上；★ ★ 一旦放到 `if` 的条件位、★ 它会把整个条件
    # ★★★★ 变成恒真 ⇒ ★★ 判据被掏空 ⇒ ★★ **一道门最坏的情形就是「判据恒真」**
    # ★★★★ （★ 比「恒绿」更隐蔽：★ 恒绿至少看起来像通过、★ 而恒真会同时给出
    # ★★★★ 一绿一红两个互相打架的结论）★★★★ ⇒ ★★ 判据位一律用 grep 的**真实
    # ★★★★ 退出码**（★ 0＝命中、★ 1＝未命中；★ ★ `if` 条件位上的非零退出码
    # ★★★★ **不会**触发 ERR trap、★ 也不会触发 `set -e`）★★★★。
    if grep -q "跳过 startup migration ${DEPLOY_FILE}" "$RUN3_LOG"; then
      note "  ✔ ${DEPLOY_FILE} 被权威台账按同一内容指纹跳过（★ F3 生效）"
    else
      bad "★ ${DEPLOY_FILE} 没有出现在「跳过」行里 ⇒ ★ F3 的匹配分支没生效（★ 版本行已删、★ 权威台账里有同一指纹）"
    fi
    if grep -q "应用 startup migration ${DEPLOY_FILE}" "$RUN3_LOG"; then
      bad "★ ${DEPLOY_FILE} 被**重放**了（★ 权威台账说它已按同一指纹应用过）"
      note "  ── 第三次安装日志里与 ${DEPLOY_FILE} 有关的行（★ 诊断）──"
      (grep -n "${DEPLOY_FILE}" "$RUN3_LOG" || true) | head -12 | sed 's/^/      /'
    else
      note "  ✔ ${DEPLOY_FILE} 没有被重放"
    fi
    rm -f "$RUN3_LOG"
  fi
fi

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
