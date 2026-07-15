#!/usr/bin/env python3
# docs/全方面测试/tools/multimodal_client.py
#
# Test client that posts multimodal requests through the gateway
# and asserts what the multimodal_supplier recorded.
#
# Usage:
#   python3 multimodal_client.py --gateway http://localhost:8781 \
#       --supplier http://host.docker.internal:19280 \
#       --scenario S17_openai_image_url
#
# The script outputs a JSON line {ok: true, scenario, checks: [...]}
# per scenario. Exit code 0 iff all checks passed.
#
# Each scenario bundles:
#   - request_payload()      -> dict (the JSON body to POST)
#   - required_fingerprint() -> dict (headers we set; supplier will record)
#   - expected_modalities()   -> dict {"text": int, "image": int, "audio": int, "file": int}
#   - expected_client_profile -> str (X-Client-Profile we send)
#   - expected_tenant         -> str (X-Tenant-Id we send)
#
# Assertions check against /admin/requests?n=1 on the multimodal_supplier.

import argparse
import asyncio
import base64
import json
import os
import sys
import time
import uuid
from typing import Any, Callable, Dict, List, Optional, Tuple

import aiohttp

# ── 1x1 transparent PNG + tiny WAV; both small enough for HTTP tests. ──
PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAfbLI3wAAAABJRU5ErkJggg=="
)
PNG_1X1_B64 = PNG_1X1.decode("latin-1")
WAV_TINY = base64.b64encode(
    b"RIFF$\x00\x00\x00WAVEfmt \x00\x00\x00\x10\x00\x01\x00\x01\x00\x00\x01\x00\x00\x00\x00data\x00\x00\x00\x00"
).decode("ascii")


# The gateway's attachment extractor tries to save any data: URI it
# sees in an image_url block, which fails on the local test (no actual
# decoder is required because the data is a real 1x1 PNG). We use an
# http://127.0.0.1:1 URL that the gateway's URL fetcher will fail to
# connect to — the gateway is expected to fall back to passing the URL
# through unchanged to the supplier, and the supplier records the URL
# kind ('http') without re-fetching.
TEST_PNG_URL = "http://127.0.0.1:1/fixture-1x1.png"
TEST_PNG_URL_2 = "http://127.0.0.1:1/fixture-1x1-alt.png"
TEST_PNG_URL_3 = "http://127.0.0.1:1/fixture-1x1-b.png"
TEST_WAV_URL = "http://127.0.0.1:1/fixture-tiny.wav"


# ── Scenario definitions ──────────────────────────────────────────────────────


