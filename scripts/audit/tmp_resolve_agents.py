# -*- coding: utf-8 -*-
"""AGENTS.md 冲突解决（★ 内容定位、★ 不依赖硬编码行号 —— 上一版按行号操作曾把文件截断到 38KB）

规则（本仓既定纪律）：
  · 块1「续轮摘要」：★ 同一句取上游版（★ 严格超集：多了 ⑯ 门扩四段）
  · 块2「2026-11-16 归档指针」：★ 取我方版（★ 体积是硬约束 524,288B、★ 我方已把
    12h 修订审计轮搬进 archive 留指针）★ **但补上上游独有的两条归档指针**
    （对账页审计轮 / 11-15 限流轮 —— ★ 我方这处块里没有）
"""
import io

p = 'AGENTS.md'
lines = io.open(p, encoding='utf-8').read().split('\n')

# ---- 定位所有冲突块（★ 按标记内容切分，不按行号）----
blocks = []
i = 0
while i < len(lines):
	if lines[i].startswith('<<<<<<< '):
		start = i
		j = i + 1
		while not lines[j].startswith('======='):
			j += 1
		sep = j
		k = sep + 1
		while not lines[k].startswith('>>>>>>> '):
			k += 1
		blocks.append((start, sep, k))
		i = k + 1
	else:
		i += 1

assert len(blocks) == 2, '★ 期望 2 个冲突块，★ 实得 %d' % len(blocks)

def resolve_block(start, sep, end):
	ours = lines[start + 1:sep]
	theirs = lines[sep + 1:end]
	# 块1：两侧都含「续轮已把修法实现并实测通过」那一句
	cur = [ln for ln in ours if '续轮已把修法实现并实测通过' in ln]
	sup = [ln for ln in theirs if '续轮已把修法实现并实测通过' in ln]
	if cur and sup:
		assert '⑯' not in cur[0], '★ 我方那行不该已含 ⑯'
		assert '⑯' in sup[0], '★ 上游那行必须是含 ⑯ 的超集'
		out = list(ours)
		out[ours.index(cur[0])] = sup[0]
		return out, '块1：同一句取上游超集版'
	# 块2：归档指针段
	if any('2026-11-16 自更新链路审计与落地轮' in ln for ln in ours[:3]):
		dup = [ln for ln in theirs if '对账页供应商与模型数据完整性审计轮' in ln]
		lim = [ln for ln in theirs if '限流统一按 key 轮' in ln]
		assert dup and lim, '★ 上游独有的两条归档指针应存在'
		return ours + dup + lim, '块2：取我方归档指针版 ＋ 补上游独有两条指针'
	raise AssertionError('★ 未识别的冲突块')

# 从后往前替换（★ 不影响前面的行号）
for start, sep, end in reversed(blocks):
	content, why = resolve_block(start, sep, end)
	lines = lines[:start] + content + lines[end + 1:]
	print('OK:', why)

assert not any(ln.startswith('<<<<<<<') or ln.startswith('>>>>>>>') or ln == '=======' for ln in lines)
io.open(p, 'w', encoding='utf-8', newline='').write('\n'.join(lines))
print('★ 冲突全部解决')
