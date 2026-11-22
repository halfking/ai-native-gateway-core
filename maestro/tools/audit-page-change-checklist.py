#!/usr/bin/env python3
"""
audit-page-change-checklist.py —— PR 模板「页面改动 Checklist」与**当前树**的对拍（L12 补轮）

## 为什么要有这道工具

L12 的原始诉求是「会话分析 E2E 没接入 CI ⇒ 改了页面但没跑 E2E 不会被拦住」。
上游 2026-11-20 的收口（`035b0c4b7`）走了 **PR checklist** 方案：在
`.github/pull_request_template.md` 里列出「改了这些页面就必须复跑会话分析 E2E」。

★ 但那份清单是**手写的**，而且**已经漂移**（2026-11-23 实测，两处）：

  ① **幽灵条目**：模板列了 `SessionAnalyticsOverviewView.vue` ——
     上游 2026-11-23 审计轮把「会话分析总览页」**取消了**
     （路由改为 redirect 到 `/admin/session-analytics/users`）
     ⇒ 该文件**在仓内已不存在** ⇒ 模板指向一个空气。
  ② **漏项**：上游把总览页的内容**重组迁入**了 `SessionAnalyticsUsersView.vue`
     —— ★ **这个新页面没有出现在模板里** ⇒ ★ **改了最需要复跑 E2E 的那个页面，
     checklist 反而不会要求复跑** ⇒ ★ **L12 的保护恰好在最需要的地方失效**。

⇒ 本工具把那份清单从「手写的字」变成「**与当前树对拍出来的**」：
  真实清单 = `web/src/views/admin/session-analytics/**` 下的全部 `.vue`
  三条判据：
    A **幽灵文件**：模板列的 `.vue` 在仓内**不存在** ⇒ 红
    B **漏项文件**：仓内的会话分析 `.vue` **既没被逐个列出、其目录也没被整体列出** ⇒ 红
    C **幽灵路径**：模板列的 `/admin/session-analytics*` 路径在 router.ts 里**不存在** ⇒ 红

★ **同类漂移的通用式**（与 L10 同款教训）：**「手工抄的清单」和「可复跑的工具」
  是两种资产** —— 前者会在第一次上游重构后过期，而**过期是静默的**
  （没人会注意到清单里那个文件已经不存在了）⇒ ⇒ **清单必须由工具对拍出来**。

## ★ 为什么是 python3 而不是 node（**这一条是接线能不能成立的前提**）

本工具要挂在**真正在跑的 CI 载体**上（`.workflow/main-verify.yml`）⇒ ★ **必须零依赖**：

  · AST 方案需要 `typescript`，而它装在 `web/node_modules` ⇒ CI 精简镜像里**没有**
    ⇒ ⇒ ★★ **「工具不可运行 ⇒ 它的产出不可复现 ⇒ 门会被判成恒绿或误红」**
    —— 这正是 L10 轮写进报告的那条教训。
  · ★ **node 本身也不可靠**：仓内既有的 CI 门 `scripts/checks/install-entrypoints-test.sh`
    对 node 的处理是「**有就跑、没有就 skip**」（实测 `command -v node` 分支）
    ⇒ ⇒ **node 缺失是常态** ⇒ ⇒ ★ **门若以 node 为前提 ⇒ CI 上要么恒红、要么被 skip**
    ⇒ ⇒ ★★ **而 python3 在载体里是确定存在的**（`.workflow/main-verify.yml` 现有 step
    就在跑 `python3 -c`）⇒ ⇒ **本工具用 python3、零第三方依赖**。

⇒ 两把尺子的分工（写清楚免得下一个人混用）：
  · **精确路由计数 / redirect 形态判别** ⇒ `maestro/tools/audit-route-coverage.mjs`
    （★ AST 口径；需要 `web/node_modules` ⇒ **只在本地跑，不进 CI 载体**）
  · **CI 上跑的必须是零依赖的那把** ⇒ 本工具（python3）

## 用法

  python3 maestro/tools/audit-page-change-checklist.py            # Markdown 报告
  python3 maestro/tools/audit-page-change-checklist.py --json     # 机读 JSON
  python3 maestro/tools/audit-page-change-checklist.py --template=<path>   # 对照臂

退出码：0 = 对拍通过（模板清单与当前树一致）；1 = **清单漂移**（判据 A/B/C 命中）；
        2 = 工具自身出错（读不到输入文件等）。
"""

import json
import os
import re
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SA_DIR = "web/src/views/admin/session-analytics"
TEMPLATE_REL = ".github/pull_request_template.md"
ROUTER_REL = "web/src/router.ts"


def fail_tool(msg):
    sys.stderr.write("工具错误：%s\n" % msg)
    return 2


def list_domain_vue_files():
    """仓内会话分析域的全部 .vue（相对仓库根，正斜杠）。"""
    root = os.path.join(REPO_ROOT, SA_DIR)
    if not os.path.isdir(root):
        return []
    out = []
    for dirpath, _dirnames, filenames in os.walk(root):
        for name in filenames:
            if name.endswith(".vue"):
                full = os.path.join(dirpath, name)
                rel = os.path.relpath(full, REPO_ROOT).replace(os.sep, "/")
                out.append(rel)
    return sorted(out)


