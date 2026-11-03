#!/usr/bin/env bash
# 切流时同步 goacos 注册表的回归门（2026-11-02）。
#
# ★ 要锁住的契约：
#   ① 切流到新端口 ⇒ 注册表必须指向**新端口**（否则消费方读到旧槽的口，
#      把活着的服务读成不健康 —— 实测 2026-11-02 的假阴性）；
#   ② 同服务下**旧端口**的那条登记必须被注销（否则 instances[0] 可能仍是死口）；
#   ③ 登记失败 ⇒ 函数返回非 0（由调用方决定是否影响切流；调用方只 warn，
#      因为此刻 nginx 已 reload，切流本身已经生效）；
#   ④ 没有 IP（DD_GOACOS_IP / DD_HOST 皆空）⇒ 跳过而不是乱登记。
#
# 沙箱：不碰真注册表 —— 用一个本地 HTTP 桩冒充 goacos。
set -euo pipefail
self=$(cd "$(dirname "$0")" && pwd)
engine=${ENGINE:-$self/deploy-seamless-docker.sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"; [[ -n "${stub_pid:-}" ]] && kill "$stub_pid" 2>/dev/null || true' EXIT

die() { echo "$*" >&2; exit 1; }
assert_equal() { [[ "$1" == "$2" ]] || die "$3 (got '$1' want '$2')"; }
assert_contains() { [[ "$1" == *"$2"* ]] || die "$3 (got '$1')"; }

# ---- 从引擎里抽出 GOACOS-REGISTER 块（与既有测试同一手法） ----------------
block() {
  awk -v label="$1" '
    index($0, label ":BEGIN") { found=1; active=1; next }
    index($0, label ":END") { if (active) { closed=1; exit } }
    active { print }
    END { if (!found || !closed) exit 1 }
  ' "$engine"
}
register_block=$(block GOACOS-REGISTER) || die 'missing GOACOS-REGISTER block'

# ---- 本地 HTTP 桩：冒充 goacos，记录收到的请求 ---------------------------
cat > "$tmp/stub.py" <<'PY'
import json, os, sys, threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

RECORD = sys.argv[2]
STALE = [{"ip": "192.168.31.211", "port": 8781}, {"ip": "192.168.31.211", "port": 8783}]


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _record(self, method, params):
        with open(RECORD, "a", encoding="utf-8") as fh:
            fh.write(json.dumps({"method": method, "params": params}, ensure_ascii=False) + "\n")

    def do_GET(self):
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        self._record("GET", q)
        # ★ 形状必须与**真 goacos** 一致：v1 的 /ns/instance/list 把 hosts 放在
        #   **顶层**（{"name":…,"hosts":[…]}）。旧桩返回 {"data":{"hosts":…}} ⇒
        #   门测的是一个服务器从不返回的形状 ⇒ 真机上的解析错误测不出来
        #   （2026-11-03 实测：真注册表顶层形状 ⇒ 引擎解析恒空 ⇒ 旧条目不注销）。
        # 形状开关：**文件**优先（argv[3]），其次环境变量 STUB_SHAPE。
        # ★ 用文件而不是环境变量 ⇒ 同一个桩、同一个端口就能切形状 ⇒ 场景④b
        #   不需要再起第二个服务（本机起第二个后台服务会出现「进程活着但没监听」）。
        shape = ""
        if len(sys.argv) > 3:
            try:
                with open(sys.argv[3], encoding="utf-8") as fh:
                    shape = fh.read().strip()
            except OSError:
                shape = ""
        shape = shape or os.environ.get("STUB_SHAPE", "")
        if shape == "nested":
            body = json.dumps({"data": {"hosts": STALE}}).encode()
        else:
            body = json.dumps({"name": "DEFAULT_GROUP@@llm-gateway", "hosts": STALE}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n).decode("utf-8") if n else ""
        q = {k: v[0] for k, v in parse_qs(raw).items()}
        self._record("POST", q)
        self.send_response(200)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")

    def do_DELETE(self):
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        self._record("DELETE", q)
        self.send_response(200)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")


srv = HTTPServer(("127.0.0.1", int(sys.argv[1])), H)
srv.serve_forever()
PY

port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)
record="$tmp/requests.jsonl"
: > "$record"
python3 "$tmp/stub.py" "$port" "$record" "$tmp/shape" &
stub_pid=$!
for _ in $(seq 1 50); do
  if python3 - "$port" <<'PY' 2>/dev/null
import socket, sys
s = socket.socket(); s.settimeout(0.3)
try:
    s.connect(("127.0.0.1", int(sys.argv[1])))
except Exception:
    sys.exit(1)
PY
  then break; fi
  sleep 0.1
done

# ---- 载入块并跑场景 ------------------------------------------------------
run_case() {
  (
    warn() { echo "WARN: $*" >&2; }
    log() { echo "LOG: $*"; }
    eval "$register_block"
    dd_register_goacos "$@"
  )
}

