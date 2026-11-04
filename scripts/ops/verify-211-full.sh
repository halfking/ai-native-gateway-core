#!/usr/bin/env bash
# =====================================================================
# scripts/ops/verify-211-full.sh — 211 部署后**全量验收**（蓝绿感知）
# =====================================================================
# 为什么需要这一个文件（2026-11-05）：
#   仓里已有 scripts/ops/verify-154-full.sh / verify-245-full.sh（systemd 形态），
#   但**没有 211 的对等物** ⇒ 211 每次部署后只能靠「部署脚本自己打的那几行绿字」
#   当验收证据 ⇒ ⇒ **部署脚本的绿灯不等于部署成功**（量具与被测物同源）。
#   211 的形态又与 154/245 不同（**蓝绿双槽**），它的验收面多出三件事：
#     ① 槽位 ↔ 端口 ↔ 容器 ↔ upstream ↔ 引擎账本 五者必须**自洽**；
#     ② **回滚面**必须仍然可用（旧槽容器与镜像还在）；
#     ③ **注册表**（goacos）必须与当前活跃口一致（否则消费方把活着的网关读成不健康）。
#   ⇒ 本脚本把这三条 + 常规运行面收成**一个可重复执行的只读探针**。
#
# 用法:
#   bash scripts/ops/verify-211-full.sh            # 期望身份 = 本仓 HEAD
#   bash scripts/ops/verify-211-full.sh eccefdb8   # 期望身份 = 指定 sha(8位)
#   LLM_GATEWAY_211_SSH=root@1.2.3.4 bash scripts/ops/verify-211-full.sh
#
# 退出码: 0 = 全绿（允许有 WARN） / 1 = 有 FAIL / 2 = 用法错 / 3 = 探针没做成
#         （★「没做成」与「全绿」必须能区分 —— 否则不可判定会被读成通过）
# =====================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

SSH_TARGET="${LLM_GATEWAY_211_SSH:-211}"
SLOT_A="${DD_SLOT_A_PORT:-8781}"
SLOT_B="${DD_SLOT_B_PORT:-8783}"
CONTAINER_PREFIX="${DD_CONTAINER_PREFIX:-llm-gateway-211}"
NGINX_CONF="${DD_NGINX_CONF:-/etc/nginx/conf.d/ns-gateway-211.conf}"
UPSTREAM_NAME="${DD_NGINX_UPSTREAM:-ns_llm_gateway_upstream}"
DATA_ROOT="${DD_GW_DATA_ROOT:-/opt/kaixuan/data/llm-gateway}"
GOACOS_ADDR="${DD_GOACOS_ADDR:-http://100.106.67.198:8848}"
GOACOS_SERVICE="${DD_GOACOS_SERVICE:-llm-gateway}"
GOACOS_NS="${DD_GOACOS_NAMESPACE:-wm-deploy}"
GOACOS_GRP="${DD_GOACOS_GROUP:-DEFAULT_GROUP}"
DOMAIN="${DD_DOMAIN:-llm.itestu.cn}"

EXPECTED_SHA="${1:-}"
if [[ -z "$EXPECTED_SHA" ]]; then
  EXPECTED_SHA="$(git -C "$REPO_ROOT" rev-parse --short=8 HEAD 2>/dev/null || true)"
fi
[[ -n "$EXPECTED_SHA" ]] || { echo "用法: $0 [expected_sha8]" >&2; exit 2; }

# bash 3.2 + set -u: 空数组展开守卫（macOS 自带 bash 3.2）。
SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=12)

echo "=== 211 全量验收（蓝绿）==="
echo "目标机  : $SSH_TARGET"
echo "期望身份: $EXPECTED_SHA"
echo

# ── 第一段：目标机侧事实（docker / nginx / 状态文件 / 槽位绑定）──────────
# 判据落在**运行中对象**的读数上（docker inspect / nginx conf / 状态文件），
# 不是回读脚本自己的声明文件。
facts="$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
  "SLOT_A='$SLOT_A' SLOT_B='$SLOT_B' PREFIX='$CONTAINER_PREFIX' \
   CONF='$NGINX_CONF' UPSTREAM='$UPSTREAM_NAME' DATA_ROOT='$DATA_ROOT' \
   python3 -" <<'PYFACTS' 2>&1
import json, os, re, subprocess, sys

