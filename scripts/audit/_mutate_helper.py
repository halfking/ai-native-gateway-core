#!/usr/bin/env python3
"""_mutate_helper.py —— 变异 harness 的文件操作 helper（2026-10-21）。

# 为什么要有它

变异脚本要精确地做两件事：**在唯一的锚点处改坏**、**一字不差地还原**。
用 shell 做这两件事要面对 tab / 引号 / 反斜杠 / 换行的四重转义地狱 ⇒
本轮实测踩了三种坑：

1. `grep -c -F` 按**行**计数 ⇒ 多行锚点被拆开分别匹配 ⇒ 命中数虚高
   （实测报「命中 248 次」而实际只有 1 处）；
2. 锚点里含**真实 tab** ⇒ 在 shell heredoc 里写不出来 / 写出来也对不齐；
3. 替换物里含 `\\$1` / 真实换行 ⇒ 注入后 Go 代码**编译红**
   ⇒ 「编译红」按纪律**不算数**（只说明代码坏了，不说明判据有牙）。

⇒ 于是把「计数 / 替换 / 还原」全部交给 python，shell 只传 **base64 编码**的参数
（base64 无需转义，跨平台也安全）。

# 用法

    python3 _mutate_helper.py count <文件> <base64(锚点)>
    python3 _mutate_helper.py replace <文件> <base64(锚点)> <base64(替换物)>
    python3 _mutate_helper.py replace_line <文件> <base64(行内子串)> <base64(新行)>

* `count`：返回锚点在文件里的**字面量**出现次数（stdout 一个整数）。
* `replace`：锚点必须**恰好命中 1 次**，否则以退出码 2 拒绝（stdout 打印命中数）。
* `replace_line`：找**包含**该子串的**唯一一行**，把**整行**替换成新行
  （用于「锚点含 tab / 换行」的场景 —— 只传行内子串即可）。

# 转义约定

`replace` / `replace_line` 的第三个参数里支持这些转义（解码后写入文件）：

    \\t  → tab
    \\n  → 换行
    \\\\  → 反斜杠

⇒ shell 端不必写真实 tab。
"""

import base64
import sys


def decode(b64: str) -> str:
    return base64.b64decode(b64.encode("ascii")).decode("utf-8")


def unescape(s: str) -> str:
    """把 shell 端传来的转义序列还原成真实字符。

    ⚠ 只处理 `\\t` / `\\n` / `\\\\` 三种 —— 其它反斜杠序列**原样保留**
      （否则 `\\$1` 这类会被吃掉，变成非法 Go 源码 ⇒ 编译红）。
    """
    out = []
    i = 0
    while i < len(s):
        if s[i] == "\\" and i + 1 < len(s):
            nxt = s[i + 1]
            if nxt == "t":
                out.append("\t")
                i += 2
                continue
            if nxt == "n":
                out.append("\n")
                i += 2
                continue
            if nxt == "\\":
                out.append("\\")
                i += 2
                continue
        out.append(s[i])
        i += 1
    return "".join(out)


def main() -> int:
    if len(sys.argv) < 4:
        print("usage: _mutate_helper.py count|replace|replace_line ...", file=sys.stderr)
        return 3
    cmd = sys.argv[1]
    path = sys.argv[2]
    # ★ needle 与 repl **同样**做转义解码 ⇒ 两边口径一致，
    #   shell 端才能用 `\t` / `\n` 表达多行锚点而不必写真实 tab。
    needle = unescape(decode(sys.argv[3]))
    with open(path, encoding="utf-8") as fh:
        text = fh.read()

    if cmd == "count":
        print(text.count(needle))
        return 0

    if cmd == "replace":
        if len(sys.argv) < 5:
            print("replace 需要替换物", file=sys.stderr)
            return 3
        repl = unescape(decode(sys.argv[4]))
        n = text.count(needle)
        if n != 1:
            print(n)
            return 2
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text.replace(needle, repl, 1))
        print(1)
        return 0

    if cmd == "replace_line":
        if len(sys.argv) < 5:
            print("replace_line 需要新行", file=sys.stderr)
            return 3
        repl = unescape(decode(sys.argv[4]))
        lines = text.split("\n")
        hits = [i for i, ln in enumerate(lines) if needle in ln]
        if len(hits) != 1:
            print(len(hits))
            return 2
        idx = hits[0]
        new_lines = lines[:idx] + repl.split("\n") + lines[idx + 1:]
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("\n".join(new_lines))
        print(1)
        return 0

    print(f"未知子命令 {cmd}", file=sys.stderr)
    return 3


if __name__ == "__main__":
    sys.exit(main())
