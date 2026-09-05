#!/usr/bin/env python3
"""
Multi-canonical test - Verifies model mapping via real gateway.
Tests that canonical names are properly translated to provider-specific names.
"""

import json
import sys
import time
import urllib.request
import urllib.error
from collections import Counter
from datetime import datetime

GATEWAY_URL = "http://localhost:8082/v1/chat/completions"

CANONICAL_MODELS = [
    "minimax-m2",
    "minimax-m3",
    "glm-4.7",
    "glm-5.1",
    "deepseek-v4",
    "mimo-v2.5",
]

EXPECTED_MAPPINGS = {
    "minimax-m2": [
        "MiniMax-M2",
        "minimaxai/minimax-m2.7",
        "MiniMax-M2.7",
        "minimax-m2.7",
    ],
    "minimax-m3": ["MiniMax-M3", "minimaxai/minimax-m3", "MiniMax-M3", "minimax-m3"],
    "glm-4.7": ["glm-4.7", "z-ai/glm-4.5"],
    "glm-5.1": ["glm-5.1", "z-ai/glm-5.2"],
    "deepseek-v4": ["deepseek-v4-flash", "deepseek-v4-pro"],
    "mimo-v2.5": ["mimo-v2.5", "mimo-v2.5-pro"],
}


def call_gateway(model, prompt="hi", timeout=20):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 30,
        "temperature": 0,
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        GATEWAY_URL,
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        start = time.time()
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8")
            elapsed = (time.time() - start) * 1000
            parsed = json.loads(body)
            return {
                "success": True,
                "model_used": parsed.get("model", "?"),
                "status": resp.status,
                "elapsed_ms": elapsed,
                "content": parsed.get("choices", [{}])[0]
                .get("message", {})
                .get("content", "")[:60],
            }
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8")
        return {"success": False, "status": e.code, "error": body[:200]}
    except Exception as e:
        return {"success": False, "error": str(e)}


def main():
    print("╔════════════════════════════════════════════════════════════╗")
    print("║   Real Gateway Multi-Canonical Model Mapping Test          ║")
    print(
        "║   "
        + datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        + "                                  ║"
    )
    print("╚════════════════════════════════════════════════════════════╝")
    print("\nArchitecture: Client canonical → Gateway maps → Provider native")

    total_success = 0
    total_tests = 0
    by_canonical = {}

    for canonical in CANONICAL_MODELS:
        print(f"\n━━━ Testing canonical: {canonical} ━━━")
        expected_models = EXPECTED_MAPPINGS.get(canonical, [])
        print(f"  Expected native models: {expected_models}")

        # Send 3 requests
        results = []
        for i in range(3):
            r = call_gateway(canonical)
            results.append(r)
            total_tests += 1
            if r["success"]:
                total_success += 1
                marker = "✓" if r["model_used"] in expected_models else "?"
                print(
                    f"  [{i + 1}] {marker} {r['elapsed_ms']:.0f}ms | model={r['model_used']} | {r['content']!r}"
                )
            else:
                print(f"  [{i + 1}] ✗ ERROR: {r.get('error', '?')[:80]}")

        by_canonical[canonical] = results

    print("\n" + "=" * 60)
    print("VERIFICATION SUMMARY")
    print("=" * 60)

    for canonical, results in by_canonical.items():
        successful = [r for r in results if r["success"]]
        expected = EXPECTED_MAPPINGS.get(canonical, [])

        if not successful:
            print(f"  ✗ {canonical}: all failed")
            continue

        # Check that returned models match expected mappings
        used_models = set(r["model_used"] for r in successful)
        valid = all(m in expected for m in used_models)

        status = "✓" if valid else "⚠"
        print(
            f"  {status} {canonical}: {len(successful)}/{len(results)} OK, used={used_models}"
        )

    print(
        f"\n  Overall: {total_success}/{total_tests} ({100 * total_success / max(total_tests, 1):.1f}%)"
    )
    return 0 if total_success == total_tests else 1


if __name__ == "__main__":
    sys.exit(main())
