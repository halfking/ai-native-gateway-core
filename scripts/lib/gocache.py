#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gocache.py —— 仓内【专用 Go 构建缓存】的 Python 侧入口（★ r1047 轮 · 2026-10-28）

★ 用法：

    import sys, os
    sys.path.insert(0, os.path.join(REPO, "scripts", "lib"))
    import gocache

    subprocess.run(cmd, env=gocache.env("mutate"), ...)

★★ **为什么需要它**：★★

★★★ **背景**：★★★ 仓里的「变异验证脚本」都是【**改树 ⇒ 跑 go test ⇒ 还原**】
★★★ ⇒ ⇒ ★★ 而 Go 的构建缓存是**内容寻址**的（★ action ID = 全部输入内容 +
★★★ 构建配置的哈希）⇒ ⇒ ★★ **它不可能返回陈旧的产物** ⇒ ⇒ ★★ 但它**会无限增长**：
★★★ **树每变一次** ⇒ ⇒ ★★★ 编译产物的 action ID 就全变 ⇒ ⇒ ★★★ ⇒ 旧的那些
★★★ **永远不会再被命中** ⇒ ⇒ ★★★ ⇒ **纯垃圾** ⇒ ⇒ ★★★ 而默认那个缓存是
★★★ **用户那个全局的** ⇒ ⇒ ★★★ ⇒ **磁盘被撑爆** ⇒ ⇒ ★★★ ⇒ 全量 `go test ./...`
★★★ **编译阶段**就红 ⇒ ⇒ ★★★ ⇒ ★★ **红的原因不指向真正的原因**。

★★★ **实测（★ 2026-10-28，本机）**：★★★ `sql/schema` 那道门（★ `go vet
★★★ -tags=integration ./...` ⇒ ⇒ **整棵树带 tag 重编一遍**）⇒ ⇒ ★★ **一次**就把
★★★ 专用缓存撑到 **1.1 GB** ⇒ ⇒ ★★ 树每变一次就再来一批 ⇒ ⇒ ★★★ ⇒ **专用缓存
★★★ 自己也会撑爆磁盘**（★ 只是换个地方撑爆）⇒ ⇒ ★★ ⇒ **「隔离」必须配一个上限**。

★★ **为什么放在仓内 `.build-local/`**：★★ ① 它**已经在 `.gitignore` 里**
★★ （★ `.gitignore:42` ⇒ `.build-local/`）⇒ ⇒ **绝不会被提交进仓库**；
★★ ② 它跟着仓走 ⇒ ⇒ `git clean -xfd` 能顺手回收它。

★★ **与 Go 侧 / shell 侧的关系**：★★ Go 侧 `internal/gocache`（★ `Dir` / `Env` /
★★ `Prune`）、shell 侧 `scripts/lib/gocache.sh` ⇒ ⇒ ★★ **三边共用同一个目录约定
★★ 与同一个上限（6 GB）** ⇒ ⇒ ★★ ⇒ **一份真值**。
"""

import os
import shutil

# MAX_BYTES 是【专用缓存】的尺寸上限（★ 6 GB）。
#
# ★ ★★ **与 `internal/gocache.MaxBytes` / `scripts/lib/gocache.sh` 的
# ★ ★★ `GOCACHE_MAX_BYTES` 同值**：★★ 三边是【同一个约定】的三条腿 ⇒ ⇒ ★★ ⇒
# ★ ★★ **改一处就要改另外两处** ⇒ ⇒ ★★ ⇒ **新脚本一律用这三个入口之一**，
# ★ ★★ **不要自己拼路径**。
MAX_BYTES = 6 * 1024 * 1024 * 1024

# REL_DIR_PREFIX 是【专用缓存目录】相对仓根的前缀（★ 必须在 `.build-local/` 下）。
REL_DIR_PREFIX = ".build-local/.gocache-"


def repo_root() -> str:
    """返回仓根（★ 问 git 自己 ⇒ ⇒ 独立于调用点的相对位置）。"""
    import subprocess

    out = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True
    )
    if out.returncode != 0:
        raise RuntimeError("解析仓根失败（需要 git 在 PATH）")
    return out.stdout.strip()


def dir_for(root: str, purpose: str = "mutate") -> str:
    """返回某个用途的【专用构建缓存目录】的绝对路径（★ **不创建**）。"""
    return os.path.join(root, REL_DIR_PREFIX + purpose)


def prune(root: str, purpose: str = "mutate") -> bool:
    """专用缓存超上限 ⇒ **整个删掉**（★ 返回是否删过）。

    ★ ★★ **为什么「整个删掉」而不是「删一部分」**：★ Go 的构建缓存
    ★ ★★ **不支持按条目删除** ⇒ ⇒ ★ 只能全清 ⇒ ⇒ ★★ 这也是「专用」的价值：
    ★ ★★ **全清的代价被限制在一个专用目录里**（★ 而不是用户的全局缓存）。

    ★ ★★ **剪枝失败不致命**：★ 只返回 False ⇒ ⇒ ★ 与仓内「★ 清理是卫生问题，
    ★ ★★ 不该让它变成判据问题」那条纪律**同款** ⇒ ⇒ ★★ ⇒ **脚本照常跑**。
    """
    d = dir_for(root, purpose)
    if not os.path.isdir(d):
        return False
    total = 0
    for dirpath, _dirnames, filenames in os.walk(d):
        for fn in filenames:
            try:
                total += os.path.getsize(os.path.join(dirpath, fn))
            except OSError:
                pass
    if total <= MAX_BYTES:
        return False
    shutil.rmtree(d, ignore_errors=True)
    return True


def env(purpose: str = "mutate", root: str = None) -> dict:
    """返回给子进程用的环境（★ `os.environ` + 【GOCACHE 覆盖】）。

    ★ ★★ **无条件覆盖**（★ 不是「仅当没设时才设」）：★★ Go `exec` 的语义是
    ★ ★★ 「**后出现的同名变量赢**」⇒ ⇒ ★★ ⇒ 即便用户环境里已经设了 `GOCACHE`，
    ★ ★★ 子进程也会用专用目录（★ ★★ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ ★★ **悄悄
    ★ ★★ 回落用全局缓存** ⇒ ⇒ ★★ ⇒ **门绿着，而隔离根本没生效**）。
    """
    if root is None:
        root = repo_root()
    prune(root, purpose)
    e = dict(os.environ)
    e["GOCACHE"] = dir_for(root, purpose)
    return e
