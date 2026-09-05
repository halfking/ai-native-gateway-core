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


async def post_profile(session, port: int, profile: dict, timeout: int = 3):
    """Set latency_ms_extra, latency_prob, fail_rate via /admin/profile"""
    url = f"http://127.0.0.1:{port}/admin/profile"
    try:
        async with session.post(url, json=profile, timeout=timeout) as resp:
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


async def post_protocol(session, port: int, protocol: str, timeout: int = 3):
    """Set protocol_mode via /admin/protocol"""
    url = f"http://127.0.0.1:{port}/admin/protocol"
    try:
        async with session.post(
            url, json={"protocol": protocol}, timeout=timeout
        ) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def post_delay(session, port: int, delay_ms: int, timeout: int = 3):
    """Set processing_delay_ms via /admin/delay"""
    url = f"http://127.0.0.1:{port}/admin/delay"
    try:
        async with session.post(
            url, json={"delay_ms": delay_ms}, timeout=timeout
        ) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def post_connlimit(session, port: int, limit: int, timeout: int = 3):
    """Set max_connections via /admin/connlimit"""
    url = f"http://127.0.0.1:{port}/admin/connlimit"
    try:
        async with session.post(url, json={"limit": limit}, timeout=timeout) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def post_fault_mode(session, port: int, fault_mode: dict, timeout: int = 3):
    """Set edge fault flags via /admin/fault-mode."""
    url = f"http://127.0.0.1:{port}/admin/fault-mode"
    try:
        async with session.post(url, json=fault_mode, timeout=timeout) as resp:
            return await resp.json()
    except Exception as e:
        return {"error": str(e)}


async def post_kill_after(session, port: int, kill_after_sec: int, timeout: int = 3):
    """2026-08-14: ask supplier to self-terminate N seconds from now."""
    url = f"http://127.0.0.1:{port}/admin/kill-after"
    try:
        async with session.post(
            url, json={"kill_after_sec": int(kill_after_sec)}, timeout=timeout
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
    if n_ok != len(ports):
        raise SystemExit(1)


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
    if sum(1 for r in results if r.get("ok")) != len(ports_all):
        raise SystemExit(1)
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
    if n_ok != len(ports):
        raise SystemExit(1)


async def cmd_set(args):
    """set <port> <state>"""
    async with aiohttp.ClientSession() as session:
        r = await post_state(session, args.port, args.state)
        print(json.dumps(r, indent=2))


async def cmd_set_protocol(args):
    """set-protocol <group|port> <chat|response|anthropic>"""
    ports = (
        ports_for_group(args.target) if args.target in GROUPS else [int(args.target)]
    )
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_protocol(session, p, args.protocol) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"set-protocol {args.target} -> {args.protocol}: {n_ok}/{len(ports)} ok")


async def cmd_set_delay(args):
    """set-delay <group|port> <ms>"""
    ports = (
        ports_for_group(args.target) if args.target in GROUPS else [int(args.target)]
    )
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_delay(session, p, int(args.ms)) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"set-delay {args.target} -> {args.ms}ms: {n_ok}/{len(ports)} ok")


async def cmd_set_connlimit(args):
    """set-connlimit <group|port> <limit>"""
    ports = (
        ports_for_group(args.target) if args.target in GROUPS else [int(args.target)]
    )
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_connlimit(session, p, int(args.limit)) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"set-connlimit {args.target} -> {args.limit}: {n_ok}/{len(ports)} ok")


async def cmd_set_fault(args):
    """set-fault <group|port> <fault> [value]"""
    ports = (
        ports_for_group(args.target) if args.target in GROUPS else [int(args.target)]
    )
    value = args.value.lower() in ("1", "true", "yes", "on")
    if args.fault in ("slow_connect_delay_ms", "slow_header_delay_ms"):
        payload = {args.fault: int(args.value)}
    else:
        payload = {args.fault: value}
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_fault_mode(session, p, payload) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"set-fault {args.target} {payload}: {n_ok}/{len(ports)} ok")


async def cmd_set_group_profile(args):
    """set-group-profile <group> <latency_ms> <latency_prob> <fail_rate>"""
    ports = ports_for_group(args.group)
    profile = {
        "latency_ms_extra": int(args.latency_ms),
        "latency_prob": float(args.latency_prob),
        "fail_rate": float(args.fail_rate),
    }
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_profile(session, p, profile) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(f"set-group-profile {args.group} {profile}: {n_ok}/{len(ports)} ok")


