#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""two-caliber-crosscheck.py —— 「两个独立口径 + 不一致即拒绝出读数」的**可复用判据模板**

§19.20.54 ⑧ 1 的待办：把「双口径对拍」从【某一个工具里的写法】提升为【仓内可复用的模板】。

# 它是什么

一个**只做对拍、不产出任何结论**的判据工具。给它两份「对同一个总体、按两种口径
各自算出来的读数」，它回答且只回答一件事：

    这两个口径**一致**吗？
      一致   ⇒ 退出码 0（调用方可以给结论）
      不一致 ⇒ 退出码 2（★ **拒绝出读数**）
      输入无效 ⇒ 退出码 3（★ 分母不可信 ⇒ 拒绝出结论）

# ★★ 为什么要「不一致即拒绝」而不是「打个警告继续」

R93 轮实测：某个「跑一批包再统计」的脚本在某包**没跑全**时把该包记成「-」，
汇总表再把「- → FAIL」写成「判定变化」⇒ ⇒ 报出 12 条「判定变化」，真实只有 8 条。
⇒ ⇒ 根因不是算错，是**分母被悄悄换掉了**，而没有任何一层会报警。
⇒ ⇒ ⇒ **⇒ 判据必须自己算出「我看到的」和「我应该看到的」，两者不一致就拒绝出读数。**

# ★★★ 两条必须写进契约的纪律

1. ★ **两侧口径必须独立**（由**不同的代码路径**各自算出）⇒ ⇒ 否则「一致」是恒真的
   ⇒ ⇒ ⇒ 所以本工具**要求显式声明** `--independent`（两次独立口径），
   ⇒ ⇒ ⇒ ⇒ 否则拒绝执行（退出码 3）⇒ ⇒⇒ ⇒ **⇒ 把「我知道自己在干什么」从口头约定变成显式动作。**
   ★★ 本工具**自己不算任何一个口径** ⇒ ⇒ 它天然不可能「算出」一个与另一侧同源的口径。
2. ★★ **「一致」只证明「两个口径对同一总体给出一致的划分」**
   ⇒ ⇒ **不证明「这个划分正确」** ⇒ ⇒ ⇒ 分类对不对仍要逐条拿原始证据判
   ⇒ ⇒ ⇒ （与 §19.20.54 ⑥ 2 同一条纪律）

# 输入格式

两份 TSV，**每行 `键<TAB>值`**：

  · 键 = 被观测对象的标识（例：`./admin\tTestFoo`）
  · 值 = 该对象上的读数（例：`PASS`）

  · `#` 开头的行与空行被忽略
  · 同一份文件里**不允许键重复**（重复 ⇒ 分母不可信 ⇒ 退出码 3）

# 用法

  two-caliber-crosscheck.py --a A.tsv --b B.tsv [--relation eq|superset|subset]
                            [--label-a 名称] [--label-b 名称]
                            [--key-cols 0,1] [--value-col M] [--independent]
                            [--json]

  --relation  A 与 B 的键集合关系（默认 eq）
                eq        ⇒ 键集合必须完全相同
                superset  ⇒ A ⊇ B（B 的每个键都必须出现在 A 里）
                subset    ⇒ A ⊆ B
  --key-cols  键由哪几列拼成（逗号分隔）⇒ 默认单列 0
              ★ 被观测对象常是二元组（矩阵场景：包 + 测试函数名）⇒ 这时用 `--key-cols 0,1`
  --value-col 从每行的哪一列取值（默认 1）⇒ 便于对接「多列台账」
  --independent
                声明两侧口径**互相独立**；不给 ⇒ 退出码 3（见纪律 1）

# 退出码

  0  一致 ⇒ 调用方可以给结论
  2  不一致 ⇒ ★ **拒绝出读数**
  3  输入无效（文件缺失 / 某侧为空 / 行格式错 / 键重复 / 未声明 --independent）⇒ 拒绝出结论
