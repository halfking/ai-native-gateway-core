#!/usr/bin/env bash
# Purpose: Read-only Redis keyspace audit for the LLM gateway.
# Status: active
# Changelog:
#   2026-08-24 v1.0 Add DB, prefix, TTL, and session preference orphan audit
# Rollback: Remove this read-only diagnostic script.

set -euo pipefail

REDIS_ADDR="${LLM_GATEWAY_REDIS_ADDR:?LLM_GATEWAY_REDIS_ADDR is required}"
REDIS_PASSWORD="${LLM_GATEWAY_REDIS_PASSWORD:?LLM_GATEWAY_REDIS_PASSWORD is required}"
REDIS_DB="${LLM_GATEWAY_REDIS_DB:-2}"
SAMPLE_LIMIT="${SAMPLE_LIMIT:-1000}"

export REDIS_AUDIT_ADDR="$REDIS_ADDR"
export REDIS_AUDIT_PASSWORD="$REDIS_PASSWORD"
export REDIS_AUDIT_DB="$REDIS_DB"
export REDIS_AUDIT_SAMPLE_LIMIT="$SAMPLE_LIMIT"

python3 - <<'PY'
import os
import socket

address = os.environ["REDIS_AUDIT_ADDR"]
password = os.environ["REDIS_AUDIT_PASSWORD"]
db = os.environ["REDIS_AUDIT_DB"]
sample_limit = os.environ["REDIS_AUDIT_SAMPLE_LIMIT"]
host, port = address.rsplit(":", 1)
sample_limit = int(sample_limit)


def send(sock, *args):
    data = [f"*{len(args)}\r\n".encode()]
    for arg in args:
        value = str(arg).encode()
        data.extend((f"${len(value)}\r\n".encode(), value, b"\r\n"))
    sock.sendall(b"".join(data))


def read(sock):
    line = b""
    while not line.endswith(b"\r\n"):
        line += sock.recv(1)
    kind, body = line[:1], line[1:-2]
    if kind in (b"+", b"-", b":"):
        return body.decode("utf-8", "replace")
    if kind == b"$":
        size = int(body)
        if size < 0:
            return None
        value = b""
        while len(value) < size + 2:
            value += sock.recv(size + 2 - len(value))
        return value[:size].decode("utf-8", "replace")
    if kind == b"*":
        return [read(sock) for _ in range(int(body))]
    raise RuntimeError(f"unexpected Redis reply: {line!r}")


def command(sock, *args):
    send(sock, *args)
    result = read(sock)
    if isinstance(result, str) and result.startswith("ERR"):
        raise RuntimeError(result)
    return result


def prefix(key):
    if key.startswith("llmgw:live:"):
        return "llmgw:live"
    if key.startswith("llmgw:cred_fp_"):
        return "llmgw:cred_fp"
    if key.startswith("llmgw:tenant:"):
        return "llmgw:tenant"
    if key.startswith("llmgw:"):
        return "llmgw:other"
    if key.startswith("session_pref:"):
        return "session_pref"
    if key.startswith("session:v2:"):
        return "session:v2"
    if key.startswith("session:"):
        return "session"
    if key.startswith("pending_response:"):
        return "pending_response"
    if key.startswith("requestjourney:"):
        return "requestjourney"
    if key.startswith("request:trace:"):
        return "request:trace"
    return key.split(":", 1)[0]


def scan(sock, pattern):
    cursor, keys = "0", []
    while True:
        cursor, page = command(sock, "SCAN", cursor, "MATCH", pattern, "COUNT", "10000")
        keys.extend(page or [])
        if cursor == "0":
            return keys


with socket.create_connection((host, int(port)), 5) as sock:
    command(sock, "AUTH", password)
    command(sock, "SELECT", db)
    patterns = (
        "session:*",
        "session_pref:*",
        "requestjourney:*",
        "pending_response:*",
        "request:trace:*",
        "ursm:*",
        "llmgw:*",
    )
    all_keys = []
    seen = set()
    for pattern in patterns:
        for key in scan(sock, pattern):
            if key not in seen:
                seen.add(key)
                all_keys.append(key)
    counts, ttl_buckets = {}, {"persistent": 0, "lt_1h": 0, "lt_24h": 0, "gte_24h": 0}
    for key in all_keys:
        group = prefix(key)
        counts[group] = counts.get(group, 0) + 1

    # TTL and orphan checks intentionally inspect a bounded sample. A full
    # per-key TTL walk would recreate the Redis load this audit is meant to
    # diagnose on a shared instance.
    for key in all_keys[:sample_limit]:
        ttl = int(command(sock, "TTL", key))
        if ttl == -1:
            ttl_buckets["persistent"] += 1
        elif ttl < 3600:
            ttl_buckets["lt_1h"] += 1
        elif ttl < 86400:
            ttl_buckets["lt_24h"] += 1
        else:
            ttl_buckets["gte_24h"] += 1

    prefs = scan(sock, "session_pref:*")
    pref_sample = prefs[:sample_limit]
    orphan_count = 0
    for start in range(0, len(pref_sample), 250):
        batch = pref_sample[start : start + 250]
        for key in batch:
            session_id = key.removeprefix("session_pref:")
            if command(sock, "EXISTS", f"session:{session_id}") == "0":
                orphan_count += 1

print(f"REDIS_AUDIT_DB={db}")
print(f"REDIS_AUDIT_TOTAL_KEYS={len(all_keys)}")
print(f"REDIS_AUDIT_TTL_SAMPLE={len(all_keys[:sample_limit])} {ttl_buckets}")
for group, count in sorted(counts.items(), key=lambda item: -item[1]):
    print(f"REDIS_AUDIT_PREFIX={group} COUNT={count}")
print(f"REDIS_AUDIT_SESSION_PREF_TOTAL={len(prefs)}")
print(f"REDIS_AUDIT_SESSION_PREF_ORPHAN_SAMPLE={len(pref_sample)}:{orphan_count}")
PY
