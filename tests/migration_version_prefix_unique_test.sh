#!/usr/bin/env bash
# migration_version_prefix_unique_test.sh —— 迁移号唯一性门（2026-11-10 第 52 增量 E1）。
#
# 背景（954/955 撞号 saga，11-09 实录）：52759157a 加 954_claude_opus_5_5_fix、
# fa81c1e37 加 955_claude_opus_5_5_multi_provider_bindings；40 分钟内另一会话以同号
# 落地 954_probe_lane_trace_events / 955_passive_model_fingerprints，最终 e3ceee172
# 删除先到者收场。生产两库实测零应用 ⇒ 未命中，但机制性风险成立：
# 版本键＝文件名数字前缀（deploy/deploy-lib/db-changelog.sh `ver=${base%%_*}`、
# 迁移自注册 `ON CONFLICT (version)`）⇒ 同号两文件时「号已应用 ⇒ 部署 pending
# 扫描按号跳过 ⇒ 新内容在该主机永不落地」（无 checksum 通道兜底）。
#
# 本门钉【树内】不变量：
#   ① sql/migrations/startup/ 下每个迁移号（前缀数字）⇒ 至多一个身份
#     （<number>_<name>.sql；.down.sql 不参与身份判定——它只是 up 的影子）。
#   ② 棘轮：≥500 号段禁止任何撞号；<500 的历史撞号（已应用迁移不可改号）
#     逐个登记在 BASELINE_DUPES，只许缩减不许新增。
#   ③ 文件名必须有前导数字号。
# 跨时间复用号段（旧文件删除后新文件顶号）在树内不可见，由 db-changelog
# 登记纪律与部署通道把守（登记为遗留，见 02 台账 E1）。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/sql/migrations/startup"
# 历史撞号基线（2026-11-10 实测 14 个；已应用迁移不可改号 ⇒ 只登记不修）。
# 新增任何一条 = 违反棘轮，门红。
BASELINE_DUPES="350 375 388 389 391 394 399 400 402 407 431 432 478 491"
FLOOR=500

pass=0; fail=0
ok()  { pass=$((pass+1)); printf '  ✅ %s\n' "$*"; }
bad() { fail=$((fail+1)); printf '  ❌ %s\n' "$*"; }

# scan_dir <dir>：打印 "NUM<TAB>NAME"（仅 up 文件；非法前缀输出 NONNUMERIC 行）
# 合法前缀（仓内既有约定）：纯数字 955 / 数字+字母小版本 361b / 数字+p+序号 478p2。
scan_dir() {
  local d="$1" f base num
  for f in "$d"/*.sql; do
    [[ -f "$f" ]] || continue
    base="$(basename "$f")"
    case "$base" in *.down.sql) continue ;; esac
    base="${base%.sql}"
    num="${base%%_*}"
    if [[ "$base" != "$num" && "$num" =~ ^[0-9]+([a-z]|[a-z]?p[0-9]+)?$ ]]; then
      # 版本键归一：子版本折叠回数字主键（361b ⇒ 361；478p2 ⇒ 478）。
      printf '%s\t%s\n' "$(LC_ALL=C sed -E 's/^([0-9]+).*/\1/' <<< "$num")" "$base"
    else
      printf 'NONNUMERIC\t%s\n' "$base"
    fi
  done
}

# dupes_of <dir>：打印 "NUM NAME NAME ..."（仅撞号项；数字序）。
# ★ 身份判定（11-10 审计 E1 复核后口径）：仓库既有约定里「主号+小写后缀」
#   （680b / 478p2 / 361b）是该主号迁移的【同族序篇/补列】——目录重命名归一
#   （682b_model_offers…_prelude 与 682_model_offers… 同族、478p2_affinity…
#   与 478_auto_route_affinity 同族）。本门的靶子是【无关身份撞同一号】
#   （954_claude_opus_5_5_fix vs 954_probe_lane_trace_events 形态），
#   不是同族序篇 ⇒ 主键折叠后只对【名字不属于该号已登记家族后缀】的算撞。
#   实现：主键 N 的身份集合里，去掉「与主迁移同名前缀」或「带家族后缀字母
#   （b/p2 等紧跟主号的子版本记法）」的成员后仍 ≥2 个无关身份 ⇒ 撞号。
#   为避免解析英文名相似度，采用保守口径：<主号><字母/p+数字> 记法一律视为
#   同族序篇（680b/361b/395b/395c/487b/487c/682b/683b/706b/707b/478p2 全数
#   逐个核过：其名均为该主号主题的延续），不参与撞号计数。
dupes_of() {
  scan_dir "$1" | sort | awk -F'\t' '
    { names[$1]=names[$1]" "$2; cnt[$1]++ }
    END {
      for (k in cnt) {
        # 拆出该主键的全部成员；「主键+紧跟字母」的子版本记法（680b/478p2）
        # 是同族序篇的既有约定 ⇒ 不算独立身份；主迁移（主键_名字）算。
        n=0; family=""
        m=split(names[k], arr, " ")
        for (i=1; i<=m; i++) {
          nm=arr[i]
          if (nm ~ ("^" k "_")) {
            n++; family=family" "nm          # 主迁移：独立身份
          } else {
            continue                          # 子版本记法：同族序篇
          }
        }
        if (n>1) printf "%s%s\n", k, family
      }
    }' | sort -n
}

