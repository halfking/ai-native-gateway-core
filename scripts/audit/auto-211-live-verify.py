#!/usr/bin/env python3
"""auto-211-live-verify.py —— 在 211 本机验证 auto 模式的**三协议面可用性**与
**模型级回退开关**（2026-11-16 auto 模式可用性轮）。

零外部依赖；密钥与 token 都不落盘、不回显：
  · admin JWT 用容器内的 LLM_GATEWAY_SECRET_KEY 现签；
  · 临时 API key 由 admin API 现场签发，验完**立即吊销**。

验的三件事：
  1. `dispatch_v2.allow_model_change` 闸门在在役实例上是**开**的
     （读 /api/admin/settings 的 effective 值；也可从启动日志核对）。
  2. `model="auto"` 在 **/v1/chat/completions** 上被解析（响应头带
     X-Gw-Auto-Decision）。
  3. `model="auto"` 在 **/v1/messages**（Anthropic 协议）与 **/v1/responses**
     上**也被解析** —— 这是本轮 F2 的修复点：修前这两个协议面把 "auto" 当成
     一个真实模型名去路由 ⇒ 零候选 ⇒ 客户端看到「模型 auto 不可用」。

用法（在 211 上执行）:
  python3 auto-211-live-verify.py
输出: 每项一行 PASS/FAIL + 关键证据；退出码 0 = 全过。
"""
import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"
PORTS = ("8781", "8783")


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def running_container() -> str:
    out = subprocess.run(
        ["docker", "ps", "--format", "{{.Names}}"], capture_output=True, text=True, check=False
    ).stdout.split()
    for name in CONTAINER_CANDIDATES:
        if name in out:
            return name
    raise SystemExit("没有在跑的 llm-gateway-211-* 容器")


def secret_key() -> str:
    return subprocess.check_output(
        ["docker", "exec", running_container(), "printenv", "LLM_GATEWAY_SECRET_KEY"]
    ).decode().strip()


def mint_token(secret: str, ttl: int = 3600) -> str:
    header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    now = int(time.time())
    payload = _b64(json.dumps({
        "user_id": USER_ID, "tenant_id": TENANT, "username": USERNAME, "role": ROLE,
        "exp": now + ttl, "iat": now, "iss": "llm-gateway", "aud": ["llm-gateway-api"],
    }, separators=(",", ":")).encode())
    sig = _b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def admin_base(port: str) -> str:
    return f"http://127.0.0.1:{port}"


def active_port() -> str:
    """活跃槽 = nginx upstream 里的那个端口；读不到就按容器在跑的槽推。"""
    try:
        out = subprocess.run(
            ["docker", "exec", running_container(), "sh", "-c",
             "cat /etc/nginx/conf.d/ns-gateway-211.conf 2>/dev/null || true"],
            capture_output=True, text=True, check=False,
        ).stdout
        for line in out.splitlines():
            if "server 127.0.0.1:" in line:
                return line.split("server 127.0.0.1:")[1].split(";")[0].strip()
    except Exception:  # noqa: BLE001
        pass
    for port in PORTS:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=5).read()
            return port
        except Exception:  # noqa: BLE001
            continue
    return PORTS[0]


def http(method: str, url: str, *, headers=None, body=None, timeout=90):
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode()
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, dict(resp.headers), resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers or {}), exc.read().decode("utf-8", "replace")
    except Exception as exc:  # noqa: BLE001
        return 0, {}, f"ERROR {exc}"


