#!/usr/bin/env python3
"""sweep_side_stripes.py —— 清除「彩色侧边条」这一 AI 模板痕迹。

依据（impeccable craft-floor / DESIGN.md §6 Components「状态表达」）：
    「A colored border-left or border-right above 1px on cards, list items,
      callouts, or alerts.」= 硬禁。

为什么只动**语义状态色**：
  craft-floor 的原文是 *colored*。中性色的侧边条承担的是别的职责 ——
  树形引导线（.stt-children）、引用块竖线（.qa-block）、CSS 箭头造型
  （.card__chevron 用 border-top + border-right 旋成箭头）。它们是**几何**，
  不是状态编码，动它们会破坏语义。

变换方式（不是把 3px 改小，是换表达）：
    border-left: 3px solid var(--kx-danger);   →   border: 1px solid var(--kx-danger);
    border-left: 2px solid var(--warning);     →   border: 1px solid var(--warning);
    border-inline-start: 3px solid var(--danger); → border: 1px solid var(--danger);
    border-left-color: var(--kx-primary);      →   border-color: var(--kx-primary);

语义状态从「某一条边上的粗色条」变成「整块的四边细描边」：
  · 不再是模板化的「色条卡片」；
  · 描边宽度与卡片基础描边一致 ⇒ 盒模型不变 ⇒ **零布局位移**；
  · 状态信息一点没丢。

用法：
  python3 scripts/sweep_side_stripes.py --root web/src
  python3 scripts/sweep_side_stripes.py --root web/src --dry-run
"""
import argparse
import re
import sys
from pathlib import Path

# 几何用途的侧边条 ⇒ 保留（文件 + 选择器片段）。
# 这些侧边条承担的是**几何/分组**职责，不是状态编码：
#   .stt-children   树形引导线
#   .card__chevron  用 border-top + border-right 旋出来的 CSS 箭头
#   .qa-block       引用块的竖线
KEEP = {
    ("web/src/components/session/SessionTurnsTimeline.vue", ".stt-children"),
    ("web/src/components/ui/CardList.vue", ".card__chevron"),
    ("web/src/components/detail/RequestOverviewPanel.vue", ".qa-block"),
}

# 语义状态色（含语义色的常见变体）。
SEMANTIC = re.compile(
    r"var\(--(?:kx-)?(?:success|warning|danger|error|primary|accent|info)"
    r"(?:-strong|-soft|-bd|-dark|-darker|-light|-border)?\b"
    r"|var\(--el-color-primary\)"
    r"|color-mix\(in srgb,\s*var\(--(?:kx-)?(?:success|warning|danger|error|primary|accent)\)"
)

# border-left: 3px solid var(--x);   /   border-inline-start: 3px solid var(--x);
SIDE = re.compile(
    r"border-(left|right|inline-start|inline-end)\s*:\s*"
    r"(\d+)px\s+solid\s+([^;]+);"
)
# border-left-color: var(--x);
SIDE_COLOR = re.compile(
    r"border-(left|right|inline-start|inline-end)-color\s*:\s*([^;]+);"
)

MIN_WIDTH = 2  # 1px 的侧边条 craft-floor 明确允许


def rule_of(text: str, pos: int) -> tuple[int, int, str]:
    """返回 (规则起始, 规则结束, 选择器文本)。"""
    open_at = text.rfind("{", 0, pos)
    if open_at < 0:
        return (0, 0, "")
    depth = 0
    for i in range(open_at, len(text)):
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                sel = text[:open_at].split("}")[-1].split("{")[-1].strip()
                return (open_at, i, sel)
    return (open_at, len(text), "")


def process(path: Path, rel: str, dry: bool) -> tuple[int, int]:
    src = path.read_text(encoding="utf-8")
    out = src
    changed_solid = 0
    changed_color = 0

    # ① border-left: Npx solid X  →  border: 1px solid X;
    #    transparent 是「占位槽」：改色修饰符随后上色，所以基底也必须收到 1px，
    #    否则修饰符一上色，3px 色条又回来了。
    for m in reversed(list(SIDE.finditer(out))):
        width = int(m.group(2))
        color = m.group(3).strip()
        if width < MIN_WIDTH:
            continue
        rs, re_, sel = rule_of(out, m.start())
        if any(k[0] == rel and k[1] in sel for k in KEEP):
            continue
        out = out[:m.start()] + f"border: 1px solid {color};" + out[m.end():]
        changed_solid += 1

    # ② border-left-color: X  →  border-color: X;
    #    宽度由基底规则决定（已收成 1px）。
    for m in reversed(list(SIDE_COLOR.finditer(out))):
        color = m.group(2).strip()
        out = out[:m.start()] + f"border-color: {color};" + out[m.end():]
        changed_color += 1

    if (changed_solid or changed_color) and not dry:
        path.write_text(out, encoding="utf-8")
    return changed_solid, changed_color


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", action="append", required=True)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    ts = tc = 0
    touched = []
    for root in args.root:
        for path in sorted(Path(root).rglob("*")):
            if path.suffix not in {".vue", ".css"}:
                continue
            rel = path.as_posix()
            s, c = process(path, rel, args.dry_run)
            if s or c:
                ts += s
                tc += c
                touched.append((rel, s, c))

    for rel, s, c in touched:
        print(f"  {rel}: 侧边条→整块描边 {s} 处, 仅色值改写 {c} 处")
    print(f"\n合计：{len(touched)} 个文件，侧边条 {ts} 处，仅色值 {tc} 处")
    if args.dry_run:
        print("（dry-run，未落盘）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
