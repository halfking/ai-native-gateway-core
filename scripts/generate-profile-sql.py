#!/usr/bin/env python3
"""
Generate SQL for 12-group differentiated provider profiles.

Reads provider-profiles.json and emits SQL that:
- Clears existing mock data (id 9010-9099)
- Inserts 60 providers with per-group category/discount/network_quality
- Inserts provider_models (3-5 per provider, from 15 custom models)
- Inserts credentials with per-group concurrency/fp_slot/plan_type/balance
- Inserts credential_model_bindings with per-group billing_mode/tier/weight/price
- Inserts credential_quotas + usage for plan groups (C/D/E)
- Updates models_canonical for the 15 custom models

Usage: python3 generate-profile-sql.py > /tmp/profile-credentials.sql
       psql -d llm_gateway -f /tmp/profile-credentials.sql
"""

import json
import os
import sys

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROFILES_FILE = os.path.join(SCRIPT_DIR, "profiles", "provider-profiles.json")

with open(PROFILES_FILE) as f:
    CONFIG = json.load(f)

PROFILES = CONFIG["profiles"]
GROUP_PORTS = CONFIG["group_port_map"]
MODELS = CONFIG["models"]

# Encrypted token for all mocks (matches LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=AAAA...)
# We'll use a placeholder; the encrypt-mock-credentials.sh script updates them after.
CIPHER_HEX_PLACEHOLDER = "v1:placeholder"