slot_a, slot_b = os.environ["SLOT_A"], os.environ["SLOT_B"]
prefix, conf = os.environ["PREFIX"], os.environ["CONF"]
upstream, data_root = os.environ["UPSTREAM"], os.environ["DATA_ROOT"]

def sh(*args):
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=20).stdout.strip()
    except Exception as exc:  # noqa: BLE001
        return ""

out = {}

# ── nginx upstream 主端口（去掉注释行，只认**活的** server 行）──────────
port = ""
try:
    txt = open(conf, encoding="utf-8", errors="replace").read()
    m = re.search(r"upstream\s+" + re.escape(upstream) + r"\s*\{(.*?)\}", txt, re.S)
    if m:
        for line in m.group(1).splitlines():
            line = line.split("#", 1)[0]
            mm = re.search(r"server\s+127\.0\.0\.1:(\d+)", line)
            if mm:
                port = mm.group(1)
                break
except Exception as exc:  # noqa: BLE001
    out["conf_error"] = str(exc)
out["upstream_port"] = port

# ── 引擎账本（状态文件）────────────────────────────────────────────────
def read_text(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            return fh.read().strip()
    except Exception:
        return ""
out["state_slot"] = read_text(os.path.join(data_root, "run", "active-slot"))
out["state_port"] = read_text(os.path.join(data_root, "run", "active-port"))

# ── 两个槽的容器事实（创建/启动/状态/镜像/端口）─────────────────────────
for slot in ("a", "b"):
    name = f"{prefix}-{slot}"
    raw = sh("docker", "inspect", name)
    if not raw:
        out[f"slot_{slot}"] = {"exists": False}
        continue
    try:
        obj = json.loads(raw)[0]
    except Exception:
        out[f"slot_{slot}"] = {"exists": True, "parse_error": True}
        continue
    st = obj.get("State", {})
    ports = (obj.get("NetworkSettings", {}).get("Ports") or {})
    published = []
    for _proto, binds in ports.items():
        for b in binds or []:
            published.append(b.get("HostPort"))
    out[f"slot_{slot}"] = {
        "exists": True,
        "status": st.get("Status"),
        "health": (st.get("Health") or {}).get("Status"),
        "image": (obj.get("Image") or "")[:19],
        "created": obj.get("Created"),
        "started": st.get("StartedAt"),
        "finished": st.get("FinishedAt"),
        "published": published,
    }

# ── 镜像事实 ──────────────────────────────────────────────────────────
out["images"] = sh("docker", "images", prefix, "--format", "{{.ID}}\t{{.CreatedAt}}")
# ★ nginx -t 的结论在 **stderr**（stdout 通常是空的）⇒ 只取 stdout 会把
#   「通过」读成「没输出」⇒ 判成 FAIL ⇒ **假阴性**（错的量具比没量具危险）。
#   ⇒ 改为取「合并输出 + 退出码」，并以**退出码**为判据。
try:
    _ng = subprocess.run(["nginx", "-t"], capture_output=True, text=True, timeout=30)
    out["nginx_t_rc"] = _ng.returncode
    out["nginx_t"] = ((_ng.stdout or "") + (_ng.stderr or "")).strip()
except Exception as exc:  # noqa: BLE001
    out["nginx_t_rc"] = -1
    out["nginx_t"] = f"nginx -t 不可执行：{exc}"
print(json.dumps(out, ensure_ascii=False))
PYFACTS
)"
rc=$?
if [[ $rc -ne 0 ]]; then
  echo "★ 探针没做成（ssh rc=${rc}）—— 这**不是**「通过」" >&2
  echo "$facts" >&2
  exit 3
fi

python3 - "$facts" "$EXPECTED_SHA" "$CONTAINER_PREFIX" <<'PYREAD'
import json, sys
raw = sys.argv[1]
expected = sys.argv[2]
prefix = sys.argv[3]
P = F = W = 0
def ok(m):
    global P; P += 1; print("  ✓", m)
def fail(m):
    global F; F += 1; print("  ✗", m)
def warn(m):
    global W; W += 1; print("  ⚠", m)

try:
    d = json.loads(raw)
except Exception as exc:
    print(f"★ 探针输出不可解析（{exc}）—— 这**不是**「通过」", file=sys.stderr)
    sys.exit(3)

