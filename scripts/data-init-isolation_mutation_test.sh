#!/usr/bin/env bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
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
# scripts/data-init-isolation_mutation_test.sh —— 「execute 真删行 + 每类别归属」的变异有牙证明
#
# ## 这道脚本证明什么
#
# 四条判据（后端）各自钉住本轮的一条行为事实：
#
#   M1  admin/data_lifecycle_data_init_isolated_db_test.go
#       「execute 真的把行删掉了，且**每类别的行数归属正确**」
#       —— 变异＝把「按类别 key 取值」退回成「按位置对齐」（旧形态）
#       —— 负控要点：某个类别一张表都没有时（老部署没迁移到位），
#          其后每个类别的数字整体错位 ⇒ 判据必须红。
#   M2  同上
#       「受保护行不删」（超级管理员 / default 租户 / 系统 key）
#       —— 变异＝把保留谓词摘掉 ⇒ 判据必须红。
#   M3  同上
#       「审计行与删除同事务」（删了就留痕、失败就一起回滚）
#       —— 变异＝不写审计行 ⇒ 判据必须红。
#   M4  同上
#       「DELETE 真的执行了」
#       —— 变异＝跳过 Exec ⇒ 判据必须红。
#
# ★ 判定口径：**源码层变异** —— 把实现里的那一条行为抽掉，重跑对应判据，
#   判据必须变红。判据仍绿 ⇒ 那是恒绿门（钉不住任何东西）。
#
# ★ 纪律五条：
#   ① 变异必须**自证生效**（锚点命中 + 替换确实改到了文件）—— 否则变异空跑，
#      空跑的「红」是假的。
#   ② 变异必须**还原**（trap 兜底 + 每条立刻还原），且备份目录只在全部跑完后才删
#      （★ 第一版在每条之后删备份目录 ⇒ 后一条的备份没建成 ⇒ 变异残留在树上）。
#   ③ 判据**没跑**（SKIP）≠ 判据绿 —— 隔离库腿没 DSN 就会 SKIP，
#      拿 SKIP 当绿 = 恒绿门 ⇒ 本脚本显式判 SKIP 为「不算数」。
#   ④ 还原后要能**核对**：脚本结束时打印工作树状态，由调用方比对。
#   ⑤ 判据必须打的是**生产代码**（admin/data_lifecycle_data_init.go），
#      不是测试文件里的反向调用。
#
# ## 环境变量
#
#   TEST_DATA_INIT_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL
#   （隔离库腿需要它；没设 ⇒ 脚本以 exit 2 退出，**不报绿**）
#
# 用法: bash scripts/data-init-isolation_mutation_test.sh
set -uo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit 2

PASS=0; FAIL=0
BACKUP_DIR="$(mktemp -d)"
RESTORE_FILES=()