export DD_GOACOS_ADDR="http://127.0.0.1:$port"
export DD_GOACOS_IP="192.168.31.211"
export DD_GOACOS_SERVICE="llm-gateway"
export DD_GOACOS_NAMESPACE="wm-deploy"
export DD_GOACOS_GROUP="DEFAULT_GROUP"
export DD_GOACOS_ENV="lab-211"

# 场景①：新端口被登记；旧端口（8781）被注销；同端口不动
out=$(run_case 8783) || die "register failed: $out"
assert_contains "$out" "registered 192.168.31.211:8783" 'register did not report the new port'
post_line=$(grep '"method": "POST"' "$record" | tail -1) || die 'no POST recorded'
assert_contains "$post_line" '"port": "8783"' 'POST did not carry the new port'
assert_contains "$post_line" '"serviceName": "llm-gateway"' 'POST did not carry the service name'
assert_contains "$post_line" '"namespaceId": "wm-deploy"' 'POST did not carry the namespace'
assert_contains "$post_line" 'health_path' 'POST metadata lost health_path'
assert_contains "$post_line" '/healthz' 'POST metadata lost the health path'
del_lines=$(grep -c '"method": "DELETE"' "$record" || true)
assert_equal "$del_lines" "1" 'expected exactly one stale DELETE'
del_line=$(grep '"method": "DELETE"' "$record" | tail -1)
assert_contains "$del_line" '"port": "8781"' 'DELETE did not target the stale port'
assert_contains "$del_line" '"serviceName": "llm-gateway"' 'DELETE did not carry the service name'

# 场景②：登记到**已经**在册的端口 ⇒ 不该再注销自己
: > "$record"
out=$(run_case 8783) || die "re-register failed: $out"
del_lines=$(grep -c '"method": "DELETE"' "$record" || true)
assert_equal "$del_lines" "1" 're-register must still drop only the stale port'

# 场景③：没有 IP ⇒ 跳过（不乱登记）
: > "$record"
out=$(DD_GOACOS_IP="" DD_HOST="" run_case 8783 2>&1) || die 'skip path must not fail'
assert_contains "$out" "DD_GOACOS_IP" 'skip path did not explain itself'
lines=$(wc -l < "$record" | tr -d ' ')
assert_equal "$lines" "0" 'skip path still touched the registry'

# 场景④：注册表不可达 ⇒ 返回非 0（由调用方决定；调用方只 warn 不回退切流）
export DD_GOACOS_ADDR="http://127.0.0.1:1"
set +e
run_case 8783 >/dev/null 2>&1
rc=$?
set -e
[[ "$rc" != "0" ]] || die 'unreachable registry must return non-zero'

# ---- 场景④b：嵌套形状（data.hosts）也必须被读出来 ------------------------
# 引擎的回落分支：先读顶层 hosts，再读 data.hosts ⇒ 两种实现都兼容。
# ★ 这条用例是为了锁住「引擎支持两种形状」，不是为了证明真服务器是哪种
#   （真服务器是**顶层**形状，见场景① 的桩）。
# ★ 用**同一个桩**（写形状文件切换）⇒ 不用再起一个服务。
printf 'nested' > "$tmp/shape"
# ★ 场景④ 把 DD_GOACOS_ADDR 指向了不可达地址（127.0.0.1:1），恢复点在场景⑤ ⇒
#   本场景必须先指回桩的真实端口，否则连的是那个死地址。
export DD_GOACOS_ADDR="http://127.0.0.1:$port"
: > "$record"
out=$(run_case 8783) || die "nested-shape register failed: $out"
del_lines=$(grep -c '"method": "DELETE"' "$record" || true)
assert_equal "$del_lines" "1" 'nested shape: stale DELETE must still happen'
del_line=$(grep '"method": "DELETE"' "$record" | tail -1)
assert_contains "$del_line" '"port": "8781"' 'nested shape: DELETE did not target the stale port'
printf 'flat' > "$tmp/shape"

# ---- 场景④c：211 外壳的默认注册地址**不得是 loopback** -------------------
# ★ 锁住 211 的事实：goacos 发布在可路由地址（100.106.67.198:8848），不在
#   127.0.0.1 ⇒ 外壳默认若回到 loopback，每次部署的登记都会静默失败。
wrapper="$self/deploy-211.sh"
[[ -f "$wrapper" ]] || die "missing wrapper: $wrapper"
addr_default=$(grep -E '^export DD_GOACOS_ADDR=' "$wrapper" | head -1)
[[ -n "$addr_default" ]] || die 'wrapper does not export DD_GOACOS_ADDR'
case "$addr_default" in
  *127.0.0.1*) die "wrapper DD_GOACOS_ADDR default is loopback; 211's goacos listens on a routable address" ;;
esac

# ---- 场景⑤：★「切流 ⇒ 注册表跟着变」这条**接线**必须成立 ----------------
# 只测块不够：块对了但没被切流路径调用，等于没修。
export DD_GOACOS_ADDR="http://127.0.0.1:$port"
: > "$record"

# 桩：nginx -t / nginx -s reload 都成功
mkdir -p "$tmp/bin"
cat > "$tmp/bin/nginx" <<'SH'
#!/usr/bin/env bash
case "$1" in
  -t) exit 0 ;;
  -s) exit 0 ;;
