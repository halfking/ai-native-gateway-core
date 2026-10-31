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
import sys

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


def ensure(root: str, purpose: str = "mutate") -> bool:
    """确保专用缓存目录**建得出来** ⇒ 返回它是否可用。

    ★★★★ **失败安全**（★ r1048 轮加的）：★★★★ 目录**建不出来**时
    ★★★★ **不许把它当 GOCACHE** ⇒ ⇒ ★★★★ 因为 go 拿到一个**不可用**的 GOCACHE
    ★★★★ 会直接报 `failed to initialize build cache` ⇒ ⇒ ★★★★ ⇒ **脚本直接坏掉**
    ★★★★ ⇒ ⇒ ★★★★ 而「脚本坏掉」比「这次没隔离」**严重得多** ⇒ ⇒ ★★★★ ⇒
    ★★★★ ⇒ **宁可漏隔离，也不许把脚本弄坏**（★ 与仓内「★ 清理是卫生问题，
    ★★★★ ★★ 不该让它变成判据问题」那条纪律**同款**）。
    """
    d = dir_for(root, purpose)
    try:
        os.makedirs(d, exist_ok=True)
    except OSError as exc:
        sys.stderr.write(
            "★ 专用构建缓存 %s 建不出来 ⇒ ⇒ 本次**不设** GOCACHE（★ 回落用环境里的那个 ⇒ ⇒ 脚本照常跑）：%s\n"
            % (d, exc)
        )
        return False
    # ★ ★★ **可写性也要查**（★ r1048 轮运行期实测逼出来的）：★★ 只查「目录在不在」
    # ★ ★★ ⇒ ⇒ ★★ 是不够的 ⇒ ⇒ ★★ 因为目录**早就建好了**（★ 上一次跑过）⇒ ⇒ ★★
    # ★ ★★ `makedirs(exist_ok=True)` 会**成功** ⇒ ⇒ ★★ ⇒ 「在不在」这一条**判绿**
    # ★ ★★ ⇒ ⇒ ★★ ⇒ 而那个目录**其实不可写** ⇒ ⇒ ★★ ⇒ go 照样会报错 ⇒ ⇒ ★★ ⇒
    # ★ ★★ ⇒ **脚本照样坏掉** ⇒ ⇒ ★★ ⇒ 所以必须查**可写性**。
    if not os.access(d, os.W_OK | os.X_OK):
        sys.stderr.write(
            "★ 专用构建缓存 %s 不可写 ⇒ ⇒ 本次**不设** GOCACHE（★ 回落用环境里的那个 ⇒ ⇒ 脚本照常跑）\n"
            % d
        )
        return False
    return True


def apply_to_environ(purpose: str = "mutate", root: str = None) -> bool:
    """把**当前进程**的 `GOCACHE` 指向专用目录（★ 返回是否生效）。

    ★★ **为什么需要这个形态**：★★ Python 的 `subprocess` **默认继承**
    ★★ `os.environ` ⇒ ⇒ ★★ 所以**在脚本开头改一次 `os.environ`** ⇒ ⇒ ★★
    ★★ ⇒ 后面所有 `subprocess.run(…)`（★ **只要没有显式传 `env=`**）⇒ ⇒
    ★★ ⇒ 都会拿到专用目录 ⇒ ⇒ ★★ ⇒ **一处改动 ⇒ 覆盖整份脚本**。

    ★★ **无条件覆盖**（★ 不是 `setdefault`）：★★ ★★ 否则用户环境里已经设了
    ★★ `GOCACHE` ⇒ ⇒ ★★ **悄悄回落用全局缓存** ⇒ ⇒ ★★ ⇒ **门绿着，而隔离
    ★★ 根本没生效**（★ ★★ 这正是 r1047 轮 M04 臂逼出来的那条纪律）。

    ★★ **失败时不改**（★ 见 `ensure` 的注释）：★★ ⇒ ⇒ ★★ 脚本照常跑。
    """
    if root is None:
        try:
            root = repo_root()
        except Exception as exc:  # ★ 问不到仓根 ⇒ ⇒ 宁可漏隔离 ⇒ ⇒ 也不许坏脚本
            sys.stderr.write(
                "★ 解析仓根失败 ⇒ ⇒ 本次**不设** GOCACHE（★ 不影响脚本继续跑）：%s\n" % exc
            )
            return False
    if not ensure(root, purpose):
        return False
    os.environ["GOCACHE"] = dir_for(root, purpose)
    return True


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


