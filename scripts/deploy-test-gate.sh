#!/usr/bin/env bash
# requires-bash: 4
# ↑ declare -A（137-138 行）；Linux 侧自然满足，macOS 侧用 ~/sdk/bash5/bin/bash 跑。
# =====================================================================
# scripts/deploy-test-gate.sh — 部署前的**测试闸门** v2（2026-10-30 修正轮）
# =====================================================================
# ★ 为什么要有这个闸门（★ 审计轮的实测发现，★ 不是假想）★★
#
#  部署脚本**只编译、不跑测试**（★ `docker build` 会因编译错误失败 ⇒ 语法
#   门是有的；★ 但**没有**任何一步跑 `go test`）⇒ ★★ **红灯也能上线** ★★
#
#   ★ 实证：★ 2026-10-29「输出合规误报轮」把守卫的越界处置从「阻断」改成
#   「降级放行」⇒ ★★ 而 2026-10-02 存储审计立的那条容量契约测试
#   （`security/sanitize/stream_capacity_audit_test.go`）**一直是红的** ★★
#   ⇒ ★★ 那条测试守的正是「越界不再杀请求」这条修复的**另一半**（★ 缓冲有界
#   + 降级放行）★★ ⇒ ★★ 如果部署跑测试，这一轮会在上线前就停住 ★★
#
# ★ v1（2026-10-29 首版）的四个实测缺陷 —— 2026-10-30 部署实测全部踩中 ★★
#
#   ① **零输出假死**：全量测试输出被 `$(...)` 捕获 ⇒ 跑完之前终端一个字都
#      没有；而 docker 冷编译（GOCACHE 在容器 /tmp + `--rm` ⇒ 每次部署都是
#      全冷）8 个包要 10~20 分钟 ⇒ **部署看起来像死住了**（用户实测反馈
#      「系统反应不正常」的根因）。
#   ② **冷缓存税**：`-e GOCACHE=/tmp/gocache`（容器内临时层）+ `--rm` ⇒
#      每次部署把整个依赖树重新编译一遍 ⇒ 闸门永远慢。
#   ③ **BASE_REF 未定义**：脚本体引用 `$BASE_REF` 但从未赋值 ⇒ 只要当前树
#      有红走基线臂，`set -u` 当场崩在 unbound variable，报错与原因完全脱钩。
#   ④ **两个伪绿洞**：(a) docker 自身失败（daemon 挂/挂载失败）⇒ 输出为空 ⇒
#      「0 个 --- FAIL 行」被判成**全绿**；(b) 包**编译失败**只打
#      `FAIL pkg [build failed]`、没有 `--- FAIL:` 行 ⇒ 同样被判成全绿。
#      ★ 闸门最坏的失败方式就是把「没跑成」报告成「绿」。
#
# ★ v2 修法 ★
#
#   · **原生 go 优先**：本机 go（~/sdk/go 等）+ GOTOOLCHAIN=local（旧 go 会
#     快速失败落 docker，**绝不**在线下载工具链）+ `-mod=vendor` ⇒ 离线、
#     复用本机 48G 热构建缓存 ⇒ **分钟级**（实测 8 包 3.6 分钟）。
#   · **docker 兜底**：没有可用 go 时才走；GOCACHE 挂**持久命名卷**
#     （默认 `llm-gw-go-gocache`）⇒ 只有第一次冷。
#   · **流式输出**：`tee` 直通终端，go test 逐包打 `ok/FAIL`，进度可见。
#   · **全覆盖断言**：`go list` 拿全量被测包清单 ⇒ 每个包必须有结果行，
#     缺任何一行 ⇒ 按**失败**处理（宁可红报，不许伪绿）。
#   · **红 = 包级 FAIL 行 ∪ 测试级 --- FAIL 名**：编译失败/进程崩溃/超时
#     都落进红名单，不再可能被「0 个 --- FAIL 行」洗成绿。
#   · `BASE_REF` 缺省 `origin/main`（可用环境变量覆盖）。
#
# ★ 逃生舱：★ `DD_SKIP_TEST_GATE=1` 跳过闸门（★ 必须**显式**给，★ 不允许
#   「闸门自己默默跳过」——★ 那样又是一次静默降级）★★
#
# 用法:
#   bash scripts/deploy-test-gate.sh              # 按 diff 选包跑测试
#   DD_SKIP_TEST_GATE=1 bash scripts/deploy-test-gate.sh   # 显式跳过
# =====================================================================
# ★★ r1048 轮（★2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★目录约定与6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒也不要把
# ★★ GOCACHE设成垃圾值 ⇒ ⇒ 那会让 go直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
 __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
 . "$__gocache_dir/scripts/lib/gocache.sh"
 gocache_use script
fi

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 按 go.mod 的版本要求挑 Go 工具链（2026-11-17 F3 轮新增）────────────
# ★ 为什么必须独立成库（而不是在门脚本里内联）：判据本身要被**实跑**测到 ⇒
#   门禁 scripts/ursmcheck/deploy_test_gate_toolchain_gate_test.go 用
#   **假 go 二进制**（打印不同版本号）真跑本库的挑选逻辑 ⇒ 钉住「按版本挑」。
if [ -f "$REPO_ROOT/scripts/lib/go-toolchain.sh" ]; then
  . "$REPO_ROOT/scripts/lib/go-toolchain.sh"
fi

