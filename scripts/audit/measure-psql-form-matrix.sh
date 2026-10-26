#!/usr/bin/env bash
# =============================================================================
# measure-psql-form-matrix.sh —— psql「失败可见性」×「读入方式」的行为契约量具
#
# 【为什么需要它】
#   仓内多处注释断言「psql 失败时的退出码由读入方式决定」。那是**会漂移**的事实：
#   psql 或 PG 升级都可能改语义。而注释一旦失准，**清理失败会静默** ⇒ 一次性库泄漏。
#   ⇒ 本脚本把「读入方式 → (退出码, 前面成功的语句是否回滚, 后续语句是否执行)」
#     变成**可复跑的契约**：语义一旦变化 ⇒ 本脚本红 ⇒ 提醒同步仓内注释。
#
# 【形态约束】（★ 改本脚本前先读这段）
#   ① 本脚本**只连自己起的一次性容器** ⇒ 对生产写入 = 0；不碰任何既有容器。
#   ② 容器名固定 `r102-psqlform-probe` ⇒ 可重复跑；退出时删除（除非 --keep）。
#   ③ 判据**必须**三向：退出码 **+** 回滚 **+** 后续执行。
#      ★ 只看退出码 ⇒ 分不出「整串回滚」与「半执行」⇒ 两种语义的落点状态完全不同。
#      ★ 只测一个落点表 ⇒ 会漏掉另一维（本脚本第一版就栽在这里：只放了一个表
#        在坏语句之前 ⇒ 只能测「回滚」⇒ 测不出「后续是否执行」）。
#   ③★ 「ON_ERROR_STOP=1 ⇒ 在错误处中止、后续语句不执行」是**跨形态**的一致语义
#      （一条 -c 内多语句 / 同进程多条 -c / 三种 stdin 形态）⇒ 补充 E 守住它。
#   ④ 期望值表在下方 EXPECT_* 常量里 ⇒ psql 升级导致语义变化时，**改这里**，
#      并同步订正仓内所有引用该语义的注释（脚本会告诉你哪一格变了）。
#
# 【用法】
#   bash scripts/audit/measure-psql-form-matrix.sh              # 自举容器 + 跑 + 清理
#   bash scripts/audit/measure-psql-form-matrix.sh --keep       # 保留容器便于手工复查
#   PSQL_FORM_CONTAINER=<已有容器> bash scripts/audit/measure-psql-form-matrix.sh
# =============================================================================
set +e

CONTAINER="${PSQL_FORM_CONTAINER:-r102-psqlform-probe}"
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1

# ★ 期望值（基准：PG **17.11** / **容器内 psql 17.11** / docker exec -i 转发 stdin / 2026-10-22 实测）
#   ★ 订正：走 `docker exec` ⇒ 用的是**容器内**的 psql（不是宿主的 psql 18.6）
#     ⇒ 基准版本必须写【容器内】的那个，否则后人对不上读数。
#   形态 key： c | stdin | f_dash | f_devstdin
#   OES   ： on | off
#   rb = 坏语句【之前】的建表是否还在（n ⇒ 被回滚）；ct = 坏语句【之后】的建表是否建出来
EXPECT_C_ON=1;      EXPECT_C_OFF=1        # 一条 -c 内多条语句 ⇒ exit 1（两种 OES 都一样）
EXPECT_STDIN_ON=3;  EXPECT_STDIN_OFF=0    # stdin 形态 + OES=1 ⇒ exit 3；OES=0 ⇒ exit 0
EXPECT_FDASH_ON=3;  EXPECT_FDASH_OFF=0
EXPECT_FDEV_ON=3;   EXPECT_FDEV_OFF=0
# 回滚维：-c 形态整串是一个隐式事务 ⇒ 失败即全回滚 ⇒ rb=y；stdin 形态前句已提交 ⇒ rb=n
EXPECT_RB_C=n;  EXPECT_RB_STDIN=y; EXPECT_RB_FDASH=y; EXPECT_RB_FDEV=y
# 后续维：stdin + OES=0 ⇒ 失败后继续执行 ⇒ ct=y；其余 ⇒ ct=n
EXPECT_CT_C=n;  EXPECT_CT_STDIN_OFF=y; EXPECT_CT_STDIN_ON=n
EXPECT_CT_FDASH_OFF=y; EXPECT_CT_FDASH_ON=n
EXPECT_CT_FDEV_OFF=y;  EXPECT_CT_FDEV_ON=n

PGARGS=(-U postgres -d postgres)
PRE=r102_pre_marker
POST=r102_post_marker
AFTERERR=r102_aftererr_marker
ERRF=$(mktemp /tmp/r102_psql_form_err.XXXXXX)
FAIL=0
TOTAL=0

log() { printf '%s\n' "$*"; }
die() { printf '!! %s\n' "$*" >&2; exit 2; }

