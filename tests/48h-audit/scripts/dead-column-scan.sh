#!/usr/bin/env bash
# tests/48h-audit/scripts/dead-column-scan.sh
#
# 「死契约列」扫描：找出**有 DDL、有写入、有数据，但没有任何消费方**的数据库列。
#
# 为什么需要它：R78 的 D12 复核发现 `providers.egress_profile` 满足全部「看起来已接线」
# 的特征——有迁移、有索引、有管理端点、有实现包（proxy 1900+ 行、8 个测试文件）——
# 却没有任何派发代码读它。真库 14 个 provider 标记为走代理，实际全部直连。
# 教训是：**grep 包名只证明存在，grep 调用方才证明被使用。**
#
# 本脚本把那条教训自动化：列名在 Go 代码里（排除 admin 管理面、db DDL、vendor、测试）
# 出现次数为 0，即为死契约列候选。
#
# 用法:
#   bash tests/48h-audit/scripts/dead-column-scan.sh [table ...]
#   # 不带参数 = 扫 providers / credentials 两张核心表
#
# 注意：0 命中是**候选**不是定论。本脚本按列名做词边界匹配，因此：
#   * snake_case 与 CamelCase 两种形式都要查（Go 侧通常用结构体字段名）；
#   * 经 `SELECT *` 整行加载的表会漏判——本仓无 SELECT *（已核），但换仓需重核；
#   * 消费方可能通过 JSON 聚合或代码生成间接读取，这类需人工判读。
# 命中为 0 的列请人工判读后再登记，不要直接当缺陷。

set -uo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

TABLES=("$@")
if [[ ${#TABLES[@]} -eq 0 ]]; then
  TABLES=(providers credentials)
fi

# 排除路径：admin 是管理面（写入方而非消费方）、db 是 DDL、vendor 是第三方、
# migrations 是 SQL、_test.go 是测试断言。
EXCLUDE_RE='^\./(admin|db|vendor|sql)/|_test\.go:'

# snake_case -> CamelCase。逐段首字母大写。
to_camel() {
  local s="$1" out="" seg
  local IFS='_'
  for seg in $s; do
    out+="${seg^}"
  done
  printf '%s' "$out"
}

psql_q() {
  # $1 = SQL。取不到就返回空（容器名/凭据不可用时让脚本降级为只扫 Go 侧）。
  docker exec llm-gateway-pg psql -U llm_gateway -d llm_gateway -tAc "$1" 2>/dev/null
}

printf '%-16s %-30s %-8s %-8s %-10s %s\n' TABLE COLUMN SNAKE CAMEL NONNULL VERDICT

found_any=0
for tbl in "${TABLES[@]}"; do
  cols=$(psql_q "select column_name from information_schema.columns where table_name='${tbl}' order by ordinal_position")
  if [[ -z "$cols" ]]; then
    printf '%-16s %s\n' "$tbl" "<无法读取列清单：PG 不可达，本表跳过>"
    continue
  fi
  while read -r col; do
    [[ -z "$col" ]] && continue
    camel=$(to_camel "$col")
    snake_hits=$(grep -rn --include='*.go' -w "$col" . 2>/dev/null \
      | grep -Ev "$EXCLUDE_RE" | wc -l | tr -d ' ')
    camel_hits=$(grep -rn --include='*.go' -w "$camel" . 2>/dev/null \
      | grep -Ev "$EXCLUDE_RE" | wc -l | tr -d ' ')
    nonnull=$(psql_q "select count(*) from ${tbl} where ${col} is not null" | head -1)
    if [[ "$snake_hits" == "0" && "$camel_hits" == "0" ]]; then
      verdict="DEAD-CONTRACT"
      if [[ -z "$nonnull" || "$nonnull" == "0" ]]; then
        verdict="dead-but-unused-data"
      fi
      found_any=1
    else
      verdict="has-consumer"
    fi
    printf '%-16s %-30s %-8s %-8s %-10s %s\n' "$tbl" "$col" "$snake_hits" "$camel_hits" "${nonnull:-?}" "$verdict"
  done <<< "$cols"
done

echo
if [[ "$found_any" == "1" ]]; then
  cat <<'EOF'
DEAD-CONTRACT = 有 DDL、有写入、可能有数据，但 admin/db 之外零消费方。
  这类列**不能**被当作「功能已接线」的证据——R78 的 egress_profile 就是这一类：
  实现包、测试、管理端点、DB 索引俱全，唯独请求路径无人读它。
  登记前请人工判读：消费方是否经 JSON 聚合 / 代码生成 / SELECT * 间接读取。
EOF
else
  echo "未发现死契约列。"
fi
