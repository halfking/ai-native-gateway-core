#!/usr/bin/env python3
"""sweep_emoji_icons.py —— 把「emoji 当功能图标」换成 Element Plus 真图标。

依据（impeccable craft-floor）：
    「Unicode glyphs or emoji standing in for an icon system. Icons are drawn,
      from a real library or authored SVG, in one consistent stroke and weight.」

本仓的 emoji 不是装饰，是**功能图标**：🔄 是刷新、🗑 是删除、✏ 是编辑、
🔒 是锁定…… 运维在值班台上按形状认按钮，emoji 的形状与描边跟全站其它图标
（Element Plus，1.5px 线性）完全不同 —— 混在一起就是「图标系统不统一」。

替换分两档（★ 这是本脚本最要紧的判断，写在这里是为了让后来者看得见边界）
══════════════════════════════════════════════════════════════════════
① **真 emoji**（⏳ ✅ ❌ ⭐ ⚡ ⏱ …）—— 无条件替换。
   emoji 本身��就是被禁的东西，出现在哪都换。

② **dingbat 字形**（✕ ▾ ⚠ …）—— 只在**图标位**上替换。
   判据是「所在元素是不是一个图标槽」：
     · class 里含 icon / caret / close（如 .alert-icon / .tp-caret / .btn-close）
     · 或是 <button> 且字形是它的**唯一内容**（关闭按钮）
     · 或是 aria-hidden 且字形是它的唯一内容（纯装饰图标位）
   ★ 为什么不一刀切：`⚠️ 数据聚合视图未就绪（…）` 里的 ⚠ 是**这句话的一部分**，
     与仓库既有的「✓/✗ 状态文字标记不判红」是同一条线 —— 换成图标会
     把一句警告改写成「图标 + 半句话」。

★ 三处必须绕开（早期版本踩过，写下来免得重犯）：
  1. **{{ }} 里的 JS 表达式** —— 塞不进组件，且常常是**数据协议**
     （`triggerMessage.startsWith('❌')` 与 `isError ? '❌ ' : '✓ '` 是一对，
     改一边就断另一边）。
  2. **属性值里的字符串** —— `:class="{ error: msg.startsWith('❌') }"`
     里的 ❌ 在标签内部。第一版只认 `{{ }}`、不认属性 ⇒ 会把属性值替换成
     `<CircleClose />` ⇒ 直接改坏模板。
  3. **脚本区里的字符串** —— 那是数据源，不是渲染出来的字形。

用法：
  python3 scripts/sweep_emoji_icons.py --root web/src
  python3 scripts/sweep_emoji_icons.py --root web/src --dry-run
"""
import argparse
import re
import sys
from pathlib import Path

# ① 真 emoji（含变体选择符 U+FE0F）→ Element Plus 图标名。
#    名字全部对着已安装的 @element-plus/icons-vue 逐个验过。
EMOJI_ICONS = {
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
    "⏳": "Loading",
    "⏱": "Timer",
    "⭐": "StarFilled",
    "⚡": "Lightning",
    "✅": "CircleCheck",
    "❌": "CircleClose",
}

# ② dingbat 字形 → Element Plus 图标名。**只在图标位上替换**（见文件头）。
GLYPH_ICONS = {
    "✕": "Close",
    "▾": "CaretBottom",
    "▴": "CaretTop",
    "▸": "CaretRight",
    "⚠": "Warning",
}

