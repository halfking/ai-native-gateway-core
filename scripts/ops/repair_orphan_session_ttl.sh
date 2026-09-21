#!/usr/bin/env bash
# =============================================================================
# repair_orphan_session_ttl.sh — 修复孤儿 session:gw_* hash 缺 TTL 问题
# =============================================================================
#
# ICR-245-A5 P0-5 (2026-08-25, 子代理 B §6.1)
#   - 背景: db2 共享 Redis 上, ~50 个无 TTL key 中, 5 个 session:gw_* hash
#     与全局 ~19.9d TTL 策略不符, 可能是 touch 路径覆盖 / 写入跳过 SETEX /
#     迁移残留. 长期不过期会拖慢 maxmemory-policy=volatile-lru 生效后的
#     "应有 TTL 的临时数据" 驱逐.
#   - 本脚本为一次性修复 + 定期巡检:
#       * dry-run 模式 (默认) 仅列出孤儿 key + 计划修复结果, 不修改任何 TTL.
#       * apply 模式实际 EXPIRE 这批孤儿 key 到 --ttl (默认 19.9d = 1717600s).
#   - 必须 154 联调: 245 + 154 各自跑一次, 各自审计一次.
#   - 需要 <env:KEY> 凭据 (规则 47):
#       * LLM_GATEWAY_REDIS_ADDR     host:port (默认 172.16.2.210:6389)
#       * LLM_GATEWAY_REDIS_PASSWORD AUTH (无默认)
#       * LLM_GATEWAY_REDIS_DB       DB number (默认 2)
#
# 用法:
#   bash scripts/ops/repair_orphan_session_ttl.sh --dry-run
#   bash scripts/ops/repair_orphan_session_ttl.sh --apply --ttl 1717600
#   bash scripts/ops/repair_orphan_session_ttl.sh --help
#
# 退出码:
#   0  完成 (含 dry-run)
#   2  参数错误
#   3  凭据缺失
#   4  redis 连接失败
#   5  SCAN/TTL 阶段出现意外错误
#
# 验证方式 (修复后):
#   redis-cli -h ... -p ... -n 2 KEYS "session:gw_*" \
#     | xargs -I {} redis-cli -h ... -p ... -n 2 TTL {} | sort -u
#   # 不应再有 -1
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ---- defaults & args ---------------------------------------------------------

MODE="dry-run"        # dry-run | apply
TTL_SECONDS="1717600" # 19.9d = 1717600s (与 ICR §6 一致)
PREFIX="session:gw_*"
SCAN_COUNT="1000"
BATCH_SIZE="500"

usage() {
  sed -n '3,42p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) MODE="dry-run"; shift ;;
    --apply)   MODE="apply"; shift ;;
    --ttl)     TTL_SECONDS="$2"; shift 2 ;;
    --prefix)  PREFIX="$2"; shift 2 ;;
    --scan-count) SCAN_COUNT="$2"; shift 2 ;;
    --batch-size) BATCH_SIZE="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "ERROR: unknown arg: $1" >&2; usage ;;
  esac
done

# ---- env validation ----------------------------------------------------------

REDIS_ADDR="${LLM_GATEWAY_REDIS_ADDR:-172.16.2.210:6389}"
REDIS_PASSWORD="${LLM_GATEWAY_REDIS_PASSWORD:-}"
REDIS_DB="${LLM_GATEWAY_REDIS_DB:-2}"

if [[ -z "$REDIS_PASSWORD" ]]; then
  echo "ERROR: LLM_GATEWAY_REDIS_PASSWORD is required (rule 47 凭据查询)." >&2
  exit 3
fi

if ! [[ "$TTL_SECONDS" =~ ^[0-9]+$ ]] || [[ "$TTL_SECONDS" -lt 1 ]]; then
  echo "ERROR: --ttl must be a positive integer (got: $TTL_SECONDS)" >&2
  exit 2
fi

if ! [[ "$SCAN_COUNT" =~ ^[0-9]+$ ]] || [[ "$SCAN_COUNT" -lt 1 ]]; then
  echo "ERROR: --scan-count must be a positive integer (got: $SCAN_COUNT)" >&2
  exit 2
fi

HOST="${REDIS_ADDR%:*}"
PORT="${REDIS_ADDR##*:}"
HOST="${HOST##*@}"  # strip creds if accidentally embedded

echo "[repair_orphan_session_ttl] mode=$MODE prefix=$PREFIX db=$REDIS_DB ttl=${TTL_SECONDS}s"

# ---- redis ops (raw RESP) ----------------------------------------------------
# redis-cli 在多 db SCAN + 多 key EXPIRE 的 pipeline 模式下行为可预测,
# 但在本机镜像 db2 已观察到 scan cursor 漂移; 用 python 直连更稳.

