#!/usr/bin/env python3
"""
Real Provider Integration Test - Fixed version
Tests all 6 real LLM providers with correct model names.
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


# Fixed model names based on actual API discovery
PROVIDERS = [
    Provider(
        name="minimax",
        base_url="https://api.minimaxi.com/v1",
        api_key="sk-cp-bT8Qagnkbdo5xFil3rddP5GA7s31eSCd5ZrAvRroVu-M6fhZr21DHDmLx5h4SV-9Rd6dG40SdVp3XbUNLEGGIlYZuw3g33w1bmt5l99ESMyOS_gf-Ba1hvY",
        models=["MiniMax-M2", "MiniMax-M3"],  # minimax官方模型
    ),
    Provider(
        name="zhipu",
        base_url="https://open.bigmodel.cn/api/coding/paas/v4",
        api_key="9f7fa0edca07455e80c7431b059182b3.2hJa8SexdbT4hu1p",
        models=["glm-4.7", "glm-5.1"],  # 智谱官方模型
    ),
    Provider(
        name="evol",
        base_url="https://mg-new.evolai.cn/openclaw-proxy/v1",
        api_key="mg-aZXHIREnemGhonu_hvo5PbuyElTvYT1u779JwoJo6bU",
        models=[
            "MiniMax-M2.7",
            "MiniMax-M3",
            "glm-5.1",
            "deepseek-v4-flash",
        ],  # 便宜的模型
    ),
    Provider(
        name="nvidia",
        base_url="https://integrate.api.nvidia.com/v1",
        api_key="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",
        models=[
            "minimaxai/minimax-m2.7",
            "minimaxai/minimax-m3",
            "z-ai/glm-5.2",
        ],  # NVIDIA正确模型名
    ),
    Provider(
        name="xiaomi",
        base_url="https://token-plan-cn.xiaomimimo.com/v1",
        api_key="tp-cia63detlzzaz731c3q3gebwi7ab6y1fas5r4sajo8n11l4m",
        models=["mimo-v2.5-pro", "mimo-v2.5"],  # 小米官方模型
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
        ],  # 自有平台
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
    response_preview: str = ""


def call_openai_chat(
    provider: Provider, model: str, prompt: str, timeout: int = 30
) -> TestResult:
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
                preview = ""
                try:
                    if "choices" in parsed and parsed["choices"]:
                        preview = parsed["choices"][0]["message"]["content"][:80]
                except (KeyError, IndexError, TypeError):
                    pass
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
                    preview,
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
            "",
        )
    except urllib.error.URLError as e:
        return TestResult(
            provider.name,
            model,
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
            model,
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
    print(f"Testing: {provider.name}")
    print(f"  URL: {provider.base_url}")
    print(f"  Models: {', '.join(provider.models)}")
    print(f"{'=' * 70}")

    for model in provider.models:
        print(f"  [{model}]", end=" ", flush=True)
        result = call_openai_chat(provider, model, prompt, timeout=20)
        if result.success:
            print(
                f"✓ {result.latency_ms:.0f}ms | {result.status_code} | tokens={result.input_tokens}+{result.output_tokens} | {result.response_preview!r}"
            )
        else:
            err = result.error or "unknown"
            if len(err) > 100:
                err = err[:100] + "..."
            print(f"✗ {err}")
        results.append(result)
    return results


def main():
    print("╔══════════════════════════════════════════════════════════════════╗")
    print("║     LLM Gateway Real Provider Integration Test (v2)            ║")
    print(
        "║     "
        + datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        + "                                       ║"
    )
    print("╚══════════════════════════════════════════════════════════════════╝")

    all_results = []
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
                "error": r.error if not r.success else None,
            }
            for r in prov_results
        ]

    with open("/tmp/real_provider_results_v2.json", "w") as f:
        json.dump(report, f, indent=2)
    print(f"\n  Full report saved to /tmp/real_provider_results_v2.json")

    return 0 if total_success == total_tests else 1


if __name__ == "__main__":
    sys.exit(main())
