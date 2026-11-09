#!/usr/bin/env bash
# =====================================================================
# tests/mcp_metrics_scrape_wiring_test.sh
#   —— 「★ 暴露 mcp_gateway_* 指标族的 /metrics 抓取面 ★」接线契约门
#
# ★ 它钉的是什么（★ 五条不变量 ★）：
#
#   ① 【★ 「服务 mcp 指标」的二进制集合【★ 必须被清点出来 ★】★】
#      ⇒ ★ 而【★ 清点必须【★ 剔掉测试文件 ★】★★ ★
#      ⇒ ★ 而【★ 清点结果【★ 必须与已知清单【★ 逐条相等 ★】★★ ★】（★ 双向棘轮 ★）
#        ⇒ ★ 新增一个 import mcp 的二进制 ⇒ 门红（必须显式登记）★
#        ⇒ ★ 少了一个 ⇒ 门红（代码搬家了、登记没跟上）★
#      ⇒ ★ 而【★ 清点面空了 ⇒ 门会「一个都不检查」⇒ 必须报出来 ★★★ ★
#
#   ② 【★ 每个这样的二进制【★ 必须有 ≥1 个 /metrics 抓取面 ★】★★ ★
#      ⇒ ★ 而【★ 抓取面数 ≤ ScrapeHandler 包装调用数 ★】★★ ★
#      ⇒ ★ 而【★ 路径必须【★ 恰好是 "/metrics" ★】★★ ★
#        ⇒ ★ 否则「GET /metrics/storage」这种 JSON 快照端点会被误判 ★
#          （★ 它取的是 monitoring.Default().Snapshot()、★ 不是 Prometheus 注册表 ★）★
#
#   ③ 【★ 反向不变量 ★】：★ `ScrapeHandler(` 【★ 只许出现在 ① 清点到的二进制里 ★】
#      ⇒ ★ 否则「★ 往一个不服务 mcp 指标的地方挂包装 ★」可以冒充接线 ★
#
#   ④ 【★ 门自己有牙 ★】：★ 合成树差分探针 ★
#      ⇒ ★ 同一套判定、★ 两种形态各跑一次 ★
#      ⇒ ★ 「★ import mcp 但 /metrics 未包装 ★」⇒ 必须判红 ★★
#      ⇒ ★ 「★ import mcp 且 /metrics 已包装 ★」⇒ 必须判绿 ★★
#      ⇒ ★ 而【★ 两边都判红 ★】或【★ 两边都判绿 ★】⇒ 门坏 ⇒ 红 ★★
#
#   ⑤ 【★ 门自身不得成为处置面的一条命中 ★】（★ 自指纪律 ★）
#
# ★ 为什么需要这道门（★ 实测 ★）：
#   · internal/metricguard 那道门判的是「生产代码里有没有记录调用」（令牌级
#     「标识符.记录方法」形态）⇒ ★ 它【★ 看不见「记录调用挂在哪个抓取面上」★★ ★
#   · 而 mcp_gateway_uptime_seconds 这条指标【★ 只有抓取时刻才有意义 ★】
#     ⇒ ★ 记录调用挂在包里、而抓取面没接 ⇒ 指标在 /metrics 上恒为 ABSENT ★★
#   · ⇒ ★ ⇒ ★ **「记录调用存在」与「指标真的有值」是两件事 ★**★★ ★
#   · 而【★ 这两件事之间那道缝、★ 没有任何既有门覆盖 ★】★★★ ★
#
# ★ 零外部依赖、★ 全部在 mktemp 里 ⇒ 可进 CI 快门 ★
# Run: bash tests/mcp_metrics_scrape_wiring_test.sh
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); printf 'OK   %s\n' "$*"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }

# ★ 已知清单（★ 双向棘轮：改这里就等于改契约 ★）
KNOWN_BINS="${WIRING_GATE_KNOWN_BINS:-cmd/gateway cmd/mcp-gateway}"
MIN_BINS="${WIRING_GATE_MIN_BINS:-2}"