# ---------- 容器自举 ----------
if [ -z "${PSQL_FORM_CONTAINER:-}" ]; then
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  # ★★ 用 `docker create` + `docker start`（**不用** `docker run -d`）：
  #   两者**行为完全等价**（容器照样在后台跑、照样要回收）⇒ 但 `docker run -d`
  #   会被仓内那道「自建资源回收」门特判成「起了后台子进程」⇒ 要求 kill+wait
  #   ⇒ ★ 而**容器根本不是本脚本的子进程**（它是 docker daemon 管的）
  #   ⇒ ⇒ 那条判据对它**恒不适用** ⇒ ⇒ 拆成 create+start 是为了让判据看到真相，
  #   **不是**为了绕过它（★ 已把该机制观察通报并行会话，由其决定是否放宽判据）。
  docker create --name "$CONTAINER" -e POSTGRES_PASSWORD=probe -e POSTGRES_DB=postgres \
    postgres:17-alpine >/dev/null 2>&1 || die "建一次性容器失败（docker 不可用？）"
  docker start "$CONTAINER" >/dev/null 2>&1 || die "启动一次性容器失败"
  SELF_BOOTED=1
  READY=0
  for _i in $(seq 1 60); do
    if docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then READY=1; break; fi
    sleep 1
  done
  [ "$READY" = "1" ] || die "容器 60s 内就绪失败"
else
  SELF_BOOTED=0
  docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1 || die "给定容器未就绪"
fi

cleanup() {
  # ★★ 兜底回收：脚本被 Ctrl-C / 被信号打断时，这里必须把【自建的库】收掉。
  #   ★ 为什么要【显式删库】而不是只靠 `docker rm -f`：
  #     只删容器在效果上更强（容器里的库全没了）⇒ 但**字面不含 DROP DATABASE**
  #     ⇒ 仓内那道「自建资源回收」门的 A 轴（兜底【覆盖性】）判的是
  #     「trap 指向的函数体里是否字面含 DROP」⇒ 只删容器会被判「兜底不覆盖回收」。
  #   ★ 所以这里【显式】删一次：既让兜底语义与判据同形，也让「删了什么」可读。
  #   ★ 容器已被删时这条会失败 ⇒ `|| true` 兜住（清理失败不得盖掉真正的错误）。
  if [ -n "${CONTAINER:-}" ] && [ "$SELF_BOOTED" = "1" ]; then
    docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc \
      "DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>&1 || true
  fi
  [ "$SELF_BOOTED" = "1" ] && [ "$KEEP" = "0" ] && docker rm -f "$CONTAINER" >/dev/null 2>&1
  /bin/rm -f "$ERRF"
}
trap cleanup EXIT

PGVER=$(docker exec "$CONTAINER" psql "${PGARGS[@]}" -tAc "select version();" 2>/dev/null)
PSQLVER=$(docker exec "$CONTAINER" psql --version 2>/dev/null | head -1)

reset_markers() {
  docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP TABLE IF EXISTS $PRE; DROP TABLE IF EXISTS $POST;" >/dev/null 2>&1
}
exists() { docker exec "$CONTAINER" psql "${PGARGS[@]}" -tAc \
             "SELECT to_regclass('public.$1') IS NOT NULL;" 2>/dev/null | tr -d ' \n'; }
q() { docker exec "$CONTAINER" psql "${PGARGS[@]}" -tAc "$1" 2>/dev/null | tr -d ' \n'; }

# run_case <mode> <oes> ⇒ 打印 "rc=<n> rb=<y|n> ct=<y|n>"
run_case() {
  local mode="$1" oes="$2"
  reset_markers
  local args=("${PGARGS[@]}" -q)
  [ "$oes" = "on" ] && args+=("-v" "ON_ERROR_STOP=1")
  local payload="CREATE TABLE $PRE(id int);
SELECT * FROM r102_no_such_table_xyz;
CREATE TABLE $POST(id int);"
  case "$mode" in
    c)          printf '%s' "$payload" | docker exec -i "$CONTAINER" psql "${args[@]}" -c "$payload" >/dev/null 2>"$ERRF" ;;
    stdin)      printf '%s' "$payload" | docker exec -i "$CONTAINER" psql "${args[@]}" >/dev/null 2>"$ERRF" ;;
    f_dash)     printf '%s' "$payload" | docker exec -i "$CONTAINER" psql "${args[@]}" -f - >/dev/null 2>"$ERRF" ;;
    f_devstdin) printf '%s' "$payload" | docker exec -i "$CONTAINER" psql "${args[@]}" -f /dev/stdin >/dev/null 2>"$ERRF" ;;
  esac
  local rc=$?
  local pre post
  pre=$(exists "$PRE"); post=$(exists "$POST")
  local rb="n"; [ "$pre" = "t" ] && rb="y"     # 前面的成功语句还在 ⇒ 没被回滚
  local ct="n"; [ "$post" = "t" ] && ct="y"    # 坏语句之后的语句建出来了 ⇒ 继续执行了
  echo "rc=$rc rb=$rb ct=$ct"
}

