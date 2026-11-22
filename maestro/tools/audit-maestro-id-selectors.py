#!/usr/bin/env python3
"""
audit-maestro-id-selectors.py —— 扫全仓 Maestro flow 的 `id:` 选择器是否被 `title`/`aria-label` 遮蔽

## 为什么要有这道工具（L13 补轮的根因固化）

2026-11-23 的 L13 收口轮实测钉死了 Maestro web 驱动的一条**选择器语义**：

> ★ **Maestro 的 `id:` 选择器匹配的是 hierarchy 的 `resource-id`；**
> ★ **而 web 驱动取 `resource-id` 时，`aria-label` / `title` 会盖掉 `data-testid`。**

实测证据（同一台机器、同一次会话）：

| 元素 | DOM 上的属性 | Maestro hierarchy 里的 `resource-id` | `id:` 能否命中 |
|---|---|---|---|
| `sa-tab-models`（会话分析 tab） | `<button data-testid="sa-tab-models">`（**无** title/aria-label） | `sa-tab-models` | ✅ **能**（真机 COMPLETED） |
| `stage-stats-open`（live2 统计胶囊） | `<button data-testid="stage-stats-open" :title="…">` | **「展开统计（全页）」**（= title 的值） | ❌ **不能**（Element not found） |
| `work-mode-switch`（顶栏模式切换） | `<button data-testid="work-mode-switch" :title="…" :aria-label="…">` | **「切到简易模式」**（= aria-label 的值） | ❌ **不能** |

⇒ ★★★ **所以「`<button>` 选不到」这个结论是错的** ——
  ★ 判别因素**不是元素类型**，而是「**有没有 `title`/`aria-label` 盖住 `data-testid`**」。
  ⇒ 本仓 2026-11-17 记在 `maestro/README.md` §6.6 的那条结论（「`<button>` 命中不了」）
  **被本轮推翻**，因为它观察到的两个反例恰好都带了 title/aria-label。

⇒ ★★ **为什么必须做成门**：一个 `id:` 被遮蔽 ⇒ 该断言**在真机上必然永远找不到**
  ⇒ **恒假红** ⇒ 而它是**静默**的（流程里表现为「找不到元素」，很容易被误判成「页面坏了」）
  ⇒ ⇒ 必须让它在**提交时**就红，而不是在真机 E2E 时才发现。

## 用法

  python3 maestro/tools/audit-maestro-id-selectors.py            # Markdown 报告
  python3 maestro/tools/audit-maestro-id-selectors.py --json     # 机读 JSON
  python3 maestro/tools/audit-maestro-id-selectors.py --root=<dir>  # 扫描另一棵根（门做对照臂用）

退出码：0 = 没有被遮蔽的选择器；1 = **有被遮蔽的选择器**（恒假红隐患）；2 = 工具自身出错。

## ★ 口径（读数请按这个口径理解）

* **扫描面**：① `maestro/flows/**/*.yaml` 里所有 `id: "xxx"` 选择器；
  ② `web/src/**/*.vue` 里所有带 `data-testid="xxx"` 的**元素**。
* **「被遮蔽」的判定**：该 `data-testid` 所在的**同一个开始标签**上出现了
  `title=` / `:title=` / `aria-label=` / `:aria-label=`。
  ★ 这是**静态近似**：用正则取开始标签 ⇒ ★ 标签跨行、属性顺序、动态绑定都可能影响解析精度
  ⇒ ⇒ **本工具只抓「明显带 title/aria-label」的元素**；它是**下限**不是上限。
* ★ 本工具**不改任何 flow** ⇒ 只报事实 ⇒ 修法由该 flow 的归属方裁决。

## ★ 与 `maestro/tools/audit-route-coverage.mjs` 的分工

* 本工具：**flow 的 `id:` 选择器 ↔ 前端 `data-testid` 元素**的**选择器语义**对拍。
* `audit-route-coverage.mjs`：**路由 ↔ flow 的 `openLink:` ↔ README §3** 的**覆盖**对拍。
"""

import glob
import json
import os
import re
import sys

REPO_ROOT_DEFAULT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
FLOW_GLOB_REL = "maestro/flows/**/*.yaml"
VUE_GLOB_REL = "web/src/**/*.vue"


