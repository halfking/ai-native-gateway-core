# -*- coding: utf-8 -*-
"""AGENTS.md 冲突块 1：同一句「续轮」摘要取上游超集版，其余保留（一次性脚本）"""
import io

p = 'AGENTS.md'
lines = io.open(p, encoding='utf-8').read().split('\n')

# 1-based: 1=<<<<<<< HEAD, 81=我方那行, 82=======, 83=上游那行, 84=>>>>>>> origin/main
assert lines[0].startswith('<<<<<<<'), lines[0][:40]
assert lines[81].startswith('======='), lines[81][:40]
assert lines[83].startswith('>>>>>>>'), lines[83][:40]

ours = lines[80]
theirs = lines[82]
assert '续轮已把修法实现并实测通过' in ours and '续轮已把修法实现并实测通过' in theirs
assert '⑯' not in ours and '⑯' in theirs, '★ 上游版必须是严格超集'

# 取上游版（超集）替换我方那行，删掉三行标记
new = lines[:80] + [theirs] + lines[83:]
io.open(p, 'w', encoding='utf-8', newline='').write('\n'.join(new))
print('OK: block1 resolved, upstream superset taken')
