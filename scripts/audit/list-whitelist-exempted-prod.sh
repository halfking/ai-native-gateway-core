#!/usr/bin/env bash
# ★ 只读盘点：★ 生产代码路径里【★ 被白名单主机词豁免掉 ★】的候选行 ★★★
#
# ★ 用途（★ 2026-11-09 第十五轮审计 ★）：★ 兄弟会话的独立复核给出一条设计要求 ——
#   「★ 白名单是【★ 按值豁免 ★】的、★ 而【★ 把那批占位符值加进去、★ 同一个值哪天
#     出现在生产代码里、★ 也一样会被豁免 ★】⇒ ★ ⇒ ★ 所以白名单落地【★ 必须先有 ★】
#     一道『★ 生产代码路径不受豁免 ★』的门 ★★★
#   本工具【★ 不改扫描器语义 ★】、★ 不改任何门、★ 不裁决任何待裁决项 ——
#   它只【★ 把「★ 被豁免的那部分 ★」盘点出来 ★】、★ 作为那道门的【★ 前置证据 ★】。
#
# ★ 做法（★ 复用扫描器本体、★ 保证口径一致 ★）：
#   ① 用【★ 原样扫描器 ★】扫生产代码路径 ⇒ 得「★ 已报出 ★」集合
#   ② 用【★ 去掉主机词白名单的临时副本 ★】扫同一批路径 ⇒ 得「★ 若不豁免会报出 ★」集合
#   ③ 差集 ＝ 【★ 被白名单豁免掉的候选 ★】★ ★ 清单只读输出、★ 退出码恒 0 ★
#
# ★ 退出码：★ 恒 0（★ 纯盘点 ★）—— ★ 不因为「★ 有豁免项 ★」而红 ★
#   ⇒ ★ 因为「★ 该不该把生产路径从豁免里摘出去 ★」★ 属【★ 待裁决 ★】★

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SCANNER="${REPO_ROOT}/scripts/scan-secrets.sh"

# ★ 生产代码路径（★ 相对仓库根 ★）—— ★ 目录清单可按需增补 ★
PROD_PATHS=(
  cmd domains internal bg middleware security telemetry services
  config controller service handler model repository
)

echo "=== ★ 生产代码路径的白名单豁免盘点（★ 只读 ★）==="
echo "    仓库根：${REPO_ROOT}"
echo "    扫描器：${SCANNER}"
echo

# ★ ① 收集存在的生产代码路径（★ 不存在就跳过、★ 避免 pathspec 报错 ★）
existing=()
for p in "${PROD_PATHS[@]}"; do
  [[ -e "${REPO_ROOT}/${p}" ]] && existing+=("${p}")
done
if [[ ${#existing[@]} -eq 0 ]]; then
  echo "★ 没有可用的生产代码路径、★ 盘点结束（★ 退出码 0 ★）"
  exit 0
fi
echo "    参与盘点的生产代码路径（${#existing[@]} 个）：${existing[*]}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

# ★ ② 基线：★ 原样扫描器 ★
echo
echo "--- ① 原样扫描器（★ 白名单生效 ★）---"
( cd "${REPO_ROOT}" && bash "${SCANNER}" --mode=normal --format=text --paths "${existing[@]}" ) > "${tmpdir}/base.txt" 2>/dev/null

# ★ ③ 探针：★ 去掉主机词白名单的临时副本 ★
#   ——★ 副本放在 scripts/ 同目录 ★ ⇒ 它按 SCRIPT_DIR 找 rules config 的行为【★ 与本体一致 ★】
probe="${REPO_ROOT}/scripts/.scan-secrets-nohl.tmp.sh"
python3 - "${SCANNER}" "${probe}" <<'PYEOF'
import re, sys
src, dst = sys.argv[1], sys.argv[2]
s = open(src, encoding='utf-8').read()
old = "  'NT 10\\.0' 'example\\.com' 'example\\.org' '127\\.0\\.0\\.1' '0\\.0\\.0\\.0' 'localhost'"
if old in s:
    s = s.replace(old, "  'NT 10\\.0'", 1)
else:
    # 兜底：★ 白名单首行形态变了就整行替换掉主机词那一行 ★
    s = re.sub(r"^\s*'NT 10\\\\\.0'.*$", "  'NT 10\\\\.0'", s, count=1, flags=re.M)
open(dst, 'w', encoding='utf-8').write(s)
print('probe written')
PYEOF
chmod +x "${probe}" 2>/dev/null || true
echo "--- ② 去主机词白名单的探针（★ 豁免失效 ★）---"
( cd "${REPO_ROOT}" && bash "${probe}" --mode=normal --format=text --paths "${existing[@]}" ) > "${tmpdir}/nohl.txt" 2>/dev/null
rm -f "${probe}"

# ★ ④ 差集 ★
python3 - "${tmpdir}/base.txt" "${tmpdir}/nohl.txt" <<'PYEOF'
import re, sys
from collections import Counter
pat = re.compile(r'^\[(BLOCK|WARN)\]\s+(.+?):(\d+)\s+([A-Z_]+)\s+—')
def load(p):
    out = {}
    for ln in open(p, encoding='utf-8', errors='replace'):
        m = pat.match(ln.strip())
        if m:
            out[(m.group(2), m.group(3), m.group(4))] = m.group(1)
    return out
base = load(sys.argv[1]); nohl = load(sys.argv[2])
exempted = {k: v for k, v in nohl.items() if k not in base}
print('    已报出（白名单生效）：%d 条' % len(base))
print('    若不豁免会报出：%d 条' % len(nohl))
print('    ==> 被白名单豁免掉的候选：%d 条' % len(exempted))
if exempted:
    print()
    print('    ★ 被豁免候选按分类：%s' % dict(Counter(k[2] for k in exempted)))
    print()
    for (f, ln, cat), sev in sorted(exempted.items()):
        print('    %-5s %-14s %s:%s' % (sev, cat, f, ln))
PYEOF

echo
echo "=== ★ 盘点结束（★ 退出码 0 ★）—— ★ 上述清单【★ 不进任何门 ★】、★ 只作裁决依据 ★"
exit 0
