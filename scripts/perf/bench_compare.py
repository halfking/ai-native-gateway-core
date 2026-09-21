#!/usr/bin/env python3
"""解析 `go test -bench` 输出，维护基准文件并检查回归。

用法（一般经由 scripts/perf/bench_compare.sh 调用）：
  bench_compare.py update --baseline <file> --input <bench输出> [--note ...]
  bench_compare.py check --baseline <file> --input <bench输出> [--thresholds <file>]

策略：
  - ns/op 默认容忍 15%，B/op 10%，allocs/op 2%（且绝对差 >=1 才判回归）；
    可用 --thresholds 文件按基准名单独覆盖（`Benchmark名=ns容忍度`）。
  - 基准文件头记录生成环境（go 版本/os/arch/git）。check 时若环境不一致：
    allocs/B 回归仍然失败（与机器无关），ns/op 回归降级为 WARN（跨机器不可比）。
"""
import argparse
import datetime
import os
import platform
import re
import subprocess
import sys

BENCH_LINE = re.compile(
    r"^(?P<name>Benchmark\S+?)(?:-(?P<procs>\d+))?\s+"
    r"(?P<iters>\d+)\s+(?P<ns>\d+(?:\.\d+)?)\s+ns/op"
    r"(?:\s+(?P<bytes>\d+)\s+B/op)?"
    r"(?:\s+(?P<allocs>\d+)\s+allocs/op)?"
)

DEFAULT_NS_TOLERANCE = 0.15
DEFAULT_BYTES_TOLERANCE = 0.10
DEFAULT_ALLOCS_TOLERANCE = 0.02


def parse_bench_output(path):
    """解析 go test -bench 输出；同名多次运行取 ns/op 最小值（benchmark 惯例）。"""
    entries = {}
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            match = BENCH_LINE.match(line.strip())
            if not match:
                continue
            name = match.group("name")
            item = {
                "ns": float(match.group("ns")),
                "bytes": int(match.group("bytes") or 0),
                "allocs": int(match.group("allocs") or 0),
            }
            prev = entries.get(name)
            if prev is None or item["ns"] < prev["ns"]:
                entries[name] = item
    return entries


def read_header(path):
    header = {}
    if not os.path.exists(path):
        return header
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            if not line.startswith("# "):
                break
            body = line[2:].strip()
            if ":" in body:
                key, _, value = body.partition(":")
                header[key.strip()] = value.strip()
    return header


def repo_root():
    try:
        return subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, check=True,
        ).stdout.strip() or None
    except Exception:
        return None


def collect_env(note=""):
    env = {
        "date": datetime.datetime.now().isoformat(timespec="seconds"),
        "os": f"{platform.system()}/{platform.machine()}",
    }
    try:
        env["go"] = subprocess.run(
            ["go", "env", "GOVERSION"], capture_output=True, text=True, check=True
        ).stdout.strip()
    except Exception:
        env["go"] = "unknown"
    cwd = repo_root()
    try:
        env["git"] = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, check=True, cwd=cwd,
        ).stdout.strip()
        if subprocess.run(
            ["git", "status", "--porcelain"],
            capture_output=True, text=True, check=True, cwd=cwd,
        ).stdout.strip():
            env["git"] += " (dirty)"
    except Exception:
        env["git"] = "unknown"
    if note:
        env["note"] = note
    return env


def load_thresholds(path):
    overrides = {}
    if path and os.path.exists(path):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                name, _, value = line.partition("=")
                try:
                    overrides[name.strip()] = float(value)
                except ValueError:
                    print(f"警告: 阈值行格式错误已忽略: {line}", file=sys.stderr)
    return overrides


def cmd_update(args):
    entries = parse_bench_output(args.input)
    if not entries:
        print(f"错误: {args.input} 中未找到 benchmark 结果行", file=sys.stderr)
        return 2
    env = collect_env(args.note)
    with open(args.baseline, "w", encoding="utf-8") as out:
        out.write("# bench-baseline（由 scripts/perf/bench_compare.sh --update 生成，请提交入库）\n")
        for key in ("date", "go", "os", "git", "note"):
            if key in env:
                out.write(f"# {key}: {env[key]}\n")
        out.write("# 阈值与用法见 scripts/perf/README.md\n")
        for name in sorted(entries):
            item = entries[name]
            out.write(
                f"{name}-8\t1\t{item['ns']:g} ns/op\t{item['bytes']} B/op\t{item['allocs']} allocs/op\n"
            )
    print(f"基准已更新: {args.baseline}（{len(entries)} 项）")
    return 0


