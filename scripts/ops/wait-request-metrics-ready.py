#!/usr/bin/env python3
"""Wait for a candidate gateway to publish the usage backlog present at the first probe."""

import argparse
from datetime import datetime
import http.client
import json
import sys
import time


def timestamp(value):
    if value is None:
        return None
    # PostgreSQL timestamps have microsecond precision. Go's JSON format
    # drops trailing zeroes, so parse fractional fields of one to six digits
    # on Python versions whose fromisoformat only reads three or six.
    layout = "%Y-%m-%dT%H:%M:%S"
    if "." in value:
        layout += ".%f"
    return datetime.strptime(value, layout + "%z")


def probe(port, token, timeout):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        connection.request("GET", "/readyz/request-metrics", headers={"Authorization": "Bearer " + token})
        response = connection.getresponse()
        if response.status in (401, 403, 404, 405):
            raise RuntimeError(f"candidate projection probe returned HTTP {response.status}")
        if response.status not in (200, 503):
            raise ValueError(f"unexpected HTTP {response.status}")
        state = json.loads(response.read(8192))
        if not isinstance(state, dict) or state.get("source") != "request_metrics":
            raise ValueError("candidate response lacks the request_metrics source")
        if any(field not in state for field in ("checked_at", "updated_at", "pending_since")):
            raise ValueError("candidate response lacks projection timestamps")
        return response.status, state
    finally:
        connection.close()


def wait_for_projection(port, token, timeout):
    deadline = time.monotonic() + timeout
    through = None
    reason = "no valid response"
    while (remaining := deadline - time.monotonic()) > 0:
        try:
            status, state = probe(port, token, min(5, remaining))
            checked = timestamp(state["checked_at"])
            updated = timestamp(state["updated_at"])
            pending = timestamp(state["pending_since"])
            if through is None and checked is not None:
                through = checked
                print(f"request_metrics watermark target: {through.isoformat()}", flush=True)
            if (status == 200 and state.get("backfill_complete") is True and through is not None
                    and checked is not None and updated is not None and updated >= through
                    and (pending is None or pending > through)):
                print(f"request_metrics ready: updated_at={updated.isoformat()}", flush=True)
                return
            reason = (f"backfill_complete={state.get('backfill_complete')}, "
                      f"updated_at={updated}, pending_since={pending}")
        except (OSError, http.client.HTTPException, AttributeError, TypeError, ValueError) as error:
            reason = str(error)
        delay = min(2, deadline - time.monotonic())
        if delay > 0:
            time.sleep(delay)
    raise RuntimeError(f"request_metrics did not reach its deployment watermark within {timeout}s ({reason})")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("port", type=int)
    parser.add_argument("timeout", type=int)
    args = parser.parse_args()
    if not 1 <= args.port <= 65535 or args.timeout < 1:
        parser.error("expected a valid port and a positive timeout")
    token = sys.stdin.readline().rstrip("\r\n")
    if not token:
        parser.error("expected the candidate ops token on stdin")
    try:
        wait_for_projection(args.port, token, args.timeout)
    except RuntimeError as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
