#!/usr/bin/env bash
# =====================================================================
# tests/migration_delivery_boundary_convergence_test.sh
#
# ★ 两条建库通道的【边界收敛】门（★ 审计 F48 · 2026-11-10 ★）
#
# ★ 这道门为什么存在
#
# 仓里有**两条**建库通道：
#   ① **全新安装链**（installer/internal/dbinit/runner.go 的 `StartupFiles`
#      ⇒ ★ 它跑 embeddata/startup 里的文件、★ 共 357 条 ★）
#   ② **存量升级通道**（deploy-lib/db-changelog.sh 的
#      `_deploy_pending_startup_migrations` ⇒ ★ 它扫 sql/migrations/startup/ ★）
#
# ⇒ 过去**两个地板是同一个常量**（`DB_LEDGER_RECONCILE_FROM:-412` ⇒ ★ 它同时
#   决定了「投递面」与「checksum 对账面」 ★）⇒ ⇒ 两条通道的边界**卡死在 412** ★
#
# ★ ★★ 这个地板的**实测代价**（★ 台账快照 ★）：
#   · 211 / local：**12** 条 <412 的迁移**已应用** ★
#   · 252：**0** 条 ⇒ ★ ★ **同一条迁移在两台机器上终态不同** ★
#   ⇒ ⇒ 而 358 建的 session_owners / session_owner_stats 正是
#     admin/user_profile.go:327-329 记的「★ 生产共享库缺 session_owners ★」
#
# ★ 本轮（F48）落成的修法：★ **投递面**与**对账地板**解耦 ★
#   · 对账地板**不动**（★ 412 以下是**有意豁免**的历史区 ⇒ ★ 降它 ⇒
#     checksum 闸门硬拒、★ 整条部署中止 ★）
#   · 新增**低版本投递区**（★ 仓内清单 scripts/migration-low-version-delivery.list ★）
#     ⇒ ★ 清单里的文件**即使版本号低于 412**、★ 只要账本无记录也**会被投递** ★
#
# ★ ★★ 门管八件事（★ 逐条机器可判 ★）：
#   ① 派生面（★ 快照 ∪ 安装器链 ★）**非空**（★ 判据面不许是空的 ★）
#   ② 派生面与「清单 ∪ 豁免」**逐条对账**：★ 不多、★ 不少（★ 漂移即红 ★）
#   ③ 清单里每个文件**存在**、★ 版本号与文件名**一致**、★ 与 embeddata
#      副本**字节一致**（★ 有副本时 ★）
#   ④ 豁免每条**都有理由**、★ 且**自证**（★ 032 的文件确实不存在 ★、
#      ★ bootstrap 的文件里确实写着「不许用于存量库」 ★）
#   ⑤ 有效部署库与仓内 legacy 副本**都有**低版本投递机制、★ 且**对账地板
#      仍是 412**（★ 没被误降 ★）
#   ⑥ 两条部署路径的低版本机制**逐字同判**（★ 不许漂移 ★）
#   ⑦ 门自己有牙（★ **变异臂**：★ 清单少一条 ⇒ ②红；★ 对账地板被降 ⇒ ⑤红 ★）
#
# ★ 退出码：0 全绿；1 有违例。
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ★ 清单与豁免都**可覆盖**（★ 供夹具与变异臂用 ★）
LIST_FILE="${MIGRATION_LOW_VERSION_DELIVERY_LIST:-scripts/migration-low-version-delivery.list}"
EXEMPT_FILE="${MIGRATION_LOW_VERSION_DELIVERY_EXEMPTIONS:-scripts/migration-low-version-delivery.exemptions}"
SNAPSHOT="scripts/audit/asset/db-changelog-ledger-snapshot.tsv"
RUNNER="installer/internal/dbinit/runner.go"
RECONCILE_FLOOR=412

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ── 抽取器 A：★ 台账快照里「某台机台已应用、而版本号低于地板」的那几条 ──
# ★ 快照的列：机台 / 版本 / 迁移文件名 / 时间戳 ★
snapshot_low_versions() {
  [[ -f "$SNAPSHOT" ]] || return 0
  awk -F'\t' -v floor="$RECONCILE_FLOOR" '
    /^#/ { next }
    NF >= 3 && $2 ~ /^[0-9]+$/ && $2 + 0 < floor { print $3 }
  ' "$SNAPSHOT" | sort -u
}