# ★ **没有路径豁免表**。
#   早期版本按 (文件, 字形) 豁免掉「句子的一部分」，结果**同文件里真正的图标位
#   一起被放过**（ChangePasswordDialog 的关闭按钮 ✕ 就这么漏了，
#   而它的 SKIP 本意只是放过另一个位置的 ✕）。
#   ⇒ 豁免改由**图标位判据**本身承担（见 is_icon_slot）：
#     句子里的 ⚠ 自动放行，图标位上的 ✕ 自动判红，两边都不需要路径表。
#   ⇒ 这是本页第二次犯同一个错（侧边条那次是路径白名单放过 .qa-block）。
#      **路径级豁免天然会连坐** —— 判据必须落在「这个位置是什么」上。
MAPPING = {**EMOJI_ICONS, **GLYPH_ICONS}
EMOJI_RE = re.compile("(" + "|".join(re.escape(k) for k in MAPPING) + ")️?")
INTERP = re.compile(r"\{\{[\s\S]*?\}\}")
# 标签区间：`<tag attr="…">`。属性值里出现 '>' 的情况本仓没有。
TAG_RE = re.compile(r"<[A-Za-z][^<>]*>")
# HTML 注释：模板里的注释不是渲染出来的字形。
COMMENT_RE = re.compile(r"<!--[\s\S]*?-->")
CLASS_RE = re.compile(r"""class\s*=\s*(["'])(.*?)\1""", re.S)
ICON_SLOT_CLASS = re.compile(r"(^|[\s_-])(icon|caret|close)([\s_-]|$)")


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


def tag_spans(text: str) -> list[tuple[int, int, str]]:
    """返回 (start, end, tag_text) 列表。"""
    return [(m.start(), m.end(), m.group(0)) for m in TAG_RE.finditer(text)]


def is_in_attr(text: str, pos: int, tags: list[tuple[int, int, str]]) -> bool:
    """位置是否落在标签内部（属性值里）。★ 早期版本漏了这条，会改坏模板。"""
    for start, end, _ in tags:
        if start <= pos < end:
            return True
    return False


def enclosing_tag(tags: list[tuple[int, int, str]], pos: int):
    """包含该位置的最后一个开标签 → (标签名, 属性串, 标签结束位置)。"""
    best = None
    for start, end, text in tags:
        if end <= pos:
            best = (start, end, text)
        else:
            break
    if best is None:
        return None
    m = re.match(r"<([A-Za-z][\w-]*)(.*)$", best[2], re.S)
    if not m:
        return None
    return m.group(1), m.group(2), best[1]


def is_sole_content(region: str, tag_end: int, glyph: str) -> bool:
    """字形是不是所在元素的唯一内容（去掉空白后）。

    ★ tag_end 是**开标签的 '>' 之后**的位置 —— 内容的起点在这里。
      早期版本从字形位置**往后**找 '>'，找回来的是下一个标签的 '>'，
      于是「唯一内容」永远判不成立（aria-hidden 的纯图标位全被放过）。
    """
    nxt = region.find("<", tag_end)
    content = region[tag_end:nxt] if nxt > tag_end else ""
    return content.strip() == glyph


def is_icon_slot(region: str, pos: int, glyph: str, tag, tags) -> bool:
    """dingbat 字形是不是处在「图标位」上。"""
    if tag is None:
        return False
    name, attrs, tag_end = tag
    cm = CLASS_RE.search(attrs)
    cls = cm.group(2) if cm else ""
    if ICON_SLOT_CLASS.search(cls):
        return True
    sole = is_sole_content(region, tag_end, glyph)
    if name == "button" and sole:
        return True
    if "aria-hidden" in attrs and sole:
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
    tags = tag_spans(region)

    def repl(m: re.Match) -> str:
        if is_in_interp(region, m.start()):
            skipped.append(f"{m.group(0)}（在 {{ }} 表达式里）")
            return m.group(0)
        if is_in_attr(region, m.start(), tags):
            skipped.append(f"{m.group(0)}（在属性值里）")
            return m.group(0)
        if any(c.start() <= m.start() < c.end() for c in COMMENT_RE.finditer(region)):
            skipped.append(f"{m.group(0)}（在 HTML 注释里）")
            return m.group(0)
        glyph = m.group(1)
        if glyph in GLYPH_ICONS:
            if not is_icon_slot(region, m.start(), glyph,
                               enclosing_tag(tags, m.start()), tags):
                skipped.append(f"{glyph}（句子的一部分，不是图标位）")
                return m.group(0)
        name = MAPPING[glyph]
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
        print("\n⚠ 以下字形按设计保留 / 需手工处理：")
        for s in all_skipped:
            print("  " + s)
    if args.dry_run:
        print("（dry-run，未落盘）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