slot_a, slot_b = "8781", "8783"
up = d.get("upstream_port") or ""
state_slot = d.get("state_slot") or ""
state_port = d.get("state_port") or ""
a = d.get("slot_a") or {}
b = d.get("slot_b") or {}

print("== ① 槽位 ↔ 端口 ↔ 容器 ↔ upstream ↔ 账本 五者自洽 ==")
if not up:
    fail("读不到 nginx upstream 主端口")
else:
    ok(f"upstream 主端口 = {up}")
slot_of = {slot_a: "a", slot_b: "b"}
live = slot_of.get(up)
if live:
    ok(f"upstream 落在槽 {live} 上（{up}）")
else:
    fail(f"upstream 端口 {up} 不属于任何配置槽位（a={slot_a} / b={slot_b}）")

# 账本必须与 upstream 同侧（不一致 = 上一次部署没写账本，或被手工改过 nginx）
if state_slot and live and state_slot != live:
    fail(f"引擎账本（run/active-slot={state_slot}）与 upstream（槽 {live}）**不一致** ⇒ 账本已陈旧")
elif state_slot and live:
    ok(f"引擎账本与 upstream 同侧（槽 {state_slot}）")
else:
    warn(f"引擎账本缺失或空（{state_slot or '无'}）")

if state_port and up and state_port != up:
    fail(f"引擎账本端口（run/active-port={state_port}）与 upstream（{up}）不一致")
elif state_port and up:
    ok(f"引擎账本端口与 upstream 一致（{state_port}）")

# 活跃槽容器必须在跑且健康；候选槽必须**已停**（停而不删 ⇒ 可回滚）
if live:
    live_c = a if live == "a" else b
    other_c = b if live == "a" else a
    if not live_c.get("exists"):
        fail(f"活跃槽容器 {prefix}-{live} 不存在")
    else:
        if live_c.get("status") == "running":
            ok(f"活跃槽容器 {prefix}-{live} 在跑（health={live_c.get('health')}）")
        else:
            fail(f"活跃槽容器 {prefix}-{live} 状态 = {live_c.get('status')}（应 running）")
        if str(up) in [str(x) for x in live_c.get("published", [])]:
            ok(f"活跃槽容器发布端口与 upstream 一致（{up}）")
        else:
            fail(f"活跃槽容器发布端口 {live_c.get('published')} 与 upstream {up} 不一致")
    if other_c.get("exists"):
        if other_c.get("status") == "exited":
            ok(f"旧槽容器 {prefix}-{'b' if live == 'a' else 'a'} 已停（保留 ⇒ 可 docker start 回滚）")
        else:
            fail(f"旧槽容器状态 = {other_c.get('status')}（应 exited；两个槽都在跑 = 上次部署没收尾）")
    else:
        warn(f"旧槽容器不存在（{prefix}-{'b' if live == 'a' else 'a'}）—— 无一键回滚面")
else:
    # upstream 不在任何槽位上：仍要求「有槽在跑」，否则线上就是断的
    for slot, c in (("a", a), ("b", b)):
        if c.get("status") == "running":
            ok(f"槽 {slot} 容器在跑（但 upstream 未指向任何槽位 ⇒ 流量落空）")
    fail("upstream 与槽位脱节 —— 需要人工处置（见 deploy-doctor.sh 的处置指引）")

print("== ② 版本身份（容器 / 镜像 / 账本）==")
for slot, c in (("a", a), ("b", b)):
    if c.get("exists"):
        print(f"    槽 {slot}: image={c.get('image')} created={c.get('created')}")

print("== ③ nginx 配置自检 ==")
_ng_rc = d.get("nginx_t_rc")
if _ng_rc == 0:
    ok("nginx -t 通过")
elif _ng_rc is None:
    warn("nginx -t 未执行（不可判定，不判红）")
else:
    fail(f"nginx -t 未通过（rc={_ng_rc}）：{(d.get('nginx_t') or '')[:200]}")

print()
print(f"小计: PASS={P} FAIL={F} WARN={W}")
sys.exit(1 if F else 0)
PYREAD
first_rc=$?