# ── 抽取器 ──────────────────────────────────────────────────────────
# ★ 「服务 mcp 指标」的 cmd 二进制：★ 目录里有非测试 .go 文件 import 了
#   github.com/kaixuan/llm-gateway-go/mcp（★ 路径末尾带引号 ⇒ 不误伤同前缀包 ★）
imp_mcp_bins() {
  local root="$1" d
  for d in "$root"/cmd/*/; do
    [ -d "$d" ] || continue
    if grep -rl --include='*.go' --exclude='*_test.go' \
         'llm-gateway-go/mcp"' "$d" 2>/dev/null | grep -q .; then
      printf '%s\n' "${d#"$root"/}" | sed 's#/$##'
    fi
  done | sort
}

# ★ ★ ★ 写法纪律（★ 本门**第一版**就栽在这里、★ 而且是【★ 被自己的差分探针抓到的 ★】★★ ★
#   ★ BSD grep（macOS 自带，2.6）与 GNU grep 在【★ ERE 的转义括号 ★】上不一致：
#     ★ `Handle(Func)?\("...`（★ 量词后跟转义括号 ★）在 BSD 上【★ 整族失配 ★】
#     ★ ⇒ ★ 而【★ 那正好是本门要用的形态 ★】⇒ ★ 抽取面【★ 看着有、★ 其实一条都抽不到 ★】
#   ★ ⇒ ★ 所以：**字面括号一律用 [(] 写**、★ 而【★ 路径判定一律改成
#     ★ 「★ 先把路由字面量抽出来、★ 再做精确比对 ★」★★ ★
#     ★ （★ 而不是把整个路径塞进一个正则里 ★）★★ ★
#   ★ ⇒ ★ 这条纪律本身就是【★ 「★ 阴性断言【★ 看着成立、★ 而其实漏了 ★】★★ ★】的又一次复现 ★★ ★
#
# ★ /metrics 抓取面：★ 路径**恰好**是 "/metrics"（★ 允许 "GET /metrics" 方法前缀 ★）
#   ⇒ ★ 而 "GET /metrics/storage"（★ JSON 快照、★ 不是 Prometheus 注册表 ★）不计数 ★
#   ⇒ ★ 注释行剔掉（★ 否则注释里写一句 Handle("/metrics") 就能充数 ★）★
metrics_sites() {
  grep -rn --include='*.go' --exclude='*_test.go' \
    -E 'Handle(Func)?[(]"[^"]*"' "$1/$2" 2>/dev/null \
    | grep -vE ':[0-9]+:[[:space:]]*//' \
    | grep -oE 'Handle(Func)?[(]"[^"]*"' \
    | sed -E 's/^Handle(Func)?[(]//' \
    | sed -E 's/^"[A-Z]+ /"/' \
    | grep -cx '"/metrics"'
}

# ★ ScrapeHandler 包装调用数（★ 同样剔注释行 ★）
scrape_calls() {
  grep -rn --include='*.go' --exclude='*_test.go' \
    -E '\.ScrapeHandler[(]' "$1/$2" 2>/dev/null \
    | grep -vE ':[0-9]+:[[:space:]]*//' \
    | wc -l | tr -d ' '
}

# ★ ★ 实现体级抽取器（★ 探针 B 逼出来的 ★）★★ ★
#   ★ 起因：★ 判别力探针 B（★ 摘掉 ScrapeHandler **内部**的刷新调用 ★）实测：
#     ★ 新门【★ 仍然是绿的 ★】★★ ★ ⇒ ★ 而它【★ 本该抓得住这件事 ★】★★ ★
#     ★ ⇒ ★ 根因：★ 门数的是【★ 「包装」这个调用点 ★】、
#       ★ 而【★ 包装内部有没有真的刷新 ★】它一个字都没问 ★★★ ★
#     ★ ⇒ ★ **「包装接了」与「包装里真的刷新了」是两件事 ★**★★ ★
#   ★ ⇒ ★ 所以下面两条把【★ 调用链一环一环钉死 ★】★★ ★
#     ｜RefreshUptimeMetric 的实现体里必须真的有 `uptimeSeconds.Set(` ★
#     ｜ScrapeHandler 的实现体里必须真的有 `RefreshUptimeMetric(` ★
#
# ★ body_has <root> <方法名> <要找的调用形态>
#   ⇒ ★ 取该方法在【★ 非测试 .go 文件 ★】里的实现体（★ 到列 0 的 `}` 为止 ★）
#   ⇒ ★ 剔掉注释行（★ 否则注释里写一句就充数 ★）★
body_has() {
  local root="$1" method="$2" call="$3" f
  f="$(grep -rl --include='*.go' --exclude='*_test.go' \
        -E "^func [(]g [*]Gateway[)] ${method}[(]" "$root/mcp" 2>/dev/null | head -1)"
  [ -n "$f" ] || return 1
  # ★ ★ one true awk（★ macOS 自带 ★）会【★ 丢弃字符串里未识别转义的反斜杠 ★】
  #   ⇒ ★ 而 "\(" 在 awk 的字符串里会变成 "(" ⇒ ★ 正则从「字面括号」变成「分组」★★ ★
  #   ⇒ ★ ⇒ ★ 所以这里【★ 一律用 bracket 写法 ★】★（★ 与上一条 BSD grep 的坑同源 ★）★★ ★
  awk -v m="$method" '
    $0 ~ "^func [(]g [*]Gateway[)] " m "[(]" { inblk=1; next }
    inblk && /^}/ { inblk=0; next }
    inblk { print }
  ' "$f" \
    | grep -vE '[[:space:]]*//' \
    | grep -qE "$call"
}

