#!/bin/bash
# q-usage.sh —— 统计生产 nginx 访问日志里 **带 q= 参数** 的 `GET /api/logs` 次数。
#
# 用途：877（session_turns.search_text 的 GIN trigram 索引）决策的**触发条件**测量。
# 该决策要求「任一 7 天窗口内带 q= 的 GET /api/logs ≥ 1 次 ⇒ 保留索引」，
# 本脚本就是那条触发条件唯一的取数口径。
#
# 用法：
#   q-usage.sh                       # 全部可回溯窗口
#   q-usage.sh 20261011 20261019     # 指定窗口 [FROM TO]，YYYYMMDD
#   q-usage.sh --log-dir /var/log/nginx --prefix '*llmgo*'
#   q-usage.sh --self-test           # 离线自检（不连任何主机，见下）
#
# 口径：
#   1) 只认 token 精确的 `[?&]q=`。**裸 `q=` 会误配** `seq=` / `req=` / `omit_body=`
#      / `mode=` / `limit=` —— 那些是别的前端参数，不是搜索词。
#   2) 只认 `GET /api/logs`（搜索接口本体）。
#   3) 窗口按**请求行里的时间戳**过滤；时间戳解析不出来的行**不参与窗口过滤**
#      （保守计入），因为 nginx 各 vhost 的 log_format 不完全一致。
#
# ⚠ 三个坑（2026-10-20 在 154 实测踩过，**每一个都会让结果静默少报**）：
#   1) 轮转文件命名是 `<name>.log-<YYYYMMDD>.gz`（中间有日期），
#      glob 必须写 `*access*.log*.gz`；写成 `*access*.log.gz` 匹配不到任何文件。
#   2) awk **不能直接读 .gz**。用 `zcat -f`：它对 gzip 文件解压、对非 gzip 文件直通，
#      于是「当前文件 + 轮转文件」可以混在一起喂给 awk。
#   3) ★ logrotate 开了 `delaycompress` ⇒ **最近一次轮转的文件是不压缩的**
#      （`llm-kxpms-cn-access.log-20261019`，没有 .gz）。
#      只 glob `*.log` + `*.log*.gz` 会**漏掉它** —— 而它装的恰恰是**当天**的流量。
#      实测：154 上漏这一族 ⇒ 少计 47 次；211 上少计 9 次。
#      本脚本用 `find` 枚举「当前 + 未压缩轮转 + 压缩轮转」三族，互不重叠。
#
# ★ 为什么带 --self-test：
#   「带 q= 的次数 = 0」这个结论，**最危险的失效模式是计数器恒为 0**
#   （匹配写错 / 根本没读到文件 / 正则写坏），此时输出和「真的没人用」一模一样。
#   本仓吃过同形的亏：一次全文检索报「47 文件 88 处命中」，实为 `GIN` 匹配进了 `BEGIN`。
#   --self-test 用一份**含真 `?q=` 命中**的离线夹具驱动同一套计数逻辑，
#   断言计数器能数出非 0 ⇒ 「0 次」这个读数才有意义。
set -o pipefail

LOG_DIR="/var/log/nginx"
PREFIX="*access*"
FROM="00000000"
TO="99999999"
SELF_TEST=0

usage() {
  sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'
}

# ---- 参数解析 ----
POSITIONAL=()
while [ $# -gt 0 ]; do
  case "$1" in
    --log-dir) LOG_DIR="$2"; shift 2 ;;
    --prefix)  PREFIX="$2";  shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; while [ $# -gt 0 ]; do POSITIONAL+=("$1"); shift; done; break ;;
    -*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    *)  POSITIONAL+=("$1"); shift ;;
  esac
