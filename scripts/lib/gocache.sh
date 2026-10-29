#!/usr/bin/env bash
# gocache.sh —— 仓内【专用 Go 构建缓存】的 shell 侧入口（★ r1047 轮 · 2026-10-28）
#
# ★ 用法（★ 两种，★ 任选其一）：
#
# ① **直接执行**（★ 推荐：★ ★★ 深度无关 —— ★ 它自己找仓根）：
#
#     export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"
#
# ② **source 进来**（★ 适合要连续用几个用途的脚本）：
#
#     . scripts/lib/gocache.sh        # ★ 引入本文件
#     gocache_use mutate               # ★ 导出 GOCACHE=<仓根>/.build-local/.gocache-mutate
#
# ★★ **为什么需要它**：★★
#
# ★★★ **背景**：★★ 仓里的「变异验证脚本」都是【**改树 ⇒ 跑 go test ⇒ 还原**】
# ★★★ ⇒ ⇒ ★★ 而 Go 的构建缓存是**内容寻址**的（★ action ID = 全部输入内容 +
# ★★★ 构建配置的哈希）⇒ ⇒ ★★ **它不可能返回陈旧的产物** ⇒ ⇒ ★★ 但它**会无限增长**：
# ★★★ **树每变一次**（★ 改一行代码、合并一次、换一个分支……）⇒ ⇒ ★★★ 编译产物的
# ★★★ action ID 就全变 ⇒ ⇒ ★★★ ⇒ 旧的那些**永远不会再被命中** ⇒ ⇒ ★★★ ⇒ **纯垃圾**
# ★★★ ⇒ ⇒ ★★★ 而默认那个缓存是**用户那个全局的**（★ 跨项目、跨天、Go 只按 5 天 trim）
# ★★★ ⇒ ⇒ ★★★ ⇒ **磁盘被撑爆** ⇒ ⇒ ★★★ ⇒ 全量 `go test ./...` **编译阶段**就红
# ★★★ ⇒ ⇒ ★★★ ⇒ ★★ **红的原因不指向真正的原因**（★ 真正的原因是磁盘，不是门有洞）。
#
# ★★★★ **实测（★ 2026-10-28，本机）**：★★ `sql/schema` 那道门（★ `go vet
# ★★★★ -tags=integration ./...` ⇒ ⇒ **整棵树带 tag 重编一遍**）⇒ ⇒ ★★ **一次**就把
# ★★★★ 专用缓存撑到 **1.1 GB** ⇒ ⇒ ★★ 树每变一次就再来一批 ⇒ ⇒ ★★★ ⇒ **专用缓存
# ★★★★ 自己也会撑爆磁盘**（★ 只是换个地方撑爆）⇒ ⇒ ★★ ⇒ **所以「隔离」必须配一个上限**。
#
# ★★ **为什么放在仓内 `.build-local/`**：★★ ① 它**已经在 `.gitignore` 里**
# ★★ （★ `.gitignore:42` ⇒ `.build-local/`）⇒ ⇒ **绝不会被提交进仓库**；
# ★★ ② 它跟着仓走 ⇒ ⇒ `git clean -xfd` 能顺手回收它；★ ③ 它是**仓库内**的路径
# ★★ ⇒ ⇒ 与 `scripts/deploy-seamless.sh` 等既有 `.build-local/.gocache` 的约定**同款**。
#
# ★★ **与 Go 侧的关系**：★★ Go 侧有 `internal/gocache`（★ `Dir` / `Env` / `Prune`）
# ★★ ⇒ ⇒ ★★ **两边共用同一个目录约定与同一个上限（6 GB）** ⇒ ⇒ ★★ ⇒ **一份真值**。
#
# ★★ **为什么上限取 6 GB**：★★ 与掏空门那套同档（★ 掏空门每跑一次涨 ~1.3 GB
# ★★ ⇒ ⇒ 6 GB ≈ 4~5 次新树触发一次清理）⇒ ⇒ ★★ 清理后下一轮是**冷编译**
# ★★ （★ 实测冷 213.35s vs 热 153.99s）⇒ ⇒ ★★ **代价可接受**，★ 而「磁盘被撑爆」
# ★★ **不可接受**。

