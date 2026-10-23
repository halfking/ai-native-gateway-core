#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
nginx-upstream-port.py — 读 / 改 nginx upstream 块里的「主 server 端口」。

★ 为什么它必须存在（2026-10-23，211 的 502 事故根修）
------------------------------------------------------
「活跃槽端口」这个事实**只存在于 nginx 里**，而引擎的整套蓝绿状态机
（判活跃槽 → 选候选槽 → 切 upstream → 停旧槽）全靠它。⇒ **读错 = 切错**。

而 nginx 的 upstream 块里**经常留着旧端口的注释**（245 实况，2026-10-23 读到的原文）：

    upstream llmgo_local_245 {
        # 2026-08-31: Atomic blue-green fragment; deployer replaces this file and reloads nginx.
        # Original hardcoded `server 127.0.0.1:8781;` did not allow nginx -s reload to
        # switch traffic between the active (8781) and the canary (8782) candidate.
        include /opt/llm-gateway-go/run/active-upstream.conf;
        keepalive 32;
    }

⇒ **不剥注释**的旧实现会：
  · 读侧：先命中**注释里那个旧端口**（8781）⇒ 「活跃槽」判错；
  · 写侧：把**注释**改掉并报 "patched" ⇒ nginx reload 后活的上游**根本没变**
    ⇒ 引擎以为「已切流」⇒ 随后停掉「旧槽」⇒ 流量落空 ⇒ **502**。

⇒ 所以本脚本做三件事：
  1. **掩码**（注释 + 引号字符串 → 等长空格）后再定位 ⇒ 偏移与原文一致，
     改写逐字节精确，注释里的端口一律不算数；
  2. **认两种形态**：
     · 端口**烤死**在块里（docker 形态 / 211）；
     · 块里是 `include`（systemd 形态 / 154、245）⇒ 端口在**生成文件**里
       ⇒ 去那个文件里取（可显式指定 --include）；
  3. **跳过 backup 行**（备份上游不是主上游）。

用法
----
    # 读（打印主端口，空 = 读不到）
    python3 nginx-upstream-port.py --conf /etc/nginx/conf.d/x.conf --upstream ns_u

    # 读（include 在别处时，显式给出那个文件）
    python3 nginx-upstream-port.py --conf c.conf --upstream u --include /path/active-upstream.conf

    # 改（就地改写主端口；--backup 写「改前内容」）
    python3 nginx-upstream-port.py --conf c.conf --upstream u --switch 8783 --backup c.conf.bak

输出（改模式）：patched / already / noop-no-block / noop-no-primary-server /
                noop-upstream-uses-include
