#!/usr/bin/env python3
"""154 网关 · 三协议还原路径矩阵验证。

gateway 的响应侧还原有三份彼此独立的实现（security/sanitize）：
  * smart_sani_guard.go   —— 传统 chat.completions（choices-only restorer）
  * native_restore.go     —— Anthropic Messages（type=message）
  * native_restore.go     —— OpenAI Responses（object=response）

只测 chat/completions 会漏掉后两份。本脚本按协议分别打真值入向、
检查出向占位符、客户端还原、以及占位符零泄漏。

用法：python3 protocol_matrix.py [messages|responses|chat|tools|all]
"""
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

GW = "http://47.97.111.154:8782"
RSQL = "/tmp/sanitest/rsql.sh"
HOST = "154"


def env(key):
    for line in open("/tmp/154env.sh", encoding="utf-8", errors="replace"):
        if line.startswith(key + "="):
            return line.split("=", 1)[1].strip()
    return ""


API_KEY = env("LLM_GATEWAY_API_KEY")
MODEL = "minimax-m3"

# 真值：刻意用带中缀连字符的 key（audit 244 F2 修复的目标形态），
# 其余用常规 PII，确保一次请求同时覆盖多条规则。
PHONE = "13900001111"
EMAIL = "li.si@example.org"
IDCARD = "110101199003078888"
# 故意不放 API key：audit 244 F2 的修复尚未部署到生产二进制，
# 带着它跑会把"未部署"混进判读。这里只验 PII 三类，
# 密钥维度的验证由 keymatrix 与 scripts/sanitize-e2e-154.sh 负责。
KEY = ""
SECRET_TEXT = f"我的手机号是 {PHONE}，邮箱 {EMAIL}，身份证 {IDCARD}，密钥 {KEY}。"


def sql(q):
    out = subprocess.run(["bash", RSQL, q], capture_output=True, text=True)
    return out.stdout.strip()


def post(path, payload, session):
    req = urllib.request.Request(
        GW + path, data=json.dumps(payload).encode(),
        headers={"Authorization": "Bearer " + API_KEY,
                 "Content-Type": "application/json",
                 "X-Session-ID": session})
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:
        return {"_http_error": e.code, "_body": e.read().decode("utf-8", "replace")[:400]}
    except Exception as e:  # noqa: BLE001
        return {"_error": str(e)}


def wait_for_outbound_body(session, tries=20, delay=2.0):
    """轮询等待该会话「已落库且 outbound_body 非空」的请求行。

    两个独立的异步点都会造成假阴性：
      1. _request_logs_hot 的行本身滞后于响应（实测 3~5s）；
      2. _request_logs_bodies_hot 的 outbound_body 又滞后于第 1 步，且
         并非每个请求都存 body —— 只等第 1 点会拿到 outbound_body IS NULL
         的行，于是「有没有占位符」被判成 nobody（读作缺失而非否）。
    必须查 _hot：_request_logs 是分区父表，热数据只落在 _request_logs_hot。
    """
    for _ in range(tries):
        rid = sql(f"select h.request_id from _request_logs_hot h "
                  f"join _request_logs_bodies_hot b on b.request_id = h.request_id "
                  f"where h.gw_session_id like 'gw_{session}' "
                  f"and b.outbound_body is not null "
                  f"order by h.ts desc limit 1")
        if rid:
            return rid
        time.sleep(delay)
    # 退化：拿任意一行，好让调用方至少能报告"无出向正文"而非"无请求行"
    return sql(f"select request_id from _request_logs_hot "
               f"where gw_session_id like 'gw_{session}' order by ts desc limit 1")