# GOCACHE_MAX_BYTES 是【专用缓存】的尺寸上限（★ 6 GB）。
#
# ★ ★★ **与 `internal/gocache.MaxBytes` 同值**：★★ 两边是【同一个约定】的两条腿
# ★ ★★ （★ 一条 shell、一条 Go）⇒ ⇒ ★★ ⇒ **改一处就要改另一处** ⇒ ⇒ ★★ ⇒
# ★ ★★ **新脚本一律用 `internal/gocache` 或本文件**，★ 不要自己拼路径。
GOCACHE_MAX_BYTES=$((6 * 1024 * 1024 * 1024))

# gocache_use <purpose> —— 把 GOCACHE 指向【专用目录】，并**把住尺寸**。
#
# ★ 用法：★ `gocache_use mutate` ⇒ ⇒ `GOCACHE=<仓根>/.build-local/.gocache-mutate`
#
# ★ ★★ **为什么「超限就整个删掉」而不是「删一部分」**：★★ Go 的构建缓存
# ★ ★★ **不支持按条目删除** ⇒ ⇒ ★ 只能全清 ⇒ ⇒ ★★ 这也是「专用」的价值：
# ★ ★★ **全清的代价被限制在一个专用目录里**（★ 而不是用户的全局缓存 —— ★ 全清那个
# ★ ★★ 要重编整个仓的一切）。
#
# ★ ★★ **剪枝失败不致命**：★★ 只打印 ⇒ ⇒ ★ 与仓内「★ 清理是卫生问题，★ 不该让它
# ★ ★★ 变成判据问题」那条纪律**同款** ⇒ ⇒ ★★ ⇒ **脚本照常跑**。
gocache_use() {
  local purpose="${1:-default}"
  local root
  # ★★ **深度无关**：★★ 优先问 git（★ ⇒ ⇒ 调用方在哪个目录都行）
  # ★★ ⇒ ⇒ ★★ 回落才是「按本文件位置推」（★ ⇒ ⇒ 本文件在 scripts/lib/ ⇒ ⇒ 两层上）。
  root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -z "$root" ]; then
    root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  fi
  local dir="$root/.build-local/.gocache-$purpose"

  # ★★★★ **失败安全**（★ r1047 轮加的 · ★ r1048 轮补齐）：★★★★ 专用目录
  # ★★★★ **建不出来**或**不可写**时 ⇒ ⇒ ★★★★ **不许 export GOCACHE** ⇒ ⇒ ★★★★
  # ★★★★ 因为 go 拿到一个**不可用**的 GOCACHE 会直接报
  # ★★★★ `failed to initialize build cache` ⇒ ⇒ ★★★★ ⇒ **脚本直接坏掉**
  # ★★★★ ⇒ ⇒ ★★★★ 而「脚本坏掉」比「这次没隔离」**严重得多** ⇒ ⇒ ★★★★ ⇒
  # ★★★★ ⇒ **宁可漏隔离，也不许把脚本弄坏**（★ 与仓内「★ 清理是卫生问题，
  # ★★★★ ★★ 不该让它变成判据问题」那条纪律**同款**）。
  # ★ ★★ **为什么「不可写」也要查**（★ r1048 轮运行期实测逼出来的）：★★ ★★ 只查
  # ★ ★★ 「目录在不在」是**不够的** ⇒ ⇒ ★★ 因为目录**早就建好了**（★ 上一次跑过）
  # ★ ★★ ⇒ ⇒ ★★ `mkdir -p` 会**成功**（★ 幂等）⇒ ⇒ ★★ ⇒ 「在不在」这一条**判绿**
  # ★ ★★ ⇒ ⇒ ★★ ⇒ 而那个目录**其实不可写** ⇒ ⇒ ★★ ⇒ go 照样会报错 ⇒ ⇒ ★★ ⇒
  # ★ ★★ ⇒ **脚本照样坏掉** ⇒ ⇒ ★★ ⇒ 所以必须查**可写性**（★ `-w`）。
  if ! mkdir -p "$dir" 2>/dev/null || [ ! -d "$dir" ] || [ ! -w "$dir" ]; then
    echo "★ 专用构建缓存 $dir 建不出来或不可写 ⇒ ⇒ 本次**不设** GOCACHE（★ 回落用环境里的那个 ⇒ ⇒ 脚本照常跑）" >&2
    return 0
  fi

  # ★ 把住尺寸（★ 每个脚本跑一次 ⇒ 代价可接受）。
  local kb
  kb=$(du -sk "$dir" 2>/dev/null | awk '{print $1}')
  if [ -n "$kb" ] && [ "$kb" -gt $((GOCACHE_MAX_BYTES / 1024)) ]; then
    echo "★ 专用构建缓存 $dir 超上限 ⇒ 整个删掉重建（★ 这轮是冷编译）" >&2
    rm -rf "$dir" || echo "★ 删专用构建缓存失败（★ 不影响结果）" >&2
    mkdir -p "$dir" 2>/dev/null || true
  fi

  # ★ ★★ **无条件覆盖**（★ 不是「仅当没设时才设」）：★★ Go `exec` 的语义是
  # ★ ★★ 「**后出现的同名变量赢**」⇒ ⇒ ★★ ⇒ 即便用户环境里已经设了 `GOCACHE`，
  # ★ ★★ 子进程也会用专用目录（★ ★★ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ ★★ **悄悄回落
  # ★ ★★ 用全局缓存** ⇒ ⇒ ★★ ⇒ **门绿着，而隔离根本没生效**）。
  export GOCACHE="$dir"
}

