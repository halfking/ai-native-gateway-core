#!/usr/bin/env python3
# docs/全方面测试/tools/mock_orchestrator.py
#
# 60-supplier 群组级控制：
#   - 按组（A-L）批量设置状态 / profile / quota
#   - 健康矩阵快照（用于 dashboard）
#
# Usage:
#   # 健康矩阵
#   ./mock_orchestrator.py health-matrix
#
#   # 设置整组
#   ./mock_orchestrator.py set-group G slow
#   ./mock_orchestrator.py set-group K rate_limited
#   ./mock_orchestrator.py reset-group K
#   ./mock_orchestrator.py reset-all
#
#   # 单实例
#   ./mock_orchestrator.py set 19090 slow
#
#   # 全组配额
#   ./mock_orchestrator.py set-quota 19090 2000 1800
#   ./mock_orchestrator.py set-group-quota C 2000 1800

import argparse
import asyncio
import json
import os
import sys
import time
from typing import List

import aiohttp

# 60 实例的端口映射（与 start_suppliers.sh / docs/ 全方面测试/02 一致）
BASE_PORT = 19080
GROUPS = ["A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L"]
GROUP_PROFILE_DEFAULT = {
    "A": {"plan": "PAYG", "concurrency": 100, "fp_slot": 50, "tier": 1},
    "B": {"plan": "PAYG", "concurrency": 100, "fp_slot": 50, "tier": 1},
    "C": {
        "plan": "TokenPlan",
        "concurrency": 10,
        "fp_slot": 5,
        "tier": 1,
        "quota": 1000000,
    },
    "D": {
        "plan": "CodePlan",
        "concurrency": 20,
        "fp_slot": 10,
        "tier": 1,
        "quota": 1000000,
    },
    "E": {"plan": "PAYG", "concurrency": 50, "fp_slot": 30, "tier": 2},
    "F": {"plan": "Free", "concurrency": 30, "fp_slot": 20, "tier": 2},
    "G": {
        "plan": "PAYG",
        "concurrency": 60,
        "fp_slot": 30,
        "tier": 2,
        "default_state": "slow",
    },
    "H": {"plan": "PAYG", "concurrency": 100, "fp_slot": 3, "tier": 1},
    "I": {"plan": "PAYG", "concurrency": 20, "fp_slot": 20, "tier": 1},
    "J": {
        "plan": "PAYG",
        "concurrency": 80,
        "fp_slot": 40,
        "tier": 1,
        "default_state": "flaky",
    },
    "K": {
        "plan": "PAYG",
        "concurrency": 40,
        "fp_slot": 20,
        "tier": 3,
        "default_state": "rate_limited",
    },
    "L": {"plan": "PAYG", "concurrency": 60, "fp_slot": 30, "tier": 2},
}


def ports_for_group(g: str) -> List[int]:
    """group A → 19080-19084, group B → 19085-19089, ... 每组 5 实例。"""
    gidx = GROUPS.index(g)
    return [BASE_PORT + gidx * 5 + i for i in range(5)]


def port_to_group(port: int) -> str:
    idx = (port - BASE_PORT) // 5
    if 0 <= idx < len(GROUPS):
        return GROUPS[idx]
    return "?"


async def post_state(session, port: int, state: str, timeout: int = 3):
    url = f"http://127.0.0.1:{port}/admin/state"
    try:
        async with session.post(url, json={"state": state}, timeout=timeout) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def post_quota(session, port: int, tokens: int, window_sec: int):
    url = f"http://127.0.0.1:{port}/admin/quota"
    try:
        async with session.post(
            url, json={"tokens": tokens, "window_sec": window_sec}, timeout=3
        ) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def get_health(session, port: int):
    url = f"http://127.0.0.1:{port}/healthz"
    try:
        async with session.get(url, timeout=2) as resp:
            return await resp.json()
    except Exception as e:
        return {"port": port, "ok": False, "error": str(e)[:80]}


async def cmd_health_matrix(args):
    """打印 12 组 × 5 实例的实时状态矩阵。"""
    all_ports = []
    for g in GROUPS:
        for p in ports_for_group(g):
            all_ports.append(p)

    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(*[get_health(session, p) for p in all_ports])

    by_group = {g: [] for g in GROUPS}
    for r in results:
        g = (
            port_to_group(r.get("port", 0))
            if "port" in r
            else port_to_group(results.index(r) + BASE_PORT)
        )
        # Better: pass back tuple
        by_group[g].append(r)

    print("\n┌─ Health Matrix ─────────────────────────────────────────────────────┐")
    for g in GROUPS:
        default = GROUP_PROFILE_DEFAULT[g]
        default_state = default.get("default_state", "healthy")
        plan = default["plan"]
        conc = default["concurrency"]
        fp = default["fp_slot"]
        tier = default["tier"]
        line = f"  {g} [{plan:9s}] C={conc:3d} fps={fp:2d} tier={tier} (default={default_state}): "
        cells = []
        for h in by_group[g]:
            state = h.get("state", "down") if h.get("ok") else "DOWN"
            cnt = f"{h.get('requests_total', 0)}"
            cells.append(f"{state}({cnt})")
        line += " | ".join(cells)
        print(line)
    print("└────────────────────────────────────────────────────────────────────┘\n")


