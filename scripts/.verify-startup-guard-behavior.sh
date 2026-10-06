#!/usr/bin/env bash
# 三条启动期目录短路守卫的**真库行为**验证（runbook §10.98.11）。
#
# 为什么需要它：db/*_guard_test.go 全是**文本断言**。它们能证明
# 「源码里写了守卫调用」，证明不了「守卫真的短路、真的不再取锁」。
#
# ★ 本脚本存在的直接理由：真跑一次就抓到了文本门完全看不见的缺陷。
#   providerModelsCanonicalClearedAtCurrent 的探针统计「缺什么」，
#   但注释那一项我写成了 WHERE EXISTS 而不是 WHERE NOT EXISTS，
#   语义整个取反 —— 生产上（列与注释都在）守卫会**恒为 false**，
#   于是每次启动照样取两次 ACCESS EXCLUSIVE，而
#   6 个子测试 + 10 条变异全部全绿。见 runbook §10.98.11。
#
# 覆盖（每个守卫一组）：
#   A 列/索引缺失时守卫必须返回 false（必须走 DDL）
#   B 齐备后守卫必须返回 true
#   C 决定性：并发读持锁时，守卫后的 ensure 必须立即返回
#   D 负控：同一条 DDL **不带守卫**直接执行必须被并发读挡住（55P03）
#     ⇒ C 的「没被挡住」才有意义；没有 D，C 可能是恒真
#
# 用完即弃的一次性容器，与 154/245/252 无关，不连生产。
set -euo pipefail
cd "$(dirname "$0")/.."

CONTAINER="${GUARD_PG_CONTAINER:-guardpg}"
PORT="${GUARD_PG_PORT:-15499}"
IMAGE="${PG_TEST_IMAGE:-postgres:16-alpine}"
PW="guard_verify_local_only"

fail() { echo "  FAIL $*" >&2; FAILED=1; }
FAILED=0

echo "== 起一次性容器 $CONTAINER ($IMAGE) =="
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=t \
  -p "127.0.0.1:$PORT:5432" "$IMAGE" >/dev/null

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

for i in $(seq 1 30); do
  if docker exec "$CONTAINER" pg_isready -U postgres -d t >/dev/null 2>&1; then break; fi
  [ "$i" -eq 30 ] && { echo "容器 30 秒内没就绪"; exit 1; }
  sleep 1
done
echo "  就绪"

DSN="postgres://postgres:$PW@127.0.0.1:$PORT/t?sslmode=disable"

echo
echo "== 真库行为验证 =="
if TEST_DATABASE_URL="$DSN" go test ./db/ \
     -run 'TestStartupDDLGuards_RealDB|TestStartupDDLGuards_EarlierOnes_RealDB' \
     -count=1 -v 2>&1 |
     tee /tmp/guard_behavior.out | grep -E '^(    )*--- (PASS|FAIL)'; then
  :
fi

# 必须真的跑起来了，不能因为「没输出」而恒真
for top in TestStartupDDLGuards_RealDB TestStartupDDLGuards_EarlierOnes_RealDB; do
  grep -q "^--- PASS: $top" /tmp/guard_behavior.out ||
    fail "行为验证没有整体通过：$top"
done
# 负控与决定性两条是这套验证的全部价值所在，必须逐条点名
for must in \
  'PASS: TestStartupDDLGuards_RealDB/provider_models_canonical_cleared_at/DDL_负控' \
  'PASS: TestStartupDDLGuards_RealDB/provider_soft_delete/DDL_负控' \
  'PASS: TestStartupDDLGuards_RealDB/goal_client_signal/DDL_负控' \
  'PASS: TestStartupDDLGuards_RealDB/provider_models_canonical_cleared_at/C_决定性' \
  'PASS: TestStartupDDLGuards_RealDB/provider_soft_delete/C_决定性' \
  'PASS: TestStartupDDLGuards_RealDB/goal_client_signal/C_决定性' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/credits_charged/D_负控' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/credits_charged/C_决定性' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/quality_fix_mode/D_负控' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/quality_fix_mode/C_决定性' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/quality_fix_mode/rollup_必须始终被建出来' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/work_type_request_logs/D_负控' \
  'PASS: TestStartupDDLGuards_EarlierOnes_RealDB/work_type_request_logs/C_决定性' ; do
  grep -q -- "$must" /tmp/guard_behavior.out || fail "缺少关键读数: $must"
done

echo
if [ "$FAILED" -eq 0 ]; then
  echo "==== 行为验证全部通过（六条守卫：6 条负控 + 6 条决定性读数） ===="
else
  echo "==== 行为验证存在缺口 ===="
fi
exit "$FAILED"