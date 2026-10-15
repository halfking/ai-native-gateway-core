#!/usr/bin/env python3
"""
R43 follow-up: for each relation that breaks the fresh-install chain, find the
canonical startup migration that CREATES it. That turns "19 failures" into
"register these N migrations", which is the actionable size of the gap.
"""
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
CANON = os.path.join(ROOT, "sql/migrations/startup")

TARGETS = [
    "candidate_failure_logs_hot",
    "session_dim",
    "session_turns_hot",
    "session_turn_details",
    "session_turn_details_hot",
    "session_turns_with_current_month",
    "request_logs_with_current_month",
    "request_logs_with_current_month_without_request_class_due_at",
    "request_logs_with_current_month_without_customer_id",
    "session_module_executions_2026_10",
    "session_bodies_hot",
    "provider_endpoint_protocols",
    "role_task_llm_mapping",
    "model_stats",
]

CREATOR_PATS = [
    r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([A-Za-z_][\w$]*)",
    r"CREATE\s+OR\s+REPLACE\s+VIEW\s+(?:public\.)?([A-Za-z_][\w$]*)",
    r"CREATE\s+UNLOGGED\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([A-Za-z_][\w$]*)",
    r"CREATE\s+PARTITION\s+OF\s+(?:public\.)?([A-Za-z_][\w$]*)",
]


def main():
    files = sorted(f for f in os.listdir(CANON)
                   if f.endswith(".sql") and not f.endswith(".down.sql"))
    creators = {}
    for f in files:
        with open(os.path.join(CANON, f), encoding="utf-8", errors="replace") as fh:
            body = fh.read()
        body = re.sub(r"--[^\n]*", "", body)
        found = set()
        for p in CREATOR_PATS:
            for m in re.finditer(p, body, re.IGNORECASE):
                found.add(m.group(1))
        for name in found:
            creators.setdefault(name, []).append(f)

    for t in TARGETS:
        hits = creators.get(t, [])
        print(f"{t}:")
        if not hits:
            print("   (no canonical startup creator found)")
        for h in hits:
            print(f"   {h}")


if __name__ == "__main__":
    main()
