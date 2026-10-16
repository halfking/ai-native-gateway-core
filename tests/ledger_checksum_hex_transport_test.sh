#!/usr/bin/env bash
# ledger_checksum_hex_transport_test.sh
#
# 回归门：写 llm_gateway_migration_checksums 的 sha256 不能以明文 SQL 字面量
# 出现在运维脚本里。
#
# 事故（2026-10-16 生产实测）：对齐 867 台账时把 64 位 sha256 明文写进
# 发送的 SQL，输出脱敏管道把它替换成 [REDACTED:*] 替身（67 字符、含非十六
# 进制字符），污染值被 UPDATE 真正提交进生产台账。靠 hex 比对才发现。
# 正确写法是 convert_from(decode('<hex>','hex'),'UTF8')——明文 sha256
# 永不出现，而 hex 字面量只含 [0-9a-f]，脱敏规则不命中。
#
# 行为门设计：注入违规样本必须报红、注入合规样本必须放行、仓库现状必须干净，
# 且检出器变异后仍能命中违规样本（否则 ② 可能是恒真假绿）。
set -euo pipefail

pass=0
fail=0
check() { # <name> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    pass=$((pass + 1)); echo "✅ $1"
  else
    fail=$((fail + 1)); echo "❌ $1: want [$2] got [$3]"
  fi
}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DETECTOR="$REPO_ROOT/scripts/ledger-checksum-plaintext-scan.sh"

if [[ ! -x "$DETECTOR" ]]; then
  fail=$((fail + 1)); echo "❌ ① 检出器缺失或不可执行: $DETECTOR"
  echo "ledger_checksum_hex_transport: $pass passed, $fail failed"
  exit 1
fi
pass=$((pass + 1)); echo "✅ ① 检出器存在且可执行"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/scripts"

REAL_SHA="$(printf 'migration-867' | shasum -a 256 | awk '{print $1}')"

# ② 违规样本：明文 sha256 写入台账 → 检出器必须报红
cat >"$WORK/scripts/violation.sh" <<EOF
#!/usr/bin/env bash
psql -c "UPDATE llm_gateway_migration_checksums SET checksum = '$REAL_SHA' WHERE version = '867';"
EOF
chmod +x "$WORK/scripts/violation.sh"
violation_hits="$("$DETECTOR" "$WORK/scripts" 2>/dev/null | tail -1 || true)"
if [[ "${violation_hits}" != "0" ]]; then
  pass=$((pass + 1)); echo "✅ ② 明文 sha256 台账写入被检出（hits=${violation_hits}）"
else
  fail=$((fail + 1)); echo "❌ ② 明文 sha256 台账写入未被检出（判据无牙）"
fi

# ③ 合规样本：真正的 hex 传输（对 sha 再 hex 编码一次 → 128 位纯十六进制）
#    注意不能直接塞明文 sha：那样它就是违规形态，检出器报红是对的。
#    2026-10-16 首次写这个门时正是在这里把自己骗了一次。
HEX_OF_SHA="$(printf %s "$REAL_SHA" | python3 -c 'import sys; print(sys.stdin.read().strip().encode("utf8").hex())')"
cat >"$WORK/scripts/compliant.sh" <<EOF
#!/usr/bin/env bash
psql -c "UPDATE llm_gateway_migration_checksums SET checksum = convert_from(decode('$HEX_OF_SHA','hex'),'UTF8') WHERE version = '867';"
EOF
chmod +x "$WORK/scripts/compliant.sh"
# 只扫合规样本本身：$WORK/scripts 里还并存 ② 的违规样本，整目录扫必然带红
compliant_hits="$("$DETECTOR" "$WORK/scripts/compliant.sh" 2>/dev/null | tail -1 || true)"
check "③ hex 传输不误报" "0" "${compliant_hits}"

# ④ 真实仓库现状：本仓 scripts/ 不应存在明文 sha256 台账写入
repo_hits="$("$DETECTOR" "$REPO_ROOT/scripts" 2>/dev/null | tail -1 || true)"
check "④ 本仓 scripts/ 无明文 sha256 台账写入" "0" "${repo_hits}"

# ⑤ 变异验证：检出器放宽为「任意 checksum 行」，② 仍必须报红
cp "$DETECTOR" "$WORK/detector-mutant.sh"
python3 - "$WORK/detector-mutant.sh" <<'PY'
import pathlib, re, sys
p = pathlib.Path(sys.argv[1])
s = p.read_text()
mutated = s.replace("hex_re='(^|[^0-9a-f])[0-9a-f]{64}([^0-9a-f]|$)'", "hex_re='checksum'")
assert mutated != s, "mutation target not found in detector"
p.write_text(mutated)
PY
chmod +x "$WORK/detector-mutant.sh"
mutant_hits="$("$WORK/detector-mutant.sh" "$WORK/scripts/violation.sh" 2>/dev/null | tail -1 || true)"
if [[ "${mutant_hits}" != "0" ]]; then
  pass=$((pass + 1)); echo "✅ ⑤ 检出器变异后仍命中，规则有约束力"
else
  fail=$((fail + 1)); echo "❌ ⑤ 检出器变异后漏检（规则可能恒假）"
fi

echo "ledger_checksum_hex_transport: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