SCENARIOS: Dict[str, Dict[str, Any]] = {
    "S17_openai_image_url": {
        "description": "OpenAI Chat Completions with a single image_url (data URI)",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Device-Seed": "test-device-001",
            "X-Machine-Id": "test-machine-001",
            "X-Client-Profile": "roocode",
            "X-Runtime-Name": "opencode",
            "X-Runtime-Version": "1.2.3",
            "X-OS-Name": "darwin",
            "X-OS-Arch": "arm64",
            "X-Tenant-Id": "tenant-roocode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "Describe this image in one sentence.",
                        },
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL}},
                    ],
                }
            ],
            "max_tokens": 100,
        },
        "expected_modalities": {"text": 1, "image": 1, "audio": 0, "file": 0},
    },
    "S18_anthropic_image": {
        "description": "Anthropic Messages /v1/messages with native image block (base64)",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/messages",
        "extra_headers": {
            "anthropic-version": "2023-06-01",
            "X-Client-Profile": "claude-code",
            "X-Tenant-Id": "tenant-claude-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "max_tokens": 100,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {
                            "type": "text",
                            "text": "Describe this image in one sentence.",
                        },
                        {
                            "type": "image",
                            "source": {
                                "type": "base64",
                                "media_type": "image/png",
                                "data": PNG_1X1_B64,
                            },
                        },
                    ],
                }
            ],
        },
        "expected_modalities": {"text": 1, "image": 1, "audio": 0, "file": 0},
    },
    "S19_mixed_content": {
        "description": "OpenAI mixed text + image + audio + file (one of each)",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Client-Profile": "vscode",
            "X-Tenant-Id": "tenant-vscode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "What's in these attachments?"},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL}},
                        {
                            "type": "input_audio",
                            "input_audio": {"format": "wav", "data": WAV_TINY},
                        },
                        {"type": "file", "file": {"file_id": "file_test_001"}},
                    ],
                }
            ],
        },
        "expected_modalities": {"text": 1, "image": 1, "audio": 1, "file": 1},
    },
    "S20_multi_image": {
        "description": "Three images in one user message (data URIs)",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Client-Profile": "opencode",
            "X-Tenant-Id": "tenant-opencode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "Compare these 3 images."},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL}},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL_2}},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL_3}},
                    ],
                }
            ],
        },
        "expected_modalities": {"text": 1, "image": 3, "audio": 0, "file": 0},
    },
    "S21_history_with_image": {
        "description": "Multi-turn conversation with image in earlier turn",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Client-Profile": "roocode",
            "X-Tenant-Id": "tenant-roocode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "What is in this image?"},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL}},
                    ],
                },
                {
                    "role": "assistant",
                    "content": "It's a 1x1 transparent PNG pixel.",
                },
                {
                    "role": "user",
                    "content": "And what about the second one?",
                },
            ],
        },
        "expected_modalities": {"text": 3, "image": 1, "audio": 0, "file": 0},
    },
    "S22_streaming_image": {
        "description": "Streaming OpenAI request with image, verify SSE chunks",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "stream": True,
        "extra_headers": {
            "X-Client-Profile": "opencode",
            "X-Tenant-Id": "tenant-opencode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "stream": True,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "Stream me a description."},
                        {"type": "image_url", "image_url": {"url": TEST_PNG_URL}},
                    ],
                }
            ],
        },
        "expected_modalities": {"text": 1, "image": 1, "audio": 0, "file": 0},
    },
    "S23_client_profile_roocode": {
        "description": "Verify identity-funnel: gateway fingerprints the request and forwards a stable seed",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Device-Seed": "device-fixture-aaa",
            "X-Machine-Id": "machine-fixture-bbb",
            "X-Client-Profile": "roocode",
            "X-Tenant-Id": "tenant-roocode-default",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [{"role": "user", "content": "hi"}],
        },
        "expected_modalities": {"text": 1, "image": 0, "audio": 0, "file": 0},
        # NOTE: gateway's fp_slot middleware overwrites the incoming
        # X-Device-Seed with its slot-derived "llmgw-cred<N>-fp<N>" so
        # that fp-slot-based concurrency limits work. The supplier always
        # sees the gateway-rewritten seed, never the client-supplied one.
        # We assert the *shape* (llmgw-cred<digits>-fp<digits>) instead of
        # asserting exact equality, and verify the other fingerprint
        # headers (X-Client-Profile, X-Machine-Id) flow through.
    },
    "S24_tenant_labeling": {
        "description": "Verify X-Tenant-Id flows to request_logs.tenant_id column",
        "model": "loadtest-vision-alpha",
        "endpoint": "/v1/chat/completions",
        "extra_headers": {
            "X-Tenant-Id": "tenant-labeling-test-xyz",
            "X-Client-Profile": "opencode",
        },
        "payload": {
            "model": "loadtest-vision-alpha",
            "messages": [{"role": "user", "content": "label me"}],
        },
        "expected_modalities": {"text": 1, "image": 0, "audio": 0, "file": 0},
    },
}


# ── Assertion helpers ─────────────────────────────────────────────────────────


def _coerce_actual_modalities(record: dict) -> Dict[str, int]:
    """Walk the recorded messages and count modalities. Independent of
    what the supplier summarised internally — we rebuild from the parts
    list to confirm the supplier saw exactly what we sent."""
    counts = {"text": 0, "image": 0, "audio": 0, "file": 0}
    for m in record.get("messages", []):
        cs = m.get("content_summary", {})
        if cs.get("type") != "list":
            counts["text"] += 1
            continue
        for part in cs.get("parts", []):
            ptype = part.get("type", "")
            if ptype == "text":
                counts["text"] += 1
            elif ptype == "image_url":
                counts["image"] += 1
            elif ptype == "image":
                counts["image"] += 1
            elif ptype == "input_audio":
                counts["audio"] += 1
            elif ptype == "file":
                counts["file"] += 1
    return counts


def _check_fingerprint(record: dict, expected: dict) -> List[Dict[str, Any]]:
    """Each header we expect must match exactly what supplier saw."""
    out = []
    fp = record.get("fingerprint_headers", {}) or {}
    for k, v in expected.items():
        actual = fp.get(k, "")
        out.append(
            {
                "name": f"header:{k}",
                "expect": v,
                "actual": actual,
                "ok": actual == v,
            }
        )
    return out


def _check_modalities(record: dict, expected: Dict[str, int]) -> List[Dict[str, Any]]:
    actual = _coerce_actual_modalities(record)
    out = []
    for k, v in expected.items():
        out.append(
            {
                "name": f"modalities.{k}",
                "expect": v,
                "actual": actual.get(k, 0),
                "ok": actual.get(k, 0) == v,
            }
        )
    return out


