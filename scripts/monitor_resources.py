#!/usr/bin/env python3
"""
monitor_resources.py - 监控 gateway 进程的资源使用情况

每 0.5 秒采样：
- VmRSS (resident memory)
- VmSize (virtual memory)
- File descriptor count
- OS thread count
- Optional Go goroutine count from an explicitly enabled pprof endpoint
- RSS delta (是否内存泄漏)

Usage:
    python3 monitor_resources.py --pid <PID> --duration 120 --output monitor.csv
"""

import argparse
import csv
import os
import sys
import time
from datetime import datetime

import requests


def read_proc_status(pid):
    """读取 /proc/<pid>/status 中的关键指标。"""
    fields = {}
    try:
        with open(f"/proc/{pid}/status", "r") as f:
            for line in f:
                if ":" not in line:
                    continue
                parts = line.split(":", 1)
                if len(parts) != 2:
                    continue
                k, v = parts
                toks = v.strip().split()
                if not toks:
                    continue
                fields[k.strip()] = toks[0]
    except (FileNotFoundError, PermissionError):
        return None
    return fields


def count_fds(pid):
    """计算进程打开的文件描述符数量。"""
    fd_dir = f"/proc/{pid}/fd"
    try:
        return len(os.listdir(fd_dir))
    except (FileNotFoundError, PermissionError, OSError):
        return 0


def count_threads(pid):
    fd_dir = f"/proc/{pid}/task"
    try:
        return len(os.listdir(fd_dir))
    except (FileNotFoundError, PermissionError, OSError):
        return 0


def read_io(pid):
    """读取进程 IO 统计。"""
    io_path = f"/proc/{pid}/io"
    try:
        stats = {}
        with open(io_path, "r") as f:
            for line in f:
                k, v = line.strip().split(":")
                stats[k] = int(v)
        return stats
    except (FileNotFoundError, PermissionError):
        return {}


def read_goroutines(pprof_url):
    """Return the goroutine count only when the endpoint is explicitly exposed."""
    if not pprof_url:
        return None
    try:
        response = requests.get(pprof_url, params={"debug": "1"}, timeout=2)
        if response.status_code != 200:
            return None
        first_line = response.text.splitlines()[0]
        if "goroutine profile:" not in first_line:
            return None
        return int(first_line.split()[-1])
    except (requests.RequestException, IndexError, ValueError):
        return None


def monitor(pid, duration_s, interval_s, output_path, pprof_url):
    """主循环: 每 interval_s 秒采样一次。"""
    start = time.monotonic()
    samples = []
    fields = [
        "ts",
        "elapsed_s",
        "vm_rss_kb",
        "vm_size_kb",
        "fd_count",
        "thread_count",
        "goroutine_count",
        "rchar_bytes",
        "wchar_bytes",
        "syscr",
        "syscw",
    ]
    print(f"[+] Monitoring PID {pid} for {duration_s}s, interval={interval_s}s")
    print(f"    Output: {output_path}")
    print()
    print(
        " " * 4 + "elapsed | vm_rss(MB) | fd_count | threads | goroutines | rchar(MB)"
    )
    print(" " * 4 + "-" * 72)

    try:
        with open(output_path, "w", newline="") as csvfile:
            writer = csv.DictWriter(csvfile, fieldnames=fields)
            writer.writeheader()

            while time.monotonic() - start < duration_s:
                now = time.monotonic()
                elapsed = now - start

                status = read_proc_status(pid)
                if status is None:
                    print(
                        f"  [!] PID {pid} not found / not accessible", file=sys.stderr
                    )
                    break

                vm_rss = int(status.get("VmRSS", 0))  # KB
                vm_size = int(status.get("VmSize", 0))  # KB
                threads = count_threads(pid)
                fds = count_fds(pid)
                io = read_io(pid)
                goroutines = read_goroutines(pprof_url)

                sample = {
                    "ts": datetime.now().isoformat(),
                    "elapsed_s": round(elapsed, 2),
                    "vm_rss_kb": vm_rss,
                    "vm_size_kb": vm_size,
                    "fd_count": fds,
                    "thread_count": threads,
                    "goroutine_count": goroutines if goroutines is not None else "",
                    "rchar_bytes": io.get("rchar", 0),
                    "wchar_bytes": io.get("wchar", 0),
                    "syscr": io.get("syscr", 0),
                    "syscw": io.get("syscw", 0),
                }
                samples.append(sample)
                writer.writerow(sample)
                csvfile.flush()

                if int(elapsed) % 5 == int(elapsed) or len(samples) <= 3:
                    print(
                        f"  {elapsed:6.1f}s |  {vm_rss / 1024:8.1f} |  {fds:>6}  |  {threads:>6}  | "
                        f"{str(goroutines if goroutines is not None else 'n/a'):>10} | "
                        f"{io.get('rchar', 0) / 1024 / 1024:8.1f}",
                        flush=True,
                    )

                time.sleep(interval_s)
    except KeyboardInterrupt:
        print("\n[!] Interrupted")

    return samples