allok=1
dupes="$(dupes_of "$DIR" | awk -F' ' '$1 ~ /^[0-9]+$/')"
new_dupes=""
while read -r line; do
  [[ -z "$line" ]] && continue
  num="${line%% *}"
  if (( 10#$num >= FLOOR )); then
    bad "迁移号 $num 在 ≥$FLOOR 号段撞号（新代码必须换新号）：$line"
    new_dupes="$new_dupes $num"
  fi
done <<< "$dupes"
if [[ -z "$new_dupes" ]]; then
  ok "≥$FLOOR 号段零撞号"
fi

# 棘轮：<FLOOR 撞号集合 ⊆ 基线
historical="$(dupes_of "$DIR" | awk -v f="$FLOOR" -F' ' '$1 ~ /^[0-9]+$/ && ($1+0) < f {print $1}')"
for h in $historical; do
  if ! grep -qw "$h" <<< "$BASELINE_DUPES"; then
    bad "迁移号 ${h} 在 <$FLOOR 段新出现撞号（不在基线）"
  fi
done
reaped=0
for b in $BASELINE_DUPES; do
  if ! grep -qw "$b" <<< "$historical"; then
    reaped=$((reaped+1))
    echo "  ℹ️ 基线 ${b} 已不复现（撞号被收敛，可从 BASELINE_DUPES 删除）"
  fi
done
# ★ 修法注：$reaped 后面紧跟全角括号时，本机 bash 对「变量名/多字节字符」
#   边界解析出 bug（报 reaped\ufffd unbound）⇒ 数字先落变量再拼接，别裸贴。
hist_count="$(wc -w <<< "$historical" | tr -d ' ')"
ok "历史撞号集合与基线对齐（现存 ${hist_count} 个，基线 14，已收敛 ${reaped} 个）"

# 刻意排除在编号迁移通道之外的文件（installer/startup_ddl_lock_guard_gate_test.go
# 与 deploy 通道 [0-9]*_ 过滤同口径：日期命名 = 一次性修复，不进版本序）。
# 白名单外再出现非编号文件 = 红。
NON_NUMBERED_ALLOWLIST="2026-07-13-multimodal-token-fields-hot"

nonnum="$(scan_dir "$DIR" | awk -F'\t' '$1=="NONNUMERIC"{printf "%s\n", $2}')"
viol=""
for f in $nonnum; do
  if ! grep -qw "$f" <<< "$NON_NUMBERED_ALLOWLIST"; then
    viol="$viol $f"
  fi
done
if [[ -z "${viol// /}" ]]; then
  ok "非编号迁移文件均在白名单（${nonnum// /→}）"
else
  bad "非编号且未豁免的迁移文件：${viol}（编号迁移必须 <数字>[_b|_p2]_<name>.sql 形态）"
fi

# ── 变异臂：合成夹具证明检测真的有牙 ────────────────────────────────
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture"
: > "$fixture/900_alpha.sql"
: > "$fixture/900_alpha.down.sql"
: > "$fixture/900_beta.sql"        # 同号不同身份 ⇒ 必须判红
: > "$fixture/901_gamma.sql"
fx_dupes="$(dupes_of "$fixture")"
if [[ "$fx_dupes" == "900 900_alpha 900_beta" ]]; then
  ok "变异臂：同号 900_alpha/900_beta 被检出且 down.sql 不误报"
else
  bad "变异臂失效：got「${fx_dupes}」want「900 900_alpha 900_beta」"
fi
fx_down="$(scan_dir "$fixture" | awk -F'\t' '$2 ~ /alpha\.down/ {print "leak"}')"
if [[ -z "$fx_down" ]]; then
  ok "变异臂：down.sql 不参与身份判定"
else
  bad "变异臂失效：down.sql 混入身份"
fi

# 变异臂②：子版本记法（680b 形态）不得被记为独立身份（同族序篇约定）
# ★ 先清掉变异臂①留下的撞号样本 900_beta（不清理会持续触发 dupes）
rm -f "$fixture/900_beta.sql"
: > "$fixture/902_main.sql"
: > "$fixture/902b_prelude.sql"
fx_fam="$(dupes_of "$fixture")"
if [[ -z "$fx_fam" ]]; then
  ok "变异臂：902b_ 同族序篇不误报为撞号"
else
  bad "变异臂失效：同族序篇被误判（${fx_fam}）"
fi
# 变异臂③：非编号文件（无下划线的日期名）必须被判 NONNUMERIC
: > "$fixture/2026-01-02-something.sql"
fx_nn="$(scan_dir "$fixture" | awk -F'\t' '$1=="NONNUMERIC"' | wc -l | tr -d ' ')"
if [[ "$fx_nn" == "1" ]]; then
  ok "变异臂：日期命名文件被判 NONNUMERIC"
else
  bad "变异臂失效：NONNUMERIC 检测无牙（got ${fx_nn}）"
fi

printf 'PASS=%d FAIL=%d\n' "$pass" "$fail"
[[ $fail -eq 0 && $allok -eq 1 ]]