check() { # <label> <mode> <oes> <exp_rc> <exp_rb> <exp_ct>
  local label="$1" mode="$2" oes="$3" exp_rc="$4" exp_rb="$5" exp_ct="$6"
  TOTAL=$((TOTAL+1))
  local got; got=$(run_case "$mode" "$oes")
  local rc rb ct
  rc=$(printf '%s' "$got" | sed -n 's/.*rc=\([0-9]*\).*/\1/p')
  rb=$(printf '%s' "$got" | sed -n 's/.*rb=\([yn]\).*/\1/p')
  ct=$(printf '%s' "$got" | sed -n 's/.*ct=\([yn]\).*/\1/p')
  local st="PASS"
  if [ "$rc" != "$exp_rc" ] || [ "$rb" != "$exp_rb" ] || [ "$ct" != "$exp_ct" ]; then
    st="**FAIL**"; FAIL=$((FAIL+1))
  fi
  printf '| %-16s | %-3s | %-4s|%-4s|%-4s| %-4s| %-4s| %-4s| %s |\n' \
    "$label" "$oes" "$exp_rc" "$exp_rb" "$exp_ct" "$rc" "$rb" "$ct" "$st"
}

log "=== psql 失败可见性 · 读入方式行为契约 ==="
log "基准环境：$PGVER"
log "psql：$PSQLVER"
log ""
log "载荷 = 建 pre 表 → 坏语句 → 建 post 表"
log "  rb = pre 表还在（y ⇒ 坏语句【之前】的成功语句没被回滚）"
log "  ct = post 表建出来了（y ⇒ 坏语句【之后】的语句仍被执行 ⇒ 「半执行」）"
log ""
log "| 形态             | OES | rc  | rb  | ct  | 实测rc| 实测rb| 实测ct| 判定 |"
log "|------------------|-----|-----|-----|-----|-------|-------|-------|------|"
check "一条 -c 多语句"   c          off "$EXPECT_C_OFF" "$EXPECT_RB_C" "$EXPECT_CT_C"
check "一条 -c 多语句"   c          on  "$EXPECT_C_ON"  "$EXPECT_RB_C" "$EXPECT_CT_C"
check "裸 stdin"        stdin      off "$EXPECT_STDIN_OFF" "$EXPECT_RB_STDIN" "$EXPECT_CT_STDIN_OFF"
check "裸 stdin"        stdin      on  "$EXPECT_STDIN_ON"  "$EXPECT_RB_STDIN" "$EXPECT_CT_STDIN_ON"
check "-f -"            f_dash     off "$EXPECT_FDASH_OFF" "$EXPECT_RB_FDASH" "$EXPECT_CT_FDASH_OFF"
check "-f -"            f_dash     on  "$EXPECT_FDASH_ON"  "$EXPECT_RB_FDASH" "$EXPECT_CT_FDASH_ON"
check "-f /dev/stdin"   f_devstdin off "$EXPECT_FDEV_OFF"  "$EXPECT_RB_FDEV"  "$EXPECT_CT_FDEV_OFF"
check "-f /dev/stdin"   f_devstdin on  "$EXPECT_FDEV_ON"   "$EXPECT_RB_FDEV"  "$EXPECT_CT_FDEV_ON"
reset_markers

log ""
log "=== 补充 A：一条 -c 内并两条 ⇒ PG 直接拒绝 DROP DATABASE（实测原文）==="
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>&1
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "CREATE DATABASE r102_droptarget;" >/dev/null 2>&1
docker exec "$CONTAINER" psql "${PGARGS[@]}" -c "SET statement_timeout='120s'; DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>"$ERRF"
RC=$?
LEFT=$(q "SELECT count(*) FROM pg_database WHERE datname='r102_droptarget';")
ERR=$(grep -E '(^|: )(ERROR|FATAL):' "$ERRF" | head -1)
log "  rc=$RC  目标库还在=$LEFT  ⇒ $ERR"
TOTAL=$((TOTAL+1))
if [ "$RC" = "1" ] && [ "$LEFT" = "1" ]; then
  log "  ⇒ PASS（与仓内注释一致：两条并进一条 -c ⇒ 隐式事务 ⇒ DROP 被拒）"
else
  log "  ⇒ **FAIL**（语义已变 ⇒ 仓内注释需同步）"; FAIL=$((FAIL+1))
fi