# _pruned_once 登记【本进程已剪过】的用途（★ 每进程每用途只剪一次）。
#
# ★ ★★ **为什么要有它**：★★ 剪枝要**遍历整个缓存目录**（★ 逐个 `stat`）⇒ ⇒ ★★
# ★★ 实测（★ 2026-10-29，本机）：★★ 3.7 GB 的专用缓存 ⇒ ⇒ ★★ 一次 **0.133 s**
# ★★ ⇒ ⇒ ★★ 若**每次**调用都剪 ⇒ ⇒ ★★ 变异台里 `run_go` 调 N 次就多烧 N × 0.13 s
# ★★ ⇒ ⇒ ★★ ⇒ ★★ 而 Go 侧 `internal/gocache.Env` 走的是 `PruneOnce`
# ★★ （★ ★★ **每进程只剪一次**）⇒ ⇒ ★★ ⇒ ★★ ⇒ **两边的口径必须说同一句话**
# ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ 三边共用同一个约定 ⇒ ⇒ ★★ ⇒ 本模块与 Go 侧对齐。
_pruned_once = set()


def prune_once(root: str, purpose: str = "mutate") -> bool:
    """剪枝，但**每进程每用途只剪一次**（★ 与 Go 侧 `PruneOnce` 同口径）。

    ★ ★★ **失败不致命**：★ 剪枝失败只是「这次不省空间」⇒ ⇒ ★ 与 `prune` 同款。
    """
    key = (os.path.abspath(root), purpose)
    if key in _pruned_once:
        return False
    _pruned_once.add(key)
    return prune(root, purpose)


def env(purpose: str = "mutate", root: str = None) -> dict:
    """返回给子进程用的环境（★ `os.environ` + 【GOCACHE 覆盖】）。

    ★ ★★ **无条件覆盖**（★ 不是「仅当没设时才设」）：★★ Go `exec` 的语义是
    ★ ★★ 「**后出现的同名变量赢**」⇒ ⇒ ★★ ⇒ 即便用户环境里已经设了 `GOCACHE`，
    ★ ★★ 子进程也会用专用目录（★ ★★ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ ★★ **悄悄
    ★ ★★ 回落用全局缓存** ⇒ ⇒ ★★ ⇒ **门绿着，而隔离根本没生效**）。

    ★★★★ **剪枝按【每进程每用途一次】**（★ r1049 审计轮实测逼出来的）：★★★★
    ★★★★ 剪枝要**遍历整个缓存目录**（★ 逐个 `stat`）⇒ ⇒ ★★★★ 3.7 GB 的缓存
    ★★★★ 实测**一次 0.133 s** ⇒ ⇒ ★★★★ 若每次调用都剪 ⇒ ⇒ ★★★★ 变异台里
    ★★★★ `run_go` 调 N 次就多烧 N × 0.13 s ⇒ ⇒ ★★★★ ⇒ ★★ 而 Go 侧
    ★★★★ `internal/gocache.Env` 走的是 `PruneOnce`（★ ★★ **每进程只剪一次**）
    ★★★★ ⇒ ⇒ ★★★★ ⇒ ★★ ⇒ **两边的口径必须说同一句话** ⇒ ⇒ ★★ ⇒ ★★ ⇒
    ★★★★ ⇒ 本函数与 Go 侧对齐（★ 三边共用同一个约定）。
    """
    if root is None:
        root = repo_root()
    prune_once(root, purpose)
    e = dict(os.environ)
    # ★★★★ **失败安全**（★ r1048 轮）：★★★★ 专用目录**建不出来**时
    # ★★★★ **不覆盖** GOCACHE ⇒ ⇒ ★★★★ 因为 go 拿到一个**不可用**的 GOCACHE
    # ★★★★ 会直接报错 ⇒ ⇒ ★★★★ ⇒ **脚本直接坏掉** ⇒ ⇒ ★★★★ ⇒
    # ★★★★ ⇒ **宁可漏隔离，也不许把脚本弄坏** ⇒ ⇒ ★★★★ ⇒ 此时**原样保留**
    # ★★★★ ⇒ 环境里原本的那个（★ 它可能是**空的** ⇒ ⇒ 那种情况下**删掉这个键**
    # ★★★★ ⇒ ⇒ 别让子进程拿到空串 ⇒ ⇒ go 会报「不是绝对路径」）。
    if ensure(root, purpose):
        e["GOCACHE"] = dir_for(root, purpose)
    elif not os.environ.get("GOCACHE"):
        e.pop("GOCACHE", None)
    return e
