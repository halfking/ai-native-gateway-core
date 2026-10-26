#!/usr/bin/env python3
"""Publish the native gateway's blue/green active slot as Prometheus file_sd.

No credentials are read. An invalid/missing active-port fails without replacing
last-known discovery data, leaving a dead target visible rather than hiding it.
"""
import argparse
import json
import os
from pathlib import Path
import tempfile


def target_data(port_text, instance):
    port = port_text.strip()
    if port not in ("8781", "8782"):
        raise ValueError("active-port must be exactly 8781 or 8782")
    return [{"targets": ["127.0.0.1:" + port], "labels": {
        "instance": instance, "service": "llm-gateway", "component": "gateway",
    }}]


def sync(active_port, output, instance):
    data = target_data(active_port.read_text(), instance)
    rendered = json.dumps(data, sort_keys=True, indent=2) + "\n"
    if output.exists() and output.read_text() == rendered:
        return False
    output.parent.mkdir(parents=True, exist_ok=True)
    # Prometheus never observes partial JSON. The temp file does not match
    # the configured exact file_sd filename and contains no secrets.
    with tempfile.NamedTemporaryFile(mode="w", dir=str(output.parent),
                                     prefix=".gateway-target-", delete=False) as fh:
        fh.write(rendered)
        fh.flush()
        os.fsync(fh.fileno())
        temporary = fh.name
    os.chmod(temporary, 0o644)
    os.replace(temporary, str(output))
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--active-port", type=Path, default=Path("/opt/llm-gateway-go/run/active-port"))
    parser.add_argument("--output", type=Path, default=Path("/opt/monitoring/prometheus/targets/gateway-active.json"))
    parser.add_argument("--instance", default="gateway-245")
    args = parser.parse_args()
    try:
        changed = sync(args.active_port, args.output, args.instance)
    except (OSError, ValueError) as exc:
        parser.exit(1, "gateway target discovery failed: %s\n" % exc)
    print("gateway target discovery: " + ("updated" if changed else "unchanged"))


if __name__ == "__main__":
    main()