def cmd_check(args):
    baseline_entries = parse_bench_output(args.baseline)
    current = parse_bench_output(args.input)
    if not current:
        print(f"错误: {args.input} 中未找到 benchmark 结果行", file=sys.stderr)
        return 2
    if not baseline_entries:
        print(f"错误: 基准文件为空或不存在: {args.baseline}，请先运行 make bench-baseline",
              file=sys.stderr)
        return 2

    overrides = load_thresholds(args.thresholds)
    header = read_header(args.baseline)
    env = collect_env()
    env_mismatch = any(
        header.get(key) and header[key] != env.get(key) for key in ("go", "os")
    )

    regressions, warnings, missing, fresh = [], [], [], []
    rows = []
    for name in sorted(set(baseline_entries) | set(current)):
        base = baseline_entries.get(name)
        new = current.get(name)
        if base is None:
            fresh.append(name)
            continue
        if new is None:
            missing.append(name)
            continue
        ns_tol = overrides.get(name, DEFAULT_NS_TOLERANCE)
        ns_delta = (new["ns"] - base["ns"]) / base["ns"] if base["ns"] else 0.0
        allocs_delta = new["allocs"] - base["allocs"]
        bytes_delta = (new["bytes"] - base["bytes"]) / base["bytes"] if base["bytes"] else 0.0

        status = "OK"
        if allocs_delta >= 1 and allocs_delta / max(base["allocs"], 1) > DEFAULT_ALLOCS_TOLERANCE:
            status = f"REG(allocs {base['allocs']}->{new['allocs']})"
        elif base["bytes"] and bytes_delta > DEFAULT_BYTES_TOLERANCE:
            status = f"REG(B/op +{bytes_delta:.0%})"
        elif ns_delta > ns_tol:
            status = f"REG(ns +{ns_delta:.1%}, tol {ns_tol:.0%})"
        elif ns_delta > ns_tol * 0.6:
            status = f"near({ns_delta:+.1%})"
        rows.append((name, base["ns"], new["ns"], ns_delta, status))
        if status.startswith("REG"):
            if env_mismatch and "allocs" not in status and "B/op" not in status:
                warnings.append((name, status))
            else:
                regressions.append((name, status))

    print(f"{'benchmark':<44}{'base ns':>12}{'new ns':>12}{'delta':>9}  状态")
    for name, base_ns, new_ns, delta, status in rows:
        print(f"{name:<44}{base_ns:>12,.0f}{new_ns:>12,.0f}{delta:>+8.1%}  {status}")
    for name in fresh:
        print(f"{name:<44}{'-':>12}{'NEW':>12}{'':>9}")
    for name in missing:
        print(f"{name:<44}{'MISSING':>12}{'-':>12}{'':>9}  基准中存在但本次未运行")

    if env_mismatch:
        print(f"\n⚠️ 环境与基准不一致（基准 go={header.get('go')} os={header.get('os')}，"
              f"当前 go={env.get('go')} os={env.get('os')}）：ns/op 回归已降级为 WARN。")
    for name, status in warnings:
        print(f"  WARN {name}: {status}")

    print(f"\n共 {len(rows)} 项可比，{len(fresh)} 项新增，{len(missing)} 项缺失，"
          f"{len(regressions)} 项超阈值回归，{len(warnings)} 项环境降级警告。")
    if regressions:
        print("结果: FAIL")
        return 1
    print("结果: PASS")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)

    p_update = sub.add_parser("update", help="用新结果更新基准文件")
    p_update.add_argument("--baseline", required=True)
    p_update.add_argument("--input", required=True)
    p_update.add_argument("--note", default="")
    p_update.set_defaults(func=cmd_update)

    p_check = sub.add_parser("check", help="对照基准检查回归")
    p_check.add_argument("--baseline", required=True)
    p_check.add_argument("--input", required=True)
    p_check.add_argument("--thresholds", default=None)
    p_check.set_defaults(func=cmd_check)

    args = parser.parse_args()
    sys.exit(args.func(args))


if __name__ == "__main__":
    main()