def repo_root():
    """★ `--root=` 可指定扫描根 ⇒ 门可以用它做**对照臂**（在临时目录里造最小仓）。"""
    for arg in sys.argv[1:]:
        if arg.startswith("--root="):
            return os.path.abspath(arg[len("--root="):])
    return REPO_ROOT_DEFAULT


def fail_tool(msg):
    sys.stderr.write("工具错误：%s\n" % msg)
    return 2


def strip_comment_lines(text):
    """去掉整行注释（行首空白 + #）⇒ 注释里提到的 id: 不算选择器。"""
    return "\n".join(l for l in text.split("\n") if not re.match(r"^\s*#", l))


def build_testid_index(root):
    """data-testid → (文件, 开始标签原文)。"""
    index = {}
    for path in glob.glob(os.path.join(root, VUE_GLOB_REL), recursive=True):
        try:
            with open(path, encoding="utf-8") as fh:
                src = fh.read()
        except OSError:
            continue
        # 开始标签：<tag ... >（允许跨行，不含 > 前出现 '<' 的情况）
        for m in re.finditer(r"<[A-Za-z][^<>]*?>", src, re.S):
            tag = m.group(0)
            for tid in re.findall(r'data-testid\s*=\s*"([^"]+)"', tag):
                index.setdefault(tid, (os.path.relpath(path, root).replace(os.sep, "/"), tag))
    return index


def collect_flow_selectors(root):
    """[(flow 文件相对路径, id)]，注释已剥离。"""
    out = []
    for path in sorted(glob.glob(os.path.join(root, FLOW_GLOB_REL), recursive=True)):
        rel = os.path.relpath(path, root).replace(os.sep, "/")
        if os.path.basename(rel).startswith("_tmp-"):
            continue  # 临时探针不入账
        with open(path, encoding="utf-8") as fh:
            code = strip_comment_lines(fh.read())
        for tid in sorted(set(re.findall(r'\bid:\s*"?([A-Za-z0-9_-]+)"?', code))):
            out.append((rel, tid))
    return out


def shadow_reason(tag):
    reasons = []
    if re.search(r"(?<![\w-])(:)?title\s*=", tag):
        reasons.append("title")
    if re.search(r"(?<![\w-])(:)?aria-label\s*=", tag):
        reasons.append("aria-label")
    return reasons


def run():
    root = repo_root()
    index = build_testid_index(root)
    selectors = collect_flow_selectors(root)

    shadowed = []
    unknown = []
    for flow, tid in selectors:
        entry = index.get(tid)
        if entry is None:
            unknown.append({"flow": flow, "id": tid})
            continue
        file, tag = entry
        reasons = shadow_reason(tag)
        if reasons:
            shadowed.append({
                "flow": flow, "id": tid, "file": file,
                "shadowedBy": reasons,
                "reason": "Maestro 的 resource-id 会被 %s 盖掉 data-testid ⇒ `id:` 必然找不到 ⇒ 恒假红"
                          % "+".join(reasons),
            })

    if "--json" in sys.argv:
        print(json.dumps({
            "scannedSelectors": len(selectors),
            "scannedTestIds": len(index),
            "shadowed": shadowed,
            "unknown": unknown,
            "pass": not shadowed,
        }, ensure_ascii=False, indent=2))
    else:
        print("# Maestro flow 的 `id:` 选择器遮蔽审计（L13 补轮）")
        print("扫描：flow 选择器 %d 个 / 前端 data-testid %d 个\n" % (len(selectors), len(index)))
        if shadowed:
            print("❌ 被 title/aria-label 遮蔽（⇒ `id:` 必然找不到 ⇒ **恒假红**）：")
            for s in shadowed:
                print("  · %s  →  id: %s" % (s["flow"], s["id"]))
                print("      元素在 %s，遮蔽属性：%s" % (s["file"], "+".join(s["shadowedBy"])))
        else:
            print("✅ 没有被遮蔽的 id 选择器")
        if unknown:
            print("\n（未在前端源码里找到对应 data-testid 的 id：%d 个 —— 另有「flow↔testid」门负责）"
                  % len(unknown))
        print("\nRESULT: %s" % ("PASS" if not shadowed else "FAIL（%d 处恒假红隐患）" % len(shadowed)))
    return 0 if not shadowed else 1


if __name__ == "__main__":
    sys.exit(run())