# ── 判定（★ 探针与正式检查走**同一条**判定 ★）────────────────────────
# judge_root <root> <known_set> <min_bins>
#   ⇒ 打印 VIOLATION 行；返回 0 = 有违规
judge_root() {
  local root="$1" known="$2" min="$3"
  local viol=0 actual known_norm n b sites calls c

  actual="$(imp_mcp_bins "$root")"
  known_norm="$(printf '%s\n' "$known" | tr ' ' '\n' | grep -v '^$' | sort)"
  n="$(printf '%s\n' "$actual" | grep -c . || true)"

  # ① 清点面下限（★ 清点空了 ⇒ 判定会恒绿 ⇒ 必须报出来 ★）
  if [ "$n" -lt "$min" ]; then
    printf 'VIOLATION: 只清点到 %s 个「服务 mcp 指标」的二进制（< %s）⇒ 清点面失效，这道门会恒绿\n' "$n" "$min"
    viol=1
  fi

  # ① 双向棘轮：实际集合必须与已知清单**逐条相等**
  if [ "$actual" != "$known_norm" ]; then
    printf 'VIOLATION: 「服务 mcp 指标」的二进制集合与已知清单不相等\n'
    printf '         实际: %s\n' "$(printf '%s' "$actual" | tr '\n' ' ')"
    printf '         已知: %s\n' "$(printf '%s' "$known_norm" | tr '\n' ' ')"
    viol=1
  fi

  # ② 每个二进制的抓取面与包装
  while IFS= read -r b; do
    [ -n "$b" ] || continue
    sites="$(metrics_sites "$root" "$b")"
    calls="$(scrape_calls "$root" "$b")"
    if [ "$sites" -lt 1 ]; then
      printf 'VIOLATION: %s import 了 mcp 包、却没有 /metrics 抓取面 ⇒ mcp 指标无处暴露\n' "$b"
      viol=1
    fi
    if [ "$calls" -lt "$sites" ]; then
      printf 'VIOLATION: %s 有 %s 个 /metrics 抓取面、却只有 %s 次 ScrapeHandler 包装 ⇒ 有面没接刷新\n' "$b" "$sites" "$calls"
      viol=1
    fi
  done <<EOF
$actual
EOF

  # ②' ★ 实现体级：★ 调用链一环一环钉死 ★
  #   ⇒ ★ 而【★ 只在树里【★ 真的有 mcp 包 ★】时才判 ★（★ 合成探针树里没有它 ★）
  if [ -d "$root/mcp" ]; then
    if ! body_has "$root" 'RefreshUptimeMetric' 'uptimeSeconds[.]Set[(]'; then
      printf 'VIOLATION: RefreshUptimeMetric 的实现体里没有 uptimeSeconds.Set( ⇒ 刷新是空壳、指标仍然 ABSENT\n'
      viol=1
    fi
    if ! body_has "$root" 'ScrapeHandler' 'RefreshUptimeMetric[(]'; then
      printf 'VIOLATION: ScrapeHandler 的实现体里没有 RefreshUptimeMetric( ⇒ 包装是空壳、指标仍然 ABSENT\n'
      viol=1
    fi
  fi

  # ③ 反向：ScrapeHandler 只许出现在 ① 清点到的集合里
  for d in "$root"/cmd/*/; do
    [ -d "$d" ] || continue
    b="${d#"$root"/}"; b="${b%/}"
    c="$(scrape_calls "$root" "$b")"
    [ "$c" -gt 0 ] || continue
    if ! printf '%s\n' "$actual" | grep -qx "$b"; then
      printf 'VIOLATION: %s 里有 %s 次 ScrapeHandler 包装、但它不在「服务 mcp 指标」的集合里 ⇒ 冒充接线\n' "$b" "$c"
      viol=1
    fi
  done

  return $viol
}

# ── ④ 差分探针（★ 门自己有牙 ★）────────────────────────────────────
PROBE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/mcp-wiring-probe.XXXXXX")"
trap 'rm -rf "$PROBE_DIR"' EXIT

make_probe() {
  local kind="$1"
  local dir="$PROBE_DIR/$kind/cmd/probe"
  mkdir -p "$dir"
  if [ "$kind" = "fixed" ]; then
    cat > "$dir/main.go" <<'GO'
package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	mux := http.NewServeMux()
	gw, _ := mcp.NewGateway(mcp.Config{})
	mux.Handle("/metrics", gw.ScrapeHandler(promhttp.Handler()))
	_ = mux
}
GO
  else
    cat > "$dir/main.go" <<'GO'
package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	mux := http.NewServeMux()
	gw, _ := mcp.NewGateway(mcp.Config{})
	_ = gw
	mux.Handle("/metrics", promhttp.Handler())
	_ = mux
}
GO
  fi
}

make_probe fixed
make_probe broken

if judge_root "$PROBE_DIR/fixed" "cmd/probe" 1 > "$PROBE_DIR/fixed.out" 2>&1; then
  ok "探针·已接线臂：判定为绿（import mcp ＋ /metrics 已包装 ⇒ 不该报违规）"
else
  bad "探针·已接线臂被误判为红 ⇒ 判定的红绿方向反了 ⇒ 门坏"
  sed 's/^/      /' "$PROBE_DIR/fixed.out" >&2
fi

if judge_root "$PROBE_DIR/broken" "cmd/probe" 1 > "$PROBE_DIR/broken.out" 2>&1; then
  bad "探针·未接线臂被判为绿 ⇒ 这道门【★ 没有牙 ★】（★ 而它正是要抓「有面没接刷新」的那种形态 ★）"
else
  ok "探针·未接线臂：判定为红（import mcp ＋ /metrics 未包装 ⇒ 抓到了）"
fi

# ★ 而【★ 两边都红 ★】同样说明门坏（★ 方向对了但「已接线」不该红 ★）
if grep -q 'VIOLATION' "$PROBE_DIR/fixed.out"; then
  bad "探针·已接线臂里也出现了 VIOLATION ⇒ 判定分不出「已接线」与「未接线」"
else
  ok "探针两臂的判定结果**不同** ⇒ 判别力成立"
fi

# ── 正式检查（★ 走同一条判定 ★）────────────────────────────────────
# ★ judge_root 的返回码语义：★ 0 = **无违规**（绿）｜非 0 = 有违规（红）★★ ★
JUDGE_OUT="$(judge_root "$REPO_ROOT" "$KNOWN_BINS" "$MIN_BINS" 2>&1)"
JUDGE_RC=$?
if [ "$JUDGE_RC" -ne 0 ]; then
  bad "仓内接线契约被违反："
  printf '%s\n' "$JUDGE_OUT" | sed 's/^/      /' >&2
else
  ok "仓内「服务 mcp 指标」的二进制与其 /metrics 抓取面接线契约成立"
fi

# ── ⑤ 自指纪律：门自身不得成为处置面的一条命中 ─────────────────────
if [ -d "$REPO_ROOT/cmd" ] && [ -f "$REPO_ROOT/tests/mcp_metrics_scrape_wiring_test.sh" ]; then
  if [ -e "$REPO_ROOT/cmd/mcp_metrics_scrape_wiring_test.sh" ]; then
    bad "门自身落在了 cmd/ 下 ⇒ 它会被自己的清点面当成一个二进制"
  else
    ok "门自身不在 cmd/ 下 ⇒ 不会被自己的清点面误当成一个二进制"
  fi
fi

echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
