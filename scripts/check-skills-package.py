#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""技能包完整性校验器（docs/skills/ 的机械门禁）.

检查项：
  1. 每个 docs/skills/<name>/SKILL.md 的 frontmatter 合法：
     name 为 [a-z0-9-]+、等于目录名、全目录唯一，description >= 20 字符。
  2. README.md 技能索引与磁盘双向同步（不多不少）。
  3. 反引号内「路径形态」引用必须在仓内可解析。

引用检查的口径（有意收窄，避免误报淹没信号）：
  - 只查「含 / 且字符集纯净」的引用（^[A-Za-z0-9_.-][A-Za-z0-9_./-]*$）。
    本仓登记过的真实缺陷形态（如 sql/embeddata/ 这类不存在路径）必然命中该形态。
  - 跳过：含空格（命令行）、以 / http # .. 开头（路由/URL/锚点/仓外相对路径）、
    含 NNN < > … * 等占位/通配、字符集不纯（代码标识符、正则片段、k=v 等）。
  - 存在性判定用「仓内路径后缀匹配」：引用 R 合法当且仅当存在仓内文件/目录，
    其路径以 R 结尾且按路径分量对齐（_shared/login.yaml 命中
    maestro/flows/_shared/login.yaml）。

用法：python3 scripts/check-skills-package.py   （退出码 0=通过，1=失败）
"""
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
ROOT = REPO / "docs" / "skills"
SKIP_DIRS = {".git", "node_modules", "dist", ".venv", "vendor"}

PATH_RE = re.compile(r"^[A-Za-z0-9_.\-][A-Za-z0-9_./\-]*$")
PLACEHOLDER_MARKS = ("NNN", "<", ">", "…", "*")
# 构建产物段：其存在性取决于是否执行过构建，不应作为门禁判据（确定性要求）。
BUILD_SEGMENTS = {"dist", "build"}


def collect_repo_paths():
    """仓内全部文件与目录的相对路径集合（用于后缀匹配）。"""
    files, dirs = set(), set()
    for p in REPO.rglob("*"):
        rel = p.relative_to(REPO).as_posix()
        if any(part in SKIP_DIRS for part in Path(rel).parts):
            continue
        if p.is_dir():
            dirs.add(rel)
        else:
            files.add(rel)
    return files, dirs


def ref_is_path_shape(ref: str) -> bool:
    if "/" not in ref:
        return False
    if not PATH_RE.fullmatch(ref):
        return False
    if ref.startswith(("/", "http", "#", "..")):
        return False
    if any(t in ref for t in PLACEHOLDER_MARKS):
        return False
    if BUILD_SEGMENTS.intersection(Path(ref).parts):
        return False
    return True


def ref_resolves(ref: str, files: set, dirs: set) -> bool:
    n = len(Path(ref).parts)
    for cand in files | dirs:
        parts = Path(cand).parts
        if len(parts) >= n and parts[-n:] == Path(ref).parts:
            return True
    return False


def main() -> int:
    errors = []
    names = {}

    skill_files = sorted(ROOT.rglob("SKILL.md"))
    if not skill_files:
        print("FAIL docs/skills/ 下没有任何 SKILL.md")
        return 1

    for sf in skill_files:
        rel = sf.relative_to(REPO).as_posix()
        text = sf.read_text(encoding="utf-8")
        m = re.match(r"^---\n(.*?)\n---\n", text, re.DOTALL)
        if not m:
            errors.append(f"{rel}: 缺 YAML frontmatter")
            continue
        fm = m.group(1)
        nm = re.search(r"^name:\s*(\S+)", fm, re.M)
        ds = re.search(r"^description:\s*(.+)", fm, re.M)
        if not nm:
            errors.append(f"{rel}: frontmatter 缺 name")
            continue
        name = nm.group(1)
        if not re.fullmatch(r"[a-z0-9-]+", name):
            errors.append(f"{rel}: name 非法（须 [a-z0-9-]+）: {name}")
        if name != sf.parent.name:
            errors.append(f"{rel}: name ({name}) != 目录名 ({sf.parent.name})")
        if not ds or len(ds.group(1).strip()) < 20:
            errors.append(f"{rel}: description 缺失或不足 20 字符")
        if name in names:
            errors.append(f"name 重复: {name}（{names[name]} 与 {rel}）")
        names[name] = rel

    files, dirs = collect_repo_paths()
    for sf in skill_files:
        rel = sf.relative_to(REPO).as_posix()
        for ref in re.findall(r"`([^`\n]+)`", sf.read_text(encoding="utf-8")):
            if ref_is_path_shape(ref) and not ref_resolves(ref, files, dirs):
                errors.append(f"{rel}: 引用路径在仓内不可解析: {ref}")

    readme = (ROOT / "README.md").read_text(encoding="utf-8") if (ROOT / "README.md").exists() else ""
    indexed = set(re.findall(r"\[`?([a-z0-9-]+)`?\]\(\1/SKILL\.md\)", readme))
    for n in sorted(set(names) - indexed):
        errors.append(f"README.md 技能索引缺: {n}")
    for n in sorted(indexed - set(names)):
        errors.append(f"README.md 技能索引含不存在的技能: {n}")

    if errors:
        print(f"FAIL（{len(errors)} 项）")
        for e in errors:
            print("  -", e)
        return 1
    print(f"OK 技能数={len(skill_files)} 索引同步 路径引用可解析 frontmatter 合法")
    return 0


if __name__ == "__main__":
    sys.exit(main())
