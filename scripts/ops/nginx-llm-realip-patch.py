#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
nginx-llm-realip-patch.py — 把「某域名的 TLS 腿」上的 X-Real-IP 从
$ns_real_ip 改成 $remote_addr，且**只在改得对的时候才改**。

为什么要有这个脚本（2026-10-23，211 部署复验轮 F2）
---------------------------------------------------
原实现把这段逻辑**内嵌在 deploy-seamless-docker.sh 的 heredoc 里**，
带来三个真缺陷（都不是「样式问题」，是**会误判/会留隐患**的真缺陷）：

1. ★ **只在自己的 conf 里找 443 块** ⇒ 在 211 上**永远找不到**。
   211 的 nginx 有两条腿：
   · mesh 腿：`ns-gateway-211.conf` 里 `server_name llm.itestu.cn` +
     `listen 100.106.67.198:8000`（上跳 252 进来；252 用 PROXY protocol
     注入真实客户端 IP ⇒ 这里**必须**保留 `$ns_real_ip`，不能改）；
   · 443 腿：在**另一个文件**里，是**通用 TLS 终结器**，`server_name`
     里**不含**该域 ⇒ 引擎在自己的 conf 里永远匹配不到「按域 443 块」
     ⇒ 每次部署都打 `noop-no-block` + 一条 WARN「块形态变了?」
     ⇒ ★ 这条 WARN 是**假的**：既不是块形态变了，也不是「该修的没修成」，
       而是**本机压根不承担该域的 443**。
   ⇒ 修法：★ **跨文件搜** + ★ 把「没找到」**分成两种结论**：
     「确实没有该域的 443 块」（信息，不是告警）与
     「找到了但形态不认识」（才配 WARN）。

2. ★ **域名写死 `llm.itestu.cn`**。引擎文件头自称「引擎本身不含任何具体
   主机名」，但补丁逻辑里写死了域名；而权威来源明明是 `DD_DOMAIN`
   （211 入口已经导出）。⇒ 换域名时这条修补会**静默地对另一个域生效**，
   或者对谁都不生效。

3. ★ **`nginx -t` 失败时不还原文件**。同仓审计
   `docs/audit/2026-10-20-211-bluegreen-docker-engine.md` §4.1 已写明
   正确形态是「备份改前内容 → 改文件 → nginx -t → 失败则**还原**」；
   而原分支只 warn 不还原 ⇒ 磁盘上留下一份**没验证过的配置**，
   此后任何一次 nginx reload 都会把它加载进去（潜伏半切换态）。
   ⇒ 本脚本负责「备份 + 改」；**还原由引擎在 `nginx -t` 失败时做**
     （还原需要知道 nginx 的判定结果，那是引擎的事）。

本脚本的职责边界
----------------
· **只判断，不猜**：所有结论都带机器可读的 `RESULT=` 分类 + 人读的 `DETAIL=`；
  引擎按**分类**决定「改 / 不改 / 报」——**不再靠退出码做分支**。
  （原实现正是在这里踩过「`if ! cmd; then 成功 else 失败`」的分支反转坑，
    见 deploy-seamless-docker.sh 里 2026-10-22「二次修」的注释。）
· **默认不改**：不带 `--apply` 只做检测，报 `would-patch`。
· **共享终结器不碰**：`server_name` 里除该域外还有别的域 ⇒ 该 443 块是共享的
  ⇒ 改它会波及别的域 ⇒ 只报告不自动改。
· **退出码只表示「有没有硬错误」**：0 = 已分类（含所有 skip），1 = 硬错误
  （文件读不了等）。分类信息在 stdout，**不靠退出码传语义**。

用法
----
    # 只检测（默认，不改任何文件）
    python3 scripts/ops/nginx-llm-realip-patch.py --domain llm.itestu.cn \
        --conf /etc/nginx/conf.d/ns-gateway-211.conf --search-root /etc/nginx/conf.d

    # 真改
    python3 scripts/ops/nginx-llm-realip-patch.py --domain llm.itestu.cn \
        --search-root /etc/nginx/conf.d --search-root /etc/nginx/sites-enabled --apply

