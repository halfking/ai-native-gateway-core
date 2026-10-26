#!/usr/bin/env python3
"""sweep_uppercase.py —— 一次性清扫「中文界面里的 text-transform: uppercase」。

背景（impeccable craft-floor / DESIGN.md §4 Typography Rules 第 5 条）：
中文没有大小写。`text-transform: uppercase` 对中文是**空操作**，只会在界面
里留下「这页是从英文模板翻译过来的」那种味道 —— 这是本仓最扎眼的一处 AI 痕迹。

配套动作：uppercase 惯用法通常配 0.03em~0.08em 的宽字距（`letter-spacing`）。
中文加宽字距会把字撑散（排版硬伤），所以同一条规则里的宽字距一并收到 0.01em。

用法：
  python3 scripts/sweep_uppercase.py --root web/src --root web-mobile/src
  python3 scripts/sweep_uppercase.py --root web/src --dry-run

不做的事：
  · 不碰 `text-transform: lowercase` / `capitalize`；
  · 不改任何模板结构、类名、颜色、间距；
  · 不新增/删除任何 CSS 规则。

allowlist：内容是**拉丁数据值**（不是 UI 文案）的站点保留 uppercase ——
那里的值本来就是 API 返回的英文 token，大写反而更易读。
"""
import argparse
import re
import sys
from pathlib import Path

# 「这处 uppercase 作用在拉丁数据值上」⇒ 保留。
# 判据：徽标/标签里显示的是 API 返回的英文 token（数据），不是 t() 出来的界面文案。
ALLOWLIST = {
    # 附件类型来自 API（image / audio / video ...），是数据不是界面文案
    ("web/src/components/detail/MultimodalAttachmentsPanel.vue", ".kind"),
    # 事件类型来自 API（opened / failure_observed / recovery_progress ...）
    ("web/src/components/RouteIncidentDrawer.vue", ".event-row__type"),
}

# 宽字距阈值：>= 0.03em 一律收到 0.01em（中文不撑字）
WIDE_TRACKING = re.compile(r"letter-spacing:\s*\.?0*\.?0([3-9])\s*em")
UPPER = re.compile(r"[ \t]*text-transform:\s*uppercase;[ \t]*\n?")


def enclosing_rule(text: str, pos: int) -> tuple[int, int]:
    """返回 pos 所在 CSS 规则块的 (start, end) 区间（以 { 与 } 为界）。"""
    open_at = text.rfind("{", 0, pos)
    if open_at < 0:
        return (0, 0)
    depth = 0
    for i in range(open_at, len(text)):
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return (open_at, i)
    return (open_at, len(text))


def selector_of(text: str, rule_start: int) -> str:
    """取规则块的选择器文本（{ 之前的部分）。"""
    head = text[:rule_start]
    return head.split("}")[-1].split("{")[-1].strip()


def process(path: Path, rel: str, dry: bool) -> tuple[int, int]:
    src = path.read_text(encoding="utf-8")
    out = src
    removed = 0
    tightened = 0

    # 从后往前处理，删除不会影响前面位置
    hits = list(UPPER.finditer(out))
    for m in reversed(hits):
        rs, re_ = enclosing_rule(out, m.start())
        sel = selector_of(out, rs)
        if any(a[0] == rel and a[1] in sel for a in ALLOWLIST):
            continue
        out = out[:m.start()] + out[m.end():]
        removed += 1
        # 同一规则内的宽字距收紧
        rs2, re2 = enclosing_rule(out, m.start())
        rule = out[rs2:re2]
        new_rule, n = WIDE_TRACKING.subn("letter-spacing: 0.01em", rule)
        if n:
            tightened += n
            out = out[:rs2] + new_rule + out[re2:]

    if removed and not dry:
        path.write_text(out, encoding="utf-8")
    return removed, tightened


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", action="append", required=True)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    total_removed = 0
    total_tightened = 0
    touched = []
    for root in args.root:
        base = Path(root)
        for path in sorted(base.rglob("*")):
            if path.suffix not in {".vue", ".css"}:
                continue
            rel = path.as_posix()
            removed, tightened = process(path, rel, args.dry_run)
            if removed:
                total_removed += removed
                total_tightened += tightened
                touched.append((rel, removed, tightened))

    for rel, r, t in touched:
        print(f"  {rel}: 去掉 uppercase {r} 处, 收紧字距 {t} 处")
    print(f"\n合计：{len(touched)} 个文件，去掉 uppercase {total_removed} 处，收紧宽字距 {total_tightened} 处")
    if args.dry_run:
        print("（dry-run，未落盘）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