# ── 第二段：应用面（本机 8781 + 公网）+ 注册表 ─────────────────────────
echo
echo "== ④ 应用面（/version / readyz / 登录 / 网页面 / 公网）=="
app_rc=0
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
  "EXPECTED_SHA='$EXPECTED_SHA' DOMAIN='$DOMAIN' \
   GOACOS_ADDR='$GOACOS_ADDR' GOACOS_SERVICE='$GOACOS_SERVICE' \
   GOACOS_NS='$GOACOS_NS' GOACOS_GRP='$GOACOS_GRP' \
   SLOT_A='$SLOT_A' SLOT_B='$SLOT_B' python3 -" <<'PYAPP' || app_rc=$?
import json, os, re, sys, time, urllib.error, urllib.request

expected = os.environ["EXPECTED_SHA"]
domain = os.environ["DOMAIN"]
gc_addr = os.environ["GOACOS_ADDR"]
gc_svc = os.environ["GOACOS_SERVICE"]
gc_ns = os.environ["GOACOS_NS"]
gc_grp = os.environ["GOACOS_GRP"]

P = F = W = 0
def ok(m):
    global P; P += 1; print("  ✓", m)
def fail(m):
    global F; F += 1; print("  ✗", m)
def warn(m):
    global W; W += 1; print("  ⚠", m)

def req(method, url, data=None, headers=None, timeout=20):
    h = dict(headers or {})
    body = None
    if data is not None:
        body = json.dumps(data).encode()
        h.setdefault("Content-Type", "application/json")
    r = urllib.request.Request(url, data=body, headers=h, method=method)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return resp.status, resp.read(), dict(resp.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)
    except Exception as exc:  # noqa: BLE001
        return 0, str(exc).encode(), {}

# ── ④.1 本机活跃口：哪个端口在服务？两个都探，**谁是 upstream 就验谁** ──
def upstream_port():
    for port in (os.environ["SLOT_A"], os.environ["SLOT_B"]):
        st, raw, _ = req("GET", f"http://127.0.0.1:{port}/healthz", timeout=6)
        if st == 200:
            return port
    return ""

live_port = upstream_port()
if not live_port:
    fail("两个槽位的本机口都探不到 /healthz（没有槽在服务？）")
    live_port = os.environ["SLOT_A"]
else:
    ok(f"本机在服务的槽位端口 = {live_port}")

base = f"http://127.0.0.1:{live_port}"

# ── ④.2 /version 身份 ───────────────────────────────────────────────
st, raw, _ = req("GET", base + "/version")
if st == 200:
    try:
        ver = json.loads(raw)
        sha = str(ver.get("git_sha") or "")
        seq = ver.get("build_seq")
        if sha.startswith(expected[:8]) or expected[:8] in sha:
            ok(f"/version 身份一致（git_sha={sha} build_seq={seq}）")
        else:
            fail(f"/version 身份不符：期望 {expected[:8]}，实得 {sha}（build_seq={seq}）")
    except Exception as exc:  # noqa: BLE001
        fail(f"/version 响应不可解析：{exc}")
else:
    fail(f"/version HTTP {st}")

# ── ④.3 readyz 契约（status=ready + database.connected + redis.connected）──
st, raw, _ = req("GET", base + "/readyz")
if st != 200:
    fail(f"/readyz HTTP {st}")
else:
    try:
        rz = json.loads(raw)
        bad = []
        if str(rz.get("status") or "").lower() != "ready":
            bad.append(f"status={rz.get('status')}")
        db = rz.get("database") or {}
        if db.get("connected") is not True:
            bad.append(f"database.connected={db.get('connected')}")
        rd = rz.get("redis") or {}
        if rd.get("connected") is not True:
            bad.append(f"redis.connected={rd.get('connected')}")
        if bad:
            fail("/readyz 契约不满足：" + ", ".join(bad))
        else:
            ok("/readyz 契约满足（ready + database + redis 全连）")
    except Exception as exc:  # noqa: BLE001
        fail(f"/readyz 响应不可解析：{exc}")

# ── ④.4 登录 + 连登无 429 ───────────────────────────────────────────
user = os.environ.get("LLM_GATEWAY_ADMIN_USER", "admin")
pwd = os.environ.get("LLM_GATEWAY_ADMIN_PASSWORD", "Veritrans&9527")
st, raw, _ = req("POST", base + "/api/auth/token", {"username": user, "password": pwd})
if st != 200:
    fail(f"登录 HTTP {st} body={raw[:120]!r}")
