#!/usr/bin/env bash
# scripts/.mutate-841-retention.sh
# 变异验证：注入已知缺陷 ⇒ 对应契约门必须变红。
#
# 纪律（每条都必须自证）：
#   1. 备份用 cp 文件副本（不用 git checkout）
#   2. 每次变异后 md5 比对，确认改的确实是目标文件
#   3. 全部变异跑完恢复，并用 md5 证明恢复成功
#   4. 「包编译失败」不算「门有牙」，单独分流
set -uo pipefail
cd "$(dirname "$0")/.."

SQL=sql/migrations/startup/841_monthly_partition_retention.sql
DOWN=sql/migrations/startup/841_monthly_partition_retention.down.sql
GO=bg/partition_manager.go
GATE="go test ./sql/migrations/startup/ -run 841 -count=1"

BK=/tmp/841-mutate-backup
rm -rf "$BK"; mkdir -p "$BK"
cp "$SQL" "$BK/841.sql"; cp "$DOWN" "$BK/841.down.sql"; cp "$GO" "$BK/pm.go"
SQL_MD5=$(md5 -q "$SQL" 2>/dev/null || md5sum "$SQL" | cut -d' ' -f1)
GO_MD5=$(md5 -q "$GO" 2>/dev/null || md5sum "$GO" | cut -d' ' -f1)

restore() {
  cp "$BK/841.sql" "$SQL"; cp "$BK/841.down.sql" "$DOWN"; cp "$BK/pm.go" "$GO"
  local a b
  a=$(md5 -q "$SQL" 2>/dev/null || md5sum "$SQL" | cut -d' ' -f1)
  b=$(md5 -q "$GO" 2>/dev/null || md5sum "$GO" | cut -d' ' -f1)
  if [[ "$a" == "$SQL_MD5" && "$b" == "$GO_MD5" ]]; then
    echo "RESTORE_OK md5 已复原"
  else
    echo "★ RESTORE_FAILED 源文件未复原！sql=$a go=$b"; exit 1
  fi
}
trap restore EXIT

teeth=0; toothless=0; broken=0

# run <id> <描述> <期望失败的测试名片段>
run() {
  local id="$1" desc="$2" want="$3"
  local out rc
  out=$($GATE 2>&1); rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "STILL_GREEN $id — $desc  ⇒ 门无牙"
    toothless=$((toothless+1)); return
  fi
  if echo "$out" | grep -qE "build failed|cannot find|undefined:|syntax error"; then
    echo "BUILD_BROKEN $id — $desc  ⇒ 包没编译起来，不算有牙"
    broken=$((broken+1)); return
  fi
  if [[ -n "$want" ]] && ! echo "$out" | grep -q "$want"; then
    echo "WRONG_GATE  $id — $desc  ⇒ 红了但不是 $want 这条，锚点挂错"
    broken=$((broken+1)); return
  fi
  echo "HAS_TEETH    $id — $desc"
  teeth=$((teeth+1))
}

echo "=== 基线（应为全绿）==="
$GATE >/dev/null 2>&1 && echo "baseline GREEN ok" || { echo "★ 基线就不绿，先修"; exit 1; }

# M1: < 改 <= ⇒ 当月与预建的未来分区会被 DROP
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
assert 'IF mth < cutoff THEN' in s
open(p,'w',encoding='utf-8').write(s.replace('IF mth < cutoff THEN','IF mth <= cutoff THEN'))
PY
run M1 "过期判定 < 改 <=（会误删当月/未来分区）" "NeverExpiresCurrentOrFuture"
restore >/dev/null

# M2: 去掉 retain_months >= 1 的 CHECK
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
assert 'retain_months >= 1' in s
open(p,'w',encoding='utf-8').write(s.replace('CHECK (retain_months >= 1)','CHECK (retain_months >= -999)'))
PY
run M2 "移除 retain_months>=1 下限" "ConfigTableShipsEmpty"
restore >/dev/null

# M3: 迁移里写死一行保留期 ⇒ 上线即删生产数据
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
anchor='COMMENT ON TABLE public.llm_gateway_partition_retention IS'
assert anchor in s
open(p,'w',encoding='utf-8').write(s.replace(anchor,
 "INSERT INTO public.llm_gateway_partition_retention (family, retain_months) VALUES ('session_turns', 1);\n"+anchor))
PY
run M3 "迁移里种一行保留期（建表即空被破）" "ConfigTableShipsEmpty"
restore >/dev/null

# M4: DROP 不走 %I 引用
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
old="EXECUTE format('DROP TABLE IF EXISTS public.%I', rec.partition_name);"
assert old in s
open(p,'w',encoding='utf-8').write(s.replace(old,
 "EXECUTE 'DROP TABLE IF EXISTS public.' || rec.partition_name;"))
PY
run M4 "DROP 改成字符串拼接（标识符不引用）" "OnlyTargetsMonthNamedChildren"
restore >/dev/null

# M5: 去掉审计写入
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
old="""        INSERT INTO public.llm_gateway_partition_drop_log
               (family, parent_name, partition_name, partition_month, live_rows)
        VALUES (rec.family, rec.parent_name, rec.partition_name,
                rec.partition_month, rec.live_rows);
"""
assert old in s
open(p,'w',encoding='utf-8').write(s.replace(old,"        -- audit removed by mutation\n"))
PY
run M5 "去掉 DROP 审计写入" "DropIsAudited"
restore >/dev/null

# M6: 父表匹配放宽成 LIKE '%' ⇒ 族名配错就误删别家分区
python3 - "$SQL" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
assert 'parent.relname = cfg.family' in s
open(p,'w',encoding='utf-8').write(s.replace('parent.relname = cfg.family',"parent.relname LIKE '%'"))
PY
run M6 "父表匹配放宽成 LIKE '%'" "OnlyTargetsMonthNamedChildren"
restore >/dev/null

# M7: down.sql 顺手删业务分区
python3 - "$DOWN" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
anchor='COMMIT;'
open(p,'w',encoding='utf-8').write(s.replace(anchor,"DROP TABLE IF EXISTS public.session_turns_2026_09;\n\n"+anchor,1))
PY
run M7 ".down.sql 顺手删业务分区" "DownDoesNotDropPartitions"
restore >/dev/null

# M8: Go 侧去掉 42P01 降级
python3 - "$GO" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
assert 'if isUndefinedTable(err) || isUndefinedFunction(err) {' in s
open(p,'w',encoding='utf-8').write(s.replace(
 'if isUndefinedTable(err) || isUndefinedFunction(err) {','if false {'))
PY
run M8 "Go 侧去掉 42P01/42883 降级" "GoCallerDegradesOnMissingTable"
restore >/dev/null

# M9: Go 侧把 Info 降成 Debug ⇒ 审计时看不到「本该删却没删」
python3 - "$GO" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding='utf-8').read()
old='slog.Info("partition_manager: monthly partition retention sweep"'
assert old in s
open(p,'w',encoding='utf-8').write(s.replace(old,'slog.Debug("partition_manager: monthly partition retention sweep"'))
PY
run M9 "Go 侧 sweep 结果降成 Debug" "GoCallerDegradesOnMissingTable"
restore >/dev/null

echo
echo "=== 汇总 ==="
echo "有牙 $teeth / 无牙 $toothless / 门坏掉 $broken （共 9 条变异）"
[[ $toothless -eq 0 && $broken -eq 0 ]]
