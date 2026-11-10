#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""F54 验收口径门的【★ 第三引擎 ★】：★ 用 python 的 `re` 读同一份规则集与语料 ★。

★ 为什么单独一个文件：★ 主门是 bash（★ 三引擎里的两个在 shell 里 ★）
⇒ ⇒ 而 python 的 `re` 只能由 python 跑 ⇒ ⇒ 抽成一个小工具、★ 主门调用它 ★
⇒ ⇒ ★ **零外部依赖**（★ 只用标准库 ★）★

★ 用法：python3 scan_secrets_reading_set_engine.py <规则tsv> <语料> <输出>
★   规则 tsv 每行：<编号> <描述> <ERE 模式>
★   输出每行：<编号>:<行号>（★ 读数集合的元素 ★）
"""
import io
import re
import sys

# ★ ★★ POSIX 字符类的**等价翻译**（★ python 的 `re` 不认 `[[:space:]]` ★）
#   ⇒ ★ 而规则集里有 **3** 条用了它（★ 22 / 26 / 37 ★）⇒ ★ 不翻译 ⇒ 那 3 条
#   ⇒ ★ **编译失败被跳过** ⇒ ★ 第三引擎的读数集合**天然少 3 类** ⇒ ★ ⇒
#   ⇒ ★ **比较就不成立**（★ 而那不是平台分叉、★ 是引擎能力差 ★）★★ ★
#   ⇒ ⇒ 所以这里按 POSIX 定义把它们翻译成**同义字符集** ★
#   ★ ⇒ ★ 翻译表是**公开定义**（★ POSIX 1003.1 的 class 定义 ★）⇒ ★ 不是猜的 ★
_POSIX_CLASSES = {
    'space': ' \\t\\r\\n\\f\\v',
    'blank': ' \\t',
    'alnum': '0-9A-Za-z',
    'alpha': 'A-Za-z',
    'digit': '0-9',
    'upper': 'A-Z',
    'lower': 'a-z',
    'xdigit': '0-9A-Fa-f',
    'punct': re.escape('!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'),
}


def translate_posix_class(pattern):
    """把 POSIX 字符类翻译成同义字符集（★ 只处理 `[:name:]` 形态 ★）。"""
    def repl(m):
        name = m.group(1)
        return _POSIX_CLASSES.get(name, m.group(0))
    return re.sub(r'\[:(\w+):\]', repl, pattern)


rules_path, corpus_path, out_path = sys.argv[1], sys.argv[2], sys.argv[3]

corpus = io.open(corpus_path, encoding='utf-8').read().split('\n')
if corpus and corpus[-1] == '':
    corpus.pop()

hits = []
for line in io.open(rules_path, encoding='utf-8'):
    if not line.strip():
        continue
    parts = line.rstrip('\n').split('\t')
    if len(parts) < 3:
        continue
    rid, _desc, pat = parts[0], parts[1], '\t'.join(parts[2:])
    try:
        rx = re.compile(translate_posix_class(pat), re.IGNORECASE)
    except re.error:
        # ★ 与扫描器同款：★ 编译不了的规则**跳过**（★ 不让一条坏规则炸掉整门 ★）
        continue
    for idx, text in enumerate(corpus, start=1):
        if rx.search(text):
            hits.append('%s:%d' % (rid, idx))

io.open(out_path, 'w', encoding='utf-8').write('\n'.join(sorted(set(hits))) + ('\n' if hits else ''))
