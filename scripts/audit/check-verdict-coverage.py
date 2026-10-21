#!/usr/bin/env python3
"""verdict 覆盖自检器（R95 轮 · 段 C）

**为什么需要它**：任何「跑一批包再统计」的脚本，都会在某包【没跑全】时把该包
记成「-」（未观测），汇总表再把「- → FAIL」写成「判定变化」⇒ ⇒ 假变化。
R93 轮实测：矩阵 A1 档有 4 个包没有留下 verdict 行，汇总表因此报出 12 条
「判定变化」，真实只有 8 条 ⇒ ⇒ 根因就是缺这道自检。

**用法**：
    check-verdict-coverage.py --scope <scope.tsv> --logs <日志目录> --arch <档名>

**输入格式**：
  scope.tsv ：每行 `package<TAB>^(TestA|TestB|...)$`（期望清单）
  日志      ：`<日志目录>/<档名>__<package 把 / 换成 _>.log`

**判据（逐函数名，不是逐包计数）**：
  包的日志里天然含有 scope 之外的测试函数 ⇒ 「总数相等」会被 out-of-scope
  的额外 verdict 污染 ⇒ ⇒ 必须核对「期望函数名集合 ⊆ 实测函数名集合」。

  missing = 期望集合 − 实测集合
  extra   = 实测集合 − 期望集合（out-of-scope，正常，不算错误，但必须记账）

**状态（三个必须区分，不可压缩成一个「-」）**：
  complete   期望函数全部留下 verdict
  no_verdict 日志存在但期望函数一个都没留下 verdict（跑过但没留下结论）
  absent     日志不存在（没跑这个包）
  partial    期望函数只留下一部分

**退出码**：
  0  全部 complete ⇒ 可以给结论
  2  存在非 complete 的包 ⇒ **拒绝给结论**（missing > 0）
  3  输入无效（scope 为空 / 行格式错 / 期望函数名为空）⇒ 拒绝给结论
"""
import argparse
import json
import os
import re
import sys

# go test 的 verdict 行：`--- PASS: TestXxx (0.01s)`
VERDICT_RE = re.compile(r'^\s*--- (?:PASS|FAIL|SKIP|BENCH):\s+([A-Za-z0-9_]+)', re.M)
# scope 行的期望清单：`^(TestA|TestB)$`
SCOPE_NAME_RE = re.compile(r'\b(Test[A-Za-z0-9_]*)\b')

STATE_COMPLETE = 'complete'
STATE_NO_VERDICT = 'no_verdict'
STATE_ABSENT = 'absent'
STATE_PARTIAL = 'partial'


class ScopeInvalid(Exception):
    """期望清单本身不合法（分母不可信）⇒ 拒绝给结论。"""


def load_scope(path):
    """解析期望清单。任何行格式异常都直接中止（分母不可信就不能给结论）。"""
    rows = []
    with open(path, encoding='utf-8') as fh:
        for lineno, ln in enumerate(fh, 1):
            ln = ln.rstrip('\n')
            if not ln.strip() or ln.lstrip().startswith('#'):
                continue
            pkg, tab, pat = ln.partition('\t')
            pkg = pkg.strip()
            pkg = pkg[2:] if pkg.startswith('./') else pkg
            if not tab or not pat.strip():
                raise ScopeInvalid(f"scope 第 {lineno} 行格式错（应为 package<TAB>正则）: {ln!r}")
            names = [n for n in SCOPE_NAME_RE.findall(pat)]
            if not names:
                raise ScopeInvalid(f"scope 第 {lineno} 行没有解析出任何期望函数名: {ln!r}")
            rows.append((pkg, names))
    # ★ 量具自检：分母不得为 0。
    if not rows:
        raise ScopeInvalid("scope 清单为空 ⇒ 分母为 0，拒绝给结论")
    return rows


def observed_funcs(log_path):
    """从日志里抽实测 verdict 的函数名集合。"""
    try:
        with open(log_path, encoding='utf-8', errors='replace') as fh:
            src = fh.read()
    except OSError:
        return set()
    return set(VERDICT_RE.findall(src))


def check(scope_path, logs_dir, arch):
    scope = load_scope(scope_path)
    rows = []
    for pkg, names in scope:
        log_path = os.path.join(logs_dir, f"{arch}__{pkg.replace('/', '_')}.log")
        if not os.path.isfile(log_path):
            rows.append({
                'package': pkg, 'expected': len(names), 'observed': 0,
                'missing': list(names), 'extra': [], 'state': STATE_ABSENT,
            })
            continue
        got = observed_funcs(log_path)
        missing = [n for n in names if n not in got]
        extra = sorted(got - set(names))
        if len(got) == 0:
            state = STATE_NO_VERDICT
        elif missing:
            state = STATE_PARTIAL
        else:
            state = STATE_COMPLETE
        rows.append({
            'package': pkg, 'expected': len(names),
            'observed': len(set(names) & got),
            'missing': missing, 'extra': extra, 'state': state,
        })
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--scope', required=True)
    ap.add_argument('--logs', required=True)
    ap.add_argument('--arch', required=True)
    ap.add_argument('--json', action='store_true')
    args = ap.parse_args()

    try:
        rows = check(args.scope, args.logs, args.arch)
    except ScopeInvalid as e:
        print(f"!! {e}")
        return 3
    bad = [r for r in rows if r['state'] != STATE_COMPLETE]
    total_expected = sum(r['expected'] for r in rows)
    total_covered = sum(r['expected'] - len(r['missing']) for r in rows)
    total_extra = sum(len(r['extra']) for r in rows)

    if args.json:
        print(json.dumps({
            'arch': args.arch,
            'packages': len(rows),
            'expected_functions': total_expected,
            'covered_functions': total_covered,
            'out_of_scope_verdicts': total_extra,
            'incomplete': bad,
            'ok': not bad,
        }, ensure_ascii=False, indent=2))
    else:
        print(f"档 {args.arch}：{len(rows)} 个包 / 期望函数 {total_expected} / "
              f"被覆盖 {total_covered} / out-of-scope verdict {total_extra}")
        for r in rows:
            if r['state'] != STATE_COMPLETE:
                print(f"  ⚠ {r['package']}: state={r['state']} "
                      f"missing={len(r['missing'])}/{r['expected']} "
                      f"{r['missing'][:6]}")
        if bad:
            print(f"!! 存在 {len(bad)} 个未跑全的包 ⇒ **拒绝给结论**"
                  f"（不得把它们记成「-」再进汇总表）")
        else:
            print("✅ 全部包期望函数均有 verdict ⇒ 可以给结论")

    return 2 if bad else 0


if __name__ == '__main__':
    sys.exit(main())
