#!/usr/bin/env python3
"""
Real Provider Integration Test v3 - Uses Canonical Model Names + Provider Mapping
Architecture:
- Client sends canonical model names (e.g., "minimax-m2")
- ModelMapper translates to provider-specific names (e.g., "minimaxai/minimax-m2.7" for NVIDIA)
- Real provider receives its native model name

Usage: python3 local_test/real_provider_test_v3.py
"""

import json
import sys
import time
import urllib.request
import urllib.error
import os
from dataclasses import dataclass
from typing import Optional, Dict, List
from datetime import datetime

sys.path.insert(0, os.path.dirname(__file__))
from models.canonical_models import CANONICAL_MODELS, get_provider_model


@dataclass
class Provider:
    name: str
    base_url: str
    api_key: str
    # canonical model name → supported
    supported_canonical: List[str]


# Define providers with their SUPPORTED canonical models
PROVIDERS = [
    Provider(
        name="minimax",
        base_url="https://api.minimaxi.com/v1",
        api_key="sk-cp-bT8Qagnkbdo5xFil3rddP5GA7s31eSCd5ZrAvRroVu-M6fhZr21DHDmLx5h4SV-9Rd6dG40SdVp3XbUNLEGGIlYZuw3g33w1bmt5l99ESMyOS_gf-Ba1hvY",
        supported_canonical=["minimax-m2", "minimax-m3"],
    ),
    Provider(
        name="zhipu",
        base_url="https://open.bigmodel.cn/api/coding/paas/v4",
        api_key="9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p",
        supported_canonical=["glm-4.7", "glm-5.1"],
    ),
    Provider(
        name="nvidia",
        base_url="https://integrate.api.nvidia.com/v1",
        api_key="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",
        supported_canonical=[
            "minimax-m2",
            "minimax-m3",
            "glm-5.1",
        ],  # nvidia has different native names
    ),
    Provider(
        name="kaixuan",
        base_url="https://llm.kxpms.cn/v1",
        api_key="sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9",
        supported_canonical=[
            "minimax-m2",
            "minimax-m3",
            "glm-5.1",
            "deepseek-v4",
            "mimo-v2.5",
        ],
    ),
    Provider(
        name="evol",
        base_url="https://mg-new.evolai.cn/openclaw-proxy/v1",
        api_key="mg-aZXHIREnemGhonu_hvo5PbuyElTvYT1u779JwoJo6bU",
        # evol supports all these models but uses "积分不足" errors for high-value ones
        # We'll skip evol in this test since credits are exhausted
        supported_canonical=[],
    ),
]


@dataclass
class TestResult:
    provider: str
    canonical_model: str
    native_model: str
    success: bool
    latency_ms: float
    status_code: int
    response: Optional[Dict] = None
    error: Optional[str] = None
    input_tokens: int = 0
    output_tokens: int = 0
    timestamp: str = ""
    response_preview: str = ""


def call_with_canonical(
    provider: Provider, canonical: str, prompt: str, timeout: int = 30
) -> TestResult:
    """
    Call provider with canonical model name → automatically mapped to native.
    """
    native_model = get_provider_model(canonical, provider.name)

    url = f"{provider.base_url.rstrip('/')}/chat/completions"
    payload = {
        "model": native_model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 50,
        "temperature": 0,
    }
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {provider.api_key}",
    }
    return _do_request(
        url, payload, headers, provider, canonical, native_model, timeout
    )


def _do_request(
    url: str,
    payload: Dict,
    headers: Dict,
    provider: Provider,
    canonical: str,
    native: str,
    timeout: int,
) -> TestResult:
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    ts = datetime.now().isoformat()
    try:
        start = time.time()
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8")
            elapsed = (time.time() - start) * 1000
            try:
                parsed = json.loads(body)
                inp = parsed.get("usage", {}).get("prompt_tokens", 0)
                out = parsed.get("usage", {}).get("completion_tokens", 0)
                preview = ""
                try:
                    if "choices" in parsed and parsed["choices"]:
                        preview = parsed["choices"][0]["message"]["content"][:80]
                except (KeyError, IndexError, TypeError):
                    pass
                return TestResult(
                    provider.name,
                    canonical,
                    native,
                    True,
                    elapsed,
                    resp.status,
                    parsed,
                    None,
                    inp,
                    out,
                    ts,
                    preview,
                )
            except json.JSONDecodeError:
                return TestResult(
                    provider.name,
                    canonical,
                    native,
                    False,
                    elapsed,
                    resp.status,
                    None,
                    f"Invalid JSON: {body[:200]}",
                    0,
                    0,
                    ts,
                    "",
                )
    except urllib.error.HTTPError as e:
        body = ""
        try:
            body = e.read().decode("utf-8")
        except Exception:
            pass
        return TestResult(
            provider.name,
            canonical,
            native,
            False,
            0,
            e.code,
            None,
            f"HTTP {e.code}: {body[:200]}",
            0,
            0,
            ts,
            "",
        )
    except urllib.error.URLError as e:
        return TestResult(
            provider.name,
            canonical,
            native,
            False,
            0,
            0,
            None,
            f"URLError: {e.reason}",
            0,
            0,
            ts,
            "",
        )
    except Exception as e:
        return TestResult(
            provider.name,
            canonical,
            native,
            False,
            0,
            0,
            None,
            f"{type(e).__name__}: {e}",
            0,
            0,
            ts,
            "",
        )


