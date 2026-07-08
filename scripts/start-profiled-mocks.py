#!/usr/bin/env python3
"""
Start 60 profiled mock instances (v3) with per-group configuration.

Reads provider-profiles.json, writes a MOCK_PROFILE_FILE for each mock,
and launches server-v3.py for each port.

Usage:
  python3 start-profiled-mocks.py [--restart] [--clean]
  --restart: kill existing mocks first
  --clean:   kill mocks and exit (no start)
"""

import json
import os
import signal
import subprocess
import sys
import time
import urllib.request

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROFILES_FILE = os.path.join(SCRIPT_DIR, "profiles", "provider-profiles.json")
MOCK_DIR = os.path.join(SCRIPT_DIR, "mocks", "llm-mock-upstream")
SERVER_V3 = os.path.join(MOCK_DIR, "server-v3.py")
PROFILE_DIR = "/tmp/mock-profiles"

with open(PROFILES_FILE) as f:
    CONFIG = json.load(f)

PROFILES = CONFIG["profiles"]
GROUP_PORTS = CONFIG["group_port_map"]


def kill_existing():
    """Kill all running server-v3.py and server-v2.py instances."""
    print("[start-mocks] Killing existing mock instances...")
    subprocess.run(["pkill", "-f", "server-v3.py"], capture_output=True)
    subprocess.run(["pkill", "-f", "server-v2.py"], capture_output=True)
    time.sleep(2)


def wait_health(port, timeout=5):
    """Check if mock on port is healthy."""
    try:
        url = f"http://localhost:{port}/healthz"
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            return resp.status == 200
    except Exception:
        return False


def start_mocks():
    os.makedirs(PROFILE_DIR, exist_ok=True)
    started = 0
    skipped = 0
    failed = 0

    for group_key, profile in PROFILES.items():
        group = profile["group"]
        ports = GROUP_PORTS[group]
        mock_profile = profile.get("mock_profile", {})
        instances = profile["instances"]

        for idx in range(instances):
            port = ports[idx]
            token = f"mock-{sum(len(GROUP_PORTS[g]) for g in list(GROUP_PORTS.keys())[:list(GROUP_PORTS.keys()).index(group)]) + idx:02d}"

            # Check if already running
            if wait_health(port, timeout=1):
                skipped += 1
                continue

            # Write profile file
            profile_file = os.path.join(PROFILE_DIR, f"mock-{port}.json")
            with open(profile_file, "w") as f:
                json.dump(mock_profile, f)

            # Start process
            env = os.environ.copy()
            env["MOCK_PORT"] = str(port)
            env["MOCK_TOKEN"] = token
            env["MOCK_PROFILE_FILE"] = profile_file
            env["MOCK_STATE_FILE"] = f"/tmp/mock-state-v3-{port}.json"

            log_file = open(f"/tmp/mock-{port}.log", "w")
            proc = subprocess.Popen(
                ["python3", SERVER_V3],
                env=env,
                stdout=log_file,
                stderr=subprocess.STDOUT,
                cwd=MOCK_DIR,
            )
            with open(f"/tmp/mock-{port}.pid", "w") as f:
                f.write(str(proc.pid))
            started += 1

    # Wait for startup
    print(f"[start-mocks] Started {started}, skipped {skipped}. Waiting for health...")
    time.sleep(4)

    # Health check
    healthy = 0
    unhealthy = []
    for group_key, profile in PROFILES.items():
        for port in GROUP_PORTS[profile["group"]]:
            if wait_health(port, timeout=2):
                healthy += 1
            else:
                unhealthy.append(port)

    print(f"[start-mocks] Health: {healthy}/60 healthy")
    if unhealthy:
        print(f"[start-mocks] Unhealthy ports: {unhealthy[:10]}...")
    return healthy


def health_matrix():
    """Print health status per group."""
    print("\n=== Health Matrix ===")
    for group_key, profile in PROFILES.items():
        group = profile["group"]
        ports = GROUP_PORTS[group]
        states = []
        for port in ports:
            try:
                url = f"http://localhost:{port}/healthz"
                with urllib.request.urlopen(url, timeout=2) as resp:
                    data = json.loads(resp.read())
                    states.append(f"✓{data.get('mode', '?')[:4]}")
            except Exception:
                states.append("✗")
        print(f"  {group} ({profile['name']:16s}): {' '.join(states)}")
    print()


if __name__ == "__main__":
    clean_only = "--clean" in sys.argv
    restart = "--restart" in sys.argv or clean_only

    if restart:
        kill_existing()

    if clean_only:
        print("[start-mocks] Clean complete, exiting.")
        sys.exit(0)

    healthy = start_mocks()
    if healthy >= 30:
        health_matrix()
        print(f"[start-mocks] ✓ {healthy}/60 mocks ready")
    else:
        print(f"[start-mocks] ✗ Only {healthy}/60 healthy, check /tmp/mock-*.log")
        sys.exit(1)
