# -*- coding: utf-8 -*-
"""AGENTS.md 冲突块 2：体积优先取我方归档指针版 ＋ 保留上游独有的两条归档指针（一次性脚本）"""
import io

p = 'AGENTS.md'
lines = io.open(p, encoding='utf-8').read().split('\n')

assert lines[655].startswith('<<<<<<<'), lines[655][:40]
assert lines[660].startswith('======='), lines[660][:40]
assert lines[676].startswith('>>>>>>>'), lines[676][:40]

ours = lines[656:660]        # 1-based 657..660：我方两条归档指针
theirs = lines[661:676]      # 1-based 662..676：上游侧

# 上游独有的两条归档指针（我方没有）
dup = [ln for ln in theirs if '对账页供应商与模型数据完整性审计轮' in ln]
limit = [ln for ln in theirs if '限流统一按 key 轮' in ln]
assert dup and limit, (len(dup), len(limit))

# 上游 662 那行是「自更新链路审计轮」的畸形指针（段头带嵌套 **）⇒ 用我方规范版
new = ours + dup + limit + lines[677:]
io.open(p, 'w', encoding='utf-8', newline='').write('\n'.join(new))
print('OK: block2 resolved (ours-archived + 2 upstream-unique pointers)')
