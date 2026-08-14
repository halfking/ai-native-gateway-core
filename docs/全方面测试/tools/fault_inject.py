#!/usr/bin/env python3
# docs/全方面测试/tools/fault_inject.py
#
# 2026-08-14: 独立的供应商闪断调度器 — 给 S24-S29 提供 timeline-based fault
# injection。脚本本身不跑任何 LLM 请求,只定时 kill/restart mock_supplier 进程
# (通过 mock_orchestrator.py + pkill),让 S## 脚本专心做业务断言。
#
# 设计原则 (rule 37 简洁优先):
#   1. 时间表 JSON 直白,不引入调度库
#   2. 时间表每一项: {"at": <sec_offset>, "action": "kill"|"restart",
#                     "group": "<A-L>"|"all", "kill_after_sec": 1}
#   3. 由 S## 脚本传 --schedule @file.json, fault_inject 启动后按时间表逐项执行
#   4. kill 用 mock_orchestrator kill-group;restart 调用 start_suppliers.sh(只重启
#      指定 group 的 5 个实例,避免破坏其它 group)
#
# 用法:
#   # 时间表样例 (schedule.json):
#   # [
#   #   {"at": 3,  "action": "kill", "group": "G"},
#   #   {"at": 9,  "action": "kill", "group": "B"},
#   #   {"at": 30, "action": "restart", "group": "G"},
#   #   {"at": 35, "action": "restart", "group": "B"}
#   # ]
#
#   fault_inject.py --schedule schedule.json --duration 60
#
# 输出:
#   JSON 行到 stdout (每项执行后一行),便于 S## 脚本解析
#   也可加 --quiet 抑制常规输出
#
# 反模式(rule 37 §1.4):
#   - 不预测结果: 不假设 "kill G 后 P95 会升高",只看实际指标
#   - 简洁优先: 不引入 apscheduler / celery,asyncio + sleep 就够了
#   - 精准修改: 只动指定 group, 不误杀 others

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path


HERE = Path(__file__).resolve().parent
START_SCRIPT = HERE / "start_suppliers.sh"
ORCHESTRATOR = HERE / "mock_orchestrator.py"


def log(msg: str, quiet: bool = False) -> None:
    if not quiet:
        print(msg, file=sys.stderr, flush=True)


def run(cmd: list[str], check: bool = True) -> tuple[int, str, str]:
    """Run a subprocess; return (rc, stdout, stderr)."""
    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=30, check=check
        )
        return proc.returncode, proc.stdout, proc.stderr
    except subprocess.CalledProcessError as e:
        return e.returncode, e.stdout or "", e.stderr or ""
    except subprocess.TimeoutExpired:
        return 124, "", "timeout"


def kill_group(group: str, kill_after_sec: int = 1, quiet: bool = False) -> dict:
    """Schedule a group to self-terminate."""
    rc, out, err = run(
        [
            sys.executable,
            str(ORCHESTRATOR),
            "kill-group",
            group,
            str(kill_after_sec),
        ],
        check=False,
    )
    log(
        f"  fault_inject: kill-group {group} (after={kill_after_sec}s) → rc={rc}", quiet
    )
    return {"action": "kill", "group": group, "rc": rc, "stderr": err.strip()[:200]}


def restart_group(group: str, quiet: bool = False) -> dict:
    """Restart the 5 instances of a single group via start_suppliers.sh logic.

    start_suppliers.sh only knows how to start all 60; we replicate just the
    per-group inner loop to avoid restarting the world (which would also
    reset the gateway's probe-worker caches and break scenario continuity).
    """
    base_port = 19080
    group_offsets = {
        "A": 0,
        "B": 5,
        "C": 10,
        "D": 15,
        "E": 20,
        "F": 25,
        "G": 30,
        "H": 35,
        "I": 40,
        "J": 45,
        "K": 50,
        "L": 55,
    }
    if group not in group_offsets:
        return {"action": "restart", "group": group, "rc": 1, "stderr": "unknown group"}
    base = group_offsets[group]
    pids_dir = Path("/tmp/lab-suppliers")
    pids_dir.mkdir(exist_ok=True)
    started = 0
    for inst in range(5):
        port = base_port + base + inst
        pid_file = pids_dir / f"{group}-{inst}.pid"
        log_file = pids_dir / f"{group}-{inst}.log"
        # kill any stale process on that port (mock_orchestrator kill-group already
        # scheduled os._exit, but if a previous run was sloppy this is a safety net)
        try:
            subprocess.run(
                ["pkill", "-f", f"mock_supplier.py --port {port}"],
                check=False,
                timeout=5,
            )
        except subprocess.TimeoutExpired:
            pass
        # launch a fresh one
        proc = subprocess.Popen(
            [
                sys.executable,
                str(HERE / "mock_supplier.py"),
                "--port",
                str(port),
                "--group",
                group,
                "--instance",
                str(inst),
            ],
            stdout=open(log_file, "wb"),
            stderr=subprocess.STDOUT,
            cwd=str(HERE),
        )
        pid_file.write_text(str(proc.pid))
        started += 1
    log(f"  fault_inject: restart-group {group} → {started} instances", quiet)
    return {
        "action": "restart",
        "group": group,
        "rc": 0,
        "started": started,
    }


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Timeline-based supplier fault injector"
    )
    parser.add_argument("--schedule", required=True, help="JSON schedule file path")
    parser.add_argument(
        "--duration",
        type=int,
        default=120,
        help="total run duration in seconds (safety cap)",
    )
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()

    sched_path = Path(args.schedule)
    if not sched_path.is_file():
        print(f"fault_inject: schedule not found: {sched_path}", file=sys.stderr)
        return 2

    try:
        schedule = json.loads(sched_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        print(f"fault_inject: invalid JSON in schedule: {e}", file=sys.stderr)
        return 2
    if not isinstance(schedule, list):
        print("fault_inject: schedule must be a JSON array", file=sys.stderr)
        return 2

    log(
        f"fault_inject: {len(schedule)} events, duration={args.duration}s, "
        f"schedule={sched_path}",
        args.quiet,
    )

    started_at = time.time()
    events = sorted(schedule, key=lambda e: e.get("at", 0))
    failed_events = 0
    for index, ev in enumerate(events):
        at = float(ev.get("at", 0))
        action = ev.get("action", "")
        group = ev.get("group", "")
        kas = int(ev.get("kill_after_sec", 1))
        # sleep until scheduled time
        target = started_at + at
        delay = target - time.time()
        if delay > 0:
            time.sleep(delay)
        if time.time() - started_at > args.duration:
            log(
                f"fault_inject: duration cap {args.duration}s reached, "
                f"skipping remaining {len(events) - index} events",
                args.quiet,
            )
            break
        if action == "kill":
            result = kill_group(group, kas, args.quiet)
        elif action == "restart":
            result = restart_group(group, args.quiet)
        else:
            result = {
                "action": action,
                "group": group,
                "rc": 1,
                "stderr": f"unknown action: {action}",
            }
        result["at_sec"] = round(time.time() - started_at, 3)
        if result["rc"] != 0:
            failed_events += 1
        # emit a JSON line to stdout for the calling S## script to parse
        print(json.dumps(result), flush=True)

    log(f"fault_inject: schedule complete (failed_events={failed_events})", args.quiet)
    return 1 if failed_events else 0


if __name__ == "__main__":
    sys.exit(main())
