#!/usr/bin/env python3
"""Sync the two columnar-governance fixes into the three 01-schema.sql baselines.

Each replacement asserts its own match count so a widened/ambiguous anchor
fails loudly instead of silently editing the wrong site.
"""
import sys

FILES = [
    "sql/schema/01-schema.sql",
    "deploy/sql/schemas/baseline/01-schema.sql",
    "installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
]

# A) auto_rotate_to_columnar: catalog `name` -> RETURNS TABLE `text`
OLD_A = """            parent.relname as parent_table,
            child.relname as child_table,
            am.amname as access_method,"""
NEW_A = """            parent.relname::text as parent_table,
            child.relname::text as child_table,
            am.amname::text as access_method,"""

# B) columnar_healthcheck: govern the 20 R17 rolled-back families
OLD_B = """            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive']::text[] AS should_be_heap"""
NEW_B = """            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive',
                  -- R17 rolled-back families (20): before 2026-10-01 these fell
                  -- through to expected='unknown', so the detector for the
                  -- outage that actually happened was blind to them.
                  'sessions','session_turns','session_turn_details',
                  'session_bodies','usage_facts','stats_event_inbox',
                  'credential_model_index','auto_route_selections',
                  'session_memora','session_censors','system_probe_runs',
                  'credit_ledger','tool_usage_stats','session_tools',
                  'session_module_executions','cache_metrics',
                  'dashboard_access_events','model_probe_runs','handoff_logs',
                  'supplier_errors']::text[] AS should_be_heap"""

# C) columnar_healthcheck: pg_inherits also holds partitioned-INDEX children
OLD_C = """        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
    )"""
NEW_C = """        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
          AND p.relkind = 'p'
          AND c.relkind = 'r'
    )"""

# D) the third-state note on 'unknown'.
# NOTE: the anchor MUST span the preceding WHEN line. An anchor of just
# "ELSE 'unknown' END::text AS expected," is a substring of the *replacement*
# too, so re-running the script would insert the comment a second time and the
# count check would still report 1 — a silent double-apply.
OLD_D = """            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE 'unknown'
        END::text AS expected,"""
NEW_D = """            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            -- 'unknown' is a legitimate third state, not just a gap: some
            -- families are columnarised per-partition by policy
            -- (request_logs_bodies, routing_decision_log_archive) and must
            -- stay out of BOTH lists -- widening should_be_columnar means
            -- editing the pinned columnar_insert_only_parents() SSOT.
            ELSE 'unknown'
        END::text AS expected,"""

REPLACEMENTS = [("A auto_rotate cast", OLD_A, NEW_A, 1),
                ("B should_be_heap", OLD_B, NEW_B, 1),
                ("C relkind filter", OLD_C, NEW_C, 1),
                ("D third-state note", OLD_D, NEW_D, 1)]

rc = 0
for path in FILES:
    with open(path, encoding="utf-8") as fh:
        body = fh.read()
    for label, old, new, want in REPLACEMENTS:
        got = body.count(old)
        if got != want:
            print(f"FAIL {path} [{label}]: expected {want} match(es), found {got}")
            rc = 1
            continue
        body = body.replace(old, new)
        print(f"ok   {path} [{label}] x{got}")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)

sys.exit(rc)