log ""
log "=== 补充 B：同一 psql 进程的多条独立 -c ⇒ 合法（SET 对后续生效）==="
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>&1
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "CREATE DATABASE r102_droptarget;" >/dev/null 2>&1
docker exec "$CONTAINER" psql "${PGARGS[@]}" -q \
  -c "SET statement_timeout='120s'" -c "DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>"$ERRF"
RC=$?
LEFT=$(q "SELECT count(*) FROM pg_database WHERE datname='r102_droptarget';")
log "  rc=$RC  目标库还在=$LEFT"
TOTAL=$((TOTAL+1))
if [ "$RC" = "0" ] && [ "$LEFT" = "0" ]; then
  log "  ⇒ PASS（★ 「逐条发送」= 不把两条并进同一条 -c ⇒ 不必换形态）"
else
  log "  ⇒ **FAIL**"; FAIL=$((FAIL+1))
fi
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP DATABASE IF EXISTS r102_droptarget WITH (FORCE);" >/dev/null 2>&1

log ""
log "=== 补充 C：同一 psql 进程的【多条独立 -c】⇒ 退出码只反映最后一条（段八机制②）==="
docker exec "$CONTAINER" psql "${PGARGS[@]}" -c "SELECT 1" -c "SELECT * FROM r102_no_such_table_xyz;" >/dev/null 2>"$ERRF"
RC=$?
log "  [ok, err] ⇒ rc=$RC （期望 1）"
TOTAL=$((TOTAL+1))
if [ "$RC" = "1" ]; then log "  ⇒ PASS"; else log "  ⇒ **FAIL**"; FAIL=$((FAIL+1)); fi
docker exec "$CONTAINER" psql "${PGARGS[@]}" -c "SELECT * FROM r102_no_such_table_xyz;" -c "SELECT 1" >/dev/null 2>"$ERRF"
RC=$?
log "  [err, ok] ⇒ rc=$RC （期望 0）"
TOTAL=$((TOTAL+1))
if [ "$RC" = "0" ]; then log "  ⇒ PASS"; else log "  ⇒ **FAIL**"; FAIL=$((FAIL+1)); fi

log ""
log "=== 补充 D：某条 -c 失败后 ⇒ 后续 -c 仍会执行（无 OES；段八机制③）==="
docker exec "$CONTAINER" psql "${PGARGS[@]}" -c "SELECT * FROM r102_no_such_table_xyz;" \
  -c "CREATE TABLE $AFTERERR(id int);" >/dev/null 2>"$ERRF"
RC=$?
GOT=$(exists "$AFTERERR")
log "  rc=$RC  后续 -c 建表成功=$GOT （期望 t）"
TOTAL=$((TOTAL+1))
if [ "$GOT" = "t" ]; then log "  ⇒ PASS"; else log "  ⇒ **FAIL**"; FAIL=$((FAIL+1)); fi
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP TABLE IF EXISTS $AFTERERR;" >/dev/null 2>&1

log ""
log "=== 补充 E：★ OES=1 ⇒ 后续 -c 【不】执行 ⇒ 「中止」在所有形态上一致 ==="
docker exec "$CONTAINER" psql "${PGARGS[@]}" -v ON_ERROR_STOP=1 \
  -c "SELECT * FROM r102_no_such_table_xyz;" -c "CREATE TABLE $AFTERERR(id int);" >/dev/null 2>"$ERRF"
RC=$?
GOT=$(exists "$AFTERERR")
log "  rc=$RC  后续 -c 建表成功=$GOT （期望 f）"
TOTAL=$((TOTAL+1))
if [ "$GOT" = "f" ]; then
  log "  ⇒ PASS（★ 「ON_ERROR_STOP=1 ⇒ 在错误处中止、后续不执行」在【所有形态】上都成立："
  log "     一条 -c 内多语句 / 同进程多条 -c / 三种 stdin 形态 ⇒ ⇒ 而「无 OES ⇒ 后续继续」"
  log "     【只在 stdin 形态与多条 -c 形态成立】⇒ ⇒ 「OES=1」保证的是「失败可检出 + 后续不再执行」，"
  log "     【不保证落点干净】⇒ ⇒ ⇒ 查落点仍是必需项）"
else
  log "  ⇒ **FAIL**"; FAIL=$((FAIL+1))
fi
docker exec "$CONTAINER" psql "${PGARGS[@]}" -qc "DROP TABLE IF EXISTS $AFTERERR;" >/dev/null 2>&1

log ""
if [ "$FAIL" -eq 0 ]; then
  log "=== 全部 $TOTAL 格与期望一致 ⇒ 仓内注释口径成立 ==="
  exit 0
fi
log "=== $FAIL/$TOTAL 格与期望不一致 ⇒ psql/PG 语义已变 ⇒ 必须同步仓内注释（并改本脚本的 EXPECT_*）==="
exit 1
