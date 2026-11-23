#!/usr/bin/env python3
"""
audit-readme-route-stats.py —— `maestro/README.md` 的路由口径清单 ↔ **当前树** 的对拍（L10 复核轮）

## 为什么要有这道工具

★ L10 首版把 README 的路由清单做成「**手工核对的产物**」⇒ **过期是静默的**：
  2026-11-23 复核轮实测：上游 `41fb572f3`（团队层级管理前端切片落地）新增了
  `/admin/teams` 与 `/admin/teams/:id` 两条路由 ⇒ README §1.1 的
  **129 / 134 / 22 / 117 / 54** 五个数**全部过期**，而**没有任何一处会红**
  ⇒ 「清单过期」这件事，只有在有人想起来重跑工具时才会被发现。

★ 同一轮还查出两个更早的静默缺陷：
  ① §1.2「router.ts 行」列是**手抄**的 ⇒ **在任何提交上都对不上**
     （逐提交扫描历史：`/catalog` 只在 `d82a9fb06` 是 220，而同一次扫描里
     catch-all 是 374、不是表里写的 435 ⇒ 两个值来自**不同时期**）；
  ② §1.2 正文写「这 **19** 条」，与标题「**20** 条」自相矛盾。

⇒ 本工具把「README 的数字与树对不上」变成**可判红的判据**。

## 与另一把尺子的分工（★ 别混用）

| 尺子 | 工具 | 运行时依赖 | 能判什么 |
|---|---|---|---|
| **AST（精确计数）** | `maestro/tools/audit-route-coverage.mjs --check` | `web/node_modules`（typescript）⇒ **只在本地跑，不进 CI** | 12 个统计量 + §1.2/§1.3/§3.13 条数 ↔ 树实测（**逐项**） |
| **正则（零依赖）** | 本工具 | **只有 python3** ⇒ **进 CI 载体** | 路由**集合** ↔ 树（逐条）+ README **内部自洽**（标题数 vs 表内行数 vs 算式） |

★ 为什么本工具不重算「权限档」那类数：它们依赖 AST 的 `meta` 归属与
  `beforeEach` 的守卫判据 ⇒ **正则口径算不准** ⇒ 那部分交给 AST 那把尺子。
  ⇒ 本工具只守两件**正则能判准**的事：**集合**与**内部自洽**。

## 判据

| 编号 | 判据 | 抓什么 |
|---|---|---|
| D1 | §1.1 权限档求和 == §1.1「需要登录」 | 算式与被算的数脱节 |
| D2 | §1.1「运行时路由总数」 == 「需要登录」+「不需要登录」 | 同上 |
| D3 | §1.2 标题条数 == §1.2 表内行数 | **只改了表、忘了改标题**（半回填） |
| D4 | §1.3 标题条数 == §1.3 表内行数 | 同上 |
| D5 | §3.13 标题条数 == §3.13 表内路径数（按路径字面量去重） | 同上（★ 该表有「一行多路由」的合并行 ⇒ 不能数行） |
| D6 | README 表格第一格登记的路由 **全部存在于** router.ts | 幽灵路由（树里已删、README 还留着） |
| D7 | router.ts 里的路由 **全部被 README 提及** | ★ **上游新增路由但没人登记**（本轮真实踩到的那类漂移） |

## 用法

  python3 maestro/tools/audit-readme-route-stats.py                # Markdown 报告
  python3 maestro/tools/audit-readme-route-stats.py --json         # 机读 JSON
  python3 maestro/tools/audit-readme-route-stats.py --readme=<path>  # 对照臂（换 README）
  python3 maestro/tools/audit-readme-route-stats.py --router=<path>  # 对照臂（换 router.ts）

退出码：0 = 全部一致；1 = **有漂移**（逐条报出）；2 = 工具自身出错（读不到输入文件等）。
"""

import json
import os
import re
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
README_REL = "maestro/README.md"
ROUTER_REL = "web/src/router.ts"


