#!/usr/bin/env python3
"""
Sensitive info redaction script for docs/*.md
Replaces real values with __{CAT}_{N}__ placeholders,
saves mapping to ~/.llm-gateway/docs-sensitive.json
"""

import json
import os
import re
import glob
from collections import OrderedDict

MAPPING_FILE = os.path.expanduser("~/.llm-gateway/docs-sensitive.json")
DOCS_DIR = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "docs"
)

SENSITIVE_MAP = OrderedDict()
PLACEHOLDER_INDEX = {}


def p(cat, val):
    if val not in PLACEHOLDER_INDEX:
        idx = len([k for k in SENSITIVE_MAP if k.startswith(f"__{cat.upper()}_")]) + 1
        ph = f"__{cat.upper()}_{idx}__"
        PLACEHOLDER_INDEX[val] = ph
        SENSITIVE_MAP[ph] = {"value": val, "category": cat, "files": []}
    return PLACEHOLDER_INDEX[val]


# === Define all sensitive values (longest first for safe replacement) ===

# API Keys
p("api_key", "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9")
p("api_key", "sk-k40DVd9aqFGumYcEkfkQvSgdv06uepSNDK0BqHwtwS3RzTgY")
p("api_key", "sk-1R7IBh2THq1Id2BDWOWHstpFu2oG09Qd1kgYn9hasxFcKZw7")
p("api_key", "sk-e2e-1781897808-B-3322")
p("api_key", "sk-probe-AAAA-BBBB-CCCC-DDDD")
p("api_key", "sk-plain-text-leak-test")
p("api_key", "sk-test")
p("api_key", "834a588e-dcfe-4daf-90c0-e65435c6e6ba")

# Passwords
p("ssh_pwd", "Kaixuan2026&#*9527")
p("admin_pwd", "Veritrans&9527")
p("db_pwd", "4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg")
p("db_pwd", "llm_gateway_2024")
p("db_pwd", "kxpass")

# Public IPs
p("pub_ip", "14.103.112.184")
p("pub_ip", "14.103.174.71")
p("pub_ip", "14.103.169.56")
p("pub_ip", "8.155.23.184")
p("pub_ip", "115.29.212.252")
p("pub_ip", "8.136.114.245")
p("pub_ip", "47.97.111.154")
p("pub_ip", "118.31.18.168")

# Private IPs
p("priv_ip", "172.31.0.2")
p("priv_ip", "172.31.0.3")
p("priv_ip", "172.31.0.4")
p("priv_ip", "172.16.2.210")
p("priv_ip", "172.16.2.241")
p("priv_ip", "172.16.2.209")
p("priv_ip", "10.43.118.61")

# Domains
p("domain", "llmgo.kxpms.cn")
p("domain", "llm.kxpms.cn")
p("domain", "llm.itestu.cn")
p("domain", "registry.kxpms.cn")
p("domain", "nexus.kxpms.cn")
p("domain", "184.kxpms.cn")
p("domain", "llmgateway-test.kxpms.cn")
p("domain", "llmgateway.internal.example.com")
p("domain", "registry.internal.example.com")
p("domain", "auth.internal.example.com")
p("domain", "mcp.internal.example.com")
p("domain", "internal.example.com")

# Git repo URLs
p("repo_url", "https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go")
p("repo_url", "codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go")
p("repo_url", "github.com/kaixuan/llm-gateway-go")

# Ports
p("port", "25022")
p("port", "8780")
p("port", "8781")
p("port", "8782")
p("port", "5432")
p("port", "5433")
p("port", "5434")
p("port", "5000")
p("port", "30080")
p("port", "6379")
p("port", "4800")
p("port", "8080")
p("port", "8081")

# Local paths
p("local_path", "/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go")

# Server paths
p("server_path", "/opt/llm-gateway-go")
p("server_path", "/opt/kx-memora-build/services/llm-gateway-go")
p("server_path", "/etc/llm-gateway-go")
p("server_path", "/etc/llm-gw")
p("server_path", "/var/log/llm-gateway-go")
p("server_path", "/var/log/llm-gateway")
p("server_path", "/var/log/llm-gateway-cleanup.log")
p("server_path", "/etc/cron.d/llm-gateway-cleanup")
p("server_path", "/opt/llm-gateway/scripts/health-check.sh")
p("server_path", "/var/log/health-check.log")

# Usernames
p("user", "xutaohuang")
p("user", "kaixuan")

# SSH user@host combos (entire strings)
p("ssh_target", "root@14.103.112.184")
p("ssh_target", "root@14.103.174.71")
p("ssh_target", "root@8.155.23.184")

# Special values from docs
p("secret", "__HOST_71_IP__")  # existing placeholder, standardize
p("secret", "__REDACTED_DB_PASSWORD__")  # existing placeholder
p("secret", "__REDACTED_SSH_PASSWORD__")  # existing placeholder

# Build a replacement list sorted by length (longest first) to avoid partial matches
REPLACEMENTS = sorted(PLACEHOLDER_INDEX.items(), key=lambda x: -len(x[0]))


def redact_file(filepath):
    with open(filepath, "r", encoding="utf-8", errors="replace") as f:
        content = f.read()
    original = content

    for val, ph in REPLACEMENTS:
        # word boundary aware replacement
        count = 0
        new_content, count = re.subn(re.escape(val), ph, content)
        if count > 0:
            content = new_content
            SENSITIVE_MAP[ph]["files"].append(os.path.relpath(filepath, DOCS_DIR))

    if content != original:
        with open(filepath, "w", encoding="utf-8") as f:
            f.write(content)
        return True
    return False


def main():
    os.makedirs(os.path.dirname(MAPPING_FILE), exist_ok=True)

    # Find all md files in docs/
    md_files = glob.glob(os.path.join(DOCS_DIR, "**/*.md"), recursive=True)
    md_files.sort()

    modified = []
    for fp in md_files:
        if redact_file(fp):
            modified.append(os.path.relpath(fp, DOCS_DIR))

    # Deduplicate file lists
    for ph in SENSITIVE_MAP:
        SENSITIVE_MAP[ph]["files"] = sorted(set(SENSITIVE_MAP[ph]["files"]))

    # Build by_category index
    by_category = {}
    for ph, info in SENSITIVE_MAP.items():
        cat = info["category"]
        by_category.setdefault(cat, []).append(ph)

    output = {
        "version": "1",
        "generated_at": "2026-07-05",
        "info": "Sensitive value → placeholder mapping. KEEP OUT OF GIT REPO.",
        "mappings": SENSITIVE_MAP,
        "by_category": by_category,
        "stats": {
            "total_placeholders": len(SENSITIVE_MAP),
            "modified_files": len(modified),
            "by_category": {cat: len(phs) for cat, phs in by_category.items()},
        },
    }

    with open(MAPPING_FILE, "w", encoding="utf-8") as f:
        json.dump(output, f, indent=2, ensure_ascii=False)

    print(f"=== Redaction Complete ===")
    print(f"Placeholders created: {len(SENSITIVE_MAP)}")
    print(f"Files modified: {len(modified)}")
    print(f"Mapping saved to: {MAPPING_FILE}")
    print(f"\nModified files:")
    for m in modified:
        print(f"  {m}")
    print(f"\nBy category:")
    for cat, phs in sorted(by_category.items()):
        print(f"  {cat}: {len(phs)}")
    print(f"\nPlaceholder list:")
    for ph, info in sorted(SENSITIVE_MAP.items()):
        print(f"  {ph} = {info['value']}")


if __name__ == "__main__":
    main()
