# tests/local/mock-client/scenarios.py
#
# 各种"真实业务"请求模板 — 由 client.py 随机采样作为请求体。
# 用法：
#   from scenarios import pick_scenario
#   scen = pick_scenario()
#   body = scen.build(client_id="local-test-1")

import hashlib
import json
import random
import time


MODELS = [
    "minimax-m3",
    "gpt-5.6-luna",
    "gpt-5.6-terra",
    "claude-sonnet-5",
    "gpt-4o",
    "chaos-503",  # 上游 503
    "chaos-429",  # rate limit
    "chaos-slow",  # 慢响应
    "chaos-very_slow",  # 极慢
    "chaos-mid_drop",  # 流中断
    "chaos-drop_done",  # 流缺 [DONE]
    "chaos-tool_bug",  # tool_call_id_mismatch
]

# 大约 80% 流量用真实模型、20% 用 chaos 模型（可调）
CHAOS_PROBABILITY = 0.20


def pick_model() -> str:
    if random.random() < CHAOS_PROBABILITY:
        return random.choice([m for m in MODELS if m.startswith("chaos-")])
    return random.choice([m for m in MODELS if not m.startswith("chaos-")])


# ── 请求场景模板 ─────────────────────────────────────────────────────────────


def single_turn(model: str) -> dict:
    """最简单的单轮请求。"""
    return {
        "model": model,
        "messages": [
            {"role": "user", "content": f"hello mock — {time.time_ns()} — please pong"},
        ],
        "max_tokens": random.choice([15, 30, 50]),
        "stream": False,
    }


def multi_turn(model: str) -> dict:
    """多轮会话 — 触发 session_cache / session_compression。"""
    messages = []
    n_turns = random.randint(2, 6)
    for i in range(n_turns):
        messages.append({"role": "user", "content": f"turn {i}: ask mock to do {i}"})
        if i < n_turns - 1:
            messages.append({"role": "assistant", "content": f"reply {i}: done"})
    messages.append({"role": "user", "content": "final ask"})
    return {
        "model": model,
        "messages": messages,
        "max_tokens": 50,
        "stream": False,
    }


def streaming(model: str) -> dict:
    """流式 — 触发 fp_slot / stream_interrupted / drop_done 路径。"""
    return {
        "model": model,
        "messages": [{"role": "user", "content": "stream me a long reply please"}],
        "max_tokens": 80,
        "stream": True,
    }


def long_context(model: str) -> dict:
    """长上下文 — 触发 session_cache L1 淘汰 + auto_route_probe。"""
    big = "B" * random.choice([1000, 5000, 20000])
    return {
        "model": model,
        "messages": [
            {"role": "system", "content": f"long context filler {big[:1000]}"},
            {"role": "user", "content": "summarize the filler"},
        ],
        "max_tokens": 80,
        "stream": False,
    }


def tool_call(model: str) -> dict:
    """工具调用 — 触发 tool_call_id_mismatch / client_bug 路径。"""
    return {
        "model": model,
        "messages": [
            {"role": "user", "content": "read /etc/passwd and report size"},
        ],
        "max_tokens": 50,
        "stream": False,
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "read",
                    "parameters": {
                        "type": "object",
                        "properties": {"path": {"type": "string"}},
                        "required": ["path"],
                    },
                },
            }
        ],
        "tool_choice": "auto",
    }


def session_follow_up(model: str, sid: str) -> dict:
    """会话内 follow-up — 触发 handoff / session_cache。"""
    n = random.randint(10, 25)
    msgs = []
    for i in range(n):
        msgs.append(
            {"role": "user", "content": f"q{i}: random {random.randint(1000, 9999)}"}
        )
        msgs.append({"role": "assistant", "content": f"a{i}: yes"})
    return {
        "model": model,
        "messages": msgs + [{"role": "user", "content": "final"}],
        "max_tokens": 50,
        "stream": False,
    }


SCENARIOS_WEIGHTS = [
    (single_turn, 50),
    (multi_turn, 15),
    (streaming, 10),
    (long_context, 8),
    (tool_call, 5),
]


def pick_scenario():
    """按权重采样场景。返回 (fn, args_dict) — fn 是构造器，args 是绑定值。"""
    funcs, weights = zip(*SCENARIOS_WEIGHTS)
    fn = random.choices(funcs, weights=weights)[0]
    return fn


def sample_turn(client_id: str, sessions: dict) -> dict:
    """对一个 client + 一个 session 抽样一次请求体。

    sessions: dict[str, list[dict]] — client 维护的 session pool（limited_size）。
    调用方负责维护 session 入参；本函数只是抽样，不会修改它。
    """
    model = pick_model()

    # 30% 概率走 session follow-up（如果有现成 session）
    if sessions and random.random() < 0.30:
        sid = random.choice(list(sessions.keys()))
        return session_follow_up(model, sid)

    return pick_scenario()(model)