# ★ 闸门用的 Go 基础镜像（docker 兜底路径用）。
GO_IMAGE="${DD_TEST_GO_IMAGE:-golang:1.27.4-alpine}"
# ★ 原生 go 的路径；留空自动探测（★ 按 go.mod 的版本要求挑，见 native_ok）。
NATIVE_GO="${DD_TEST_GO_BIN:-}"
# ★ go test 自身的测试超时（防单个测试挂死）；docker 侧用秒。
#
# ★★★ 2026-11-23 L30 轮：★ **两条路径必须同口径**（★ 原生路径早就传了 `-timeout`，
# ★ ★ 而 docker 路径原先**只给了外层 timeout** ⇒ ★ ★ `go test` 用了默认 10 分钟
# ★ ★ ⇒ ★ ★ `./admin/`（★ 525 个测试文件）实测在 600.356s 处被腰斩、★ **不是判据红**
# ★ ★ ⇒ ★ ★ 详见 `run_docker()` 里的注记）。
#
# ★ ★ 两者的关系（★ 改任一个都要看这条）：★ **`DOCKER_TIMEOUT_SECS` 必须大于
# ★ `TEST_TIMEOUT` 换算后的秒数**（★ 默认 1800s > 900s ⇒ 留 900s 余量）⇒ ★★ **因为
# ★ ★ 外层是「兜底杀开关」**：★ ★ 它只该在 go test 自己的上限都到时还没结束才动手
# ★ ★ ⇒ ★ ★ 留出余量是为了让 go test **打印完整的 panic 栈**（★ 否则又是
# ★ ★ 「腰斩了却看不到为什么」⇒ ★ ★ 仓内已吃过一次这个亏）。
TEST_TIMEOUT="${DD_TEST_TIMEOUT:-15m}"
DOCKER_TIMEOUT_SECS="${DD_TEST_DOCKER_TIMEOUT_SECS:-1800}"
CACHE_VOLUME="${DD_TEST_CACHE_VOLUME:-llm-gw-go-gocache}"
# ★ 基线参照（★ v1 缺陷③：引用了但从未定义 ⇒ 有红必 unbound 崩）★
BASE_REF="${BASE_REF:-origin/main}"

# ★ 回落清单：★ diff 为空（★ 例如从干净树部署）时跑这些核心包。
FALLBACK_PACKAGES=(
  ./admin/
  ./domains/sanitizerules/
  ./domains/hooks/outputcompliance/
  ./security/sanitize/
)

log() { printf '[test-gate] %s\n' "$*" >&2; }
die() { printf '[test-gate] FAIL: %s\n' "$*" >&2; exit 1; }

# ── 参数处理（★ 2026-11-02 第十九轮 F-23）──────────────────────────────
#
# ★ 为什么必须有：此前本脚本**不处理任何参数** ⇒ `bash scripts/deploy-test-gate.sh
#   --help` 会**跑全量闸门**（本机 go 跑 8 包 ≈ 3.6 分钟；无 go 时还会拉起
#   docker）⇒ 运维想看用法，却拿到一次完整的部署前测试。
#   ★ **未知参数**同样如此 ⇒ 静默跑全量，连报错都没有。
usage() {
  cat <<'EOF'
用法:
  bash scripts/deploy-test-gate.sh                      # 按 diff 选包跑测试
  DD_SKIP_TEST_GATE=1 bash scripts/deploy-test-gate.sh  # 显式跳过闸门
选项:
  -h, --help    打印本用法并退出（**不跑任何测试**）
退出码:
  0  闸门通过 / --help
  1  闸门失败（有新红 / 缺结果行 / 跑不起来）
  2  参数错误
EOF
}
case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
  '')
    ;;
  *)
    printf '[test-gate] FAIL: 未知参数: %s\n' "$1" >&2
    usage
    exit 2
    ;;
esac

cd "$REPO_ROOT"