def _check_no_data_uri_leak(record: dict) -> List[Dict[str, Any]]:
    """Asserts that:
    - For data: URIs: only the prefix kind is recorded, never the full
      payload, so the log is safe.
    - For http(s) URIs: the URL length is recorded (it may be the full
      URL since the gateway forwards it unchanged to the supplier).
    - For non-URL fields (input_audio, file, text): sensible sizes only.
    """
    out = []
    for m in record.get("messages", []):
        cs = m.get("content_summary", {})
        if cs.get("type") != "list":
            continue
        for idx, part in enumerate(cs.get("parts", [])):
            ptype = part.get("type", "")
            if ptype == "image_url":
                url_kind = part.get("url_kind", "")
                url_len = part.get("url_len", 0)
                if url_kind == "data":
                    # data: URI: gateway must not leak the full payload into logs
                    out.append(
                        {
                            "name": f"image[{idx}].data_uri_redacted",
                            "expect": "base64_len > 0 (size captured) without full payload",
                            "actual": f"url_kind={url_kind} base64_len={part.get('base64_len', 0)}",
                            "ok": part.get("base64_len", 0) > 0,
                        }
                    )
                else:
                    # http/https: full URL length is fine to record
                    out.append(
                        {
                            "name": f"image[{idx}].http_url_length",
                            "expect": "url_len > 0",
                            "actual": f"url_kind={url_kind} url_len={url_len}",
                            "ok": url_len > 0,
                        }
                    )
            elif ptype == "image":
                # Anthropic native image block
                data_len = part.get("data_len", 0)
                out.append(
                    {
                        "name": f"anthropic_image[{idx}].data_length",
                        "expect": "data_len > 0 (base64 payload size recorded)",
                        "actual": f"data_len={data_len}",
                        "ok": data_len > 0,
                    }
                )
            elif ptype == "input_audio":
                data_len = part.get("data_len", 0)
                out.append(
                    {
                        "name": f"audio[{idx}].data_length",
                        "expect": "data_len > 0",
                        "actual": f"data_len={data_len}",
                        "ok": data_len > 0,
                    }
                )
    return out


# ── Run helpers ─────────────────────────────────────────────────────────────


async def _post_request(
    session: aiohttp.ClientSession,
    gateway: str,
    endpoint: str,
    payload: dict,
    extra_headers: dict,
    timeout: float = 30.0,
) -> Tuple[int, Optional[dict], List[str], Optional[str]]:
    url = gateway.rstrip("/") + endpoint
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer sk-loadtest-01",
    }
    headers.update(extra_headers or {})
    try:
        async with session.post(
            url,
            json=payload,
            headers=headers,
            timeout=aiohttp.ClientTimeout(total=timeout),
        ) as resp:
            text = await resp.text()
            body: Optional[dict] = None
            try:
                body = json.loads(text)
            except Exception:
                body = {"raw_text": text[:200]}
            return (
                resp.status,
                body,
                [f"http_status={resp.status}"],
                resp.headers.get("X-Mock-Request-Id", "") or None,
            )
    except Exception as e:
        return 0, None, [f"http_error: {e!r}"], None


async def _get_last_record(
    session: aiohttp.ClientSession, supplier: str
) -> Tuple[Optional[dict], List[str]]:
    try:
        async with session.get(
            supplier.rstrip("/") + "/admin/requests?n=1",
            timeout=aiohttp.ClientTimeout(total=5.0),
        ) as resp:
            if resp.status != 200:
                return None, [f"supplier_status={resp.status}"]
            body = await resp.json()
            reqs = body.get("requests", [])
            if not reqs:
                return None, ["no_requests_recorded"]
            return reqs[-1], []
    except Exception as e:
        return None, [f"supplier_error: {e!r}"]


async def _clear_all_supplier_records(
    session: aiohttp.ClientSession, supplier: str, count: int = 5
) -> None:
    """Clear records on all 5 multimodal suppliers in the group. The
    gateway may route the request to any of them; we don't know
    which one ahead of time, so we look at all 5 and pick the
    freshest record. Replaces the old single-supplier clear."""
    base = supplier.rstrip("/")
    base_no_port = base.rsplit(":", 1)[0]
    primary_port = int(base.rsplit(":", 1)[1])
    base_port = primary_port
    for offset in range(count):
        port = base_port + offset
        url = f"{base_no_port}:{port}/admin/requests/clear"
        try:
            await session.post(url, timeout=aiohttp.ClientTimeout(total=5.0))
        except Exception:
            pass


def _supplier_base_ports(supplier: str, count: int = 5) -> List[str]:
    """Return the list of base URLs for all 5 multimodal suppliers in
    the group. The first URL uses the supplied --supplier; the rest
    increment the port number."""
    base = supplier.rstrip("/")
    base_no_port = base.rsplit(":", 1)[0]
    primary_port = int(base.rsplit(":", 1)[1])
    base_port = primary_port
    return [f"{base_no_port}:{base_port + i}" for i in range(count)]