# ── 抽取器 B：★ 安装器链 StartupFiles 里版本号低于地板的那几条 ──────────
# ★ ★★ 逐行**剥掉行注释**（★ Go 源码里 `//` 之后是注释 ★）⇒ ★ 注释里提到
#   的文件名**不会**被当成登记项 ★ ⇒ ★ 然后只取**整行就是这个文件名**的
#   那种行（★ StartupFiles 的元素在仓里是**逐行一个**的 ★）
installer_low_files() {
  [[ -f "$RUNNER" ]] || return 0
  awk '
    # ★ 逐行剥注释（★ 字符串字面量里的引号要跟着走 ★）
    function strip(line,   i,n,c,out,ins) {
      out=""; ins=0; n=length(line)
      for (i=1;i<=n;i++) {
        c=substr(line,i,1)
        if (ins) {
          if (c=="\\") { out=out c substr(line,i+1,1); i++; continue }
          if (c=="\"") { ins=0; out=out "\""; continue }
          out=out c; continue
        }
        if (c=="\"") { ins=1; out=out "\""; continue }
        if (c=="/" && substr(line,i+1,1)=="/") break
        out=out c
      }
      return out
    }
    /StartupFiles: \[\]string\{/ { inf=1; next }
    inf && /^\t\t\},?$/ { exit }
    inf {
      s=strip($0)
      while (match(s, /"[^"]*\.sql"/)) {
        print substr(s, RSTART+1, RLENGTH-2)
        s=substr(s, RSTART+RLENGTH)
      }
    }
  ' "$RUNNER" | sort -u | awk -v floor="$RECONCILE_FLOOR" '
    function ver(s,   m) { m=s; sub(/[^0-9].*$/, "", m); return (m=="") ? 0 : m+0 }
    { if (ver($0) < floor) print }
  '
}

# ── 抽取器 C：★ 清单与豁免（★ 逐行整串匹配、★ 注释与空行忽略 ★）────────
list_entries() {
  [[ -f "$1" ]] || return 0
  grep -vE '^[[:space:]]*(#|$)' "$1" | awk -F'\t' '{print $1}' | sed 's/[[:space:]]*$//' | sort -u
}
exempt_entries() {
  [[ -f "$1" ]] || return 0
  grep -vE '^[[:space:]]*(#|$)' "$1" | awk -F'\t' '{print $1}' | sed 's/[[:space:]]*$//' | sort -u
}

echo "ℹ️ 清单：$LIST_FILE"
echo "ℹ️ 豁免：$EXEMPT_FILE"
echo

# ── 断言 ①：★ 派生面非空（★ 判据面不许是空的 ★）────────────────────
snapshot_set="$WORK/snapshot.txt"; installer_set="$WORK/installer.txt"
derived_set="$WORK/derived.txt"
snapshot_low_versions > "$snapshot_set"
installer_low_files  > "$installer_set"
cat "$snapshot_set" "$installer_set" | sort -u > "$derived_set"

n_snapshot=$(wc -l < "$snapshot_set" | tr -d ' ')
n_installer=$(wc -l < "$installer_set" | tr -d ' ')
n_derived=$(wc -l < "$derived_set" | tr -d ' ')
if [[ "$n_derived" -ge 1 && "$n_snapshot" -ge 1 && "$n_installer" -ge 1 ]]; then
  ok "① 派生面非空（★ 台账快照 $n_snapshot 条 ＋ 安装器链 $n_installer 条 ⇒ 并集 $n_derived 条）"