def probe(label, path, payload, expect_shapes, session=None):
    """打一次请求，取证出向/客户端两侧。"""
    session = session or f"pm-{label}-{int(time.time())}"
    resp = post(path, payload, session)
    rid = wait_for_outbound_body(session)
    if not rid:
        print(f"{label:<10} NO_REQUEST_ROW  resp={json.dumps(resp, ensure_ascii=False)[:150]}")
        return False

    # 约 30% 的请求按 body 保留策略不落 outbound_body（实测近 30 分钟
    # 144/502）。那是「没存」而不是「没脱敏」，不能判失败 —— 此时退化为
    # 只用客户端侧断言，并把取证缺口如实标出来。
    has_body = sql(f"select coalesce((outbound_body is not null)::text,'false') "
                   f"from _request_logs_bodies_hot where request_id='{rid}'") == "true"

    def outbound_has(pat):
        if not has_body:
            return "n/a"
        return sql(f"select coalesce((outbound_body::text like '%{pat}%')::text,'nobody') "
                   f"from _request_logs_bodies_hot where request_id='{rid}'")

    pairs = [("phone", PHONE), ("email", EMAIL), ("idcard", IDCARD)]
    if KEY:
        pairs.append(("key", KEY))
    leaks = [name for name, val in pairs if outbound_has(val) == "true"]
    ph = outbound_has("SENSITIVE")

    # 客户端：响应里不能残留占位符。这一条在任何情况下都必须成立。
    body = json.dumps(resp, ensure_ascii=False)
    client_leak_ph = "SENSITIVE:" in body
    client_ok = resp and "_error" not in resp and "_http_error" not in resp

    ok = (not leaks) and not client_leak_ph and client_ok
    if has_body:
        # 有出向正文就必须看到占位符，否则说明入向根本没脱敏
        ok = ok and ph == "true"

    print(f"{label:<10} {'PASS' if ok else 'FAIL'}  "
          f"outbound_body={'yes' if has_body else 'NOT-STORED'}  "
          f"outbound_placeholder={ph}  outbound_leaks={leaks or 'none'}  "
          f"client_placeholder_leak={client_leak_ph}")
    if not ok:
        print(f"           shapes_expected={expect_shapes}")
        print(f"           resp={body[:220]}")
    if not has_body:
        print("           ↳ 该请求未落 outbound_body（body 保留策略），"
              "本例只验客户端侧；出向取证请重跑至命中存 body 的请求")
    return ok


def case_messages():
    """Anthropic Messages：type=message 形状，走 restoreNativeMessagesBody。"""
    return probe(
        "messages", "/v1/messages",
        {"model": MODEL, "max_tokens": 256,
         "messages": [{"role": "user", "content": SECRET_TEXT + " 请回复收到。"}]},
        ["type=message"])


def case_responses():
    """OpenAI Responses：object=response 形状，走 restoreNativeResponsesBody。"""
    return probe(
        "responses", "/v1/responses",
        {"model": MODEL,
         "input": [{"role": "user",
                    "content": [{"type": "input_text", "text": SECRET_TEXT + " 请回复收到。"}]}]},
        ["object=response"])


def case_chat():
    """传统 chat.completions，choices-only restorer + 流式路径。"""
    ok = probe(
        "chat", "/v1/chat/completions",
        {"model": MODEL, "stream": False,
         "messages": [{"role": "user", "content": SECRET_TEXT + " 请回复收到。"}]},
        ["choices"])

    # 流式：跨 chunk 还原走另一段代码（restoreStreamChunk）
    session = f"pm-chatstream-{int(time.time())}"
    req = urllib.request.Request(
        GW + "/v1/chat/completions",
        data=json.dumps({"model": MODEL, "stream": True,
                         "messages": [{"role": "user",
                                       "content": SECRET_TEXT + " 请回复收到。"}]}).encode(),
        headers={"Authorization": "Bearer " + API_KEY,
                 "Content-Type": "application/json", "X-Session-ID": session})
    try:
        with urllib.request.urlopen(req, timeout=180) as r:
            raw = r.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001
        print(f"{'chatstream':<10} FAIL  request error: {e}")
        return False
    text = "".join(_stream_text(raw))
    leak_ph = "SENSITIVE:" in text
    verdict = "PASS" if not leak_ph and text.strip() else "FAIL"
    print(f"{'chatstream':<10} {verdict}  "
          f"chars={len(text)}  client_placeholder_leak={leak_ph}  text={text[:80]!r}")
    if leak_ph:
        print("           ↳ 这是 smart_sani_guard.go 的已知未部署缺陷：模型把 "
              "占位符改写成 '(SENSITIVE:' 并跨 chunk 下发时，\n"
              "             缓冲逻辑认不出 '(' 开头。修复已就绪（parenMarkerStart），\n"
              "             需随二进制部署后复验。")
    return (not leak_ph) and bool(text.strip())


def _stream_text(raw):
    out = []
    for line in raw.splitlines():
        if not line.startswith("data: "):
            continue
        payload = line[6:].strip()
        if payload == "[DONE]":
            continue
        try:
            obj = json.loads(payload)
        except json.JSONDecodeError:
            continue
        for ch in obj.get("choices", []) or []:
            d = ch.get("delta") or {}
            out.append(d.get("content") or "")
            out.append(d.get("reasoning_content") or "")
    return out