def test_provider(provider: Provider) -> List[TestResult]:
    results = []
    prompt = "1+1=?"
    print(f"\n{'=' * 70}")
    print(f"Provider: {provider.name}")
    print(f"  URL: {provider.base_url}")
    print(f"  Supported canonical models: {provider.supported_canonical}")
    print(f"{'=' * 70}")

    for canonical in provider.supported_canonical:
        native = get_provider_model(canonical, provider.name)
        print(f"  {canonical:20s} → {native:25s}", end=" ", flush=True)
        result = call_with_canonical(provider, canonical, prompt, timeout=20)
        if result.success:
            print(
                f"✓ {result.latency_ms:.0f}ms | {result.status_code} | {result.input_tokens}+{result.output_tokens}t | {result.response_preview!r}"
            )
        else:
            err = result.error or "unknown"
            if len(err) > 80:
                err = err[:80] + "..."
            print(f"✗ {err}")
        results.append(result)
    return results


def main():
    print("╔══════════════════════════════════════════════════════════════════╗")
    print("║   Real Provider Test v3 - Canonical Names + Model Mapping     ║")
    print(
        "║   "
        + datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        + "                                       ║"
    )
    print("╚══════════════════════════════════════════════════════════════════╝")
    print("\nArchitecture:")
    print("  Client → Canonical Name (e.g., 'minimax-m2')")
    print(
        "  ModelMapper → Provider Native Name (e.g., 'minimaxai/minimax-m2.7' for NVIDIA)"
    )
    print("  Provider → Returns native response")

    all_results = []
    for provider in PROVIDERS:
        if not provider.supported_canonical:
            print(
                f"\n⊘ Skipping {provider.name} (no supported models - credits exhausted)"
            )
            continue
        try:
            results = test_provider(provider)
            all_results.extend(results)
        except Exception as e:
            print(f"  Error: {e}")

    # Summary
    print("\n" + "=" * 70)
    print("SUMMARY")
    print("=" * 70)

    by_provider = {}
    for r in all_results:
        by_provider.setdefault(r.provider, []).append(r)

    total_success = sum(1 for r in all_results if r.success)
    total_tests = len(all_results)

    for prov_name, prov_results in by_provider.items():
        success = sum(1 for r in prov_results if r.success)
        total = len(prov_results)
        avg_latency = sum(r.latency_ms for r in prov_results if r.success) / max(
            success, 1
        )
        status = "✓" if success == total else "⚠" if success > 0 else "✗"
        print(
            f"  {status} {prov_name}: {success}/{total} success, avg: {avg_latency:.0f}ms"
        )

    print(
        f"\n  Overall: {total_success}/{total_tests} ({100 * total_success / max(total_tests, 1):.1f}%)"
    )

    # Show all working canonical→native mappings
    print("\n" + "=" * 70)
    print("WORKING MAPPINGS (canonical → native, proven via real API)")
    print("=" * 70)
    for r in all_results:
        if r.success:
            print(
                f"  ✓ {r.canonical_model:20s} → {r.native_model:30s} via {r.provider}"
            )

    # Save report
    report = {
        "timestamp": datetime.now().isoformat(),
        "architecture": "canonical-name + provider mapping",
        "total_tests": total_tests,
        "total_success": total_success,
        "providers": {},
    }
    for prov_name, prov_results in by_provider.items():
        report["providers"][prov_name] = [
            {
                "canonical": r.canonical_model,
                "native": r.native_model,
                "success": r.success,
                "latency_ms": r.latency_ms,
                "status_code": r.status_code,
                "input_tokens": r.input_tokens,
                "output_tokens": r.output_tokens,
                "preview": r.response_preview,
                "error": r.error if not r.success else None,
            }
            for r in prov_results
        ]

    with open("/tmp/real_provider_results_v3.json", "w") as f:
        json.dump(report, f, indent=2)
    print(f"\n  Full report saved to /tmp/real_provider_results_v3.json")

    return 0 if total_success == total_tests else 1


if __name__ == "__main__":
    sys.exit(main())
