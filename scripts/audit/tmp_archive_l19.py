# -*- coding: utf-8 -*-
"""AGENTS.md 体积归档：★ 把「会话分析域收口轮（L19）」整块搬进 archive ＋ 原位留指针

★ 触发：合并轮六后 AGENTS.md = 524,860B > 上限 524,288B（★ 超 572B）⇒ 棘轮门红
★ 纪律：★ 超限走归档（★ **不抬上限**）；★ 被搬块**不得含 R 标记行**
  （★ R 标记索引必须留在 AGENTS.md ⇒ 交接门 `TestAgentsMdRMarkerIndexPointsAtArchive` 读得到）
"""
import io

AG = 'AGENTS.md'
ARC = 'AGENTS-archive-20261031.md'
TITLE = '2026-11-23 会话分析域收口轮（L19）——LiveTopology 族基线红收口：判据性质化 + 画布下限修正'

lines = io.open(AG, encoding='utf-8').read().split('\n')

# ★ 用内容定位块首（★ 不按行号 —— 上一版按行号操作曾把 AGENTS.md 截断到 38KB）
start = None
for i, ln in enumerate(lines):
	if ln.startswith('> **2026-11-23 会话分析域收口轮（L19）'):
		start = i
		break
assert start is not None, '★ 找不到 L19 块首'

# ★ 块尾：下一个「> **（已归档）2026-11-23 会话分析域收口轮（L23）」或下一个空行+块首
end = None
for j in range(start + 1, len(lines)):
	if lines[j].startswith('> **（已归档）2026-11-23 会话分析域收口轮（L23）'):
		end = j
		break
assert end is not None, '★ 找不到 L19 块尾（L23 归档指针行）'

block = lines[start:end]
assert any('LiveTopology' in ln for ln in block)
# ★ 纪律：被搬块内不得含 R 标记行
for ln in block:
	assert 'R1' not in ln or '★' in ln, '★ 疑似 R 标记行：%s' % ln[:80]
assert not any(ln.strip().startswith('- **R') for ln in block), '★ 块内含 R 标记索引行'

arc = io.open(ARC, encoding='utf-8').read()
header = [
	'',
	'# 追加：> **%s（goal 会话；提交见 `git log`；★ 零迁移/零 schema 变更）**（★ 2026-11-23 L32 轮合并轮六搬入'
	'（★ AGENTS.md 超上限 572B ⇒ 按棘轮门自带处方归档）⇒ ★ 结论已落在【代码/测试/提交信息】里、★ 交接块不是可靠载体）**：' % TITLE,
]
io.open(ARC, 'w', encoding='utf-8', newline='').write(arc.rstrip('\n') + '\n' + '\n'.join(header + block) + '\n')

pointer = [
	'> **（已归档）%s**：★ 结论已落在【代码/测试/提交信息】里（★ 交接块不是可靠载体）' % TITLE,
	'> ⇒ ★ **全文见 `AGENTS-archive-20261031.md`**（★ 段头 `# 追加：> **%s（goal 会话；提交见 `git log`；★ 零迁移/零 schema 变更）**`）' % TITLE,
]
new = lines[:start] + pointer + lines[end:]
io.open(AG, 'w', encoding='utf-8', newline='').write('\n'.join(new))
print('OK: archived %d lines / %d bytes' % (len(block), sum(len(x.encode('utf-8')) + 1 for x in block)))