done
[ ${#POSITIONAL[@]} -ge 1 ] && FROM="${POSITIONAL[0]}"
[ ${#POSITIONAL[@]} -ge 2 ] && TO="${POSITIONAL[1]}"

# ---- 核心计数：stdin 为日志行，输出 "total withq lines" ----
count_q() {
  awk -v from="$FROM" -v to="$TO" '
    function monthnum(m,   pos) {
      pos = index("JanFebMarAprMayJunJulAugSepOctNovDec", m)
      if (pos == 0) return 0
      return int((pos + 2) / 3)
    }
    {
      lines++
      # 请求行里的时间戳：[19/Oct/2026:13:55:36 +0800]
      if (match($0, /\[[0-9][0-9]\/[A-Za-z][A-Za-z][A-Za-z]\/[0-9][0-9][0-9][0-9]/)) {
        ts = substr($0, RSTART + 1, 20)
        split(ts, a, /[\/:]/)
        d = a[3] sprintf("%02d", monthnum(a[2])) sprintf("%02d", a[1])
      } else {
        d = ""            # 时间戳解析不出来 ⇒ 不参与窗口过滤（保守计入）
      }
      if (d != "" && (d + 0) < (from + 0)) next
      if (d != "" && (d + 0) > (to + 0)) next
      if (index($0, "GET /api/logs") == 0) next
      total++
      if (index($0, "?q=") || index($0, "&q=")) withq++
    }
    END { printf "%d %d %d\n", total + 0, withq + 0, lines + 0 }
  '
}

# ---- 文件枚举：当前 + 未压缩轮转 + 压缩轮转（三族互不重叠）----
collect_files() {
  plain=(); gz=(); current=(); plain_rotated=()
  local f
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    plain+=("$f")
    case "$f" in
      *.log) current+=("$f") ;;
      *) plain_rotated+=("$f") ;;
    esac
  done <<EOF
$(find "$LOG_DIR" -maxdepth 1 -type f \
   \( -name "${PREFIX}.log" -o -name "${PREFIX}.log-*" -o -name "${PREFIX}.log.[0-9]*" \) \
   ! -name '*.gz' 2>/dev/null | sort)
EOF
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    gz+=("$f")
  done <<EOF
$(find "$LOG_DIR" -maxdepth 1 -type f \
   \( -name "${PREFIX}.log-*.gz" -o -name "${PREFIX}.log.[0-9]*.gz" \) 2>/dev/null | sort)
EOF
}

# ---- 自检：同一套计数逻辑 + 一份**含真命中**的离线夹具 ----
run_self_test() {
  tmp=$(mktemp -d) || { echo "self-test: mktemp failed" >&2; return 1; }
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/logs"
  LOG_DIR="$tmp/logs"          # 自检在临时目录里跑，不碰任何真实日志
  cat > "$tmp/logs/access.log" <<'EOF'
1.1.1.1 - - [19/Oct/2026:10:00:00 +0800] "GET /api/logs?q=hello HTTP/1.1" 200 1 "-" "ua"
1.1.1.1 - - [19/Oct/2026:10:00:01 +0800] "GET /api/logs?seq=123&req=abc HTTP/1.1" 200 1 "-" "ua"
1.1.1.1 - - [19/Oct/2026:10:00:02 +0800] "GET /api/logs HTTP/1.1" 200 1 "-" "ua"
1.1.1.1 - - [19/Oct/2026:10:00:03 +0800] "GET /api/other?q=x HTTP/1.1" 200 1 "-" "ua"
EOF
  # 已压缩轮转文件
  cat > "$tmp/logs/access.log-20261018.gz.tmp" <<'EOF'
1.1.1.1 - - [18/Oct/2026:10:00:00 +0800] "GET /api/logs?q=rotated HTTP/1.1" 200 1 "-" "ua"
EOF
  gzip -c "$tmp/logs/access.log-20261018.gz.tmp" > "$tmp/logs/access.log-20261018.gz"
  rm -f "$tmp/logs/access.log-20261018.gz.tmp"
  # ★ 未压缩轮转文件（logrotate delaycompress 的产物：最近一次轮转不压）
  #   这一族装的是**当天**流量，漏掉它 ⇒ 静默少报（154 上实测少计 47 次）
  cat > "$tmp/logs/access.log-20261019" <<'EOF'
1.1.1.1 - - [19/Oct/2026:11:00:00 +0800] "GET /api/logs?q=plain-rotated HTTP/1.1" 200 1 "-" "ua"
EOF

  fail=0
  check() { # expected_total expected_withq expected_lines name
    got=$(count_q < "$tmp/out" 2>/dev/null)
    if [ "$got" != "$1 $2 $3" ]; then
      echo "self-test FAIL: $4 — expected 'total withq lines' = '$1 $2 $3', got '$got'" >&2
      fail=1
    else
      echo "self-test ok: $4"
    fi
  }

  # 全窗口：当前 4 行 + 压缩轮转 1 行 + 未压缩轮转 1 行 = 6 行
  #   GET /api/logs 命中 5 行（`/api/other?q=x` 不算）
  #   带 [?&]q= 的 3 行：`?q=hello` / `?q=rotated` / `?q=plain-rotated`
  #   （`?seq=123&req=abc` **不得**被裸 q= 误配进来）
  collect_files
  all=( "${plain[@]}" "${gz[@]}" )
  zcat -f -- "${all[@]}" > "$tmp/out" 2>/dev/null
  check 5 3 6 "计数能数出非 0（否则「0 次」不可信）"

  # 三族文件都要被枚举到
  nf_plain=${#plain[@]}; nf_gz=${#gz[@]}
  if [ "$nf_plain" -ne 2 ] || [ "$nf_gz" -ne 1 ]; then
    echo "self-test FAIL: 文件枚举不对 —— plain=$nf_plain(应 2: 当前 + 未压缩轮转) gz=$nf_gz(应 1)" >&2
    fail=1
  else
    echo "self-test ok: 当前 / 未压缩轮转 / 压缩轮转 三族都枚举到"
  fi

  # 窗口排除 20261018 ⇒ 压缩轮转那行不计入 total/withq
  # （lines 是**读到的行数**，不随窗口变化）
  FROM=20261019 TO=20261019
  zcat -f -- "${all[@]}" > "$tmp/out" 2>/dev/null
  check 4 2 6 "窗口过滤生效（total/withq 随窗口变，lines 是读到的行数）"

  # 轮转文件**一个都没命中**时必须告警（glob 写错的典型形态）：
  # 目录里只有当前文件、没有任何轮转文件 ⇒ 统计会静默退化成「只有当天」
  FROM=00000000 TO=99999999
  mkdir -p "$tmp/only-current"
  : > "$tmp/only-current/access.log"
  out=$(bash "$0" --log-dir "$tmp/only-current" 2>&1)
  if ! printf '%s' "$out" | grep -q "轮转"; then
    echo "self-test FAIL: 轮转文件未命中时没有告警（glob 写错会静默少报）" >&2
    fail=1
  else
    echo "self-test ok: 轮转文件未命中时有告警"
  fi

  if [ "$fail" -ne 0 ]; then return 1; fi
  echo "self-test: ALL PASS"
  return 0
}

if [ "$SELF_TEST" -eq 1 ]; then
  run_self_test
  exit $?
fi

# ---- 主流程 ----
collect_files
all=( "${plain[@]}" "${gz[@]}" )

if [ ${#all[@]} -eq 0 ]; then
  echo "no access logs found under $LOG_DIR (prefix=$PREFIX)" >&2
  echo "      ⇒ 若期望覆盖轮转期，确认轮转名是 <name>.log-<YYYYMMDD>.gz，glob 需 '$PREFIX.log*.gz'" >&2
  exit 1
fi

# 告警 1：轮转文件一个都没命中。生产上 154 的轮转名是 <name>.log-<YYYYMMDD>.gz，
#         glob 写成 *access*.log.gz 会匹配不到 ⇒ 统计会静默退化成「只有当天」。
if [ ${#plain_rotated[@]} -eq 0 ] && [ ${#gz[@]} -eq 0 ]; then
  echo "WARN: 没有命中任何轮转文件（$LOG_DIR/$PREFIX.log-*）" >&2
  echo "      ⇒ 若期望覆盖轮转期，说明 glob 不对：轮转名是 <name>.log-<YYYYMMDD>.gz" >&2
  echo "      ⇒ 统计只覆盖了当前文件，结论会**静默少报**" >&2
fi

# 告警 2：目录里有 .gz 但没被我们的 glob 命中 ⇒ 多半是 prefix 写错了
gz_in_dir=( "$LOG_DIR"/*.gz )
if [ ${#gz_in_dir[@]} -gt ${#gz[@]} ]; then
  echo "WARN: $LOG_DIR 下有 ${#gz_in_dir[@]} 个 .gz，只命中 ${#gz[@]} 个（prefix='$PREFIX' 可能过窄）" >&2
fi

# 计数（zcat 的 stderr 不丢：解压失败会让「0」变得不可解释）
ERR=$(mktemp)
trap 'rm -f "$ERR"' EXIT
zcat -f -- "${all[@]}" 2>"$ERR" | count_q > "$ERR.count"
rc=${PIPESTATUS[0]}
if [ "$rc" -ne 0 ] || [ -s "$ERR" ]; then
  echo "WARN: 解压/读取有告警（zcat exit=${rc}），计数结果可能偏低：" >&2
  head -3 "$ERR" >&2
fi
read -r TOTAL WITHQ LINES < "$ERR.count" || {
  echo "ERROR: 计数没有产出结果（awk 未输出）⇒ 读数不可解释，拒绝打印" >&2
  rm -f "$ERR.count"
  exit 1
}
rm -f "$ERR.count"

printf 'log_dir=%s prefix=%s window=%s..%s\n' "$LOG_DIR" "$PREFIX" "$FROM" "$TO"
printf 'files=%d (current=%d plain_rotated=%d gz_rotated=%d)  lines_scanned=%d\n' \
  "${#all[@]}" "${#current[@]}" "${#plain_rotated[@]}" "${#gz[@]}" "$LINES"
printf 'GET /api/logs total=%d  with_[?&]q= =%d\n' "$TOTAL" "$WITHQ"

# 触发条件判定（877 决策）：
#   任一 7 天窗口 ≥1 次带 q= ⇒ 保留 GIN 索引；30 天内为 0 ⇒ 删索引、把 ?q= 置为显式启用。
if [ "$WITHQ" -eq 0 ]; then
  echo "verdict: 窗口内 0 次带 q= ⇒ 未触发「保留 877」的触发条件"
else
  echo "verdict: 窗口内 $WITHQ 次带 q= ⇒ 已触发「保留 877」"
fi