else
  bad "① 派生面是空的（★ 快照 $n_snapshot 条 / 安装器链 $n_installer 条）⇒ 判据面失效"
fi

# ── 断言 ②：★ 派生面与「清单 ∪ 豁免」逐条对账（★ 不多不少 ★）────────
list_set="$WORK/list.txt"; exempt_set="$WORK/exempt.txt"
list_entries "$LIST_FILE" > "$list_set"
exempt_entries "$EXEMPT_FILE" > "$exempt_set"
covered_set="$WORK/covered.txt"
cat "$list_set" "$exempt_set" | sort -u > "$covered_set"

missing="$WORK/missing.txt"      # 派生面里没被覆盖的
extra="$WORK/extra.txt"          # 覆盖面里不该有的
comm -23 "$derived_set" "$covered_set" > "$missing.txt"
comm -13 "$derived_set" "$covered_set" > "$extra.txt"
n_missing=$(wc -l < "$missing.txt" | tr -d ' ')
n_extra=$(wc -l < "$extra.txt" | tr -d ' ')

if [[ "$n_missing" -eq 0 ]]; then
  ok "②a 派生面全覆盖（★ 派生 $n_derived 条、★ 清单 $(( $(wc -l < "$list_set" | tr -d ' ') )) 条 ＋ 豁免 $(( $(wc -l < "$exempt_set" | tr -d ' ') )) 条）"
else
  bad "②a 派生面有 $n_missing 条没被覆盖（★ 清单/豁免漂移了）"
  sed 's/^/      ❌ /' "$missing.txt" >&2
fi
if [[ "$n_extra" -eq 0 ]]; then
  ok "②b 覆盖面没有多余项（★ 清单 ∪ 豁免 == 派生面）"
else
  bad "②b 覆盖面有 $n_extra 条不在派生面里（★ 清单多收了）"
  sed 's/^/      ❌ /' "$extra.txt" >&2
fi