async def cmd_kill_group(args):
    """2026-08-14: kill-group <group> [kill_after_sec]

    Without kill_after_sec: schedule every instance in the group to
    self-terminate in 1 second (immediate flash-disconnect).
    With kill_after_sec: schedule self-termination in that many seconds.
    """
    ports = ports_for_group(args.group)
    kas = int(args.kill_after_sec) if args.kill_after_sec is not None else 1
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_kill_after(session, p, kas) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(
        f"kill-group {args.group} kill_after_sec={kas}: {n_ok}/{len(ports)} scheduled "
        f"(suppliers will os._exit)"
    )
    if n_ok != len(ports):
        raise SystemExit(1)


async def cmd_set_group_disconnect_after(args):
    """2026-08-14: set-group-disconnect-after <group> <ms>

    Sets each instance in the group to abort its TCP connection `ms` ms after
    the first response byte. Used to model mid-flight upstream RST. One-shot:
    after the first disconnect fires, the flag self-resets (state stays).
    """
    ports = ports_for_group(args.group)
    payload = {"disconnect_after_ms": int(args.ms)}
    async with aiohttp.ClientSession() as session:
        results = await asyncio.gather(
            *[post_fault_mode(session, p, payload) for p in ports]
        )
    n_ok = sum(1 for r in results if r.get("ok"))
    print(
        f"set-group-disconnect-after {args.group} {args.ms}ms: {n_ok}/{len(ports)} ok"
    )


