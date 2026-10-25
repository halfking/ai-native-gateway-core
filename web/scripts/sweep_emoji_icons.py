#!/usr/bin/env python3
"""sweep_emoji_icons.py —— 把「emoji 当功能图标」换成 Element Plus 真图标。

依据（impeccable craft-floor）：
    「Unicode glyphs or emoji standing in for an icon system. Icons are drawn,
      from a real library or authored SVG, in one consistent stroke and weight.」

本仓的 emoji 不是装饰，是**功能图标**：🔄 是刷新、🗑 是删除、✏ 是编辑、
🔒 是锁定…… 运维在值班台上按形状认按钮，emoji 的形状与描边跟全站其它图标
（Element Plus，1.5px 线性）完全不同 —— 混在一起就是「图标系统不统一」。

替换规则：
  · 模板文本节点里的 emoji → <ElementPlusIcon />
  · 自动补/并入 `import { … } from '@element-plus/icons-vue'`
  · **不动** {{ }} 里的 JS 表达式（那不是文本节点，塞不进组件）
  · **不动** .ts 数据文件里的 emoji（那是数据源，另行处理）

用法：
  python3 scripts/sweep_emoji_icons.py --root web/src
  python3 scripts/sweep_emoji_icons.py --root web/src --dry-run
"""
import argparse
import re
import sys
from pathlib import Path

# emoji（含变体选择符 U+FE0F）→ Element Plus 图标名
# 名字全部对着已安装的 @element-plus/icons-vue 逐个验过（见提交说明）
MAPPING = {
    "🗑": "Delete",
    "✏": "EditPen",
    "📧": "Message",
    "🔑": "Key",
    "🔄": "Refresh",
    "🔁": "RefreshRight",
    "🔗": "Link",
    "📭": "MessageBox",
    "📦": "Box",
    "💡": "InfoFilled",
    "🔍": "Search",
    "🔒": "Lock",
    "🔓": "Unlock",
    "👁": "View",
    "⚙": "Setting",
    "🛠": "Tools",
    "📖": "Reading",
    "⏸": "VideoPause",
    "🕐": "Clock",
    "📌": "Position",
    "📱": "Iphone",
}

# 任何含 U+FE0F 变体选择符的位置
EMOJI_RE = re.compile("(" + "|".join(re.escape(k) for k in MAPPING) + ")️?")
INTERP = re.compile(r"\{\{[\s\S]*?\}\}")


def template_span(src: str) -> tuple[int, int]:
    s = src.find("<template>")
    e = src.find("</template>", s) if s >= 0 else -1
    return (s, e if e > s else len(src))


def is_in_interp(text: str, pos: int) -> bool:
    """位置是否落在 {{ }} 里。"""
    for m in INTERP.finditer(text):
        if m.start() <= pos < m.end():
            return True
    return False


def process(path: Path, rel: str, dry: bool) -> tuple[int, list[str], list[str]]:
    src = path.read_text(encoding="utf-8")
    ts, te = template_span(src)
    if ts < 0:
        return (0, [], [])
    region = src[ts:te]

    used: list[str] = []
    skipped: list[str] = []

    def repl(m: re.Match) -> str:
        if is_in_interp(region, m.start()):
            skipped.append(m.group(0))
            return m.group(0)
        name = MAPPING[m.group(1)]
        if name not in used:
            used.append(name)
        return f"<{name} />"

    new_region = EMOJI_RE.sub(repl, region)
    if not used:
        return (0, [], skipped)

    out = src[:ts] + new_region + src[te:]

    # ── 补 import ──
    if "@element-plus/icons-vue" in out:
        m = re.search(r"import\s*\{([^}]*)\}\s*from\s*'@element-plus/icons-vue'", out)
        if m:
            existing = [x.strip() for x in m.group(1).split(",") if x.strip()]
            merged = existing + [u for u in used if u not in existing]
            out = out[:m.start()] + f"import {{ {', '.join(merged)} }} from '@element-plus/icons-vue'" + out[m.end():]
    else:
        script_open = re.search(r"<script setup[^>]*>\n", out)
        if not script_open:
            return (0, [], skipped)
        insert_at = script_open.end()
        # 插到最后一条 import 之后。
        # ★ 插入点必须落在**行首**：早期版本直接用正则匹配的 end 位置，
        #   而 `$`（re.M）匹配的是「换行之前」，于是新 import 被插到了
        #   上一行的行尾 —— 产出 `from 'element-plus'import { … }` 这种
        #   语法错误（ForbiddenView.vue 就这么炸过一次）。
        # ⇒ 找到位置后再往后推到下一行的行首。
        last_import_end = None
        for m in re.finditer(r"^import\b[^\n]*(?:\n[^\n]*?)*?from\s+'[^']+'", out, re.M):
            last_import_end = m.end()
        if last_import_end and last_import_end > insert_at:
            nl = out.find("\n", last_import_end)
            insert_at = (nl + 1) if nl >= 0 else len(out)
        stmt = f"import {{ {', '.join(used)} }} from '@element-plus/icons-vue'\n"
        out = out[:insert_at] + stmt + out[insert_at:]

    if not dry:
        path.write_text(out, encoding="utf-8")
    return (len(used), used, skipped)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", action="append", required=True)
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    total = 0
    all_skipped = []
    for root in args.root:
        for path in sorted(Path(root).rglob("*.vue")):
            rel = path.as_posix()
            n, used, skipped = process(path, rel, args.dry_run)
            if n:
                total += n
                print(f"  {rel}: {', '.join(used)}")
            for s in skipped:
                all_skipped.append(f"{rel}  {s}")

    print(f"\n合计：{total} 个图标被替换")
    if all_skipped:
        print("\n⚠ 以下 emoji 落在 {{ }} JS 表达式里，需手工处理：")
        for s in all_skipped:
            print("  " + s)
    if args.dry_run:
        print("（dry-run，未落盘）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