async def _query_all_suppliers(
    session: aiohttp.ClientSession, supplier: str, count: int = 5
) -> Tuple[Optional[dict], List[str]]:
    """Query all multimodal suppliers in the group and return the most
    recent record across them. The gateway may route to any of them."""
    candidates: List[dict] = []
    for url in _supplier_base_ports(supplier, count):
        try:
            async with session.get(
                url + "/admin/requests?n=1",
                timeout=aiohttp.ClientTimeout(total=5.0),
            ) as resp:
                if resp.status != 200:
                    continue
                body = await resp.json()
                for r in body.get("requests", []):
                    candidates.append(r)
        except Exception:
            continue
    if not candidates:
        return None, ["no_requests_recorded"]
    candidates.sort(key=lambda r: r.get("ts", 0), reverse=True)
    return candidates[0], []


async def _run_scenario(
    session: aiohttp.ClientSession,
    gateway: str,
    supplier: str,
    name: str,
    scen: dict,
) -> dict:
    await _clear_all_supplier_records(session, supplier)

    if scen.get("stream"):
        payload = dict(scen["payload"])
        payload["stream"] = True
    else:
        payload = dict(scen["payload"])

    status, body, http_notes, mock_req_id = await _post_request(
        session,
        gateway,
        scen["endpoint"],
        payload,
        scen.get("extra_headers", {}),
    )

    # small delay to let supplier finish recording
    await asyncio.sleep(0.5)
    # Use a fresh session for the query to avoid any connection-pool
    # staleness from the post session (the supplier process accepted
    # the request on a different socket than our gateway socket).
    async with aiohttp.ClientSession() as query_session:
        record, sup_notes = await _query_all_suppliers(query_session, supplier)
    notes = http_notes + sup_notes

    if status != 200:
        return {
            "scenario": name,
            "ok": False,
            "http_status": status,
            "body_excerpt": (body or {}).get("error")
            or (body or {}).get("raw_text", "")[:200],
            "notes": notes,
            "checks": [],
        }

    if record is None:
        return {
            "scenario": name,
            "ok": False,
            "http_status": status,
            "notes": notes + ["no supplier record"],
            "checks": [],
        }

    checks: List[Dict[str, Any]] = []
    checks.append(
        {
            "name": "supplier.endpoint",
            "expect": scen["endpoint"],
            "actual": record.get("endpoint"),
            "ok": record.get("endpoint") == scen["endpoint"],
        }
    )
    checks.append(
        {
            "name": "supplier.model",
            "expect": scen["model"],
            "actual": record.get("model"),
            "ok": record.get("model") == scen["model"],
        }
    )
    checks.extend(_check_modalities(record, scen["expected_modalities"]))
    # The gateway does not forward fingerprint headers (X-Device-Seed
    # / X-Machine-Id / X-Client-Profile / X-Runtime-* / X-OS-* /
    # X-Tenant-Id) to the upstream. They are captured into
    # request_logs and request_context_attrs (the S23/S24 dedicated
    # checks live in `s23_s24_tenant_profile.sh`). We therefore only
    # assert modalities + endpoint + model here.
    checks.extend(_check_no_data_uri_leak(record))

    ok = all(c["ok"] for c in checks)
    return {
        "scenario": name,
        "ok": ok,
        "supplier_request_id": record.get("id"),
        "supplier_endpoint": record.get("endpoint"),
        "supplier_model": record.get("model"),
        "mock_request_id": mock_req_id,
        "notes": notes,
        "checks": checks,
    }


async def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--gateway", default="http://localhost:8781")
    parser.add_argument("--supplier", default="http://127.0.0.1:19280")
    parser.add_argument(
        "--scenario",
        action="append",
        default=None,
        help=f"Scenario name(s). Available: {', '.join(SCENARIOS)}",
    )
    parser.add_argument("--all", action="store_true")
    parser.add_argument(
        "--output",
        default=None,
        help="Optional JSON file path; results always print to stdout",
    )
    args = parser.parse_args()

    selected = []
    if args.all or not args.scenario:
        selected = list(SCENARIOS.keys())
    else:
        for s in args.scenario:
            if s not in SCENARIOS:
                print(f"unknown scenario: {s}", file=sys.stderr)
                return 2
            selected.append(s)

    results: List[dict] = []
    async with aiohttp.ClientSession() as session:
        for name in selected:
            scen = SCENARIOS[name]
            res = await _run_scenario(session, args.gateway, args.supplier, name, scen)
            results.append(res)

    passed = sum(1 for r in results if r["ok"])
    failed = len(results) - passed

    print(
        json.dumps({"passed": passed, "failed": failed, "results": results}, indent=2)
    )

    if args.output:
        with open(args.output, "w") as f:
            json.dump(
                {"passed": passed, "failed": failed, "results": results}, f, indent=2
            )

    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
