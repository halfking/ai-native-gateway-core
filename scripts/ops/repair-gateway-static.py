#!/usr/bin/env python3
"""Repair the known gateway static vhost contract; dry-run unless --apply."""
import argparse
import difflib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time


ROOT = "/opt/llm-gateway-go/current/web"
MENU = re.compile(r"(?m)^(\s*alias )/opt/llm-gateway-go/(?:releases/[0-9]+-[a-f0-9]+|current)/web/menu-config\.json;$")
SPA = """    location / {
        if (-f /opt/llm-gateway-go/maintenance/UPGRADING) { return 503; }
        root /opt/llm-gateway-go/current/web;
        index index.html;
        try_files $uri /index.html;
    }"""
# Location-level add_header suppresses inheritance on nginx < 1.29.3.
CACHE_HEADERS = """        add_header Cache-Control "no-cache" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header X-Frame-Options "DENY" always;
        add_header Referrer-Policy "strict-origin-when-cross-origin" always;
"""
CACHED_SPA = SPA.replace("        index index.html;", CACHE_HEADERS + "        index index.html;")


def repair(text):
    if text.count("server_name llm.kxpms.cn;") != 2:
        raise ValueError("expected the dedicated 154 HTTP/HTTPS vhost; refusing other topology")
    if text.count("location = /menu-config.json {") != 1 or len(MENU.findall(text)) != 1:
        raise ValueError("expected one known menu alias; refusing unknown config")
    if text.count(SPA) + text.count(CACHED_SPA) != 1:
        raise ValueError("expected one known SPA block; refusing unknown config")
    return MENU.sub(lambda m: m.group(1) + ROOT + "/menu-config.json;", text).replace(SPA, CACHED_SPA)


def atomic_write(path, content, reference):
    fd, name = tempfile.mkstemp(prefix=".gateway-static-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as out:
            out.write(content)
            out.flush()
            os.fsync(out.fileno())
        shutil.copystat(reference, name)
        st = reference.stat()
        if os.geteuid() == 0:
            os.chown(name, st.st_uid, st.st_gid)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def apply(path, before, after, nginx="nginx"):
    if path.is_symlink() or path.read_text() != before:
        raise ValueError("config changed or is a symlink; refusing overwrite")
    if before == after:
        subprocess.run([nginx, "-t"], check=True)
        return None
    subprocess.run([nginx, "-t"], check=True)
    backup = path.with_name(path.name + ".pre-static-repair-" + time.strftime("%Y%m%dT%H%M%S") + "-" + str(os.getpid()))
    if backup.exists():
        raise ValueError("backup already exists")
    shutil.copy2(path, backup)
    try:
        atomic_write(path, after, backup)
        subprocess.run([nginx, "-t"], check=True)
        subprocess.run([nginx, "-s", "reload"], check=True)
    except Exception:
        atomic_write(path, before, backup)
        subprocess.run([nginx, "-t"], check=True)
        subprocess.run([nginx, "-s", "reload"], check=True)
        raise
    return backup


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    before = args.config.read_text()
    after = repair(before)
    print("".join(difflib.unified_diff(before.splitlines(True), after.splitlines(True), fromfile=str(args.config), tofile="repaired")), end="")
    if args.apply:
        backup = apply(args.config, before, after)
        print("APPLIED backup=" + str(backup) if backup else "UNCHANGED (nginx -t passed)")
    else:
        print("DRY_RUN (no writes, no reload)")


if __name__ == "__main__":
    main()