esac
exit 0
SH
chmod +x "$tmp/bin/nginx"

# 桩：upstream 端口判定助手（把 upstream 里的端口改成目标值）
cat > "$tmp/helper.py" <<'PY'
import re, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
conf, upstream, target = args["--conf"], args["--upstream"], args["--switch"]
text = open(conf, encoding="utf-8").read()
new = re.sub(r"(upstream\s+" + re.escape(upstream) + r"\s*\{[^}]*?server\s+127\.0\.0\.1:)\d+",
              r"\g<1>" + target, text, count=1)
if new == text:
    sys.exit(2)
open(args["--backup"], "w", encoding="utf-8").write(text)
open(conf, "w", encoding="utf-8").write(new)
PY

conf="$tmp/nginx.conf"
cat > "$conf" <<'EOF'
upstream ns_llm_gateway_upstream {
    server 127.0.0.1:8781 max_fails=2 fail_timeout=10s;
}
EOF

switch_block=$(awk '
  /^dd_switch_upstream\(\) \{/ {active=1}
  active {print}
  active && /^\}$/ {exit}
' "$engine") || die 'cannot extract dd_switch_upstream'
[[ -n "$switch_block" ]] || die 'empty dd_switch_upstream'

out=$(
  export PATH="$tmp/bin:$PATH"
  export DD_NGINX_CONF="$conf"
  export DD_NGINX_UPSTREAM="ns_llm_gateway_upstream"
  export DD_UPSTREAM_PORT_HELPER="$tmp/helper.py"
  (
    warn() { echo "WARN: $*" >&2; }
    log() { echo "LOG: $*"; }
    eval "$register_block"
    eval "$switch_block"
    dd_switch_upstream 8783
  ) 2>&1
) || die "dd_switch_upstream failed: $out"
assert_contains "$out" "8783" 'switch did not report the new port'
assert_contains "$(cat "$conf")" "127.0.0.1:8783" 'the upstream itself was not switched'
post_line=$(grep '"method": "POST"' "$record" | tail -1) || die 'switch did not register into goacos'
assert_contains "$post_line" '"port": "8783"' 'the switch registered the wrong port'
del_line=$(grep '"method": "DELETE"' "$record" | tail -1) || die 'switch did not drop the stale registration'
assert_contains "$del_line" '"port": "8781"' 'the switch did not drop the stale port'

# ---- 场景⑥：★「导出的量」必须真的能到远端（转发白名单）------------------
# ★ 为什么要有这条：--via 的转发是**显式白名单**（引擎里的 for _v in ...），
#   不是「导出全部 DD_*」⇒ 部署机 export 了，远端**收不到** ⇒ 远端只能吃引擎
#   自己的默认。实测（2026-11-03）：deploy-211.sh 导出了 DD_GOACOS_*，
#   而白名单里没有 ⇒ 唯独 DD_GOACOS_ENV 没有默认兜底 ⇒ 注册实例的 metadata
#   会少 env 键 ⇒ **改动在真实路径上没完全生效**。
#   ⇒ 这里把「导出的 DD_GOACOS_* ⊆ 转发白名单」锁死。
deploy_211="${DEPLOY_211:-$self/deploy-211.sh}"
[[ -f "$deploy_211" ]] || die "missing $deploy_211"

forward_block=$(awk '
  /for _v in / {buf=""; collecting=1}
  collecting {buf = buf $0 "\n"}
  collecting && /; do/ {
    if (buf ~ /DD_TARGET/) {printf "%s", buf; exit}
    collecting=0
  }
' "$engine") || true
[[ -n "$forward_block" ]] || die 'cannot locate the --via forward list in the engine'
forwarded=$(printf '%s' "$forward_block" | grep -oE '\bDD_[A-Z0-9_]+' | sort -u)

exported=$(grep -oE 'export[[:space:]]+(DD_GOACOS_[A-Z0-9_]+)=' "$deploy_211" \
  | sed -E 's/export[[:space:]]+//; s/=$//' | sort -u)
[[ -n "$exported" ]] || die 'no DD_GOACOS_* exported by deploy-211.sh (parser broke?)'

for v in $exported; do
  printf '%s\n' "$forwarded" | grep -qx "$v" \
    || die "$v is exported by deploy-211.sh but NOT in the engine's --via forward list => it never reaches the remote"
done

# 反向：引擎读的每个 DD_GOACOS_*，要么在转发清单里、要么在块内自带 :- 兜底
read_names=$(printf '%s' "$register_block" | grep -oE '\bDD_GOACOS_[A-Z0-9_]+' | sort -u)
[[ -n "$read_names" ]] || die 'the register block reads no DD_GOACOS_* (parser broke?)'
for v in $read_names; do
  if printf '%s\n' "$forwarded" | grep -qx "$v"; then
    continue
  fi
  printf '%s' "$register_block" | grep -q "${v}:-" \
    || die "$v is read by dd_register_goacos but is neither forwarded nor given a :- fallback => it silently resolves empty on the remote"
done

echo "OK goacos register contract holds"