def fail_tool(msg):
    sys.stderr.write("工具错误：%s\n" % msg)
    return 2


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def router_route_paths(src):
    """router.ts 里的**路由对象**的 path 集合（零依赖正则口径）。

    ★ 四种形态都要抓，否则会漏：
      ① 行首形态      `{ path: '/x', ... }`
      ② 多行对象的属性行 `    path: '/x',`
      ③ dev-only 条件展开 `? [{ path: '/dev/x', ... }]`
      ④ `externalMaintainRedirect('/ops', ...)` 造的 5 条 `/ops/*`
    ⇒ ★ 实测：本口径与 AST 口径（`audit-route-coverage.mjs`）**逐条相等**（136 = 136）。
    """
    paths = set()
    paths |= set(re.findall(r"^\s*\{ path: '([^']+)'", src, re.M))
    paths |= set(re.findall(r"^\s*path: '([^']+)',", src, re.M))
    paths |= set(re.findall(r"\?\s*\[\{ path: '([^']+)'", src))
    paths |= set(re.findall(r"externalMaintainRedirect\(\s*'([^']+)'", src))
    return paths


def section(text, heading_re):
    """取某节正文（不含标题行本身）：从标题行末尾到下一个同级标题行。"""
    m = re.search(heading_re, text)
    if not m:
        return None
    rest = text[m.end():]
    nxt = re.search(r"\n### |\n## ", rest)
    return rest if not nxt else rest[: nxt.start()]


def table_rows(sec):
    """markdown 表格的行数（去掉表头行与分隔行）。"""
    if sec is None:
        return None
    rows = [l for l in sec.split("\n") if l.strip().startswith("|")]
    return max(0, len(rows) - 2)


def first_cell_paths(sec):
    """表格**第一格**里的路径字面量（去重）；★ 只认第一格 —— 正文里的
    `/admin/teams/${teamId}` 之类是**模板**不是路由，混进来会误报幽灵。"""
    out = set()
    if sec is None:
        return out
    for line in sec.split("\n"):
        t = line.strip()
        if not t.startswith("|"):
            continue
        idx = t.find("|", 1)
        cell = t[1:idx] if idx > 0 else ""
        for m in re.finditer(r"`(/[^`]*?)`", cell):
            p = m.group(1)
            if "${" in p:
                continue
            out.add(p)
    return out


def all_mentioned_paths(readme):
    """README 全文（含正文）提到过的路径字面量。"""
    return set(re.findall(r"`(/[^`\s]*)`", readme))


def num(text, pattern):
    if not text:
        return None
    m = re.search(pattern, text)
    return int(m.group(1)) if m else None