# ★★ **直接执行**（★ `gocache.sh <purpose>`）⇒ ⇒ 剪枝并**打印** GOCACHE 路径。
# ★★ 供调用方 `export GOCACHE="$(…/gocache.sh mutate)"` 使用 ⇒ ⇒ **深度无关**。
# ★★
# ★★ **为什么这个形态【不】做「失败就不打印」**：★★ ★★ 因为 r1047 轮已经用这个形态
# ★★ 接了 **15 个 shell 文件** ⇒ ⇒ ★★ 而「不打印」⇒ ⇒ 调用方拿到
# ★★ `GOCACHE=""` ⇒ ⇒ ★★ go 会报 `GOCACHE is not an absolute path` ⇒ ⇒ ★★ **脚本坏掉**
# ★★ ⇒ ⇒ ★★ ⇒ **静默地把已接好的脚本弄坏，比「没隔离」严重得多** ⇒ ⇒ ★★ ⇒
# ★★ ⇒ 所以这里【照旧打印】⇒ ⇒ ★★ 而「失败安全」交给**函数形态**
# ★★ （★ `gocache_use` ⇒ ⇒ 建不出来就**不 export** ⇒ ⇒ 调用方的 go 子进程
# ★★ ⇒ ⇒ 照常用环境里那个 ⇒ ⇒ **脚本照常跑**）。
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  _gocache_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -z "$_gocache_root" ]; then
    _gocache_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  fi
  _gocache_purpose="${1:-default}"
  _gocache_dir="$_gocache_root/.build-local/.gocache-$_gocache_purpose"
  if ! mkdir -p "$_gocache_dir" 2>/dev/null || [ ! -d "$_gocache_dir" ]; then
    echo "★ 专用构建缓存 $_gocache_dir 建不出来 ⇒ ⇒ 本次隔离**可能不生效**（★ 不影响脚本继续跑）" >&2
  fi
  _gocache_kb=$(du -sk "$_gocache_dir" 2>/dev/null | awk '{print $1}')
  if [ -n "$_gocache_kb" ] && [ "$_gocache_kb" -gt $((GOCACHE_MAX_BYTES / 1024)) ]; then
    echo "★ 专用构建缓存 $_gocache_dir 超上限 ⇒ 整个删掉重建（★ 这轮是冷编译）" >&2
    rm -rf "$_gocache_dir" || echo "★ 删专用构建缓存失败（★ 不影响结果）" >&2
    mkdir -p "$_gocache_dir" 2>/dev/null || true
  fi
  printf '%s\n' "$_gocache_dir"
fi
