#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# db-package-isolation-check.sh —— **db 包隔离验收**：跑 db 包前后，
# 形态库的「关系集 + 列集」必须**逐字一致**。
#
# ── 为什么需要这把尺子（R78 轮实测） ────────────────────────────────
#
# 2026-10-21 实测：`go test ./db/` 会把形态库洗成玩具形态 ——
#
#	关系数 2436 → 2296（消失 145 / 新增 5，净 −140）
#	掉列 959（全量口径；其中表列 433、视图列 526）
#	被毁 7 表 + 25 视图 + 5 物化视图 + 103 索引 + 5 序列
#	★ 还把 890 已退役的 v1 族 `_request_logs` **复活**，并带走 canonical 视图
#	  `request_logs_with_current_month`
#
# 后果不是「库脏了」，是**别的门全假红**（relation does not exist / 列数不对），
# 于是「包级红清单」里混进了大量与被测对象无关的红。
#
# 静态棘轮门（db/db_package_isolation_gate_test.go）守的是「新写的破坏性门不许再犯」；
# 本脚本守的是**行为**：「现在跑一次 db 包，形态库到底变没变」。
# 两者互补：静态门可能漏（判据认错形态），行为尺子不会漏（差多少就是差多少）。
#
# ── 用法 ──────────────────────────────────────────────────────────────
#
#	# 1) 备一个形态库（装了产品迁移链的库；见 scripts/audit/probe-onetime-db-shape.sh）
#	DB_NAME=itgate_shape bash scripts/audit/probe-onetime-db-shape.sh
#
#	# 2) 跑本脚本（DSN 里带口令；口令**不落盘**）
#	DB_URL='postgres://llm_gateway:***@127.0.0.1:5432/itgate_shape?sslmode=disable' \
#	  bash scripts/audit/db-package-isolation-check.sh "$DB_URL"
#
# 退出码：0 = 形态库逐字未变；1 = 变了（并打印差异清单）；2 = 用法/环境错；3 = 量具坏了。
#
# ── ★ 五条纪律 ───────────────────────────────────────────────────────
#
#	① **两坐标**：指纹取自**同一个库**（不是两个库对比）。
#	② **列集也要比**：只比关系数会漏「表还在但被换成玩具形态」这一类
#	   （R78 实测里 report_snapshots 就是这样：表还在，索引没了）。
#	③ **差异要能定位**：本脚本打印的是**逐行 diff**，不是计数 ——
#	   计数只会告诉你「变了 140 个」，不会告诉你「变了什么」。
#	④ **两端清单都要先自证，再比**：
#	   「空清单」与「差异巨大」**同样可疑** —— R78 实测两次量具事故
#	   ① psql 报错被 `2>/dev/null` 吞成空文件 ⇒ diff 读出「0 个差异」；
#	   ② 漏了 `PGPASSWORD` ⇒ psql 连不上 ⇒ 清单里躺着 2 行错误文本
#	   ⇒ diff 读出「2440 行差异」（看着像库被摧毁了，其实是量具没连上）。
#	   ⇒ 本脚本因此**不吞 stderr**，且对每次指纹都做「非空 + 量级」自检。
#	⑤ **连接用 URI 位置参数，不用 PG* 环境变量**：
#	   本机的 `~/bin/psql` 是一个**转发进 llm-gateway-pg 容器的 shim**
#	   （宿主 Homebrew 的 libpq 客户端被卸了），它**忽略 PGHOST/PGPASSWORD
#	   等环境变量** ⇒ 只设环境变量会连到「/tmp/.s.PGSQL.5432」这个不存在的本地 socket。
#	   ⇒ 本脚本统一用 `psql "<uri>" -Atc "<sql>"`：**对真 psql 与 shim 都成立**，
#	   且口令只在进程命令行里，不落盘。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DB_URL="${1:-}"
shift || true

if [[ -z "$DB_URL" ]]; then
  echo "用法: $0 <dsn> [go test 参数...]" >&2
  exit 2
fi

if ! command -v psql >/dev/null 2>&1; then
  echo "缺 psql ⇒ 装 libpq（macOS: brew install libpq）后重试" >&2
  exit 2
fi

# 库名只用于报错信息；连接本身走 URI 位置参数（纪律 ⑤）。
DB_NAME="$(python3 - "$DB_URL" <<'PY'
import sys, urllib.parse
u = urllib.parse.urlparse(sys.argv[1])
print((u.path or "/postgres").lstrip("/"))
PY
)"