def run():
    readme_path = os.path.join(REPO_ROOT, README_REL)
    router_path = os.path.join(REPO_ROOT, ROUTER_REL)
    for arg in sys.argv[1:]:
        if arg.startswith("--readme="):
            readme_path = arg[len("--readme="):]
            if not os.path.isabs(readme_path):
                readme_path = os.path.join(REPO_ROOT, readme_path)
        if arg.startswith("--router="):
            router_path = arg[len("--router="):]
            if not os.path.isabs(router_path):
                router_path = os.path.join(REPO_ROOT, router_path)
    for p in (readme_path, router_path):
        if not os.path.isfile(p):
            return fail_tool("读不到输入文件 %s" % p)

    readme = read(readme_path)
    router = read(router_path)

    s11 = section(readme, r"### 1\.1 实测统计")
    s12 = section(readme, r"### 1\.2 纯 redirect 路由")
    s13 = section(readme, r"### 1\.3 带命名动态段的路由")
    s313 = section(readme, r"### 3\.13 已知缺口")

    d = {
        "pathEntries": num(s11, r"\| `path:` 条目 \| \*\*(\d+)\*\* \|"),
        "runtimeTotal": num(s11, r"\| \*\*运行时路由总数\*\* \| \*\*(\d+)\*\* \|"),
        "publicCount": num(s11, r"\| \*\*不需要登录\*\* \| \*\*(\d+)\*\* \|"),
        "authCount": num(s11, r"\| \*\*需要登录\*\* \| \*\*(\d+)\*\* \|"),
        "anyUser": num(s11, r"\| 任意登录用户（[^|]*） \| \*\*(\d+)\*\* \|"),
        "super": num(s11, r"\| `requiresSuper`（[^|]*） \| \*\*(\d+)\*\* \|"),
        "platformOps": num(s11, r"\| `requiresPlatformOps`（[^|]*） \| \*\*(\d+)\*\* \|"),
        "providerConsole": num(s11, r"\| `requiresProviderConsole`（[^|]*） \| \*\*(\d+)\*\* \|"),
        "s12Heading": num(readme, r"### 1\.2 纯 redirect 路由（(\d+) 条）"),
        "s12Rows": table_rows(s12),
        "s13Heading": num(readme, r"### 1\.3 带命名动态段的路由（(\d+) 条）"),
        "s13Rows": table_rows(s13),
        "s313Heading": num(readme, r"### 3\.13 已知缺口：\*\*未被任何 flow 的 `openLink` 直接打开\*\*的 (\d+) 条路由"),
    }
    gap_paths = first_cell_paths(s313)
    d["s313PathCount"] = len(gap_paths)

    router_paths = router_route_paths(router)
    readme_table_paths = first_cell_paths(readme)   # ★ 全文所有表格的第一格
    readme_mentioned = all_mentioned_paths(readme)

    checks = []

    def add(name, ok, detail):
        checks.append({"name": name, "pass": bool(ok), "detail": detail})

    # D1 / D2 —— README 内部算式
    tiers_sum = None
    if None not in (d["anyUser"], d["super"], d["platformOps"], d["providerConsole"]):
        tiers_sum = d["anyUser"] + d["super"] + d["platformOps"] + d["providerConsole"]
        add("D1 §1.1 权限档求和 == 需要登录",
            tiers_sum == d["authCount"],
            "权限档求和 %s vs 需要登录 %s" % (tiers_sum, d["authCount"]))
    else:
        add("D1 §1.1 权限档求和 == 需要登录", False, "读不到权限档的数（README 结构变了？）")

    if None not in (d["runtimeTotal"], d["authCount"], d["publicCount"]):
        add("D2 §1.1 运行时总数 == 需要登录 + 不需要登录",
            d["runtimeTotal"] == d["authCount"] + d["publicCount"],
            "%s vs %s + %s" % (d["runtimeTotal"], d["authCount"], d["publicCount"]))
    else:
        add("D2 §1.1 运行时总数 == 需要登录 + 不需要登录", False, "读不到 §1.1 的数（README 结构变了？）")

    # D3 / D4 / D5 —— 标题条数 vs 表内行数/路径数（抓「半回填」）
    add("D3 §1.2 标题条数 == 表内行数",
        d["s12Heading"] is not None and d["s12Heading"] == d["s12Rows"],
        "标题 %s vs 表内 %s 行" % (d["s12Heading"], d["s12Rows"]))
    add("D4 §1.3 标题条数 == 表内行数",
        d["s13Heading"] is not None and d["s13Heading"] == d["s13Rows"],
        "标题 %s vs 表内 %s 行" % (d["s13Heading"], d["s13Rows"]))
    add("D5 §3.13 标题条数 == 表内路径数",
        d["s313Heading"] is not None and d["s313Heading"] == d["s313PathCount"],
        "标题 %s vs 表内 %s 条路径" % (d["s313Heading"], d["s313PathCount"]))

    # D6 —— 幽灵路由（README 登记了、树里已没有）
    ghosts = sorted(p for p in readme_table_paths if p not in router_paths)
    add("D6 README 登记的路由都存在于 router.ts（无幽灵路由）",
        not ghosts,
        ("幽灵路由：" + "、".join(ghosts)) if ghosts else "%d 条全部存在" % len(readme_table_paths))

    # D7 —— 未登记路由（树里新增了、README 没提）
    unlisted = sorted(p for p in router_paths if p not in readme_mentioned)
    add("D7 router.ts 的路由都被 README 提及（无未登记路由）",
        not unlisted,
        ("未登记：" + "、".join(unlisted)) if unlisted else "%d 条全部登记" % len(router_paths))

    failed = [c for c in checks if not c["pass"]]
    out = {
        "readme": os.path.relpath(readme_path, REPO_ROOT).replace(os.sep, "/"),
        "router": os.path.relpath(router_path, REPO_ROOT).replace(os.sep, "/"),
        "declared": d,
        "routerPathCount": len(router_paths),
        "readmeTablePathCount": len(readme_table_paths),
        "ghosts": ghosts,
        "unlisted": unlisted,
        "checks": checks,
        "pass": not failed,
    }
    if "--json" in sys.argv:
        print(json.dumps(out, ensure_ascii=False, indent=2))
    else:
        print("# maestro/README 路由口径 ↔ 当前树 对拍（L10 复核轮）")
        print("README：%s；router.ts：%s\n" % (out["readme"], out["router"]))
        for c in checks:
            print("%s %s  — %s" % ("✅" if c["pass"] else "❌", c["name"], c["detail"]))
        print("\nRESULT: %s（%d / %d 条一致）" % (
            "PASS" if not failed else "FAIL",
            len(checks) - len(failed), len(checks)))
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(run())