else:
    ok(f"登录 OK（user={user}）")
    codes = []
    for _ in range(6):
        st2, _, _ = req("POST", base + "/api/auth/token", {"username": user, "password": pwd})
        codes.append(st2)
    if all(c == 200 for c in codes):
        ok("连登 6 次无 429（限流桶已 Reset）")
    else:
        fail(f"连登出现非 200：{codes}")

# ── ④.5 网页面：SPA + 入口 JS ───────────────────────────────────────
st, raw, hdrs = req("GET", base + "/")
if st != 200:
    fail(f"站点根路径 HTTP {st}")
else:
    html = raw.decode("utf-8", errors="replace")
    ctype = hdrs.get("Content-Type", "")
    if "<html" in html.lower():
        ok(f"站点根路径返回 HTML（{ctype}）")
    else:
        fail(f"站点根路径不是 HTML（{ctype}）—— 常见病因：web/dist 没种子进容器")
    m = re.search(r'src="([^"]*index-[^"]+\.js)"', html)
    if not m:
        warn("HTML 里没找到 index-*.js 入口引用（不判红，但入口 JS 门无从验起）")
    else:
        js = m.group(1)
        st3, raw3, h3 = req("GET", base + js)
        if st3 == 200 and len(raw3) > 1000:
            ok(f"入口 JS 可取（{js}，{len(raw3)} bytes，{h3.get('Content-Type','?')}）")
        else:
            fail(f"入口 JS 取不到：{js} HTTP {st3}")

# ── ④.6 公网入口 + 公网版本身份 ─────────────────────────────────────
st, raw, _ = req("GET", f"https://{domain}/", timeout=25)
if st == 200:
    ok(f"公网入口 200（https://{domain}/）")
else:
    fail(f"公网入口 HTTP {st}（https://{domain}/）")
st, raw, _ = req("GET", f"https://{domain}/version", timeout=25)
if st == 200:
    try:
        pv = json.loads(raw)
        psha = str(pv.get("git_sha") or "")
        if expected[:8] in psha:
            ok(f"公网 /version 身份一致（{psha}）")
        else:
            fail(f"公网 /version 身份不符：期望 {expected[:8]}，实得 {psha} ⇒ 切流后的服务不是本次发布")
    except Exception as exc:  # noqa: BLE001
        fail(f"公网 /version 不可解析：{exc}")
else:
    fail(f"公网 /version HTTP {st}")

# ── ④.7 注册表（goacos）：必须与当前活跃口一致，且**不留旧槽条目** ────
try:
    url = (gc_addr.rstrip("/") + "/nacos/v1/ns/instance/list?"
           + "serviceName=" + gc_svc + "&namespaceId=" + gc_ns + "&groupName=" + gc_grp)
    st, raw, _ = req("GET", url, timeout=10)
    doc = json.loads(raw) if st == 200 else {}
    hosts = doc.get("hosts") or []
    if not hosts and isinstance(doc.get("data"), dict):
        hosts = doc["data"].get("hosts") or []
    entries = [(str(h.get("ip")), int(h.get("port") or 0)) for h in hosts if isinstance(h, dict)]
    if not entries:
        warn("注册表里没有本服务的实例条目（消费方将读不到发布口）")
    else:
        hit = [e for e in entries if e[1] == int(live_port)]
        stale = [e for e in entries if e[1] != int(live_port)]
        if hit:
            ok(f"注册表指向当前活跃口 {live_port}（{hit}）")
        else:
            fail(f"注册表**没有**当前活跃口 {live_port}（实有 {entries}）⇒ 消费方会把活着的网关读成不健康")
        if stale:
            fail(f"注册表仍留着旧槽条目 {stale} ⇒ 假阴性来源")
        else:
            ok("注册表无旧槽残留条目")
except Exception as exc:  # noqa: BLE001
    warn(f"注册表读不到（{exc}）—— 不可判定，不判红")

print()
print(f"小计: PASS={P} FAIL={F} WARN={W}")
sys.exit(1 if F else 0)
PYAPP

echo
if [[ ${first_rc} -ne 0 || ${app_rc} -ne 0 ]]; then
  echo "★ 验收结论：**有 FAIL**（槽位段 rc=${first_rc}，应用段 rc=${app_rc}）"
  exit 1
fi
echo "★ 验收结论：全绿（WARN 不阻断）"
exit 0