def analyze(samples, pid):
    """输出资源使用分析。"""
    if not samples:
        print("[!] No samples to analyze")
        return

    rss_start = samples[0]["vm_rss_kb"]
    rss_end = samples[-1]["vm_rss_kb"]
    rss_max = max(s["vm_rss_kb"] for s in samples)
    rss_delta_pct = (rss_end - rss_start) / max(1, rss_start) * 100

    fd_start = samples[0]["fd_count"]
    fd_end = samples[-1]["fd_count"]
    fd_max = max(s["fd_count"] for s in samples)

    threads_start = samples[0]["thread_count"]
    threads_end = samples[-1]["thread_count"]
    threads_max = max(s["thread_count"] for s in samples)
    goroutine_samples = [
        s["goroutine_count"] for s in samples if s["goroutine_count"] != ""
    ]

    print()
    print("=" * 64)
    print(f"         RESOURCE MONITORING ANALYSIS (PID {pid})")
    print("=" * 64)
    print()
    print("RSS (resident memory):")
    print(f"  Start:    {rss_start / 1024:.1f} MB")
    print(f"  End:      {rss_end / 1024:.1f} MB")
    print(f"  Peak:     {rss_max / 1024:.1f} MB")
    print(f"  Delta:    {(rss_end - rss_start) / 1024:+.1f} MB ({rss_delta_pct:+.1f}%)")
    print(
        "  Interpretation: compare this post-load value after a cooldown/GC window; RSS alone is not proof of a leak."
    )
    print()
    print("File descriptors:")
    print(f"  Start:    {fd_start}")
    print(f"  End:      {fd_end}")
    print(f"  Peak:     {fd_max}")
    print(f"  Delta:    {fd_end - fd_start:+d}")
    print(
        "  Interpretation: persistent growth after load drains is suspicious; peak concurrency alone is not a leak."
    )
    print()
    print("Threads:")
    print(f"  Start:    {threads_start}")
    print(f"  End:      {threads_end}")
    print(f"  Peak:     {threads_max}")
    print(f"  Delta:    {threads_end - threads_start:+d}")
    print("  Note: OS threads are not Go goroutines.")
    if goroutine_samples:
        print()
        print("Go goroutines (pprof):")
        print(f"  Start:    {goroutine_samples[0]}")
        print(f"  End:      {goroutine_samples[-1]}")
        print(f"  Peak:     {max(goroutine_samples)}")
    else:
        print("  Go goroutines: not observed (pprof endpoint disabled or unavailable)")
    print()
    print("=" * 64)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--pid", type=int, required=True, help="PID of gateway process")
    parser.add_argument(
        "--duration", type=int, default=120, help="Total monitoring duration (s)"
    )
    parser.add_argument(
        "--interval", type=float, default=0.5, help="Sampling interval (s)"
    )
    parser.add_argument("--output", default="monitor.csv", help="CSV output path")
    parser.add_argument(
        "--pprof-url", default="", help="Optional /debug/pprof/goroutine endpoint"
    )
    args = parser.parse_args()

    samples = monitor(
        args.pid, args.duration, args.interval, args.output, args.pprof_url
    )
    analyze(samples, args.pid)
    print(f"\n[+] Results saved to {args.output}")


if __name__ == "__main__":
    main()