async def cmd_set_group(args):
    if args.group not in GROUPS:
        print(f"unknown group: {args.group}", file=sys.stderr)
        sys.exit(1)
    ports = ports_for_group(args.group)
    print(f"set-group {args.group} -> {args.state}  (ports: {ports[0]}-{ports[-1]})")
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_state(session, p, args.state) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"  ok: {n_ok}/{len(ports)}")


async def cmd_reset_group(args):
    """重置到 GROUP_PROFILE_DEFAULT（含 default_state）。"""
    if args.group not in GROUPS:
        print(f"unknown group: {args.group}", file=sys.stderr)
        sys.exit(1)
    default_state = GROUP_PROFILE_DEFAULT[args.group].get("default_state", "healthy")
    ports = ports_for_group(args.group)
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_state(session, p, default_state) for p in ports]
        )
    print(
        f"reset {args.group} → {default_state}: "
        f"{sum(1 for r in results if r.get('ok'))}/{len(ports)} ok"
    )


async def cmd_reset_all(args):
    # 2026-07-15: BUG FIX — `reset-all` should reset every group to a healthy
    # baseline, not to each group's `default_state`. Multiple groups (G, J, K)
    # ship with non-healthy defaults (slow / flaky / rate_limited) which would
    # poison the baseline run S01 and any other scenario that calls
    # `reset_all_suppliers()` (S02, S03, S07, S08, S09, S10, S12, S15).
    # The per-group `default_state` is still reachable via `reset-group G`.
    healthy_state = "healthy"
    ports_all: list[int] = []
    for g in GROUPS:
        ports_all.extend(ports_for_group(g))
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_state(session, p, healthy_state) for p in ports_all]
        )
    print("\nreset-all (every group forced to healthy):")
    print(f"  ok: {sum(1 for r in results if r.get('ok'))}/{len(ports_all)}")
    print("\nreset-all: done")


async def cmd_set_quota(args):
    """单实例配额：set-quota <port> <tokens> <window_sec>"""
    async with aiohttp.ClientSession() as session:
        r = await post_quota(session, args.port, args.tokens, args.window_sec)
        print(json.dumps(r, indent=2))


async def cmd_set_group_quota(args):
    """整组配额：set-group-quota <group> <tokens> <window_sec>"""
    ports = ports_for_group(args.group)
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_quota(session, p, args.tokens, args.window_sec) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(
        f"set-group-quota {args.group} tokens={args.tokens} window={args.window_sec}s: "
        f"{n_ok}/{len(ports)} ok"
    )


async def cmd_set(args):
    """set <port> <state>"""
    async with aiohttp.ClientSession() as session:
        r = await post_state(session, args.port, args.state)
        print(json.dumps(r, indent=2))


def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="cmd", required=True)

    sub.add_parser("health-matrix", help="打印 60 实例状态矩阵")

    s = sub.add_parser("set-group")
    s.add_argument("group")
    s.add_argument("state")

    r = sub.add_parser("reset-group")
    r.add_argument("group")

    sub.add_parser("reset-all")

    q = sub.add_parser("set-quota")
    q.add_argument("port", type=int)
    q.add_argument("tokens", type=int)
    q.add_argument("window_sec", type=int)

    gq = sub.add_parser("set-group-quota")
    gq.add_argument("group")
    gq.add_argument("tokens", type=int)
    gq.add_argument("window_sec", type=int)

    p = sub.add_parser("set")
    p.add_argument("port", type=int)
    p.add_argument("state")

    args = parser.parse_args()
    handler = {
        "health-matrix": cmd_health_matrix,
        "set-group": cmd_set_group,
        "reset-group": cmd_reset_group,
        "reset-all": cmd_reset_all,
        "set-quota": cmd_set_quota,
        "set-group-quota": cmd_set_group_quota,
        "set": cmd_set,
    }[args.cmd]
    asyncio.run(handler(args))


if __name__ == "__main__":
    main()