def case_tools():
    """工具调用：工具参数里的敏感值走 sanitizeToolCalls + unsafeToolValue。

    要真正走到 tool_calls 分支，不能指望模型去复述占位符——实测 minimax-m3
    见到脱敏标记就拒绝发起调用（三次尝试全部 finish_reason=stop），那是模型
    的防护策略，不是网关缺陷。所以拆成两段：

      1) 工具 schema 与入参声明完整走一遍 sanitizeToolCalls，验证出向脱敏；
      2) 用一个不含敏感值的独立请求，确保 tool_calls 分支真的被执行到，
         验证响应侧 unsafeToolValue 的放行/拦截判定。

    两段的出向证据都单独取证。
    """
    ok_schema = case_tools_sanitized_args()
    ok_call = case_tools_call_path()
    return ok_schema and ok_call


def case_tools_sanitized_args():
    """入参声明含真实 PII：出向必须只剩占位符。"""
    session = f"pm-toolsschema-{int(time.time())}"
    payload = {
        "model": MODEL, "stream": False,
        "messages": [{"role": "user",
                      "content": f"把这段登记到系统：手机号 {PHONE}，邮箱 {EMAIL}。"}],
        "tools": [{"type": "function", "function": {
            "name": "save_contact",
            "description": "保存联系人信息",
            "parameters": {"type": "object", "properties": {
                "phone": {"type": "string"},
                "email": {"type": "string"}},
                "required": ["phone"]}}}],
        "tool_choice": {"type": "function",
                        "function": {"name": "save_contact"}}}
    resp = post("/v1/chat/completions", payload, session)
    rid = wait_for_outbound_body(session)
    has_body = bool(rid) and sql(
        f"select coalesce((outbound_body is not null)::text,'false') "
        f"from _request_logs_bodies_hot where request_id='{rid}'") == "true"

    leaks, ph = [], "n/a"
    if rid and has_body:
        for name, val in (("phone", PHONE), ("email", EMAIL)):
            if sql(f"select coalesce((outbound_body::text like '%{val}%')::text,'nobody') "
                   f"from _request_logs_bodies_hot where request_id='{rid}'") == "true":
                leaks.append(name)
        ph = sql(f"select coalesce((outbound_body::text like '%SENSITIVE%')::text,'nobody') "
                 f"from _request_logs_bodies_hot where request_id='{rid}'")

    body = json.dumps(resp, ensure_ascii=False)
    client_leak_ph = "SENSITIVE:" in body
    ok = not leaks and not client_leak_ph
    if has_body:
        ok = ok and ph == "true"
    print(f"{'tools/args':<12} {'PASS' if ok else 'FAIL'}  "
          f"outbound_body={'yes' if has_body else 'NOT-STORED'}  "
          f"outbound_placeholder={ph}  leaks={leaks or 'none'}  "
          f"client_placeholder_leak={client_leak_ph}")
    if not has_body:
        print("             ↳ 该请求未落 outbound_body（保留策略），本例只验客户端侧")
    return ok


def case_tools_call_path():
    """不含敏感值的工具调用：确保 tool_calls 分支真的被执行。"""
    session = f"pm-toolscall-{int(time.time())}"
    payload = {
        "model": MODEL, "stream": False,
        "messages": [{"role": "user",
                      "content": "请调用 save_contact 工具保存联系人，姓名字段填 Alex。"}],
        "tools": [{"type": "function", "function": {
            "name": "save_contact",
            "description": "保存联系人信息",
            "parameters": {"type": "object", "properties": {
                "name": {"type": "string"},
                "phone": {"type": "string"}},
                "required": ["name"]}}}],
        "tool_choice": {"type": "function",
                        "function": {"name": "save_contact"}}}
    resp = post("/v1/chat/completions", payload, session)
    body = json.dumps(resp, ensure_ascii=False)
    has_tool_calls = '"tool_calls"' in body
    client_leak_ph = "SENSITIVE:" in body
    ok = has_tool_calls and not client_leak_ph and "_error" not in resp
    print(f"{'tools/call':<12} {'PASS' if ok else 'FAIL'}  "
          f"tool_calls={has_tool_calls}  client_placeholder_leak={client_leak_ph}")
    if not ok:
        print(f"             resp={body[:220]}")
    return ok


CASES = {"messages": case_messages, "responses": case_responses,
         "chat": case_chat, "tools": case_tools}

if __name__ == "__main__":
    which = sys.argv[1:] or ["all"]
    names = list(CASES) if which == ["all"] else which
    results = {}
    for n in names:
        try:
            results[n] = CASES[n]()
        except Exception as e:  # noqa: BLE001
            print(f"{n:<10} ERROR  {e}")
            results[n] = False
    print("\n" + "=" * 46)
    for n, ok in results.items():
        print(f"  {'PASS' if ok else 'FAIL'}  {n}")
    print("=" * 46)
    sys.exit(0 if all(results.values()) else 1)
