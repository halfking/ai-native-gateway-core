#!/usr/bin/env python3
"""
Mock orchestrator v2 — group-aware profile management.

Commands:
  health-matrix         — Show 12-group health status
  profile-all           — Show all mock profiles
  set-group <G> <mode>  — Set all mocks in group G to a mode
  reset-group <G>       — Reset group to its profile defaults
  set-concurrency <port> <limit> [overflow]  — Dynamic concurrency change
  set-quota <port> <total> <window_seconds>   — Dynamic quota change
  reset-quota-all       — Reset all quota windows
  disable-group <G>     — Disable group at DB level (providers.manual_disabled)
  enable-group <G>      — Re-enable group
"""

import json
import os
import sys
import time
import urllib.request

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROFILES_FILE = os.path.join(SCRIPT_DIR, "profiles", "provider-profiles.json")

with open(PROFILES_FILE) as f:
    CONFIG = json.load(f)

GROUP_PORTS = CONFIG["group_port_map"]
PROFILES = CONFIG["profiles"]

# Reverse: group letter → profile key
GROUP_PROFILE = {}
for key, prof in PROFILES.items():
    GROUP_PROFILE[prof["group"]] = (key, prof)


def http_json(method, url, data=None, timeout=3):
    try:
        if data:
            req = urllib.request.Request(url, data=json.dumps(data).encode(),
                                          headers={"Content-Type": "application/json"}, method=method)
        else:
            req = urllib.request.Request(url, method=method)
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read())
    except Exception as e:
        return {"error": str(e)}


def cmd_health_matrix():
    print("Group  Profile                    Status")
    print("─────  ────────────────────────  ─────────────")
    for group in sorted(GROUP_PORTS.keys()):
        ports = GROUP_PORTS[group]
        key, prof = GROUP_PROFILE[group]
        states = []
        for port in ports:
            r = http_json("GET", f"http://localhost:{port}/healthz")
            if "error" in r:
                states.append("✗")
            else:
                states.append(f"✓{r.get('mode', '?')[:4]}")
        print(f"  {group}    {prof['name']:28s} {' '.join(states)}")


def cmd_profile_all():
    for group in sorted(GROUP_PORTS.keys()):
        ports = GROUP_PORTS[group]
        key, prof = GROUP_PROFILE[group]
        print(f"\n=== Group {group}: {prof['name']} ===")
        for port in ports:
            r = http_json("GET", f"http://localhost:{port}/admin/profile")
            if "error" in r:
                print(f"  port {port}: ✗ {r['error']}")
            else:
                print(f"  port {port}: mode={r.get('mode','?')} conc={r.get('concurrency_limit',0)} "
                      f"quota={r.get('token_quota_used',0)}/{r.get('token_quota_total',0)} "
                      f"rpm={r.get('rpm_limit',0)}")


def cmd_set_group(group, mode, ttl=0):
    if group not in GROUP_PORTS:
        print(f"Unknown group: {group}. Valid: {list(GROUP_PORTS.keys())}")
        return
    ports = GROUP_PORTS[group]
    for port in ports:
        r = http_json("POST", f"http://localhost:{port}/admin/state", {"mode": mode, "ttl_seconds": ttl})
        if "error" in r:
            print(f"  port {port}: ✗ {r['error']}")
        else:
            print(f"  port {port}: ✓ → {mode}")


def cmd_reset_group(group):
    if group not in GROUP_PORTS:
        print(f"Unknown group: {group}")
        return
    key, prof = GROUP_PROFILE[group]
    mock_profile = prof.get("mock_profile", {"mode": "healthy"})
    ports = GROUP_PORTS[group]
    for port in ports:
        r = http_json("POST", f"http://localhost:{port}/admin/profile", mock_profile)
        print(f"  port {port}: {'✓' if 'error' not in r else '✗'} reset")


def cmd_set_concurrency(port, limit, overflow="reject"):
    r = http_json("POST", f"http://localhost:{port}/admin/profile",
                  {"concurrency_limit": limit, "concurrency_overflow": overflow})
    print(f"port {port}: {'✓' if 'error' not in r else '✗'} conc={limit} overflow={overflow}")


def cmd_set_quota(port, total, window_seconds):
    r = http_json("POST", f"http://localhost:{port}/admin/profile",
                  {"token_quota_total": total, "token_quota_window_seconds": window_seconds,
                   "token_quota_used": 0})
    print(f"port {port}: {'✓' if 'error' not in r else '✗'} quota={total} window={window_seconds}s")


def cmd_reset_quota_all():
    for group in GROUP_PORTS:
        for port in GROUP_PORTS[group]:
            http_json("POST", f"http://localhost:{port}/admin/quota/reset")
    print("✓ Reset all quota windows")


def cmd_disable_group(group):
    """Disable at DB level using psql."""
    if group not in GROUP_PORTS:
        print(f"Unknown group: {group}")
        return
    ports = GROUP_PORTS[group]
    # Find provider IDs from port mapping: port 19080+N → provider_id 9010+N
    for port in ports:
        pid = 9010 + (port - 19080)
        os.system(f"psql -d llm_gateway -c \"UPDATE providers SET manual_disabled = true WHERE id = {pid};\" > /dev/null 2>&1")
    print(f"✓ Disabled group {group} ({len(ports)} providers)")


def cmd_enable_group(group):
    if group not in GROUP_PORTS:
        print(f"Unknown group: {group}")
        return
    ports = GROUP_PORTS[group]
    for port in ports:
        pid = 9010 + (port - 19080)
        os.system(f"psql -d llm_gateway -c \"UPDATE providers SET manual_disabled = false WHERE id = {pid};\" > /dev/null 2>&1")
    print(f"✓ Enabled group {group} ({len(ports)} providers)")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(1)

    cmd = sys.argv[1]
    if cmd == "health-matrix":
        cmd_health_matrix()
    elif cmd == "profile-all":
        cmd_profile_all()
    elif cmd == "set-group":
        cmd_set_group(sys.argv[2], sys.argv[3], int(sys.argv[4]) if len(sys.argv) > 4 else 0)
    elif cmd == "reset-group":
        cmd_reset_group(sys.argv[2])
    elif cmd == "set-concurrency":
        cmd_set_concurrency(int(sys.argv[2]), int(sys.argv[3]), sys.argv[4] if len(sys.argv) > 4 else "reject")
    elif cmd == "set-quota":
        cmd_set_quota(int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]))
    elif cmd == "reset-quota-all":
        cmd_reset_quota_all()
    elif cmd == "disable-group":
        cmd_disable_group(sys.argv[2])
    elif cmd == "enable-group":
        cmd_enable_group(sys.argv[2])
    else:
        print(f"Unknown command: {cmd}")
        print(__doc__)
