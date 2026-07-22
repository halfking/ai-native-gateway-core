#!/usr/bin/env python3
"""
Real Provider Integration Test
Tests all 6 real LLM providers with actual API calls.

Usage: python3 local_test/real_provider_test.py
"""

import json
import sys
import time
import urllib.request
import urllib.error
from dataclasses import dataclass
from typing import Optional, Dict, List
from datetime import datetime


@dataclass
class Provider:
    name: str
    base_url: str
    api_key: str
    models: List[str]
    api_type: str = "openai-chat"  # openai-chat, anthropic-messages, openai-completions


PROVIDERS = [
    Provider(
        name="minimax",
        base_url="https://api.minimaxi.com/v1",
        api_key="sk-cp-bT8Qagnkbdo5xFil3rddP5GA7s31eSCd5ZrAvRroVu-M6fhZr21DHDmLx5h4SV-9Rd6dG40SdVp3XbUNLEGGIlYZuw3g33w1bmt5l99ESMyOS_gf-Ba1hvY",
        models=["MiniMax-M2", "MiniMax-M3"],
    ),
    Provider(
        name="zhipu",
        base_url="https://open.bigmodel.cn/api/coding/paas/v4",
        api_key="9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p",
        models=["glm-4.7", "glm-5.1"],
    ),
    Provider(
        name="evol",
        base_url="https://mg-new.evolai.cn/openclaw-proxy/v1",
        api_key="mg-aZXHIREnemGhonu_hvo5PbuyElTvYT1u779JwoJo6bU",
        models=["gpt-5.4", "gpt-5.5", "claude-opus-4-8", "claude-sonnet-4-6"],
        api_type="openai-completions",
    ),
    Provider(
        name="nvidia",
        base_url="https://integrate.api.nvidia.com/v1",
        api_key="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",
        models=["minimax-m2.7", "glm-4.7", "glm-5.1", "minimax-m3"],
    ),
    Provider(
        name="xiaomi",
        base_url="https://token-plan-cn.xiaomimimo.com/v1",
        api_key="tp-cia63detlzzaz731c3q3gebwi7ab6y1fas5r4sajo8n11l4m",
        models=["mimo-v2.5-pro", "mimo-v2.5"],
    ),
    Provider(
        name="kaixuan",
        base_url="https://llm.kxpms.cn/v1",
        api_key="sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9",
        models=[
            "minimax-m2.7",
            "minimax-m3",
            "glm-5.1",
            "deepseek-v4-pro",
            "mimo-v2.5-pro",
        ],
    ),
]


@dataclass
class TestResult:
    provider: str
    model: str
    success: bool
    latency_ms: float
    status_code: int
    response: Optional[Dict] = None
    error: Optional[str] = None
    input_tokens: int = 0
    output_tokens: int = 0
    timestamp: str = ""


def call_openai_chat(
    provider: Provider, model: str, prompt: str, timeout: int = 30
) -> TestResult:
    """Call OpenAI-compatible /chat/completions endpoint."""
    url = f"{provider.base_url.rstrip('/')}/chat/completions"
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 50,
        "temperature": 0,
    }
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {provider.api_key}",
    }
    return _do_request(url, payload, headers, provider, model, timeout)


def call_anthropic(
    provider: Provider, model: str, prompt: str, timeout: int = 30
) -> TestResult:
    """Call Anthropic-compatible /messages endpoint."""
    url = f"{provider.base_url.rstrip('/')}/messages"
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 50,
    }
    headers = {
        "Content-Type": "application/json",
        "x-api-key": provider.api_key,
        "anthropic-version": "2023-06-01",
    }
    return _do_request(url, payload, headers, provider, model, timeout)


def _do_request(
    url: str, payload: Dict, headers: Dict, provider: Provider, model: str, timeout: int
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
                return TestResult(
                    provider.name,
                    model,
                    True,
                    elapsed,
                    resp.status,
                    parsed,
                    None,
                    inp,
                    out,
                    ts,
                )
            except json.JSONDecodeError:
                return TestResult(
                    provider.name,
                    model,
                    False,
                    elapsed,
                    resp.status,
                    None,
                    f"Invalid JSON: {body[:200]}",
                    0,
                    0,
                    ts,
                )
    except urllib.error.HTTPError as e:
        elapsed = 0
        body = ""
        try:
            body = e.read().decode("utf-8")
        except Exception:
            pass
        return TestResult(
            provider.name,
            model,
            False,
            elapsed,
            e.code,
            None,
            f"HTTP {e.code}: {body[:300]}",
            0,
            0,
            ts,
        )
    except urllib.error.URLError as e:
        return TestResult(
            provider.name, model, False, 0, 0, None, f"URLError: {e.reason}", 0, 0, ts
        )
    except Exception as e:
        return TestResult(
            provider.name,
            model,
            False,
            0,
            0,
            None,
            f"{type(e).__name__}: {e}",
            0,
            0,
            ts,
        )


def test_provider(provider: Provider) -> List[TestResult]:
    results = []
    prompt = "1+1=?"
    print(f"\n{'=' * 70}")
    print(f"Testing: {provider.name}")
    print(f"  URL: {provider.base_url}")
    print(f"  Models: {', '.join(provider.models)}")
    print(f"{'=' * 70}")

    for model in provider.models:
        print(f"\n  [{model}]", end=" ", flush=True)
        if provider.api_type == "anthropic-messages":
            result = call_anthropic(provider, model, prompt)
        else:
            result = call_openai_chat(provider, model, prompt)

        if result.success:
            content = ""
            try:
                if "choices" in (result.response or {}):
                    content = result.response["choices"][0]["message"]["content"][:80]
                elif "content" in (result.response or {}):
                    content = str(result.response["content"])[:80]
            except (KeyError, IndexError, TypeError):
                content = "<no content>"
            print(
                f"✓ {result.latency_ms:.0f}ms | {result.status_code} | tokens={result.input_tokens}+{result.output_tokens} | {content!r}"
            )
        else:
            print(f"✗ {result.error or 'unknown error'}")
        results.append(result)
    return results


def main():
    print("╔══════════════════════════════════════════════════════════════════╗")
    print("║     LLM Gateway Real Provider Integration Test                  ║")
    print(
        "║     "
        + datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        + "                                              ║"
    )
    print("╚══════════════════════════════════════════════════════════════════╝")

    all_results = []
    total_success = 0
    total_tests = 0

    for provider in PROVIDERS:
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
            f"  {status} {prov_name}: {success}/{total} success, avg latency: {avg_latency:.0f}ms"
        )

    print(
        f"\n  Overall: {total_success}/{total_tests} ({100 * total_success / max(total_tests, 1):.1f}%)"
    )

    # Save report
    report = {
        "timestamp": datetime.now().isoformat(),
        "total_tests": total_tests,
        "total_success": total_success,
        "providers": {},
    }
    for prov_name, prov_results in by_provider.items():
        report["providers"][prov_name] = [
            {
                "model": r.model,
                "success": r.success,
                "latency_ms": r.latency_ms,
                "status_code": r.status_code,
                "input_tokens": r.input_tokens,
                "output_tokens": r.output_tokens,
                "error": r.error,
            }
            for r in prov_results
        ]

    with open("/tmp/real_provider_results.json", "w") as f:
        json.dump(report, f, indent=2)
    print(f"\n  Full report saved to /tmp/real_provider_results.json")

    return 0 if total_success == total_tests else 1


if __name__ == "__main__":
    sys.exit(main())
