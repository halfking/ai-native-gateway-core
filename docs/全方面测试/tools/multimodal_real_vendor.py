# docs/全方面测试/tools/multimodal_real_vendor.py
#
# Real-vendor multimodal smoke test. Designed for the user-driven
# verification step described in 10-多模态客户端与画像.md (round 4):
# "请增加多模态的数据的测试，这个要在本地测试后要使用真实的供应商多模态
#  模型进行测试验证，注意请求量，不需要做并发测试，只需要做流程测试。"
#
# This script does NOT run a load test. It makes 1-2 sequential requests
# per scenario (no concurrency, no N-rps burst) so the operator can
# verify the gateway correctly forwards multimodal content to real
# providers (e.g. volcano-ark coding plan, zhipu glm-4.6v, deepseek
# janus, moonshot kimi, etc.) and inspect the vendor's actual response.
#
# Usage:
#   # 1. Add a real API key to the gateway (admin UI or DB direct).
#   # 2. Edit the scenarios below to point at your real model.
#   # 3. Run:
#   LLM_GATEWAY_BASE="http://localhost:8781" \
#   AUTH_TOKEN="sk-your-real-key-here" \
#   python3 multimodal_real_vendor.py --scenario "volcano-glm-4-6v"
#
# The output is a single human-readable report per scenario:
#   - HTTP status
#   - Latency
#   - Response model + content preview
#   - request_logs row (tenant_id, client_profile, identity_hash)

import argparse
import asyncio
import json
import os
import sys
import time
from typing import Any, Dict, List, Optional

import aiohttp


# Each scenario targets one real vendor multimodal model. The credentials
# row in the DB must already exist (insert via admin UI / direct psql).
# The model name below must match canonical_name in models_canonical.
SCENARIOS: Dict[str, Dict[str, Any]] = {
    "volcano-glm-4-6v": {
        "description": "Volcano Ark GLM-4.6V (multimodal vision-language)",
        "model": "glm-4.6v",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "Describe this image in one sentence."},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
    "volcano-doubao-1-5-vision": {
        "description": "Volcano Ark Doubao 1.5 Vision (multimodal)",
        "model": "doubao-1-5-vision-pro-32k",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "What do you see in this picture?"},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
    "zhipu-glm-4v": {
        "description": "Zhipu GLM-4V (multimodal vision-language)",
        "model": "glm-4v",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "描述这张图片。"},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
    "deepseek-janus": {
        "description": "DeepSeek Janus-Pro (multimodal)",
        "model": "janus-pro-7b",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "Describe this image briefly."},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
    "kimi-vl": {
        "description": "Moonshot Kimi-VL (multimodal vision-language)",
        "model": "moonshot-v1-8k-vision",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "简要描述这张图片。"},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
    "minimax-m2-her": {
        "description": "MiniMax M2 (multimodal: image + text)",
        "model": "minimax-m2-7b",
        "extra_headers": {},
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": "Please describe this image."},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": "https://upload.wikimedia.org/wikipedia/commons/thumb/8/85/Elon_Musk_Royal_Society_%28crop2%29.jpg/200px-Elon_Musk_Royal_Society_%28crop2%29.jpg"
                        },
                    },
                ],
            }
        ],
    },
}


async def _request(
    session: aiohttp.ClientSession,
    base: str,
    auth: str,
    model: str,
    messages: List[dict],
    extra_headers: Dict[str, str],
    timeout: float = 60.0,
) -> Dict[str, Any]:
    url = base.rstrip("/") + "/v1/chat/completions"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {auth}",
    }
    headers.update(extra_headers or {})
    payload = {"model": model, "messages": messages, "max_tokens": 200}
    t0 = time.monotonic()
    async with session.post(
        url, json=payload, headers=headers, timeout=aiohttp.ClientTimeout(total=timeout)
    ) as resp:
        text = await resp.text()
        elapsed = time.monotonic() - t0
        body: Optional[dict] = None
        try:
            body = json.loads(text)
        except Exception:
            body = {"raw_text": text[:400]}
        return {
            "status": resp.status,
            "elapsed_sec": elapsed,
            "body": body,
            "request_id": resp.headers.get("X-Request-Id", ""),
        }