"""
import argparse
import json
import os
import sys

EXIT_OK = 0
EXIT_MISMATCH = 2
EXIT_INVALID = 3


class InputInvalid(Exception):
    """输入本身不可信 ⇒ 拒绝出结论（不是「不一致」，是「没法比」）。"""


def read_caliber(path, key_cols, value_col, label):
    """读一份口径文件 ⇒ 返回 {键: 值}。

    ★ 键可以是**复合的**（--key-cols 0,1 ⇒ 键 = 第 0 列 + '|' + 第 1 列）
      ⇒ 因为「被观测对象」常常是二元组（矩阵场景：包 + 测试函数名）。
    """
    if not os.path.exists(path):
        raise InputInvalid("%s 的口径文件不存在: %s" % (label, path))
    out = {}
    order = []
    with open(path, encoding="utf-8", errors="replace") as f:
        for ln, raw in enumerate(f, 1):
            line = raw.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            cols = line.split("\t")
            need = max(max(key_cols), value_col) + 1
            if len(cols) < need:
                raise InputInvalid("%s 第 %d 行列数 = %d，不足 %d 列: %s"
                                   % (label, ln, len(cols), need, line[:120]))
            k = "|".join(cols[c].strip() for c in key_cols)
            v = cols[value_col].strip()
            if not k.strip("|"):
                raise InputInvalid("%s 第 %d 行键为空" % (label, ln))
            if k in out:
                raise InputInvalid("%s 第 %d 行键重复: %s" % (label, ln, k))
            out[k] = v
            order.append(k)
    if not out:
        raise InputInvalid("%s 的口径是空集 ⇒ 分母为 0 ⇒ 拒绝出结论" % label)
    return out, order


def main():
    ap = argparse.ArgumentParser(description="两个独立口径的对拍；不一致即拒绝出读数")
    ap.add_argument("--a", required=True, help="口径 A 的 TSV")
    ap.add_argument("--b", required=True, help="口径 B 的 TSV")
    ap.add_argument("--relation", default="eq", choices=["eq", "superset", "subset"])
    ap.add_argument("--label-a", default="A")
    ap.add_argument("--label-b", default="B")
    ap.add_argument("--key-cols", default="0",
                    help="键由哪几列拼成（逗号分隔，如 0,1 ⇒ 键 = 第0列|第1列）；默认单列 0")
    ap.add_argument("--value-col", type=int, default=1)
    ap.add_argument("--independent", action="store_true",
                    help="★ 声明两侧口径由不同代码路径各自算出（不给则拒绝执行）")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    key_cols = []
    for tok in args.key_cols.split(","):
        tok = tok.strip()
        if not tok.isdigit():
            sys.stderr.write("!! --key-cols 含非数字列号: %r\n" % args.key_cols)
            return EXIT_INVALID
        key_cols.append(int(tok))
    if not key_cols:
        sys.stderr.write("!! --key-cols 不能为空\n")
        return EXIT_INVALID

    def emit_invalid(msg):
        """★ 输入无效时也要给机器可读的输出（否则调用方只能靠 exit code 猜）。"""
        if args.json:
            print(json.dumps({"consistent": False, "valid": False, "error": msg},
                             ensure_ascii=False, indent=2))
        else:
            sys.stderr.write("!! %s\n" % msg)
        return EXIT_INVALID

    try:
        if not args.independent:
            raise InputInvalid(
                "未声明 --independent ⇒ ★ 两个口径若同源，「一致」是恒真的 ⇒ 拒绝执行\n"
                "  ⇒ 纪律：两侧必须由**不同的代码路径**各自算出（见文件头纪律 1）")
        a, a_order = read_caliber(args.a, key_cols, args.value_col, args.label_a)
        b, b_order = read_caliber(args.b, key_cols, args.value_col, args.label_b)
    except InputInvalid as e:
        return emit_invalid(str(e))

    ka, kb = set(a), set(b)
    inter = ka & kb
    only_a = [k for k in a_order if k in ka - kb]
    only_b = [k for k in b_order if k in kb - ka]
    # ★ 值不一致：同一键上两侧读数不同 ⇒ 也是「不一致」（即使键集合相同）
    value_mismatch = [(k, a[k], b[k]) for k in a_order if k in inter and a[k] != b[k]]

    # ★★ 恒等式自检：|A| = |A∩B| + |只A| ⇒ 打印出来 ⇒ 读者能自己验分母没被换掉
    ident_a = "|A|=%d = |A∩B|=%d + |只A|=%d ⇒ %s" % (
        len(ka), len(inter), len(only_a),
        "✓" if len(ka) == len(inter) + len(only_a) else "✗ 恒等式不成立")
    ident_b = "|B|=%d = |A∩B|=%d + |只B|=%d ⇒ %s" % (
        len(kb), len(inter), len(only_b),
        "✓" if len(kb) == len(inter) + len(only_b) else "✗ 恒等式不成立")

    rel_ok = True
    if args.relation == "eq":
        rel_ok = (not only_a) and (not only_b)
    elif args.relation == "superset":
        rel_ok = (not only_b)          # B 的每个键都在 A 里
    else:  # subset
        rel_ok = (not only_a)

    consistent = rel_ok and not value_mismatch

    if args.json:
        print(json.dumps({
            "consistent": consistent,
            "valid": True,
            "relation": args.relation,
            "counts": {"a": len(ka), "b": len(kb), "inter": len(inter),
                       "only_a": len(only_a), "only_b": len(only_b),
                       "value_mismatch": len(value_mismatch)},
            "only_a": only_a, "only_b": only_b,
            "value_mismatch": [{"key": k, "a": x, "b": y} for k, x, y in value_mismatch],
            "identities": [ident_a, ident_b],
        }, ensure_ascii=False, indent=2))
    else:
        print("== 双口径对拍 ==")
        print("  口径 A = %s（%s）  口径 B = %s（%s）  关系 = %s"
              % (args.label_a, args.a, args.label_b, args.b, args.relation))
        print("  %s" % ident_a)
        print("  %s" % ident_b)
        if only_a:
            print("  ★ 只在 A（%d 个）:" % len(only_a))
            for k in only_a[:20]:
                print("      %s = %s" % (k, a[k]))
            if len(only_a) > 20:
                print("      …（其余 %d 个略）" % (len(only_a) - 20))
        if only_b:
            print("  ★ 只在 B（%d 个）:" % len(only_b))
            for k in only_b[:20]:
                print("      %s = %s" % (k, b[k]))
            if len(only_b) > 20:
                print("      …（其余 %d 个略）" % (len(only_b) - 20))
        if value_mismatch:
            print("  ★ 同一键上两侧读数不同（%d 个）:" % len(value_mismatch))
            for k, x, y in value_mismatch[:20]:
                print("      %s: A=%s  B=%s" % (k, x, y))
        if consistent:
            print("✅ 两个口径一致 ⇒ 调用方可以给结论")
            print("★ 诚实边界：一致只证明「两个口径对同一总体给出一致的划分」⇒ 不证明「划分正确」")
        else:
            print("✗ 两个口径不一致 ⇒ ★ **拒绝出读数**")
    return EXIT_OK if consistent else EXIT_MISMATCH


if __name__ == "__main__":
    sys.exit(main())