def gen_sql():
    lines = []
    lines.append("-- ====================================================================")
    lines.append("-- 12-Group Differentiated Provider Profiles — auto-generated")
    lines.append("-- 60 mock providers, 15 custom models, per-group concurrency/cost/quota")
    lines.append("-- ====================================================================")
    lines.append("BEGIN;")
    lines.append("")
    lines.append("-- Clear existing mock data")
    lines.append("DELETE FROM public.credential_quota_usage WHERE quota_id IN (SELECT id FROM public.credential_quotas WHERE credential_id BETWEEN 9010 AND 9099);")
    lines.append("DELETE FROM public.credential_quotas WHERE credential_id BETWEEN 9010 AND 9099;")
    lines.append("DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;")
    lines.append("DELETE FROM public.provider_models WHERE provider_id BETWEEN 9010 AND 9099;")
    lines.append("DELETE FROM public.credentials WHERE id BETWEEN 9010 AND 9099;")
    lines.append("DELETE FROM public.providers WHERE id BETWEEN 9010 AND 9099;")
    lines.append("")

    # Ensure canonical models exist
    lines.append("-- Canonical models")
    lines.append("INSERT INTO public.models_canonical (canonical_name, display_name, family, modality) VALUES")
    for i, m in enumerate(MODELS):
        family = m.split("-")[1]  # mini/standard/pro/ultra/vision
        comma = "," if i < len(MODELS) - 1 else ""
        lines.append(f"    ('{m}', '{m}', 'loadtest', 'text'){comma}")
    lines.append("ON CONFLICT (canonical_name) DO NOTHING;")
    lines.append("")

    provider_id = 9010
    cred_id = 9010
    pm_id = 90000  # provider_model ids start high to avoid collision
    binding_id = 90000
    quota_id = 91000

    for group_key, profile in PROFILES.items():
        group = profile["group"]
        ports = GROUP_PORTS[group]
        instances = profile["instances"]

        for idx in range(instances):
            port = ports[idx]
            pid = provider_id
            cid = cred_id
            token = f"mock-{pid - 9010:02d}"
            code = f"profile-{group.lower()}-{idx:02d}"
            label = f"{profile['name']} {idx:02d}"
            base_url = f"http://localhost:{port}"

            cat = profile.get("category", "official")
            discount = profile.get("discount_rate", 1.0)
            nqs = profile.get("network_quality_score", 0.9)

            lines.append(f"-- {label} (group {group}, port {port})")
            # Provider
            lines.append(f"INSERT INTO public.providers (id, tenant_id, code, display_name, kind, category, protocol, base_url, enabled, manual_disabled, discount_rate, network_quality_score) VALUES (")
            lines.append(f"    {pid}, 'default', '{code}', '{label}', 'cloud', '{cat}', 'openai-completions', '{base_url}', true, false, {discount}, {nqs}")
            lines.append(");")

            # Provider models: each provider gets 3 models (cycle through 15)
            models_for_this = []
            for mi in range(3):
                model_name = MODELS[(pid - 9010 + mi) % len(MODELS)]
                models_for_this.append((pm_id, model_name))
                canonical_sub = f"(SELECT id FROM models_canonical WHERE canonical_name='{model_name}')"
                lines.append(f"INSERT INTO public.provider_models (id, provider_id, raw_model_name, outbound_model_name, canonical_id, available) VALUES (")
                lines.append(f"    {pm_id}, {pid}, '{model_name}', '{model_name}', {canonical_sub}, true")
                lines.append(");")
                pm_id += 1

            # Credential
            conc = profile["concurrency_limit"]
            fp = profile["fp_slot_limit"]
            plan_type = profile["plan_type"]
            balance = profile.get("balance_usd")
            balance_sql = "NULL" if balance is None else str(balance)
            pool_group = profile.get("pool_group")
            pool_sql = f"'{pool_group}'" if pool_group else "NULL"
            sr = profile.get("success_rate", 0.95)
            p95 = profile.get("p95_latency_ms", 500)

            lines.append(f"INSERT INTO public.credentials (id, provider_id, tenant_id, label, secret_ciphertext, status, lifecycle_status, availability_state, quota_state, circuit_state, manual_disabled, fp_slot_limit, concurrency_limit, plan_type, balance_usd, pool_group) VALUES (")
            lines.append(f"    {cid}, {pid}, 'default', '{label} Key', '{CIPHER_HEX_PLACEHOLDER}'::bytea, 'active', 'active', 'ready', 'ok', 'closed', false, {fp}, {conc}, '{plan_type}', {balance_sql}::numeric, {pool_sql}")
            lines.append(");")

            # Credential model bindings with per-group routing/price
            billing = profile["billing_mode"]
            tier = profile["routing_tier"]
            weight = profile["weight"]
            mprio = profile["manual_priority"]
            price_in = profile.get("price_in_per_1m", 0)
            price_out = profile.get("price_out_per_1m", 0)
            currency = profile.get("currency", "USD")

            for (pmid, model_name) in models_for_this:
                lines.append(f"INSERT INTO public.credential_model_bindings (id, credential_id, provider_model_id, available, billing_mode, routing_tier, weight, manual_priority, unit_price_in_per_1m, unit_price_out_per_1m, currency, success_rate, p95_latency_ms, plan_type_origin) VALUES (")
                lines.append(f"    {binding_id}, {cid}, {pmid}, true, '{billing}', {tier}, {weight}, {mprio}, {price_in}::numeric, {price_out}::numeric, '{currency}', {sr}::numeric, {p95}, 'manual'")
                lines.append(");")
                binding_id += 1

            # Credential quotas for plan groups
            qc = profile.get("quota_config")
            if qc:
                cap_tokens = qc["cap_total_tokens"]
                window_type = qc["window_type"]
                period = qc.get("period", "monthly")
                lines.append(f"INSERT INTO public.credential_quotas (id, credential_id, quota_name, window_type, period, cap_total_tokens, enabled, priority) VALUES (")
                lines.append(f"    {quota_id}, {cid}, '{group_key}_quota', '{window_type}', '{period}', {cap_tokens}, true, 100")
                lines.append(");")
                lines.append(f"INSERT INTO public.credential_quota_usage (quota_id, window_started_at, window_ends_at, used_total_tokens, used_requests, used_cost_usd, exhausted) VALUES (")
                lines.append(f"    {quota_id}, NOW(), NOW() + INTERVAL '30 days', 0, 0, 0::numeric, false")
                lines.append(");")
                quota_id += 1

            lines.append("")
            provider_id += 1
            cred_id += 1

    lines.append("COMMIT;")
    lines.append("")
    lines.append("-- Verification")
    lines.append("SELECT 'providers' AS metric, COUNT(*)::text AS value FROM providers WHERE id BETWEEN 9010 AND 9099")
    lines.append("UNION ALL SELECT 'credentials', COUNT(*)::text FROM credentials WHERE id BETWEEN 9010 AND 9099")
    lines.append("UNION ALL SELECT 'routable_bindings', COUNT(*)::text FROM v_routable_credential_models WHERE credential_id BETWEEN 9010 AND 9099 AND is_routable")
    lines.append("UNION ALL SELECT 'quotas', COUNT(*)::text FROM credential_quotas WHERE credential_id BETWEEN 9010 AND 9099;")
    lines.append("")
    lines.append("-- Per-group routable breakdown")
    lines.append("SELECT LEFT(p.code, 9) AS grp, c.plan_type, COUNT(DISTINCT c.id) AS creds, COUNT(*) FILTER (WHERE v.is_routable) AS routable")
    lines.append("FROM v_routable_credential_models v")
    lines.append("JOIN credentials c ON c.id = v.credential_id")
    lines.append("JOIN providers p ON p.id = c.provider_id")
    lines.append("WHERE c.id BETWEEN 9010 AND 9099")
    lines.append("GROUP BY 1, 2 ORDER BY 1;")

    return "\n".join(lines)


if __name__ == "__main__":
    print(gen_sql())