# ── 清扫上一次被 SIGKILL 留下的基线 worktree（★ 第十九轮 F-22）─────────
#
# ★ 为什么需要：基线树是 `git worktree add --detach` 建的，**只在 EXIT trap
#   里移除** ⇒ 被 SIGKILL（OOM / kill -9）打断时 trap 不跑 ⇒
#   `.git/worktrees/<name>` 的**管理记录永久残留** ⇒ 一次一次累积
#   （`git worktree list` 越来越长、临时目录越堆越多）。
#   ★ 而 `git worktree prune` **救不了**：SIGKILL 时 mktemp 目录**也还在**
#   ⇒ prune 的判据是「工作目录已消失」⇒ 它认为这个 worktree 还活着。
#   ⇒ 修法：给本闸门的临时目录一个**可识别的名字前缀**
#   （llm-gateway-test-gate.）⇒ 下次启动时按这个前缀精确清扫。
#   ★ 只扫**本闸门自己**留下的（按名字前缀）⇒ 不碰任何别人的 worktree。
sweep_stale_gate_worktrees() {
  local common wt_root d target dir
  common="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  [[ -n "$common" ]] || return 0
  case "$common" in
    /*) ;;
    *) common="$REPO_ROOT/$common" ;;
  esac
  wt_root="$common/worktrees"
  [[ -d "$wt_root" ]] || return 0
  for d in "$wt_root"/*; do
    [[ -d "$d" ]] || continue
    target=""
    if [[ -f "$d/gitdir" ]]; then
      target="$(sed -n 's|^gitdir: ||p' "$d/gitdir" 2>/dev/null | head -1 || true)"
    fi
    case "$target" in
      *llm-gateway-test-gate.*) ;;
      *) continue ;;
    esac
    dir="$(dirname "$target")"
    log "★ 清扫上一次被 SIGKILL 留下的基线 worktree：$dir ★"
    git worktree remove --force "$dir" >/dev/null 2>&1 || true
    rm -rf "$dir" >/dev/null 2>&1 || true
    rm -rf "$d" >/dev/null 2>&1 || true
  done
  # ★ **不**调 `git worktree prune`：它清的是「**任何**工作目录已消失的
  #   worktree 记录」（不只本闸门的）⇒ 作用域超出本门的意图。
  #   本门只按**名字前缀**精确清扫自己那一份。
  return 0
}
sweep_stale_gate_worktrees

if [[ "${DD_SKIP_TEST_GATE:-0}" == "1" ]]; then
  log "★ 闸门被显式跳过（DD_SKIP_TEST_GATE=1）★ —— 这次上线**没有**测试证据"
  exit 0
fi

# ── 守卫域映射：改动文件 ⇒ 该被谁看（★ 2026-11-23 N10 根因收口）────────────
#
# ★ 为什么必须有（★ 缺口是实测出来的、★ 不是推演）：
#
#	闸门按【改动的 .go 文件】选包 ⇒ ★ **纯 .sql / 纯 .sh 改动一个包都不触发**。
#	★ 而**门不住 .go 文件里**：
#	   ★ 「canonical ≡ embeddata 副本逐字节一致」「物化视图别名列闭合」
#	   ★ 「迁移预算」「自登记」「DDL 形态」…… ⇒ ★ 全住在 ./sql/migrations/startup
#	     （★ 178 个测试文件、★ **全是 _test.go** ⇒ ★ .go 规则**永远选不到**）；
#	   ★ 「部署脚本不得用相对 $(dirname "$0") 解析同目录脚本」⇒ ★ 住在
#	     ./scripts/deploygate（★ **也只有 _test.go** ⇒ ★ 同样选不到）。
#
#	★ 实测（211 seq 3230 前后）：★ **一天内两次**漏改 embeddata 副本
#	  （★ `761e641ed` D2 去重改写、★ `d587edc47` 加关并行 SET）⇒ ★ 两次「副本身份门」
#	  都是**红的**、★ ★ 却没有任何一步在部署里看见它
#	  ⇒ ⇒ 闸门选的包与「这次改动真正该被谁看」之间存在**盲区**。
#
#	★ ★ 还有第二层盲区（★ 同源）：★ 部署脚本门住在 ./scripts/deploygate，
#	  ★ 而该目录**只有 _test.go** ⇒ ★ **改 scripts/deploy-*.sh 时它也永远不被跑**
#	  ⇒ ★ 并行车那道「相对路径」门（★ 2026-11-23 刚补的）同样**从未在部署里跑过**。
#
# ⇒ 修法：★ 把「改动文件的种类」映射到「该被谁看」显式写成表（★ 下面那张表），
#   ★ 选包时按表补选。★ 判据抽成函数是**为了让判据可测**：
#   ★ scripts/deploygate 的守卫会把这段函数**原样抽出来**在 bash 里跑
#   （★ 对照臂：规则换成永不匹配 ⇒ 判据必须变红）。
#
# ★ 表的格式：★ "<正则>|<门包>"，★ 一行一条。★ 正则是 **ERE**（grep -E 口径）。
# ★ ★ 每条规则都必须有**仓内真实文件**命中（★ 否则是死规则）⇒ ★ 由守卫当场抓住。
DD_GUARDED_DOMAINS=(
  # ★ 迁移 SQL ⇒ 迁移族门（★ 副本一致 / 列闭合 / 预算 / 自登记 / DDL 形态）
  "^sql/migrations/startup/.*\.sql$|./sql/migrations/startup"
  # ★ embeddata 副本 ⇒ 同上（★ 副本一致性正是「副本 vs canonical」）
  "^installer/cmd/llm-gw-installer/embeddata/startup/.*\.sql$|./sql/migrations/startup"
  # ★ 部署脚本 ⇒ 部署脚本门（★ 相对路径 / 顶层展开闭合）
  "^scripts/deploy-[^/]*\.sh$|./scripts/deploygate"
  # ★ ★ 2026-11-23（12h 修订审计轮）补规两条 —— ★ 这两条是**实测出来的盲区**：
  #   ① 离线升级副本 `installer/migrations/**/up.sql` —— ★ 原映射**无任何规则命中**
  #      （实测：用闸门自身的判据函数原样跑，`changed=installer/migrations/994_*/up.sql`
  #      ⇒ **无输出**）⇒ 而仓内三方门（internal/sqlguard/migration_994_delivery_test.go）
  #      恰恰就是比「canonical vs offline 副本」的 ⇒ ★ 它在部署路径上**结构性不可见**。
  #   ② `./internal/sqlguard` —— ★ 承载三方门（+ 迁移通道/注册表连续性门）的包，
  #      ★ 全仓 grep 命中 0 处 ⇒ ★ 改了迁移却不会跑迁移门。
  "^installer/migrations/.*/(up|down)\.sql$|./internal/sqlguard"
  "^installer/migrations/.*/up\.sql$|./sql/migrations/startup"
)

# dd_guard_domain_packages <changed_files>：★ 改动文件 ⇒ 该被谁看的门包。
#
# ★ 判据面：**所有**非 .go 的改动（★ .go 改动由上面的目录规则负责）。
dd_guard_domain_packages() {
  local changed="${1:-}" line re pkg
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    # ★★ 分隔符按**最后一个** '|' 切（★ 不是第一个）。
    #
    # ★ 为什么：★ 正则里**自带** '|' 是合法的（★ 如 `/(up|down)\.sql$`）
    #   ⇒ 按第一个切会把正则从中间劈开（★ 实测：★ 规则4 被切成
    #   正则=`^installer/migrations/.*/(up` / 门包=`down)\.sql$|./internal/sqlguard`
    #   ⇒ 正则编译不过 ⇒ ★ **那条规则永远不生效** ⇒ ★ 而门包是错的
    #   ⇒ ⇒ 选了它就会「go test 一个不存在的包」⇒ 把部署**判死**。
    #   ⇒ ★ 门包路径里**不会出现** '|' ⇒ 按最后一个切必然切对。
    [[ "$line" == *"|"* ]] || continue
    re="${line%|*}"
    pkg="${line##*|}"
    [[ -n "$re" && -n "$pkg" ]] || continue
    if printf '%s\n' "$changed" | grep -E "$re" >/dev/null 2>&1; then
      printf '%s\n' "$pkg"
    fi
  done < <(printf '%s\n' "${DD_GUARDED_DOMAINS[@]}")
  return 0
}

# ── 基线 commit（★ 选包与基线对照**共用**同一个值）──────────────────────
#
# ★ 为什么抽成函数（★ 2026-11-23 L9 修复轮实测出来的机制缺陷）：
#
#	★ 原先**两套口径**：
#	  · 选包   ＝ `git diff HEAD~1 HEAD`（★ **只看最后一笔提交**）
#	  · 基线对照＝ `git merge-base HEAD "$BASE_REF"`（★ 跑**基线树**）
#	⇒ ⇒ 两套口径漂移 ⇒ ★ **选出来的包，基线树可能根本没跑过**
#	⇒ ⇒ 「这盏灯是新的红还是旧的红」**判不出来** ⇒ ★ 新红会被当成既有红放行。
#
#	★ 实测（211 seq 3236→3237 那一轮）：★ 本轮的 admin 生产改动在
#	  `044349a2c`（★ **倒数第二笔**）⇒ ★ 最后一笔是并行会话的 ratelimit 改动
#	  ⇒ ⇒ 选包只选出 `./cmd/gateway ./ratelimit` ⇒ ★ **`./admin` 压根没进闸门**
#	  ⇒ ★ 而闸门当时「全绿」。
#
#	★ 正解：★ 选包与基线对照**共用同一个 merge-base**
#	  ⇒ 选包 ＝ 「基线树 vs 当前树」的差异所触及的包
#	  ⇒ ⇒ 两套口径**永不漂移** ⇒ ★ 新红/旧红**可判定**。
dd_gate_base_commit() {
  git merge-base HEAD "$BASE_REF" 2>/dev/null || true
}

# dd_gate_changed_files [base_commit]：★ 本次部署的**改动清单**（★ 四路并集）。
#
# ★ 四路缺任何一路都会**漏选包** ⇒ 漏掉的包**不会被闸门看见**：
#   ① 工作区未提交改动   git diff HEAD
#   ② 暂存区未提交改动   git diff --cached
#   ③ 最后一笔提交       git diff HEAD~1 HEAD
#      （★ 兜底：BASE_REF 退化成 HEAD 自身时，③ 仍能选出一笔）
#   ④ ★★ 相对**基线树**的全部改动  git diff <base_commit> HEAD
#      （★ base_commit＝merge-base(HEAD, $BASE_REF)＝**基线对照跑的那棵树**）
#      ⇒ ★ 这一路才是「本次上线相对现役**真正**改了什么」的完整答案。
dd_gate_changed_files() {
  local base_commit="${1:-}"
  {
    git diff --name-only -z HEAD 2>/dev/null || true
    git diff --name-only -z --cached 2>/dev/null || true
    git diff --name-only -z HEAD~1 HEAD 2>/dev/null || true
    if [[ -n "$base_commit" ]]; then
      git diff --name-only -z "$base_commit" HEAD 2>/dev/null || true
    fi
  } | tr '\0' '\n' | sort -u
}

# dd_gate_packages_from_files <changed_files>：★ 改动文件清单 ⇒ 该跑的包清单。
#
# ★ 判据面：★ 改动的 .go 文件所在目录（★ 该目录含 _test.go 才选）
#   ＋ 非 .go 改动按守卫域映射补选（★ 见上 DD_GUARDED_DOMAINS）。
# ★ 抽成函数是**为了让判据可测**（★ scripts/deploygate 的守卫会把它
#   **原样抽出来**在真 git 仓库里跑 ⇒ ★ 对照臂：删掉第④路 ⇒ 必须选不出 ./admin）。
dd_gate_packages_from_files() {
  local changed="${1:-}" dir extra
  local -a out=()
  if [[ -n "$changed" ]]; then
    while IFS= read -r dir; do
      [[ -n "$dir" ]] || continue
      if compgen -G "$dir/*_test.go" >/dev/null 2>&1; then
        out+=("./$dir")
      fi
    done < <(printf '%s\n' "$changed" | grep '\.go$' | xargs -n1 dirname 2>/dev/null | sort -u || true)
    # ★ 非 .go 改动 ⇒ 按守卫域映射补选（★ 2026-11-23 N10，★ 见上「为什么」）
    while IFS= read -r extra; do
      [[ -n "$extra" ]] || continue
      out+=("$extra")
    done < <(dd_guard_domain_packages "$changed")
  fi
  if [[ ${#out[@]} -gt 0 ]]; then
    printf '%s\n' "${out[@]}"
  fi
  return 0
}

# ── 选包：改动触及的目录（只留有 _test.go 的） ──────────────────────────
base_commit="$(dd_gate_base_commit)"
changed_files="$(dd_gate_changed_files "$base_commit")"

packages=()
if [[ -n "$changed_files" ]]; then
  mapfile -t packages < <(dd_gate_packages_from_files "$changed_files")
fi

if [[ ${#packages[@]} -eq 0 ]]; then
  log "★ diff 没有触及带测试的包 ⇒ 回落核心包清单 ★"
  packages=("${FALLBACK_PACKAGES[@]}")
else
  # ★ 去重：★ .go 规则与守卫域映射**可能选中同一个包**（★ 例如改了
  # scripts/deploygate 里的 .go 又改了 scripts/deploy-*.sh）⇒ ★ 清单里出现
  # 重复项 ⇒ ★ 日志读起来像「跑了两遍」⇒ ★ 这里排掉（★ go test 本身会去重，
  # ★ 但清单是给人看的、★ 也要给覆盖断言用）。
  mapfile -t packages < <(printf '%s\n' "${packages[@]}" | sort -u)
fi

log "要跑的包：${packages[*]}"

# ── 嵌套 Go 模块分箱（★ 2026-10-30 部署实测缺陷）────────────────────────
# installer/ 自带 go.mod ⇒ 是嵌套模块，从主模块根 `go test ./installer/...`
# 必然 setup failed（"main module does not contain package"）⇒ ① 触及
# installer 的提交会让干净树部署卡死在闸门；② assert_full_coverage 的
# go list 也在主模块根跑 ⇒ 同样拿不到 installer 的包清单 ⇒ 走「读数不全」
# 红报。修法 = 按模块根分箱：嵌套模块的包**在其模块根**下跑（GOFLAGS 按
# 树内有无 vendor 决定：installer 无 vendor ⇒ -mod=mod 走模块缓存），覆盖
# 断言同源同根。响亮记录分箱结果，不许静默跳过（那会把闸门降级成摆设）。
main_packages=()
nested_modules=()   # 去重后的嵌套模块根列表，如 installer
declare -A nested_seen=()
declare -A nested_pkgs=()
for p in "${packages[@]}"; do
  dir="${p#./}"; dir="${dir%/}"
  modroot="$dir"
  while [[ -n "$modroot" && "$modroot" != "." && ! -f "$REPO_ROOT/$modroot/go.mod" ]]; do
    modroot="$(dirname "$modroot")"
  done
  if [[ -z "$modroot" || "$modroot" == "." || ! -f "$REPO_ROOT/$modroot/go.mod" ]]; then
    main_packages+=("$p")
  else
    if [[ -z "${nested_seen[$modroot]+x}" ]]; then
      nested_seen[$modroot]=1
      nested_modules+=("$modroot")
    fi
    nested_pkgs[$modroot]+=" ./${dir#"$modroot"/}"
  fi
done
packages=("${main_packages[@]}")
if [[ ${#nested_modules[@]} -gt 0 ]]; then
  log "★ 嵌套模块分箱：${nested_modules[*]} ⇒ 各自模块根下跑（不从主模块根）★"
fi

# ── 工作目录与清理 ────────────────────────────────────────────────────
# ★ 目录名带**可识别前缀**（llm-gateway-test-gate.）⇒ SIGKILL 残留能被
#   下次启动的 sweep_stale_gate_worktrees 精确识别并清扫（F-22）。
workdir="$(mktemp -d "${TMPDIR:-/tmp}/llm-gateway-test-gate.XXXXXX")"
base_tree=""
cleanup() {
  if [[ -n "$base_tree" && -d "$base_tree" ]]; then
    git worktree remove --force "$base_tree" >/dev/null 2>&1 || true
  fi
  rm -rf "$workdir" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── 原生 go 探测（★ 2026-11-17 修：按 go.mod 的**版本要求**挑，不是按存在性挑）──
#
# ★ 旧实现（按存在性挑）是 2026-11-17 一次部署卡在门上的真因：
#   「第一个存在的 go」⇒ 本机命中 homebrew 的 **go1.27.3**，而 go.mod 要求
#   **go >= 1.27.4** ⇒ 原生路径被 go.mod **直接拒绝**（"go.mod requires go >= 1.27.4"）
#   ⇒ 落 docker 兜底 ⇒ 而兜底**没设 CGO_ENABLED=1** ⇒ 8 条 SQLite 测试全红
#   ⇒ ⇒ **部署卡在门上**，而红的理由（SQLite 驱动不可用）与真因（工具链版本）**毫无关系**。
#
# ★ 新判据（scripts/lib/go-toolchain.sh，可独立实跑测）：
#   · 要求 = go.mod 的 `go X.Y.Z` 指令；
#   · 候选逐个取 `go version`，**第一个满足要求**的即采用；
#   · 都不满足 ⇒ **返回 1**（交由 docker 兜底）⇒ 而不是挑一个注定被 go.mod 拒绝的旧版。
native_ok() {
  if [[ -n "$NATIVE_GO" ]]; then
    [[ -x "$NATIVE_GO" ]] || return 1
    # ★ 显式给的也要**核对版本**：给了一个旧版 go ⇒ 原生路径必然被 go.mod 拒绝
    #   ⇒ 那就等于没给 ⇒ 落到 docker 兜底（而不是让门以「SQLite 驱动不可用」的红失败）。
    local req_ev ver_ev
    req_ev="$(llmgw_go_required_version "$REPO_ROOT/go.mod" 2>/dev/null || echo 1.0)"
    ver_ev="$(llmgw_go_toolchain_version "$NATIVE_GO" 2>/dev/null || true)"
    if [[ -n "$ver_ev" ]] && llmgw_go_version_ge "$ver_ev" "$req_ev"; then
      return 0
    fi
    log "★ DD_TEST_GO_BIN 指定的 go 版本不满足 go.mod（${ver_ev:-未知} < ${req_ev}）⇒ 不用它"
    NATIVE_GO=""
    return 1
  fi
  if ! declare -F llmgw_go_pick_detail >/dev/null 2>&1; then
    log "★ scripts/lib/go-toolchain.sh 不在场 ⇒ 无法按版本挑工具链 ⇒ 落 docker 兜底"
    return 1
  fi
  local picked detail
  detail="$(llmgw_go_pick_detail "$REPO_ROOT/go.mod" \
      "$(command -v go 2>/dev/null)" "$HOME/sdk/go/bin/go" \
      /usr/local/go/bin/go /opt/homebrew/bin/go)"
  picked="$(printf '%s\n' "$detail" | sed -n '1p')"
  detail="$(printf '%s\n' "$detail" | sed -n '2p')"
  if [[ -z "$picked" ]]; then
    log "★ 没有候选 go 满足 go.mod 的版本要求（${detail}）⇒ 落 docker 兜底"
    return 1
  fi
  log "★ 原生 go 候选核对（${detail}）⇒ 选中 ${picked}"
  NATIVE_GO="$picked"
  return 0
}

# modflag_for <tree> <subdir>：树内该子目录有 vendor ⇒ -mod=vendor（主模块
# 惯例）；没有 ⇒ -mod=mod（嵌套模块如 installer 无 vendor，走本机模块缓存）。
modflag_for() {
  if [[ -d "$1/${2:-}/vendor" ]]; then echo "-mod=vendor"; else echo "-mod=mod"; fi
}

# ── 跑法一：原生 go（热缓存 ⇒ 分钟级；GOTOOLCHAIN=local ⇒ 绝不在线拉工具链）──
run_native() {
  local tree="$1" outfile="$2" subdir="$3"; shift 3
  local modflag; modflag="$(modflag_for "$tree" "$subdir")"
  log "跑法：原生 go（$(GOTOOLCHAIN=local "$NATIVE_GO" version 2>/dev/null | awk '{print $3}')，复用本机构建缓存；超时 ${TEST_TIMEOUT}；${subdir:-主模块根} ${modflag}）"
  log "首个包出结果前有 1~2 分钟编译静默，属 go test 正常形态（不是卡死）…"
  (
    cd "$tree${subdir:+/$subdir}" &&
    GOFLAGS="$modflag" GOTOOLCHAIN=local "$NATIVE_GO" test -count=1 -timeout "$TEST_TIMEOUT" "$@"
  ) 2>&1 | tee "$outfile"
  # 红绿由结果行判定，不按退出码（红退出码非 0 是**预期**，不能让 set -e 杀掉解析）。
}

# ── 跑法二：docker 兜底（GOCACHE 持久命名卷 ⇒ 只有第一次冷） ────────────
#
# ★ 2026-11-17 修：**兜底路径必须与生产镜像同款编译配置** ⇒ 开 CGO。
#   Dockerfile 里明确写着 `CGO_ENABLED=1` + 装 `gcc`/`musl-dev`（因为 lite 存储
#   模式要 SQLite ⇒ CGO 关掉就没有 sqlite 驱动）⇒ 而兜底路径**没设** ⇒
#   8 条 `TestLiteRequestLogSink_*` 全红（「当前二进制以 CGO_ENABLED=0 构建，
#   SQLite 驱动不可用」）⇒ 红的理由与真因（工具链版本）**毫无关系**。
#   ⇒ 兜底现在也装 gcc/musl-dev 并设 CGO_ENABLED=1（与 Dockerfile 同款）。
#   ★ 装包失败**不阻断**：照常跑测试，并打印一行明确说明「SQLite 类测试的红是
#     环境问题，不是代码问题」⇒ 宁可让人看见真话，也不要把环境红伪装成代码红。
run_docker() {
  local tree="$1" outfile="$2" subdir="$3"; shift 3
  local modflag; modflag="$(modflag_for "$tree" "$subdir")"
  # ★★★ 2026-11-23 L30 轮：★ **给 `go test` 补上自己的 `-timeout`**。
  # ★ 实测（★ 真跑、★ 本机 alpine 容器 ＋ 嵌套 docker）：★ `./admin/` 包
  # （★ **525 个测试文件**、★ 仓内最大包）在 **600.356s** 处被腰斩 ⇒ ★ 栈顶是
  # `testing.tRunner` 的 timeout panic、★ **不是任何断言红** ⇒ ★ **「腰斩」不是「判据红」**。
  # ★ ★ 根因＝**这条路径只给了外层 `timeout`（★ 默认 1800s）、★ 没给 `go test` 自己的
  # `-timeout`** ⇒ ⇒ ★ **`go test` 用了默认的 10 分钟上限** ⇒ ⇒ ★ ★ **而同一道闸门的
  # 原生路径（★ `run_native`）是传了 `-timeout "$TEST_TIMEOUT"`（★ 默认 15m）的**
  # ⇒ ⇒ ★★★ **⇒ 同一道闸门、两条路径的预算口径不一致 ⇒ ★ docker 路径必然腰斩 admin 包**。
  # ★ 修法＝**补 `-timeout`，与原生路径同口径**（★ 不动 admin 包源码、★ 不抬高上限）。
  # ★ ★ 外层 `timeout` 留作**兜底杀开关**（★ 比 go test 自己的上限多 5 分钟 ⇒
  #   ★ ★ 留出让 go test 打印完整 panic 栈的余量 ⇒ ★ 否则又是「腰斩却看不到为什么」）。
  log "跑法：docker（${GO_IMAGE}；CGO_ENABLED=1（与 Dockerfile 同款）；GOCACHE 用持久卷 ${CACHE_VOLUME} ⇒ 仅首次冷编译；★ go test 自身超时 ${TEST_TIMEOUT}、★ 外层兜底 ${DOCKER_TIMEOUT_SECS}s；${subdir:-主模块根} ${modflag}）"
  docker run --rm \
    -v "$tree":/w -w "/w${subdir:+/$subdir}" \
    -v "$CACHE_VOLUME":/tmp/gocache \
    -e GOFLAGS="$modflag" -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
    -e CGO_ENABLED=1 \
    "$GO_IMAGE" \
    sh -c "sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories 2>/dev/null || true; \
           if ! (apk update >/dev/null 2>&1 && apk --no-cache add gcc musl-dev >/dev/null 2>&1); then \
             echo '[test-gate] ★ 容器内 gcc/musl-dev 安装失败 ⇒ CGO 不可用 ⇒ SQLite 类测试会红（**环境问题，不是代码问题**）' >&2; \
           fi; \
           timeout $DOCKER_TIMEOUT_SECS go test -count=1 -timeout $TEST_TIMEOUT $*" 2>&1 | tee "$outfile"
}

# ── 判定原语 ──────────────────────────────────────────────────────────
# readable_results：输出里至少有一行包级结果（ok / FAIL / ? [no test files]）。
readable_results() { grep -qE '^(ok|FAIL|\?)' "$1"; }

# red_names：红名单 = 测试级 `--- FAIL:` 名 ∪ 包级 `FAIL <pkg>` 行。
# ★ 包级 FAIL 行是权威：`[build failed]`、测试进程 panic、超时击杀都只有
#   这一行 ⇒ v1 只认 `--- FAIL:` 的口径会把它们全部洗成「全绿」。
red_names() {
  {
    sed -n 's/^ *--- FAIL: \([^ ]*\).*/\1/p' "$1"
    awk '$1 == "FAIL" && NF > 1 { print $2 }' "$1"
  } | sort -u
}

# ── 基线红豁免表（★ 2026-11-23 12h 修订审计轮新增）────────────────────────
#
# ★ 为什么要有它（形态）：闸门此前对「基线本来就红」一律**只告警不拦**
#   （`new_reds` 为空即 exit 0）⇒ ★ **一旦某个红被并进 main，它就永远不拦路**。
#   实测：994 三方门（internal/sqlguard/migration_994_delivery_test.go）在
#   origin/main 上**实测为红** ⇒ 即使把 ./internal/sqlguard 选进包里，
#   它也**不会**阻断部署 ⇒ 副本分叉这类问题可以静悄悄地留在主干上。
#
# ★ 收紧后的语义：**基线红必须已被显式认领**——
#   · 在豁免表内且未过期 ⇒ 放行（并打印认领人/过期日/原因）；
#   · 不在表内 ⇒ **仍 die**（⇒ 基线红不会随合并而永久静音）；
#   · 在表内但**已过期** ⇒ 视同未认领 ⇒ 仍 die（⇒ 防止「一次认领、永久静音」）。
#
# ★ 表格式：TSV，每行 `<红测试名>\t<认领人>\t<过期日 YYYY-MM-DD|N/A>\t<原因>`，
#   '#' 开头为注释。红测试名的形态取自 red_names()（`TestXxx` 或 `pkg.TestXxx`）。
EXEMPTION_FILE="${SCRIPT_DIR}/deploygate/baseline_red_exemptions.tsv"

# is_exempted_red <红测试名>：在豁免表内且未过期 ⇒ 返回 0。
is_exempted_red() {
  local want="$1"
  [[ -f "$EXEMPTION_FILE" ]] || return 1
  local line name owner expiry reason today
  today="$(date +%Y-%m-%d)"
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    [[ "${line:0:1}" == "#" ]] && continue
    IFS=$'\t' read -r name owner expiry reason <<< "$line"
    [[ "$name" == "$want" ]] || continue
    if [[ -n "$expiry" && "$expiry" != "N/A" && "$expiry" < "$today" ]]; then
      err "★ 豁免已过期（${expiry} < 今天 ${today}）：${want} ⇒ 需重新认领 ★"
      return 1
    fi
    return 0
  done < <(grep -v '^#' "$EXEMPTION_FILE" 2>/dev/null || true)
  return 1
}

# assert_full_coverage <outfile> <native|docker> <tree> <subdir> [packages...]
# 每个被选中的包都必须有结果行，缺任何一行 ⇒ 失败。
# ★ 这是伪绿的主闸：runner 半途夭折（OOM/超时/断连）时「已有行」都是绿的 ⇒
#   只看红行会把「没跑完」读成「全绿」。
# ★ go list 与 go test 必须同根同 mod 标志（嵌套模块在它自己的模块根 list，
#   -mod=mod）⇒ 要不然主模块根拿不到嵌套模块的包清单 ⇒ 误报「读数不全」。
assert_full_coverage() {
  local outfile="$1" mode="$2" tree="$3" subdir="$4"; shift 4
  local import_paths
  if [[ "$mode" == "native" ]]; then
    import_paths="$( cd "$tree${subdir:+/$subdir}" && \
      GOTOOLCHAIN=local GOFLAGS="$(modflag_for "$tree" "$subdir")" "$NATIVE_GO" list "$@" 2>/dev/null || true )"
  else
    import_paths="$(docker run --rm -v "$tree":/w -w "/w${subdir:+/$subdir}" \
      -e GOFLAGS="$(modflag_for "$tree" "$subdir")" "$GO_IMAGE" go list "$@" 2>/dev/null || true)"
  fi
  if [[ -z "$import_paths" ]]; then
    die "go list 拿不到被测包清单（根=${tree}${subdir:+/$subdir}）⇒ 覆盖断言做不成 ⇒ 按失败处理"
  fi
  local p missing=0
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    if ! grep -Eq "^(ok|FAIL|\?)[[:space:]]+${p}([[:space:]]|\[|$)" "$outfile"; then
      log "★ 包 $p 没有任何结果行 ⇒ 读数不全"
      missing=1
    fi
  done <<< "$import_paths"
  if [[ $missing -ne 0 ]]; then
    printf '%s\n' '—— 测试输出尾部 ——' >&2
    tail -15 "$outfile" >&2
    die "测试读数不全（有包没有结果行）⇒ 宁可红报，不许伪绿（★ v1 缺陷④）"
  fi
}

# ── 跑一棵树：主模块 + 每个嵌套模块各跑一次，输出合并、逐箱覆盖断言 ──
# 用法：run_tree <tree> <outfile> <native|docker>   （native|docker 由调用方
# 已定的跑法决定；当前树与基线树共用。）
run_tree() {
  local tree="$1" outfile="$2" mode="$3" box sub p d
  : > "$outfile"
  for box in "" "${nested_modules[@]}"; do
    sub="$box"
    local -a pkgs=()
    if [[ -z "$box" ]]; then
      # ★ 2026-11-23 N10：★ **该树里没有的门包要跳过**（★ 不用跳过）。
      # ★ 场景：★ 基线树 = `git merge-base HEAD origin/main` ⇒ ★ 若某个门包是
      # ★   本轮才新建的，★ 基线树里**没有它** ⇒ ★ `go test ./该包` 会报
      # ★   「包不存在」而**不产出包级结果行** ⇒ ★ assert_full_coverage 判
      # ★   「读数不全」⇒ ★ **把基线跑判死**（★ 基线红只是告警、★ 不该变成拦路）。
      for p in "${packages[@]}"; do
        d="${p#./}"; d="${d%/}"
        [[ -d "$tree/$d" ]] || continue
        pkgs+=("$p")
      done
      [[ ${#pkgs[@]} -eq 0 ]] && continue
    else
      [[ -d "$tree/$box" ]] || continue
      read -r -a pkgs <<< "${nested_pkgs[$box]}"
    fi
    if [[ "$mode" == native ]]; then
      run_native "$tree" "$outfile.part" "$sub" "${pkgs[@]}" || true
    else
      run_docker "$tree" "$outfile.part" "$sub" "${pkgs[@]}" || true
    fi
    if ! readable_results "$outfile.part"; then
      log "★ ${box:-主模块}：该箱没有产出可判读的结果 ⇒ ${mode} 跑法不可用"
      return 1
    fi
    assert_full_coverage "$outfile.part" "$mode" "$tree" "$sub" "${pkgs[@]}"
    cat "$outfile.part" >> "$outfile"
  done
}

# ── 主跑：当前树 ──────────────────────────────────────────────────────
out_current="$workdir/current.txt"
use_native=0

if native_ok; then
  if run_tree "$REPO_ROOT" "$out_current" native; then
    use_native=1
  else
    log "★ 原生 go 没有产出可判读的结果 ⇒ 落 docker 兜底（原生输出尾部：$(tail -2 "$out_current" | tr '\n' ' ')）"
  fi
else
  log "★ 本机没有可用的 go ⇒ docker 兜底 ★"
fi

if [[ $use_native -ne 1 ]]; then
  if ! docker image inspect "$GO_IMAGE" >/dev/null 2>&1; then
    log "拉取测试镜像 $GO_IMAGE ..."
    docker pull "$GO_IMAGE" || die "测试镜像不可用（${GO_IMAGE}）⇒ 闸门做不成。★ 要跳过请显式给 DD_SKIP_TEST_GATE=1（★ 那次上线将没有测试证据）★"
  fi
  if ! run_tree "$REPO_ROOT" "$out_current" docker; then
    printf '%s\n' '—— 测试输出尾部 ——' >&2
    tail -15 "$out_current" >&2
    die "docker 测试没有产出可判读的结果 ⇒ 宁可红报，不许伪绿（★ v1 缺陷④a）"
  fi
fi

current_reds="$(red_names "$out_current")"

if [[ -z "$current_reds" ]]; then
  log "★ 闸门通过：${packages[*]} 全绿 ★"
  exit 0
fi

log "★ 当前树有红（$(printf '%s' "$current_reds" | tr '\n' ' ')）★ ⇒ 比对基线（${BASE_REF}）"

# ── 基线树（★ 只在有红时才跑）────────────────────────────────────────
# ★ ★ 必须走**同一个** dd_gate_base_commit（★ 而不是自己再算一次 merge-base）：
#   ★ 选包那一侧算的是「基线树 vs 当前树」的差异 ⇒ ★ 这一侧跑的就必须是
#   ★ **同一棵树** ⇒ ⇒ 否则「基线红 / 当前红」不可比 ⇒ ★ 新红会被当成旧红放行。
#   ★ ★ 两者一旦各算各的、口径漂移 ⇒ 这道门就会**静默放过新红**（★ L9 实测事故）。
base_commit="$(dd_gate_base_commit)"
if [[ -z "$base_commit" ]]; then
  printf '%s\n' '—— 当前树输出尾部 ——' >&2
  tail -40 "$out_current" >&2
  die "有红灯且拿不到基线（git merge-base HEAD $BASE_REF 失败）⇒ 无法区分新旧红灯 ⇒ 拒绝上线"
fi
base_tree="$(mktemp -d)/base"
git worktree add --detach "$base_tree" "$base_commit" >/dev/null 2>&1 || {
  die "★ 建基线 worktree 失败 ⇒ 无法区分新旧红灯 ⇒ 拒绝上线 ★"
}

out_base="$workdir/base.txt"
base_use_native=0
if [[ $use_native -eq 1 ]]; then
  if run_tree "$base_tree" "$out_base" native; then
    base_use_native=1
  else
    log "★ 基线树原生跑法没有产出可判读结果 ⇒ docker 兜底"
  fi
fi
if [[ $base_use_native -ne 1 ]]; then
  if ! run_tree "$base_tree" "$out_base" docker; then
    printf '%s\n' '—— 基线输出尾部 ——' >&2
    tail -15 "$out_base" >&2
    die "基线树测试没有产出可判读的结果 ⇒ 无法区分新旧红灯 ⇒ 拒绝上线"
  fi
fi

base_reds="$(red_names "$out_base")"

new_reds="$(comm -23 <(printf '%s\n' "$current_reds") <(printf '%s\n' "$base_reds") | grep -v '^$' || true)"

if [[ -n "$base_reds" ]]; then
  log "★ 基线（${base_commit}）**本来就红**：$(printf '%s' "$base_reds" | tr '\n' ' ')★"
  log "★   ⇒ 这些红灯**不是**本次改动带来的 ★"
  # ★ 2026-11-23（12h 修订审计轮）：从「一律告警放行」收紧为
  #   「**必须已被豁免表显式认领**」⇒ 未认领的基线红 **die**（否则基线红会随
  #   合并永久静音，★ 实测 994 三方门在 origin/main 上就是这么红的）。
  unclaimed=""
  while IFS= read -r r; do
    [[ -n "$r" ]] || continue
    if is_exempted_red "$r"; then
      log "★   ✓ ${r} ⇒ 已被豁免表认领（表：${EXEMPTION_FILE}）★"
    else
      unclaimed="${unclaimed}${r}"$'\n'
    fi
  done <<< "$base_reds"
  if [[ -n "$unclaimed" ]]; then
    printf '%s' "$unclaimed" >&2
    die "★ 基线红里存在**未被豁免表认领**的红灯（★ 见上）⇒ 拒绝上线 ★"
    die "★   认领方式：把该红测试名写进 ${EXEMPTION_FILE}（<名字>\\t<认领人>\\t<过期日>\\t<原因>）★"
  fi
fi

if [[ -z "$new_reds" ]]; then
  log "★ 闸门通过：没有**新红**（★ 现有红灯均为基线既有）★"
  exit 0
fi

log "★ **新红**（本次改动带来的）：$(printf '%s' "$new_reds" | tr '\n' ' ')★"
grep -E "^(--- FAIL|    --- FAIL|FAIL|ok)" "$out_current" | head -30 >&2 || true
die "★ 新红 ⇒ 拒绝上线（★ 红灯上线过一次：★ 2026-10-29 的容量契约测试）★"
