#!/usr/bin/env bash
# R93 轮：变异证明 —— `char_concat_guard_test.go` 这道门**必须有牙**。
#
# ★ 纪律（来自 R89/R92 的教训）：
#   ① 变异必须**改形态不改断言**（把被守的形态改坏 ⇒ 门必须红）。
#   ② **编译红 ≠ 断言红** ⇒ 变异后必须先确认整包能编译，再看断言结果。
#   ③ 还原必须**逐字节**校验（md5 对拍被改的那个路径 —— 不是别处）。
#   ④ 每个变异跑**单测**（快）＋ 至少一个跑**整包**（证明不误伤其它门）。
#
# 变异清单：
#   M1  在一个**被扫描的**文件里注入裸拼接（`c.relkind || 'x'`）⇒ 门必须红
#   M2  去掉一处 `::text`（`.db-audit/sql/01_baseline.sql` 的 `c.relkind::text`）⇒ 门必须红
#   M3  把白名单删到 5 个（< 30 ⇒ 量具自检必须红）⇒ 门必须红
#   M4  改判据本体（让 `r93CharConcats` 永远返回空）⇒ 门必须红
set -uo pipefail
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go || exit 2

PASS=0; FAIL=0
say() { printf '%s\n' "$*"; }
ok()   { PASS=$((PASS+1)); say "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); say "  ✗ $1"; }

GUARD_TEST="scripts/ursmcheck/char_concat_guard_test.go"
TARGET_SQL=".db-audit/sql/01_baseline.sql"

md5_of() { md5 -q "$1"; }

run_gate() {
  # 只跑这道门；返回 0=绿 1=红
  go test ./scripts/ursmcheck/ -run 'TestNoCharColumnConcatenatedWithoutCast|TestR93CharConcatJudgment' -count=1 >/tmp/r93_mut_gate.log 2>&1
}

say "== M0 基线：门必须是绿的 =="
if run_gate; then ok "M0 基线绿（门的基线状态正确）"; else bad "M0 基线就红了 ⇒ 先修基线再谈变异"; tail -20 /tmp/r93_mut_gate.log; fi

# ── M1：注入裸拼接 ─────────────────────────────────────────────────────
say "== M1：注入裸拼接（改形态不改断言）=="
F=".db-audit/sql/zz_r93_mutation_probe.sql"
mkdir -p .db-audit/sql
# ★★ 探针内容在**运行时**拼出来，源码里不留裸拼接形态 ——
#   否则这道门会**如实**把本脚本自己判成真阳性（第一版就是这样 ⇒ 收尾整包红）。
#   ★★ 这不是「门误报」：那是**真的裸拼接**，门判对了 ⇒ ⇒ 改的是**本脚本**。
{
  printf '%s\n' "-- R93 变异探针（M1 临时文件，验完即删）"
  printf 'SELECT c.%s %s %s FROM pg_class c;\n' "relkind" "||" "'x'"
} > "$F"
M1_MD5_BEFORE="$(md5_of "$TARGET_SQL")"
if run_gate; then
  bad "M1：注入裸拼接后门**仍然绿** ⇒ 门没有牙"
else
  if grep -q "zz_r93_mutation_probe" /tmp/r93_mut_gate.log; then
    ok "M1：门红且**命中了变异文件**（不是别的错）"
  else
    bad "M1：门红了但报错里没有变异文件 ⇒ 红的不是它"
    tail -20 /tmp/r93_mut_gate.log
  fi
fi
rm -f "$F"
if [ "$(md5_of "$TARGET_SQL")" = "$M1_MD5_BEFORE" ]; then ok "M1 还原：目标文件 md5 未变"; else bad "M1 还原：目标文件被改动了"; fi
# ★★ 探针文件必须**当场**删干净：它留在树里 ⇒ 收尾的整包检查必然红
#   （R93 第一版就是在这里红了一次 ⇒ 看起来像「变异有残留」，其实是探针没删）
if [ -e "$F" ]; then bad "M1 探针文件仍在：$F"; else ok "M1 探针文件已删除（收尾整包检查的前提）"; fi

# ── M2：去掉一处 ::text ───────────────────────────────────────────────
say "== M2：去掉一处 ::text（真阳性反向）=="
M2_MD5_BEFORE="$(md5_of "$TARGET_SQL")"
cp "$TARGET_SQL" /tmp/r93_m2_backup.sql
python3 - "$TARGET_SQL" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "c.relkind::text"
assert s.count(old) >= 1, "没找到 c.relkind::text"
open(p, "w", encoding="utf-8").write(s.replace(old, "c.relkind", 1))
print("  (已去掉 1 处 ::text)")
PYEOF
if run_gate; then
  bad "M2：去掉 ::text 后门**仍然绿** ⇒ 门只认『新注入』不认『已修复』"
else
  if grep -q "01_baseline.sql" /tmp/r93_mut_gate.log; then ok "M2：门红且命中 01_baseline.sql"; else bad "M2：门红了但没命中目标文件"; tail -20 /tmp/r93_mut_gate.log; fi
fi
cp /tmp/r93_m2_backup.sql "$TARGET_SQL"
if [ "$(md5_of "$TARGET_SQL")" = "$M2_MD5_BEFORE" ]; then ok "M2 还原：目标文件 md5 逐字一致"; else bad "M2 还原：md5 不一致"; fi

# ── M3：把白名单削到 5 个（量具自检必须红）────────────────────────────
say "== M3：白名单削到 5 个（量具自检必须红）=="
M3_MD5_BEFORE="$(md5_of "$GUARD_TEST")"
cp "$GUARD_TEST" /tmp/r93_m3_backup.go
python3 - "$GUARD_TEST" <<'PYEOF'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
m = re.search(r"var r93CharColumns = \[\]string\{(.*?)\n\}", s, re.S)
assert m, "没找到白名单"
new = '\n\t"relkind", "attstorage", "attalign", "contype", "partstrat",'
s2 = s[:m.start()] + "var r93CharColumns = []string{" + new + "\n}" + s[m.end():]
open(p, "w", encoding="utf-8").write(s2)
print("  (白名单已削到 5 个)")
PYEOF
if run_gate; then
  bad "M3：白名单被削后门**仍然绿** ⇒ 量具自检失效"
else
  if grep -qE "白名单只有|判据口径失效" /tmp/r93_mut_gate.log; then ok "M3：门红且是**量具自检**红的（白名单不足）"; else bad "M3：门红了但不是量具自检"; tail -20 /tmp/r93_mut_gate.log; fi
fi
cp /tmp/r93_m3_backup.go "$GUARD_TEST"
if [ "$(md5_of "$GUARD_TEST")" = "$M3_MD5_BEFORE" ]; then ok "M3 还原：门文件 md5 逐字一致"; else bad "M3 还原：md5 不一致"; fi

# ── M4：改判据本体（永远返回空）──────────────────────────────────────
say "== M4：判据本体失效（永远返回空）=="
M4_MD5_BEFORE="$(md5_of "$GUARD_TEST")"
cp "$GUARD_TEST" /tmp/r93_m4_backup.go
python3 - "$GUARD_TEST" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "\ttoks := r93Tokenize(line)"
assert old in s
s = s.replace(old, "\ttoks := r93Tokenize(line)\n\tif true {\n\t\treturn nil\n\t}", 1)
open(p, "w", encoding="utf-8").write(s)
print("  (判据已被短路)")
PYEOF
if run_gate; then
  bad "M4：判据被短路后门**仍然绿** ⇒ 判据根本不参与判定"
else
  if grep -q "TestR93CharConcatJudgment" /tmp/r93_mut_gate.log; then ok "M4：门红且是**自检**红的（判据失效被自检抓住）"; else bad "M4：门红了但不是自检"; tail -20 /tmp/r93_mut_gate.log; fi
fi
cp /tmp/r93_m4_backup.go "$GUARD_TEST"
if [ "$(md5_of "$GUARD_TEST")" = "$M4_MD5_BEFORE" ]; then ok "M4 还原：门文件 md5 逐字一致"; else bad "M4 还原：md5 不一致"; fi

# ── 收尾：整包必须回绿 ───────────────────────────────────────────────
say "== 收尾：整包必须回绿（证明变异没有留下残留）=="
if go test ./scripts/ursmcheck/ -count=1 >/tmp/r93_mut_pkg.log 2>&1; then
  ok "整包 scripts/ursmcheck 绿（变异已全部还原）"
else
  bad "整包红 ⇒ 有残留"
  tail -30 /tmp/r93_mut_pkg.log
fi

rm -f /tmp/r93_m2_backup.sql /tmp/r93_m3_backup.go /tmp/r93_m4_backup.go
say ""
say "== 变异结论：通过 ${PASS} / 失败 ${FAIL} =="
[ "$FAIL" = "0" ] || exit 1