def main() -> int:
    secret = secret_key()
    token = mint_token(secret)
    port = active_port()
    base = admin_base(port)
    auth = {"Authorization": f"Bearer {token}"}
    failures = []

    print(f"# 容器 {running_container()} · 活跃端口 {port}")

    # ── 1. 闸门：dispatch_v2.allow_model_change 必须是开 ────────────────────
    status, _, text = http("GET", f"{base}/api/admin/settings/dispatch_v2.allow_model_change",
                            headers=auth)
    gate_ok = False
    if status == 200:
        try:
            payload = json.loads(text)
            value = payload.get("value", payload.get("effective_value"))
            gate_ok = value in (True, "true", "True")
            print(f"[1] allow_model_change = {value!r} (HTTP {status})")
        except Exception:  # noqa: BLE001
            print(f"[1] allow_model_change 解析失败 (HTTP {status}): {text[:200]}")
    else:
        print(f"[1] 取设置失败 HTTP {status}: {text[:200]}")
    if not gate_ok:
        failures.append("dispatch_v2.allow_model_change 未开启")
    else:
        print("    PASS")

    # ── 2. 临时 key ────────────────────────────────────────────────────────
    # key_alias 在租户+应用维度必须唯一 ⇒ 用时间戳后缀，避免与既有别名撞车。
    alias = f"auto-live-verify-{int(time.time())}"
    status, _, text = http("POST", f"{base}/api/keys", headers=auth, body={
        "application_code": "default",
        "tenant_id": TENANT,
        "key_alias": alias,
        "remark": "auto 模式可用性轮（2026-11-16）临时验收 key，验后即吊销",
    })
    if status not in (200, 201):
        print(f"[2] 临时 key 签发失败 HTTP {status}: {text[:300]}")
        failures.append("临时 key 签发失败")
        return 1
    try:
        key_payload = json.loads(text)
    except Exception:  # noqa: BLE001
        print(f"[2] 临时 key 响应不可解析: {text[:300]}")
        failures.append("临时 key 响应不可解析")
        return 1
    key_id = key_payload.get("id") or (key_payload.get("data") or {}).get("id")
    if not key_id:
        print(f"[2] 响应里没有 key id: {text[:300]}")
        failures.append("响应里没有 key id")
        return 1

    status, _, text = http("GET", f"{base}/api/keys/{key_id}/reveal", headers=auth)
    api_key = ""
    try:
        reveal = json.loads(text)
        api_key = reveal.get("key") or reveal.get("api_key") or (reveal.get("data") or {}).get("key", "")
    except Exception:  # noqa: BLE001
        api_key = ""
    if not api_key:
        print(f"[2] 取完整 key 失败 HTTP {status}: {text[:300]}")
        failures.append("取完整 key 失败")
        return 1
    print(f"[2] 临时 key 已签发 id={key_id}")

    try:
        # ── 3. 三个协议面的 model="auto" ──────────────────────────────────
        cases = [
            ("chat", "/v1/chat/completions", {
                "model": "auto",
                "messages": [{"role": "user", "content": "回复一个词：ok"}],
                "max_tokens": 16,
            }),
            ("messages", "/v1/messages", {
                "model": "auto",
                "max_tokens": 16,
                "messages": [{"role": "user", "content": "回复一个词：ok"}],
            }),
            ("responses", "/v1/responses", {
                "model": "auto",
                "input": "回复一个词：ok",
                "max_output_tokens": 16,
            }),
        ]
        for label, path, payload in cases:
            status, headers, text = http("POST", f"{base}{path}",
                                        headers={"Authorization": f"Bearer {api_key}"},
                                        body=payload, timeout=120)
            decision = headers.get("X-Gw-Auto-Decision") or headers.get("x-gw-auto-decision") or ""
            resolved = ""
            if decision:
                try:
                    resolved = json.loads(decision).get("chosen_model", "")
                except Exception:  # noqa: BLE001
                    resolved = "(决策头不可解析)"
            ok = status == 200 and bool(decision)
            detail = f"HTTP {status}"
            if resolved:
                detail += f" · 解析到模型 {resolved}"
            elif text:
                detail += f" · 响应片段 {text[:160]}"
            print(f"[3] {label:9s} model=auto → {detail}")
            if ok:
                print("    PASS")
            else:
                failures.append(f"{label} 协议面 model=auto 未被解析")
    finally:
        # ── 4. 吊销临时 key ────────────────────────────────────────────────
        http("DELETE", f"{base}/api/keys/{key_id}", headers=auth)
        print(f"[4] 临时 key {key_id} 已吊销")

    # ── 5. 零候选重选是否真的跑过（容器日志） ──────────────────────────────
    # 修前：auto 选中的模型零候选 ⇒ handler 当场 503（no_candidates），
    # 客户端只看到「原来的模型不可用」。修后应当出现重选记录，或者
    # 压根没触发零候选（决策侧已不再选死模型 —— 根修 6 的效果）。
    try:
        logs = subprocess.run(
            ["docker", "logs", "--since", "20m", running_container()],
            capture_output=True, text=True, check=False,
        ).stdout + subprocess.run(
            ["docker", "logs", "--since", "20m", running_container()],
            capture_output=True, text=True, check=False,
        ).stderr
        switched = [ln for ln in logs.splitlines() if "switched model after zero candidates" in ln]
        print(f"[5] 零候选重选记录（近 20 分钟）：{len(switched)} 条")
        for ln in switched[-3:]:
            print("    " + ln[:300])
        no_cand = [ln for ln in logs.splitlines() if "no_candidates" in ln]
        print(f"    同窗内 no_candidates 出现 {len(no_cand)} 次")
    except Exception as exc:  # noqa: BLE001
        print(f"[5] 读容器日志失败：{exc}")

    # ── 6. 真库里最近的 auto 请求（决策模型 / 结果 / 失败码） ───────────────
    try:
        sql = (
            "SELECT ts, coalesce(auto_decision->>'chosen_model', outbound_model, client_model), "
            "coalesce(outbound_model, ''), success, coalesce(failure_detail_code, '') "
            "FROM request_logs_with_current_month WHERE is_auto_request = true "
            "ORDER BY ts DESC LIMIT 8;"
        )
        out = subprocess.run(
            ["docker", "exec", "acc-pg17", "psql", "-U", "postgres", "-d", "llm_gateway",
             "-t", "-A", "-F", " | ", "-c", sql],
            capture_output=True, text=True, check=False,
        ).stdout.strip()
        print("[6] 真库最近 auto 请求：")
        for line in [ln for ln in out.splitlines() if ln.strip()]:
            print("    " + line[:220])
            if "no_candidates" in line:
                failures.append("真库仍有 auto 请求以 no_candidates 收场")
    except Exception as exc:  # noqa: BLE001
        print(f"[6] 读真库失败：{exc}")

    if failures:
        print("FAIL: " + "; ".join(failures))
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