def extract_template_section(md):
    """抽出模板里「会话分析系统」那一节（到下一个二/三级标题为止）。"""
    start = md.find("### 会话分析系统")
    if start < 0:
        return ""
    rest = md[start + 3:]
    m = re.search(r"\n#{2,3} ", rest)
    return rest[: m.start()] if m else rest


def parse_template(md):
    section = extract_template_section(md)
    vue_names = sorted(set(re.findall(r"SessionAnalytics[A-Za-z0-9]*\.vue", section)))
    dir_paths = sorted(set(re.findall(
        r"web/src/views/admin/session-analytics/[A-Za-z0-9_\-/]*/?", section)))
    route_paths = sorted(set(
        p.strip("`") for p in re.findall(r"`/admin/session-analytics[^`]*`", section)))
    return {"section": section, "vueNames": vue_names,
            "dirPaths": dir_paths, "routePaths": route_paths}


def list_router_paths():
    """router.ts 里出现的所有 `path: '<字面量>'`。

    ★ 口径边界（**必须写清楚**）：这是正则口径 ⇒ **会多算** redirect 对象内嵌的
      `path:` ⇒ ⇒ **判据 C 只可能偏松、不会假红**。
      需要精确路由计数请用 maestro/tools/audit-route-coverage.mjs（AST 口径）。
    """
    path = os.path.join(REPO_ROOT, ROUTER_REL)
    with open(path, encoding="utf-8") as fh:
        src = fh.read()
    return set(m.group(2) for m in re.finditer(r"\bpath\s*:\s*(['\"`])([^'\"`]+)\1", src))


def run():
    template_path = REPO_ROOT + "/" + TEMPLATE_REL
    for arg in sys.argv[1:]:
        if arg.startswith("--template="):
            template_path = arg[len("--template="):]
            if not os.path.isabs(template_path):
                template_path = os.path.join(REPO_ROOT, template_path)
    if not os.path.isfile(template_path):
        return fail_tool("读不到模板 %s" % template_path)

    with open(template_path, encoding="utf-8") as fh:
        md = fh.read()
    tpl = parse_template(md)
    real_files = list_domain_vue_files()
    router_paths = list_router_paths()

    # A 幽灵文件
    ghosts = [n for n in tpl["vueNames"]
              if not any(f.endswith("/" + n) for f in real_files)]
    # B 漏项文件
    missing = []
    for f in real_files:
        base = f.rsplit("/", 1)[-1]
        if base in tpl["vueNames"]:
            continue
        dir_with_slash = f.rsplit("/", 1)[0] + "/"
        if any(dir_with_slash.endswith(d) for d in tpl["dirPaths"]):
            continue
        missing.append(f)
    # C 幽灵路径
    bad_paths = [p for p in tpl["routePaths"] if p not in router_paths]

    checks = [
        {"name": "判据 A：模板列出的每个 .vue 在仓内都存在（无幽灵条目）",
         "pass": not ghosts,
         "detail": ("幽灵：" + "、".join(ghosts)) if ghosts
                   else ("%d 个全部存在" % len(tpl["vueNames"]))},
        {"name": "判据 B：仓内每个会话分析 .vue 都被模板覆盖（逐个列出或所在目录被整体列出）",
         "pass": not missing,
         "detail": ("漏项：" + "、".join(missing)) if missing
                   else ("%d 个全部覆盖" % len(real_files))},
        {"name": "判据 C：模板列出的每个会话分析路由在 router.ts 里都存在",
         "pass": not bad_paths,
         "detail": ("幽灵路径：" + "、".join(bad_paths)) if bad_paths
                   else ("%d 条全部存在" % len(tpl["routePaths"]))},
    ]
    failed = [c for c in checks if not c["pass"]]

    if "--json" in sys.argv:
        print(json.dumps({
            "template": os.path.relpath(template_path, REPO_ROOT).replace(os.sep, "/"),
            "realFiles": real_files,
            "templateVueNames": tpl["vueNames"],
            "templateDirPaths": tpl["dirPaths"],
            "templateRoutePaths": tpl["routePaths"],
            "ghosts": ghosts,
            "missing": missing,
            "badPaths": bad_paths,
            "checks": checks,
            "pass": not failed,
        }, ensure_ascii=False, indent=2))
    else:
        print("# PR 模板「页面改动 Checklist」对拍（L12）")
        print("模板：%s" % os.path.relpath(template_path, REPO_ROOT).replace(os.sep, "/"))
        print("仓内会话分析域 .vue：%d 个；模板列出：%d 个 + 目录 %d 个\n"
              % (len(real_files), len(tpl["vueNames"]), len(tpl["dirPaths"])))
        for c in checks:
            print("%s %s%s" % ("✅" if c["pass"] else "❌", c["name"],
                               ("  — " + c["detail"]) if c["detail"] else ""))
        print("\nRESULT: %s（%s）" % (
            "PASS" if not failed else "FAIL",
            "清单与当前树一致" if not failed else "%d 条判据红" % len(failed)))
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(run())