async def _query_request_log(base: str, request_id: str) -> Optional[Dict[str, Any]]:
    """Fetch the request_logs row by X-Request-Id (admin endpoint)."""
    if not request_id:
        return None
    url = base.rstrip("/") + f"/api/admin/request-logs?request_id={request_id}"
    try:
        async with aiohttp.ClientSession() as s:
            async with s.get(url, timeout=aiohttp.ClientTimeout(total=5.0)) as resp:
                if resp.status != 200:
                    return None
                body = await resp.json()
                items = body.get("items", body) if isinstance(body, dict) else body
                if isinstance(items, list) and items:
                    return items[0]
                return None
    except Exception:
        return None


def _print_report(
    scenario_name: str,
    scen: Dict[str, Any],
    result: Dict[str, Any],
    log: Optional[Dict[str, Any]],
) -> None:
    body = result["body"] or {}
    choice = (body.get("choices") or [{}])[0]
    content = (choice.get("message") or {}).get("content", "")
    usage = body.get("usage", {}) or {}

    print(f"=== {scenario_name}: {scen['description']} ===")
    print(f"  HTTP status      : {result['status']}")
    print(f"  Latency          : {result['elapsed_sec'] * 1000:.0f} ms")
    print(f"  X-Request-Id     : {result['request_id']}")
    print(f"  Response model   : {body.get('model', '<none>')}")
    print(f"  Finish reason    : {choice.get('finish_reason', '<none>')}")
    print(
        f"  Usage            : prompt={usage.get('prompt_tokens', '?')} "
        f"completion={usage.get('completion_tokens', '?')} "
        f"total={usage.get('total_tokens', '?')}"
    )
    if isinstance(content, str):
        preview = content[:300] + ("..." if len(content) > 300 else "")
    elif isinstance(content, list):
        preview = json.dumps(content, ensure_ascii=False)[:300]
    else:
        preview = str(content)[:300]
    print(f"  Content preview  : {preview}")
    if log:
        print("  request_logs row :")
        for k in (
            "tenant_id",
            "client_profile",
            "identity_hash",
            "virtual_client_id",
            "virtual_ip",
            "credential_id",
            "provider_id",
        ):
            if k in log:
                print(f"    {k:20s} = {log[k]}")
    else:
        print("  request_logs row : <not available>")
    if result["status"] >= 400:
        err = body.get("error") if isinstance(body, dict) else body
        print(f"  Error            : {json.dumps(err, ensure_ascii=False)[:300]}")


async def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--scenario",
        action="append",
        default=None,
        help="Scenario name(s) to run (default: all)",
    )
    parser.add_argument("--all", action="store_true", help="Run all defined scenarios")
    args = parser.parse_args()

    base = os.environ.get("LLM_GATEWAY_BASE", "http://localhost:8781")
    auth = os.environ.get("AUTH_TOKEN", "")
    if not auth:
        print(
            "AUTH_TOKEN env var is required (e.g. AUTH_TOKEN=sk-...)", file=sys.stderr
        )
        return 2

    selected = (
        list(SCENARIOS.keys()) if (args.all or not args.scenario) else args.scenario
    )
    for s in selected:
        if s not in SCENARIOS:
            print(f"unknown scenario: {s}", file=sys.stderr)
            return 2

    print(f"# Real-vendor multimodal smoke test against {base}")
    print(f"# {len(selected)} scenario(s), sequential, no concurrency")
    print()

    async with aiohttp.ClientSession() as session:
        for name in selected:
            scen = SCENARIOS[name]
            result = await _request(
                session,
                base,
                auth,
                scen["model"],
                scen["messages"],
                scen.get("extra_headers", {}),
            )
            log = await _query_request_log(base, result["request_id"])
            _print_report(name, scen, result, log)
            print()
            # Small spacing between scenarios to avoid hammering the
            # vendor's rate limiter.
            await asyncio.sleep(1.0)

    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