# ── 断言 ③：★ 清单里每个文件存在、版本号一致、与 embeddata 字节一致 ──
bad3=0
while IFS= read -r f; do
  [[ -n "$f" ]] || continue
  src="sql/migrations/startup/$f"
  if [[ ! -f "$src" ]]; then
    bad "③ 清单里的 $f 在 sql/migrations/startup/ 里不存在（★ 无源可投）"
    bad3=$((bad3 + 1)); continue
  fi
  ver="${f%%_*}"
  # ★ 版本号前缀：★ 允许**字母后缀**（★ 例 `361b_provider_models_source_prelude`
  #   ★ 台账里记的版本号是 361 ★）⇒ ★ 但**数字部分必须能解析** ★
  if [[ ! "$ver" =~ ^[0-9]+[a-z]?$ ]]; then
    bad "③ 清单里的 $f 版本号前缀不可解析（★ 期望 3 位数字、★ 可带字母后缀）"
    bad3=$((bad3 + 1)); continue
  fi
  vnum="${ver%%[a-z]*}"
  # ★ ★ 必须是 **3 位**、★ 且**低于**对账地板（★ 10# 强制十进制：★ 前导零的
  #   ★ 数字在 bash 算术里是**八进制** ⇒ `(( 08 ))` 会直接报错 ★）
  if [[ "${#vnum}" -ne 3 ]] || (( 10#$vnum >= RECONCILE_FLOOR )); then
    bad "③ 清单里的 ${f} 版本号 ${vnum} 不合法（★ 低版本区应当是 3 位、★ 且低于 ${RECONCILE_FLOOR}）"
    bad3=$((bad3 + 1)); continue
  fi
  emb="installer/cmd/llm-gw-installer/embeddata/startup/$f"
  if [[ -f "$emb" ]]; then
    if ! cmp -s "$src" "$emb"; then
      bad "③ 清单里的 $f 与 embeddata 副本**字节不一致**（★ 两条通道会跑不同的内容）"
      bad3=$((bad3 + 1))
    fi
  fi
done < "$list_set"
if [[ "$bad3" -eq 0 ]]; then
  ok "③ 清单里每个文件都存在、★ 版本号可解析、★ 与 embeddata 副本字节一致"
fi

# ── 断言 ④：★ 豁免每条都有理由、且**自证** ─────────────────────────
bad4=0
while IFS= read -r line; do
  [[ -n "$line" ]] || continue
  f="${line%%$'\t'*}"
  reason="${line#*$'\t'}"
  if [[ "$reason" == "$line" || -z "${reason//[[:space:]]/}" ]]; then
    bad "④ 豁免 $f 没有写理由（★ 豁免必须自证）"
    bad4=$((bad4 + 1))
  fi
done < <(grep -vE '^[[:space:]]*(#|$)' "$EXEMPT_FILE" 2>/dev/null || true)
# ★ 032：★ 自证 = 仓里确实没有它的 canonical 文件 ★
if grep -qE '^032_session_tenant_binding\.sql($|\t)' "$EXEMPT_FILE" 2>/dev/null; then
  if [[ ! -f "sql/migrations/startup/032_session_tenant_binding.sql" ]]; then
    ok "④a 032 的豁免自证成立（★ 仓里确实没有它的 canonical 文件 ⇒ 无源可投）"
  else
    bad "④a 032 被豁免、★ 但它的 canonical 文件**在仓里**（★ 豁免理由已过期）"
    bad4=$((bad4 + 1))
  fi
else
  bad "④a 豁免表里没有 032（★ 它在 211/local 上被应用过 ⇒ 252 缺它 ⇒ 必须有交代）"
  bad4=$((bad4 + 1))
fi
# ★ bootstrap：★ 自证 = 文件里那句「不许用于存量库」还在 ★
BOOT="installer/cmd/llm-gw-installer/embeddata/startup/session_turns_hot_bootstrap.sql"
# ★ ★ 那句声明在文件里**跨了两行**（★ `must not be applied` ／ `to an existing
#   deployment.` ★）⇒ ⇒ 必须先把换行压成空格再匹配（★ 否则自证永远不成立 ★）
if grep -qE '^session_turns_hot_bootstrap\.sql($|\t)' "$EXEMPT_FILE" 2>/dev/null; then
  # ★ ★ 那句声明**跨了两行**、★ 而续行还带 `-- ` 前缀 ⇒ ⇒ 先去掉注释前缀、
  #   ★ 再把换行压成空格（★ 否则这句自证永远不成立 ★）
  if [[ -f "$BOOT" ]] && sed 's/^[[:space:]]*--[[:space:]]*//' "$BOOT" | tr '\n' ' ' | grep -qi 'must not be applied to an existing deployment'; then
    ok "④b bootstrap 的豁免自证成立（★ 文件里那句「不许用于存量库」还在）"
  else
    bad "④b bootstrap 的豁免自证**不成立**（★ 文件缺失、★ 或那句声明没了）"
    bad4=$((bad4 + 1))
  fi
else
  bad "④b 豁免表里没有 session_turns_hot_bootstrap.sql（★ 安装器专用文件必须显式交代）"
  bad4=$((bad4 + 1))
fi
if [[ "$bad4" -eq 0 ]]; then
  ok "④c 每条豁免都写了理由（★ 豁免不许是空口）"
fi

# ── 断言 ⑤：★ 有效部署库与 legacy 副本都有机制、且对账地板仍是 412 ───
# ★ ★ 有效部署库按**候选链**解析（★ scripts/deploy-lib-candidates.sh 是唯一
#   真相、★ 门不许写死布局名 ★）
# shellcheck source=../scripts/deploy-lib-candidates.sh
source "$REPO_ROOT/scripts/deploy-lib-candidates.sh"
SHARED_LIB=""
if SHARED_LIB="$(deploy_lib_find db-changelog.sh 2>/dev/null)"; then
  ok "⑤a 有效部署库解析成功（★ ${SHARED_LIB}）"
else
  bad "⑤a 解析不到有效部署库（★ 候选链一个都没命中 ⇒ 没法证明两条部署路径不漂移）"
  SHARED_LIB=""
fi
LEGACY="scripts/deploy-lib.legacy/db-changelog.sh"

check_lib() {
  local label="$1" path="$2" rc=0
  # ★ 候选链返回的是**目录**（★ 里面才有 db-changelog.sh ★）
  local lib="$path"
  [[ "$lib" == */db-changelog.sh ]] || lib="$path/db-changelog.sh"
  [[ -n "$path" && -f "$lib" ]] || { bad "⑤b ${label} 的 db-changelog.sh 不存在（★ ${path}）"; return 1; }
  if ! grep -q '_deploy_low_version_delivery_allowed' "$lib"; then
    bad "⑤b $label 没有低版本投递机制（★ 找不到 _deploy_low_version_delivery_allowed）"
    rc=1
  fi
  if ! grep -q 'DB_MIGRATION_LOW_VERSION_DELIVERY_LIST' "$lib"; then
    bad "⑤c $label 的低版本机制不读清单变量（★ DB_MIGRATION_LOW_VERSION_DELIVERY_LIST）"
    rc=1
  fi
  # ★ ★ 对账地板必须**仍然是 412**（★ 降它 ⇒ checksum 闸门硬拒 ⇒ 部署中止 ★）
  if ! grep -q 'DB_LEDGER_RECONCILE_FROM:-412' "$lib"; then
    bad "⑤d $label 的对账地板不再是 412（★ 412 以下是**有意豁免**的历史区 ⇒ 降它会让部署硬拒）"
    rc=1
  fi
  return $rc
}
rc=0
if [[ -n "$SHARED_LIB" ]]; then check_lib "有效部署库" "$SHARED_LIB" || rc=1; fi
check_lib "仓内 legacy 副本" "$LEGACY" || rc=1
if [[ $rc -eq 0 ]]; then
  ok "⑤e 低版本投递机制**到位**、★ 而**对账地板仍是 412**（★ 两个地板已解耦）"
fi

# ── 断言 ⑥：★ 两条部署路径的低版本机制**逐字同判** ──────────────────
SHARED_LIB_FILE=""
[[ -n "$SHARED_LIB" ]] && SHARED_LIB_FILE="$SHARED_LIB/db-changelog.sh"
[[ -f "$SHARED_LIB_FILE" ]] || SHARED_LIB_FILE=""
if [[ -n "$SHARED_LIB_FILE" && -f "$LEGACY" ]]; then
  extract_mech() {
    awk '/^_deploy_low_version_delivery_allowed\(\) \{/,/^\}/' "$1"
  }
  if diff -q <(extract_mech "$SHARED_LIB_FILE") <(extract_mech "$LEGACY") >/dev/null 2>&1; then
    ok "⑥ 两条部署路径的低版本机制**逐字一致**（★ 共享库与 legacy 副本不漂移）"
  else
    bad "⑥ 两条部署路径的低版本机制**不一致**（★ 共享库与 legacy 副本漂移了）"
    diff <(extract_mech "$SHARED_LIB_FILE") <(extract_mech "$LEGACY") | head -20 >&2
  fi
fi

# ── 断言 ⑦：★★ 行为级：★ 真跑一遍投递函数（★ 不许只验「文本在不在」 ★）──
# ★ 把库里的两个函数**原样抽出来**、★ 用桩替掉远端依赖（★ ssh / psql / 日志 ★）、
#   ★ 然后真跑一次 `_deploy_pending_startup_migrations` ⇒ ★ 看它**到底**吐出
#   ★ 哪些文件 ★ ⇒ ★ 这才是「机制有没有生效」的证据 ★
if [[ -n "$SHARED_LIB_FILE" ]]; then
  HARNESS="$WORK/harness.sh"
  {
    printf '%s\n' 'set -uo pipefail'
    printf 'DB_CHANGELOG_REPO_ROOT="%s"\n' "$REPO_ROOT"
    printf '%s\n' '_db_log() { :; }' '_db_warn() { :; }' '_db_err() { :; }'
    printf '%s\n' '_deploy_applied_migration_ids() { printf "%s\\n" "413" "930"; }'
    printf '%s\n' '_deploy_migration_has_retired_marker() { return 1; }'
    # ★ ★ 仓库根解析器**必须一起抽出来**（★ 否则清单查找拿不到根目录 ⇒
    #   ★ 低版本区永远不许通过 ⇒ ★ 行为断言会**假红** ★）
    awk '/^_db_changelog_repo_root\(\) \{/,/^\}/' "$SHARED_LIB_FILE"
    awk '/^_deploy_low_version_delivery_allowed\(\) \{/,/^\}/' "$SHARED_LIB_FILE"
    awk '/^_deploy_pending_startup_migrations\(\) \{/,/^\}/' "$SHARED_LIB_FILE"
    printf '%s\n' '_deploy_pending_startup_migrations "stub" "stub"'
  } > "$HARNESS"
  pending_out="$WORK/pending.txt"
  ( cd "$REPO_ROOT" && bash "$HARNESS" ) > "$pending_out" 2>/dev/null
  n_pending=$(wc -l < "$pending_out" | tr -d ' ')
  if [[ "$n_pending" -ge 1 ]]; then
    ok "⑦a 投递函数跑起来了（★ 桩账本下吐出 $n_pending 条 pending）"
  else
    bad "⑦a 投递函数一条都没吐（★ 机制可能坏了、★ 或桩没生效）"
  fi
  # ★ 清单里的低版本文件**必须**进投递面 ★
  probe_missing=""
  for probe in 358_session_ownership.sql 044_health_source_probe_now.sql \
               361b_provider_models_source_prelude.sql; do
    grep -q "/$probe\$" "$pending_out" || probe_missing="$probe_missing $probe"
  done
  if [[ -z "$probe_missing" ]]; then
    ok "⑦b 清单里的低版本文件**真的进了投递面**（★ 含 361b_ 这种带字母后缀的版本号）"
  else
    bad "⑦b 清单里的这些文件**没进**投递面：${probe_missing}"
  fi
  # ★ 未登记的低版本文件**不许**进（★ 投递面必须精确 ★）
  if grep -q "/000_base_tables\.sql\$" "$pending_out"; then
    bad "⑦c 未登记的低版本文件也进了投递面（★ 投递面不精确 ⇒ 会把历史迁移一次性推给存量库）"
  else
    ok "⑦c 未登记的低版本文件**没有**进投递面（★ 投递面精确、★ 只认清单）"
  fi
  # ★ ≥地板的投递行为**必须没变** ★
  if grep -q "/412_rca_ts_index\.sql\$" "$pending_out"; then
    ok "⑦d ≥地板的迁移照旧投递（★ 本轮没动既有投递行为）"
  else
    bad "⑦d ≥地板的迁移没进投递面（★ 本轮**动到**了既有行为）"
  fi
else
  bad "⑦ 解析不到有效部署库 ⇒ 行为级判据做不了（★ 不许跳过、★ 要红）"
fi

# ★ ★★ 行为级判据**自己也要有牙**（★ M3 ★）：★ 把低版本分支打坏 ⇒
#   ★ 清单里的文件**必须**不再进投递面 ⇒ ⇒ 证明 ⑦b 不是恒绿 ★
if [[ -n "$SHARED_LIB_FILE" ]]; then
  mut_lib2="$WORK/mut2-db-changelog.sh"
  sed 's/_deploy_low_version_delivery_allowed "$base"/false/g' "$SHARED_LIB_FILE" > "$mut_lib2"
  MUT_HARNESS="$WORK/harness_mut.sh"
  {
    printf '%s\n' 'set -uo pipefail'
    printf 'DB_CHANGELOG_REPO_ROOT="%s"\n' "$REPO_ROOT"
    printf '%s\n' '_db_log() { :; }' '_db_warn() { :; }' '_db_err() { :; }'
    printf '%s\n' '_deploy_applied_migration_ids() { printf "%s\\n" "413" "930"; }'
    printf '%s\n' '_deploy_migration_has_retired_marker() { return 1; }'
    awk '/^_db_changelog_repo_root\(\) \{/,/^\}/' "$mut_lib2"
    awk '/^_deploy_low_version_delivery_allowed\(\) \{/,/^\}/' "$mut_lib2"
    awk '/^_deploy_pending_startup_migrations\(\) \{/,/^\}/' "$mut_lib2"
    printf '%s\n' '_deploy_pending_startup_migrations "stub" "stub"'
  } > "$MUT_HARNESS"
  mut_out="$WORK/pending_mut.txt"
  ( cd "$REPO_ROOT" && bash "$MUT_HARNESS" ) > "$mut_out" 2>/dev/null
  if grep -q "/358_session_ownership\.sql\$" "$mut_out"; then
    bad "⑧a 变异臂 M3：★ 低版本分支打坏后清单文件**仍进**投递面 ⇒ 行为判据无牙齿"
  else
    ok "⑧a 变异臂 M3：★ 低版本分支打坏 ⇒ 清单文件不再进投递面（★ 行为判据有牙齿）"
  fi
fi

# ── 断言 ⑧：★ 门自己有牙（★ 变异臂 ★）──────────────────────────────
# ★ M1：★ 从清单里删掉一条 ⇒ ②a 必须红 ★
mut_list="$WORK/mut.list"
cp "$LIST_FILE" "$mut_list"
first_entry="$(head -n 1 "$list_set")"
grep -vFx "$first_entry" "$mut_list" > "$mut_list.tmp" && mv "$mut_list.tmp" "$mut_list"
m1_list="$WORK/m1_list.txt"; m1_exempt="$WORK/m1_exempt.txt"; m1_covered="$WORK/m1_covered.txt"; m1_missing="$WORK/m1_missing.txt"
list_entries "$mut_list" > "$m1_list"
exempt_entries "$EXEMPT_FILE" > "$m1_exempt"
cat "$m1_list" "$m1_exempt" | sort -u > "$m1_covered"
comm -23 "$derived_set" "$m1_covered" > "$m1_missing"
if [[ -s "$m1_missing" ]]; then
  ok "⑦a 变异臂 M1：★ 清单少一条 ⇒ 派生面覆盖判据转红（★ 门有牙齿）"
else
  bad "⑦a 变异臂 M1：★ 清单少一条**没有**让判据转红 ⇒ 本门恒绿"
fi

# ★ M2：★ 把对账地板**降到 351** ⇒ ⑤d 必须红 ★
mut_lib="$WORK/mut-db-changelog.sh"
if [[ -f "$LEGACY" ]]; then
  sed 's/DB_LEDGER_RECONCILE_FROM:-412/DB_LEDGER_RECONCILE_FROM:-351/' "$LEGACY" > "$mut_lib"
  if ! grep -q 'DB_LEDGER_RECONCILE_FROM:-412' "$mut_lib"; then
    ok "⑦b 变异臂 M2：★ 对账地板被降到 351 ⇒ 对账地板判据转红（★ 门有牙齿）"
  else
    bad "⑦b 变异臂 M2：★ 地板降了却没让判据转红 ⇒ 本门恒绿"
  fi
else
  bad "⑦b 变异臂 M2：★ 找不到 legacy 副本 ⇒ 变异臂没法做"
fi

echo
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]] || exit 1
exit 0