export REDIS_REPAIR_ADDR="$REDIS_ADDR"
export REDIS_REPAIR_PASSWORD="$REDIS_PASSWORD"
export REDIS_REPAIR_DB="$REDIS_DB"
export REDIS_REPAIR_PREFIX="$PREFIX"
export REDIS_REPAIR_SCAN_COUNT="$SCAN_COUNT"
export REDIS_REPAIR_BATCH_SIZE="$BATCH_SIZE"
export REDIS_REPAIR_MODE="$MODE"
export REDIS_REPAIR_TTL="$TTL_SECONDS"

python3 - <<'PY'
import os
import socket
import sys

address   = os.environ["REDIS_REPAIR_ADDR"]
password  = os.environ["REDIS_REPAIR_PASSWORD"]
db        = int(os.environ["REDIS_REPAIR_DB"])
prefix    = os.environ["REDIS_REPAIR_PREFIX"]
scan_cnt  = int(os.environ["REDIS_REPAIR_SCAN_COUNT"])
batch_sz  = int(os.environ["REDIS_REPAIR_BATCH_SIZE"])
mode      = os.environ["REDIS_REPAIR_MODE"]
ttl       = int(os.environ["REDIS_REPAIR_TTL"])

host, port = address.rsplit(":", 1)
host = host.split("@")[-1]


def send(sock, *args):
    out = [f"*{len(args)}\r\n".encode()]
    for a in args:
        b = str(a).encode()
        out.extend((f"${len(b)}\r\n".encode(), b, b"\r\n"))
    sock.sendall(b"".join(out))


def read_line(sock):
    line = b""
    while not line.endswith(b"\r\n"):
        chunk = sock.recv(1)
        if not chunk:
            raise RuntimeError("redis closed mid-line")
        line += chunk
    return line[:-2]


def read(sock):
    line = read_line(sock)
    kind, body = line[:1], line[1:]
    if kind == b"+":
        return body.decode()
    if kind == b"-":
        raise RuntimeError(body.decode())
    if kind == b":":
        return int(body)
    if kind == b"$":
        size = int(body)
        if size < 0:
            return None
        buf = b""
        while len(buf) < size + 2:
            buf += sock.recv(size + 2 - len(buf))
        return buf[:size].decode()
    if kind == b"*":
        n = int(body)
        if n < 0:
            return None
        return [read(sock) for _ in range(n)]
    raise RuntimeError(f"unexpected redis reply: {line!r}")


def cmd(sock, *args):
    send(sock, *args)
    return read(sock)


try:
    sock = socket.create_connection((host, int(port)), timeout=10)
except OSError as exc:
    print(f"FATAL: cannot connect to {host}:{port} — {exc}", file=sys.stderr)
    sys.exit(4)

try:
    cmd(sock, "AUTH", password)
    cmd(sock, "SELECT", db)

    # ---- 阶段 1: SCAN 收集 candidate keys --------------------------------
    cursor, candidates = "0", []
    pages = 0
    while True:
        cursor, page = cmd(sock, "SCAN", cursor, "MATCH", prefix, "COUNT", scan_cnt)
        candidates.extend(page or [])
        pages += 1
        if cursor == "0":
            break

    print(f"[scan] prefix={prefix} matched={len(candidates)} pages={pages}")

    # ---- 阶段 2: pipeline TTL 查询, 过滤 -1 -----------------------------
    orphans = []
    for start in range(0, len(candidates), batch_sz):
        batch = candidates[start:start + batch_sz]
        # pipelined TTL
        send(sock, *[("TTL", k) for k in zip(*[iter(batch)] * 1)][0] if False else None)  # placeholder
        # easier: serial
        for k in batch:
            t = cmd(sock, "TTL", k)
            if t == -1:
                orphans.append(k)

    print(f"[filter] orphans_without_ttl={len(orphans)} of {len(candidates)}")
    if not orphans:
        print("[ok] no orphans, nothing to repair")
        sys.exit(0)

    # ---- 阶段 3: dry-run 报告 / apply EXPIRE -----------------------------
    if mode == "dry-run":
        for k in orphans[:50]:
            print(f"[dry-run] would EXPIRE {k} {ttl}")
        if len(orphans) > 50:
            print(f"[dry-run] ... and {len(orphans) - 50} more")
        print(f"[dry-run] total planned EXPIRE operations: {len(orphans)}")
        sys.exit(0)

    # apply
    fixed = 0
    failed = 0
    for k in orphans:
        r = cmd(sock, "EXPIRE", k, ttl)
        if r == 1:
            fixed += 1
        else:
            failed += 1
            print(f"[apply] FAILED {k} (reply={r})", file=sys.stderr)

    print(f"[apply] fixed={fixed} failed={failed}")
    if failed > 0:
        sys.exit(5)
    sys.exit(0)
finally:
    try:
        sock.close()
    except Exception:
        pass
PY
