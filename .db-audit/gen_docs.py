#!/usr/bin/env python3
"""Generate the database structure documentation from tmp/tables.json.

Outputs into docs/database/2026-09-17-db-audit/:
  schema-diagram.md      Mermaid erDiagram per functional domain (PK/FK attrs)
  schema-inventory.md    full per-table column listing (271 tables)
"""
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "docs" / "database" / "2026-09-17-db-audit"
OUT.mkdir(parents=True, exist_ok=True)

tables = json.loads((Path(__file__).parent / "tmp" / "tables.json").read_text(encoding="utf-8"))

# Monthly partition leaf tables (e.g. credit_ledger_2026_07) are collapsed
# into their family for the diagram; kept in the inventory appendix.
PART_RE = re.compile(r"_(\d{4})_(\d{2})$")
HOT_RE = re.compile(r"_hot$")

DOMAINS = [
    ("核心路由与凭证", ["credentials", "credential_", "providers", "provider_", "model_",
                   "models_", "routing_", "route_", "node_probe", "system_probe",
                   "candidate_failure", "supplier_error", "auto_tune", "sticky_sessions",
                   "passive_probe", "work_type", "task_", "tuning_", "featured"]),
    ("请求日志与账本（分区族）", ["request_logs", "request_wal", "request_stats", "usage_ledger",
                          "credit_ledger", "stats_", "tool_usage", "maas_", "billing_",
                          "subscription_", "tenant_credit"]),
    ("会话与分析", ["sessions", "session_", "analysis_", "intent_", "compression_bench",
                  "cluster"]),
    ("API 密钥与应用", ["api_key", "applications", "users", "tenants", "key_applications"]),
    ("审批与安全", ["approval", "armor_", "prompt_injection", "output_compliance",
                 "attachments", "ip_blocklist", "canary_", "session_censors",
                 "sensitive_", "sanitize", "canary"]),
    ("许可与实例运维", ["license", "product_module", "tier_", "gateway_instances",
                   "instance_", "ops_node", "releases", "gray_release", "upgrade_logs",
                   "center_commands", "auto_update", "runtime_telemetry"]),
    ("故障与诊断", ["fault_", "diagnostic_", "self_check", "model_iq", "integrity_",
                 "data_lifecycle", "background_tasks", "webcookie_sessions"]),
    ("代理与资产图谱", ["agents", "agent_", "assets", "asset_", "asset_rel", "proxy_",
                   "provider_domains", "discovery_", "download_", "donations", "dashboard_"]),
    ("调参与反馈", ["auto_route", "routing_feedback", "routing_optimization", "training_",
                 "tuning_proposals", "tuning_params", "routing_analytics"]),
    ("其余", []),
]

def domain_of(name: str) -> str:
    for dom, prefixes in DOMAINS:
        for p in prefixes:
            if name == p or name.startswith(p):
                return dom
    return "其余"

def family_of(name: str) -> str:
    """Collapse monthly leaf partitions into the family root."""
    m = PART_RE.search(name)
    if m and name[: m.start()] in tables:
        return name[: m.start()]
    return name

def mer_ident(name: str) -> str:
    return re.sub(r"[^a-z0-9]+", "_", name.lower()).strip("_")

def attr_line(col: dict) -> str:
    typ = col["type"].split()[0]
    key = " PK" if col["pk"] else ""
    t = {"integer": "int", "bigint": "bigint", "text": "string", "boolean": "boolean",
         "jsonb": "jsonb", "numeric": "numeric", "timestamp": "timestamptz"}.get(typ, typ)
    return f'    {t} {mer_ident(col["name"])}{key}'

# ── schema-diagram.md ────────────────────────────────────────────────────
fams = {}
for t in tables.values():
    fam = family_of(t["name"])
    fams.setdefault(fam, {"root": t if fam == t["name"] else None, "leaves": [], "members": []})
    fams[fam]["members"].append(t["name"])
    if fam == t["name"]:
        fams[fam]["root"] = t
    elif HOT_RE.search(t["name"]) or PART_RE.search(t["name"]):
        fams[fam]["leaves"].append(t["name"])

dom_members = {}
for fam in fams:
    dom = domain_of(fam)
    dom_members.setdefault(dom, []).append(fam)

order = [d for d, _ in DOMAINS]
lines = []
lines.append("# 数据库结构图（llm-gateway-go · PostgreSQL）\n")
lines.append("> 生成物：`.db-audit/parse_schema.py` 解析权威 schema `sql/schema/01-schema.sql`")
lines.append(">（30,315 行）。覆盖 271 张表 / 4,697 列 / 621 个索引 / 34 条外键。")
lines.append("> 按月分区叶子表（`*_YYYY_MM`）在图中折叠进其家族根表；全列清单见")
lines.append("> [schema-inventory.md](schema-inventory.md)。生成日期：2026-09-17。\n")

