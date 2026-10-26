#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""wol.py — Wake-on-LAN 魔术包发送器（211 整机失联时的可选恢复手段）。

★ 背景（2026-10-23）：211（192.168.31.211）整机失联，本机 ARP 缓存里没有
  .211 条目 ⇒ 连它的网卡 MAC 都拿不到 ⇒ **无法发魔术包**。
  拿 MAC 的可行来源（按侵入性从低到高）：
    ① 路由器 192.168.31.1（OpenWrt/LuCI）的 dnsmasq 租约表
       /tmp/dhcp.leases —— ★ 主机断电时**租约仍在**（这是关键）；
       读法：ssh root@192.168.31.1 'cat /tmp/dhcp.leases'（需要路由器口令）
       或在 LuCI 面板「网络 → DHCP/DNS → 租约」里看 MAC。
    ② 同一网段其它主机的 ARP/neighbour 缓存（会随时间老化）。
    ③ 现场看网口/机箱标签。

★ 用法：
    python3 scripts/ops/wol.py AA:BB:CC:DD:EE:FF              # 广播到 255.255.255.255:9
    python3 scripts/ops/wol.py AA:BB:CC:DD:EE:FF 192.168.31.255

★ ⚠ 边界（必须先讲清）：
    · 魔术包要求**目标网卡处于待机且仍供电**（软关机/正常关机通常会断电网卡）；
      断电/拔电插头 ⇒ 无效。
    · 要求发送方与目标**同一二层网段**（本机 192.168.31.38 与 211 同段 ⇒ 满足）。
    · 有些网卡需要 BIOS/UEFI 里开启 "Wake on LAN" 且 OS 允许（Windows 需网卡属性
      「允许此设备唤醒计算机」+ 快速启动；Linux 需 `ethtool -s eth0 wol g`）。
    · ★ 因此：它是**低概率**手段，不是可靠手段；现场断电重启永远优先。
"""
import socket
import sys


def magic_packet(mac: str) -> bytes:
    parts = mac.replace("-", ":").split(":")
    if len(parts) != 6:
        raise ValueError("MAC must be 6 bytes, got %r" % (mac,))
    raw = bytes(int(part, 16) for part in parts)
    return b"\xff" * 6 + raw * 16


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    mac = sys.argv[1]
    dest = sys.argv[2] if len(sys.argv) > 2 else "255.255.255.255"
    pkt = magic_packet(mac)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
    sent = 0
    for _ in range(5):
        sock.sendto(pkt, (dest, 9))
        sent += 1
    sock.close()
    print("sent %d magic packets for %s -> %s:9 (%d bytes)" % (sent, mac, dest, len(pkt)))
    print("now wait ~30-60s and probe: ping 192.168.31.211 / ssh -o ConnectTimeout=5 211 'hostname'")
    return 0


if __name__ == "__main__":
    sys.exit(main())
