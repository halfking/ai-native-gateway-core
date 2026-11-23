#!/usr/bin/env bash
# =====================================================================
# scripts/lib/deploy-surface.sh —— 「部署通道投递面」解析的**唯一真源**
#
# ★★ 为什么要有这个库（★ 2026-11-24 L34 七轮 ★★）★★
#   ★ 仓里有两道门都要解析**同一个东西**：★ 通道脚本 `apply-db-revision-sequence.sh`
#     ★ 里 `files=()` 那个数组 ⇒ ★★ **「通道真正会 apply 的文件集」★★
#   ★ · `scripts/checks/deploy-ledger-coverage-gate.sh`（★ 覆盖门：★ 投递面 ↔ 权威账 ★★）
#   ★ · `scripts/checks/legacy-select-star-column-order-gate.sh`（★ 列序对拍门 ★★）
#   ★ ⇒ ★★ **同一口径写两份 ⇒ 迟早分叉**（★ 仓内既有教训：★ 「同一条规定写了两份」
#     ★ ★ ⇒ ★ 两边各改各的 ⇒ ★ 门与门开始互相矛盾 ★★）★★
#   ★ ⇒ ★★ **⇒ 抽成共享库 ⇒ ★ 分叉在结构上不可能** ★★（★ 而不是靠约定 ★★）★★
#
# ★★ 口径（★ 与仓内 Go 门 `deployChannelStartupOrder` 同口径、★ 与 L34 二轮实测一致 ★★）★★
#   ★ 逐行解析 `files=(` … `)`｜★ **只取未被注释的行**（★ `#` 开头 ⇒ 不是投递面 ★★）
#   ★ ｜★ 剥 `$ROOT_DIR/sql/migrations/startup/` 前缀（★ **用 index+substr、★ 不用正则** ★★
#     ★ ⇒ ★ 零 awk 方言依赖：★ mawk（Linux 生产）与 BSD awk（macOS）行为一致 ★★）
#   ★ ｜★ **去重且保留首次位置**（★ 800 出现两次 ⇒ 首次才是投递起点 ★★）
#   ★ ｜★ **跳过 `.down.sql`**（★ 回滚文件不是投递面 ★★）
#
# ★★ 用法 ★★
#   source scripts/lib/deploy-surface.sh
#   deploy_surface_extract "scripts/apply-db-revision-sequence.sh" "$out_file"
#   ★ ⇒ `$out_file` 每行一个 basename、★ 顺序＝投递顺序 ★★
#   deploy_surface_position "$out_file" "<basename>" ⇒ ★ 打印位次（1 起）★ ★ 不在 ⇒ 空 ★★
# =====================================================================

# ★★ deploy_surface_extract <deploy_script> <out_file> ★★
# ★ 输出：投递面文件 basename 列表（每行一条、★ 顺序＝投递顺序 ★★）
# ★ 退出码：0＝成功；★ **1＝解析出 0 条**（★ 调用方应据此判 ABORT、★ 不许当通过 ★★）
deploy_surface_extract() {
  local deploy_script="$1"
  local out_file="$2"
  [ -f "$deploy_script" ] || return 2
  awk '
    BEGIN { in_files = 0 }
    {
      line = $0
      if (!in_files) {
        if (line ~ /^files=\(/) { in_files = 1 }
        next
      }
      if (line ~ /^\)/) { in_files = 0; next }
      # ★★ 注释行（★ `#` 开头 ★★）⇒ ★ **不是投递面** ★★
      trimmed = line
      sub(/^[ \t]+/, "", trimmed)
      if (trimmed ~ /^#/ || trimmed == "") { next }
      # ★ 只取「整行只有这一条引号路径」的写法（★ 通道脚本的实际形态 ★）
      pfx = "$ROOT_DIR/sql/migrations/startup/"
      p = index(line, pfx)
      if (p > 0) {
        rest = substr(line, p + length(pfx))
        sub(/^[ \t"]+/, "", rest)
        sub(/[\t"]+$/, "", rest)
        # ★ 只接受「纯文件名 + .sql」形态（★ 排除带参数的行 ★）
        if (rest ~ /^[A-Za-z0-9_.-]+[.]sql$/) { print rest }
      }
    }
  ' "$deploy_script" | awk '!seen[$0]++' | grep -v '\.down\.sql$' >"$out_file" || true
  local n
  n="$(wc -l <"$out_file" | tr -d ' ')"
  if [ "${n:-0}" -eq 0 ]; then
    return 1
  fi
  return 0
}

# ★★ deploy_surface_position <surface_file> <basename> ★★
# ★ 输出：该文件在投递面里的位次（★ 1 起、★ 按首次出现计 ★★）
# ★ 不在投递面里 ⇒ **输出空、退出码 1**（★ 调用方据此说「本次部署不会碰它」 ★★）
deploy_surface_position() {
  local surface_file="$1"
  local base="$2"
  awk -v want="$base" '
    { if (!found && $0 == want) { print NR; found = 1 } }
  ' "$surface_file"
}