async def cmd_chaos_scenario(args):
    """chaos-scenario <name> — 预设混沌场景"""
    scenarios = {
        "slowdown": {
            "description": "50%供应商降速到2-4s",
            "action": lambda: None,  # placeholder
        },
        "flapping": {
            "description": "30%供应商flaky",
            "action": lambda: None,
        },
        "mixed": {
            "description": "混合故障：20%慢+20%错误+10%配额",
            "action": lambda: None,
        },
    }
    if args.name not in scenarios:
        print(f"可用场景: {', '.join(scenarios.keys())}", file=sys.stderr)
        sys.exit(1)
    print(f"chaos-scenario {args.name}: {scenarios[args.name]['description']}")


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

    # Protocol mode control
    sp = sub.add_parser("set-protocol")
    sp.add_argument("target", help="group (A-L) or port number")
    sp.add_argument("protocol", choices=["chat", "response", "anthropic"])

    # Processing delay control
    sd = sub.add_parser("set-delay")
    sd.add_argument("target", help="group (A-L) or port number")
    sd.add_argument("ms", help="processing delay in milliseconds")

    # Connection limit control
    sc = sub.add_parser("set-connlimit")
    sc.add_argument("target", help="group (A-L) or port number")
    sc.add_argument("limit", help="max concurrent connections (0=unlimited)")

    # Fault mode control
    sf = sub.add_parser("set-fault")
    sf.add_argument("target", help="group (A-L) or port number")
    sf.add_argument(
        "fault",
        choices=[
            "slow_connect_delay_ms",
            "timeout_response",
            "huge_response",
            "truncated_response",
            "slow_header_delay_ms",
            "invalid_json_response",
        ],
    )
    sf.add_argument("value", help="boolean value, or delay milliseconds")

    # Group profile (latency/fail-rate)
    sgp = sub.add_parser("set-group-profile")
    sgp.add_argument("group", choices=GROUPS)
    sgp.add_argument("latency_ms", help="extra latency in ms")
    sgp.add_argument("latency_prob", help="probability of extra latency (0-1)")
    sgp.add_argument("fail_rate", help="random failure rate (0-1)")

    # 2026-08-14: flash-disconnect control (S24-S29)
    kg = sub.add_parser("kill-group", help="schedule supplier group self-termination")
    kg.add_argument("group", choices=GROUPS)
    kg.add_argument(
        "kill_after_sec",
        nargs="?",
        type=int,
        default=None,
        help="seconds until self-terminate (default 1s)",
    )
    sgda = sub.add_parser(
        "set-group-disconnect-after",
        help="set each instance in group to abort mid-response after N ms",
    )
    sgda.add_argument("group", choices=GROUPS)
    sgda.add_argument("ms", type=int, help="milliseconds after first byte")

    # Chaos scenario presets
    cs = sub.add_parser("chaos-scenario")
    cs.add_argument(
        "name",
        choices=[
            "slowdown",  # 50% suppliers slow
            "flapping",  # 30% suppliers flaky
            "mixed",  # mixed faults (slow + error + quota)
            "network_chaos",  # network latency variation
            "extreme_load",  # extreme load simulation
            "reset",  # reset all to healthy
        ],
    )

    args = parser.parse_args()

    # Chaos scenario dispatcher
    if args.cmd == "chaos-scenario":
        if args.name == "reset":
            # Reset all to healthy
            args.cmd = "reset-all"
        elif args.name == "slowdown":
            # 50% groups go slow (C, E, G, I, K)
            async def run_slowdown():
                for g in ["C", "E", "G", "I", "K"]:
                    ports = ports_for_group(g)
                    async with aiohttp.ClientSession() as session:
                        await asyncio.gather(
                            *[
                                post_profile(
                                    session,
                                    p,
                                    {"latency_ms_extra": 2000, "latency_prob": 0.8},
                                )
                                for p in ports
                            ]
                        )
                    print(f"  {g} -> slow (2s, 80%)")

            asyncio.run(run_slowdown())
            return
        elif args.name == "flapping":
            # 30% groups go flaky (D, F, H, J, L)
            async def run_flapping():
                for g in ["D", "F", "H", "J", "L"]:
                    ports = ports_for_group(g)
                    async with aiohttp.ClientSession() as session:
                        await asyncio.gather(
                            *[
                                post_profile(session, p, {"fail_rate": 0.3})
                                for p in ports
                            ]
                        )
                    print(f"  {g} -> flaky (30%)")

            asyncio.run(run_flapping())
            return
        elif args.name == "mixed":

            async def run_mixed():
                # 20% slow + 20% error + 10% quota
                async with aiohttp.ClientSession() as session:
                    # C: slow
                    for p in ports_for_group("C"):
                        await post_profile(
                            session, p, {"latency_ms_extra": 2000, "latency_prob": 0.8}
                        )
                    print("  C -> slow")
                    # E: error
                    for p in ports_for_group("E"):
                        await post_state(session, p, "server_error")
                    print("  E -> server_error")
                    # G: quota
                    for p in ports_for_group("G"):
                        await post_quota(session, p, 100, 60)
                    print("  G -> quota_429 (100 tokens/min)")

            asyncio.run(run_mixed())
            return
        elif args.name == "network_chaos":
            # Simulate network latency variation
            async def run_network_chaos():
                import random

                for g in ["A", "B", "C", "D", "E"]:
                    ports = ports_for_group(g)
                    async with aiohttp.ClientSession() as session:
                        for p in ports:
                            # Random latency between 100ms and 2000ms
                            latency = random.randint(100, 2000)
                            await post_profile(
                                session,
                                p,
                                {"latency_ms_extra": latency, "latency_prob": 0.5},
                            )
                    print(f"  {g} -> network_chaos (100-2000ms)")

            asyncio.run(run_network_chaos())
            return
        elif args.name == "extreme_load":
            # Simulate extreme load with very low latency variation
            async def run_extreme_load():
                for g in ["A", "B", "C"]:
                    ports = ports_for_group(g)
                    async with aiohttp.ClientSession() as session:
                        await asyncio.gather(
                            *[
                                post_profile(
                                    session,
                                    p,
                                    {
                                        "latency_ms_extra": 500,
                                        "latency_prob": 0.3,
                                        "fail_rate": 0.05,
                                    },
                                )
                                for p in ports
                            ]
                        )
                    print(f"  {g} -> extreme_load (500ms, 30%, 5% fail)")

            asyncio.run(run_extreme_load())
            return

    handler = {
        "health-matrix": cmd_health_matrix,
        "set-group": cmd_set_group,
        "reset-group": cmd_reset_group,
        "reset-all": cmd_reset_all,
        "set-quota": cmd_set_quota,
        "set-group-quota": cmd_set_group_quota,
        "set": cmd_set,
        "set-protocol": cmd_set_protocol,
        "set-delay": cmd_set_delay,
        "set-connlimit": cmd_set_connlimit,
        "set-fault": cmd_set_fault,
        "set-group-profile": cmd_set_group_profile,
        "kill-group": cmd_kill_group,
        "set-group-disconnect-after": cmd_set_group_disconnect_after,
    }[args.cmd]
    asyncio.run(handler(args))


if __name__ == "__main__":
    main()