restore_all() {
  local f
  for f in ${RESTORE_FILES[@]+"${RESTORE_FILES[@]}"}; do
    if [[ -n "$f" && -f "$BACKUP_DIR/$(basename "$f")" ]]; then
      cp "$BACKUP_DIR/$(basename "$f")" "$f" || echo "  x 还原失败: $f"
    fi
  done
}
cleanup() {
  restore_all
  rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

# 判据用的 DSN：没有它，隔离库腿会 SKIP ⇒ 拿 SKIP 当绿是自欺。
DSN="${TEST_DATA_INIT_DSN:-${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}}"
if [[ -z "$DSN" ]]; then
  echo "x 没有 DSN（TEST_DATA_INIT_DSN / LLM_GATEWAY_DATABASE_URL / DATABASE_URL 都没设）"
  echo "  ⇒ 隔离库判据会 SKIP ⇒ 本脚本拒绝把「没跑」记成「有牙」"
  exit 2
fi
export TEST_DATA_INIT_DSN="$DSN"

TARGET="admin/data_lifecycle_data_init.go"

# run_mutation <标签> <判据 -run 正则> <python 变异体>
#
# ★ 变异体必须自己 assert 锚点命中（python 抛异常 ⇒ 退出码非 0 ⇒ 判空跑）。
run_mutation() {
  local label="$1" test="$2" python_body="$3"
  local backup="$BACKUP_DIR/$(basename "$TARGET")"

  cp "$TARGET" "$backup" || { echo "  x ${label}: 备份失败"; FAIL=$((FAIL+1)); return; }
  RESTORE_FILES+=("$TARGET")

  if ! python3 -c "$python_body"; then
    echo "  x ${label}: 锚点未命中 => 变异空跑（不算数）"
    cp "$backup" "$TARGET"
    FAIL=$((FAIL+1)); return
  fi
  if cmp -s "$TARGET" "$backup"; then
    echo "  x ${label}: 变异没有真正改到文件 => 变异空跑（不算数）"
    cp "$backup" "$TARGET"
    FAIL=$((FAIL+1)); return
  fi

  local log="/tmp/data-init-mutation-${label}.log"
  go test ./admin/ -run "$test" -count=1 -v >"$log" 2>&1
  local rc=$?

  # ★ 还原**本条**（备份目录留给后面的条目用）。
  cp "$backup" "$TARGET"

  if grep -q -- "--- SKIP" "$log"; then
    echo "  x ${label}: 判据被 SKIP 了（没跑）=> 不算数（跳过不构成证据）"
    grep -E -- "--- SKIP" "$log" | head -2 | sed 's/^/      /'
    FAIL=$((FAIL+1)); return
  fi
  if [[ $rc -ne 0 ]]; then
    echo "  v ${label}: 判据变红（rc=${rc}）=> 守卫有牙"
    grep -E -- "^\s+--- FAIL|--- FAIL" "$log" | head -3 | sed 's/^/      /'
    PASS=$((PASS+1))
  else
    echo "  x ${label}: 判据仍绿 => 这是恒绿门"
    FAIL=$((FAIL+1))
  fi
}

echo "M1: 退回「按位置对齐」⇒ 每类别归属判据必须红"
run_mutation "M1" 'TestDataInitExecute_OnIsolatedDB_ReallyDeletesRows|TestDataInitPerCategoryCounts_MissingCategoryDoesNotShiftOthers' '
import pathlib
p = pathlib.Path("admin/data_lifecycle_data_init.go")
s = p.read_text()

# ① Apply 不再给「一张表都没有」的类别占位 ⇒ 返回值只含真正执行过的类别。
old1 = """	for _, d := range plan.Deletes {
		if _, ok := perCat[d.Category]; !ok {
			perCat[d.Category] = 0
		}
	}
"""
assert old1 in s, "M1 anchor 1 (Apply 占位) not found"
s = s.replace(old1, "", 1)

# ② 归属改回按位置对齐（旧形态）。
old2 = """	out := make(map[string]int64, len(order))
	for _, key := range order {
		out[key] = deleted[key]
	}
	return out
}"""
new2 = """	out := make(map[string]int64, len(order))
	vals := make([]int64, 0, len(order))
	for _, key := range order {
		if v, ok := deleted[key]; ok {
			vals = append(vals, v)
		}
	}
	for i, key := range order {
		if i < len(vals) {
			out[key] = vals[i]
		} else {
			out[key] = 0
		}
	}
	return out
}"""
assert old2 in s, "M1 anchor 2 (归属映射) not found"
s = s.replace(old2, new2, 1)
p.write_text(s)
'

echo "M2: 摘掉受保护行的保留谓词 ⇒ 受保护行判据必须红"
run_mutation "M2" 'TestDataInitExecute_OnIsolatedDB_ReallyDeletesRows' '
import pathlib
p = pathlib.Path("admin/data_lifecycle_data_init.go")
s = p.read_text()
old = """			d := dataInitDelete{Category: key, Table: t.Name}
			if t.Keep != nil {"""
new = """			d := dataInitDelete{Category: key, Table: t.Name}
			if false && t.Keep != nil {"""
assert old in s, "M2 anchor (keep predicate) not found"
s = s.replace(old, new, 1)
p.write_text(s)
'

echo "M3: 不写审计行 ⇒ 审计同事务判据必须红"
run_mutation "M3" 'TestDataInitExecute_OnIsolatedDB_ReallyDeletesRows' '
import pathlib
p = pathlib.Path("admin/data_lifecycle_data_init.go")
s = p.read_text()
old = """	// 审计行（与删除同事务）。
	if err := logAuditExec(ctx, tx, actor, action, audit); err != nil {
		return nil, fmt.Errorf("audit insert failed: %w", err)
	}
"""
assert old in s, "M3 anchor (audit insert) not found"
s = s.replace(old, "", 1)
p.write_text(s)
'

echo "M4: 跳过 DELETE ⇒ 真删行判据必须红"
run_mutation "M4" 'TestDataInitExecute_OnIsolatedDB_ReallyDeletesRows' '
import pathlib
p = pathlib.Path("admin/data_lifecycle_data_init.go")
s = p.read_text()
old = """		tag, err := tx.Exec(ctx, d.SQL, d.Args...)"""
new = """		tag, err := pgconn.CommandTag{}, error(nil)"""
assert old in s, "M4 anchor (delete exec) not found"
s = s.replace(old, new, 1)
p.write_text(s)
'

# ★ 还原核对：源码必须回到变异前的样子（否则变异残留在树上）。
echo "--- 还原核对 ---"
if grep -q "if false && t.Keep != nil" "$TARGET"; then
  echo "  x M2 的变异残留在源码里"; FAIL=$((FAIL+1))
fi
if grep -q "tag, err := pgconn.CommandTag{}" "$TARGET"; then
  echo "  x M4 的变异残留在源码里"; FAIL=$((FAIL+1))
fi
if ! grep -q "out\[key\] = deleted\[key\]" "$TARGET"; then
  echo "  x M1 的变异残留在源码里"; FAIL=$((FAIL+1))
fi
if ! grep -q "logAuditExec(ctx, tx, actor, action, audit)" "$TARGET"; then
  echo "  x M3 的变异残留在源码里"; FAIL=$((FAIL+1))
fi

echo "--- 工作树状态（由调用方比对） ---"
git -C "$REPO_ROOT" status --porcelain -- "$TARGET" | sed 's/^/  /'
echo "  (空 = 目标文件已还原)"

echo
echo "变异有牙结果: ${PASS} 红 / ${FAIL} 失败"
[[ $FAIL -eq 0 ]] || exit 1
exit 0