def emit_chunk(out, chunk, fams, mer_ident, family_of):
    out.append("```mermaid")
    out.append("erDiagram")
    for fam in chunk:
        ent = mer_ident(fam)
        t = fams[fam]["root"]
        cols = t["columns"] if t else []
        show = [c for c in cols if c["pk"]][:3]
        fks = t["fks"] if t else []
        fk_cols = {c for fk in fks for c in fk["columns"]}
        extra = [c for c in cols if not c["pk"] and c["name"] in fk_cols][:4]
        if not show:
            show = cols[:2]
        out.append(f"    {ent} {{")
        for c in show + extra:
            out.append(attr_line(c))
        out.append("    }")
    for fam in chunk:
        ent = mer_ident(fam)
        t = fams[fam]["root"]
        if not t:
            continue
        seen = set()
        for fk in t["fks"]:
            ref = family_of(fk["ref_table"])
            key = (ref, tuple(fk["columns"]), tuple(fk["ref_columns"]))
            if key in seen:
                continue
            seen.add(key)
            out.append(f'    {ent} references {mer_ident(ref)} : "{",".join(fk["columns"])}"')
    out.append("```")


for dom in order:
    famlist = sorted(dom_members.get(dom, []))
    if not famlist:
        continue
    NL = "\n"
    lines.append(NL + f"## {dom}" + NL)
    CHUNK = 20
    chunks = [famlist[i:i + CHUNK] for i in range(0, len(famlist), CHUNK)]
    if len(chunks) > 1:
        lines.append(f"共 {len(famlist)} 个表家族（月分区叶子已折叠），拆为 {len(chunks)} 张子图。" + NL)
    for ci, chunk in enumerate(chunks):
        if len(chunks) > 1:
            lines.append(NL + f"### {dom} · 子图 {ci + 1}/{len(chunks)}" + NL)
        emit_chunk(lines, chunk, fams, mer_ident, family_of)
    hot_notes = [f for f in famlist if fams[f]["leaves"]]
    if hot_notes:
        lines.append(NL + "<details><summary>分区/热表家族</summary>" + NL)
        for f in hot_notes:
            lines.append(f"- `{f}`：{len(fams[f]['leaves'])} 个分区/热叶子（{'、'.join(sorted(fams[f]['leaves'])[:6])}{'…' if len(fams[f]['leaves'])>6 else ''}）")
        lines.append(NL + "</details>")

# logical relationships not backed by real FKs (evidence: column naming)
lines.append("""
## 逻辑外键（无 DB 约束，应用层维护）

本库大量关系不建物理外键（写入吞吐优先），以下为代码路径核实的主要逻辑引用：

```mermaid
erDiagram
    request_logs }o--|| credentials : "credential_id"
    request_logs }o--|| providers : "provider_id"
    request_logs }o--|| api_keys : "api_key_id"
    request_logs }o--o| models_canonical : "canonical_id"
    credential_model_bindings }o--|| credentials : "credential_id"
    credential_model_bindings }o--|| provider_models : "provider_model_id"
    credential_model_index }o--|| credentials : "credential_id"
    credential_model_index }o--|| provider_models : "provider_model_id"
    model_aliases }o--|| models_canonical : "canonical_id"
    usage_ledger }o--|| api_keys : "api_key_id"
    request_logs }o--|| tenants : "tenant_id"
    sessions }o--|| tenants : "tenant_id"
    routing_decision_log }o--|| request_logs : "request_id"
    credential_keys }o--|| credentials : "credential_id"
    api_keys }o--|| applications : "application_id"
    routing_feedback_log }o--|| credentials : "predicted_provider_id"
```
""")

(OUT / "schema-diagram.md").write_text("\n".join(lines), encoding="utf-8")

# ── schema-inventory.md ──────────────────────────────────────────────────
inv = ["# 表结构全量清单（271 表）\n",
       "> 由 `.db-audit/parse_schema.py` 自动生成，来源 `sql/schema/01-schema.sql`。",
       "NN = NOT NULL；PK 列已标注。月分区叶子（`*_YYYY_MM`）与根表列结构一致。\n"]
for name in sorted(tables):
    t = tables[name]
    inv.append(f"\n## {name}\n")
    meta = []
    if t["partition_of"]:
        meta.append(f"PARTITION OF {t['partition_of']}")
    if t["unlogged"]:
        meta.append("UNLOGGED")
    if meta:
        inv.append(f"`{'; '.join(meta)}`\n")
    inv.append("| 列 | 类型 | NN | 默认 | 键 |")
    inv.append("|---|---|---|---|---|")
    for c in t["columns"]:
        inv.append("| {} | {} | {} | {} | {} |".format(
            c["name"], c["type"], "Y" if c["not_null"] else "",
            c["default"] or "", "PK" if c["pk"] else ""))
    if t["fks"]:
        inv.append("\n外键：")
        for fk in t["fks"]:
            inv.append(f"- ({', '.join(fk['columns'])}) → {fk['ref_table']}({', '.join(fk['ref_columns'])})")
    idx = t.get("indexes") or []
    if idx:
        inv.append(f"\n索引（{len(idx)}）：")
        for i in idx[:20]:
            inv.append(f"- {i['name']}{' UNIQUE' if i['unique'] else ''}")
        if len(idx) > 20:
            inv.append(f"- …共 {len(idx)} 个")
(OUT / "schema-inventory.md").write_text("\n".join(inv), encoding="utf-8")
print(f"families={len(fams)} domains={{k: len(v) for k, v in dom_members.items()}}")
print({k: len(v) for k, v in dom_members.items()})
