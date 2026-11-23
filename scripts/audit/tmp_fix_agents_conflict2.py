# -*- coding: utf-8 -*-
"""AGENTS.md 冲突块 1 修正：删首行 marker 与尾 marker（上游超集已就位）（一次性脚本）"""
import io

p = 'AGENTS.md'
lines = io.open(p, encoding='utf-8').read().split('\n')

assert lines[0].startswith('<<<<<<<'), lines[0][:40]
assert lines[81].startswith('>>>>>>>'), lines[81][:40]
# 校验：中间那行必须是上游超集版（含 ⑯）
assert '⑯' in lines[80], lines[80][:60]

new = lines[1:81] + lines[82:]
io.open(p, 'w', encoding='utf-8', newline='').write('\n'.join(new))
print('OK: markers removed')
