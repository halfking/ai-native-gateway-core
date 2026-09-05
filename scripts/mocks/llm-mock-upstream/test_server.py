import importlib.util
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from aiohttp import ClientSession
from aiohttp.test_utils import AioHTTPTestCase


SERVER_PATH = Path(__file__).with_name("server.py")


def load_server_module():
    state_file = Path(tempfile.gettempdir()) / f"llm-mock-test-{os.getpid()}.json"
    state_file.unlink(missing_ok=True)
    original = os.environ.get("MOCK_STATE_FILE")
    os.environ["MOCK_STATE_FILE"] = str(state_file)
    os.environ["MOCK_LATENCY_MS_MIN"] = "0"
    os.environ["MOCK_LATENCY_MS_MAX"] = "0"
    try:
        spec = importlib.util.spec_from_file_location("llm_mock_server_test", SERVER_PATH)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module
    finally:
        if original is None:
            os.environ.pop("MOCK_STATE_FILE", None)
        else:
            os.environ["MOCK_STATE_FILE"] = original
        os.environ.pop("MOCK_LATENCY_MS_MIN", None)
        os.environ.pop("MOCK_LATENCY_MS_MAX", None)


SERVER = load_server_module()


class MockServerTest(AioHTTPTestCase):
    async def get_application(self):
        return SERVER.build_app()

    async def post_completion(self, **overrides):
        payload = {
            "model": "gpt-4o-mini",
            "messages": [
                {
                    "role": "user",
                    "content": (
                        "Transcript:\n\n"
                        "[body_ref: internal://body/req-42/prompt] (user)\n"
                        "I prefer concise answers.\n\n"
                    ),
                }
            ],
        }
        payload.update(overrides)
        async with ClientSession() as session:
            async with session.post(
                f"{self.server.make_url('/v1/chat/completions')}", json=payload
            ) as response:
                return response.status, await response.json()

    async def test_fact_mode_returns_grounded_openai_compatible_completion(self):
        with patch.dict(os.environ, {"MOCK_FACT_EXTRACTOR": "1"}):
            status, response = await self.post_completion(
                response_format={"type": "json_object"}
            )

        self.assertEqual(status, 200)
        content = json.loads(response["choices"][0]["message"]["content"])
        self.assertEqual(len(content["facts"]), 1)
        fact = content["facts"][0]
        self.assertGreater(fact["confidence"], 0.55)
        self.assertEqual(fact["body_ref"], "internal://body/req-42/prompt")
        self.assertEqual(fact["quote"], "I prefer concise answers.")
        self.assertEqual(fact["privacy"], "normal")

    async def test_fact_mode_does_not_cross_body_boundaries(self):
        payload = {
            "model": "gpt-4o-mini",
            "messages": [{
                "role": "user",
                "content": (
                    "Transcript:\n\n"
                    "[body_ref: internal://body/req-1/prompt] (user)\n"
                    "first fact\n\n"
                    "[body_ref: internal://body/req-2/prompt] (user)\n"
                    "second fact\n\n"
                ),
            }],
            "response_format": {"type": "json_object"},
        }
        with patch.dict(os.environ, {"MOCK_FACT_EXTRACTOR": "1"}):
            async with ClientSession() as session:
                async with session.post(
                    f"{self.server.make_url('/v1/chat/completions')}", json=payload
                ) as response:
                    result = await response.json()
        self.assertEqual(result["choices"][0]["message"]["content"],
                         '{"facts":[{"body_ref":"internal://body/req-1/prompt","confidence":0.9,"fact_type":"statement","privacy":"normal","quote":"first fact","statement":"The conversation includes this statement: first fact","subject":"conversation statement"}]}')

    async def test_echo_remains_default_and_json_gate_is_required(self):
        status, response = await self.post_completion()
        self.assertEqual(status, 200)
        self.assertEqual(
            response["choices"][0]["message"]["content"],
            "echo: Transcript:\n\n[body_ref: internal://body/req-42/prompt] (user)\nI prefer concise a",
        )

        with patch.dict(os.environ, {"MOCK_FACT_EXTRACTOR": "1"}):
            status, response = await self.post_completion(
                response_format={"type": "text"}
            )
        self.assertEqual(status, 200)
        self.assertTrue(response["choices"][0]["message"]["content"].startswith("echo: "))


if __name__ == "__main__":
    unittest.main()