if [[ -z "$DB_NAME" || "$DB_NAME" == "postgres" ]]; then
  echo "DSN 必须指向一个**形态库**（不能是 postgres 维护库）" >&2
  exit 2
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/dbpkg-iso-XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fingerprint() {
  local out="$1"
  # ⚠ 不用 `2>/dev/null`：psql 的报错必须**看得见**。
  #   被吞掉时 stderr 里的 ERROR/HINT 会成为「清单的行」，
  #   于是空库与「连不上」会长得一模一样（纪律 ④）。
  if ! psql "$DB_URL" -Atc "select c.relkind::text||' '||n.nspname||'.'||c.relname
                             from pg_class c join pg_namespace n on n.oid=c.relnamespace
                             where n.nspname not in ('pg_catalog','information_schema','pg_toast')" \
      | sort > "$out.rel"; then
    echo "❌ 取关系集指纹失败（psql 连不上或查询报错）—— 这不是「库变了」，是**量具坏了**" >&2
    return 3
  fi
  if ! psql "$DB_URL" -Atc "select table_name||'.'||column_name||':'||data_type
                             from information_schema.columns where table_schema='public'" \
      | sort > "$out.col"; then
    echo "❌ 取列集指纹失败（psql 连不上或查询报错）—— 这不是「库变了」，是**量具坏了**" >&2
    return 3
  fi
  # 清单自证：空清单 / 只有个位数 ⇒ 一定不是形态库 ⇒ 拒绝继续比。
  local rel_n col_n
  rel_n=$(wc -l < "$out.rel" | tr -d ' ')
  col_n=$(wc -l < "$out.col" | tr -d ' ')
  if [[ "$rel_n" -lt 100 || "$col_n" -lt 100 ]]; then
    echo "❌ 指纹清单量级不对（关系 ${rel_n} / 列 ${col_n}）⇒ 这不是形态库，或量具没连上" >&2
    echo "   ⇒ **拒绝比较**：空清单/残清单与「0 差异」长得一模一样（纪律 ④）" >&2
    return 3
  fi
}

echo "== 形态库 = ${DB_NAME}（连接走 URI 位置参数 ⇒ 兼容 psql shim）"

fingerprint "$WORK/before" || exit 3
before_rel=$(wc -l < "$WORK/before.rel" | tr -d ' ')
before_col=$(wc -l < "$WORK/before.col" | tr -d ' ')
echo "== 跑前指纹：关系 $before_rel / 列 $before_col"

# ⚠ ★ 必须把 DSN 环境变量**注入给 go test**，否则所有真库腿会 SKIP，
#   包在 2 秒内「跑完」，而隔离结论是**空的**（R78 实测踩过：
#   脚本第一版没注入 ⇒ `ok ... 1.652s` ⇒ 什么都没跑却显示「形态库未变」）。
#   权威变量清单 = scripts/audit/datasource_env.sh（单一事实源，40 个名字）。
DSN_ENV_FILE="$ROOT/scripts/audit/datasource_env.sh"
if [[ ! -f "$DSN_ENV_FILE" ]]; then
  echo "找不到权威 DSN 清单 $DSN_ENV_FILE ⇒ 拒绝跑（否则真库腿会静默 SKIP）" >&2
  exit 2
fi
# shellcheck disable=SC1090
source "$DSN_ENV_FILE" "$DB_URL"

echo "== 跑 db 包（这会花几分钟）…"
( cd "$ROOT" && go test ./db/ -count=1 -v ) > "$WORK/test.log" 2>&1 || true
passed=$(grep -c '^--- PASS:' "$WORK/test.log" || true)
failed=$(grep -c '^--- FAIL:' "$WORK/test.log" || true)
skipped=$(grep -c '^--- SKIP:' "$WORK/test.log" || true)
echo "== 包级读数：PASS $passed / FAIL $failed / SKIP $skipped"
grep -E '^(--- FAIL|ok|FAIL)' "$WORK/test.log" | sed 's/^/   /' || true
if [[ "$skipped" -gt 0 ]]; then
  echo "⚠️  有 ${skipped} 条测试被 SKIP ⇒ **这些腿的隔离性本轮未被验证**"
  echo "   （SKIP 不是证据：变量没到位时门会安静地跳过，而不是失败）"
  grep -E '^--- SKIP:' "$WORK/test.log" | sed 's/^/   /' | head -20
fi

fingerprint "$WORK/after" || exit 3
after_rel=$(wc -l < "$WORK/after.rel" | tr -d ' ')
after_col=$(wc -l < "$WORK/after.col" | tr -d ' ')
echo "== 跑后指纹：关系 $after_rel / 列 $after_col"

rel_diff=$(diff "$WORK/before.rel" "$WORK/after.rel" || true)
col_diff=$(diff "$WORK/before.col" "$WORK/after.col" || true)

if [[ -z "$rel_diff" && -z "$col_diff" ]]; then
  echo "✅ 形态库逐字未变（关系集与列集都一致）"
  exit 0
fi

echo "❌ 形态库被改了 —— 逐行差异如下（- 是消失的，+ 是新增/变形的）"
if [[ -n "$rel_diff" ]]; then
  echo "---- 关系集差异 ----"
  echo "$rel_diff"
fi
if [[ -n "$col_diff" ]]; then
  echo "---- 列集差异 ----"
  echo "$col_diff"
fi
exit 1