输出（每行一个键值对，RESULT 一定在第一行）：
    RESULT=patched
    FILE=/etc/nginx/conf.d/llm.conf
    BACKUP=/etc/nginx/conf.d/llm.conf.bak-dd-realip
    DETAIL=...
"""

import argparse
import os
import re
import sys

# ── 语法常量 ────────────────────────────────────────────────────────────
# `server` 必须是**语句开头**：前一个字符只能是空白 / ; / { / }。
# 这样 `location /server {` 不会被误当成 server 块。
SERVER_RE = re.compile(r'(?<![^\s;{}])server\s*\{')
# `proxy_set_header X-Real-IP <值>;`（值不含空白与分号）
REALIP_RE = re.compile(r'proxy_set_header\s+X-Real-IP\s+([^\s;]+)\s*;')

OLD_VALUE = '$ns_real_ip'
NEW_VALUE = '$remote_addr'
BACKUP_SUFFIX = '.bak-dd-realip'


class HardError(Exception):
    """硬错误（读不了文件等）——与「分类结论」严格区分。"""


# ── 文本层：掩码 / 切块 / 取指令 ────────────────────────────────────────
def mask_noncode(text):
    """把注释与引号字符串内容替换成**等长**空格（换行保留），返回掩码。

    掩码**只用于定位**：注释/字符串里的 `{` `}` `server` 不能骗过括号配对。
    掩码与原文**长度一致** ⇒ 掩码上的偏移在原文上直接可用 ⇒ 改写逐字节精确。
    """
    out = list(text)
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == '#':
            while i < n and text[i] != '\n':
                out[i] = ' '
                i += 1
            continue
        if ch in ('"', "'"):
            quote = ch
            i += 1
            while i < n:
                if text[i] == '\\':
                    out[i] = ' '
                    if i + 1 < n and text[i + 1] != '\n':
                        out[i + 1] = ' '
                    i += 2
                    continue
                if text[i] == quote:
                    i += 1
                    break
                if text[i] != '\n':
                    out[i] = ' '
                i += 1
            continue
        i += 1
    return ''.join(out)


def server_blocks(masked):
    """返回每个 `server { ... }` 块的 (open_brace_idx, end) 绝对区间。

    ★ 起点是**左花括号**而不是 `server` 关键字 —— 块内语句的层级是从
    「{ 之后」开始数的；起点若落在关键字上，首条语句会被算成
    「`server {` 这半截 + 后面内容」，深度从此错位一级
    ⇒ 块自身层级的 listen/server_name 全部读不到（2026-10-23 实测踩过）。
    """
    spans = []
    for m in SERVER_RE.finditer(masked):
        depth = 0
        i = m.end() - 1
        while i < len(masked):
            c = masked[i]
            if c == '{':
                depth += 1
            elif c == '}':
                depth -= 1
                if depth == 0:
                    spans.append((m.end() - 1, i + 1))
                    break
            i += 1
    return spans


def block_statements(masked, start, end):
    """块**自身层级**的语句（不含嵌套 location/if 内部）。

    产出 (stmt_start, stmt_end, stmt_text)；stmt_text 是掩码上的文本。
    """
    depth = 1
    stmt_start = start + 1
    i = start + 1
    while i < end:
        c = masked[i]
        if c == '{':
            depth += 1
            i += 1
            stmt_start = i
            continue
        if c == '}':
            depth -= 1
            i += 1
            stmt_start = i
            continue
        if c == ';' and depth == 1:
            yield (stmt_start, i, masked[stmt_start:i])
            stmt_start = i + 1
            i += 1
            continue
        i += 1


def directive(masked, start, end, name):
    """取块自身层级的某指令：返回 (stmt_start, stmt_end, 参数字符串) 或 None。"""
    for s, e, text in block_statements(masked, start, end):
        parts = text.split(None, 1)
        if not parts or parts[0] != name:
            continue
        rest = parts[1].strip() if len(parts) > 1 else ''
        return (s, e, rest)
    return None


def find_realip(masked, start, end):
    """块内（任意层级，通常在 location 里）找 X-Real-IP 头设置。

    返回 (match_start, match_end, value_start, value_end, value) 或 None。
    """
    m = REALIP_RE.search(masked, start, end)
    if not m:
        return None
    return (m.start(), m.end(), m.start(1), m.end(1), m.group(1))


# ── 语义层 ──────────────────────────────────────────────────────────────
def name_matches(pattern, domain):
    """server_name 模式是否覆盖该域：精确匹配，或 `*.后缀` 通配。"""
    p = pattern.lower().rstrip('.')
    d = domain.lower().rstrip('.')
    if p == d:
        return True
    if p.startswith('*.') and d.endswith(p[1:]):
        return True
    return False


def is_tls_443(listen_args):
    """listen 参数里是否有 443 且带 ssl。"""
    args = listen_args.split()
    if 'ssl' not in args:
        return False
    for a in args:
        if a == '443' or a.endswith(':443'):
            return True
    return False


def collect_files(explicit, search_roots):
    """候选文件：显式给出的 + 搜索根下的文件；按 realpath 去重（sites-enabled 是符号链接）。"""
    files = []
    seen = set()

    def add(path):
        try:
            rp = os.path.realpath(path)
        except OSError:
            rp = path
        if rp in seen:
            return
        if not os.path.isfile(path):
            return
        seen.add(rp)
        files.append(path)

    for path in explicit:
        add(path)
    for root in search_roots:
        if not os.path.isdir(root):
            continue
        try:
            names = sorted(os.listdir(root))
        except OSError:
            continue
        for name in names:
            add(os.path.join(root, name))
    return files


def analyze(files, domain):
    """扫描候选文件，返回 (分类, 明细行列表, 可改的块列表)。"""
    patchable = []      # [(path, text, masked, value_start, value_end, block_span)]
    already = []
    shared = []
    unknown = []
    non_tls_legs = []   # 该域有 server 块，但不在 443 TLS 腿上（mesh 腿等）
    errors = []
    seen_blocks = set()

    for path in files:
        try:
            with open(path, 'r', encoding='utf-8', errors='replace') as fh:
                text = fh.read()
        except OSError as exc:
            errors.append('%s: 读不了 (%s)' % (path, exc))
            continue
        masked = mask_noncode(text)
        for bs, be in server_blocks(masked):
            key = (path, bs, be)
            if key in seen_blocks:
                continue
            seen_blocks.add(key)

            names_stmt = directive(masked, bs, be, 'server_name')
            names = names_stmt[2].split() if names_stmt else []
            if not any(name_matches(n, domain) for n in names):
                continue

            listen_stmt = directive(masked, bs, be, 'listen')
            listen_args = listen_stmt[2] if listen_stmt else ''
            if not is_tls_443(listen_args):
                # 该域的 server 块存在，但**不是** 443 TLS 腿
                # （211 的 mesh 腿 :8000 就是这种：上跳 252 注入真实 IP，
                #   这里**必须**保留 $ns_real_ip，不该改）。
                non_tls_legs.append((path, listen_args or '(无 listen)'))
                continue

            others = [n for n in names if n != '_' and not name_matches(n, domain)]
            if others:
                shared.append((path, listen_args, names))
                continue

            realip = find_realip(masked, bs, be)
            if realip is None:
                unknown.append((path, listen_args, '该 443 块里没有 X-Real-IP 头设置'))
                continue
            value = realip[4]
            if value == NEW_VALUE:
                already.append((path, listen_args))
                continue
            if value == OLD_VALUE:
                patchable.append((path, text, masked, realip[2], realip[3], (bs, be)))
                continue
            unknown.append((path, listen_args, 'X-Real-IP 的值是 %s（既不是 %s 也不是 %s）'
                            % (value, OLD_VALUE, NEW_VALUE)))

    # ── 分类（顺序即优先级）────────────────────────────────────────────
    detail = []
    if patchable:
        for path, _text, _masked, vs, ve, span in patchable:
            detail.append('可改：%s 的 server 块（%d..%d）里 X-Real-IP %s → %s'
                          % (path, span[0], span[1], OLD_VALUE, NEW_VALUE))
        return ('patchable', detail, patchable, errors)
    if already:
        for path, listen_args in already:
            detail.append('已是目标形态：%s（listen %s）的 X-Real-IP 已是 %s'
                          % (path, listen_args, NEW_VALUE))
        return ('already', detail, [], errors)
    if shared:
        for path, listen_args, names in shared:
            detail.append('★ 共享终结器，未改：%s（listen %s）的 server_name 还含 %s ⇒ '
                          '改它会波及别的域'
                          % (path, listen_args, ' '.join(n for n in names
                                                        if not name_matches(n, domain))))
        return ('shared', detail, [], errors)
    if unknown:
        for path, listen_args, why in unknown:
            detail.append('★ 形态不认识：%s（listen %s）—— %s' % (path, listen_args, why))
        return ('unknown', detail, [], errors)
    # 没有可改/已好/共享/不认识 ⇒ 结论是「本机不承担该域的 443」
    if non_tls_legs:
        for path, listen_args in non_tls_legs:
            detail.append('该域的 server 块在 %s，但只 listen %s（非 443 TLS 腿）'
                          % (path, listen_args))
        detail.append('⇒ 本机不由该域的 443 腿承担（443 由通用终结器/上游负责）'
                      ' ⇒ 无需 X-Real-IP 修补')
    else:
        detail.append('本机没有任何 server_name 含 %s 的 server 块' % domain)
    return ('no-443-leg', detail, [], errors)


def apply_patch(patchable):
    """把可改的块改掉；每个文件写一份**改前**备份（每次覆盖，不复用旧备份）。"""
    changed = []
    for path, text, _masked, vs, ve, _span in patchable:
        new_text = text[:vs] + NEW_VALUE + text[ve:]
        backup = path + BACKUP_SUFFIX
        with open(backup, 'w', encoding='utf-8') as fh:
            fh.write(text)
        with open(path, 'w', encoding='utf-8') as fh:
            fh.write(new_text)
        changed.append((path, backup))
    return changed


def main(argv=None):
    ap = argparse.ArgumentParser(
        description='把某域名 TLS 腿上的 X-Real-IP 从 $ns_real_ip 改成 $remote_addr')
    ap.add_argument('--domain', required=True, help='目标域名（如 llm.itestu.cn）')
    ap.add_argument('--conf', action='append', default=[],
                    help='显式候选 nginx 配置文件（可重复）')
    ap.add_argument('--search-root', action='append', default=[],
                    help='要扫描的 nginx 配置目录（可重复，如 /etc/nginx/conf.d）')
    ap.add_argument('--apply', action='store_true',
                    help='真改（默认只检测并报 would-patch）')
    args = ap.parse_args(argv)

    domain = args.domain.strip()
    if not domain:
        sys.stderr.write('ERROR: --domain 不能为空\n')
        return 1

    files = collect_files(args.conf, args.search_root)
    if not files:
        print('RESULT=no-443-leg')
        print('DETAIL=候选配置文件为空（conf/search-root 都没给到可读文件）')
        return 0

    try:
        kind, detail, patchable, errors = analyze(files, domain)
    except HardError as exc:
        sys.stderr.write('ERROR: %s\n' % exc)
        return 1

    for err in errors:
        print('DETAIL=★ 读配置失败：%s' % err)

    if kind == 'patchable':
        if args.apply:
            try:
                changed = apply_patch(patchable)
            except OSError as exc:
                sys.stderr.write('ERROR: 写文件失败：%s\n' % exc)
                return 1
            print('RESULT=patched')
            for path, backup in changed:
                print('FILE=%s' % path)
                print('BACKUP=%s' % backup)
        else:
            print('RESULT=would-patch')
    elif kind == 'already':
        print('RESULT=already')
    elif kind == 'shared':
        print('RESULT=skip-shared-leg')
    elif kind == 'unknown':
        print('RESULT=noop-unknown-shape')
    else:
        print('RESULT=skip-domain-not-on-443-block')

    for line in detail:
        print('DETAIL=%s' % line)
    return 0


if __name__ == '__main__':
    sys.exit(main())
