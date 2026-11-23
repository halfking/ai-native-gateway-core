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

## ★ 2026-11-24（L34 九轮）：锚点索引抽成共享库

★ 原先本工具自己扫前端建索引（**只认静态 `data-testid`**）⇒ 而同一条规定
（「flow 的 `id:` 在前端从哪来」）现在有**两个消费者**：本工具（遮蔽）
与新工具 `audit-flow-anchor-existence.py`（存在性）⇒ ⇒ 写两份必然漂移。
⇒ 索引规则抽到 `maestro/tools/flow_anchors.py`（**同一条规定只写一份**），
  两个工具共用。

★ 顺带订正本工具 `unknown` 的**口径**：原先「前端没有这个 data-testid」
  就算 unknown ⇒ ★ **把 HTML `id=` 与动态模板串锚点误判成 unknown**
  （实测 17 个「unknown」里 **10 个是假阳性**：3 个 HTML `id=`、
  7 个动态档位锚点）⇒ ★ 换成共享库后口径才准（现在 **0 个**）。
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import flow_anchors  # noqa: E402  (path must be set first)

REPO_ROOT_DEFAULT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


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


def shadow_reason(tag):
    reasons = []
    if re.search(r"(?<![\w-])(:)?title\s*=", tag):
        reasons.append("title")
    if re.search(r"(?<![\w-])(:)?aria-label\s*=", tag):
        reasons.append("aria-label")
    return reasons


def run():
    root = repo_root()
    index = flow_anchors.build_anchor_index(root)
    selectors, _skipped_native = flow_anchors.collect_flow_selectors(root)

    shadowed = []
    unknown = []
    unshadowable = 0
    for flow, tid in selectors:
        entry = index["data_testid"].get(tid)
        if entry is None:
            # ★ 2026-11-24：不是「没有这个锚点」，而是「这个锚点不由静态
            #   data-testid 提供」⇒ 须再问一次「它到底存不存在」：
            #   · 存在（HTML id / 绑定表达式 / 动态模板）⇒ 只是**没法做静态遮蔽判定**
            #     ⇒ 不算 unknown（原口径把它算成 unknown ⇒ 17 个里 10 个是假阳性）；
            #   · 不存在 ⇒ 真幽灵 ⇒ 由 `audit-flow-anchor-existence.py` 负责判红。
            ok, _source = flow_anchors.resolve_anchor(tid, index)
            if ok:
                unshadowable += 1
            else:
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
            "scannedTestIds": len(index["data_testid"]),
            "shadowed": shadowed,
            "unknown": unknown,
            "notStaticTestId": unshadowable,
            "pass": not shadowed,
        }, ensure_ascii=False, indent=2))
    else:
        print("# Maestro flow 的 `id:` 选择器遮蔽审计（L13 补轮）")
        print("扫描：flow 选择器 %d 个 / 前端 data-testid %d 个\n" % (len(selectors), len(index["data_testid"])))
        if shadowed:
            print("❌ 被 title/aria-label 遮蔽（⇒ `id:` 必然找不到 ⇒ **恒假红**）：")
            for s in shadowed:
                print("  · %s  →  id: %s" % (s["flow"], s["id"]))
                print("      元素在 %s，遮蔽属性：%s" % (s["file"], "+".join(s["shadowedBy"])))
        else:
            print("✅ 没有被遮蔽的 id 选择器")
        if unknown:
            print("\n（前端树里**根本不存在**的 id：%d 个 —— 由 "
                  "`maestro/tools/audit-flow-anchor-existence.py`（门："
                  "`scripts/checks/maestro-flow-anchor-gate.sh`）负责判红）"
                  % len(unknown))
        if unshadowable:
            print("（另有 %d 个锚点不是静态 data-testid（HTML id / 动态模板）⇒ "
                  "静态遮蔽判定不适用，但它们**存在**）" % unshadowable)
        print("\nRESULT: %s" % ("PASS" if not shadowed else "FAIL（%d 处恒假红隐患）" % len(shadowed)))
    return 0 if not shadowed else 1


if __name__ == "__main__":
    sys.exit(run())