退出码：0 成功或已分类；2 改模式下的 noop（**不是**硬错误，由调用方分类处置）
"""

import argparse
import os
import re
import sys


def mask(text):
    """把注释与引号字符串内容替换成**等长**空格（换行保留）。

    ⇒ 掩码与原文**长度一致** ⇒ 掩码上的偏移在原文上直接可用 ⇒ 改写逐字节精确。
    """
    out = []
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == '#':
            while i < n and text[i] != '\n':
                out.append(' ')
                i += 1
            continue
        if ch in ('"', "'"):
            quote = ch
            out.append(' ')
            i += 1
            while i < n:
                c = text[i]
                if c == '\\' and i + 1 < n:
                    out.append(' ')
                    out.append('\n' if text[i + 1] == '\n' else ' ')
                    i += 2
                    continue
                if c == quote:
                    out.append(' ')
                    i += 1
                    break
                out.append('\n' if c == '\n' else ' ')
                i += 1
            continue
        out.append(ch)
        i += 1
    return ''.join(out)


def find_block(masked, upstream):
    """返回 upstream 块内容在**同一坐标系**里的 (start, end)；找不到返回 None。"""
    m = re.search(r'upstream\s+' + re.escape(upstream) + r'\s*\{(.*?)\n\}',
                  masked, re.S)
    if not m:
        return None
    return m.start(1), m.end(1)


def primary_port_in(text, upstream):
    """从（已掩码或未掩码的）文本里取主 server 端口；取不到返回 ''。"""
    masked = mask(text)
    span = find_block(masked, upstream)
    if span is None:
        return ''
    body = masked[span[0]:span[1]]
    for line in body.split('\n'):
        if 'backup' in line:
            continue
        mm = re.search(r'server\s+127\.0\.0\.1:(\d+)', line)
        if mm:
            return mm.group(1)
    return ''


def include_target(masked, upstream):
    """upstream 块里的 include 路径（没有则 ''）。"""
    span = find_block(masked, upstream)
    if span is None:
        return ''
    body = masked[span[0]:span[1]]
    mm = re.search(r'include\s+([^;]+);', body)
    return mm.group(1).strip() if mm else ''


def resolve_include(conf_path, rel, explicit):
    """把 include 目标解析成一个可读路径：显式 > 绝对 > /etc/nginx/ > conf 同目录。"""
    cands = []
    if explicit:
        cands.append(explicit)
    if rel:
        cands.append(rel)
        cands.append(os.path.join('/etc/nginx', rel.lstrip('/')))
        cands.append(os.path.join(os.path.dirname(conf_path), rel.lstrip('/')))
    for cand in cands:
        try:
            if cand and os.path.isfile(cand):
                return cand
        except OSError:
            continue
    return ''


def read_port(conf_path, upstream, explicit_include=''):
    try:
        with open(conf_path, encoding='utf-8', errors='replace') as fh:
            text = fh.read()
    except OSError:
        return ''
    port = primary_port_in(text, upstream)
    if port:
        return port
    masked = mask(text)
    rel = include_target(masked, upstream)
    path = resolve_include(conf_path, rel, explicit_include)
    if not path:
        return ''
    try:
        with open(path, encoding='utf-8', errors='replace') as fh:
            sub = fh.read()
    except OSError:
        return ''
    return primary_port_in(sub, upstream) or _port_from_plain(sub)


def _port_from_plain(text):
    """include 文件里就是一行 `server 127.0.0.1:<port>;`（没有 upstream 块）。"""
    masked = mask(text)
    for line in masked.split('\n'):
        if 'backup' in line:
            continue
        mm = re.search(r'server\s+127\.0\.0\.1:(\d+)', line)
        if mm:
            return mm.group(1)
    return ''


def switch_port(conf_path, upstream, newport, backup_path):
    try:
        with open(conf_path, encoding='utf-8', errors='replace') as fh:
            text = fh.read()
    except OSError as exc:
        sys.stderr.write('ERROR: 读不了 %s (%s)\n' % (conf_path, exc))
        return 1

    masked = mask(text)
    span = find_block(masked, upstream)
    if span is None:
        print('noop-no-block')
        return 2
    body_start, body_end = span
    port_abs = port_end = -1
    for lm in re.finditer(r'[^\n]*', masked[body_start:body_end]):
        line = lm.group(0)
        if 'backup' in line:
            continue
        sm = re.search(r'(server\s+127\.0\.0\.1:)(\d+)', line)
        if sm:
            port_abs = body_start + lm.start() + sm.start(2)
            port_end = body_start + lm.start() + sm.end(2)
            break
    if port_abs < 0:
        if re.search(r'include\s+[^;]+;', masked[body_start:body_end]):
            print('noop-upstream-uses-include')
            return 2
        print('noop-no-primary-server')
        return 2

    newtext = text[:port_abs] + newport + text[port_end:]
    if backup_path:
        try:
            with open(backup_path, 'w', encoding='utf-8') as fh:
                fh.write(text)
        except OSError as exc:
            sys.stderr.write('ERROR: 写不了备份 %s (%s)\n' % (backup_path, exc))
            return 1
    if newtext == text:
        print('already')
        return 0
    try:
        with open(conf_path, 'w', encoding='utf-8') as fh:
            fh.write(newtext)
    except OSError as exc:
        sys.stderr.write('ERROR: 写不了 %s (%s)\n' % (conf_path, exc))
        return 1
    print('patched')
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description='读/改 nginx upstream 的主 server 端口')
    ap.add_argument('--conf', required=True, help='nginx 配置文件')
    ap.add_argument('--upstream', required=True, help='upstream 块名')
    ap.add_argument('--include', default='', help='include 形态下端口所在文件（显式给出）')
    ap.add_argument('--print-include', action='store_true',
                    help='只打印 upstream 块里的 include 目标（没有则打印空行）')
    ap.add_argument('--switch', default='', help='改模式：改成这个端口')
    ap.add_argument('--backup', default='', help='改模式：备份路径（写「改前内容」）')
    args = ap.parse_args(argv)

    if args.print_include:
        try:
            with open(args.conf, encoding='utf-8', errors='replace') as fh:
                print(include_target(mask(fh.read()), args.upstream))
        except OSError:
            print('')
        return 0
    if args.switch:
        return switch_port(args.conf, args.upstream, args.switch, args.backup)
    print(read_port(args.conf, args.upstream, args.include))
    return 0


if __name__ == '__main__':
    sys.exit(main())
